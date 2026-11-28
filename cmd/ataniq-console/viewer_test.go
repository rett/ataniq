package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/consoleclient"
	"ataniq/internal/leadattach"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
	"ataniq/internal/terminalconsole/sessionpane"
	tea "charm.land/bubbletea/v2"
)

// scriptedTickets is the lead client of these tests: it answers each ticket
// command with the next step of a script, which may write a ticket file, and
// records what it was asked. It opens no socket.
type scriptedTickets struct {
	mu    sync.Mutex
	calls []ticketCall
	// steps are the answers in order; the last one repeats.
	steps []func(n int) (consoleclient.LeadReceipt, error)
}

type ticketCall struct {
	role   string
	epoch  uint64
	writer bool
}

func (s *scriptedTickets) Ticket(role string, epoch uint64, writer bool) (consoleclient.LeadReceipt, error) {
	s.mu.Lock()
	n := len(s.calls)
	s.calls = append(s.calls, ticketCall{role, epoch, writer})
	step := s.steps[min(n, len(s.steps)-1)]
	s.mu.Unlock()
	return step(n)
}

func (s *scriptedTickets) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *scriptedTickets) asked() []ticketCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ticketCall(nil), s.calls...)
}

// ticketPlace is a lead's folder and ticket file in a private temporary
// folder.
type ticketPlace struct {
	t            *testing.T
	folder, file string
}

func newTicketPlace(t *testing.T) ticketPlace {
	t.Helper()
	dir := t.TempDir()
	return ticketPlace{t: t, folder: dir, file: filepath.Join(dir, "ticket")}
}

// put writes a new ticket into the ticket file, as the daemon does, and
// returns it.
func (p ticketPlace) put() string {
	p.t.Helper()
	tk, err := wire.NewTicket()
	if err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(p.file, []byte(tk+"\n"), 0o600); err != nil {
		p.t.Fatal(err)
	}
	if err := os.Chmod(p.file, 0o600); err != nil {
		p.t.Fatal(err)
	}
	return tk
}

func (p ticketPlace) done(epoch uint64) consoleclient.LeadReceipt {
	return consoleclient.LeadReceipt{
		CommandID: "c", Op: consoleclient.LeadOpTicket, Role: "lead-alpha", Status: consoleclient.LeadDone,
		Epoch: epoch, Folder: p.folder, TicketFile: p.file,
	}
}

// mints is the step of a daemon that mints a new ticket for each command.
func (p ticketPlace) mints(epoch uint64, minted *[]string) func(int) (consoleclient.LeadReceipt, error) {
	return func(int) (consoleclient.LeadReceipt, error) {
		*minted = append(*minted, p.put())
		return p.done(epoch), nil
	}
}

func refused(code string) consoleclient.LeadReceipt {
	return consoleclient.LeadReceipt{
		CommandID: "c", Op: consoleclient.LeadOpTicket, Role: "lead-alpha", Status: consoleclient.LeadRefused, Code: code,
	}
}

func fail(err error) func(int) (consoleclient.LeadReceipt, error) {
	return func(int) (consoleclient.LeadReceipt, error) { return consoleclient.LeadReceipt{}, err }
}

func answer(r consoleclient.LeadReceipt) func(int) (consoleclient.LeadReceipt, error) {
	return func(int) (consoleclient.LeadReceipt, error) { return r, nil }
}

// pollClock waits a millisecond for what the client waits half a second.
type pollClock struct{}

type pollTimer struct{ t *time.Timer }

func (pollClock) NewTimer(time.Duration) leadattach.Timer {
	return pollTimer{time.NewTimer(time.Millisecond)}
}
func (p pollTimer) C() <-chan time.Time { return p.t.C }
func (p pollTimer) Stop() bool          { return p.t.Stop() }

// pipeHolder is the holder of these tests: each dial gets one end of an
// in-memory pipe, and the holder reads the hello, reports its ticket and
// drops the connection.
type pipeHolder struct {
	hellos chan string
	dials  chan struct{}
}

