package command

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tests for the hold of a queue: ReleaseHold, Withdraw, and what Restore
// accepts and refuses.

// heldTest is resentTest after late evidence for m1: lead a is held for m1,
// r1 is queued at the head and m2 is behind it.
func heldTest(t *testing.T) (*Machine, *fakeJournal) {
	t.Helper()
	m, j := resentTest(t)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantHeld(t, m, "m1", at(51*time.Second))
	return m, j
}

func TestReleaseHoldEndsTheHoldAndNothingElse(t *testing.T) {
	m, j := heldTest(t)
	before := snap(m)
	n := len(j.recs)
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(j.recs) != n+1 {
		t.Fatalf("%d records written", len(j.recs)-n)
	}
	r := j.recs[n]
	if r.Type != RecordRelease || r.Target != "lead:a" || r.Operator != "alice" || r.Reason != ReasonHoldReleased || r.EvidenceKind != EvidenceOwnerDecision ||
		!r.At.Equal(at(2*time.Minute)) || r.ID != "" || r.From != "" || r.To != "" {
		t.Errorf("release record %+v", r)
	}
	// No command changed, and the hold is gone from the view.
	after := snap(m)
	if !reflect.DeepEqual(before.Cmds, after.Cmds) || !reflect.DeepEqual(before.Texts, after.Texts) {
		t.Error("a release changed a command")
	}
	if v := leadView(t, m); v.Held || v.HeldFor != "" || !v.HeldSince.IsZero() || v.HeldReason != "" || v.HeldAlso != nil {
		t.Errorf("view %+v", v)
	}
	// The queue dispatches again, head first.
	if d := mustBegin(t, m, leadA, at(3*time.Minute)); d.ID != "r1" {
		t.Errorf("dispatched %s", d.ID)
	}
}

func TestReleaseHoldRefusals(t *testing.T) {
	m, j := heldTest(t)
	n := len(j.recs)
	before := snap(m)
	for name, op := range map[string]string{
		"empty":    "",
		"blank":    "  ",
		"newline":  "a\nb",
		"control":  "a\x1bb",
		"too long": strings.Repeat("x", maxOperatorBytes+1),
	} {
		if err := m.ReleaseHold(leadA, op, seenOf(m, leadA), at(time.Minute)); err == nil {
			t.Errorf("%s operator: no refusal", name)
		} else {
			wantRefusal(t, err, ReasonBadOperator)
		}
	}
	if len(j.recs) != n {
		t.Fatal("a refused release was journaled")
	}
	sameSnap(t, "a refused release", before, snap(m))
	// A queue that is not held, a target that has no queue, the zero target.
	wantRefusal(t, m.ReleaseHold(leadB, "alice", seenOf(m, leadB), at(time.Minute)), ReasonNotHeld)
	wantRefusal(t, m.ReleaseHold(Target{}, "alice", 0, at(time.Minute)), ReasonNotHeld)
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(2*time.Minute)), ReasonNotHeld)
}

func TestReleaseHoldOnAJournalFailureChangesNothing(t *testing.T) {
	m, j := heldTest(t)
	before, n := snap(m), len(j.recs)
	j.failNext = true
	wantJournalError(t, m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(time.Minute)))
	sameSnap(t, "a failed release", before, snap(m))
	if len(j.recs) != n {
		t.Error("a failed append left a record")
	}
	wantHeld(t, m, "m1", at(2*time.Minute))
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(j.recs) != n+1 {
		t.Errorf("%d records after the repeat", len(j.recs)-n)
	}
}

