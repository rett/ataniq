package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"ataniq/internal/consoleclient"
	"ataniq/internal/terminalconsole"
	"ataniq/internal/terminalconsole/leadctl"
	tea "charm.land/bubbletea/v2"
)

// The leads view. With --daemon, when the daemon's hello lists the lead
// control's operations, the console is given the daemon's lead client as the
// view's backend, through the adapter below, and the owner reaches the view
// with L. The client is consoleclient's, the view is the console's, and this
// file is the only place where the two meet: the view imports no client and
// the client knows nothing of the view's types.
//
// An attach is not the console's to carry out: the view quits the console and
// asks for it, and the loop at the end of this file runs it by the kind of
// attach the ticket is for, and then opens the leads view again.

// errNoReceipt is the one error the adapter returns, for every error of the
// client that is not a refusal before admission (see answer). The view says what happened in a fixed line of its own and reads
// the list again, and it never shows an error's text; this value carries none
// either, so nothing the client or the daemon wrote can reach the view even
// when the view changes.
var errNoReceipt = errors.New("no receipt")

// attachKind says how an attach to a lead is carried out. It is chosen by the
// ticket the daemon gave, and the loop carries the attach out with the runner
// registered for the kind: the adapter assumes no terminal.
type attachKind string

// attachTerminal is the attach to a lead's terminal: the attach client with
// the holder's attach socket, which is the only kind there is today.
const attachTerminal attachKind = "terminal"

// attachKindOf is the kind of attach a done ticket's receipt is for. A
// receipt names none today, and every ticket the daemon gives is for the
// holder's terminal; when a receipt names a kind, this is the one place that
// reads it.
func attachKindOf(consoleclient.LeadReceipt) attachKind { return attachTerminal }

// leadsClient is the part of consoleclient.Leads this command uses: a test
// gives its own.
type leadsClient interface {
	Detect() error
	List() (consoleclient.LeadListing, error)
	Define(role string) (consoleclient.LeadReceipt, error)
	Start(role string) (consoleclient.LeadReceipt, error)
	Stop(role string, epoch uint64) (consoleclient.LeadReceipt, error)
	Ticket(role string, epoch uint64, writer bool) (consoleclient.LeadReceipt, error)
	Send(role string, epoch uint64, messageID, body string) (consoleclient.LeadReceipt, error)
	Close() error
}

var _ leadsClient = (*consoleclient.Leads)(nil)

// newLeadsFunc makes the lead client for a daemon: newLeadsClient, or a
// test's own.
type newLeadsFunc func(dir string) (leadsClient, error)

// newLeadsClient makes consoleclient's client for the daemon whose socket is
// in dir. It opens no connection.
func newLeadsClient(dir string) (leadsClient, error) {
	l, err := consoleclient.NewLeads(consoleclient.LeadsConfig{Dir: dir, Client: clientName})
	if err != nil {
		return nil, err
	}
	return l, nil
}

// attachTarget is where a ticket's attach is: the folder and ticket file of a
// done ticket, which the view hands back in its AttachRequest.
type attachTarget struct{ folder, ticketFile string }

// maxKinds bounds what the adapter remembers: the ticket of a lead names the
// same folder and file each time, so a few entries is all there is.
const maxKinds = 64

// leadsAdapter gives the console's leads view the lead client: it satisfies
// leadctl.Backend. It converts the client's values to the view's, and it
// remembers the kind of every done ticket, for the loop to read.
type leadsAdapter struct {
	c leadsClient
	// kindOf reads the attach kind of a done ticket: attachKindOf.
	kindOf func(consoleclient.LeadReceipt) attachKind

	mu    sync.Mutex
	kinds map[attachTarget]attachKind
}

var _ leadctl.Backend = (*leadsAdapter)(nil)

func newLeadsAdapter(c leadsClient) *leadsAdapter {
	return &leadsAdapter{c: c, kindOf: attachKindOf, kinds: map[attachTarget]attachKind{}}
}

// List reads the leads.
func (a *leadsAdapter) List() (leadctl.Listing, error) {
	l, err := a.c.List()
	if err != nil {
		return leadctl.Listing{}, errNoReceipt
	}
	out := leadctl.Listing{MaxRunning: l.MaxRunning, Running: l.Running, LeftOut: l.LeftOut}
	for _, r := range l.Leads {
		lead := leadctl.Lead{
			Key: r.Key, Role: r.Role, Defined: r.Defined, Provision: r.Provision,
			State: r.State, StateReason: r.StateReason, Epoch: r.Epoch, Unit: r.Unit, Inject: r.Inject, MessageSpent: r.MessageSpent,
			MessageResult: leadctl.Delivery{Outcome: r.MessageResult.Outcome, Reason: r.MessageResult.Reason, Written: r.MessageResult.Written},
		}
		if r.Last != nil {
			// The summary of a lead's last command: it is no answer to a
			// press, so Earlier is not read.
			if last, ok := viewReceipt(*r.Last); ok {
				lead.Last = &last
			}
		}
		out.Leads = append(out.Leads, lead)
	}
	return out, nil
}

