package seatlink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/credkind"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// decideNow is a time before the test record's expiry
// (2026-11-21T14:02:04Z) and after it was asked.
var decideNow = time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)

// deskEntry is one decision challenge the fake desk drew.
type deskEntry struct {
	frame      ownerwire.DecisionFrame
	nonce      []byte
	challenge  []byte
	credential string
}

// decideDesk is the daemon's side of the gate for the reads, the login and
// the decisions, as the contract gives it: the reads of fakeReads, the login
// of fakeDesk, and seat.challenge and seat.decide over the same key list,
// verified with the daemon's verifier (credkind). It answers a verified
// decision with outcome.
type decideDesk struct {
	reads *fakeReads
	login *fakeDesk

	mu      sync.Mutex
	entries map[string]deskEntry
	// expired makes the desk refuse a challenge approval-expired.
	expired bool
	// tamper, when set, changes the answer to seat.challenge.
	tamper func(f ownerwire.DecisionFrame, r *ownerwire.SeatChallengeResult)
	// outcome, when set, is what a verified seat.decide answers.
	outcome func(ctx context.Context, f ownerwire.DecisionFrame) (json.RawMessage, error)
	// challenged are the frames seat.challenge was asked for; decided
	// those seat.decide reached the outcome with; decideCalls counts every
	// seat.decide that reached the desk.
	challenged  []ownerwire.DecisionFrame
	decided     []ownerwire.DecisionFrame
	decideCalls int
}

func (d *decideDesk) handle(ctx context.Context, p seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
	switch op {
	case ownerwire.OpList, ownerwire.OpShow, ownerwire.OpPaths:
		return d.reads.handle(ctx, p, op, params)
	case OpLoginChallenge, OpLogin:
		return d.login.handle(ctx, p, op, params)
	case OpDecideChallenge:
		return d.challenge(params)
	case OpDecide:
		return d.decide(ctx, params)
	}
	return nil, seatgate.NewCodedError(codeUnknownOp)
}

// key returns the paired credential of a display id, and whether it is
// revoked.
func (d *decideDesk) key(display string) (credkind.Credential, bool, bool) {
	d.login.mu.Lock()
	defer d.login.mu.Unlock()
	for _, k := range d.login.keys {
		if DisplayID(k.CredentialID) == display {
			return k, d.login.revoked[string(k.CredentialID)], true
		}
	}
	return credkind.Credential{}, false, false
}

func (d *decideDesk) challenge(params json.RawMessage) (json.RawMessage, error) {
	q, err := ownerwire.DecodeSeatChallengeParams(params)
	if err != nil {
		return nil, seatgate.NewCodedError("bad-params")
	}
	d.reads.mu.Lock()
	pending := d.reads.pending
	d.reads.mu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.challenged = append(d.challenged, q.Frame)
	if q.Frame.ID != pending.ID {
		return nil, seatgate.NewCodedError(codeApprovalVoid)
	}
	if d.expired {
		return nil, seatgate.NewCodedError(codeApprovalExpired)
	}
	_, revoked, ok := d.key(q.Credential)
	if !ok {
		return nil, seatgate.NewCodedError(codeKeyUnknown)
	}
	if revoked {
		return nil, seatgate.NewCodedError(ownerwire.CodeKeyRevoked)
	}
	if q.Frame.BindingSHA256 != pending.BindingSHA256 {
		return nil, seatgate.NewCodedError(codeBindingMismatch)
	}
	want := []record.SignOff{}
	if q.Frame.Decision == record.DecisionApprove {
		for _, f := range pending.Binding.Summary.Flagged {
			want = append(want, record.SignOff{Kind: f.Kind, Path: f.Path})
		}
	}
	if ownerwire.SignedOffDigest(want) != ownerwire.SignedOffDigest(q.Frame.SignedOff) {
		return nil, seatgate.NewCodedError(codeDecisionInvalid)
	}
	nonce := make([]byte, 32)
	rand.Read(nonce)
	idb := make([]byte, 8)
	rand.Read(idb)
	ch, err := ownerwire.DecisionChallenge(d.login.instance, q.Frame, nonce)
	if err != nil {
		return nil, seatgate.NewCodedError(codeDecisionInvalid)
	}
	id := hex.EncodeToString(idb)
	d.entries[id] = deskEntry{frame: q.Frame, nonce: nonce, challenge: ch, credential: q.Credential}
	res := ownerwire.SeatChallengeResult{ChallengeID: id, Challenge: append([]byte(nil), ch...), Nonce: nonce,
		ExpiresAt: record.FormatTime(time.Now().Add(5 * time.Minute))}
	if d.tamper != nil {
		d.tamper(q.Frame, &res)
	}
	return ownerwire.EncodeSeatChallengeResult(res)
}

