package gatehost

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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

// SeatPort is what the handler needs of the service for a login and a
// decision from the phone: the four operations only the gate serves, each
// with the gate's own values. [*ownerchannel.Service] is the real one; a test
// gives a fake.
type SeatPort interface {
	SeatLoginChallenge(ctx context.Context, src ownerchannel.Source, g ownerchannel.Gate) (ownerwire.SeatLoginChallengeResult, error)
	SeatLogin(ctx context.Context, src ownerchannel.Source, g ownerchannel.Gate, p ownerwire.SeatLoginParams) (ownerwire.SeatLoginResult, error)
	SeatChallenge(ctx context.Context, src ownerchannel.Source, g ownerchannel.Gate, p ownerwire.SeatChallengeParams) (ownerwire.SeatChallengeResult, error)
	SeatDecide(ctx context.Context, src ownerchannel.Source, g ownerchannel.Gate, p ownerwire.SeatDecideParams) (ownerwire.DecideResult, error)
}

var _ SeatPort = (*ownerchannel.Service)(nil)

// WaitPort is what the handler needs of the service for seat.wait, the long
// wait for a change of the pending cards. [*ownerchannel.Service] is the real
// one. A handler serves the operation, and lists [CapWatch] in its hello,
// only when the service it was made with is a WaitPort: it is looked for
// once, in [New], so that a service that cannot wait is simply not asked.
type WaitPort interface {
	WaitPending(ctx context.Context, since string) (ownerwire.SeatWaitResult, error)
}

var _ WaitPort = (*ownerchannel.Service)(nil)

// GenesisPort is what the handler needs of the daemon for seat.genesis, the
// guided new-project interview: one call that takes the params as the wire's
// decoder read them and answers the wire's result. The daemon's thread store
// ([ataniq/internal/genesis/thread].Store) is the real one; a test gives a
// fake. A refusal is an error that carries the gate's word, which is found by
// its method
//
//	GateCode() string
//
// through [errors.As]: one of the six words of seat.genesis
// ([ownerwire.CodeGenesisBusy] and the rest) or [CodeBadParams]. A refusal
// that carries any other word, and every other error, is answered failed and
// logged, as are a cancelled call (unavailable) and an expired one
// (deadline-exceeded). A handler serves the operation, and lists [CapGenesis]
// in its hello, only when it was given a port with [WithGenesis].
type GenesisPort interface {
	Genesis(ctx context.Context, p ownerwire.SeatGenesisParams) (ownerwire.SeatGenesisResult, error)
}

// The capabilities the handler serves, the names the gate's hello lists.
const (
	// CapPair is the seat's pairing call, seat.pair.
	CapPair = "pair"
	// CapRead is the reads of the cards: owner.list, owner.show,
	// owner.paths and owner.patch.
	CapRead = "read"
	// CapLogin is a login from the phone: seat.login-challenge and
	// seat.login. Only a handler made with [NewDeciding] serves it.
	CapLogin = "login"
	// CapDecide is a decision from the phone: seat.challenge and
	// seat.decide. Only a handler made with [NewDeciding] serves it.
	CapDecide = "decide"
	// CapWatch is the long wait for a change of the pending cards, seat.wait.
	// A handler serves it, with either constructor, when its service is a
	// [WaitPort]; it needs no seat origin, since it only learns that the
	// cards changed and decides nothing.
	CapWatch = "watch"
	// CapGenesis is the guided new-project interview, seat.genesis. A
	// handler serves it, with either constructor, only when it was made with
	// [WithGenesis]; like seat.wait it needs no seat origin, since an answer
	// is a message from the owner and decides nothing.
	CapGenesis = "genesis"
)

// MaxWaitsPerPeer is how many seat.wait calls the handler holds at once for
// one peer. The seat holds one; the second is room for a reconnect, whose
// predecessor may still be unwinding on its connection's context. A third is
// answered [ownerwire.CodeWaitFull].
const MaxWaitsPerPeer = 2

