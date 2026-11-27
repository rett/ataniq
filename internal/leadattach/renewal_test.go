package leadattach_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadattach"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
)

type viewerTicketFunc func(context.Context) (string, error)

func (f viewerTicketFunc) ViewerTicket(ctx context.Context) (string, error) { return f(ctx) }

func TestViewerRoleMismatchBackoff(t *testing.T) {
	source := viewerTicketFunc(func(context.Context) (string, error) { return wire.NewTicket() })
	e := newEnv(t, setup{pipe: true, viewerTickets: source})
	for range 2 {
		hc := e.h.accept()
		hc.welcome(wire.RoleWriter)
		if frames := hc.closed(); string(frames) != string(wire.TypeDetach) {
			t.Fatalf("frames after mismatched welcome: %q", frames)
		}
	}
	hc := e.h.accept()
	hc.welcome(wire.RoleViewer)
	hc.snapshot()
	e.untilConn(terminalconsole.LiveAttached)
	waits := e.clock.asked()
	if len(waits) != 2 || waits[0] != 2*leadattach.TicketPoll || waits[1] != 4*leadattach.TicketPoll {
		t.Errorf("retry delays after two role mismatches: %v", waits)
	}
}

func TestViewerRenewsAfterDroppedHelloAndResync(t *testing.T) {
	leakCheck(t)
	issued := make(chan string, 3)
	source := viewerTicketFunc(func(context.Context) (string, error) {
		ticket, err := wire.NewTicket()
		if err == nil {
			issued <- ticket
		}
		return ticket, err
	})
	e := newEnv(t, setup{pipe: true, viewerTickets: source, mode: leadattach.ModeWriter, mutate: func(c *leadattach.Config) {
		c.TicketReady = func() error { return errTest } // the scratch file is unavailable
	}})
	redeemed := make(map[string]bool)
	accept := func() *hconn {
		t.Helper()
		hc := e.h.accept()
		select {
		case ticket := <-issued:
			if hc.hello.Ticket != ticket || redeemed[ticket] {
				t.Fatal("hello did not redeem a fresh ticket from the source")
			}
			redeemed[ticket] = true
		default:
			t.Fatal("hello without a new issuance")
		}
		return hc
	}

	first := accept()
	e.sink.Key(keyA)
	first.nc.Close() // ticket consumed, welcome lost
	if types := first.closed(); len(types) != 0 {
		t.Fatalf("frames after the first hello: %q", types)
	}

	second := accept()
	second.welcome(wire.RoleViewer)
	second.snapshot()
	e.untilConn(terminalconsole.LiveAttached)
	e.sink.Key(keyA)
	second.corrupt("A", "B")
	second.check()
	if types := second.closed(); string(types) != string(wire.TypeDetach) {
		t.Fatalf("frames before resync: %q", types)
	}

	third := accept()
	e.sink.Paste("during resync")
	third.welcome(wire.RoleViewer)
	third.pane.Write([]byte("renewed screen"))
	third.snapshot()
	st := e.untilConn(terminalconsole.LiveAttached)
	e.sameScreen(third)
	e.sink.Key(keyA)
	e.c.Resize(100, 30)
	if st.Role != terminalconsole.LiveViewer || st.Resyncs != 1 || e.c.Status().RefusedInput != 4 {
		t.Errorf("status after renewal: %+v", e.c.Status())
	}
	e.c.Close()
	if types := third.closed(); string(types) != string(wire.TypeDetach) {
		t.Errorf("frames from renewed viewer: %q", types)
	}
}

func TestViewerTicketSourceRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, ticket, reason string
		err                  error
	}{
		{"refused", "", "test error", errTest},
		{"malformed", "bad", "malformed ticket", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, setup{pipe: true, viewerTickets: viewerTicketFunc(func(context.Context) (string, error) {
				return tc.ticket, tc.err
			})})
			st := e.untilConn(terminalconsole.LiveWaitingTicket)
			if !strings.Contains(st.Reason, tc.reason) {
				t.Errorf("reason %q, want %q", st.Reason, tc.reason)
			}
			e.c.Close()
			for _, hc := range e.h.all {
				if types := hc.closed(); len(types) != 0 {
					t.Errorf("frames after ticket refusal: %q", types)
				}
			}
		})
	}
}

func TestViewerTicketSourceRequired(t *testing.T) {
	e := newEnv(t, setup{pipe: true, noStart: true})
	c := leadattach.NewViewer(leadattach.Config{Socket: e.h.path, Tickets: e.tickets.get}, nil)
	defer c.Close()
	if _, err := c.Start(terminalconsole.LiveStart{Pane: e.pane}); err == nil || !strings.Contains(err.Error(), "no ticket source") {
		t.Fatalf("start without a viewer ticket source: %v", err)
	}
}

func TestViewerTicketSourceCanceledOnClose(t *testing.T) {
	asking := make(chan struct{})
	e := newEnv(t, setup{pipe: true, viewerTickets: viewerTicketFunc(func(ctx context.Context) (string, error) {
		close(asking)
		<-ctx.Done()
		return "", ctx.Err()
	})})
	select {
	case <-asking:
	case <-time.After(wait):
		t.Fatal("source never asked for a ticket")
	}
	e.c.Close()
	if types := e.h.all[0].closed(); len(types) != 0 {
		t.Errorf("frames after canceled request: %q", types)
	}
}
