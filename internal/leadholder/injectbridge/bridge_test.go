//go:build linux && ataniq_canary

package injectbridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ataniq/internal/inject"
	"ataniq/internal/inject/osc7501"
	"ataniq/internal/leadholder/session"
)

// An agent that says it is ready with bracketed paste on, and then reports
// every byte it reads. Its screen is one no recogniser knows.
const readyScript = "raw; mode paste; say READY; echo; wait"

// A pair that is not in the tested table is refused by the real fence with the
// session untouched: the result comes back while the test itself holds the
// input lock, so the lock was not asked for, and the agent has read nothing.
func TestUntestedPairIsRefusedWithTheSessionUntouched(t *testing.T) {
	s := startSession(t, readyScript)
	waitScreen(t, s, "READY")
	var tr osc7501.Tracker
	b := New(s, Config{Runtime: sampleRuntime, Version: sampleVersion, Tracker: &tr})
	if rt, v := b.Runtime(); rt != sampleRuntime || v != sampleVersion {
		t.Fatalf("Runtime = %q, %q", rt, v)
	}

	text := sampleText("msg-1")
	header, _, _ := strings.Cut(text, "\n")
	var res inject.Result
	holdingTheInputLock(t, s, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		res = inject.Deliver(ctx, b, inject.Message{Text: text, Header: header}, inject.Options{Deadline: time.Now().Add(3 * time.Second)})
	})
	if res.Outcome != inject.OutcomeUntestedVersion || res.Written != inject.WrittenNothing {
		t.Errorf("the fence answered %q, written %q (%s), want untested version, nothing", res.Outcome, res.Written, res.Detail)
	}
	if res.Before != nil || res.After != nil {
		t.Errorf("the fence read the screen: before %v, after %v", res.Before, res.After)
	}
	if s.InputBroken() {
		t.Error("the input is broken")
	}
	onlyProbeReceived(t, s)
}

// With a pair of the tested table, taken from the library at run time, the
// fence takes the lock and reads the screen, and on a screen no recogniser
// knows it writes nothing.
func TestListedPairWritesNothingOnAScreenNoRecogniserKnows(t *testing.T) {
	for _, pair := range testedPairs(t) {
		t.Run("pair", func(t *testing.T) {
			s := startSession(t, readyScript)
			waitScreen(t, s, "READY")
			var tr osc7501.Tracker
			b := New(s, Config{Runtime: pair.Runtime, Version: pair.Version, Tracker: &tr})

			text := sampleText("msg-1")
			header, _, _ := strings.Cut(text, "\n")
			res := inject.Deliver(testCtx(t), b, inject.Message{Text: text, Header: header}, inject.Options{Deadline: time.Now().Add(10 * time.Second)})
			if res.Outcome != inject.OutcomeNotIdle || res.Written != inject.WrittenNothing {
				t.Fatalf("the fence answered %q, written %q (%s), want not idle, nothing", res.Outcome, res.Written, res.Readings())
			}
			// The first check ran, through the bridge's lock and view, and saw
			// what the session shows: an unknown screen, bracketed paste on.
			if res.Before == nil {
				t.Fatalf("the fence made no first check: %s", res.Readings())
			}
			if res.Before.Screen.Known || res.Before.Passed() {
				t.Errorf("the first check read the screen as known: %s", res.Before)
			}
			if tm := res.Before.Term; !tm.ParserGround || tm.FrameOpen || !tm.BracketedPaste {
				t.Errorf("the first check read the terminal as %+v", tm)
			}
			if res.Before.Report.Has {
				t.Errorf("the first check read a report: %+v", res.Before.Report)
			}
			if res.After != nil {
				t.Errorf("the fence made a second check: %s", res.After)
			}
			if s.InputBroken() {
				t.Error("the input is broken")
			}
			onlyProbeReceived(t, s)
		})
	}
}

// An ended context keeps its meaning through the bridge: the lock is not
// taken, the function does not run and errors.Is finds the context's error.
func TestWithInputLockReturnsTheContextsErrorAndDoesNotRunTheFunction(t *testing.T) {
	s := startSession(t, readyScript)
	b := New(s, Config{Runtime: sampleRuntime, Version: sampleVersion})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	for name, ctx := range map[string]context.Context{"cancelled": cancelled, "past its deadline": expired} {
		ran := false
		err := b.WithInputLock(ctx, func(inject.Locked) error { ran = true; return nil })
		if err == nil || !errors.Is(err, ctx.Err()) {
			t.Errorf("%s: WithInputLock = %v, want an error that wraps %v", name, err, ctx.Err())
		}
		if ran {
			t.Errorf("%s: the function ran", name)
		}
	}

	// A context that ends while the lock is held by another waits no longer.
	holdingTheInputLock(t, s, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		ran := false
		err := b.WithInputLock(ctx, func(inject.Locked) error { ran = true; return nil })
		if !errors.Is(err, context.DeadlineExceeded) || ran {
			t.Errorf("WithInputLock behind a held lock = %v, ran %t, want the deadline's error and no run", err, ran)
		}
	})
}

// Settle's error for an ended context wraps the context's error, and the
// function's own error comes back as it is.
func TestErrorsOfSettleAndOfTheFunctionAreReturnedUnchanged(t *testing.T) {
	s := startSession(t, readyScript)
	waitScreen(t, s, "READY")
	b := New(s, Config{Runtime: sampleRuntime, Version: sampleVersion})

	own := errors.New("the function's own error")
	err := b.WithInputLock(testCtx(t), func(l inject.Locked) error {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := l.Settle(ctx, 0, 10*time.Millisecond, time.Second); !errors.Is(err, context.Canceled) {
			t.Errorf("Settle with an ended context = %v, want an error that wraps context.Canceled", err)
		}
		frozenErr := l.Frozen(time.Now().Add(patience), func(inject.View, func([]byte) error) error { return own })
		if frozenErr != own {
			t.Errorf("Frozen = %v, want the function's error", frozenErr)
		}
		return own
	})
	if err != own {
		t.Errorf("WithInputLock = %v, want the function's error", err)
	}
}

