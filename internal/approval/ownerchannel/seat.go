package ownerchannel

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"ataniq/internal/approval/credkind"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/presence"
	"ataniq/internal/approval/record"
)

// This file is the seat gate's side of the service: a login and a decision
// from the owner's phone, each a WebAuthn assertion over a challenge the
// daemon draws and binds to its own record (docs/design/seat-gate.md, "The
// challenge", "Decide", "Login", "Presence").

// The errors of the seat's login and decision, found with errors.Is, beside
// [ErrChallengeStale], [ErrChallengesFull], [ErrKeyUnknown],
// [ErrKeysUnavailable] and [ErrNotSeat]. Each has a fixed text that holds
// nothing a caller sent; the gate answers each with the code of package
// ownerwire that names it.
var (
	// ErrAssertionInvalid says the assertion names another credential than
	// the challenge's, or did not verify, for any reason
	// (ownerwire.CodeAssertionInvalid). The error wraps the check that
	// failed, an error of package credkind or a fixed one of this package,
	// for the log; it never holds a byte of the assertion.
	ErrAssertionInvalid = errors.New("ownerchannel: the assertion was not accepted")
	// ErrKeyRevoked says the credential is on the list of deciding keys and
	// revoked (ownerwire.CodeKeyRevoked).
	ErrKeyRevoked = errors.New("ownerchannel: the credential was revoked")
	// ErrKindRefused says the credential's stored kind is not a kind the
	// verifier knows ("unverified" included), or not one the card's project
	// accepts (ownerwire.CodeKindRefused).
	ErrKindRefused = errors.New("ownerchannel: the credential's kind is not accepted")
	// ErrSeatParams says params of the seat gate are not of their form: an
	// envelope (a challenge id, an assertion's lengths) the gate should not
	// have passed on, which burns nothing, or a frame whose content is wrong,
	// found after the challenge was taken and burnt. The gate answers it
	// bad-params.
	ErrSeatParams = errors.New("ownerchannel: the params are not of their form")
	// errGate says the gate's own values are not of their form. It is a
	// mistake of the daemon, never of a seat.
	errGate = errors.New("ownerchannel: the gate's instance, origin or relying party id is not of its form")
	// errOtherCredential is the check that failed when the assertion names
	// another credential than the challenge's.
	errOtherCredential = errors.New("ownerchannel: the assertion names another credential than the challenge's")
)

// Gate is what the seat gate tells the service of itself: what a seat's
// challenges are bound to and its assertions are checked against. Every value
// is the daemon's own, never a seat's: the gate's key fingerprint from its
// identity, and the seat origin and relying party id the gate read once, from
// the owner-local file, when it started.
type Gate struct {
	// Instance is the gate's own key fingerprint, 64 lower-case hexadecimal
	// digits: the pin the seat holds. Every challenge is bound to it.
	Instance string
	// Origin is the seat origin in its canonical form, "https://" and a
	// lower-case host name with no port, and RPID its host, the relying
	// party id.
	Origin string
	RPID   string
}

// check holds the gate's values to their form: the instance's, and an origin
// the journal's evidence can hold (https:// and 1 to 253 lower-case letters,
// digits, dots and dashes) whose host is the relying party id.
func (g Gate) check() error {
	host, ok := strings.CutPrefix(g.Origin, "https://")
	if !ok || !record.IsLowerHex(g.Instance, ownerwire.InstanceLength) || host == "" || len(host) > 253 || host != g.RPID {
		return errGate
	}
	for i := 0; i < len(host); i++ {
		switch c := host[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-':
		default:
			return errGate
		}
	}
	return nil
}

// seatOnly refuses a source that is not a seat: its surface must be
// [SurfaceSeat], its session well formed, and it never the decider.
func seatOnly(src Source) error {
	if src.Surface != SurfaceSeat || src.Decider || !presence.ValidSession(src.Session) {
		return ErrNotSeat
	}
	return nil
}

