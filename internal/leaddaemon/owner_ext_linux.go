//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
)

// The surface of the owner connection: the only kind of surface that may be
// the decider.
const ownerSurface = "console"

// ownerConsoleName is the name a client must give as the first word of its
// client string to be granted the decider's place.
const ownerConsoleName = "ataniq-console"

// The reasons a connection is not the decider, in the host's words. They go to
// the console and show in its footer; none holds anything a peer sent.
const (
	reasonNotConsole = "this client does not name the console, so it may read but not decide"
	reasonNoTerminal = "this client has no controlling terminal, so it may read but not decide"
	reasonNotJudged  = "the daemon could not check this client's process, so it may read but not decide"
	reasonSecond     = "another console is the one that may decide; this one is read-only"
	reasonGone       = "this connection ended"
)

// Fixed texts of the errors the extension answers with itself. None holds
// anything a peer sent.
const (
	msgOwnerNoConn    = "the request could not be tied to a connection"
	msgOwnerBadParams = "the parameters are not valid for this operation"
	msgOwnerSession   = "this connection already named another session or client"
	msgOwnerNotDecide = "this connection is not the one that may decide"
	msgOwnerStopped   = "the owner channel is not serving"
	msgOwnerDoor      = "the door could not be reached; the decision is journaled and is handed over later"
	msgOwnerJournal   = "the journal was reopened after a write that may not have been made; look at the card again"
	msgOwnerFailed    = "the operation failed; see the daemon's log"
	msgOwnerStopping  = "the daemon is stopping"
)

// The codes the extension adds to the record package's, which are the owner
// connection's refusal codes.
const (
	ownerCodeUnavailable     clientapi.ErrorCode = "unavailable"
	ownerCodeDoor            clientapi.ErrorCode = "door-unavailable"
	ownerCodeJournalReopened clientapi.ErrorCode = "journal-reopened"
	ownerCodeFailed          clientapi.ErrorCode = "failed"
)

// The codes of the pairing and key operations, each for one error of the
// service, with a fixed text of its own. None says anything a peer sent, and
// none says more than the error: a refusal of the pairing code is the one
// code whatever was wrong with it.
const (
	ownerCodeKeysUnavailable   clientapi.ErrorCode = "keys-unavailable"
	ownerCodeKeysFull          clientapi.ErrorCode = "keys-full"
	ownerCodeKeyUnknown        clientapi.ErrorCode = "key-unknown"
	ownerCodeKeyAmbiguous      clientapi.ErrorCode = "key-ambiguous"
	ownerCodePairingOpen       clientapi.ErrorCode = "pairing-open"
	ownerCodePairingNone       clientapi.ErrorCode = "pairing-none"
	ownerCodePairingExpired    clientapi.ErrorCode = "pairing-expired"
	ownerCodePairingChanged    clientapi.ErrorCode = "pairing-changed"
	ownerCodePairingRefused    clientapi.ErrorCode = "pairing-refused"
	ownerCodeCredentialInvalid clientapi.ErrorCode = "credential-invalid"
)

// The fixed texts of the pairing codes.
const (
	msgOwnerKeysUnavailable   = "the list of deciding keys cannot be used; see the daemon's log"
	msgOwnerKeysFull          = "the list of deciding keys is full"
	msgOwnerKeyUnknown        = "no credential has this id"
	msgOwnerKeyAmbiguous      = "the id names more than one credential; give more of it"
	msgOwnerPairingOpen       = "a pairing is already open; start again with replace to end it"
	msgOwnerPairingNone       = "no credential is waiting to be confirmed or rejected"
	msgOwnerPairingExpired    = "the credential waited too long; the pairing is over"
	msgOwnerPairingChanged    = "the pending credential is not the one you compared; look at the words again"
	msgOwnerPairingRefused    = "the pairing was refused"
	msgOwnerCredentialInvalid = "the credential cannot be used"
	msgOwnerNoSeat            = "this build has no seat that may pair a phone"
)

// ownerExt is the extension that serves the owner.* operations.
type ownerExt struct {
	oc  *OwnerChannel
	svc OwnerService
	reg *ownerConns
	// seat is [OwnerChannel]'s seat source; nil refuses seat.pair.
	seat func(v ownerView) (ownerchannel.Source, bool)
}

var _ clientapi.Extension = (*ownerExt)(nil)

// OwnerOperations returns the operations of the owner connection, in the order
// hello lists them, in a fresh slice.
func OwnerOperations() []string { return ownerwire.Operations() }

