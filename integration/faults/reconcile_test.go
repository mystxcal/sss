// Package faults injects crashes at the dangerous boundaries of the commit and
// deletion protocols and asserts that recovery always reaches a valid state.
//
// The non-negotiable invariant under test: no code ever resolves to incomplete
// data, and no incomplete transfer is ever published.
package faults

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sss/sss/internal/clock"
	"github.com/sss/sss/internal/config"
	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/reconcile"
	"github.com/sss/sss/internal/store/files"
	"github.com/sss/sss/internal/store/sqlite"
	"github.com/sss/sss/internal/transfer"
)

type env struct {
	t      *testing.T
	cfg    config.Server
	store  *sqlite.Store
	layout *files.Layout
	svc    *transfer.Service
	log    *slog.Logger
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	cfg := config.DefaultServer()
	cfg.Storage.DataDir = filepath.Join(dir, "srv")
	cfg.Storage.DBPath = filepath.Join(dir, "var", "sss.db")
	cfg.Server.UnixSocket = filepath.Join(dir, "s.sock")
	cfg.Auth.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2Ex$ZGlnZXN0ZGlnZXN0ZGlnZXN0ZGlnZXN0ZGln"

	store, err := sqlite.Open(cfg.Storage.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	layout, err := files.New(cfg.Storage.DataDir)
	if err != nil {
		t.Fatalf("layout: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := transfer.New(cfg, store, layout, clock.Real{}, log)
	return &env{t: t, cfg: cfg, store: store, layout: layout, svc: svc, log: log}
}

// stageCommittedPayload builds a valid live directory for a transfer whose
// database record is still pre-commit. This is exactly the state a crash
// between the staging rename and the database transaction leaves behind.
func (e *env) stageCommittedPayload(id string, content []byte) protocol.Manifest {
	e.t.Helper()
	root, err := e.layout.PrepareStaging(id)
	if err != nil {
		e.t.Fatalf("prepare staging: %v", err)
	}
	payload := filepath.Join(root, files.PayloadDir)
	if err := os.WriteFile(filepath.Join(payload, "alpha.txt"), content, 0o640); err != nil {
		e.t.Fatalf("write payload: %v", err)
	}
	digest := integrity.HashBytes(content)
	m := protocol.Manifest{
		SchemaVersion:   1,
		TransferID:      id,
		CreatedAt:       time.Now().UTC(),
		Roots:           []string{"alpha.txt"},
		DigestAlgorithm: protocol.DigestAlgorithm,
		Segments: []protocol.Segment{{
			ID: "s-0", Kind: protocol.SegmentRaw, WireSize: int64(len(content)),
			Digest: digest, StoragePath: files.PayloadDir + "/alpha.txt",
		}},
		Entries: []protocol.Entry{{
			Path: "alpha.txt", Type: protocol.EntryFile, Size: int64(len(content)),
			MTimeUnixNS: time.Now().UnixNano(), Mode: 0o644, Digest: digest, SegmentID: "s-0",
		}},
	}
	if err := files.WriteManifest(root, m); err != nil {
		e.t.Fatalf("write manifest: %v", err)
	}
	if err := platform.SealPayload(root); err != nil {
		e.t.Fatalf("seal payload: %v", err)
	}
	if _, err := e.layout.Publish(id); err != nil {
		e.t.Fatalf("publish: %v", err)
	}
	return m
}

// A crash after the staging rename but before the database transaction must be
// completed idempotently: the transfer becomes committed with a fresh code.
func TestReconcileCompletesInterruptedCommit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := "t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	if err := e.store.CreateTransfer(ctx, sqlite.Transfer{
		ID: id, State: sqlite.StateVerifying, CreatedAt: time.Now(),
		RequestedTTLMinutes: 120, RootPath: e.layout.StagingPath(id),
	}); err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	e.stageCommittedPayload(id, []byte("interrupted commit\n"))

	rep, err := reconcile.Run(ctx, e.svc, e.log)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.CompletedCommits != 1 {
		t.Fatalf("completed commits = %d, want 1", rep.CompletedCommits)
	}
	rec, err := e.store.GetTransfer(ctx, id)
	if err != nil {
		t.Fatalf("get transfer: %v", err)
	}
	if !rec.Committed() {
		t.Fatalf("transfer state = %s with code %q", rec.State, rec.Code)
	}
	if got := rec.ExpiresAt.Sub(rec.CommittedAt); got != 120*time.Minute {
		t.Errorf("expiry window = %s, want the requested 2h", got)
	}

	// Reconciliation is idempotent: a second pass changes nothing.
	rep2, err := reconcile.Run(ctx, e.svc, e.log)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if rep2.CompletedCommits != 0 || rep2.FailedTransfers != 0 {
		t.Errorf("second pass = %+v, want no changes", rep2)
	}
	after, _ := e.store.GetTransfer(ctx, id)
	if after.Code != rec.Code {
		t.Errorf("code changed from %s to %s on the second pass", rec.Code, after.Code)
	}
}

