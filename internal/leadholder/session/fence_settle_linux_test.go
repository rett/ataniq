//go:build linux

package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Settle applies the output the reader held, on the caller's side: while the
// fence holds the lock the screen does not change until Settle runs.
func TestSettleAppliesWhatTheReaderHeldOnTheCallersSide(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; say LATE; wait")
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g") // the agent goes on and prints LATE
		until(t, "the agent's output to be held", func() bool { return s.unapplied.Load() > 0 })
		time.Sleep(100 * time.Millisecond)
		if text := screenText(s); strings.Contains(text, "LATE") {
			t.Fatalf("the reader applied output while the fence held the lock: %q", text)
		}
		settle(t, f, 0, 100*time.Millisecond, 5*time.Second)
		if text := screenText(s); !strings.Contains(text, "LATE") {
			t.Fatalf("Settle returned and the screen does not show the output: %q", text)
		}
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			if v.Pending() {
				t.Error("Pending after Settle")
			}
		})
	})
}

func TestSettleWaitsForTheMinimumAndReturnsDrainedOnAQuietAgent(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; wait")
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		begin := time.Now()
		drained, err := f.Settle(fenceCtx(t), 400*time.Millisecond, 50*time.Millisecond, 10*time.Second)
		took := time.Since(begin)
		if err != nil || !drained {
			t.Fatalf("Settle = %v, %v", drained, err)
		}
		if took < 400*time.Millisecond || took > 5*time.Second {
			t.Errorf("Settle took %v, want a little over its minimum of 400 ms", took)
		}
		// The quiet is counted from the call, not from the last output long ago.
		begin = time.Now()
		drained, err = f.Settle(fenceCtx(t), 0, 300*time.Millisecond, 10*time.Second)
		if took = time.Since(begin); err != nil || !drained || took < 300*time.Millisecond {
			t.Errorf("Settle = %v, %v after %v, want drained after the 300 ms of quiet", drained, err, took)
		}
	})
}

func TestSettleReportsNotDrainedWhenTheMaximumEndsTheWaitWithOutputArriving(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; flood 0 0 3")
	until(t, "the agent's output", func() bool { return position(s) > 0 })
	fenced(t, s, func(f Fenced) {
		begin := time.Now()
		drained, err := f.Settle(fenceCtx(t), 0, 200*time.Millisecond, 500*time.Millisecond)
		took := time.Since(begin)
		if err != nil || drained {
			t.Fatalf("Settle = %v, %v on an agent that floods, want not drained and no error", drained, err)
		}
		if took < 500*time.Millisecond || took > 10*time.Second {
			t.Errorf("Settle took %v, want about its maximum of 500 ms", took)
		}
	})
}

func TestSettleEndsWithAnErrorWrappingTheContext(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; wait")
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := f.Settle(ctx, 0, time.Second, time.Minute); !errors.Is(err, context.Canceled) {
			t.Errorf("Settle with an ended context = %v, want an error wrapping context.Canceled", err)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		begin := time.Now()
		_, err := f.Settle(ctx, time.Minute, time.Minute, time.Minute)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Settle whose context ends while it waits = %v, want an error wrapping context.DeadlineExceeded", err)
		}
		if took := time.Since(begin); took > 5*time.Second {
			t.Errorf("Settle took %v to see its context end", took)
		}
	})
}

func TestSettleErrorsWhenTheOutputStreamEnded(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; say BYE; exit 0")
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		// Keep lifecycle at its exit-status update so it cannot apply BYE
		// itself or close the session before Settle observes the stream end.
		s.fmu.Lock()
		defer s.fmu.Unlock()
		writeFrozen(t, f, "g")
		// At EOF all final bytes are in the fence's buffer.
		ctx := fenceCtx(t)
		select {
		case <-s.readDone:
		case <-ctx.Done():
			t.Fatal("the output reader did not finish")
		}
		if drained, err := f.Settle(ctx, 0, 0, time.Second); drained || !errors.Is(err, ErrStreamEnded) {
			t.Errorf("Settle = %v, %v, want false and an error wrapping ErrStreamEnded", drained, err)
		}
		if text := screenText(s); !strings.Contains(text, "BYE") {
			t.Errorf("the output before the end was not applied: %q", text)
		}
	})
}

