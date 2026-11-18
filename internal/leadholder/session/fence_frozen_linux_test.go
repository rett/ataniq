//go:build linux

package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/terminalconsole/sessionpane"
)

// While Frozen runs nothing is applied: the reader keeps reading into a buffer,
// Resize, Screen and the subscriber wait, and the buffer is applied by the next
// Settle.
func TestFrozenAppliesNothingWhileTheReaderKeepsReading(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; sleep 300ms; say LATE; wait")
	waitScreen(t, s, "READY")
	var c capture
	unsub, err := s.Subscribe(c.fn)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	before := len(c.all())

	var screenReturned, resizeReturned atomic.Bool
	var wg sync.WaitGroup
	fenced(t, s, func(f Fenced) {
		frozen(t, f, func(v FenceView, write func([]byte) error) {
			if v.Pending() {
				t.Error("Pending before the agent wrote anything")
			}
			if err := write([]byte("g")); err != nil {
				t.Fatalf("write: %v", err)
			}
			first := v.Snapshot()
			wg.Add(2)
			go func() {
				defer wg.Done()
				s.Screen(func(*sessionpane.Pane, uint64) {})
				screenReturned.Store(true)
			}()
			go func() {
				defer wg.Done()
				s.Resize(90, 28)
				resizeReturned.Store(true)
			}()
			// The agent writes "RX:67" and, 300 ms later, LATE: the reader
			// reads both and applies neither.
			until(t, "the agent's output to be read", func() bool { return s.unapplied.Load() > 0 })
			until(t, "Pending", v.Pending)
			time.Sleep(500 * time.Millisecond)
			if again := v.Snapshot(); again.Text != first.Text || again.CursorX != first.CursorX || again.CursorY != first.CursorY {
				t.Errorf("the screen changed inside Frozen:\nbefore %q\nafter  %q", first.Text, again.Text)
			}
			if strings.Contains(first.Text, "LATE") || strings.Contains(first.Text, "RX:") {
				t.Errorf("output was applied: %q", first.Text)
			}
			if !v.Pending() {
				t.Error("Pending is false with the agent's output held")
			}
			if len(c.all()) != before {
				t.Error("the subscriber was called inside Frozen")
			}
			if screenReturned.Load() || resizeReturned.Load() {
				t.Error("Screen or Resize returned while Frozen ran")
			}
		})
		// Frozen is over: Screen and Resize go on, but the output is still held.
		wg.Wait()
		if !screenReturned.Load() || !resizeReturned.Load() {
			t.Error("Screen or Resize did not return after Frozen")
		}
		if text := screenText(s); strings.Contains(text, "LATE") {
			t.Errorf("the buffer was applied before Settle: %q", text)
		}
		settle(t, f, 0, 100*time.Millisecond, 5*time.Second)
		if text := screenText(s); !strings.Contains(text, "LATE") || !strings.Contains(text, "RX:67") {
			t.Errorf("the buffer was not applied by Settle: %q", text)
		}
		if len(c.all()) == before {
			t.Error("the subscriber did not see the output after Settle")
		}
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			if v.Pending() {
				t.Error("Pending after Settle")
			}
		})
	})
}

// The agent can keep writing a lot of output while the fence is frozen and
// does not block on it: the reader goes on reading into the buffer. The agent
// reports, in a file, that its flood is out, and it is inside Frozen that the
// test sees the file, with nothing applied.
func TestFrozenTheAgentCanWriteALotOfOutputWithoutBlocking(t *testing.T) {
	const flood = 3 << 20
	e := newEnv(t)
	done := filepath.Join(t.TempDir(), "flooded")
	s := e.start("raw; say READY; echo 1; flood 0 "+strconv.Itoa(flood)+" 5; pidfile "+done+"; wait", func(c *Config) { c.heldLimit = 16 << 20 })
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		frozen(t, f, func(v FenceView, write func([]byte) error) {
			if err := write([]byte("g")); err != nil {
				t.Fatal(err)
			}
			until(t, "the agent's flood to be out", func() bool {
				_, err := os.Stat(done)
				return err == nil
			})
			if text := v.Snapshot().Text; strings.Contains(text, "RX:") {
				t.Errorf("output was applied: %q", text)
			}
			if !v.Pending() {
				t.Error("Pending is false with the flood held")
			}
		})
		if c := s.Counters(); c["held_output_full"] != 0 {
			t.Errorf("the buffer was full: %v", c)
		}
		settle(t, f, 0, 100*time.Millisecond, 20*time.Second)
		if got := s.Counters()["output_bytes"]; got < flood {
			t.Errorf("output_bytes %d is less than the flood of %d: output was lost", got, flood)
		}
	})
}