// A crash during materialization leaves consumed segments behind. That transfer
// can never be completed, so it must fail cleanly instead of publishing.
func TestReconcileFailsInterruptedMaterialization(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := "t-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	if err := e.store.CreateTransfer(ctx, sqlite.Transfer{
		ID: id, State: sqlite.StateVerifying, CreatedAt: time.Now(),
		RequestedTTLMinutes: 30, RootPath: e.layout.StagingPath(id),
	}); err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	// Staging exists but was never published and has no manifest.
	if _, err := e.layout.PrepareStaging(id); err != nil {
		t.Fatalf("prepare staging: %v", err)
	}

	rep, err := reconcile.Run(ctx, e.svc, e.log)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.FailedTransfers != 1 {
		t.Fatalf("failed transfers = %d, want 1", rep.FailedTransfers)
	}
	rec, err := e.store.GetTransfer(ctx, id)
	if err != nil {
		t.Fatalf("get transfer: %v", err)
	}
	if rec.State != sqlite.StateFailed || rec.Code != "" {
		t.Fatalf("state = %s code = %q, want FAILED with no code", rec.State, rec.Code)
	}
}

// A committed record whose payload vanished must stop resolving rather than
// serve incomplete data.
func TestReconcileDropsCommittedRecordWithoutPayload(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := "t-cccccccccccccccccccccccccccccccc"

	if err := e.store.CreateTransfer(ctx, sqlite.Transfer{
		ID: id, State: sqlite.StateCreated, CreatedAt: time.Now(),
		RequestedTTLMinutes: 30, RootPath: e.layout.StagingPath(id),
	}); err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	e.stageCommittedPayload(id, []byte("about to be lost\n"))
	if _, err := e.store.Publish(ctx, id, time.Now(), 30, "digest", 18); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Simulate storage loss underneath a committed record.
	if err := platform.RemoveAllForce(e.layout.LivePath(id)); err != nil {
		t.Fatalf("remove live: %v", err)
	}

	rep, err := reconcile.Run(ctx, e.svc, e.log)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.FailedTransfers != 1 {
		t.Errorf("failed transfers = %d, want 1", rep.FailedTransfers)
	}
	if _, err := e.store.GetTransfer(ctx, id); err == nil {
		t.Error("record survived even though its payload was gone")
	}
}

// A live directory with no metadata can never be published, so it is trashed.
func TestReconcileTrashesOrphanDirectories(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	orphanLive := "t-dddddddddddddddddddddddddddddddd"
	orphanStaging := "t-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	e.stageCommittedPayload(orphanLive, []byte("orphan\n"))
	if _, err := e.layout.PrepareStaging(orphanStaging); err != nil {
		t.Fatalf("prepare staging: %v", err)
	}

	rep, err := reconcile.Run(ctx, e.svc, e.log)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.OrphanLive != 1 || rep.OrphanStaging != 1 {
		t.Fatalf("report = %+v, want one orphan of each kind", rep)
	}
	if _, err := os.Stat(e.layout.LivePath(orphanLive)); !os.IsNotExist(err) {
		t.Error("orphan live directory still present")
	}
	if _, err := os.Stat(e.layout.StagingPath(orphanStaging)); !os.IsNotExist(err) {
		t.Error("orphan staging directory still present")
	}
	entries, err := os.ReadDir(e.layout.Trash())
	if err != nil {
		t.Fatalf("read trash: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("trash still holds %d entries after reconciliation", len(entries))
	}
}

// An interrupted deletion finishes on the next start.
func TestReconcileFinishesInterruptedDeletion(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := "t-ffffffffffffffffffffffffffffffff"

	if err := e.store.CreateTransfer(ctx, sqlite.Transfer{
		ID: id, State: sqlite.StateDeleting, CreatedAt: time.Now(),
		RequestedTTLMinutes: 30, RootPath: e.layout.StagingPath(id),
	}); err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	e.stageCommittedPayload(id, []byte("half deleted\n"))

	rep, err := reconcile.Run(ctx, e.svc, e.log)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.FinishedDeletes != 1 {
		t.Fatalf("finished deletes = %d, want 1", rep.FinishedDeletes)
	}
	if _, err := os.Stat(e.layout.LivePath(id)); !os.IsNotExist(err) {
		t.Error("live directory survived a finished deletion")
	}
	if _, err := e.store.GetTransfer(ctx, id); err == nil {
		t.Error("record survived a finished deletion")
	}
}
