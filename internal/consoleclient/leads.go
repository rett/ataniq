package consoleclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadwire"
)

// NoLeadControlText is what the console says when the daemon it is connected
// to has no lead control. It is the console's own sentence for the case that
// the daemon lists no lead operation.
const NoLeadControlText = "no lead control in this daemon"

// DefaultLeadsTimeout bounds each dial and each call of a [Leads] whose
// configuration names no other bound.
const DefaultLeadsTimeout = 10 * time.Second

// MaxPendingCommands is the most commands a [Leads] holds that were sent and
// have no final receipt. At the bound a new command is refused with
// [ErrTooManyPending]: the client never forgets an unconfirmed command, for
// then a repeat of it would be a second command.
const MaxPendingCommands = 64

// maxLeadRole is the longest role the client sends or accepts.
const maxLeadRole = 128

// unrecognisedWord stands in for a word of a receipt or a row that is not in
// the grammar of such words. The console's view draws a fixed line for a word
// it does not know.
const unrecognisedWord = "unrecognised"

// The errors of a [Leads] that are not a failure of the connection. Each text
// is fixed in this package: nothing the daemon sent is in one.
var (
	// ErrNoLeadControl says the daemon's hello lists no operation of the lead
	// control plane.
	ErrNoLeadControl = errors.New(NoLeadControlText)
	// ErrLeadsVersion says the peer offers lead operations but no directory.
	ErrLeadsVersion = errors.New("the daemon's lead control is not of this console's version")
	// ErrLeadCapability says the connection does not offer the requested action.
	ErrLeadCapability = errors.New("unavailable lead action")
	// ErrLeadsClosed is the answer to every call after [Leads.Close].
	ErrLeadsClosed = errors.New("the lead client is closed")
	// ErrLeadRequest says that the arguments are not ones the daemon takes.
	// Nothing was sent.
	ErrLeadRequest = errors.New("that request is not one the daemon takes, so nothing was sent")
	// ErrAnswerLost says that a command was sent and its outcome could not
	// be recovered. The command may have been admitted. When offered again,
	// calling with the same arguments repeats the
	// command's id, so nothing is done twice; reading the leads shows where
	// it stands.
	ErrAnswerLost = errors.New("no answer came back, so the command may or may not have been taken: read the leads to see where it stands")
	// ErrCommandConflict says that the daemon no longer holds the command's
	// id, or holds it for another command. The command was not run again. The
	// client forgets the id; the leads show where things stand.
	ErrCommandConflict = errors.New("the daemon no longer holds that command, so it was not run again: read the leads before sending it again")
	// ErrCommandUnavailable says that the daemon could not admit the command
	// now. Nothing was admitted; calling again repeats the id.
	ErrCommandUnavailable = errors.New("the daemon could not take the command now, so nothing was done: try again")
	// ErrLeadNotListed says that the daemon does not have the lead.
	ErrLeadNotListed = errors.New("the daemon does not have that lead, so nothing was done")
	// ErrLeadRejected says that the daemon did not accept the request as
	// valid, which a console and daemon of the same version do not cause.
	ErrLeadRejected = errors.New("the daemon did not accept the request, so nothing was done: check that the console and daemon are the same version")
	// ErrTooManyPending says that [MaxPendingCommands] commands were sent and
	// have no final receipt, so no new one is sent.
	ErrTooManyPending = errors.New("too many commands have no answer yet, so nothing was sent: read the leads")
	// ErrNoCommandID says that no command id could be made.
	ErrNoCommandID = errors.New("no command id could be made, so nothing was sent")
)

// LeadOp is the operation a [LeadReceipt] is for.
type LeadOp string

// The operations of a receipt.
const (
	LeadOpDefine LeadOp = "define"
	LeadOpStart  LeadOp = "start"
	LeadOpStop   LeadOp = "stop"
	LeadOpTicket LeadOp = "ticket"
	LeadOpInject LeadOp = "inject"
)

// LeadStatus is where a command stands.
type LeadStatus string

// The statuses of a receipt. All but [LeadInProgress] are final.
const (
	LeadInProgress  LeadStatus = "in-progress"
	LeadDone        LeadStatus = "done"
	LeadRefused     LeadStatus = "refused"
	LeadUnavailable LeadStatus = "unavailable"
	LeadUncertain   LeadStatus = "uncertain"
)