// waitFullLogEvery is the least time between two log lines about wait-full
// for one peer: the answer is not counted by the refusal bound (see
// countedFor), so the log, which the bound keeps short, is bounded here.
const waitFullLogEvery = time.Minute

// Caps returns the capability list of a handler made with [New] over a service
// that cannot wait, pairing and the reads, in a new slice. [Handler.Caps] is
// the list of a given handler, which adds [CapWatch] when its service can wait.
func Caps() []string { return []string{CapPair, CapRead} }

// DecidingCaps returns the capability list of a handler made with
// [NewDeciding] over a service that cannot wait, in a new slice.
func DecidingCaps() []string { return []string{CapPair, CapRead, CapLogin, CapDecide} }

// opCaps maps each operation the handler may serve to its capability.
var opCaps = map[string]string{
	ownerwire.OpSeatPair:           CapPair,
	ownerwire.OpList:               CapRead,
	ownerwire.OpShow:               CapRead,
	ownerwire.OpPaths:              CapRead,
	ownerwire.OpPatch:              CapRead,
	ownerwire.OpSeatLoginChallenge: CapLogin,
	ownerwire.OpSeatLogin:          CapLogin,
	ownerwire.OpSeatChallenge:      CapDecide,
	ownerwire.OpSeatDecide:         CapDecide,
	ownerwire.OpSeatWait:           CapWatch,
	ownerwire.OpSeatGenesis:        CapGenesis,
}

// Operations returns the operations a handler made with [New] serves, in a
// new slice, in the order of the package documentation. A handler made with
// [NewDeciding] serves [ownerwire.GateOperations] as well.
func Operations() []string {
	return []string{ownerwire.OpSeatPair, ownerwire.OpList, ownerwire.OpShow, ownerwire.OpPaths, ownerwire.OpPatch}
}

// Handler is the [seatgate.Handler] of the daemon's gate. Make one with
// [New] or [NewDeciding]. It is safe for concurrent use, as the gate needs.
type Handler struct {
	svc    Port
	pinned func(fingerprint string) bool
	log    func(format string, a ...any)
	now    func() time.Time // the clock of the refusal bound
	bound  bound
	// seat and gate are a deciding handler's: the service's login and
	// decision, and the gate's own values handed to them. seat is nil for a
	// handler made with New.
	seat SeatPort
	gate ownerchannel.Gate
	// wait is the service's wait, or nil when it cannot wait (CapWatch is
	// then not served).
	wait  WaitPort
	waits waitSlots
	// genesis is the daemon's thread store, or nil (CapGenesis is then not
	// served).
	genesis GenesisPort
	caps    map[string]bool
	// counts holds how often each code was answered, in memory.
	cmu    sync.Mutex
	counts map[string]int64
}

var _ seatgate.Handler = (*Handler)(nil)

// Option changes what a handler serves beyond what its service gives.
type Option func(*Handler)

// WithGenesis gives the handler the daemon's thread store, so that it serves
// seat.genesis and lists [CapGenesis]. A nil port serves nothing.
func WithGenesis(p GenesisPort) Option {
	return func(h *Handler) {
		if p != nil {
			h.genesis = p
			h.caps[CapGenesis] = true
		}
	}
}

