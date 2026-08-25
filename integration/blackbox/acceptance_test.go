package blackbox

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sss/sss/internal/protocol"
)

var codePattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)

// A1/A2: authentication is required and failures are stable.
func TestAuthentication(t *testing.T) {
	s := startServer(t, options{})

	t.Run("missing credential", func(t *testing.T) {
		resp, err := http.Get(s.baseURL + "/v1/info")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if resp.Header.Get("WWW-Authenticate") == "" {
			t.Error("missing WWW-Authenticate header")
		}
		if got := decodeError(t, resp); got.Code != protocol.ErrAuthRequired {
			t.Errorf("code = %s, want %s", got.Code, protocol.ErrAuthRequired)
		}
	})

	t.Run("wrong credential", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, s.baseURL+"/v1/info", nil)
		req.SetBasicAuth("sss", "not-the-password")
		resp := s.do(req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if got := decodeError(t, resp); got.Code != protocol.ErrAuthInvalid {
			t.Errorf("code = %s, want %s", got.Code, protocol.ErrAuthInvalid)
		}
	})

	t.Run("healthz needs no credential", func(t *testing.T) {
		resp, err := http.Get(s.baseURL + "/healthz")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}

// B1: a single file round-trips byte for byte and stays available.
func TestSimpleSingleFile(t *testing.T) {
	s := startServer(t, options{})
	content := []byte("alpha\n")
	code := s.sendFile("alpha.txt", content, "", "")
	if !codePattern.MatchString(code) {
		t.Fatalf("code %q does not match the documented form", code)
	}

	status, body, headers := s.receive(code, "")
	if status != http.StatusOK {
		t.Fatalf("receive status = %d", status)
	}
	if !bytes.Equal(body, content) {
		t.Errorf("body = %q, want %q", body, content)
	}
	if !strings.Contains(headers.Get("Content-Disposition"), "alpha.txt") {
		t.Errorf("Content-Disposition = %q", headers.Get("Content-Disposition"))
	}

	// A successful receive does not consume the handoff.
	status2, body2, _ := s.receive(code, "")
	if status2 != http.StatusOK || !bytes.Equal(body2, content) {
		t.Errorf("second receive failed: status %d", status2)
	}

	resp := s.do(s.request(http.MethodGet, "/v1/transfers/"+code, nil))
	defer resp.Body.Close()
	var meta protocol.TransferMetadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if meta.FileCount != 1 || meta.TotalBytes != int64(len(content)) {
		t.Errorf("metadata = %+v", meta)
	}
	if len(meta.Entries) != 1 || meta.Entries[0].Path != "alpha.txt" {
		t.Errorf("entries = %+v", meta.Entries)
	}
}

// B2: several files arrive as a ZIP and no synthetic note file is added.
func TestSimpleMultipleFiles(t *testing.T) {
	s := startServer(t, options{})
	files := map[string][]byte{
		"alpha.txt": []byte("alpha\n"),
		"beta.csv":  []byte("a,b,c\n1,2,3\n"),
	}
	resp := s.uploadFiles(files, "look at these", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body %s", resp.StatusCode, body)
	}
	code := strings.TrimSpace(string(body))

	status, archiveBytes, headers := s.receive(code, "")
	if status != http.StatusOK {
		t.Fatalf("receive status = %d", status)
	}
	if ct := headers.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content type = %q, want application/zip", ct)
	}
	zr, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	got := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = data
	}
	if len(got) != len(files) {
		t.Fatalf("zip holds %d entries, want %d: %v", len(got), len(files), keys(got))
	}
	for name, want := range files {
		if !bytes.Equal(got[name], want) {
			t.Errorf("%s = %q, want %q", name, got[name], want)
		}
	}
}

// B3: a raw stream keeps its declared filename and bytes.
func TestSimpleRawStream(t *testing.T) {
	s := startServer(t, options{})
	payload := bytes.Repeat([]byte("raw-stream-"), 4096)
	req := s.request(http.MethodPost, "/s/raw", bytes.NewReader(payload))
	req.Header.Set("X-SSS-Filename", "project.tar.gz")
	req.Header.Set("X-SSS-Note", "inspect this project")
	req.Header.Set("X-SSS-TTL", "120")
	resp := s.do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body %s", resp.StatusCode, body)
	}
	code := strings.TrimSpace(string(body))

	status, got, headers := s.receive(code, "")
	if status != http.StatusOK {
		t.Fatalf("receive status = %d", status)
	}
	if !bytes.Equal(got, payload) {
		t.Error("raw stream bytes differ")
	}
	if !strings.Contains(headers.Get("Content-Disposition"), "project.tar.gz") {
		t.Errorf("Content-Disposition = %q", headers.Get("Content-Disposition"))
	}
}