// LeadReceipt is the answer to a command, or the last one of a lead. It holds
// no text the daemon wrote: the receipt's message and the result's detail are
// left out, and Code, Outcome, Reason and Written are words of a closed
// grammar (a word outside it is "unrecognised"). Folder and TicketFile are paths for the console to attach
// with, checked to be absolute and clean, and are not for showing.
type LeadReceipt struct {
	// CommandID identifies the command. For a command this client sent it is
	// the id the client made.
	CommandID string
	Op        LeadOp
	Role      string
	Status    LeadStatus
	// Code is the reason code of a refused or unavailable receipt.
	Code string
	// Epoch is the epoch of a done start, ticket or inject. It is 0 in the
	// summary of a lead's last command, which has no result.
	Epoch uint64
	// Outcome, Reason and Written are the holder's three words about a done
	// inject: what happened to the message, why, and how much of it reached
	// the lead's input. Each is a word of the grammar of
	// [LeadReceipt]; Reason is empty for a submitted message, and all three
	// are empty in the summary of a lead's last command, which has no result.
	Outcome, Reason, Written string
	// Folder and TicketFile are the result of a done ticket.
	Folder, TicketFile string
	// Earlier says that this final receipt answers a command that an earlier
	// call of this client sent and kept (its answer was lost, or it had none
	// that was final), not the call that returned it. The command is not run
	// again, and the receipt may be old: a view must not show it as the
	// outcome of the press that returned it. The id is forgotten with the
	// receipt, so the next press is a new command. It is false in every other
	// receipt, and in the summary of a lead's last command.
	Earlier bool
}

// LeadDelivery is the holder's three words about the epoch's message, as a
// row of the list carries them: the words of [LeadReceipt]'s Outcome, Reason
// and Written, each a word of the closed grammar. All three are empty when the
// daemon has no answer.
type LeadDelivery struct {
	Outcome, Reason, Written string
}

// LeadRow is one lead of a [LeadListing]. Role is exactly as the daemon sent
// it, because every command is sent with it. An unreadable row has no capabilities
// and an empty role if the address could not be read. Its key and state
// reason are safe to show.
type LeadRow struct {
	Capabilities LeadCapabilities
	Key          string
	Role         string
	Defined      bool
	Provision    string
	State        string
	// StateReason is the reason code the registry shows with State, a word of
	// the closed grammar, empty with none. A row of a daemon built before the
	// member has none. On unreadable rows the client sets a "client/" reason.
	StateReason string
	Epoch       uint64
	Unit        string
	Inject      bool
	// MessagesEnabled is the lead's configured message switch. Inject also
	// requires the current connection to offer lead.inject.
	MessagesEnabled bool
	// MessageSpent says that the epoch's message was sent and is not free
	// again. A message that was not delivered (the holder answered not-now or
	// refused with nothing written, or refused the request itself) leaves it
	// false.
	MessageSpent bool
	// MessageResult is the holder's answer to the current epoch's last message
	// when the daemon has it, whether the message is spent or not, and empty
	// otherwise (a row of a daemon built before the member has none).
	MessageResult LeadDelivery
	// Last is the lead's last command in summary form, nil with none.
	Last *LeadReceipt
}

// LeadListing is the answer to [Leads.List].
type LeadListing struct {
	MaxRunning, Running int
	Leads               []LeadRow
	// LeftOut counts rows beyond [leadwire.MaxLeads].
	LeftOut int
}

// LeadsConfig configures a [Leads].
type LeadsConfig struct {
	// Dir is the folder of the daemon's socket.
	Dir string
	// Client is the program's name. The client string the daemon is given is
	// this name and the run's random session id. Empty means
	// "ataniq-console".
	Client string
	// Random is the source of the session id and of the command ids. Nil
	// means the system's.
	Random io.Reader
	// Timeout bounds each dial and each call. Zero means
	// [DefaultLeadsTimeout].
	Timeout time.Duration
}

// maxLeadsName is the most bytes of [LeadsConfig.Client]: the client string
// is at most [leadwire.MaxClient] bytes with the "-" and 16 hexadecimal
// characters of the session id.
const maxLeadsName = leadwire.MaxClient - 17

// leadKey is what makes two calls "the same": the operation and every
// argument. The client and the command id are not in it.
type leadKey struct {
	op        string
	role      string
	epoch     uint64
	attach    string
	messageID string
	body      string
}

// LeadCapabilities describes the operations advertised by a single hello.
// A listing row also takes account of that lead's message switch.
type LeadCapabilities struct {
	Directory, Conversation, Viewer, Writer bool
	Define, Start, Stop, Inject             bool
}

// leadHelloResult extends the v1 hello with optional ticket_roles. An explicit
// list narrows lead.ticket to those roles. Without the field, the full legacy
// scratch interface supports both roles. For a partial offer a ticket alone
// suffices to view, while a writer needs an explicit offer.
type leadHelloResult struct {
	clientapi.HelloResult
	TicketRoles json.RawMessage `json:"ticket_roles"`
}