// assertionShaped reports whether an assertion is within the bounds of
// ownerwire.Assertion, which the gate's decoder holds it to before it is
// passed on. The service asks again before it takes a challenge, so that a
// caller that skipped the decoder burns nothing with it.
func assertionShaped(a ownerwire.Assertion) bool {
	in := func(b []byte, min, max int) bool { return len(b) >= min && len(b) <= max }
	return in(a.CredentialID, ownerwire.MinCredentialIDBytes, ownerwire.MaxCredentialIDBytes) &&
		in(a.AuthenticatorData, ownerwire.MinAuthenticatorDataBytes, ownerwire.MaxAuthenticatorDataBytes) &&
		in(a.ClientDataJSON, 1, ownerwire.MaxClientDataBytes) &&
		in(a.Signature, 1, ownerwire.MaxSignatureBytes)
}

// credentialOf is a row of the list of keys as the verifier takes it.
func credentialOf(k ownerjournal.StoredKey) credkind.Credential {
	return credkind.Credential{
		Kind: k.Kind, CredentialID: k.CredentialID, SPKI: k.PublicKey, AAGUID: k.AAGUID,
		BackupEligible: k.BackupEligible, BackupState: k.BackupState && k.BackupEligible, SignCount: k.Counter,
	}
}

// inputOf is an assertion as the verifier takes it.
func inputOf(a ownerwire.Assertion) credkind.AssertionInput {
	return credkind.AssertionInput{
		CredentialID: a.CredentialID, ClientDataJSON: a.ClientDataJSON,
		AuthenticatorData: a.AuthenticatorData, Signature: a.Signature,
	}
}

// seatKey finds the credential with this display id on the list: one that is
// not on it is [ErrKeyUnknown], one that is revoked [ErrKeyRevoked], and a list
// that cannot be used [ErrKeysUnavailable].
func (s *Service) seatKey(id string) (ownerjournal.StoredKey, error) {
	_, keys, err := s.usableKeys()
	if err != nil {
		return ownerjournal.StoredKey{}, err
	}
	for _, k := range keys {
		if k.ID == id {
			if k.Revoked {
				return ownerjournal.StoredKey{}, ErrKeyRevoked
			}
			return k, nil
		}
	}
	return ownerjournal.StoredKey{}, ErrKeyUnknown
}

// policyOf is the kind policy of a project: the one [Config].KindPolicy gives,
// every kind when the service has none. ok is false for a project the policy
// does not know, which accepts no kind.
func (s *Service) policyOf(project string) (credkind.Policy, bool) {
	if s.cfg.KindPolicy == nil {
		return credkind.Policy{}, true
	}
	return s.cfg.KindPolicy(project)
}

// kindAccepted reports whether a stored kind is one the verifier knows and,
// for a project (a login has none), one the project accepts.
func (s *Service) kindAccepted(kind, project string) bool {
	if _, ok := credkind.ByName(kind); !ok {
		return false
	}
	if project == "" {
		return true
	}
	p, ok := s.policyOf(project)
	return ok && p.Accepts(kind)
}

// verified is what a verified assertion leaves: the counter it reported is
// raised on the list, and a counter that did not grow while one of the two is
// not zero is told as a possible clone. Neither refuses the assertion: a
// counter is a claim, and the highest one stays.
func (s *Service) verified(k ownerjournal.StoredKey, res credkind.Result, session string) {
	if res.CounterSuspect {
		s.notice("the passkey " + k.ID + " reported a sign counter that did not grow, through gate session " + session + ": it may have been copied; it was not refused")
	}
	if res.Counter > k.Counter {
		if err := s.cfg.Keys.RaiseCounter(k.ID, res.Counter, s.now()); err != nil {
			s.notice("the sign counter of the passkey " + k.ID + " could not be kept: " + keysErr(err).Error())
		}
	}
}