// Operations lists the owner connection's operations.
func (x *ownerExt) Operations() []string { return OwnerOperations() }

// Mutating is true: a decision, a beat, a change of the limit and the pairing
// and key writes change things.
func (x *ownerExt) Mutating() bool { return true }

// Handle serves one operation. A connection that cannot be found is refused
// for the operations that need to know it (the decider, a decision, a beat, a
// change of the limit, the pairing and key writes and seat.pair); the reads do
// not.
func (x *ownerExt) Handle(ctx context.Context, op string, params json.RawMessage) (any, *clientapi.ErrorBody) {
	switch op {
	case ownerwire.OpList:
		if f := ownerNothing(params); f != nil {
			return nil, f
		}
		return ownerResult(x.svc.List(ctx))(ownerwire.EncodeListing, x)
	case ownerwire.OpShow:
		p, err := ownerwire.DecodeShowParams(params)
		if err != nil {
			return nil, x.fail(err)
		}
		return ownerResult(x.svc.Show(ctx, p.ID))(ownerwire.EncodeShowResult, x)
	case ownerwire.OpPaths:
		p, err := ownerwire.DecodePageParams(params)
		if err != nil {
			return nil, x.fail(err)
		}
		return ownerResult(x.svc.Paths(ctx, p))(ownerwire.EncodePathsPage, x)
	case ownerwire.OpPatch:
		p, err := ownerwire.DecodePageParams(params)
		if err != nil {
			return nil, x.fail(err)
		}
		return ownerResult(x.svc.Patch(ctx, p))(ownerwire.EncodePatchPage, x)
	case ownerwire.OpLimit:
		p, err := ownerwire.DecodeLimitParams(params)
		if err != nil {
			return nil, x.fail(err)
		}
		return ownerResult(x.svc.Limit(ctx, p.Project))(ownerwire.EncodeLimitView, x)
	case ownerwire.OpLooked:
		if f := ownerNothing(params); f != nil {
			return nil, f
		}
		if err := x.svc.Looked(ctx); err != nil {
			return nil, x.fail(err)
		}
		return json.RawMessage(ownerwire.EncodeNothing()), nil
	case ownerwire.OpDecider:
		return x.decider(ctx, params)
	case ownerwire.OpDecide:
		return x.decide(ctx, params)
	case ownerwire.OpBeat:
		return x.beat(params)
	case ownerwire.OpLimitSet:
		return x.limitSet(ctx, params)
	case ownerwire.OpPairStatus:
		if f := ownerNothing(params); f != nil {
			return nil, f
		}
		return ownerResult(x.svc.PairStatus(ctx))(ownerwire.EncodePairStatus, x)
	case ownerwire.OpKeys:
		if f := ownerNothing(params); f != nil {
			return nil, f
		}
		return ownerResult(x.svc.Keys(ctx))(ownerwire.EncodeKeysView, x)
	case ownerwire.OpPairStart:
		return x.pairStart(ctx, params)
	case ownerwire.OpPairConfirm:
		return x.pairConfirm(ctx, params)
	case ownerwire.OpPairReject:
		return x.pairReject(ctx, params)
	case ownerwire.OpKeyRevoke:
		return x.keyRevoke(ctx, params)
	case ownerwire.OpKeysOff:
		return x.keysOff(ctx, params)
	case ownerwire.OpSeatPair:
		return x.seatPair(ctx, params)
	}
	return nil, &clientapi.ErrorBody{Code: clientapi.CodeUnsupportedOperation, Message: msgOwnerBadParams}
}

// ownerNothing checks the params of an operation that takes none: absent, or
// the empty object.
func ownerNothing(params json.RawMessage) *clientapi.ErrorBody {
	if len(params) == 0 {
		return nil
	}
	if err := ownerwire.DecodeNothing(params); err != nil {
		return &clientapi.ErrorBody{Code: clientapi.CodeInvalidRequest, Message: msgOwnerBadParams}
	}
	return nil
}

// ownerResult encodes the result of a service call with the encoder of its
// type, which holds the same caps and checks as the decoder on the console's
// side, and answers the error otherwise.
func ownerResult[T any](v T, err error) func(enc func(T) ([]byte, error), x *ownerExt) (any, *clientapi.ErrorBody) {
	return func(enc func(T) ([]byte, error), x *ownerExt) (any, *clientapi.ErrorBody) {
		if err != nil {
			return nil, x.fail(err)
		}
		b, err := enc(v)
		if err != nil {
			return nil, x.fail(err)
		}
		return json.RawMessage(b), nil
	}
}