// A session that is over is a session error, not a context's: the lock
// function does not run and the error is the session's.
func TestWithInputLockOnAClosedSessionReturnsTheSessionsError(t *testing.T) {
	s := startSession(t, readyScript)
	b := New(s, Config{Runtime: sampleRuntime, Version: sampleVersion})
	s.Close()
	ran := false
	err := b.WithInputLock(testCtx(t), func(inject.Locked) error { ran = true; return nil })
	if !errors.Is(err, session.ErrClosed) || ran {
		t.Errorf("WithInputLock = %v, ran %t, want session.ErrClosed and no run", err, ran)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the session's error reads as a context's: %v", err)
	}
}

// A bridge with no session, and a nil bridge, fail without a panic.
func TestBridgeWithoutASession(t *testing.T) {
	var none *Bridge
	for name, b := range map[string]*Bridge{"no session": New(nil, Config{}), "nil": none} {
		ran := false
		err := b.WithInputLock(context.Background(), func(inject.Locked) error { ran = true; return nil })
		if !errors.Is(err, ErrNoSession) || ran {
			t.Errorf("%s: WithInputLock = %v, ran %t", name, err, ran)
		}
		if rt, v := b.Runtime(); rt != "" || v != "" {
			t.Errorf("%s: Runtime = %q, %q", name, rt, v)
		}
	}
}

// The view hands on what the session's view says, and the write of a Frozen
// call reaches the agent as one write.
func TestViewAndWriteAreTheSessions(t *testing.T) {
	s := startSession(t, readyScript)
	waitScreen(t, s, "READY")
	b := New(s, Config{Runtime: sampleRuntime, Version: sampleVersion})

	inFrozen(t, b, func(v inject.View, write func([]byte) error) {
		if tm := v.Term(); !tm.ParserGround || tm.FrameOpen || !tm.BracketedPaste {
			t.Errorf("Term = %+v, want ground, no frame, bracketed paste", tm)
		}
		if v.Ended() {
			t.Error("Ended is true for a running agent")
		}
		if v.Pending() {
			t.Error("Pending is true after a Settle with a quiet agent")
		}
		if q := v.OwnerQuiet(); q != session.OwnerNeverTyped {
			t.Errorf("OwnerQuiet = %v with a writer who never typed", q)
		}
		if q := v.ScreenQuiet(); q < 0 || q > patience {
			t.Errorf("ScreenQuiet = %v", q)
		}
		if got := v.Snapshot(); !strings.Contains(got.Text, "READY") || got.Cols != 100 || got.Rows != 30 {
			t.Errorf("Snapshot = %dx%d %q", got.Cols, got.Rows, got.Text)
		}
		if err := write([]byte("one write")); err != nil {
			t.Errorf("write: %v", err)
		}
	})
	waitReceived(t, s, "one write")

	s.Input([]byte("k"))
	inFrozen(t, b, func(v inject.View, _ func([]byte) error) {
		if q := v.OwnerQuiet(); q < 0 || q > patience {
			t.Errorf("OwnerQuiet = %v after the writer typed", q)
		}
	})

	// With a frame open and bracketed paste off the view says so.
	s2 := startSession(t, "raw; frameopen "+hx("HALF")+"; wait")
	waitScreen(t, s2, "HALF")
	inFrozen(t, New(s2, Config{}), func(v inject.View, _ func([]byte) error) {
		if tm := v.Term(); !tm.FrameOpen || tm.BracketedPaste {
			t.Errorf("Term = %+v, want an open frame and no bracketed paste", tm)
		}
	})
}

// A write that fails breaks the input, and the session says so to every later
// Settle: the bridge passes that error on as the session made it.
func TestAfterAFailedWriteSettleReturnsTheBrokenInputError(t *testing.T) {
	s, b := brokenSession(t, Config{Runtime: sampleRuntime, Version: sampleVersion})
	err := b.WithInputLock(testCtx(t), func(l inject.Locked) error {
		_, err := l.Settle(testCtx(t), 0, 10*time.Millisecond, time.Second)
		return err
	})
	if !errors.Is(err, session.ErrInputBroken) {
		t.Errorf("Settle after a failed write = %v, want an error that wraps session.ErrInputBroken", err)
	}
	if !s.InputBroken() {
		t.Error("the session does not say that its input is broken")
	}
}

// brokenSession returns a session whose input a failed write has broken, with
// its bridge: the agent never reads, and one write larger than the terminal's
// input buffer does not finish by its bound.
func brokenSession(t *testing.T, cfg Config) (*session.Session, *Bridge) {
	t.Helper()
	s := startSession(t, "raw; mode paste; say READY; wait")
	waitScreen(t, s, "READY")
	b := New(s, cfg)
	var writeErr error
	err := b.WithInputLock(testCtx(t), func(l inject.Locked) error {
		return l.Frozen(time.Now().Add(300*time.Millisecond), func(_ inject.View, write func([]byte) error) error {
			writeErr = write([]byte(strings.Repeat("x", 1<<20)))
			return nil
		})
	})
	if err != nil {
		t.Fatalf("WithInputLock: %v", err)
	}
	if writeErr == nil {
		t.Fatal("a write the agent never reads succeeded")
	}
	if !s.InputBroken() {
		t.Fatalf("the failed write (%v) did not break the input", writeErr)
	}
	return s, b
}
