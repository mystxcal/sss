package client

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/protocol"
)

// segmentServer serves a segment with byte-range support, counts the payload
// bytes it hands out, and can corrupt the first delivery of one group.
type segmentServer struct {
	data       []byte
	outboard   []byte
	served     atomic.Int64
	corruptAt  int64 // -1 disables
	corrupted  atomic.Bool
	rangeStart atomic.Int64
}

func (s *segmentServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/outboard") {
			w.Write(s.outboard)
			return
		}
		start, end := int64(0), int64(len(s.data))-1
		if h := r.Header.Get("Range"); h != "" {
			// Both forms the client sends: "bytes=N-" and the bounded
			// "bytes=N-M" used to repair a single group.
			if _, err := fmt.Sscanf(h, "bytes=%d-%d", &start, &end); err != nil {
				fmt.Sscanf(h, "bytes=%d-", &start)
				end = int64(len(s.data)) - 1
			}
			if end > int64(len(s.data))-1 {
				end = int64(len(s.data)) - 1
			}
			w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+
				strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(int64(len(s.data)), 10))
			w.WriteHeader(http.StatusPartialContent)
		}
		s.rangeStart.Store(start)
		body := make([]byte, end-start+1)
		copy(body, s.data[start:end+1])
		// Corrupt one group, once, to exercise the re-fetch path.
		if s.corruptAt >= 0 && !s.corrupted.Load() && start <= s.corruptAt && s.corruptAt < int64(len(s.data)) {
			s.corrupted.Store(true)
			body[s.corruptAt-start] ^= 0xff
		}
		n, _ := w.Write(body)
		s.served.Add(int64(n))
	})
}

func newSegmentFixture(t *testing.T, size int, corruptAt int64) (*segmentServer, *httptest.Server, protocol.ClaimSegment) {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand: %v", err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "seg")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	root, _, err := integrity.OutboardFile(src, filepath.Join(dir, "seg.obao"))
	if err != nil {
		t.Fatalf("outboard: %v", err)
	}
	outboard, err := os.ReadFile(filepath.Join(dir, "seg.obao"))
	if err != nil {
		t.Fatalf("read outboard: %v", err)
	}
	ss := &segmentServer{data: data, outboard: outboard, corruptAt: corruptAt}
	srv := httptest.NewServer(ss.handler())
	t.Cleanup(srv.Close)
	seg := protocol.ClaimSegment{
		ID:             "seg",
		Kind:           protocol.SegmentRaw,
		Length:         int64(size),
		Digest:         root,
		Path:           "/segment",
		OutboardPath:   "/segment/outboard",
		OutboardLength: int64(len(outboard)),
	}
	return ss, srv, seg
}

func TestFetchVerifiedProducesTheOriginalBytes(t *testing.T) {
	ss, srv, seg := newSegmentFixture(t, 5*integrity.GroupSize+1234, -1)
	c := New(srv.URL, "")
	target := filepath.Join(t.TempDir(), "out")

	if perr := fetchVerified(context.Background(), c, "token", seg, target, ss.outboard); perr != nil {
		t.Fatalf("fetch: %v", perr)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if digest := integrity.HashBytes(got); digest != seg.Digest {
		t.Errorf("digest %s != %s", digest, seg.Digest)
	}
	if int64(len(got)) != seg.Length {
		t.Errorf("length %d != %d", len(got), seg.Length)
	}
}

// The Phase 1 gate: a corrupt range costs one verification group, not the
// whole segment.
func TestFetchVerifiedRefetchesOnlyTheCorruptGroup(t *testing.T) {
	const size = 100 * integrity.GroupSize // 6.4 MiB
	// Corrupt a byte in the middle group.
	ss, srv, seg := newSegmentFixture(t, size, 50*integrity.GroupSize+17)
	c := New(srv.URL, "")
	target := filepath.Join(t.TempDir(), "out")

	if perr := fetchVerified(context.Background(), c, "token", seg, target, ss.outboard); perr != nil {
		t.Fatalf("fetch: %v", perr)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if digest := integrity.HashBytes(got); digest != seg.Digest {
		t.Fatalf("corruption survived: digest %s != %s", digest, seg.Digest)
	}
	// The server streams from the failing offset to the end, but the client
	// stops reading once each group is proved, so the re-fetch must stay far
	// below a second full copy.
	overhead := ss.served.Load() - int64(size)
	t.Logf("one corrupt group cost %d extra bytes served (%.2f%% of the %d byte segment)", overhead, 100*float64(overhead)/float64(size), size)
	// One corrupt group must cost one group. Anything approaching a second copy
	// of the segment means the main stream is being torn down on failure.
	if overhead > 2*integrity.GroupSize {
		t.Errorf("re-fetched %d bytes after one corrupt group, want no more than %d",
			overhead, 2*integrity.GroupSize)
	}
	if ss.rangeStart.Load() == 0 {
		t.Error("recovery restarted from offset 0 instead of the failing group")
	}
}

// Transfers committed before verification trees existed advertise no outboard,
// and must still receive correctly through the whole-segment path.
func TestFetchFallsBackWhenNoOutboardIsOffered(t *testing.T) {
	ss, srv, seg := newSegmentFixture(t, 3*integrity.GroupSize+77, -1)
	seg.OutboardPath = ""
	seg.OutboardLength = 0
	c := New(srv.URL, "")
	target := filepath.Join(t.TempDir(), "out")

	if perr := fetchOneSegment(context.Background(), c, "token", seg, target); perr != nil {
		t.Fatalf("fetch: %v", perr)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if digest := integrity.HashBytes(got); digest != seg.Digest {
		t.Errorf("digest %s != %s", digest, seg.Digest)
	}
	if int64(len(got)) != seg.Length {
		t.Errorf("length %d != %d", len(got), seg.Length)
	}
	_ = ss
}

func TestFetchVerifiedResumesFromAGroupBoundary(t *testing.T) {
	const size = 8 * integrity.GroupSize
	ss, srv, seg := newSegmentFixture(t, size, -1)
	c := New(srv.URL, "")
	target := filepath.Join(t.TempDir(), "out")

	// A previous attempt left three verified groups plus a torn partial group.
	partial := append([]byte{}, ss.data[:3*integrity.GroupSize+99]...)
	if err := os.WriteFile(target, partial, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if perr := fetchVerified(context.Background(), c, "token", seg, target, ss.outboard); perr != nil {
		t.Fatalf("fetch: %v", perr)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if digest := integrity.HashBytes(got); digest != seg.Digest {
		t.Errorf("digest %s != %s", digest, seg.Digest)
	}
	// The torn tail is discarded and only whole groups are re-fetched: the
	// three complete groups already on disk are never requested again.
	if ss.rangeStart.Load() != 3*integrity.GroupSize {
		t.Errorf("resumed at offset %d, want %d", ss.rangeStart.Load(), 3*integrity.GroupSize)
	}
	if served := ss.served.Load(); served > size-3*integrity.GroupSize {
		t.Errorf("served %d bytes, more than the %d that were missing", served, size-3*integrity.GroupSize)
	}
}
