// Package transfer owns the lifecycle invariants. HTTP handlers call into this
// service; they never implement state transitions themselves.
package transfer

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sss/sss/internal/admission"
	"github.com/sss/sss/internal/clock"
	"github.com/sss/sss/internal/config"
	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/store/files"
	"github.com/sss/sss/internal/store/sqlite"
)

// RemoteLeaseMinutes bounds a remote receive session. A claim created before
// expiry may finish inside this lease; renewal requires active progress.
const RemoteLeaseMinutes = 30

// Service coordinates storage, metadata, and lifecycle rules.
type Service struct {
	cfg    config.Server
	store  *sqlite.Store
	layout *files.Layout
	adm    *admission.Controller
	clk    clock.Clock
	log    *slog.Logger

	uploadSem   chan struct{}
	downloadSem chan struct{}

	manifests *manifestCache

	startedAt time.Time
	readyMu   sync.RWMutex
	ready     bool
}

// New wires a transfer service.
func New(cfg config.Server, store *sqlite.Store, layout *files.Layout, clk clock.Clock, log *slog.Logger) *Service {
	s := &Service{
		cfg:         cfg,
		store:       store,
		layout:      layout,
		clk:         clk,
		log:         log,
		uploadSem:   make(chan struct{}, cfg.Limits.MaxConcurrentUploads),
		downloadSem: make(chan struct{}, cfg.Limits.MaxConcurrentDownloads),
		manifests:   newManifestCache(64),
		startedAt:   clk.Now(),
	}
	s.adm = admission.New(cfg.Storage.DataDir, cfg.Limits.DiskHighWatermarkPercent, cfg.Limits.MaxTransferBytes, store)
	return s
}

// Config exposes the server configuration to composition code.
func (s *Service) Config() config.Server { return s.cfg }

// Store exposes the metadata repository to the janitor and reconciler.
func (s *Service) Store() *sqlite.Store { return s.store }

// Layout exposes the filesystem layout to the janitor and reconciler.
func (s *Service) Layout() *files.Layout { return s.layout }

// Admission exposes the disk admission controller.
func (s *Service) Admission() *admission.Controller { return s.adm }

// Now reports the service clock.
func (s *Service) Now() time.Time { return s.clk.Now() }

// SetReady marks readiness once startup reconciliation has finished.
func (s *Service) SetReady(ready bool) {
	s.readyMu.Lock()
	s.ready = ready
	s.readyMu.Unlock()
}

// Ready reports whether reconciliation has completed.
func (s *Service) Ready() bool {
	s.readyMu.RLock()
	defer s.readyMu.RUnlock()
	return s.ready
}

// acquireUpload bounds concurrent upload work.
func (s *Service) acquireUpload(ctx context.Context) (func(), *protocol.Error) {
	select {
	case s.uploadSem <- struct{}{}:
		return func() { <-s.uploadSem }, nil
	case <-ctx.Done():
		return nil, protocol.Errorf(protocol.ErrNetwork, "request cancelled while waiting for an upload slot")
	}
}

// AcquireDownload bounds concurrent download and archive streams.
func (s *Service) AcquireDownload(ctx context.Context) (func(), *protocol.Error) {
	select {
	case s.downloadSem <- struct{}{}:
		return func() { <-s.downloadSem }, nil
	case <-ctx.Done():
		return nil, protocol.Errorf(protocol.ErrNetwork, "request cancelled while waiting for a download slot")
	}
}

// Info returns the capability document.
func (s *Service) Info() protocol.Info {
	usage, reserved, ok := s.adm.Status(context.Background())
	return protocol.Info{
		DefaultTTLMinutes: s.cfg.Limits.DefaultTTLMinutes,
		MaxTTLMinutes:     s.cfg.Limits.MaxTTLMinutes,
		Capabilities: []string{
			"simple-multipart",
			"simple-raw",
			"simple-download-auto-raw-zip-tar",
			"resumable-tus-1.0.0",
			"segment-raw",
			"segment-tar-zstd",
			"digest-blake3",
			"range-download",
			"local-unix-socket",
			"idempotency-key",
		},
		Limits: map[string]any{
			"max_files":                   s.cfg.Limits.MaxFiles,
			"max_note_bytes":              s.cfg.Limits.MaxNoteBytes,
			"max_transfer_bytes":          s.cfg.Limits.MaxTransferBytes,
			"disk_high_watermark_percent": s.cfg.Limits.DiskHighWatermarkPercent,
			"accepting_transfers":         ok,
			"disk_free_bytes":             usage.FreeBytes,
			"reserved_bytes":              reserved,
		},
	}
}

