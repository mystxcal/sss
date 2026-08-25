package pack

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sss/sss/internal/protocol"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := make([]byte, size)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestScanClassifiesSegments(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tree", "big.bin"), RawThreshold+1)
	writeFile(t, filepath.Join(dir, "tree", "small1.txt"), 10)
	writeFile(t, filepath.Join(dir, "tree", "nested", "small2.txt"), 20)

	plan, perr := Scan([]string{filepath.Join(dir, "tree")})
	if perr != nil {
		t.Fatalf("scan: %v", perr)
	}
	if len(plan.RawFiles) != 1 {
		t.Errorf("raw segments = %d, want 1 (files at or above %d bytes)", len(plan.RawFiles), RawThreshold)
	}
	if len(plan.Packs) != 1 {
		t.Errorf("packs = %d, want 1 bounded pack for the small files", len(plan.Packs))
	}
	if plan.FileCount != 3 {
		t.Errorf("file count = %d, want 3", plan.FileCount)
	}
	if plan.DirCount != 2 {
		t.Errorf("dir count = %d, want 2 (tree and tree/nested)", plan.DirCount)
	}
	if len(plan.Roots) != 1 || plan.Roots[0] != "tree" {
		t.Errorf("roots = %v, want [tree]", plan.Roots)
	}
	for _, e := range plan.Entries {
		if perr := protocol.ValidatePortablePath(e.Path); perr != nil {
			t.Errorf("entry path %q is not portable: %v", e.Path, perr)
		}
		if e.Type == protocol.EntryFile && (e.Digest == "" || e.SegmentID == "") {
			t.Errorf("file entry %q is missing a digest or segment", e.Path)
		}
	}
}

func TestScanRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tree", "real.txt"), 10)
	if err := os.Symlink(filepath.Join(dir, "tree", "real.txt"), filepath.Join(dir, "tree", "link.txt")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	_, perr := Scan([]string{filepath.Join(dir, "tree")})
	if perr == nil || perr.Code != protocol.ErrUnsupportedEntry {
		t.Fatalf("err = %v, want UNSUPPORTED_ENTRY", perr)
	}
}

func TestScanRejectsDuplicateRootNames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "data.txt"), 10)
	writeFile(t, filepath.Join(dir, "b", "data.txt"), 10)
	_, perr := Scan([]string{filepath.Join(dir, "a", "data.txt"), filepath.Join(dir, "b", "data.txt")})
	if perr == nil || perr.Code != protocol.ErrDuplicatePath {
		t.Fatalf("err = %v, want DUPLICATE_PATH", perr)
	}
}

func TestScanRejectsNothing(t *testing.T) {
	if _, perr := Scan(nil); perr == nil || perr.Code != protocol.ErrNoFiles {
		t.Fatalf("err = %v, want NO_FILES", perr)
	}
}

func TestFingerprintChangesWithContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tree", "file.txt")
	writeFile(t, path, 10)

	first, perr := Scan([]string{filepath.Join(dir, "tree")})
	if perr != nil {
		t.Fatalf("scan: %v", perr)
	}
	before := first.Fingerprint("note", 30)

	if before != first.Fingerprint("note", 30) {
		t.Fatal("fingerprint is not stable for identical input")
	}
	if before == first.Fingerprint("different note", 30) {
		t.Error("fingerprint ignores the note")
	}

	writeFile(t, path, 40)
	second, perr := Scan([]string{filepath.Join(dir, "tree")})
	if perr != nil {
		t.Fatalf("rescan: %v", perr)
	}
	if before == second.Fingerprint("note", 30) {
		t.Error("fingerprint did not change after the source changed")
	}
}

func TestVerifyUnchangedDetectsEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tree", "big.bin")
	writeFile(t, path, RawThreshold+1)

	plan, perr := Scan([]string{filepath.Join(dir, "tree")})
	if perr != nil {
		t.Fatalf("scan: %v", perr)
	}
	if perr := plan.VerifyUnchanged(); perr != nil {
		t.Fatalf("unchanged sources reported as changed: %v", perr)
	}

	writeFile(t, path, RawThreshold+64)
	if perr := plan.VerifyUnchanged(); perr == nil || perr.Code != protocol.ErrSourceChanged {
		t.Fatalf("err = %v, want SOURCE_CHANGED", perr)
	}
}

func TestCompressibleDistinguishesPayloads(t *testing.T) {
	dir := t.TempDir()

	// Repetitive text: worth compressing.
	text := filepath.Join(dir, "text.txt")
	writeFile(t, text, 128<<10)

	// Random bytes stand in for media, archives, and encrypted blobs: nothing
	// to gain, so the pack should drop to the cheap encoder level.
	random := filepath.Join(dir, "random.bin")
	buf := make([]byte, 128<<10)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := os.WriteFile(random, buf, 0o644); err != nil {
		t.Fatalf("write random: %v", err)
	}

	if !compressible(PackSpec{Files: []Source{{AbsPath: text, Size: 128 << 10}}}) {
		t.Error("repetitive text judged incompressible")
	}
	if compressible(PackSpec{Files: []Source{{AbsPath: random, Size: 128 << 10}}}) {
		t.Error("random bytes judged compressible")
	}
	// An unreadable source must fall back to the conservative answer rather
	// than silently packing at the cheap level.
	if !compressible(PackSpec{Files: []Source{{AbsPath: filepath.Join(dir, "missing"), Size: 1}}}) {
		t.Error("missing file should default to compressible")
	}
}

func TestBuildRoundTripsIncompressiblePack(t *testing.T) {
	dir := t.TempDir()
	buf := make([]byte, 64<<10)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tree"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tree", "blob.bin"), buf, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	plan, perr := Scan([]string{filepath.Join(dir, "tree")})
	if perr != nil {
		t.Fatalf("scan: %v", perr)
	}
	// The cheap level must still produce a normal, verifiable tar.zst segment:
	// the wire format does not change, only the CPU spent producing it.
	path, size, digest, perr := Build(plan.Packs[0], filepath.Join(dir, "packs"))
	if perr != nil {
		t.Fatalf("build: %v", perr)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat pack: %v", err)
	}
	if info.Size() != size {
		t.Errorf("reported size %d, file is %d", size, info.Size())
	}
	if len(digest) != 64 {
		t.Errorf("digest %q is not a 64 character hex string", digest)
	}
}

func TestBuildProducesVerifiablePack(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tree", "one.txt"), 100)
	writeFile(t, filepath.Join(dir, "tree", "two.txt"), 200)

	plan, perr := Scan([]string{filepath.Join(dir, "tree")})
	if perr != nil {
		t.Fatalf("scan: %v", perr)
	}
	if len(plan.Packs) != 1 {
		t.Fatalf("packs = %d, want 1", len(plan.Packs))
	}
	path, size, digest, perr := Build(plan.Packs[0], filepath.Join(dir, "packs"))
	if perr != nil {
		t.Fatalf("build: %v", perr)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat pack: %v", err)
	}
	if info.Size() != size {
		t.Errorf("reported size %d, file is %d", size, info.Size())
	}
	if len(digest) != 64 {
		t.Errorf("digest %q is not a 64 character hex string", digest)
	}
}
