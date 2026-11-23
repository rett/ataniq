package ownerjournal

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/credkind"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/webauthntest"
)

// The seat of these tests: its relying party, its origin, the gate's own key
// fingerprint and the gate session (the first 16 digits of the seat's pin).
const (
	seatRPID     = "seat.example"
	seatOrigin   = "https://seat.example"
	seatInstance = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	seatSession  = "a1b2c3d4e5f60718"
)

func newAuthenticator(t *testing.T) *webauthntest.Authenticator {
	t.Helper()
	a, err := webauthntest.New(seatRPID, seatOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// phoneDecision is a decision made on the phone: the record confirmed by
// passkey, and the evidence of the assertion that confirmed it, made by the
// authenticator over the challenge of the frame, as the daemon would draw it.
type phoneDecision struct {
	record   []byte
	evidence SeatEvidence
}

// passkeyRecord makes a phone decision for the pending id: an approve with no
// sign-off (the pending records here are prunes, which flag nothing), or a
// deny with a reason.
func passkeyRecord(t *testing.T, a *webauthntest.Authenticator, id, project, binding string, approve bool, at time.Time) phoneDecision {
	t.Helper()
	frame := ownerwire.DecisionFrame{ID: id, BindingSHA256: binding, Decision: record.DecisionDeny, SignedOff: []record.SignOff{}, Reason: "not from here <&>"}
	if approve {
		frame.Decision, frame.Reason = record.DecisionApprove, ""
	}
	nonce := bytes.Repeat([]byte{0xab, byte(at.Unix())}, 16)
	challenge, err := ownerwire.DecisionChallenge(seatInstance, frame, nonce)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Get(challenge)
	if err != nil {
		t.Fatal(err)
	}
	ev := SeatEvidence{
		CredentialID: got.CredentialID, AuthenticatorData: got.AuthenticatorData,
		ClientDataJSON: got.ClientDataJSON, Signature: got.Signature,
		Nonce: nonce, Instance: seatInstance, Origin: seatOrigin,
	}
	return phoneDecision{record: sealPasskey(t, frame, project, at, ev, "0011223344556677"), evidence: ev}
}

// sealPasskey writes the decision record of a frame confirmed by the
// assertion of the evidence.
func sealPasskey(t *testing.T, f ownerwire.DecisionFrame, project string, at time.Time, ev SeatEvidence, challengeID string) []byte {
	t.Helper()
	d := record.Decision{
		Schema:        record.SchemaDecision,
		ID:            f.ID,
		Planner:       "planner-a",
		Decision:      f.Decision,
		BindingSHA256: f.BindingSHA256,
		Project:       project,
		Subject:       record.Subject{Kind: record.SubjectSeat, RoleName: "role-a", Epoch: 7, SeatRev: 12},
		PlanToken:     "h-3fa9c2e1",
		SignedOff:     f.SignedOff,
		Reason:        f.Reason,
		DecidedAt:     record.FormatTime(at),
		ExpiresAt:     record.FormatTime(at.Add(30 * time.Minute)),
		Confirmed:     record.ConfirmedPasskey,
		Seat: record.SeatBlock{
			AssertionSHA256: record.AssertionDigest(ev.CredentialID, ev.AuthenticatorData, ev.ClientDataJSON, ev.Signature),
			ChallengeID:     challengeID,
			Credential:      KeyDisplayID(ev.CredentialID),
			Session:         seatSession,
		},
	}
	if f.Decision == record.DecisionApprove {
		d.Uses = 1
	}
	b, err := record.EncodeDecision(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// audit is what an auditor does with the journal and the key list: it reads
// the record and the evidence back, recomputes the challenge from the record,
// the nonce and the instance, checks the signature with the credential's
// public key from the list, and matches the record's assertion digest.
func audit(t *testing.T, what string, rec []byte, ev SeatEvidence, keys []StoredKey) {
	t.Helper()
	d, err := record.DecodeDecision(rec)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if got := record.AssertionDigest(ev.CredentialID, ev.AuthenticatorData, ev.ClientDataJSON, ev.Signature); got != d.Seat.AssertionSHA256 {
		t.Fatalf("%s: the evidence digests to %s, the record names %s", what, got, d.Seat.AssertionSHA256)
	}
	var key *StoredKey
	for i := range keys {
		if keys[i].ID == d.Seat.Credential {
			key = &keys[i]
		}
	}
	if key == nil {
		t.Fatalf("%s: the credential %s is not on the list", what, d.Seat.Credential)
	}
	frame := ownerwire.DecisionFrame{ID: d.ID, BindingSHA256: d.BindingSHA256, Decision: d.Decision, SignedOff: d.SignedOff, Reason: d.Reason}
	challenge, err := ownerwire.DecisionChallenge(ev.Instance, frame, ev.Nonce)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	host := strings.TrimPrefix(ev.Origin, "https://")
	cred := credkind.Credential{Kind: key.Kind, CredentialID: key.CredentialID, SPKI: key.PublicKey, AAGUID: key.AAGUID,
		BackupEligible: key.BackupEligible, BackupState: key.BackupState, UserVerified: true, SignCount: key.Counter}
	in := credkind.AssertionInput{CredentialID: ev.CredentialID, ClientDataJSON: ev.ClientDataJSON, AuthenticatorData: ev.AuthenticatorData, Signature: ev.Signature}
	if _, err := credkind.Verify(cred, in, credkind.Expect{Challenge: challenge, Origin: ev.Origin, RPID: host}); err != nil {
		t.Fatalf("%s: the signature does not check again: %v", what, err)
	}
	// The same evidence does not check against another decision: another
	// reason, or the other answer.
	frame.Reason += "x"
	if frame.Decision == record.DecisionApprove {
		frame.Decision, frame.Reason, frame.SignedOff = record.DecisionDeny, "", nil
	}
	other, err := ownerwire.DecisionChallenge(ev.Instance, frame, ev.Nonce)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if _, err := credkind.Verify(cred, in, credkind.Expect{Challenge: other, Origin: ev.Origin, RPID: host}); !errors.Is(err, credkind.ErrChallenge) {
		t.Fatalf("%s: the evidence checks against another decision: %v", what, err)
	}
}

// Approve, deny and held, decided on the phone, through the real journal:
// each activity entry says the surface seat, the seat block's session and no
// desk activity, the evidence is kept beside the decision, and both read back
// the same after the store is reopened, where the signature checks again
// against the key on the list.
func TestPhoneDecisionsThroughTheJournal(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	keyPath := newKeyPath(t)
	k := mustOpenKeys(t, keyPath)
	a := newAuthenticator(t)
	if _, err := k.AddKey(NewKey{CredentialID: a.CredentialID, PublicKey: a.SPKI(), Kind: credkind.NameUnknown, PairedAt: base.Add(-time.Hour), Words: keyWords}); err != nil {
		t.Fatal(err)
	}

	pendingA, bindingA := pendingRecord(t, "ap-approve", "project-a", base.Add(time.Hour))
	approve := passkeyRecord(t, a, "ap-approve", "project-a", bindingA, true, base)
	_, bindingD := pendingRecord(t, "ap-deny", "project-a", base.Add(time.Hour))
	deny := passkeyRecord(t, a, "ap-deny", "project-a", bindingD, false, base.Add(time.Minute))
	pendingH, bindingH := pendingRecord(t, "ap-held", "project-a", base.Add(time.Hour))
	held := passkeyRecord(t, a, "ap-held", "project-a", bindingH, true, base.Add(2*time.Minute))
	_ = pendingA

	dA, err := s.AppendDecision(approve.record, Seen{ShownSHA256: shown, Evidence: &approve.evidence})
	if err != nil {
		t.Fatalf("the approve: %v", err)
	}
	dD, err := s.AppendDecision(deny.record, Seen{Evidence: &deny.evidence})
	if err != nil {
		t.Fatalf("the deny: %v", err)
	}
	card, err := s.AppendHeld(held.record, pendingH, Seen{Evidence: &held.evidence})
	if err != nil {
		t.Fatalf("the held approve: %v", err)
	}

	type decided struct {
		seq   int64
		at    time.Time
		shown string
		phone phoneDecision
	}
	all := map[string]decided{
		"approve": {dA.Seq(), base, shown, approve},
		"deny":    {dD.Seq(), base.Add(time.Minute), "", deny},
		"held":    {card.Seq, base.Add(2 * time.Minute), "", held},
	}
	check := func(when string, s *Store, keys []StoredKey) {
		t.Helper()
		for name, d := range all {
			what := when + ": " + name
			act, err := s.ActivityOf(d.seq)
			want := Activity{Ref: d.seq, At: d.at, Surface: SurfaceSeat, Session: seatSession, ShownSHA256: d.shown}
			if err != nil || !reflect.DeepEqual(act, want) {
				t.Errorf("%s: the activity is %+v, %v; want %+v", what, act, err, want)
			}
			ev, err := s.EvidenceOf(d.seq)
			if err != nil || !reflect.DeepEqual(ev, d.phone.evidence) {
				t.Errorf("%s: the evidence reads back as %+v, %v", what, ev, err)
				continue
			}
			audit(t, what, d.phone.record, ev, keys)
		}
	}
	check("open", s, mustKeys(t, k))

	// The bodies hold the evidence, split as the doc says: the decision's
	// what was signed, the activity's who signed it and where from.
	entries := mustEntries(t, s)
	if len(entries) != 6 {
		t.Fatalf("%d entries, want 6", len(entries))
	}
	b64 := base64.StdEncoding.EncodeToString
	ev := approve.evidence
	wantDecision := `{"approve":true,"binding_sha256":"` + bindingA + `","client_data_json":"` + b64(ev.ClientDataJSON) +
		`","nonce":"` + hex.EncodeToString(ev.Nonce) + `","signature":"` + b64(ev.Signature) + `"}`
	if got := string(entries[0].Body); got != wantDecision {
		t.Errorf("the approve's body is\n%s\nwant\n%s", got, wantDecision)
	}
	wantActivity := `{"authenticator_data":"` + b64(ev.AuthenticatorData) + `","credential_id":"` + b64(ev.CredentialID) +
		`","instance":"` + seatInstance + `","last_activity":"","origin":"` + seatOrigin + `","session":"` + seatSession +
		`","shown_sha256":"` + shown + `","surface":"seat"}`
	if got := string(entries[1].Body); got != wantActivity {
		t.Errorf("the approve's activity body is\n%s\nwant\n%s", got, wantActivity)
	}
	if !bytes.Equal(entries[0].Record, approve.record) {
		t.Error("the approve's record is not kept byte for byte")
	}

	// A phone approve is a card use like any other, and a deny is not.
	uses, err := s.CardUses("project-a", base.Add(-time.Hour))
	if err != nil || len(uses) != 1 || !uses[0].Equal(base) {
		t.Errorf("the card uses are %v, %v", uses, err)
	}

	mustClose(t, s)
	mustClose2(t, k)
	s = mustOpen(t, path)
	k = mustOpenKeys(t, keyPath)
	check("reopened", s, mustKeys(t, k))
	// The replay at a restart hands the phone decisions over again, byte for
	// byte, and shows the held card.
	undelivered, err := s.Undelivered()
	if err != nil || len(undelivered) != 2 {
		t.Fatalf("undelivered: %v, %v", seqsOf(undelivered), err)
	}
	for i, want := range [][]byte{approve.record, deny.record} {
		if !bytes.Equal(undelivered[i].Bytes(), want) {
			t.Errorf("undelivered decision %d is not the record that was journaled", i)
		}
	}
	cards, err := s.HeldCards("project-a")
	if err != nil || len(cards) != 1 || cards[0].Seq != card.Seq || !bytes.Equal(cards[0].Decision, held.record) {
		t.Fatalf("held cards after the reopen: %+v, %v", cards, err)
	}
}

// A decision confirmed by code keeps its activity from the console block and
// its bodies as they were, and has no evidence.
func TestADeskDecisionKeepsItsActivityAndHasNoEvidence(t *testing.T) {
	s := mustOpen(t, newPath(t))
	approve, _ := approveOf(t, "ap-one", "project-a", base)
	d, err := s.AppendDecision(approve, Seen{LastActivity: base.Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	entries := mustEntries(t, s)
	rec, _ := record.DecodeDecision(approve)
	if got, want := string(entries[0].Body), `{"approve":true,"binding_sha256":"`+rec.BindingSHA256+`"}`; got != want {
		t.Errorf("the decision's body is %s, want %s", got, want)
	}
	if got, want := string(entries[1].Body), `{"last_activity":"2026-11-20T13:59:00Z","session":"0123456789abcdef","shown_sha256":"","surface":"console"}`; got != want {
		t.Errorf("the activity's body is %s, want %s", got, want)
	}
	if _, err := s.EvidenceOf(d.Seq()); !errors.Is(err, ErrNoEvidence) {
		t.Errorf("the evidence of a desk decision: %v, want ErrNoEvidence", err)
	}
	for _, seq := range []int64{2, 99} {
		if _, err := s.EvidenceOf(seq); err == nil || errors.Is(err, ErrNoEvidence) {
			t.Errorf("EvidenceOf(%d): %v, want a refusal for an entry that is no decision", seq, err)
		}
	}
}

// A decision confirmed by passkey is journaled only with the evidence of the
// assertion its record names, and with no desk activity; a decision
// confirmed by code only without evidence. A refusal writes nothing.
func TestAPhoneDecisionNeedsItsOwnEvidence(t *testing.T) {
	s := mustOpen(t, newPath(t))
	a := newAuthenticator(t)
	pending, binding := pendingRecord(t, "ap-one", "project-a", base.Add(time.Hour))
	good := passkeyRecord(t, a, "ap-one", "project-a", binding, true, base)
	other := passkeyRecord(t, a, "ap-one", "project-a", binding, true, base.Add(time.Second))
	desk, _ := approveOf(t, "ap-one", "project-a", base)

	// A record whose seat block names the assertion of the evidence but
	// another credential.
	wrongCred := func() []byte {
		d, err := record.DecodeDecision(good.record)
		if err != nil {
			t.Fatal(err)
		}
		d.Seat.Credential = "0123456789abcdef"
		b, err := record.EncodeDecision(d)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}()

	evidence := func(change func(v *SeatEvidence)) *SeatEvidence {
		v := good.evidence
		change(&v)
		return &v
	}
	cases := map[string]struct {
		decision []byte
		seen     Seen
	}{
		"no evidence":                        {good.record, Seen{}},
		"a desk activity":                    {good.record, Seen{LastActivity: base.Add(-time.Second), Evidence: &good.evidence}},
		"the evidence of another assertion":  {good.record, Seen{Evidence: &other.evidence}},
		"a signature changed":                {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Signature = append([]byte{}, v.Signature...); v.Signature[5] ^= 1 })}},
		"a record that names another key":    {wrongCred, Seen{Evidence: &good.evidence}},
		"evidence for a desk decision":       {desk, Seen{Evidence: &good.evidence}},
		"a nonce of 31 bytes":                {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Nonce = v.Nonce[:31] })}},
		"no nonce":                           {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Nonce = nil })}},
		"a short instance":                   {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Instance = v.Instance[:63] })}},
		"an instance in capitals":            {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Instance = strings.ToUpper(v.Instance) })}},
		"no origin":                          {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Origin = "" })}},
		"an origin with no scheme":           {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Origin = "seat.example" })}},
		"an origin of http":                  {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Origin = "http://seat.example" })}},
		"an origin with a port":              {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Origin = "https://seat.example:443" })}},
		"an origin with a path":              {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Origin = "https://seat.example/" })}},
		"an origin in capitals":              {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Origin = "https://Seat.example" })}},
		"an origin with no host":             {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Origin = "https://" })}},
		"an origin with a control character": {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Origin = "https://seat\x1b.example" })}},
		"an origin with a host of 254 bytes": {good.record, Seen{Evidence: evidence(func(v *SeatEvidence) { v.Origin = "https://" + strings.Repeat("a", 254) })}},
	}
	for name, c := range cases {
		if _, err := s.AppendDecision(c.decision, c.seen); err == nil {
			t.Errorf("%s: the decision was journaled", name)
		}
		if c.decision[0] == '{' && bytes.Contains(c.decision, []byte(`"confirmed":"passkey"`)) {
			if _, err := s.AppendHeld(c.decision, pending, c.seen); err == nil {
				t.Errorf("%s: the held entry was journaled", name)
			}
		}
	}

	// Each field of the assertion is held to the bounds of the seat gate's
	// words, below and above; the record names the assertion of the changed
	// evidence, so only the bound refuses it.
	field := map[string]func(v *SeatEvidence) *[]byte{
		"credential id":      func(v *SeatEvidence) *[]byte { return &v.CredentialID },
		"authenticator data": func(v *SeatEvidence) *[]byte { return &v.AuthenticatorData },
		"client data":        func(v *SeatEvidence) *[]byte { return &v.ClientDataJSON },
		"signature":          func(v *SeatEvidence) *[]byte { return &v.Signature },
	}
	bounds := map[string][2]int{
		"credential id":      {ownerwire.MinCredentialIDBytes, ownerwire.MaxCredentialIDBytes},
		"authenticator data": {ownerwire.MinAuthenticatorDataBytes, ownerwire.MaxAuthenticatorDataBytes},
		"client data":        {1, ownerwire.MaxClientDataBytes},
		"signature":          {1, ownerwire.MaxSignatureBytes},
	}
	frame := ownerwire.DecisionFrame{ID: "ap-one", BindingSHA256: binding, Decision: record.DecisionApprove, SignedOff: []record.SignOff{}}
	for name, b := range bounds {
		for _, n := range []int{b[0] - 1, b[1] + 1} {
			v := good.evidence
			*field[name](&v) = bytes.Repeat([]byte{7}, n)
			rec := sealPasskey(t, frame, "project-a", base, v, "0011223344556677")
			if _, err := s.AppendDecision(rec, Seen{Evidence: &v}); err == nil {
				t.Errorf("%s of %d bytes: the decision was journaled", name, n)
			}
		}
	}
	if n := len(mustEntries(t, s)); n != 0 {
		t.Fatalf("the refusals wrote %d entries", n)
	}
	// The good one is taken, once.
	if _, err := s.AppendDecision(good.record, Seen{Evidence: &good.evidence}); err != nil {
		t.Fatalf("the good decision: %v", err)
	}
}

