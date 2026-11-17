//go:build linux

package session

import (
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/terminalconsole/sessionpane"
)

func TestCtrlZIsNeverForwardedRawOrCooked(t *testing.T) {
	for _, mode := range []string{"raw", "cooked"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			s := e.start(mode + "; say READY; echo; wait")
			waitScreen(t, s, "READY")
			in, want := []byte("ab\x1acd\n"), "abcd\n"
			if mode == "raw" {
				in, want = []byte("ab\x1acd"), "abcd"
			}
			h := s.Input(in)
			if h.Err != nil || h.Suspend != 1 || h.Signal != 0 || !h.Any() {
				t.Fatalf("Held %+v, want one suspend", h)
			}
			if h.Written != len(in)-1 {
				t.Errorf("Written %d, want %d", h.Written, len(in)-1)
			}
			waitRX(t, s, want)
			if gone := s.Facts().TUIExited; gone != No {
				t.Errorf("the agent is gone: %v", gone)
			}
			if n := s.Counters()["held_keys_suspend"]; n != 1 {
				t.Errorf("held_keys_suspend = %d", n)
			}
		})
	}
}

// With ISIG set the bytes for VINTR, VQUIT and VSUSP are held back, and the
// agent survives (case H12). Without ISIG (raw) they are input.
func TestSignalKeysHeldWhenCookedAndForwardedWhenRaw(t *testing.T) {
	t.Run("cooked", func(t *testing.T) {
		e := newEnv(t)
		s := e.start("cooked; say READY; echo; wait")
		waitScreen(t, s, "READY")
		h := s.Input([]byte("a\x03\x1c\x1ab\n"))
		if h.Err != nil || h.Signal != 2 || h.Suspend != 1 {
			t.Fatalf("Held %+v, want 2 signal and 1 suspend", h)
		}
		waitRX(t, s, "ab\n")
		// It lives: more input is read.
		s.Input([]byte("c\n"))
		waitRX(t, s, "ab\nc\n")
		if gone := s.Facts().TUIExited; gone != No {
			t.Fatalf("the agent died: %v", s.Facts())
		}
		c := s.Counters()
		if c["held_keys_signal"] != 2 || c["held_keys_suspend"] != 1 {
			t.Errorf("counters %v", c)
		}
		var kinds []string
		for _, ev := range eventsNamed(e.events(), eventlog.EventKeyHeld) {
			kinds = append(kinds, ev.Reason)
		}
		if got := strings.Join(kinds, " "); got != "suspend signal" && got != "signal suspend" {
			t.Errorf("key-held events %q", got)
		}
	})
	t.Run("raw", func(t *testing.T) {
		e := newEnv(t)
		s := e.start("raw; say READY; echo; wait")
		waitScreen(t, s, "READY")
		h := s.Input([]byte("\x03\x1c"))
		if h.Err != nil || h.Any() || h.Written != 2 {
			t.Fatalf("Held %+v, want nothing held", h)
		}
		waitRX(t, s, "\x03\x1c")
	})
}

// The terminal's settings are read at each input: ISIG toggled by the agent
// changes what is held.
func TestISIGIsReadAtEachInput(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; isig on; say READY1; echo 2; isig off; say READY2; echo 2; wait")
	waitScreen(t, s, "READY1")
	h := s.Input([]byte("\x03ab"))
	if h.Signal != 1 || h.Written != 2 {
		t.Fatalf("with ISIG on: %+v", h)
	}
	waitScreen(t, s, "READY2")
	h = s.Input([]byte("\x03c"))
	if h.Any() || h.Written != 2 {
		t.Fatalf("with ISIG off: %+v", h)
	}
	waitRX(t, s, "ab\x03c")
}

func TestPasteWrapsInBracketedPasteWhenTheAgentAskedForIt(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; mode paste; say READY; echo; wait")
	waitScreen(t, s, "READY")
	var modes sessionpane.Modes
	s.Screen(func(p *sessionpane.Pane, _ uint64) { modes = p.Modes() })
	if !modes.BracketedPaste {
		t.Fatal("the session did not see mode 2004")
	}
	h := s.Paste("one\x1b[201~two\x1b[2\x1b[201~01~three")
	if h.Err != nil || h.Any() {
		t.Fatalf("Held %+v", h)
	}
	want := "\x1b[200~" + "onetwothree" + "\x1b[201~"
	waitRX(t, s, want)
	// The paste text cannot end the paste early.
	if strings.Count(string(rxBytes(screenText(s))), "\x1b[201~") != 1 {
		t.Error("a paste-end sequence from the text reached the agent")
	}
}

