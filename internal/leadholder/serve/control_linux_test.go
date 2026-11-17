//go:build linux

package serve

import (
	"encoding/hex"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/wire"
)

// Case A8: a peer of another user is refused on both sockets, and logged.
func TestPeerOfAnotherUserRefused(t *testing.T) {
	var other atomic.Bool
	e := newEnv(t, "raw; say READY; echo; wait", func(o *envOpts) {
		o.cfg = func(c *Config) {
			c.PeerCred = func(conn *net.UnixConn) (PeerCred, error) {
				cred, err := peerCred(conn)
				if err == nil && other.Load() {
					cred.UID++
				}
				return cred, err
			}
		}
	})
	waitScreen(t, e.sess, "READY")
	other.Store(true)

	conn := e.dialAttach()
	if b := readAll(conn); len(b) != 0 {
		t.Errorf("the holder answered a peer of another user with %d bytes", len(b))
	}
	e.waitEvent("the attach refusal", named("refusal", "attach-peer-uid"))
	if _, err := e.control(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}); err == nil {
		t.Error("the control socket answered a peer of another user")
	}
	e.waitEvent("the control refusal", named("refusal", "control-peer-uid"))
	if e.count(cntRefusedUID) != 1 || e.count(cntControlPeer) != 1 {
		t.Errorf("counters %d %d", e.count(cntRefusedUID), e.count(cntControlPeer))
	}
	other.Store(false)
	e.attach(wire.RoleViewer).waitSnapshots(1)
}

// Case A12: a same-user process that is not the daemon is refused on the
// control socket: the pid in the lock file is not its own, or the file is not
// locked.
func TestControlPeerMustBeTheDaemon(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait")
	waitScreen(t, e.sess, "READY")
	status := wire.Request{Op: wire.OpStatus, Epoch: testEpoch}
	if r, err := e.control(status); err != nil || !r.OK {
		t.Fatalf("the daemon: %+v, %v", r, err)
	}

	// Another process holds the lock and its pid is in the file.
	e.holdDaemonLock(os.Getppid())
	if _, err := e.control(status); err == nil {
		t.Fatal("a process that is not the daemon was answered")
	}
	e.waitEvent("not the daemon", named("refusal", "control-peer-not-the-daemon"))

	// The pid is right but nobody holds the lock.
	if err := os.WriteFile(e.lockPath, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.holdDaemonLock(os.Getpid())
	e.releaseDaemonLock()
	if _, err := e.control(status); err == nil {
		t.Fatal("an unlocked daemon lock was accepted")
	}
	e.waitEvent("the lock not held", named("refusal", "control-peer-daemon-lock-not-held"))

	// No lock file at all.
	if err := os.Remove(e.lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control(status); err == nil {
		t.Fatal("a missing daemon lock was accepted")
	}
	e.waitEvent("no lock file", named("refusal", "control-peer-daemon-lock"))

	// A pid that does not parse.
	if err := os.WriteFile(e.lockPath, []byte("not a pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.control(status); err == nil {
		t.Fatal("garbage in the daemon lock was accepted")
	}
	if got := e.count(cntControlPeer); got != 4 {
		t.Errorf("refused control peers: %d, want 4", got)
	}

	// The daemon again.
	e.holdDaemonLock(os.Getpid())
	if r, err := e.control(status); err != nil || !r.OK {
		t.Fatalf("the daemon after all: %+v, %v", r, err)
	}
}

// The pidfd of the peer names its process: a pid from another process in the
// injected function does not pass.
func TestControlPeerPidfdInjected(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait", func(o *envOpts) {
		o.cfg = func(c *Config) { c.PidfdPID = func(int) (int, error) { return os.Getppid(), nil } }
	})
	waitScreen(t, e.sess, "READY")
	if _, err := e.control(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}); err == nil {
		t.Fatal("a peer whose pidfd names another process was answered")
	}
	e.waitEvent("not the daemon", named("refusal", "control-peer-not-the-daemon"))
}