// The hello determines which calls can go out. Reject ambiguous offer fields
// before encoding/json can fold their case or overwrite an earlier value.
func (h *leadHelloResult) UnmarshalJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return clientapi.ErrBadResponse
	}
	seen := make(map[string]bool)
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return clientapi.ErrBadResponse
		}
		for _, field := range []string{"protocols", "operations", "ticket_roles"} {
			if strings.EqualFold(key, field) {
				if key != field || seen[field] {
					return clientapi.ErrBadResponse
				}
				seen[field] = true
			}
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
	}
	type plainHello leadHelloResult
	return json.Unmarshal(raw, (*plainHello)(h))
}

func (h leadHelloResult) capabilities() (LeadCapabilities, error) {
	has := func(op string) bool { return slices.Contains(h.Operations, op) }
	c := LeadCapabilities{
		Directory: has(leadwire.OpList), Conversation: has("conversation.read"),
		Define: has(leadwire.OpDefine), Start: has(leadwire.OpStart),
		Stop: has(leadwire.OpStop), Inject: has(leadwire.OpInject),
	}
	if has(leadwire.OpTicket) {
		if h.TicketRoles == nil {
			c.Viewer = true
			c.Writer = c.Directory && c.Define && c.Start && c.Stop && c.Inject
		} else {
			var roles []string
			if err := json.Unmarshal(h.TicketRoles, &roles); err != nil || roles == nil {
				return LeadCapabilities{}, &Unavailable{State: StateBadAnswer}
			}
			c.Viewer = slices.Contains(roles, leadwire.AttachViewer)
			c.Writer = slices.Contains(roles, leadwire.AttachWriter)
		}
	}
	return c, nil
}

// CheckCommand reports whether a control for this lead can send the command.
// It returns a fixed refusal reason suitable for showing beside the action.
// writer selects the attach role for LeadOpTicket.
func (c LeadCapabilities) CheckCommand(op LeadOp, writer bool) error {
	var offered bool
	var action string
	switch op {
	case LeadOpDefine:
		offered, action = c.Define, "defining this lead"
	case LeadOpStart:
		offered, action = c.Start, "starting this lead"
	case LeadOpStop:
		offered, action = c.Stop, "stopping this lead"
	case LeadOpInject:
		offered, action = c.Inject, "sending a message to this lead"
	case LeadOpTicket:
		offered, action = c.Viewer, "a viewer ticket for this lead"
		if writer {
			offered, action = c.Writer, "a writer ticket for this lead"
		}
	default:
		return ErrLeadRequest
	}
	if !offered {
		return fmt.Errorf("%w: the daemon does not offer %s", ErrLeadCapability, action)
	}
	return nil
}

// leadRPC is the part of a clientapi connection used by the lead client.
type leadRPC interface {
	Call(context.Context, string, any, any) error
	Close() error
}

type leadConnection struct {
	leadRPC
	caps LeadCapabilities
}

// Leads is the console's client of the daemon's lead control plane: the six
// operations of package leadwire over the client protocol. It satisfies the
// method set of the console's leads view through an adapter (the view's
// [Backend] has types of its own), and holds the one connection it needs,
// made when the first call needs it and made again by the first call after
// it ended.
//
// The rules it keeps:
//
//   - Each connection's hello determines which actions it can serve.
//     Directory access needs only lead.list. A command needs its advertised
//     operation and a ticket also needs the requested attach role.
//   - Each command is sent with an id of 128 random bits and a client string
//     that names this run. The client keeps the id and the arguments of every
//     command it sent that has no final receipt. A call with the same
//     operation and arguments repeats the kept id, whatever came back
//     before (nothing, or in-progress), so the daemon runs the command at
//     most once. A final receipt (done, refused, unavailable or uncertain)
//     forgets the id; so does a list that shows the id as the last command
//     of a lead with a final status. A final receipt of an id that an
//     earlier call kept has [LeadReceipt.Earlier] set: it answers that
//     call's command, which may be long past. An answer that is lost is asked for by
//     repeating the same id and arguments once if the new connection offers
//     the action. If recovery fails the call returns [ErrAnswerLost]
//     and the id is kept. A new id is never sent for a command that has no
//     final receipt. The one case in which the client lets an id go without
//     a final receipt is an answer that says the id is not held ("conflict"),
//     because the daemon will not run it again whatever is sent.
//   - Every call returns a receipt, or an error only when no receipt came
//     back. Nothing the daemon wrote reaches a result or an error as text:
//     receipt messages are dropped, words are checked against a grammar,
//     and every string for showing is cleaned and bounded.
//   - Arguments are checked before anything is sent, and a call that fails
//     the check sends nothing and keeps nothing.
//
// Leads is safe for use from several goroutines. Two calls with the same
// arguments at once use the same command id. Make one with [NewLeads].
type Leads struct {
	dir     string
	client  string
	timeout time.Duration
	random  io.Reader

	closed atomic.Bool
	live   atomic.Pointer[leadConnection]

	// connMu is held while a connection is made, and while it is read or
	// dropped.
	connMu sync.Mutex
	conn   *leadConnection
	dial   func(context.Context, string) (leadRPC, error)

	// mu guards the commands and the source of ids.
	mu      sync.Mutex
	pending map[leadKey]string
}