// Status builds the operational snapshot served on the local socket.
func (s *Service) Status(ctx context.Context) (protocol.AdminStatus, error) {
	usage, reserved, ok := s.adm.Status(ctx)
	committed, err := s.store.CountByState(ctx, sqlite.StateCommitted)
	if err != nil {
		return protocol.AdminStatus{}, err
	}
	staging, err := s.store.CountByState(ctx, sqlite.StateCreated, sqlite.StateUploading, sqlite.StateVerifying)
	if err != nil {
		return protocol.AdminStatus{}, err
	}
	claims, err := s.store.TotalActiveClaims(ctx, s.clk.Now())
	if err != nil {
		return protocol.AdminStatus{}, err
	}
	return protocol.AdminStatus{
		Ready:            s.Ready(),
		UptimeSeconds:    int64(s.clk.Now().Sub(s.startedAt).Seconds()),
		Committed:        committed,
		Staging:          staging,
		ActiveClaims:     claims,
		DiskTotalBytes:   usage.TotalBytes,
		DiskFreeBytes:    usage.FreeBytes,
		DiskUsedPercent:  usage.UsedPercent(),
		ReservedBytes:    reserved,
		AdmissionOK:      ok,
		HighWatermarkPct: s.cfg.Limits.DiskHighWatermarkPercent,
		StorageDir:       s.cfg.Storage.DataDir,
		Limits: map[string]any{
			"max_files":          s.cfg.Limits.MaxFiles,
			"max_transfer_bytes": s.cfg.Limits.MaxTransferBytes,
		},
	}, nil
}

// ResolveTTL applies the default when a request omits a TTL and enforces bounds.
func (s *Service) ResolveTTL(minutes int) (int, *protocol.Error) {
	if minutes == 0 {
		return s.cfg.Limits.DefaultTTLMinutes, nil
	}
	if err := protocol.ValidateTTL(minutes); err != nil {
		return 0, err
	}
	return minutes, nil
}

// CheckNote enforces the configured note size.
func (s *Service) CheckNote(note string) *protocol.Error {
	if len(note) > s.cfg.Limits.MaxNoteBytes {
		return protocol.Errorf(protocol.ErrInvalidRequest, "note exceeds the %d byte limit", s.cfg.Limits.MaxNoteBytes)
	}
	return nil
}

// lookupCommitted resolves a public code to a claimable committed transfer.
func (s *Service) lookupCommitted(ctx context.Context, codeInput string) (sqlite.Transfer, *protocol.Error) {
	canonical, ok := protocol.NormalizeCode(codeInput)
	if !ok {
		return sqlite.Transfer{}, protocol.Errorf(protocol.ErrInvalidCode, "code is not eight valid characters")
	}
	t, err := s.store.GetTransferByCode(ctx, canonical)
	if errors.Is(err, sqlite.ErrNotFound) {
		return sqlite.Transfer{}, protocol.Errorf(protocol.ErrTransferNotFound, "no transfer for that code")
	}
	if err != nil {
		return sqlite.Transfer{}, protocol.Errorf(protocol.ErrInternal, "lookup failed")
	}
	switch t.State {
	case sqlite.StateCommitted:
	case sqlite.StateExpired, sqlite.StateDeleting, sqlite.StateDeleted:
		return sqlite.Transfer{}, protocol.Errorf(protocol.ErrTransferExpired, "transfer has expired")
	default:
		return sqlite.Transfer{}, protocol.Errorf(protocol.ErrTransferNotFound, "no transfer for that code")
	}
	if !t.ExpiresAt.IsZero() && !s.clk.Now().Before(t.ExpiresAt) {
		// Expiry is authoritative even before the janitor notices.
		_ = s.store.SetState(ctx, t.ID, sqlite.StateExpired)
		return sqlite.Transfer{}, protocol.Errorf(protocol.ErrTransferExpired, "transfer has expired")
	}
	return t, nil
}

