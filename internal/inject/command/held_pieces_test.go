package command

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Tests of the final review, finding B: a counted piece of late information is
// readable. The hold record keeps a bounded, safe rendering of the call, and
// the view shows the last pieces, whether or not the note could be stored.

// P2 of the review: the ninth piece is refused as late-notes-full and the
// release used to cover it unread.
func TestTheNinthPieceIsInTheJournalAndInTheView(t *testing.T) {
	m, j := resentTest(t)
	for i := 0; i < maxLateNotes; i++ {
		if err := m.ConfirmDelivery("m1", "file:"+strings.Repeat("x", i+1), at(time.Duration(50+i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	const ninth = "conv: user turn id=m1 DELIVERED"
	n := len(j.recs)
	wantRefusal(t, m.ConfirmDelivery("m1", ninth, at(time.Minute)), ReasonLateNotesFull)
	if len(j.recs) != n+1 {
		t.Fatalf("records %+v", j.recs[n:])
	}
	h := j.recs[n]
	if h.Type != RecordHold || h.Held != maxLateNotes+1 || h.Detail != "late-evidence: "+ninth {
		t.Errorf("hold record %+v", h)
	}
	v := leadView(t, m)
	if v.HeldSeq != maxLateNotes+1 || len(v.HeldPieces) != maxHeldPieces {
		t.Fatalf("view: counter %d, %d pieces", v.HeldSeq, len(v.HeldPieces))
	}
	last := v.HeldPieces[len(v.HeldPieces)-1]
	want := HeldPiece{Held: maxLateNotes + 1, ID: "m1", At: at(time.Minute), Detail: "late-evidence: " + ninth}
	if last != want {
		t.Errorf("last piece %+v, want %+v", last, want)
	}
	// The pieces are the last eight, oldest first: counters 2 to 9.
	for i, p := range v.HeldPieces {
		if p.Held != uint64(i+2) {
			t.Errorf("piece %d has counter %d", i, p.Held)
		}
	}
	if len(lateNotes(t, m, "m1")) != maxLateNotes {
		t.Error("the refused piece was stored as a note")
	}
	// A release of what the operator saw, with its pieces, goes through, and the
	// pieces end with the hold.
	if err := m.ReleaseHold(leadA, "op", v.HeldSeq, at(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if v := leadView(t, m); v.Held || len(v.HeldPieces) != 0 {
		t.Errorf("after the release %+v", v)
	}
}

func TestEveryKindOfPieceHasARenderingInTheView(t *testing.T) {
	m, j := resentTest(t)
	n := len(j.recs)
	if _, err := m.RecordFence("m1", FenceResult{Written: "weird", Outcome: OutcomeNotIdle, Readings: "busy\x1b[31m"}, at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, m.ConfirmDelivery("m1", strings.Repeat("y", maxEvidenceBytes+1), at(51*time.Second)), ReasonBadEvidence)
	wantRefusal(t, m.ConfirmDelivery("m1", "  ", at(52*time.Second)), ReasonBadEvidence)
	if err := m.ConfirmDelivery("m1", "file:7\x00", at(53*time.Second)); err != nil {
		t.Fatal(err)
	}
	ps := leadView(t, m).HeldPieces
	if len(ps) != 4 {
		t.Fatalf("pieces %+v", ps)
	}
	wants := []string{
		"late-fence-result: written unknown, outcome not idle; busy�[31m",
		"late-evidence: " + strings.Repeat("y", maxEvidenceBytes+1),
		"late-evidence:   ",
		"late-evidence: file:7�",
	}
	for i, p := range ps {
		if p.Detail != wants[i] || p.Held != uint64(i+1) || p.ID != "m1" || !p.At.Equal(at(time.Duration(50+i)*time.Second)) {
			t.Errorf("piece %d: %+v, want detail %q", i, p, wants[i])
		}
	}
	// What the view shows is what the journal holds, for every hold record.
	hs := holdRecords(j.recs[n:])
	if len(hs) != 4 {
		t.Fatalf("hold records %d", len(hs))
	}
	for i, h := range hs {
		if h.Detail != ps[i].Detail {
			t.Errorf("record %d detail %q, view %q", i, h.Detail, ps[i].Detail)
		}
	}
}

func TestAPieceRenderingIsCutAtTheDetailBound(t *testing.T) {
	m, j := halfResentTest(t)
	big := strings.Repeat("é", 5000)
	if _, err := m.RecordFence("m1", FenceResult{Written: WrittenPaste, Outcome: OutcomeNotSubmitted, Readings: big}, at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	h := holdRecords(j.recs)[0]
	if len(h.Detail) == 0 || len(h.Detail) > maxDetailBytes || !strings.HasPrefix(h.Detail, "late-fence-result: written paste, outcome not submitted; ") {
		t.Errorf("detail of %d bytes: %.80q", len(h.Detail), h.Detail)
	}
	if got := leadView(t, m).HeldPieces; len(got) != 1 || got[0].Detail != h.Detail {
		t.Errorf("view %+v", got)
	}
}

func TestRestoreRebuildsTheHeldPieces(t *testing.T) {
	m, j := resentTest(t)
	for i := 0; i < maxLateNotes+3; i++ {
		_ = m.ConfirmDelivery("m1", "file:"+strings.Repeat("z", i+1), at(time.Duration(50+i)*time.Second))
	}
	// The note list is full, so this piece is refused, and still counted.
	_, err := m.RecordFence("m1", FenceResult{Written: WrittenPaste, Outcome: OutcomeNotSubmitted}, at(2*time.Minute))
	wantRefusal(t, err, ReasonLateNotesFull)
	r, err := Restore(testLimits(), &fakeJournal{}, roundTrip(t, j.recs), at(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got, want := leadView(t, r).HeldPieces, leadView(t, m).HeldPieces
	if len(want) != maxHeldPieces || !reflect.DeepEqual(got, want) {
		t.Errorf("restored %+v\nlive     %+v", got, want)
	}
}

func TestRestoreRefusesAHoldWithoutABoundedSafeRendering(t *testing.T) {
	m, j := resentTest(t)
	if err := m.ConfirmDelivery("m1", "file:10", at(50*time.Second)); err != nil {
		t.Fatal(err)
	}
	var hold int
	for i, r := range j.recs {
		if r.Type == RecordHold {
			hold = i
		}
	}
	for name, detail := range map[string]string{
		"empty":     "",
		"too long":  strings.Repeat("a", maxDetailBytes+1),
		"a control": "late-evidence: \x1b[2J",
		"bad utf8":  "late-evidence: \xff",
	} {
		t.Run(name, func(t *testing.T) {
			rs := append([]Record(nil), j.recs...)
			rs[hold].Detail = detail
			if _, err := Restore(testLimits(), &fakeJournal{}, rs, at(time.Hour)); err == nil {
				t.Error("restored")
			}
		})
	}
}
