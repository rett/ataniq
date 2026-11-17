package command

import (
	"strings"
	"testing"
	"time"
)

// Tests for the late records of a command an operator resolved as "send
// again": the annotation on it, and the freeze of its queued replacement.

// resentTest is lateTest after an operator chose "send again" for m1 as r1:
// m1 is resolved, r1 is queued at the head and m2 is behind it.
func resentTest(t *testing.T) (*Machine, *fakeJournal) {
	t.Helper()
	m, j := lateTest(t)
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionResend, Operator: "op", NewID: "r1"}, at(45*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "m1", StateResolved)
	wantState(t, m, "r1", StateQueued)
	return m, j
}

// wantFrozenReplacement checks the state after late evidence for a command
// that was sent again: nothing dispatches, the replacement is uncertain with
// the new reason, and the queue is frozen by it.
func wantFrozenReplacement(t *testing.T, m *Machine, replacement string, now time.Time) {
	t.Helper()
	wantState(t, m, replacement, StateUncertain)
	c, _ := m.Command(replacement)
	if c.Reason != ReasonOriginalEvidenceArrived {
		t.Errorf("replacement %s has reason %q", replacement, c.Reason)
	}
	_, err := m.BeginDispatch(leadA, now)
	wantRefusal(t, err, ReasonQueueFrozen)
	if v := m.View().Targets[0]; !v.Frozen || v.FrozenBy != replacement {
		t.Errorf("view %+v", v)
	}
	for s := 0; s < 3600; s += 7 {
		if recs, err := m.Tick(now.Add(time.Duration(s) * time.Second)); err != nil || len(recs) != 0 {
			t.Fatalf("tick wrote %+v (%v)", recs, err)
		}
	}
}

