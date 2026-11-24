// Package seatlink is the seat app's port to the daemon over the seat gate:
// [Daemon] implements [seatapp.Daemon] by calling the gate's operations
// through a [seatgate.Client].
//
// Package seatapp may import nothing of the repository, so the link lives here,
// between the app and the transport. It opens no socket and no file: the
// command owns the client, its keys and its pins, and runs it.
//
// # Pairing
//
// [Daemon.Pair] sends seat.pair with [ownerwire.SeatPairParams], encoded by
// package ownerwire (which checks its form first), with the registration's
// authenticator data and relying party id from the request, from which the
// daemon reads the kind the credential claims, and reads the answer with
// [ownerwire.DecodeSeatPairResult], which holds it to exactly
// [ownerwire.PairWords] words of the fingerprint's grammar. The errors the app
// tells apart are mapped so:
//
//	the gate's code pairing-refused             seatapp.ErrCodeRefused
//	a code ownerwire does not take as a code    seatapp.ErrCodeRefused (nothing is sent)
//	a registration ownerwire does not take      a plain error (nothing is sent)
//	the link's budget for the call spent        seatapp.ErrBusy (nothing is sent)
//	seatgate.ErrDisconnected                    seatapp.ErrDaemonUnavailable
//	the context ended (cancelled or deadline)   seatapp.ErrDaemonUnavailable
//	anything else (any other code, a closed
//	client, an answer not of its form)          a plain error
//
// Only a refusal the daemon gave, and a code that cannot be one, say the code
// was refused, so the phone never tells the owner to scan again when the desk
// was not asked, and never says the desk is unreachable when the desk refused.
// Nothing is retried: a pairing call that was lost may have used one of the
// code's tries.
//
// The pairing code is a secret. No error of this package holds it, nor any
// byte of the request or of the answer; an error holds at most the gate's
// code, which is of the grammar [a-z0-9-]{1,40}.
//
// # Login
//
// [Daemon.LoginChallenge] sends seat.login-challenge and [Daemon.Login]
// seat.login, in the form docs/design/seat-gate.md gives them ("Login"). The
// link asks neither unless the gate's hello listed the capability "login"
// ([seatapp.ErrNotOffered] otherwise). Before a login challenge reaches the
// phone, the link recomputes it from the gate key's fingerprint, which [New]
// is given with [WithGatePin], and the daemon's nonce, with the one function
// the daemon uses too (ownerwire.LoginChallenge); a challenge that differs is
// an error and nothing is asked of the phone. The words of both calls are
// package ownerwire's (ownerwire.SeatLoginChallengeResult,
// ownerwire.SeatLoginParams, ownerwire.SeatLoginResult), read and written
// with its decoders and encoders. A login's answer must name the credential
// the assertion named, by its raw id and by its display id ([DisplayID]). The
// codes:
//
//	challenge-stale                             seatapp.ErrChallengeStale
//	assertion-invalid, key-unknown,
//	key-revoked, kind-refused                   seatapp.ErrLoginRefused
//	unknown-op (a gate without the operations)  seatapp.ErrNotOffered
//	rate-limited, busy, too-many-refusals       seatapp.ErrBusy
//
// and the rest as for the pairing.
//
// # Cards
//
// [Daemon.List], [Daemon.Show] and [Daemon.Pages] read the cards with
// owner.list, owner.show and owner.paths, and only when the gate's hello
// listed "read". Show builds the card the phone shows from the record's
// bytes, never from anything the gate rendered: the result is read with
// [ownerwire.DecodeShowResult], which takes nothing beside the record and its
// marks, the bytes must be their digest's, they must decode as the pending
// record of the card asked for, and the card model ([card.New]) checks them
// again, the binding's digest among it. A lead's text leaves the card model
// only through the lead-text escaping, so the phone shows it as the desk
// console does, and the app then shows it through its Text. Pages asks for
// the review the card was shown with (the link remembers it, and shows the
// card first when it does not know it) and takes only the page asked for. An
// id that is not a card's is not sent. The codes:
//
//	approval-void                               seatapp.ErrNoCard
//	review-changed                              seatapp.ErrCardChanged
//	too-large                                   seatapp.ErrTooLarge
//	unknown-op                                  seatapp.ErrNotOffered
//	rate-limited, busy, too-many-refusals       seatapp.ErrBusy
//
// and the rest as for the pairing. A record the card model refuses is no
// card; the phone says it could not be read. A card Show built is kept,
// with its review, so that a decision is built from the card the phone was
// shown; Show also passes on the daemon's mark of a decision on its way.
//
// # Decisions
//
// [Daemon.Offer], [Daemon.DecideChallenge] and [Daemon.Decide] are a
// decision from the phone, in the form docs/design/seat-gate.md gives it
// ("The challenge", "Decide" and "The phone's side"), and only when the
// gate's hello listed "decide" and the link has the gate's pin; without
// them Offer offers neither, so the Card page holds no form. Offer asks
// the gate nothing: it answers what the Card page may offer by the card
// model's rules (MayApprove with every flagged path signed off, and Deny),
// and withholds Approve, saying which part, for a card the page did not
// show whole (seatapp.Whole): a part with a path or a ref too long to be
// shown whole is not read, and a flagged path cut is never signed off by
// its index, since two of them may read the same on the page.
// DecideChallenge freezes the decision from the card Show kept, at the
// binding the Card page showed: the action, the flagged paths signed off by
// their index, in the card's order, and the reason, through the card
// model's Approve (MayApprove with the session's reading) or Deny; a
// decision they refuse is not sent. It sends seat.challenge, and recomputes
// the challenge the daemon answers from the gate's pin, the frozen frame and
// the daemon's nonce with ownerwire.DecisionChallenge, the function the
// daemon calls too; a challenge that differs is seatapp.ErrChallengeDiffers
// and is never handed on, so the phone is never asked to sign it. Decide
// sends seat.decide with the frame and the assertion, and nothing is
// retried. Both check their params with the daemon's decoder, cap included,
// and the transport sends exactly those bytes; params over the gate's cap
// are seatapp.ErrTooLarge and are not sent. The codes, on both calls:
//
//	challenge-stale                             seatapp.ErrChallengeStale
//	binding-mismatch, review-changed            seatapp.ErrCardChanged
//	approval-expired                            seatapp.ErrCardExpired
//	approval-consumed, approval-void,
//	previous-epoch, decision-invalid            seatapp.ErrCannotDecide
//	record-too-large (a decision record the
//	journal could not hold)                     seatapp.ErrTooLarge
//	assertion-invalid                           seatapp.ErrAssertionRefused
//	key-revoked, key-unknown                    seatapp.ErrKeyRevoked
//	kind-refused                                seatapp.ErrKindRefused
//	challenges-full, rate-limited, busy,
//	too-many-refusals                           seatapp.ErrBusy
//	unknown-op                                  seatapp.ErrNotOffered
//
// Before a decision is sent, a gate that is not connected is
// seatapp.ErrDaemonUnavailable. After it is sent, only a refusal above says
// for certain that nothing was decided: unavailable, deadline-exceeded,
// door-unavailable, journal-reopened and failed, a lost link, the seat's own
// deadline and an answer not of its form are seatapp.ErrOutcomeUnknown, and
// any other code is a [*RefusedError], whose outcome the app treats as
// unknown too.
//
// # The gate's rate
//
// The gate takes [GateRate] requests a minute on the seat's one connection,
// pings included, and every browser's traffic shares it. The link counts
// every call it sends, by kind, over [BudgetWindow], and sends none past
// the room of its kind (see budget.go): anyone's (pairings and logins),
// reads, decisions, the watcher's waits and the interview's calls, beside
// the pings of the transport every [PingInterval]. A call without room is seatapp.ErrBusy and
// is not sent; the watcher alone sleeps for room instead.
// The rooms and the pings add up to GateRate, so the gate's rate never
// refuses a call the link sent. A decision's challenge is asked only with
// room for its seat.decide as well, kept for that one challenge until the
// decision is sent, the app drops the challenge
// ([seatapp.ChallengeDropper]), or the challenge's life ends ([HoldLife] at
// most): the seat.decide of a challenge the link drew, in its life, is never
// refused for room, whichever session or credential drew it. A login
// challenge is asked only with room for its seat.login as well, kept for it
// in the same way, so a login is never refused for room once the phone has
// answered; strangers can still use up anyone's room with login challenges.
//
// # The watcher
//
// [Daemon.StartWatch] starts the link's one goroutine, which watches the
// pending cards with seat.wait for the whole seat, never once for each
// browser, and only when the gate's hello lists "watch" ([CapWatch]).
// [Daemon.Watch] is a snapshot of what it last learned (the version, the
// count, the cards by id, kind, binding digest and held mark, whether the
// gate is out of reach, and when), and [Daemon.WatchChanged] a channel that
// closes when it changes, for the app to select on; neither touches the gate.
// The waits have a room of their own (waitRoom). See watch.go for the
// timings. [Daemon.Close] stops the goroutine.
//
// # The interview
//
// [Daemon.Genesis] implements [seatapp.Genesis], the guided new-project
// interview (seat.genesis), and only when the gate's hello lists "genesis"
// ([CapGenesis]); otherwise every call is [seatapp.ErrNotOffered] and nothing
// is sent, so the seat shows no "New project". The app's call is turned into
// [ownerwire.SeatGenesisParams] and held to the wire's form by its encoder
// before anything is sent: a thread id of another form is
// seatapp.ErrGenesisUnknownThread and an answer the wire does not take
// seatapp.ErrGenesisBadAnswer. Every call sent has the room of its kind
// (kindGenesis, 10 in [BudgetWindow]). The answer is read with
// [ownerwire.DecodeSeatGenesisResult], must be one the verb answers and of the
// thread called, and is turned into the app's values with every count and
// length checked again; an answer that does not pass is an error that holds
// nothing of it. A summary's rows are given their questions, for a change of
// the answer, from the question catalog this build holds
// (internal/genesis/catalog, v1), and only when the thread is walked under
// it: the digests are equal. The codes:
//
//	genesis-busy                                seatapp.ErrGenesisBusy
//	genesis-unknown-thread                      seatapp.ErrGenesisUnknownThread
//	genesis-bad-answer                          seatapp.ErrGenesisBadAnswer
//	genesis-over-cap                            seatapp.ErrGenesisOverCap
//	genesis-secret-shaped                       seatapp.ErrGenesisSecretShaped
//	genesis-done                                seatapp.ErrGenesisDone
//	genesis-stale-round                         seatapp.ErrGenesisStaleRound
//	rate-limited, busy, too-many-refusals       seatapp.ErrBusy
//	unknown-op                                  seatapp.ErrNotOffered
//
// and the rest as for the pairing. Nothing is retried. The link keeps a short
// memory of the answers (genesis.go): a list is answered from the last one for
// 20 seconds, and a round from the last answer of its thread for 10 seconds,
// so the card lists that reload and the page a post redirects to do not spend
// the room of the owner's taps; a call that changes threads forgets both
// before it is sent and again when it ends, and a read is kept only when no
// such call began or ended while it was asked. The memory holds nothing past its time but until the
// next call, and no log line of this package holds an answer.
package seatlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// CapPair is the capability of the seat.pair operation in a hello.
const CapPair = "pair"

