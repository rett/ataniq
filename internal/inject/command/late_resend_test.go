package command

import (
	"strings"
	"testing"
	"time"
)

// Tests for late information about a command an operator decided about: the
// hold of the queue, and the note that follows it.

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

// halfResentTest is lateTest after the first append of a resend of m1 as r1
// worked and the second failed: m1 is uncertain, r1 is queued at the head.
func halfResentTest(t *testing.T) (*Machine, *fakeJournal) {
	t.Helper()
	m, j := lateTest(t)
	m.journal = &countJournal{inner: j, failAt: 2}
	_, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionResend, Operator: "op", NewID: "r1"}, at(45*time.Second))
	wantJournalError(t, err)
	m.journal = j
	wantState(t, m, "m1", StateUncertain)
	wantState(t, m, "r1", StateQueued)
	return m, j
}

// droppedTest is lateTest after an operator chose "drop" for m1: m1 is
// resolved and m2 is queued at the head.
func droppedTest(t *testing.T) (*Machine, *fakeJournal) {
	t.Helper()
	m, j := lateTest(t)
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDrop, Operator: "op"}, at(45*time.Second)); err != nil {
		t.Fatal(err)
	}
	return m, j
}

// leadView is the view of lead a's queue.
func leadView(t *testing.T, m *Machine) TargetView {
	t.Helper()
	for _, tv := range m.View().Targets {
		if tv.Target == leadA.String() {
			return tv
		}
	}
	t.Fatal("lead a has no queue and no hold")
	return TargetView{}
}

// wantHeld checks that lead a's queue is held for about: BeginDispatch refuses
// it as queue-held and names the command, and the view says so. It dispatches
// nothing and writes nothing.
func wantHeld(t *testing.T, m *Machine, about string, now time.Time) {
	t.Helper()
	before := snap(m)
	_, err := m.BeginDispatch(leadA, now)
	ref := wantRefusal(t, err, ReasonQueueHeld)
	if ref.CommandID != about || !strings.Contains(ref.Detail, about) {
		t.Errorf("the refusal names %q (%s), want %q", ref.CommandID, ref.Detail, about)
	}
	if v := leadView(t, m); !v.Held || v.HeldFor != about {
		t.Errorf("view %+v", v)
	}
	sameSnap(t, "a refused dispatch", before, snap(m))
}

// wantTicksLeaveItAlone runs an hour of ticks and requires that none writes.
func wantTicksLeaveItAlone(t *testing.T, m *Machine, from time.Time) {
	t.Helper()
	for s := 0; s < 3600; s += 7 {
		if recs, err := m.Tick(from.Add(time.Duration(s) * time.Second)); err != nil || len(recs) != 0 {
			t.Fatalf("tick wrote %+v (%v)", recs, err)
		}
	}
}

// lateCall is one way late information reaches the machine.
type lateCall struct {
	name string
	do   func(m *Machine, id string, now time.Time) error
}

var lateCalls = []lateCall{
	{"evidence", func(m *Machine, id string, now time.Time) error { return m.ConfirmDelivery(id, "file:10", now) }},
	{"fence result", func(m *Machine, id string, now time.Time) error {
		_, err := m.RecordFence(id, FenceResult{Written: WrittenPasteAndEnter, Outcome: OutcomeSubmitted, Readings: "held"}, now)
		return err
	}},
	{"fence result, nothing written", func(m *Machine, id string, now time.Time) error {
		_, err := m.RecordFence(id, FenceResult{Written: WrittenNothing, Outcome: OutcomeNotIdle}, now)
		return err
	}},
}