func (d *decideDesk) decide(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	q, err := ownerwire.DecodeSeatDecideParams(params)
	if err != nil {
		return nil, seatgate.NewCodedError("bad-params")
	}
	d.mu.Lock()
	d.decideCalls++
	// The entry is burnt first, whatever comes next.
	e, ok := d.entries[q.ChallengeID]
	delete(d.entries, q.ChallengeID)
	d.mu.Unlock()
	if !ok {
		return nil, seatgate.NewCodedError(ownerwire.CodeChallengeStale)
	}
	if ownerwire.CheckDecisionFrame(q.Frame) != nil {
		return nil, seatgate.NewCodedError("bad-params")
	}
	key, revoked, known := d.key(e.credential)
	switch {
	case !known:
		return nil, seatgate.NewCodedError(codeKeyUnknown)
	case !bytes.Equal(q.Assertion.CredentialID, key.CredentialID):
		return nil, seatgate.NewCodedError(ownerwire.CodeAssertionInvalid)
	case revoked:
		return nil, seatgate.NewCodedError(ownerwire.CodeKeyRevoked)
	}
	again, err := ownerwire.DecisionChallenge(d.login.instance, q.Frame, e.nonce)
	if err != nil || !bytes.Equal(again, e.challenge) {
		return nil, seatgate.NewCodedError(ownerwire.CodeChallengeStale)
	}
	if _, err := credkind.Verify(key, credkind.AssertionInput{
		CredentialID: q.Assertion.CredentialID, ClientDataJSON: q.Assertion.ClientDataJSON,
		AuthenticatorData: q.Assertion.AuthenticatorData, Signature: q.Assertion.Signature,
	}, credkind.Expect{Challenge: e.challenge, Origin: seatOrigin, RPID: seatRPID}); err != nil {
		return nil, seatgate.NewCodedError(ownerwire.CodeAssertionInvalid)
	}
	d.mu.Lock()
	d.decided = append(d.decided, q.Frame)
	outcome := d.outcome
	d.mu.Unlock()
	if outcome != nil {
		return outcome(ctx, q.Frame)
	}
	state := record.StateApproved
	if q.Frame.Decision == record.DecisionDeny {
		state = record.StateDenied
	}
	return ownerwire.EncodeDecideResult(ownerwire.DecideResult{State: state, Line: string(state) + " on the phone; the stand-in door took it"})
}

// counts returns how many challenges and decisions reached the desk.
func (d *decideDesk) counts() (challenges, decides int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.challenged), d.decideCalls
}

// decideRig is a gate that serves the decide desk, a link to it with the
// gate's pin, and a phone whose passkey the desk has paired.
type decideRig struct {
	desk  *decideDesk
	link  *Daemon
	gate  *gate
	seat  seatgate.Identity
	phone *webauthntest.Authenticator
	// credential is the phone's display id.
	credential string
}

func newDecideRig(t *testing.T, caps ...string) *decideRig {
	t.Helper()
	if caps == nil {
		caps = []string{"pair", "read", "login", "decide"}
	}
	p, b := vector(t)
	r := &decideRig{desk: &decideDesk{reads: &fakeReads{pending: p, bytes: b}, entries: map[string]deskEntry{}}}
	seat := newIdentity(t, "seat")
	r.seat = seat
	r.gate = serveGateCaps(t, []string{seat.Fingerprint()}, caps, func(ctx context.Context, p seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
		return r.desk.handle(ctx, p, op, params)
	})
	r.desk.login = newFakeDesk(r.gate.id.Fingerprint())
	phone, err := webauthntest.New(seatRPID, seatOrigin)
	if err != nil {
		t.Fatal(err)
	}
	r.desk.login.pair(t, phone)
	r.phone, r.credential = phone, DisplayID(phone.CredentialID)
	r.link = dialPinned(t, r.gate, seat)
	return r
}

// show reads the card through the link, as the Card page does.
func (r *decideRig) show(t *testing.T) seatapp.Card {
	t.Helper()
	c, err := r.link.Show(context.Background(), r.desk.reads.pending.ID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	return c
}

// shownWhole says that the Card page showed every part of the card whole.
var shownWhole = seatapp.Whole{Rows: true, Lists: true, Refs: true, Flagged: true, Differs: true}

// approve is the request of an approve of the shown card with every flagged
// path signed off, read whole a second ago.
func (r *decideRig) approve() seatapp.DecideChallengeRequest {
	p := r.desk.reads.pending
	return seatapp.DecideChallengeRequest{
		ID: p.ID, BindingSHA256: p.BindingSHA256, Approve: true, SignedOff: []int{0, 1},
		Credential: r.credential, Reading: seatapp.Reading{Opened: decideNow.Add(-time.Second), Whole: shownWhole}, Now: decideNow,
	}
}

// deny is the request of a deny of the shown card with a reason.
func (r *decideRig) deny(reason string) seatapp.DecideChallengeRequest {
	q := r.approve()
	q.Approve, q.SignedOff, q.Reason = false, nil, reason
	return q
}

// sign makes the phone's assertion over a challenge, as the decide request.
func (r *decideRig) sign(t *testing.T, ch seatapp.DecideChallenge) seatapp.DecideRequest {
	t.Helper()
	as, err := r.phone.Get(ch.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	return seatapp.DecideRequest{ChallengeID: ch.ID, Frame: ch.Frame, CredentialID: as.CredentialID,
		AuthenticatorData: as.AuthenticatorData, ClientDataJSON: as.ClientDataJSON, Signature: as.Signature}
}

// TestSeatLinkDecisionChallengeVector: the frame the link builds from the
// card gives the contract's decision vectors (docs/design/seat-gate.md,
// "Test vectors"), for the approve of the test record with its two
// sign-offs and for the deny with the reason "not this one": the gate
// answers the vector's nonce and challenge, and the link, which recomputes
// it from the card it showed, takes it.
func TestSeatLinkDecisionChallengeVector(t *testing.T) {
	instance := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(0x20 + i)
	}
	for _, tc := range []struct {
		name string
		req  func(*decideRig) seatapp.DecideChallengeRequest
		want string
	}{
		{"approve", (*decideRig).approve, "8f232e82c7b8dfac8f89b1587ebd03ea0b1bfa36f683932ade5f796600e5d20c"},
		{"deny", func(r *decideRig) seatapp.DecideChallengeRequest { return r.deny("not this one") }, "f55e409d4b13759dd6c7c4caae52bd1af767a006afd71e446e9995ce37d8184d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDecideRig(t)
			want, _ := hex.DecodeString(tc.want)
			r.desk.tamper = func(_ ownerwire.DecisionFrame, res *ownerwire.SeatChallengeResult) {
				res.Nonce, res.Challenge = nonce, want
			}
			_, c := dial(t, r.gate, r.seat)
			link, err := New(c, WithGatePin(instance))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := link.Show(context.Background(), r.desk.reads.pending.ID); err != nil {
				t.Fatal(err)
			}
			ch, err := link.DecideChallenge(context.Background(), tc.req(r))
			if err != nil {
				t.Fatalf("challenge: %v", err)
			}
			if hex.EncodeToString(ch.Challenge) != tc.want {
				t.Errorf("challenge %x", ch.Challenge)
			}
		})
	}
}

