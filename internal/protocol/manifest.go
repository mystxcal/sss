package protocol

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Segment kinds carried on the wire.
const (
	SegmentRaw     = "raw"
	SegmentTarZstd = "tar_zstd"
)

// DigestAlgorithm is the only digest algorithm supported in v1.
const DigestAlgorithm = "blake3"

// Entry types in a manifest.
const (
	EntryFile      = "file"
	EntryDirectory = "directory"
)

// Segment describes one unit of transferable bytes.
type Segment struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	WireSize int64  `json:"wire_size"`
	Digest   string `json:"digest"`
	// StoragePath is the server-side location relative to the transfer root.
	// It is filled in on the published manifest and never accepted from a client.
	StoragePath string `json:"storage_path,omitempty"`
}

// Entry describes one file or directory in the payload.
type Entry struct {
	Path        string `json:"path"`
	Type        string `json:"type"`
	Size        int64  `json:"size,omitempty"`
	MTimeUnixNS int64  `json:"mtime_unix_ns"`
	Mode        uint32 `json:"mode"`
	Digest      string `json:"digest,omitempty"`
	SegmentID   string `json:"segment_id,omitempty"`
	// SegmentOffset locates a packed file inside a tar_zstd segment. Raw
	// segments always carry the whole file and leave this at zero.
	SegmentOffset int64 `json:"segment_offset,omitempty"`
}