// SeatLoginChallenge draws a login challenge for the seat's gate session
// (seat.login-challenge): 32 random bytes of nonce and the challenge
// ownerwire.LoginChallenge of the gate's instance and the nonce, kept 5
// minutes for one use by that session, and the raw ids of the credentials
// that may log in: on the list, not revoked, of a kind the verifier knows,
// newest first, at most [ownerwire.MaxLoginAllow]. It is never refused for
// room: the oldest login challenge is dropped when [MaxLoginChallenges]
// remain. The source must be a seat ([ErrNotSeat]); a list of keys that
// cannot be used is [ErrKeysUnavailable].
func (s *Service) SeatLoginChallenge(ctx context.Context, src Source, g Gate) (ownerwire.SeatLoginChallengeResult, error) {
	_, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.SeatLoginChallengeResult{}, err
	}
	defer done()
	if err := seatOnly(src); err != nil {
		return ownerwire.SeatLoginChallengeResult{}, err
	}
	if err := g.check(); err != nil {
		return ownerwire.SeatLoginChallengeResult{}, err
	}
	_, keys, err := s.usableKeys()
	if err != nil {
		return ownerwire.SeatLoginChallengeResult{}, err
	}
	allow := [][]byte{}
	for i := len(keys) - 1; i >= 0 && len(allow) < ownerwire.MaxLoginAllow; i-- {
		if k := keys[i]; !k.Revoked && s.kindAccepted(k.Kind, "") {
			allow = append(allow, bytes.Clone(k.CredentialID))
		}
	}
	nonce, err := s.seat.drawNonce(ownerwire.NonceBytes)
	if err != nil {
		return ownerwire.SeatLoginChallengeResult{}, err
	}
	ch, err := ownerwire.LoginChallenge(g.Instance, nonce)
	if err != nil {
		return ownerwire.SeatLoginChallengeResult{}, err
	}
	raw, wall := s.nowBoth()
	e := &challengeEntry{kind: challengeLogin, session: src.Session, nonce: nonce, challenge: ch,
		ends: raw.Add(ChallengeLife), endsWall: wall.Add(ChallengeLife)}
	if err := s.seat.add(e, raw); err != nil {
		return ownerwire.SeatLoginChallengeResult{}, err
	}
	return ownerwire.SeatLoginChallengeResult{
		ChallengeID: e.id, Challenge: bytes.Clone(ch), Nonce: bytes.Clone(nonce),
		ExpiresAt: record.FormatTime(e.endsWall), Allow: allow,
	}, nil
}

// SeatLogin takes an assertion over a login challenge (seat.login) and
// answers the credential that made it. It decides nothing, and the daemon
// keeps no session. In order: the source is a seat ([ErrNotSeat]) and the
// params' envelope of its form ([ErrSeatParams], nothing burnt); the entry of
// this id, of the login kind and of the source's session is taken and burnt
// ([ErrChallengeStale] for none or an expired one); the credential is found by
// the assertion's credential id, byte for byte, on the list ([ErrKeyUnknown],
// [ErrKeyRevoked]), of a kind the verifier knows ([ErrKindRefused]: a login
// belongs to no project, so no project's policy is asked); and the assertion
// verifies against it with the entry's challenge and the gate's origin and
// relying party id ([ErrAssertionInvalid]). The verified counter is kept.
func (s *Service) SeatLogin(ctx context.Context, src Source, g Gate, p ownerwire.SeatLoginParams) (ownerwire.SeatLoginResult, error) {
	_, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.SeatLoginResult{}, err
	}
	defer done()
	if err := seatOnly(src); err != nil {
		return ownerwire.SeatLoginResult{}, err
	}
	if err := g.check(); err != nil {
		return ownerwire.SeatLoginResult{}, err
	}
	if !record.IsLowerHex(p.ChallengeID, ownerwire.ChallengeIDLength) || !assertionShaped(p.Assertion) {
		return ownerwire.SeatLoginResult{}, ErrSeatParams
	}
	raw, _ := s.nowBoth()
	e, err := s.seat.take(p.ChallengeID, challengeLogin, src.Session, raw)
	if err != nil {
		return ownerwire.SeatLoginResult{}, err
	}
	k, err := s.seatKey(ownerjournal.KeyDisplayID(p.Assertion.CredentialID))
	if err != nil {
		return ownerwire.SeatLoginResult{}, err
	}
	if !bytes.Equal(k.CredentialID, p.Assertion.CredentialID) {
		return ownerwire.SeatLoginResult{}, ErrKeyUnknown
	}
	if !s.kindAccepted(k.Kind, "") {
		return ownerwire.SeatLoginResult{}, ErrKindRefused
	}
	res, err := credkind.Verify(credentialOf(k), inputOf(p.Assertion), credkind.Expect{Challenge: e.challenge, Origin: g.Origin, RPID: g.RPID})
	if err != nil {
		return ownerwire.SeatLoginResult{}, fmt.Errorf("%w: %w", ErrAssertionInvalid, err)
	}
	s.verified(k, res, src.Session)
	return ownerwire.SeatLoginResult{Credential: k.ID, CredentialID: bytes.Clone(k.CredentialID), Kind: k.Kind}, nil
}