// TestSeatLinkDecideWithASoftwareAuthenticator: an approve and a deny from
// the card the link showed, through the gate to a desk that recomputes the
// challenge and verifies the assertion with the daemon's verifier. The frame
// the desk gets is the card's: its binding, and for the approve the flagged
// paths in the card's order whatever order the owner ticked them in. A
// challenge used once is not taken again.
func TestSeatLinkDecideWithASoftwareAuthenticator(t *testing.T) {
	r := newDecideRig(t)
	r.show(t)
	q := r.approve()
	q.SignedOff = []int{1, 0}
	ch, err := r.link.DecideChallenge(context.Background(), q)
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	p := r.desk.reads.pending
	f := ch.Frame
	if f.ID != p.ID || f.BindingSHA256 != p.BindingSHA256 || !f.Approve || f.Reason != "" || len(f.SignedOff) != 2 ||
		f.SignedOff[0].Path != string(p.Binding.Summary.Flagged[0].Path) || f.SignedOff[1].Path != string(p.Binding.Summary.Flagged[1].Path) {
		t.Errorf("frame %+v", f)
	}
	req := r.sign(t, ch)
	res, err := r.link.Decide(context.Background(), req)
	if err != nil || res.State != "approved" || res.Held || !strings.Contains(res.Line, "approved") {
		t.Fatalf("decide: %+v %v", res, err)
	}
	if _, err := r.link.Decide(context.Background(), req); !errors.Is(err, seatapp.ErrChallengeStale) {
		t.Errorf("the same challenge again: %v", err)
	}

	ch, err = r.link.DecideChallenge(context.Background(), r.deny("not this one"))
	if err != nil {
		t.Fatalf("deny challenge: %v", err)
	}
	if ch.Frame.Approve || len(ch.Frame.SignedOff) != 0 || ch.Frame.Reason != "not this one" {
		t.Errorf("deny frame %+v", ch.Frame)
	}
	if res, err := r.link.Decide(context.Background(), r.sign(t, ch)); err != nil || res.State != "denied" {
		t.Errorf("deny: %+v %v", res, err)
	}

	// An assertion of another key, or over another challenge, is refused by
	// the desk's verifier.
	ch, _ = r.link.DecideChallenge(context.Background(), r.approve())
	other, _ := webauthntest.New(seatRPID, seatOrigin)
	as, _ := other.Get(ch.Challenge)
	if _, err := r.link.Decide(context.Background(), seatapp.DecideRequest{ChallengeID: ch.ID, Frame: ch.Frame, CredentialID: r.phone.CredentialID,
		AuthenticatorData: as.AuthenticatorData, ClientDataJSON: as.ClientDataJSON, Signature: as.Signature}); !errors.Is(err, seatapp.ErrAssertionRefused) {
		t.Errorf("another key's signature: %v", err)
	}
	ch, _ = r.link.DecideChallenge(context.Background(), r.approve())
	stale := ch
	stale.Challenge = bytes.Repeat([]byte{9}, 32)
	if _, err := r.link.Decide(context.Background(), r.sign(t, stale)); !errors.Is(err, seatapp.ErrAssertionRefused) {
		t.Errorf("an assertion over another challenge: %v", err)
	}
}

