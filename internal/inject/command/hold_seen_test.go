package command

import (
	"strings"
	"testing"
	"time"
)

// Tests of the fifth review: evidence refused as bad is still late information
// and holds the queue (finding 1), and a release covers only the information
// the operator has seen (finding 2: the hold counter).

// holdRecords returns the hold records among recs.
func holdRecords(recs []Record) []Record {
	var out []Record
	for _, r := range recs {
		if r.Type == RecordHold {
			out = append(out, r)
		}
	}
	return out
}

func TestEvidenceRefusedAsBadStillHoldsTheQueue(t *testing.T) {
	cases := map[string]struct {
		setup  func(t *testing.T) (*Machine, *fakeJournal)
		reason Reason
	}{
		"resent":      {resentTest, ReasonHoldAfterResend},
		"half resent": {halfResentTest, ReasonHoldDuringResend},
		"dropped":     {droppedTest, ReasonHoldAfterDrop},
	}
	bads := map[string]string{
		"over long": strings.Repeat("x", maxEvidenceBytes+1),
		"empty":     "",
		"blank":     "   ",
		"newlines":  "\n\t \n",
	}
	for cname, c := range cases {
		for bname, bad := range bads {
			t.Run(cname+"/"+bname, func(t *testing.T) {
				m, j := c.setup(t)
				n := len(j.recs)
				wantRefusal(t, m.ConfirmDelivery("m1", bad, at(50*time.Second)), ReasonBadEvidence)
				// One record: the hold. No note, because the evidence is unusable.
				if len(j.recs) != n+1 {
					t.Fatalf("records %+v", j.recs[n:])
				}
				h := j.recs[n]
				if h.Type != RecordHold || h.ID != "m1" || h.Reason != c.reason || h.Held != 1 || !h.At.Equal(at(50*time.Second)) {
					t.Errorf("hold record %+v", h)
				}
				if got := lateNotes(t, m, "m1"); len(got) != 0 {
					t.Errorf("notes %+v", got)
				}
				wantHeld(t, m, "m1", at(51*time.Second))
				wantTicksLeaveItAlone(t, m, at(time.Minute))
				if v := leadView(t, m); v.HeldSeq != 1 || v.HeldReason != c.reason {
					t.Errorf("view %+v", v)
				}
				// The same bad call again is a repeat of the last piece: nothing.
				wantRefusal(t, m.ConfirmDelivery("m1", bad, at(52*time.Second)), ReasonBadEvidence)
				if len(j.recs) != n+1 || seqA(m) != 1 {
					t.Errorf("a repeat wrote %+v, counter %d", j.recs[n+1:], seqA(m))
				}
				// Good evidence after it is new information: counter 2 and a note.
				if err := m.ConfirmDelivery("m1", "file:10", at(53*time.Second)); err != nil {
					t.Fatal(err)
				}
				if seqA(m) != 2 || len(lateNotes(t, m, "m1")) != 1 {
					t.Errorf("counter %d, notes %d", seqA(m), len(lateNotes(t, m, "m1")))
				}
			})
		}
	}
}

func TestBadEvidenceHoldOnAJournalFailureChangesNothing(t *testing.T) {
	m, j := resentTest(t)
	before, n := snap(m), len(j.recs)
	j.failNext = true
	wantJournalError(t, m.ConfirmDelivery("m1", "", at(50*time.Second)))
	sameSnap(t, "a bad evidence call whose hold failed", before, snap(m))
	if len(j.recs) != n {
		t.Fatal("a record was written")
	}
	// The daemon repeats the call: refused as bad, and held.
	wantRefusal(t, m.ConfirmDelivery("m1", "", at(51*time.Second)), ReasonBadEvidence)
	wantHeld(t, m, "m1", at(52*time.Second))
}

func TestBadEvidenceForAnUncertainCommandWithNoResendHoldsNothing(t *testing.T) {
	m, j := lateTest(t)
	n := len(j.recs)
	wantRefusal(t, m.ConfirmDelivery("m1", "", at(50*time.Second)), ReasonBadEvidence)
	if len(j.recs) != n || leadView(t, m).Held {
		t.Errorf("records %+v, view %+v", j.recs[n:], leadView(t, m))
	}
}

func TestBadEvidenceForAnEndedCommandIsRefusedAndNotJournaled(t *testing.T) {
	m, j := lateTest(t)
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDelivered, Operator: "op"}, at(45*time.Second)); err != nil {
		t.Fatal(err)
	}
	n := len(j.recs)
	wantRefusal(t, m.ConfirmDelivery("m1", "", at(50*time.Second)), ReasonWrongState)
	if len(j.recs) != n {
		t.Errorf("records %+v", j.recs[n:])
	}
}

