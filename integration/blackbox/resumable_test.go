package blackbox

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/protocol"
)

// createTransfer opens an advanced transfer with one raw segment.
func (s *server) createRawTransfer(t *testing.T, id string, content []byte, ttl int) protocol.CreateTransferResponse {
	t.Helper()
	digest := integrity.HashBytes(content)
	req := protocol.CreateTransferRequest{
		TTLMinutes:                ttl,
		Note:                      "advanced send",
		ExpectedMaterializedBytes: int64(len(content)),
		ExpectedFileCount:         1,
		Segments: []protocol.SegmentPlan{{
			ID:              id,
			Kind:            protocol.SegmentRaw,
			ExpectedLength:  int64(len(content)),
			DigestAlgorithm: protocol.DigestAlgorithm,
			ExpectedDigest:  &digest,
		}},
	}
	body, _ := json.Marshal(req)
	httpReq := s.request(http.MethodPost, "/v1/transfers", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp := s.do(httpReq)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("create transfer status = %d body %s", resp.StatusCode, data)
	}
	var out protocol.CreateTransferResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return out
}

func (s *server) patch(t *testing.T, uploadPath string, offset int64, chunk []byte) *http.Response {
	t.Helper()
	req := s.request(http.MethodPatch, uploadPath, bytes.NewReader(chunk))
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	return s.do(req)
}

// I: segments are independently resumable and offsets are exact.
func TestResumableSegmentUpload(t *testing.T) {
	s := startServer(t, options{})
	content := bytes.Repeat([]byte("resumable-payload-"), 4096)
	created := s.createRawTransfer(t, "r-0000", content, 60)
	uploadPath := created.Uploads[0].UploadPath

	// A fresh upload resource starts at offset zero.
	head := s.do(s.request(http.MethodHead, uploadPath, nil))
	head.Body.Close()
	if head.Header.Get("Tus-Resumable") != "1.0.0" {
		t.Errorf("Tus-Resumable = %q", head.Header.Get("Tus-Resumable"))
	}
	if got := head.Header.Get("Upload-Offset"); got != "0" {
		t.Errorf("initial offset = %q, want 0", got)
	}
	if got := head.Header.Get("Upload-Length"); got != strconv.Itoa(len(content)) {
		t.Errorf("length = %q, want %d", got, len(content))
	}

	half := len(content) / 2
	resp := s.patch(t, uploadPath, 0, content[:half])
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("first patch status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Upload-Offset"); got != strconv.Itoa(half) {
		t.Errorf("accepted offset = %q, want %d", got, half)
	}

	// Resuming from the wrong offset is a stable conflict, not silent damage.
	bad := s.patch(t, uploadPath, 0, content[:16])
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusConflict {
		t.Fatalf("offset mismatch status = %d, want 409", bad.StatusCode)
	}
	if got := decodeError(t, bad); got.Code != protocol.ErrOffsetMismatch {
		t.Errorf("code = %s, want %s", got.Code, protocol.ErrOffsetMismatch)
	}

	// Committing before every byte arrives must fail.
	early := s.commit(t, created.TransferID, manifestFor(created.TransferID, "r-0000", "big.bin", content))
	defer early.Body.Close()
	if early.StatusCode != http.StatusConflict {
		t.Fatalf("premature commit status = %d, want 409", early.StatusCode)
	}

	resp2 := s.patch(t, uploadPath, int64(half), content[half:])
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("second patch status = %d", resp2.StatusCode)
	}

	commit := s.commit(t, created.TransferID, manifestFor(created.TransferID, "r-0000", "big.bin", content))
	commitBody, _ := io.ReadAll(commit.Body)
	commit.Body.Close()
	if commit.StatusCode != http.StatusCreated {
		t.Fatalf("commit status = %d body %s", commit.StatusCode, commitBody)
	}
	var committed protocol.CommitResponse
	if err := json.Unmarshal(commitBody, &committed); err != nil {
		t.Fatalf("decode commit: %v", err)
	}
	if !codePattern.MatchString(committed.Code) {
		t.Fatalf("code %q malformed", committed.Code)
	}

	status, got, _ := s.receive(committed.Code, "")
	if status != http.StatusOK || !bytes.Equal(got, content) {
		t.Fatalf("download after resumable upload: status %d", status)
	}

	// Commit is idempotent: repeating it returns the original code.
	again := s.commit(t, created.TransferID, manifestFor(created.TransferID, "r-0000", "big.bin", content))
	againBody, _ := io.ReadAll(again.Body)
	again.Body.Close()
	if again.StatusCode != http.StatusOK {
		t.Fatalf("repeat commit status = %d, want 200", again.StatusCode)
	}
	var repeat protocol.CommitResponse
	_ = json.Unmarshal(againBody, &repeat)
	if repeat.Code != committed.Code {
		t.Errorf("repeat commit code = %s, want %s", repeat.Code, committed.Code)
	}
}

// A manifest that disagrees with the uploaded bytes must never publish.
func TestCommitRejectsDigestMismatch(t *testing.T) {
	s := startServer(t, options{})
	content := []byte("the real bytes")
	created := s.createRawTransfer(t, "r-0000", content, 60)
	resp := s.patch(t, created.Uploads[0].UploadPath, 0, content)
	resp.Body.Close()

	lying := manifestFor(created.TransferID, "r-0000", "claim.txt", content)
	lying.Segments[0].Digest = integrity.HashBytes([]byte("different bytes"))
	lying.Entries[0].Digest = lying.Segments[0].Digest

	commit := s.commit(t, created.TransferID, lying)
	defer commit.Body.Close()
	if commit.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", commit.StatusCode)
	}
	if got := decodeError(t, commit); got.Code != protocol.ErrHashMismatch {
		t.Errorf("code = %s, want %s", got.Code, protocol.ErrHashMismatch)
	}
}