// Manifest loads the published manifest of a committed transfer.
func (s *Service) Manifest(t sqlite.Transfer) (protocol.Manifest, *protocol.Error) {
	if m, ok := s.manifests.get(t.ID); ok {
		return m, nil
	}
	m, err := files.ReadManifest(s.layout.LivePath(t.ID))
	if err != nil {
		return protocol.Manifest{}, protocol.Errorf(protocol.ErrInternal, "manifest unavailable")
	}
	s.manifests.put(t.ID, m)
	return m, nil
}

// PayloadDir returns the immutable payload directory of a committed transfer.
func (s *Service) PayloadDir(t sqlite.Transfer) string {
	return filepath.Join(s.layout.LivePath(t.ID), files.PayloadDir)
}

// Metadata returns the public transfer document for a code.
func (s *Service) Metadata(ctx context.Context, codeInput string) (protocol.TransferMetadata, *protocol.Error) {
	t, perr := s.lookupCommitted(ctx, codeInput)
	if perr != nil {
		return protocol.TransferMetadata{}, perr
	}
	m, perr := s.Manifest(t)
	if perr != nil {
		return protocol.TransferMetadata{}, perr
	}
	entries := make([]protocol.EntrySummary, 0, len(m.Entries))
	for _, e := range m.SortedEntries() {
		entries = append(entries, protocol.EntrySummary{Path: e.Path, Type: e.Type, Size: e.Size})
	}
	return protocol.TransferMetadata{
		Code:        protocol.FormatCode(t.Code),
		Note:        t.Note,
		CommittedAt: t.CommittedAt,
		ExpiresAt:   t.ExpiresAt,
		FileCount:   m.FileCount(),
		TotalBytes:  m.TotalBytes(),
		Entries:     entries,
	}, nil
}

// Download resolves a code to everything the simple download path needs.
type Download struct {
	Transfer   sqlite.Transfer
	Manifest   protocol.Manifest
	PayloadDir string
}

// OpenDownload resolves a code for the simple /r/{code} endpoint.
func (s *Service) OpenDownload(ctx context.Context, codeInput string) (Download, *protocol.Error) {
	t, perr := s.lookupCommitted(ctx, codeInput)
	if perr != nil {
		return Download{}, perr
	}
	m, perr := s.Manifest(t)
	if perr != nil {
		return Download{}, perr
	}
	return Download{Transfer: t, Manifest: m, PayloadDir: s.PayloadDir(t)}, nil
}

// CreateClaim opens a bounded remote receive session.
func (s *Service) CreateClaim(ctx context.Context, codeInput string) (protocol.ClaimResponse, *protocol.Error) {
	t, perr := s.lookupCommitted(ctx, codeInput)
	if perr != nil {
		return protocol.ClaimResponse{}, perr
	}
	m, perr := s.Manifest(t)
	if perr != nil {
		return protocol.ClaimResponse{}, perr
	}
	now := s.clk.Now()
	token := newToken()
	claimID := newClaimID()
	lease := now.Add(RemoteLeaseMinutes * time.Minute)
	err := s.store.CreateClaim(ctx, sqlite.Claim{
		ID:         claimID,
		TransferID: t.ID,
		Kind:       sqlite.ClaimRemote,
		CreatedAt:  now,
		LeaseUntil: lease,
		TokenHash:  hashToken(token),
	})
	if err != nil {
		return protocol.ClaimResponse{}, protocol.Errorf(protocol.ErrInternal, "could not create claim")
	}
	segments := make([]protocol.ClaimSegment, 0, len(m.Segments))
	outboards := s.outboardsPresent(t)
	for _, seg := range m.Segments {
		cs := protocol.ClaimSegment{
			ID:     seg.ID,
			Kind:   seg.Kind,
			Length: seg.WireSize,
			Digest: seg.Digest,
			Path:   "/v1/claims/" + claimID + "/segments/" + seg.ID,
		}
		// Transfers committed before verification trees existed have none, and
		// advertising a path that would 404 would only cost the receiver a
		// round trip before it fell back.
		if outboards {
			cs.OutboardPath = cs.Path + "/outboard"
			cs.OutboardLength = integrity.OutboardSize(seg.WireSize)
		}
		segments = append(segments, cs)
	}
	return protocol.ClaimResponse{
		ClaimID:    claimID,
		Token:      token,
		LeaseUntil: lease.UTC(),
		Manifest:   m,
		Segments:   segments,
	}, nil
}