// Every piece of late information advances the counter, whether its note is
// written, skipped, or refused; only a call that repeats the last piece byte
// for byte does not.
func TestEveryPieceOfLateInformationAdvancesTheCounter(t *testing.T) {
	m, j := resentTest(t)
	fence := func(w Written, o FenceOutcome, readings string, now time.Time) error {
		_, err := m.RecordFence("m1", FenceResult{Written: w, Outcome: o, Readings: readings}, now)
		return err
	}
	type call struct {
		name    string
		do      func(now time.Time) error
		want    Reason // the refusal, or "" for none
		records int    // records the call writes
		seq     uint64 // the counter after it
		notes   int    // the notes of m1 after it
	}
	calls := []call{
		{"first evidence", func(now time.Time) error { return m.ConfirmDelivery("m1", "file:10", now) }, "", 2, 1, 1},
		{"the same evidence again", func(now time.Time) error { return m.ConfirmDelivery("m1", "file:10", now) }, "", 0, 1, 1},
		{"a fence result", func(now time.Time) error { return fence(WrittenPasteAndEnter, OutcomeSubmitted, "a", now) }, "", 2, 2, 2},
		{"the same fence result again", func(now time.Time) error { return fence(WrittenPasteAndEnter, OutcomeSubmitted, "a", now) }, "", 0, 2, 2},
		{"the same fence result with other readings", func(now time.Time) error { return fence(WrittenPasteAndEnter, OutcomeSubmitted, "b", now) }, "", 2, 3, 3},
		{"the first evidence once more: its note is skipped", func(now time.Time) error { return m.ConfirmDelivery("m1", "file:10", now) }, "", 1, 4, 3},
		{"evidence that is refused as bad", func(now time.Time) error { return m.ConfirmDelivery("m1", " ", now) }, ReasonBadEvidence, 1, 5, 3},
		{"the same bad evidence again", func(now time.Time) error { return m.ConfirmDelivery("m1", " ", now) }, ReasonBadEvidence, 0, 5, 3},
		{"a fence result after it", func(now time.Time) error { return fence(WrittenPasteAndEnter, OutcomeSubmitted, "a", now) }, "", 1, 6, 3},
	}
	for i, c := range calls {
		n := len(j.recs)
		err := c.do(at(time.Duration(50+i) * time.Second))
		if c.want == "" && err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.want != "" {
			wantRefusal(t, err, c.want)
		}
		if got := len(j.recs) - n; got != c.records {
			t.Fatalf("%s: %d records written, want %d: %+v", c.name, got, c.records, j.recs[n:])
		}
		if c.records > 0 && j.recs[n].Type != RecordHold {
			t.Errorf("%s: the first record is %+v", c.name, j.recs[n])
		}
		if got := seqA(m); got != c.seq {
			t.Fatalf("%s: counter %d, want %d", c.name, got, c.seq)
		}
		if got := len(lateNotes(t, m, "m1")); got != c.notes {
			t.Fatalf("%s: %d notes, want %d", c.name, got, c.notes)
		}
		wantHeld(t, m, "m1", at(time.Hour))
	}
	// The counter is journaled with each hold record.
	for i, h := range holdRecords(j.recs) {
		if h.Held != uint64(i+1) || !validPiece(h.Piece) {
			t.Errorf("hold record %d: %+v", i, h)
		}
	}
}

