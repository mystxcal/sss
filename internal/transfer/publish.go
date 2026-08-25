package transfer

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/store/files"
	"github.com/sss/sss/internal/store/sqlite"
)

// publish runs the commit protocol for an already-materialized staging tree:
//
//  1. write and fsync the final manifest;
//  2. make the staged payload read-only;
//  3. atomically rename staging -> live;
//  4. allocate the code and mark COMMITTED in one database transaction.
//
// A crash between steps 3 and 4 leaves a valid live directory whose database
// record is pre-commit; startup reconciliation finishes it idempotently. No
// code is ever visible before the database says COMMITTED.
func (s *Service) publish(ctx context.Context, transferID string, m protocol.Manifest, ttlMinutes int, note string, materializedBytes int64) (protocol.CommitResponse, *protocol.Error) {
	if err := s.store.SetState(ctx, transferID, sqlite.StateVerifying); err != nil {
		return protocol.CommitResponse{}, protocol.Errorf(protocol.ErrInternal, "could not record verification state")
	}
	if err := s.store.SetNoteAndTTL(ctx, transferID, note, ttlMinutes); err != nil {
		return protocol.CommitResponse{}, protocol.Errorf(protocol.ErrInternal, "could not record transfer metadata")
	}
	stagingRoot := s.layout.StagingPath(transferID)

	digest, perr := writeAndDigestManifest(stagingRoot, m)
	if perr != nil {
		return protocol.CommitResponse{}, perr
	}
	if err := platform.SealPayload(stagingRoot); err != nil {
		return protocol.CommitResponse{}, protocol.Errorf(protocol.ErrInternal, "could not seal payload: %v", err)
	}
	if _, err := s.layout.Publish(transferID); err != nil {
		return protocol.CommitResponse{}, protocol.Errorf(protocol.ErrInternal, "could not publish payload: %v", err)
	}
	rec, err := s.store.Publish(ctx, transferID, s.clk.Now(), ttlMinutes, digest, materializedBytes)
	if err != nil {
		if perr := protocol.AsError(err); perr.Code != protocol.ErrInternal {
			return protocol.CommitResponse{}, perr
		}
		return protocol.CommitResponse{}, protocol.Errorf(protocol.ErrInternal, "could not allocate code: %v", err)
	}
	s.manifests.put(transferID, m)
	s.log.Info("transfer committed",
		"transfer_id", transferID,
		"code", protocol.FormatCode(rec.Code),
		"files", m.FileCount(),
		"bytes", materializedBytes,
		"ttl_minutes", ttlMinutes,
		"expires_at", rec.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"))
	return protocol.CommitResponse{
		Code:        protocol.FormatCode(rec.Code),
		CommittedAt: rec.CommittedAt,
		ExpiresAt:   rec.ExpiresAt,
	}, nil
}

// writeAndDigestManifest stores the manifest durably and returns its digest.
func writeAndDigestManifest(transferDir string, m protocol.Manifest) (string, *protocol.Error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", protocol.Errorf(protocol.ErrInternal, "could not encode manifest")
	}
	if err := files.WriteManifest(transferDir, m); err != nil {
		return "", protocol.Errorf(protocol.ErrInternal, "could not write manifest: %v", err)
	}
	return integrity.HashBytes(data), nil
}

// CompleteInterruptedPublish finishes a commit whose filesystem publication
// succeeded but whose database transaction did not.
func (s *Service) CompleteInterruptedPublish(ctx context.Context, t sqlite.Transfer) (sqlite.Transfer, error) {
	return s.completeInterruptedPublish(ctx, t)
}

// LivePayloadValid reports whether a live directory holds a complete payload.
func (s *Service) LivePayloadValid(transferID string) bool { return s.livePayloadValid(transferID) }

// completeInterruptedPublish finishes a commit whose filesystem publication
// succeeded but whose database transaction did not. It is idempotent.
func (s *Service) completeInterruptedPublish(ctx context.Context, t sqlite.Transfer) (sqlite.Transfer, error) {
	liveDir := s.layout.LivePath(t.ID)
	m, err := files.ReadManifest(liveDir)
	if err != nil {
		return t, err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return t, err
	}
	// A published directory must be sealed even if the crash happened first.
	if err := platform.SealPayload(liveDir); err != nil {
		return t, err
	}
	ttl := t.RequestedTTLMinutes
	if ttl == 0 {
		ttl = s.cfg.Limits.DefaultTTLMinutes
	}
	rec, err := s.store.Publish(ctx, t.ID, s.clk.Now(), ttl, integrity.HashBytes(data), m.TotalBytes())
	if err != nil {
		return t, err
	}
	s.log.Info("completed interrupted commit during reconciliation",
		"transfer_id", t.ID, "code", protocol.FormatCode(rec.Code))
	return rec, nil
}

// livePayloadValid reports whether a live directory holds a usable payload.
func (s *Service) livePayloadValid(transferID string) bool {
	liveDir := s.layout.LivePath(transferID)
	if !files.ManifestExists(liveDir) {
		return false
	}
	m, err := files.ReadManifest(liveDir)
	if err != nil {
		return false
	}
	payload := filepath.Join(liveDir, files.PayloadDir)
	for _, e := range m.Entries {
		p, err := platform.SafeJoin(payload, e.Path)
		if err != nil {
			return false
		}
		info, err := statFile(p)
		if err != nil {
			return false
		}
		if e.Type == protocol.EntryFile && info.Size() != e.Size {
			return false
		}
	}
	return true
}
