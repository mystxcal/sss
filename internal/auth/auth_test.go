package auth

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sss/sss/internal/protocol"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash %q is not argon2id", hash)
	}
	if strings.Contains(hash, "correct horse") {
		t.Fatal("the plaintext password appears in the stored hash")
	}

	v, err := NewVerifier(hash)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	if !v.Verify("correct horse battery staple") {
		t.Error("correct password rejected")
	}
	// Repeat to exercise the accepted-password fast path.
	if !v.Verify("correct horse battery staple") {
		t.Error("correct password rejected on the cached path")
	}
	if v.Verify("wrong password") {
		t.Error("wrong password accepted")
	}
	if v.Verify("") {
		t.Error("empty password accepted")
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same password")
	b, _ := HashPassword("same password")
	if a == b {
		t.Fatal("two hashes of one password are identical; the salt is not random")
	}
}

func TestVerifierRejectsMalformedHash(t *testing.T) {
	for _, bad := range []string{
		"", "plaintext", "$argon2id$broken",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$ZGln",
	} {
		if _, err := NewVerifier(bad); err == nil {
			t.Errorf("NewVerifier(%q) accepted a malformed hash", bad)
		}
	}
}

func TestCheckBasic(t *testing.T) {
	hash, _ := HashPassword("base-password")
	v, _ := NewVerifier(hash)

	t.Run("missing credential", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/v1/info", nil)
		err := v.CheckBasic(req)
		if err == nil || err.Code != protocol.ErrAuthRequired {
			t.Fatalf("err = %v, want AUTH_REQUIRED", err)
		}
	})

	t.Run("wrong username", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/v1/info", nil)
		req.SetBasicAuth("admin", "base-password")
		err := v.CheckBasic(req)
		if err == nil || err.Code != protocol.ErrAuthInvalid {
			t.Fatalf("err = %v, want AUTH_INVALID", err)
		}
	})

	t.Run("correct credential", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/v1/info", nil)
		req.SetBasicAuth(Username, "base-password")
		if err := v.CheckBasic(req); err != nil {
			t.Fatalf("err = %v, want success", err)
		}
	})
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(3, func() time.Time { return now })

	for i := 0; i < 3; i++ {
		if !l.Allow("10.0.0.1") {
			t.Fatalf("attempt %d blocked too early", i+1)
		}
		l.Fail("10.0.0.1")
	}
	if l.Allow("10.0.0.1") {
		t.Error("limiter did not block after the configured failures")
	}
	if !l.Allow("10.0.0.2") {
		t.Error("a different address must not be penalized")
	}

	// Tokens refill over time.
	now = now.Add(time.Minute)
	if !l.Allow("10.0.0.1") {
		t.Error("limiter did not refill after a minute")
	}
}
