// Package auth implements the single shared base password: argon2id hashing at
// rest, constant-time verification, and failed-attempt rate limiting.
//
// The plaintext password is never stored, logged, or included in diagnostics.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/sss/sss/internal/protocol"
)

// Username is fixed for every public operation in v1.
const Username = "sss"

// Argon2id parameters. These are deliberately memory-hard; the verifier caches
// successful results so a hot upload path does not pay the cost per request.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword returns an encoded argon2id hash of the password.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password must not be empty")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

type params struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	key     []byte
}

func parseHash(encoded string) (params, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return params{}, fmt.Errorf("password_hash is not an argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return params{}, fmt.Errorf("password_hash has an unreadable version")
	}
	if version != argon2.Version {
		return params{}, fmt.Errorf("password_hash uses argon2 version %d, want %d", version, argon2.Version)
	}
	var p params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return params{}, fmt.Errorf("password_hash has unreadable parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return params{}, fmt.Errorf("password_hash has an unreadable salt")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return params{}, fmt.Errorf("password_hash has an unreadable digest")
	}
	p.salt, p.key = salt, key
	return p, nil
}

// Verifier checks base passwords against a stored hash.
type Verifier struct {
	p params

	mu       sync.RWMutex
	accepted [sha256.Size]byte // digest of the last accepted password
	hasCache bool
}

// NewVerifier parses the configured hash once at startup.
func NewVerifier(encodedHash string) (*Verifier, error) {
	p, err := parseHash(encodedHash)
	if err != nil {
		return nil, err
	}
	return &Verifier{p: p}, nil
}

// Verify reports whether the supplied password matches the configured hash.
//
// A successful password's SHA-256 is remembered so repeated requests avoid a
// 64 MiB argon2 derivation each time. Only a digest of an already-accepted
// secret is held, and it never leaves the process.
func (v *Verifier) Verify(password string) bool {
	sum := sha256.Sum256([]byte(password))
	v.mu.RLock()
	cached := v.hasCache && subtle.ConstantTimeCompare(sum[:], v.accepted[:]) == 1
	v.mu.RUnlock()
	if cached {
		return true
	}
	key := argon2.IDKey([]byte(password), v.p.salt, v.p.time, v.p.memory, v.p.threads, uint32(len(v.p.key)))
	if subtle.ConstantTimeCompare(key, v.p.key) != 1 {
		return false
	}
	v.mu.Lock()
	v.accepted = sum
	v.hasCache = true
	v.mu.Unlock()
	return true
}

// CheckBasic validates an Authorization header value. It returns a stable error
// distinguishing a missing credential from a rejected one.
func (v *Verifier) CheckBasic(r *http.Request) *protocol.Error {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return protocol.Errorf(protocol.ErrAuthRequired, "authentication required")
	}
	if subtle.ConstantTimeCompare([]byte(user), []byte(Username)) != 1 {
		// Still run the password derivation path so a wrong username is not
		// distinguishable by timing from a wrong password.
		v.Verify(pass)
		return protocol.Errorf(protocol.ErrAuthInvalid, "credential rejected")
	}
	if !v.Verify(pass) {
		return protocol.Errorf(protocol.ErrAuthInvalid, "credential rejected")
	}
	return nil
}

// Limiter rate-limits failed authentication attempts per client address.
type Limiter struct {
	perMinute int
	mu        sync.Mutex
	buckets   map[string]*bucket
	now       func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter builds a limiter allowing perMinute failures per address.
func NewLimiter(perMinute int, now func() time.Time) *Limiter {
	if perMinute <= 0 {
		perMinute = 20
	}
	if now == nil {
		now = time.Now
	}
	return &Limiter{perMinute: perMinute, buckets: map[string]*bucket{}, now: now}
}

// Allow reports whether another attempt from key may be processed.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) > 4096 {
			l.evictLocked(now)
		}
		b = &bucket{tokens: float64(l.perMinute), last: now}
		l.buckets[key] = b
	}
	refill := now.Sub(b.last).Minutes() * float64(l.perMinute)
	b.tokens = min(float64(l.perMinute), b.tokens+refill)
	b.last = now
	return b.tokens >= 1
}

// Fail records a failed attempt against key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(l.perMinute), last: l.now()}
		l.buckets[key] = b
	}
	b.tokens--
	if b.tokens < 0 {
		b.tokens = 0
	}
}

func (l *Limiter) evictLocked(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last) > 10*time.Minute {
			delete(l.buckets, k)
		}
	}
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
