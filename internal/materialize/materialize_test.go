package materialize

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/store/files"
)

// stage builds a staging tree with the given segment files.
func stage(t *testing.T, segments map[string][]byte) string {
	t.Helper()
	root := t.TempDir()
	for _, sub := range []string{files.PayloadDir, files.SegmentsDir, files.PacksDir} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	for id, content := range segments {
		if err := os.WriteFile(filepath.Join(root, files.SegmentsDir, id), content, 0o640); err != nil {
			t.Fatalf("write segment: %v", err)
		}
	}
	return root
}

// tarZstd builds a pack containing the given entries, which may deliberately
// disagree with a manifest.
func tarZstd(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf writeBuffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd: %v", err)
	}
	tw := tar.NewWriter(enc)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(content)), Mode: 0o644, ModTime: time.Now()}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.data
}

type writeBuffer struct{ data []byte }

func (w *writeBuffer) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}

func packManifest(t *testing.T, transferID string, packBytes []byte, entries map[string][]byte) protocol.Manifest {
	t.Helper()
	m := protocol.Manifest{
		SchemaVersion:   1,
		TransferID:      transferID,
		CreatedAt:       time.Now().UTC(),
		Roots:           []string{"project"},
		DigestAlgorithm: protocol.DigestAlgorithm,
		Segments: []protocol.Segment{{
			ID: "p-0", Kind: protocol.SegmentTarZstd,
			WireSize: int64(len(packBytes)), Digest: integrity.HashBytes(packBytes),
		}},
	}
	for path, content := range entries {
		m.Entries = append(m.Entries, protocol.Entry{
			Path: path, Type: protocol.EntryFile, Size: int64(len(content)),
			MTimeUnixNS: time.Now().UnixNano(), Mode: 0o644,
			Digest: integrity.HashBytes(content), SegmentID: "p-0",
		})
	}
	return m
}

