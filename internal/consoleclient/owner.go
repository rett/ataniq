//go:build linux && ataniq_ownerchannel

package consoleclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
)

// NoOwnerChannelText is what the console says when the daemon it is
// connected to has no owner channel. It is the console's own sentence for
// the case that no backend was given.
const NoOwnerChannelText = "no owner channel in this build"

// DefaultOwnerTimeout bounds each call of an [Owner] whose configuration
// names no other bound.
const DefaultOwnerTimeout = 10 * time.Second

// maxOpenCards bounds what an [Owner] remembers of the cards it opened.
const maxOpenCards = 256

// The errors of an [Owner] that are not a refusal of the daemon. Each text is
// fixed in this package: nothing the daemon sent is in one.
var (
	// ErrNoOwnerChannel says the daemon's hello lists no operation of the
	// owner channel.
	ErrNoOwnerChannel = errors.New(NoOwnerChannelText)
	// ErrOwnerVersion says the daemon lists some of the owner channel's
	// operations and not all of them.
	ErrOwnerVersion = errors.New("the daemon's owner channel is not of this console's version")
	// ErrConnectionLost is the answer to a decision whose card was read over
	// a connection that has ended since. Nothing was sent.
	ErrConnectionLost = errors.New("the connection to the daemon was lost, so nothing was sent: open the card again")
	// ErrDecisionUnsettled is the answer to a decision on a card whose
	// earlier decision was sent and not answered. Nothing was sent.
	ErrDecisionUnsettled = errors.New("an earlier answer on this card did not come back, so nothing was sent: open the card to see where it stands")
	// ErrCardNotOpen is the answer to a page or a decision for a card this
	// client has not opened.
	ErrCardNotOpen = errors.New("that card was not opened through this connection")
	// ErrOwnerClosed is the answer to every call after [Owner.Close].
	ErrOwnerClosed = errors.New("the owner client is closed")
)

// Unavailable is the error for a daemon that could not be asked or whose
// answer was not usable. State is one of the states [Read] knows, so the text
// and the next step are the fixed ones of that state.
type Unavailable struct {
	State State
}

// Error returns the state's one-line text.
func (u *Unavailable) Error() string { return stateTexts[u.State][0] }

// NextStep returns the state's next step.
func (u *Unavailable) NextStep() string { return stateTexts[u.State][1] }

// OwnerConfig configures an [Owner].
type OwnerConfig struct {
	// Dir is the folder of the daemon's socket.
	Dir string
	// Client is the program's name and version, as the daemon is told it.
	// Empty means "ataniq-console".
	Client string
	// Random is the source of the run's session id. Nil means the system's.
	Random io.Reader
	// Timeout bounds each dial and each call. Zero means
	// [DefaultOwnerTimeout].
	Timeout time.Duration
}

// Owner is the console's approvals backend over the daemon's owner channel:
// it satisfies the method set the console's approvals view calls, and holds
// the one connection it needs, made when the first call needs it and made
// again by the first call after it ended.
//
// The rules it keeps:
//
//   - It decides nothing. A decision is the owner's, handed in as a value,
//     and it goes out exactly as handed in, in the form the daemon's wire
//     package writes; the daemon fills everything else, and the clock is the
//     daemon's. No time of the console's is sent.
//   - A decision is sent at most once. It is never sent on a connection other
//     than the one its card was read over: a connection that ended since drops
//     what was typed, and nothing is sent ([ErrConnectionLost]). A decision
//     whose answer did not come back is in doubt, and the card takes no
//     second one until it is opened again ([ErrDecisionUnsettled]). A
//     decision the daemon answered with a final state is answered again with
//     that outcome and not sent twice. A reconnect never replays anything.
//   - Every refusal of the daemon with a code of the approval packages
//     reaches the caller as a [*record.Error] with that code; every other
//     failure is an [*Unavailable] or one of the errors above. No error text
//     comes from the daemon, and every line it shows has been cleaned.
//   - One connection at a time: a call waits for the one before it, so a
//     second connection can never claim to be the decider next to the first.
//
// Owner is safe for use from several goroutines. Make one with [NewOwner].
type Owner struct {
	dir     string
	name    string
	session string
	timeout time.Duration

	closed atomic.Bool
	live   atomic.Pointer[clientapi.Client]

	// mu is held for the whole of every call.
	mu        sync.Mutex
	conn      *ownerConn
	gen       uint64
	cards     map[string]openCard
	order     []string
	doubt     map[string]bool
	settled   map[string]card.Outcome
	notListed int64
}

