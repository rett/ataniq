package ownerchannel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/credkind"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/webauthntest"
)

// The seat gate as the tests know it: its key fingerprint and the seat
// origin it read at start, and the gate session of the pinned seat.
var (
	testGate = Gate{
		Instance: strings.Repeat("0123456789abcdef", 4),
		Origin:   "https://seat.example",
		RPID:     "seat.example",
	}
	gateSrc  = Source{Surface: SurfaceSeat, Session: "ab12ab12ab12ab12"}
	otherSrc = Source{Surface: SurfaceSeat, Session: "cd34cd34cd34cd34"}
)

// seatWorld is an env with a list of keys and a phone whose passkey is on it.
type seatWorld struct {
	*env
	keys  *fakeKeys
	phone *webauthntest.Authenticator
	key   ownerjournal.StoredKey
	// policy is the kind policy of project-a; nil accepts every kind.
	policyMu sync.Mutex
	policy   []string
}

func newSeatWorld(t *testing.T) *seatWorld {
	t.Helper()
	w := &seatWorld{env: newEnv(t), keys: newFakeKeys()}
	w.env.keys = w.keys
	w.phone, w.key = w.pairPhone(nil)
	w.start()
	return w
}

// start makes a service with the world's kind policy.
func (w *seatWorld) start() {
	w.t.Helper()
	st, err := ownerjournal.Open(w.path)
	if err != nil {
		w.t.Fatal(err)
	}
	cfg := w.config(st)
	cfg.KindPolicy = func(project string) (credkind.Policy, bool) {
		w.policyMu.Lock()
		defer w.policyMu.Unlock()
		if project != "project-a" {
			return credkind.Policy{}, false
		}
		return credkind.Policy{Accepted: append([]string(nil), w.policy...)}, true
	}
	svc, err := New(context.Background(), cfg)
	if err != nil {
		_ = st.Close()
		w.t.Fatalf("New: %v", err)
	}
	w.svc = svc
	w.t.Cleanup(func() { _ = svc.Close() })
}

func (w *seatWorld) restart() {
	w.t.Helper()
	w.stop()
	w.start()
}

func (w *seatWorld) setPolicy(kinds ...string) {
	w.policyMu.Lock()
	defer w.policyMu.Unlock()
	w.policy = kinds
}

