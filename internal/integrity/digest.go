// Package integrity provides the digest helpers used for every verification
// step. v1 uses BLAKE3-256 everywhere, rendered as lowercase hex.
package integrity

import (
	"encoding/hex"
	"hash"
	"io"
	"os"

	"lukechampine.com/blake3"
)

// bufferSize bounds the memory used per hashing operation.
const bufferSize = 1 << 20

// New returns a fresh 256-bit BLAKE3 hasher.
func New() hash.Hash { return blake3.New(32, nil) }

// Sum renders a hasher's digest as lowercase hex.
func Sum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

// Reader hashes everything read through it.
type Reader struct {
	r io.Reader
	h hash.Hash
	n int64
}

// NewReader wraps r so that bytes are hashed and counted as they are consumed.
func NewReader(r io.Reader) *Reader { return &Reader{r: r, h: New()} }

// Read implements io.Reader.
func (d *Reader) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	if n > 0 {
		d.h.Write(p[:n])
		d.n += int64(n)
	}
	return n, err
}

// Digest returns the hex digest of everything read so far.
func (d *Reader) Digest() string { return Sum(d.h) }

// Count returns the number of bytes read so far.
func (d *Reader) Count() int64 { return d.n }

// HashFile returns the digest and size of a file on disk using bounded memory.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return HashStream(f)
}

// HashStream returns the digest and length of a stream using bounded memory.
func HashStream(r io.Reader) (string, int64, error) {
	h := New()
	buf := make([]byte, bufferSize)
	n, err := io.CopyBuffer(h, r, buf)
	if err != nil {
		return "", 0, err
	}
	return Sum(h), n, nil
}

// HashBytes returns the digest of an in-memory slice.
func HashBytes(b []byte) string {
	h := New()
	h.Write(b)
	return Sum(h)
}