// Manifest is the complete portable description of a handoff.
type Manifest struct {
	SchemaVersion   int       `json:"schema_version"`
	TransferID      string    `json:"transfer_id"`
	CreatedAt       time.Time `json:"created_at"`
	Note            string    `json:"note,omitempty"`
	Roots           []string  `json:"roots"`
	DigestAlgorithm string    `json:"digest_algorithm,omitempty"`
	Segments        []Segment `json:"segments"`
	Entries         []Entry   `json:"entries"`
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MaxNoteBytesHard is the manifest schema ceiling for a note.
const MaxNoteBytesHard = 16384

// FileCount returns the number of regular file entries.
func (m *Manifest) FileCount() int {
	n := 0
	for _, e := range m.Entries {
		if e.Type == EntryFile {
			n++
		}
	}
	return n
}

// TotalBytes returns the materialized size of all file entries.
func (m *Manifest) TotalBytes() int64 {
	var n int64
	for _, e := range m.Entries {
		if e.Type == EntryFile {
			n += e.Size
		}
	}
	return n
}

// SingleRootFile reports the entry when the manifest holds exactly one regular
// file at the payload root, which is what enables raw simple downloads.
func (m *Manifest) SingleRootFile() (Entry, bool) {
	var found Entry
	count := 0
	for _, e := range m.Entries {
		if e.Type == EntryDirectory {
			return Entry{}, false
		}
		if strings.Contains(e.Path, "/") {
			return Entry{}, false
		}
		found = e
		count++
	}
	return found, count == 1
}

// SegmentByID returns the segment with the given identifier.
func (m *Manifest) SegmentByID(id string) (Segment, bool) {
	for _, s := range m.Segments {
		if s.ID == id {
			return s, true
		}
	}
	return Segment{}, false
}

// Validate enforces every structural and semantic manifest rule. All manifest
// input is treated as hostile even though the devices are trusted.
func (m *Manifest) Validate() *Error {
	if m.SchemaVersion != 1 {
		return Errorf(ErrInvalidRequest, "unsupported manifest schema_version %d", m.SchemaVersion)
	}
	if len(m.TransferID) < 16 || len(m.TransferID) > 128 {
		return Errorf(ErrInvalidRequest, "manifest transfer_id length out of range")
	}
	if m.CreatedAt.IsZero() {
		return Errorf(ErrInvalidRequest, "manifest created_at is required")
	}
	if m.DigestAlgorithm != "" && m.DigestAlgorithm != DigestAlgorithm {
		return Errorf(ErrInvalidRequest, "unsupported digest algorithm %q", m.DigestAlgorithm)
	}
	if len(m.Note) > MaxNoteBytesHard {
		return Errorf(ErrInvalidRequest, "note exceeds %d bytes", MaxNoteBytesHard)
	}
	if !utf8.ValidString(m.Note) {
		return Errorf(ErrInvalidRequest, "note must be valid UTF-8")
	}
	if len(m.Roots) == 0 {
		return Errorf(ErrNoFiles, "manifest declares no roots")
	}
	seenRoot := map[string]bool{}
	for _, r := range m.Roots {
		if r == "" || len(r) > 255 {
			return Errorf(ErrInvalidPath, "invalid root name")
		}
		if err := ValidateComponent(r); err != nil {
			return err
		}
		if seenRoot[strings.ToLower(r)] {
			return Errorf(ErrDuplicatePath, "duplicate root %q", r)
		}
		seenRoot[strings.ToLower(r)] = true
	}
	if len(m.Segments) == 0 {
		return Errorf(ErrInvalidRequest, "manifest declares no segments")
	}
	segIDs := map[string]bool{}
	for _, s := range m.Segments {
		if s.ID == "" || len(s.ID) > 128 {
			return Errorf(ErrInvalidRequest, "invalid segment id")
		}
		if segIDs[s.ID] {
			return Errorf(ErrInvalidRequest, "duplicate segment id %q", s.ID)
		}
		segIDs[s.ID] = true
		if s.Kind != SegmentRaw && s.Kind != SegmentTarZstd {
			return Errorf(ErrInvalidRequest, "unsupported segment kind %q", s.Kind)
		}
		if s.WireSize < 0 {
			return Errorf(ErrInvalidRequest, "negative segment size")
		}
		if !digestPattern.MatchString(s.Digest) {
			return Errorf(ErrInvalidRequest, "segment %q has malformed digest", s.ID)
		}
	}
	if len(m.Entries) == 0 {
		return Errorf(ErrNoFiles, "manifest declares no entries")
	}

	seenPath := map[string]bool{}
	rawSegmentUsed := map[string]bool{}
	for _, e := range m.Entries {
		if err := ValidatePortablePath(e.Path); err != nil {
			return err
		}
		key := normalizedPathKey(e.Path)
		if seenPath[key] {
			return Errorf(ErrDuplicatePath, "duplicate normalized path %q", e.Path)
		}
		seenPath[key] = true

		top := strings.SplitN(e.Path, "/", 2)[0]
		if !seenRoot[strings.ToLower(top)] {
			return Errorf(ErrInvalidPath, "entry %q is outside declared roots", e.Path)
		}
		if e.Mode > 0o777 {
			return Errorf(ErrInvalidRequest, "entry %q has invalid mode", e.Path)
		}
		if e.MTimeUnixNS < 0 {
			return Errorf(ErrInvalidRequest, "entry %q has invalid mtime", e.Path)
		}
		switch e.Type {
		case EntryDirectory:
			if e.Size != 0 || e.Digest != "" || e.SegmentID != "" {
				return Errorf(ErrInvalidRequest, "directory %q carries file fields", e.Path)
			}
		case EntryFile:
			if e.Size < 0 {
				return Errorf(ErrInvalidRequest, "entry %q has negative size", e.Path)
			}
			if !digestPattern.MatchString(e.Digest) {
				return Errorf(ErrInvalidRequest, "entry %q has malformed digest", e.Path)
			}
			if !segIDs[e.SegmentID] {
				return Errorf(ErrInvalidRequest, "entry %q references unknown segment", e.Path)
			}
			seg, _ := m.SegmentByID(e.SegmentID)
			if seg.Kind == SegmentRaw {
				if rawSegmentUsed[e.SegmentID] {
					return Errorf(ErrInvalidRequest, "raw segment %q is claimed by multiple entries", e.SegmentID)
				}
				rawSegmentUsed[e.SegmentID] = true
				if seg.WireSize != e.Size {
					return Errorf(ErrInvalidRequest, "raw segment %q size disagrees with entry %q", e.SegmentID, e.Path)
				}
				if seg.Digest != e.Digest {
					return Errorf(ErrInvalidRequest, "raw segment %q digest disagrees with entry %q", e.SegmentID, e.Path)
				}
			}
			if e.SegmentOffset < 0 {
				return Errorf(ErrInvalidRequest, "entry %q has negative segment offset", e.Path)
			}
		default:
			return Errorf(ErrUnsupportedEntry, "entry %q has unsupported type %q", e.Path, e.Type)
		}
	}
	// Every declared segment must be referenced, otherwise a client could park
	// unaccounted bytes inside a committed transfer.
	referenced := map[string]bool{}
	for _, e := range m.Entries {
		if e.SegmentID != "" {
			referenced[e.SegmentID] = true
		}
	}
	for _, s := range m.Segments {
		if !referenced[s.ID] {
			return Errorf(ErrInvalidRequest, "segment %q is not referenced by any entry", s.ID)
		}
	}
	return nil
}

// SortedEntries returns entries ordered by path, directories before their
// contents, which gives materialization and archive streaming a stable order.
func (m *Manifest) SortedEntries() []Entry {
	out := make([]Entry, len(m.Entries))
	copy(out, m.Entries)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func normalizedPathKey(p string) string {
	// Windows and macOS destinations are case-insensitive, so two entries that
	// differ only by case cannot both be materialized portably.
	return strings.ToLower(p)
}

// reservedWindowsNames cannot exist as a path component on Windows.
var reservedWindowsNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidatePortablePath enforces the portable relative-path rules used in
// manifests: slash separated, relative, no traversal, no empty components, no
// NUL, and no component that cannot exist on a supported platform.
func ValidatePortablePath(p string) *Error {
	if p == "" {
		return Errorf(ErrInvalidPath, "empty path")
	}
	if len(p) > 4096 {
		return Errorf(ErrInvalidPath, "path exceeds 4096 bytes")
	}
	if !utf8.ValidString(p) {
		return Errorf(ErrInvalidPath, "path is not valid UTF-8")
	}
	if strings.HasPrefix(p, "/") {
		return Errorf(ErrInvalidPath, "absolute path %q", p)
	}
	if strings.Contains(p, `\`) {
		return Errorf(ErrInvalidPath, "path %q contains a backslash", p)
	}
	// A drive-relative or UNC-style prefix must never reach a destination join.
	if len(p) >= 2 && p[1] == ':' {
		return Errorf(ErrInvalidPath, "path %q looks like a drive path", p)
	}
	for _, c := range strings.Split(p, "/") {
		if err := ValidateComponent(c); err != nil {
			return err
		}
	}
	return nil
}

// ValidateComponent enforces the rules for one path component.
func ValidateComponent(c string) *Error {
	if c == "" {
		return Errorf(ErrInvalidPath, "empty path component")
	}
	if c == "." || c == ".." {
		return Errorf(ErrInvalidPath, "path component %q is not allowed", c)
	}
	if len(c) > 255 {
		return Errorf(ErrInvalidPath, "path component exceeds 255 bytes")
	}
	for _, r := range c {
		if r == 0 {
			return Errorf(ErrInvalidPath, "path contains NUL")
		}
		if r < 0x20 || r == 0x7f {
			return Errorf(ErrInvalidPath, "path contains a control character")
		}
		if strings.ContainsRune(`<>:"|?*\/`, r) {
			return Errorf(ErrInvalidPath, "path component contains reserved character %q", string(r))
		}
		if unicode.Is(unicode.Cf, r) {
			return Errorf(ErrInvalidPath, "path component contains a format control character")
		}
	}
	if strings.HasSuffix(c, ".") || strings.HasSuffix(c, " ") {
		return Errorf(ErrInvalidPath, "path component %q ends with a dot or space", c)
	}
	base := strings.ToLower(c)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if reservedWindowsNames[base] {
		return Errorf(ErrInvalidPath, "path component %q is a reserved name", c)
	}
	return nil
}
