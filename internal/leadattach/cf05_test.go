package leadattach_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"ataniq/internal/leadattach"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
)

func TestCF05Client(t *testing.T) {
	t.Run("PinAtEpochZero", func(t *testing.T) {
		// The wire refuses epoch 0 and an empty lead in a Welcome, so the pin
		// is fed Welcomes directly: a part that was pinned at zero stays
		// pinned, and a part that was never pinned follows the first Welcome.
		zero := wire.Welcome{Lead: "lead-one", Epoch: 0}
		moved := wire.Welcome{Lead: "lead-one", Epoch: 1}
		other := wire.Welcome{Lead: "lead-two", Epoch: 0}
		emptyLead := wire.Welcome{Lead: "", Epoch: 7}
		viewer := func() *leadattach.Client {
			return leadattach.NewViewer(leadattach.Config{Socket: "x", Tickets: func() (string, error) { return "", nil }}, viewerTicketFunc(nil))
		}
		scratch := func() *leadattach.Client {
			return leadattach.New(leadattach.Config{Socket: "x", Tickets: func() (string, error) { return "", nil }, ExpectLead: "lead-one"})
		}
		for _, tc := range []struct {
			name string
			c    *leadattach.Client
		}{{"Viewer", viewer()}, {"ScratchWithLead", scratch()}} {
			c := tc.c
			if !c.TargetMatches(zero) {
				t.Fatalf("%s: the first Welcome at epoch 0 was refused", tc.name)
			}
			c.PinTarget(zero)
			if c.TargetMatches(moved) {
				t.Errorf("%s: epoch 0 was not pinned: a Welcome at epoch 1 matches", tc.name)
			}
			if c.TargetMatches(other) {
				t.Errorf("%s: a Welcome of another lead matches", tc.name)
			}
			if !c.TargetMatches(zero) {
				t.Errorf("%s: the pinned target no longer matches itself", tc.name)
			}
		}

		c := leadattach.NewViewer(leadattach.Config{Socket: "x", Tickets: func() (string, error) { return "", nil }}, viewerTicketFunc(nil))
		c.PinTarget(emptyLead)
		if c.TargetMatches(wire.Welcome{Lead: "lead-one", Epoch: 7}) {
			t.Error("an empty lead was not pinned")
		}
		if !c.TargetMatches(emptyLead) {
			t.Error("the pinned empty lead does not match itself")
		}

		// A client with no pin follows every Welcome and pins nothing.
		free := leadattach.New(leadattach.Config{Socket: "x", Tickets: func() (string, error) { return "", nil }})
		free.PinTarget(zero)
		if !free.TargetMatches(other) || !free.TargetMatches(moved) {
			t.Error("an unpinned scratch client was pinned by its first Welcome")
		}

		t.Run("RoleMismatchPinsNothing", func(t *testing.T) {
			e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter, tickets: 3, mutate: func(cfg *leadattach.Config) {
				cfg.ExpectLead = "lead-one"
			}})
			first := e.h.accept()
			first.welcomeTarget(wire.RoleViewer, "lead-one", 5) // not what was asked for
			requireDetach(t, first)
			// The mismatched Welcome did not pin its epoch: the next one, at
			// another epoch, is accepted.
			next := e.h.accept()
			next.welcomeTarget(wire.RoleWriter, "lead-one", 6)
			next.snapshot()
			st := e.untilConn(terminalconsole.LiveAttached)
			if st.Epoch != 6 || st.Role != terminalconsole.LiveWriter {
				t.Fatalf("status after the role mismatch: %+v", st)
			}
			next.nc.Close()
			again := e.h.accept()
			again.welcomeTarget(wire.RoleWriter, "lead-one", 7)
			requireDetach(t, again)
			e.untilConn(terminalconsole.LiveStoppedReselect)
			if ns := e.c.Notices(); len(ns) != 2 || ns[0].Code != "role-mismatch" || ns[1].Code != "epoch-changed" {
				t.Fatalf("notices %+v, want the role mismatch and then the reselect", ns)
			}
		})
	})

	t.Run("ReselectOnNewPath", func(t *testing.T) {
		stale := fmt.Errorf("stale selection: %w", leadattach.ErrReselect)
		quiet := func(e *env) {
			t.Helper()
			e.h.noConnection(6 * leadattach.TicketPoll / 100) // scaled clock: several polls
		}

		t.Run("PresenceCheck", func(t *testing.T) {
			var calls atomic.Int64
			e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter, mutate: func(cfg *leadattach.Config) {
				cfg.ExpectLead = "lead-one"
				cfg.TicketReady = func() error { calls.Add(1); return stale }
			}})
			requireReselect(t, e)
			quiet(e)
			if calls.Load() != 1 || e.tickets.callCount() != 0 || e.h.accepts.Load() != 0 {
				t.Fatalf("polled on: %d checks, %d ticket calls, %d dials", calls.Load(), e.tickets.callCount(), e.h.accepts.Load())
			}
		})

		t.Run("TicketAfterDial", func(t *testing.T) {
			e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter, mutate: func(cfg *leadattach.Config) {
				cfg.ExpectLead = "lead-one"
			}})
			e.tickets.err = stale
			requireReselect(t, e)
			quiet(e)
			if e.tickets.callCount() != 1 || e.h.accepts.Load() != 1 {
				t.Fatalf("polled on: %d ticket calls, %d dials", e.tickets.callCount(), e.h.accepts.Load())
			}
			if st := e.c.Status(); st.Conn != terminalconsole.LiveStoppedReselect {
				t.Fatalf("status %+v", st)
			}
		})

		t.Run("AfterAnAttach", func(t *testing.T) {
			e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter, tickets: 1, mutate: func(cfg *leadattach.Config) {
				cfg.ExpectLead = "lead-one"
			}})
			e.tickets.err = stale // asked once the one ticket is gone
			first := e.h.accept()
			first.welcome(wire.RoleWriter)
			first.snapshot()
			e.untilConn(terminalconsole.LiveAttached)
			first.nc.Close()
			requireReselect(t, e)
			quiet(e)
			if e.tickets.callCount() != 2 || e.h.accepts.Load() != 2 {
				t.Fatalf("polled on: %d ticket calls, %d dials", e.tickets.callCount(), e.h.accepts.Load())
			}
		})
	})
}