func TestLateEvidenceAfterAResendFreezesTheQueuedReplacement(t *testing.T) {
	m, j := resentTest(t)
	n := len(j.recs)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatalf("late evidence after a resend: %v", err)
	}
	if len(j.recs) != n+2 {
		t.Fatalf("%d records written, want 2", len(j.recs)-n)
	}
	a, f := j.recs[n], j.recs[n+1]
	if a.ID != "m1" || a.From != StateResolved || a.To != StateResolved || a.Reason != ReasonLateEvidence || a.EvidenceKind != EvidenceProviderReport || a.Evidence != "file:10" {
		t.Errorf("annotation %+v", a)
	}
	if f.ID != "r1" || f.From != StateQueued || f.To != StateUncertain || f.Reason != ReasonOriginalEvidenceArrived || f.Operator != "" {
		t.Errorf("freeze record %+v", f)
	}
	// The original keeps its decision and carries the note.
	c, _ := m.Command("m1")
	if c.State != StateResolved || c.Reason != ReasonOperatorResend || c.Replacement != "r1" || len(c.Late) != 1 || c.Late[0].Evidence != "file:10" {
		t.Errorf("m1 %+v", c)
	}
	wantFrozenReplacement(t, m, "r1", at(time.Minute))
	// An operator decides about the replacement as usual: drop it, and the
	// queue goes on with m2.
	if _, err := m.Resolve(Resolution{ID: "r1", Decision: DecisionDrop, Operator: "op"}, at(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if d := mustBegin(t, m, leadA, at(2*time.Hour)); d.ID != "m2" {
		t.Errorf("dispatched %s", d.ID)
	}
}

func TestLateFenceResultAfterAResendFreezesTheQueuedReplacement(t *testing.T) {
	m, j := resentTest(t)
	n := len(j.recs)
	up, err := m.RecordFence("m1", FenceResult{Written: WrittenPasteAndEnter, Outcome: OutcomeSubmitted, Readings: "held"}, at(50*time.Second))
	if err != nil || up.State != StateResolved {
		t.Fatalf("late result after a resend: %v %+v", err, up)
	}
	if len(j.recs) != n+2 || j.recs[n].Reason != ReasonLateFenceResult || j.recs[n].From != StateResolved || j.recs[n+1].Reason != ReasonOriginalEvidenceArrived {
		t.Fatalf("records %+v", j.recs[n:])
	}
	wantFrozenReplacement(t, m, "r1", at(time.Minute))
	// Each decision still works on the frozen replacement: a second resend.
	if _, err := m.Resolve(Resolution{ID: "r1", Decision: DecisionResend, Operator: "op", NewID: "r2"}, at(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "r1", StateResolved)
	wantState(t, m, "r2", StateQueued)
	if d := mustBegin(t, m, leadA, at(2*time.Hour)); d.ID != "r2" {
		t.Errorf("dispatched %s", d.ID)
	}
}

func TestLateEvidenceAlongAResendChainFreezesTheNewestReplacement(t *testing.T) {
	m, j := resentTest(t)
	// r1 is dispatched, a Tick makes it uncertain, and it is sent again as r2.
	mustBegin(t, m, leadA, at(50*time.Second))
	if _, err := m.Tick(at(90 * time.Second)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "r1", StateUncertain)
	if _, err := m.Resolve(Resolution{ID: "r1", Decision: DecisionResend, Operator: "op", NewID: "r2"}, at(100*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "r2", StateQueued)
	n := len(j.recs)
	// Evidence for the first command freezes the newest one, r2.
	if err := m.ConfirmDelivery("m1", "file:10", at(110*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(j.recs) != n+2 || j.recs[n+1].ID != "r2" {
		t.Fatalf("records %+v", j.recs[n:])
	}
	wantState(t, m, "r1", StateResolved)
	wantFrozenReplacement(t, m, "r2", at(2*time.Minute))
	// Evidence for the middle command is an annotation on it, and r2 is
	// already uncertain: only the annotation is written.
	n = len(j.recs)
	if err := m.ConfirmDelivery("r1", "file:20", at(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(j.recs) != n+1 || j.recs[n].ID != "r1" || j.recs[n].From != StateResolved {
		t.Fatalf("records %+v", j.recs[n:])
	}
	// A chain of two: the decision on r2 (resend as r3), then evidence for m1.
	if _, err := m.Resolve(Resolution{ID: "r2", Decision: DecisionResend, Operator: "op", NewID: "r3"}, at(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	n = len(j.recs)
	if _, err := m.RecordFence("m1", FenceResult{Written: WrittenNothing, Outcome: OutcomeNotIdle}, at(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(j.recs) != n+2 || j.recs[n+1].ID != "r3" || j.recs[n+1].Reason != ReasonOriginalEvidenceArrived {
		t.Fatalf("records %+v", j.recs[n:])
	}
	wantFrozenReplacement(t, m, "r3", at(6*time.Minute))
}

func TestLateEvidenceAfterAResendOnlyAnnotatesADispatchingOrEndedReplacement(t *testing.T) {
	for name, drive := range map[string]struct {
		to    State
		setup func(t *testing.T, m *Machine)
	}{
		"dispatching": {StateDispatching, func(t *testing.T, m *Machine) { mustBegin(t, m, leadA, at(50*time.Second)) }},
		"delivered": {StateDelivered, func(t *testing.T, m *Machine) {
			id := submitted(t, m, leadA, at(50*time.Second))
			if err := m.ConfirmDelivery(id, "file:1", at(51*time.Second)); err != nil {
				t.Fatal(err)
			}
		}},
		"refused": {StateRefused, func(t *testing.T, m *Machine) {
			d := mustBegin(t, m, leadA, at(50*time.Second))
			mustFence(t, m, d.ID, WrittenNothing, OutcomeRefusedText, at(51*time.Second))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			m, j := resentTest(t)
			drive.setup(t, m)
			wantState(t, m, "r1", drive.to)
			n := len(j.recs)
			if err := m.ConfirmDelivery("m1", "file:10", at(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, err := m.RecordFence("m1", FenceResult{Written: WrittenPaste, Outcome: OutcomeNotSubmitted}, at(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if len(j.recs) != n+2 {
				t.Fatalf("%d records written, want the 2 annotations", len(j.recs)-n)
			}
			for _, r := range j.recs[n:] {
				if r.ID != "m1" || r.From != StateResolved || r.To != StateResolved {
					t.Errorf("record %+v is not an annotation on m1", r)
				}
			}
			wantState(t, m, "r1", drive.to)
			if notes := lateNotes(t, m, "m1"); len(notes) != 2 {
				t.Errorf("notes %+v", notes)
			}
		})
	}
}

func TestRestoreOfALateAfterResendJournalGivesTheSameView(t *testing.T) {
	m, j := resentTest(t)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	n := len(j.recs)
	r, err := Restore(testLimits(), &fakeJournal{}, j.recs, at(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sameSnap(t, "restore after late evidence for a resent command", snap(m), snap(r))
	wantFrozenReplacement(t, r, "r1", at(time.Hour))
	if len(j.recs) != n {
		t.Error("the live journal grew")
	}
}

func TestRestoreCompletesAFreezeThatWasNeverWritten(t *testing.T) {
	m, j := resentTest(t)
	j.failNext = false
	// The annotation is journaled; the record that freezes r1 is not.
	n := len(j.recs)
	failSecond := &countJournal{inner: j, failAt: 2}
	m.journal = failSecond
	err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second))
	wantJournalError(t, err)
	if len(j.recs) != n+1 {
		t.Fatalf("%d records written, want the annotation only", len(j.recs)-n)
	}
	// r1 holds its queue although it is still queued: nothing dispatches.
	wantState(t, m, "r1", StateQueued)
	_, derr := m.BeginDispatch(leadA, at(51*time.Second))
	wantRefusal(t, derr, ReasonQueueFrozen)
	// Restore writes the missing record, as the machine would have.
	jr := &fakeJournal{}
	r, rerr := Restore(testLimits(), jr, j.recs, at(time.Hour))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(jr.recs) != 1 || jr.recs[0].ID != "r1" || jr.recs[0].Reason != ReasonOriginalEvidenceArrived || jr.recs[0].To != StateUncertain {
		t.Fatalf("restore wrote %+v", jr.recs)
	}
	wantFrozenReplacement(t, r, "r1", at(time.Hour))
	// So does a Tick of the live machine.
	recs, terr := m.Tick(at(2 * time.Minute))
	if terr != nil || len(recs) != 1 || recs[0].ID != "r1" || recs[0].Reason != ReasonOriginalEvidenceArrived {
		t.Fatalf("tick wrote %+v (%v)", recs, terr)
	}
	wantFrozenReplacement(t, m, "r1", at(3*time.Minute))
}

// countJournal appends to inner, and fails the append numbered failAt (from 1)
// after it was set.
type countJournal struct {
	inner  *fakeJournal
	failAt int
	n      int
}

func (c *countJournal) Append(r Record) error {
	c.n++
	if c.n == c.failAt {
		return errDisk
	}
	return c.inner.Append(r)
}

func TestRepeatedLateEvidenceFinishesAFreezeThatFailed(t *testing.T) {
	m, j := resentTest(t)
	m.journal = &countJournal{inner: j, failAt: 2}
	wantJournalError(t, m.ConfirmDelivery("m1", "file:10", at(50*time.Second)))
	n := len(j.recs)
	// The same evidence again: the note is not written twice; the freeze is.
	if err := m.ConfirmDelivery("m1", "file:10", at(51*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(j.recs) != n+1 || j.recs[n].ID != "r1" || j.recs[n].Reason != ReasonOriginalEvidenceArrived {
		t.Fatalf("records %+v", j.recs[n:])
	}
	if notes := lateNotes(t, m, "m1"); len(notes) != 1 {
		t.Errorf("notes %+v", notes)
	}
	wantFrozenReplacement(t, m, "r1", at(time.Minute))
}

func TestLateEvidenceAfterAResendOnAJournalFailureChangesNothing(t *testing.T) {
	m, j := resentTest(t)
	before := snap(m)
	n := len(j.recs)
	j.failNext = true
	wantJournalError(t, m.ConfirmDelivery("m1", "file:10", at(50*time.Second)))
	sameSnap(t, "a failed late evidence after a resend", before, snap(m))
	j.failNext = true
	_, err := m.RecordFence("m1", FenceResult{Written: WrittenPasteAndEnter, Outcome: OutcomeSubmitted}, at(50*time.Second))
	wantJournalError(t, err)
	sameSnap(t, "a failed late result after a resend", before, snap(m))
	if len(j.recs) != n {
		t.Error("a failed append left a record")
	}
	wantState(t, m, "r1", StateQueued)
	if d := mustBegin(t, m, leadA, at(time.Minute)); d.ID != "r1" {
		t.Errorf("dispatched %s", d.ID)
	}
}

func TestLateRecordsOfADroppedOrDeliveredCommandAreRefused(t *testing.T) {
	m, _ := lateTest(t)
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDrop, Operator: "op"}, at(45*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, m.ConfirmDelivery("m1", "file:10", at(50*time.Second)), ReasonWrongState)
	_, err := m.RecordFence("m1", FenceResult{Written: WrittenPasteAndEnter, Outcome: OutcomeSubmitted}, at(50*time.Second))
	wantRefusal(t, err, ReasonWrongState)
	wantState(t, m, "m2", StateQueued)
}

func TestRestoreRefusesLateRecordsOfResolvedCommandsTheMachineWouldNotWrite(t *testing.T) {
	m, j := resentTest(t)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	all := clone(j.recs)
	nAll := len(all)
	// The journal up to, and without, the late records.
	good := clone(all[:nAll-2])
	n := uint64(len(good))
	annotation := func(id string) Record {
		return Record{
			Seq: n + 1, At: t0, ID: id, Target: "lead:a", Sender: "ataniq", Kind: "note",
			From: StateResolved, To: StateResolved, Reason: ReasonLateEvidence, EvidenceKind: EvidenceProviderReport, Evidence: "file:1",
		}
	}
	freeze := func(id string) Record {
		return Record{
			Seq: n + 2, At: t0, ID: id, Target: "lead:a", Sender: "ataniq", Kind: "note",
			From: StateQueued, To: StateUncertain, Reason: ReasonOriginalEvidenceArrived,
		}
	}
	with := func(rs ...Record) []Record { return append(clone(good), rs...) }
	if _, err := Restore(testLimits(), &fakeJournal{}, with(annotation("m1"), freeze("r1")), at(time.Hour)); err != nil {
		t.Fatalf("the records the machine writes: %v", err)
	}
	// The same journal, as the machine wrote it.
	if _, err := Restore(testLimits(), &fakeJournal{}, all, at(time.Hour)); err != nil {
		t.Fatalf("the live journal: %v", err)
	}
	// A freeze with no late evidence behind it.
	lone := freeze("r1")
	lone.Seq = n + 1
	wantRestoreError(t, "freeze with no evidence", with(lone), n+1)
	wantRestoreError(t, "freeze of m2, which replaces nothing", with(annotation("m1"), freeze("m2")), n+2)
	for name, mod := range map[string]func(*Record){
		"freeze with an operator": func(r *Record) { r.Operator = "op" },
		"freeze with evidence":    func(r *Record) { r.EvidenceKind, r.Evidence = EvidenceObservation, "unknown" },
		"freeze with a reason":    func(r *Record) { r.Reason = ReasonNoFenceResult },
		"freeze with a replacement": func(r *Record) {
			r.Replacement = "x"
		},
	} {
		f := freeze("r1")
		mod(&f)
		wantRestoreError(t, name, with(annotation("m1"), f), n+2)
	}
	// A second freeze of the same command.
	f2 := freeze("r1")
	f2.Seq = n + 3
	wantRestoreError(t, "second freeze", with(annotation("m1"), freeze("r1"), f2), n+3)
	// Malformed annotations.
	for name, mod := range map[string]func(*Record){
		"annotation with an operator": func(r *Record) { r.Operator = "op" },
		"annotation with a bad kind":  func(r *Record) { r.EvidenceKind = EvidenceOwnerDecision },
		"annotation on r1 queued":     func(r *Record) { r.ID, r.From, r.To = "r1", StateQueued, StateQueued },
	} {
		a := annotation("m1")
		mod(&a)
		wantRestoreError(t, name, with(a), n+1)
	}
	// A dropped command takes no late record.
	dm, dj := lateTest(t)
	if _, err := dm.Resolve(Resolution{ID: "m1", Decision: DecisionDrop, Operator: "op"}, at(45*time.Second)); err != nil {
		t.Fatal(err)
	}
	dn := uint64(len(dj.recs))
	a := annotation("m1")
	a.Seq = dn + 1
	wantRestoreError(t, "annotation on a dropped command", append(clone(dj.recs), a), dn+1)
}

func TestARepeatedLateNoteIsSkippedAndTheCapHasItsOwnReason(t *testing.T) {
	m, j := lateTest(t)
	n := len(j.recs)
	for i := 0; i < 20; i++ {
		if err := m.ConfirmDelivery("m1", "file:1", at(time.Minute)); err != nil {
			t.Fatalf("repeat %d: %v", i, err)
		}
		if _, err := m.RecordFence("m1", FenceResult{Written: WrittenPasteAndEnter, Outcome: OutcomeSubmitted, Readings: "r"}, at(time.Minute)); err != nil {
			t.Fatalf("repeat %d: %v", i, err)
		}
	}
	if len(j.recs) != n+2 {
		t.Fatalf("%d records written for two distinct notes", len(j.recs)-n)
	}
	// Fill up with distinct notes.
	for i := 2; i < maxLateNotes; i++ {
		if err := m.ConfirmDelivery("m1", "file:"+strings.Repeat("x", i), at(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if len(lateNotes(t, m, "m1")) != maxLateNotes {
		t.Fatalf("notes %d", len(lateNotes(t, m, "m1")))
	}
	n = len(j.recs)
	// A repeat is still skipped when the cap is reached; a new note is refused
	// with its own reason, not as a wrong state.
	if err := m.ConfirmDelivery("m1", "file:1", at(time.Minute)); err != nil {
		t.Errorf("a repeat at the cap: %v", err)
	}
	r := wantRefusal(t, m.ConfirmDelivery("m1", "file:other", at(time.Minute)), ReasonLateNotesFull)
	if r.Reason == ReasonWrongState {
		t.Error("the cap is refused as a wrong state")
	}
	_, err := m.RecordFence("m1", FenceResult{Written: WrittenNothing, Outcome: OutcomeNotIdle}, at(time.Minute))
	wantRefusal(t, err, ReasonLateNotesFull)
	if len(j.recs) != n {
		t.Error("a skipped or refused note was journaled")
	}
	// An ended command keeps its notes for the audit.
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDelivered, Operator: "op"}, at(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := lateNotes(t, m, "m1"); len(got) != maxLateNotes {
		t.Errorf("an ended command has %d notes", len(got))
	}
}

func TestRestoreRefusesNoFenceResultAfterEnter(t *testing.T) {
	good := goodRecords(t) // m1 has its Enter recorded
	n := uint64(len(good))
	r := Record{
		Seq: n + 1, At: t0, ID: "m1", Target: "lead:a", Sender: "ataniq", Kind: "note",
		From: StateDispatching, To: StateUncertain, Reason: ReasonNoFenceResult, EvidenceKind: EvidenceObservation, Evidence: "unknown",
	}
	wantRestoreError(t, "no-fence-result after Enter", append(clone(good), r), n+1)
	// The same record before Enter is what a Tick writes.
	before := clone(good[:len(good)-1])
	r.Seq = uint64(len(before)) + 1
	if _, err := Restore(testLimits(), &fakeJournal{}, append(before, r), at(time.Hour)); err != nil {
		t.Fatalf("no-fence-result before Enter: %v", err)
	}
}
