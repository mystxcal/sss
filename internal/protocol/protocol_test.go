package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeCode(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		valid bool
	}{
		{"K7M4-Q2PX", "K7M4Q2PX", true},
		{"k7m4-q2px", "K7M4Q2PX", true},
		{"K7M4Q2PX", "K7M4Q2PX", true},
		{"k7m4q2px", "K7M4Q2PX", true},
		{" K7M4 - Q2PX \n", "K7M4Q2PX", true},
		{"K7M4-Q2P", "", false},   // too short
		{"K7M4-Q2PXX", "", false}, // too long
		{"K7M4-Q2PI", "", false},  // I is not in the alphabet
		{"K7M4-Q2PL", "", false},  // L is not in the alphabet
		{"K7M4-Q2PO", "", false},  // O is not in the alphabet
		{"K7M4-Q2PU", "", false},  // U is not in the alphabet
		{"K7M4-Q2P!", "", false},  // punctuation
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeCode(tc.in)
		if ok != tc.valid {
			t.Errorf("NormalizeCode(%q) valid = %v, want %v", tc.in, ok, tc.valid)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewCodeShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		code := NewCode()
		if len(code) != 9 || code[4] != '-' {
			t.Fatalf("code %q is not XXXX-XXXX", code)
		}
		canonical, ok := NormalizeCode(code)
		if !ok {
			t.Fatalf("generated code %q does not normalize", code)
		}
		for _, r := range canonical {
			if !strings.ContainsRune(Alphabet, r) {
				t.Fatalf("code %q contains %q which is outside the alphabet", code, string(r))
			}
		}
		if seen[canonical] {
			t.Fatalf("duplicate code %q in a small sample", canonical)
		}
		seen[canonical] = true
	}
}

func TestParseTTL(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr string
	}{
		{"", DefaultTTLMinutes, ""},
		{"30m", 30, ""},
		{"2h", 120, ""},
		{"3000m", 3000, ""},
		{"120", 120, ""},
		{"3001", 0, ErrTTLOutOfRange},
		{"3001m", 0, ErrTTLOutOfRange},
		{"0", 0, ErrTTLOutOfRange},
		{"0m", 0, ErrTTLOutOfRange},
		{"90s", 0, ErrInvalidRequest},
		{"nonsense", 0, ErrInvalidRequest},
	}
	for _, tc := range cases {
		got, err := ParseTTL(tc.in)
		switch {
		case tc.wantErr == "":
			if err != nil {
				t.Errorf("ParseTTL(%q) error = %v, want success", tc.in, err)
				continue
			}
			if got != tc.want {
				t.Errorf("ParseTTL(%q) = %d, want %d", tc.in, got, tc.want)
			}
		default:
			if err == nil {
				t.Errorf("ParseTTL(%q) = %d, want error %s", tc.in, got, tc.wantErr)
				continue
			}
			if err.Code != tc.wantErr {
				t.Errorf("ParseTTL(%q) code = %s, want %s", tc.in, err.Code, tc.wantErr)
			}
		}
	}
}

func TestValidatePortablePath(t *testing.T) {
	valid := []string{
		"alpha.txt",
		"project/readme.md",
		"deeply/nested/path/file.bin",
		"with space/ünïcode.txt",
		"dots.in.name.tar.gz",
	}
	for _, p := range valid {
		if err := ValidatePortablePath(p); err != nil {
			t.Errorf("ValidatePortablePath(%q) = %v, want ok", p, err)
		}
	}

	invalid := map[string]string{
		"/absolute":           ErrInvalidPath,
		"../escape":           ErrInvalidPath,
		"nested/../../escape": ErrInvalidPath,
		"trailing/":           ErrInvalidPath,
		"double//slash":       ErrInvalidPath,
		`back\slash`:          ErrInvalidPath,
		"C:/drive":            ErrInvalidPath,
		"con":                 ErrInvalidPath,
		"COM1.txt":            ErrInvalidPath,
		"ends.with.dot.":      ErrInvalidPath,
		"ends with space ":    ErrInvalidPath,
		"pipe|char":           ErrInvalidPath,
		"question?mark":       ErrInvalidPath,
		"colon:name":          ErrInvalidPath,
		"":                    ErrInvalidPath,
		"control\x01char":     ErrInvalidPath,
	}
	for p, want := range invalid {
		err := ValidatePortablePath(p)
		if err == nil {
			t.Errorf("ValidatePortablePath(%q) = ok, want %s", p, want)
			continue
		}
		if err.Code != want {
			t.Errorf("ValidatePortablePath(%q) code = %s, want %s", p, err.Code, want)
		}
	}
}

func validManifest() Manifest {
	digest := strings.Repeat("ab", 32)
	return Manifest{
		SchemaVersion:   1,
		TransferID:      "t-0123456789abcdef0123456789abcdef",
		CreatedAt:       time.Now().UTC(),
		Roots:           []string{"project"},
		DigestAlgorithm: DigestAlgorithm,
		Segments: []Segment{
			{ID: "r-0", Kind: SegmentRaw, WireSize: 10, Digest: digest},
		},
		Entries: []Entry{
			{Path: "project", Type: EntryDirectory, MTimeUnixNS: 1, Mode: 0o755},
			{Path: "project/a.txt", Type: EntryFile, Size: 10, MTimeUnixNS: 1, Mode: 0o644, Digest: digest, SegmentID: "r-0"},
		},
	}
}

