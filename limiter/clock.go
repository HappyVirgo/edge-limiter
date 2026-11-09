package limiter

import (
	"sync"
	"time"
)

// Clock is the time source used by backends. Injecting it makes algorithm
// tests deterministic.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// ManualClock is a Clock that only moves when told to. Safe for concurrent use.
type ManualClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewManualClock returns a ManualClock frozen at start.
func NewManualClock(start time.Time) *ManualClock { return &ManualClock{now: start} }

// Now returns the current fake time.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
