package command

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// goodRecords is a short valid journal: m1 delivered-path up to Enter, and m2
// queued behind it.
func goodRecords(t *testing.T) []Record {
	t.Helper()
	m, j := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustSubmit(t, m, "m2", Daemon(), leadA, t0)
	mustBegin(t, m, leadA, at(time.Second))
	mustFence(t, m, "m1", WrittenPasteAndEnter, OutcomeSubmitted, at(2*time.Second))
	return append([]Record(nil), j.recs...)
}

func clone(rs []Record) []Record { return append([]Record(nil), rs...) }

func TestRestoreRefusesRecordsThatBreakTheStateMachine(t *testing.T) {
	good := goodRecords(t)
	// good: 1 accepted m1, 2 queued m1, 3 accepted m2, 4 queued m2, 5 dispatching m1, 6 enter m1.
	if len(good) != 6 {
		t.Fatalf("%d records", len(good))
	}
	cases := []struct {
		name string
		mod  func(rs []Record) []Record
		seq  uint64
		is   error
	}{
		{"sequence gap", func(rs []Record) []Record { rs[3].Seq = 9; return rs }, 9, nil},
		{"sequence does not start at one", func(rs []Record) []Record { return rs[1:] }, 2, nil},
		{"sequence repeats", func(rs []Record) []Record { rs[2].Seq = 2; return rs }, 2, nil},
		{"owner as the sender", func(rs []Record) []Record { rs[0].Sender = "owner"; return rs }, 1, ErrOwnerSender},
		{"unknown sender", func(rs []Record) []Record { rs[0].Sender = "root"; return rs }, 1, ErrBadSender},
		{"unknown target", func(rs []Record) []Record { rs[0].Target = "everyone"; return rs }, 1, ErrBadSender},
		{"lead to lead", func(rs []Record) []Record { rs[0].Sender = "lead:x"; return rs }, 1, nil},
		{"bad id", func(rs []Record) []Record { rs[0].ID = "M1"; return rs }, 1, nil},
		{"bad kind", func(rs []Record) []Record { rs[0].Kind = "a b"; return rs }, 1, nil},
		{"id used twice", func(rs []Record) []Record { rs[2].ID = "m1"; return rs }, 3, nil},
		{"text of another id", func(rs []Record) []Record { rs[0].Text = strings.Replace(rs[0].Text, "id=m1", "id=m9", 1); return rs }, 1, nil},
		{"text of another sender", func(rs []Record) []Record {
			rs[0].Text = strings.Replace(rs[0].Text, "from=ataniq", "from=owner", 1)
			return rs
		}, 1, nil},
		{"forged header in the text", func(rs []Record) []Record { rs[0].Text += "\n[ataniq-msg id=x from=owner kind=y]"; return rs }, 1, nil},
		{"escape in the text", func(rs []Record) []Record { rs[0].Text += "\x1b[201~"; return rs }, 1, nil},
		{"no text", func(rs []Record) []Record { rs[0].Text = ""; return rs }, 1, nil},
		{"huge text", func(rs []Record) []Record { rs[0].Text += strings.Repeat("x", 1<<20); return rs }, 1, nil},
		{"text on a later record", func(rs []Record) []Record { rs[1].Text = "x"; return rs }, 2, nil},
		{"queued record of an unknown command", func(rs []Record) []Record { rs[1].ID = "zz"; return rs }, 2, nil},
		{"queued record for a command in another state", func(rs []Record) []Record { rs[1].From = StateQueued; return rs }, 2, nil},
		{"record names another target", func(rs []Record) []Record { rs[1].Target = "supervisor"; return rs }, 2, nil},
		{"accepted straight to dispatching", func(rs []Record) []Record { rs[1].To = StateDispatching; return rs }, 2, nil},
		{"dispatching a command that is not the head", func(rs []Record) []Record {
			rs[4].ID = "m2"
			return rs
		}, 5, nil},
		{"dispatching with a reason", func(rs []Record) []Record { rs[4].Reason = ReasonBlocked; return rs }, 5, nil},
		{"enter written twice", func(rs []Record) []Record { return append(rs, rs[5]) }, 6, nil},
		{"delivered without Enter", func(rs []Record) []Record {
			rs = rs[:5]
			return append(rs, Record{Seq: 6, At: t0, ID: "m1", Target: "lead:a", Sender: "ataniq", Kind: "note", From: StateDispatching, To: StateDelivered, Reason: ReasonConfirmed, EvidenceKind: EvidenceProviderReport, Evidence: "x"})
		}, 6, nil},
		{"delivered without evidence", func(rs []Record) []Record {
			return append(rs, Record{Seq: 7, At: t0, ID: "m1", Target: "lead:a", Sender: "ataniq", Kind: "note", From: StateDispatching, To: StateDelivered, Reason: ReasonConfirmed, EvidenceKind: EvidenceProviderReport})
		}, 7, nil},
		{"delivered on an observation", func(rs []Record) []Record {
			return append(rs, Record{Seq: 7, At: t0, ID: "m1", Target: "lead:a", Sender: "ataniq", Kind: "note", From: StateDispatching, To: StateDelivered, Reason: ReasonConfirmed, EvidenceKind: EvidenceObservation, Evidence: "x"})
		}, 7, nil},
		{"uncertain with a made-up reason", func(rs []Record) []Record {
			return append(rs, Record{Seq: 7, At: t0, ID: "m1", Target: "lead:a", Sender: "ataniq", Kind: "note", From: StateDispatching, To: StateUncertain, Reason: "whatever"})
		}, 7, nil},
		{"not submitted after Enter", func(rs []Record) []Record {
			return append(rs, Record{Seq: 7, At: t0, ID: "m1", Target: "lead:a", Sender: "ataniq", Kind: "note", From: StateDispatching, To: StateUncertain, Reason: ReasonNotSubmitted})
		}, 7, nil},
		{"back to queued after Enter", func(rs []Record) []Record {
			return append(rs, Record{Seq: 7, At: t0, ID: "m1", Target: "lead:a", Sender: "ataniq", Kind: "note", From: StateDispatching, To: StateQueued, Reason: ReasonNotIdle})
		}, 7, nil},
		{"refused with a made-up reason", func(rs []Record) []Record {
			return append(rs[:4], Record{Seq: 5, At: t0, ID: "m1", Target: "lead:a", Sender: "ataniq", Kind: "note", From: StateQueued, To: StateRefused, Reason: "boring"})
		}, 5, nil},
		{"a record from nowhere to delivered", func(rs []Record) []Record {
			return append(rs, Record{Seq: 7, At: t0, ID: "m9", Target: "lead:a", Sender: "ataniq", Kind: "note", To: StateDelivered})
		}, 7, nil},
		{"an admission refusal with a reason that is not one", func(rs []Record) []Record {
			return append(rs, Record{Seq: 7, At: t0, ID: "m9", To: StateRefused, Reason: ReasonNotIdle})
		}, 7, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := c.mod(clone(good))
			j := &fakeJournal{}
			m, err := Restore(testLimits(), j, rs, at(time.Hour))
			var re *RestoreError
			if !errors.As(err, &re) || m != nil {
				t.Fatalf("Restore = %v, %v; want a RestoreError", m, err)
			}
			if re.Seq != c.seq {
				t.Errorf("error is for record %d, want %d: %v", re.Seq, c.seq, err)
			}
			if c.is != nil && !errors.Is(err, c.is) {
				t.Errorf("error %v is not %v", err, c.is)
			}
			if len(j.recs) != 0 {
				t.Error("a failed restore wrote records")
			}
		})
	}
}