// ownerConn is one connection with what the daemon said when it was made.
type ownerConn struct {
	c   *clientapi.Client
	gen uint64
	// decider and reason are the daemon's answer to owner.decider when the
	// connection was made; fresh says that no caller has read them yet.
	decider bool
	reason  string
	fresh   bool
}

// openCard is what the client keeps of a card it opened: the review digest
// that its pages are asked for, and the connection it was read over.
type openCard struct {
	review string
	gen    uint64
}

// NewOwner returns an Owner. It reads the session id and checks the client
// name; it opens no connection.
func NewOwner(cfg OwnerConfig) (*Owner, error) {
	o := &Owner{
		dir:     cfg.Dir,
		name:    cfg.Client,
		timeout: cfg.Timeout,
		cards:   map[string]openCard{},
		doubt:   map[string]bool{},
		settled: map[string]card.Outcome{},
	}
	if o.name == "" {
		o.name = "ataniq-console"
	}
	if o.timeout <= 0 {
		o.timeout = DefaultOwnerTimeout
	}
	r := cfg.Random
	if r == nil {
		r = rand.Reader
	}
	var id [8]byte
	if _, err := io.ReadFull(r, id[:]); err != nil {
		return nil, errors.New("consoleclient: no session id could be made")
	}
	o.session = hex.EncodeToString(id[:])
	if _, err := ownerwire.EncodeDeciderParams(ownerwire.DeciderParams{Client: o.name, Session: o.session}); err != nil {
		return nil, errors.New("consoleclient: the client name is not one the daemon accepts")
	}
	return o, nil
}

// NotListed returns how many pending cards the daemon's last listing left
// out because it did not fit one page, or 0.
func (o *Owner) NotListed() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.notListed
}

// Close ends the connection. It does not wait for a call in flight: that call
// returns an error. After Close every call returns [ErrOwnerClosed].
func (o *Owner) Close() error {
	o.closed.Store(true)
	if c := o.live.Load(); c != nil {
		return c.Close()
	}
	return nil
}

// ctx returns the context of one dial or call.
func (o *Owner) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), o.timeout)
}

// drop closes the connection, so that the next call makes a new one. The
// caller holds o.mu.
func (o *Owner) drop() {
	if o.conn != nil {
		o.conn.c.Close()
		o.conn = nil
	}
	o.live.Store(nil)
}

// connect returns the connection, making one when there is none: it dials,
// reads the hello, checks that the daemon speaks this protocol and lists the
// owner channel's operations, and tells the daemon who this surface is
// (owner.decider). A daemon that fails any step leaves no connection behind.
// The caller holds o.mu.
func (o *Owner) connect() (*ownerConn, error) {
	if o.closed.Load() {
		return nil, ErrOwnerClosed
	}
	if o.conn != nil {
		return o.conn, nil
	}
	ctx, cancel := o.ctx()
	defer cancel()
	c, err := clientapi.Dial(ctx, clientapi.SocketPath(o.dir))
	if err != nil {
		return nil, &Unavailable{State: dialState(err)}
	}
	fail := func(err error) (*ownerConn, error) {
		c.Close()
		return nil, err
	}
	hello, err := c.Hello(ctx)
	if err != nil {
		return fail(&Unavailable{State: callState(err)})
	}
	if !speaksProtocol(hello.Protocols) {
		return fail(&Unavailable{State: StateOtherProtocol})
	}
	have := 0
	for _, op := range ownerwire.Operations() {
		for _, listed := range hello.Operations {
			if listed == op {
				have++
				break
			}
		}
	}
	switch {
	case have == 0:
		return fail(ErrNoOwnerChannel)
	case have < len(ownerwire.Operations()):
		return fail(ErrOwnerVersion)
	}
	params, err := ownerwire.EncodeDeciderParams(ownerwire.DeciderParams{Client: o.name, Session: o.session})
	if err != nil {
		return fail(&Unavailable{State: StateBadAnswer})
	}
	var raw json.RawMessage
	if err := c.Call(ctx, ownerwire.OpDecider, json.RawMessage(params), &raw); err != nil {
		return fail(mapError(err))
	}
	res, err := ownerwire.DecodeDeciderResult(raw)
	if err != nil {
		return fail(&Unavailable{State: StateBadAnswer})
	}
	if o.closed.Load() {
		return fail(ErrOwnerClosed)
	}
	o.gen++
	o.conn = &ownerConn{c: c, gen: o.gen, decider: res.Decider, reason: clean(res.Reason), fresh: true}
	o.live.Store(c)
	return o.conn, nil
}

