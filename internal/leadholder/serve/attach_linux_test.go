//go:build linux

package serve

import (
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
)

func TestAttachWriter(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait", func(o *envOpts) {
		o.cfg = func(c *Config) { c.CheckInterval = 50 * time.Millisecond }
	})
	waitScreen(t, e.sess, "READY")
	c := e.attach(wire.RoleWriter)

	c.mu.Lock()
	w := *c.welcome
	c.mu.Unlock()
	if w.Version != wire.ProtocolVersion || w.Role != wire.RoleWriter || w.Lead != "proj-lead" || w.Epoch != testEpoch ||
		w.Runtime != "fake" || w.RuntimeVersion != "1.2.3" || w.Caps != wire.DefaultCaps() {
		t.Fatalf("welcome %+v", w)
	}

	c.waitSnapshots(1)
	c.waitChecks(1)
	// The first frames after the Welcome are the snapshot and what goes with
	// it, in the order of the design.
	c.mu.Lock()
	order := string(c.frames[:6])
	c.mu.Unlock()
	if want := string([]byte{wire.TypeWelcome, wire.TypeSnapshot, wire.TypeModes, wire.TypeSize, wire.TypeState, wire.TypeRole}); order != want {
		t.Fatalf("frames %q, want %q", order, want)
	}
	if got := c.text(); !strings.Contains(got, "READY") {
		t.Fatalf("the restored screen is %q", got)
	}
	c.mu.Lock()
	size := c.sizes[0]
	st := c.states[0]
	role := c.roles[0]
	c.mu.Unlock()
	if size != (wire.Size{Columns: 80, Rows: 24}) {
		t.Errorf("size %+v: the writer's Hello size is the session's", size)
	}
	if cols, rows := e.sess.Size(); cols != 80 || rows != 24 {
		t.Errorf("the session is %dx%d", cols, rows)
	}
	if st.Idle != wire.IdleUnknown || st.Facts.TUIExited != wire.FactNo || st.Facts.StreamEnded != wire.FactNo {
		t.Errorf("state %+v", st)
	}
	if role.Role != wire.RoleWriter || role.WriterName != "test-console" || role.WriterPID == 0 {
		t.Errorf("role %+v", role)
	}

	// Typed bytes reach the agent and its answer comes back as output.
	c.input("hi")
	waitRX(t, e.sess, "hi")
	c.waitText("RX:6869")
	e.inSync(c)
	c.waitChecks(3) // the interval is short: checks go on
	c.noProblems()

	at := e.waitEvent("attach", named("attach", "writer"))
	if at.ClientName != "test-console" || at.ClientPID == 0 || at.ClientCommand == "" || at.Cols != 80 {
		t.Errorf("attach event %+v", at)
	}
	if e.count(cntSnapshotsTaken) != 1 || e.count(cntChecks) < 3 {
		t.Errorf("counters: snapshots %d checks %d", e.count(cntSnapshotsTaken), e.count(cntChecks))
	}
}

// Case A2: a Hello with no ticket, a used ticket, an expired one and a ticket
// for another epoch is refused with a notice and logged.
func TestTicketRefusals(t *testing.T) {
	now := time.Now()
	clock := &fakeClock{t: now}
	e := newEnv(t, "raw; say READY; echo; wait", func(o *envOpts) {
		o.cfg = func(c *Config) { c.Now = clock.Now; c.TicketLifetime = 30 * time.Second }
	})
	waitScreen(t, e.sess, "READY")

	refused := func(c *tclient, code string) {
		t.Helper()
		n := c.waitNotice(wire.ReasonRefusedTicket)
		if n.Text == "" {
			t.Errorf("a refusal with no text")
		}
		if err := c.waitEnd(); err == nil {
			t.Error("no end")
		}
		e.waitEvent(code, named("refusal", code))
	}
	hello := func(ticket string) *tclient {
		conn := e.dialAttach()
		c := e.newClient(conn)
		p, err := wire.EncodeHello(wire.Hello{Version: wire.ProtocolVersion, Ticket: ticket, Name: "x", Columns: 80, Rows: 24})
		if err != nil {
			t.Fatal(err)
		}
		if err := wire.Write(conn, wire.ToHolder, wire.TypeHello, p); err != nil {
			t.Fatal(err)
		}
		return c
	}

	// No ticket at all.
	conn := e.dialAttach()
	c := e.newClient(conn)
	raw := []byte(`{"version":1,"name":"x","columns":80,"rows":24}`)
	if _, err := conn.Write(rawFrame(wire.TypeHello, uint32(len(raw)), raw)); err != nil {
		t.Fatal(err)
	}
	refused(c, "attach-ticket-missing")

	// A ticket that was never registered.
	never, _ := wire.NewTicket()
	refused(hello(never), "attach-ticket-unknown")

	// A used ticket: the first use consumes it.
	tk := e.newTicket(wire.RoleViewer)
	first := e.attachWith(tk)
	first.waitSnapshots(1)
	first.detach()
	first.waitEnd()
	refused(hello(tk), "attach-ticket-unknown")

	// An expired ticket.
	old := e.newTicket(wire.RoleViewer)
	clock.advance(31 * time.Second)
	refused(hello(old), "attach-ticket-expired")

	// A ticket for another epoch is not registered at all: the control
	// request is refused, and the Hello with it finds no ticket.
	other, _ := wire.NewTicket()
	r := e.mustControl(wire.Request{Op: wire.OpTicket, Epoch: testEpoch + 1, Role: wire.RoleViewer, Ticket: other, CommandID: "cmd-9"})
	if r.OK || r.Code != wire.CodeWrongEpoch {
		t.Fatalf("a ticket for another epoch: %+v", r)
	}
	refused(hello(other), "attach-ticket-unknown")

	if got := e.count(cntRefusedTicket); got != 5 {
		t.Errorf("refused tickets %d, want 5", got)
	}
}

