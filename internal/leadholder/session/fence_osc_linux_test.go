//go:build linux

package session

import (
	"errors"
	"testing"
	"time"

	"ataniq/internal/terminalconsole/sessionpane"
)

// With no fence the hook answers on the applying side: an agent that writes
// the query and then a request for the device attributes reads the hook's
// reply before the reply to the request.
func TestOSCHookReplyReachesTheAgentBeforeTheReplyToALaterRequest(t *testing.T) {
	e := newEnv(t)
	var h ackHook
	s := e.start("raw; print "+hx(testQuery+testDA1)+"; echo; wait", withHook(&h))
	waitRX(t, s, testAck+testDA1Resp)
	if n := h.calls.Load(); n != 1 {
		t.Errorf("the hook was called %d times, want 1", n)
	}
}

// The hook is given the whole string, and a string it does not handle is not
// answered and not consumed twice.
func TestOSCHookIsGivenTheWholeStringAndCanDecline(t *testing.T) {
	e := newEnv(t)
	got := make(chan string, 4)
	hook := OSCHook{Cmd: testOSC, Fn: func(data []byte) ([]byte, bool) {
		got <- string(data)
		return []byte("no"), false
	}}
	s := e.start("raw; print "+hx(testQuery+testDA1)+"; echo; wait", func(c *Config) { c.OSC = []OSCHook{hook} })
	select {
	case d := <-got:
		if d != "9911;?" {
			t.Errorf("the hook was given %q, want the command number, a semicolon and the rest", d)
		}
	case <-time.After(patience):
		t.Fatal("the hook was not called")
	}
	// A handler that declines answers nothing: only the attributes come back.
	waitRX(t, s, testDA1Resp)
}

// With no hook nothing is answered to the string, and the session behaves as it
// did before hooks existed.
func TestOSCNoHookNothingIsAnswered(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; print " + hx(testQuery+testDA1) + "; echo; wait")
	waitRX(t, s, testDA1Resp)
	time.Sleep(200 * time.Millisecond)
	if got := string(rxBytes(screenText(s))); got != testDA1Resp {
		t.Errorf("the agent received %q, want only the reply to the request for the attributes", got)
	}
}

// Output buffered while the fence is frozen reaches the hook at the next
// Settle and not before, and the hook's reply is written by that Settle.
func TestOSCHookIsCalledAtTheNextSettleForOutputBufferedDuringFrozen(t *testing.T) {
	e := newEnv(t)
	var h ackHook
	s := e.start("raw; say READY; echo 1; sleep 100ms; print "+hx(testQuery+testDA1)+"; echo; wait", withHook(&h))
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		frozen(t, f, func(v FenceView, write func([]byte) error) {
			if err := write([]byte("g")); err != nil {
				t.Fatal(err)
			}
			// The agent prints the query 100 ms after it read the byte; the
			// reader reads it and the hook is not called.
			until(t, "the query to be read", func() bool { return s.unapplied.Load() >= int64(len(testQuery+testDA1)) })
			time.Sleep(100 * time.Millisecond)
			if n := h.calls.Load(); n != 0 {
				t.Errorf("the hook was called %d times inside Frozen", n)
			}
		})
		if n := h.calls.Load(); n != 0 {
			t.Errorf("the hook was called %d times after Frozen and before Settle", n)
		}
		settle(t, f, 0, 100*time.Millisecond, 5*time.Second)
		if n := h.calls.Load(); n != 1 {
			t.Errorf("the hook was called %d times by Settle, want 1", n)
		}
		if n := s.replies.pending(); n != 0 {
			t.Errorf("%d bytes of replies were left in the queue by Settle", n)
		}
		until(t, "the agent to report the replies", func() bool {
			settle(t, f, 0, 50*time.Millisecond, 5*time.Second)
			return len(rxFromText(viewText(t, f))) >= 1+len(testAck)+len(testDA1Resp)
		})
		if got, want := string(rxFromText(viewText(t, f))), "g"+testAck+testDA1Resp; got != want {
			t.Errorf("the agent received %q, want %q", got, want)
		}
	})
}

// Several hooks, each for its own command, are all installed; the last set for
// a command is tried first.
func TestOSCHooksOfSeveralCommandsAreInstalledBeforeTheChildStarts(t *testing.T) {
	e := newEnv(t)
	var a, b ackHook
	hookB := OSCHook{Cmd: 9912, Fn: func([]byte) ([]byte, bool) { b.calls.Add(1); return []byte("B"), true }}
	s := e.start("raw; print "+hx("\x1b]9912;?\x07"+testQuery+testDA1)+"; echo; wait", func(c *Config) {
		c.OSC = []OSCHook{a.hook(), hookB}
	})
	waitRX(t, s, "B"+testAck+testDA1Resp)
	if a.calls.Load() != 1 || b.calls.Load() != 1 {
		t.Errorf("calls %d and %d, want 1 and 1", a.calls.Load(), b.calls.Load())
	}
}

// A command the pane handles itself, a nil function and a negative command
// make Start fail, with nothing left open: the role lock is free again.
func TestOSCHookStartRefusesWhatThePaneRefuses(t *testing.T) {
	for _, bad := range []OSCHook{
		{Cmd: 0, Fn: func([]byte) ([]byte, bool) { return nil, true }},
		{Cmd: 52, Fn: func([]byte) ([]byte, bool) { return nil, true }},
		{Cmd: testOSC},
		{Cmd: -1, Fn: func([]byte) ([]byte, bool) { return nil, true }},
	} {
		e := newEnv(t)
		cfg := e.config("raw; wait")
		cfg.OSC = []OSCHook{bad}
		fds := countFDs(t)
		_, err := Start(cfg)
		if err == nil {
			t.Fatalf("Start accepted the hook %+v", bad)
		}
		if bad.Cmd == 0 || bad.Cmd == 52 {
			if !errors.Is(err, sessionpane.ErrOSCReserved) {
				t.Errorf("Start = %v, want an error wrapping ErrOSCReserved", err)
			}
		}
		if n := countFDs(t); n > fds {
			t.Errorf("descriptors %d -> %d after a refused Start", fds, n)
		}
		// The role lock was released: a session can start.
		cfg.OSC = nil
		s, err := Start(cfg)
		if err != nil {
			t.Fatalf("Start after a refused one: %v", err)
		}
		s.Close()
	}
}
