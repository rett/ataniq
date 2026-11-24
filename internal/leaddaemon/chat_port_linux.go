//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
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
	calls []time.Time // the calls served the last minute
}

// chatBinding holds the bound source.
type chatBinding struct{ src chatSource }

// NewLeadChat returns a port that answers chat-busy until the scratch mode of
// a [Run] whose [Scratch].Chat it is binds it.
func NewLeadChat() *LeadChat { return &LeadChat{} }

// bind gives the port its source; nil unbinds it.
func (c *LeadChat) bind(src chatSource) {
	if c == nil {
		return
	}
	if src == nil {
		c.src.Store(nil)
		return
	}
	c.src.Store(&chatBinding{src: src})
}

// admit reports whether one more call fits in the minute, and counts it.
func (c *LeadChat) admit() bool {
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.calls[:0]
	for _, t := range c.calls {
		if now.Sub(t) < time.Minute && !t.After(now) {
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
