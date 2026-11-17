package leadunit

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Clock is the time a lifecycle reads and waits on, so that a test can drive
// it without real time.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// After returns a channel that receives the time once d has passed. The
	// wait cannot be cancelled: a caller that stops waiting leaves it behind
	// until it fires. Code that a test must be able to synchronize with uses
	// Sleep, which a context cancels, and not After.
	After(d time.Duration) <-chan time.Time
	// Sleep waits for d, or returns the context's error once it is done. It is
	// the one waiting primitive that [FakeClock.BlockUntil] counts.
	Sleep(ctx context.Context, d time.Duration) error
}

// SystemClock is the [Clock] of real time.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// After is [time.After].
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Sleep waits for d or until ctx is done.
func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FakeClock is a [Clock] that only moves when [FakeClock.Advance] is called.
// It is for tests. It is safe for concurrent use.
//
// A test learns that a goroutine is waiting with [FakeClock.BlockUntil], which
// counts the calls of [FakeClock.Sleep] and nothing else. A call of
// [FakeClock.After] cannot be cancelled, so an abandoned one would stay
// counted until an Advance reached it, and a BlockUntil meant for another
// goroutine would return early and its Advance be lost. The waiting code of a
// lifecycle therefore uses Sleep with a context.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	next    uint64
	waiters []*fakeWaiter
}

type fakeWaiter struct {
	id      uint64
	at      time.Time
	ch      chan time.Time
	sleeper bool
}

// NewFakeClock returns a fake clock that reads start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

// Now returns the fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After returns a channel that receives the fake time at which d has passed,
// once [FakeClock.Advance] has moved the clock that far. For d of 0 or less
// the channel is ready at once. The wait cannot be cancelled and is not
// counted by [FakeClock.BlockUntil] or [FakeClock.Sleepers].
func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	ch, _ := c.wait(d, false)
	return ch
}

func (c *FakeClock) wait(d time.Duration, sleeper bool) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch, func() {}
	}
	c.next++
	w := &fakeWaiter{id: c.next, at: c.now.Add(d), ch: ch, sleeper: sleeper}
	c.waiters = append(c.waiters, w)
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, x := range c.waiters {
			if x.id == w.id {
				c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
				return
			}
		}
	}
}

// Sleep waits until [FakeClock.Advance] has moved the clock by d, or returns
// the context's error once it is done.
func (c *FakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ch, cancel := c.wait(d, true)
	defer cancel()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Advance moves the clock forward by d (a d of 0 or less moves nothing) and
// wakes every waiter whose time has come, in the order of their times.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d > 0 {
		c.now = c.now.Add(d)
	}
	sort.SliceStable(c.waiters, func(i, j int) bool { return c.waiters[i].at.Before(c.waiters[j].at) })
	n := 0
	for n < len(c.waiters) && !c.waiters[n].at.After(c.now) {
		c.waiters[n].ch <- c.waiters[n].at
		n++
	}
	c.waiters = c.waiters[n:]
}

// Waiters returns how many calls of After or Sleep are waiting for the clock,
// those whose caller has stopped waiting included when they are calls of
// After. Use [FakeClock.Sleepers] to know that goroutines are waiting.
func (c *FakeClock) Waiters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// Sleepers returns how many calls of [FakeClock.Sleep] are waiting for the
// clock. A Sleep whose context is done is no longer waiting.
func (c *FakeClock) Sleepers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, w := range c.waiters {
		if w.sleeper {
			n++
		}
	}
	return n
}

// BlockUntil returns once at least n calls of [FakeClock.Sleep] are waiting
// for the clock, or once ctx is done, in which case it returns the context's
// error. A call of After is not counted: it cannot be cancelled, so an
// abandoned one would make BlockUntil return before the goroutine the test
// waits for is asleep. A test uses it to know that a goroutine has reached its
// wait before it calls [FakeClock.Advance]. It polls in real time.
func (c *FakeClock) BlockUntil(ctx context.Context, n int) error {
	for c.Sleepers() < n {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	return nil
}
