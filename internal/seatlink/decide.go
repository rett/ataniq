package seatlink

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// The decision operations of the gate and their capability, as the contract
// (docs/design/seat-gate.md, "The challenge" and "Decide") names them. The
// names are package ownerwire's; the capability is the gate's word for them.
const (
	// CapDecide is the capability of the two decision operations.
	CapDecide = "decide"
	// OpDecideChallenge asks the daemon for a decision's challenge:
	// ownerwire.OpSeatChallenge.
	OpDecideChallenge = ownerwire.OpSeatChallenge
	// OpDecide hands the daemon a decision and the assertion over its
	// challenge: ownerwire.OpSeatDecide.
	OpDecide = ownerwire.OpSeatDecide
)

// The codes of the gate a decision tells apart that are not words of
// package ownerwire or of package seatgate.
const (
	codeBindingMismatch  = "binding-mismatch"
	codeApprovalExpired  = "approval-expired"
	codeApprovalConsumed = "approval-consumed"
	codePreviousEpoch    = "previous-epoch"
	codeDecisionInvalid  = "decision-invalid"
	codeUnavailable      = "unavailable"
	codeDoorUnavailable  = "door-unavailable"
	codeJournalReopened  = "journal-reopened"
)

// refusalCodes are the codes of the gate that refuse a decision for certain,
// on seat.challenge and seat.decide alike, with the app's error for each.
// After any of them nothing was decided.
var refusalCodes = map[string]error{
	ownerwire.CodeChallengeStale:   seatapp.ErrChallengeStale,
	codeBindingMismatch:            seatapp.ErrCardChanged,
	codeReviewChanged:              seatapp.ErrCardChanged,
	codeApprovalExpired:            seatapp.ErrCardExpired,
	codeApprovalConsumed:           seatapp.ErrCannotDecide,
	codeApprovalVoid:               seatapp.ErrCannotDecide,
	codePreviousEpoch:              seatapp.ErrCannotDecide,
	codeDecisionInvalid:            seatapp.ErrCannotDecide,
	ownerwire.CodeAssertionInvalid: seatapp.ErrAssertionRefused,
	ownerwire.CodeKeyRevoked:       seatapp.ErrKeyRevoked,
	codeKeyUnknown:                 seatapp.ErrKeyRevoked,
	ownerwire.CodeKindRefused:      seatapp.ErrKindRefused,
	ownerwire.CodeChallengesFull:   seatapp.ErrBusy,
	seatgate.CodeRateLimited:       seatapp.ErrBusy,
	seatgate.CodeBusy:              seatapp.ErrBusy,
	codeTooManyRefusals:            seatapp.ErrBusy,
	codeUnknownOp:                  seatapp.ErrNotOffered,
}

// unknownCodes are the codes of the gate that, as an answer to seat.decide,
// leave the outcome unknown: the decision may be journaled and on its way to
// the door, or not (docs/design/seat-gate.md, "The phone's side").
var unknownCodes = map[string]bool{
	codeUnavailable:       true,
	seatgate.CodeDeadline: true,
	codeDoorUnavailable:   true,
	codeJournalReopened:   true,
	seatgate.CodeFailed:   true,
}

// The errors of the decision calls the app does not tell apart. None holds
// anything of the request or of the answer.
var (
	// errDecisionAnswer is the error of an answer to seat.challenge that is
	// not of its form.
	errDecisionAnswer = errors.New("seatlink: the gate's answer to the decision's challenge is not of its form")
	// errSignOff is the error of a sign-off index that names no flagged path
	// of the card, or names one twice, or of a deny with sign-offs. Nothing
	// is sent.
	errSignOff = errors.New("seatlink: the sign-offs do not name the card's flagged paths")
	// errFrame is the error of a decision the wire's encoder refuses.
	// Nothing is sent.
	errFrame = errors.New("seatlink: the decision is not of the wire's form")
	// errCredential is the error of a session's credential that is not a
	// display id. Nothing is sent.
	errCredential = errors.New("seatlink: the credential is not a display id")
)