// SeatChallenge draws the challenge for one decision of the owner on the
// phone (seat.challenge). It checks, in order: the source is a seat
// ([ErrNotSeat]) and the params of their form ([ErrSeatParams]); the pending
// record exists and is pending (the refusals of [Service.Decide]); the journal
// holds no decision of the card on its way to the door (decision-invalid); the
// credential named is on the list ([ErrKeyUnknown]), not revoked
// ([ErrKeyRevoked]), and of a kind the verifier knows and the card's project
// accepts ([ErrKindRefused]); the frame's binding digest is that of the
// daemon's own record (binding-mismatch); and its sign-offs are the record's
// flagged list for an approve, and none for a deny (decision-invalid). So the
// phone is never asked to sign what would be refused. None of these refusals
// is journaled: no decision was made.
//
// The challenge is ownerwire.DecisionChallenge of the gate's instance, of a
// frame the service builds from its own record (the pending id, the binding
// digest and the flagged list) with the decision and the reason the owner
// chose, and of 32 bytes of nonce from the random source. It is kept 5
// minutes for one use by the source's gate session, with the credential, the
// card and its project; a challenge of the same session, credential and card
// is replaced, and a 33rd outstanding one is refused [ErrChallengesFull].
func (s *Service) SeatChallenge(ctx context.Context, src Source, g Gate, p ownerwire.SeatChallengeParams) (ownerwire.SeatChallengeResult, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.SeatChallengeResult{}, err
	}
	defer done()
	if err := seatOnly(src); err != nil {
		return ownerwire.SeatChallengeResult{}, err
	}
	if err := g.check(); err != nil {
		return ownerwire.SeatChallengeResult{}, err
	}
	if _, err := ownerwire.EncodeSeatChallengeParams(p); err != nil {
		return ownerwire.SeatChallengeResult{}, ErrSeatParams
	}
	f := p.Frame
	shown, pending, err := s.lookup(ctx, f.ID)
	switch {
	case err == nil:
	case isVoid(err):
		return ownerwire.SeatChallengeResult{}, refuse(record.CodeDecisionInvalid, "no pending record has this id")
	case isRefusal(err):
		return ownerwire.SeatChallengeResult{}, err
	default:
		return ownerwire.SeatChallengeResult{}, wrapDoor(err)
	}
	if shown.Entry.State != record.StatePending {
		return ownerwire.SeatChallengeResult{}, endedRefusal(shown.Entry)
	}
	if err := s.syncIndex(ctx); err != nil {
		return ownerwire.SeatChallengeResult{}, s.jfix(ctx, err)
	}
	if r, ok := s.indexed(pending.ID); ok && r.decided && !r.delivered {
		return ownerwire.SeatChallengeResult{}, refuse(record.CodeDecisionInvalid, "a decision for this card is already on its way to the door")
	}
	k, err := s.seatKey(p.Credential)
	if err != nil {
		return ownerwire.SeatChallengeResult{}, err
	}
	project := pending.Binding.Project
	if !s.kindAccepted(k.Kind, project) {
		return ownerwire.SeatChallengeResult{}, ErrKindRefused
	}
	if f.BindingSHA256 != pending.BindingSHA256 {
		return ownerwire.SeatChallengeResult{}, refuse(record.CodeBindingMismatch, "the digest is not the digest of the binding the record has")
	}
	// The frame the phone signs is the daemon's own: the record's id,
	// binding and flagged list, with the owner's decision and reason.
	own := ownerwire.DecisionFrame{ID: pending.ID, BindingSHA256: pending.BindingSHA256, Decision: f.Decision, SignedOff: []record.SignOff{}, Reason: f.Reason}
	if f.Decision == record.DecisionApprove {
		var flagged []record.Flag
		if sum := pending.Binding.Summary; sum != nil {
			flagged = sum.Flagged
		}
		if !record.SignOffsEqual(flagged, f.SignedOff) {
			return ownerwire.SeatChallengeResult{}, refuse(record.CodeDecisionInvalid, "the sign-offs are not the record's flagged list")
		}
		for _, fl := range flagged {
			own.SignedOff = append(own.SignedOff, record.SignOff{Kind: fl.Kind, Path: fl.Path})
		}
	} else if len(f.SignedOff) != 0 {
		return ownerwire.SeatChallengeResult{}, refuse(record.CodeDecisionInvalid, "a deny has no sign-off")
	}
	nonce, err := s.seat.drawNonce(ownerwire.NonceBytes)
	if err != nil {
		return ownerwire.SeatChallengeResult{}, err
	}
	ch, err := ownerwire.DecisionChallenge(g.Instance, own, nonce)
	if err != nil {
		return ownerwire.SeatChallengeResult{}, refuse(record.CodeDecisionInvalid, detailOf(err))
	}
	raw, wall := s.nowBoth()
	e := &challengeEntry{kind: challengeDecision, session: src.Session, nonce: nonce, challenge: ch,
		ends: raw.Add(ChallengeLife), endsWall: wall.Add(ChallengeLife),
		credential: k.ID, pendingID: pending.ID, project: project}
	if err := s.seat.add(e, raw); err != nil {
		return ownerwire.SeatChallengeResult{}, err
	}
	return ownerwire.SeatChallengeResult{
		ChallengeID: e.id, Challenge: bytes.Clone(ch), Nonce: bytes.Clone(nonce), ExpiresAt: record.FormatTime(e.endsWall),
	}, nil
}

