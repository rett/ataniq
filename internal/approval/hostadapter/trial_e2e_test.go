//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/credkind"
	"ataniq/internal/approval/door"
	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
)

// These tests run the phone trial through the chain of the decision tests
// (seat_decide_e2e_test.go, whose world and helpers they use), with the
// trial's service in place of the scratch pass's:
//
//	the settings "ataniqd trial init" writes, read back by LoadScratch
//	  -> OpenTrial: the real service and journal in a state folder, the
//	     stand-in door, the trial host (no git, no mirror, no sandbox)
//	  -> Trial.Submit, which "ataniqd trial submit" calls
//	webauthntest authenticator (the phone)
//	  -> seatapp -> seatlink -> seatgate over TLS 1.3 with pinned keys on
//	     127.0.0.1 -> gatehost.NewDeciding -> the trial's service
//
// Nothing in the decision is a trial's: the challenge, the assertion and
// its verification, the decision record, the door's checks and the journal's
// evidence are the ones a real card goes through. Only the card is a sample,
// and its use applies nothing.

// trialWorld is a decision world on a trial: the settings written and read
// back, the trial opened on them in a private state folder with a key file
// and the pairing tests' presence window, and a deciding gate on 127.0.0.1
// behind a relay. It starts no seat.
func trialWorld(t *testing.T) (*decideWorld, *Trial) {
	t.Helper()
	clock := doortest.NewClock(start)
	state := filepath.Join(privateDir(t), "state")
	if _, err := WriteTrialScratch(state); err != nil {
		t.Fatalf("WriteTrialScratch: %v", err)
	}
	sc, err := LoadScratch(filepath.Join(state, OwnerDirName, ScratchName))
	if err != nil || !sc.Trial {
		t.Fatalf("the trial's settings read back: %+v, %v", sc, err)
	}
	keys, err := ownerjournal.OpenKeys(filepath.Join(state, OwnerDirName, ownerjournal.KeysFileName))
	if err != nil {
		t.Fatalf("OpenKeys: %v", err)
	}
	policy := sc.KindPolicy()
	tr, err := OpenTrial(context.Background(), TrialConfig{
		StateDir: state, Scratch: sc, Now: clock.Now, Manual: true,
		Service: ownerchannel.Config{
			Keys: keys, PresenceWindow: pairPresenceWindow,
			KindPolicy: func(project string) (credkind.Policy, bool) { return policy, project == sc.Project },
		},
	})
	if err != nil {
		_ = keys.Close()
		t.Fatalf("OpenTrial: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	w := &decideWorld{door: &swapDoor{d: tr.door}, keyFile: keys}
	w.pairWorld = &pairWorld{t: t, rig: &rig{t: t, clock: clock, state: state}, svc: tr.Service,
		gateID: newPairIdentity(t, "gate"), seatID: newPairIdentity(t, "seat")}
	w.otherID = newPairIdentity(t, "seat")
	w.gate = ownerchannel.Gate{Instance: w.gateID.Fingerprint(), Origin: pairOrigin, RPID: pairRPID}
	addr, calls := w.startGate(true)
	w.calls = calls
	w.relay = newDecRelay(t, addr)
	w.addr = w.relay.addr()
	return w, tr
}

// submitTrial makes a sample card of the kind given and requires a pending one.
func submitTrial(t *testing.T, tr *Trial, kind string) TrialCard {
	t.Helper()
	c, err := tr.Submit(pairCtx(t), kind)
	if err != nil {
		t.Fatalf("Submit %s: %v", kind, err)
	}
	if c.State != string(ownerchannel.RequestPending) || c.PendingID == "" || c.Kind != kind {
		t.Fatalf("the sample %s is %+v, want a pending card", kind, c)
	}
	return c
}

// trialJournal closes the trial and opens its journal.
func trialJournal(t *testing.T, tr *Trial, state string) *ownerjournal.Store {
	t.Helper()
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	jr, err := ownerjournal.Open(filepath.Join(state, OwnerDirName, JournalName))
	if err != nil {
		t.Fatalf("the journal: %v", err)
	}
	t.Cleanup(func() { _ = jr.Close() })
	return jr
}

// The trial, whole: the settings are written once and read back, the phone
// pairs at the desk and logs in, the open list is held by a poll, a sample
// finish arrives and wakes it, the phone opens the card (the STAND-IN banner,
// the sample's note, its flagged Makefile), signs it off and approves it with
// its passkey. The stand-in door takes the approve with the seat block of the
// assertion, the use goes through the door's checks, and the outcome is
// not-applied with the trial's line: nothing moved. A sample prune is decided
// the same way. The journal keeps both decisions with the evidence of their
// assertions, which an audit verifies from the journal and the key list alone,
// and both outcomes say that nothing was applied.
func TestTrialE2EAPhoneDecidesSampleCardsAndNothingIsApplied(t *testing.T) {
	w, tr := trialWorld(t)
	if _, err := WriteTrialScratch(w.rig.state); !errors.Is(err, ErrScratchExists) {
		t.Errorf("a second trial init: %v, want ErrScratchExists", err)
	}
	s := w.newSeat(w.seatID)
	s.link.StartWatch()
	t.Cleanup(func() { _ = s.link.Close() })
	phone := newPhone(t)
	item := w.pair(s, phone)
	sess := s.logIn(phone)

	// The open list is held; a sample card wakes it.
	_, v0, live := s.pageWatch("/cards/", sess)
	if !live || v0 == "" {
		t.Fatalf("the list names live %v, version %q", live, v0)
	}
	poll := s.startPoll(sess, v0, "", "")
	held(t, poll, "a poll with the empty list's version")
	fin := submitTrial(t, tr, TrialFinish)
	if got := answer(t, poll, watchWithin, "after a sample card"); got.State != "changed" || got.Waiting != 1 {
		t.Errorf("the poll answers %+v, want changed with 1 waiting", got)
	}
	card := fin.PendingID
	p := w.pendingOf(card)
	if p.Binding.Summary == nil || len(p.Binding.Summary.Flagged) != 1 || p.Binding.Summary.Flagged[0].Path != "Makefile" ||
		p.Binding.Verb != record.VerbFeatureFinish || p.Binding.Argv[0] != fin.Feature || p.Binding.PlanToken == "" ||
		!strings.Contains(string(p.PlanText), "feature finish "+fin.Feature) {
		t.Fatalf("the sample card is %+v", p.Binding)
	}
	page := s.show(sess, card)
	for _, want := range []string{decideFormMark, `id="signoff-0"`, "Makefile", "STAND-IN", "TRIAL sample card 1", fin.Feature} {
		if !strings.Contains(page, want) {
			t.Errorf("the Card page has no %s", want)
		}
	}

	_, as, res := s.decide(sess, phone, card, "approve", []int{0}, "")
	wantDone(t, res, record.StateNotApplied)
	if got := res.str(t, "line"); got != TrialLine {
		t.Errorf("the phone is told %q, want %q", got, TrialLine)
	}
	d, st := w.doorDecision(card)
	if d == nil || st != record.StateNotApplied || d.Decision != record.DecisionApprove || d.Confirmed != record.ConfirmedPasskey ||
		d.Seat.Credential != item.ID || d.Seat.AssertionSHA256 != record.AssertionDigest(as.CredentialID, as.AuthenticatorData, as.ClientDataJSON, as.Signature) ||
		len(d.SignedOff) != 1 || d.SignedOff[0].Path != "Makefile" {
		t.Fatalf("the door holds %+v in the state %s, want the approve by passkey, not applied", d, st)
	}
	again := s.get("/cards/"+card, sess)
	if again.status != 200 || !strings.Contains(string(again.body), TrialLine) || strings.Contains(string(again.body), decideFormMark) {
		t.Errorf("the card read again: %d, the trial's line shown %v", again.status, strings.Contains(string(again.body), TrialLine))
	}

	// A sample prune; a second one while it is open is the stand-in's
	// duplicate, a refusal and no card.
	pr := submitTrial(t, tr, TrialBranchPrune)
	dup, err := tr.Submit(pairCtx(t), TrialBranchPrune)
	if err != nil || dup.State != string(ownerchannel.RequestRefused) || dup.Code != string(record.CodeRequestDuplicate) {
		t.Errorf("a second prune while one is open: %+v, %v", dup, err)
	}
	pp := w.pendingOf(pr.PendingID)
	if pp.Binding.Verb != record.VerbBranchPrune || len(pp.Binding.Effects) < 2 || pp.Binding.Summary != nil {
		t.Fatalf("the sample prune is %+v", pp.Binding)
	}
	_, _, res = s.decide(sess, phone, pr.PendingID, "approve", nil, "")
	wantDone(t, res, record.StateNotApplied)
	if got := res.str(t, "line"); got != TrialLine {
		t.Errorf("the phone is told %q for the prune", got)
	}

	// Nothing was applied: the trial host's Apply ran once per card and
	// moved nothing, and no pin is left.
	h := tr.host
	h.mu.Lock()
	applies, pins, tip := h.applies, len(h.pins), h.tip
	h.mu.Unlock()
	if applies != 2 || pins != 0 || tip != p.Binding.Effects[0].Old {
		t.Errorf("the trial host applied %d times, holds %d pins, its target is at %s (was %s)", applies, pins, tip, p.Binding.Effects[0].Old)
	}

	// The audit.
	stored, err := w.keyFile.Keys()
	if err != nil {
		t.Fatal(err)
	}
	jr := trialJournal(t, tr, w.rig.state)
	es, err := jr.Entries(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	k := kinds(es)
	if k[ownerjournal.KindDecision] != 2 || k[ownerjournal.KindOutcome] != 2 || k[ownerjournal.KindUse] != 2 {
		t.Fatalf("the journal holds %v, want two decisions, two uses and two outcomes", k)
	}
	for _, e := range es {
		switch e.Kind {
		case ownerjournal.KindDecision:
			auditSeatDecision(t, jr, e, stored)
		case ownerjournal.KindOutcome:
			var body struct {
				State string `json:"state"`
				Line  string `json:"line"`
			}
			if err := json.Unmarshal(e.Body, &body); err != nil || body.State != string(record.StateNotApplied) || body.Line != TrialLine {
				t.Errorf("an outcome of the journal is %q (%v)", e.Body, err)
			}
		}
	}
}

// A trial card cannot be applied to anything: trial settings that name a
// place to apply to, or anything of a merge, are refused, OpenTrial refuses
// settings that are not a trial's, and the trial host's Apply and Mirror say
// not-applied whatever they are handed and move nothing.
func TestTrialCardsCannotBeAppliedToAnything(t *testing.T) {
	dir := privateDir(t)
	for name, change := range map[string]func(*Scratch){
		"mirror":   func(s *Scratch) { s.Mirror = dir + "/mirror.git" },
		"drop":     func(s *Scratch) { s.Drop = dir + "/drop" },
		"sandbox":  func(s *Scratch) { s.Sandbox = dir + "/sandbox.json" },
		"work":     func(s *Scratch) { s.Work = dir + "/work" },
		"roots":    func(s *Scratch) { s.Roots = []string{dir} },
		"gates":    func(s *Scratch) { s.Gates = []ScratchGate{{Name: "suite", Argv: []string{"make", "test"}}} },
		"identity": func(s *Scratch) { s.Identity = ScratchIdentity{Name: "Host", Email: "host@example.invalid"} },
		"door":     func(s *Scratch) { s.Door = "real" },
		"kinds":    func(s *Scratch) { s.Kinds = []string{"passkey", "passkey"} },
		"target":   func(s *Scratch) { s.Target = "refs/tags/v1" },
	} {
		sc := TrialScratch()
		change(&sc)
		b, err := json.Marshal(sc)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadScratch(path); err == nil {
			t.Errorf("trial settings with a %s are taken", name)
		}
	}
	notTrial := TrialScratch()
	notTrial.Trial = false
	if _, err := OpenTrial(context.Background(), TrialConfig{StateDir: dir, Scratch: notTrial}); err == nil {
		t.Error("OpenTrial takes settings that are not a trial's")
	}

	h := newTrialHost("trial-project", "refs/heads/develop")
	effects := []record.Effect{{Kind: record.EffectRef, Ref: "refs/heads/develop", Old: h.tip, New: trialID("x")}}
	for _, f := range []func(context.Context, ownerchannel.ApplyInput) (door.Outcome, error){h.Apply, h.Mirror} {
		if out, err := f(context.Background(), ownerchannel.ApplyInput{Project: "trial-project", Effects: effects}); err != nil || out != door.OutcomeNotApplied {
			t.Errorf("the trial host says %q, %v", out, err)
		}
	}
	if got := h.tip; got != effects[0].Old {
		t.Errorf("the trial's target moved to %s", got)
	}
	if h.OutcomeLine(door.OutcomeApplied) != TrialLine {
		t.Error("the trial host's outcome line is not the trial's")
	}
}

// The kinds of sample card are named as the operator types them: finish and
// branch-prune, the verb's words joined. "prune" alone is no kind, and no kind
// is a git maintenance subcommand, which the package's scan for maintenance
// literals relies on.
func TestTrialKindsAreFinishAndBranchPrune(t *testing.T) {
	if strings.Join(TrialKinds, ",") != "finish,branch-prune" || TrialBranchPrune != "branch-prune" {
		t.Fatalf("the kinds are %v", TrialKinds)
	}
	for _, k := range TrialKinds {
		for _, w := range maintenanceWords {
			if k == w {
				t.Errorf("the kind %q is a git maintenance subcommand", k)
			}
		}
	}
	clock := doortest.NewClock(start)
	state := filepath.Join(privateDir(t), "state")
	sc, err := WriteTrialScratch(state)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := OpenTrial(context.Background(), TrialConfig{StateDir: state, Scratch: sc, Now: clock.Now, Manual: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	if c := submitTrial(t, tr, "branch-prune"); c.Kind != "branch-prune" {
		t.Errorf("the card is %+v", c)
	}
	for _, kind := range []string{"prune", "branch prune", "Finish", ""} {
		if c, err := tr.Submit(context.Background(), kind); err == nil {
			t.Errorf("the kind %q makes %+v", kind, c)
		}
	}
}

// sizes returns how much the trial host keeps in memory.
func (h *trialHost) sizes() (features, merges, branches, pins, evidence int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.features), len(h.merges), len(h.branches), len(h.pins), len(h.evidence)
}

// A request the door or the service refused makes no card, and leaves nothing
// of its sample in the host's memory: the sample feature of a refused finish
// and the sample branches of a refused prune are not kept, however many are
// refused. The accepted ones are kept as before.
func TestTrialRefusedSubmissionsDoNotGrowTheHost(t *testing.T) {
	clock := doortest.NewClock(start)
	state := filepath.Join(privateDir(t), "state")
	sc, err := WriteTrialScratch(state)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := OpenTrial(context.Background(), TrialConfig{StateDir: state, Scratch: sc, Now: clock.Now, Manual: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	ctx := context.Background()

	// The door admits doortest.RequestsPerMinute requests a minute: the rest
	// are refused.
	for i := 0; i < doortest.RequestsPerMinute; i++ {
		submitTrial(t, tr, TrialFinish)
	}
	f0, m0, b0, p0, e0 := tr.host.sizes()
	if f0 != doortest.RequestsPerMinute || m0 != f0 || p0 != f0 || e0 != f0 || b0 != 0 {
		t.Fatalf("after %d cards the host keeps %d, %d, %d, %d, %d", f0, f0, m0, b0, p0, e0)
	}
	for i := 0; i < 25; i++ {
		c, err := tr.Submit(ctx, TrialFinish)
		if err != nil || c.State != string(ownerchannel.RequestRefused) || c.Code != string(record.CodeRequestRate) {
			t.Fatalf("a finish past the rate: %+v, %v", c, err)
		}
	}
	if f, m, b, p, e := tr.host.sizes(); f != f0 || m != m0 || b != b0 || p != p0 || e != e0 {
		t.Errorf("refused finishes grew the host: %d, %d, %d, %d, %d from %d, %d, %d, %d, %d", f, m, b, p, e, f0, m0, b0, p0, e0)
	}

	// A minute later a prune is made, and the ones refused as duplicates leave
	// nothing.
	clock.Advance(2 * time.Minute)
	submitTrial(t, tr, TrialBranchPrune)
	_, _, b1, _, _ := tr.host.sizes()
	if b1 < 2 || b1 > 3 {
		t.Fatalf("the prune left %d branches, want 2 or 3", b1)
	}
	for i := 0; i < 5; i++ {
		c, err := tr.Submit(ctx, TrialBranchPrune)
		if err != nil || c.State != string(ownerchannel.RequestRefused) {
			t.Fatalf("a duplicate prune: %+v, %v", c, err)
		}
	}
	if _, _, b, _, _ := tr.host.sizes(); b != b1 {
		t.Errorf("refused prunes grew the host's branches from %d to %d", b1, b)
	}
}

// A feature the host drops takes its merges and their suite records with it,
// and nothing else.
func TestTrialHostDropsAFeatureWithItsMerges(t *testing.T) {
	h := newTrialHost("trial-project", "refs/heads/develop")
	var keep, drop string
	for n, name := range []string{"trial-keep-1", "trial-drop-2"} {
		tip := h.addFeature(name, "stamp", n+1)
		b, err := h.Build(context.Background(), ownerchannel.MergeInput{Project: "trial-project", Feature: name, FeatureTip: tip, TargetTip: h.tip, Key: "key-" + name})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Verify(context.Background(), ownerchannel.VerifyInput{Project: "trial-project", Merge: b.Commit}); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			keep = name
		} else {
			drop = name
		}
	}
	if f, m, _, _, e := h.sizes(); f != 2 || m != 2 || e != 2 {
		t.Fatalf("the host keeps %d, %d, %d", f, m, e)
	}
	h.dropFeature(drop)
	f, m, _, _, e := h.sizes()
	if f != 1 || m != 1 || e != 1 {
		t.Errorf("after the drop the host keeps %d features, %d merges, %d records", f, m, e)
	}
	h.mu.Lock()
	_, kept := h.features[keep]
	h.mu.Unlock()
	if !kept {
		t.Error("the drop took the other feature")
	}
}