// The largest evidence the bounds take fits the bodies, of a decision and of
// a held entry alike, and reads back whole.
func TestTheLargestEvidenceFits(t *testing.T) {
	s := mustOpen(t, newPath(t))
	v := SeatEvidence{
		CredentialID:      bytes.Repeat([]byte{1}, ownerwire.MaxCredentialIDBytes),
		AuthenticatorData: bytes.Repeat([]byte{2}, ownerwire.MaxAuthenticatorDataBytes),
		ClientDataJSON:    bytes.Repeat([]byte{'"'}, ownerwire.MaxClientDataBytes),
		Signature:         bytes.Repeat([]byte{4}, ownerwire.MaxSignatureBytes),
		Nonce:             bytes.Repeat([]byte{5}, ownerwire.NonceBytes),
		Instance:          seatInstance,
		Origin:            "https://" + strings.Repeat("a", 253),
	}
	for i, held := range []bool{false, true} {
		id := []string{"ap-decided", "ap-held"}[i]
		pending, binding := pendingRecord(t, id, "project-a", base.Add(time.Hour))
		frame := ownerwire.DecisionFrame{ID: id, BindingSHA256: binding, Decision: record.DecisionApprove, SignedOff: []record.SignOff{}}
		rec := sealPasskey(t, frame, "project-a", base, v, "0011223344556677")
		var seq int64
		if held {
			card, err := s.AppendHeld(rec, pending, Seen{ShownSHA256: shown, Evidence: &v})
			if err != nil {
				t.Fatalf("held: %v", err)
			}
			seq = card.Seq
		} else {
			d, err := s.AppendDecision(rec, Seen{ShownSHA256: shown, Evidence: &v})
			if err != nil {
				t.Fatalf("decision: %v", err)
			}
			seq = d.Seq()
		}
		got, err := s.EvidenceOf(seq)
		if err != nil || !reflect.DeepEqual(got, v) {
			t.Fatalf("held %v: the evidence reads back as %v", held, err)
		}
	}
	for _, e := range mustEntries(t, s) {
		if len(e.Body) > 4096 {
			t.Errorf("entry %d has a body of %d bytes", e.Seq, len(e.Body))
		}
	}
}