// NewLeads returns a Leads. It reads the session id and checks the client
// name; it opens no connection.
func NewLeads(cfg LeadsConfig) (*Leads, error) {
	name := cfg.Client
	if name == "" {
		name = "ataniq-console"
	}
	if len(name) > maxLeadsName {
		return nil, errors.New("consoleclient: the client name is too long")
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 0x21 || name[i] > 0x7e {
			return nil, errors.New("consoleclient: the client name is not one the daemon accepts")
		}
	}
	l := &Leads{
		dir:     cfg.Dir,
		dial:    func(ctx context.Context, path string) (leadRPC, error) { return clientapi.Dial(ctx, path) },
		timeout: cfg.Timeout,
		random:  cfg.Random,
		pending: map[leadKey]string{},
	}
	if l.timeout <= 0 {
		l.timeout = DefaultLeadsTimeout
	}
	if l.random == nil {
		l.random = rand.Reader
	}
	var id [8]byte
	if _, err := io.ReadFull(l.random, id[:]); err != nil {
		return nil, errors.New("consoleclient: no session id could be made")
	}
	l.client = name + "-" + hex.EncodeToString(id[:])
	return l, nil
}

// Close ends the connection. It does not wait for a call in flight: that call
// returns an error. After Close every call returns [ErrLeadsClosed].
func (l *Leads) Close() error {
	l.closed.Store(true)
	if c := l.live.Load(); c != nil {
		return c.Close()
	}
	return nil
}

// Detect reports whether the lead directory can be opened.
func (l *Leads) Detect() error {
	caps, err := l.Capabilities()
	if err == nil && !caps.Directory {
		return ErrLeadsVersion
	}
	return err
}

// Capabilities reads the current connection's offer. It sends only hello.
func (l *Leads) Capabilities() (LeadCapabilities, error) {
	c, err := l.connect()
	if err != nil {
		return LeadCapabilities{}, err
	}
	return c.caps, nil
}