func TestSettleInsideFrozenIsAnError(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; wait")
	fenced(t, s, func(f Fenced) {
		frozen(t, f, func(FenceView, func([]byte) error) {
			if _, err := f.Settle(fenceCtx(t), 0, 0, time.Second); err == nil {
				t.Error("Settle inside Frozen returned no error")
			}
			err := f.Frozen(time.Now().Add(time.Second), func(FenceView, func([]byte) error) error { return nil })
			if err == nil {
				t.Error("Frozen inside Frozen returned no error")
			}
		})
	})
}

// The replies the applied output caused are written, in the order the emulator
// made them, before Settle returns: the OSC hook's reply before the reply to
// the request for the device attributes that follows it.
func TestSettleWritesTheRepliesInEmulatorOrderBeforeItReturns(t *testing.T) {
	e := newEnv(t)
	var h ackHook
	s := e.start("raw; say READY; echo 1; print "+hx(testQuery+testDA1)+"; echo; wait", withHook(&h))
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g")
		settle(t, f, 0, 100*time.Millisecond, 5*time.Second)
		if n := h.calls.Load(); n != 1 {
			t.Errorf("the hook was called %d times, want 1", n)
		}
		if n := s.replies.pending(); n != 0 {
			t.Errorf("%d bytes of replies still wait in the queue after Settle returned", n)
		}
		// The agent read both replies, one after the other; its echo is
		// applied by the next Settle.
		until(t, "the agent to report the replies", func() bool {
			settle(t, f, 0, 50*time.Millisecond, 5*time.Second)
			return len(rxFromText(viewText(t, f))) >= 1+len(testAck)+len(testDA1Resp)
		})
		if got, want := string(rxFromText(viewText(t, f))), "g"+testAck+testDA1Resp; got != want {
			t.Errorf("the agent received %q, want %q", got, want)
		}
	})
}

// Many replies, more than the terminal's input buffer holds, are written by
// one Settle while the agent reads them: it does not wait for the lock it
// owns, and the queue is empty when it returns.
func TestSettleWritesMoreRepliesThanTheTerminalBuffersWithoutWaitingForItsOwnLock(t *testing.T) {
	const queries = 400 // 400 replies of 15 bytes
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; print "+hx(strings.Repeat(testDA1, queries))+"; echo; wait", bigScreen)
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g")
		begin := time.Now()
		settle(t, f, 0, 100*time.Millisecond, 30*time.Second)
		if took := time.Since(begin); took > 20*time.Second {
			t.Errorf("Settle took %v", took)
		}
		if n := s.replies.pending(); n != 0 {
			t.Errorf("%d bytes of replies still wait in the queue", n)
		}
		want := "g" + strings.Repeat(testDA1Resp, queries)
		until(t, "the agent to have read every reply", func() bool {
			settle(t, f, 0, 50*time.Millisecond, 5*time.Second)
			return len(rxFromText(viewText(t, f))) >= len(want)
		})
		if got := string(rxFromText(viewText(t, f))); got != want {
			t.Errorf("the agent received %d bytes, want %d", len(got), len(want))
		}
	})
	if c := s.Counters(); c["reply_dropped_chunks"] != 0 || c["input_broken"] != 0 {
		t.Errorf("counters %v", c)
	}
}

// An agent that asks a question and waits for the answer before it draws
// again goes on during a long Settle: the replies are written as the output
// that asked arrives, not only when the Settle ends.
func TestSettleAnswersAnAgentThatWaitsForTheAnswerWhileItWaits(t *testing.T) {
	e := newEnv(t)
	argv := "raw; say READY; echo 1; print " + hx(testDA1) + "; echo 1; say ANSWERED; wait"
	s := e.start(argv)
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g")
		// One Settle, much longer than the exchange: the agent's last line
		// arrives inside it, which needs the reply to have been written.
		settle(t, f, 2*time.Second, 100*time.Millisecond, 10*time.Second)
		if text := screenText(s); !strings.Contains(text, "ANSWERED") {
			t.Errorf("the agent was not answered during Settle: %q", text)
		}
	})
}
