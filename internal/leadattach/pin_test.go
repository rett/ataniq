package leadattach_test

import (
	"context"
	"fmt"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/leadattach"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
)

func requireDetach(t *testing.T, hc *hconn) {
	t.Helper()
	if types := hc.closed(); string(types) != string(wire.TypeDetach) {
		t.Fatalf("frames after Hello = %q, want only Detach", types)
	}
}

func requireReselect(t *testing.T, e *env) terminalconsole.LiveStatus {
	t.Helper()
	const reason = "the lead's epoch changed: select the lead again"
	st := e.untilConn(terminalconsole.LiveStoppedReselect)
	if st.Reason != reason || st.Conn.String() != "stopped, reselect" {
		t.Fatalf("stopped status = %v, %q", st.Conn, st.Reason)
	}
	want := terminalconsole.LiveNotice{Code: "epoch-changed", Text: reason}
	if ns := e.c.Notices(); len(ns) != 1 || ns[0] != want {
		t.Fatalf("notices = %+v, want %+v", ns, want)
	}
	return st
}

func TestCF03(t *testing.T) {
	source := viewerTicketFunc(func(context.Context) (string, error) { return wire.NewTicket() })
	viewer := setup{pipe: true, viewerTickets: source}
	attach := func(t *testing.T, e *env, screen string) *hconn {
		t.Helper()
		hc := e.h.accept()
		hc.welcome(wire.RoleViewer)
		hc.pane.Write([]byte(screen))
		hc.snapshot()
		e.untilConn(terminalconsole.LiveAttached)
		e.sameScreen(hc)
		return hc
	}

	t.Run("SameEpochResumes", func(t *testing.T) {
		e := newEnv(t, viewer)
		first := attach(t, e, "before disconnect")
		first.nc.Close()
		next := attach(t, e, "after reconnect")
		next.write("!")
		e.sameScreen(next)
		if first.hello.Ticket == next.hello.Ticket || counter(e.c.Status(), "client-snapshots") != 2 {
			t.Fatal("reconnect did not use a fresh ticket and snapshot")
		}
	})

	t.Run("LostFrameResync", func(t *testing.T) {
		e := newEnv(t, viewer)
		first := attach(t, e, "before gap")
		first.pos++ // one byte of output never reached the client
		first.write("after gap")
		requireDetach(t, first)
		next := attach(t, e, "restored")
		next.write(" and following output")
		e.sameScreen(next)
		st := e.c.Status()
		if st.Resyncs != 1 || counter(st, "client-position-gaps") != 1 || first.hello.Ticket == next.hello.Ticket {
			t.Fatalf("expected a resync with a fresh ticket after one gap: %+v", st)
		}
	})

	for _, tc := range []struct {
		name, lead string
		epoch      uint64
	}{
		{"EpochChangeStops", "lead-one", 4},
		{"LeadChangeStops", "lead-two", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, viewer)
			first := attach(t, e, "selected screen")
			screen := e.pane.Digest()
			first.nc.Close()
			next := e.h.accept()
			next.welcomeTarget(wire.RoleViewer, tc.lead, tc.epoch)
			// Offer a different snapshot immediately after Welcome.
			snap := enc(t)(next.pane.AppendSnapshot(nil))
			payload := enc(t)(wire.EncodeSnapshot(0, snap))
			if err := wire.Write(next.nc, wire.ToClient, wire.TypeSnapshot, payload); err == nil {
				t.Fatal("client read the replacement snapshot")
			}
			requireDetach(t, next)
			st := requireReselect(t, e)
			if st.Lead != "lead-one" || st.Epoch != 3 || e.pane.Digest() != screen || counter(st, "client-snapshots") != 1 {
				t.Fatalf("the replacement changed the selected session: %+v", st)
			}
			e.h.noConnection(3 * leadattach.TicketPoll / 100) // scaled clock
		})
	}

	t.Run("ReselectFromSource", func(t *testing.T) {
		issued := &tickets{err: fmt.Errorf("stale selection: %w", leadattach.ErrReselect)}
		issued.give(1)
		e := newEnv(t, setup{pipe: true, viewerTickets: viewerTicketFunc(func(context.Context) (string, error) {
			return issued.get()
		})})
		first := attach(t, e, "selected screen")
		first.nc.Close()
		requireReselect(t, e)
		e.c.Close()
		if issued.callCount() != 2 || e.h.accepts.Load() != 1 || len(e.clock.asked()) != 1 {
			t.Fatal("expected one reconnect delay, a refused renewal and no further poll or dial")
		}
		if st := e.c.Status(); st.Conn != terminalconsole.LiveStoppedReselect {
			t.Fatalf("lost terminal status on close: %+v", st)
		}
	})

	t.Run("NoInput", func(t *testing.T) {
		e := newEnv(t, setup{pipe: true, viewerTickets: source, mode: leadattach.ModeWriter})
		hc := attach(t, e, "read only")
		hc.modes([]int{1000, 1006, 2004}, nil)
		e.barrier(hc)
		e.sink.Key(keyA)
		e.sink.Paste("paste")
		e.sink.Mouse(uv.MouseClickEvent{X: 1, Y: 1, Button: uv.MouseLeft})
		e.c.Resize(100, 30)
		if st := e.c.Status(); st.Role != terminalconsole.LiveViewer || st.RefusedInput != 3 {
			t.Fatalf("viewer status after input: %+v", st)
		}
		e.c.Close()
		requireDetach(t, hc)
	})
}

func TestScratchAttachTargetPin(t *testing.T) {
	t.Run("FirstWelcomeMustMatch", func(t *testing.T) {
		for _, pin := range []leadattach.Config{{ExpectLead: "lead-two"}, {ExpectEpoch: 4}} {
			e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter, tickets: 1, mutate: func(cfg *leadattach.Config) {
				cfg.ExpectLead, cfg.ExpectEpoch = pin.ExpectLead, pin.ExpectEpoch
			}})
			hc := e.h.accept()
			hc.welcome(wire.RoleWriter)
			e.sink.Key(keyA)
			requireDetach(t, hc)
			st := requireReselect(t, e)
			if st.Role != "" || st.Lead != "" || st.Epoch != 0 || counter(st, "client-snapshots") != 0 {
				t.Fatalf("first Welcome published a mismatched target: %+v", st)
			}
		}
	})

	for _, tc := range []struct {
		name, lead string
		epoch      uint64
	}{
		{"Lead", "lead-one", 0},
		{"Epoch", "", 3},
		{"Unpinned", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, setup{pipe: true, tickets: 2, mutate: func(cfg *leadattach.Config) {
				cfg.ExpectLead, cfg.ExpectEpoch = tc.lead, tc.epoch
			}})
			first := e.h.accept()
			first.welcome(wire.RoleViewer)
			first.snapshot()
			e.untilConn(terminalconsole.LiveAttached)
			first.nc.Close()
			next := e.h.accept()
			// Change the component learned from the first Welcome.
			if tc.epoch != 0 {
				next.welcomeTarget(wire.RoleViewer, "lead-two", 3)
			} else {
				next.welcomeTarget(wire.RoleViewer, "lead-one", 4)
			}
			if tc.lead == "" && tc.epoch == 0 {
				next.snapshot()
				if st := e.untilConn(terminalconsole.LiveAttached); st.Epoch != 4 {
					t.Fatalf("scratch attach did not follow the new epoch: %+v", st)
				}
				return
			}
			requireDetach(t, next)
			requireReselect(t, e)
		})
	}
}