// New returns the handler that serves pairing and the reads with svc. pinned
// says whether a key fingerprint is pinned now; it is asked on every call, and
// a peer it says no to is refused not-pinned. log receives one line for each
// refusal, within the refusal bound (see the package documentation); nil
// discards them. svc and pinned are required. The options add what the daemon
// supplies beyond the service.
func New(svc Port, pinned func(fingerprint string) bool, log func(format string, a ...any), opts ...Option) (*Handler, error) {
	if svc == nil || pinned == nil {
		return nil, errors.New("gatehost: the service and the pin check are required")
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	h := &Handler{svc: svc, pinned: pinned, log: log, now: time.Now,
		caps: map[string]bool{CapPair: true, CapRead: true}, counts: map[string]int64{}}
	if w, ok := svc.(WaitPort); ok {
		h.wait = w
		h.caps[CapWatch] = true
	}
	for _, o := range opts {
		o(h)
	}
	return h, nil
}

// NewDeciding returns the handler that also serves a login and a decision
// from the phone, with seat and the gate's own values g: the gate's key
// fingerprint, and the seat origin and its relying party id, which the daemon
// read once when the gate started. The daemon makes one only when the origin
// is configured and its service can decide; the service checks g on every
// call. The rest is as for [New]; seat is required.
func NewDeciding(svc Port, seat SeatPort, g ownerchannel.Gate, pinned func(fingerprint string) bool, log func(format string, a ...any), opts ...Option) (*Handler, error) {
	if seat == nil {
		return nil, errors.New("gatehost: a deciding handler needs the service's seat operations")
	}
	h, err := New(svc, pinned, log, opts...)
	if err != nil {
		return nil, err
	}
	h.seat, h.gate = seat, g
	h.caps[CapLogin], h.caps[CapDecide] = true, true
	return h, nil
}

// Caps returns the capability list of the handler, for the gate's hello
// ([seatgate.ServerConfig].Caps), in a new slice: [Caps] or [DecidingCaps],
// [CapWatch] after them when the handler's service can wait, and [CapGenesis]
// last when the handler was given the thread store.
func (h *Handler) Caps() []string {
	caps := Caps()
	if h.seat != nil {
		caps = DecidingCaps()
	}
	if h.wait != nil {
		caps = append(caps, CapWatch)
	}
	if h.genesis != nil {
		caps = append(caps, CapGenesis)
	}
	return caps
}

// Counts returns how often the handler answered each refusal code since it
// was made, too-many-refusals included, in a new map. It is kept in memory
// only.
func (h *Handler) Counts() map[string]int64 {
	h.cmu.Lock()
	defer h.cmu.Unlock()
	out := make(map[string]int64, len(h.counts))
	for k, v := range h.counts {
		out[k] = v
	}
	return out
}

// note counts one answer of a code.
func (h *Handler) note(code string) {
	h.cmu.Lock()
	h.counts[code]++
	h.cmu.Unlock()
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
		h.note(CodeTooManyRefusals)
		return nil, seatgate.NewCodedError(CodeTooManyRefusals)
	}
	var o outcome
	if !seatgate.ValidFingerprint(peer.Fingerprint) || !h.pinned(peer.Fingerprint) {
		o = outcome{code: CodeNotPinned}
	} else {
		o = h.serve(ctx, peer, op, params)
	}
	if o.code == "" {
		if o.issued {
			h.log("seat gate: %s issued a challenge (peer %s)", op, peerName(peer))
		}
		return o.out, nil
	}
	h.note(o.code)
	if countedFor(op, o.code) && !h.bound.count(key, h.now()) {
		// Past the bound, by requests admitted before it was reached: the
		// peer is shut out from here, and the line was or will be written.
		return nil, seatgate.NewCodedError(o.code)
	}
	if o.code == ownerwire.CodeWaitFull && !h.waits.mayLogFull(key, h.now()) {
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
	// issued says a challenge was drawn, which the log tells.
	issued bool
}

// serve runs one request that came from a pinned peer. An operation whose
// capability the handler does not serve is unknown-op, before its params
// are read.
func (h *Handler) serve(ctx context.Context, peer seatgate.Peer, op string, params json.RawMessage) outcome {
	if c, ok := opCaps[op]; !ok || !h.caps[c] {
		return outcome{code: CodeUnknownOp}
	}
	switch op {
	case ownerwire.OpSeatPair:
		p, ok := decodeParams(params, ownerwire.DecodeSeatPairParams)
		if !ok {
			return outcome{code: CodeBadParams}
		}
		// A gate with a seat origin registers a credential only for its own
		// relying party, the one every login and decision is checked
		// against: the service registers with the id the seat relayed.
		if h.seat != nil && p.RPID != "" && p.RPID != h.gate.RPID {
			return outcome{code: CodeCredentialInvalid}
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
	case ownerwire.OpSeatLoginChallenge:
		if _, ok := decodeParams(params, decodeNothing); !ok {
			return outcome{code: CodeBadParams}
		}
		r, err := h.seat.SeatLoginChallenge(ctx, seatSource(peer), h.gate)
		o := answer(r, err, ownerwire.EncodeSeatLoginChallengeResult)
		o.issued = o.code == ""
		return o
	case ownerwire.OpSeatLogin:
		// The envelope only: the service takes the challenge before it
		// judges the assertion.
		p, ok := decodeParams(params, ownerwire.DecodeSeatLoginParams)
		if !ok {
			return outcome{code: CodeBadParams}
		}
		r, err := h.seat.SeatLogin(ctx, seatSource(peer), h.gate, p)
		return answer(r, err, ownerwire.EncodeSeatLoginResult)
	case ownerwire.OpSeatChallenge:
		p, ok := decodeParams(params, ownerwire.DecodeSeatChallengeParams)
		if !ok {
			return outcome{code: CodeBadParams}
		}
		r, err := h.seat.SeatChallenge(ctx, seatSource(peer), h.gate, p)
		o := answer(r, err, ownerwire.EncodeSeatChallengeResult)
		o.issued = o.code == ""
		return o
	case ownerwire.OpSeatDecide:
		// The envelope only (ownerwire.DecodeSeatDecideParams): the frame's
		// content is checked by the service after it took and burnt the
		// challenge.
		p, ok := decodeParams(params, ownerwire.DecodeSeatDecideParams)
		if !ok {
			return outcome{code: CodeBadParams}
		}
		r, err := h.seat.SeatDecide(ctx, seatSource(peer), h.gate, p)
		return answer(r, err, ownerwire.EncodeDecideResult)
	case ownerwire.OpSeatWait:
		p, ok := decodeParams(params, ownerwire.DecodeSeatWaitParams)
		if !ok {
			return outcome{code: CodeBadParams}
		}
		// One slot for the call's whole life. The service holds the call
		// for the request's own deadline less a margin (ctx is the gate's,
		// which carries it) and returns when ctx is done, which is when the
		// request ends or the peer's connection does: the slot is then
		// given back and nothing is left running.
		key := boundKey(peer)
		if !h.waits.take(key) {
			return outcome{code: ownerwire.CodeWaitFull}
		}
		defer h.waits.give(key)
		r, err := h.wait.WaitPending(ctx, p.Since)
		return answer(r, err, ownerwire.EncodeSeatWaitResult)
	case ownerwire.OpSeatGenesis:
		// The wire's strict decoder holds the verb and exactly the members
		// the verb takes, each within its grammar and caps. Whether the thread
		// exists, the node is a question of the round and the text looks like
		// a secret are the store's, and each has its word.
		p, ok := decodeParams(params, ownerwire.DecodeSeatGenesisParams)
		if !ok {
			return outcome{code: CodeBadParams}
		}
		r, err := h.genesis.Genesis(ctx, p)
		return genesisAnswer(r, err)
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
		case ownerwire.CodeAssertionInvalid:
			// The cause names the check that failed, from the verifier's
			// fixed list; it holds no byte of the assertion.
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
		h.log("seat gate: %s refused %s: %s (peer %s)", h.opName(op), o.code, safeText(o.cause.Error()), who)
	} else {
		h.log("seat gate: %s refused %s (peer %s)", h.opName(op), o.code, who)
	}
	return seatgate.NewCodedError(o.code)
}

// opName is the operation as the log names it: one the handler may serve,
// or a fixed word for any other, which a peer chose.
func (h *Handler) opName(op string) string {
	if c, ok := opCaps[op]; ok && h.caps[c] {
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
