package command

import (
	"testing"
	"time"
)

// Tests of the final review, finding C: a decision about an uncertain command
// covers only the late notes the operator saw (Resolution.SeenLate).

var allDecisions = []Resolution{
	{Decision: DecisionDelivered},
	{Decision: DecisionDrop},
	{Decision: DecisionResend, NewID: "r1"},
}

// P1 of the review: a note arrives after the operator read the command, and
// the decision used to go through.
func TestADecisionIsRefusedWhenANoteArrivedAfterTheOperatorRead(t *testing.T) {
	for _, d := range allDecisions {
		t.Run(string(d.Decision), func(t *testing.T) {
			m, j := lateTest(t)
			seen := seenNotes(t, m, "m1")
			if seen != 0 {
				t.Fatalf("notes %d", seen)
			}
			// Evidence for an uncertain command with no resend pending is a note
			// only: no hold, no counter.
			if err := m.ConfirmDelivery("m1", "conv:line 42 id=m1", at(50*time.Second)); err != nil {
				t.Fatal(err)
			}
			if v := leadView(t, m); v.Held {
				t.Fatalf("held: %+v", v)
			}
			before, n := snap(m), len(j.recs)

			d.ID, d.Operator, d.SeenLate = "m1", "op", seen
			_, err := m.Resolve(d, at(time.Minute))
			ref := wantRefusal(t, err, ReasonNotesChanged)
			if ref.CommandID != "m1" {
				t.Errorf("refusal %+v", ref)
			}
			if len(j.recs) != n {
				t.Errorf("a refused decision journaled %+v", j.recs[n:])
			}
			sameSnap(t, "a refused decision", before, snap(m))
			wantState(t, m, "m1", StateUncertain)

			// Too many is refused as well, then the current count is accepted.
			d.SeenLate = 2
			_, err = m.Resolve(d, at(time.Minute))
			wantRefusal(t, err, ReasonNotesChanged)
			d.SeenLate = seenNotes(t, m, "m1")
			if d.SeenLate != 1 {
				t.Fatalf("notes %d", d.SeenLate)
			}
			if _, err := m.Resolve(d, at(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if got := stateOf(t, m, "m1"); got == StateUncertain {
				t.Error("still uncertain")
			}
		})
	}
}

func TestADecisionOnACommandWithNoNotesNeedsNoCount(t *testing.T) {
	m, _ := lateTest(t)
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDrop, Operator: "op"}, at(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

// A late fence result is a note too, and a repeat of a note the command has is
// skipped: it does not change the count the operator saw.
func TestOnlyAStoredNoteChangesTheCountTheOperatorSaw(t *testing.T) {
	m, _ := lateTest(t)
	if err := m.ConfirmDelivery("m1", "file:1", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	seen := seenNotes(t, m, "m1")
	if err := m.ConfirmDelivery("m1", "file:1", at(51*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDrop, Operator: "op", SeenLate: seen}, at(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

// The half-written resend: repeating the same call finishes it only with the
// current count, so the note that arrived in between is seen first.
func TestAHalfWrittenResendIsFinishedOnlyWithTheNotesSeen(t *testing.T) {
	m, j := halfResentTest(t)
	res := Resolution{ID: "m1", Decision: DecisionResend, Operator: "op", NewID: "r1", SeenLate: seenNotes(t, m, "m1")}
	if err := m.ConfirmDelivery("m1", "conv:line 42 id=m1", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	if v := leadView(t, m); !v.Held || v.HeldReason != ReasonHoldDuringResend {
		t.Fatalf("view %+v", v)
	}
	before, n := snap(m), len(j.recs)
	_, err := m.Resolve(res, at(time.Minute))
	wantRefusal(t, err, ReasonNotesChanged)
	if len(j.recs) != n {
		t.Errorf("journaled %+v", j.recs[n:])
	}
	sameSnap(t, "the refused repeat", before, snap(m))
	wantState(t, m, "m1", StateUncertain)
	wantState(t, m, "r1", StateQueued)

	res.SeenLate = seenNotes(t, m, "m1")
	if res.SeenLate != 1 {
		t.Fatalf("notes %d", res.SeenLate)
	}
	rs, err := m.Resolve(res, at(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	// Only the second record of the resend is written.
	if rs.NewID != "r1" || len(j.recs) != n+1 || j.recs[n].From != StateUncertain || j.recs[n].To != StateResolved {
		t.Errorf("resolved %+v, records %+v", rs, j.recs[n:])
	}
	wantState(t, m, "m1", StateResolved)
	// The hold stands for the operator to release.
	wantHeld(t, m, "m1", at(2*time.Minute))
}