// pairPhone makes a passkey for the seat origin and puts it on the list as
// the desk's confirm does: registered by credkind from its registration.
// change, when set, changes the authenticator first.
func (w *seatWorld) pairPhone(change func(*webauthntest.Authenticator)) (*webauthntest.Authenticator, ownerjournal.StoredKey) {
	w.t.Helper()
	a, err := webauthntest.New(testGate.RPID, testGate.Origin)
	if err != nil {
		w.t.Fatal(err)
	}
	if change != nil {
		change(a)
	}
	reg := a.Create(bytes.Repeat([]byte{1}, 32))
	c, err := credkind.Register(credkind.RegistrationInput{RPID: testGate.RPID, CredentialID: reg.CredentialID, SPKI: reg.PublicKey, AuthenticatorData: reg.AuthenticatorData})
	if err != nil {
		w.t.Fatal(err)
	}
	k, err := w.keys.AddKey(ownerjournal.NewKey{
		CredentialID: c.CredentialID, PublicKey: c.SPKI, Kind: c.Kind, AAGUID: c.AAGUID,
		BackupEligible: c.BackupEligible, BackupState: c.BackupState, Counter: c.SignCount,
		PairedAt: start, Words: "acorn bridge cedar delta ember falcon garden harbor",
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return a, k
}

// issue asks a decision challenge for frame as the seat src, for the
// world's key.
func (w *seatWorld) issue(src Source, f ownerwire.DecisionFrame) (ownerwire.SeatChallengeResult, error) {
	return w.svc.SeatChallenge(context.Background(), src, testGate, ownerwire.SeatChallengeParams{Frame: f, Credential: w.key.ID})
}

// sign has the phone sign a challenge.
func sign(t *testing.T, a *webauthntest.Authenticator, challenge []byte) ownerwire.Assertion {
	t.Helper()
	got, err := a.Get(challenge)
	if err != nil {
		t.Fatal(err)
	}
	return ownerwire.Assertion{CredentialID: got.CredentialID, AuthenticatorData: got.AuthenticatorData, ClientDataJSON: got.ClientDataJSON, Signature: got.Signature}
}

// signed issues a challenge for f and returns the params of the decide the
// phone signs for it.
func (w *seatWorld) signed(f ownerwire.DecisionFrame) ownerwire.SeatDecideParams {
	w.t.Helper()
	ch, err := w.issue(gateSrc, f)
	if err != nil {
		w.t.Fatalf("SeatChallenge: %v", err)
	}
	return ownerwire.SeatDecideParams{ChallengeID: ch.ChallengeID, Frame: f, Assertion: sign(w.t, w.phone, ch.Challenge)}
}

func (w *seatWorld) decide(src Source, p ownerwire.SeatDecideParams) (ownerwire.DecideResult, error) {
	return w.svc.SeatDecide(context.Background(), src, testGate, p)
}

func denyFrame(p record.Pending, reason string) ownerwire.DecisionFrame {
	f := frame(p, false)
	f.Reason = reason
	return f
}

// countEntries returns how many entries of each kind the journal holds.
func (w *seatWorld) countEntries() map[ownerjournal.Kind]int {
	out := map[ownerjournal.Kind]int{}
	for _, k := range w.kinds() {
		out[k]++
	}
	return out
}

// noDecisionWasMade fails the test when the journal has a decision, a held
// or a refused entry, or the door saw a decision.
func (w *seatWorld) noDecisionWasMade(t *testing.T) {
	t.Helper()
	n := w.countEntries()
	if n[ownerjournal.KindDecision]+n[ownerjournal.KindHeld]+n[ownerjournal.KindRefused] != 0 {
		t.Fatalf("the journal holds %v", w.kinds())
	}
	if d, _, _ := w.door.counts(); d != 0 {
		t.Fatalf("the door saw %d decisions", d)
	}
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("the error is %v, want %v", err, want)
	}
}

// An approve from the phone, through the real journal: the record says it
// was confirmed by passkey with a seat block, the journal's activity says the
// surface seat and the gate session with no desk activity, and the evidence
// read back after a restart lets an auditor recompute the challenge from the
// record, check the signature with the key from the list and match the
// record's digest. The verified counter is kept on the list.
func TestASeatApproveRunsToAppliedWithItsEvidence(t *testing.T) {
	w := newSeatWorld(t)
	w.phone.CounterStep = 3
	p := w.card("feature-a")
	f := frame(p, true)
	params := w.signed(f)
	res, err := w.decide(gateSrc, params)
	if err != nil {
		t.Fatalf("SeatDecide: %v", err)
	}
	if res.State != record.StateApplied || res.Held {
		t.Fatalf("the result is %+v", res)
	}
	if calls, moves := w.intake.appliesSeen(); calls != 1 || moves != 1 {
		t.Fatalf("applied %d times, moved %d", calls, moves)
	}
	if k, _ := w.svc.seatKey(w.key.ID); k.Counter != 3 {
		t.Fatalf("the counter kept is %d", k.Counter)
	}

	d := w.entries(ownerjournal.KindDecision)[0]
	dec, err := record.DecodeDecision(d.Record)
	if err != nil {
		t.Fatal(err)
	}
	want := record.SeatBlock{AssertionSHA256: params.Assertion.Digest(), ChallengeID: params.ChallengeID, Credential: w.key.ID, Session: gateSrc.Session}
	if dec.Confirmed != record.ConfirmedPasskey || dec.Seat != want || dec.Console != (record.Console{}) {
		t.Fatalf("the record is confirmed %q with %+v and %+v", dec.Confirmed, dec.Seat, dec.Console)
	}
	if !record.SignOffsEqual(p.Binding.Summary.Flagged, dec.SignedOff) || dec.Uses != 1 {
		t.Fatalf("the record is %+v", dec)
	}
	shown, err := w.stand.Show(context.Background(), p.ID)
	if err != nil || !bytes.Equal(shown.Decision.Bytes, d.Record) {
		t.Fatalf("the door holds other bytes than the journal: %v", err)
	}

	// The activity entry: the surface seat, the gate session, no desk time.
	act := w.entries(ownerjournal.KindActivity)
	var body struct {
		Surface      string `json:"surface"`
		Session      string `json:"session"`
		LastActivity string `json:"last_activity"`
	}
	if len(act) != 1 || json.Unmarshal(act[0].Body, &body) != nil || body.Surface != "seat" || body.Session != gateSrc.Session || body.LastActivity != "" {
		t.Fatalf("the activity is %s", act[0].Body)
	}

	// After a restart, the evidence is read back and checks again.
	w.restart()
	ev, err := w.svc.journal().(*ownerjournal.Store).EvidenceOf(d.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Instance != testGate.Instance || ev.Origin != testGate.Origin || record.AssertionDigest(ev.CredentialID, ev.AuthenticatorData, ev.ClientDataJSON, ev.Signature) != dec.Seat.AssertionSHA256 {
		t.Fatalf("the evidence is %+v", ev)
	}
	audit := ownerwire.DecisionFrame{ID: dec.ID, BindingSHA256: dec.BindingSHA256, Decision: dec.Decision, SignedOff: dec.SignedOff, Reason: dec.Reason}
	ch, err := ownerwire.DecisionChallenge(ev.Instance, audit, ev.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	key := w.key
	key.Counter = 0
	if _, err := credkind.Verify(credentialOf(key), credkind.AssertionInput{CredentialID: ev.CredentialID, ClientDataJSON: ev.ClientDataJSON, AuthenticatorData: ev.AuthenticatorData, Signature: ev.Signature},
		credkind.Expect{Challenge: ch, Origin: ev.Origin, RPID: testGate.RPID}); err != nil {
		t.Fatalf("the auditor's check: %v", err)
	}
}

// A login proves the phone holds a paired credential: the allow-list names
// the usable keys, newest first, and the answer the credential that signed.
// A login assertion is no decision, and a decision's assertion is no login.
func TestASeatLoginAnswersTheCredential(t *testing.T) {
	w := newSeatWorld(t)
	second, k2 := w.pairPhone(nil)
	revoked, k3 := w.pairPhone(nil)
	if err := w.keys.RevokeKeys([]string{k3.ID}, start); err != nil {
		t.Fatal(err)
	}
	w.keys.seed("00000000000000aa", false) // a key of the kind "unverified"
	ctx := context.Background()

	lc, err := w.svc.SeatLoginChallenge(ctx, gateSrc, testGate)
	if err != nil {
		t.Fatal(err)
	}
	if len(lc.Allow) != 2 || !bytes.Equal(lc.Allow[0], k2.CredentialID) || !bytes.Equal(lc.Allow[1], w.key.CredentialID) {
		t.Fatalf("allow is %d ids", len(lc.Allow))
	}
	want, err := ownerwire.LoginChallenge(testGate.Instance, lc.Nonce)
	if err != nil || !bytes.Equal(want, lc.Challenge) {
		t.Fatal("the login challenge is not the contract's")
	}
	if _, err := ownerwire.EncodeSeatLoginChallengeResult(lc); err != nil {
		t.Fatal(err)
	}
	got, err := w.svc.SeatLogin(ctx, gateSrc, testGate, ownerwire.SeatLoginParams{ChallengeID: lc.ChallengeID, Assertion: sign(t, second, lc.Challenge)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Credential != k2.ID || !bytes.Equal(got.CredentialID, k2.CredentialID) || got.Kind != k2.Kind {
		t.Fatalf("the login answered %+v", got)
	}
	if _, err := ownerwire.EncodeSeatLoginResult(got); err != nil {
		t.Fatal(err)
	}
	// One use.
	_, err = w.svc.SeatLogin(ctx, gateSrc, testGate, ownerwire.SeatLoginParams{ChallengeID: lc.ChallengeID, Assertion: sign(t, second, lc.Challenge)})
	wantErr(t, err, ErrChallengeStale)

	login := func(a *webauthntest.Authenticator) error {
		lc, err := w.svc.SeatLoginChallenge(ctx, gateSrc, testGate)
		if err != nil {
			t.Fatal(err)
		}
		_, err = w.svc.SeatLogin(ctx, gateSrc, testGate, ownerwire.SeatLoginParams{ChallengeID: lc.ChallengeID, Assertion: sign(t, a, lc.Challenge)})
		return err
	}
	wantErr(t, login(revoked), ErrKeyRevoked)
	stranger, err := webauthntest.New(testGate.RPID, testGate.Origin)
	if err != nil {
		t.Fatal(err)
	}
	wantErr(t, login(stranger), ErrKeyUnknown)
	w.phone.Flags = webauthntest.FlagUP
	wantErr(t, login(w.phone), credkind.ErrUserVerification)
	w.phone.Flags = webauthntest.FlagUP | webauthntest.FlagUV
	w.phone.Origin = "https://other.example"
	wantErr(t, login(w.phone), credkind.ErrOrigin)
	w.phone.Origin = testGate.Origin

	// An unverified kind cannot log in.
	unv, kU := w.pairPhone(nil)
	w.keys.mu.Lock()
	for i := range w.keys.keys {
		if w.keys.keys[i].ID == kU.ID {
			w.keys.keys[i].Kind = "unverified"
		}
	}
	w.keys.mu.Unlock()
	wantErr(t, login(unv), ErrKindRefused)

	// A login's assertion offered as a decision is refused, and so is a
	// decision's offered as a login. Here the two challenges also have
	// different nonces; TestLoginAndDecisionChallengesAreSeparateDomains
	// shows that they differ with the same instance and nonce.
	p := w.card("feature-a")
	lc, err = w.svc.SeatLoginChallenge(ctx, gateSrc, testGate)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := w.issue(gateSrc, frame(p, true))
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.decide(gateSrc, ownerwire.SeatDecideParams{ChallengeID: ch.ChallengeID, Frame: frame(p, true), Assertion: sign(t, w.phone, lc.Challenge)})
	wantErr(t, err, credkind.ErrChallenge)
	ch, err = w.issue(gateSrc, frame(p, true))
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.SeatLogin(ctx, gateSrc, testGate, ownerwire.SeatLoginParams{ChallengeID: lc.ChallengeID, Assertion: sign(t, w.phone, ch.Challenge)})
	wantErr(t, err, credkind.ErrChallenge)
	// A decision's challenge id is not a login's, and the reverse.
	_, err = w.svc.SeatLogin(ctx, gateSrc, testGate, ownerwire.SeatLoginParams{ChallengeID: ch.ChallengeID, Assertion: sign(t, w.phone, ch.Challenge)})
	wantErr(t, err, ErrChallengeStale)
	w.noDecisionWasMade(t)
}

// The login and the decision challenges are of separate domains: their
// domain strings differ and neither starts the other, and with the same
// instance and the same nonce a login's challenge is no decision's, an
// approve's or a deny's, so an assertion over one never verifies as the
// other. The service draws both with ownerwire's functions; a login
// challenge of the service is the login domain's.
func TestLoginAndDecisionChallengesAreSeparateDomains(t *testing.T) {
	if ownerwire.LoginDomain == ownerwire.DecisionDomain ||
		strings.HasPrefix(ownerwire.LoginDomain, ownerwire.DecisionDomain) ||
		strings.HasPrefix(ownerwire.DecisionDomain, ownerwire.LoginDomain) {
		t.Fatalf("the domains %q and %q are not separate", ownerwire.LoginDomain, ownerwire.DecisionDomain)
	}
	w := newSeatWorld(t)
	p := w.card("feature-a")
	lc, err := w.svc.SeatLoginChallenge(context.Background(), gateSrc, testGate)
	if err != nil {
		t.Fatal(err)
	}
	login, err := ownerwire.LoginChallenge(testGate.Instance, lc.Nonce)
	if err != nil || !bytes.Equal(login, lc.Challenge) {
		t.Fatalf("the service's login challenge is not the login domain's: %v", err)
	}
	for _, f := range []ownerwire.DecisionFrame{frame(p, true), denyFrame(p, ""), denyFrame(p, "no")} {
		decision, err := ownerwire.DecisionChallenge(testGate.Instance, f, lc.Nonce)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(decision, login) {
			t.Fatalf("the %s challenge is the login's for the same instance and nonce", f.Decision)
		}
		// The phone's assertion over the decision's challenge, with the
		// login's own nonce, is no login.
		_, err = w.svc.SeatLogin(context.Background(), gateSrc, testGate, ownerwire.SeatLoginParams{ChallengeID: lc.ChallengeID, Assertion: sign(t, w.phone, decision)})
		wantErr(t, err, credkind.ErrChallenge)
		if lc, err = w.svc.SeatLoginChallenge(context.Background(), gateSrc, testGate); err != nil {
			t.Fatal(err)
		}
		if login, err = ownerwire.LoginChallenge(testGate.Instance, lc.Nonce); err != nil {
			t.Fatal(err)
		}
	}
}

// A deny from the phone needs a fresh assertion as an approve does: one that
// does not verify decides nothing. A verified deny carries no sign-off, and
// past the day's limit it is neither held nor counted as a use.
func TestASeatDenyNeedsAnAssertionAndIsNeverHeld(t *testing.T) {
	w := newSeatWorld(t)
	w.setLimit(1)
	w.useUp(1, "feature-a")
	uses, err := w.svc.journal().CardUses("project-a", startOfDay(w.clock.Now()))
	if err != nil || len(uses) != 1 {
		t.Fatalf("uses %v %v", uses, err)
	}
	w.resetRef()
	p := w.card("feature-b")
	f := denyFrame(p, "not this one")

	bad := w.signed(f)
	bad.Assertion.Signature = append([]byte(nil), bad.Assertion.Signature...)
	bad.Assertion.Signature[len(bad.Assertion.Signature)-1] ^= 1
	_, err = w.decide(gateSrc, bad)
	wantErr(t, err, ErrAssertionInvalid)
	if w.state(p.ID) != record.StatePending || len(w.entries(ownerjournal.KindDecision)) != 1 {
		t.Fatal("a deny without a good assertion was taken")
	}

	res, err := w.decide(gateSrc, w.signed(f))
	if err != nil {
		t.Fatal(err)
	}
	if res.State != record.StateDenied || res.Held {
		t.Fatalf("the deny came to %+v", res)
	}
	decs := w.entries(ownerjournal.KindDecision)
	dec, err := record.DecodeDecision(decs[len(decs)-1].Record)
	if err != nil || dec.Decision != record.DecisionDeny || len(dec.SignedOff) != 0 || dec.Reason != "not this one" || dec.Uses != 0 || dec.Confirmed != record.ConfirmedPasskey {
		t.Fatalf("the deny's record is %+v %v", dec, err)
	}
	if after, err := w.svc.journal().CardUses("project-a", startOfDay(w.clock.Now())); err != nil || len(after) != 1 {
		t.Fatalf("a deny counted as a use: %v %v", after, err)
	}
	if w.heldCount() != 0 {
		t.Fatal("a deny was held")
	}
}

// An approve from the phone past the day's limit is held: journaled with its
// evidence, not handed to the door, and it uses nothing. A later approve
// needs a new tap and a new assertion.
func TestASeatApprovePastTheLimitIsHeld(t *testing.T) {
	w := newSeatWorld(t)
	w.setLimit(1)
	w.useUp(1, "feature-a")
	w.resetRef()
	p := w.card("feature-b")
	decides, _, _ := w.door.counts()
	params := w.signed(frame(p, true))
	res, err := w.decide(gateSrc, params)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Held || res.State != record.StatePending {
		t.Fatalf("the approve came to %+v", res)
	}
	if after, _, _ := w.door.counts(); after != decides || w.state(p.ID) != record.StatePending {
		t.Fatal("a held approve reached the door")
	}
	if uses, err := w.svc.journal().CardUses("project-a", startOfDay(w.clock.Now())); err != nil || len(uses) != 1 {
		t.Fatalf("a held approve used: %v %v", uses, err)
	}
	held := w.entries(ownerjournal.KindHeld)
	if len(held) != 1 {
		t.Fatalf("%d held entries", len(held))
	}
	ev, err := w.svc.journal().(*ownerjournal.Store).EvidenceOf(held[0].Seq)
	if err != nil || record.AssertionDigest(ev.CredentialID, ev.AuthenticatorData, ev.ClientDataJSON, ev.Signature) != params.Assertion.Digest() {
		t.Fatalf("the held entry's evidence: %v", err)
	}
	shown, err := w.svc.Show(context.Background(), p.ID)
	if err != nil || !shown.Held || shown.OnItsWay {
		t.Fatalf("the card reads %+v %v", shown, err)
	}
	// The held challenge is spent.
	_, err = w.decide(gateSrc, params)
	wantErr(t, err, ErrChallengeStale)
}

// A challenge is used once: a replay of a used id, a second decide after
// the first succeeded, and an id after a restart are stale.
func TestASeatChallengeIsUsedOnce(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	f := denyFrame(p, "")
	params := w.signed(f)
	if _, err := w.decide(gateSrc, params); err != nil {
		t.Fatal(err)
	}
	_, err := w.decide(gateSrc, params)
	wantErr(t, err, ErrChallengeStale)

	w.resetRef()
	q := w.card("feature-b")
	params = w.signed(frame(q, true))
	w.restart()
	_, err = w.decide(gateSrc, params)
	wantErr(t, err, ErrChallengeStale)
	if w.state(q.ID) != record.StatePending {
		t.Fatal("a challenge outlived a restart")
	}
	// An expired one too, at its expiry.
	params = w.signed(frame(q, true))
	w.advance(ChallengeLife)
	_, err = w.decide(gateSrc, params)
	wantErr(t, err, ErrChallengeStale)
	if w.state(q.ID) != record.StatePending {
		t.Fatal("an expired challenge decided")
	}
}

// The seat cannot change what the phone signed: an assertion over a
// challenge for one card, decision, sign-off set or reason does not decide
// another, and the try burns the challenge.
func TestASeatChallengeBindsTheCardTheDecisionTheSignOffsAndTheReason(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	w.resetRef()
	other := w.card("feature-b")
	approve := frame(p, true)
	deny := denyFrame(p, "why not")
	fewer := approve
	fewer.SignedOff = approve.SignedOff[:1]
	cases := map[string]struct {
		signedFor, sent ownerwire.DecisionFrame
	}{
		"another card":       {approve, frame(other, true)},
		"another decision":   {approve, denyFrame(p, "")},
		"approve for a deny": {deny, approve},
		"another reason":     {deny, denyFrame(p, "why yes")},
		"no reason":          {deny, denyFrame(p, "")},
		"fewer sign-offs":    {approve, fewer},
		"another binding": {approve, func() ownerwire.DecisionFrame {
			f := approve
			f.BindingSHA256 = strings.Repeat("0", 64)
			return f
		}()},
	}
	for name, c := range cases {
		params := w.signed(c.signedFor)
		sent := params
		sent.Frame = c.sent
		_, err := w.decide(gateSrc, sent)
		if !errors.Is(err, ErrChallengeStale) {
			t.Fatalf("%s: %v", name, err)
		}
		// The challenge is burnt: the right frame is refused afterwards.
		_, err = w.decide(gateSrc, params)
		if !errors.Is(err, ErrChallengeStale) {
			t.Fatalf("%s, then the right frame: %v", name, err)
		}
	}
	w.noDecisionWasMade(t)
	// And the assertion for one card's challenge, under another card's
	// challenge id, is refused by the verifier.
	pa := w.signed(approve)
	pb := w.signed(frame(other, true))
	pb.Assertion = pa.Assertion
	_, err := w.decide(gateSrc, pb)
	wantErr(t, err, ErrAssertionInvalid)
	w.noDecisionWasMade(t)
}

// An entry belongs to the gate session that asked for it: another session
// neither uses nor burns it, whatever it sends, a malformed assertion
// included. A frame whose content is wrong, sent by the owner's own session,
// is refused after the take and burns the entry.
func TestASeatChallengeBelongsToItsSession(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	params := w.signed(frame(p, true))

	_, err := w.decide(otherSrc, params)
	wantErr(t, err, ErrChallengeStale)
	short := params
	short.Assertion.Signature = nil
	_, err = w.decide(otherSrc, short)
	wantErr(t, err, ErrSeatParams)
	_, err = w.decide(gateSrc, short)
	wantErr(t, err, ErrSeatParams)
	badID := params
	badID.ChallengeID = strings.ToUpper(params.ChallengeID)
	_, err = w.decide(gateSrc, badID)
	wantErr(t, err, ErrSeatParams)
	_, err = w.svc.SeatLogin(context.Background(), otherSrc, testGate, ownerwire.SeatLoginParams{ChallengeID: params.ChallengeID, Assertion: params.Assertion})
	wantErr(t, err, ErrChallengeStale)
	if n := w.svc.seat.size(challengeDecision); n != 1 {
		t.Fatalf("%d entries after another session's tries", n)
	}
	if res, err := w.decide(gateSrc, params); err != nil || res.State != record.StateApplied {
		t.Fatalf("the owner's decide: %+v %v", res, err)
	}

	w.resetRef()
	q := w.card("feature-b")
	params = w.signed(denyFrame(q, ""))
	wrong := params
	wrong.Frame.SignedOff = []record.SignOff{{Kind: record.FlagExecConfig, Path: "Makefile"}}
	_, err = w.decide(gateSrc, wrong)
	wantErr(t, err, ErrSeatParams)
	_, err = w.decide(gateSrc, params)
	wantErr(t, err, ErrChallengeStale)
	if w.state(q.ID) != record.StatePending {
		t.Fatal("a burnt challenge decided")
	}
	// A source that is not a seat takes nothing.
	params = w.signed(denyFrame(q, ""))
	for _, s := range []Source{src, {Surface: "console", Session: gateSrc.Session}, {Surface: SurfaceSeat, Session: gateSrc.Session, Decider: true}, {Surface: SurfaceSeat, Session: "x"}} {
		_, err = w.decide(s, params)
		wantErr(t, err, ErrNotSeat)
	}
	if res, err := w.decide(gateSrc, params); err != nil || res.State != record.StateDenied {
		t.Fatalf("the owner's deny after the others: %+v %v", res, err)
	}
}

// Of several decides of one challenge at once, exactly one is taken.
func TestSimultaneousSeatDecidesOfOneChallenge(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	params := w.signed(frame(p, true))
	var wg sync.WaitGroup
	results := make(chan error, 8)
	gate := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			_, err := w.decide(gateSrc, params)
			results <- err
		}()
	}
	close(gate)
	wg.Wait()
	close(results)
	ok, stale := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrChallengeStale):
			stale++
		default:
			t.Fatalf("a decide answered %v", err)
		}
	}
	if ok != 1 || stale != 7 {
		t.Fatalf("%d taken, %d stale", ok, stale)
	}
	if n := len(w.entries(ownerjournal.KindDecision)); n != 1 {
		t.Fatalf("%d decisions", n)
	}
}

