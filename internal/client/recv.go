package client

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/materialize"
	"github.com/sss/sss/internal/pack"
	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/store/files"
)

// RecvOptions configures a receive.
type RecvOptions struct {
	Code        string
	Destination string // empty means a unique sss-<CODE> path in the working directory
	Workers     int    // materialization workers
	Concurrency int    // simultaneous segment fetches
	Progress    Progress
}

// RecvResult reports what a receive produced.
type RecvResult struct {
	Path     string
	Files    int
	Bytes    int64
	ZeroCopy bool
	Note     string
	ClaimID  string
}

// Recv downloads a handoff and exposes it only after complete download,
// extraction, and verification. The destination appears atomically.
func Recv(ctx context.Context, c *Client, opts RecvOptions) (RecvResult, *protocol.Error) {
	opts.Progress.report(PhaseClaiming, "opening receive session")
	claim, perr := c.Claim(ctx, opts.Code)
	if perr != nil {
		return RecvResult{}, perr
	}
	canonical, _ := protocol.NormalizeCode(opts.Code)

	dest, perr := resolveDestination(opts.Destination, canonical)
	if perr != nil {
		return RecvResult{}, perr
	}
	// Work inside a hidden sibling so a partially received tree is never
	// visible at the final path.
	partial := filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".sss-partial")
	segmentsDir := filepath.Join(partial, files.SegmentsDir)
	if err := os.MkdirAll(segmentsDir, 0o700); err != nil {
		return RecvResult{}, protocol.Errorf(protocol.ErrDestinationExists, "cannot create working directory: %v", err)
	}

	if perr := fetchSegments(ctx, c, claim, segmentsDir, opts); perr != nil {
		return RecvResult{}, perr
	}

	opts.Progress.report(PhaseExtracting, "materializing payload")
	workers := opts.Workers
	if workers < 1 {
		workers = 4
	}
	result, perr := materialize.Run(ctx, partial, claim.Manifest, workers)
	if perr != nil {
		// Materialization consumes staged segments, so a failed attempt cannot
		// be resumed; clear the working directory and let the caller retry.
		_ = platform.RemoveAllForce(partial)
		return RecvResult{}, perr
	}

	opts.Progress.report(PhaseFinalizing, "publishing destination")
	payload := filepath.Join(partial, files.PayloadDir)
	if _, err := os.Lstat(dest); err == nil {
		_ = platform.RemoveAllForce(partial)
		return RecvResult{}, protocol.Errorf(protocol.ErrDestinationExists, "%s already exists", dest)
	}
	if err := os.Rename(payload, dest); err != nil {
		_ = platform.RemoveAllForce(partial)
		return RecvResult{}, protocol.Errorf(protocol.ErrDestinationExists, "cannot publish destination: %v", err)
	}
	_ = platform.RemoveAllForce(partial)

	if perr := c.CompleteClaim(ctx, claim.ClaimID, claim.Token); perr != nil {
		// Completion is bookkeeping only; the payload is already delivered.
		opts.Progress.report(PhaseFinalizing, "could not record completion: "+perr.Message)
	}

	abs, err := filepath.Abs(dest)
	if err != nil {
		abs = dest
	}
	return RecvResult{
		Path:    abs,
		Files:   claim.Manifest.FileCount(),
		Bytes:   result.MaterializedBytes,
		Note:    claim.Manifest.Note,
		ClaimID: claim.ClaimID,
	}, nil
}

// fetchSegments pulls every segment with bounded concurrency. Segments are
// immutable and each is verified against its own digest, so parallel fetches
// cannot interact; the bound exists to keep a receiver from opening an
// unreasonable number of connections, not for correctness.
func fetchSegments(ctx context.Context, c *Client, claim protocol.ClaimResponse, segmentsDir string, opts RecvOptions) *protocol.Error {
	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = pack.DownloadConcurrency
	}
	if concurrency > len(claim.Segments) {
		concurrency = len(claim.Segments)
	}
	if concurrency == 0 {
		return nil
	}

	ch := make(chan protocol.ClaimSegment)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		failure *protocol.Error
		done    int
	)
	setErr := func(e *protocol.Error) {
		mu.Lock()
		if failure == nil {
			failure = e
		}
		mu.Unlock()
	}
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for seg := range ch {
				target := filepath.Join(segmentsDir, seg.ID)
				if perr := fetchOneSegment(fetchCtx, c, claim.Token, seg, target); perr != nil {
					setErr(perr)
					cancel()
					return
				}
				mu.Lock()
				done++
				progress := done
				mu.Unlock()
				opts.Progress.report(PhaseDownload,
					plural(progress, "segment", "segments")+" of "+itoa(len(claim.Segments))+" complete")
			}
		}()
	}
	for _, seg := range claim.Segments {
		select {
		case ch <- seg:
		case <-fetchCtx.Done():
		}
	}
	close(ch)
	wg.Wait()
	return failure
}

