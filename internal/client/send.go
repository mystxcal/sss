package client

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sss/sss/internal/pack"
	"github.com/sss/sss/internal/protocol"
)

// Phase names reported to the caller for progress output.
const (
	PhaseScanning   = "Scanning"
	PhasePacking    = "Packing"
	PhaseUploading  = "Uploading"
	PhaseCommitting = "Committing"
	PhaseClaiming   = "Claiming"
	PhaseDownload   = "Downloading"
	PhaseExtracting = "Extracting"
	PhaseVerifying  = "Verifying"
	PhaseFinalizing = "Finalizing"
)

// Progress receives phase notifications. It is always optional.
type Progress func(phase, detail string)

func (p Progress) report(phase, detail string) {
	if p != nil {
		p(phase, detail)
	}
}

// SendOptions configures a send.
type SendOptions struct {
	Roots       []string
	Note        string
	TTLMinutes  int
	StateDir    string
	Concurrency int
	Progress    Progress
}

// SendResult reports what a send produced.
type SendResult struct {
	Code       string
	CommitedAt time.Time
	ExpiresAt  time.Time
	Files      int
	Bytes      int64
	Resumed    bool
}

// session is the resumable state of an in-flight send.
type session struct {
	Fingerprint string             `json:"fingerprint"`
	TransferID  string             `json:"transfer_id"`
	Uploads     map[string]string  `json:"uploads"` // segment id -> upload path
	Packs       map[string]packRef `json:"packs"`
	CreatedAt   time.Time          `json:"created_at"`
	ServerURL   string             `json:"server_url"`
}

type packRef struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// Send performs a resumable advanced send and returns the allocated code.
//
// Rerunning the same command resumes when it is safe: the sources must be
// byte-identical and the server must still hold the transfer.
func Send(ctx context.Context, c *Client, opts SendOptions) (SendResult, *protocol.Error) {
	opts.Progress.report(PhaseScanning, "reading sources")
	plan, perr := pack.Scan(opts.Roots)
	if perr != nil {
		return SendResult{}, perr
	}
	fingerprint := plan.Fingerprint(opts.Note, opts.TTLMinutes)
	sessionPath := filepath.Join(opts.StateDir, "sessions", fingerprint+".json")

	sess, resumed := loadSession(sessionPath, fingerprint, c.BaseURL())
	if resumed {
		if perr := plan.VerifyUnchanged(); perr != nil {
			// A changed source can never resume: fail loudly rather than
			// silently mixing old and new bytes.
			removeSession(sessionPath, sess)
			return SendResult{}, perr
		}
	}

	// Build (or reuse) packs before declaring segment sizes to the server.
	packDir := filepath.Join(opts.StateDir, "packs", fingerprint)
	packs := map[string]packRef{}
	if len(plan.Packs) > 0 {
		opts.Progress.report(PhasePacking, plural(len(plan.Packs), "pack", "packs"))
	}
	for _, spec := range plan.Packs {
		if resumed {
			if ref, ok := sess.Packs[spec.SegmentID]; ok && packUsable(ref) {
				packs[spec.SegmentID] = ref
				continue
			}
		}
		path, size, digest, perr := pack.Build(spec, packDir)
		if perr != nil {
			return SendResult{}, perr
		}
		packs[spec.SegmentID] = packRef{Path: path, Size: size, Digest: digest}
	}

	segments := make([]protocol.SegmentPlan, 0, len(plan.RawFiles)+len(packs))
	manifestSegments := make([]protocol.Segment, 0, cap(segments))
	for segID, src := range plan.RawFiles {
		digest := src.Digest
		segments = append(segments, protocol.SegmentPlan{
			ID:              segID,
			Kind:            protocol.SegmentRaw,
			ExpectedLength:  src.Size,
			DigestAlgorithm: protocol.DigestAlgorithm,
			ExpectedDigest:  &digest,
		})
		manifestSegments = append(manifestSegments, protocol.Segment{
			ID: segID, Kind: protocol.SegmentRaw, WireSize: src.Size, Digest: src.Digest,
		})
	}
	for _, spec := range plan.Packs {
		ref := packs[spec.SegmentID]
		digest := ref.Digest
		segments = append(segments, protocol.SegmentPlan{
			ID:              spec.SegmentID,
			Kind:            protocol.SegmentTarZstd,
			ExpectedLength:  ref.Size,
			DigestAlgorithm: protocol.DigestAlgorithm,
			ExpectedDigest:  &digest,
		})
		manifestSegments = append(manifestSegments, protocol.Segment{
			ID: spec.SegmentID, Kind: protocol.SegmentTarZstd, WireSize: ref.Size, Digest: ref.Digest,
		})
	}

	if resumed {
		// Confirm the server still knows this transfer before trusting it.
		if !sessionAlive(ctx, c, sess) {
			removeSession(sessionPath, sess)
			resumed = false
		}
	}
	if !resumed {
		created, perr := c.CreateTransfer(ctx, protocol.CreateTransferRequest{
			TTLMinutes:                opts.TTLMinutes,
			Note:                      opts.Note,
			ExpectedMaterializedBytes: plan.TotalBytes,
			ExpectedFileCount:         plan.FileCount,
			Segments:                  segments,
		}, "")
		if perr != nil {
			return SendResult{}, perr
		}
		sess = &session{
			Fingerprint: fingerprint,
			TransferID:  created.TransferID,
			Uploads:     map[string]string{},
			Packs:       packs,
			CreatedAt:   time.Now().UTC(),
			ServerURL:   c.BaseURL(),
		}
		for _, u := range created.Uploads {
			sess.Uploads[u.SegmentID] = u.UploadPath
		}
		if err := saveSession(sessionPath, sess); err != nil {
			return SendResult{}, protocol.Errorf(protocol.ErrDestinationExists, "cannot record resume state: %v", err)
		}
	} else {
		sess.Packs = packs
		_ = saveSession(sessionPath, sess)
	}

	opts.Progress.report(PhaseUploading, protocol.HumanBytes(plan.TotalBytes)+" in "+plural(len(segments), "segment", "segments"))
	if perr := uploadSegments(ctx, c, sess, plan, packs, opts); perr != nil {
		return SendResult{}, perr
	}

	manifest := protocol.Manifest{
		SchemaVersion:   1,
		TransferID:      sess.TransferID,
		CreatedAt:       time.Now().UTC(),
		Note:            opts.Note,
		Roots:           plan.Roots,
		DigestAlgorithm: protocol.DigestAlgorithm,
		Segments:        manifestSegments,
		Entries:         plan.Entries,
	}
	opts.Progress.report(PhaseCommitting, "verifying on server")
	commit, perr := c.Commit(ctx, sess.TransferID, manifest)
	if perr != nil {
		return SendResult{}, perr
	}
	removeSession(sessionPath, sess)
	return SendResult{
		Code:       commit.Code,
		CommitedAt: commit.CommittedAt,
		ExpiresAt:  commit.ExpiresAt,
		Files:      plan.FileCount,
		Bytes:      plan.TotalBytes,
		Resumed:    resumed,
	}, nil
}