// A write inside Frozen, larger than what the terminal buffers on its way to
// the agent (about 12 KB), to an agent that is itself blocked writing its
// output, does not deadlock: the agent reads its input once its output is out,
// which needs the reader to take that output meanwhile.
func TestFrozenWriteToAnAgentThatIsBlockedWritingDoesNotDeadlock(t *testing.T) {
	const flood = 2000000 // at 1 MB a second: the agent reads no input for about two seconds
	e := newEnv(t)
	payload := strings.Repeat("x", 64<<10)
	s := e.start("raw; say READY; echo 1; flood 1000000 "+strconv.Itoa(flood)+" 5; echo "+strconv.Itoa(len(payload))+"; say GOT; wait", func(c *Config) {
		c.heldLimit = 16 << 20
		c.PTY.WriteTimeout = 30 * time.Second
	})
	waitScreen(t, s, "READY")
	var c capture
	unsub, err := s.Subscribe(c.fn)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g") // the flood begins
		until(t, "the agent to have read the first byte", func() bool {
			if _, err := f.Settle(fenceCtx(t), 0, 20*time.Millisecond, 50*time.Millisecond); err != nil {
				t.Fatal(err)
			}
			return bytes.Contains(c.all(), []byte("RX:67"))
		})
		begin := time.Now()
		err := f.Frozen(time.Now().Add(30*time.Second), func(v FenceView, write func([]byte) error) error {
			return write([]byte(payload))
		})
		if err != nil {
			t.Fatalf("a write of %d bytes inside Frozen to a flooding agent: %v after %v", len(payload), err, time.Since(begin))
		}
		if took := time.Since(begin); took < 500*time.Millisecond {
			t.Errorf("the write took %v: the agent was not busy writing when it began", took)
		}
		until(t, "the agent to be done", func() bool {
			f.Settle(fenceCtx(t), 0, 50*time.Millisecond, 500*time.Millisecond)
			return strings.Contains(screenText(s), "GOT")
		})
	})
	if c := s.Counters(); c["held_output_full"] != 0 || c["input_broken"] != 0 {
		t.Errorf("counters %v", c)
	}
}

// A write past its bound returns an error, is never retried or completed, and
// no paste end is written; the input is broken after it, as after any failed
// write.
func TestFrozenWritePastItsBoundErrorsAndWritesNoPasteEnd(t *testing.T) {
	e := newEnv(t)
	// The agent does not read for a while, then reports what it received.
	s := e.start("raw; say READY; sleep 2s; echo; wait", bigScreen)
	waitScreen(t, s, "READY")
	payload := "\x1b[200~" + strings.Repeat("p", 20000) // a paste with no end
	var first, second error
	var took time.Duration
	fenced(t, s, func(f Fenced) {
		begin := time.Now()
		first = f.Frozen(time.Now().Add(400*time.Millisecond), func(v FenceView, write func([]byte) error) error {
			err := write([]byte(payload))
			// Never retried: a second write is refused and writes nothing.
			second = write([]byte("\x1b[201~"))
			return err
		})
		took = time.Since(begin)
	})
	if !errors.Is(first, os.ErrDeadlineExceeded) {
		t.Fatalf("write = %v, want an error wrapping os.ErrDeadlineExceeded", first)
	}
	if took < 350*time.Millisecond || took > 5*time.Second {
		t.Errorf("Frozen took %v, want about its bound of 400 ms", took)
	}
	if !errors.Is(second, ErrInputBroken) {
		t.Errorf("a write after the failed one = %v, want ErrInputBroken", second)
	}
	if !s.InputBroken() {
		t.Error("the input is not broken after a failed write")
	}
	if h := s.Input([]byte("z")); !errors.Is(h.Err, ErrInputBroken) {
		t.Errorf("writer input after the failed write = %v, want ErrInputBroken", h.Err)
	}
	// The agent now reads what reached it: a prefix of the paste, no end.
	var got []byte
	until(t, "the agent to report what it received", func() bool {
		got = rxFromText(screenText(s))
		return len(got) > 0
	})
	time.Sleep(300 * time.Millisecond)
	got = rxFromText(screenText(s))
	if len(got) == 0 || len(got) >= len(payload) || !bytes.HasPrefix([]byte(payload), got) {
		t.Errorf("the agent received %d bytes of %d, want a strict prefix of the paste", len(got), len(payload))
	}
	if bytes.Contains(got, []byte("\x1b[201~")) {
		t.Error("a paste end reached the agent")
	}
	if c := s.Counters(); c["input_write_errors"] != 1 {
		t.Errorf("input_write_errors = %d, want 1", c["input_write_errors"])
	}
}

// A bound that has passed before the write began writes nothing and does not
// break the input.
func TestFrozenWriteAfterItsBoundWritesNothingAndDoesNotBreakTheInput(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo; wait")
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		err := f.Frozen(time.Now().Add(-time.Second), func(_ FenceView, write func([]byte) error) error {
			return write([]byte("never"))
		})
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("write after the bound = %v, want os.ErrDeadlineExceeded", err)
		}
	})
	if s.InputBroken() {
		t.Error("the input is broken by a write that was never attempted")
	}
	s.Input([]byte("ok"))
	waitRX(t, s, "ok")
}

