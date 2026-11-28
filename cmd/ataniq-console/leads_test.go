package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/consoleclient"
	"ataniq/internal/terminalconsole"
	"ataniq/internal/terminalconsole/leadctl"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// fakeLeads is a lead client with no daemon: it answers with what the test
// set, and records what it was asked.
type fakeLeads struct {
	mu     sync.Mutex
	detect error
	list   consoleclient.LeadListing
	// receipt is the answer to every command; err, when set, is the error
	// instead.
	receipt consoleclient.LeadReceipt
	err     error
	calls   []string
	lists   int
	closed  int
}

func (f *fakeLeads) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeLeads) answer() (consoleclient.LeadReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.receipt, f.err
}

func (f *fakeLeads) Detect() error { return f.detect }
func (f *fakeLeads) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}
func (f *fakeLeads) List() (consoleclient.LeadListing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return f.list, f.err
}
func (f *fakeLeads) Define(role string) (consoleclient.LeadReceipt, error) {
	f.note("define " + role)
	return f.answer()
}
func (f *fakeLeads) Start(role string) (consoleclient.LeadReceipt, error) {
	f.note("start " + role)
	return f.answer()
}
func (f *fakeLeads) Stop(role string, epoch uint64) (consoleclient.LeadReceipt, error) {
	f.note("stop " + role)
	return f.answer()
}
func (f *fakeLeads) Ticket(role string, epoch uint64, writer bool) (consoleclient.LeadReceipt, error) {
	if writer {
		f.note("ticket " + role + " writer")
	} else {
		f.note("ticket " + role + " viewer")
	}
	return f.answer()
}
func (f *fakeLeads) Send(role string, epoch uint64, id, body string) (consoleclient.LeadReceipt, error) {
	f.note("send " + role)
	return f.answer()
}

func (f *fakeLeads) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists
}

func (f *fakeLeads) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeLeads) closedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func aLead() consoleclient.LeadRow {
	return consoleclient.LeadRow{Capabilities: consoleclient.LeadCapabilities{
		Directory: true, Define: true, Start: true, Stop: true, Inject: true, Writer: true, Viewer: true,
	}, Key: "alpha", Role: "lead-alpha", Defined: true, Provision: "done", State: "ready", Epoch: 3, Unit: "unit-alpha", Inject: true, MessagesEnabled: true}
}

func doneTicket() consoleclient.LeadReceipt {
	return consoleclient.LeadReceipt{
		CommandID: "c1", Op: consoleclient.LeadOpTicket, Role: "lead-alpha", Status: consoleclient.LeadDone, Epoch: 3,
		Folder: "/run/lead-alpha", TicketFile: "/run/lead-alpha/ticket",
	}
}

func TestTheKindOfATicketComesFromOneFunction(t *testing.T) {
	// The adapter's kind of attach comes from one function, which today says
	// terminal for every ticket.
	if got := attachKindOf(consoleclient.LeadReceipt{}); got != attachTerminal {
		t.Fatalf("kind %q", got)
	}
}

