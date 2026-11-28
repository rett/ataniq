// Package protocol implements the holder-side Codex app-server line codec.
// One instance observes one ordered stdio stream. The caller owns framing,
// request correlation and the decision to dispatch any encoded payload.
package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

	"ataniq/internal/agentsession"
	core "ataniq/internal/agentsession/protocol"
	"ataniq/internal/leadholder/wire"
)

const Version = "0.172.0"
const SchemaDigest = "88f541c50da39797a301e606fc3b40ec6fa4f97926fb89bfdda2336780333a61"

var ErrSchema = errors.New("app-server schema digest mismatch")
var ErrTurn = errors.New("app-server thread or turn does not match")

// Codec adds the initialization notification, thread creation, explicitly
// targeted steer, and permission answers to the neutral request vocabulary.
type Codec interface {
	core.Codec
	Initialized() []byte
	StartThread(id string) ([]byte, error)
	// Forget abandons a thread/start binding attempt. A forgotten turn/start
	// keeps its reservation so the codec can still apply a late answer.
	Forget(id string)
	Steer(id, thread, expectedTurn, text string) ([]byte, error)
	DecodeGrant(request, reply []byte) (Grant, error)
	EncodeGrant(request []byte, grant Grant) ([]byte, error)
}

// A bound thread admits a Message when its latest status is idle, it has no
// live turns, and turnStartID is empty. Encoding the message reserves its
// request ID until a valid answer arrives, including an answer after Forget.
//
// While awaiting that answer, the latest live turn/started holds provisional
// control of steering and permission requests until the first completed ID
// is evicted. After eviction, only our turn/start answer can give control.
// An error releases the reservation and leaves no turn in control. A success
// releases it and gives control to the named turn unless its completion is
// remembered. Completing the controlling turn releases control.
// A change of control invalidates the previous turn's pending permissions.
//
// Each live turn supplies deltas. StopTurn targets the latest live start,
// with a turn known only from its answer as fallback. A completion retires
// its turn. The codec remembers up to MaxPending distinct completed IDs in
// FIFO order and ignores delayed starts for them. A delayed start for an
// evicted ID can become live and block admission. Thread status governs
// admission independently of these turn lifetimes.
//
// thread/closed or systemError ends admission and control for this codec.
type codec struct {
	startID     string // the one thread/start whose result may establish the binding
	turnStartID string
	completed   []string // oldest completion first
	evicted     bool     // a delayed start can now name a forgotten completion
	thread      string
	ownTurn     string   // the live turn identified by our turn/start answer
	liveTurns   []string // interrupt preference increases from first to last
	closed      bool
	threadState agentsession.State // latest status reported for the bound thread
	// Outstanding permission requests on this turn, keyed by string or
	// integer wire ID. The digest binds the decision to the observed bytes.
	pending map[any][sha256.Size]byte
}

// New requires the independently measured digest of the selected build.
func New(version, digest string) (Codec, error) {
	if version != Version {
		return nil, core.ErrUntested
	}
	if digest != SchemaDigest {
		return nil, ErrSchema
	}
	return &codec{pending: make(map[any][sha256.Size]byte)}, nil
}

func (*codec) Framing() core.Framing { return core.Lines }

func (*codec) Initialized() []byte { return []byte(`{"method":"initialized"}`) }

func (c *codec) StartThread(id string) ([]byte, error) {
	if c.startID != "" || c.thread != "" {
		return nil, ErrTurn
	}
	b, err := c.request(id, "thread/start", map[string]string{"approvalPolicy": "on-request", "sandbox": "read-only"})
	if err == nil {
		c.startID = id
	}
	return b, err
}

func (c *codec) request(id, method string, params any) ([]byte, error) {
	if id == "" {
		return nil, core.ErrInvalidFrame
	}
	if id == c.turnStartID || id == c.startID {
		return nil, ErrTurn
	}
	b, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params}, json.Deterministic(true))
	if err != nil {
		return nil, core.ErrInvalidFrame
	}
	if len(b) >= core.MaxFrameBytes {
		return nil, core.ErrFrameTooLarge
	}
	return b, nil
}