// The key is checked when the challenge is issued, when the decide takes it,
// and again under the revocation lock right before the append.
func TestARevokedOrUnknownKeyDecidesNothing(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	ctx := context.Background()

	// Not on the list.
	_, err := w.svc.SeatChallenge(ctx, gateSrc, testGate, ownerwire.SeatChallengeParams{Frame: frame(p, true), Credential: "fedcba9876543210"})
	wantErr(t, err, ErrKeyUnknown)

	// Another paired credential's assertion for this credential's
	// challenge.
	other, _ := w.pairPhone(nil)
	params := w.signed(frame(p, true))
	ch, err := w.issue(gateSrc, frame(p, true))
	if err != nil {
		t.Fatal(err)
	}
	params.ChallengeID, params.Assertion = ch.ChallengeID, sign(t, other, ch.Challenge)
	_, err = w.decide(gateSrc, params)
	wantErr(t, err, ErrAssertionInvalid)
	wantErr(t, err, errOtherCredential)

	// Revoked between challenge and decide.
	params = w.signed(frame(p, true))
	if _, err := w.svc.KeyRevoke(ctx, src, ownerwire.KeyRevokeParams{ID: w.key.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = w.decide(gateSrc, params)
	wantErr(t, err, ErrKeyRevoked)
	// Revoked before the challenge.
	_, err = w.issue(gateSrc, frame(p, true))
	wantErr(t, err, ErrKeyRevoked)
	w.noDecisionWasMade(t)

	// A list that cannot be used refuses, and is no empty list.
	w.keys.failRead = errStoreDown
	_, err = w.issue(gateSrc, frame(p, true))
	wantErr(t, err, ErrKeysUnavailable)
	_, err = w.svc.SeatLoginChallenge(ctx, gateSrc, testGate)
	wantErr(t, err, ErrKeysUnavailable)
}

// A revoke and the append have one order. A revoke that returned before the
// append wins: nothing is appended. A decision appended first stands, and
// the revoke, which waited for the append, comes after it.
func TestARevokeAgainstTheAppendInBothOrders(t *testing.T) {
	t.Run("revoke first", func(t *testing.T) {
		w := newSeatWorld(t)
		p := w.card("feature-a")
		params := w.signed(frame(p, true))
		w.at["seat-verified"] = func() {
			if _, err := w.svc.KeyRevoke(context.Background(), src, ownerwire.KeyRevokeParams{ID: w.key.ID}); err != nil {
				t.Errorf("KeyRevoke: %v", err)
			}
		}
		_, err := w.decide(gateSrc, params)
		wantErr(t, err, ErrKeyRevoked)
		w.noDecisionWasMade(t)
		if w.state(p.ID) != record.StatePending {
			t.Fatal("the card was decided")
		}
	})
	t.Run("append first", func(t *testing.T) {
		w := newSeatWorld(t)
		p := w.card("feature-a")
		params := w.signed(frame(p, true))
		revoked := make(chan error, 1)
		atLock := make(chan struct{})
		var decidedFirst, lockHeld, doneBeforeAppend bool
		// The revoke passes "revoke-locking" right before it takes the
		// revocation lock and "revoke-locked" once it has it.
		w.at["revoke-locking"] = func() { close(atLock) }
		w.at["revoke-locked"] = func() {
			decidedFirst = w.countEntries()[ownerjournal.KindDecision] == 1
		}
		// The point is passed under the revocation lock, right before the
		// append: a revoke started there reaches the lock and waits for the
		// append, so it cannot return while the hook runs.
		w.at["key-rechecked"] = func() {
			go func() {
				_, err := w.svc.KeyRevoke(context.Background(), src, ownerwire.KeyRevokeParams{ID: w.key.ID})
				revoked <- err
			}()
			<-atLock
			if w.svc.pmu.TryLock() {
				w.svc.pmu.Unlock()
			} else {
				lockHeld = true
			}
			select {
			case err := <-revoked:
				doneBeforeAppend = true
				revoked <- err
			default:
			}
		}
		res, err := w.decide(gateSrc, params)
		if err != nil || res.State != record.StateApplied {
			t.Fatalf("the decision appended first: %+v %v", res, err)
		}
		if !lockHeld {
			t.Fatal("the append does not hold the revocation lock")
		}
		if doneBeforeAppend {
			t.Fatal("the revoke returned while the append held the revocation lock")
		}
		if err := <-revoked; err != nil {
			t.Fatal(err)
		}
		if !decidedFirst {
			t.Fatal("the revoke took the lock before the decision was appended")
		}
		if k, err := w.svc.seatKey(w.key.ID); !errors.Is(err, ErrKeyRevoked) {
			t.Fatalf("the key after the revoke: %+v %v", k, err)
		}
	})
}

// A key whose kind the verifier does not know (unverified) or the card's
// project does not accept decides nothing, and a policy that changes between
// the challenge and the decide is asked again.
func TestTheKindPolicyHoldsAtChallengeAndDecide(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	if w.key.Kind != credkind.NameUnknown {
		t.Fatalf("the test's passkey is of the kind %s", w.key.Kind)
	}
	w.setPolicy(credkind.NameVault)
	_, err := w.issue(gateSrc, frame(p, true))
	wantErr(t, err, ErrKindRefused)

	w.setPolicy(credkind.NameUnknown)
	params := w.signed(frame(p, true))
	w.setPolicy(credkind.NamePlatform)
	_, err = w.decide(gateSrc, params)
	wantErr(t, err, ErrKindRefused)

	w.setPolicy()
	w.keys.mu.Lock()
	w.keys.keys[0].Kind = "unverified"
	w.keys.mu.Unlock()
	_, err = w.issue(gateSrc, frame(p, true))
	wantErr(t, err, ErrKindRefused)
	w.noDecisionWasMade(t)
}

// Each failure of the assertion is refused and decides nothing: user
// verification off, another origin, another relying party, a broken
// signature, another backup eligibility than the paired one.
func TestAnAssertionThatDoesNotVerifyDecidesNothing(t *testing.T) {
	cases := map[string]struct {
		change func(*webauthntest.Authenticator)
		want   error
	}{
		"user verification off": {func(a *webauthntest.Authenticator) { a.Flags = webauthntest.FlagUP }, credkind.ErrUserVerification},
		"another origin":        {func(a *webauthntest.Authenticator) { a.Origin = "https://seat.example.evil" }, credkind.ErrOrigin},
		"another relying party": {func(a *webauthntest.Authenticator) { a.RPID = "evil.example" }, credkind.ErrRPID},
		"another key":           {func(a *webauthntest.Authenticator) { b, _ := webauthntest.New("x", "y"); a.Key = b.Key }, credkind.ErrSignature},
		"backup eligible now":   {func(a *webauthntest.Authenticator) { a.Flags |= webauthntest.FlagBE }, credkind.ErrBackup},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newSeatWorld(t)
			p := w.card("feature-a")
			ch, err := w.issue(gateSrc, frame(p, true))
			if err != nil {
				t.Fatal(err)
			}
			c.change(w.phone)
			params := ownerwire.SeatDecideParams{ChallengeID: ch.ChallengeID, Frame: frame(p, true), Assertion: sign(t, w.phone, ch.Challenge)}
			_, err = w.decide(gateSrc, params)
			wantErr(t, err, ErrAssertionInvalid)
			wantErr(t, err, c.want)
			w.noDecisionWasMade(t)
			if !strings.Contains(err.Error(), c.want.Error()) || strings.Contains(err.Error(), testGate.Origin) {
				t.Fatalf("the error says %q", err)
			}
		})
	}
}