// connect returns the connection, making one when there is none: it dials,
// reads the hello and checks that the daemon speaks this protocol and lists
// the lead control's operations. A daemon that fails any step leaves no
// connection behind.
func (l *Leads) connect() (*leadConnection, error) {
	if l.closed.Load() {
		return nil, ErrLeadsClosed
	}
	l.connMu.Lock()
	defer l.connMu.Unlock()
	if l.conn != nil {
		return l.conn, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	peer, err := l.dial(ctx, clientapi.SocketPath(l.dir))
	if err != nil {
		return nil, &Unavailable{State: dialState(err)}
	}
	fail := func(err error) (*leadConnection, error) {
		peer.Close()
		return nil, err
	}
	var hello leadHelloResult
	if err := peer.Call(ctx, clientapi.OpHello, nil, &hello); err != nil {
		return fail(&Unavailable{State: callState(err)})
	}
	if !speaksProtocol(hello.Protocols) {
		return fail(&Unavailable{State: StateOtherProtocol})
	}
	caps, err := hello.capabilities()
	if err != nil {
		return fail(err)
	}
	// A ticket operation can explicitly offer neither known attach role.
	// Keep that offer so Ticket can explain which role is unavailable.
	if caps == (LeadCapabilities{}) && !slices.Contains(hello.Operations, leadwire.OpTicket) {
		return fail(ErrNoLeadControl)
	}
	if l.closed.Load() {
		return fail(ErrLeadsClosed)
	}
	c := &leadConnection{leadRPC: peer, caps: caps}
	l.conn = c
	l.live.Store(c)
	return c, nil
}

// drop closes c, which a call has found unusable, and forgets it when it is
// still the connection, so that the next call makes a new one.
func (l *Leads) drop(c *leadConnection) {
	l.connMu.Lock()
	defer l.connMu.Unlock()
	c.Close()
	if l.conn == c {
		l.conn = nil
		l.live.Store(nil)
	}
}

// call sends one request on c and reads the raw result.
func (l *Leads) call(c *leadConnection, op string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
	defer cancel()
	var raw json.RawMessage
	if err := c.Call(ctx, op, params, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// endsConnection reports whether the code is one after which the server
// closes the connection, and that is answered before any operation is served.
func endsConnection(code clientapi.ErrorCode) bool {
	switch code {
	case clientapi.CodeUnauthorized, clientapi.CodeBusy, clientapi.CodeTooLarge, clientapi.CodeUnsupportedProtocol:
		return true
	}
	return false
}

// List reads the leads. It changes nothing, and so it may be repeated on a
// new connection when the answer was lost: it is, once. It forgets the id of
// every kept command that a row shows as the lead's last, with a final
// status.
func (l *Leads) List() (LeadListing, error) {
	for attempt := 0; ; attempt++ {
		c, err := l.connect()
		if err != nil {
			return LeadListing{}, err
		}
		if !c.caps.Directory {
			return LeadListing{}, ErrLeadsVersion
		}
		raw, err := l.call(c, leadwire.OpList, nil)
		if err != nil {
			var e *clientapi.Error
			if errors.As(err, &e) {
				if endsConnection(e.Code) {
					l.drop(c)
				}
				return LeadListing{}, &Unavailable{State: callState(err)}
			}
			l.drop(c)
			if l.closed.Load() {
				return LeadListing{}, ErrLeadsClosed
			}
			if attempt == 0 {
				continue
			}
			return LeadListing{}, &Unavailable{State: callState(err)}
		}
		out, ok := buildListing(raw, c.caps)
		if !ok {
			l.drop(c)
			return LeadListing{}, &Unavailable{State: StateBadAnswer}
		}
		l.reconcile(out)
		return out, nil
	}
}

// Define asks the daemon to provision and define the lead role.
func (l *Leads) Define(role string) (LeadReceipt, error) {
	if !validLeadRole(role) {
		return LeadReceipt{}, ErrLeadRequest
	}
	k := leadKey{op: leadwire.OpDefine, role: role}
	return l.command(k, func(id string) any {
		return leadwire.DefineParams{Client: l.client, CommandID: id, Role: role}
	})
}

// Start asks the daemon to start the lead role.
func (l *Leads) Start(role string) (LeadReceipt, error) {
	if !validLeadRole(role) {
		return LeadReceipt{}, ErrLeadRequest
	}
	k := leadKey{op: leadwire.OpStart, role: role}
	return l.command(k, func(id string) any {
		return leadwire.StartParams{Client: l.client, CommandID: id, Role: role}
	})
}

// Stop asks the daemon to stop the lead role in epoch. An epoch of 0 means
// the current one.
func (l *Leads) Stop(role string, epoch uint64) (LeadReceipt, error) {
	if !validLeadRole(role) {
		return LeadReceipt{}, ErrLeadRequest
	}
	k := leadKey{op: leadwire.OpStop, role: role, epoch: epoch}
	return l.command(k, func(id string) any {
		return leadwire.StopParams{Client: l.client, CommandID: id, Role: role, Epoch: epoch}
	})
}

// Ticket asks the daemon for an attach ticket for the role in epoch, as a
// writer or, with writer false, as a viewer. The ticket is in the ticket
// file of a done receipt, not in the answer.
func (l *Leads) Ticket(role string, epoch uint64, writer bool) (LeadReceipt, error) {
	if !validLeadRole(role) {
		return LeadReceipt{}, ErrLeadRequest
	}
	attach := leadwire.AttachViewer
	if writer {
		attach = leadwire.AttachWriter
	}
	k := leadKey{op: leadwire.OpTicket, role: role, epoch: epoch, attach: attach}
	return l.command(k, func(id string) any {
		return leadwire.TicketParams{Client: l.client, CommandID: id, Role: role, Epoch: epoch, AttachRole: attach}
	})
}

// Send sends one message to the lead in epoch. messageID is made by the
// caller and has the lowercase grammar of the lead holder's message ids; the
// body is valid UTF-8 of at most [leadwire.MaxBody] bytes.
func (l *Leads) Send(role string, epoch uint64, messageID, body string) (LeadReceipt, error) {
	if !validLeadRole(role) || !validMessageID(messageID) || len(body) > leadwire.MaxBody || !utf8.ValidString(body) {
		return LeadReceipt{}, ErrLeadRequest
	}
	k := leadKey{op: leadwire.OpInject, role: role, epoch: epoch, messageID: messageID, body: body}
	return l.command(k, func(id string) any {
		return leadwire.InjectParams{Client: l.client, CommandID: id, Role: role, Epoch: epoch, MessageID: messageID, Body: body}
	})
}

// claim returns the id of the command k: the kept one when the same command
// was sent and has no final receipt, and a new one, kept now, otherwise. kept
// says that the id was kept from an earlier call.
func (l *Leads) claim(k leadKey) (id string, kept bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if id, ok := l.pending[k]; ok {
		return id, true, nil
	}
	if len(l.pending) >= MaxPendingCommands {
		return "", false, ErrTooManyPending
	}
	var b [16]byte
	if _, err := io.ReadFull(l.random, b[:]); err != nil {
		return "", false, ErrNoCommandID
	}
	id = hex.EncodeToString(b[:])
	l.pending[k] = id
	return id, false, nil
}

// release forgets the id of the command k, when it is still the one kept.
func (l *Leads) release(k leadKey, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending[k] == id {
		delete(l.pending, k)
	}
}

// reconcile forgets the id of every kept command that the listing shows as
// the last command of a lead, with a final status.
func (l *Leads) reconcile(lst LeadListing) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.pending) == 0 {
		return
	}
	for _, row := range lst.Leads {
		r := row.Last
		if r == nil || r.Status == LeadInProgress {
			continue
		}
		for k, id := range l.pending {
			if id == r.CommandID && k.role == r.Role && k.op == "lead."+string(r.Op) {
				delete(l.pending, k)
			}
		}
	}
}

// command sends the command k and returns its receipt. mk builds the params
// for an id.
func (l *Leads) command(k leadKey, mk func(id string) any) (LeadReceipt, error) {
	c, err := l.connect()
	op := LeadOp(strings.TrimPrefix(k.op, "lead."))
	writer := k.attach == leadwire.AttachWriter
	if err == nil {
		err = c.caps.CheckCommand(op, writer)
	}
	if err != nil {
		// A failure to send this time cannot settle an earlier attempt.
		l.mu.Lock()
		_, pending := l.pending[k]
		l.mu.Unlock()
		if pending && !errors.Is(err, ErrLeadsClosed) {
			return LeadReceipt{}, ErrAnswerLost
		}
		return LeadReceipt{}, err
	}
	id, kept, err := l.claim(k)
	if err != nil {
		return LeadReceipt{}, err
	}
	var raw json.RawMessage
	for attempt := 0; ; attempt++ {
		raw, err = l.call(c, k.op, mk(id))
		if err == nil {
			break
		}
		var e *clientapi.Error
		switch {
		case errors.As(err, &e):
			return LeadReceipt{}, l.refusal(c, k, id, e)
		case errors.Is(err, clientapi.ErrBadParams), errors.Is(err, clientapi.ErrRequestTooLarge):
			// Nothing went out.
			l.release(k, id)
			return LeadReceipt{}, ErrLeadRequest
		}
		// The request may have gone out and the answer is lost. Ask for it
		// by sending the same command again, once, on a new connection.
		l.drop(c)
		if l.closed.Load() {
			return LeadReceipt{}, ErrLeadsClosed
		}
		if attempt == 1 {
			return LeadReceipt{}, ErrAnswerLost
		}
		if c, err = l.connect(); err != nil {
			if errors.Is(err, ErrLeadsClosed) {
				return LeadReceipt{}, err
			}
			return LeadReceipt{}, ErrAnswerLost
		}
		if c.caps.CheckCommand(op, writer) != nil {
			// The first request may have been admitted. Keep its id.
			return LeadReceipt{}, ErrAnswerLost
		}
	}
	return l.settle(c, k, id, kept, raw)
}

// refusal maps an error response to a command. Only before admission does
// the daemon answer with an error, so what it says is what was or was not
// admitted; "internal" is the exception, because a handler that failed may
// have admitted the command, and it keeps the id.
func (l *Leads) refusal(c *leadConnection, k leadKey, id string, e *clientapi.Error) error {
	if endsConnection(e.Code) {
		// Answered before the operation was served.
		l.drop(c)
		l.release(k, id)
		return &Unavailable{State: callState(e)}
	}
	switch string(e.Code) {
	case leadwire.ErrCodeInvalidRequest:
		l.release(k, id)
		return ErrLeadRejected
	case leadwire.ErrCodeRefused:
		l.release(k, id)
		return ErrLeadNotListed
	case leadwire.ErrCodeConflict:
		l.release(k, id)
		return ErrCommandConflict
	case leadwire.ErrCodeUnavailable:
		return ErrCommandUnavailable
	}
	return &Unavailable{State: callState(e)}
}

// settle checks the receipt of a command that was answered, forgets the
// command's id when the receipt is final, and builds the caller's value. kept
// says that the id was kept from an earlier call, so a final receipt is the
// answer to that call's command.
func (l *Leads) settle(c *leadConnection, k leadKey, id string, kept bool, raw json.RawMessage) (LeadReceipt, error) {
	bad := func() (LeadReceipt, error) {
		l.drop(c)
		return LeadReceipt{}, &Unavailable{State: StateBadAnswer}
	}
	var r leadwire.Receipt
	if decodeLeadJSON(raw, &r) != nil {
		return bad()
	}
	if r.CommandID != id || r.Op != k.op || r.Role != k.role || !knownStatus(r.Status) {
		return bad()
	}
	out := LeadReceipt{
		CommandID: id,
		Op:        LeadOp(strings.TrimPrefix(k.op, "lead.")),
		Role:      k.role,
		Status:    LeadStatus(r.Status),
		Code:      leadWord(r.Code),
	}
	if r.Status.Final() {
		l.release(k, id)
		out.Earlier = kept
	}
	if r.Status == leadwire.StatusDone && !fillResult(&out, k, r.Result) {
		return bad()
	}
	return out, nil
}

// fillResult reads the result of a done command into out, and says whether
// it is a result of the command's operation, for the lead it was asked for.
func fillResult(out *LeadReceipt, k leadKey, raw json.RawMessage) bool {
	switch k.op {
	case leadwire.OpDefine:
		var r leadwire.DefineResult
		return decodeLeadJSON(raw, &r) == nil && r.Role == k.role
	case leadwire.OpStart:
		var r leadwire.StartResult
		if decodeLeadJSON(raw, &r) != nil || r.Role != k.role {
			return false
		}
		out.Epoch = r.Epoch
	case leadwire.OpStop:
		var r leadwire.StopResult
		return decodeLeadJSON(raw, &r) == nil && r.Role == k.role
	case leadwire.OpTicket:
		var r leadwire.TicketResult
		if decodeLeadJSON(raw, &r) != nil || r.Role != k.role || (k.epoch != 0 && r.Epoch != k.epoch) {
			return false
		}
		if !validLeadPath(r.Folder) || !validLeadPath(r.TicketFile) {
			return false
		}
		out.Epoch, out.Folder, out.TicketFile = r.Epoch, r.Folder, r.TicketFile
	case leadwire.OpInject:
		var r leadwire.InjectResult
		if decodeLeadJSON(raw, &r) != nil || r.Role != k.role || r.MessageID != k.messageID || (k.epoch != 0 && r.Epoch != k.epoch) {
			return false
		}
		out.Epoch, out.Outcome = r.Epoch, leadWord(r.Result.Outcome)
		out.Reason, out.Written = leadWord(r.Result.Reason), leadWord(r.Result.Written)
	}
	return true
}

// listingFields decodes only the named members of one object. Inspect the
// keys before decoding their values: a repeated or case-folded role, epoch
// or message switch must never change which commands a row can send.
// Keep nested values raw until their own boundary (listing, row, receipt).
func listingFields(raw json.RawMessage, fields map[string]any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return false
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return false
		}
		dst, known := fields[key]
		if !known {
			for name := range fields {
				if strings.EqualFold(key, name) {
					return false
				}
			}
			continue
		}
		if dst == nil || (string(value) == "null" && key != "last" && key != "leads") || json.Unmarshal(value, dst) != nil {
			return false
		}
		fields[key] = nil // refuse another occurrence
	}
	_, err := dec.Token()
	return err == nil && dec.Decode(new(json.RawMessage)) == io.EOF
}

