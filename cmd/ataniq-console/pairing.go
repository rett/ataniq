package main

import (
	"context"
	"errors"
	"time"

	"ataniq/internal/consoleclient"
	"ataniq/internal/ownerlocal"
	"ataniq/internal/terminalconsole"
)

// The pair-a-phone view's backend over the daemon's owner client. The view
// takes a context and the daemon's pairing as the view's own types; the owner
// client takes no context and returns the wire's. This file is the one place
// that bridges them, and it changes neither side.
//
// The command may not import the wire package (see imports_test.go), so the
// adapter reads the client's answers by their fields, as strings.

// pairTimeLayout is the spelling of a time on the wire, a record's time (the
// wire package has already refused every other spelling; the adapter checks
// again, since a time it cannot read is an answer it cannot use).
const pairTimeLayout = "2006-01-02T15:04:05Z"

// The phases of a pairing as the wire spells them (ownerwire.PairNone and its
// siblings; a test holds these equal).
const (
	wirePairNone    = "none"
	wirePairWaiting = "waiting"
	wirePairPending = "pending"
	wirePairExpired = "expired"
)

// errPairAnswer is the error for an answer of the daemon that the view cannot
// use. Its text is fixed: nothing the daemon sent is in it.
var errPairAnswer = errors.New("the daemon's pairing answer was not usable")

// pairAdapter is the terminalconsole.PairingBackend of --daemon.
//
// It wraps the same *consoleclient.Owner as the console's approvals backend.
// That is required, not tidy: the pairing writes need the decider, and the
// decider is one connection's. A second Owner would be a second console to
// the daemon, would not be the decider and would be refused. The rig makes
// the adapter from the owner it hands to the approvals view and from no other.
//
// What the adapter does about the context: the owner client takes none and
// bounds each call itself (by its configured timeout, 10 s, which is the view's
// own bound too). So the adapter checks ctx first, and a call that is already
// over makes no call at all; it then calls once, in the caller's goroutine, and
// never again. Nothing is sent again after a failure: a PairStart whose answer
// did not come back lost its code by design, and the view says so and starts
// again. The adapter does not leave a call running behind the caller's back:
// the owner client holds its lock for the whole call, so an abandoned call
// would block the approvals view behind it.
type pairAdapter struct {
	o *consoleclient.Owner
}

var _ terminalconsole.PairingBackend = (*pairAdapter)(nil)

// newPairAdapter returns the adapter for the owner client o.
func newPairAdapter(o *consoleclient.Owner) *pairAdapter { return &pairAdapter{o: o} }

// PairStart makes a one-time code.
func (a *pairAdapter) PairStart(ctx context.Context, replace bool) (terminalconsole.PairOffer, error) {
	if err := ctx.Err(); err != nil {
		return terminalconsole.PairOffer{}, err
	}
	res, err := a.o.PairStart(replace)
	if err != nil {
		return terminalconsole.PairOffer{}, err
	}
	return offerFrom(res.Code, res.ExpiresAt)
}

// PairStatus reads the pairing's state.
func (a *pairAdapter) PairStatus(ctx context.Context) (terminalconsole.PairState, error) {
	if err := ctx.Err(); err != nil {
		return terminalconsole.PairState{}, err
	}
	st, err := a.o.PairStatus()
	if err != nil {
		return terminalconsole.PairState{}, err
	}
	return stateFrom(st.Phase, st.Words, st.PendingID, st.ExpiresAt, claim{kind: st.Kind, backupEligible: st.BackupEligible, backupState: st.BackupState})
}

// PairConfirm adds the pending phone's key as a deciding key.
func (a *pairAdapter) PairConfirm(ctx context.Context, id string) (terminalconsole.PairPaired, error) {
	if err := ctx.Err(); err != nil {
		return terminalconsole.PairPaired{}, err
	}
	k, err := a.o.PairConfirm(id)
	if err != nil {
		return terminalconsole.PairPaired{}, err
	}
	return terminalconsole.PairPaired{ID: k.ID, Kind: k.Kind}, nil
}

// PairReject ends the pairing without adding anything.
func (a *pairAdapter) PairReject(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.o.PairReject(id)
}

// offerFrom makes the view's offer of the wire's code and end time. A time it
// cannot read is a bad answer, and a code the view then does not get.
func offerFrom(code, expires string) (terminalconsole.PairOffer, error) {
	at, err := time.Parse(pairTimeLayout, expires)
	if err != nil || code == "" {
		return terminalconsole.PairOffer{}, errPairAnswer
	}
	return terminalconsole.PairOffer{Code: code, ExpiresAt: at}, nil
}

// claim is what the pending credential's authenticator claimed, as the wire
// spells it (ownerwire.PairStatus.Kind, BackupEligible and BackupState). The
// adapter passes it on as it is: it is the view's to word as a claim and to
// bound, and the adapter judges none of it.
type claim struct {
	kind           string
	backupEligible bool
	backupState    bool
}

// stateFrom makes the view's state of the wire's status. A pending status
// has exactly terminalconsole.PairWordCount words and a pending id; with any
// other count the view would show a fingerprint that is not the one the phone
// shows, so the answer is a bad one and no words are passed on. The claim is
// passed on for a pending status only, as the wire sends it only then. A
// phase the adapter does not know, and a time it cannot read (none has no
// time), are bad answers too.
func stateFrom(phase string, words []string, pendingID, expires string, c claim) (terminalconsole.PairState, error) {
	var st terminalconsole.PairState
	switch phase {
	case wirePairNone:
		st.Phase = terminalconsole.PairNone
		if expires == "" {
			return st, nil
		}
	case wirePairWaiting:
		st.Phase = terminalconsole.PairWaiting
	case wirePairPending:
		st.Phase = terminalconsole.PairPending
		if len(words) != terminalconsole.PairWordCount || pendingID == "" {
			return terminalconsole.PairState{}, errPairAnswer
		}
		copy(st.Words[:], words)
		st.PendingID = pendingID
		st.Kind = c.kind
		st.BackupEligible = c.backupEligible
		st.BackupState = c.backupState
	case wirePairExpired:
		st.Phase = terminalconsole.PairExpired
	default:
		return terminalconsole.PairState{}, errPairAnswer
	}
	at, err := time.Parse(pairTimeLayout, expires)
	if err != nil {
		return terminalconsole.PairState{}, errPairAnswer
	}
	st.ExpiresAt = at
	return st, nil
}

// pairOrigin returns the origin of the approval app for the pair-a-phone view:
// the owner-local seat origin file, read once when the console starts. A file
// that is missing, empty or refused gives "", and the view then says "pairing
// origin not configured" and draws no QR. The value is not logged and is in no
// error; whether it is a usable origin is the view's check.
func pairOrigin(getenv func(string) string) string {
	origin, err := ownerlocal.SeatOrigin(getenv)
	if err != nil {
		return ""
	}
	return origin
}