// shownCards remembers the card the link last built for each id: the card a
// Card page showed. A decision is built from it, never from what a browser
// sent. A pending record does not change, so neither does its binding; only
// what came after it does, and the daemon judges that again.
type shownCards struct {
	mu sync.Mutex
	m  map[string]card.Card
}

func (s *shownCards) get(id string) (card.Card, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[id]
	return c, ok
}

func (s *shownCards) put(id string, c card.Card) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil || len(s.m) >= maxReviews {
		s.m = map[string]card.Card{}
	}
	s.m[id] = c
}

// readingOf is the card model's reading of a card as one session read it on
// the phone. The Card page holds the card whole, so it counts as the rows,
// the four lists, the refs and the "differs from" block read to their end;
// a cut path list counts only by the pages of the whole list the session
// read, for the card's own review.
func readingOf(c card.Card, r seatapp.Reading) card.Reading {
	rd := card.Reading{Opened: r.Opened, Paths: true, Refs: true, Differs: true}
	for k := range rd.Lists {
		rd.Lists[k] = true
	}
	if c.Diff != nil {
		for _, n := range r.PagesRead {
			rd.ReadPage(card.Page{Kind: card.PagePaths, N: n, Pages: r.Pages, ReviewDigest: c.Diff.ReviewDigest})
		}
	}
	return rd
}

// reasonNotShown is the host's line for a card the link did not show at the
// binding asked.
const reasonNotShown = "the card changed; look at it again"

// Offer says what the Card page may offer for a card the link showed at the
// binding given, as a session read it: an approve when the card model's
// MayApprove allows it with every flagged path signed off (the sign-offs are
// the owner's to tick on the page; a card that opened under 400 ms ago is
// offered, since the tap comes later and is judged then), and a deny when
// its Deny allows one with no reason. It asks the gate nothing.
func (d *Daemon) Offer(id, binding string, r seatapp.Reading, now time.Time) seatapp.Offer {
	c, ok := d.shown.get(id)
	if !ok || c.BindingSHA256 != binding || binding == "" {
		return seatapp.Offer{ApproveWhy: reasonNotShown, DenyWhy: reasonNotShown}
	}
	var out seatapp.Offer
	rd := readingOf(c, r)
	for _, f := range c.Flagged {
		rd.SignedOff = append(rd.SignedOff, f.SignOff())
	}
	out.Approve, out.ApproveWhy = c.MayApprove(rd, now)
	if !out.Approve && out.ApproveWhy == card.ReasonJustOpened {
		out.Approve, out.ApproveWhy = true, ""
	}
	_, out.Deny, out.DenyWhy = c.Deny("", now)
	return out
}

// decidable checks what both decision calls need before anything is sent: a
// context that has not ended, a gate pin, a connected gate that lists
// "decide".
func (d *Daemon) decidable(ctx context.Context) error {
	if ctx.Err() != nil {
		return seatapp.ErrDaemonUnavailable
	}
	if d.pin == "" {
		return errNoPin
	}
	if _, ok := d.c.Connected(); !ok {
		return seatapp.ErrDaemonUnavailable
	}
	if !d.offers(CapDecide) {
		return seatapp.ErrNotOffered
	}
	return nil
}