// TestSeatLinkDecideChallengeIsRecomputed: a challenge the gate answers that
// is not the one the gate's pin, the card the link showed and the daemon's
// nonce give is never handed on, so the phone is never asked to sign it: a
// changed bit, and a challenge drawn for another binding, another decision,
// other sign-offs, another reason, another card or another gate.
func TestSeatLinkDecideChallengeIsRecomputed(t *testing.T) {
	recompute := func(instance string, change func(*ownerwire.DecisionFrame)) func(ownerwire.DecisionFrame, *ownerwire.SeatChallengeResult) {
		return func(f ownerwire.DecisionFrame, res *ownerwire.SeatChallengeResult) {
			change(&f)
			if instance == "" {
				instance = strings.Repeat("ab", 32)
			}
			ch, err := ownerwire.DecisionChallenge(instance, f, res.Nonce)
			if err != nil {
				panic(err)
			}
			res.Challenge = ch
		}
	}
	for name, tamper := range map[string]func(*decideRig) func(ownerwire.DecisionFrame, *ownerwire.SeatChallengeResult){
		"a changed bit": func(*decideRig) func(ownerwire.DecisionFrame, *ownerwire.SeatChallengeResult) {
			return func(_ ownerwire.DecisionFrame, res *ownerwire.SeatChallengeResult) { res.Challenge[31] ^= 1 }
		},
		"another binding": func(r *decideRig) func(ownerwire.DecisionFrame, *ownerwire.SeatChallengeResult) {
			return recompute(r.gate.id.Fingerprint(), func(f *ownerwire.DecisionFrame) { f.BindingSHA256 = strings.Repeat("0", 64) })
		},
		"another decision": func(r *decideRig) func(ownerwire.DecisionFrame, *ownerwire.SeatChallengeResult) {
			return recompute(r.gate.id.Fingerprint(), func(f *ownerwire.DecisionFrame) { f.Decision, f.SignedOff = record.DecisionDeny, []record.SignOff{} })
		},
		"other sign-offs": func(r *decideRig) func(ownerwire.DecisionFrame, *ownerwire.SeatChallengeResult) {
			return recompute(r.gate.id.Fingerprint(), func(f *ownerwire.DecisionFrame) { f.SignedOff = f.SignedOff[:1] })
		},
		"another reason": func(r *decideRig) func(ownerwire.DecisionFrame, *ownerwire.SeatChallengeResult) {
			return recompute(r.gate.id.Fingerprint(), func(f *ownerwire.DecisionFrame) {
				f.Decision, f.SignedOff, f.Reason = record.DecisionDeny, []record.SignOff{}, "x"
			})
		},
		"another card": func(r *decideRig) func(ownerwire.DecisionFrame, *ownerwire.SeatChallengeResult) {
			return recompute(r.gate.id.Fingerprint(), func(f *ownerwire.DecisionFrame) { f.ID = "ap-20261120-0008" })
		},
		"another gate": func(*decideRig) func(ownerwire.DecisionFrame, *ownerwire.SeatChallengeResult) {
			return recompute("", func(*ownerwire.DecisionFrame) {})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newDecideRig(t)
			r.show(t)
			r.desk.tamper = tamper(r)
			ch, err := r.link.DecideChallenge(context.Background(), r.approve())
			if !errors.Is(err, seatapp.ErrChallengeDiffers) || ch.Challenge != nil || ch.ID != "" {
				t.Errorf("%+v %v", ch, err)
			}
			if _, decides := r.desk.counts(); decides != 0 {
				t.Errorf("%d decisions reached the desk", decides)
			}
		})
	}
	// The honest desk's challenge is taken.
	r := newDecideRig(t)
	r.show(t)
	if _, err := r.link.DecideChallenge(context.Background(), r.approve()); err != nil {
		t.Errorf("honest: %v", err)
	}
}

// TestSeatLinkDecideFromTheShownCardOnly: the decision is built from the
// card the link showed, at the binding the Card page showed, by the card
// model's rules; a decision they do not allow is not sent. An expired card
// gets no new challenge.
func TestSeatLinkDecideFromTheShownCardOnly(t *testing.T) {
	r := newDecideRig(t)
	if _, err := r.link.DecideChallenge(context.Background(), r.approve()); !errors.Is(err, seatapp.ErrCardChanged) {
		t.Errorf("a card not shown: %v", err)
	}
	r.show(t)
	for name, tc := range map[string]struct {
		change func(*seatapp.DecideChallengeRequest)
		want   error
		reason string
	}{
		"another binding":      {func(q *seatapp.DecideChallengeRequest) { q.BindingSHA256 = strings.Repeat("0", 64) }, seatapp.ErrCardChanged, ""},
		"no binding":           {func(q *seatapp.DecideChallengeRequest) { q.BindingSHA256 = "" }, seatapp.ErrCardChanged, ""},
		"another card":         {func(q *seatapp.DecideChallengeRequest) { q.ID = "ap-20261120-0008" }, seatapp.ErrCardChanged, ""},
		"a sign-off missing":   {func(q *seatapp.DecideChallengeRequest) { q.SignedOff = []int{0} }, nil, "1 flagged path is " + card.ReasonUnsigned},
		"no sign-off":          {func(q *seatapp.DecideChallengeRequest) { q.SignedOff = nil }, nil, "2 flagged paths are " + card.ReasonUnsigned},
		"a sign-off twice":     {func(q *seatapp.DecideChallengeRequest) { q.SignedOff = []int{0, 0, 1} }, errSignOff, ""},
		"an index past":        {func(q *seatapp.DecideChallengeRequest) { q.SignedOff = []int{0, 1, 2} }, errSignOff, ""},
		"a negative index":     {func(q *seatapp.DecideChallengeRequest) { q.SignedOff = []int{-1, 0, 1} }, errSignOff, ""},
		"a deny with sign-off": {func(q *seatapp.DecideChallengeRequest) { q.Approve = false }, errSignOff, ""},
		"an approve's reason":  {func(q *seatapp.DecideChallengeRequest) { q.Reason = "why" }, errFrame, ""},
		"just opened":          {func(q *seatapp.DecideChallengeRequest) { q.Reading.Opened = q.Now }, nil, card.ReasonJustOpened},
		"never opened":         {func(q *seatapp.DecideChallengeRequest) { q.Reading.Opened = time.Time{} }, nil, card.ReasonNotOpened},
		"expired": {func(q *seatapp.DecideChallengeRequest) {
			q.Now = time.Date(2026, 11, 21, 14, 2, 4, 0, time.UTC)
		}, nil, card.ReasonExpired},
		"an expired deny": {func(q *seatapp.DecideChallengeRequest) {
			q.Approve, q.SignedOff, q.Now = false, nil, time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
		}, nil, card.ReasonExpired},
		"a long reason": {func(q *seatapp.DecideChallengeRequest) {
			q.Approve, q.SignedOff, q.Reason = false, nil, strings.Repeat("x", record.MaxReasonBytes+1)
		}, nil, card.ReasonTooLong},
		"a credential that is no display id": {func(q *seatapp.DecideChallengeRequest) { q.Credential = "x" }, errCredential, ""},
	} {
		q := r.approve()
		tc.change(&q)
		_, err := r.link.DecideChallenge(context.Background(), q)
		var na *seatapp.NotAllowedError
		switch {
		case tc.want != nil && !errors.Is(err, tc.want):
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		case tc.want == nil && (!errors.As(err, &na) || na.Reason != tc.reason):
			t.Errorf("%s: %v, want the reason %q", name, err, tc.reason)
		}
	}
	if challenges, _ := r.desk.counts(); challenges != 0 {
		t.Errorf("%d challenges were asked of the desk", challenges)
	}
	// A deny needs no reading and no sign-off.
	q := r.deny("")
	q.Reading = seatapp.Reading{}
	if _, err := r.link.DecideChallenge(context.Background(), q); err != nil {
		t.Errorf("a deny of a flagged card with nothing ticked: %v", err)
	}
	// The desk refuses an expired card as well: a new challenge does not
	// renew it.
	r.desk.expired = true
	if _, err := r.link.DecideChallenge(context.Background(), r.approve()); !errors.Is(err, seatapp.ErrCardExpired) {
		t.Errorf("the desk's expiry: %v", err)
	}
}

