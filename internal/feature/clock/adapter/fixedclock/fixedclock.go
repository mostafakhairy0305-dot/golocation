// Package fixedclock implements clock.Clock with a time the caller sets. It
// exists so tests across several features can make staleness, freshness, and
// status timestamps deterministic instead of sleeping and hoping.
//
// It is not behind a _test.go file because more than one package's tests need
// it, and Go has no other way to share a test double.
package fixedclock

import (
	"sync"
	"time"

	clock "github.com/mostafakhairy0305-dot/golocation/internal/feature/clock/port"
)

// Clock reports whatever time it was last told. It is safe for concurrent use,
// because the code under test may read it from a background goroutine while
// the test advances it.
type Clock struct {
	mu  sync.RWMutex `exhaustruct:"optional"`
	now time.Time
}

var _ clock.Clock = (*Clock)(nil)

// New builds a Clock stopped at now, normalised to UTC.
func New(now time.Time) *Clock { return &Clock{now: now.UTC()} }

// Now returns the time the clock was last set to.
func (c *Clock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.now
}

// Set jumps the clock to an absolute time.
func (c *Clock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = now.UTC()
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}
