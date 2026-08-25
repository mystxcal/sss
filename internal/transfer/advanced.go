package transfer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/sss/sss/internal/admission"
	"github.com/sss/sss/internal/ids"
	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/materialize"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/store/files"
	"github.com/sss/sss/internal/store/sqlite"
)

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

// CreateTransfer opens an uncommitted resumable transfer and its upload
// resources. Nothing here is addressable by a code.
func (s *Service) CreateTransfer(ctx context.Context, req protocol.CreateTransferRequest) (protocol.CreateTransferResponse, *protocol.Error) {
	ttl, perr := s.ResolveTTL(req.TTLMinutes)
	if perr != nil {
		return protocol.CreateTransferResponse{}, perr
	}
	if perr := s.CheckNote(req.Note); perr != nil {
		return protocol.CreateTransferResponse{}, perr
	}
	if len(req.Segments) == 0 {
		return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInvalidRequest, "at least one segment is required")
	}
	if req.ExpectedFileCount > s.cfg.Limits.MaxFiles {
		return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrTooManyFiles,
			"more than %d files", s.cfg.Limits.MaxFiles)
	}
	seen := map[string]bool{}
	var declared int64
	for _, seg := range req.Segments {
		if seg.ID == "" || len(seg.ID) > 128 {
			return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInvalidRequest, "invalid segment id")
		}
		if perr := protocol.ValidateComponent(seg.ID); perr != nil {
			// Segment IDs become filenames in staging, so they must be safe.
			return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInvalidRequest, "segment id %q is not usable", seg.ID)
		}
		if seen[seg.ID] {
			return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInvalidRequest, "duplicate segment id %q", seg.ID)
		}
		seen[seg.ID] = true
		if seg.Kind != protocol.SegmentRaw && seg.Kind != protocol.SegmentTarZstd {
			return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInvalidRequest, "unsupported segment kind %q", seg.Kind)
		}
		if seg.DigestAlgorithm != "" && seg.DigestAlgorithm != protocol.DigestAlgorithm {
			return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInvalidRequest, "unsupported digest algorithm %q", seg.DigestAlgorithm)
		}
		if seg.ExpectedLength < 0 {
			return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInvalidRequest, "negative segment length")
		}
		if seg.ExpectedDigest != nil && !hexDigest.MatchString(*seg.ExpectedDigest) {
			return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInvalidRequest, "malformed expected digest")
		}
		declared += seg.ExpectedLength
	}
	if perr := s.adm.Admit(ctx, declared); perr != nil {
		return protocol.CreateTransferResponse{}, perr
	}

	id := ids.Transfer()
	root, err := s.layout.PrepareStaging(id)
	if err != nil {
		return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInternal, "could not create staging area")
	}
	now := s.clk.Now()
	rec := sqlite.Transfer{
		ID:                  id,
		State:               sqlite.StateCreated,
		CreatedAt:           now,
		RequestedTTLMinutes: ttl,
		Note:                req.Note,
		ReservedBytes:       admission.Reserve(declared),
		RootPath:            root,
	}
	if err := s.store.CreateTransfer(ctx, rec); err != nil {
		_ = s.layout.RemoveStaging(id)
		return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInternal, "could not record transfer")
	}

	uploads := make([]protocol.UploadResource, 0, len(req.Segments))
	for _, seg := range req.Segments {
		uploadID := ids.Upload()
		digest := ""
		if seg.ExpectedDigest != nil {
			digest = *seg.ExpectedDigest
		}
		err := s.store.InsertSegment(ctx, sqlite.Segment{
			ID:                  seg.ID,
			TransferID:          id,
			UploadID:            uploadID,
			Kind:                seg.Kind,
			ExpectedLength:      seg.ExpectedLength,
			DigestAlgorithm:     protocol.DigestAlgorithm,
			ExpectedDigest:      digest,
			State:               sqlite.SegmentPending,
			RelativeStoragePath: files.SegmentsDir + "/" + seg.ID,
		})
		if err != nil {
			_ = s.layout.RemoveStaging(id)
			_ = s.store.Purge(ctx, id)
			return protocol.CreateTransferResponse{}, protocol.Errorf(protocol.ErrInternal, "could not record segment")
		}
		uploads = append(uploads, protocol.UploadResource{
			SegmentID:  seg.ID,
			UploadID:   uploadID,
			UploadPath: "/v1/uploads/" + uploadID,
		})
	}
	s.log.Info("transfer created",
		"transfer_id", id, "segments", len(req.Segments), "declared_bytes", declared, "ttl_minutes", ttl)
	return protocol.CreateTransferResponse{TransferID: id, Uploads: uploads}, nil
}

// UploadState reports the accepted offset and declared length of a segment.
func (s *Service) UploadState(ctx context.Context, uploadID string) (offset, length int64, perr *protocol.Error) {
	seg, err := s.store.GetSegmentByUploadID(ctx, uploadID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return 0, 0, protocol.Errorf(protocol.ErrTransferNotFound, "unknown upload resource")
	}
	if err != nil {
		return 0, 0, protocol.Errorf(protocol.ErrInternal, "upload lookup failed")
	}
	return seg.ReceivedLength, seg.ExpectedLength, nil
}

