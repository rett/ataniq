package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"ataniq/internal/consoleclient"
	"ataniq/internal/leadattach"
)

// The viewer's tickets. A viewer attach from the leads view renews its own
// ticket: each time the attach client connects, including after the holder
// dropped it, it asks this source, and the source asks the daemon for a viewer
// ticket for the lead and epoch the owner selected. A ticket is read from the
// ticket file once: the read empties the file, and a ticket is never kept or
// given twice. A writer attach is not renewed: it reads the one ticket the
// leads view made.

// viewerTicketer is the part of the lead client the source asks.
type viewerTicketer interface {
	Ticket(role string, epoch uint64, writer bool) (consoleclient.LeadReceipt, error)
}

var _ viewerTicketer = (leadsClient)(nil)

// The stale-selection codes of a refused ticket: the epoch is not the lead's,
// or the lead is not running. Both mean that what the owner selected is gone.
const (
	codeStaleEpoch = "stale-epoch"
	codeNotRunning = "not-running"
	// codeTicketRefused is the receipt code of a ticket the holder refused.
	// The holder does so for a viewer ticket while another is outstanding or
	// a viewer is attached.
	codeTicketRefused = "ticket-refused"
	codeRoleBusy      = "role-busy"
)

// The texts the source gives the attach client, which shows them in its
// status. Each is fixed or holds one word of the daemon's closed grammar of
// codes; none holds a path, a ticket or what a peer wrote.
var (
	errViewerBusy   = errors.New("the lead is busy with another viewer ticket or viewer; trying again")
	errViewerFolder = errors.New("the daemon named another lead folder than the one selected")
)

// viewerTickets is the daemon-backed leadattach.TicketSource of one viewer
// attach: one lead in one epoch, which the owner selected.
type viewerTickets struct {
	leads           viewerTicketer
	role            string
	epoch           uint64
	folder          string
	firstTicketFile string
	// read takes the one ticket of a ticket file and empties the file:
	// leadattach.FileTickets, or a test's own.
	read func(path string) (string, error)

	mu    sync.Mutex
	asked bool
}

var _ leadattach.TicketSource = (*viewerTickets)(nil)

// newViewerTickets returns the source of a viewer attach for req's lead and
// epoch. folder is where the lead's holder is and firstTicketFile the ticket
// file of the done ticket the leads view made: the first ask takes that
// ticket, because the holder refuses a second viewer ticket while one is
// outstanding. Every later ask is a command of its own.
func newViewerTickets(leads viewerTicketer, role string, epoch uint64, folder, firstTicketFile string) *viewerTickets {
	return &viewerTickets{
		leads: leads, role: role, epoch: epoch, folder: folder, firstTicketFile: firstTicketFile,
		read: func(path string) (string, error) { return leadattach.FileTickets(path)() },
	}
}

// ViewerTicket gives a ticket for this connection, or says why there is none:
// leadattach.ErrNoTicket when the daemon cannot be asked now or the ticket was
// spent, an error that wraps leadattach.ErrReselect when the lead or its epoch
// is gone, and another display-safe error for a refusal.
func (v *viewerTickets) ViewerTicket(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	v.mu.Lock()
	first := !v.asked
	v.asked = true
	v.mu.Unlock()
	if first && v.firstTicketFile != "" {
		// A ticket the leads view made is the first one. When it is spent or
		// is not one, the daemon is asked as for any later one.
		if t, err := v.read(v.firstTicketFile); err == nil {
			return t, nil
		}
	}
	r, err := v.leads.Ticket(v.role, v.epoch, false)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", viewerTicketError(err)
	}
	return v.fromReceipt(r)
}

// fromReceipt reads the ticket of the receipt the daemon answered.
func (v *viewerTickets) fromReceipt(r consoleclient.LeadReceipt) (string, error) {
	switch r.Status {
	case consoleclient.LeadDone:
	case consoleclient.LeadRefused:
		return "", refusedViewerTicket(r.Code)
	case consoleclient.LeadInProgress:
		return "", leadattach.ErrNoTicket
	default:
		return "", fmt.Errorf("the daemon could not make a viewer ticket (%s)", codeText(r.Code))
	}
	if r.Epoch != v.epoch {
		return "", fmt.Errorf("%w: the lead's epoch changed", leadattach.ErrReselect)
	}
	if r.Folder != v.folder {
		return "", errViewerFolder
	}
	// A done receipt that answers a command kept from an earlier ask (r.Earlier)
	// is not a new ticket: its file may be empty, which is a spent ticket and
	// no ticket. The read below says so, and the next ask is a new command.
	return v.read(r.TicketFile)
}

// refusedViewerTicket maps the code of a refused ticket.
func refusedViewerTicket(code string) error {
	switch code {
	case codeStaleEpoch, codeNotRunning:
		return fmt.Errorf("%w: the lead's epoch changed or the lead stopped", leadattach.ErrReselect)
	case codeTicketRefused, codeRoleBusy:
		return errViewerBusy
	}
	return fmt.Errorf("the daemon refused a viewer ticket (%s)", codeText(code))
}

// viewerTicketError maps an error of the lead client for a ticket command.
// The daemon could not be reached, or did not answer: no ticket yet. The lead
// is not the daemon's: select again. A refusal before admission, or a daemon
// that is busy, gets the client's own fixed text.
func viewerTicketError(err error) error {
	var un *consoleclient.Unavailable
	switch {
	case errors.Is(err, consoleclient.ErrLeadNotListed):
		return fmt.Errorf("%w: the daemon does not have that lead", leadattach.ErrReselect)
	case errors.Is(err, consoleclient.ErrAnswerLost), errors.Is(err, consoleclient.ErrLeadsClosed):
		// An answer that was lost keeps the command's id, so the next ask
		// returns its receipt and does not mint another ticket.
		return leadattach.ErrNoTicket
	case errors.Is(err, consoleclient.ErrCommandUnavailable):
		return errViewerBusy
	case errors.As(err, &un):
		switch un.State {
		case consoleclient.StateNoDaemon, consoleclient.StateSlow, consoleclient.StateUnavailable:
			return leadattach.ErrNoTicket
		}
		return errors.New(un.Error())
	case errors.Is(err, consoleclient.ErrLeadCapability):
		return errors.New("the daemon does not offer viewer tickets for this lead")
	case errors.Is(err, consoleclient.ErrCommandConflict):
		return errors.New("the daemon no longer holds the ticket command; asking again")
	case errors.Is(err, consoleclient.ErrLeadRejected), errors.Is(err, consoleclient.ErrLeadRequest):
		return errors.New("the daemon did not accept the viewer ticket request")
	}
	// Any other error of the client carries no text the view may show.
	return errors.New("the viewer ticket could not be asked for")
}

func codeText(s string) string {
	if s == "" {
		return "no code"
	}
	return s
}
