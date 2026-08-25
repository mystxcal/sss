// Package pack plans how a local file tree becomes wire segments: large files
// travel as independent raw segments, small files are grouped into bounded
// tar.zst packs so a huge tree does not become one request per file.
package pack

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/protocol"
)

// Internal tuning defaults. These are implementation details, not user knobs:
// changing them does not change the protocol contract.
const (
	// RawThreshold is the size at or above which a file gets its own segment.
	RawThreshold = 1 << 20
	// PackTarget is the uncompressed size a pack aims for.
	PackTarget = 64 << 20
	// UploadConcurrency bounds simultaneous segment transfers.
	UploadConcurrency = 4
	// DownloadConcurrency bounds simultaneous segment fetches. Segments are
	// immutable and independently verified, so receiving them in parallel costs
	// nothing in correctness and keeps a receiver from being limited to what one
	// TCP flow can carry.
	DownloadConcurrency = 4
)

// Source describes one local file that will be sent.
type Source struct {
	AbsPath string
	RelPath string // portable, slash separated
	Size    int64
	MTimeNS int64
	Mode    uint32
	Digest  string
}

// PackSpec groups small files into one tar.zst segment.
type PackSpec struct {
	SegmentID string
	Files     []Source
	RawBytes  int64
}

// Plan is the complete send plan for a set of roots.
type Plan struct {
	Roots      []string
	Entries    []protocol.Entry
	RawFiles   map[string]Source // segment id -> source file
	Packs      []PackSpec
	TotalBytes int64
	FileCount  int
	DirCount   int
}

// Scan walks the requested roots, rejecting anything not portable in v1, and
// produces a deterministic plan. Every file is hashed once here; those digests
// are reused for the manifest and for verification on both sides.
func Scan(roots []string) (*Plan, *protocol.Error) {
	if len(roots) == 0 {
		return nil, protocol.Errorf(protocol.ErrNoFiles, "no files or directories were given")
	}
	plan := &Plan{RawFiles: map[string]Source{}}
	seenRoots := map[string]bool{}
	var sources []Source

	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, protocol.Errorf(protocol.ErrInvalidPath, "cannot resolve %q", root)
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, protocol.Errorf(protocol.ErrInvalidPath, "cannot read %q: %v", root, err)
		}
		name := filepath.Base(abs)
		if perr := protocol.ValidateComponent(name); perr != nil {
			return nil, perr
		}
		if seenRoots[strings.ToLower(name)] {
			return nil, protocol.Errorf(protocol.ErrDuplicatePath,
				"two roots would both be named %q; rename or send them separately", name)
		}
		seenRoots[strings.ToLower(name)] = true
		plan.Roots = append(plan.Roots, name)

		switch {
		case info.Mode().IsRegular():
			src, perr := readSource(abs, name, info)
			if perr != nil {
				return nil, perr
			}
			sources = append(sources, src)
		case info.IsDir():
			collected, dirs, perr := walkDir(abs, name)
			if perr != nil {
				return nil, perr
			}
			sources = append(sources, collected...)
			plan.Entries = append(plan.Entries, dirs...)
		default:
			return nil, protocol.Errorf(protocol.ErrUnsupportedEntry,
				"%q is not a regular file or directory (symlinks, devices, sockets, and FIFOs are rejected in v1)", root)
		}
	}

	if len(sources) == 0 && len(plan.Entries) == 0 {
		return nil, protocol.Errorf(protocol.ErrNoFiles, "nothing to send")
	}

	// Deterministic ordering keeps packs and resume fingerprints stable.
	sort.Slice(sources, func(i, j int) bool { return sources[i].RelPath < sources[j].RelPath })

	seenPaths := map[string]bool{}
	for _, e := range plan.Entries {
		seenPaths[strings.ToLower(e.Path)] = true
	}

	var currentPack PackSpec
	packIndex := 0
	flushPack := func() {
		if len(currentPack.Files) == 0 {
			return
		}
		currentPack.SegmentID = fmt.Sprintf("p-%04d", packIndex)
		packIndex++
		for i := range currentPack.Files {
			// Entries are appended once the pack has an ID.
			plan.Entries = append(plan.Entries, protocol.Entry{
				Path:        currentPack.Files[i].RelPath,
				Type:        protocol.EntryFile,
				Size:        currentPack.Files[i].Size,
				MTimeUnixNS: currentPack.Files[i].MTimeNS,
				Mode:        currentPack.Files[i].Mode,
				Digest:      currentPack.Files[i].Digest,
				SegmentID:   currentPack.SegmentID,
			})
		}
		plan.Packs = append(plan.Packs, currentPack)
		currentPack = PackSpec{}
	}

	rawIndex := 0
	for _, src := range sources {
		key := strings.ToLower(src.RelPath)
		if seenPaths[key] {
			return nil, protocol.Errorf(protocol.ErrDuplicatePath, "two entries normalize to %q", src.RelPath)
		}
		seenPaths[key] = true
		plan.TotalBytes += src.Size
		plan.FileCount++

		if src.Size >= RawThreshold {
			segID := fmt.Sprintf("r-%04d", rawIndex)
			rawIndex++
			plan.RawFiles[segID] = src
			plan.Entries = append(plan.Entries, protocol.Entry{
				Path:        src.RelPath,
				Type:        protocol.EntryFile,
				Size:        src.Size,
				MTimeUnixNS: src.MTimeNS,
				Mode:        src.Mode,
				Digest:      src.Digest,
				SegmentID:   segID,
			})
			continue
		}
		currentPack.Files = append(currentPack.Files, src)
		currentPack.RawBytes += src.Size
		if currentPack.RawBytes >= PackTarget {
			flushPack()
		}
	}
	flushPack()

	for _, e := range plan.Entries {
		if e.Type == protocol.EntryDirectory {
			plan.DirCount++
		}
	}
	return plan, nil
}

