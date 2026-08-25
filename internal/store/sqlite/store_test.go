package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sss/sss/internal/protocol"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sss.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func createTransfer(t *testing.T, s *Store, id string) Transfer {
	t.Helper()
	rec := Transfer{
		ID: id, State: StateCreated, CreatedAt: time.Now().Truncate(time.Second),
		RequestedTTLMinutes: 30, RootPath: "/srv/sss/staging/" + id,
	}
	if err := s.CreateTransfer(context.Background(), rec); err != nil {
		t.Fatalf("create: %v", err)
	}
	return rec
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sss.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	createTransfer(t, first, "t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	if _, err := second.GetTransfer(context.Background(), "t-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("record lost across reopen: %v", err)
	}
}

func TestPublishAllocatesUniqueCodeOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := "t-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	createTransfer(t, s, id)

	now := time.Now().Truncate(time.Second)
	rec, err := s.Publish(ctx, id, now, 120, "digest", 4096)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if rec.Code == "" || rec.State != StateCommitted {
		t.Fatalf("record = %+v", rec)
	}
	if _, ok := protocol.NormalizeCode(rec.Code); !ok {
		t.Errorf("stored code %q is not canonical", rec.Code)
	}
	if got := rec.ExpiresAt.Sub(rec.CommittedAt); got != 120*time.Minute {
		t.Errorf("expiry window = %s, want 2h", got)
	}

	// Publication is idempotent: the code never changes.
	again, err := s.Publish(ctx, id, now.Add(time.Hour), 30, "digest", 4096)
	if err != nil {
		t.Fatalf("repeat publish: %v", err)
	}
	if again.Code != rec.Code || !again.ExpiresAt.Equal(rec.ExpiresAt) {
		t.Errorf("repeat publish changed the record: %+v vs %+v", again, rec)
	}

	byCode, err := s.GetTransferByCode(ctx, rec.Code)
	if err != nil || byCode.ID != id {
		t.Fatalf("lookup by code failed: %v", err)
	}
}

func TestPublishRejectsTerminalStates(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := "t-cccccccccccccccccccccccccccccccc"
	createTransfer(t, s, id)
	if err := s.SetState(ctx, id, StateAbandoned); err != nil {
		t.Fatalf("set state: %v", err)
	}
	_, err := s.Publish(ctx, id, time.Now(), 30, "digest", 0)
	if err == nil {
		t.Fatal("an abandoned transfer was published")
	}
	if perr := protocol.AsError(err); perr.Code != protocol.ErrStateConflict {
		t.Errorf("code = %s, want STATE_CONFLICT", perr.Code)
	}
}

func TestConcurrentPublishesGetDistinctCodes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const n = 12
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = "t-" + string(rune('a'+i)) + "0123456789abcdef0123456789abcd"
		createTransfer(t, s, ids[i])
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		codes = map[string]bool{}
	)
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			rec, err := s.Publish(ctx, id, time.Now(), 30, "digest", 0)
			if err != nil {
				t.Errorf("publish %s: %v", id, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if codes[rec.Code] {
				t.Errorf("duplicate code %s", rec.Code)
			}
			codes[rec.Code] = true
		}(id)
	}
	wg.Wait()
	if len(codes) != n {
		t.Errorf("allocated %d distinct codes, want %d", len(codes), n)
	}
}

