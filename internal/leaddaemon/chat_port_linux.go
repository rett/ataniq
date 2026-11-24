//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"ataniq/internal/approval/ownerwire"
)

// This file is the seat's side of the scratch leads' replies: [LeadChat], the
// chat port the seat gate's seat.chat route calls. It fits that route's port
// (gatehost.ChatPort) by its method set alone and imports nothing of the
// approval packages but the wire's types (ownerwire): it holds no way to the
// owner channel's service, the decide path, the pairing, the key list, the
// limits, the intake or the host's effects, and nothing of the lead control
// plane but a read-only view (chatSource) that the scratch mode binds to it.
// A test holds both: the imports of this file, and that a chat call reaches
// no lifecycle, holder, command table or owner service.

// The bounds of [LeadChat].
const (
	// ChatCallsPerMinute is how many seat.chat calls the daemon serves in a
	// minute, across every connection of the gate: a call past it is answered
	// chat-busy, which the seat meets in good order and asks again later.
	ChatCallsPerMinute = 40
)

// chatRefusal is a refusal of the port, with the gate's word.
type chatRefusal string

func (r chatRefusal) Error() string { return "lead chat: " + string(r) }

// GateCode is the gate's word for the refusal: one of the words of seat.chat
// (ownerwire.CodeChatOff, CodeChatUnknownLead, CodeChatBusy) or bad-params.
func (r chatRefusal) GateCode() string { return string(r) }

const (
	refuseOff     chatRefusal = ownerwire.CodeChatOff
	refuseUnknown chatRefusal = ownerwire.CodeChatUnknownLead
	refuseBusy    chatRefusal = ownerwire.CodeChatBusy
	// refuseParams is the gate's bad-params word (gatehost.CodeBadParams),
	// which this file does not import.
	refuseParams chatRefusal = "bad-params"
)

// chatSource is what the port reads: the rows of the leads the seat may list,
// and one page of a lead's conversation. The scratch mode's chat desk is the
// one source; it checks the desk's switch, the store and the registry on
// every call. Nothing of it changes anything of a lead.
type chatSource interface {
	seatLeads() ([]ownerwire.ChatLeadRow, error)
	seatRead(lead string, after int64, limit int) (ownerwire.SeatChatResult, error)
	// seatCursor answers what the chat cursor covers: the leads the seat may
	// read now (switched read, not stopped by a scan, a store that works),
	// each with its switch generation and head, and nothing of any other
	// lead.
	seatCursor() ([]ownerwire.ChatCursorLead, error)
	// onChange gives the source the function it calls when what seatCursor
	// answers may have changed; nil takes it back.
	onChange(func())
}

// LeadChat is the daemon's chat port for the seat: the read-only view of the
// scratch leads' replies. Its zero value and a nil one answer chat-busy, as
// does one the scratch mode has not bound yet (the gate is built before the
// daemon runs, and the scratch mode binds the port once its leads' stores are
// open) and one it unbound at shutdown, so a gate can be given the port
// before the daemon runs. A panic below it is answered chat-busy too.
//
// Every call is a read: the switch of each lead (off by default, and off
// again after every restart of the daemon) is checked on every list and every
// read, and a lead that is off is never shown but as off. At most
// [ChatCallsPerMinute] calls a minute are served across every connection.
type LeadChat struct {
	src atomic.Pointer[chatBinding]
	// now is the clock of the call bound; nil means time.Now.
	now func() time.Time

	mu    sync.Mutex
	calls []time.Time // the calls served, until a full minute has passed since each

	// beforeLock, a test's hook, runs in admit before the lock is taken.
	beforeLock func()

	// cmu guards cch, the broadcast of ChatCursorChanged: closed and
	// replaced when the bound source says its cursor may have changed, and
	// at every bind.
	cmu sync.Mutex
	cch chan struct{}
}

// chatBinding holds the bound source and the key of its chat cursor: a
// random key made at the bind, held in the daemon's memory alone, so the
// cursors of one binding compare equal exactly when their sets do, and no
// cursor can be recognised across a bind (a restart, a replaced source).
type chatBinding struct {
	src chatSource
	key ownerwire.ChatCursorKey
}

// NewLeadChat returns a port that answers chat-busy until the scratch mode of
// a [Run] whose [Scratch].Chat it is binds it.
func NewLeadChat() *LeadChat { return &LeadChat{} }

// bind gives the port its source; nil unbinds it. The source it replaces is
// told no more to say its changes, and the cursor's broadcast is closed, so a
// held wait reads the cursor of the new binding (or none).
func (c *LeadChat) bind(src chatSource) {
	if c == nil {
		return
	}
	var old *chatBinding
	if src == nil {
		old = c.src.Swap(nil)
	} else {
		src.onChange(c.cursorChanged)
		old = c.src.Swap(&chatBinding{src: src, key: ownerwire.NewChatCursorKey()})
	}
	if old != nil && old.src != nil && (src == nil || old.src != src) {
		old.src.onChange(nil)
	}
	c.cursorChanged()
}