// call sends one request on the connection and returns the raw result. A
// failure that leaves the stream in doubt, or a daemon that cannot be trusted
// any more, drops the connection. The caller holds o.mu and has a connection.
func (o *Owner) call(op string, params any) (json.RawMessage, error) {
	ctx, cancel := o.ctx()
	defer cancel()
	var raw json.RawMessage
	err := o.conn.c.Call(ctx, op, params, &raw)
	if err == nil {
		return raw, nil
	}
	var e *clientapi.Error
	if errors.As(err, &e) {
		switch e.Code {
		case clientapi.CodeUnauthorized, clientapi.CodeBusy, clientapi.CodeTooLarge, clientapi.CodeUnsupportedProtocol:
			o.drop()
		}
	} else {
		o.drop()
	}
	return nil, mapError(err)
}

// ask makes the connection when there is none and sends one request on it.
// The caller holds o.mu.
func (o *Owner) ask(op string, params any) (json.RawMessage, error) {
	if _, err := o.connect(); err != nil {
		return nil, err
	}
	return o.call(op, params)
}

// badAnswer drops the connection, whose peer sent an answer that the wire
// package refuses, and returns the error for it. The caller holds o.mu.
func (o *Owner) badAnswer() error {
	o.drop()
	return &Unavailable{State: StateBadAnswer}
}

// remember notes that the card of id was opened over the connection with the
// review digest. The caller holds o.mu.
func (o *Owner) remember(id, review string) {
	if _, known := o.cards[id]; !known {
		if len(o.order) >= maxOpenCards {
			old := o.order[0]
			o.order = o.order[1:]
			delete(o.cards, old)
			delete(o.doubt, old)
			delete(o.settled, old)
		}
		o.order = append(o.order, id)
	}
	o.cards[id] = openCard{review: review, gen: o.conn.gen}
}

// The codes of the approval packages that a daemon may refuse a request with.
// A refusal with another code is not one of them.
var refusalCodes = map[clientapi.ErrorCode]record.Code{}

func init() {
	for _, c := range []record.Code{
		record.CodeApprovalRefused, record.CodeRequestInvalid, record.CodeRequestRate,
		record.CodeRequestPendingCap, record.CodeRequestDuplicate, record.CodeRequestThrottled,
		record.CodePreviousEpoch, record.CodeNotAssigned, record.CodeVerbNotRequestable,
		record.CodeFeatureName, record.CodeFeatureMoved, record.CodeMergeConflict,
		record.CodeVerificationFailed, record.CodePruneUnmerged, record.CodePruneProtected,
		record.CodeDecisionInvalid, record.CodeBindingMismatch, record.CodeEvidenceChanged,
		record.CodeApprovalExpired, record.CodeApprovalVoid, record.CodeApprovalConsumed,
		record.CodeDecisionSignature, record.CodeGrantRevoked, record.CodeGrantNoMatch,
		record.CodeVerbNotGrantable, record.CodeRecordTooLarge, record.CodeRecordInvalid,
		record.CodeReviewChanged, record.CodeOwnerAway, record.CodeNotDecider, record.CodeLimitRefused,
	} {
		refusalCodes[clientapi.ErrorCode(c)] = c
	}
}

// mapError turns an error of the client protocol into the error this package
// returns. A refusal with a code of the approval packages becomes a
// *record.Error with that code; the limit's refusal carries its reason,
// cleaned, because the limit's rules write it from numbers and the owner needs
// it, and every other code carries no text. Everything else becomes an
// *Unavailable, with the state [Read] would have given it.
func mapError(err error) error {
	var e *clientapi.Error
	if errors.As(err, &e) {
		if code, ok := refusalCodes[e.Code]; ok {
			detail := ""
			if code == record.CodeLimitRefused {
				detail = clean(e.Message)
			}
			return &record.Error{Code: code, Detail: detail}
		}
	}
	return &Unavailable{State: callState(err)}
}