func TestLateInformationAfterAResendHoldsTheQueueBeforeItWritesTheNote(t *testing.T) {
	for _, lc := range lateCalls {
		t.Run(lc.name, func(t *testing.T) {
			m, j := resentTest(t)
			n := len(j.recs)
			if err := lc.do(m, "m1", at(50*time.Second)); err != nil {
				t.Fatal(err)
			}
			if len(j.recs) != n+2 {
				t.Fatalf("%d records written, want the hold and the note", len(j.recs)-n)
			}
			h, a := j.recs[n], j.recs[n+1]
			if h.Type != RecordHold || h.ID != "m1" || h.Target != "lead:a" || h.Reason != ReasonHoldAfterResend || !h.At.Equal(at(50*time.Second)) ||
				h.From != "" || h.To != "" || h.Operator != "" || h.Evidence != "" || h.Text != "" {
				t.Errorf("hold record %+v", h)
			}
			if a.Type != "" || a.ID != "m1" || a.From != StateResolved || a.To != StateResolved || (a.Reason != ReasonLateEvidence && a.Reason != ReasonLateFenceResult) {
				t.Errorf("note record %+v", a)
			}
			// The original keeps its decision and carries the note; no command moved.
			c, _ := m.Command("m1")
			if c.State != StateResolved || c.Reason != ReasonOperatorResend || c.Replacement != "r1" || len(c.Late) != 1 {
				t.Errorf("m1 %+v", c)
			}
			wantState(t, m, "r1", StateQueued)
			wantState(t, m, "m2", StateQueued)
			wantHeld(t, m, "m1", at(time.Minute))
			if v := leadView(t, m); v.HeldReason != ReasonHoldAfterResend || !v.HeldSince.Equal(at(50*time.Second)) || v.Frozen || v.HeldAlso != nil {
				t.Errorf("view %+v", v)
			}
			wantTicksLeaveItAlone(t, m, at(time.Minute))
			wantHeld(t, m, "m1", at(2*time.Hour))
			if h.Held != 1 || seqA(m) != 1 {
				t.Errorf("counter %d, view %d", h.Held, seqA(m))
			}
			// A second, different piece while held is more information: a hold
			// record that advances the counter, then the note.
			n = len(j.recs)
			if err := m.ConfirmDelivery("m1", "file:11", at(3*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if len(j.recs) != n+2 || j.recs[n].Type != RecordHold || j.recs[n].Held != 2 || j.recs[n+1].Type != "" || j.recs[n+1].From != StateResolved {
				t.Errorf("records %+v", j.recs[n:])
			}
			if seqA(m) != 2 {
				t.Errorf("counter %d", seqA(m))
			}
		})
	}
}

// P1 and P1b of the review: the replacement is dispatching when the evidence
// arrives, and its fence then reports. Whatever it reports, the queue stays
// held and nothing is dispatched.
func TestLateInformationWhileTheReplacementIsDispatching(t *testing.T) {
	outcomes := []struct {
		name  string
		w     Written
		o     FenceOutcome
		state State
	}{
		{"not idle", WrittenNothing, OutcomeNotIdle, StateQueued},
		{"blocked", WrittenNothing, OutcomeBlocked, StateQueued},
		{"untested version", WrittenNothing, OutcomeUntestedVersion, StateRefused},
		{"refused text", WrittenNothing, OutcomeRefusedText, StateRefused},
		{"not submitted", WrittenPaste, OutcomeNotSubmitted, StateUncertain},
		{"died", WrittenUnknown, OutcomeDied, StateUncertain},
		{"submitted", WrittenPasteAndEnter, OutcomeSubmitted, StateDispatching},
	}
	for _, lc := range lateCalls {
		for _, oc := range outcomes {
			t.Run(lc.name+"/"+oc.name, func(t *testing.T) {
				m, _ := resentTest(t)
				mustBegin(t, m, leadA, at(50*time.Second))
				wantState(t, m, "r1", StateDispatching)
				if err := lc.do(m, "m1", at(51*time.Second)); err != nil {
					t.Fatal(err)
				}
				// The dispatching replacement is not interrupted.
				wantState(t, m, "r1", StateDispatching)
				wantHeld(t, m, "m1", at(52*time.Second))
				mustFence(t, m, "r1", oc.w, oc.o, at(53*time.Second))
				wantState(t, m, "r1", oc.state)
				if oc.state == StateDispatching {
					if err := m.ConfirmDelivery("r1", "file:99", at(54*time.Second)); err != nil {
						t.Fatal(err)
					}
					wantState(t, m, "r1", StateDelivered)
				}
				wantHeld(t, m, "m1", at(55*time.Second))
			})
		}
	}
}

func TestLateInformationAfterTheReplacementEnded(t *testing.T) {
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
		"uncertain": {StateUncertain, func(t *testing.T, m *Machine) {
			d := mustBegin(t, m, leadA, at(50*time.Second))
			mustFence(t, m, d.ID, WrittenPaste, OutcomeNotSubmitted, at(51*time.Second))
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
			// A hold record and a note for each of the two pieces.
			if len(j.recs) != n+4 || j.recs[n].Type != RecordHold || j.recs[n+1].From != StateResolved ||
				j.recs[n+2].Type != RecordHold || j.recs[n+3].From != StateResolved {
				t.Fatalf("records %+v", j.recs[n:])
			}
			wantState(t, m, "r1", drive.to)
			wantHeld(t, m, "m1", at(3*time.Minute))
			if notes := lateNotes(t, m, "m1"); len(notes) != 2 {
				t.Errorf("notes %+v", notes)
			}
		})
	}
}

func TestLateInformationAlongAResendChainHoldsOneQueueForEveryCommand(t *testing.T) {
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
	// Evidence for the first command holds the queue.
	if err := m.ConfirmDelivery("m1", "file:10", at(110*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(j.recs) != n+2 || j.recs[n].Type != RecordHold || j.recs[n].ID != "m1" {
		t.Fatalf("records %+v", j.recs[n:])
	}
	wantState(t, m, "r2", StateQueued)
	wantHeld(t, m, "m1", at(2*time.Minute))
	// Evidence for the middle command holds it for that command too, once.
	n = len(j.recs)
	if err := m.ConfirmDelivery("r1", "file:20", at(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(j.recs) != n+2 || j.recs[n].Type != RecordHold || j.recs[n].ID != "r1" || j.recs[n].Reason != ReasonHoldAfterResend {
		t.Fatalf("records %+v", j.recs[n:])
	}
	if v := leadView(t, m); v.HeldFor != "m1" || len(v.HeldAlso) != 1 || v.HeldAlso[0] != "r1" {
		t.Errorf("view %+v", v)
	}
	wantHeld(t, m, "m1", at(4*time.Minute))
	// One release ends the hold for both, and the queue goes on.
	if err := m.ReleaseHold(leadA, "op", seenOf(m, leadA), at(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if v := leadView(t, m); v.Held || v.HeldAlso != nil {
		t.Errorf("view %+v", v)
	}
	if d := mustBegin(t, m, leadA, at(6*time.Minute)); d.ID != "r2" {
		t.Errorf("dispatched %s", d.ID)
	}
}

func TestLateInformationForADroppedCommandHoldsTheQueueBehindIt(t *testing.T) {
	for _, lc := range lateCalls {
		t.Run(lc.name, func(t *testing.T) {
			m, j := droppedTest(t)
			wantState(t, m, "m2", StateQueued)
			n := len(j.recs)
			if err := lc.do(m, "m1", at(50*time.Second)); err != nil {
				t.Fatal(err)
			}
			if len(j.recs) != n+2 || j.recs[n].Type != RecordHold || j.recs[n].ID != "m1" || j.recs[n].Reason != ReasonHoldAfterDrop ||
				j.recs[n+1].From != StateResolved || j.recs[n+1].To != StateResolved {
				t.Fatalf("records %+v", j.recs[n:])
			}
			c, _ := m.Command("m1")
			if c.State != StateResolved || c.Reason != ReasonOperatorDrop || len(c.Late) != 1 {
				t.Errorf("m1 %+v", c)
			}
			wantHeld(t, m, "m1", at(time.Minute))
			wantTicksLeaveItAlone(t, m, at(time.Minute))
			// The release lets m2 go.
			if err := m.ReleaseHold(leadA, "op", seenOf(m, leadA), at(2*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if d := mustBegin(t, m, leadA, at(2*time.Hour)); d.ID != "m2" {
				t.Errorf("dispatched %s", d.ID)
			}
		})
	}
}

// P2 of the review: the evidence arrives while a resend is half written.
func TestLateEvidenceDuringAHalfWrittenResendHoldsTheQueue(t *testing.T) {
	m, j := halfResentTest(t)
	n := len(j.recs)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(j.recs) != n+2 || j.recs[n].Type != RecordHold || j.recs[n].Reason != ReasonHoldDuringResend || j.recs[n].ID != "m1" ||
		j.recs[n+1].From != StateUncertain || j.recs[n+1].To != StateUncertain {
		t.Fatalf("records %+v", j.recs[n:])
	}
	// The queue is frozen by m1 as well as held.
	if v := leadView(t, m); !v.Frozen || v.FrozenBy != "m1" || !v.Held || v.HeldReason != ReasonHoldDuringResend {
		t.Errorf("view %+v", v)
	}
	wantHeld(t, m, "m1", at(51*time.Second))
	// The operator can only finish the resend; Resolve refuses anything else.
	_, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDelivered, Operator: "op"}, at(52*time.Second))
	wantRefusal(t, err, ReasonWrongState)
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionResend, Operator: "op", NewID: "r1", SeenLate: seenNotes(t, m, "m1")}, at(53*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "m1", StateResolved)
	wantState(t, m, "r1", StateQueued)
	// The completed resend does not release the hold, and it does not write a
	// second one for the same command.
	wantHeld(t, m, "m1", at(54*time.Second))
	wantTicksLeaveItAlone(t, m, at(time.Minute))
	holds := 0
	for _, r := range j.recs {
		if r.Type == RecordHold {
			holds++
		}
	}
	if holds != 1 {
		t.Errorf("%d hold records", holds)
	}
}

// With a resend half written and the queue held, the operator who does not
// want the resend can withdraw the replacement, and then decide about the
// original.
func TestAHalfWrittenResendInAHeldQueueCanBeWithdrawn(t *testing.T) {
	m, _ := halfResentTest(t)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := m.Withdraw("r1", "op", at(51*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "r1", StateResolved)
	// The original is uncertain with nothing pending: the evidence says it
	// arrived, and the operator says so.
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDelivered, Operator: "op", SeenLate: seenNotes(t, m, "m1")}, at(52*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantHeld(t, m, "m1", at(53*time.Second))
	if err := m.ReleaseHold(leadA, "op", seenOf(m, leadA), at(54*time.Second)); err != nil {
		t.Fatal(err)
	}
	if d := mustBegin(t, m, leadA, at(55*time.Second)); d.ID != "m2" {
		t.Errorf("dispatched %s", d.ID)
	}
}

// P3 of the review: eight different notes, then the resend, then new evidence.
func TestLateEvidenceWithTheNoteCapFullStillHoldsTheQueue(t *testing.T) {
	m, j := lateTest(t)
	for i := 0; i < maxLateNotes; i++ {
		if err := m.ConfirmDelivery("m1", "file:"+strings.Repeat("x", i+1), at(time.Duration(41+i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range j.recs {
		if r.Type != "" {
			t.Fatalf("an uncertain command with no resend wrote %+v", r)
		}
	}
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionResend, Operator: "op", NewID: "r1", SeenLate: seenNotes(t, m, "m1")}, at(time.Minute)); err != nil {
		t.Fatal(err)
	}
	n := len(j.recs)
	wantRefusal(t, m.ConfirmDelivery("m1", "file:99", at(70*time.Second)), ReasonLateNotesFull)
	if len(j.recs) != n+1 || j.recs[n].Type != RecordHold || j.recs[n].ID != "m1" || j.recs[n].Reason != ReasonHoldAfterResend {
		t.Fatalf("records %+v", j.recs[n:])
	}
	wantHeld(t, m, "m1", at(71*time.Second))
	if got := len(lateNotes(t, m, "m1")); got != maxLateNotes {
		t.Errorf("%d notes", got)
	}
	// A fence result is a new piece: the hold is not written again, but the
	// counter advances, and the note is refused. So is the evidence once more,
	// which is no longer the last piece.
	_, err := m.RecordFence("m1", FenceResult{Written: WrittenNothing, Outcome: OutcomeNotIdle}, at(72*time.Second))
	wantRefusal(t, err, ReasonLateNotesFull)
	wantRefusal(t, m.ConfirmDelivery("m1", "file:99", at(73*time.Second)), ReasonLateNotesFull)
	if len(j.recs) != n+3 || j.recs[n+1].Type != RecordHold || j.recs[n+2].Type != RecordHold {
		t.Errorf("%d records after repeats: %+v", len(j.recs)-n, j.recs[n:])
	}
	if got := seenOf(m, leadA); got != 3 {
		t.Errorf("counter %d", got)
	}
	wantState(t, m, "r1", StateQueued)
}

// A note that repeats an earlier one is skipped, but after a release the
// repeat holds the queue again.
func TestARepeatedNoteAfterAReleaseHoldsTheQueueAgain(t *testing.T) {
	m, j := resentTest(t)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	// While held, the repeat writes nothing.
	n := len(j.recs)
	if err := m.ConfirmDelivery("m1", "file:10", at(51*time.Second)); err != nil || len(j.recs) != n {
		t.Fatalf("a repeat while held: %v, %d records", err, len(j.recs)-n)
	}
	if err := m.ReleaseHold(leadA, "op", seenOf(m, leadA), at(52*time.Second)); err != nil {
		t.Fatal(err)
	}
	n = len(j.recs)
	if err := m.ConfirmDelivery("m1", "file:10", at(53*time.Second)); err != nil {
		t.Fatalf("the repeat after a release: %v", err)
	}
	if len(j.recs) != n+1 || j.recs[n].Type != RecordHold || j.recs[n].ID != "m1" || !j.recs[n].At.Equal(at(53*time.Second)) {
		t.Fatalf("records %+v", j.recs[n:])
	}
	if got := len(lateNotes(t, m, "m1")); got != 1 {
		t.Errorf("%d notes", got)
	}
	wantHeld(t, m, "m1", at(54*time.Second))
	if v := leadView(t, m); !v.HeldSince.Equal(at(53 * time.Second)) {
		t.Errorf("held since %v", v.HeldSince)
	}
}

// Late information for an uncertain command with no resend pending is a note
// and nothing else: that command already freezes the queue.
func TestLateInformationForAnUncertainCommandWithNoResendHoldsNothing(t *testing.T) {
	for _, lc := range lateCalls {
		t.Run(lc.name, func(t *testing.T) {
			m, j := lateTest(t)
			n := len(j.recs)
			if err := lc.do(m, "m1", at(50*time.Second)); err != nil {
				t.Fatal(err)
			}
			if len(j.recs) != n+1 || j.recs[n].Type != "" || j.recs[n].From != StateUncertain {
				t.Fatalf("records %+v", j.recs[n:])
			}
			if v := leadView(t, m); v.Held || !v.Frozen {
				t.Errorf("view %+v", v)
			}
			_, err := m.BeginDispatch(leadA, at(time.Minute))
			wantRefusal(t, err, ReasonQueueFrozen)
		})
	}
}

func TestLateInformationOnAJournalFailureAtEitherAppend(t *testing.T) {
	for _, lc := range lateCalls {
		t.Run(lc.name+"/hold fails", func(t *testing.T) {
			m, j := resentTest(t)
			before, n := snap(m), len(j.recs)
			j.failNext = true
			wantJournalError(t, lc.do(m, "m1", at(50*time.Second)))
			sameSnap(t, "a failed hold", before, snap(m))
			if len(j.recs) != n {
				t.Error("a failed append left a record")
			}
			if len(m.holds) != 0 {
				t.Error("the queue is held")
			}
			// The daemon repeats the call.
			if err := lc.do(m, "m1", at(51*time.Second)); err != nil {
				t.Fatal(err)
			}
			if len(j.recs) != n+2 || j.recs[n].Type != RecordHold || j.recs[n+1].From != StateResolved {
				t.Fatalf("records %+v", j.recs[n:])
			}
			wantHeld(t, m, "m1", at(52*time.Second))
		})
		t.Run(lc.name+"/note fails", func(t *testing.T) {
			m, j := resentTest(t)
			n := len(j.recs)
			m.journal = &countJournal{inner: j, failAt: 2}
			wantJournalError(t, lc.do(m, "m1", at(50*time.Second)))
			m.journal = j
			// The hold stands; the note is not written.
			if len(j.recs) != n+1 || j.recs[n].Type != RecordHold {
				t.Fatalf("records %+v", j.recs[n:])
			}
			if len(lateNotes(t, m, "m1")) != 0 {
				t.Error("a note without a record")
			}
			wantHeld(t, m, "m1", at(51*time.Second))
			// The repeat writes the note only.
			if err := lc.do(m, "m1", at(52*time.Second)); err != nil {
				t.Fatal(err)
			}
			if len(j.recs) != n+2 || j.recs[n+1].Type != "" || j.recs[n+1].From != StateResolved {
				t.Fatalf("records %+v", j.recs[n:])
			}
			if len(lateNotes(t, m, "m1")) != 1 {
				t.Error("the note is missing")
			}
			wantHeld(t, m, "m1", at(53*time.Second))
		})
	}
}

func TestLateRecordsOfADeliveredCommandAreRefused(t *testing.T) {
	m, j := lateTest(t)
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDelivered, Operator: "op"}, at(45*time.Second)); err != nil {
		t.Fatal(err)
	}
	n := len(j.recs)
	wantRefusal(t, m.ConfirmDelivery("m1", "file:10", at(50*time.Second)), ReasonWrongState)
	_, err := m.RecordFence("m1", FenceResult{Written: WrittenPasteAndEnter, Outcome: OutcomeSubmitted}, at(50*time.Second))
	wantRefusal(t, err, ReasonWrongState)
	if len(j.recs) != n {
		t.Error("a refused late record was journaled")
	}
	if len(m.holds) != 0 {
		t.Error("the queue is held")
	}
}

func TestLateRecordsOfAWithdrawnCommandAreRefused(t *testing.T) {
	m, j := resentTest(t)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := m.Withdraw("r1", "op", at(51*time.Second)); err != nil {
		t.Fatal(err)
	}
	n := len(j.recs)
	wantRefusal(t, m.ConfirmDelivery("r1", "file:11", at(52*time.Second)), ReasonWrongState)
	_, err := m.RecordFence("r1", FenceResult{Written: WrittenNothing, Outcome: OutcomeNotIdle}, at(52*time.Second))
	wantRefusal(t, err, ReasonWrongState)
	if len(j.recs) != n {
		t.Error("a refused late record was journaled")
	}
}

func TestRestoreOfALateAfterResendJournalGivesTheSameView(t *testing.T) {
	m, j := resentTest(t)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	n := len(j.recs)
	jr := &fakeJournal{}
	r, err := Restore(testLimits(), jr, j.recs, at(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sameSnap(t, "restore after late evidence for a resent command", snap(m), snap(r))
	wantHeld(t, r, "m1", at(time.Hour))
	wantTicksLeaveItAlone(t, r, at(time.Hour))
	if len(jr.recs) != 0 || len(j.recs) != n {
		t.Error("a restore wrote a record, or the live journal grew")
	}
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
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDelivered, Operator: "op", SeenLate: seenNotes(t, m, "m1")}, at(2*time.Minute)); err != nil {
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