// connection returns the connection of the calling goroutine, or counts and
// refuses.
func (x *ownerExt) connection() (*ownerConn, *clientapi.ErrorBody) {
	c := x.reg.current()
	if c == nil {
		x.oc.counts.NoConnection.Add(1)
		x.oc.logf("owner channel: a request could not be tied to a connection")
		return nil, &clientapi.ErrorBody{Code: ownerCodeUnavailable, Message: msgOwnerNoConn}
	}
	return c, nil
}

// decider answers owner.decider: it keeps the client and the session for the
// connection, and grants the decider's place when the connection may have it.
func (x *ownerExt) decider(ctx context.Context, params json.RawMessage) (any, *clientapi.ErrorBody) {
	p, err := ownerwire.DecodeDeciderParams(params)
	if err != nil {
		return nil, x.fail(err)
	}
	c, fail := x.connection()
	if fail != nil {
		return nil, fail
	}
	if !x.reg.declare(c, p.Client, p.Session) {
		x.oc.counts.SessionChanged.Add(1)
		x.oc.logf("owner channel: a connection named a second session or client (peer pid %d)", c.pid)
		return nil, &clientapi.ErrorBody{Code: string2code(record.CodeDecisionInvalid), Message: msgOwnerSession}
	}
	if x.reg.isDecider(c) {
		return x.deciderAnswer(true, "")
	}
	switch {
	case clientName(p.Client) != ownerConsoleName:
		x.oc.counts.NotConsole.Add(1)
		x.oc.logf("owner channel: the decider's place was refused to a client that does not name the console (peer pid %d)", c.pid)
		return x.deciderAnswer(false, reasonNotConsole)
	case c.pid < 1:
		x.oc.counts.NotInspected.Add(1)
		x.oc.logf("owner channel: the decider's place was refused: the peer's process id is not known")
		return x.deciderAnswer(false, reasonNotJudged)
	}
	proc, err := x.oc.Inspector.Inspect(PeerRef{PID: c.pid, AcceptedAt: c.acceptedAt})
	switch {
	case err != nil:
		x.oc.counts.NotInspected.Add(1)
		x.oc.logf("owner channel: the decider's place was refused: the peer (pid %d) could not be checked: %s", c.pid, SafeText(err.Error()))
		return x.deciderAnswer(false, reasonNotJudged)
	case proc.TTY == "" || !ownerPrintable(proc.TTY, 256):
		x.oc.counts.NoTerminal.Add(1)
		x.oc.logf("owner channel: the decider's place was refused to a peer with no controlling terminal (pid %d)", c.pid)
		return x.deciderAnswer(false, reasonNoTerminal)
	}
	switch x.reg.grant(c, proc.TTY) {
	case seatTaken:
		x.oc.counts.SecondConsole.Add(1)
		x.oc.logf("owner channel: a second console asked for the decider's place and is read-only (peer pid %d)", c.pid)
		return x.deciderAnswer(false, reasonSecond)
	case seatClosed:
		return x.deciderAnswer(false, reasonGone)
	}
	x.oc.counts.DeciderGranted.Add(1)
	x.oc.logf("owner channel: the decider's place is given to peer pid %d on %s", c.pid, SafeText(proc.TTY))
	return x.deciderAnswer(true, "")
}

func (x *ownerExt) deciderAnswer(decider bool, reason string) (any, *clientapi.ErrorBody) {
	b, err := ownerwire.EncodeDeciderResult(ownerwire.DeciderResult{Decider: decider, Reason: reason})
	if err != nil {
		return nil, x.fail(err)
	}
	return json.RawMessage(b), nil
}

// clientName is the first word of a client string.
func clientName(client string) string {
	name, _, _ := strings.Cut(client, " ")
	return name
}

