package blackbox

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sss/sss/internal/clock"
	"github.com/sss/sss/internal/protocol"
)

// B4: expiry is measured from commit, blocks new claims, and eventually
// deletes the payload.
func TestExpiryLifecycle(t *testing.T) {
	clk := clock.NewFixed(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	s := startServer(t, options{fixedClock: clk})
	content := []byte("expiring\n")
	code := s.sendFile("alpha.txt", content, "", "1")

	if status, body, _ := s.receive(code, ""); status != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("receive before expiry: status %d", status)
	}

	clk.Advance(2 * time.Minute)

	resp := s.do(s.request(http.MethodGet, "/r/"+code, nil))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status after expiry = %d, want 410", resp.StatusCode)
	}
	if got := decodeError(t, resp); got.Code != protocol.ErrTransferExpired {
		t.Errorf("code = %s, want %s", got.Code, protocol.ErrTransferExpired)
	}

	// Cleanup is idempotent and removes the expired payload.
	for i := 0; i < 2; i++ {
		status, body := s.localPost("/local/cleanup")
		if status != http.StatusOK {
			t.Fatalf("cleanup status = %d", status)
		}
		var result protocol.CleanupResult
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode cleanup: %v", err)
		}
		if i == 0 && result.Deleted != 1 {
			t.Errorf("first cleanup deleted %d, want 1", result.Deleted)
		}
		if i == 1 && result.Deleted != 0 {
			t.Errorf("second cleanup deleted %d, want 0", result.Deleted)
		}
	}
	if status := s.statusSnapshot(t); status.Committed != 0 {
		t.Errorf("committed after cleanup = %d, want 0", status.Committed)
	}
}

// G: a restart preserves committed transfers, codes, and expiry.
func TestRestartPreservesCommittedTransfers(t *testing.T) {
	s := startServer(t, options{})
	content := []byte("survives a restart\n")
	code := s.sendFile("alpha.txt", content, "restart note", "120")

	resp := s.do(s.request(http.MethodGet, "/v1/transfers/"+code, nil))
	var before protocol.TransferMetadata
	_ = json.NewDecoder(resp.Body).Decode(&before)
	resp.Body.Close()

	s2 := s.restart()

	status, body, _ := s2.receive(code, "")
	if status != http.StatusOK {
		t.Fatalf("receive after restart: status %d", status)
	}
	if !bytes.Equal(body, content) {
		t.Error("bytes differ after restart")
	}

	resp2 := s2.do(s2.request(http.MethodGet, "/v1/transfers/"+code, nil))
	var after protocol.TransferMetadata
	_ = json.NewDecoder(resp2.Body).Decode(&after)
	resp2.Body.Close()
	if !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Errorf("expiry moved from %s to %s", before.ExpiresAt, after.ExpiresAt)
	}
	if after.Note != before.Note || after.FileCount != before.FileCount {
		t.Errorf("metadata changed across restart: %+v vs %+v", before, after)
	}
}
