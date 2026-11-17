package leadunit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

var epoch0 = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

func recv(t *testing.T, ch <-chan time.Time) (time.Time, bool) {
	t.Helper()
	select {
	case v := <-ch:
		return v, true
	default:
		return time.Time{}, false
	}
}

func TestFakeClockAfter(t *testing.T) {
	c := NewFakeClock(epoch0)
	if !c.Now().Equal(epoch0) {
		t.Fatal("Now")
	}
	a := c.After(2 * time.Second)
	b := c.After(time.Second)
	now := c.After(0)
	if _, ok := recv(t, now); !ok {
		t.Fatal("After(0) not ready")
	}
	if c.Waiters() != 2 {
		t.Fatalf("waiters %d", c.Waiters())
	}
	c.Advance(500 * time.Millisecond)
	if _, ok := recv(t, b); ok {
		t.Fatal("fired early")
	}
	c.Advance(500 * time.Millisecond)
	if v, ok := recv(t, b); !ok || !v.Equal(epoch0.Add(time.Second)) {
		t.Fatalf("b %v %v", v, ok)
	}
	if _, ok := recv(t, a); ok {
		t.Fatal("a fired early")
	}
	c.Advance(time.Hour)
	if v, ok := recv(t, a); !ok || !v.Equal(epoch0.Add(2*time.Second)) {
		t.Fatalf("a %v %v", v, ok)
	}
	if c.Waiters() != 0 || !c.Now().Equal(epoch0.Add(time.Hour+time.Second)) {
		t.Fatalf("waiters %d now %v", c.Waiters(), c.Now())
	}
	c.Advance(-time.Hour)
	if !c.Now().Equal(epoch0.Add(time.Hour + time.Second)) {
		t.Fatal("negative Advance moved the clock")
	}
}

func TestFakeClockSleep(t *testing.T) {
	c := NewFakeClock(epoch0)
	ctx := context.Background()
	if err := c.Sleep(ctx, 0); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Sleep(ctx, 5*time.Second) }()
	bctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.BlockUntil(bctx, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("Sleep returned early: %v", err)
	default:
	}
	c.Advance(4 * time.Second)
	c.Advance(time.Second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFakeClockSleepCancel(t *testing.T) {
	c := NewFakeClock(epoch0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Sleep(ctx, time.Hour) }()
	if err := c.BlockUntil(ctx, 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if c.Waiters() != 0 {
		t.Fatalf("cancelled sleeper still waits: %d", c.Waiters())
	}
	if err := c.Sleep(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("done context: %v", err)
	}
}

func TestFakeClockBlockUntilContext(t *testing.T) {
	c := NewFakeClock(epoch0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.BlockUntil(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestFakeClockConcurrent(t *testing.T) {
	c := NewFakeClock(epoch0)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Sleep(context.Background(), time.Second)
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.BlockUntil(ctx, 8); err != nil {
		t.Fatal(err)
	}
	c.Advance(time.Second)
	wg.Wait()
}

func TestSystemClock(t *testing.T) {
	var c Clock = SystemClock{}
	if c.Now().IsZero() {
		t.Fatal("zero time")
	}
	if err := c.Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.After(time.Millisecond):
	case <-time.After(10 * time.Second):
		t.Fatal("After did not fire")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
