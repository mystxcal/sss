package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// SealPayload must make the contents immutable while leaving the transfer
// directory itself writable.
//
// Regression: sealing the root broke publication and deletion for the real
// service user. Moving a directory to a new parent rewrites its ".." entry, so
// rename(2) needs write permission on the directory being moved. Running tests
// as root hides this, because root bypasses the check — hence the explicit
// mode assertions here.
func TestSealPayloadKeepsRootWritable(t *testing.T) {
	root := t.TempDir()
	transferDir := filepath.Join(root, "t-transfer")
	payload := filepath.Join(transferDir, "payload", "nested")
	if err := os.MkdirAll(payload, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(payload, "alpha.txt")
	if err := os.WriteFile(file, []byte("alpha\n"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	manifest := filepath.Join(transferDir, "manifest.json")
	if err := os.WriteFile(manifest, []byte("{}\n"), 0o640); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if err := SealPayload(transferDir); err != nil {
		t.Fatalf("seal: %v", err)
	}

	rootInfo, err := os.Stat(transferDir)
	if err != nil {
		t.Fatalf("stat transfer dir: %v", err)
	}
	if rootInfo.Mode().Perm()&0o200 == 0 {
		t.Errorf("transfer directory mode = %v; publication and deletion renames need owner write",
			rootInfo.Mode().Perm())
	}

	for _, sealed := range []string{filepath.Join(transferDir, "payload"), payload, file, manifest} {
		info, err := os.Stat(sealed)
		if err != nil {
			t.Fatalf("stat %s: %v", sealed, err)
		}
		if info.Mode().Perm()&0o222 != 0 {
			t.Errorf("%s mode = %v, want no write bits", sealed, info.Mode().Perm())
		}
	}

	// The sealed directory must still be movable, which is what publication
	// and deletion do.
	moved := filepath.Join(root, "moved")
	if err := os.Rename(transferDir, moved); err != nil {
		t.Fatalf("sealed transfer directory could not be renamed: %v", err)
	}

	// And it must still be removable after write bits are restored.
	if err := RemoveAllForce(moved); err != nil {
		t.Fatalf("sealed transfer directory could not be deleted: %v", err)
	}
}