func TestMaterializePackRoundTrip(t *testing.T) {
	entries := map[string][]byte{
		"project/one.txt":       []byte("one\n"),
		"project/sub/two.txt":   []byte("two\n"),
		"project/sub/three.bin": []byte("three\n"),
	}
	packBytes := tarZstd(t, entries)
	root := stage(t, map[string][]byte{"p-0": packBytes})
	m := packManifest(t, "t-0123456789abcdef0123456789abcdef", packBytes, entries)

	result, perr := Run(context.Background(), root, m, 2)
	if perr != nil {
		t.Fatalf("materialize: %v", perr)
	}
	for path, want := range entries {
		got, err := os.ReadFile(filepath.Join(root, files.PayloadDir, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if result.MaterializedBytes != m.TotalBytes() {
		t.Errorf("materialized bytes = %d, want %d", result.MaterializedBytes, m.TotalBytes())
	}
	// The pack is retained for later ranged downloads; the staged segment is not.
	if _, err := os.Stat(filepath.Join(root, files.PacksDir, "p-0")); err != nil {
		t.Errorf("pack was not retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, files.SegmentsDir)); !os.IsNotExist(err) {
		t.Error("staged segments directory survived materialization")
	}
}

func TestMaterializeRejectsUndeclaredPackEntry(t *testing.T) {
	declared := map[string][]byte{"project/one.txt": []byte("one\n")}
	smuggled := map[string][]byte{
		"project/one.txt":     []byte("one\n"),
		"project/stowaway.sh": []byte("#!/bin/sh\n"),
	}
	packBytes := tarZstd(t, smuggled)
	root := stage(t, map[string][]byte{"p-0": packBytes})
	m := packManifest(t, "t-0123456789abcdef0123456789abcdef", packBytes, declared)

	_, perr := Run(context.Background(), root, m, 1)
	if perr == nil || perr.Code != protocol.ErrInvalidPath {
		t.Fatalf("err = %v, want INVALID_PATH for an undeclared entry", perr)
	}
	if _, err := os.Stat(filepath.Join(root, files.PayloadDir, "project", "stowaway.sh")); err == nil {
		t.Error("an undeclared entry was written to the payload")
	}
}

func TestMaterializeRejectsTraversalInPack(t *testing.T) {
	hostile := map[string][]byte{"../../escape.txt": []byte("owned\n")}
	packBytes := tarZstd(t, hostile)
	root := stage(t, map[string][]byte{"p-0": packBytes})
	m := packManifest(t, "t-0123456789abcdef0123456789abcdef", packBytes,
		map[string][]byte{"project/one.txt": []byte("one\n")})

	_, perr := Run(context.Background(), root, m, 1)
	if perr == nil {
		t.Fatal("a traversal entry was accepted")
	}
	if perr.Code != protocol.ErrInvalidPath && perr.Code != protocol.ErrHashMismatch {
		t.Errorf("code = %s, want a path or integrity failure", perr.Code)
	}
	outside := filepath.Join(filepath.Dir(root), "escape.txt")
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("extraction escaped the payload root")
	}
}

func TestMaterializeRejectsSizeDisagreement(t *testing.T) {
	entries := map[string][]byte{"project/one.txt": []byte("one\n")}
	packBytes := tarZstd(t, entries)
	root := stage(t, map[string][]byte{"p-0": packBytes})
	m := packManifest(t, "t-0123456789abcdef0123456789abcdef", packBytes, entries)
	m.Entries[0].Size = 99 // manifest lies about the size

	_, perr := Run(context.Background(), root, m, 1)
	if perr == nil || perr.Code != protocol.ErrHashMismatch {
		t.Fatalf("err = %v, want HASH_MISMATCH", perr)
	}
}

func TestMaterializeRawSegmentBecomesPayloadFile(t *testing.T) {
	content := []byte("raw segment content\n")
	root := stage(t, map[string][]byte{"r-0": content})
	digest := integrity.HashBytes(content)
	m := protocol.Manifest{
		SchemaVersion:   1,
		TransferID:      "t-0123456789abcdef0123456789abcdef",
		CreatedAt:       time.Now().UTC(),
		Roots:           []string{"big.bin"},
		DigestAlgorithm: protocol.DigestAlgorithm,
		Segments: []protocol.Segment{{
			ID: "r-0", Kind: protocol.SegmentRaw, WireSize: int64(len(content)), Digest: digest,
		}},
		Entries: []protocol.Entry{{
			Path: "big.bin", Type: protocol.EntryFile, Size: int64(len(content)),
			MTimeUnixNS: time.Now().UnixNano(), Mode: 0o755, Digest: digest, SegmentID: "r-0",
		}},
	}

	result, perr := Run(context.Background(), root, m, 1)
	if perr != nil {
		t.Fatalf("materialize: %v", perr)
	}
	target := filepath.Join(root, files.PayloadDir, "big.bin")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(got) != string(content) {
		t.Error("raw segment content differs")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("executable bit not applied: %v", info.Mode().Perm())
	}
	// The raw segment is served from the payload file itself: no duplicate.
	if result.Published.Segments[0].StoragePath != files.PayloadDir+"/big.bin" {
		t.Errorf("storage path = %q", result.Published.Segments[0].StoragePath)
	}
}

func TestMaterializeDetectsCorruptedContent(t *testing.T) {
	content := []byte("original content\n")
	root := stage(t, map[string][]byte{"r-0": []byte("tampered content\n")})
	digest := integrity.HashBytes(content)
	m := protocol.Manifest{
		SchemaVersion:   1,
		TransferID:      "t-0123456789abcdef0123456789abcdef",
		CreatedAt:       time.Now().UTC(),
		Roots:           []string{"file.txt"},
		DigestAlgorithm: protocol.DigestAlgorithm,
		Segments: []protocol.Segment{{
			ID: "r-0", Kind: protocol.SegmentRaw, WireSize: int64(len(content)), Digest: digest,
		}},
		Entries: []protocol.Entry{{
			Path: "file.txt", Type: protocol.EntryFile, Size: int64(len(content)),
			MTimeUnixNS: time.Now().UnixNano(), Mode: 0o644, Digest: digest, SegmentID: "r-0",
		}},
	}

	_, perr := Run(context.Background(), root, m, 1)
	if perr == nil || perr.Code != protocol.ErrHashMismatch {
		t.Fatalf("err = %v, want HASH_MISMATCH", perr)
	}
}