// B4/B5: notes round-trip exactly and TTL bounds are enforced.
func TestNotesAndTTLBounds(t *testing.T) {
	s := startServer(t, options{})

	note := "Review the results and return a verdict"
	code := s.sendFile("alpha.txt", []byte("alpha\n"), note, "120")
	resp := s.do(s.request(http.MethodGet, "/v1/transfers/"+code, nil))
	var meta protocol.TransferMetadata
	_ = json.NewDecoder(resp.Body).Decode(&meta)
	resp.Body.Close()
	if meta.Note != note {
		t.Errorf("note = %q, want %q", meta.Note, note)
	}
	if got := meta.ExpiresAt.Sub(meta.CommittedAt); got != 120*time.Minute {
		t.Errorf("expiry window = %s, want 2h from commit", got)
	}
	// The note is metadata, not a file injected into the payload.
	if meta.FileCount != 1 {
		t.Errorf("file count = %d, want 1", meta.FileCount)
	}

	t.Run("default ttl is 30 minutes", func(t *testing.T) {
		code := s.sendFile("d.txt", []byte("d"), "", "")
		resp := s.do(s.request(http.MethodGet, "/v1/transfers/"+code, nil))
		var meta protocol.TransferMetadata
		_ = json.NewDecoder(resp.Body).Decode(&meta)
		resp.Body.Close()
		if got := meta.ExpiresAt.Sub(meta.CommittedAt); got != 30*time.Minute {
			t.Errorf("default window = %s, want 30m", got)
		}
	})

	t.Run("maximum ttl is accepted", func(t *testing.T) {
		resp := s.uploadFiles(map[string][]byte{"m.txt": []byte("m")}, "", "3000")
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("ttl=3000 status = %d, want 201", resp.StatusCode)
		}
	})

	t.Run("beyond maximum ttl is rejected", func(t *testing.T) {
		resp := s.uploadFiles(map[string][]byte{"m.txt": []byte("m")}, "", "3001")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", resp.StatusCode)
		}
		if got := decodeError(t, resp); got.Code != protocol.ErrTTLOutOfRange {
			t.Errorf("code = %s, want %s", got.Code, protocol.ErrTTLOutOfRange)
		}
	})
}

// C: every documented spelling of a code resolves; invalid ones do not.
func TestCodeNormalization(t *testing.T) {
	s := startServer(t, options{})
	content := []byte("normalize\n")
	code := s.sendFile("alpha.txt", content, "", "")
	bare := strings.ReplaceAll(code, "-", "")

	for _, variant := range []string{
		code,
		strings.ToLower(code),
		bare,
		strings.ToLower(bare),
	} {
		status, body, _ := s.receive(variant, "")
		if status != http.StatusOK || !bytes.Equal(body, content) {
			t.Errorf("variant %q: status %d", variant, status)
		}
	}

	t.Run("invalid alphabet", func(t *testing.T) {
		resp := s.do(s.request(http.MethodGet, "/r/AAAA-BBBI", nil))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		if got := decodeError(t, resp); got.Code != protocol.ErrInvalidCode {
			t.Errorf("code = %s, want %s", got.Code, protocol.ErrInvalidCode)
		}
	})

	t.Run("unknown code", func(t *testing.T) {
		resp := s.do(s.request(http.MethodGet, "/r/AAAA-BBBB", nil))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
		if got := decodeError(t, resp); got.Code != protocol.ErrTransferNotFound {
			t.Errorf("code = %s, want %s", got.Code, protocol.ErrTransferNotFound)
		}
	})
}

