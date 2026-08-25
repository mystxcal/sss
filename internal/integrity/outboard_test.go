package integrity

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

// The whole of Phase 1 rests on the Bao root being the same value already
// recorded as a segment digest. If this ever fails, verified streaming stops
// being additive and becomes a protocol break.
func TestOutboardRootIsTheBlake3Digest(t *testing.T) {
	for _, size := range []int64{0, 1, GroupSize - 1, GroupSize, GroupSize + 1, 5*GroupSize + 17} {
		data := make([]byte, size)
		if _, err := rand.Read(data); err != nil {
			t.Fatalf("rand: %v", err)
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "segment")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		root, _, err := OutboardFile(path, filepath.Join(dir, "segment.obao"))
		if err != nil {
			t.Fatalf("outboard (size %d): %v", size, err)
		}
		if want := HashBytes(data); root != want {
			t.Errorf("size %d: bao root %s != blake3 digest %s", size, root, want)
		}
	}
}

func TestVerifyGroupAcceptsAuthenticRanges(t *testing.T) {
	data := make([]byte, 5*GroupSize+512)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "segment")
	obPath := filepath.Join(dir, "segment.obao")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	root, _, err := OutboardFile(path, obPath)
	if err != nil {
		t.Fatalf("outboard: %v", err)
	}
	outboard, err := os.ReadFile(obPath)
	if err != nil {
		t.Fatalf("read outboard: %v", err)
	}

	// Every group verifies on its own, in any order, with no neighbouring bytes.
	for off := int64(len(data)) - int64(len(data))%GroupSize; off >= 0; off -= GroupSize {
		end := off + GroupSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		if off == end {
			continue
		}
		if !VerifyGroup(data[off:end], outboard, off, root) {
			t.Errorf("authentic group at offset %d rejected", off)
		}
	}
}

func TestVerifyGroupRejectsTamperedAndMisplacedData(t *testing.T) {
	data := make([]byte, 4*GroupSize)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "segment")
	obPath := filepath.Join(dir, "segment.obao")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	root, _, err := OutboardFile(path, obPath)
	if err != nil {
		t.Fatalf("outboard: %v", err)
	}
	outboard, err := os.ReadFile(obPath)
	if err != nil {
		t.Fatalf("read outboard: %v", err)
	}

	// One flipped bit anywhere in a group must fail that group.
	tampered := bytes.Clone(data[GroupSize : 2*GroupSize])
	tampered[123] ^= 0x01
	if VerifyGroup(tampered, outboard, GroupSize, root) {
		t.Error("tampered group accepted")
	}

	// Authentic bytes replayed at the wrong offset must fail: position is part
	// of what the tree proves.
	if VerifyGroup(data[0:GroupSize], outboard, GroupSize, root) {
		t.Error("group accepted at the wrong offset")
	}

	// A wrong root must fail even for authentic bytes.
	if VerifyGroup(data[0:GroupSize], outboard, 0, HashBytes([]byte("not the root"))) {
		t.Error("group accepted against the wrong root")
	}
	if VerifyGroup(data[0:GroupSize], outboard, 0, "not-hex") {
		t.Error("malformed root accepted")
	}
}

func TestOutboardSizeMatchesWhatIsWritten(t *testing.T) {
	for _, size := range []int64{1, GroupSize, 3*GroupSize + 9} {
		data := make([]byte, size)
		dir := t.TempDir()
		path := filepath.Join(dir, "segment")
		obPath := filepath.Join(dir, "segment.obao")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, _, err := OutboardFile(path, obPath); err != nil {
			t.Fatalf("outboard: %v", err)
		}
		info, err := os.Stat(obPath)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Size() != OutboardSize(size) {
			t.Errorf("size %d: outboard file is %d bytes, OutboardSize says %d",
				size, info.Size(), OutboardSize(size))
		}
		// The tree must stay a rounding error next to the payload.
		if size >= GroupSize && info.Size() > size/100 {
			t.Errorf("size %d: outboard %d bytes exceeds 1%% of payload", size, info.Size())
		}
	}
}