// Nothing but an operator's call ends a hold: not an hour of ticks, not a
// restore, not a decision about a command, not the end of the replacement.
func TestNothingButAReleaseEndsAHold(t *testing.T) {
	m, j := heldTest(t)
	wantTicksLeaveItAlone(t, m, at(time.Minute))
	wantTicksLeaveItAlone(t, m, at(48*time.Hour))
	wantHeld(t, m, "m1", at(72*time.Hour))
	// Restore, with and without an hour in between, and again from what it wrote.
	for _, when := range []time.Duration{time.Minute, 24 * time.Hour, 365 * 24 * time.Hour} {
		jr := &fakeJournal{}
		r, err := Restore(testLimits(), jr, j.recs, at(when))
		if err != nil {
			t.Fatal(err)
		}
		if len(jr.recs) != 0 {
			t.Fatalf("restore wrote %+v", jr.recs)
		}
		wantHeld(t, r, "m1", at(when))
		wantTicksLeaveItAlone(t, r, at(when))
	}
	// Withdrawing the replacement leaves the hold in place.
	if err := m.Withdraw("r1", "alice", at(time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantHeld(t, m, "m1", at(time.Hour))
	// Resolving another command does too.
	if err := m.ConfirmDelivery("m1", "file:11", at(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantHeld(t, m, "m1", at(2*time.Hour))
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if d := mustBegin(t, m, leadA, at(3*time.Hour)); d.ID != "m2" {
		t.Errorf("dispatched %s", d.ID)
	}
}

func TestWithdrawEndsAQueuedCommandOfAHeldQueue(t *testing.T) {
	m, j := heldTest(t)
	n := len(j.recs)
	if err := m.Withdraw("r1", "alice", at(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(j.recs) != n+1 {
		t.Fatalf("%d records written", len(j.recs)-n)
	}
	r := j.recs[n]
	if r.Type != "" || r.ID != "r1" || r.From != StateQueued || r.To != StateResolved || r.Reason != ReasonOperatorWithdrawn || r.Operator != "alice" ||
		r.EvidenceKind != EvidenceOwnerDecision || r.Replacement != "" || r.Text != "" {
		t.Errorf("withdraw record %+v", r)
	}
	c, _ := m.Command("r1")
	if c.State != StateResolved || c.Reason != ReasonOperatorWithdrawn || c.Operator != "alice" || c.Of != "m1" {
		t.Errorf("r1 %+v", c)
	}
	if got := ids(m, leadA); len(got) != 1 || got[0] != "m2" {
		t.Errorf("queue %v", got)
	}
	if m.cmds["r1"].text != "" {
		t.Error("a withdrawn command keeps its text")
	}
	// It does not end the hold and it sends nothing.
	wantHeld(t, m, "m1", at(3*time.Minute))
	// A command withdrawn from the middle of the queue leaves the others in order.
	mustSubmit(t, m, "m3", Daemon(), leadA, at(4*time.Minute))
	mustSubmit(t, m, "m4", Daemon(), leadA, at(4*time.Minute))
	if err := m.Withdraw("m3", "alice", at(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := ids(m, leadA); !reflect.DeepEqual(got, []string{"m2", "m4"}) {
		t.Errorf("queue %v", got)
	}
	// The queue empty and still held, the view shows it.
	for _, id := range []string{"m2", "m4"} {
		if err := m.Withdraw(id, "alice", at(6*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if v := leadView(t, m); !v.Held || len(v.Commands) != 0 {
		t.Errorf("view %+v", v)
	}
	_, err := m.BeginDispatch(leadA, at(7*time.Minute))
	wantRefusal(t, err, ReasonQueueHeld)
}

func TestWithdrawRefusals(t *testing.T) {
	m, j := heldTest(t)
	mustSubmit(t, m, "s1", Daemon(), SupervisorTarget(), at(2*time.Minute))
	n := len(j.recs)
	before := snap(m)
	wantRefusal(t, m.Withdraw("nope", "alice", at(3*time.Minute)), ReasonNoCommand)
	// Not queued: m1 is resolved.
	wantRefusal(t, m.Withdraw("m1", "alice", at(3*time.Minute)), ReasonWrongState)
	// A queued command of a queue that is not held.
	wantRefusal(t, m.Withdraw("s1", "alice", at(3*time.Minute)), ReasonNotHeld)
	for _, op := range []string{"", " ", "a\nb", strings.Repeat("x", maxOperatorBytes+1)} {
		wantRefusal(t, m.Withdraw("r1", op, at(3*time.Minute)), ReasonBadOperator)
	}
	if len(j.recs) != n {
		t.Fatal("a refused withdrawal was journaled")
	}
	sameSnap(t, "refused withdrawals", before, snap(m))
	// A dispatching command is not interrupted: release, dispatch, hold again.
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	mustBegin(t, m, leadA, at(5*time.Minute))
	if err := m.ConfirmDelivery("m1", "file:12", at(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "r1", StateDispatching)
	wantRefusal(t, m.Withdraw("r1", "alice", at(7*time.Minute)), ReasonWrongState)
	// A queue that was released takes no withdrawal either.
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(8*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, m.Withdraw("m2", "alice", at(9*time.Minute)), ReasonNotHeld)
}

func TestWithdrawOnAJournalFailureChangesNothing(t *testing.T) {
	m, j := heldTest(t)
	before, n := snap(m), len(j.recs)
	j.failNext = true
	wantJournalError(t, m.Withdraw("r1", "alice", at(time.Minute)))
	sameSnap(t, "a failed withdrawal", before, snap(m))
	if len(j.recs) != n {
		t.Error("a failed append left a record")
	}
	if err := m.Withdraw("r1", "alice", at(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "r1", StateResolved)
}

// The queue time limit does not refuse the head of a held queue, which is not
// being tried.
func TestAHeldQueueHeadIsNotRefusedAsNotIdle(t *testing.T) {
	m, _ := heldTest(t)
	wantTicksLeaveItAlone(t, m, at(3*time.Hour))
	wantState(t, m, "r1", StateQueued)
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "r1", StateQueued)
}

// A replacement that was dispatching and went back to queued while the queue
// was held is dispatched after the release, and not before.
func TestAReplacementRequeuedUnderAHoldDispatchesAfterTheRelease(t *testing.T) {
	m, _ := resentTest(t)
	mustBegin(t, m, leadA, at(50*time.Second))
	if err := m.ConfirmDelivery("m1", "file:10", at(51*time.Second)); err != nil {
		t.Fatal(err)
	}
	mustFence(t, m, "r1", WrittenNothing, OutcomeNotIdle, at(52*time.Second))
	wantState(t, m, "r1", StateQueued)
	wantHeld(t, m, "m1", at(53*time.Second))
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(54*time.Second)); err != nil {
		t.Fatal(err)
	}
	if d := mustBegin(t, m, leadA, at(55*time.Second)); d.ID != "r1" {
		t.Errorf("dispatched %s", d.ID)
	}
}

// --- Restore ---------------------------------------------------------------

// A record of the commands of lateTest: sender ataniq, lead a.
func cmdRec(seq uint64, id string, from, to State, reason Reason) Record {
	return Record{Seq: seq, At: t0, ID: id, Target: "lead:a", Sender: "ataniq", Kind: "note", From: from, To: to, Reason: reason}
}

// holdRec is the hold record of the first piece of late information in a
// journal with no hold yet: the counter it sets is 1. holdRecN sets another.
func holdRec(seq uint64, id string, reason Reason) Record { return holdRecN(seq, id, reason, 1) }

func holdRecN(seq uint64, id string, reason Reason, held uint64) Record {
	return Record{
		Seq: seq, At: t0, Type: RecordHold, ID: id, Target: "lead:a", Reason: reason,
		Held: held, Piece: pieceDigest("test piece", id, strconv.FormatUint(held, 10)),
	}
}

// releaseRec is the release of a hold whose counter is 1; releaseRecN sets the
// counter the operator saw.
func releaseRec(seq uint64, operator string) Record { return releaseRecN(seq, operator, 1) }

func releaseRecN(seq uint64, operator string, seen uint64) Record {
	return Record{
		Seq: seq, At: t0, Type: RecordRelease, Target: "lead:a", Reason: ReasonHoldReleased,
		EvidenceKind: EvidenceOwnerDecision, Evidence: releaseEvidence, Operator: operator, Held: seen,
	}
}

func withdrawRec(seq uint64, id string) Record {
	r := cmdRec(seq, id, StateQueued, StateResolved, ReasonOperatorWithdrawn)
	r.Operator, r.EvidenceKind, r.Evidence = "op", EvidenceOwnerDecision, withdrawnEvidence
	return r
}

func noteRec(seq uint64, id string, state State) Record {
	r := cmdRec(seq, id, state, state, ReasonLateEvidence)
	r.EvidenceKind, r.Evidence = EvidenceProviderReport, "file:1"
	return r
}

func dispatchOf(seq uint64, id string) Record {
	return cmdRec(seq, id, StateQueued, StateDispatching, "")
}

func TestRestoreAcceptsWhatTheLiveMachineWritesForAHold(t *testing.T) {
	m, j := heldTest(t)
	if _, err := Restore(testLimits(), &fakeJournal{}, j.recs, at(time.Hour)); err != nil {
		t.Fatalf("the live journal: %v", err)
	}
	// A longer life: release, dispatch, hold again, withdraw, release.
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := m.ConfirmDelivery("m1", "file:10", at(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := m.Withdraw("r1", "alice", at(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(testLimits(), &fakeJournal{}, j.recs, at(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sameSnap(t, "restore of a journal with holds, releases and a withdrawal", snap(m), snap(r))
}

func TestRestoreRefusesHoldAndReleaseRecordsTheMachineWouldNotWrite(t *testing.T) {
	_, j := resentTest(t)
	good := clone(j.recs)
	n := uint64(len(good))
	with := func(rs ...Record) []Record { return append(clone(good), rs...) }
	hold := func(mod func(*Record)) Record {
		r := holdRec(n+1, "m1", ReasonHoldAfterResend)
		if mod != nil {
			mod(&r)
		}
		return r
	}
	// The record the machine writes.
	if _, err := Restore(testLimits(), &fakeJournal{}, with(hold(nil)), at(time.Hour)); err != nil {
		t.Fatalf("a hold after a resend: %v", err)
	}
	for name, mod := range map[string]func(*Record){
		"hold about an unknown command":      func(r *Record) { r.ID = "zz" },
		"hold about a queued command":        func(r *Record) { r.ID = "r1" },
		"hold about a command with no id":    func(r *Record) { r.ID = "" },
		"hold with the reason of a drop":     func(r *Record) { r.Reason = ReasonHoldAfterDrop },
		"hold with the reason of a resend":   func(r *Record) { r.Reason = ReasonHoldDuringResend },
		"hold with no reason":                func(r *Record) { r.Reason = "" },
		"hold with a reason of another kind": func(r *Record) { r.Reason = ReasonLateEvidence },
		"hold with another target":           func(r *Record) { r.Target = "supervisor" },
		"hold with a sender":                 func(r *Record) { r.Sender = "ataniq" },
		"hold with a kind":                   func(r *Record) { r.Kind = "note" },
		"hold with a state":                  func(r *Record) { r.From, r.To = StateResolved, StateResolved },
		"hold with an operator":              func(r *Record) { r.Operator = "op" },
		"hold with evidence":                 func(r *Record) { r.EvidenceKind, r.Evidence = EvidenceOwnerDecision, "x" },
		"hold with a text":                   func(r *Record) { r.Text = "x" },
		"hold with a predecessor":            func(r *Record) { r.Of = "m1" },
		"hold with a replacement":            func(r *Record) { r.Replacement = "r1" },
		"record of an unknown type":          func(r *Record) { r.Type = "queue-lock" },
	} {
		wantRestoreError(t, name, with(hold(mod)), n+1)
	}
	// A second hold for the same command while the queue is held for it.
	h2 := hold(nil)
	h2.Seq = n + 2
	wantRestoreError(t, "second hold for the same command", with(hold(nil), h2), n+2)
	// A hold about a delivered command, a refused one, a withdrawn one.
	dm, dj := lateTest(t)
	if _, err := dm.Resolve(Resolution{ID: "m1", Decision: DecisionDelivered, Operator: "op"}, at(45*time.Second)); err != nil {
		t.Fatal(err)
	}
	dn := uint64(len(dj.recs))
	h := holdRec(dn+1, "m1", ReasonHoldAfterResend)
	wantRestoreError(t, "hold about a delivered command", append(clone(dj.recs), h), dn+1)
	h.Reason = ReasonHoldAfterDrop
	wantRestoreError(t, "hold about a delivered command, as a drop", append(clone(dj.recs), h), dn+1)
	// A hold about an uncertain command with no resend pending.
	_, lj := lateTest(t)
	ln := uint64(len(lj.recs))
	h = holdRec(ln+1, "m1", ReasonHoldDuringResend)
	wantRestoreError(t, "hold about an uncertain command with no resend", append(clone(lj.recs), h), ln+1)
	// A hold about a dropped command carries the drop reason.
	_, pj := droppedTest(t)
	pn := uint64(len(pj.recs))
	if _, err := Restore(testLimits(), &fakeJournal{}, append(clone(pj.recs), holdRec(pn+1, "m1", ReasonHoldAfterDrop)), at(time.Hour)); err != nil {
		t.Fatalf("a hold after a drop: %v", err)
	}
	wantRestoreError(t, "a hold about a dropped command with the reason of a resend", append(clone(pj.recs), holdRec(pn+1, "m1", ReasonHoldAfterResend)), pn+1)

	// Releases.
	wantRestoreError(t, "a release with no hold", with(releaseRec(n+1, "op")), n+1)
	rel := func(mod func(*Record)) []Record {
		r := releaseRec(n+2, "op")
		if mod != nil {
			mod(&r)
		}
		return with(hold(nil), r)
	}
	if _, err := Restore(testLimits(), &fakeJournal{}, rel(nil), at(time.Hour)); err != nil {
		t.Fatalf("a release: %v", err)
	}
	for name, mod := range map[string]func(*Record){
		"release with no operator":         func(r *Record) { r.Operator = "" },
		"release with a blank operator":    func(r *Record) { r.Operator = "  " },
		"release with a long operator":     func(r *Record) { r.Operator = strings.Repeat("x", maxOperatorBytes+1) },
		"release of another target":        func(r *Record) { r.Target = "lead:b" },
		"release naming a command":         func(r *Record) { r.ID = "m1" },
		"release with a state":             func(r *Record) { r.From, r.To = StateQueued, StateQueued },
		"release with the wrong reason":    func(r *Record) { r.Reason = ReasonOperatorDrop },
		"release with no reason":           func(r *Record) { r.Reason = "" },
		"release as an observation":        func(r *Record) { r.EvidenceKind = EvidenceObservation },
		"release with other evidence text": func(r *Record) { r.Evidence = "ok" },
		"release with a sender":            func(r *Record) { r.Sender = "ataniq" },
		"release with a text":              func(r *Record) { r.Text = "x" },
	} {
		wantRestoreError(t, name, rel(mod), n+2)
	}
	// A second release of the same hold.
	r2 := releaseRec(n+3, "op")
	wantRestoreError(t, "release of a released hold", with(hold(nil), releaseRec(n+2, "op"), r2), n+3)
	// After a release the same command may hold the queue again.
	again := holdRecN(n+3, "m1", ReasonHoldAfterResend, 2)
	if _, err := Restore(testLimits(), &fakeJournal{}, with(hold(nil), releaseRec(n+2, "op"), again), at(time.Hour)); err != nil {
		t.Fatalf("a hold after a release: %v", err)
	}
}

func TestRestoreRefusesWithdrawalsAndDispatchesTheMachineWouldNotWrite(t *testing.T) {
	_, j := resentTest(t)
	good := clone(j.recs)
	n := uint64(len(good))
	with := func(rs ...Record) []Record { return append(clone(good), rs...) }
	// The records the machine writes.
	if _, err := Restore(testLimits(), &fakeJournal{}, with(holdRec(n+1, "m1", ReasonHoldAfterResend), withdrawRec(n+2, "r1")), at(time.Hour)); err != nil {
		t.Fatalf("a withdrawal: %v", err)
	}
	if _, err := Restore(testLimits(), &fakeJournal{}, with(holdRec(n+1, "m1", ReasonHoldAfterResend), releaseRec(n+2, "op"), dispatchOf(n+3, "r1")), at(time.Hour)); err != nil {
		t.Fatalf("a dispatch after a release: %v", err)
	}
	// A withdrawal outside a held queue.
	wantRestoreError(t, "withdrawal from a queue that is not held", with(withdrawRec(n+1, "r1")), n+1)
	wantRestoreError(t, "withdrawal after a release", with(holdRec(n+1, "m1", ReasonHoldAfterResend), releaseRec(n+2, "op"), withdrawRec(n+3, "r1")), n+3)
	for name, mod := range map[string]func(*Record){
		"withdrawal with no operator":       func(r *Record) { r.Operator = "" },
		"withdrawal with another reason":    func(r *Record) { r.Reason = ReasonOperatorDrop },
		"withdrawal as an observation":      func(r *Record) { r.EvidenceKind = EvidenceObservation },
		"withdrawal with another evidence":  func(r *Record) { r.Evidence = string(DecisionDrop) },
		"withdrawal naming a replacement":   func(r *Record) { r.Replacement = "m2" },
		"withdrawal with a predecessor":     func(r *Record) { r.Of = "m1" },
		"withdrawal of a command not there": func(r *Record) { r.ID = "zz" },
	} {
		w := withdrawRec(n+2, "r1")
		mod(&w)
		wantRestoreError(t, name, with(holdRec(n+1, "m1", ReasonHoldAfterResend), w), n+2)
	}
	// A withdrawal of a command that is not queued: m1 is resolved, and a
	// dispatching command is not queued.
	w := withdrawRec(n+2, "m1")
	w.From = StateResolved
	wantRestoreError(t, "withdrawal of a resolved command", with(holdRec(n+1, "m1", ReasonHoldAfterResend), w), n+2)
	// A dispatch while the queue is held, and a note with no hold behind it.
	wantRestoreError(t, "dispatch while held", with(holdRec(n+1, "m1", ReasonHoldAfterResend), dispatchOf(n+2, "r1")), n+2)
	wantRestoreError(t, "note for a resent command with no hold", with(noteRec(n+1, "m1", StateResolved)), n+1)
	wantRestoreError(t, "note after a release", with(holdRec(n+1, "m1", ReasonHoldAfterResend), noteRec(n+2, "m1", StateResolved), releaseRec(n+3, "op"), noteRec(n+4, "m1", StateResolved)), n+4)
	if _, err := Restore(testLimits(), &fakeJournal{}, with(holdRec(n+1, "m1", ReasonHoldAfterResend), noteRec(n+2, "m1", StateResolved)), at(time.Hour)); err != nil {
		t.Fatalf("a note behind its hold: %v", err)
	}
	// A hold about another command does not cover the note for m1.
	wantRestoreError(t, "note held for another command", with(holdRec(n+1, "m1", ReasonHoldAfterResend), noteRec(n+2, "m1", StateResolved), noteRec(n+3, "r1", StateQueued)), n+3)
	// A note for a dropped command with no hold, and for a withdrawn one.
	_, dj := droppedTest(t)
	dn := uint64(len(dj.recs))
	wantRestoreError(t, "note for a dropped command with no hold", append(clone(dj.recs), noteRec(dn+1, "m1", StateResolved)), dn+1)
	if _, err := Restore(testLimits(), &fakeJournal{}, append(clone(dj.recs), holdRec(dn+1, "m1", ReasonHoldAfterDrop), noteRec(dn+2, "m1", StateResolved)), at(time.Hour)); err != nil {
		t.Fatalf("a note behind its hold, after a drop: %v", err)
	}
	wantRestoreError(t, "note for a withdrawn command",
		with(holdRec(n+1, "m1", ReasonHoldAfterResend), withdrawRec(n+2, "r1"), noteRec(n+3, "r1", StateResolved)), n+3)
}

// The note of an uncertain command whose resend is pending needs its hold, and
// the note of one with no resend needs none.
func TestRestoreRefusesANoteForAPendingResendWithNoHold(t *testing.T) {
	_, j := halfResentTest(t)
	good := clone(j.recs)
	n := uint64(len(good))
	wantRestoreError(t, "note during a half-written resend with no hold", append(clone(good), noteRec(n+1, "m1", StateUncertain)), n+1)
	if _, err := Restore(testLimits(), &fakeJournal{}, append(clone(good), holdRec(n+1, "m1", ReasonHoldDuringResend), noteRec(n+2, "m1", StateUncertain)), at(time.Hour)); err != nil {
		t.Fatalf("a note behind its hold: %v", err)
	}
	// With no resend pending, a note is accepted as it always was.
	_, lj := lateTest(t)
	ln := uint64(len(lj.recs))
	if _, err := Restore(testLimits(), &fakeJournal{}, append(clone(lj.recs), noteRec(ln+1, "m1", StateUncertain)), at(time.Hour)); err != nil {
		t.Fatalf("a note for an uncertain command: %v", err)
	}
}

// snapJournal keeps the records and, before each append, the state of its
// machine with every earlier record applied, so a test can compare a restore
// of every prefix with the live machine at that point.
type snapJournal struct {
	fakeJournal
	m     *Machine
	snaps map[int]snapshot
}

func (s *snapJournal) Append(r Record) error {
	if s.failNext {
		return s.fakeJournal.Append(r)
	}
	s.snaps[len(s.recs)] = snap(s.m)
	return s.fakeJournal.Append(r)
}

// hasAccepted reports whether a snapshot has a command in state accepted,
// which a restore completes and the live machine has not yet.
func hasAccepted(s snapshot) bool {
	for _, c := range s.Cmds {
		if c.State == StateAccepted {
			return true
		}
	}
	return false
}

// holdsScenario drives lead a through holds in every way: late evidence for a
// resent command and for a dropped one, a half-written resend, a release, a
// withdrawal, and a hold set again after a release.
func holdsScenario(t *testing.T, m *Machine) {
	t.Helper()
	d := Daemon()
	mustSubmit(t, m, "m1", d, leadA, at(0))
	mustSubmit(t, m, "m2", d, leadA, at(0))
	mustSubmit(t, m, "m3", d, leadA, at(0))
	mustBegin(t, m, leadA, at(time.Second))
	if _, err := m.Tick(at(40 * time.Second)); err != nil { // m1: no fence result
		t.Fatal(err)
	}
	// A late result for an uncertain command: a note only.
	mustFence(t, m, "m1", WrittenPasteAndEnter, OutcomeSubmitted, at(41*time.Second))
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionResend, Operator: "op", NewID: "r1"}, at(45*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Evidence for the resent command: hold, then note.
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	// The same again, and a different one: notes only.
	if err := m.ConfirmDelivery("m1", "file:10", at(51*time.Second)); err != nil {
		t.Fatal(err)
	}
	mustFence(t, m, "m1", WrittenNothing, OutcomeNotIdle, at(52*time.Second))
	// Bad evidence is a piece too, and so is the first evidence again, whose
	// note is skipped.
	wantRefusal(t, m.ConfirmDelivery("m1", " ", at(52*time.Second+300*time.Millisecond)), ReasonBadEvidence)
	if err := m.ConfirmDelivery("m1", "file:10", at(52*time.Second+600*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := m.Withdraw("r1", "op", at(53*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseHold(leadA, "op", seenOf(m, leadA), at(54*time.Second)); err != nil {
		t.Fatal(err)
	}
	// m2 is dispatched while the evidence for m1 arrives again: held again.
	mustBegin(t, m, leadA, at(55*time.Second))
	if err := m.ConfirmDelivery("m1", "file:10", at(56*time.Second)); err != nil {
		t.Fatal(err)
	}
	mustFence(t, m, "m2", WrittenPaste, OutcomeNotSubmitted, at(57*time.Second))
	// m2 uncertain, held: drop it and late evidence for it is a second hold.
	if _, err := m.Resolve(Resolution{ID: "m2", Decision: DecisionDrop, Operator: "op"}, at(58*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := m.ConfirmDelivery("m2", "file:20", at(59*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseHold(leadA, "op", seenOf(m, leadA), at(60*time.Second)); err != nil {
		t.Fatal(err)
	}
	// m3 goes; a half-written resend of it; evidence during it; then finish it.
	mustBegin(t, m, leadA, at(61*time.Second))
	mustFence(t, m, "m3", WrittenPaste, OutcomeNotSubmitted, at(62*time.Second))
	hj := m.journal
	m.journal = &failSecondOf{inner: hj, failAt: 2}
	_, err := m.Resolve(Resolution{ID: "m3", Decision: DecisionResend, Operator: "op", NewID: "r3"}, at(63*time.Second))
	wantJournalError(t, err)
	m.journal = hj
	if err := m.ConfirmDelivery("m3", "file:30", at(64*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(Resolution{ID: "m3", Decision: DecisionResend, Operator: "op", NewID: "r3"}, at(65*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Dispatch is refused while held; release, dispatch r3, evidence again while
	// r3 is dispatching, and its fence says not idle.
	if err := m.ReleaseHold(leadA, "op", seenOf(m, leadA), at(66*time.Second)); err != nil {
		t.Fatal(err)
	}
	mustBegin(t, m, leadA, at(67*time.Second))
	mustFence(t, m, "m3", WrittenNothing, OutcomeBlocked, at(68*time.Second)) // late, for m3: hold
	mustFence(t, m, "r3", WrittenNothing, OutcomeNotIdle, at(69*time.Second))
	if err := m.ReleaseHold(leadA, "op", seenOf(m, leadA), at(70*time.Second)); err != nil {
		t.Fatal(err)
	}
	mustBegin(t, m, leadA, at(71*time.Second))
}

// failSecondOf passes everything to inner and fails the failAt-th append of
// its own.
type failSecondOf struct {
	inner  Journal
	failAt int
	n      int
}

func (f *failSecondOf) Append(r Record) error {
	f.n++
	if f.n == f.failAt {
		return errDisk
	}
	return f.inner.Append(r)
}

func TestRestoreFromEveryPrefixOfAHoldJournalMatchesTheLiveMachine(t *testing.T) {
	sj := &snapJournal{snaps: map[int]snapshot{}}
	m, err := New(testLimits(), sj)
	if err != nil {
		t.Fatal(err)
	}
	sj.m = m
	holdsScenario(t, m)
	sj.snaps[len(sj.recs)] = snap(m)
	holds, releases, withdraws, topSeq := 0, 0, 0, uint64(0)
	for _, s := range sj.snaps {
		for _, tv := range s.View.Targets {
			if tv.HeldSeq > topSeq {
				topSeq = tv.HeldSeq
			}
		}
	}
	if topSeq < 4 {
		t.Fatalf("the scenario reaches the counter %d", topSeq)
	}
	for _, r := range sj.recs {
		switch {
		case r.Type == RecordHold:
			holds++
		case r.Type == RecordRelease:
			releases++
		case r.Reason == ReasonOperatorWithdrawn:
			withdraws++
		}
	}
	if holds < 4 || releases < 3 || withdraws < 1 {
		t.Fatalf("the scenario wrote %d holds, %d releases, %d withdrawals", holds, releases, withdraws)
	}
	compared := 0
	for k := 0; k <= len(sj.recs); k++ {
		rj := &fakeJournal{}
		r, err := Restore(testLimits(), rj, sj.recs[:k], at(24*time.Hour))
		if err != nil {
			t.Fatalf("prefix of %d records: %v", k, err)
		}
		live, ok := sj.snaps[k]
		if !ok || hasAccepted(live) {
			continue
		}
		compared++
		got, want := normalize(snap(r)), normalize(live)
		if !reflect.DeepEqual(got.View, want.View) || !reflect.DeepEqual(got.Cmds, want.Cmds) || !reflect.DeepEqual(got.Texts, want.Texts) {
			t.Errorf("prefix of %d records:\nrestored %+v\nlive     %+v", k, got.View, want.View)
		}
		// What restore writes are completions only, never a hold or a release.
		for _, x := range rj.recs {
			if x.Type != "" || (x.To != StateUncertain && x.To != StateQueued) {
				t.Errorf("restore appended %+v", x)
			}
		}
		if len(r.holds) != len(live.View.heldTargets()) {
			t.Errorf("prefix %d: %d holds restored, %d live", k, len(r.holds), len(live.View.heldTargets()))
		}
		// The restored counter is the live one: a release that saw less is
		// refused, and one that saw it is accepted.
		if seen := seenOf(r, leadA); seen != 0 {
			if want := live.View.heldSeqOf(leadA.String()); seen != want {
				t.Errorf("prefix %d: counter %d restored, %d live", k, seen, want)
			}
			if err := r.ReleaseHold(leadA, "op", seen-1, at(25*time.Hour)); err == nil {
				t.Errorf("prefix %d: a release that saw the counter %d was accepted at %d", k, seen-1, seen)
			}
			if err := r.ReleaseHold(leadA, "op", seen, at(25*time.Hour)); err != nil {
				t.Errorf("prefix %d: a release that saw the counter %d: %v", k, seen, err)
			}
		}
	}
	if compared < len(sj.recs)/2 {
		t.Errorf("compared %d of %d prefixes", compared, len(sj.recs)+1)
	}
}

// heldTargets lists the targets of a view that are held.
func (v View) heldTargets() []string {
	var out []string
	for _, tv := range v.Targets {
		if tv.Held {
			out = append(out, tv.Target)
		}
	}
	return out
}

// heldSeqOf is the counter of a target in a view, 0 when it is not held.
func (v View) heldSeqOf(target string) uint64 {
	for _, tv := range v.Targets {
		if tv.Target == target {
			return tv.HeldSeq
		}
	}
	return 0
}

// roundTrip encodes the records as JSON and decodes them again.
func roundTrip(t *testing.T, rs []Record) []Record {
	t.Helper()
	var out []Record
	for _, r := range rs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var d Record
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		d.At = r.At
		out = append(out, d)
	}
	return out
}

func TestRestoredHoldJournalFromJSON(t *testing.T) {
	m, j := heldTest(t)
	if err := m.ReleaseHold(leadA, "alice", seenOf(m, leadA), at(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := m.ConfirmDelivery("m1", "file:77", at(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	decoded := roundTrip(t, j.recs)
	r, err := Restore(testLimits(), &fakeJournal{}, decoded, at(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sameSnap(t, "restore from JSON", snap(m), snap(r))
	for _, rec := range decoded {
		if rec.Type == RecordHold && (rec.ID != "m1" || rec.Held == 0 || !validPiece(rec.Piece)) {
			t.Errorf("hold %+v", rec)
		}
		if rec.Type == RecordRelease && rec.Held != 1 {
			t.Errorf("release %+v", rec)
		}
	}
}