// truncated makes the rig's record a card whose path list is cut, of two
// pages.
func truncated(t *testing.T, r *decideRig) {
	t.Helper()
	p := r.desk.reads.pending
	p.Binding.Summary.PathsTruncated = true
	p.Binding.Summary.Files = 7
	digest, err := record.BindingDigest(p.Binding)
	if err != nil {
		t.Fatal(err)
	}
	p.BindingSHA256 = digest
	b, err := record.EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	r.desk.reads.pending, r.desk.reads.bytes = p, b
}

// TestSeatLinkOffer: the Card page is offered an approve when the card
// model's MayApprove allows it with every flagged path signed off, and a
// deny when its Deny allows one; neither for a card the link did not show,
// for one past its expiry, or for one that ended; and on a card whose path
// list is cut, the approve only once every page of the whole list was read.
func TestSeatLinkOffer(t *testing.T) {
	r := newDecideRig(t)
	p := r.desk.reads.pending
	read := seatapp.Reading{Opened: decideNow, Whole: shownWhole}
	if o := r.link.Offer(p.ID, p.BindingSHA256, read, decideNow); o.Approve || o.Deny {
		t.Errorf("a card not shown: %+v", o)
	}
	r.show(t)
	if o := r.link.Offer(p.ID, p.BindingSHA256, read, decideNow); !o.Approve || !o.Deny || o.ApproveWhy != "" {
		t.Errorf("a card read: %+v", o)
	}
	if o := r.link.Offer(p.ID, strings.Repeat("0", 64), read, decideNow); o.Approve || o.Deny {
		t.Errorf("another binding: %+v", o)
	}
	late := time.Date(2026, 11, 21, 14, 2, 4, 0, time.UTC)
	if o := r.link.Offer(p.ID, p.BindingSHA256, read, late); o.Approve || o.Deny || o.ApproveWhy != card.ReasonExpired {
		t.Errorf("past its expiry: %+v", o)
	}
	r.desk.reads.show, _ = ownerwire.EncodeShowResult(ownerwire.ShowResult{Record: ownerwire.NewRecord(r.desk.reads.bytes),
		Outcome: &ownerwire.Outcome{State: record.StateApproved, Line: "approved at the desk"}})
	r.show(t)
	if o := r.link.Offer(p.ID, p.BindingSHA256, read, decideNow); o.Approve || o.Deny {
		t.Errorf("an ended card: %+v", o)
	}

	r = newDecideRig(t)
	truncated(t, r)
	r.show(t)
	p = r.desk.reads.pending
	if o := r.link.Offer(p.ID, p.BindingSHA256, read, decideNow); o.Approve || o.ApproveWhy != card.ReasonPathPages || !o.Deny {
		t.Errorf("a cut list, no page read: %+v", o)
	}
	read.Pages, read.PagesRead, read.Review = 2, []int{1}, p.Binding.Summary.ReviewDigest
	if o := r.link.Offer(p.ID, p.BindingSHA256, read, decideNow); o.Approve {
		t.Errorf("a cut list, one page of two read: %+v", o)
	}
	read.PagesRead = []int{1, 0}
	if o := r.link.Offer(p.ID, p.BindingSHA256, read, decideNow); !o.Approve {
		t.Errorf("a cut list, every page read: %+v", o)
	}
	// The challenge asks the same: every page read.
	q := r.approve()
	q.BindingSHA256 = p.BindingSHA256
	if _, err := r.link.DecideChallenge(context.Background(), q); err == nil {
		t.Error("a challenge for a cut list with no page read")
	}
	q.Reading.Pages, q.Reading.PagesRead, q.Reading.Review = 2, []int{0, 1}, strings.Repeat("e", 64)
	if _, err := r.link.DecideChallenge(context.Background(), q); err == nil {
		t.Error("a challenge for a cut list whose pages were read for another review")
	}
	if o := r.link.Offer(p.ID, p.BindingSHA256, q.Reading, decideNow); o.Approve {
		t.Errorf("a cut list whose pages were read for another review: %+v", o)
	}
	q.Reading.Review = p.Binding.Summary.ReviewDigest
	if _, err := r.link.DecideChallenge(context.Background(), q); err != nil {
		t.Errorf("a challenge for a cut list read whole: %v", err)
	}
}