// outboardsPresent reports whether a committed transfer carries verification
// trees. Transfers published before they existed do not.
func (s *Service) outboardsPresent(t sqlite.Transfer) bool {
	info, err := os.Stat(filepath.Join(s.layout.LivePath(t.ID), files.OutboardsDir))
	return err == nil && info.IsDir()
}

// ClaimOutboardFile opens a segment's verification tree for a claim holder. The
// caller closes the returned file.
func (s *Service) ClaimOutboardFile(ctx context.Context, claimID, token, segmentID string) (*os.File, int64, *protocol.Error) {
	t, seg, perr := s.claimSegment(ctx, claimID, token, segmentID)
	if perr != nil {
		return nil, 0, perr
	}
	path := filepath.Join(s.layout.LivePath(t.ID), files.OutboardsDir, seg.ID+files.OutboardSuffix)
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, protocol.Errorf(protocol.ErrTransferNotFound, "no verification tree for this segment")
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, protocol.Errorf(protocol.ErrInternal, "verification tree is unreadable")
	}
	_ = s.store.RenewClaim(ctx, claimID, s.clk.Now().Add(RemoteLeaseMinutes*time.Minute))
	return f, info.Size(), nil
}

// claimSegment authorizes a claim and resolves one of its manifest segments.
func (s *Service) claimSegment(ctx context.Context, claimID, token, segmentID string) (sqlite.Transfer, protocol.Segment, *protocol.Error) {
	claim, perr := s.authorizeClaim(ctx, claimID, token)
	if perr != nil {
		return sqlite.Transfer{}, protocol.Segment{}, perr
	}
	t, err := s.store.GetTransfer(ctx, claim.TransferID)
	if err != nil {
		return sqlite.Transfer{}, protocol.Segment{}, protocol.Errorf(protocol.ErrTransferNotFound, "transfer is gone")
	}
	if t.State == sqlite.StateDeleting || t.State == sqlite.StateDeleted {
		return sqlite.Transfer{}, protocol.Segment{}, protocol.Errorf(protocol.ErrTransferExpired, "transfer has been deleted")
	}
	m, perr := s.Manifest(t)
	if perr != nil {
		return sqlite.Transfer{}, protocol.Segment{}, perr
	}
	seg, ok := m.SegmentByID(segmentID)
	if !ok {
		return sqlite.Transfer{}, protocol.Segment{}, protocol.Errorf(protocol.ErrTransferNotFound, "unknown segment")
	}
	return t, seg, nil
}

// ClaimSegmentFile opens an immutable segment for a claim holder. The caller
// closes the returned file.
func (s *Service) ClaimSegmentFile(ctx context.Context, claimID, token, segmentID string) (*os.File, int64, *protocol.Error) {
	t, seg, perr := s.claimSegment(ctx, claimID, token, segmentID)
	if perr != nil {
		return nil, 0, perr
	}
	if seg.StoragePath == "" {
		return nil, 0, protocol.Errorf(protocol.ErrInternal, "segment has no storage path")
	}
	path, err := platform.SafeJoin(s.layout.LivePath(t.ID), seg.StoragePath)
	if err != nil {
		return nil, 0, protocol.Errorf(protocol.ErrInternal, "segment path is unsafe")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, protocol.Errorf(protocol.ErrInternal, "segment is unavailable")
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, protocol.Errorf(protocol.ErrInternal, "segment is unreadable")
	}
	// Active progress renews the bounded lease.
	_ = s.store.RenewClaim(ctx, claimID, s.clk.Now().Add(RemoteLeaseMinutes*time.Minute))
	return f, info.Size(), nil
}

