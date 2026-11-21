package leadctl

import (
	"image/color"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Backend is where the view reads the leads from and sends its commands to.
// The daemon's client satisfies it through an adapter that the command writes:
// this package imports no client, so the types below are its own.
//
// The view calls a backend only from a command (tea.Cmd), never from Update,
// SetSize or Draw, so a slow backend cannot hold the keys. Methods may be
// called from several goroutines at once.
//
// Every method that changes something returns the receipt of the command, and
// an error only when no receipt came back (the answer was lost). The view
// then shows one fixed line and reads the list again; it never sends the
// command again by itself. A backend that is called again with the same
// arguments while its earlier call has no final receipt must repeat the
// earlier command's id, so that nothing is done twice. The view never shows
// the text of an error.
type Backend interface {
	// List reads the leads and the receipt of the last command of each.
	List() (Listing, error)
	// Define provisions and defines the lead role.
	Define(role string) (Receipt, error)
	// Start starts the lead role.
	Start(role string) (Receipt, error)
	// Stop stops the lead role in epoch. An epoch of 0 means the current one.
	Stop(role string, epoch uint64) (Receipt, error)
	// Ticket asks for an attach ticket for the role in epoch, as a writer
	// or, with writer false, as a viewer.
	Ticket(role string, epoch uint64, writer bool) (Receipt, error)
	// Send sends one message to the lead in epoch. messageID is made by the
	// view and is the lowercase grammar of the lead holder's message ids.
	Send(role string, epoch uint64, messageID, body string) (Receipt, error)
}

// Status is the status of a receipt.
type Status string

// The statuses of a receipt.
const (
	// InProgress: the command was admitted and has not ended.
	InProgress Status = "in-progress"
	// Done: the command ended and did what it was asked.
	Done Status = "done"
	// Refused: the daemon refused the command and changed nothing that
	// matters.
	Refused Status = "refused"
	// Unavailable: the command could not be carried out.
	Unavailable Status = "unavailable"
	// Uncertain: the daemon cannot say what became of the command, as after
	// a restart while it ran.
	Uncertain Status = "uncertain"
)

// Op is the operation a receipt is for.
type Op string

// The operations.
const (
	OpDefine Op = "define"
	OpStart  Op = "start"
	OpStop   Op = "stop"
	OpTicket Op = "ticket"
	OpInject Op = "inject"
)

// The outcomes of an inject that reached the lead's holder.
const (
	OutcomeSubmitted = "submitted"
	OutcomeNotNow    = "not-now"
	OutcomeRefused   = "refused"
	OutcomeUncertain = "uncertain"
)

// Receipt is the answer to a command, or the last one of a lead. The view
// draws nothing of it but words of its own chosen by Status, Op, Outcome and
// Code: any text a peer sent is not a field here, and a Code or Outcome that
// the view does not know is drawn as a fixed line that says so.
type Receipt struct {
	// CommandID identifies the command. It is compared and never drawn.
	CommandID string
	Op        Op
	Status    Status
	// Code is the reason code of a refused or unavailable receipt.
	Code string
	// Epoch is the epoch of a start, ticket or inject.
	Epoch uint64
	// Outcome is the outcome of a done inject.
	Outcome string
	// Folder and TicketFile are the result of a done ticket: where to attach.
	// They are handed to the parent in an Attach action and never drawn.
	Folder, TicketFile string
	// Earlier says that this final receipt answers a command that an earlier
	// press sent and kept, not the press that returned it: the command was
	// not run again and the receipt may be old. The view draws it as the
	// answer to an earlier command, never attaches with a done ticket of it,
	// and does not take a message's epoch as settled by it. It is false in
	// the receipts of the list.
	Earlier bool
}

// Lead is one lead of the listing.
type Lead struct {
	// Key is the short name shown in the table.
	Key string
	// Role is the lead's role, the identifier every command is sent with.
	Role string
	// Defined says that the registry has the role.
	Defined bool
	// Provision is "done", "incomplete" or "none".
	Provision string
	// State is the lead's state in the registry, such as "ready".
	State string
	// Epoch is the current epoch, 0 with none.
	Epoch uint64
	// Unit is the lead's unit name.
	Unit string
	// Inject says that messages are on for the lead.
	Inject bool
	// MessageSpent says that the epoch's one message was sent.
	MessageSpent bool
	// Last is the receipt of the lead's last command, nil with none.
	Last *Receipt
}

// Listing is the answer to List.
type Listing struct {
	MaxRunning, Running int
	Leads               []Lead
	// LeftOut counts the rows the backend did not return. The view adds the
	// rows it drops itself, and shows a fixed line when the sum is not zero.
	LeftOut int
}

// Colors are the colours the view draws with. The parent fills them from its
// theme, so that the view has no palette of its own. A nil colour is the
// terminal's default.
type Colors struct {
	// Fg and Bg are the colours of the text and of the region.
	Fg, Bg color.Color
	// Dim is the colour of labels and key hints.
	Dim color.Color
	// Accent is the colour of headings and of the cursor.
	Accent color.Color
	// Warn is the colour of refusals and of the line that says why a key
	// did nothing.
	Warn color.Color
}

// DefaultPoll is how often the list is read again while a receipt is in
// progress.
const DefaultPoll = 2 * time.Second

// Options configure a Model.
type Options struct {
	// Backend is where the leads come from. It must not be nil.
	Backend Backend
	Colors  Colors
	// Random is the source of the ids of messages. With none, or when it
	// fails, no message is sent and the view says why.
	Random io.Reader
	// After makes a command that sends a message once d has passed. Nil
	// means tea.Tick. A test gives its own clock.
	After func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd
	// Poll is how often the list is read while a receipt is in progress.
	// Zero means DefaultPoll.
	Poll time.Duration
}

// Kind says what the parent is asked to do after an Update.
type Kind int

const (
	// None asks for nothing.
	None Kind = iota
	// Close asks the parent to close the view: Esc was pressed in the list.
	Close
	// Quit asks the parent to quit: ctrl+c was pressed. A field that was
	// open has been dropped.
	Quit
	// Attach asks the parent to attach to a lead, as Action.Attach says.
	Attach
)

// AttachRequest is where and how to attach to a lead, from the receipt of a
// ticket that was done.
type AttachRequest struct {
	Role  string
	Epoch uint64
	// Writer is true for a writer attach and false for a viewer.
	Writer     bool
	Folder     string
	TicketFile string
}

// Action is what Update returns. Attach is set for the Attach kind.
type Action struct {
	Kind   Kind
	Attach AttachRequest
}
