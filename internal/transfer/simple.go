package transfer

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sss/sss/internal/ids"
	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/store/files"
	"github.com/sss/sss/internal/store/sqlite"
)

func newToken() string   { return ids.Token() }
func newClaimID() string { return ids.Claim() }

// admissionCheckInterval is how many bytes a streaming upload writes between
// disk headroom checks.
const admissionCheckInterval = 32 << 20

// SimpleSession stages a non-resumable upload from the simple HTTP endpoints.
// The session holds no payload bytes in memory: parts are streamed to disk and
// hashed on the way through.
type SimpleSession struct {
	svc        *Service
	id         string
	root       string
	note       string
	ttlMinutes int
	entries    []protocol.Entry
	segments   []protocol.Segment
	roots      []string
	seenNames  map[string]bool
	bytes      int64
	sinceCheck int64
	committed  bool
}

// BeginSimple opens a staging session for a simple upload.
func (s *Service) BeginSimple(ctx context.Context) (*SimpleSession, *protocol.Error) {
	if perr := s.adm.CheckStreaming(ctx); perr != nil {
		return nil, perr
	}
	id := ids.Transfer()
	root, err := s.layout.PrepareStaging(id)
	if err != nil {
		return nil, protocol.Errorf(protocol.ErrInternal, "could not create staging area")
	}
	now := s.clk.Now()
	rec := sqlite.Transfer{
		ID:                  id,
		State:               sqlite.StateCreated,
		CreatedAt:           now,
		RequestedTTLMinutes: s.cfg.Limits.DefaultTTLMinutes,
		RootPath:            root,
	}
	if err := s.store.CreateTransfer(ctx, rec); err != nil {
		_ = s.layout.RemoveStaging(id)
		return nil, protocol.Errorf(protocol.ErrInternal, "could not record transfer")
	}
	return &SimpleSession{
		svc:        s,
		id:         id,
		root:       root,
		ttlMinutes: s.cfg.Limits.DefaultTTLMinutes,
		seenNames:  map[string]bool{},
	}, nil
}

// ID returns the internal transfer identifier.
func (ss *SimpleSession) ID() string { return ss.id }

// SetNote records the optional handoff note.
func (ss *SimpleSession) SetNote(note string) *protocol.Error {
	if perr := ss.svc.CheckNote(note); perr != nil {
		return perr
	}
	ss.note = note
	return nil
}

// SetTTL records the requested expiry in minutes.
func (ss *SimpleSession) SetTTL(minutes int) *protocol.Error {
	ttl, perr := ss.svc.ResolveTTL(minutes)
	if perr != nil {
		return perr
	}
	ss.ttlMinutes = ttl
	return nil
}

