//go:build linux

package leadattach_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadattach"
	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/leadholder/ptyhost"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
	"ataniq/internal/terminalconsole/sessionpane"

	uv "github.com/charmbracelet/ultraviolet"
)

// The fake agent is this test binary run again: see fakeagent.
func TestMain(m *testing.M) {
	fakeagent.RunIfChild()
	os.Exit(m.Run())
}

const realEpoch = 5

// realHolder is a real holder service on a session of the fake agent, in a
// folder of its own: the client under test speaks to it as the console would.
type realHolder struct {
	t    *testing.T
	dir  string
	sess *session.Session
	srv  *serve.Server
}

func newRealHolder(t *testing.T, script string) *realHolder {
	t.Helper()
	// A short path: the sockets of the service are unix sockets.
	root, err := os.MkdirTemp("", "la")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	h := &realHolder{t: t, dir: filepath.Join(root, "rt")}
	logDir, state := filepath.Join(root, "log"), filepath.Join(root, "state")
	for _, d := range []string{h.dir, logDir, state} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	lg, err := eventlog.Open(logDir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lg.Close() })
	argv, env := fakeagent.Command(script)
	h.sess, err = session.Start(session.Config{
		Role: "proj/lead", Epoch: strconv.Itoa(realEpoch), Argv: argv, Env: env, Cols: 100, Rows: 30,
		RoleLock: filepath.Join(root, "lead.lock"), StateDir: state, Log: lg,
		StopGrace: 300 * time.Millisecond, PTY: ptyhost.Config{NoCgroup: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.sess.Close() })

	// The control socket accepts the process that holds the daemon's lock:
	// this one.
	lock := filepath.Join(root, "daemon.lock")
	if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lf, err := os.Open(lock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lf.Close() })
	if err := unix.Flock(int(lf.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	h.srv, err = serve.New(serve.Config{
		Session: h.sess, Dir: h.dir, Lead: "proj-lead", Epoch: realEpoch, Runtime: "fake", RuntimeVersion: "1.2.3",
		Log: lg, DaemonLock: lock, CheckInterval: 50 * time.Millisecond, PollInterval: 20 * time.Millisecond,
		FlushWait: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.srv.Close() })
	go h.srv.Serve()
	return h
}

// ticket registers a single-use ticket for role through the control socket,
// as the daemon would.
func (h *realHolder) ticket(role wire.Role) (string, error) {
	tk, err := wire.NewTicket()
	if err != nil {
		return "", err
	}
	c, err := net.Dial("unix", filepath.Join(h.dir, serve.ControlSocket))
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(wait))
	p, err := wire.EncodeRequest(wire.Request{Op: wire.OpTicket, Epoch: realEpoch, Role: role, Ticket: tk})
	if err != nil {
		return "", err
	}
	if err := wire.Write(c, wire.ControlRequests, wire.TypeRequest, p); err != nil {
		return "", err
	}
	f, err := wire.NewReader(c, wire.ControlResponses).Next()
	if err != nil {
		return "", err
	}
	r, err := wire.DecodeResponse(f.Payload)
	if err != nil {
		return "", err
	}
	if !r.OK {
		return "", fmt.Errorf("the holder refused the ticket: %+v", r)
	}
	return tk, nil
}

// rx is the bytes the fake agent says it received, from its screen.
func (h *realHolder) rx() string {
	var screen string
	h.sess.Screen(func(p *sessionpane.Pane, _ uint64) {
		cols, rows := p.Size()
		screen, _ = p.Text(uv.Pos(0, 0), uv.Pos(cols-1, rows-1), 1<<20)
	})
	var got []byte
	for _, line := range strings.Split(screen, "\n") {
		hexs, ok := strings.CutPrefix(line, "RX:")
		if !ok {
			continue
		}
		hexs = strings.TrimSpace(hexs)
		for i := 0; i+1 < len(hexs); i += 2 {
			v, err := strconv.ParseUint(hexs[i:i+2], 16, 8)
			if err != nil {
				break
			}
			got = append(got, byte(v))
		}
	}
	return string(got)
}