func TestManifestValidate(t *testing.T) {
	base := validManifest()
	if err := base.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"bad schema version", func(m *Manifest) { m.SchemaVersion = 2 }, ErrInvalidRequest},
		{"traversal path", func(m *Manifest) { m.Entries[1].Path = "../escape" }, ErrInvalidPath},
		{"entry outside roots", func(m *Manifest) { m.Entries[1].Path = "elsewhere/a.txt" }, ErrInvalidPath},
		{"duplicate path", func(m *Manifest) { m.Entries = append(m.Entries, m.Entries[1]) }, ErrDuplicatePath},
		{"case-only duplicate", func(m *Manifest) {
			dup := m.Entries[1]
			dup.Path = "project/A.TXT"
			m.Entries = append(m.Entries, dup)
		}, ErrDuplicatePath},
		{"unknown segment", func(m *Manifest) { m.Entries[1].SegmentID = "missing" }, ErrInvalidRequest},
		{"unreferenced segment", func(m *Manifest) {
			m.Segments = append(m.Segments, Segment{ID: "r-1", Kind: SegmentRaw, WireSize: 1, Digest: strings.Repeat("cd", 32)})
		}, ErrInvalidRequest},
		{"malformed digest", func(m *Manifest) { m.Entries[1].Digest = "short" }, ErrInvalidRequest},
		{"raw segment shared by two entries", func(m *Manifest) {
			extra := m.Entries[1]
			extra.Path = "project/b.txt"
			m.Entries = append(m.Entries, extra)
		}, ErrInvalidRequest},
		{"raw size disagreement", func(m *Manifest) { m.Entries[1].Size = 11 }, ErrInvalidRequest},
		{"unsupported entry type", func(m *Manifest) { m.Entries[1].Type = "symlink" }, ErrUnsupportedEntry},
		{"unsupported digest algorithm", func(m *Manifest) { m.DigestAlgorithm = "sha256" }, ErrInvalidRequest},
		{"no entries", func(m *Manifest) { m.Entries = nil }, ErrNoFiles},
		{"no segments", func(m *Manifest) { m.Segments = nil }, ErrInvalidRequest},
		{"mode out of range", func(m *Manifest) { m.Entries[1].Mode = 0o7777 }, ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			tc.mutate(&m)
			err := m.Validate()
			if err == nil {
				t.Fatalf("mutation accepted, want %s", tc.want)
			}
			if err.Code != tc.want {
				t.Errorf("code = %s, want %s", err.Code, tc.want)
			}
		})
	}
}

func TestSingleRootFile(t *testing.T) {
	m := validManifest()
	if _, ok := m.SingleRootFile(); ok {
		t.Error("a tree with a directory is not a single root file")
	}

	flat := Manifest{Entries: []Entry{{Path: "alpha.txt", Type: EntryFile, Size: 3}}}
	entry, ok := flat.SingleRootFile()
	if !ok || entry.Path != "alpha.txt" {
		t.Errorf("SingleRootFile = %+v, %v", entry, ok)
	}

	two := Manifest{Entries: []Entry{
		{Path: "a.txt", Type: EntryFile}, {Path: "b.txt", Type: EntryFile},
	}}
	if _, ok := two.SingleRootFile(); ok {
		t.Error("two files must not be served raw")
	}
}

func TestErrorCatalogMappings(t *testing.T) {
	// The catalog is a published contract: statuses and exit codes are stable.
	cases := map[string]struct {
		status int
		exit   int
	}{
		ErrAuthRequired:        {401, 3},
		ErrAuthInvalid:         {401, 3},
		ErrRateLimited:         {429, 5},
		ErrInvalidRequest:      {400, 2},
		ErrInvalidCode:         {400, 2},
		ErrTransferNotFound:    {404, 4},
		ErrTransferExpired:     {410, 4},
		ErrTTLOutOfRange:       {422, 2},
		ErrNoFiles:             {422, 2},
		ErrUnsupportedEntry:    {422, 7},
		ErrInvalidPath:         {422, 7},
		ErrDuplicatePath:       {422, 7},
		ErrSourceChanged:       {409, 6},
		ErrOffsetMismatch:      {409, 6},
		ErrStateConflict:       {409, 6},
		ErrIdempotencyConflict: {409, 6},
		ErrPayloadTooLarge:     {413, 7},
		ErrTooManyFiles:        {413, 7},
		ErrInsufficientStorage: {507, 7},
		ErrHashMismatch:        {422, 6},
		ErrClaimExpired:        {410, 4},
		ErrProtocolMismatch:    {426, 5},
		ErrInternal:            {500, 8},
	}
	for code, want := range cases {
		if got := HTTPStatus(code); got != want.status {
			t.Errorf("%s status = %d, want %d", code, got, want.status)
		}
		if got := ExitCode(code); got != want.exit {
			t.Errorf("%s exit = %d, want %d", code, got, want.exit)
		}
	}
	if got := ExitCode(ErrDestinationExists); got != 7 {
		t.Errorf("DESTINATION_EXISTS exit = %d, want 7", got)
	}
	if got := ExitCode(ErrNetwork); got != 5 {
		t.Errorf("NETWORK_ERROR exit = %d, want 5", got)
	}
}

func TestCheckCompatible(t *testing.T) {
	if err := CheckCompatible("1.0"); err != nil {
		t.Errorf("1.0 rejected: %v", err)
	}
	if err := CheckCompatible("1.7"); err != nil {
		t.Errorf("minor additions must stay compatible: %v", err)
	}
	if err := CheckCompatible("2.0"); err == nil {
		t.Error("major version 2 must be rejected")
	} else if err.Code != ErrProtocolMismatch {
		t.Errorf("code = %s, want %s", err.Code, ErrProtocolMismatch)
	}
}