// AddFile streams one uploaded part into the staged payload.
//
// The supplied name is treated as hostile: only its final component is used and
// it must be a portable path component.
func (ss *SimpleSession) AddFile(ctx context.Context, name string, r io.Reader) *protocol.Error {
	clean := sanitizeUploadName(name)
	if clean == "" {
		return protocol.Errorf(protocol.ErrInvalidPath, "uploaded part has no usable filename")
	}
	if perr := protocol.ValidateComponent(clean); perr != nil {
		return perr
	}
	if ss.seenNames[strings.ToLower(clean)] {
		return protocol.Errorf(protocol.ErrDuplicatePath, "two uploaded files normalize to %q", clean)
	}
	if len(ss.entries) >= ss.svc.cfg.Limits.MaxFiles {
		return protocol.Errorf(protocol.ErrTooManyFiles, "more than %d files", ss.svc.cfg.Limits.MaxFiles)
	}
	target, err := platform.SafeJoin(filepath.Join(ss.root, files.PayloadDir), clean)
	if err != nil {
		return protocol.Errorf(protocol.ErrInvalidPath, "unsafe filename %q", clean)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if err != nil {
		return protocol.Errorf(protocol.ErrInternal, "could not create staged file")
	}
	hasher := integrity.New()
	buf := make([]byte, 1<<20)
	var written int64
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return protocol.Errorf(protocol.ErrInsufficientStorage, "could not write staged bytes: %v", werr)
			}
			hasher.Write(buf[:n])
			written += int64(n)
			ss.sinceCheck += int64(n)
			max := ss.svc.cfg.Limits.MaxTransferBytes
			if max > 0 && ss.bytes+written > max {
				f.Close()
				return protocol.Errorf(protocol.ErrPayloadTooLarge, "transfer exceeds the configured maximum of %d bytes", max)
			}
			if ss.sinceCheck >= admissionCheckInterval {
				ss.sinceCheck = 0
				if perr := ss.svc.adm.CheckStreaming(ctx); perr != nil {
					f.Close()
					return perr
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			return protocol.Errorf(protocol.ErrNetwork, "upload stream failed: %v", readErr)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return protocol.Errorf(protocol.ErrInternal, "could not flush staged file")
	}
	if err := f.Close(); err != nil {
		return protocol.Errorf(protocol.ErrInternal, "could not close staged file")
	}

	digest := integrity.Sum(hasher)
	segID := ids.Segment()
	now := ss.svc.clk.Now()
	ss.entries = append(ss.entries, protocol.Entry{
		Path:        clean,
		Type:        protocol.EntryFile,
		Size:        written,
		MTimeUnixNS: now.UnixNano(),
		Mode:        0o644,
		Digest:      digest,
		SegmentID:   segID,
	})
	ss.segments = append(ss.segments, protocol.Segment{
		ID:          segID,
		Kind:        protocol.SegmentRaw,
		WireSize:    written,
		Digest:      digest,
		StoragePath: files.PayloadDir + "/" + clean,
	})
	ss.roots = append(ss.roots, clean)
	ss.seenNames[strings.ToLower(clean)] = true
	ss.bytes += written
	return nil
}

// FileCount reports how many parts have been staged.
func (ss *SimpleSession) FileCount() int { return len(ss.entries) }

// Commit verifies, publishes, and allocates the public code.
func (ss *SimpleSession) Commit(ctx context.Context) (protocol.CommitResponse, *protocol.Error) {
	if len(ss.entries) == 0 {
		return protocol.CommitResponse{}, protocol.Errorf(protocol.ErrNoFiles, "no file part was supplied")
	}
	m := protocol.Manifest{
		SchemaVersion:   1,
		TransferID:      ss.id,
		CreatedAt:       ss.svc.clk.Now().UTC(),
		Note:            ss.note,
		Roots:           ss.roots,
		DigestAlgorithm: protocol.DigestAlgorithm,
		Segments:        ss.segments,
		Entries:         ss.entries,
	}
	if perr := m.Validate(); perr != nil {
		return protocol.CommitResponse{}, perr
	}
	resp, perr := ss.svc.publish(ctx, ss.id, m, ss.ttlMinutes, ss.note, m.TotalBytes())
	if perr != nil {
		return protocol.CommitResponse{}, perr
	}
	ss.committed = true
	return resp, nil
}

// Abort discards an incomplete session. Staging data is never addressable by a
// code, so discarding it can never remove published content.
func (ss *SimpleSession) Abort(ctx context.Context) {
	if ss.committed {
		return
	}
	if err := ss.svc.layout.RemoveStaging(ss.id); err != nil {
		ss.svc.log.Warn("could not remove staging directory", "transfer_id", ss.id, "error", err.Error())
	}
	if err := ss.svc.store.Purge(ctx, ss.id); err != nil {
		ss.svc.log.Warn("could not purge abandoned transfer", "transfer_id", ss.id, "error", err.Error())
	}
}

// sanitizeUploadName reduces a client-supplied filename to its final component.
func sanitizeUploadName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Base(path.Clean(name))
	name = strings.TrimSpace(name)
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	return name
}

// SimpleRawTTL parses the X-SSS-TTL header value.
func (s *Service) SimpleRawTTL(raw string) (int, *protocol.Error) {
	if strings.TrimSpace(raw) == "" {
		return s.cfg.Limits.DefaultTTLMinutes, nil
	}
	minutes, perr := protocol.ParseTTL(raw)
	if perr != nil {
		return 0, perr
	}
	return minutes, nil
}
