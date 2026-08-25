package pack

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/sss/sss/internal/integrity"
	"github.com/sss/sss/internal/protocol"
)

// Build writes one pack as a tar.zst file and returns its wire size and digest.
// The pack is streamed: no archive is ever held in memory.
func Build(spec PackSpec, dir string) (path string, size int64, digest string, perr *protocol.Error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, "", protocol.Errorf(protocol.ErrDestinationExists, "cannot create pack directory: %v", err)
	}
	path = filepath.Join(dir, spec.SegmentID+".tar.zst")
	f, err := os.Create(path)
	if err != nil {
		return "", 0, "", protocol.Errorf(protocol.ErrDestinationExists, "cannot create pack: %v", err)
	}
	defer func() {
		if perr != nil {
			f.Close()
			os.Remove(path)
		}
	}()

	hasher := integrity.New()
	counter := &countingWriter{w: io.MultiWriter(f, hasher)}
	// SpeedDefault keeps small-file trees cheap to pack without wasting CPU.
	// Payloads that do not compress at all — media, archives, encrypted blobs —
	// drop to SpeedFastest, which still emits a valid zstd stream (so the wire
	// format and the decoder are untouched) at a fraction of the CPU. On a fast
	// link, compression is the throughput ceiling, so this is the difference
	// between the CPU and the NIC being the bottleneck.
	level := zstd.SpeedDefault
	if !compressible(spec) {
		level = zstd.SpeedFastest
	}
	enc, err := zstd.NewWriter(counter, zstd.WithEncoderLevel(level), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return "", 0, "", protocol.Errorf(protocol.ErrInternal, "cannot start compression: %v", err)
	}
	tw := tar.NewWriter(enc)
	buf := make([]byte, 1<<20)
	for _, src := range spec.Files {
		mode := int64(0o644)
		if src.Mode&0o111 != 0 {
			mode = 0o755
		}
		hdr := &tar.Header{
			Name:     src.RelPath,
			Typeflag: tar.TypeReg,
			Size:     src.Size,
			Mode:     mode,
			ModTime:  time.Unix(0, src.MTimeNS),
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return "", 0, "", protocol.Errorf(protocol.ErrInternal, "cannot write pack header: %v", err)
		}
		in, err := os.Open(src.AbsPath)
		if err != nil {
			return "", 0, "", protocol.Errorf(protocol.ErrSourceChanged, "cannot read %q: %v", src.AbsPath, err)
		}
		n, err := io.CopyBuffer(tw, in, buf)
		in.Close()
		if err != nil {
			return "", 0, "", protocol.Errorf(protocol.ErrSourceChanged, "cannot read %q: %v", src.AbsPath, err)
		}
		if n != src.Size {
			return "", 0, "", protocol.Errorf(protocol.ErrSourceChanged, "%q changed while it was being packed", src.AbsPath)
		}
	}
	if err := tw.Close(); err != nil {
		return "", 0, "", protocol.Errorf(protocol.ErrInternal, "cannot finish pack: %v", err)
	}
	if err := enc.Close(); err != nil {
		return "", 0, "", protocol.Errorf(protocol.ErrInternal, "cannot finish compression: %v", err)
	}
	if err := f.Sync(); err != nil {
		return "", 0, "", protocol.Errorf(protocol.ErrInternal, "cannot flush pack: %v", err)
	}
	if err := f.Close(); err != nil {
		return "", 0, "", protocol.Errorf(protocol.ErrInternal, "cannot close pack: %v", err)
	}
	return path, counter.n, integrity.Sum(hasher), nil
}

// sampleBytes is how much of a pack is test-compressed to decide its level, and
// compressibleRatio is the size reduction below which compressing the whole pack
// is judged not worth the CPU.
const (
	sampleBytes       = 64 << 10
	compressibleRatio = 0.95
)

// compressible reports whether a sample of the pack's contents is worth
// compressing. Sampling reads at most sampleBytes and is deliberately cheap:
// being wrong costs a little CPU or a little size, never correctness. Any read
// error answers true, so the conservative path stays the default.
func compressible(spec PackSpec) bool {
	sample := make([]byte, 0, sampleBytes)
	for _, src := range spec.Files {
		if len(sample) >= sampleBytes {
			break
		}
		f, err := os.Open(src.AbsPath)
		if err != nil {
			return true
		}
		buf := make([]byte, sampleBytes-len(sample))
		n, err := io.ReadFull(f, buf)
		f.Close()
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return true
		}
		sample = append(sample, buf[:n]...)
	}
	if len(sample) < 1<<12 {
		// Too little to judge; the pack is small enough that it does not matter.
		return true
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return true
	}
	defer enc.Close()
	compressed := enc.EncodeAll(sample, nil)
	return float64(len(compressed))/float64(len(sample)) < compressibleRatio
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