func TestPasteIsPlainWithoutMode2004(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo; wait")
	waitScreen(t, s, "READY")
	h := s.Paste("a\x1b[201~b")
	if h.Err != nil || h.Any() {
		t.Fatalf("Held %+v", h)
	}
	waitRX(t, s, "ab")
}

// Held keys are taken out of pasted text too, before the paste-end removal: a
// held byte inside a paste-end sequence cannot hide it.
func TestPasteHoldsKeysAndRemovesPasteEndAfterwards(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; isig on; mode paste; say READY; echo; wait")
	waitScreen(t, s, "READY")
	h := s.Paste("x\x03\x1b[201\x1a~y\x1c")
	if h.Signal != 2 || h.Suspend != 1 {
		t.Fatalf("Held %+v", h)
	}
	waitRX(t, s, "\x1b[200~xy\x1b[201~")
	// A paste with nothing left writes nothing.
	h = s.Paste("\x03\x1b[201~")
	if h.Written != 0 || h.Err != nil {
		t.Errorf("empty paste: %+v", h)
	}
}

func TestStripPasteEnd(t *testing.T) {
	for _, c := range []struct{ in, out string }{
		{"", ""},
		{"plain", "plain"},
		{"\x1b[201~", ""},
		{"a\x1b[201~b\x1b[201~", "ab"},
		{"\x1b[2\x1b[201~01~", ""},
		{"\x1b[\x1b[2\x1b[201~01~201~x", "x"},
		{"\x1b[200~", "\x1b[200~"},
		{"\x1b[201", "\x1b[201"},
		{"~~\x1b[201~~", "~~~"},
	} {
		if got := string(stripPasteEnd([]byte(c.in))); got != c.out {
			t.Errorf("stripPasteEnd(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}

func TestInjectIsDisabledAndLogged(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; wait")
	waitScreen(t, s, "READY")
	err := s.Inject(InjectRequest{MessageID: "msg-1", Sender: "owner", Digest: "sha256:abcd"})
	if !errors.Is(err, ErrInjectionDisabled) {
		t.Fatalf("Inject = %v", err)
	}
	evs := eventsNamed(e.events(), eventlog.EventInject)
	if len(evs) != 1 || evs[0].Reason != "disabled" || evs[0].MessageID != "msg-1" || evs[0].Sender != "owner" || evs[0].Digest != "sha256:abcd" {
		t.Fatalf("inject events %+v", evs)
	}
	if rx := rxBytes(screenText(s)); len(rx) != 0 {
		t.Errorf("the agent received %q", rx)
	}
}

// Writer input, and the replies, wait while a critical section holds the
// input lock; the section's own writes are the only bytes in between.
func TestWithInputLockExcludesWriterInput(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo; wait")
	waitScreen(t, s, "READY")
	if !s.LastWriterInput().IsZero() {
		t.Fatal("LastWriterInput before any input")
	}

	entered, release := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	var kept AgentWriter
	secDone := make(chan error, 1)
	go func() {
		secDone <- s.WithInputLock(func(w AgentWriter) error {
			kept = w
			close(entered)
			if _, err := w.Write([]byte("AA")); err != nil {
				return err
			}
			w.Screen(func(p *sessionpane.Pane, pos uint64) {
				if pos == 0 || !strings.Contains(paneText(p), "READY") {
					t.Errorf("the writer's screen is not the current one (pos %d)", pos)
				}
			})
			<-release
			return nil
		})
	}()
	<-entered
	inputDone := make(chan Held, 1)
	go func() {
		h := s.Input([]byte("B"))
		if !released.Load() {
			t.Error("Input returned while the critical section held the lock")
		}
		inputDone <- h
	}()
	pasteDone := make(chan Held, 1)
	go func() {
		h := s.Paste("C")
		if !released.Load() {
			t.Error("Paste returned while the critical section held the lock")
		}
		pasteDone <- h
	}()
	// LastWriterInput records the attempt at once, even while it waits.
	poll(t, func() string { return "the input attempt to be recorded" }, func() bool { return !s.LastWriterInput().IsZero() })
	select {
	case <-inputDone:
		t.Fatal("Input returned under the lock")
	case <-pasteDone:
		t.Fatal("Paste returned under the lock")
	case <-time.After(100 * time.Millisecond):
	}
	released.Store(true)
	close(release)
	if err := <-secDone; err != nil {
		t.Fatal(err)
	}
	if h := <-inputDone; h.Err != nil {
		t.Fatal(h.Err)
	}
	if h := <-pasteDone; h.Err != nil {
		t.Fatal(h.Err)
	}
	// "AA" came first; "B" and "C" follow in either order.
	poll(t, func() string { return "all four bytes" }, func() bool { return len(rxBytes(screenText(s))) >= 4 })
	if got := string(rxBytes(screenText(s))); got != "AABC" && got != "AACB" {
		t.Errorf("the agent received %q", got)
	}
	if _, err := kept.Write([]byte("Z")); err == nil {
		t.Error("the section's writer works after the section")
	}
}

func TestInputAfterCloseIsRefused(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; wait")
	waitScreen(t, s, "READY")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if h := s.Input([]byte("a")); !errors.Is(h.Err, ErrClosed) {
		t.Errorf("Input after Close: %+v", h)
	}
	if h := s.Paste("a"); !errors.Is(h.Err, ErrClosed) {
		t.Errorf("Paste after Close: %+v", h)
	}
	if err := s.WithInputLock(func(AgentWriter) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Errorf("WithInputLock after Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// Once the session is over (the final record is written) input from every
// source is refused with ErrClosed and nothing reaches the terminal, even while
// a grandchild that never reads still holds it: a write would time out and
// break the input after the record said it was whole.
func TestInputAfterTheSessionIsOverIsRefused(t *testing.T) {
	e := newEnv(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	s := e.start("raw; say READY; spawn same "+pidFile+" ttl=60s; exit 0", func(c *Config) {
		shortWriteTimeout(c)
		c.StreamWait = 300 * time.Millisecond
		c.TreeWait = 300 * time.Millisecond
	})
	waitDone(t, s)
	if f := s.Facts(); f.TUIExited != Yes || f.StreamEnded != No {
		t.Fatalf("facts %+v: the terminal should still be held by the grandchild", f)
	}
	if s.closed.Load() {
		t.Fatal("the session is closed")
	}

	start := time.Now()
	if h := s.Input([]byte(bigInput)); !errors.Is(h.Err, ErrClosed) || h.Written != 0 {
		t.Errorf("Input after the end = %+v", h)
	}
	if h := s.Paste(bigInput); !errors.Is(h.Err, ErrClosed) || h.Written != 0 {
		t.Errorf("Paste after the end = %+v", h)
	}
	called := false
	if err := s.WithInputLock(func(AgentWriter) error { called = true; return nil }); !errors.Is(err, ErrClosed) || called {
		t.Errorf("WithInputLock after the end: %v (called %v)", err, called)
	}
	// A reply that waits for the lock is dropped, not written. The write
	// would block for the whole timeout, and break the input.
	s.replies.push([]byte(bigInput))
	poll(t, func() string { return "the reply queue to be taken" }, func() bool { return s.replies.pending() == 0 })
	s.imu.Lock() // the reply goroutine has finished with the batch
	s.imu.Unlock()
	if time.Since(start) > 250*time.Millisecond {
		t.Errorf("the refusals took %v: something was written", time.Since(start))
	}

	cnt := s.Counters()
	if s.InputBroken() || cnt["input_broken"] != 0 || cnt["input_write_errors"] != 0 || cnt["reply_write_errors"] != 0 {
		t.Errorf("the input broke after the end; counters %v", cnt)
	}
	rec, err := s.Final()
	if err != nil || rec.Counters["input_broken"] != 0 {
		t.Errorf("the final record: %v, counters %v", err, rec.Counters)
	}
	for _, ev := range eventsNamed(e.events(), eventlog.EventState) {
		if ev.State == "input-broken" {
			t.Errorf("an input-broken event after the end: %+v", ev)
		}
	}
}

// bigInput is more than the terminal's input buffer holds, so that a write to
// an agent that does not read cannot finish.
var bigInput = strings.Repeat("x", 64<<10)

func shortWriteTimeout(c *Config) { c.PTY.WriteTimeout = 300 * time.Millisecond }

// The first failed or timed-out write from any source breaks the input: every
// later input, from every source, is refused and logged once, and counted.
func TestAFailedWriteBreaksTheInputForEverySource(t *testing.T) {
	queries := filepath.Join(t.TempDir(), "queries")
	if err := os.WriteFile(queries, []byte(strings.Repeat("\x1b[c", 40000)), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, script, reason string
		trigger              func(t *testing.T, s *Session)
	}{
		{"writer", "raw; say READY; wait", "writer-write-timeout", func(t *testing.T, s *Session) {
			if h := s.Input([]byte(bigInput)); !errors.Is(h.Err, os.ErrDeadlineExceeded) || h.Written >= len(bigInput) {
				t.Fatalf("Input = %+v, want a timeout and a prefix", h)
			}
		}},
		{"paste", "raw; say READY; wait", "paste-write-timeout", func(t *testing.T, s *Session) {
			if h := s.Paste(bigInput); !errors.Is(h.Err, os.ErrDeadlineExceeded) || h.Written >= len(bigInput) {
				t.Fatalf("Paste = %+v, want a timeout and a prefix", h)
			}
		}},
		{"section", "raw; say READY; wait", "section-write-timeout", func(t *testing.T, s *Session) {
			err := s.WithInputLock(func(w AgentWriter) error {
				_, err := w.Write([]byte(bigInput))
				return err
			})
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("WithInputLock = %v, want a timeout", err)
			}
		}},
		{"replies", "raw; say READY; printfile " + queries + "; say AFTER; wait", "reply-write-timeout", func(t *testing.T, s *Session) {
			poll(t, func() string { return fmt.Sprintf("the reply write to fail; counters %v", s.Counters()) }, s.InputBroken)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			s := e.start(c.script, shortWriteTimeout)
			waitScreen(t, s, "READY")
			if s.InputBroken() {
				t.Fatal("the input is broken before any failure")
			}
			c.trigger(t, s)
			if !s.InputBroken() {
				t.Fatal("InputBroken is false after a failed write")
			}
			refused := s.Counters()["input_refused"]

			if h := s.Input([]byte("Q")); !errors.Is(h.Err, ErrInputBroken) || h.Written != 0 {
				t.Errorf("Input after the break = %+v", h)
			}
			if h := s.Paste("Q"); !errors.Is(h.Err, ErrInputBroken) || h.Written != 0 {
				t.Errorf("Paste after the break = %+v", h)
			}
			var w AgentWriter
			err := s.WithInputLock(func(aw AgentWriter) error {
				w = aw
				n, err := aw.Write([]byte("Q"))
				if n != 0 {
					t.Errorf("the section wrote %d bytes after the break", n)
				}
				return err
			})
			if !errors.Is(err, ErrInputBroken) {
				t.Errorf("a section's write after the break: %v", err)
			}
			if n, err := w.Write([]byte("Q")); n != 0 || err == nil {
				t.Errorf("a stale writer wrote %d, %v", n, err)
			}
			cnt := s.Counters()
			if cnt["input_broken"] != 1 || cnt["input_refused"] < refused+3 {
				t.Errorf("counters after three refusals: %v (refused before: %d)", cnt, refused)
			}

			// One state event, with the source and the cause as the reason.
			var broken []eventlog.Event
			for _, ev := range eventsNamed(e.events(), eventlog.EventState) {
				if ev.State == "input-broken" {
					broken = append(broken, ev)
				}
			}
			if len(broken) != 1 || broken[0].Reason != c.reason {
				t.Errorf("input-broken events %+v, want one with reason %q", broken, c.reason)
			}
		})
	}
}

// After a cut the agent receives no byte: not the writer's next key, which
// would sit inside a bracketed paste that never ended, and not a reply.
func TestTheAgentReceivesNothingAfterTheCut(t *testing.T) {
	e := newEnv(t)
	// The agent does not read for a while; then it asks a terminal question
	// (the answer must not be written) and reads everything that was written.
	s := e.start("raw; say READY; sleep 2s; print 1b5b63; echo; wait", shortWriteTimeout)
	waitScreen(t, s, "READY")
	var out capture
	unsub, err := s.Subscribe(out.fn)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	h := s.Paste(bigInput)
	if !errors.Is(h.Err, os.ErrDeadlineExceeded) || h.Written == 0 || h.Written >= len(bigInput) {
		t.Fatalf("Paste = %+v, want a prefix and a timeout", h)
	}
	if h := s.Input([]byte("Q")); !errors.Is(h.Err, ErrInputBroken) {
		t.Errorf("Input after the cut = %+v", h)
	}
	if err := s.WithInputLock(func(w AgentWriter) error { _, err := w.Write([]byte("R")); return err }); !errors.Is(err, ErrInputBroken) {
		t.Errorf("a section's write after the cut: %v", err)
	}
	// The agent's question comes after its sleep: its answer is refused too.
	poll(t, func() string { return fmt.Sprintf("the reply to be refused; counters %v", s.Counters()) }, func() bool {
		return s.Counters()["input_refused"] >= 3
	})
	// The agent now reads what was written, and only that.
	want := strings.Repeat("x", h.Written)
	var got []byte
	poll(t, func() string { return fmt.Sprintf("the agent to read %d bytes, read %d", len(want), len(got)) }, func() bool {
		got = rxBytes(string(out.all()))
		return len(got) >= len(want)
	})
	// Anything else would be in the same terminal buffer; give it a moment.
	time.Sleep(150 * time.Millisecond)
	if got = rxBytes(string(out.all())); string(got) != want {
		t.Errorf("the agent read %d bytes, want the %d of the prefix and nothing after; tail %q", len(got), len(want), got[max(0, len(got)-20):])
	}
}

// A fence that waits for the screen to settle lets the replies through, so
// that an agent waiting for a terminal answer is not stalled by the lock.
func TestFlushRepliesLetsAnAgentThatWaitsForAnAnswerGoOn(t *testing.T) {
	e := newEnv(t)
	got := filepath.Join(t.TempDir(), "answers")
	s := e.start("raw; say READY; echo 1; query osc10,da1 "+got+"; say AFTER; wait", func(c *Config) {
		c.Colours = sessionpane.Colours{Foreground: color.RGBA{R: 1, G: 2, B: 3, A: 255}}
	})
	waitScreen(t, s, "READY")

	var flushed int
	err := s.WithInputLock(func(w AgentWriter) error {
		if n, err := w.FlushReplies(); n != 0 || err != nil {
			t.Errorf("FlushReplies with nothing queued = %d, %v", n, err)
		}
		// The byte that makes the agent send its queries.
		if _, err := w.Write([]byte("x")); err != nil {
			return err
		}
		poll(t, func() string { return "the agent's queries to be answered by the emulator" }, func() bool { return s.replies.pending() > 0 })
		// The answers wait for the lock, which this section holds.
		time.Sleep(100 * time.Millisecond) // a window to see that nothing moves
		var stalled string
		w.Screen(func(p *sessionpane.Pane, _ uint64) { stalled = paneText(p) })
		if strings.Contains(stalled, "AFTER") {
			t.Error("the agent went on without its answers")
		}
		n, err := w.FlushReplies()
		if err != nil || n == 0 {
			t.Errorf("FlushReplies = %d, %v", n, err)
		}
		flushed = n
		if n, err := w.FlushReplies(); n != 0 || err != nil {
			t.Errorf("a second FlushReplies = %d, %v", n, err)
		}
		// Still inside the section: the agent got its answers and went on.
		poll(t, func() string { return "the agent to go on inside the section" }, func() bool {
			var text string
			w.Screen(func(p *sessionpane.Pane, _ uint64) { text = paneText(p) })
			return strings.Contains(text, "AFTER")
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != flushed {
		t.Errorf("the agent read %d bytes, FlushReplies returned %d", len(b), flushed)
	}
	// In order: the colour answer first, then the device attributes.
	i, j := strings.Index(string(b), "\x1b]10;rgb:"), strings.Index(string(b), "\x1b[?")
	if i < 0 || j < 0 || i > j {
		t.Errorf("the answers %q are missing or out of order", b)
	}
	if n := s.Counters()["reply_write_errors"]; n != 0 {
		t.Errorf("reply_write_errors = %d", n)
	}
	var w AgentWriter
	s.WithInputLock(func(aw AgentWriter) error { w = aw; return nil })
	if _, err := w.FlushReplies(); err == nil {
		t.Error("FlushReplies works after the section")
	}
}
