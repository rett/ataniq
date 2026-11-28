package leadattach_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/leadattach"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
)

// fenceWriter is a writer client whose holder is stuck: one paste is in the
// sender's write, and a run of keys, a paste and (with resize) a size wait
// behind it. It returns the connection and how many things wait.
func fenceWriter(t *testing.T, mutate func(*leadattach.Config), resize bool) (*env, *hconn, int) {
	t.Helper()
	e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter, mutate: mutate})
	hc := e.attach(wire.RoleWriter)
	sent := hc.writes.Load() // the Hello, and nothing else yet
	parked := hc.holdAfter(wire.TypePaste)
	e.sink.Paste("first")
	select {
	case <-parked:
	case <-time.After(wait):
		t.Fatal("the holder did not read the first paste")
	}
	if f := hc.next(); f.typ != wire.TypePaste || string(f.payload) != "first" {
		t.Fatalf("the holder read a %s frame %q, want the first paste", wire.TypeName(f.typ), f.payload)
	}
	e.sink.Paste("second") // in the sender's write: nobody reads it yet
	hc.untilWrites(sent + 2)
	e.sink.Key(keyA)
	e.sink.Key(keyA)
	e.sink.Key(keyA)
	e.sink.Paste("last")
	waiting := 4 // three keys, one paste
	if resize {
		e.c.Resize(100, 30)
		waiting++
	}
	if st := e.c.Status(); st.InputFenced != 0 || counter(st, "client-input-dropped") != 0 {
		t.Fatalf("input counted before anything was fenced: %+v", st.Counters)
	}
	return e, hc, waiting
}

// requireFenced waits for the fenced count and checks it in the status and in
// the counters, and that nothing was counted as dropped.
func requireFenced(t *testing.T, e *env, want uint64) {
	t.Helper()
	e.until("the fenced count", func(st terminalconsole.LiveStatus) bool { return st.InputFenced >= want })
	if st := e.c.Status(); st.InputFenced != want || counter(st, "client-input-fenced") != want || counter(st, "client-input-dropped") != 0 {
		t.Fatalf("fenced %d (counter %d), dropped %d; want %d fenced and none dropped",
			st.InputFenced, counter(st, "client-input-fenced"), counter(st, "client-input-dropped"), want)
	}
}

// fencedNotices returns the notices that say input was fenced.
func fencedNotices(ns []terminalconsole.LiveNotice) (fenced []terminalconsole.LiveNotice) {
	for _, n := range ns {
		if n.Code == "input-fenced" {
			fenced = append(fenced, n)
		}
	}
	return fenced
}

// requireOnlyTheFrameInFlight lets the holder read again, waits for the paste
// that was already in the sender's write, closes the client and checks that
// nothing else of what was typed reached the holder.
func requireOnlyTheFrameInFlight(t *testing.T, e *env, hc *hconn) {
	t.Helper()
	hc.resume()
	if f := hc.next(); f.typ != wire.TypePaste || string(f.payload) != "second" {
		t.Fatalf("the holder read a %s frame %q, want the paste that was in flight", wire.TypeName(f.typ), f.payload)
	}
	e.c.Close()
	if types := hc.closed(); string(types) != string(wire.TypeDetach) {
		t.Fatalf("frames after the fence: %q, want only the detach", types)
	}
}

func codes(ns []terminalconsole.LiveNotice) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Code)
	}
	return out
}