// longFlagged makes the rig's record a card whose two flagged paths are
// longer than the Card page shows and differ only past the cut, so that
// their labels on the page are the same.
func longFlagged(t *testing.T, r *decideRig) {
	t.Helper()
	p := r.desk.reads.pending
	long := strings.Repeat("a", 1024)
	s := p.Binding.Summary
	s.Paths[0].Path, s.Paths[1].Path = record.Text(long+"/one"), record.Text(long+"/two")
	s.Flagged[0] = record.Flag{Kind: record.FlagExecConfig, Path: record.Text(long + "/one")}
	s.Flagged[1] = record.Flag{Kind: record.FlagExecConfig, Path: record.Text(long + "/two")}
	digest, err := record.BindingDigest(p.Binding)
	if err != nil {
		t.Fatal(err)
	}
	p.BindingSHA256 = digest
	b, err := record.EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	r.desk.reads.pending, r.desk.reads.bytes = p, b
}

// TestSeatLinkCutPartsAreNotRead: a part of the card the Card page did not
// show whole counts as not read, and Approve is neither offered nor sent for
// it, with the host's line that says which part. Two flagged paths whose
// labels on the page are the same (they differ only past the cut) are not
// signed off by their index: the desk is asked no challenge. Deny is still
// offered. Each part, cut alone, withholds Approve.
func TestSeatLinkCutPartsAreNotRead(t *testing.T) {
	r := newDecideRig(t)
	longFlagged(t, r)
	c := r.show(t)
	if a, b := fmtLead(c.Flagged[0].Path), fmtLead(c.Flagged[1].Path); a != b {
		t.Fatalf("the labels differ: the test does not show what it means to")
	}
	p := r.desk.reads.pending
	cut := shownWhole
	cut.Flagged = false
	read := seatapp.Reading{Opened: decideNow.Add(-time.Second), Whole: cut}
	if o := r.link.Offer(p.ID, p.BindingSHA256, read, decideNow); o.Approve || o.ApproveWhy != reasonCutFlagged || !o.Deny {
		t.Errorf("a cut flagged path: %+v", o)
	}
	q := r.approve()
	q.BindingSHA256, q.Reading = p.BindingSHA256, read
	var na *seatapp.NotAllowedError
	if _, err := r.link.DecideChallenge(context.Background(), q); !errors.As(err, &na) || na.Reason != reasonCutFlagged {
		t.Errorf("an approve of cut flagged paths: %v", err)
	}
	if n, _ := r.desk.counts(); n != 0 {
		t.Errorf("the desk was asked %d challenges", n)
	}
	q.Approve, q.SignedOff = false, nil
	if _, err := r.link.DecideChallenge(context.Background(), q); err != nil {
		t.Errorf("a deny of a card with cut flagged paths: %v", err)
	}

	// Each part alone, on the test card (which has refs, rows, lists and
	// flagged paths, and here a "differs from" block).
	r = newDecideRig(t)
	r.show(t)
	p = r.desk.reads.pending
	c2, _ := r.link.shown.get(p.ID)
	c2.Differs = &card.Differs{EarlierID: "ap-20261119-0001"}
	r.link.shown.put(p.ID, c2)
	for _, tc := range []struct {
		off  func(*seatapp.Whole)
		want string
	}{
		{func(w *seatapp.Whole) { w.Refs = false }, reasonCutRefs},
		{func(w *seatapp.Whole) { w.Rows = false }, reasonCutRows},
		{func(w *seatapp.Whole) { w.Lists = false }, reasonCutLists},
		{func(w *seatapp.Whole) { w.Flagged = false }, reasonCutFlagged},
		{func(w *seatapp.Whole) { w.Differs = false }, reasonCutDiffers},
	} {
		w := shownWhole
		tc.off(&w)
		if o := r.link.Offer(p.ID, p.BindingSHA256, seatapp.Reading{Opened: decideNow.Add(-time.Second), Whole: w}, decideNow); o.Approve || o.ApproveWhy != tc.want {
			t.Errorf("%s: %+v", tc.want, o)
		}
	}
	if o := r.link.Offer(p.ID, p.BindingSHA256, seatapp.Reading{Opened: decideNow.Add(-time.Second), Whole: shownWhole}, decideNow); !o.Approve {
		t.Errorf("every part whole: %+v", o)
	}
	// The card model's reading has the parts not shown whole as not read,
	// whatever else holds Approve back.
	if rd := readingOf(c2, seatapp.Reading{Opened: decideNow}); rd.Paths || rd.Refs || rd.Differs || rd.Lists != [card.NumLists]bool{} {
		t.Errorf("a reading with nothing whole counts as read: %+v", rd)
	}
	if rd := readingOf(c2, seatapp.Reading{Opened: decideNow, Whole: shownWhole}); !rd.Paths || !rd.Refs || !rd.Differs || !rd.Lists[card.NumLists-1] {
		t.Errorf("a reading with everything whole: %+v", rd)
	}
}

// ampFlagged makes the rig's record a card whose four flagged paths are
// mostly '&', n of them each: each well under the page's cut, the decision
// frame under its cap, and each '&' five bytes longer once escaped for HTML.
func ampFlagged(t *testing.T, r *decideRig, n int) {
	t.Helper()
	p := r.desk.reads.pending
	s := p.Binding.Summary
	s.Flagged = nil
	for i := 0; i < 4; i++ {
		path := record.Text("d" + strconv.Itoa(i) + "/" + strings.Repeat("&", n))
		s.Paths[i].Path = path
		s.Flagged = append(s.Flagged, record.Flag{Kind: record.FlagExecConfig, Path: path})
	}
	digest, err := record.BindingDigest(p.Binding)
	if err != nil {
		t.Fatal(err)
	}
	p.BindingSHA256 = digest
	b, err := record.EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	r.desk.reads.pending, r.desk.reads.bytes = p, b
}