// Case H8: a request for another epoch is refused, logged with its command id,
// and does nothing.
func TestWrongEpoch(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	tk, _ := wire.NewTicket()
	for i, req := range []wire.Request{
		{Op: wire.OpStatus},
		{Op: wire.OpStop},
		{Op: wire.OpReleaseWriter},
		{Op: wire.OpInject},
		{Op: wire.OpTicket, Role: wire.RoleViewer, Ticket: tk},
	} {
		req.Epoch = testEpoch + 1
		req.CommandID = "old-" + string(rune('a'+i))
		r := e.mustControl(req)
		if r.OK || r.Code != wire.CodeWrongEpoch {
			t.Fatalf("%s with another epoch: %+v", req.Op, r)
		}
		e.waitEvent("the refusal of "+string(req.Op), func(ev eventlog.Event) bool {
			return ev.Event == "refusal" && ev.Reason == "wrong-epoch" && ev.CommandID == req.CommandID
		})
	}
	if e.count(cntControlEpoch) != 5 {
		t.Errorf("counter %d", e.count(cntControlEpoch))
	}
	// Nothing happened: the lead runs, the writer is still the writer, the
	// ticket was not registered, and nothing was logged as injected.
	select {
	case <-e.sess.Done():
		t.Fatal("a stop for another epoch stopped the session")
	default:
	}
	st := e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}).Status
	if !st.WriterPresent || st.ViewerPresent {
		t.Errorf("status %+v", st)
	}
	if r := e.mustControl(wire.Request{Op: wire.OpTicket, Epoch: testEpoch, Role: wire.RoleViewer, Ticket: tk}); !r.OK {
		t.Errorf("the viewer ticket was registered by the request for another epoch: %+v", r)
	}
	for _, ev := range e.eventsNow() {
		if ev.Event == "inject" {
			t.Errorf("inject event %+v", ev)
		}
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t, "raw; say READY; print "+hex.EncodeToString([]byte("\x1b]2;the title\x07"))+"; wait")
	waitScreen(t, e.sess, "READY")
	poll(t, func() string { return "the title" }, func() bool {
		return strings.Contains(e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch, CommandID: "st-1"}).Status.Title, "the title")
	})
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)
	r := e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch, CommandID: "st-2"})
	if !r.OK || r.Status == nil {
		t.Fatalf("%+v", r)
	}
	st := *r.Status
	if st.Lead != "proj-lead" || st.Epoch != testEpoch || st.Runtime != "fake" || st.Idle != wire.IdleUnknown ||
		st.Size != (wire.Size{Columns: 80, Rows: 24}) || st.Caps != wire.DefaultCaps() || !st.WriterPresent || !st.ViewerPresent ||
		st.Facts.TUIExited != wire.FactNo || st.Title != "the title" {
		t.Errorf("status %+v", st)
	}
	for _, name := range []string{
		"output_bytes", "held_keys_suspend", // the session's
		"cluster_bytes_dropped", "modes_ignored", "links_over_length", "links_over_limit", "strings_truncated", // the pane's caps
		"titles_cut", "clipboard_requests_blocked",
	} {
		if _, ok := st.Counters[name]; !ok {
			t.Errorf("no counter %s in the status", name)
		}
	}
	e.waitEvent("the command", func(ev eventlog.Event) bool {
		return ev.Event == "state" && ev.Reason == "status" && ev.CommandID == "st-2" && ev.Outcome == "ok"
	})
}

func TestStopAndInject(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)

	r := e.mustControl(wire.Request{Op: wire.OpInject, Epoch: testEpoch, CommandID: "inj-1"})
	if r.OK || r.Code != wire.CodeDisabled {
		t.Fatalf("inject: %+v", r)
	}
	ev := e.waitEvent("the inject refusal", func(ev eventlog.Event) bool { return ev.Event == "inject" })
	if ev.Reason != "disabled" {
		t.Errorf("event %+v", ev)
	}

	r = e.mustControl(wire.Request{Op: wire.OpStop, Epoch: testEpoch, CommandID: "stop-1"})
	if !r.OK {
		t.Fatalf("stop: %+v", r)
	}
	waitDone(t, e.sess)
	e.waitEvent("the stop command", func(ev eventlog.Event) bool {
		return ev.Event == "state" && ev.Reason == "stop" && ev.CommandID == "stop-1"
	})
	// The clients get the end, and Serve returns.
	w.waitFor("the exit", func() bool { return w.exit != nil })
	w.waitEnd()
	select {
	case err := <-e.served:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(patience):
		t.Fatal("Serve did not return")
	}
}