// The gate's own values must be of their form; a seat never sets them.
func TestAGateOfTheWrongFormIsRefused(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	params := w.signed(frame(p, true))
	for _, g := range []Gate{
		{},
		{Instance: testGate.Instance, Origin: "http://seat.example", RPID: "seat.example"},
		{Instance: testGate.Instance, Origin: "https://seat.example", RPID: "other.example"},
		{Instance: testGate.Instance, Origin: "https://Seat.example", RPID: "Seat.example"},
		{Instance: testGate.Instance, Origin: "https://seat.example:8443", RPID: "seat.example:8443"},
		{Instance: strings.ToUpper(testGate.Instance), Origin: testGate.Origin, RPID: testGate.RPID},
	} {
		if _, err := w.svc.SeatDecide(context.Background(), gateSrc, g, params); !errors.Is(err, errGate) {
			t.Fatalf("%+v: %v", g, err)
		}
		if _, err := w.svc.SeatLoginChallenge(context.Background(), gateSrc, g); !errors.Is(err, errGate) {
			t.Fatalf("%+v: %v", g, err)
		}
	}
	// None of them burnt the entry.
	if res, err := w.decide(gateSrc, params); err != nil || res.State != record.StateApplied {
		t.Fatalf("%+v %v", res, err)
	}
}

