package command

import (
	"strings"
	"testing"
	"time"
)

// uncertainQueue makes lead a's queue hold an uncertain m1 and a queued m2.
func uncertainQueue(t *testing.T) (*Machine, *fakeJournal) {
	t.Helper()
	m, j := newTest(t)
	mustSubmit(t, m, "m1", Supervisor(), leadA, t0)
	mustSubmit(t, m, "m2", Supervisor(), leadA, t0)
	mustBegin(t, m, leadA, at(time.Second))
	mustFence(t, m, "m1", WrittenPaste, OutcomeNotSubmitted, at(2*time.Second))
	return m, j
}

func TestResolveDelivered(t *testing.T) {
	m, j := uncertainQueue(t)
	res, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDelivered, Operator: "alice", Note: "seen in the log"}, at(time.Minute))
	if err != nil || res.State != StateDelivered || res.ID != "m1" {
		t.Fatalf("%v %+v", err, res)
	}
	last := j.recs[len(j.recs)-1]
	if last.From != StateUncertain || last.To != StateDelivered || last.Reason != ReasonOperatorDelivered || last.Operator != "alice" ||
		last.EvidenceKind != EvidenceOwnerDecision || last.Detail != "seen in the log" || last.ID != "m1" {
		t.Errorf("record %+v", last)
	}
	// The queue is unfrozen, and the next message goes.
	if v := m.View().Targets[0]; v.Frozen {
		t.Error("still frozen")
	}
	if d := mustBegin(t, m, leadA, at(2*time.Minute)); d.ID != "m2" {
		t.Errorf("dispatched %s", d.ID)
	}
}

func TestResolveDrop(t *testing.T) {
	m, j := uncertainQueue(t)
	res, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDrop, Operator: "alice"}, at(time.Minute))
	if err != nil || res.State != StateResolved || res.NewID != "" {
		t.Fatalf("%v %+v", err, res)
	}
	last := j.recs[len(j.recs)-1]
	if last.To != StateResolved || last.Reason != ReasonOperatorDrop || last.Replacement != "" || last.EvidenceKind != EvidenceOwnerDecision || last.Evidence != string(DecisionDrop) {
		t.Errorf("record %+v", last)
	}
	if v := m.View().Targets[0]; v.Frozen || len(v.Commands) != 1 {
		t.Errorf("view %+v", v)
	}
	if d := mustBegin(t, m, leadA, at(2*time.Minute)); d.ID != "m2" {
		t.Errorf("dispatched %s", d.ID)
	}
	// Dropped means gone: it is never dispatched, and its text is dropped.
	if c := m.cmds["m1"]; c.text != "" {
		t.Error("the text of a resolved command is kept")
	}
}

