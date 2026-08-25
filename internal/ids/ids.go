// Package ids generates transfer, segment, claim, and upload identifiers plus
// receive tokens. All values come from a cryptographically secure source.
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a 128-bit lowercase hex identifier with the given prefix.
func New(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sss: crypto/rand unavailable: " + err.Error())
	}
	return prefix + hex.EncodeToString(b[:])
}

// Transfer returns a new transfer identifier.
// The manifest schema requires at least 16 characters, which this satisfies.
func Transfer() string { return New("t-") }

// Segment returns a new segment identifier.
func Segment() string { return New("s-") }

// Upload returns a new upload resource identifier.
func Upload() string { return New("u-") }

// Claim returns a new claim identifier.
func Claim() string { return New("c-") }

// Token returns a 256-bit opaque bearer token for a claim session.
func Token() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sss: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// RequestID returns a short request correlation identifier.
func RequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sss: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