// decide answers owner.decide. The service is told whether the connection is
// the decider, and the session and the process of the connection, which the
// service writes into the decision record and into the journal; a frame from a
// connection that is not the decider is still handed to the service, which
// journals it refused, and is counted here; after [DecisionRefusalBound] of
// them the connection is closed and no further frame is journaled.
func (x *ownerExt) decide(ctx context.Context, params json.RawMessage) (any, *clientapi.ErrorBody) {
	c, fail := x.connection()
	if fail != nil {
		return nil, fail
	}
	v := x.reg.view(c)
	if !v.decider {
		x.oc.counts.DecisionNotDecider.Add(1)
		if x.reg.refuseDecide(c) > DecisionRefusalBound {
			// Past the bound a frame is refused without the journal and the
			// connection ends: one connection cannot fill the journal or hold
			// the decision lock. The bound is per connection: a process of
			// the same user can connect again and get its frames refused and
			// journaled up to the bound once more (the same-user limit of
			// OC20).
			x.oc.counts.DecisionFlooded.Add(1)
			x.oc.logf("owner channel: a connection that is not the decider sent more than %d decision frames and was closed (peer pid %d)", DecisionRefusalBound, c.pid)
			_ = c.Close()
			return nil, &clientapi.ErrorBody{Code: string2code(record.CodeNotDecider), Message: msgOwnerNotDecide}
		}
		x.oc.logf("owner channel: a decision frame was refused: the connection is not the decider (peer pid %d)", c.pid)
		// The frame is read so that the journal can say which card it was
		// for; the answer is the same whatever the frame holds, and a frame
		// that does not read is refused without the journal.
		if f, err := ownerwire.DecodeDecisionFrame(params); err == nil {
			_, _ = x.svc.Decide(ctx, x.source(v), f)
		}
		return nil, &clientapi.ErrorBody{Code: string2code(record.CodeNotDecider), Message: msgOwnerNotDecide}
	}
	f, err := ownerwire.DecodeDecisionFrame(params)
	if err != nil {
		x.oc.counts.DecisionInvalid.Add(1)
		return nil, x.fail(err)
	}
	return ownerResult(x.svc.Decide(ctx, x.source(v), f))(ownerwire.EncodeDecideResult, x)
}

// beat answers owner.beat: a beat reaches the service, which hears only the
// decider's.
func (x *ownerExt) beat(params json.RawMessage) (any, *clientapi.ErrorBody) {
	if f := ownerNothing(params); f != nil {
		return nil, f
	}
	c, fail := x.connection()
	if fail != nil {
		return nil, fail
	}
	if err := x.svc.Beat(x.source(x.reg.view(c))); err != nil {
		return nil, x.fail(err)
	}
	return json.RawMessage(ownerwire.EncodeNothing()), nil
}

// limitSet answers owner.limit-set; the service refuses it to a connection that
// is not the decider.
func (x *ownerExt) limitSet(ctx context.Context, params json.RawMessage) (any, *clientapi.ErrorBody) {
	p, err := ownerwire.DecodeLimitSetParams(params)
	if err != nil {
		return nil, x.fail(err)
	}
	c, fail := x.connection()
	if fail != nil {
		return nil, fail
	}
	return ownerResult(x.svc.LimitSet(ctx, x.source(x.reg.view(c)), p))(ownerwire.EncodeLimitView, x)
}

// pairSource is the source of a write of the pairing and key operations: the
// connection must be the decider, which the daemon knows and the service takes
// as given. A write from a connection that is not is refused not-decider
// before its params are read, and counts against the same per-connection bound
// as a decision frame: past [DecisionRefusalBound] of them the connection is
// closed.
func (x *ownerExt) pairSource() (ownerchannel.Source, *clientapi.ErrorBody) {
	c, fail := x.connection()
	if fail != nil {
		return ownerchannel.Source{}, fail
	}
	v := x.reg.view(c)
	if v.decider {
		return x.source(v), nil
	}
	x.oc.counts.PairNotDecider.Add(1)
	if x.reg.refuseDecide(c) > DecisionRefusalBound {
		x.oc.counts.DecisionFlooded.Add(1)
		x.oc.logf("owner channel: a connection that is not the decider sent more than %d refused writes and was closed (peer pid %d)", DecisionRefusalBound, c.pid)
		_ = c.Close()
	} else {
		x.oc.logf("owner channel: a pairing or key write was refused: the connection is not the decider (peer pid %d)", c.pid)
	}
	return ownerchannel.Source{}, &clientapi.ErrorBody{Code: string2code(record.CodeNotDecider), Message: msgOwnerNotDecide}
}

// pairStart answers owner.pair-start. The code in its result is the only place
// the code is; it is not logged.
func (x *ownerExt) pairStart(ctx context.Context, params json.RawMessage) (any, *clientapi.ErrorBody) {
	s, fail := x.pairSource()
	if fail != nil {
		return nil, fail
	}
	p, err := ownerwire.DecodePairStartParams(params)
	if err != nil {
		return nil, x.fail(err)
	}
	return ownerResult(x.svc.PairStart(ctx, s, p))(ownerwire.EncodePairStartResult, x)
}