func TestBadControlRequests(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait")
	waitScreen(t, e.sess, "READY")
	send := func(raw []byte) []byte {
		c, err := sockDial(e.dir, ControlSocket)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, _ = c.Write(raw)
		return readAll(c)
	}
	// A request that does not decode gets a refusal; a frame of the wrong
	// kind or over the cap gets none.
	bad := []byte(`{"op":"status","epoch":0}`)
	if b := send(rawFrame(wire.TypeRequest, uint32(len(bad)), bad)); len(b) == 0 {
		t.Error("no answer to a request that does not decode")
	}
	if b := send(rawFrame('x', 2, []byte("{}"))); len(b) != 0 {
		t.Error("an answer to an unknown frame type")
	}
	if b := send(rawFrame(wire.TypeRequest, wire.MaxControlPayload+1, nil)); len(b) != 0 {
		t.Error("an answer to an oversized frame")
	}
	if got := e.count(cntControlOther); got != 3 {
		t.Errorf("counter %d", got)
	}
	if r, err := e.control(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}); err != nil || !r.OK {
		t.Fatalf("after: %+v %v", r, err)
	}
}

// Tickets: one outstanding per role, an expired one is gone, and the number of
// outstanding tickets is bounded.
func TestTicketLimits(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	e := newEnv(t, "raw; say READY; wait", func(o *envOpts) {
		o.cfg = func(c *Config) { c.Now = clock.Now; c.TicketLifetime = time.Minute }
	})
	waitScreen(t, e.sess, "READY")
	reg := func(role wire.Role) wire.Response {
		tk, _ := wire.NewTicket()
		return e.mustControl(wire.Request{Op: wire.OpTicket, Epoch: testEpoch, Role: role, Ticket: tk})
	}
	if r := reg(wire.RoleWriter); !r.OK {
		t.Fatalf("%+v", r)
	}
	if r := reg(wire.RoleWriter); r.OK || r.Code != wire.CodeBusy {
		t.Fatalf("a second writer ticket: %+v", r)
	}
	if r := reg(wire.RoleViewer); !r.OK {
		t.Fatalf("%+v", r)
	}
	if r := reg(wire.RoleViewer); r.OK || r.Code != wire.CodeBusy {
		t.Fatalf("a second viewer ticket: %+v", r)
	}
	clock.advance(2 * time.Minute)
	if r := reg(wire.RoleWriter); !r.OK {
		t.Fatalf("a writer ticket after the others expired: %+v", r)
	}
	// The registered ticket cannot be registered twice.
	tk, _ := wire.NewTicket()
	e.srv.mu.Lock()
	e.srv.tickets = append(e.srv.tickets[:0],
		ticketRec{ticket: tk, role: wire.RoleWriter, expires: clock.Now().Add(time.Minute)})
	e.srv.mu.Unlock()
	if r := e.mustControl(wire.Request{Op: wire.OpTicket, Epoch: testEpoch, Role: wire.RoleViewer, Ticket: tk}); r.OK || r.Code != wire.CodeBadRequest {
		t.Fatalf("the same ticket twice: %+v", r)
	}
	// The bound itself: more outstanding tickets than there are roles cannot
	// be made through the control socket, so the bound is checked where it is.
	e.srv.mu.Lock()
	for i := 0; i < MaxTickets; i++ {
		x, _ := wire.NewTicket()
		e.srv.tickets = append(e.srv.tickets, ticketRec{ticket: x, role: "other", expires: clock.Now().Add(time.Minute)})
	}
	e.srv.mu.Unlock()
	if r := reg(wire.RoleViewer); r.OK || r.Code != wire.CodeBusy {
		t.Fatalf("a ticket over the bound: %+v", r)
	}
}
