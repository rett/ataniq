package doortest

import (
	"sync"
	"time"
)

// Clock is a clock a test moves by hand. Its Now method is what a test
// hands to [Config], and to whatever else in the test must agree on the
// time. A Clock is safe for use by several goroutines.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock that stands at start.
func NewClock(start time.Time) *Clock {
	return &Clock{now: start}
}

// Now returns the time the clock stands at.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set puts the clock at t, which may be earlier than where it stood.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock by d and returns the new time.
func (c *Clock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}