// A piece whose note is refused because the list is full advances the counter,
// and a call that repeats the refused piece does not.
func TestAPieceWhoseNoteIsRefusedAdvancesTheCounter(t *testing.T) {
	m, j := resentTest(t)
	for i := 0; i < maxLateNotes; i++ {
		if err := m.ConfirmDelivery("m1", "file:"+strings.Repeat("x", i+1), at(time.Duration(50+i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if got := seqA(m); got != maxLateNotes {
		t.Fatalf("counter %d", got)
	}
	n := len(j.recs)
	wantRefusal(t, m.ConfirmDelivery("m1", "file:new", at(time.Minute)), ReasonLateNotesFull)
	if seqA(m) != maxLateNotes+1 || len(j.recs) != n+1 {
		t.Errorf("counter %d, %d records", seqA(m), len(j.recs)-n)
	}
	wantRefusal(t, m.ConfirmDelivery("m1", "file:new", at(time.Minute)), ReasonLateNotesFull)
	if seqA(m) != maxLateNotes+1 || len(j.recs) != n+1 {
		t.Errorf("a repeat: counter %d, %d records", seqA(m), len(j.recs)-n)
	}
}

// P7 of the review: the operator reads the view after the first piece, then a
// second piece arrives, then the release. The release must not cover the
// second piece.
func TestAReleaseDecidedOnTheFirstPieceIsRefusedAfterTheSecond(t *testing.T) {
	m, j := resentTest(t)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	seen := leadView(t, m).HeldSeq
	if seen == 0 {
		t.Fatal("the view shows no counter")
	}
	if _, err := m.RecordFence("m1", FenceResult{Written: WrittenPasteAndEnter, Outcome: OutcomeSubmitted, Readings: "paste and Enter"}, at(60*time.Second)); err != nil {
		t.Fatal(err)
	}
	before, n := snap(m), len(j.recs)
	ref := wantRefusal(t, m.ReleaseHold(leadA, "alice", seen, at(70*time.Second)), ReasonHoldChanged)
	if ref.Reason != "hold-changed" || ref.CommandID != "m1" {
		t.Errorf("refusal %+v", ref)
	}
	sameSnap(t, "a refused release", before, snap(m))
	if len(j.recs) != n {
		t.Fatalf("records %+v", j.recs[n:])
	}
	wantHeld(t, m, "m1", at(71*time.Second))
	// The operator looks again and releases what is shown.
	now := leadView(t, m).HeldSeq
	if now <= seen {
		t.Fatalf("counter %d after %d", now, seen)
	}
	if err := m.ReleaseHold(leadA, "alice", now, at(72*time.Second)); err != nil {
		t.Fatal(err)
	}
	if last := j.recs[len(j.recs)-1]; last.Type != RecordRelease || last.Held != now {
		t.Errorf("release record %+v", last)
	}
	if d := mustBegin(t, m, leadA, at(73*time.Second)); d.ID != "r1" {
		t.Errorf("dispatched %s", d.ID)
	}
}

// A second piece about another command the queue is held for also changes what
// a release covers.
func TestAReleaseIsRefusedWhenInformationAboutAnotherCommandArrives(t *testing.T) {
	m, _ := lateTest(t)
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDrop, Operator: "op"}, at(45*time.Second)); err != nil {
		t.Fatal(err)
	}
	mustBegin(t, m, leadA, at(46*time.Second))
	mustFence(t, m, "m2", WrittenPaste, OutcomeNotSubmitted, at(47*time.Second))
	if _, err := m.Resolve(Resolution{ID: "m2", Decision: DecisionDrop, Operator: "op"}, at(48*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	seen := seenOf(m, leadA)
	if err := m.ConfirmDelivery("m2", "file:20", at(51*time.Second)); err != nil {
		t.Fatal(err)
	}
	v := leadView(t, m)
	if v.HeldFor != "m1" || len(v.HeldAlso) != 1 || v.HeldAlso[0] != "m2" || v.HeldSeq != seen+1 {
		t.Fatalf("view %+v", v)
	}
	wantRefusal(t, m.ReleaseHold(leadA, "alice", seen, at(52*time.Second)), ReasonHoldChanged)
	// A third piece, about the first command, moves it again.
	if err := m.ConfirmDelivery("m1", "file:11", at(53*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, m.ReleaseHold(leadA, "alice", seen+1, at(54*time.Second)), ReasonHoldChanged)
	if err := m.ReleaseHold(leadA, "alice", seen+2, at(55*time.Second)); err != nil {
		t.Fatal(err)
	}
	if seenOf(m, leadA) != 0 {
		t.Error("a released queue shows a counter")
	}
}

// The counter is kept across a release, so a value the operator saw in an
// earlier hold never matches a later one.
func TestTheCounterIsNotResetByARelease(t *testing.T) {
	m, j := resentTest(t)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseHold(leadA, "alice", 1, at(51*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := m.ConfirmDelivery("m1", "file:10", at(52*time.Second)); err != nil {
		t.Fatal(err)
	}
	if v := leadView(t, m); v.HeldSeq != 2 {
		t.Fatalf("view %+v", v)
	}
	wantRefusal(t, m.ReleaseHold(leadA, "alice", 1, at(53*time.Second)), ReasonHoldChanged)
	wantRefusal(t, m.ReleaseHold(leadA, "alice", 0, at(53*time.Second)), ReasonHoldChanged)
	if err := m.ReleaseHold(leadA, "alice", 2, at(54*time.Second)); err != nil {
		t.Fatal(err)
	}
	if h := holdRecords(j.recs); len(h) != 2 || h[0].Held != 1 || h[1].Held != 2 {
		t.Errorf("hold records %+v", h)
	}
	// A restored machine goes on counting from the journal.
	r, err := Restore(testLimits(), &fakeJournal{}, j.recs, at(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ConfirmDelivery("m1", "file:10", at(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := seenOf(r, leadA); got != 3 {
		t.Errorf("counter %d after restore", got)
	}
}

func TestReleaseHoldChecksTheCounterAfterTheQueueAndTheOperator(t *testing.T) {
	m, _ := heldTest(t)
	wantRefusal(t, m.ReleaseHold(leadB, "alice", 99, at(time.Minute)), ReasonNotHeld)
	wantRefusal(t, m.ReleaseHold(leadA, "", 99, at(time.Minute)), ReasonBadOperator)
	wantRefusal(t, m.ReleaseHold(leadA, "alice", 99, at(time.Minute)), ReasonHoldChanged)
	wantHeld(t, m, "m1", at(2*time.Minute))
}

func TestRestoreRefusesHoldAndReleaseCountersTheMachineWouldNotWrite(t *testing.T) {
	_, j := heldTest(t) // one hold record, counter 1, then its note
	good := clone(j.recs)
	n := uint64(len(good))
	first := holdRecords(good)[0]
	if first.Held != 1 {
		t.Fatalf("first hold %+v", first)
	}
	with := func(rs ...Record) []Record { return append(clone(good), rs...) }

	// A second piece of information: counter 2, a new piece.
	if _, err := Restore(testLimits(), &fakeJournal{}, with(holdRecN(n+1, "m1", ReasonHoldAfterResend, 2)), at(time.Hour)); err != nil {
		t.Fatalf("a second piece: %v", err)
	}
	for name, held := range map[string]uint64{"zero": 0, "the same": 1, "skipping one": 3} {
		wantRestoreError(t, "hold with the counter "+name, with(holdRecN(n+1, "m1", ReasonHoldAfterResend, held)), n+1)
	}
	for name, piece := range map[string]string{
		"no piece":        "",
		"not hex":         strings.Repeat("z", 64),
		"short":           first.Piece[:62],
		"upper case":      strings.ToUpper(first.Piece),
		"longer than one": first.Piece + "00",
	} {
		r := holdRecN(n+1, "m1", ReasonHoldAfterResend, 2)
		r.Piece = piece
		wantRestoreError(t, "hold with "+name, with(r), n+1)
	}
	// The machine journals no hold for a call that repeats the last piece.
	r := holdRecN(n+1, "m1", ReasonHoldAfterResend, 2)
	r.Piece = first.Piece
	wantRestoreError(t, "hold that repeats the last piece", with(r), n+1)
	// A piece that repeats an earlier one but not the last is information.
	r2 := holdRecN(n+2, "m1", ReasonHoldAfterResend, 3)
	r2.Piece = first.Piece
	if _, err := Restore(testLimits(), &fakeJournal{}, with(holdRecN(n+1, "m1", ReasonHoldAfterResend, 2), r2), at(time.Hour)); err != nil {
		t.Fatalf("an earlier piece again: %v", err)
	}

	// Releases carry the counter that was current.
	wantRestoreError(t, "release with the counter 0", with(releaseRecN(n+1, "op", 0)), n+1)
	wantRestoreError(t, "release that saw too much", with(releaseRecN(n+1, "op", 2)), n+1)
	wantRestoreError(t, "release that saw too little after a second piece", with(holdRecN(n+1, "m1", ReasonHoldAfterResend, 2), releaseRecN(n+2, "op", 1)), n+2)
	if _, err := Restore(testLimits(), &fakeJournal{}, with(holdRecN(n+1, "m1", ReasonHoldAfterResend, 2), releaseRecN(n+2, "op", 2)), at(time.Hour)); err != nil {
		t.Fatalf("a release of the current counter: %v", err)
	}
	// The counter goes on after a release: the next hold sets 2 + 1.
	rel := with(releaseRecN(n+1, "op", 1))
	wantRestoreError(t, "a hold after a release that restarts the counter", append(clone(rel), holdRecN(n+2, "m1", ReasonHoldAfterResend, 1)), n+2)
	again := holdRecN(n+2, "m1", ReasonHoldAfterResend, 2)
	if _, err := Restore(testLimits(), &fakeJournal{}, append(clone(rel), again), at(time.Hour)); err != nil {
		t.Fatalf("a hold after a release: %v", err)
	}
	// After it, a release that saw the counter of the first hold is stale.
	wantRestoreError(t, "a stale release in a second hold", append(clone(rel), again, releaseRecN(n+3, "op", 1)), n+3)
	// And the same piece as the first hold's is not a repeat in a new hold.
	same := holdRecN(n+2, "m1", ReasonHoldAfterResend, 2)
	same.Piece = first.Piece
	if _, err := Restore(testLimits(), &fakeJournal{}, append(clone(rel), same), at(time.Hour)); err != nil {
		t.Fatalf("the first piece again after a release: %v", err)
	}
	// Only hold and release records carry a counter or a piece.
	note := noteRec(n+1, "m1", StateResolved)
	note.Held = 1
	wantRestoreError(t, "a note with a counter", with(note), n+1)
	note = noteRec(n+1, "m1", StateResolved)
	note.Piece = first.Piece
	wantRestoreError(t, "a note with a piece", with(note), n+1)
}