// A counter that grows is kept; one that does not grow while not zero is
// told as a possible clone and the decision stands; a synced passkey's
// zeros are neither.
func TestTheSignCounter(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	if _, err := w.decide(gateSrc, w.signed(denyFrame(p, ""))); err != nil {
		t.Fatal(err)
	}
	if w.keys.raises != 0 || len(w.noticed()) != 0 {
		t.Fatalf("a synced passkey's zero counter: %d raises, %q", w.keys.raises, w.noticed())
	}
	w.phone.SignCount, w.phone.CounterStep = 10, 0
	w.resetRef()
	q := w.card("feature-b")
	if _, err := w.decide(gateSrc, w.signed(denyFrame(q, ""))); err != nil {
		t.Fatal(err)
	}
	if k, _ := w.svc.seatKey(w.key.ID); k.Counter != 10 || w.keys.raises != 1 {
		t.Fatalf("the counter is %d after %d raises", k.Counter, w.keys.raises)
	}
	// The same counter again: a possible clone, not refused, not lowered.
	w.resetRef()
	r := w.card("feature-c")
	res, err := w.decide(gateSrc, w.signed(denyFrame(r, "")))
	if err != nil || res.State != record.StateDenied {
		t.Fatalf("a non-growing counter was refused: %+v %v", res, err)
	}
	notes := w.noticed()
	if len(notes) != 1 || !strings.Contains(notes[0], "may have been copied") || !strings.Contains(notes[0], w.key.ID) {
		t.Fatalf("notices %q", notes)
	}
	if k, _ := w.svc.seatKey(w.key.ID); k.Counter != 10 || w.keys.raises != 1 {
		t.Fatalf("the counter is %d after %d raises", k.Counter, w.keys.raises)
	}
	// A list that cannot keep the counter does not refuse the decision.
	w.keys.failRaise = errStoreDown
	w.phone.CounterStep = 1
	w.resetRef()
	s := w.card("feature-d")
	if res, err := w.decide(gateSrc, w.signed(denyFrame(s, ""))); err != nil || res.State != record.StateDenied {
		t.Fatalf("%+v %v", res, err)
	}
}