// Define provisions and defines the lead role.
func (a *leadsAdapter) Define(role string) (leadctl.Receipt, error) {
	r, err := a.c.Define(role)
	return a.answer(leadctl.OpDefine, r, err)
}

// Start starts the lead role.
func (a *leadsAdapter) Start(role string) (leadctl.Receipt, error) {
	r, err := a.c.Start(role)
	return a.answer(leadctl.OpStart, r, err)
}

// Stop stops the lead role in epoch.
func (a *leadsAdapter) Stop(role string, epoch uint64) (leadctl.Receipt, error) {
	r, err := a.c.Stop(role, epoch)
	return a.answer(leadctl.OpStop, r, err)
}

// Ticket asks for an attach ticket. A done ticket's attach kind is
// remembered for the loop.
func (a *leadsAdapter) Ticket(role string, epoch uint64, writer bool) (leadctl.Receipt, error) {
	cr, err := a.c.Ticket(role, epoch, writer)
	r, err := a.answer(leadctl.OpTicket, cr, err)
	if err == nil && r.Status == leadctl.Done {
		a.remember(attachTarget{r.Folder, r.TicketFile}, a.kindOf(cr))
	}
	return r, err
}

// Send sends one message to the lead in epoch.
func (a *leadsAdapter) Send(role string, epoch uint64, messageID, body string) (leadctl.Receipt, error) {
	r, err := a.c.Send(role, epoch, messageID, body)
	return a.answer(leadctl.OpInject, r, err)
}

// attachKind is the kind of attach a request of the view is for: the kind of
// the ticket whose receipt carried the folder and ticket file. It is empty
// for a request the adapter did not hand out.
func (a *leadsAdapter) attachKind(req terminalconsole.AttachRequest) attachKind {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.kinds[attachTarget{req.Folder, req.TicketFile}]
}

func (a *leadsAdapter) remember(t attachTarget, k attachKind) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.kinds[t]; !ok && len(a.kinds) >= maxKinds {
		clear(a.kinds)
	}
	a.kinds[t] = k
}

// answer turns the client's answer to a command (op) into the view's.
//
// An error of the client that says that the command was refused before the
// daemon admitted it, and that nothing was done, becomes a Refused receipt
// with the code the view has words for (see refusedCode). Every other error
// of the client becomes errNoReceipt: the view's contract says an error comes
// only when no receipt came back, and it draws one fixed line for all of
// them and reads the list again, which is what the owner is to do next. The
// receipt made for a refusal has no command id, and nothing the client
// wrote is in it.
//
// A receipt that answers an earlier command (Earlier) is given to the view
// with the flag set: the view draws it as the answer to an earlier command
// and never attaches with it. This is the one place that reads the flag.
func (a *leadsAdapter) answer(op leadctl.Op, r consoleclient.LeadReceipt, err error) (leadctl.Receipt, error) {
	if err != nil {
		if code, ok := refusedCode(err); ok {
			return leadctl.Receipt{Op: op, Status: leadctl.Refused, Code: code}, nil
		}
		return leadctl.Receipt{}, errNoReceipt
	}
	out, ok := viewReceipt(r)
	if !ok {
		return leadctl.Receipt{}, errNoReceipt
	}
	out.Earlier = r.Earlier
	return out, nil
}

// refusedCode maps an error of the client for a command that was refused
// before the daemon admitted it to the reason code of a refusal, where the
// view has words for the code: not-listed, conflict and invalid-request. The
// other errors of the client are left to the fixed line of a lost answer:
// the client's own limits (too many commands without an answer, no command
// id) and "could not take the command now" are no refusal the daemon made,
// and the view has no words for them.
func refusedCode(err error) (string, bool) {
	switch {
	case errors.Is(err, consoleclient.ErrLeadNotListed):
		return "not-listed", true
	case errors.Is(err, consoleclient.ErrCommandConflict):
		return "conflict", true
	case errors.Is(err, consoleclient.ErrLeadRejected), errors.Is(err, consoleclient.ErrLeadRequest):
		return "invalid-request", true
	}
	return "", false
}