// pairConfirm answers owner.pair-confirm. Its params name the pending
// credential, and the service refuses one that is not the one pending now.
func (x *ownerExt) pairConfirm(ctx context.Context, params json.RawMessage) (any, *clientapi.ErrorBody) {
	s, fail := x.pairSource()
	if fail != nil {
		return nil, fail
	}
	p, err := ownerwire.DecodePairPendingParams(params)
	if err != nil {
		return nil, x.fail(err)
	}
	return ownerResult(x.svc.PairConfirm(ctx, s, p))(ownerwire.EncodeKeyItem, x)
}

// pairReject answers owner.pair-reject.
func (x *ownerExt) pairReject(ctx context.Context, params json.RawMessage) (any, *clientapi.ErrorBody) {
	s, fail := x.pairSource()
	if fail != nil {
		return nil, fail
	}
	p, err := ownerwire.DecodePairPendingParams(params)
	if err != nil {
		return nil, x.fail(err)
	}
	if err := x.svc.PairReject(ctx, s, p); err != nil {
		return nil, x.fail(err)
	}
	return json.RawMessage(ownerwire.EncodeNothing()), nil
}

// keyRevoke answers owner.key-revoke.
func (x *ownerExt) keyRevoke(ctx context.Context, params json.RawMessage) (any, *clientapi.ErrorBody) {
	s, fail := x.pairSource()
	if fail != nil {
		return nil, fail
	}
	p, err := ownerwire.DecodeKeyRevokeParams(params)
	if err != nil {
		return nil, x.fail(err)
	}
	return ownerResult(x.svc.KeyRevoke(ctx, s, p))(ownerwire.EncodeKeysView, x)
}

// keysOff answers owner.keys-off.
func (x *ownerExt) keysOff(ctx context.Context, params json.RawMessage) (any, *clientapi.ErrorBody) {
	s, fail := x.pairSource()
	if fail != nil {
		return nil, fail
	}
	if f := ownerNothing(params); f != nil {
		return nil, f
	}
	return ownerResult(x.svc.KeysOff(ctx, s))(ownerwire.EncodeKeysView, x)
}

// seatPair answers seat.pair, the seat app's call. It is answered only to a
// connection the seat source says is a seat, and no build has a seat source: it
// is refused "unavailable" to every connection, whatever its params, before
// they are read, so a refused call spends no try of a code and the pairing is
// not touched. The params are never logged.
func (x *ownerExt) seatPair(ctx context.Context, params json.RawMessage) (any, *clientapi.ErrorBody) {
	c, fail := x.connection()
	if fail != nil {
		return nil, fail
	}
	var s ownerchannel.Source
	ok := false
	if x.seat != nil {
		s, ok = x.seat(x.reg.view(c))
	}
	if !ok {
		x.oc.counts.SeatRefused.Add(1)
		x.oc.logf("owner channel: a seat.pair call was refused: this build has no seat source (peer pid %d)", c.pid)
		return nil, &clientapi.ErrorBody{Code: ownerCodeUnavailable, Message: msgOwnerNoSeat}
	}
	p, err := ownerwire.DecodeSeatPairParams(params)
	if err != nil {
		return nil, x.fail(err)
	}
	return ownerResult(x.svc.PairPresent(ctx, s, p))(ownerwire.EncodeSeatPairResult, x)
}

// source is what the service is told about the connection. The process, the
// terminal and the client name are given only for the decider, whose
// decisions carry them; the session is the one owner.decider named, or the
// one the daemon made for a connection that never asked.
func (x *ownerExt) source(v ownerView) ownerchannel.Source {
	src := ownerchannel.Source{Surface: ownerSurface, Session: v.session, Decider: v.decider}
	if v.decider {
		src.PID, src.TTY, src.Client = int64(v.pid), v.tty, v.client
	}
	return src
}