// errNoCursor is the error of a cursor the port cannot give: it is not bound,
// or its source failed. The extended wait then answers without one.
var errNoCursor = errors.New("lead chat: no chat cursor")

// ChatCursor answers the chat cursor of the extended seat.wait
// (ownerchannel.ChatCursor, by its method set alone):
// [ownerwire.ChatCursor], under the binding's key, over the leads the seat
// may read now, each with the generation of its switch and its head. A lead whose chat is off, stopped by
// a scan, or whose store failed is not in it, so nothing it does moves the
// cursor. It reads only the desk's switches and the stores' status: it reads
// no reply, changes nothing, and is not counted against
// [ChatCallsPerMinute], since the seat does not send it. An unbound port, a
// failing source and a panic below it answer an error: no cursor.
func (c *LeadChat) ChatCursor(ctx context.Context) (v int64, err error) {
	if c == nil {
		return 0, errNoCursor
	}
	defer func() {
		if recover() != nil {
			v, err = 0, errNoCursor
		}
	}()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	b := c.src.Load()
	if b == nil || b.src == nil {
		return 0, errNoCursor
	}
	leads, err := b.src.seatCursor()
	if err != nil {
		return 0, errNoCursor
	}
	return ownerwire.ChatCursor(b.key, leads), nil
}

// ChatCursorChanged answers a channel that is closed when the cursor may
// next have changed: an enabled lead's head moved, a lead's switch changed,
// a scan stopped a lead's chat or its store failed, or the port was bound or
// unbound. A lead whose chat is off closes nothing.
func (c *LeadChat) ChatCursorChanged() <-chan struct{} {
	if c == nil {
		return nil
	}
	c.cmu.Lock()
	defer c.cmu.Unlock()
	if c.cch == nil {
		c.cch = make(chan struct{})
	}
	return c.cch
}

// cursorChanged closes the broadcast. It never blocks and takes only cmu.
func (c *LeadChat) cursorChanged() {
	c.cmu.Lock()
	if c.cch != nil {
		close(c.cch)
		c.cch = nil
	}
	c.cmu.Unlock()
}

// admit reports whether one more call fits in the minute, and counts it. The
// clock is read under the lock, so the admissions are counted in the order
// they were made; an admission is counted until a full minute has passed
// since it, and one the clock shows as later than now (a clock that went
// back) is counted too, never dropped.
func (c *LeadChat) admit() bool {
	if c.beforeLock != nil {
		c.beforeLock()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	kept := c.calls[:0]
	for _, t := range c.calls {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	c.calls = kept
	if len(c.calls) >= ChatCallsPerMinute {
		return false
	}
	c.calls = append(c.calls, now)
	return true
}

// Chat serves one seat.chat call: the leads, or one page of a lead's
// conversation. The gate's strict decoder has read the params; the port
// checks the verb again and refuses anything else with bad-params.
func (c *LeadChat) Chat(ctx context.Context, p ownerwire.SeatChatParams) (res ownerwire.SeatChatResult, err error) {
	if c == nil {
		return ownerwire.SeatChatResult{}, refuseBusy
	}
	defer func() {
		if recover() != nil {
			res, err = ownerwire.SeatChatResult{}, refuseBusy
		}
	}()
	if err := ctx.Err(); err != nil {
		return ownerwire.SeatChatResult{}, err
	}
	b := c.src.Load()
	if b == nil || b.src == nil {
		return ownerwire.SeatChatResult{}, refuseBusy
	}
	switch p.Verb {
	case ownerwire.ChatVerbLeads:
		if p.Lead != "" || p.After != 0 || p.Limit != 0 {
			return ownerwire.SeatChatResult{}, refuseParams
		}
	case ownerwire.ChatVerbRead:
		if !ownerwire.ValidChatLead(p.Lead) || p.After < 0 || p.After > ownerwire.MaxChatSeq ||
			p.Limit < 1 || p.Limit > ownerwire.MaxChatReadLimit {
			return ownerwire.SeatChatResult{}, refuseParams
		}
	default:
		return ownerwire.SeatChatResult{}, refuseParams
	}
	if !c.admit() {
		return ownerwire.SeatChatResult{}, refuseBusy
	}
	if p.Verb == ownerwire.ChatVerbLeads {
		rows, err := b.src.seatLeads()
		if err != nil {
			return ownerwire.SeatChatResult{}, err
		}
		if rows == nil {
			rows = []ownerwire.ChatLeadRow{}
		}
		return ownerwire.SeatChatResult{State: ownerwire.ChatStateLeads, Leads: rows}, nil
	}
	return b.src.seatRead(p.Lead, p.After, p.Limit)
}