// viewReceipt converts a receipt of the client. It says false for an
// operation or a status the view does not have: the client checks both, so
// this is a guard, not a path.
func viewReceipt(r consoleclient.LeadReceipt) (leadctl.Receipt, bool) {
	out := leadctl.Receipt{
		CommandID: r.CommandID, Code: r.Code, Epoch: r.Epoch,
		Outcome: r.Outcome, Reason: r.Reason, Written: r.Written,
		Folder: r.Folder, TicketFile: r.TicketFile,
	}
	switch r.Op {
	case consoleclient.LeadOpDefine:
		out.Op = leadctl.OpDefine
	case consoleclient.LeadOpStart:
		out.Op = leadctl.OpStart
	case consoleclient.LeadOpStop:
		out.Op = leadctl.OpStop
	case consoleclient.LeadOpTicket:
		out.Op = leadctl.OpTicket
	case consoleclient.LeadOpInject:
		out.Op = leadctl.OpInject
	default:
		return leadctl.Receipt{}, false
	}
	switch r.Status {
	case consoleclient.LeadInProgress:
		out.Status = leadctl.InProgress
	case consoleclient.LeadDone:
		out.Status = leadctl.Done
	case consoleclient.LeadRefused:
		out.Status = leadctl.Refused
	case consoleclient.LeadUnavailable:
		out.Status = leadctl.Unavailable
	case consoleclient.LeadUncertain:
		out.Status = leadctl.Uncertain
	default:
		return leadctl.Receipt{}, false
	}
	return out, true
}

// detectLeads makes the lead client for dir and asks the daemon whether it
// has lead control: the adapter when its hello lists the lead operations, and
// nil otherwise, so that the console runs as it does without them. A daemon
// that could not be asked is "otherwise" too; the console is started again to
// ask again. The client is closed when it is not used.
func detectLeads(dir string, mk newLeadsFunc) *leadsAdapter {
	c, err := mk(dir)
	if err != nil {
		return nil
	}
	if c.Detect() != nil {
		c.Close()
		return nil
	}
	return newLeadsAdapter(c)
}

// openLeadsKey is the key that opens the leads view, which the loop sends to
// the console it opens again after an attach.
func openLeadsKey() tea.Msg { return tea.KeyPressMsg{Code: 'L', Text: "L"} }

// consoleRunner runs a console to its end. With openLeads it has the console
// open the leads view first.
type consoleRunner func(m *terminalconsole.Model, openLeads bool) error

// attachRunner carries out an attach of one kind, to its end.
type attachRunner func(req terminalconsole.AttachRequest) error

// errAttachPath is what the terminal attach says of a folder whose socket
// address does not fit. The text does not repeat the folder.
var errAttachPath = errors.New("the lead's attach socket path is too long for a unix socket address")

// terminalAttachConfig is the --attach configuration for a request of the view:
// the folder's attach socket and the ticket file, as a writer or as a
// viewer. The options are the console's own.
func terminalAttachConfig(cfg config, req terminalconsole.AttachRequest) (config, error) {
	socket := filepath.Join(req.Folder, attachSocketName)
	if len(socket) > maxSocketPath {
		return config{}, errAttachPath
	}
	return config{
		attach:  &attachConfig{socket: socket, ticketFile: req.TicketFile, viewer: !req.Writer, shiftEnter: shiftEnters[0]},
		options: cfg.options,
	}, nil
}

// terminalAttach returns the runner of the terminal attach: the existing
// --attach console, in the request's folder and with its ticket file.
func terminalAttach(cfg config, run consoleRunner) attachRunner {
	return func(req terminalconsole.AttachRequest) error {
		ac, err := terminalAttachConfig(cfg, req)
		if err != nil {
			return err
		}
		m := attachModel(ac)
		defer m.Close()
		return run(m, false)
	}
}

// daemonLoop is --daemon as the command line runs it: the daemon's console
// until the owner quits it, and, each time the leads view asks for an attach,
// the attach, and then the console again with the leads view open and the list
// read afresh, so that the owner goes list, attach, back.
type daemonLoop struct {
	rig *daemonRig
	run consoleRunner
	// attach is the runner of each kind of attach.
	attach map[attachKind]attachRunner
	errout io.Writer
}

// loop runs the console and returns the exit code.
func (l daemonLoop) loop() int {
	open := false
	for {
		m := l.rig.model()
		err := l.run(m, open)
		req, attach := m.Attach()
		m.Close()
		if err != nil {
			fmt.Fprintln(l.errout, "console:", err)
			return 1
		}
		if !attach {
			return 0
		}
		l.attachOnce(req)
		open = true
	}
}

// attachOnce carries out the attach the view asked for, by the kind of its
// ticket. Whatever happens the owner comes back to the leads view: a failure
// is told in a line of its own, without the folder or the error's peer text.
func (l daemonLoop) attachOnce(req terminalconsole.AttachRequest) {
	run, ok := l.attach[l.rig.attachKind(req)]
	if !ok {
		fmt.Fprintln(l.errout, "error: this console does not carry out the kind of attach that ticket is for")
		return
	}
	if err := run(req); err != nil {
		fmt.Fprintln(l.errout, "console:", err)
	}
}