// uploadSegments pushes every segment with bounded concurrency, resuming each
// one from the offset the server already accepted.
func uploadSegments(ctx context.Context, c *Client, sess *session, plan *pack.Plan, packs map[string]packRef, opts SendOptions) *protocol.Error {
	type work struct {
		segmentID string
		path      string
		length    int64
	}
	var jobs []work
	for segID, src := range plan.RawFiles {
		jobs = append(jobs, work{segmentID: segID, path: src.AbsPath, length: src.Size})
	}
	for segID, ref := range packs {
		jobs = append(jobs, work{segmentID: segID, path: ref.Path, length: ref.Size})
	}

	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = pack.UploadConcurrency
	}
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}
	if concurrency == 0 {
		return nil
	}

	ch := make(chan work)
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
	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				uploadPath := sess.Uploads[j.segmentID]
				if uploadPath == "" {
					setErr(protocol.Errorf(protocol.ErrStateConflict, "no upload resource for segment %q", j.segmentID))
					cancel()
					return
				}
				if perr := uploadOne(uploadCtx, c, uploadPath, j.path, j.length); perr != nil {
					setErr(perr)
					cancel()
					return
				}
				mu.Lock()
				done++
				progress := done
				mu.Unlock()
				opts.Progress.report(PhaseUploading, plural(progress, "segment", "segments")+" of "+itoa(len(jobs))+" complete")
			}
		}()
	}
	for _, j := range jobs {
		select {
		case ch <- j:
		case <-uploadCtx.Done():
		}
	}
	close(ch)
	wg.Wait()
	return failure
}

// uploadOne transfers a single segment, resuming from the server's offset.
func uploadOne(ctx context.Context, c *Client, uploadPath, sourcePath string, length int64) *protocol.Error {
	for attempt := 0; attempt < 3; attempt++ {
		offset, _, perr := c.UploadOffset(ctx, uploadPath)
		if perr != nil {
			return perr
		}
		if offset == length {
			return nil
		}
		if offset > length {
			return protocol.Errorf(protocol.ErrStateConflict, "server holds more bytes than the segment contains")
		}
		f, err := os.Open(sourcePath)
		if err != nil {
			return protocol.Errorf(protocol.ErrSourceChanged, "cannot read %q: %v", sourcePath, err)
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return protocol.Errorf(protocol.ErrSourceChanged, "cannot seek %q: %v", sourcePath, err)
		}
		accepted, perr := c.UploadPatch(ctx, uploadPath, offset, f, length-offset)
		f.Close()
		if perr == nil && accepted == length {
			return nil
		}
		if perr != nil {
			switch perr.Code {
			case protocol.ErrOffsetMismatch, protocol.ErrNetwork:
				// Re-read the accepted offset and continue from there.
				continue
			default:
				return perr
			}
		}
	}
	return protocol.Errorf(protocol.ErrNetwork, "could not finish uploading a segment after several attempts")
}

func loadSession(path, fingerprint, serverURL string) (*session, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var s session
	if json.Unmarshal(data, &s) != nil {
		return nil, false
	}
	if s.Fingerprint != fingerprint || s.TransferID == "" || s.ServerURL != serverURL {
		return nil, false
	}
	if s.Uploads == nil {
		return nil, false
	}
	return &s, true
}

func saveSession(path string, s *session) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func removeSession(path string, s *session) {
	_ = os.Remove(path)
	if s == nil {
		return
	}
	for _, ref := range s.Packs {
		_ = os.Remove(ref.Path)
	}
	if len(s.Packs) > 0 {
		for _, ref := range s.Packs {
			_ = os.Remove(filepath.Dir(ref.Path))
			break
		}
	}
}

func packUsable(ref packRef) bool {
	info, err := os.Stat(ref.Path)
	return err == nil && info.Size() == ref.Size
}

// sessionAlive checks that the server still holds the transfer behind a session.
func sessionAlive(ctx context.Context, c *Client, s *session) bool {
	for _, uploadPath := range s.Uploads {
		if _, _, perr := c.UploadOffset(ctx, uploadPath); perr != nil {
			return false
		}
		return true
	}
	return false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
