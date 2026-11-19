package ownerchannel

import (
	"context"
	"errors"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

func TestAnApproveRunsToApplied(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")

	res, err := e.decide(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != record.StateApplied || res.Held || res.Line == "" {
		t.Fatalf("the result is %+v", res)
	}
	if got := e.intake.ref(); got != mergeID {
		t.Errorf("the target is at %s, want the merge", got)
	}
	if calls, moves := e.intake.appliesSeen(); calls != 1 || moves != 1 {
		t.Errorf("applied %d times, moved the ref %d times", calls, moves)
	}
	if e.state(p.ID) != record.StateApplied {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
	want := []ownerjournal.Kind{
		ownerjournal.KindCount, ownerjournal.KindDecision, ownerjournal.KindActivity,
		ownerjournal.KindDelivered, ownerjournal.KindUse, ownerjournal.KindOutcome,
	}
	got := e.kinds()
	if len(got) != len(want) {
		t.Fatalf("the journal holds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the journal holds %v, want %v", got, want)
		}
	}
	replies := e.mail.repliesFor("rq-test-01")
	if len(replies) != 1 || replies[0].State != record.StateApplied || replies[0].PendingID != p.ID {
		t.Errorf("the lead was told %+v", replies)
	}
	if n := e.merger.pinCount(); n != 0 {
		t.Errorf("%d pins remain after the outcome", n)
	}

	// The journaled bytes are the record the door holds, filled in by the
	// host, with nothing the frame did not say beyond the daemon's fields.
	d := e.entries(ownerjournal.KindDecision)[0]
	dec, err := record.DecodeDecision(d.Record)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Planner != p.Planner || dec.Project != p.Binding.Project || dec.PlanToken != p.Binding.PlanToken ||
		dec.Uses != 1 || dec.Confirmed != record.ConfirmedCode || dec.Remark != "" ||
		dec.DecidedAt != record.FormatTime(e.clock.Now()) ||
		dec.ExpiresAt != record.FormatTime(e.clock.Now().Add(30*time.Minute)) {
		t.Errorf("the decision record is %+v", dec)
	}
	if dec.Console != (record.Console{PID: 41822, TTY: "/dev/pts/4", Client: "console-a 0.0.1", Decider: true, Surface: "console", Session: "0123456789abcdef"}) {
		t.Errorf("the console block is %+v", dec.Console)
	}
	if !record.SignOffsEqual(p.Binding.Summary.Flagged, dec.SignedOff) || len(dec.SignedOff) == 0 {
		t.Errorf("the sign-offs are %+v, want the flagged list", dec.SignedOff)
	}
	shown, err := e.stand.Show(context.Background(), p.ID)
	if err != nil || string(shown.Decision.Bytes) != string(d.Record) {
		t.Errorf("the door holds other bytes than the journal: %v", err)
	}
	if a, err := e.svc.journal().Entries(0, 0); err != nil || len(a) == 0 {
		t.Fatal("no entries", err)
	}
}

func TestADenyIsDeliveredAndIsNotAUse(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	f := frame(p, false)
	f.Reason = "not this one"
	e.beat()
	res, err := e.svc.Decide(context.Background(), src, f)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != record.StateDenied || res.Held {
		t.Fatalf("the result is %+v", res)
	}
	if e.state(p.ID) != record.StateDenied {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
	if _, _, settles := e.door.counts(); settles != 0 {
		t.Error("a deny was settled")
	}
	if calls, _ := e.intake.appliesSeen(); calls != 0 {
		t.Error("a deny was applied")
	}
	uses, err := e.svc.journal().CardUses("project-a", startOfDay(e.clock.Now()))
	if err != nil || len(uses) != 0 {
		t.Errorf("a deny counted as a card use: %v %v", uses, err)
	}
	dec, _ := record.DecodeDecision(e.entries(ownerjournal.KindDecision)[0].Record)
	if dec.Decision != record.DecisionDeny || dec.Uses != 0 || dec.Reason != "not this one" {
		t.Errorf("the record is %+v", dec)
	}
	replies := e.mail.repliesFor("rq-test-01")
	if len(replies) != 1 || replies[0].State != record.StateDenied {
		t.Errorf("the lead was told %+v", replies)
	}
	if n := e.merger.pinCount(); n != 0 {
		t.Errorf("%d pins remain", n)
	}
}

// refusedEntry returns the only refused entry and its code.
func (e *env) refusedEntries() []ownerjournal.Entry { return e.entries(ownerjournal.KindRefused) }

func TestARefusedDecisionIsJournaledWithItsCode(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	ctx := context.Background()

	type try struct {
		name string
		f    func() (ownerwire.DecideResult, error)
		want record.Code
	}
	other := src
	other.Decider = false
	wrong := frame(p, true)
	wrong.BindingSHA256 = record.Digest([]byte("another binding"))
	missing := frame(p, true)
	missing.SignedOff = []record.SignOff{}
	surplus := frame(p, true)
	surplus.SignedOff = append(surplus.SignedOff, record.SignOff{Kind: record.FlagSymlink, Path: record.Text("lib/other")})
	unknown := frame(p, true)
	unknown.ID = "ap-nothing-here"
	reordered := frame(p, true)
	reordered.SignedOff = append(reordered.SignedOff, record.SignOff{Kind: record.FlagSymlink, Path: record.Text("lib/other")})
	reordered.SignedOff[0], reordered.SignedOff[1] = reordered.SignedOff[1], reordered.SignedOff[0]
	badFrame := frame(p, false)
	badFrame.SignedOff = []record.SignOff{{Kind: record.FlagExecConfig, Path: record.Text("Makefile")}}

	e.beat()
	cases := []try{
		{"not the decider", func() (ownerwire.DecideResult, error) { return e.svc.Decide(ctx, other, frame(p, true)) }, record.CodeNotDecider},
		{"a frame that is not well formed", func() (ownerwire.DecideResult, error) { return e.svc.Decide(ctx, src, badFrame) }, record.CodeDecisionInvalid},
		{"another binding", func() (ownerwire.DecideResult, error) { return e.svc.Decide(ctx, src, wrong) }, record.CodeBindingMismatch},
		// OC16
		{"a flagged path with no sign-off", func() (ownerwire.DecideResult, error) { return e.svc.Decide(ctx, src, missing) }, record.CodeDecisionInvalid},
		{"a sign-off for a path that is not flagged", func() (ownerwire.DecideResult, error) { return e.svc.Decide(ctx, src, surplus) }, record.CodeDecisionInvalid},
		{"sign-offs in another order", func() (ownerwire.DecideResult, error) { return e.svc.Decide(ctx, src, reordered) }, record.CodeDecisionInvalid},
		{"an id the door does not know", func() (ownerwire.DecideResult, error) { return e.svc.Decide(ctx, src, unknown) }, record.CodeDecisionInvalid},
	}
	for i, c := range cases {
		_, err := c.f()
		wantCode(t, err, c.want)
		got := e.refusedEntries()
		if len(got) != i+1 {
			t.Fatalf("%s: %d refused entries, want %d", c.name, len(got), i+1)
		}
		last := got[len(got)-1]
		if string(last.Body) == "" || last.Kind != ownerjournal.KindRefused {
			t.Fatalf("%s: %+v", c.name, last)
		}
		if a, err := e.svc.journal().Entries(last.Seq, 1); err != nil || len(a) != 1 || a[0].Kind != ownerjournal.KindActivity {
			t.Errorf("%s: the refused entry has no activity entry after it: %v", c.name, a)
		}
	}
	if e.state(p.ID) != record.StatePending {
		t.Errorf("a refused decision reached the door: %s", e.state(p.ID))
	}
	if decides, _, _ := e.door.counts(); decides != 0 {
		t.Errorf("the door was handed %d decisions", decides)
	}
	if n := len(e.entries(ownerjournal.KindDecision)); n != 0 {
		t.Errorf("%d decision entries after refusals", n)
	}
	body := string(e.refusedEntries()[0].Body)
	if body != `{"code":"not-decider","session":"0123456789abcdef","surface":"console"}` {
		t.Errorf("the first refused entry's body is %s", body)
	}
	// The card is still decidable with the exact sign-offs (OC16).
	if res, err := e.svc.Decide(ctx, src, frame(p, true)); err != nil || res.State != record.StateApplied {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestADecisionNeedsThePresenceOfTheOwner(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	ctx := context.Background()

	// No beat ever: away.
	_, err := e.svc.Decide(ctx, src, frame(p, true))
	wantCode(t, err, record.CodeOwnerAway)
	// A decision cannot make him present.
	_, err = e.svc.Decide(ctx, src, frame(p, true))
	wantCode(t, err, record.CodeOwnerAway)

	e.beat()
	e.advance(9*time.Minute + 59*time.Second)
	// Just inside the window, the decision goes through, and its action
	// counts as activity.
	e2 := e.card("feature-b")
	e.beat()
	if _, err := e.svc.Decide(ctx, src, frame(e2, false)); err != nil {
		t.Fatal(err)
	}
	e.advance(10*time.Minute + time.Second)
	_, err = e.svc.Decide(ctx, src, frame(p, true))
	wantCode(t, err, record.CodeOwnerAway)

	got := e.refusedEntries()
	if len(got) != 3 {
		t.Fatalf("%d refused entries, want 3", len(got))
	}
	act, err := e.svc.journal().Entries(got[0].Seq, 1)
	if err != nil || len(act) != 1 {
		t.Fatal(err)
	}
	// The activity entry of a refusal keeps the last activity before it.
	if string(act[0].Body) != `{"last_activity":"","session":"0123456789abcdef","shown_sha256":"","surface":"console"}` {
		t.Errorf("the first refusal's activity is %s", act[0].Body)
	}
	last, err := e.svc.journal().Entries(got[2].Seq, 1)
	if err != nil || len(last) != 1 || string(last[0].Body) == string(act[0].Body) {
		t.Errorf("the last refusal's activity has no last activity: %v %v", last, err)
	}
	// A beat from a console that is not the decider makes no one present.
	other := src
	other.Decider = false
	other.Session = "fedcba9876543210"
	if err := e.svc.Beat(other); err != nil {
		t.Fatal(err)
	}
	other.Decider = true
	_, err = e.svc.Decide(ctx, other, frame(p, true))
	wantCode(t, err, record.CodeOwnerAway)
}

func TestTheJournalKeepsTheActivityBeforeTheDecision(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.beat()
	e.advance(5 * time.Minute)
	if _, err := e.svc.Decide(context.Background(), src, frame(p, true)); err != nil {
		t.Fatal(err)
	}
	act, err := e.svc.journal().Entries(e.entries(ownerjournal.KindDecision)[0].Seq, 1)
	if err != nil || len(act) != 1 || act[0].Kind != ownerjournal.KindActivity {
		t.Fatal(act, err)
	}
	want := `{"last_activity":"` + record.FormatTime(start) + `","session":"0123456789abcdef","shown_sha256":"","surface":"console"}`
	if string(act[0].Body) != want {
		t.Errorf("the activity is %s, want %s", act[0].Body, want)
	}
	// The action that carried the decision counts as activity: another
	// decision 9 minutes later is inside the window of that action, though
	// not inside the window of the beat.
	e.advance(9 * time.Minute)
	q := e.card("feature-b")
	if _, err := e.svc.Decide(context.Background(), src, frame(q, false)); err != nil {
		t.Fatalf("a decision inside the window of the last action was refused: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestACardThatIsNotPendingCannotBeDecided(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	p := e.card("feature-a")
	if res, err := e.decide(p, true); err != nil || res.State != record.StateApplied {
		t.Fatalf("%+v %v", res, err)
	}
	e.beat()
	_, err := e.svc.Decide(ctx, src, frame(p, true))
	wantCode(t, err, record.CodeApprovalConsumed)

	e.advance(time.Minute)
	e.resetRef()
	q := e.card("feature-b")
	if err := e.stand.Withdraw(q.ID); err != nil {
		t.Fatal(err)
	}
	e.beat()
	_, err = e.svc.Decide(ctx, src, frame(q, true))
	wantCode(t, err, record.CodeApprovalVoid)

	e.advance(time.Minute)
	r := e.card("feature-c")
	e.advance(25 * time.Hour)
	e.beat()
	_, err = e.svc.Decide(ctx, src, frame(r, true))
	wantCode(t, err, record.CodeApprovalExpired)
}

func TestTheDoorRefusingADecisionClosesIt(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.beat()
	// The pending record ends at the door after the service looked and
	// before the decision reached it.
	e.at["decision-journaled"] = func() {
		if err := e.stand.Void(p.ID); err != nil {
			t.Fatal(err)
		}
	}
	_, err := e.svc.Decide(context.Background(), src, frame(p, true))
	wantCode(t, err, record.CodeApprovalVoid)
	delete(e.at, "decision-journaled")
	if len(e.entries(ownerjournal.KindDelivered)) != 1 {
		t.Fatalf("the refusal was not journaled as a delivery: %v", e.kinds())
	}
	// A restart does not hand it over again.
	before, _, _ := e.door.counts()
	e.restart()
	after, _, _ := e.door.counts()
	if before != after {
		t.Errorf("the restart handed over a decision the door had refused (%d, %d)", before, after)
	}
	// The card ended, so the lead was told and the pin is gone.
	replies := e.mail.repliesFor("rq-test-01")
	if len(replies) != 1 || replies[0].State != record.StateVoid {
		t.Errorf("the lead was told %+v", replies)
	}
}

func TestADoorThatCannotBeReachedLeavesTheDecisionToBeDelivered(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.door.failDecide = errors.New("connection refused")
	_, err := e.decide(p, true)
	if !errors.Is(err, ErrDoorUnavailable) {
		t.Fatalf("Decide returned %v", err)
	}
	if c, ok := record.CodeOf(err); ok {
		t.Fatalf("a failure of the door was reported as the refusal %s", c)
	}
	if n := len(e.entries(ownerjournal.KindDecision)); n != 1 {
		t.Fatalf("%d decision entries", n)
	}
	if n := len(e.entries(ownerjournal.KindDelivered)); n != 0 {
		t.Fatalf("a decision the door never saw was marked delivered")
	}
	e.door.failDecide = nil
	// The next tick hands it over, and the approval is carried out.
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.state(p.ID) != record.StateApplied {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
	if calls, moves := e.intake.appliesSeen(); calls != 1 || moves != 1 {
		t.Errorf("applied %d times, moved %d", calls, moves)
	}
}

func TestADecisionTwiceIsRefused(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.door.failDecide = errors.New("down")
	_, _ = e.decide(p, true)
	e.door.failDecide = nil
	e.beat()
	// The card has a journaled decision the door has not seen yet. Another
	// one for it is refused, and the first is handed over.
	_, err := e.svc.Decide(context.Background(), src, frame(p, false))
	wantCode(t, err, record.CodeDecisionInvalid)
	if e.state(p.ID) != record.StateApplied {
		t.Errorf("the first decision was not carried out: %s", e.state(p.ID))
	}
}

// OC31, last case: a later failed run on a merge with an approved, unused
// decision. The door finds the evidence changed and voids the approval.
func TestOC31ALaterFailedRunVoidsTheApproval(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.intake.stale = true
	res, err := e.decide(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != record.StateVoid {
		t.Fatalf("the result is %+v, want void", res)
	}
	if calls, _ := e.intake.appliesSeen(); calls != 0 {
		t.Error("an approval whose evidence changed was applied")
	}
	uses := e.entries(ownerjournal.KindUse)
	if len(uses) != 1 || string(uses[0].Body) != `{"code":"evidence-changed"}` {
		t.Errorf("the use entries are %v", uses)
	}
	replies := e.mail.repliesFor("rq-test-01")
	if len(replies) != 1 || replies[0].State != record.StateVoid {
		t.Errorf("the lead was told %+v", replies)
	}
}

func TestAnUnknownOutcomeWaitsForTheOwner(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.intake.applyErr = errors.New("update-ref was killed")
	res, err := e.decide(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != record.StateUnknown {
		t.Fatalf("the result is %+v, want unknown", res)
	}
	if e.state(p.ID) != record.StateUnknown {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
	if n := e.merger.pinCount(); n != 1 {
		t.Errorf("%d pins, want the pin kept while the outcome is unknown", n)
	}
	if got := e.mail.all(); len(got) != 0 {
		t.Errorf("the lead was told of an outcome that is not final: %v", got)
	}

	// Restarts and ticks do not settle it, even when the mirror now shows
	// the ref at its new value.
	e.intake.mu.Lock()
	e.intake.refs[targetRef] = mergeID
	e.intake.mu.Unlock()
	e.restart()
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.state(p.ID) != record.StateUnknown {
		t.Fatalf("the service settled an unknown outcome by itself: %s", e.state(p.ID))
	}
	if _, _, settles := e.door.counts(); settles != 1 {
		t.Errorf("%d settles, want the one that said unknown", settles)
	}

	// Only the owner's word moves it, and only as the decider and present.
	notDecider := src
	notDecider.Decider = false
	_, err = e.svc.Dispose(context.Background(), notDecider, p.ID, record.StateApplied)
	wantCode(t, err, record.CodeNotDecider)
	_, err = e.svc.Dispose(context.Background(), src, p.ID, record.StateApplied)
	wantCode(t, err, record.CodeOwnerAway)
	e.beat()
	_, err = e.svc.Dispose(context.Background(), src, p.ID, record.StateDenied)
	wantCode(t, err, record.CodeDecisionInvalid)
	res, err = e.svc.Dispose(context.Background(), src, p.ID, record.StateApplied)
	if err != nil || res.State != record.StateApplied {
		t.Fatalf("%+v %v", res, err)
	}
	if e.state(p.ID) != record.StateApplied {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
	replies := e.mail.repliesFor("rq-test-01")
	if len(replies) != 1 || replies[0].State != record.StateApplied {
		t.Errorf("the lead was told %+v", replies)
	}
	if n := e.merger.pinCount(); n != 0 {
		t.Errorf("%d pins after the owner settled it", n)
	}
	unknown, err := e.svc.journal().UnknownOutcomes()
	if err != nil || len(unknown) != 0 {
		t.Errorf("an outcome is still unknown: %v %v", unknown, err)
	}
	_, err = e.svc.Dispose(context.Background(), src, p.ID, record.StateApplied)
	wantCode(t, err, record.CodeDecisionInvalid)
}

func TestAnApprovalTheMirrorDoesNotShowIsNotAppliedTwice(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	// The target moved between the plan and the use: the door refuses the
	// use as a mismatch and voids the approval, and nothing is applied.
	e.intake.mu.Lock()
	e.intake.refs[targetRef] = "5555555555555555555555555555555555555555"
	e.intake.mu.Unlock()
	res, err := e.decide(p, true)
	if err != nil || res.State != record.StateVoid {
		t.Fatalf("%+v %v", res, err)
	}
	if calls, _ := e.intake.appliesSeen(); calls != 0 {
		t.Error("applied against a target that moved")
	}
}

func TestAnApplyThatFindsTheRefMovedIsNotApplied(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	// The ref moves after the fresh read and before the update.
	e.at["door-used"] = func() {
		e.intake.mu.Lock()
		e.intake.refs[targetRef] = "5555555555555555555555555555555555555555"
		e.intake.mu.Unlock()
	}
	res, err := e.decide(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != record.StateNotApplied {
		t.Fatalf("the result is %+v", res)
	}
	if e.state(p.ID) != record.StateNotApplied {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
	replies := e.mail.repliesFor("rq-test-01")
	if len(replies) != 1 || replies[0].State != record.StateNotApplied {
		t.Errorf("the lead was told %+v", replies)
	}
}

func TestAFailedFreshReadLeavesTheApprovalOpenForATick(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.intake.freshErr = errors.New("mirror busy")
	res, err := e.decide(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != record.StateApproved {
		t.Fatalf("the result is %+v", res)
	}
	if _, uses, _ := e.door.counts(); uses != 0 {
		t.Error("the door's use ran with no fresh values")
	}
	e.intake.freshErr = nil
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.state(p.ID) != record.StateApplied {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
}

func TestAFrameThatCannotBecomeARecordIsRefused(t *testing.T) {
	e := newEnv(t)
	e.start()
	// A pending record may flag more paths than a decision record can sign
	// off: the frame cap equals the decision record's cap.
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
		if len(b) > 8192-200 {
			break
		}
	}
	e.intake.summary.Flagged = flagged
	p := e.card("feature-a")
	f := frame(p, true)
	f.SignedOff = signs
	e.beat()
	_, err := e.svc.Decide(context.Background(), src, f)
	wantCode(t, err, record.CodeRecordTooLarge)
	if got := e.refusedEntries(); len(got) != 1 || !contains(string(got[0].Body), "decision-invalid") {
		t.Errorf("the refusal was not journaled as decision-invalid: %v", got)
	}
	if e.state(p.ID) != record.StatePending {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
}

var _ = door.RecordOf