func buildListing(raw json.RawMessage, caps LeadCapabilities) (LeadListing, bool) {
	var out LeadListing
	var rows []json.RawMessage
	fields := map[string]any{"max_running": &out.MaxRunning, "running": &out.Running, "leads": &rows}
	if !listingFields(raw, fields) || fields["leads"] != nil {
		return LeadListing{}, false
	}
	out.Running, out.MaxRunning = nonNegative(out.Running), nonNegative(out.MaxRunning)
	for i, rawRow := range rows {
		if i == leadwire.MaxLeads {
			out.LeftOut = len(rows) - i
			break
		}
		out.Leads = append(out.Leads, listingRow(rawRow, caps, i+1))
	}
	return out, true
}

func listingRow(raw json.RawMessage, caps LeadCapabilities, number int) LeadRow {
	var w leadwire.Lead
	var last json.RawMessage
	fields := map[string]any{
		"key": &w.Key, "role": &w.Role, "defined": &w.Defined, "provision": &w.Provision,
		"state": &w.State, "state_reason": &w.StateReason, "epoch": &w.Epoch, "unit": &w.Unit,
		"inject": &w.Inject, "message_spent": &w.MessageSpent, "message_result": &w.MessageResult,
		"last": &last,
	}
	if !listingFields(raw, fields) || !validLeadRole(w.Role) || (w.Inject && fields["message_spent"] != nil) {
		// Recover only an unambiguous label and role, even when the bad
		// member appeared first. The degraded row carries no capabilities.
		var key, role string
		if !listingFields(raw, map[string]any{"key": &key, "role": &role}) {
			role = ""
		}
		return degradedLead(key, role, number, "invalid-row")
	}
	caps.Inject = caps.Inject && w.Inject
	row := LeadRow{
		Capabilities:    caps,
		Key:             clean(w.Key),
		Role:            w.Role,
		Defined:         w.Defined,
		Provision:       leadWord(w.Provision),
		State:           clean(w.State),
		StateReason:     leadWord(w.StateReason),
		Epoch:           w.Epoch,
		Unit:            clean(w.Unit),
		Inject:          caps.Inject,
		MessagesEnabled: w.Inject,
		MessageSpent:    w.MessageSpent,
		MessageResult: LeadDelivery{
			Outcome: leadWord(w.MessageResult.Outcome),
			Reason:  leadWord(w.MessageResult.Reason),
			Written: leadWord(w.MessageResult.Written),
		},
	}
	if len(last) > 0 && string(last) != "null" {
		// This summary can settle a pending command. Require an exact,
		// unambiguous receipt even though its enclosing row is extensible.
		var r leadwire.Receipt
		if jsonv2.Unmarshal(last, &r, jsonv2.RejectUnknownMembers(true)) != nil ||
			!validCommandID(r.CommandID) || !leadwire.Mutating(r.Op) || r.Role != w.Role || !knownStatus(r.Status) {
			return degradedLead(w.Key, w.Role, number, "invalid-last-receipt")
		}
		row.Last = &LeadReceipt{
			CommandID: r.CommandID, Op: LeadOp(strings.TrimPrefix(r.Op, "lead.")),
			Role: r.Role, Status: LeadStatus(r.Status), Code: leadWord(r.Code),
		}
	}
	return row
}