// UploadPatch appends bytes to a segment at an exact offset.
func (s *Service) UploadPatch(ctx context.Context, uploadID string, offset int64, body io.Reader) (int64, *protocol.Error) {
	release, perr := s.acquireUpload(ctx)
	if perr != nil {
		return 0, perr
	}
	defer release()

	seg, err := s.store.GetSegmentByUploadID(ctx, uploadID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return 0, protocol.Errorf(protocol.ErrTransferNotFound, "unknown upload resource")
	}
	if err != nil {
		return 0, protocol.Errorf(protocol.ErrInternal, "upload lookup failed")
	}
	t, err := s.store.GetTransfer(ctx, seg.TransferID)
	if err != nil {
		return 0, protocol.Errorf(protocol.ErrTransferNotFound, "unknown transfer")
	}
	switch t.State {
	case sqlite.StateCreated, sqlite.StateUploading:
	default:
		return 0, protocol.Errorf(protocol.ErrStateConflict, "transfer no longer accepts uploads")
	}
	if offset != seg.ReceivedLength {
		return seg.ReceivedLength, protocol.Errorf(protocol.ErrOffsetMismatch,
			"server holds offset %d", seg.ReceivedLength)
	}
	if offset > seg.ExpectedLength {
		return seg.ReceivedLength, protocol.Errorf(protocol.ErrInvalidRequest, "offset beyond declared length")
	}
	if perr := s.adm.CheckStreaming(ctx); perr != nil {
		return seg.ReceivedLength, perr
	}

	path := filepath.Join(s.layout.StagingPath(t.ID), files.SegmentsDir, seg.ID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return seg.ReceivedLength, protocol.Errorf(protocol.ErrInternal, "could not open segment")
	}
	defer f.Close()
	// Drop anything past the accepted offset: a previous attempt may have
	// written bytes that were never acknowledged.
	if err := f.Truncate(offset); err != nil {
		return seg.ReceivedLength, protocol.Errorf(protocol.ErrInternal, "could not align segment")
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return seg.ReceivedLength, protocol.Errorf(protocol.ErrInternal, "could not seek segment")
	}

	if t.State == sqlite.StateCreated {
		_ = s.store.SetState(ctx, t.ID, sqlite.StateUploading)
	}

	remaining := seg.ExpectedLength - offset
	buf := make([]byte, 1<<20)
	var written int64
	var sinceCheck int64
	readErr := error(nil)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if written+int64(n) > remaining {
				// Persist what is valid, then reject the overrun.
				valid := remaining - written
				if valid > 0 {
					if _, werr := f.Write(buf[:valid]); werr == nil {
						written += valid
					}
				}
				s.finishPatch(ctx, f, seg, offset+written)
				return offset + written, protocol.Errorf(protocol.ErrInvalidRequest,
					"body exceeds the declared segment length")
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				s.finishPatch(ctx, f, seg, offset+written)
				return offset + written, protocol.Errorf(protocol.ErrInsufficientStorage, "could not write segment bytes")
			}
			written += int64(n)
			sinceCheck += int64(n)
			if sinceCheck >= admissionCheckInterval {
				sinceCheck = 0
				if perr := s.adm.CheckStreaming(ctx); perr != nil {
					s.finishPatch(ctx, f, seg, offset+written)
					return offset + written, perr
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			readErr = rerr
			break
		}
	}
	newOffset := offset + written
	s.finishPatch(ctx, f, seg, newOffset)
	if readErr != nil {
		// The accepted offset is durable, so the client can resume from here.
		return newOffset, protocol.Errorf(protocol.ErrNetwork, "upload stream interrupted")
	}
	return newOffset, nil
}

// finishPatch makes the accepted offset durable before it is acknowledged.
func (s *Service) finishPatch(ctx context.Context, f *os.File, seg sqlite.Segment, newOffset int64) {
	if err := f.Sync(); err != nil {
		s.log.Warn("could not sync segment", "segment_id", seg.ID, "error", err.Error())
	}
	state := sqlite.SegmentPending
	if newOffset == seg.ExpectedLength {
		state = sqlite.SegmentReceived
	}
	if err := s.store.SetSegmentProgress(ctx, seg.TransferID, seg.ID, newOffset, state); err != nil {
		s.log.Warn("could not record segment progress", "segment_id", seg.ID, "error", err.Error())
	}
	if delta := newOffset - seg.ReceivedLength; delta > 0 {
		_ = s.store.AddWireBytes(ctx, seg.TransferID, delta)
	}
}

