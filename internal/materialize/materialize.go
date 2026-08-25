// Package materialize turns verified staged segments into the final payload
// tree. Every archive header and manifest entry is treated as hostile even
// though the sending devices are trusted.
package materialize

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/store/files"
)

// copyBufferSize bounds memory per extraction stream.
const copyBufferSize = 1 << 20

// Result reports what materialization produced.
type Result struct {
	// Published is the manifest as stored in the live directory: identical to
	// the client manifest except that every segment carries the server-side
	// storage path used to serve later downloads.
	Published protocol.Manifest
	// MaterializedBytes is the total size of the payload tree.
	MaterializedBytes int64
}

// Run materializes a staged transfer in place. The staging root must contain a
// segments directory holding one file per declared segment.
func Run(ctx context.Context, stagingRoot string, m protocol.Manifest, workers int) (Result, *protocol.Error) {
	if workers < 1 {
		workers = 1
	}
	payloadDir := filepath.Join(stagingRoot, files.PayloadDir)
	packsDir := filepath.Join(stagingRoot, files.PacksDir)
	segmentsDir := filepath.Join(stagingRoot, files.SegmentsDir)

	if err := os.MkdirAll(payloadDir, 0o750); err != nil {
		return Result{}, protocol.Errorf(protocol.ErrInternal, "create payload directory: %v", err)
	}

	// Index entries by segment so extraction can reject anything undeclared.
	entriesBySegment := map[string][]protocol.Entry{}
	entryByPath := map[string]protocol.Entry{}
	for _, e := range m.Entries {
		if e.Type == protocol.EntryFile {
			entriesBySegment[e.SegmentID] = append(entriesBySegment[e.SegmentID], e)
		}
		entryByPath[e.Path] = e
	}

	// Directories first, so extraction never has to invent a parent.
	for _, e := range m.SortedEntries() {
		var dir string
		var err error
		if e.Type == protocol.EntryDirectory {
			dir, err = platform.SafeJoin(payloadDir, e.Path)
		} else {
			parent := filepath.Dir(e.Path)
			if parent == "." {
				continue
			}
			dir, err = platform.SafeJoin(payloadDir, parent)
		}
		if err != nil {
			return Result{}, protocol.Errorf(protocol.ErrInvalidPath, "unsafe path %q", e.Path)
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return Result{}, protocol.Errorf(protocol.ErrInternal, "create directory: %v", err)
		}
	}

	published := m
	published.Segments = make([]protocol.Segment, len(m.Segments))
	copy(published.Segments, m.Segments)

	for i, seg := range published.Segments {
		staged := filepath.Join(segmentsDir, seg.ID)
		if _, err := os.Stat(staged); err != nil {
			return Result{}, protocol.Errorf(protocol.ErrStateConflict, "segment %q was not uploaded", seg.ID)
		}
		switch seg.Kind {
		case protocol.SegmentRaw:
			list := entriesBySegment[seg.ID]
			if len(list) != 1 {
				return Result{}, protocol.Errorf(protocol.ErrInvalidRequest, "raw segment %q must back exactly one file", seg.ID)
			}
			entry := list[0]
			target, err := platform.SafeJoin(payloadDir, entry.Path)
			if err != nil {
				return Result{}, protocol.Errorf(protocol.ErrInvalidPath, "unsafe path %q", entry.Path)
			}
			// A raw segment becomes the payload file directly: no second copy.
			if err := os.Rename(staged, target); err != nil {
				return Result{}, protocol.Errorf(protocol.ErrInternal, "place raw segment: %v", err)
			}
			published.Segments[i].StoragePath = files.PayloadDir + "/" + entry.Path
		case protocol.SegmentTarZstd:
			if err := os.MkdirAll(packsDir, 0o750); err != nil {
				return Result{}, protocol.Errorf(protocol.ErrInternal, "create packs directory: %v", err)
			}
			packPath := filepath.Join(packsDir, seg.ID)
			if err := os.Rename(staged, packPath); err != nil {
				return Result{}, protocol.Errorf(protocol.ErrInternal, "place pack segment: %v", err)
			}
			if perr := extractPack(ctx, packPath, payloadDir, entriesBySegment[seg.ID]); perr != nil {
				return Result{}, perr
			}
			published.Segments[i].StoragePath = files.PacksDir + "/" + seg.ID
		default:
			return Result{}, protocol.Errorf(protocol.ErrInvalidRequest, "unsupported segment kind %q", seg.Kind)
		}
	}

	// Apply portable metadata: executable bit and modification time.
	for _, e := range m.Entries {
		if e.Type != protocol.EntryFile {
			continue
		}
		target, err := platform.SafeJoin(payloadDir, e.Path)
		if err != nil {
			return Result{}, protocol.Errorf(protocol.ErrInvalidPath, "unsafe path %q", e.Path)
		}
		mode := os.FileMode(0o644)
		if e.Mode&0o111 != 0 {
			mode = 0o755
		}
		if err := os.Chmod(target, mode); err != nil {
			return Result{}, protocol.Errorf(protocol.ErrInternal, "apply mode: %v", err)
		}
		mtime := time.Unix(0, e.MTimeUnixNS)
		if err := os.Chtimes(target, mtime, mtime); err != nil {
			return Result{}, protocol.Errorf(protocol.ErrInternal, "apply mtime: %v", err)
		}
	}

	if perr := verify(ctx, payloadDir, m, workers); perr != nil {
		return Result{}, perr
	}

	// Directory times come last so file writes cannot disturb them.
	for _, e := range m.SortedEntries() {
		if e.Type != protocol.EntryDirectory {
			continue
		}
		target, err := platform.SafeJoin(payloadDir, e.Path)
		if err != nil {
			continue
		}
		mtime := time.Unix(0, e.MTimeUnixNS)
		_ = os.Chtimes(target, mtime, mtime)
	}

	if err := os.Remove(segmentsDir); err != nil && !os.IsNotExist(err) {
		// Leftover files here mean a client uploaded bytes the manifest never
		// declared; that is a hard failure rather than a silent cleanup.
		if !errors.Is(err, os.ErrNotExist) {
			return Result{}, protocol.Errorf(protocol.ErrStateConflict, "staged segments remain after materialization")
		}
	}

	return Result{Published: published, MaterializedBytes: m.TotalBytes()}, nil
}