// CapRead is the capability of the reads of the cards in a hello.
const CapRead = "read"

// codePairingRefused is the code the gate answers a pairing the daemon did
// not accept with: the code unknown, expired, used or out of tries.
const codePairingRefused = "pairing-refused"

// The app's copy of the word count and the wire's must be the same number:
// either difference below is a negative constant, which does not convert to
// uint, if they are not.
const (
	_ = uint(ownerwire.PairWords - seatapp.WordCount)
	_ = uint(seatapp.WordCount - ownerwire.PairWords)
)

// Caps is the capability list the seat says in its hello
// ([seatgate.ClientConfig].Caps), in a new slice.
func Caps() []string {
	return []string{CapPair, CapRead, CapLogin, CapDecide, CapWatch, CapGenesis, CapChat}
}

// errBadAnswer is the error of an answer that is not a pairing result of its
// form. It holds nothing of the answer.
var errBadAnswer = errors.New("seatlink: the gate's answer to the pairing is not of its form")

// errRegistration is the error of a request whose registration the wire does
// not take (authenticator data of another size, or a relying party id that is
// not a host name). Nothing is sent: the code is not spent on it.
var errRegistration = errors.New("seatlink: the registration is not of the wire's form")

// errFailed is the error of a call that failed for a reason the app does not
// tell apart. It holds nothing of the request; it wraps the transport's error,
// whose texts are fixed.
var errFailed = errors.New("seatlink: the call to the gate failed")