// fetchOneSegment downloads a segment, preferring verified streaming when the
// server offers a verification tree and falling back to whole-segment hashing
// when it does not.
func fetchOneSegment(ctx context.Context, c *Client, token string, seg protocol.ClaimSegment, target string) *protocol.Error {
	if seg.OutboardPath == "" || seg.OutboardLength <= 0 {
		return fetchSegment(ctx, c, token, seg, target)
	}
	outboard, perr := c.OutboardBytes(ctx, seg.OutboardPath, token, seg.OutboardLength)
	if perr != nil {
		// A missing or malformed tree is not fatal: the whole-segment path still
		// proves the same digest, just less precisely.
		return fetchSegment(ctx, c, token, seg, target)
	}
	return fetchVerified(ctx, c, token, seg, target, outboard)
}

// fetchVerified downloads a segment one verification group at a time, proving
// each group against the segment digest before it is written.
//
// Two properties follow, and both matter more than the speed:
//
//   - Nothing unverified is ever persisted, so whatever is on disk after a
//     crash, a kill, or a torn write is authentic by construction and resume
//     needs no re-hashing.
//   - A corrupt or tampered range costs exactly one group to re-fetch, not the
//     whole segment, because each group is proved independently against the
//     BLAKE3 root rather than as part of one long hash.
func fetchVerified(ctx context.Context, c *Client, token string, seg protocol.ClaimSegment, target string, outboard []byte) *protocol.Error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return protocol.Errorf(protocol.ErrDestinationExists, "cannot write segment: %v", err)
	}
	defer f.Close()

	// Resume only from a group boundary: a partial group cannot be proved, so
	// the tail below the boundary is discarded rather than trusted.
	var offset int64
	if info, statErr := f.Stat(); statErr == nil {
		offset = info.Size() - info.Size()%integrity.GroupSize
		if offset > seg.Length {
			offset = seg.Length - seg.Length%integrity.GroupSize
		}
	}
	if err := f.Truncate(offset); err != nil {
		return protocol.Errorf(protocol.ErrDestinationExists, "cannot trim segment: %v", err)
	}

	buf := make([]byte, integrity.GroupSize)
	var body io.ReadCloser
	defer func() {
		if body != nil {
			body.Close()
		}
	}()
	attempts := 0
	for offset < seg.Length {
		if attempts >= 3 {
			return protocol.Errorf(protocol.ErrHashMismatch,
				"segment %q failed verification at offset %d after retries", seg.ID, offset)
		}
		want := seg.Length - offset
		if want > integrity.GroupSize {
			want = integrity.GroupSize
		}
		if body == nil {
			r, perr := c.SegmentReader(ctx, seg.Path, token, offset)
			if perr != nil {
				if perr.Code == protocol.ErrNetwork {
					attempts++
					continue
				}
				return perr
			}
			body = r
		}
		if _, err := io.ReadFull(body, buf[:want]); err != nil {
			// The stream itself is gone; reopen from the current offset.
			body.Close()
			body = nil
			attempts++
			continue
		}
		if !integrity.VerifyGroup(buf[:want], outboard, offset, seg.Digest) {
			// The stream is still positioned correctly for the *next* group, so
			// repair this one with a side request and keep reading. Tearing the
			// main stream down here would force the whole remainder to be sent
			// again — the exact cost this design exists to avoid.
			if perr := repairGroup(ctx, c, token, seg, offset, want, outboard, buf); perr != nil {
				attempts++
				continue
			}
		}
		if _, err := f.WriteAt(buf[:want], offset); err != nil {
			return protocol.Errorf(protocol.ErrDestinationExists, "cannot write segment: %v", err)
		}
		offset += want
		attempts = 0
	}
	if err := f.Sync(); err != nil {
		return protocol.Errorf(protocol.ErrDestinationExists, "cannot flush segment: %v", err)
	}
	// Every byte was proved against the root before it was written, so there is
	// no final whole-file hash to pay for.
	return nil
}