// login has the phone a log in through the seat with a fresh login
// challenge.
func (w *seatWorld) login(a *webauthntest.Authenticator) error {
	w.t.Helper()
	lc, err := w.svc.SeatLoginChallenge(context.Background(), gateSrc, testGate)
	if err != nil {
		w.t.Fatal(err)
	}
	_, err = w.svc.SeatLogin(context.Background(), gateSrc, testGate, ownerwire.SeatLoginParams{ChallengeID: lc.ChallengeID, Assertion: sign(w.t, a, lc.Challenge)})
	return err
}

// Two verifications that read the key before either raised its counter, as
// two at once do, are compared in turn against the highest counter: the
// second, a copy that reports the same counter, is told as a possible clone
// though its snapshot of the key held the lower one. The Result is what
// credkind.Verify gives for a counter of 6 against a stored 5.
func TestVerificationsOfOneSnapshotCompareTheirCountersInTurn(t *testing.T) {
	w := newSeatWorld(t)
	if err := w.keys.RaiseCounter(w.key.ID, 5, start); err != nil {
		t.Fatal(err)
	}
	snapshot, err := w.svc.seatKey(w.key.ID)
	if err != nil || snapshot.Counter != 5 {
		t.Fatalf("%+v %v", snapshot, err)
	}
	w.svc.verified(snapshot, credkind.Result{Counter: 6}, gateSrc.Session)
	if notes := w.noticed(); len(notes) != 0 {
		t.Fatalf("the first: %q", notes)
	}
	w.svc.verified(snapshot, credkind.Result{Counter: 6}, otherSrc.Session)
	notes := w.noticed()
	if len(notes) != 1 || !strings.Contains(notes[0], "may have been copied") || !strings.Contains(notes[0], otherSrc.Session) {
		t.Fatalf("the second: %q", notes)
	}
	if k, _ := w.svc.seatKey(w.key.ID); k.Counter != 6 || w.keys.raises != 2 {
		t.Fatalf("the counter is %d after %d raises", k.Counter, w.keys.raises)
	}

	// At once: of eight verifications of one counter from one snapshot, one
	// raises it and seven are told.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.svc.verified(snapshot, credkind.Result{Counter: 9}, gateSrc.Session)
		}()
	}
	wg.Wait()
	if n := len(w.noticed()); n != 1+7 {
		t.Fatalf("%d notices: %q", n, w.noticed())
	}
	if k, _ := w.svc.seatKey(w.key.ID); k.Counter != 9 || w.keys.raises != 3 {
		t.Fatalf("the counter is %d after %d raises", k.Counter, w.keys.raises)
	}
}