// A Hello that the strict decoder refuses but that carries a well-formed ticket
// uses the ticket up: the same ticket in a correct Hello is refused as unknown.
func TestTicketConsumedByAMalformedHello(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait")
	waitScreen(t, e.sess, "READY")
	for _, tc := range []struct{ name, body string }{
		{"a name over the cap", `"version":1,"name":"` + strings.Repeat("n", 65) + `","columns":80,"rows":24`},
		{"a size out of range", `"version":1,"name":"x","columns":100000,"rows":24`},
		{"a bad colour", `"version":1,"name":"x","columns":80,"rows":24,"colors":{"foreground":"red"}`},
		{"a field this decoder does not know", `"version":1,"name":"x","columns":80,"rows":24,"future":true`},
		{"a newer version", `"version":2,"name":"x","columns":80,"rows":24,"future":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tk := e.newTicket(wire.RoleViewer)
			conn := e.dialAttach()
			c := e.newClient(conn)
			raw := []byte(`{"ticket":"` + tk + `",` + tc.body + `}`)
			if _, err := conn.Write(rawFrame(wire.TypeHello, uint32(len(raw)), raw)); err != nil {
				t.Fatal(err)
			}
			if err := c.waitEnd(); err == nil {
				t.Error("no end")
			}
			// The ticket is gone: a correct Hello with it finds none.
			late := e.attachWithNoWait(tk)
			late.waitNotice(wire.ReasonRefusedTicket)
			late.waitEnd()
			e.waitEvent("the ticket is unknown", named("refusal", "attach-ticket-unknown"))
		})
	}
}

// A ticket is consumed by its first use even when the attach does not complete:
// here the viewer's place was taken after the ticket was registered.
func TestTicketConsumedWhenSlotBusy(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	tk := e.newTicket(wire.RoleViewer) // no viewer yet
	if r := e.mustControl(wire.Request{Op: wire.OpReleaseWriter, Epoch: testEpoch}); !r.OK {
		t.Fatalf("release: %+v", r)
	}
	// The writer is the viewer now.
	w.waitFor("the role change", func() bool { return w.lastRoleLocked().Role == wire.RoleViewer })

	late := e.attachWithNoWait(tk)
	late.waitNotice(wire.ReasonRefusedBusy)
	late.waitEnd()
	e.waitEvent("busy refusal", named("refusal", "attach-busy"))

	again := e.attachWithNoWait(tk)
	again.waitNotice(wire.ReasonRefusedTicket)
	again.waitEnd()
	if e.count(cntRefusedBusy) != 1 {
		t.Errorf("busy refusals %d", e.count(cntRefusedBusy))
	}
}

func TestVersionRefused(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait")
	waitScreen(t, e.sess, "READY")
	conn := e.dialAttach()
	c := e.newClient(conn)
	// A newer client: a field this decoder does not know, and version 2.
	raw := []byte(`{"version":2,"ticket":"x","name":"x","columns":80,"rows":24,"future":true}`)
	if _, err := conn.Write(rawFrame(wire.TypeHello, uint32(len(raw)), raw)); err != nil {
		t.Fatal(err)
	}
	c.waitNotice(wire.ReasonRefusedVersion)
	c.waitEnd()
	e.waitEvent("version refusal", named("refusal", "attach-version"))
	if e.count(cntRefusedVersion) != 1 {
		t.Errorf("counter %d", e.count(cntRefusedVersion))
	}
}