func newPipeHolder() *pipeHolder {
	return &pipeHolder{hellos: make(chan string, 16), dials: make(chan struct{}, 64)}
}

func (h *pipeHolder) dial(context.Context, string) (net.Conn, error) {
	h.dials <- struct{}{}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		f, err := wire.NewReader(server, wire.ToHolder).Next()
		if err != nil || f.Type != wire.TypeHello {
			return
		}
		if hello, err := wire.DecodeHello(f.Payload); err == nil {
			h.hellos <- hello.Ticket
		}
	}()
	return client, nil
}

func (h *pipeHolder) next(t *testing.T) string {
	t.Helper()
	select {
	case tk := <-h.hellos:
		return tk
	case <-time.After(5 * time.Second):
		t.Fatal("the holder was not dialled with a hello")
		return ""
	}
}

// viewerEnv is a viewer attach client with a source over leads, started on
// an in-memory pane.
type viewerEnv struct {
	t      *testing.T
	c      *leadattach.Client
	holder *pipeHolder
}

func startViewer(t *testing.T, leads viewerTicketer, place ticketPlace, epoch uint64, first string) *viewerEnv {
	t.Helper()
	e := &viewerEnv{t: t, holder: newPipeHolder()}
	a := &attachConfig{
		socket: filepath.Join(place.folder, attachSocketName), viewer: true,
		tickets:    newViewerTickets(leads, "lead-alpha", epoch, place.folder, first),
		expectLead: "lead-alpha", expectEpoch: epoch,
	}
	e.c = viewerLive(a, leadattach.Config{Socket: a.socket, Name: clientName, Dial: e.holder.dial, Clock: pollClock{}})
	pane := sessionpane.New(80, 24, sessionpane.Options{})
	t.Cleanup(pane.Close)
	t.Cleanup(e.c.Close)
	if _, err := e.c.Start(terminalconsole.LiveStart{Pane: pane, Cols: 80, Rows: 24, Colours: sessionpane.NeutralColours, Wake: func() {}}); err != nil {
		t.Fatal(err)
	}
	return e
}