// frameOf is the decision the card model gives for the request, from the
// card the link showed: approve through Card.Approve (MayApprove, with the
// session's reading and the sign-offs named), deny through Card.Deny.
func frameOf(c card.Card, r seatapp.DecideChallengeRequest) (ownerwire.DecisionFrame, error) {
	rd := readingOf(c, r.Reading)
	if !r.Approve {
		if len(r.SignedOff) != 0 {
			return ownerwire.DecisionFrame{}, errSignOff
		}
		dec, ok, why := c.Deny(r.Reason, r.Now)
		if !ok {
			return ownerwire.DecisionFrame{}, &seatapp.NotAllowedError{Reason: why}
		}
		return ownerwire.DecisionFrame{ID: dec.ID, BindingSHA256: dec.BindingSHA256, Decision: record.DecisionDeny,
			SignedOff: []record.SignOff{}, Reason: dec.Reason}, nil
	}
	if r.Reason != "" {
		return ownerwire.DecisionFrame{}, errFrame
	}
	seen := make(map[int]bool, len(r.SignedOff))
	for _, i := range r.SignedOff {
		if i < 0 || i >= len(c.Flagged) || seen[i] {
			return ownerwire.DecisionFrame{}, errSignOff
		}
		seen[i] = true
		rd.SignedOff = append(rd.SignedOff, c.Flagged[i].SignOff())
	}
	dec, ok, why := c.Approve(rd, r.Now)
	if !ok {
		return ownerwire.DecisionFrame{}, &seatapp.NotAllowedError{Reason: why}
	}
	signed := append([]record.SignOff{}, dec.SignedOff...)
	return ownerwire.DecisionFrame{ID: dec.ID, BindingSHA256: dec.BindingSHA256, Decision: record.DecisionApprove, SignedOff: signed}, nil
}

// frameView is the frame as the app keeps it.
func frameView(f ownerwire.DecisionFrame) seatapp.DecisionFrame {
	out := seatapp.DecisionFrame{ID: f.ID, BindingSHA256: f.BindingSHA256, Approve: f.Decision == record.DecisionApprove, Reason: f.Reason}
	for _, s := range f.SignedOff {
		out.SignedOff = append(out.SignedOff, seatapp.SignOff{Kind: s.Kind, Path: string(s.Path)})
	}
	return out
}

// wireFrame is the frame the app kept, in the wire's words.
func wireFrame(f seatapp.DecisionFrame) ownerwire.DecisionFrame {
	out := ownerwire.DecisionFrame{ID: f.ID, BindingSHA256: f.BindingSHA256, Decision: record.DecisionDeny, Reason: f.Reason, SignedOff: []record.SignOff{}}
	if f.Approve {
		out.Decision = record.DecisionApprove
	}
	for _, s := range f.SignedOff {
		out.SignedOff = append(out.SignedOff, record.SignOff{Kind: s.Kind, Path: record.Text(s.Path)})
	}
	return out
}

// DecideChallenge freezes the decision the owner tapped, from the card the
// link showed at the binding the Card page showed, and asks the daemon for
// its challenge (seat.challenge). Before it returns the challenge it
// recomputes it from the gate's pin, the frozen frame and the daemon's
// nonce, with the one function the daemon calls too
// (ownerwire.DecisionChallenge), and compares the two in constant time: a
// challenge that differs is [seatapp.ErrChallengeDiffers], and the phone is
// asked nothing. A card the link did not show at that binding is
// [seatapp.ErrCardChanged]; a decision the card model does not allow (an
// approve with a flagged path not signed off, a card past its expiry) is a
// [*seatapp.NotAllowedError]. Neither is sent.
func (d *Daemon) DecideChallenge(ctx context.Context, r seatapp.DecideChallengeRequest) (seatapp.DecideChallenge, error) {
	if err := d.decidable(ctx); err != nil {
		return seatapp.DecideChallenge{}, err
	}
	c, ok := d.shown.get(r.ID)
	if !ok || r.BindingSHA256 == "" || c.BindingSHA256 != r.BindingSHA256 {
		return seatapp.DecideChallenge{}, seatapp.ErrCardChanged
	}
	if !record.IsLowerHex(r.Credential, ownerwire.KeyIDLength) {
		return seatapp.DecideChallenge{}, errCredential
	}
	frame, err := frameOf(c, r)
	if err != nil {
		return seatapp.DecideChallenge{}, err
	}
	params, err := ownerwire.EncodeSeatChallengeParams(ownerwire.SeatChallengeParams{Frame: frame, Credential: r.Credential})
	if err != nil {
		return seatapp.DecideChallenge{}, errFrame
	}
	var raw json.RawMessage
	if err := d.c.Call(ctx, OpDecideChallenge, json.RawMessage(params), &raw); err != nil {
		return seatapp.DecideChallenge{}, mapCall(ctx, OpDecideChallenge, err, refusalCodes)
	}
	var probe ownerwire.SeatChallengeResult
	if seatgate.DecodeStrict(raw, &probe) != nil {
		return seatapp.DecideChallenge{}, errDecisionAnswer
	}
	res, err := ownerwire.DecodeSeatChallengeResult(raw)
	if err != nil {
		return seatapp.DecideChallenge{}, errDecisionAnswer
	}
	expires, err := record.ParseTime(res.ExpiresAt)
	if err != nil {
		return seatapp.DecideChallenge{}, errDecisionAnswer
	}
	want, err := ownerwire.DecisionChallenge(d.pin, frame, res.Nonce)
	if err != nil || subtle.ConstantTimeCompare(want, res.Challenge) != 1 {
		return seatapp.DecideChallenge{}, seatapp.ErrChallengeDiffers
	}
	return seatapp.DecideChallenge{ID: res.ChallengeID, Challenge: want, Expires: expires, Frame: frameView(frame)}, nil
}

