package blackbox

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sss/sss/internal/protocol"
)

// F: VPS-local receipt returns the existing path and copies nothing.
func TestLocalReceiveReturnsExistingPath(t *testing.T) {
	s := startServer(t, options{})
	content := []byte("local-plane payload\n")
	code := s.sendFile("alpha.txt", content, "note for local", "")

	beforeBytes := treeSize(t, s.dataDir)

	status, body := s.localGet("/local/r/"+code, "")
	if status != http.StatusOK {
		t.Fatalf("local receive status = %d body %s", status, body)
	}
	path := strings.TrimSpace(string(body))
	if !strings.HasPrefix(path, filepath.Join(s.dataDir, "live")) {
		t.Fatalf("path %q is not inside the configured live root", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("payload path does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("payload path %q is not a directory", path)
	}
	got, err := os.ReadFile(filepath.Join(path, "alpha.txt"))
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Error("local payload bytes differ")
	}

	// The payload is read-only: no local agent may modify committed content.
	if info.Mode().Perm()&0o222 != 0 {
		t.Errorf("payload directory mode = %v, want no write bits", info.Mode().Perm())
	}
	fileInfo, err := os.Stat(filepath.Join(path, "alpha.txt"))
	if err != nil {
		t.Fatalf("stat payload file: %v", err)
	}
	if fileInfo.Mode().Perm()&0o222 != 0 {
		t.Errorf("payload file mode = %v, want no write bits", fileInfo.Mode().Perm())
	}

	// No second copy of the payload was created.
	afterBytes := treeSize(t, s.dataDir)
	if afterBytes > beforeBytes+4096 {
		t.Errorf("storage grew by %d bytes during a local receive", afterBytes-beforeBytes)
	}

	t.Run("json form", func(t *testing.T) {
		status, body := s.localGet("/local/r/"+code, "application/json")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		var claim protocol.LocalClaim
		if err := json.Unmarshal(body, &claim); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !claim.OK || !claim.ReadOnly || claim.Path != path {
			t.Errorf("claim = %+v", claim)
		}
		if claim.LeaseUntil.IsZero() {
			t.Error("lease_until is missing")
		}
	})
}

// The local plane must not be reachable from the public listener.
func TestLocalRoutesAreNotPublic(t *testing.T) {
	s := startServer(t, options{})
	code := s.sendFile("alpha.txt", []byte("alpha\n"), "", "")
	resp := s.do(s.request(http.MethodGet, "/local/r/"+code, nil))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("public /local/r status = %d, want 404", resp.StatusCode)
	}
}

// The local plane also serves status and on-demand cleanup.
func TestLocalStatusAndCleanup(t *testing.T) {
	s := startServer(t, options{})
	s.sendFile("alpha.txt", []byte("alpha\n"), "", "")

	status := s.statusSnapshot(t)
	if !status.Ready {
		t.Error("server reports not ready")
	}
	if status.Committed != 1 {
		t.Errorf("committed = %d, want 1", status.Committed)
	}
	if status.StorageDir != s.dataDir {
		t.Errorf("storage dir = %q, want %q", status.StorageDir, s.dataDir)
	}

	code, body := s.localPost("/local/cleanup")
	if code != http.StatusOK {
		t.Fatalf("cleanup status = %d body %s", code, body)
	}
	var result protocol.CleanupResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode cleanup: %v", err)
	}
	if result.Deleted != 0 {
		t.Errorf("cleanup deleted %d live transfers that had not expired", result.Deleted)
	}
}

func treeSize(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("measure tree: %v", err)
	}
	return total
}