func (c *codec) Encode(id string, req core.Request) ([]byte, error) {
	if req.Operation != core.Message && req.Text != "" {
		return nil, core.ErrInvalidFrame
	}
	switch req.Operation {
	case core.Open:
		return c.request(id, "initialize", map[string]any{"clientInfo": map[string]string{"name": "ataniq", "version": "1"}})
	case core.Message:
		if c.threadState != "idle" || len(c.liveTurns) != 0 || c.turnStartID != "" {
			return nil, ErrTurn
		}
		b, err := c.message(id, "turn/start", "", req.Text)
		if err == nil {
			c.turnStartID = id
		}
		return b, err
	case core.StopTurn:
		if len(c.liveTurns) == 0 {
			return nil, ErrTurn
		}
		return c.request(id, "turn/interrupt", map[string]string{"threadId": c.thread, "turnId": c.liveTurns[len(c.liveTurns)-1]})
	default:
		return nil, core.ErrUnsupported
	}
}

func (c *codec) Forget(id string) {
	if id == c.startID {
		c.startID = ""
	}
}

func (c *codec) controlTurn() string {
	if !c.evicted && c.turnStartID != "" && len(c.liveTurns) > 0 {
		return c.liveTurns[len(c.liveTurns)-1]
	}
	return c.ownTurn
}

func (c *codec) current(thread, turn string) bool {
	return turn != "" && turn == c.controlTurn() && thread == c.thread
}

func (c *codec) Steer(id, thread, expectedTurn, text string) ([]byte, error) {
	if !c.current(thread, expectedTurn) {
		return nil, ErrTurn
	}
	return c.message(id, "turn/steer", expectedTurn, text)
}

func (c *codec) message(id, method, expectedTurn, text string) ([]byte, error) {
	if err := wire.CheckInjectText(text); err != nil {
		return nil, err
	}
	params := map[string]any{"threadId": c.thread, "input": []map[string]string{{"type": "text", "text": text}}}
	if expectedTurn != "" {
		params["expectedTurnId"] = expectedTurn
	}
	return c.request(id, method, params)
}

// Keep request IDs in their original wire type. A server request may use an
// integer, while answers to our own requests must echo their string IDs.
type envelope struct {
	ID        jsontext.Value `json:"id"`
	Method    jsontext.Value `json:"method"`
	Params    jsontext.Value `json:"params"`
	Result    jsontext.Value `json:"result"`
	Error     jsontext.Value `json:"error"`
	EmittedAt jsontext.Value `json:"emittedAtMs"`
	method    string
}

func parse(payload []byte) (envelope, error) {
	var e envelope
	if len(payload) >= core.MaxFrameBytes {
		return e, core.ErrFrameTooLarge
	}
	if bytes.ContainsRune(payload, '\n') || json.Unmarshal(payload, &e, json.RejectUnknownMembers(true)) != nil {
		return envelope{}, core.ErrInvalidFrame
	}
	if e.Method != nil {
		if json.Unmarshal(e.Method, &e.method) != nil || e.method == "" ||
			e.Result != nil || e.Error != nil || (e.Params != nil && e.Params.Kind() != '{') {
			return envelope{}, core.ErrInvalidFrame
		}
		if e.ID != nil && !validID(e.ID) {
			return envelope{}, core.ErrInvalidFrame
		}
	} else if e.Params != nil || !validID(e.ID) || (e.Result == nil) == (e.Error == nil) {
		return envelope{}, core.ErrInvalidFrame
	}
	if e.EmittedAt != nil {
		var ms int64
		if e.ID != nil || e.EmittedAt.Kind() != '0' || json.Unmarshal(e.EmittedAt, &ms) != nil {
			return envelope{}, core.ErrInvalidFrame
		}
	}
	return e, nil
}

func validID(id jsontext.Value) bool {
	switch id.Kind() {
	case '"':
		var s string
		return json.Unmarshal(id, &s) == nil && s != ""
	case '0':
		var n int64
		return json.Unmarshal(id, &n) == nil
	}
	return false
}
