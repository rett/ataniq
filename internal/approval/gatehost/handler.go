package gatehost

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatgate"
)

// Port is what the handler needs of the owner channel's service: the seat's
// pairing call and the four reads. [*ownerchannel.Service] is the real one; a
// test gives a fake.
type Port interface {
	PairPresent(ctx context.Context, src ownerchannel.Source, p ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error)
	List(ctx context.Context) (ownerwire.Listing, error)
	Show(ctx context.Context, id string) (ownerwire.ShowResult, error)
	Paths(ctx context.Context, p ownerwire.PageParams) (ownerwire.PathsPage, error)
	Patch(ctx context.Context, p ownerwire.PageParams) (ownerwire.PatchPage, error)
}

var _ Port = (*ownerchannel.Service)(nil)

// The capabilities the handler serves, the names the gate's hello lists.
const (
	// CapPair is the seat's pairing call, seat.pair.
	CapPair = "pair"
	// CapRead is the reads of the cards: owner.list, owner.show,
	// owner.paths and owner.patch.
	CapRead = "read"
)

// Caps returns the capability list of the handler, for the gate's hello
// ([seatgate.ServerConfig].Caps), in a new slice.
func Caps() []string { return []string{CapPair, CapRead} }

// opCaps maps each operation the handler serves to its capability.
var opCaps = map[string]string{
	ownerwire.OpSeatPair: CapPair,
	ownerwire.OpList:     CapRead,
	ownerwire.OpShow:     CapRead,
	ownerwire.OpPaths:    CapRead,
	ownerwire.OpPatch:    CapRead,
}

// Operations returns the operations the handler serves, in a new slice, in
// the order of the package documentation.
func Operations() []string {
	return []string{ownerwire.OpSeatPair, ownerwire.OpList, ownerwire.OpShow, ownerwire.OpPaths, ownerwire.OpPatch}
}

// Handler is the [seatgate.Handler] of the daemon's gate. Make one with
// [New]. It is safe for concurrent use, as the gate needs.
type Handler struct {
	svc    Port
	pinned func(fingerprint string) bool
	log    func(format string, a ...any)
	now    func() time.Time // the clock of the refusal bound
	bound  bound
}

var _ seatgate.Handler = (*Handler)(nil)

