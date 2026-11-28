package gatehost

import (
	"context"
	"encoding/json"
	"errors"

	"ataniq/internal/approval/ownerwire"
)

// This file is the route of seat.chat, the seat's read-only view of the
// leads' conversations. It holds the chat port and nothing else of the
// handler's: the route reads the params with the wire's strict decoder, hands
// them to the port, and encodes the port's answer with the wire's encoder. It
// reaches no other port of the handler: not the owner channel's service (its
// reads, the pairing, a login or a decision), not the wait and not the thread
// store; a test holds that the route names none of them, and another that a
// call of it reaches none.

// CapChat is the read-only view of the leads' conversations, seat.chat. A
// handler serves it, with either constructor, only when it was made with
// [WithChat]; like seat.wait and seat.genesis it needs no seat origin, since
// it only reads and decides nothing.
const CapChat = "chat"

// ChatPort is what the handler needs of the daemon for seat.chat: one call
// that takes the params as the wire's decoder read them and answers the wire's
// result. The daemon gives one only to a gate that serves chat (in this slice,
// the phone trial's stand-in, package internal/chat/standin); a test gives a
// fake. A refusal is an error that carries the gate's word, found by its
// method
//
//	GateCode() string
//
// through [errors.As]: one of [ownerwire.CodeChatOff],
// [ownerwire.CodeChatUnknownLead] and [ownerwire.CodeChatBusy], or
// [CodeBadParams]. A refusal that carries any other word, and every other
// error, is answered failed and logged, as are a cancelled call (unavailable)
// and an expired one (deadline-exceeded).
//
// The port is read-only: nothing of the wire asks it to send, and it is given
// nothing but the params and the request's context, so it holds no way to the
// decide path, the pairing, the key list, the limits, the intake or the
// host's effects.
type ChatPort interface {
	Chat(ctx context.Context, p ownerwire.SeatChatParams) (ownerwire.SeatChatResult, error)
}

// WithChat gives the handler the daemon's chat port, so that it serves
// seat.chat and lists [CapChat], last. A nil port serves nothing.
func WithChat(p ChatPort) Option {
	return func(h *Handler) {
		if p != nil {
			h.chat = p
			h.caps[CapChat] = true
		}
	}
}

// chatCodes are the gate words a [ChatPort] may refuse with: the three words
// of seat.chat, and bad-params for params the port itself refuses.
var chatCodes = map[string]bool{
	ownerwire.CodeChatOff:         true,
	ownerwire.CodeChatUnknownLead: true,
	ownerwire.CodeChatBusy:        true,
	CodeBadParams:                 true,
}

// errChatWord is the cause logged for a refusal of the port that carries a
// word that is not one of seat.chat. The word itself is not logged: the log
// holds only texts the handler chose.
var errChatWord = errors.New("the chat port refused with a word that is not one of seat.chat")

// serveChat runs one seat.chat call: the wire's strict decoder holds the verb
// and exactly the members the verb takes, each within its grammar and bounds,
// so a send, a lead of another form or a limit past 16 never reaches the
// port. Whether the lead exists and whether its chat is on are the port's,
// and each has its word.
func (h *Handler) serveChat(ctx context.Context, params json.RawMessage) outcome {
	p, ok := decodeParams(params, ownerwire.DecodeSeatChatParams)
	if !ok {
		return outcome{code: CodeBadParams}
	}
	r, err := h.chat.Chat(ctx, p)
	return chatAnswer(r, err)
}

// chatAnswer turns the port's answer into the handler's outcome: its result
// encoded (the encoder holds the cap and every rule of the result, so a page
// over 16 KiB, an off row with a state or a text with a bidirectional control
// is not sent), a refusal that carries one of the words of seat.chat as that
// word, and any other error as the generic answers of [answer].
func chatAnswer(r ownerwire.SeatChatResult, err error) outcome {
	if err != nil {
		var g interface{ GateCode() string }
		if errors.As(err, &g) {
			if c := g.GateCode(); chatCodes[c] {
				return outcome{code: c}
			}
			return outcome{code: CodeFailed, cause: errChatWord}
		}
	}
	return answer(r, err, ownerwire.EncodeSeatChatResult)
}