// A manifest path that escapes the payload root must be refused.
func TestCommitRejectsPathTraversal(t *testing.T) {
	s := startServer(t, options{})
	content := []byte("escape attempt")
	created := s.createRawTransfer(t, "r-0000", content, 60)
	resp := s.patch(t, created.Uploads[0].UploadPath, 0, content)
	resp.Body.Close()

	hostile := manifestFor(created.TransferID, "r-0000", "../../etc/passwd", content)
	hostile.Roots = []string{".."}
	commit := s.commit(t, created.TransferID, hostile)
	defer commit.Body.Close()
	if commit.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", commit.StatusCode)
	}
	if got := decodeError(t, commit); got.Code != protocol.ErrInvalidPath {
		t.Errorf("code = %s, want %s", got.Code, protocol.ErrInvalidPath)
	}
}

// H: disk admission refuses transfers that cannot fit.
func TestDiskAdmissionRefusesOversizedTransfer(t *testing.T) {
	s := startServer(t, options{})
	huge := int64(1) << 50 // 1 PiB, larger than any test host
	req := protocol.CreateTransferRequest{
		TTLMinutes: 60,
		Segments: []protocol.SegmentPlan{{
			ID:              "r-0000",
			Kind:            protocol.SegmentRaw,
			ExpectedLength:  huge,
			DigestAlgorithm: protocol.DigestAlgorithm,
		}},
	}
	body, _ := json.Marshal(req)
	httpReq := s.request(http.MethodPost, "/v1/transfers", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp := s.do(httpReq)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", resp.StatusCode)
	}
	if got := decodeError(t, resp); got.Code != protocol.ErrInsufficientStorage {
		t.Errorf("code = %s, want %s", got.Code, protocol.ErrInsufficientStorage)
	}

	// Existing committed downloads keep working under pressure.
	code := s.sendFile("alpha.txt", []byte("still available\n"), "", "")
	if status, _, _ := s.receive(code, ""); status != http.StatusOK {
		t.Errorf("download after admission refusal: status %d", status)
	}
}

// Claim sessions authorize with their own token and support ranges.
func TestClaimSegmentDownload(t *testing.T) {
	s := startServer(t, options{})
	content := bytes.Repeat([]byte("claimable-"), 2048)
	code := s.sendFile("claim.bin", content, "", "")

	body, _ := json.Marshal(protocol.ClaimRequest{Code: code})
	req := s.request(http.MethodPost, "/v1/claims", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := s.do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("claim status = %d", resp.StatusCode)
	}
	var claim protocol.ClaimResponse
	if err := json.NewDecoder(resp.Body).Decode(&claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if claim.Token == "" || claim.ClaimID == "" || len(claim.Segments) != 1 {
		t.Fatalf("claim = %+v", claim)
	}
	if !claim.LeaseUntil.After(time.Now().Add(time.Minute)) {
		t.Errorf("lease_until = %s looks too short", claim.LeaseUntil)
	}

	t.Run("token required", func(t *testing.T) {
		bare, _ := http.NewRequest(http.MethodGet, s.baseURL+claim.Segments[0].Path, nil)
		r := s.do(bare)
		defer r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status without token = %d, want 401", r.StatusCode)
		}
	})

	t.Run("range resume", func(t *testing.T) {
		half := len(content) / 2
		r, _ := http.NewRequest(http.MethodGet, s.baseURL+claim.Segments[0].Path, nil)
		r.Header.Set("Authorization", "Bearer "+claim.Token)
		r.Header.Set("Range", "bytes="+strconv.Itoa(half)+"-")
		resp := s.do(r)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", resp.StatusCode)
		}
		got, _ := io.ReadAll(resp.Body)
		if !bytes.Equal(got, content[half:]) {
			t.Error("ranged bytes differ")
		}
	})

	t.Run("completion does not consume the transfer", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodPost, s.baseURL+"/v1/claims/"+claim.ClaimID+"/complete", nil)
		r.Header.Set("Authorization", "Bearer "+claim.Token)
		resp := s.do(r)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("complete status = %d, want 204", resp.StatusCode)
		}
		if status, _, _ := s.receive(code, ""); status != http.StatusOK {
			t.Errorf("transfer gone after completion: status %d", status)
		}
	})
}

func (s *server) commit(t *testing.T, transferID string, m protocol.Manifest) *http.Response {
	t.Helper()
	body, _ := json.Marshal(protocol.CommitRequest{Manifest: m})
	req := s.request(http.MethodPost, "/v1/transfers/"+transferID+"/commit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return s.do(req)
}

// manifestFor builds a one-file manifest for a raw segment.
func manifestFor(transferID, segmentID, path string, content []byte) protocol.Manifest {
	digest := integrity.HashBytes(content)
	root := path
	if i := bytes.IndexByte([]byte(path), '/'); i >= 0 {
		root = path[:i]
	}
	return protocol.Manifest{
		SchemaVersion:   1,
		TransferID:      transferID,
		CreatedAt:       time.Now().UTC(),
		Roots:           []string{root},
		DigestAlgorithm: protocol.DigestAlgorithm,
		Segments: []protocol.Segment{{
			ID: segmentID, Kind: protocol.SegmentRaw, WireSize: int64(len(content)), Digest: digest,
		}},
		Entries: []protocol.Entry{{
			Path:        path,
			Type:        protocol.EntryFile,
			Size:        int64(len(content)),
			MTimeUnixNS: time.Now().UnixNano(),
			Mode:        0o644,
			Digest:      digest,
			SegmentID:   segmentID,
		}},
	}
}