// extractPack streams one tar.zst segment into the payload, accepting only
// entries the manifest declared for that segment.
func extractPack(ctx context.Context, packPath, payloadDir string, declared []protocol.Entry) *protocol.Error {
	allowed := make(map[string]protocol.Entry, len(declared))
	for _, e := range declared {
		allowed[e.Path] = e
	}
	f, err := os.Open(packPath)
	if err != nil {
		return protocol.Errorf(protocol.ErrInternal, "open pack: %v", err)
	}
	defer f.Close()
	dec, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		return protocol.Errorf(protocol.ErrInternal, "open zstd stream: %v", err)
	}
	defer dec.Close()

	tr := tar.NewReader(dec)
	seen := map[string]bool{}
	buf := make([]byte, copyBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return protocol.Errorf(protocol.ErrInternal, "extraction cancelled")
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return protocol.Errorf(protocol.ErrHashMismatch, "pack is unreadable: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return protocol.Errorf(protocol.ErrUnsupportedEntry, "pack contains unsupported entry type for %q", hdr.Name)
		}
		if perr := protocol.ValidatePortablePath(hdr.Name); perr != nil {
			return perr
		}
		entry, ok := allowed[hdr.Name]
		if !ok {
			return protocol.Errorf(protocol.ErrInvalidPath, "pack contains undeclared entry %q", hdr.Name)
		}
		if seen[hdr.Name] {
			return protocol.Errorf(protocol.ErrDuplicatePath, "pack contains %q twice", hdr.Name)
		}
		seen[hdr.Name] = true
		if hdr.Size != entry.Size {
			return protocol.Errorf(protocol.ErrHashMismatch, "packed size for %q disagrees with the manifest", hdr.Name)
		}
		target, err := platform.SafeJoin(payloadDir, hdr.Name)
		if err != nil {
			return protocol.Errorf(protocol.ErrInvalidPath, "unsafe path %q", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return protocol.Errorf(protocol.ErrInternal, "create directory: %v", err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
		if err != nil {
			return protocol.Errorf(protocol.ErrInternal, "create %q: %v", hdr.Name, err)
		}
		// LimitReader guards against a lying header inflating the payload.
		n, err := io.CopyBuffer(out, io.LimitReader(tr, entry.Size+1), buf)
		if err != nil {
			out.Close()
			return protocol.Errorf(protocol.ErrInternal, "write %q: %v", hdr.Name, err)
		}
		if err := out.Close(); err != nil {
			return protocol.Errorf(protocol.ErrInternal, "close %q: %v", hdr.Name, err)
		}
		if n != entry.Size {
			return protocol.Errorf(protocol.ErrHashMismatch, "packed bytes for %q disagree with the manifest", hdr.Name)
		}
	}
	if len(seen) != len(allowed) {
		return protocol.Errorf(protocol.ErrHashMismatch, "pack is missing declared entries")
	}
	return nil
}

// verify re-hashes every materialized file with bounded concurrency.
func verify(ctx context.Context, payloadDir string, m protocol.Manifest, workers int) *protocol.Error {
	type job struct{ entry protocol.Entry }
	jobs := make(chan job)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		failure *protocol.Error
	)
	fail := func(e *protocol.Error) {
		mu.Lock()
		if failure == nil {
			failure = e
		}
		mu.Unlock()
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					fail(protocol.Errorf(protocol.ErrInternal, "verification cancelled"))
					continue
				}
				target, err := platform.SafeJoin(payloadDir, j.entry.Path)
				if err != nil {
					fail(protocol.Errorf(protocol.ErrInvalidPath, "unsafe path %q", j.entry.Path))
					continue
				}
				info, err := os.Lstat(target)
				if err != nil {
					fail(protocol.Errorf(protocol.ErrHashMismatch, "missing materialized file %q", j.entry.Path))
					continue
				}
				if !info.Mode().IsRegular() {
					fail(protocol.Errorf(protocol.ErrUnsupportedEntry, "materialized %q is not a regular file", j.entry.Path))
					continue
				}
				if info.Size() != j.entry.Size {
					fail(protocol.Errorf(protocol.ErrHashMismatch, "size mismatch for %q", j.entry.Path))
					continue
				}
				digest, _, err := integrity.HashFile(target)
				if err != nil {
					fail(protocol.Errorf(protocol.ErrInternal, "hash %q: %v", j.entry.Path, err))
					continue
				}
				if digest != j.entry.Digest {
					fail(protocol.Errorf(protocol.ErrHashMismatch, "digest mismatch for %q", j.entry.Path))
				}
			}
		}()
	}
	for _, e := range m.Entries {
		if e.Type == protocol.EntryFile {
			jobs <- job{entry: e}
		}
	}
	close(jobs)
	wg.Wait()
	return failure
}