// repairGroup re-fetches exactly one verification group and proves it, leaving
// buf holding the authentic bytes on success. It requests a bounded range, so
// the cost of recovering from a corrupt group is that group and nothing else.
func repairGroup(ctx context.Context, c *Client, token string, seg protocol.ClaimSegment, offset, want int64, outboard, buf []byte) *protocol.Error {
	r, perr := c.SegmentRangeReader(ctx, seg.Path, token, offset, want)
	if perr != nil {
		return perr
	}
	defer r.Close()
	if _, err := io.ReadFull(r, buf[:want]); err != nil {
		return protocol.Errorf(protocol.ErrNetwork, "group re-fetch interrupted: %v", err)
	}
	if !integrity.VerifyGroup(buf[:want], outboard, offset, seg.Digest) {
		return protocol.Errorf(protocol.ErrHashMismatch, "group at offset %d failed verification", offset)
	}
	return nil
}

// fetchSegment downloads one immutable segment, resuming from whatever is
// already on disk and verifying the digest before it is used.
func fetchSegment(ctx context.Context, c *Client, token string, seg protocol.ClaimSegment, target string) *protocol.Error {
	for attempt := 0; attempt < 3; attempt++ {
		var offset int64
		if info, err := os.Stat(target); err == nil {
			offset = info.Size()
			if offset > seg.Length {
				// Local file is longer than the immutable segment: start over.
				_ = os.Remove(target)
				offset = 0
			}
		}
		if offset < seg.Length {
			body, perr := c.SegmentReader(ctx, seg.Path, token, offset)
			if perr != nil {
				if perr.Code == protocol.ErrNetwork && attempt < 2 {
					continue
				}
				return perr
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				body.Close()
				return protocol.Errorf(protocol.ErrDestinationExists, "cannot write segment: %v", err)
			}
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				body.Close()
				f.Close()
				return protocol.Errorf(protocol.ErrDestinationExists, "cannot seek segment: %v", err)
			}
			buf := make([]byte, 1<<20)
			_, copyErr := io.CopyBuffer(f, body, buf)
			body.Close()
			syncErr := f.Sync()
			f.Close()
			if copyErr != nil {
				if attempt < 2 {
					continue
				}
				return protocol.Errorf(protocol.ErrNetwork, "segment download interrupted: %v", copyErr)
			}
			if syncErr != nil {
				return protocol.Errorf(protocol.ErrDestinationExists, "cannot flush segment: %v", syncErr)
			}
		}
		digest, size, err := integrity.HashFile(target)
		if err != nil {
			return protocol.Errorf(protocol.ErrDestinationExists, "cannot verify segment: %v", err)
		}
		if size == seg.Length && digest == seg.Digest {
			return nil
		}
		// A corrupt or truncated segment is discarded and re-fetched once.
		_ = os.Remove(target)
	}
	return protocol.Errorf(protocol.ErrHashMismatch, "segment %q failed verification after retries", seg.ID)
}

// resolveDestination applies the documented destination rules: never overwrite,
// and default to a unique sss-<CODE> path in the working directory.
func resolveDestination(explicit, canonicalCode string) (string, *protocol.Error) {
	if explicit != "" {
		clean := filepath.Clean(explicit)
		if _, err := os.Lstat(clean); err == nil {
			return "", protocol.Errorf(protocol.ErrDestinationExists, "%s already exists", clean)
		}
		parent := filepath.Dir(clean)
		if info, err := os.Stat(parent); err != nil || !info.IsDir() {
			return "", protocol.Errorf(protocol.ErrDestinationExists, "%s is not an existing directory", parent)
		}
		return clean, nil
	}
	base := "sss-" + protocol.FormatCode(strings.ToUpper(canonicalCode))
	unique, err := platform.UniqueDestination(base)
	if err != nil {
		return "", protocol.Errorf(protocol.ErrDestinationExists, "%v", err)
	}
	return unique, nil
}