func TestExpiryAndLeasePinning(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := "t-dddddddddddddddddddddddddddddddd"
	createTransfer(t, s, id)
	now := time.Now().Truncate(time.Second)
	if _, err := s.Publish(ctx, id, now, 1, "digest", 0); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if n, err := s.ExpireDue(ctx, now); err != nil || n != 0 {
		t.Fatalf("expired %d before expiry (err %v)", n, err)
	}
	later := now.Add(2 * time.Minute)
	if n, err := s.ExpireDue(ctx, later); err != nil || n != 1 {
		t.Fatalf("expired %d at expiry (err %v)", n, err)
	}

	// An active lease pins the data even after expiry.
	claimID := "c-1"
	if err := s.CreateClaim(ctx, Claim{
		ID: claimID, TransferID: id, Kind: ClaimRemote,
		CreatedAt: now, LeaseUntil: later.Add(30 * time.Minute), TokenHash: "hash",
	}); err != nil {
		t.Fatalf("create claim: %v", err)
	}
	pinned, err := s.UnpinnedExpired(ctx, later)
	if err != nil {
		t.Fatalf("unpinned: %v", err)
	}
	if len(pinned) != 0 {
		t.Errorf("a leased transfer was offered for deletion")
	}

	// Once the claim completes, the transfer may be deleted.
	if err := s.CompleteClaim(ctx, claimID, later); err != nil {
		t.Fatalf("complete claim: %v", err)
	}
	unpinned, err := s.UnpinnedExpired(ctx, later)
	if err != nil {
		t.Fatalf("unpinned: %v", err)
	}
	if len(unpinned) != 1 {
		t.Fatalf("unpinned = %d, want 1", len(unpinned))
	}

	if err := s.Purge(ctx, id); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := s.GetTransfer(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("record survived purge: %v", err)
	}
	if _, err := s.GetClaim(ctx, claimID); !errors.Is(err, ErrNotFound) {
		t.Errorf("claim survived purge: %v", err)
	}
}

func TestSegmentIdentifiersAreScopedToTransfer(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := "t-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	second := "t-ffffffffffffffffffffffffffffffff"
	createTransfer(t, s, first)
	createTransfer(t, s, second)

	// Clients choose their own segment names, so the same name must be usable
	// in two different transfers.
	for _, id := range []string{first, second} {
		if err := s.InsertSegment(ctx, Segment{
			ID: "r-0000", TransferID: id, UploadID: "u-" + id, Kind: protocol.SegmentRaw,
			ExpectedLength: 100, DigestAlgorithm: protocol.DigestAlgorithm,
			State: SegmentPending, RelativeStoragePath: "segments/r-0000",
		}); err != nil {
			t.Fatalf("insert segment for %s: %v", id, err)
		}
	}

	if err := s.SetSegmentProgress(ctx, first, "r-0000", 50, SegmentPending); err != nil {
		t.Fatalf("progress: %v", err)
	}
	a, err := s.GetSegment(ctx, first, "r-0000")
	if err != nil {
		t.Fatalf("get segment: %v", err)
	}
	b, err := s.GetSegment(ctx, second, "r-0000")
	if err != nil {
		t.Fatalf("get segment: %v", err)
	}
	if a.ReceivedLength != 50 || b.ReceivedLength != 0 {
		t.Errorf("progress leaked across transfers: %d and %d", a.ReceivedLength, b.ReceivedLength)
	}
}

func TestIdempotencyKeys(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	rec := Idempotency{
		KeyHash: "hash-1", Operation: "simple-send",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), RequestFingerprint: "fp",
	}

	if _, found, err := s.RememberIdempotency(ctx, rec); err != nil || found {
		t.Fatalf("first use: found=%v err=%v", found, err)
	}
	existing, found, err := s.RememberIdempotency(ctx, rec)
	if err != nil || !found {
		t.Fatalf("second use: found=%v err=%v", found, err)
	}
	if existing.RequestFingerprint != "fp" {
		t.Errorf("fingerprint = %q", existing.RequestFingerprint)
	}

	if err := s.CompleteIdempotency(ctx, "hash-1", "t-1", "K7M4-Q2PX"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	stored, _, err := s.RememberIdempotency(ctx, rec)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if stored.ResponseCode != "K7M4-Q2PX" {
		t.Errorf("response code = %q", stored.ResponseCode)
	}

	if err := s.PurgeExpiredIdempotency(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := s.LookupIdempotency(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired key survived: %v", err)
	}
}

func TestStaleStaging(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	old := "t-11111111111111111111111111111111"
	fresh := "t-22222222222222222222222222222222"
	now := time.Now()

	if err := s.CreateTransfer(ctx, Transfer{
		ID: old, State: StateUploading, CreatedAt: now.Add(-8 * time.Hour),
		RequestedTTLMinutes: 30, RootPath: "/tmp/" + old,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	createTransfer(t, s, fresh)

	stale, err := s.StaleStaging(ctx, now.Add(-6*time.Hour))
	if err != nil {
		t.Fatalf("stale: %v", err)
	}
	if len(stale) != 1 || stale[0].ID != old {
		t.Fatalf("stale = %+v, want only the old transfer", stale)
	}
}