// When the list has no room left for counter records, the desk is told once,
// for that key, and the highest counter is kept in memory, so a copy that
// reports less than the highest verified, though more than the stored one, is
// still told while the daemon runs. The login stands each time.
func TestACounterTheListHasNoRoomForIsToldAndKeptInMemory(t *testing.T) {
	w := newSeatWorld(t)
	w.keys.failRaise = ownerjournal.ErrKeysFull
	w.phone.SignCount, w.phone.CounterStep = 10, 0
	if err := w.login(w.phone); err != nil {
		t.Fatal(err)
	}
	notes := w.noticed()
	if len(notes) != 1 || !strings.Contains(notes[0], "no room left") || !strings.Contains(notes[0], w.key.ID) {
		t.Fatalf("notices %q", notes)
	}
	w.phone.CounterStep = 1
	if err := w.login(w.phone); err != nil {
		t.Fatal(err)
	}
	if n := len(w.noticed()); n != 1 {
		t.Fatalf("the room was told again: %q", w.noticed())
	}
	// A copy that counted less: above the stored 0, below the 11 verified.
	w.phone.SignCount, w.phone.CounterStep = 5, 0
	if err := w.login(w.phone); err != nil {
		t.Fatal(err)
	}
	notes = w.noticed()
	if len(notes) != 2 || !strings.Contains(notes[1], "may have been copied") {
		t.Fatalf("notices %q", notes)
	}
	if k, _ := w.svc.seatKey(w.key.ID); k.Counter != 0 || w.keys.raises != 0 {
		t.Fatalf("the counter is %d after %d raises", k.Counter, w.keys.raises)
	}
	// Another failure of the list is told each time, as before.
	w.keys.failRaise = errStoreDown
	w.phone.SignCount, w.phone.CounterStep = 20, 0
	if err := w.login(w.phone); err != nil {
		t.Fatal(err)
	}
	notes = w.noticed()
	if len(notes) != 3 || !strings.Contains(notes[2], "could not be kept") {
		t.Fatalf("notices %q", notes)
	}
}

// The challenge is drawn only for what would be taken: a card that is not
// pending, one with a decision on its way, a binding that is not the
// record's, sign-offs that are not its flagged list, and the 33rd challenge
// are refused, and none of it is journaled.
func TestASeatChallengeIsDrawnOnlyForWhatWouldBeTaken(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	f := frame(p, true)
	f.BindingSHA256 = strings.Repeat("1", 64)
	_, err := w.issue(gateSrc, f)
	wantCode(t, err, record.CodeBindingMismatch)
	f = frame(p, true)
	f.SignedOff = f.SignedOff[:1]
	_, err = w.issue(gateSrc, f)
	wantCode(t, err, record.CodeDecisionInvalid)
	f = frame(p, true)
	f.ID = "ap-20261120-0999"
	_, err = w.issue(gateSrc, f)
	wantCode(t, err, record.CodeDecisionInvalid)
	_, err = w.svc.SeatChallenge(context.Background(), src, testGate, ownerwire.SeatChallengeParams{Frame: frame(p, true), Credential: w.key.ID})
	wantErr(t, err, ErrNotSeat)
	_, err = w.svc.SeatChallenge(context.Background(), gateSrc, testGate, ownerwire.SeatChallengeParams{Frame: frame(p, true), Credential: "not a key"})
	wantErr(t, err, ErrSeatParams)
	w.noDecisionWasMade(t)

	// The challenge is the one the seat computes from the card it showed.
	ch, err := w.issue(gateSrc, frame(p, true))
	if err != nil {
		t.Fatal(err)
	}
	want, err := ownerwire.DecisionChallenge(testGate.Instance, frame(p, true), ch.Nonce)
	if err != nil || !bytes.Equal(want, ch.Challenge) {
		t.Fatal("the challenge is not the contract's")
	}
	if at, err := record.ParseTime(ch.ExpiresAt); err != nil || !at.Equal(w.clock.Now().Add(ChallengeLife)) {
		t.Fatalf("expires %s %v", ch.ExpiresAt, err)
	}
	if _, err := ownerwire.EncodeSeatChallengeResult(ch); err != nil {
		t.Fatal(err)
	}

	// 32 challenges outstanding, of 32 sessions: the 33rd is refused.
	for i := 1; i < MaxDecisionChallenges; i++ {
		s := Source{Surface: SurfaceSeat, Session: fmt.Sprintf("%016x", i)}
		if _, err := w.issue(s, frame(p, true)); err != nil {
			t.Fatalf("challenge %d: %v", i, err)
		}
	}
	_, err = w.issue(Source{Surface: SurfaceSeat, Session: "ffffffffffffffff"}, frame(p, true))
	wantErr(t, err, ErrChallengesFull)
	// The same tap again replaces.
	if _, err := w.issue(gateSrc, frame(p, true)); err != nil {
		t.Fatal(err)
	}

	// A decision on its way to the door: no challenge.
	w.door.failDecide = errors.New("down")
	if _, err := w.env.decide(p, false); !errors.Is(err, ErrDoorUnavailable) {
		t.Fatal(err)
	}
	_, err = w.issue(gateSrc, frame(p, true))
	wantCode(t, err, record.CodeDecisionInvalid)
	w.door.failDecide = nil
	if err := w.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A card that is not pending: no challenge.
	_, err = w.issue(gateSrc, frame(p, true))
	wantCode(t, err, record.CodeDecisionInvalid)
}