func TestResolveResendCreatesANewCommandAtTheHead(t *testing.T) {
	m, j := uncertainQueue(t)
	n := len(j.recs)
	oldText := m.cmds["m1"].text
	res, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionResend, Operator: "alice", NewID: "m1b", Note: "try once more"}, at(time.Minute))
	if err != nil || res.State != StateResolved || res.NewID != "m1b" {
		t.Fatalf("%v %+v", err, res)
	}
	// Two records: the new command, then the resolution of the old one.
	if len(j.recs)-n != 2 {
		t.Fatalf("%d records", len(j.recs)-n)
	}
	nr, or := j.recs[n], j.recs[n+1]
	if nr.ID != "m1b" || nr.From != "" || nr.To != StateQueued || nr.Of != "m1" || nr.Operator != "alice" || nr.Sender != "supervisor" || nr.Target != "lead:a" ||
		nr.Kind != "note" || nr.Text != strings.Replace(oldText, "id=m1 ", "id=m1b ", 1) || nr.EvidenceKind != EvidenceOwnerDecision {
		t.Errorf("new record %+v", nr)
	}
	if or.ID != "m1" || or.From != StateUncertain || or.To != StateResolved || or.Reason != ReasonOperatorResend || or.Replacement != "m1b" || or.Operator != "alice" || or.EvidenceKind != EvidenceOwnerDecision {
		t.Errorf("old record %+v", or)
	}
	// A new identity, in state queued, at the head; the old one ended.
	if got := ids(m, leadA); len(got) != 2 || got[0] != "m1b" || got[1] != "m2" {
		t.Fatalf("queue %v", got)
	}
	newc, _ := m.Command("m1b")
	oldc, _ := m.Command("m1")
	if newc.State != StateQueued || newc.Of != "m1" || oldc.State != StateResolved || oldc.Replacement != "m1b" || newc.ID == oldc.ID {
		t.Errorf("new %+v old %+v", newc, oldc)
	}
	if v := m.View().Targets[0]; v.Frozen {
		t.Error("still frozen")
	}
	d := mustBegin(t, m, leadA, at(2*time.Minute))
	if d.ID != "m1b" || d.Header != "[ataniq-msg id=m1b from=supervisor kind=note]" || !strings.HasSuffix(d.Text, "\nhello m1") {
		t.Errorf("dispatch %+v", d)
	}
	// The new command can be delivered; the id of the old one is not reusable.
	mustFence(t, m, "m1b", WrittenPasteAndEnter, OutcomeSubmitted, at(2*time.Minute))
	if err := m.ConfirmDelivery("m1b", "pos:3", at(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	_, err = m.Submit(msg("m1", Daemon(), leadA), at(3*time.Minute))
	wantRefusal(t, err, ReasonDuplicateID)
}

func TestResolveResendTimesStartAtTheNewCommand(t *testing.T) {
	m, _ := uncertainQueue(t)
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionResend, Operator: "alice", NewID: "m1b"}, at(time.Hour)); err != nil {
		t.Fatal(err)
	}
	c, _ := m.Command("m1b")
	if !c.Admitted.Equal(at(time.Hour)) {
		t.Errorf("admitted %v", c.Admitted)
	}
	// The queue time limit counts from the new command, not from the old one.
	if recs, _ := m.Tick(at(time.Hour + time.Minute)); len(recs) != 0 {
		t.Errorf("tick %+v", recs)
	}
}

func TestResolveRefusals(t *testing.T) {
	m, j := uncertainQueue(t)
	n := len(j.recs)
	before := snap(m)
	cases := []struct {
		name string
		r    Resolution
		want Reason
	}{
		{"unknown command", Resolution{ID: "zz", Decision: DecisionDrop, Operator: "a"}, ReasonNoCommand},
		{"not uncertain", Resolution{ID: "m2", Decision: DecisionDrop, Operator: "a"}, ReasonWrongState},
		{"no operator", Resolution{ID: "m1", Decision: DecisionDrop}, ReasonBadOperator},
		{"blank operator", Resolution{ID: "m1", Decision: DecisionDrop, Operator: "  \t"}, ReasonBadOperator},
		{"operator with a control character", Resolution{ID: "m1", Decision: DecisionDrop, Operator: "a\x1b[0m"}, ReasonBadOperator},
		{"operator with a newline", Resolution{ID: "m1", Decision: DecisionDrop, Operator: "a\nb"}, ReasonBadOperator},
		{"operator too long", Resolution{ID: "m1", Decision: DecisionDrop, Operator: strings.Repeat("a", 129)}, ReasonBadOperator},
		{"no decision", Resolution{ID: "m1", Operator: "a"}, ReasonBadDecision},
		{"unknown decision", Resolution{ID: "m1", Decision: "retry", Operator: "a"}, ReasonBadDecision},
		{"resend without a new id", Resolution{ID: "m1", Decision: DecisionResend, Operator: "a"}, ReasonBadID},
		{"resend with a bad new id", Resolution{ID: "m1", Decision: DecisionResend, Operator: "a", NewID: "M 1"}, ReasonBadID},
		{"resend with the old id", Resolution{ID: "m1", Decision: DecisionResend, Operator: "a", NewID: "m1"}, ReasonDuplicateID},
		{"resend with a used id", Resolution{ID: "m1", Decision: DecisionResend, Operator: "a", NewID: "m2"}, ReasonDuplicateID},
	}
	for _, c := range cases {
		_, err := m.Resolve(c.r, at(time.Minute))
		wantRefusal(t, err, c.want)
	}
	if len(j.recs) != n {
		t.Error("a refused resolution was journaled")
	}
	sameSnap(t, "refused resolutions", before, snap(m))
	wantState(t, m, "m1", StateUncertain)
}

