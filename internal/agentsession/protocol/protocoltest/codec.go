// Package protocoltest supplies a synthetic line codec for contract tests.
package protocoltest

import (
	"strconv"
	"strings"

	"ataniq/internal/agentsession"
	"ataniq/internal/agentsession/protocol"
)

type Codec struct{}

func (Codec) Framing() protocol.Framing { return protocol.Lines }

// Encode writes an operation and request ID, followed for messages by quoted
// text so embedded line breaks stay inside the payload.
func (Codec) Encode(id string, r protocol.Request) ([]byte, error) {
	prefix := string(r.Operation) + " " + id
	switch r.Operation {
	case protocol.Open, protocol.StopSession:
		return []byte(prefix), nil
	case protocol.Message:
		return strconv.AppendQuote([]byte(prefix+" "), r.Text), nil
	default:
		return nil, protocol.ErrUnsupported
	}
}

// Decode recognises "answer ID", "answer ID TEXT", "state STATE" and
// "progress TEXT". The text on an answer is a delivery observation.
func (Codec) Decode(payload []byte) (protocol.Decoded, error) {
	op, rest, _ := strings.Cut(string(payload), " ")
	var event protocol.Event
	switch op {
	case "answer":
		id, text, _ := strings.Cut(rest, " ")
		if id == "" {
			return protocol.Decoded{}, protocol.ErrInvalidFrame
		}
		d := protocol.Decoded{AnswerTo: &id}
		if text != "" {
			d.Events = []protocol.Event{{Kind: "delivery", Text: text}}
		}
		return d, nil
	case "state":
		state := agentsession.State(rest)
		if !state.Valid() {
			state = "unknown"
		}
		event = protocol.Event{Kind: "state", State: state}
	case "progress":
		event = protocol.Event{Kind: "progress", Text: rest}
	default:
		return protocol.Decoded{}, nil
	}
	return protocol.Decoded{Events: []protocol.Event{event}}, nil
}