// RefusedError is a refusal of the gate with a code the app does not tell
// apart. Code is of the grammar of [seatgate.ValidCode]; Op is the operation
// refused, empty for the pairing.
type RefusedError struct {
	Op   string
	Code string
}

func (e *RefusedError) Error() string {
	what := "the pairing"
	if e.Op != "" && e.Op != ownerwire.OpSeatPair {
		what = e.Op
	}
	return "seatlink: the gate refused " + what + ": " + e.Code
}

// Daemon is the seat app's daemon port over the gate. It is safe for
// concurrent use, as its client is.
type Daemon struct {
	c *seatgate.Client
	// pin is the gate key's fingerprint, from which the link recomputes a
	// login challenge; empty when the link was made without it.
	pin string
	// reviews are the review digests of the cards the link showed.
	reviews reviews
	// shown are the cards the link showed, from which a decision is built.
	shown shownCards
	// budget counts the calls the link sends, by kind (see budget.go).
	budget budget
	// hub is the watcher's published state (see watch.go).
	hub watchHub
	// gmem is the link's short memory of the interview's answers, and
	// gnow its clock (see genesis.go).
	cmem chatMemory
	gmem genesisMemory
	gnow func() time.Time
	// wmu guards w and wclosed; wt are the watcher's timings, which tests
	// set before StartWatch.
	wmu     sync.Mutex
	w       *watcher
	wclosed bool
	wt      watchTimes
}