func TestFrozenWriteIsValidOnlyInsideItsFunctionAndReturnsTheFunctionsError(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo; wait")
	waitScreen(t, s, "READY")
	boom := errors.New("boom")
	var leaked func([]byte) error
	fenced(t, s, func(f Fenced) {
		calls := 0
		err := f.Frozen(time.Now().Add(time.Minute), func(_ FenceView, write func([]byte) error) error {
			calls++
			leaked = write
			return boom
		})
		if !errors.Is(err, boom) || calls != 1 {
			t.Errorf("Frozen = %v after %d calls, want fn's error once", err, calls)
		}
	})
	if err := leaked([]byte("late")); err == nil {
		t.Error("a write after Frozen returned succeeded")
	}
	if s.InputBroken() {
		t.Error("the input broke")
	}
}

// When the buffer is full the reader waits for room and drops nothing: the
// output arrives whole and in order once the fence takes it, the wait is
// counted, and the reader was never stopped for good.
func TestFrozenFullBufferMakesTheReaderWaitAndLosesNothing(t *testing.T) {
	const total = 1 << 20
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; flood 0 "+strconv.Itoa(total)+" 9; say DONE; wait", func(c *Config) { c.heldLimit = 32 << 10 })
	waitScreen(t, s, "READY")
	var c capture
	unsub, err := s.Subscribe(c.fn)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g")
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			until(t, "the buffer to fill", func() bool { return s.Counters()["held_output_full"] > 0 })
			if !v.Pending() {
				t.Error("Pending is false with a full buffer")
			}
			if n, err := s.host.Queued(); err != nil || n == 0 {
				// The reader is waiting for room: the master holds what the
				// agent wrote that the reader has not taken.
				t.Logf("Queued = %d, %v", n, err)
			}
		})
		until(t, "the agent to finish", func() bool {
			settle(t, f, 0, 50*time.Millisecond, 20*time.Second)
			return strings.Contains(screenText(s), "DONE")
		})
	})
	want := append([]byte("RX:67\r\n"), fakeagent.FloodBytes(9, total)...)
	want = append(want, "DONE\r\n"...)
	got := c.all()
	// The subscriber saw everything from its subscription on: after READY.
	if !bytes.Equal(got, want) {
		t.Errorf("the subscriber saw %d bytes, want %d, in order and whole (first difference at %d)", len(got), len(want), firstDiff(got, want))
	}
	if n := s.Counters()["held_output_full"]; n == 0 {
		t.Error("held_output_full is 0 after the buffer filled")
	}
}

func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// What the reader held through many short fences, while the agent writes all
// the time, reaches the emulator whole and in order: the hand-over between
// the reader, Settle and the end of a fence never reorders or loses a byte.
func TestFencesInTurnWithAFloodKeepTheOutputWholeAndInOrder(t *testing.T) {
	const total = 2 << 20
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; flood 0 " + strconv.Itoa(total) + " 11; say DONE; wait")
	waitScreen(t, s, "READY")
	var c capture
	unsub, err := s.Subscribe(c.fn)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	s.Input([]byte("g"))
	for i := 0; ; i++ {
		fenced(t, s, func(f Fenced) {
			f.Settle(fenceCtx(t), 0, 5*time.Millisecond, time.Duration(1+i%5)*10*time.Millisecond)
			frozen(t, f, func(v FenceView, _ func([]byte) error) { _ = v.Snapshot() })
		})
		if i%3 == 0 {
			time.Sleep(time.Duration(i%4) * time.Millisecond)
		}
		if len(c.all()) >= 7+total+6 {
			break
		}
		if i > 5000 {
			t.Fatalf("the output did not arrive: %d bytes", len(c.all()))
		}
	}
	want := append([]byte("RX:67\r\n"), fakeagent.FloodBytes(11, total)...)
	want = append(want, "DONE\r\n"...)
	if got := c.all(); !bytes.Equal(got, want) {
		t.Errorf("the output has %d bytes, want %d; first difference at %d", len(got), len(want), firstDiff(got, want))
	}
	if n := s.Counters()["output_bytes"]; n != uint64(len("READY\r\n")+7+total+6) {
		t.Errorf("output_bytes %d", n)
	}
}

// A session whose agent ends while a fence holds output the fence did not
// apply still has that output on its last screen: the lifetime path applies
// it before the pane ends.
func TestTheLastScreenHoldsOutputThatAFenceDidNotApply(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; say LASTWORDS; exit 0")
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g")
		select {
		case <-s.Done():
		case <-time.After(patience):
			t.Fatal("the session did not end")
		}
		if text := screenText(s); !strings.Contains(text, "LASTWORDS") {
			t.Errorf("the screen after the end lacks the held output: %q", text)
		}
	})
	rec, err := s.Final()
	if err != nil || !strings.Contains(rec.Screen.Text, "LASTWORDS") {
		t.Errorf("the final record's screen %q, %v", rec.Screen.Text, err)
	}
}

// Close does not wait for a reader that is waiting for room.
func TestCloseDuringAFenceDoesNotWaitForTheReaderThatWaitsForRoom(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; flood 0 0 4", func(c *Config) { c.heldLimit = 8 << 10 })
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g")
		until(t, "the reader to wait for room", func() bool { return s.Counters()["held_output_full"] > 0 })
		done := make(chan error, 1)
		go func() { done <- s.Close() }()
		select {
		case <-done:
		case <-time.After(patience):
			t.Fatal("Close did not return while the reader waited for room")
		}
	})
}