// EvidenceOf checks what it reads back against the record kept beside it:
// evidence that is not whole, or not of the assertion, the credential or the
// session the record names, is damage. (The nonce and the instance are in no
// record: what checks them is the auditor's challenge and signature, above.)
func TestEvidenceThatDoesNotHoldIsDamage(t *testing.T) {
	a := newAuthenticator(t)
	_, binding := pendingRecord(t, "ap-one", "project-a", base.Add(time.Hour))
	good := passkeyRecord(t, a, "ap-one", "project-a", binding, true, base)
	how, err := confirmationOf(mustDecode(t, good.record), Seen{Evidence: &good.evidence})
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString
	cases := map[string]func(own, act map[string]any){
		"another signature":           func(own, _ map[string]any) { own["signature"] = b64([]byte{0x30, 0x01}) },
		"a signature that is not b64": func(own, _ map[string]any) { own["signature"] = "*" },
		"a nonce in capitals":         func(own, _ map[string]any) { own["nonce"] = strings.ToUpper(own["nonce"].(string)) },
		"a short nonce":               func(own, _ map[string]any) { own["nonce"] = "00" },
		"no client data":              func(own, _ map[string]any) { delete(own, "client_data_json") },
		"another credential id":       func(_, act map[string]any) { act["credential_id"] = b64(bytes.Repeat([]byte{9}, 32)) },
		"another origin":              func(_, act map[string]any) { act["origin"] = "http://seat.example" },
		"an instance in capitals":     func(_, act map[string]any) { act["instance"] = strings.ToUpper(seatInstance) },
		"another session":             func(_, act map[string]any) { act["session"] = "0123456789abcdef" },
		"another surface":             func(_, act map[string]any) { act["surface"] = "console" },
		"a desk activity":             func(_, act map[string]any) { act["last_activity"] = stamp(base) },
		"a record of another key":     nil,
	}
	// The record of the last case names another credential, with the same
	// assertion digest, so that only the credential differs.
	otherKey := mustDecode(t, good.record)
	otherKey.Seat.Credential = "0123456789abcdef"
	otherRecord, err := record.EncodeDecision(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := mustOpen(t, newPath(t))
			own := with(map[string]any{"approve": true, "binding_sha256": binding}, copyMap(how.own))
			act := with(map[string]any{"last_activity": "", "session": seatSession, "shown_sha256": "", "surface": SurfaceSeat}, copyMap(how.activity))
			rec := good.record
			if change == nil {
				rec = otherRecord
			} else {
				change(own, act)
			}
			ob, err := body(own)
			if err != nil {
				t.Fatal(err)
			}
			ab, err := body(act)
			if err != nil {
				t.Fatal(err)
			}
			// Written past the checks of AppendDecision, as damage would be.
			_, err = s.write(func(t *txn) error {
				e, err := t.add(Entry{Kind: KindDecision, At: base, PendingID: "ap-one", Project: "project-a", Body: ob, Record: rec})
				if err != nil {
					return err
				}
				_, err = t.add(Entry{Kind: KindActivity, At: base, PendingID: "ap-one", Project: "project-a", Ref: e.Seq, Body: ab})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.EvidenceOf(1); !errors.Is(err, ErrDamaged) {
				t.Fatalf("EvidenceOf: %v, want ErrDamaged", err)
			}
		})
	}
}

// The bounds of the evidence are those the seat gate's words hold an
// assertion to.
func TestTheEvidenceBoundsAreTheWires(t *testing.T) {
	if minCredentialIDLen != ownerwire.MinCredentialIDBytes || maxCredentialIDLen != ownerwire.MaxCredentialIDBytes ||
		minEvidenceAuthData != ownerwire.MinAuthenticatorDataBytes || maxEvidenceAuthData != ownerwire.MaxAuthenticatorDataBytes ||
		maxEvidenceClientData != ownerwire.MaxClientDataBytes || maxEvidenceSignature != ownerwire.MaxSignatureBytes ||
		evidenceNonceBytes != ownerwire.NonceBytes || evidenceInstanceLength != ownerwire.InstanceLength {
		t.Fatal("a bound of the evidence differs from the seat gate's words")
	}
}

func mustDecode(t *testing.T, data []byte) record.Decision {
	t.Helper()
	d, err := record.DecodeDecision(data)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
