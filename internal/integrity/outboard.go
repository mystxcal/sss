package integrity

import (
	"encoding/hex"
	"io"
	"os"

	"lukechampine.com/blake3/bao"
)

// GroupLog is the Bao group size as a power of two chunks. BLAKE3 chunks are
// 1 KiB, so group 6 gives 64 KiB verification groups: small enough that a
// corrupt range costs one group to re-fetch, large enough that the outboard
// tree is about 0.1% of the payload and SIMD hashing still applies.
const GroupLog = 6

// GroupSize is the number of payload bytes covered by one verification group.
const GroupSize = 1024 << GroupLog

// OutboardSize returns the exact byte length of the outboard tree for a payload
// of the given length. Callers preallocate with it because Bao encoding writes
// out of order.
func OutboardSize(dataLen int64) int64 {
	return int64(bao.EncodedSize(int(dataLen), GroupLog, true))
}

// WriteOutboard computes the Bao verification tree for r and writes it to dst,
// returning the tree root as lowercase hex.
//
// The root is exactly the BLAKE3-256 digest of the same bytes, so an outboard
// tree can be added to an existing segment without changing its identity: the
// digest already recorded in the manifest is the root that verifies every slice
// of it. That is what makes verified streaming additive rather than a protocol
// break.
func WriteOutboard(dst io.WriterAt, r io.Reader, dataLen int64) (string, error) {
	root, err := bao.Encode(dst, r, dataLen, GroupLog, true)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(root[:]), nil
}

// OutboardFile computes the verification tree for a file on disk and writes it
// to outboardPath, returning the root as lowercase hex and the payload size.
//
// It returns the same digest and size HashFile would, in the same single pass
// over the data, so a caller that already hashes a file to verify it can obtain
// the tree for free by calling this instead.
func OutboardFile(path, outboardPath string) (string, int64, error) {
	src, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return "", 0, err
	}
	size := info.Size()
	dst, err := os.OpenFile(outboardPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, err
	}
	// Bao writes out of order, so the file must already be the full size.
	if err := dst.Truncate(OutboardSize(size)); err != nil {
		dst.Close()
		return "", 0, err
	}
	root, err := WriteOutboard(dst, src, size)
	if err != nil {
		dst.Close()
		return "", 0, err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		return "", 0, err
	}
	return root, size, dst.Close()
}

// VerifyGroup reports whether data is the authentic payload at offset, proved
// against root using the outboard tree. offset must be a multiple of GroupSize,
// and data must be exactly one group unless it is the final one.
//
// This is the operation whole-payload hashing cannot do: it needs neither the
// preceding bytes nor the following ones, so groups may be verified as they
// arrive, out of order, from a source that is not trusted.
func VerifyGroup(data, outboard []byte, offset int64, rootHex string) bool {
	root, err := hex.DecodeString(rootHex)
	if err != nil || len(root) != 32 {
		return false
	}
	var r [32]byte
	copy(r[:], root)
	return bao.VerifyChunk(data, outboard, GroupLog, uint64(offset), r)
}