// fail turns an error of the service or of a decoder into the answer: a refusal
// keeps its code and its host-written detail, anything else is a code of its
// own with a fixed text, and the error itself goes to the log.
func (x *ownerExt) fail(err error) *clientapi.ErrorBody {
	var re *record.Error
	switch {
	case errors.As(err, &re):
		code := clientapi.ErrorCode(re.Code)
		if !ownerCodeOK(code) {
			x.oc.logf("owner channel: a refusal with a code that cannot be sent: %s", SafeText(err.Error()))
			return &clientapi.ErrorBody{Code: ownerCodeFailed, Message: msgOwnerFailed}
		}
		msg := re.Detail
		if msg == "" || !ownerPrintable(msg, 256) {
			msg = string(re.Code)
		}
		return &clientapi.ErrorBody{Code: code, Message: msg}
	case errors.Is(err, context.Canceled):
		return &clientapi.ErrorBody{Code: ownerCodeUnavailable, Message: msgOwnerStopping}
	case errors.Is(err, ownerchannel.ErrClosed), errors.Is(err, ownerchannel.ErrStopped):
		return &clientapi.ErrorBody{Code: ownerCodeUnavailable, Message: msgOwnerStopped}
	case errors.Is(err, ownerchannel.ErrKeysUnavailable):
		// The cause, which may name a path, is in the log and not in the
		// answer.
		x.oc.logf("owner channel: the list of deciding keys cannot be used: %s", SafeText(err.Error()))
		return &clientapi.ErrorBody{Code: ownerCodeKeysUnavailable, Message: msgOwnerKeysUnavailable}
	case errors.Is(err, ownerchannel.ErrKeysFull):
		return &clientapi.ErrorBody{Code: ownerCodeKeysFull, Message: msgOwnerKeysFull}
	case errors.Is(err, ownerchannel.ErrKeyUnknown):
		return &clientapi.ErrorBody{Code: ownerCodeKeyUnknown, Message: msgOwnerKeyUnknown}
	case errors.Is(err, ownerchannel.ErrKeyAmbiguous):
		return &clientapi.ErrorBody{Code: ownerCodeKeyAmbiguous, Message: msgOwnerKeyAmbiguous}
	case errors.Is(err, ownerchannel.ErrPairingOpen):
		return &clientapi.ErrorBody{Code: ownerCodePairingOpen, Message: msgOwnerPairingOpen}
	case errors.Is(err, ownerchannel.ErrNoPairing):
		return &clientapi.ErrorBody{Code: ownerCodePairingNone, Message: msgOwnerPairingNone}
	case errors.Is(err, ownerchannel.ErrPairingExpired):
		return &clientapi.ErrorBody{Code: ownerCodePairingExpired, Message: msgOwnerPairingExpired}
	case errors.Is(err, ownerchannel.ErrPairingChanged):
		return &clientapi.ErrorBody{Code: ownerCodePairingChanged, Message: msgOwnerPairingChanged}
	case errors.Is(err, ownerchannel.ErrPairRefused):
		return &clientapi.ErrorBody{Code: ownerCodePairingRefused, Message: msgOwnerPairingRefused}
	case errors.Is(err, ownerchannel.ErrCredentialInvalid):
		return &clientapi.ErrorBody{Code: ownerCodeCredentialInvalid, Message: msgOwnerCredentialInvalid}
	case errors.Is(err, ownerchannel.ErrNotSeat):
		return &clientapi.ErrorBody{Code: ownerCodeUnavailable, Message: msgOwnerNoSeat}
	case errors.Is(err, ownerchannel.ErrDoorUnavailable):
		return &clientapi.ErrorBody{Code: ownerCodeDoor, Message: msgOwnerDoor}
	case errors.Is(err, ownerchannel.ErrJournalReopened):
		return &clientapi.ErrorBody{Code: ownerCodeJournalReopened, Message: msgOwnerJournal}
	}
	x.oc.logf("owner channel: %s", SafeText(err.Error()))
	return &clientapi.ErrorBody{Code: ownerCodeFailed, Message: msgOwnerFailed}
}

func string2code(c record.Code) clientapi.ErrorCode { return clientapi.ErrorCode(c) }

// ownerCodeOK reports whether the code can go on the wire as an extension's:
// lower-case letters, digits and dashes, starting with a letter, and none of
// the codes that end a connection.
func ownerCodeOK(code clientapi.ErrorCode) bool {
	if len(code) < 1 || len(code) > 64 {
		return false
	}
	for i := 0; i < len(code); i++ {
		switch c := code[i]; {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return false
		}
	}
	switch code {
	case clientapi.CodeUnauthorized, clientapi.CodeBusy, clientapi.CodeTooLarge, clientapi.CodeUnsupportedProtocol:
		return false
	}
	return true
}

// ownerPrintable reports whether s is valid UTF-8 of printable text, at most
// max bytes.
func ownerPrintable(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}
