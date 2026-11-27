// Package protocol defines the provisional stdio contract. The holder owns
// the pipes and serializes calls for one launch. A pack supplies a pure codec
// for that build's wire grammar. Core owns request IDs, limits and framing.
package protocol

import (
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"ataniq/internal/agentsession"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/pack"
)

type Framing string

const Lines Framing = "lines"

const (
	MaxPending    = 64
	MaxEvents     = 16 // per frame
	MaxEventBytes = 1024
)

var (
	ErrUntested      = errors.New("protocol codec unavailable for build")
	ErrUnsupported   = errors.New("unsupported protocol request")
	ErrPendingLimit  = errors.New("too many pending protocol requests")
	ErrUnknownAnswer = errors.New("answer has no pending request")
)

type Operation string

const (
	Open        Operation = "open"
	Message     Operation = "message"
	StopTurn    Operation = "stop-turn"
	StopSession Operation = "stop-session"
)

// Request.Text is the admitted, rendered message including its header.
// Only Message carries text. A build may omit Open or either stop operation.
type Request struct {
	Operation Operation
	Text      string
}

// Event is an untrusted transport observation. The holder supplies the
// sequence and time. Only a state event has a State.
type Event struct {
	Kind  string
	State agentsession.State
	Text  string
}

// Decoded contains the observations from one payload. An answer must set
// AnswerTo even when it carries no events. Notifications leave it nil.
// An unrecognised frame yields the zero value and therefore no observation.
type Decoded struct {
	AnswerTo *string
	Events   []Event
}

// Codec handles bounded payloads without the final LF. Encode must put id
// on the wire, including for open and stop requests. It returns ErrUnsupported
// for an omitted operation. Decode must distinguish answers from notifications
// and reject malformed answers. Decode transfers ownership of its result to
// the caller. One codec serves one launch and does no I/O.
type Codec interface {
	Framing() Framing
	Encode(id string, req Request) ([]byte, error)
	Decode(payload []byte) (Decoded, error)
}

type Source interface {
	ProtocolCodec(version string) (Codec, bool)
}

// Session tracks outstanding requests for one launch. Discard it on restart.
// The caller serializes Encode, Decode and Forget in transport order.
type Session struct {
	codec   Codec
	pending map[string]struct{}
}

// From selects a fresh codec for a declared protocol build. An exact alias
// resolves to the same canonical version that the pack used in its manifest.
func From(p pack.Pack, version string) (*Session, error) {
	source, ok := p.(Source)
	if !ok || version == "" {
		return nil, ErrUntested
	}
	for _, b := range p.Manifest().Builds {
		if version != b.Version && !slices.Contains(b.Aliases, version) {
			continue
		}
		if slices.Contains(b.Transports, pack.Protocol) {
			c, ok := source.ProtocolCodec(b.Version)
			if ok && c != nil && c.Framing() == Lines {
				return &Session{codec: c, pending: make(map[string]struct{})}, nil
			}
		}
		break
	}
	return nil, ErrUntested
}

// Encode registers a request only after the codec produces one bounded
// payload. The caller owns the returned LF-terminated frame.
func (s *Session) Encode(req Request) (id string, frame []byte, err error) {
	switch req.Operation {
	case Message:
		if err := wire.CheckInjectText(req.Text); err != nil {
			return "", nil, err
		}
	case Open, StopTurn, StopSession:
		if req.Text != "" {
			return "", nil, ErrInvalidFrame
		}
	default:
		return "", nil, ErrUnsupported
	}
	if len(s.pending) >= MaxPending {
		return "", nil, ErrPendingLimit
	}
	// rand.Text draws at least 128 random bits for each request.
	for {
		id = rand.Text()
		if _, exists := s.pending[id]; !exists {
			break
		}
	}
	payload, err := s.codec.Encode(id, req)
	if err != nil {
		return "", nil, err
	}
	if err := checkPayload(payload); err != nil {
		return "", nil, err
	}
	frame = make([]byte, len(payload)+1)
	copy(frame, payload)
	frame[len(payload)] = '\n'
	s.pending[id] = struct{}{}
	return id, frame, nil
}

// Forget retires an abandoned request, for example after a timeout. It makes
// a late answer stale. It makes no assertion about delivery to the peer.
func (s *Session) Forget(id string) {
	delete(s.pending, id)
}

// Decode accepts one complete line. It releases events only after checking
// the entire frame and consumes a matching answer exactly once.
func (s *Session) Decode(frame []byte) (Decoded, error) {
	if len(frame) > MaxFrameBytes {
		return Decoded{}, ErrFrameTooLarge
	}
	if len(frame) == 0 || frame[len(frame)-1] != '\n' {
		return Decoded{}, ErrInvalidFrame
	}
	payload := frame[:len(frame)-1]
	if err := checkPayload(payload); err != nil {
		return Decoded{}, err
	}
	d, err := s.codec.Decode(payload)
	if err != nil {
		return Decoded{}, err
	}
	if d.AnswerTo != nil {
		if _, ok := s.pending[*d.AnswerTo]; !ok {
			return Decoded{}, ErrUnknownAnswer
		}
	}
	if len(d.Events) > MaxEvents {
		return Decoded{}, ErrInvalidFrame
	}
	for i := range d.Events {
		e := &d.Events[i]
		switch e.Kind {
		case "state":
			if !e.State.Valid() {
				e.State = "unknown"
			}
		case "delivery", "progress", "question", "limit", "ended":
			e.State = ""
		default:
			return Decoded{}, ErrInvalidFrame
		}
		if !utf8.ValidString(e.Text) {
			return Decoded{}, ErrInvalidFrame
		}
		if len(e.Text) > MaxEventBytes {
			end := MaxEventBytes
			for !utf8.RuneStart(e.Text[end]) {
				end--
			}
			e.Text = strings.Clone(e.Text[:end])
		}
	}
	if d.AnswerTo != nil {
		s.Forget(*d.AnswerTo)
	}
	return d, nil
}