// New returns the handler that serves the gate's operations with svc. pinned
// says whether a key fingerprint is pinned now; it is asked on every call, and
// a peer it says no to is refused not-pinned. log receives one line for each
// refusal, within the refusal bound (see the package documentation); nil
// discards them. svc and pinned are required.
func New(svc Port, pinned func(fingerprint string) bool, log func(format string, a ...any)) (*Handler, error) {
	if svc == nil || pinned == nil {
		return nil, errors.New("gatehost: the service and the pin check are required")
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Handler{svc: svc, pinned: pinned, log: log, now: time.Now}, nil
}

// Handle serves one request of a seat. It is called by the gate, in a
// goroutine of the request's own; see the package documentation for what it
// answers.
func (h *Handler) Handle(ctx context.Context, peer seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
	key := boundKey(peer)
	if ok, first := h.bound.admit(key, h.now()); !ok {
		if first {
			h.log("seat gate: the peer %s was refused %d times within %v; it is refused %s, unlogged, until they age out", peerName(peer), RefusalBound, RefusalWindow, CodeTooManyRefusals)
		}
		return nil, seatgate.NewCodedError(CodeTooManyRefusals)
	}
	var o outcome
	if !seatgate.ValidFingerprint(peer.Fingerprint) || !h.pinned(peer.Fingerprint) {
		o = outcome{code: CodeNotPinned}
	} else {
		o = h.serve(ctx, peer, op, params)
	}
	if o.code == "" {
		return o.out, nil
	}
	if counted[o.code] && !h.bound.count(key, h.now()) {
		// Past the bound, by requests admitted before it was reached: the
		// peer is shut out from here, and the line was or will be written.
		return nil, seatgate.NewCodedError(o.code)
	}
	return nil, h.refused(peer, op, o)
}

// outcome is what serving one request came to: a result, or a code and,
// for a failure, its cause, which goes to the log and not to the seat.
type outcome struct {
	out   json.RawMessage
	code  string
	cause error
}

// serve runs one request that came from a pinned peer.
func (h *Handler) serve(ctx context.Context, peer seatgate.Peer, op string, params json.RawMessage) outcome {
	switch op {
	case ownerwire.OpSeatPair:
		p, ok := decodeParams(params, ownerwire.DecodeSeatPairParams)
		if !ok {
			return outcome{code: CodeBadParams}
		}
		r, err := h.svc.PairPresent(ctx, seatSource(peer), p)
		return answer(r, err, ownerwire.EncodeSeatPairResult)
	case ownerwire.OpList:
		if _, ok := decodeParams(params, decodeNothing); !ok {
			return outcome{code: CodeBadParams}
		}
		r, err := h.svc.List(ctx)
		return answer(r, err, ownerwire.EncodeListing)
	case ownerwire.OpShow:
		p, ok := decodeParams(params, ownerwire.DecodeShowParams)
		if !ok {
			return outcome{code: CodeBadParams}
		}
		r, err := h.svc.Show(ctx, p.ID)
		return answer(r, err, ownerwire.EncodeShowResult)
	case ownerwire.OpPaths:
		p, ok := decodeParams(params, ownerwire.DecodePageParams)
		if !ok {
			return outcome{code: CodeBadParams}
		}
		r, err := h.svc.Paths(ctx, p)
		return answer(r, err, ownerwire.EncodePathsPage)
	case ownerwire.OpPatch:
		p, ok := decodeParams(params, ownerwire.DecodePageParams)
		if !ok {
			return outcome{code: CodeBadParams}
		}
		r, err := h.svc.Patch(ctx, p)
		return answer(r, err, ownerwire.EncodePatchPage)
	}
	return outcome{code: CodeUnknownOp}
}

// seatSource is the source every call of a seat is handed to the service
// with: the seat's surface, a session named by the peer's key, and never the
// decider. peer's fingerprint has been checked to be 64 hexadecimal digits.
func seatSource(peer seatgate.Peer) ownerchannel.Source {
	return ownerchannel.Source{Surface: ownerchannel.SurfaceSeat, Session: peer.Fingerprint[:16]}
}

// decodeParams reads params, absent ones as {}, by the gate's strict rules
// and then by dec, the ownerwire decoder of the operation, and reports
// whether both took them. Nothing of a refusal is kept: its text could name
// what the params held.
func decodeParams[T any](params json.RawMessage, dec func([]byte) (T, error)) (T, bool) {
	var zero T
	raw := params
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var probe T
	if seatgate.DecodeStrict(raw, &probe) != nil {
		return zero, false
	}
	v, err := dec(raw)
	if err != nil {
		return zero, false
	}
	return v, true
}

// decodeNothing is [ownerwire.DecodeNothing] in the shape of the other
// decoders.
func decodeNothing(b []byte) (ownerwire.Nothing, error) {
	return ownerwire.Nothing{}, ownerwire.DecodeNothing(b)
}

// answer encodes the result of a service call with the encoder of its type,
// which holds the same caps and checks as the seat's decoder, or turns the
// service's error into its code.
func answer[T any](v T, err error, enc func(T) ([]byte, error)) outcome {
	if err != nil {
		code := codeOf(err)
		switch code {
		case CodeFailed, CodeKeysUnavailable:
			// The cause, which may name a path, is for the log.
			return outcome{code: code, cause: err}
		}
		return outcome{code: code}
	}
	b, err := enc(v)
	if err != nil {
		return outcome{code: CodeFailed, cause: errResultInvalid}
	}
	return outcome{out: b}
}

// errResultInvalid is the cause logged for a result of the service that its
// encoder refused. The encoder's own text is not kept: it could name the
// content.
var errResultInvalid = errors.New("the service's result does not encode")

// refused logs a refusal and returns the error that carries its code.
func (h *Handler) refused(peer seatgate.Peer, op string, o outcome) error {
	who := peerName(peer)
	if o.cause != nil {
		h.log("seat gate: %s refused %s: %s (peer %s)", opName(op), o.code, safeText(o.cause.Error()), who)
	} else {
		h.log("seat gate: %s refused %s (peer %s)", opName(op), o.code, who)
	}
	return seatgate.NewCodedError(o.code)
}

// opName is the operation as the log names it: one of the five, or a fixed
// word for any other, which a peer chose.
func opName(op string) string {
	if _, ok := opCaps[op]; ok {
		return op
	}
	return "an unknown operation"
}

// peerName is the peer as the log names it: the first 16 hexadecimal digits
// of its key fingerprint.
func peerName(peer seatgate.Peer) string {
	if !seatgate.ValidFingerprint(peer.Fingerprint) {
		return "with no valid fingerprint"
	}
	return peer.Fingerprint[:16]
}

// maxLogText caps the error text of one log line.
const maxLogText = 300

// safeText makes s one printable line of at most maxLogText bytes: a control
// character or a byte that is not UTF-8 becomes a question mark.
func safeText(s string) string {
	out := make([]rune, 0, len(s))
	n := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '�' {
			r = '?'
		}
		n += len(string(r))
		if n > maxLogText {
			break
		}
		out = append(out, r)
	}
	return string(out)
}
