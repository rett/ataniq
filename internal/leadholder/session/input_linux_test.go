//go:build linux

package session

import (
	"errors"
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
