package gatehost

import (
	"context"
	"encoding/json"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
)

// This file is the extended seat.wait: the wait that also carries the chat
// cursor of the leads the desk switched on (ownerwire, waitchat.go). It is
// opt-in on both sides. The handler lists [CapWaitChat] only when it serves
// the wait and the chat, its service can answer the extended wait
// ([ChatWaitPort], which the owner channel's service is) and its chat port
// gives a cursor ([ownerchannel.ChatCursor], which the trial's stand-in and
// the scratch leads' port give); a seat sends the extended params only to a
// gate that lists it.
//
// A handler that does not list it reads seat.wait's params with the plain
// decoder alone, which refuses a chat member: such params are bad-params, as
// at a gate of an earlier build. A handler that lists it takes both forms: the
// plain params are answered exactly as before, with the plain result, and the
// extended params with the extended result. The chat port's cursor is read by
// the service's wait and nowhere else; the cursor's methods only read the
// desk's switches and heads, and the handler gives them nothing but the
// request's context.

// CapWaitChat is the extended seat.wait: the chat cursor beside the Card
// version. It needs [CapWatch] and [CapChat], and is listed after them, last.
const CapWaitChat = "wait-chat"

// ChatWaitPort is what the handler needs of the service for the extended
// wait. [*ownerchannel.Service] is the real one.
type ChatWaitPort interface {
	WaitPendingChat(ctx context.Context, since string, chat int64, src ownerchannel.ChatCursor) (ownerwire.SeatWaitResult, error)
}

var _ ChatWaitPort = (*ownerchannel.Service)(nil)

// chatCursorOf is the chat port's cursor, or nil when it gives none.
func chatCursorOf(p ChatPort) ownerchannel.ChatCursor {
	if c, ok := p.(ownerchannel.ChatCursor); ok {
		return c
	}
	return nil
}

// waitChat reports whether the handler offers the extended wait.
func (h *Handler) waitChat() bool {
	return h.wait != nil && h.chatWait != nil && h.chat != nil && h.cursor != nil
}

// decodeWait reads seat.wait's params: the plain form, and, only when the
// handler offers the extended wait, the extended form; extended says which
// it was.
func (h *Handler) decodeWait(params json.RawMessage) (p ownerwire.SeatWaitParams, extended, ok bool) {
	if p, ok := decodeParams(params, ownerwire.DecodeSeatWaitParams); ok {
		return p, false, true
	}
	if !h.waitChat() {
		return ownerwire.SeatWaitParams{}, false, false
	}
	p, ok = decodeParams(params, ownerwire.DecodeSeatWaitChatParams)
	return p, ok, ok
}