// walkDir collects regular files and directories under a root, rejecting
// anything that is not portable.
func walkDir(absRoot, virtualName string) ([]Source, []protocol.Entry, *protocol.Error) {
	var sources []Source
	var dirs []protocol.Entry
	var failure *protocol.Error

	err := filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			failure = protocol.Errorf(protocol.ErrInvalidPath, "cannot read %q: %v", p, err)
			return failure
		}
		rel, rerr := filepath.Rel(absRoot, p)
		if rerr != nil {
			failure = protocol.Errorf(protocol.ErrInvalidPath, "cannot resolve %q", p)
			return failure
		}
		virtual := virtualName
		if rel != "." {
			virtual = virtualName + "/" + filepath.ToSlash(rel)
		}
		if perr := protocol.ValidatePortablePath(virtual); perr != nil {
			failure = perr
			return perr
		}
		info, ierr := d.Info()
		if ierr != nil {
			failure = protocol.Errorf(protocol.ErrInvalidPath, "cannot stat %q: %v", p, ierr)
			return failure
		}
		switch {
		case d.IsDir():
			dirs = append(dirs, protocol.Entry{
				Path:        virtual,
				Type:        protocol.EntryDirectory,
				MTimeUnixNS: info.ModTime().UnixNano(),
				Mode:        0o755,
			})
			return nil
		case info.Mode().IsRegular():
			src, perr := readSource(p, virtual, info)
			if perr != nil {
				failure = perr
				return perr
			}
			sources = append(sources, src)
			return nil
		default:
			failure = protocol.Errorf(protocol.ErrUnsupportedEntry,
				"%q is not a regular file (symlinks, devices, sockets, and FIFOs are rejected in v1)", p)
			return failure
		}
	})
	if failure != nil {
		return nil, nil, failure
	}
	if err != nil {
		return nil, nil, protocol.Errorf(protocol.ErrInvalidPath, "%v", err)
	}
	return sources, dirs, nil
}

func readSource(absPath, relPath string, info os.FileInfo) (Source, *protocol.Error) {
	digest, size, err := integrity.HashFile(absPath)
	if err != nil {
		return Source{}, protocol.Errorf(protocol.ErrInvalidPath, "cannot read %q: %v", absPath, err)
	}
	if size != info.Size() {
		return Source{}, protocol.Errorf(protocol.ErrSourceChanged, "%q changed while it was being read", absPath)
	}
	mode := uint32(0o644)
	if info.Mode().Perm()&0o111 != 0 {
		mode = 0o755
	}
	return Source{
		AbsPath: absPath,
		RelPath: relPath,
		Size:    size,
		MTimeNS: info.ModTime().UnixNano(),
		Mode:    mode,
		Digest:  digest,
	}, nil
}

// Fingerprint identifies a set of sources so a resumed run can prove that
// nothing changed. Any size, mtime, or digest change invalidates the session.
func (p *Plan) Fingerprint(note string, ttlMinutes int) string {
	h := integrity.New()
	fmt.Fprintf(h, "v1\n%s\n%d\n", note, ttlMinutes)
	for _, e := range p.SortedEntries() {
		fmt.Fprintf(h, "%s\t%s\t%d\t%d\t%o\t%s\n", e.Type, e.Path, e.Size, e.MTimeUnixNS, e.Mode, e.Digest)
	}
	return integrity.Sum(h)
}

// SortedEntries returns the plan entries in a stable order.
func (p *Plan) SortedEntries() []protocol.Entry {
	out := make([]protocol.Entry, len(p.Entries))
	copy(out, p.Entries)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// VerifyUnchanged re-checks every source's size and modification time. It is
// called before a resumed upload so a changed source fails loudly.
func (p *Plan) VerifyUnchanged() *protocol.Error {
	check := func(src Source) *protocol.Error {
		info, err := os.Lstat(src.AbsPath)
		if err != nil {
			return protocol.Errorf(protocol.ErrSourceChanged, "%q is no longer readable", src.AbsPath)
		}
		if !info.Mode().IsRegular() || info.Size() != src.Size || info.ModTime().UnixNano() != src.MTimeNS {
			return protocol.Errorf(protocol.ErrSourceChanged, "%q changed since the transfer began", src.AbsPath)
		}
		return nil
	}
	for _, src := range p.RawFiles {
		if perr := check(src); perr != nil {
			return perr
		}
	}
	for _, spec := range p.Packs {
		for _, src := range spec.Files {
			if perr := check(src); perr != nil {
				return perr
			}
		}
	}
	return nil
}