// until waits for cond on the client's status.
func (e *viewerEnv) until(what string, cond func(terminalconsole.LiveStatus) bool) terminalconsole.LiveStatus {
	e.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		st := e.c.Status()
		if cond(st) {
			return st
		}
		select {
		case <-deadline:
			e.t.Fatalf("no %s; status %v, %q", what, st.Conn, st.Reason)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestCF03ViewerRenewal(t *testing.T) {
	t.Run("RenewsOnDrop", func(t *testing.T) {
		place := newTicketPlace(t)
		var minted []string
		leads := &scriptedTickets{steps: []func(int) (consoleclient.LeadReceipt, error){place.mints(3, &minted)}}
		viewTicket := place.put() // the ticket the leads view made
		e := startViewer(t, leads, place, 3, place.file)
		// The holder drops each connection after the hello. The first connects
		// with the ticket the view made, which the holder would refuse a
		// second request for while it is outstanding; each later connection
		// has a ticket of its own, from a new command, read once.
		got := []string{e.holder.next(t), e.holder.next(t), e.holder.next(t)}
		if got[0] != viewTicket {
			t.Fatalf("the first connection did not use the view's ticket")
		}
		leads.mu.Lock()
		asked := append([]ticketCall(nil), leads.calls[:2]...)
		leads.mu.Unlock()
		if len(minted) < 2 || got[1] != minted[0] || got[2] != minted[1] {
			t.Fatalf("connections %d and %d did not use the tickets of two new commands", 2, 3)
		}
		if got[0] == got[1] || got[1] == got[2] || got[0] == got[2] {
			t.Fatal("a ticket was used twice")
		}
		for _, c := range asked {
			if c != (ticketCall{"lead-alpha", 3, false}) {
				t.Fatalf("a ticket command was %+v, want the selected lead and epoch as a viewer", c)
			}
		}
	})

	t.Run("WrongEpochReselects", func(t *testing.T) {
		for _, c := range []struct {
			name string
			step func(place ticketPlace) func(int) (consoleclient.LeadReceipt, error)
		}{
			{"stale epoch", func(ticketPlace) func(int) (consoleclient.LeadReceipt, error) {
				return answer(refused("stale-epoch"))
			}},
			{"lead not running", func(ticketPlace) func(int) (consoleclient.LeadReceipt, error) {
				return answer(refused("not-running"))
			}},
			{"lead not listed", func(ticketPlace) func(int) (consoleclient.LeadReceipt, error) {
				return fail(consoleclient.ErrLeadNotListed)
			}},
			{"receipt of another epoch", func(p ticketPlace) func(int) (consoleclient.LeadReceipt, error) {
				return func(int) (consoleclient.LeadReceipt, error) { p.put(); return p.done(4), nil }
			}},
		} {
			t.Run(c.name, func(t *testing.T) {
				place := newTicketPlace(t)
				leads := &scriptedTickets{steps: []func(int) (consoleclient.LeadReceipt, error){c.step(place)}}
				src := newViewerTickets(leads, "lead-alpha", 3, place.folder, "")
				if _, err := src.ViewerTicket(context.Background()); !errors.Is(err, leadattach.ErrReselect) {
					t.Fatalf("the source said %v, want a reselect", err)
				}
				e := startViewer(t, leads, place, 3, "")
				st := e.until("the reselect stop", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveStoppedReselect })
				if !strings.Contains(st.Reason, "select the lead again") {
					t.Fatalf("reason %q", st.Reason)
				}
				// The client stopped: it asks no more and never dialled.
				before := leads.count()
				time.Sleep(20 * time.Millisecond)
				if leads.count() != before || len(e.holder.dials) != 0 {
					t.Fatalf("after the stop: %d more commands, %d dials", leads.count()-before, len(e.holder.dials))
				}
			})
		}

		// The loop that carried out the attach goes back to a list read afresh.
		l := newLoopRig(t)
		l.scripts = []func(d *viewDriver){
			func(d *viewDriver) {
				d.send(openLeadsKey())
				listShown(d)
				d.send(tea.KeyPressMsg{Code: 'v', Text: "v"})
				attachRequested(d)
			},
			func(d *viewDriver) { listShown(d); quitLeads(d) },
		}
		var listsAtStop int
		runner := func(m *terminalconsole.Model, open bool) error { return l.run(m, open) }
		code := daemonLoop{
			rig: l.rig, run: runner, errout: &l.errout,
			attach: map[attachKind]attachRunner{attachTerminal: func(req terminalconsole.AttachRequest) error {
				l.event("attach")
				// The lead was restarted: the daemon says the epoch is stale.
				l.f.mu.Lock()
				l.f.receipt = refused("stale-epoch")
				l.f.mu.Unlock()
				m, err := terminalAttachConfig(config{}, req, l.f)
				if err != nil {
					return err
				}
				src := m.attach.tickets
				if _, err := src.ViewerTicket(context.Background()); !errors.Is(err, leadattach.ErrReselect) {
					t.Errorf("the source said %v, want a reselect", err)
				}
				listsAtStop = l.f.listCount()
				return nil
			}},
		}.loop()
		if code != 0 || !reflect.DeepEqual(l.events, []string{"console", "attach", "console open"}) {
			t.Fatalf("exit %d, events %v, %s", code, l.events, l.errout.String())
		}
		if l.f.listCount() <= listsAtStop || listsAtStop < 1 {
			t.Fatalf("the list was read %d times at the stop and %d at the end", listsAtStop, l.f.listCount())
		}
	})

	t.Run("BusyIsDisplayed", func(t *testing.T) {
		for _, c := range []struct {
			name string
			step func(int) (consoleclient.LeadReceipt, error)
			want string
		}{
			{"the holder refused", answer(refused("ticket-refused")), "busy"},
			{"the daemon could not take it now", fail(consoleclient.ErrCommandUnavailable), "busy"},
			{"another refusal", answer(refused("holder-version-skew")), "holder-version-skew"},
		} {
			t.Run(c.name, func(t *testing.T) {
				place := newTicketPlace(t)
				leads := &scriptedTickets{steps: []func(int) (consoleclient.LeadReceipt, error){c.step}}
				_, err := newViewerTickets(leads, "lead-alpha", 3, place.folder, "").ViewerTicket(context.Background())
				if err == nil || errors.Is(err, leadattach.ErrNoTicket) || errors.Is(err, leadattach.ErrReselect) {
					t.Fatalf("the source said %v, want an error to display", err)
				}
				if !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), place.folder) {
					t.Fatalf("error %q", err)
				}
				// The client shows the reason and asks again.
				e := startViewer(t, leads, place, 3, "")
				st := e.until("the reason", func(st terminalconsole.LiveStatus) bool {
					return st.Conn == terminalconsole.LiveWaitingTicket && strings.Contains(st.Reason, c.want)
				})
				if strings.Contains(st.Reason, place.folder) {
					t.Fatalf("the status names a folder: %q", st.Reason)
				}
				e.until("a second ask", func(terminalconsole.LiveStatus) bool { return leads.count() >= 3 })
			})
		}
	})

	t.Run("DaemonDownIsNoTicket", func(t *testing.T) {
		for name, err := range map[string]error{
			"no daemon":    &consoleclient.Unavailable{State: consoleclient.StateNoDaemon},
			"slow daemon":  &consoleclient.Unavailable{State: consoleclient.StateSlow},
			"at its limit": &consoleclient.Unavailable{State: consoleclient.StateUnavailable},
			"answer lost":  consoleclient.ErrAnswerLost,
			"client shut":  consoleclient.ErrLeadsClosed,
		} {
			leads := &scriptedTickets{steps: []func(int) (consoleclient.LeadReceipt, error){fail(err)}}
			_, got := newViewerTickets(leads, "lead-alpha", 3, "/run/x", "").ViewerTicket(context.Background())
			if !errors.Is(got, leadattach.ErrNoTicket) {
				t.Errorf("%s: the source said %v, want no ticket", name, got)
			}
		}
		// A daemon that cannot be understood is not "down": it is shown.
		leads := &scriptedTickets{steps: []func(int) (consoleclient.LeadReceipt, error){fail(&consoleclient.Unavailable{State: consoleclient.StateBadAnswer})}}
		if _, got := newViewerTickets(leads, "lead-alpha", 3, "/run/x", "").ViewerTicket(context.Background()); got == nil || errors.Is(got, leadattach.ErrNoTicket) {
			t.Errorf("a bad answer was %v", got)
		}
		// With the daemon down the client waits with no reason, and connects
		// when the daemon is back.
		place := newTicketPlace(t)
		var minted []string
		down := fail(&consoleclient.Unavailable{State: consoleclient.StateNoDaemon})
		up := place.mints(3, &minted)
		leads = &scriptedTickets{steps: []func(int) (consoleclient.LeadReceipt, error){down, down, up}}
		e := startViewer(t, leads, place, 3, "")
		st := e.until("a wait", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveWaitingTicket })
		if st.Reason != "waiting for a ticket" {
			t.Fatalf("reason %q", st.Reason)
		}
		if got := e.holder.next(t); len(minted) == 0 || got != minted[0] {
			t.Fatalf("the connection did not use the ticket of the command after the outage")
		}
	})

	t.Run("KeptIDReplaySpentTicket", func(t *testing.T) {
		place := newTicketPlace(t)
		var minted []string
		earlier := func(int) (consoleclient.LeadReceipt, error) {
			// The kept command is answered again with its receipt, not run
			// again: its ticket was minted for the first ask, and the file
			// has been read since.
			r := place.done(3)
			r.Earlier = true
			return r, nil
		}
		leads := &scriptedTickets{steps: []func(int) (consoleclient.LeadReceipt, error){
			fail(consoleclient.ErrAnswerLost), // the answer to the first ask never came
			earlier,                           // the second ask: the kept id's receipt
			place.mints(3, &minted),           // the third ask: a new command
		}}
		src := newViewerTickets(leads, "lead-alpha", 3, place.folder, "")
		ctx := context.Background()
		if _, err := src.ViewerTicket(ctx); !errors.Is(err, leadattach.ErrNoTicket) {
			t.Fatalf("a lost answer was %v", err)
		}
		// The ticket of the first command was minted and read since: the file
		// is empty, and a spent ticket is no ticket.
		old := place.put()
		if got, err := leadattach.FileTickets(place.file)(); err != nil || got != old {
			t.Fatalf("setup: %q, %v", got, err)
		}
		if got, err := src.ViewerTicket(ctx); !errors.Is(err, leadattach.ErrNoTicket) || got != "" {
			t.Fatalf("a spent ticket was %q, %v", got, err)
		}
		fresh, err := src.ViewerTicket(ctx)
		if err != nil || len(minted) != 1 || fresh != minted[0] || fresh == old {
			t.Fatalf("the new command's ticket was %q, %v (old %q)", fresh, err, old)
		}
		// A ticket is read once: the file is empty again, and nothing is kept.
		if _, err := leadattach.FileTickets(place.file)(); !errors.Is(err, leadattach.ErrNoTicket) {
			t.Fatalf("the ticket file still held the ticket: %v", err)
		}
		// The view's own ticket is read once too: when it is spent the daemon
		// is asked.
		first := newTicketPlace(t)
		viewTicket := first.put()
		steps := &scriptedTickets{steps: []func(int) (consoleclient.LeadReceipt, error){first.mints(3, &minted)}}
		s2 := newViewerTickets(steps, "lead-alpha", 3, first.folder, first.file)
		if got, err := s2.ViewerTicket(ctx); err != nil || got != viewTicket || steps.count() != 0 {
			t.Fatalf("the view's ticket was %q, %v after %d commands", got, err, steps.count())
		}
		if got, err := s2.ViewerTicket(ctx); err != nil || got == viewTicket || steps.count() != 1 {
			t.Fatalf("the next ticket was %q, %v after %d commands", got, err, steps.count())
		}
		// A cancelled ask makes no command.
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s2.ViewerTicket(cancelled); !errors.Is(err, context.Canceled) || steps.count() != 1 {
			t.Fatalf("a cancelled ask was %v after %d commands", err, steps.count())
		}
	})

	t.Run("WriterFallsBackToViewerTickets", func(t *testing.T) {
		cfg, _, _ := parse([]string{"--daemon", t.TempDir(), "--scratch-writer"}, &bytes.Buffer{})
		place := newTicketPlace(t)
		var minted []string
		leads := &scriptedTickets{steps: []func(int) (consoleclient.LeadReceipt, error){place.mints(3, &minted)}}
		// The ticket file holds the writer ticket the view made; the viewer
		// tickets of the fallback are commands of their own, never that file.
		writerTicket := place.put()
		req := terminalconsole.AttachRequest{Role: "lead-alpha", Epoch: 3, Writer: true, Folder: place.folder, TicketFile: place.file}
		got, err := terminalAttachConfig(cfg, req, leads)
		if err != nil {
			t.Fatal(err)
		}
		lc := fileTicketConfig(got.attach, leadattach.ModeWriter)
		if n := leads.count(); n != 0 {
			t.Fatalf("%d ticket commands before the writer role was lost", n)
		}
		tk, err := lc.ViewerTickets.ViewerTicket(context.Background())
		if err != nil || tk == writerTicket || len(minted) != 1 || tk != minted[0] {
			t.Fatalf("viewer ticket %q, %v; minted %v, writer ticket %q", tk, err, minted, writerTicket)
		}
		if asked := leads.asked(); len(asked) != 1 || asked[0] != (ticketCall{"lead-alpha", 3, false}) {
			t.Fatalf("asked %+v, want one viewer ticket for the lead and epoch", asked)
		}
		// The client hands it its own context, which Close cancels: a
		// cancelled ask makes no command.
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := lc.ViewerTickets.ViewerTicket(cancelled); !errors.Is(err, context.Canceled) || leads.count() != 1 {
			t.Fatalf("a cancelled ask was %v after %d commands", err, leads.count())
		}
	})

	t.Run("WriterPathUnchanged", func(t *testing.T) {
		cfg, _, _ := parse([]string{"--daemon", t.TempDir(), "--scratch-writer"}, &bytes.Buffer{})
		leads := &scriptedTickets{steps: []func(int) (consoleclient.LeadReceipt, error){fail(errors.New("no command is made"))}}
		req := terminalconsole.AttachRequest{Role: "lead-alpha", Epoch: 3, Writer: true, Folder: "/run/lead-alpha", TicketFile: "/run/lead-alpha/ticket"}
		got, err := terminalAttachConfig(cfg, req, leads)
		if err != nil {
			t.Fatal(err)
		}
		a := got.attach
		if a.viewer || a.tickets != nil || a.expectLead != "" || a.expectEpoch != 0 || a.ticketFile != req.TicketFile || a.socket != "/run/lead-alpha/"+attachSocketName {
			t.Fatalf("writer attach %+v", a)
		}
		// It reads its one ticket from the file; once the writer role is lost it
		// becomes a viewer with viewer tickets from the daemon, asked for
		// afresh (no ticket file is the first of them).
		if a.writerFallback == nil {
			t.Fatal("the writer has no viewer tickets for after the writer role is lost")
		}
		lc := fileTicketConfig(a, leadattach.ModeWriter)
		if lc.Mode != leadattach.ModeWriter || !lc.ReconnectAsViewer || lc.ViewerTickets == nil {
			t.Fatalf("writer config: mode %v, reconnect as viewer %t, viewer tickets %t", lc.Mode, lc.ReconnectAsViewer, lc.ViewerTickets != nil)
		}
		// With no lead client a writer still attaches, with no fallback; a
		// viewer cannot.
		bare, err := terminalAttachConfig(cfg, req, nil)
		if err != nil || bare.attach.writerFallback != nil {
			t.Fatalf("a writer without a lead client: %+v, %v", bare.attach, err)
		}
		if lc := fileTicketConfig(bare.attach, leadattach.ModeWriter); lc.ReconnectAsViewer || lc.ViewerTickets != nil {
			t.Fatalf("a writer without a fallback reconnects as a viewer: %+v", lc)
		}
		req.Writer = false
		if _, err := terminalAttachConfig(cfg, req, nil); !errors.Is(err, errNoViewerTickets) {
			t.Fatalf("a viewer without a lead client: %v", err)
		}
		// The writer's console is the file-ticket one and asks no one.
		m := attachModel(got)
		m.Close()
		// So is the --attach flag's viewer, which has no daemon to ask.
		cfg, _, ok := parse([]string{"--attach", t.TempDir(), "--ticket-file", filepath.Join(t.TempDir(), "t"), "--viewer"}, &bytes.Buffer{})
		if !ok || cfg.attach == nil || !cfg.attach.viewer || cfg.attach.tickets != nil {
			t.Fatalf("--attach --viewer: %+v", cfg.attach)
		}
		attachModel(cfg).Close()
		if n := leads.count(); n != 0 {
			t.Fatalf("%d ticket commands were made", n)
		}
	})
}