// Decide hands the daemon the frame the app kept for a challenge and the
// phone's assertion over it (seat.decide), and returns the daemon's outcome.
// Before anything is sent, a gate that is not connected is
// [seatapp.ErrDaemonUnavailable]. Once it is sent, only a refusal says for
// certain that nothing was decided: a refusal code maps as for the
// challenge; the codes unavailable, deadline-exceeded, door-unavailable,
// journal-reopened and failed, a lost link, the seat's own deadline, and an
// answer not of its form are [seatapp.ErrOutcomeUnknown]; any other code is
// a [*RefusedError], whose outcome the app treats as unknown too. Nothing is
// retried.
func (d *Daemon) Decide(ctx context.Context, r seatapp.DecideRequest) (seatapp.DecideResult, error) {
	if err := d.decidable(ctx); err != nil {
		return seatapp.DecideResult{}, err
	}
	params, err := ownerwire.EncodeSeatDecideParams(ownerwire.SeatDecideParams{
		ChallengeID: r.ChallengeID,
		Frame:       wireFrame(r.Frame),
		Assertion: ownerwire.Assertion{
			CredentialID: r.CredentialID, AuthenticatorData: r.AuthenticatorData,
			ClientDataJSON: r.ClientDataJSON, Signature: r.Signature,
		},
	})
	if err != nil || ownerwire.CheckDecisionFrame(wireFrame(r.Frame)) != nil {
		return seatapp.DecideResult{}, errFrame
	}
	var raw json.RawMessage
	if err := d.c.Call(ctx, OpDecide, json.RawMessage(params), &raw); err != nil {
		return seatapp.DecideResult{}, mapDecide(err)
	}
	var probe ownerwire.DecideResult
	if seatgate.DecodeStrict(raw, &probe) != nil {
		return seatapp.DecideResult{}, seatapp.ErrOutcomeUnknown
	}
	res, err := ownerwire.DecodeDecideResult(raw)
	if err != nil {
		return seatapp.DecideResult{}, seatapp.ErrOutcomeUnknown
	}
	return seatapp.DecideResult{State: string(res.State), Line: res.Line, Held: res.Held}, nil
}

// mapDecide maps the error of a seat.decide that was sent: a refusal to its
// error, a code that leaves the outcome unknown, a lost link and an ended
// context to [seatapp.ErrOutcomeUnknown], and any other code to a
// [*RefusedError].
func mapDecide(err error) error {
	var re *seatgate.RemoteError
	if errors.As(err, &re) {
		if mapped, ok := refusalCodes[re.Code]; ok {
			return mapped
		}
		if unknownCodes[re.Code] || !seatgate.ValidCode(re.Code) {
			return seatapp.ErrOutcomeUnknown
		}
		return &RefusedError{Op: OpDecide, Code: re.Code}
	}
	return seatapp.ErrOutcomeUnknown
}