func TestCFCapsAdapterPartialOffer(t *testing.T) {
	row := aLead()
	row.Capabilities = consoleclient.LeadCapabilities{Directory: true, Viewer: true}
	other := row
	other.Role, other.MessagesEnabled = "lead-beta", false
	other.Capabilities.Viewer, other.Capabilities.Writer = false, true
	f := &fakeLeads{list: consoleclient.LeadListing{Leads: []consoleclient.LeadRow{row, other}}}
	list, err := newLeadsAdapter(f, true).List()
	if err != nil || len(list.Leads) != 2 {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
	for _, tc := range []struct {
		row     int
		control leadctl.Control
		want    leadctl.Availability
	}{
		{0, leadctl.Control{Op: leadctl.OpTicket}, leadctl.Availability{Offered: true}},
		{0, leadctl.Control{Op: leadctl.OpTicket, Writer: true}, leadctl.Availability{Reason: "writer-ticket-not-offered"}},
		{0, leadctl.Control{Op: leadctl.OpInject}, leadctl.Availability{Reason: "inject-not-offered"}},
		{1, leadctl.Control{Op: leadctl.OpTicket}, leadctl.Availability{Reason: "viewer-ticket-not-offered"}},
		{1, leadctl.Control{Op: leadctl.OpTicket, Writer: true}, leadctl.Availability{Offered: true}},
		{1, leadctl.Control{Op: leadctl.OpInject}, leadctl.Availability{Reason: "messages-off"}},
	} {
		if got := list.Leads[tc.row].Actions[tc.control]; got != tc.want {
			t.Errorf("row %d, %+v: got %+v, want %+v", tc.row, tc.control, got, tc.want)
		}
	}
}

// A row whose message was not delivered, not spent, with the holder's words, is
// handed to the view as it is.
func TestTheAdapterMapsAFreeMessageWithTheWordsOfTheLastOne(t *testing.T) {
	r := aLead()
	r.MessageSpent = false
	r.MessageResult = consoleclient.LeadDelivery{Outcome: "refused", Reason: "text", Written: "nothing"}
	f := &fakeLeads{list: consoleclient.LeadListing{Leads: []consoleclient.LeadRow{r}}}
	got, err := newLeadsAdapter(f, true).List()
	if err != nil || len(got.Leads) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if l := got.Leads[0]; l.MessageSpent || l.MessageResult != (leadctl.Delivery{Outcome: "refused", Reason: "text", Written: "nothing"}) {
		t.Fatalf("%+v", l)
	}
}

func TestTheAdapterMapsTheListing(t *testing.T) {
	f := &fakeLeads{list: consoleclient.LeadListing{
		MaxRunning: 4, Running: 1, LeftOut: 2,
		Leads: []consoleclient.LeadRow{
			func() consoleclient.LeadRow {
				r := aLead()
				r.MessageSpent = true
				r.StateReason = "holder-ready"
				r.MessageResult = consoleclient.LeadDelivery{Outcome: "not-now", Reason: "not-ready", Written: "nothing"}
				r.Last = &consoleclient.LeadReceipt{CommandID: "c9", Op: consoleclient.LeadOpStart, Role: "lead-alpha", Status: consoleclient.LeadRefused, Code: "max-running", Earlier: true}
				return r
			}(),
			{Key: "beta", Role: "lead-beta"},
		},
	}}
	got, err := newLeadsAdapter(f, true).List()
	if err != nil {
		t.Fatal(err)
	}
	want := leadctl.Listing{MaxRunning: 4, Running: 1, LeftOut: 2, Leads: []leadctl.Lead{
		{Key: "alpha", Role: "lead-alpha", Defined: true, Provision: "done", State: "ready", StateReason: "holder-ready", Epoch: 3, Unit: "unit-alpha", Inject: true, MessageSpent: true,
			MessageResult: leadctl.Delivery{Outcome: "not-now", Reason: "not-ready", Written: "nothing"},
			Last:          &leadctl.Receipt{CommandID: "c9", Op: leadctl.OpStart, Status: leadctl.Refused, Code: "max-running"}},
		{Key: "beta", Role: "lead-beta"},
	}}
	want.Leads[0].Actions = map[leadctl.Control]leadctl.Availability{
		{Op: leadctl.OpDefine}: {Offered: true}, {Op: leadctl.OpStart}: {Offered: true},
		{Op: leadctl.OpStop}: {Offered: true}, {Op: leadctl.OpInject}: {Offered: true},
		{Op: leadctl.OpTicket}: {Offered: true}, {Op: leadctl.OpTicket, Writer: true}: {Offered: true},
	}
	want.Leads[1].Actions = map[leadctl.Control]leadctl.Availability{
		{Op: leadctl.OpDefine}: {Reason: "define-not-offered"}, {Op: leadctl.OpStart}: {Reason: "start-not-offered"},
		{Op: leadctl.OpStop}: {Reason: "stop-not-offered"}, {Op: leadctl.OpInject}: {Reason: "messages-off"},
		{Op: leadctl.OpTicket}: {Reason: "viewer-ticket-not-offered"}, {Op: leadctl.OpTicket, Writer: true}: {Reason: "writer-ticket-not-offered"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestTheAdapterMapsEveryReceipt(t *testing.T) {
	ops := map[consoleclient.LeadOp]leadctl.Op{
		consoleclient.LeadOpDefine: leadctl.OpDefine, consoleclient.LeadOpStart: leadctl.OpStart,
		consoleclient.LeadOpStop: leadctl.OpStop, consoleclient.LeadOpTicket: leadctl.OpTicket,
		consoleclient.LeadOpInject: leadctl.OpInject,
	}
	statuses := map[consoleclient.LeadStatus]leadctl.Status{
		consoleclient.LeadInProgress: leadctl.InProgress, consoleclient.LeadDone: leadctl.Done,
		consoleclient.LeadRefused: leadctl.Refused, consoleclient.LeadUnavailable: leadctl.Unavailable,
		consoleclient.LeadUncertain: leadctl.Uncertain,
	}
	for cop, vop := range ops {
		for cst, vst := range statuses {
			f := &fakeLeads{receipt: consoleclient.LeadReceipt{
				CommandID: "c1", Op: cop, Role: "r", Status: cst, Code: "stale-epoch", Epoch: 7, Outcome: "not-now", Reason: "not-ready", Written: "nothing",
				Folder: "/f", TicketFile: "/f/t",
			}}
			a := newLeadsAdapter(f, true)
			for name, call := range map[string]func() (leadctl.Receipt, error){
				"define": func() (leadctl.Receipt, error) { return a.Define("r") },
				"start":  func() (leadctl.Receipt, error) { return a.Start("r") },
				"stop":   func() (leadctl.Receipt, error) { return a.Stop("r", 7) },
				"ticket": func() (leadctl.Receipt, error) { return a.Ticket("r", 7, true) },
				"send":   func() (leadctl.Receipt, error) { return a.Send("r", 7, "m1", "hi") },
			} {
				got, err := call()
				if err != nil {
					t.Fatalf("%s %s %s: %v", name, cop, cst, err)
				}
				want := leadctl.Receipt{CommandID: "c1", Op: vop, Status: vst, Code: "stale-epoch", Epoch: 7, Outcome: "not-now", Reason: "not-ready", Written: "nothing", Folder: "/f", TicketFile: "/f/t"}
				if got != want {
					t.Fatalf("%s %s %s: got %+v want %+v", name, cop, cst, got, want)
				}
			}
		}
	}
	// The arguments reach the client as given.
	f := &fakeLeads{receipt: doneTicket()}
	a := newLeadsAdapter(f, true)
	a.Define("r1")
	a.Start("r2")
	a.Stop("r3", 4)
	a.Ticket("r4", 4, true)
	a.Ticket("r5", 4, false)
	a.Send("r6", 4, "m", "b")
	want := []string{"define r1", "start r2", "stop r3", "ticket r4 writer", "ticket r5 viewer", "send r6"}
	if got := f.callList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %v want %v", got, want)
	}
}

func TestTheAdapterShowsEveryErrorAsOneFixedLine(t *testing.T) {
	hostile := errors.New("\x1b]0;owned\x07 a peer-written secret boom")
	errs := []error{
		// Plain errors of the client that are no refusal the view has words
		// for, a bad answer, and a failure of the connection.
		consoleclient.ErrCommandUnavailable, consoleclient.ErrTooManyPending,
		consoleclient.ErrNoCommandID, consoleclient.ErrAnswerLost, consoleclient.ErrLeadsClosed,
		consoleclient.ErrNoLeadControl, consoleclient.ErrLeadsVersion,
		&consoleclient.Unavailable{State: consoleclient.StateBadAnswer}, hostile,
	}
	for _, e := range errs {
		f := &fakeLeads{err: e}
		a := newLeadsAdapter(f, true)
		for name, call := range map[string]func() (leadctl.Receipt, error){
			"define": func() (leadctl.Receipt, error) { return a.Define("r") },
			"start":  func() (leadctl.Receipt, error) { return a.Start("r") },
			"stop":   func() (leadctl.Receipt, error) { return a.Stop("r", 0) },
			"ticket": func() (leadctl.Receipt, error) { return a.Ticket("r", 1, false) },
			"send":   func() (leadctl.Receipt, error) { return a.Send("r", 1, "m", "b") },
		} {
			r, err := call()
			if err != errNoReceipt || r != (leadctl.Receipt{}) {
				t.Fatalf("%s with %v: %+v, %v", name, e, r, err)
			}
		}
		if l, err := a.List(); err != errNoReceipt || len(l.Leads) != 0 {
			t.Fatalf("list with %v: %+v, %v", e, l, err)
		}
	}
	if strings.ContainsAny(errNoReceipt.Error(), "\x1b/") {
		t.Fatalf("the fixed error holds text: %q", errNoReceipt)
	}
}

func TestTheAdapterGivesAnEarlierReceiptToTheViewWithTheFlag(t *testing.T) {
	f := &fakeLeads{}
	a := newLeadsAdapter(f, true)
	for _, r := range []consoleclient.LeadReceipt{
		{CommandID: "c1", Op: consoleclient.LeadOpTicket, Role: "r", Status: consoleclient.LeadDone, Epoch: 1, Folder: "/f", TicketFile: "/f/t", Earlier: true},
		{CommandID: "c2", Op: consoleclient.LeadOpStart, Role: "r", Status: consoleclient.LeadDone, Epoch: 1, Earlier: true},
		{CommandID: "c3", Op: consoleclient.LeadOpInject, Role: "r", Status: consoleclient.LeadRefused, Code: "stale-epoch", Earlier: true},
	} {
		f.receipt = r
		var got leadctl.Receipt
		var err error
		switch r.Op {
		case consoleclient.LeadOpTicket:
			got, err = a.Ticket("r", 1, true)
		case consoleclient.LeadOpStart:
			got, err = a.Start("r")
		default:
			got, err = a.Send("r", 1, "m", "b")
		}
		if err != nil || !got.Earlier || got.CommandID != r.CommandID || got.Code != r.Code || got.Epoch != r.Epoch || string(got.Status) != string(r.Status) {
			t.Fatalf("%s: %+v, %v", r.Op, got, err)
		}
	}
	// The remembered kind of an earlier ticket is harmless: the view makes no
	// attach from an earlier receipt (its own tests). It is still the same
	// ticket's kind when the ticket is later taken, not earlier.
	f.receipt = doneTicket()
	got, err := a.Ticket("lead-alpha", 3, true)
	if err != nil || got.Status != leadctl.Done || got.Earlier {
		t.Fatalf("%+v, %v", got, err)
	}
	// A receipt that is not earlier does not carry the flag.
	f.receipt = consoleclient.LeadReceipt{CommandID: "c9", Op: consoleclient.LeadOpStart, Role: "r", Status: consoleclient.LeadDone}
	if got, err := a.Start("r"); err != nil || got.Earlier {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestTheAdapterGivesARefusalBeforeAdmissionAsARefusedReceipt(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{consoleclient.ErrLeadNotListed, "not-listed"},
		{consoleclient.ErrCommandConflict, "conflict"},
		{consoleclient.ErrLeadRejected, "invalid-request"},
		{consoleclient.ErrLeadRequest, "invalid-request"},
		// Wrapped, as an error of the client may be.
		{fmt.Errorf("wrapped: %w", consoleclient.ErrLeadNotListed), "not-listed"},
	} {
		f := &fakeLeads{err: c.err}
		a := newLeadsAdapter(f, true)
		for name, call := range map[string]struct {
			do func() (leadctl.Receipt, error)
			op leadctl.Op
		}{
			"define": {func() (leadctl.Receipt, error) { return a.Define("r") }, leadctl.OpDefine},
			"start":  {func() (leadctl.Receipt, error) { return a.Start("r") }, leadctl.OpStart},
			"stop":   {func() (leadctl.Receipt, error) { return a.Stop("r", 0) }, leadctl.OpStop},
			"ticket": {func() (leadctl.Receipt, error) { return a.Ticket("r", 1, false) }, leadctl.OpTicket},
			"send":   {func() (leadctl.Receipt, error) { return a.Send("r", 1, "m", "b") }, leadctl.OpInject},
		} {
			r, err := call.do()
			want := leadctl.Receipt{Op: call.op, Status: leadctl.Refused, Code: c.code}
			if err != nil || r != want {
				t.Fatalf("%s with %v: %+v, %v", name, c.err, r, err)
			}
		}
		// Nothing is remembered for an attach from a refused ticket.
		if k := a.attachKind(terminalconsole.AttachRequest{}); k != "" {
			t.Fatalf("kind %q", k)
		}
	}
}

func TestAdapterCapabilityRefusals(t *testing.T) {
	caps := consoleclient.LeadCapabilities{Directory: true}
	f := &fakeLeads{}
	a := newLeadsAdapter(f, true)
	for _, tc := range []struct {
		op     consoleclient.LeadOp
		writer bool
		call   func() (leadctl.Receipt, error)
		code   string
	}{
		{consoleclient.LeadOpDefine, false, func() (leadctl.Receipt, error) { return a.Define("r") }, "define-not-offered"},
		{consoleclient.LeadOpStart, false, func() (leadctl.Receipt, error) { return a.Start("r") }, "start-not-offered"},
		{consoleclient.LeadOpStop, false, func() (leadctl.Receipt, error) { return a.Stop("r", 1) }, "stop-not-offered"},
		{consoleclient.LeadOpInject, false, func() (leadctl.Receipt, error) { return a.Send("r", 1, "m", "hello") }, "inject-not-offered"},
		{consoleclient.LeadOpTicket, false, func() (leadctl.Receipt, error) { return a.Ticket("r", 1, false) }, "viewer-ticket-not-offered"},
		{consoleclient.LeadOpTicket, true, func() (leadctl.Receipt, error) { return a.Ticket("r", 1, true) }, "writer-ticket-not-offered"},
	} {
		f.err = caps.CheckCommand(tc.op, tc.writer)
		got, err := tc.call()
		want := leadctl.Receipt{Op: leadctl.Op(tc.op), Status: leadctl.Refused, Code: tc.code}
		if got != want || err != nil {
			t.Errorf("%s: got %+v, %v; want %+v", tc.code, got, err, want)
		}
	}
}

func TestTheAdapterRemembersTheKindOfADoneTicketOnly(t *testing.T) {
	f := &fakeLeads{receipt: doneTicket()}
	a := newLeadsAdapter(f, true)
	req := terminalconsole.AttachRequest{Role: "lead-alpha", Epoch: 3, Writer: true, Folder: "/run/lead-alpha", TicketFile: "/run/lead-alpha/ticket"}
	if k := a.attachKind(req); k != "" {
		t.Fatalf("before a ticket: %q", k)
	}
	f.receipt.Status = consoleclient.LeadRefused
	f.receipt.Code = "not-running"
	a.Ticket("lead-alpha", 3, true)
	if k := a.attachKind(req); k != "" {
		t.Fatalf("after a refused ticket: %q", k)
	}
	f.receipt = doneTicket()
	a.Ticket("lead-alpha", 3, true)
	if k := a.attachKind(req); k != attachTerminal {
		t.Fatalf("after a done ticket: %q", k)
	}
	// The kind comes from the ticket's receipt, through the one function.
	a.kindOf = func(consoleclient.LeadReceipt) attachKind { return "web" }
	a.Ticket("lead-alpha", 3, true)
	if k := a.attachKind(req); k != "web" {
		t.Fatalf("kind %q", k)
	}
	// The memory is bounded.
	for i := 0; i < 3*maxKinds; i++ {
		f.receipt.Folder = "/run/" + strings.Repeat("x", i%7+1) + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
		a.Ticket("lead-alpha", 3, true)
	}
	a.mu.Lock()
	n := len(a.kinds)
	a.mu.Unlock()
	if n > maxKinds {
		t.Fatalf("%d kinds", n)
	}
}

// rigFor makes a daemon rig with the given lead client.
// rigFor is the rig of a console started with --scratch-writer.
func rigFor(t *testing.T, f leadsClient) *daemonRig { return rigForWriter(t, f, true) }

// rigForWriter is the rig of a console started with or without --scratch-writer.
func rigForWriter(t *testing.T, f leadsClient, scratchWriter bool) *daemonRig {
	t.Helper()
	args := []string{"--daemon", t.TempDir()}
	if scratchWriter {
		args = append(args, "--scratch-writer")
	}
	cfg, _, ok := parse(args, &bytes.Buffer{})
	if !ok {
		t.Fatal("parse")
	}
	cfg.options.Refresh = -1
	cfg.options.ApprovalsRefresh = -1
	rig, err := newDaemonRig(cfg, func(string, bool) (leadsClient, error) { return f, nil })
	if err != nil {
		t.Fatal(err)
	}
	rig.read = func(context.Context, string) consoleclient.Result { return consoleclient.Result{} }
	t.Cleanup(rig.Close)
	return rig
}

func TestTheLeadsViewIsGivenOnlyToADaemonWithLeadControl(t *testing.T) {
	for _, c := range []struct {
		name   string
		detect error
		new    error
		has    bool
	}{
		{"all six operations", nil, nil, true},
		{"none", consoleclient.ErrNoLeadControl, nil, false},
		{"some", consoleclient.ErrLeadsVersion, nil, false},
		{"not asked", &consoleclient.Unavailable{State: consoleclient.StateNoDaemon}, nil, false},
		{"no client", nil, errors.New("no client"), false},
	} {
		f := &fakeLeads{detect: c.detect, list: consoleclient.LeadListing{Leads: []consoleclient.LeadRow{aLead()}}}
		cfg, _, _ := parse([]string{"--daemon", t.TempDir()}, &bytes.Buffer{})
		rig, err := newDaemonRig(cfg, func(string, bool) (leadsClient, error) {
			if c.new != nil {
				return nil, c.new
			}
			return f, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := rig.cfg.options.Leads != nil; got != c.has || (rig.leads != nil) != c.has {
			t.Fatalf("%s: leads backend %v, want %v", c.name, got, c.has)
		}
		rig.Close()
		// The client is closed once with the rig when it is used, and closed
		// at once when it is not.
		if c.new == nil && f.closedCount() != 1 {
			t.Fatalf("%s: the client was closed %d times", c.name, f.closedCount())
		}
		if rig.attachKind(terminalconsole.AttachRequest{}) != "" {
			t.Fatalf("%s: a kind with no ticket", c.name)
		}
	}
}

func TestWithoutLeadControlLSaysSo(t *testing.T) {
	rig := rigFor(t, &fakeLeads{detect: consoleclient.ErrNoLeadControl})
	d := newViewDriver(t, rig.model())
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	d.send(openLeadsKey())
	d.until("the screen without lead control", func(v string) bool { return strings.Contains(v, terminalconsole.NoLeadControl) })
}

func TestWithLeadControlLOpensTheListAndTheKeysReachTheClient(t *testing.T) {
	f := &fakeLeads{list: consoleclient.LeadListing{MaxRunning: 2, Leads: []consoleclient.LeadRow{aLead()}}}
	f.receipt = consoleclient.LeadReceipt{CommandID: "c1", Op: consoleclient.LeadOpStart, Role: "lead-alpha", Status: consoleclient.LeadRefused, Code: "max-running"}
	rig := rigFor(t, f)
	d := newViewDriver(t, rig.model())
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	d.send(openLeadsKey())
	d.until("the list", func(v string) bool { return strings.Contains(v, "LEAD CONTROL") && strings.Contains(v, "alpha") })
	d.send(tea.KeyPressMsg{Code: 's', Text: "s"})
	d.until("the receipt", func(v string) bool { return strings.Contains(v, "the limit of running leads is reached") })
	if got := f.callList(); !reflect.DeepEqual(got, []string{"start lead-alpha"}) {
		t.Fatalf("calls %v", got)
	}
}

// A start the daemon refuses because this build does not launch the lead's
// recorded profile reaches the screen with its own words, which say to define
// the lead again.
func TestARefusedStartOfAProfileNotLaunchedSaysDefineAgain(t *testing.T) {
	f := &fakeLeads{list: consoleclient.LeadListing{MaxRunning: 2, Leads: []consoleclient.LeadRow{aLead()}}}
	f.receipt = consoleclient.LeadReceipt{CommandID: "c1", Op: consoleclient.LeadOpStart, Role: "lead-alpha", Status: consoleclient.LeadRefused, Code: "profile-not-launchable"}
	rig := rigFor(t, f)
	d := newViewDriver(t, rig.model())
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	d.send(openLeadsKey())
	d.until("the list", func(v string) bool { return strings.Contains(v, "LEAD CONTROL") && strings.Contains(v, "alpha") })
	d.send(tea.KeyPressMsg{Code: 's', Text: "s"})
	d.until("the receipt", func(v string) bool {
		return strings.Contains(v, "start: refused: this build does not launch the lead's recorded profile; define the lead again")
	})
}

// What the daemon's row says reaches the screen: the state's reason draws the
// ready line, and the message's words draw the MSG column, with nothing guessed
// by the view: a message that was not delivered shows as free while the daemon
// says it is not spent, and as spent and not delivered while it says it is.
func TestTheViewDrawsTheStateReasonAndTheMessageResultTheDaemonGave(t *testing.T) {
	row := aLead()
	row.MessageSpent = false
	row.StateReason = "holder-ready"
	row.MessageResult = consoleclient.LeadDelivery{Outcome: "not-now", Reason: "not-ready", Written: "nothing"}
	f := &fakeLeads{list: consoleclient.LeadListing{MaxRunning: 2, Leads: []consoleclient.LeadRow{row}}}
	rig := rigFor(t, f)
	d := newViewDriver(t, rig.model())
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	d.send(openLeadsKey())
	d.until("the ready line", func(v string) bool { return strings.Contains(v, "alpha: ready (holder-ready)") })
	d.until("the message's fate", func(v string) bool { return strings.Contains(v, "free (not delivered)") })
	spentRow := row
	spentRow.MessageSpent = true
	f3 := &fakeLeads{list: consoleclient.LeadListing{MaxRunning: 2, Leads: []consoleclient.LeadRow{spentRow}}}
	d3 := newViewDriver(t, rigFor(t, f3).model())
	d3.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	d3.send(openLeadsKey())
	d3.until("the spent message's fate", func(v string) bool { return strings.Contains(v, "spent, not delivered") })

	// A row without them, as a daemon that gives none, draws the plain state
	// and the message by the daemon's flag alone.
	row.StateReason, row.MessageResult = "", consoleclient.LeadDelivery{}
	f2 := &fakeLeads{list: consoleclient.LeadListing{MaxRunning: 2, Leads: []consoleclient.LeadRow{row}}}
	d2 := newViewDriver(t, rigFor(t, f2).model())
	d2.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	d2.send(openLeadsKey())
	v := d2.until("the list", func(v string) bool { return strings.Contains(v, "alpha") })
	if strings.Contains(v, "holder-ready") || strings.Contains(v, "not delivered") {
		t.Fatalf("the view drew what the row did not say:\n%s", v)
	}
}

func TestAnErrorOfTheClientIsTheViewsFixedLine(t *testing.T) {
	f := &fakeLeads{list: consoleclient.LeadListing{Leads: []consoleclient.LeadRow{aLead()}}}
	rig := rigFor(t, f)
	d := newViewDriver(t, rig.model())
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	d.send(openLeadsKey())
	d.until("the list", func(v string) bool { return strings.Contains(v, "alpha") })
	f.mu.Lock()
	f.err = errors.New("\x1b]0;owned\x07 secret text of the peer")
	f.mu.Unlock()
	d.send(tea.KeyPressMsg{Code: 's', Text: "s"})
	v := d.until("the fixed line", func(v string) bool { return strings.Contains(v, "no answer came back") })
	if strings.Contains(v, "secret") || strings.Contains(v, "owned") {
		t.Fatalf("the error's text is on the screen:\n%s", v)
	}
}

func TestTheTerminalAttachIsTheAttachFlagsWithTheRequestsFolder(t *testing.T) {
	cfg, _, _ := parse([]string{"--daemon", t.TempDir(), "--scratch-writer", "--theme", "light"}, &bytes.Buffer{})
	got, err := terminalAttachConfig(cfg, terminalconsole.AttachRequest{Role: "r", Epoch: 2, Writer: true, Folder: "/run/lead-alpha", TicketFile: "/run/lead-alpha/ticket"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.attach == nil || got.attach.socket != "/run/lead-alpha/"+attachSocketName || got.attach.ticketFile != "/run/lead-alpha/ticket" || got.attach.viewer {
		t.Fatalf("attach %+v", got.attach)
	}
	if got.attach.shiftEnter != shiftEnters[0] || got.options.Theme != "light" || got.daemon != "" {
		t.Fatalf("config %+v", got)
	}
	got, err = terminalAttachConfig(cfg, terminalconsole.AttachRequest{Role: "r", Epoch: 2, Writer: false, Folder: "/run/x", TicketFile: "/run/x/t"}, &fakeLeads{})
	if err != nil || !got.attach.viewer || got.attach.tickets == nil || got.attach.expectLead != "r" || got.attach.expectEpoch != 2 {
		t.Fatalf("viewer: %+v, %v", got.attach, err)
	}
	// The model is the --attach one: the live session is its source.
	m := attachModel(got)
	m.Close()
	long := "/" + strings.Repeat("d", maxSocketPath)
	_, err = terminalAttachConfig(cfg, terminalconsole.AttachRequest{Folder: long, TicketFile: "/t"}, nil)
	if err != errAttachPath || strings.Contains(err.Error(), "ddd") {
		t.Fatalf("a folder too long: %v", err)
	}
}

// loopRig drives the loop with fakes: a console is a script run against the
// model, and an attach is a recorded call.
type loopRig struct {
	t      *testing.T
	rig    *daemonRig
	f      *fakeLeads
	mu     sync.Mutex
	events []string
	errout bytes.Buffer
	// scripts[i] is the i-th console's life.
	scripts []func(d *viewDriver)
	runs    int
	// attachErr is what a terminal attach returns.
	attachErr error
	attached  []terminalconsole.AttachRequest
	// listsAtAttach is how many times the list was read when the attach ran.
	listsAtAttach int
}

func newLoopRig(t *testing.T) *loopRig {
	f := &fakeLeads{list: consoleclient.LeadListing{MaxRunning: 2, Leads: []consoleclient.LeadRow{aLead()}}, receipt: doneTicket()}
	return &loopRig{t: t, f: f, rig: rigFor(t, f)}
}

func (l *loopRig) event(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, s)
}

func (l *loopRig) run(m *terminalconsole.Model, openLeads bool) error {
	l.mu.Lock()
	i := l.runs
	l.runs++
	l.mu.Unlock()
	if openLeads {
		l.event("console open")
	} else {
		l.event("console")
	}
	d := newViewDriver(l.t, m)
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	if openLeads {
		d.send(openLeadsKey())
	}
	if i >= len(l.scripts) {
		l.t.Fatalf("console %d was not scripted", i)
	}
	l.scripts[i](d)
	return nil
}

func (l *loopRig) loop() (code int) {
	runner := func(m *terminalconsole.Model, open bool) error { return l.run(m, open) }
	return daemonLoop{
		rig: l.rig, run: runner, errout: &l.errout,
		attach: map[attachKind]attachRunner{attachTerminal: func(req terminalconsole.AttachRequest) error {
			l.event("attach")
			l.listsAtAttach = l.f.listCount()
			l.attached = append(l.attached, req)
			return l.attachErr
		}},
	}.loop()
}

// listShown waits for the leads view to show the list.
func listShown(d *viewDriver) {
	d.until("the list", func(v string) bool { return strings.Contains(v, "LEAD CONTROL") && strings.Contains(v, "alpha") })
}

func attachRequested(d *viewDriver) {
	d.until("the attach request", func(string) bool { _, ok := d.m.Attach(); return ok })
}

func quitLeads(d *viewDriver) {
	d.send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
}

func TestTheLoopGoesFromTheListToTheAttachAndBackToAFreshList(t *testing.T) {
	l := newLoopRig(t)
	l.scripts = []func(d *viewDriver){
		func(d *viewDriver) {
			d.send(tea.KeyPressMsg{Code: 'L', Text: "L"})
			listShown(d)
			d.send(tea.KeyPressMsg{Code: 'w', Text: "w"})
			attachRequested(d)
		},
		func(d *viewDriver) {
			listShown(d)
			quitLeads(d)
		},
	}
	if code := l.loop(); code != 0 {
		t.Fatalf("exit %d: %s", code, l.errout.String())
	}
	if want := []string{"console", "attach", "console open"}; !reflect.DeepEqual(l.events, want) {
		t.Fatalf("events %v want %v", l.events, want)
	}
	want := terminalconsole.AttachRequest{Role: "lead-alpha", Epoch: 3, Writer: true, Folder: "/run/lead-alpha", TicketFile: "/run/lead-alpha/ticket"}
	if len(l.attached) != 1 || l.attached[0] != want {
		t.Fatalf("attached %+v want %+v", l.attached, want)
	}
	// The list the second console shows was read after the attach.
	if got := l.f.listCount(); got <= l.listsAtAttach || l.listsAtAttach < 1 {
		t.Fatalf("lists: %d before the attach, %d at the end", l.listsAtAttach, got)
	}
	// One ticket was asked for, as a writer; the second console sent nothing.
	if got := l.f.callList(); !reflect.DeepEqual(got, []string{"ticket lead-alpha writer"}) {
		t.Fatalf("calls %v", got)
	}
	// The client lives through the attach and ends with the rig.
	if l.f.closedCount() != 0 {
		t.Fatal("the lead client was closed with the first console")
	}
}

func TestTheLoopAttachesAsAViewerWhenAsked(t *testing.T) {
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
	if code := l.loop(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(l.attached) != 1 || l.attached[0].Writer {
		t.Fatalf("attached %+v", l.attached)
	}
}

func TestTheLoopEndsWhenTheOwnerQuitsWithoutAnAttach(t *testing.T) {
	l := newLoopRig(t)
	l.scripts = []func(d *viewDriver){func(d *viewDriver) {
		d.send(openLeadsKey())
		listShown(d)
		quitLeads(d)
	}}
	if code := l.loop(); code != 0 || l.runs != 1 || len(l.attached) != 0 {
		t.Fatalf("exit %d, %d consoles, %d attaches", code, l.runs, len(l.attached))
	}
}

func TestTheLoopReturnsToTheListWhenTheAttachFails(t *testing.T) {
	l := newLoopRig(t)
	l.attachErr = errors.New("the attach did not work")
	l.scripts = []func(d *viewDriver){
		func(d *viewDriver) {
			d.send(openLeadsKey())
			listShown(d)
			d.send(tea.KeyPressMsg{Code: 'w', Text: "w"})
			attachRequested(d)
		},
		func(d *viewDriver) { listShown(d); quitLeads(d) },
	}
	if code := l.loop(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if l.runs != 2 || !strings.Contains(l.errout.String(), "the attach did not work") {
		t.Fatalf("%d consoles; errout %q", l.runs, l.errout.String())
	}
}

func TestTheLoopChoosesTheAttachByItsKind(t *testing.T) {
	// A ticket of another kind goes to that kind's runner, and a kind with
	// none is told in one line and the owner is back at the list.
	for _, c := range []struct {
		name     string
		kind     attachKind
		runners  func(l *loopRig) map[attachKind]attachRunner
		wantKind string
		wantLine string
	}{
		{"another kind", "web", func(l *loopRig) map[attachKind]attachRunner {
			return map[attachKind]attachRunner{
				attachTerminal: func(terminalconsole.AttachRequest) error { l.event("terminal"); return nil },
				"web":          func(terminalconsole.AttachRequest) error { l.event("web"); return nil },
			}
		}, "web", ""},
		{"a kind with no runner", "hologram", func(l *loopRig) map[attachKind]attachRunner {
			return map[attachKind]attachRunner{attachTerminal: func(terminalconsole.AttachRequest) error { l.event("terminal"); return nil }}
		}, "", "does not carry out the kind of attach"},
	} {
		l := newLoopRig(t)
		l.rig.leads.kindOf = func(consoleclient.LeadReceipt) attachKind { return c.kind }
		l.scripts = []func(d *viewDriver){
			func(d *viewDriver) {
				d.send(openLeadsKey())
				listShown(d)
				d.send(tea.KeyPressMsg{Code: 'w', Text: "w"})
				attachRequested(d)
			},
			func(d *viewDriver) { listShown(d); quitLeads(d) },
		}
		code := daemonLoop{
			rig: l.rig, errout: &l.errout, attach: c.runners(l),
			run: func(m *terminalconsole.Model, open bool) error { return l.run(m, open) },
		}.loop()
		if code != 0 {
			t.Fatalf("%s: exit %d", c.name, code)
		}
		var want []string
		if c.wantKind != "" {
			want = []string{"console", c.wantKind, "console open"}
		} else {
			want = []string{"console", "console open"}
		}
		if !reflect.DeepEqual(l.events, want) {
			t.Fatalf("%s: events %v want %v", c.name, l.events, want)
		}
		if !strings.Contains(l.errout.String(), c.wantLine) || (c.wantLine != "" && strings.Contains(l.errout.String(), "hologram")) {
			t.Fatalf("%s: errout %q", c.name, l.errout.String())
		}
	}
}

func TestTheLoopStopsWhenAConsoleFails(t *testing.T) {
	l := newLoopRig(t)
	var errout bytes.Buffer
	code := daemonLoop{
		rig: l.rig, errout: &errout,
		run: func(m *terminalconsole.Model, open bool) error { return errors.New("no terminal") },
	}.loop()
	if code != 1 || !strings.Contains(errout.String(), "console: no terminal") {
		t.Fatalf("exit %d: %q", code, errout.String())
	}
}

func TestTheDaemonConsoleWithoutLeadControlRunsAsBefore(t *testing.T) {
	rig := rigFor(t, &fakeLeads{detect: consoleclient.ErrNoLeadControl})
	var errout bytes.Buffer
	code := daemonLoop{
		rig: rig, errout: &errout,
		run: func(m *terminalconsole.Model, open bool) error {
			if open {
				t.Error("the leads view was asked for")
			}
			if _, ok := m.Attach(); ok {
				t.Error("an attach with no lead control")
			}
			return nil
		},
	}.loop()
	if code != 0 || errout.Len() != 0 {
		t.Fatalf("exit %d: %q", code, errout.String())
	}
}

func TestTheOpenLeadsKeyIsTheKeyThatOpensTheView(t *testing.T) {
	// The same message the loop sends opens the screen even at a small
	// size, before the program has told the console its size.
	rig := rigFor(t, &fakeLeads{detect: consoleclient.ErrNoLeadControl})
	m := rig.model()
	defer m.Close()
	m.Update(openLeadsKey())
	if v := viewOf(m, 80, 24); !strings.Contains(v, terminalconsole.NoLeadControl) {
		t.Fatalf("the key did not open the leads screen:\n%s", v)
	}
}

// --scratch-writer is the writer attach's only switch. Without it the offer
// for the writer ticket is the one that says how to turn it on, whatever the
// daemon offers; with it, the daemon's offer decides.
func TestRowActionsOfferTheWriterOnlyWithTheFlag(t *testing.T) {
	writer := leadctl.Control{Op: leadctl.OpTicket, Writer: true}
	viewer := leadctl.Control{Op: leadctl.OpTicket}
	for _, tc := range []struct {
		name          string
		scratchWriter bool
		daemonWriter  bool
		want          leadctl.Availability
	}{
		{"flag and offer", true, true, leadctl.Availability{Offered: true}},
		{"flag, no offer", true, false, leadctl.Availability{Reason: "writer-ticket-not-offered"}},
		{"no flag, offer", false, true, leadctl.Availability{Reason: leadctl.ReasonWriterOff}},
		{"no flag, no offer", false, false, leadctl.Availability{Reason: leadctl.ReasonWriterOff}},
	} {
		row := aLead()
		row.Capabilities.Writer = tc.daemonWriter
		got := rowActions(row, tc.scratchWriter)
		if got[writer] != tc.want {
			t.Errorf("%s: writer %+v, want %+v", tc.name, got[writer], tc.want)
		}
		if got[viewer] != (leadctl.Availability{Offered: true}) || !got[leadctl.Control{Op: leadctl.OpStart}].Offered {
			t.Errorf("%s: the other controls changed: %+v", tc.name, got)
		}
	}
}

// Without the flag a writer ticket request that reaches the adapter all the
// same is refused there: the lead client is not asked, and a viewer ticket is.
func TestTheAdapterNeverAsksForAWriterTicketWithoutTheFlag(t *testing.T) {
	f := &fakeLeads{receipt: doneTicket()}
	a := newLeadsAdapter(f, false)
	r, err := a.Ticket("lead-alpha", 3, true)
	if err != nil || r.Status != leadctl.Refused || r.Op != leadctl.OpTicket || r.Code != leadctl.ReasonWriterOff {
		t.Fatalf("writer ticket: %+v, %v", r, err)
	}
	if got := f.callList(); len(got) != 0 {
		t.Fatalf("the lead client was asked: %v", got)
	}
	if r, err := a.Ticket("lead-alpha", 3, false); err != nil || r.Status != leadctl.Done {
		t.Fatalf("viewer ticket: %+v, %v", r, err)
	}
	if got := f.callList(); !reflect.DeepEqual(got, []string{"ticket lead-alpha viewer"}) {
		t.Fatalf("calls %v", got)
	}
}

// The leads view without --scratch-writer: the hint has no [w] and says how to
// turn it on, w shows the same words and calls nothing, and v still attaches.
func TestTheLeadsViewIsViewerOnlyWithoutTheFlag(t *testing.T) {
	f := &fakeLeads{list: consoleclient.LeadListing{Leads: []consoleclient.LeadRow{aLead()}}, receipt: doneTicket()}
	rig := rigForWriter(t, f, false)
	d := newViewDriver(t, rig.model())
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	d.send(openLeadsKey())
	listShown(d)
	view := d.until("the writer-off hint", func(v string) bool { return strings.Contains(v, "writer off: start with --scratch-writer") })
	if strings.Contains(view, "[w]") || strings.Contains(view, "attach as writer") || !strings.Contains(view, "[v] attach as viewer") {
		t.Fatalf("the hint:\n%s", view)
	}
	d.send(tea.KeyPressMsg{Code: 'w', Text: "w"})
	// The key's own answer is a line of its own, set before any command runs.
	var noted bool
	for _, line := range strings.Split(ansi.Strip(d.m.View().Content), "\n") {
		noted = noted || strings.TrimSpace(line) == "writer off: start with --scratch-writer"
	}
	if !noted {
		t.Fatalf("w did not say why:\n%s", ansi.Strip(d.m.View().Content))
	}
	if _, ok := d.m.Attach(); ok {
		t.Fatal("w asked for an attach")
	}
	time.Sleep(50 * time.Millisecond)
	if got := f.callList(); len(got) != 0 {
		t.Fatalf("w reached the lead client: %v", got)
	}
}

// --scratch-writer belongs to --daemon.
func TestScratchWriterFlag(t *testing.T) {
	var errout bytes.Buffer
	for _, args := range [][]string{
		{"--scratch-writer"}, {"--demo", "--scratch-writer"}, {"--attach", t.TempDir(), "--ticket-file", "/t", "--scratch-writer"},
	} {
		errout.Reset()
		if _, code, ok := parse(args, &errout); ok || code != 2 || !strings.Contains(errout.String(), "--scratch-writer needs --daemon") {
			t.Errorf("%v: ok %t, code %d, %q", args, ok, code, errout.String())
		}
	}
	cfg, _, ok := parse([]string{"--daemon", t.TempDir()}, &errout)
	if !ok || cfg.scratchWriter {
		t.Fatalf("--daemon alone: %+v, ok %t", cfg, ok)
	}
	cfg, _, ok = parse([]string{"--daemon", t.TempDir(), "--scratch-writer"}, &errout)
	if !ok || !cfg.scratchWriter {
		t.Fatalf("--daemon --scratch-writer: %+v, ok %t", cfg, ok)
	}
	// A writer attach without the flag is refused where the attach is made.
	cfg, _, _ = parse([]string{"--daemon", t.TempDir()}, &errout)
	req := terminalconsole.AttachRequest{Role: "r", Epoch: 2, Writer: true, Folder: "/run/x", TicketFile: "/run/x/t"}
	if _, err := terminalAttachConfig(cfg, req, &fakeLeads{}); !errors.Is(err, errWriterOff) {
		t.Fatalf("a writer attach without the flag: %v", err)
	}
	req.Writer = false
	if _, err := terminalAttachConfig(cfg, req, &fakeLeads{}); err != nil {
		t.Fatalf("a viewer attach without the flag: %v", err)
	}
}