// The documented explicit formats behave as specified.
func TestDownloadFormats(t *testing.T) {
	s := startServer(t, options{})
	files := map[string][]byte{"one.txt": []byte("one\n"), "two.txt": []byte("two\n")}
	resp := s.uploadFiles(files, "", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	code := strings.TrimSpace(string(body))

	t.Run("tar", func(t *testing.T) {
		status, archiveBytes, headers := s.receive(code, "tar")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		if ct := headers.Get("Content-Type"); ct != "application/x-tar" {
			t.Errorf("content type = %q", ct)
		}
		tr := tar.NewReader(bytes.NewReader(archiveBytes))
		seen := 0
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("read tar: %v", err)
			}
			want, ok := files[hdr.Name]
			if !ok {
				t.Fatalf("unexpected tar entry %q", hdr.Name)
			}
			got, _ := io.ReadAll(tr)
			if !bytes.Equal(got, want) {
				t.Errorf("%s bytes differ", hdr.Name)
			}
			seen++
		}
		if seen != len(files) {
			t.Errorf("tar held %d entries, want %d", seen, len(files))
		}
	})

	t.Run("raw is rejected for multiple files", func(t *testing.T) {
		resp := s.do(s.request(http.MethodGet, "/r/"+code+"?format=raw", nil))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("unsupported format", func(t *testing.T) {
		resp := s.do(s.request(http.MethodGet, "/r/"+code+"?format=7z", nil))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})
}

// E: several receivers may hold the same code at once.
func TestMultipleConcurrentReceivers(t *testing.T) {
	s := startServer(t, options{})
	content := bytes.Repeat([]byte("concurrent"), 5000)
	code := s.sendFile("shared.bin", content, "", "")

	const receivers = 4
	var wg sync.WaitGroup
	errs := make(chan string, receivers)
	for i := 0; i < receivers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, body, _ := s.receive(code, "")
			if status != http.StatusOK {
				errs <- "status " + http.StatusText(status)
				return
			}
			if !bytes.Equal(body, content) {
				errs <- "bytes differ"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}

	// One completed receive must not delete the transfer.
	if status, _, _ := s.receive(code, ""); status != http.StatusOK {
		t.Errorf("transfer disappeared after concurrent receives: status %d", status)
	}
}

// The simple endpoints reject an empty send.
func TestSendWithoutFiles(t *testing.T) {
	s := startServer(t, options{})
	resp := s.uploadFiles(nil, "note only", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if got := decodeError(t, resp); got.Code != protocol.ErrNoFiles {
		t.Errorf("code = %s, want %s", got.Code, protocol.ErrNoFiles)
	}
}

// Limits produce the documented stable errors.
func TestLimits(t *testing.T) {
	t.Run("file count", func(t *testing.T) {
		s := startServer(t, options{maxFiles: 1})
		resp := s.uploadFiles(map[string][]byte{"a.txt": []byte("a"), "b.txt": []byte("b")}, "", "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", resp.StatusCode)
		}
		if got := decodeError(t, resp); got.Code != protocol.ErrTooManyFiles {
			t.Errorf("code = %s, want %s", got.Code, protocol.ErrTooManyFiles)
		}
	})

	t.Run("transfer size", func(t *testing.T) {
		s := startServer(t, options{maxTransferBytes: 16})
		resp := s.uploadFiles(map[string][]byte{"big.bin": bytes.Repeat([]byte("x"), 1024)}, "", "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", resp.StatusCode)
		}
		if got := decodeError(t, resp); got.Code != protocol.ErrPayloadTooLarge {
			t.Errorf("code = %s, want %s", got.Code, protocol.ErrPayloadTooLarge)
		}
	})
}

// D1: an interrupted upload never produces a code or leaves published state.
func TestInterruptedUploadPublishesNothing(t *testing.T) {
	s := startServer(t, options{})
	before := s.statusSnapshot(t)

	// A multipart body that ends mid-part is exactly what a killed client
	// produces on the wire.
	broken := "--boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"partial.bin\"\r\n\r\nhalf-written"
	req := s.request(http.MethodPost, "/s", strings.NewReader(broken))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	resp := s.do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("truncated upload returned %d and body %q; no code may be issued",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if codePattern.MatchString(strings.TrimSpace(string(body))) {
		t.Fatal("a code was issued for an incomplete upload")
	}

	after := s.statusSnapshot(t)
	if after.Committed != before.Committed {
		t.Errorf("committed transfers changed from %d to %d", before.Committed, after.Committed)
	}
}

func (s *server) statusSnapshot(t *testing.T) protocol.AdminStatus {
	t.Helper()
	status, body := s.localGet("/local/status", "application/json")
	if status != http.StatusOK {
		t.Fatalf("local status = %d", status)
	}
	var out protocol.AdminStatus
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return out
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