func TestResolveOnlyOnce(t *testing.T) {
	m, _ := uncertainQueue(t)
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDelivered, Operator: "a"}, t0); err != nil {
		t.Fatal(err)
	}
	_, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionDrop, Operator: "a"}, t0)
	wantRefusal(t, err, ReasonWrongState)
}

func TestResendTextMustStillFit(t *testing.T) {
	m, _ := newTest(t)
	body := bodyFor("m1", Daemon(), "note", 4096)
	if _, err := m.Submit(Message{ID: "m1", From: Daemon(), To: leadA, Kind: "note", Body: body}, t0); err != nil {
		t.Fatal(err)
	}
	mustBegin(t, m, leadA, t0)
	mustFence(t, m, "m1", WrittenPaste, OutcomeNotSubmitted, t0)
	_, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionResend, Operator: "a", NewID: "m1-again"}, t0)
	wantRefusal(t, err, ReasonTooLong)
	wantState(t, m, "m1", StateUncertain)
	// A new id of the same length fits.
	if _, err := m.Resolve(Resolution{ID: "m1", Decision: DecisionResend, Operator: "a", NewID: "m2"}, t0); err != nil {
		t.Fatal(err)
	}
}

func TestResendRetryAfterTheSecondAppendFailed(t *testing.T) {
	m, j := uncertainQueue(t)
	m.journal = &failOnNth{fakeJournal: j, n: 2}
	res := Resolution{ID: "m1", Decision: DecisionResend, Operator: "alice", NewID: "m1b"}
	_, err := m.Resolve(res, at(time.Minute))
	wantJournalError(t, err)
	// The new command was journaled and is queued ahead of the uncertain one;
	// the queue is still frozen, so nothing is lost and nothing can go out.
	wantState(t, m, "m1", StateUncertain)
	wantState(t, m, "m1b", StateQueued)
	if v := m.View().Targets[0]; !v.Frozen || v.FrozenBy != "m1" {
		t.Errorf("view %+v", v)
	}
	_, err = m.BeginDispatch(leadA, at(time.Minute))
	wantRefusal(t, err, ReasonQueueFrozen)
	// A restore of that journal is valid and shows the same.
	r, err := Restore(testLimits(), &fakeJournal{}, j.recs, at(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(r, leadA); len(got) != 3 || got[0] != "m1b" {
		t.Errorf("restored queue %v", got)
	}
	// The same call again writes only the resolution.
	n := len(j.recs)
	out, err := m.Resolve(res, at(time.Minute))
	if err != nil || out.NewID != "m1b" || out.State != StateResolved {
		t.Fatalf("%v %+v", err, out)
	}
	if len(j.recs)-n != 1 || j.recs[n].To != StateResolved {
		t.Errorf("records %+v", j.recs[n:])
	}
	if got := ids(m, leadA); len(got) != 2 || got[0] != "m1b" {
		t.Errorf("queue %v", got)
	}
	// While a resend is half done, no other decision and no other new id is
	// accepted, so the message cannot be sent twice.
	m2, j2 := uncertainQueue(t)
	m2.journal = &failOnNth{fakeJournal: j2, n: 2}
	m2.Resolve(res, at(time.Minute))
	for _, other := range []Resolution{
		{ID: "m1", Decision: DecisionResend, Operator: "alice", NewID: "m1c"},
		{ID: "m1", Decision: DecisionDrop, Operator: "alice"},
		{ID: "m1", Decision: DecisionDelivered, Operator: "alice"},
	} {
		_, err := m2.Resolve(other, at(time.Minute))
		wantRefusal(t, err, ReasonWrongState)
	}
	wantState(t, m2, "m1", StateUncertain)
	if _, ok := m2.Command("m1c"); ok {
		t.Error("a second copy was made")
	}
}

// failOnNth fails the nth append only.
type failOnNth struct {
	*fakeJournal
	n, calls int
}

func (f *failOnNth) Append(r Record) error {
	f.calls++
	if f.calls == f.n {
		return errDisk
	}
	return f.fakeJournal.Append(r)
}