// CompleteClaim records completion without consuming the transfer.
func (s *Service) CompleteClaim(ctx context.Context, claimID, token string) *protocol.Error {
	claim, perr := s.authorizeClaim(ctx, claimID, token)
	if perr != nil {
		return perr
	}
	if err := s.store.CompleteClaim(ctx, claim.ID, s.clk.Now()); err != nil {
		return protocol.Errorf(protocol.ErrInternal, "could not record completion")
	}
	return nil
}

func (s *Service) authorizeClaim(ctx context.Context, claimID, token string) (sqlite.Claim, *protocol.Error) {
	claim, err := s.store.GetClaim(ctx, claimID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return sqlite.Claim{}, protocol.Errorf(protocol.ErrTransferNotFound, "unknown claim")
	}
	if err != nil {
		return sqlite.Claim{}, protocol.Errorf(protocol.ErrInternal, "claim lookup failed")
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(claim.TokenHash)) != 1 {
		return sqlite.Claim{}, protocol.Errorf(protocol.ErrAuthInvalid, "claim token rejected")
	}
	if !s.clk.Now().Before(claim.LeaseUntil) {
		return sqlite.Claim{}, protocol.Errorf(protocol.ErrClaimExpired, "receive session lease ended")
	}
	return claim, nil
}

// LocalPath returns the existing committed payload path for a VPS-local
// receiver and records a cleanup grace lease. No bytes are copied.
func (s *Service) LocalPath(ctx context.Context, codeInput string) (protocol.LocalClaim, *protocol.Error) {
	t, perr := s.lookupCommitted(ctx, codeInput)
	if perr != nil {
		return protocol.LocalClaim{}, perr
	}
	payload := s.PayloadDir(t)
	if _, err := os.Stat(payload); err != nil {
		return protocol.LocalClaim{}, protocol.Errorf(protocol.ErrInternal, "payload is unavailable")
	}
	now := s.clk.Now()
	lease := now.Add(time.Duration(s.cfg.Storage.LocalClaimGraceMinutes) * time.Minute)
	err := s.store.CreateClaim(ctx, sqlite.Claim{
		ID:         newClaimID(),
		TransferID: t.ID,
		Kind:       sqlite.ClaimLocal,
		CreatedAt:  now,
		LeaseUntil: lease,
	})
	if err != nil {
		return protocol.LocalClaim{}, protocol.Errorf(protocol.ErrInternal, "could not record local claim")
	}
	return protocol.LocalClaim{
		OK:         true,
		Code:       protocol.FormatCode(t.Code),
		Path:       payload,
		ReadOnly:   true,
		LeaseUntil: lease.UTC(),
	}, nil
}

// InvalidateManifest drops a cached manifest, used when a transfer is deleted.
func (s *Service) InvalidateManifest(transferID string) { s.manifests.drop(transferID) }

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// manifestCache keeps a bounded number of published manifests in memory so that
// a receive session with many segment requests does not re-read a large
// manifest for every request.
type manifestCache struct {
	mu    sync.Mutex
	limit int
	order []string
	items map[string]protocol.Manifest
}

func newManifestCache(limit int) *manifestCache {
	return &manifestCache{limit: limit, items: map[string]protocol.Manifest{}}
}

func (c *manifestCache) get(id string) (protocol.Manifest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.items[id]
	return m, ok
}

func (c *manifestCache) put(id string, m protocol.Manifest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[id]; !exists {
		c.order = append(c.order, id)
		for len(c.order) > c.limit {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.items, oldest)
		}
	}
	c.items[id] = m
}

func (c *manifestCache) drop(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, id)
	for i, v := range c.order {
		if v == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}