func TestCF05Client(t *testing.T) {
	t.Run("DemoteDropsQueuedInput", func(t *testing.T) {
		e, hc, waiting := fenceWriter(t, nil, true)
		hc.roleFrame(wire.RoleViewer, "another")
		requireFenced(t, e, uint64(waiting))
		if st := e.c.Status(); st.Role != terminalconsole.LiveViewer {
			t.Fatalf("role %v after the demotion", st.Role)
		}
		// The holder follows its Role frame with the Notice of the change: the
		// fence already said its own, and does not say it again.
		hc.notice(wire.ReasonWriterReleased, "the writer was released")
		hc.notice(wire.Reason("barrier"), "")
		ns := e.notices(3)
		if got := codes(ns); len(got) != 3 || got[0] != "input-fenced" || got[1] != "writer-released" || got[2] != "barrier" || ns[0].Text == "" {
			t.Fatalf("notices %+v, want one input-fenced, the holder's notice and the barrier", ns)
		}
		requireFenced(t, e, uint64(waiting))
		// Nothing typed after the demotion is queued either.
		e.sink.Key(keyA)
		e.sink.Paste("later")
		e.c.Resize(120, 40)
		if st := e.c.Status(); st.RefusedInput != 2 || st.InputFenced != uint64(waiting) {
			t.Fatalf("after the fence: refused %d, fenced %d", st.RefusedInput, st.InputFenced)
		}
		requireOnlyTheFrameInFlight(t, e, hc)
	})

	t.Run("ReleasedNoticeDropsQueuedInput", func(t *testing.T) {
		e, hc, waiting := fenceWriter(t, nil, true)
		// A release that closes the connection sends this Notice and no Role
		// frame: it alone takes the place and fences the queue.
		hc.notice(wire.ReasonWriterReleased, "you were released as the writer")
		requireFenced(t, e, uint64(waiting))
		if st := e.c.Status(); st.Role != terminalconsole.LiveViewer {
			t.Fatalf("role %v after the release", st.Role)
		}
		ns := e.notices(2)
		if got := codes(ns); len(got) != 2 || got[0] != "writer-released" || got[1] != "input-fenced" || ns[1].Text == "" {
			t.Fatalf("notices %+v, want the holder's notice and one input-fenced", ns)
		}
		e.sink.Key(keyA)
		if st := e.c.Status(); st.RefusedInput != 1 {
			t.Fatalf("refused %d, want the key typed after the release", st.RefusedInput)
		}
		requireOnlyTheFrameInFlight(t, e, hc)
	})

	t.Run("ReplacementDropsQueuedInput", func(t *testing.T) {
		e, hc, waiting := fenceWriter(t, nil, false)
		e.tickets.give(1)
		// The holder drops the connection under the sender: what was queued
		// and the paste in flight are given up, and the client attaches again.
		hc.nc.Close()
		requireFenced(t, e, uint64(waiting)+1)
		if ns := e.notices(1); len(fencedNotices(ns)) != 1 {
			t.Fatalf("notices %+v, want one input-fenced", ns)
		}
		hc2 := e.reattachAs(wire.RoleWriter)
		e.sink.Key(key('b', 0, "b"))
		if got := hc2.input(1); got != "b" {
			t.Fatalf("the replacing connection first received %q, want the key typed after it", got)
		}
		requireFenced(t, e, uint64(waiting)+1)
		e.barrier(hc2)
	})

	t.Run("ReconnectAsViewer", func(t *testing.T) {
		// viewers is a fresh source of viewer tickets for one subtest, and the
		// option that sets it.
		type viewers struct {
			calls atomic.Int64
			given tickets
		}
		newViewers := func() (*viewers, func(*leadattach.Config)) {
			v := &viewers{}
			return v, func(cfg *leadattach.Config) {
				cfg.ReconnectAsViewer = true
				cfg.ViewerTickets = viewerTicketFunc(func(context.Context) (string, error) {
					v.calls.Add(1)
					v.given.give(1)
					return v.given.get()
				})
			}
		}
		// reconnectsAsViewer drops the connection and checks that the next one
		// asks for a viewer ticket, accepts only the viewer role and takes no
		// input, and that a writer grant after it is refused.
		reconnectsAsViewer := func(t *testing.T, e *env, hc *hconn, v *viewers) {
			t.Helper()
			writerAsked := e.tickets.callCount()
			hc.nc.Close()
			next := e.h.accept()
			if v.calls.Load() != 1 || next.hello.Ticket != v.given.ticket(0) {
				t.Fatalf("the hello's ticket %q, viewer tickets asked %d times: want the one viewer ticket", next.hello.Ticket, v.calls.Load())
			}
			if got := e.tickets.callCount(); got != writerAsked {
				t.Fatalf("the writer ticket source was asked %d times after the drop", got-writerAsked)
			}
			next.welcome(wire.RoleViewer)
			next.snapshot()
			st := e.untilConn(terminalconsole.LiveAttached)
			if st.Role != terminalconsole.LiveViewer {
				t.Fatalf("role %v after the reconnect", st.Role)
			}
			before := st.RefusedInput
			e.sink.Key(keyA)
			e.sink.Paste("x")
			if st := e.c.Status(); st.RefusedInput != before+2 {
				t.Fatalf("a viewer's input was not refused: %+v", st)
			}
			// A writer grant on a later connection is refused: the owner asks
			// for the writer again, with a new client.
			next.nc.Close()
			again := e.h.accept()
			again.welcome(wire.RoleWriter)
			requireDetach(t, again)
			e.until("the mismatch", func(terminalconsole.LiveStatus) bool {
				for _, n := range e.c.Notices() {
					if n.Code == "role-mismatch" {
						return true
					}
				}
				return false
			})
		}

		t.Run("AfterDemotion", func(t *testing.T) {
			v, set := newViewers()
			e, hc, waiting := fenceWriter(t, set, false)
			hc.roleFrame(wire.RoleViewer, "another")
			requireFenced(t, e, uint64(waiting))
			ns := e.notices(1)
			if fenced := fencedNotices(ns); len(fenced) != 1 || !contains(fenced[0].Text, "reconnects as a viewer") {
				t.Fatalf("notices %+v, want an input-fenced notice that says the console reconnects as a viewer", ns)
			}
			hc.resume()
			reconnectsAsViewer(t, e, hc, v)
		})

		t.Run("AfterRelease", func(t *testing.T) {
			v, set := newViewers()
			e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter, mutate: set})
			hc := e.attach(wire.RoleWriter)
			hc.notice(wire.ReasonWriterReleased, "you were released as the writer")
			e.until("viewer", func(st terminalconsole.LiveStatus) bool { return st.Role == terminalconsole.LiveViewer })
			reconnectsAsViewer(t, e, hc, v)
		})

		t.Run("AfterALostConnection", func(t *testing.T) {
			v, set := newViewers()
			e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter, mutate: set})
			hc := e.attach(wire.RoleWriter)
			reconnectsAsViewer(t, e, hc, v)
		})

		t.Run("CloseCancelsASlowViewerTicket", func(t *testing.T) {
			// The source honors only its context, as a daemon command in
			// flight does: Close must cancel it and not wait for it.
			asking := make(chan struct{})
			var once atomic.Bool
			e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter, mutate: func(cfg *leadattach.Config) {
				cfg.ReconnectAsViewer = true
				cfg.ViewerTickets = viewerTicketFunc(func(ctx context.Context) (string, error) {
					if once.CompareAndSwap(false, true) {
						close(asking)
					}
					<-ctx.Done()
					return "", ctx.Err()
				})
			}})
			hc := e.attach(wire.RoleWriter)
			hc.nc.Close()
			select {
			case <-asking:
			case <-time.After(wait):
				t.Fatal("the client never asked for a viewer ticket")
			}
			closed := make(chan struct{})
			go func() { e.c.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(wait):
				t.Fatal("Close waited for the viewer ticket source instead of cancelling it")
			}
		})

		t.Run("WithoutViewerTickets", func(t *testing.T) {
			// The writer source keeps being asked, and a writer grant is refused.
			e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter, mutate: func(cfg *leadattach.Config) { cfg.ReconnectAsViewer = true }})
			hc := e.attach(wire.RoleWriter)
			e.tickets.give(1)
			hc.nc.Close()
			next := e.h.accept()
			next.welcome(wire.RoleWriter)
			requireDetach(t, next)
		})

		t.Run("DefaultKeepsTheWriter", func(t *testing.T) {
			e := newEnv(t, setup{pipe: true, mode: leadattach.ModeWriter})
			hc := e.attach(wire.RoleWriter)
			hc.roleFrame(wire.RoleViewer, "another")
			e.until("viewer", func(st terminalconsole.LiveStatus) bool { return st.Role == terminalconsole.LiveViewer })
			e.tickets.give(1)
			hc.nc.Close()
			next := e.reattachAs(wire.RoleWriter)
			e.sink.Key(key('b', 0, "b"))
			if got := next.input(1); got != "b" {
				t.Fatalf("a client without ReconnectAsViewer sent %q, want the key typed after it reattached as writer", got)
			}
		})
	})

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