// TestSeatLinkDecisionOnTheWire: the transport marshals the params again,
// escaping & as \u0026, so the link checks the bytes the gate will read. A
// frame of flagged paths full of & that the wire's encoder takes (3931
// bytes against the cap of 9216) but that is over the cap once escaped is
// ErrTooLarge, and nothing reaches the gate; on the base it reached the gate
// and was refused bad-params, a refusal the gate counts. One that fits once
// escaped is decided, with the paths as they are. The bytes the link hands
// the transport are a fixed point of its marshal.
func TestSeatLinkDecisionOnTheWire(t *testing.T) {
	r := newDecideRig(t)
	ampFlagged(t, r, 900)
	r.show(t)
	q := r.approve()
	q.BindingSHA256, q.SignedOff = r.desk.reads.pending.BindingSHA256, []int{0, 1, 2, 3}
	c, _ := r.link.shown.get(q.ID)
	f, err := frameOf(c, q)
	if err != nil {
		t.Fatal(err)
	}
	params, err := ownerwire.EncodeSeatChallengeParams(ownerwire.SeatChallengeParams{Frame: f, Credential: r.credential})
	if err != nil || len(params) > ownerwire.MaxSeatChallengeParamsBytes {
		t.Fatalf("the params are %d bytes (%v): the test does not show what it means to", len(params), err)
	}
	before := r.gate.calls.Load()
	if _, err := r.link.DecideChallenge(context.Background(), q); !errors.Is(err, seatapp.ErrTooLarge) {
		t.Errorf("params over the cap on the wire: %v, want %v", err, seatapp.ErrTooLarge)
	}
	if n := r.gate.calls.Load() - before; n != 0 {
		t.Errorf("%d calls reached the gate", n)
	}

	r = newDecideRig(t)
	ampFlagged(t, r, 200)
	r.show(t)
	q = r.approve()
	q.BindingSHA256, q.SignedOff = r.desk.reads.pending.BindingSHA256, []int{0, 1, 2, 3}
	ch, err := r.link.DecideChallenge(context.Background(), q)
	if err != nil {
		t.Fatalf("params that fit on the wire: %v", err)
	}
	if _, err := r.link.Decide(context.Background(), r.sign(t, ch)); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got := r.desk.decided[0].SignedOff; len(got) != 4 || string(got[0].Path) != "d0/"+strings.Repeat("&", 200) {
		t.Errorf("the desk got the sign-offs %d", len(got))
	}

	wire, err := onWire([]byte(`{"a":"<&>\u2028"}`), func([]byte) error { return nil })
	again, _ := json.Marshal(wire)
	if err != nil || !bytes.Equal(again, wire) || string(wire) != `{"a":"\u003c\u0026\u003e\u2028"}` {
		t.Errorf("onWire gives %s (%v), marshalled again %s", wire, err, again)
	}
}

// TestSeatLinkDecideCodes: the gate's refusals of a challenge and of a
// decision map to the app's errors, and an answer to a decision that leaves
// its outcome unknown is ErrOutcomeUnknown, never a refusal.
func TestSeatLinkDecideCodes(t *testing.T) {
	refusals := map[string]error{
		"challenge-stale":   seatapp.ErrChallengeStale,
		"binding-mismatch":  seatapp.ErrCardChanged,
		"review-changed":    seatapp.ErrCardChanged,
		"approval-expired":  seatapp.ErrCardExpired,
		"approval-consumed": seatapp.ErrCannotDecide,
		"approval-void":     seatapp.ErrCannotDecide,
		"previous-epoch":    seatapp.ErrCannotDecide,
		"decision-invalid":  seatapp.ErrCannotDecide,
		"assertion-invalid": seatapp.ErrAssertionRefused,
		"key-revoked":       seatapp.ErrKeyRevoked,
		"key-unknown":       seatapp.ErrKeyRevoked,
		"kind-refused":      seatapp.ErrKindRefused,
		"challenges-full":   seatapp.ErrBusy,
		"rate-limited":      seatapp.ErrBusy,
		"busy":              seatapp.ErrBusy,
		"too-many-refusals": seatapp.ErrBusy,
		"unknown-op":        seatapp.ErrNotOffered,
	}
	for code, want := range refusals {
		t.Run(code, func(t *testing.T) {
			r := newDecideRig(t)
			r.show(t)
			ch, err := r.link.DecideChallenge(context.Background(), r.approve())
			if err != nil {
				t.Fatal(err)
			}
			r.desk.outcome = func(context.Context, ownerwire.DecisionFrame) (json.RawMessage, error) {
				return nil, seatgate.NewCodedError(code)
			}
			if _, err := r.link.Decide(context.Background(), r.sign(t, ch)); !errors.Is(err, want) {
				t.Errorf("decide: %v, want %v", err, want)
			}
		})
	}
	// The challenge maps the same codes.
	for code, want := range refusals {
		r := newDecideRig(t)
		g := serveGateCaps(t, []string{r.seat.Fingerprint()}, []string{"read", "decide"}, func(ctx context.Context, p seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
			if op == ownerwire.OpShow {
				return r.desk.handle(ctx, p, op, params)
			}
			return nil, seatgate.NewCodedError(code)
		})
		link := dialPinned(t, g, r.seat)
		if _, err := link.Show(context.Background(), r.desk.reads.pending.ID); err != nil {
			t.Fatal(err)
		}
		q := r.approve()
		if _, err := link.DecideChallenge(context.Background(), q); !errors.Is(err, want) {
			t.Errorf("challenge %s: %v, want %v", code, err, want)
		}
	}
	// After a decision was sent, these leave the outcome unknown; any other
	// code is a refusal the app does not tell apart.
	for _, code := range []string{"unavailable", "deadline-exceeded", "door-unavailable", "journal-reopened", "failed", "shutting-down"} {
		r := newDecideRig(t)
		r.show(t)
		ch, _ := r.link.DecideChallenge(context.Background(), r.approve())
		r.desk.outcome = func(context.Context, ownerwire.DecisionFrame) (json.RawMessage, error) {
			return nil, seatgate.NewCodedError(code)
		}
		_, err := r.link.Decide(context.Background(), r.sign(t, ch))
		var re *RefusedError
		if code == "shutting-down" {
			if !errors.As(err, &re) || re.Code != code {
				t.Errorf("%s: %v", code, err)
			}
			continue
		}
		if !errors.Is(err, seatapp.ErrOutcomeUnknown) {
			t.Errorf("%s: %v, want the outcome unknown", code, err)
		}
	}
}