// The link keeps room for the decision of each challenge it draws, so it
// takes the app's word when a challenge is dropped.
var _ seatapp.ChallengeDropper = (*Daemon)(nil)

// Option sets an option of [New].
type Option func(*Daemon) error

// WithGatePin gives the link the gate key's fingerprint, the pin the seat's
// client holds (64 lower-case hex digits). The link recomputes each login
// challenge from it and the daemon's nonce; without it, no login is asked.
func WithGatePin(pin string) Option {
	return func(d *Daemon) error {
		if !seatgate.ValidFingerprint(pin) {
			return errors.New("seatlink: the gate pin is not a fingerprint")
		}
		d.pin = pin
		return nil
	}
}

// New returns the port over c. The caller runs c ([seatgate.Client.Run]) and
// closes it; until c is connected, every call is [seatapp.ErrDaemonUnavailable].
func New(c *seatgate.Client, opts ...Option) (*Daemon, error) {
	if c == nil {
		return nil, errors.New("seatlink: a client is required")
	}
	d := &Daemon{c: c, budget: budget{now: time.Now}, gnow: time.Now}
	for _, o := range opts {
		if err := o(d); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// Pair relays a pairing to the daemon. See the package comment for the
// errors.
func (d *Daemon) Pair(ctx context.Context, r seatapp.PairRequest) (seatapp.PairResult, error) {
	if ctx.Err() != nil {
		return seatapp.PairResult{}, seatapp.ErrDaemonUnavailable
	}
	bare := ownerwire.SeatPairParams{
		Code:         r.Code,
		CredentialID: r.CredentialID,
		PublicKey:    r.SPKI,
	}
	if _, err := ownerwire.EncodeSeatPairParams(bare); err != nil {
		// The app has checked the credential id's length and that the key
		// is a P-256 key, both inside the wire's bounds, so what the wire
		// refuses here is the code: one the daemon could not accept. It is
		// not sent, and the error holds nothing of it.
		return seatapp.PairResult{}, seatapp.ErrCodeRefused
	}
	// The registration goes with the credential, so that the daemon reads
	// the kind it claims; the claims the app read are not sent, since the
	// daemon reads them again itself. A request without a registration
	// pairs a credential of the kind "unverified".
	full := bare
	full.AuthenticatorData, full.RPID = r.AuthenticatorData, r.RPID
	params, err := ownerwire.EncodeSeatPairParams(full)
	if err != nil {
		return seatapp.PairResult{}, errRegistration
	}
	if !d.budget.take(kindPublic) {
		return seatapp.PairResult{}, seatapp.ErrBusy
	}
	var raw json.RawMessage
	if err := d.c.Call(ctx, ownerwire.OpSeatPair, json.RawMessage(params), &raw); err != nil {
		return seatapp.PairResult{}, mapCall(ctx, ownerwire.OpSeatPair, err, pairCodes)
	}
	res, err := ownerwire.DecodeSeatPairResult(raw)
	if err != nil || len(res.Words) != ownerwire.PairWords {
		return seatapp.PairResult{}, errBadAnswer
	}
	var out seatapp.PairResult
	copy(out.Words[:], res.Words)
	return out, nil
}

// pairCodes are the codes of the gate the pairing maps to the app's errors.
var pairCodes = map[string]error{codePairingRefused: seatapp.ErrCodeRefused}

// mapCall maps the error of a call of op to the errors the app tells apart:
// a code of the gate that codes names to its error, any other code to a
// [*RefusedError], and a lost link or an ended context to
// [seatapp.ErrDaemonUnavailable].
func mapCall(ctx context.Context, op string, err error, codes map[string]error) error {
	var re *seatgate.RemoteError
	switch {
	case errors.As(err, &re):
		if mapped, ok := codes[re.Code]; ok {
			return mapped
		}
		if !seatgate.ValidCode(re.Code) {
			return errFailed
		}
		return &RefusedError{Op: op, Code: re.Code}
	case errors.Is(err, seatgate.ErrDisconnected),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		ctx.Err() != nil:
		return seatapp.ErrDaemonUnavailable
	}
	return fmt.Errorf("%w: %w", errFailed, err)
}