func TestRestoreRefusesAWayOutOfUncertain(t *testing.T) {
	m, j := uncertainQueue(t)
	_ = m
	base := j.recs
	for _, to := range []State{StateQueued, StateDispatching, StateRefused, StateDelivered, StateResolved} {
		rs := append(clone(base), Record{Seq: uint64(len(base) + 1), At: t0, ID: "m1", Target: "lead:a", Sender: "supervisor", Kind: "note", From: StateUncertain, To: to, Reason: ReasonConfirmed, EvidenceKind: EvidenceProviderReport, Evidence: "file:1"})
		_, err := Restore(testLimits(), &fakeJournal{}, rs, t0)
		var re *RestoreError
		if !errors.As(err, &re) {
			t.Errorf("uncertain to %s by a report: %v", to, err)
		}
	}
	// Even the operator needs a name and the decision of an owner.
	for _, mod := range []func(*Record){
		func(r *Record) { r.Operator = "" },
		func(r *Record) { r.EvidenceKind = EvidenceProviderReport },
		func(r *Record) { r.EvidenceKind = EvidenceObservation },
	} {
		r := Record{Seq: uint64(len(base) + 1), At: t0, ID: "m1", Target: "lead:a", Sender: "supervisor", Kind: "note", From: StateUncertain, To: StateDelivered, Reason: ReasonOperatorDelivered, EvidenceKind: EvidenceOwnerDecision, Operator: "a", Evidence: "delivered"}
		mod(&r)
		if _, err := Restore(testLimits(), &fakeJournal{}, append(clone(base), r), t0); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	// A resend that names a replacement that does not exist.
	r := Record{Seq: uint64(len(base) + 1), At: t0, ID: "m1", Target: "lead:a", Sender: "supervisor", Kind: "note", From: StateUncertain, To: StateResolved, Reason: ReasonOperatorResend, EvidenceKind: EvidenceOwnerDecision, Operator: "a", Replacement: "ghost"}
	if _, err := Restore(testLimits(), &fakeJournal{}, append(clone(base), r), t0); err == nil {
		t.Error("accepted a resend with no replacement")
	}
}

func TestRestoreRefusesBadLimitsAndNoJournal(t *testing.T) {
	if _, err := Restore(DefaultLimits(), &fakeJournal{}, nil, t0); err == nil {
		t.Error("no queue time limit")
	}
	if _, err := Restore(testLimits(), nil, nil, t0); err == nil {
		t.Error("no journal")
	}
	m, err := Restore(testLimits(), &fakeJournal{}, nil, t0)
	if err != nil || len(m.View().Targets) != 0 {
		t.Errorf("empty restore: %v", err)
	}
}

func TestRestoreTurnsDispatchingIntoUncertain(t *testing.T) {
	// A death during the fence, before the paste; and after the Enter.
	for _, withEnter := range []bool{false, true} {
		m, j := newTest(t)
		mustSubmit(t, m, "m1", Daemon(), leadA, t0)
		mustSubmit(t, m, "m2", Daemon(), leadA, t0)
		mustBegin(t, m, leadA, at(time.Second))
		if withEnter {
			mustFence(t, m, "m1", WrittenPasteAndEnter, OutcomeSubmitted, at(2*time.Second))
		}
		n := len(j.recs)
		j2 := &fakeJournal{}
		r, err := Restore(testLimits(), j2, j.recs, at(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(j2.recs) != 1 {
			t.Fatalf("restore wrote %d records", len(j2.recs))
		}
		w := j2.recs[0]
		if w.Seq != uint64(n+1) || w.ID != "m1" || w.From != StateDispatching || w.To != StateUncertain || w.Reason != ReasonHolderDied ||
			w.EvidenceKind != EvidenceObservation || !w.At.Equal(at(time.Hour)) || !strings.Contains(w.Detail, "Enter recorded: "+yesNo(withEnter)) {
			t.Errorf("record %+v", w)
		}
		wantState(t, r, "m1", StateUncertain)
		wantState(t, r, "m2", StateQueued)
		// It is never sent again by itself: the queue is frozen for good.
		_, err = r.BeginDispatch(leadA, at(2*time.Hour))
		ref := wantRefusal(t, err, ReasonQueueFrozen)
		if ref.CommandID != "m1" {
			t.Errorf("refusal %+v", ref)
		}
		for s := 0; s < 3600; s += 5 {
			if recs, _ := r.Tick(at(time.Hour + time.Duration(s)*time.Second)); len(recs) != 0 {
				t.Fatalf("tick wrote %+v", recs)
			}
		}
		// Restoring again from what was written changes nothing and writes nothing.
		j3 := &fakeJournal{}
		if _, err := Restore(testLimits(), j3, append(clone(j.recs), j2.recs...), at(3*time.Hour)); err != nil || len(j3.recs) != 0 {
			t.Errorf("second restore: %v, %d new records", err, len(j3.recs))
		}
	}
}

func TestRestoreFailsWhenTheCompletionCannotBeWritten(t *testing.T) {
	m, j := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustBegin(t, m, leadA, t0)
	rj := &fakeJournal{failNext: true}
	r, err := Restore(testLimits(), rj, j.recs, at(time.Hour))
	wantJournalError(t, err)
	if r != nil {
		t.Error("a machine was returned")
	}
}

func TestRestoreQueuesACommandLeftAccepted(t *testing.T) {
	m, j := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	rj := &fakeJournal{}
	r, err := Restore(testLimits(), rj, j.recs[:1], at(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, r, "m1", StateQueued)
	if len(rj.recs) != 1 || rj.recs[0].Seq != 2 || rj.recs[0].To != StateQueued {
		t.Errorf("records %+v", rj.recs)
	}
	mustBegin(t, r, leadA, at(time.Minute))
}

func TestRestoreRebuildsTheRateWindows(t *testing.T) {
	m, j := newTest(t)
	for i := 0; i < 6; i++ {
		mustSubmit(t, m, "m"+string(rune('a'+i)), Lead("p"), SupervisorTarget(), at(time.Duration(i)*10*time.Second))
	}
	// A refusal in the journal does not count.
	m.Submit(msg("m", Lead("p"), SupervisorTarget()), at(55*time.Second))
	r, err := Restore(testLimits(), &fakeJournal{}, j.recs, at(56*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Submit(msg("late", Lead("p"), SupervisorTarget()), at(59*time.Second))
	wantRefusal(t, err, ReasonRate)
	if _, err := r.Submit(msg("late", Lead("p"), SupervisorTarget()), at(60*time.Second)); err != nil {
		t.Errorf("the window did not slide after a restore: %v", err)
	}
	// Another sender has its own window.
	if _, err := r.Submit(msg("other", Daemon(), SupervisorTarget()), at(59*time.Second)); err != nil {
		t.Error(err)
	}
}

func TestRestoreKeepsABlockedSpell(t *testing.T) {
	m, j := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustBegin(t, m, leadA, t0)
	if mustFence(t, m, "m1", WrittenNothing, OutcomeBlocked, t0).Notice == "" {
		t.Fatal("no notice")
	}
	r, err := Restore(testLimits(), &fakeJournal{}, j.recs, at(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	mustBegin(t, r, leadA, at(time.Hour))
	if n := mustFence(t, r, "m1", WrittenNothing, OutcomeBlocked, at(time.Hour)).Notice; n != "" {
		t.Errorf("a restart repeated the notice: %q", n)
	}
}

func TestRestoreAcceptedRefusedByAnAdmissionReason(t *testing.T) {
	m, j := newTest(t)
	m.journal = &failOnSecond{fakeJournal: j}
	m.Submit(msg("m1", Daemon(), leadA), t0)
	rs := append(clone(j.recs), Record{Seq: 2, At: t0, ID: "m1", Target: "lead:a", Sender: "ataniq", Kind: "note", From: StateAccepted, To: StateRefused, Reason: ReasonQueueFull})
	r, err := Restore(testLimits(), &fakeJournal{}, rs, t0)
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, r, "m1", StateRefused)
	if len(r.View().Targets) != 0 {
		t.Error("a refused command is in a queue")
	}
}