// Commit verifies a declared manifest against the staged segments, materializes
// the payload, publishes it, and allocates the code. Repeating a successful
// commit returns the original result.
func (s *Service) Commit(ctx context.Context, transferID string, m protocol.Manifest) (protocol.CommitResponse, bool, *protocol.Error) {
	t, err := s.store.GetTransfer(ctx, transferID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrTransferNotFound, "unknown transfer")
	}
	if err != nil {
		return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrInternal, "transfer lookup failed")
	}
	if t.Committed() {
		return protocol.CommitResponse{
			Code:        protocol.FormatCode(t.Code),
			CommittedAt: t.CommittedAt,
			ExpiresAt:   t.ExpiresAt,
		}, false, nil
	}
	switch t.State {
	case sqlite.StateCreated, sqlite.StateUploading, sqlite.StateVerifying:
	default:
		return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrStateConflict,
			"transfer cannot be committed from state %s", t.State)
	}
	if m.TransferID != transferID {
		return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrInvalidRequest,
			"manifest transfer_id does not match the transfer")
	}
	if perr := m.Validate(); perr != nil {
		return protocol.CommitResponse{}, false, perr
	}
	if fc := m.FileCount(); fc > s.cfg.Limits.MaxFiles {
		return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrTooManyFiles,
			"%d files exceeds the limit of %d", fc, s.cfg.Limits.MaxFiles)
	}
	if perr := s.CheckNote(m.Note); perr != nil {
		return protocol.CommitResponse{}, false, perr
	}
	total := m.TotalBytes()
	if max := s.cfg.Limits.MaxTransferBytes; max > 0 && total > max {
		return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrPayloadTooLarge,
			"transfer exceeds the configured maximum of %d bytes", max)
	}

	stored, err := s.store.ListSegments(ctx, transferID)
	if err != nil {
		return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrInternal, "segment lookup failed")
	}
	byID := make(map[string]sqlite.Segment, len(stored))
	for _, seg := range stored {
		byID[seg.ID] = seg
	}
	if len(stored) != len(m.Segments) {
		return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrInvalidRequest,
			"manifest declares %d segments but the transfer has %d", len(m.Segments), len(stored))
	}
	stagingRoot := s.layout.StagingPath(transferID)
	// Verification trees are built in the same pass that verifies the segments,
	// so they cost no extra read of the payload. They live outside SegmentsDir
	// because materialization requires that directory to end up empty.
	outboardsDir := filepath.Join(stagingRoot, files.OutboardsDir)
	if err := os.MkdirAll(outboardsDir, 0o750); err != nil {
		return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrInternal,
			"could not create verification tree directory: %v", err)
	}
	for _, ms := range m.Segments {
		seg, ok := byID[ms.ID]
		if !ok {
			return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrInvalidRequest,
				"manifest references unknown segment %q", ms.ID)
		}
		if seg.Kind != ms.Kind {
			return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrInvalidRequest,
				"segment %q changed kind after creation", ms.ID)
		}
		if seg.ExpectedLength != ms.WireSize {
			return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrInvalidRequest,
				"segment %q length disagrees with its declared length", ms.ID)
		}
		if seg.ReceivedLength != seg.ExpectedLength {
			return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrStateConflict,
				"segment %q has %d of %d bytes", ms.ID, seg.ReceivedLength, seg.ExpectedLength)
		}
		path := filepath.Join(stagingRoot, files.SegmentsDir, ms.ID)
		// The Bao root is the BLAKE3 digest, so building the tree here both
		// verifies the segment and leaves behind the proof a receiver needs to
		// verify any slice of it independently.
		digest, size, err := integrity.OutboardFile(path, filepath.Join(outboardsDir, ms.ID+files.OutboardSuffix))
		if err != nil {
			return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrStateConflict,
				"segment %q is missing", ms.ID)
		}
		if size != ms.WireSize {
			return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrHashMismatch,
				"segment %q holds %d bytes, expected %d", ms.ID, size, ms.WireSize)
		}
		if digest != ms.Digest {
			// The uploaded bytes are the authority: a manifest that disagrees
			// with them is an integrity failure, not a request formatting one.
			return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrHashMismatch,
				"segment %q failed integrity verification", ms.ID)
		}
		if seg.ExpectedDigest != "" && seg.ExpectedDigest != ms.Digest {
			return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrInvalidRequest,
				"segment %q digest changed after creation", ms.ID)
		}
	}

	if err := s.store.SetState(ctx, transferID, sqlite.StateVerifying); err != nil {
		return protocol.CommitResponse{}, false, protocol.Errorf(protocol.ErrInternal, "could not record verification state")
	}
	result, perr := materialize.Run(ctx, stagingRoot, m, s.cfg.Limits.MaxMaterializeWorkers)
	if perr != nil {
		_ = s.store.Fail(ctx, transferID, perr.Code)
		return protocol.CommitResponse{}, false, perr
	}
	ttl := t.RequestedTTLMinutes
	if ttl == 0 {
		ttl = s.cfg.Limits.DefaultTTLMinutes
	}
	resp, perr := s.publish(ctx, transferID, result.Published, ttl, m.Note, result.MaterializedBytes)
	if perr != nil {
		return protocol.CommitResponse{}, false, perr
	}
	return resp, true, nil
}

// SweepInterval is how often the janitor runs by default.
const SweepInterval = 30 * time.Second
