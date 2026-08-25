// Package clock provides an injectable time source so lifecycle rules stay testable.
package clock

import (
	"sync"
	"time"
)

// Clock reports the current time.
type Clock interface {
	Now() time.Time
}

// Real is the system clock.
type Real struct{}

// Now returns the current system time.
func (Real) Now() time.Time { return time.Now() }

// Fixed is a controllable clock for tests. It is safe for concurrent use so a
// test can advance time while a server is serving requests.
type Fixed struct {
	mu sync.Mutex
	t  time.Time
}

// NewFixed returns a clock stopped at t.
func NewFixed(t time.Time) *Fixed { return &Fixed{t: t} }

// Now returns the configured instant.
func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

// Advance moves the fixed clock forward.
func (f *Fixed) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}
