//go:build linux

package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The lock holds writer input for the whole of fn and delivers it after, in
// the order it came; nothing reached the agent while fn ran.
func TestWithFenceHoldsWriterInputAndDeliversItInOrderAfterFn(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo; wait")
	waitScreen(t, s, "READY")
	var wg sync.WaitGroup
	var returned atomic.Int32
	typed := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
			returned.Add(1)
		}()
		time.Sleep(60 * time.Millisecond) // it waits for the lock before the next one starts
	}
	fenced(t, s, func(f Fenced) {
		typed(func() { s.Input([]byte("1")) })
		typed(func() { s.Paste("2") })
		typed(func() { s.Input([]byte("3")) })
		time.Sleep(200 * time.Millisecond)
		if n := returned.Load(); n != 0 {
			t.Errorf("%d of the writer's inputs returned while the lock was held", n)
		}
		settle(t, f, 100*time.Millisecond, 50*time.Millisecond, 5*time.Second)
		if text := viewText(t, f); strings.Contains(text, "RX:") {
			t.Errorf("the agent received input while the lock was held: %q", text)
		}
	})
	wg.Wait()
	waitRX(t, s, "123")
}

// Keys typed while the lock is held count for OwnerQuiet from when they were
// typed, and a writer that has typed nothing is quiet for as long as can be
// said.
func TestWithFenceKeysTypedWhileHeldCountFromWhenTyped(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo; wait")
	waitScreen(t, s, "READY")
	var wg sync.WaitGroup
	fenced(t, s, func(f Fenced) {
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			if q := v.OwnerQuiet(); q != OwnerNeverTyped {
				t.Errorf("OwnerQuiet = %v before any input, want OwnerNeverTyped", q)
			}
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Input([]byte("k"))
		}()
		until(t, "the input to be noted", func() bool { return !s.LastWriterInput().IsZero() })
		time.Sleep(300 * time.Millisecond)
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			q := v.OwnerQuiet()
			if q < 280*time.Millisecond || q > 10*time.Second {
				t.Errorf("OwnerQuiet = %v for a key typed 300 ms ago and still held, want about 300 ms", q)
			}
		})
		// The key was not delivered meanwhile.
		settle(t, f, 0, 50*time.Millisecond, 5*time.Second)
		if text := viewText(t, f); strings.Contains(text, "RX:") {
			t.Errorf("the key reached the agent inside the fence: %q", text)
		}
	})
	wg.Wait()
	waitRX(t, s, "k")
}

func TestWithFenceEndedContextDoesNotRunFn(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; wait")

	// An ended context with the lock free.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := 0
	err := s.WithFence(ctx, func(Fenced) error { ran++; return nil })
	if !errors.Is(err, context.Canceled) || ran != 0 {
		t.Fatalf("WithFence with an ended context = %v (ran %d), want an error wrapping context.Canceled and no run", err, ran)
	}

	// The lock held by another critical section while the context ends.
	hold, release := make(chan struct{}), make(chan struct{})
	go s.WithInputLock(func(AgentWriter) error {
		close(hold)
		<-release
		return nil
	})
	<-hold
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = s.WithFence(ctx, func(Fenced) error { ran++; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || ran != 0 {
		t.Fatalf("WithFence with a context that ends while waiting = %v (ran %d), want an error wrapping context.DeadlineExceeded and no run", err, ran)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("WithFence took %v to give up", took)
	}
	close(release)

	// The lock is not left taken, and fn runs once.
	calls := 0
	if err := s.WithFence(fenceCtx(t), func(Fenced) error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatalf("WithFence after that = %v, ran %d times", err, calls)
	}
	if h := s.Input([]byte("x")); h.Err != nil {
		t.Errorf("Input after the fences: %v", h.Err)
	}
}

func TestWithFenceReturnsFnsErrorAndRefusesOnceTheSessionIsOver(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; wait")
	boom := errors.New("boom")
	if err := s.WithFence(fenceCtx(t), func(Fenced) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("WithFence = %v, want fn's error", err)
	}
	// The fence's calls are valid only inside fn.
	var leaked Fenced
	s.WithFence(fenceCtx(t), func(f Fenced) error { leaked = f; return nil })
	if _, err := leaked.Settle(fenceCtx(t), 0, 0, 0); err == nil {
		t.Error("Settle outside its fence returned no error")
	}
	if err := leaked.Frozen(time.Now().Add(time.Second), func(FenceView, func([]byte) error) error { return nil }); err == nil {
		t.Error("Frozen outside its fence returned no error")
	}
	s.Stop()
	waitDone(t, s)
	called := false
	if err := s.WithFence(fenceCtx(t), func(Fenced) error { called = true; return nil }); !errors.Is(err, ErrClosed) || called {
		t.Errorf("WithFence on a session that is over = %v (ran %v), want ErrClosed", err, called)
	}
}

// The existing critical section still excludes the fence and the other way
// round: both are the one input lock.
func TestWithFenceAndWithInputLockExcludeEachOther(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; wait")
	var inFence, inLock atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				s.WithFence(fenceCtx(t), func(Fenced) error {
					if inFence.Add(1) != 1 || inLock.Load() != 0 {
						t.Error("two critical sections at once")
					}
					time.Sleep(20 * time.Millisecond)
					inFence.Add(-1)
					return nil
				})
				return
			}
			s.WithInputLock(func(AgentWriter) error {
				if inLock.Add(1) != 1 || inFence.Load() != 0 {
					t.Error("two critical sections at once")
				}
				time.Sleep(20 * time.Millisecond)
				inLock.Add(-1)
				return nil
			})
		}(i)
	}
	wg.Wait()
}
