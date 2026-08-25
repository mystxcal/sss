package protocol

import (
	"crypto/rand"
	"strings"
)

// Alphabet is the human-safe Base32 alphabet used for handoff codes.
// It omits I, L, O, and U to avoid transcription mistakes.
const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// CodeLength is the number of alphabet characters in a code.
const CodeLength = 8

// NewCode returns a fresh random code in canonical XXXX-XXXX form.
func NewCode() string {
	var raw [CodeLength]byte
	buf := make([]byte, CodeLength)
	if _, err := rand.Read(buf); err != nil {
		panic("sss: crypto/rand unavailable: " + err.Error())
	}
	// 32 is a divisor of 256, so masking the low five bits stays uniform.
	for i, b := range buf {
		raw[i] = Alphabet[b&31]
	}
	return FormatCode(string(raw[:]))
}

// FormatCode renders eight canonical characters as XXXX-XXXX.
func FormatCode(canonical string) string {
	if len(canonical) != CodeLength {
		return canonical
	}
	return canonical[:4] + "-" + canonical[4:]
}

// NormalizeCode removes hyphens and ASCII whitespace, uppercases the rest, and
// verifies that exactly eight alphabet characters remain. It returns the
// canonical unhyphenated form.
func NormalizeCode(input string) (string, bool) {
	var b strings.Builder
	b.Grow(len(input))
	for _, r := range input {
		switch r {
		case '-', ' ', '\t', '\n', '\r', '\v', '\f':
			continue
		}
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		if r > 127 || !strings.ContainsRune(Alphabet, r) {
			return "", false
		}
		b.WriteRune(r)
	}
	s := b.String()
	if len(s) != CodeLength {
		return "", false
	}
	return s, true
}