// An approve whose decision record would not fit the record's cap (a
// pending record may flag more paths than a decision record can sign off)
// gets no challenge: it is refused record-too-large before the phone is
// asked, nothing is journaled, and nothing is kept to sign. A deny of the
// same card, which signs off nothing, gets one. On the base the challenge
// was drawn, and the decision was refused only after the passkey.
func TestASeatChallengeIsDrawnOnlyForARecordThatFits(t *testing.T) {
	w := newSeatWorld(t)
	var flagged []record.Flag
	var signs []record.SignOff
	for i := 0; ; i++ {
		path := record.Text("scripts/" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + "/a-rather-long-name-for-a-flagged-path/file.sh")
		flagged = append(flagged, record.Flag{Kind: record.FlagExecConfig, Path: path, Pattern: "scripts"})
		signs = append(signs, record.SignOff{Kind: record.FlagExecConfig, Path: path})
		b, err := ownerwire.EncodeDecisionFrame(ownerwire.DecisionFrame{
			ID: "ap-20261120-0007", BindingSHA256: record.Digest(nil), Decision: record.DecisionApprove, SignedOff: signs,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > record.MaxDecisionBytes-200 {
			break
		}
	}
	w.intake.summary.Flagged = flagged
	p := w.card("feature-a")
	f := frame(p, true)
	if _, err := ownerwire.EncodeSeatChallengeParams(ownerwire.SeatChallengeParams{Frame: f, Credential: w.key.ID}); err != nil {
		t.Fatalf("the params do not fit the wire: the test does not show what it means to: %v", err)
	}
	_, err := w.issue(gateSrc, f)
	wantCode(t, err, record.CodeRecordTooLarge)
	if n := w.svc.seat.size(challengeDecision); n != 0 {
		t.Errorf("%d challenges kept", n)
	}
	w.noDecisionWasMade(t)
	if _, err := w.issue(gateSrc, denyFrame(p, "")); err != nil {
		t.Errorf("a deny of the card: %v", err)
	}
}

// A card decided at the desk after the phone's challenge was drawn is not
// decided again from the phone; the refusal is the desk's, after the
// assertion verified.
func TestASeatDecideAfterTheDeskDecided(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	params := w.signed(frame(p, true))
	if _, err := w.env.decide(p, false); err != nil {
		t.Fatal(err)
	}
	_, err := w.decide(gateSrc, params)
	wantCode(t, err, record.CodeDecisionInvalid)
	if n := len(w.entries(ownerjournal.KindDecision)); n != 1 {
		t.Fatalf("%d decisions", n)
	}
}

// A seat gets nothing by its surface: the desk's Decide refuses it
// not-decider, its beat makes no one present, and a decision from the phone
// is no activity at the desk.
func TestAVerifiedAssertionIsPresenceForItsOneDecisionOnly(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	_, err := w.svc.Decide(context.Background(), gateSrc, frame(p, true))
	wantCode(t, err, record.CodeNotDecider)
	if err := w.svc.Beat(gateSrc); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.SeatDecide(context.Background(), gateSrc, testGate, w.signed(frame(p, true))); err != nil {
		t.Fatal(err)
	}
	w.resetRef()
	q := w.card("feature-b")
	_, err = w.svc.Decide(context.Background(), src, frame(q, true))
	wantCode(t, err, record.CodeOwnerAway)
	refused := w.entries(ownerjournal.KindRefused)
	if len(refused) != 2 {
		t.Fatalf("%d refused entries", len(refused))
	}
	var b struct {
		Surface string `json:"surface"`
	}
	if json.Unmarshal(refused[0].Body, &b) != nil || b.Surface != "seat" {
		t.Fatalf("the seat's refusal at the desk says %s", refused[0].Body)
	}
}

// The service burns a challenge only where the contract says, and the
// errors it answers hold no byte the seat sent.
func TestSeatErrorsHoldNothingTheSeatSent(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	params := w.signed(denyFrame(p, "marker-reason"))
	params.Assertion.ClientDataJSON = []byte(`{"type":"webauthn.get","challenge":"marker","origin":"https://seat.example"}`)
	_, err := w.decide(gateSrc, params)
	wantErr(t, err, ErrAssertionInvalid)
	sum := sha256.Sum256(params.Assertion.ClientDataJSON)
	for _, s := range []string{"marker", string(params.Assertion.CredentialID), fmt.Sprintf("%x", sum), params.ChallengeID} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("the error %q holds %q", err, s)
		}
	}
}

// The clock moves: an entry is good until just before its five minutes.
func TestASeatChallengeLivesFiveMinutes(t *testing.T) {
	w := newSeatWorld(t)
	p := w.card("feature-a")
	params := w.signed(denyFrame(p, ""))
	w.advance(ChallengeLife - time.Second)
	if res, err := w.decide(gateSrc, params); err != nil || res.State != record.StateDenied {
		t.Fatalf("%+v %v", res, err)
	}
}