// SeatDecide takes the owner's decision with an assertion over its challenge
// (seat.decide). It checks, in this order:
//
//  1. the source is a seat ([ErrNotSeat]), and the params' envelope (the
//     challenge id, the assertion's lengths) of its form ([ErrSeatParams],
//     nothing burnt);
//  2. the entry of this id, of the decision kind and of the source's gate
//     session is taken out of the store in one step and burnt, whatever
//     happens next: none, or an expired one, is [ErrChallengeStale]; an entry
//     of another session or kind is not found and not burnt;
//  3. the frame's content is of its form ([ErrSeatParams]);
//  4. the assertion names the entry's credential, byte for byte
//     ([ErrAssertionInvalid]), which is on the list ([ErrKeyUnknown]), not
//     revoked ([ErrKeyRevoked]), of a kind the verifier knows and the entry's
//     project accepts ([ErrKindRefused]);
//  5. the challenge recomputed from the frame as sent, the gate's instance and
//     the entry's nonce is the entry's, compared in constant time
//     ([ErrChallengeStale]): the frame cannot change between challenge and
//     decide;
//  6. the assertion verifies, with the project's policy, against the stored
//     credential, the entry's challenge and the gate's origin and relying
//     party id ([ErrAssertionInvalid]). User verification is required. The
//     counter it reported is raised, and one that did not grow is told as a
//     possible clone, not refused;
//  7. from here the path is the desk's ([Service.Decide]), under the lock
//     that serialises decisions: the record's state, the binding digest
//     against the daemon's own record, the sign-offs, and the day's limit with
//     "held". Then, under the lock that revocation takes, the key is checked
//     again ([ErrKeyRevoked], [ErrKeyUnknown]: nothing is appended) and the
//     decision or the held decision is appended. A revoke that returned before
//     the append wins; a decision appended first stands. Then the door.
//
// The verified assertion stands in for the desk's presence window for this
// one decision, and for the decider's place, which a seat never holds: it is
// not activity at the desk, and nothing else a seat sends counts as presence.
// The record is confirmed by passkey with a seat block (the assertion's
// digest, the challenge id, the credential's display id and the gate
// session), and the journal keeps the evidence of the assertion beside it,
// with the nonce, the instance and the origin. A deny needs the assertion as
// an approve does, carries no sign-off, is never held and uses nothing. An
// approve past the day's limit is held and uses nothing. Refusals of steps 1
// to 6 are not journaled; after step 6 a refusal the journal has a kind for
// is journaled as refused with the surface seat, the gate session and no
// desk activity.
func (s *Service) SeatDecide(ctx context.Context, src Source, g Gate, p ownerwire.SeatDecideParams) (ownerwire.DecideResult, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.DecideResult{}, err
	}
	defer done()
	// 1.
	if err := seatOnly(src); err != nil {
		return ownerwire.DecideResult{}, err
	}
	if err := g.check(); err != nil {
		return ownerwire.DecideResult{}, err
	}
	if !record.IsLowerHex(p.ChallengeID, ownerwire.ChallengeIDLength) || !assertionShaped(p.Assertion) {
		return ownerwire.DecideResult{}, ErrSeatParams
	}
	// 2.
	raw, _ := s.nowBoth()
	e, err := s.seat.take(p.ChallengeID, challengeDecision, src.Session, raw)
	if err != nil {
		return ownerwire.DecideResult{}, err
	}
	// 3.
	f := p.Frame
	if err := ownerwire.CheckDecisionFrame(f); err != nil {
		return ownerwire.DecideResult{}, ErrSeatParams
	}
	// 4.
	if ownerjournal.KeyDisplayID(p.Assertion.CredentialID) != e.credential {
		return ownerwire.DecideResult{}, fmt.Errorf("%w: %w", ErrAssertionInvalid, errOtherCredential)
	}
	k, err := s.seatKey(e.credential)
	if err != nil {
		return ownerwire.DecideResult{}, err
	}
	if !bytes.Equal(k.CredentialID, p.Assertion.CredentialID) {
		return ownerwire.DecideResult{}, fmt.Errorf("%w: %w", ErrAssertionInvalid, errOtherCredential)
	}
	policy, ok := s.policyOf(e.project)
	if _, known := credkind.ByName(k.Kind); !known || !ok || !policy.Accepts(k.Kind) {
		return ownerwire.DecideResult{}, ErrKindRefused
	}
	// 5.
	ch, err := ownerwire.DecisionChallenge(g.Instance, f, e.nonce)
	if err != nil || subtle.ConstantTimeCompare(ch, e.challenge) != 1 {
		return ownerwire.DecideResult{}, ErrChallengeStale
	}
	// 6.
	res, err := policy.Verify(credentialOf(k), inputOf(p.Assertion), credkind.Expect{Challenge: e.challenge, Origin: g.Origin, RPID: g.RPID})
	switch {
	case errors.Is(err, credkind.ErrNotAccepted), errors.Is(err, credkind.ErrKind):
		return ownerwire.DecideResult{}, ErrKindRefused
	case err != nil:
		return ownerwire.DecideResult{}, fmt.Errorf("%w: %w", ErrAssertionInvalid, err)
	}
	s.verified(k, res, src.Session)
	if err := s.fault("seat-verified"); err != nil {
		return ownerwire.DecideResult{}, err
	}

	// 7.
	s.dmu.Lock()
	defer s.dmu.Unlock()
	now := s.now()
	r := &refusal{s: s, src: src, id: f.ID, now: now}
	shown, pending, perr := s.lookup(ctx, f.ID)
	switch {
	case perr == nil:
		r.project = pending.Binding.Project
	case isVoid(perr):
	default:
		return ownerwire.DecideResult{}, wrapDoor(perr)
	}
	evidence := &ownerjournal.SeatEvidence{
		CredentialID: bytes.Clone(p.Assertion.CredentialID), AuthenticatorData: bytes.Clone(p.Assertion.AuthenticatorData),
		ClientDataJSON: bytes.Clone(p.Assertion.ClientDataJSON), Signature: bytes.Clone(p.Assertion.Signature),
		Nonce: bytes.Clone(e.nonce), Instance: g.Instance, Origin: g.Origin,
	}
	return s.decideAuthed(ctx, r, f, shown, pending, perr, confirmation{
		fill: func(rec *record.Decision) {
			rec.Confirmed = record.ConfirmedPasskey
			rec.Seat = record.SeatBlock{
				AssertionSHA256: p.Assertion.Digest(), ChallengeID: e.id,
				Credential: e.credential, Session: src.Session,
			}
		},
		seen: ownerjournal.Seen{Evidence: evidence},
		recheck: func() error {
			_, err := s.seatKey(e.credential)
			return err
		},
	})
}