// TestSeatLinkDecideLostAnswer: once a decision is sent, a link that drops,
// the seat's own deadline, and an answer not of its form leave the outcome
// unknown, and nothing is sent again; a gate that is not connected before
// the decision is sent is unavailable, and nothing is sent.
func TestSeatLinkDecideLostAnswer(t *testing.T) {
	r := newDecideRig(t)
	r.show(t)
	ch, _ := r.link.DecideChallenge(context.Background(), r.approve())
	r.desk.outcome = func(ctx context.Context, f ownerwire.DecisionFrame) (json.RawMessage, error) {
		go r.gate.stop()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, err := r.link.Decide(context.Background(), r.sign(t, ch)); !errors.Is(err, seatapp.ErrOutcomeUnknown) {
		t.Errorf("a dropped link: %v", err)
	}
	if _, decides := r.desk.counts(); decides != 1 {
		t.Errorf("%d decisions reached the desk", decides)
	}
	// The gate is gone: nothing is sent, and the phone is told so.
	if _, err := r.link.Decide(context.Background(), r.sign(t, ch)); !errors.Is(err, seatapp.ErrDaemonUnavailable) {
		t.Errorf("no gate: %v", err)
	}
	if _, err := r.link.DecideChallenge(context.Background(), r.approve()); !errors.Is(err, seatapp.ErrDaemonUnavailable) {
		t.Errorf("no gate, challenge: %v", err)
	}
	if _, decides := r.desk.counts(); decides != 1 {
		t.Errorf("%d decisions reached the desk", decides)
	}

	r = newDecideRig(t)
	r.show(t)
	ch, _ = r.link.DecideChallenge(context.Background(), r.approve())
	release := make(chan struct{})
	defer close(release)
	r.desk.outcome = func(ctx context.Context, f ownerwire.DecisionFrame) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, seatgate.NewCodedError("failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := r.link.Decide(ctx, r.sign(t, ch)); !errors.Is(err, seatapp.ErrOutcomeUnknown) {
		t.Errorf("the seat's own deadline: %v", err)
	}

	r = newDecideRig(t)
	r.show(t)
	ch, _ = r.link.DecideChallenge(context.Background(), r.approve())
	r.desk.outcome = func(context.Context, ownerwire.DecisionFrame) (json.RawMessage, error) {
		return json.RawMessage(`{"state":"approved","line":"x","held":true}`), nil
	}
	if _, err := r.link.Decide(context.Background(), r.sign(t, ch)); !errors.Is(err, seatapp.ErrOutcomeUnknown) {
		t.Errorf("an answer not of its form: %v", err)
	}
	// A held approve is an answer: pending, held, with the host's line.
	ch, _ = r.link.DecideChallenge(context.Background(), r.approve())
	r.desk.outcome = func(context.Context, ownerwire.DecisionFrame) (json.RawMessage, error) {
		return ownerwire.EncodeDecideResult(ownerwire.DecideResult{State: record.StatePending, Line: "held: the limit for today is reached", Held: true})
	}
	if res, err := r.link.Decide(context.Background(), r.sign(t, ch)); err != nil || !res.Held || res.State != "pending" {
		t.Errorf("held: %+v %v", res, err)
	}
}

// TestSeatLinkDecideNeedsTheCapability: a gate that does not list "decide"
// is asked nothing, and neither is one the link has no pin for.
func TestSeatLinkDecideNeedsTheCapability(t *testing.T) {
	r := newDecideRig(t, "pair", "read", "login")
	r.show(t)
	before := r.gate.calls.Load()
	if _, err := r.link.DecideChallenge(context.Background(), r.approve()); !errors.Is(err, seatapp.ErrNotOffered) {
		t.Errorf("challenge: %v", err)
	}
	if _, err := r.link.Decide(context.Background(), seatapp.DecideRequest{}); !errors.Is(err, seatapp.ErrNotOffered) {
		t.Errorf("decide: %v", err)
	}
	if r.gate.calls.Load() != before {
		t.Error("the gate was asked")
	}
	r = newDecideRig(t)
	_, c := dial(t, r.gate, r.seat)
	bare, _ := New(c)
	before = r.gate.calls.Load()
	if _, err := bare.DecideChallenge(context.Background(), r.approve()); !errors.Is(err, errNoPin) {
		t.Errorf("no pin: %v", err)
	}
	if r.gate.calls.Load() != before {
		t.Error("a link without the pin asked the gate")
	}
}