func (h *realHolder) waitRX(want string) {
	h.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		got := h.rx()
		if len(got) >= len(want) {
			if got != want {
				h.t.Fatalf("the agent received %q, want %q", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("the agent received %q, want %q", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The client against the real service: it attaches as the writer with a
// ticket from the control socket, follows the agent's output without a check
// mismatch, sends keys for the modes the holder reports, resizes, and ends
// with the session.
func TestTheClientAgainstTheRealHolderService(t *testing.T) {
	leakCheck(t)
	// DECCKM (1) is set before the agent reads.
	h := newRealHolder(t, "raw; mode 1; say READY; echo; wait")
	pane := sessionpane.New(80, 24, sessionpane.Options{})
	t.Cleanup(pane.Close)
	wake := make(chan struct{}, 1)
	c := leadattach.New(leadattach.Config{
		Socket: filepath.Join(h.dir, serve.AttachSocket), Mode: leadattach.ModeWriter, Name: "test console",
		Tickets: func() (string, error) { return h.ticket(wire.RoleWriter) },
	})
	t.Cleanup(c.Close)
	sink, err := c.Start(terminalconsole.LiveStart{
		Pane: pane, Cols: 80, Rows: 24, Colours: sessionpane.NeutralColours,
		Wake: func() {
			select {
			case wake <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	until := func(what string, cond func(terminalconsole.LiveStatus) bool) terminalconsole.LiveStatus {
		t.Helper()
		deadline := time.After(wait)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			if st := c.Status(); cond(st) {
				return st
			}
			select {
			case <-wake:
			case <-tick.C:
			case <-deadline:
				t.Fatalf("timed out waiting for %s; status %+v", what, c.Status())
			}
		}
	}

	st := until("attached with the agent's screen", func(st terminalconsole.LiveStatus) bool {
		return st.Conn == terminalconsole.LiveAttached && strings.Contains(paneText(pane), "READY")
	})
	if st.Role != terminalconsole.LiveWriter || st.Lead != "proj-lead" || st.Runtime != "fake" || st.Epoch != realEpoch {
		t.Errorf("status %+v", st)
	}

	// Keys reach the agent encoded for the modes of the holder: Up is SS3 A
	// under DECCKM.
	sink.Key(uv.KeyPressEvent{Code: 'a', Text: "a"})
	sink.Key(uv.KeyPressEvent{Code: uv.KeyUp})
	h.waitRX("a\x1bOA")

	// A resize is the holder's, and the pane follows its Size frame.
	c.Resize(120, 40)
	until("the new size", func(st terminalconsole.LiveStatus) bool { return st.Cols == 120 && st.Rows == 40 })
	if cols, rows := h.sess.Size(); cols != 120 || rows != 40 {
		t.Errorf("the session is %dx%d", cols, rows)
	}

	// The checks of the holder go on matching what the client shows, and the
	// screens are the same.
	until("checks to pass", func(terminalconsole.LiveStatus) bool {
		var same bool
		h.sess.Screen(func(p *sessionpane.Pane, _ uint64) { same = p.Digest() == pane.Digest() })
		return same
	})
	if st := c.Status(); st.CheckMismatches != 0 || st.Resyncs != 0 {
		t.Errorf("mismatches %d, resyncs %d", st.CheckMismatches, st.Resyncs)
	}

	// The session ends: State and Exit end the pane, and the client with it.
	h.sess.Stop()
	st = until("the end", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveDisconnected })
	if st.Reason != "session ended" {
		t.Errorf("reason %q", st.Reason)
	}
	if ended, _, _ := pane.Ended(); !ended {
		t.Error("the pane did not end")
	}
	if len(st.Counters) == 0 {
		t.Error("no counters")
	}
}