func degradedLead(key, role string, number int, reason string) LeadRow {
	if !validLeadRole(role) {
		role = ""
	}
	key = clean(key)
	if key == "" {
		key = role
	}
	if key == "" {
		key = fmt.Sprintf("row-%d", number)
	}
	// The slash is outside leadWord's grammar, so a decoded daemon reason
	// cannot be mistaken for a client-side failure by the leads view.
	return LeadRow{Key: key, Role: role, State: "degraded", StateReason: "client/" + reason}
}

func nonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// knownStatus reports whether s is one of the five statuses.
func knownStatus(s leadwire.Status) bool {
	return s == leadwire.StatusInProgress || s.Final()
}

// decodeLeadJSON decodes raw into v, refusing a member v does not have and
// anything after the value.
func decodeLeadJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the value")
	}
	return nil
}

// leadWord returns s when it is empty or a word of the grammar of reason
// codes and outcomes (lower-case letters, digits and dashes, the first a
// letter, at most 64 bytes), and "unrecognised" otherwise.
func leadWord(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > 64 {
		return unrecognisedWord
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return unrecognisedWord
		}
	}
	return s
}

// validLeadRole reports whether s can be sent as a role: 1 to 128 printable
// ASCII bytes with no space. Whether the daemon has the lead is for the
// daemon to say.
func validLeadRole(s string) bool {
	if len(s) < 1 || len(s) > maxLeadRole {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// validMessageID reports whether s has the lowercase grammar of the lead
// holder's message ids: [a-z0-9][a-z0-9._-]{0,63}.
func validMessageID(s string) bool {
	if len(s) < 1 || len(s) > leadwire.MaxMessageID {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// validCommandID reports whether s is a command id: 1 to
// [leadwire.MaxCommandID] bytes of letters, digits and . _ : -.
func validCommandID(s string) bool {
	if len(s) < 1 || len(s) > leadwire.MaxCommandID {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == ':' || c == '-':
		default:
			return false
		}
	}
	return true
}

// validLeadPath reports whether s is an absolute, clean path of valid UTF-8
// with no control character, of at most 4096 bytes. It is kept as sent, for
// the console to open, and is never shown.
func validLeadPath(s string) bool {
	if len(s) < 1 || len(s) > 4096 || !utf8.ValidString(s) || !strings.HasPrefix(s, "/") || path.Clean(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}
