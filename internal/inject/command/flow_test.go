package command

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// rec is the part of a record that the happy path test compares.
type rec struct {
	Seq      uint64
	At       time.Duration
	ID       string
	From, To State
	Reason   Reason
	EvKind   EvidenceKind
	Evidence string
	HasText  bool
}

func summarize(rs []Record) []rec {
	var out []rec
	for _, r := range rs {
		out = append(out, rec{r.Seq, r.At.Sub(t0), r.ID, r.From, r.To, r.Reason, r.EvidenceKind, r.Evidence, r.Text != ""})
	}
	return out
}

func TestHappyPathJournalSequence(t *testing.T) {
	m, j := newTest(t)
	adm := mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	d := mustBegin(t, m, leadA, at(time.Second))
	if d.ID != "m1" || d.Text != adm.Text || d.Header != adm.Header || d.Target != leadA {
		t.Errorf("dispatch %+v", d)
	}
	// The intent is journaled before the text is returned.
	if last := j.recs[len(j.recs)-1]; last.To != StateDispatching {
		t.Errorf("the dispatching record is not journaled when BeginDispatch returns: %+v", last)
	}
	up := mustFence(t, m, "m1", WrittenPasteAndEnter, OutcomeSubmitted, at(2*time.Second))
	if up.State != StateDispatching || up.Reason != ReasonEnterWritten || up.Notice != "" {
		t.Errorf("update %+v", up)
	}
	c, _ := m.Command("m1")
	if !c.Submitted.Equal(at(2 * time.Second)) {
		t.Errorf("submitted time %v", c.Submitted)
	}
	if err := m.ConfirmDelivery("m1", "conversation.jsonl:4711", at(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "m1", StateDelivered)
	if len(m.View().Targets) != 0 {
		t.Error("a delivered command is still in a queue")
	}

	want := []rec{
		{1, 0, "m1", "", StateAccepted, "", "", "", true},
		{2, 0, "m1", StateAccepted, StateQueued, "", "", "", false},
		{3, time.Second, "m1", StateQueued, StateDispatching, "", "", "", false},
		{4, 2 * time.Second, "m1", StateDispatching, StateDispatching, ReasonEnterWritten, EvidenceObservation, "paste and Enter", false},
		{5, 5 * time.Second, "m1", StateDispatching, StateDelivered, ReasonConfirmed, EvidenceProviderReport, "conversation.jsonl:4711", false},
	}
	if got := summarize(j.recs); !reflect.DeepEqual(got, want) {
		t.Errorf("journal:\n got %+v\nwant %+v", got, want)
	}
	for _, r := range j.recs {
		if r.Target != "lead:a" || r.Sender != "ataniq" || r.Kind != "note" {
			t.Errorf("record identity %+v", r)
		}
	}
}

func TestBeginDispatchRefusals(t *testing.T) {
	m, _ := newTest(t)
	_, err := m.BeginDispatch(leadA, t0)
	wantRefusal(t, err, ReasonQueueEmpty)
	_, err = m.BeginDispatch(Target{}, t0)
	wantRefusal(t, err, ReasonQueueEmpty)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustBegin(t, m, leadA, t0)
	_, err = m.BeginDispatch(leadA, t0)
	wantRefusal(t, err, ReasonAlreadyDispatching)
	// Also after Enter, while waiting for the evidence.
	mustFence(t, m, "m1", WrittenPasteAndEnter, OutcomeSubmitted, t0)
	_, err = m.BeginDispatch(leadA, t0)
	wantRefusal(t, err, ReasonAlreadyDispatching)
}

func TestFIFOOrderAndOnlyTheHeadDispatches(t *testing.T) {
	m, _ := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustSubmit(t, m, "m2", Supervisor(), leadA, t0)
	mustSubmit(t, m, "m3", Daemon(), leadB, t0)
	if got := ids(m, leadA); !reflect.DeepEqual(got, []string{"m1", "m2"}) {
		t.Fatalf("queue %v", got)
	}
	// Another target dispatches independently.
	if d := mustBegin(t, m, leadB, t0); d.ID != "m3" {
		t.Errorf("lead b dispatched %s", d.ID)
	}
	d := mustBegin(t, m, leadA, t0)
	if d.ID != "m1" {
		t.Fatalf("dispatched %s first", d.ID)
	}
	// The second is not dispatched while the first is dispatching, nor after it
	// was submitted, nor after it went back to the queue's head.
	_, err := m.BeginDispatch(leadA, t0)
	wantRefusal(t, err, ReasonAlreadyDispatching)
	mustFence(t, m, "m1", WrittenNothing, OutcomeNotIdle, at(time.Second))
	d = mustBegin(t, m, leadA, at(2*time.Second))
	if d.ID != "m1" {
		t.Fatalf("after a requeue the head is %s", d.ID)
	}
	mustFence(t, m, "m1", WrittenPasteAndEnter, OutcomeSubmitted, at(3*time.Second))
	_, err = m.BeginDispatch(leadA, at(4*time.Second))
	wantRefusal(t, err, ReasonAlreadyDispatching)
	if err := m.ConfirmDelivery("m1", "pos:1", at(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	d = mustBegin(t, m, leadA, at(6*time.Second))
	if d.ID != "m2" || !strings.Contains(d.Text, "id=m2 from=supervisor") {
		t.Fatalf("second dispatch %+v", d)
	}
	// A refused head also lets the next one go.
	mustFence(t, m, "m2", WrittenNothing, OutcomeUntestedVersion, at(7*time.Second))
	if len(ids(m, leadA)) != 0 {
		t.Error("queue not empty")
	}
}

func TestBeginDispatchJournalFailureWritesNothing(t *testing.T) {
	m, j := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	before, n := snap(m), len(j.recs)
	j.failNext = true
	d, err := m.BeginDispatch(leadA, t0)
	wantJournalError(t, err)
	if d != (Dispatch{}) {
		t.Errorf("text returned without the journaled intent: %+v", d)
	}
	sameSnap(t, "a failed BeginDispatch", before, snap(m))
	if len(j.recs) != n {
		t.Error("a record was written")
	}
	mustBegin(t, m, leadA, t0)
}

func TestFenceResults(t *testing.T) {
	cases := []struct {
		name       string
		w          Written
		o          FenceOutcome
		after      time.Duration // now, since the admission
		state      State
		reason     Reason
		submitted  bool
		inQueue    bool
		wantNotice bool
	}{
		{"not idle", WrittenNothing, OutcomeNotIdle, time.Minute, StateQueued, ReasonNotIdle, false, true, false},
		{"not idle at the limit", WrittenNothing, OutcomeNotIdle, 10 * time.Minute, StateQueued, ReasonNotIdle, false, true, false},
		{"not idle past the limit", WrittenNothing, OutcomeNotIdle, 10*time.Minute + time.Nanosecond, StateRefused, ReasonNotIdle, false, false, false},
		{"blocked", WrittenNothing, OutcomeBlocked, time.Minute, StateQueued, ReasonBlocked, false, true, true},
		{"blocked past the limit stays queued", WrittenNothing, OutcomeBlocked, time.Hour, StateQueued, ReasonBlocked, false, true, true},
		{"untested version", WrittenNothing, OutcomeUntestedVersion, time.Minute, StateRefused, ReasonUntestedVersion, false, false, false},
		{"refused text", WrittenNothing, OutcomeRefusedText, time.Minute, StateRefused, ReasonTextRefused, false, false, false},
		{"not submitted", WrittenPaste, OutcomeNotSubmitted, time.Minute, StateUncertain, ReasonNotSubmitted, false, true, false},
		{"submitted", WrittenPasteAndEnter, OutcomeSubmitted, time.Minute, StateDispatching, ReasonEnterWritten, true, true, false},
		{"died with unknown writes", WrittenUnknown, OutcomeDied, time.Minute, StateUncertain, ReasonHolderDied, false, true, false},
		{"unknown writes", WrittenUnknown, OutcomeNotIdle, time.Minute, StateUncertain, ReasonHolderDied, false, true, false},
		{"died after nothing", WrittenNothing, OutcomeDied, time.Minute, StateUncertain, ReasonHolderDied, false, true, false},
		{"died after the paste", WrittenPaste, OutcomeDied, time.Minute, StateUncertain, ReasonHolderDied, false, true, false},
		{"died after Enter", WrittenPasteAndEnter, OutcomeDied, time.Minute, StateUncertain, ReasonHolderDied, false, true, false},
		{"nothing written but submitted", WrittenNothing, OutcomeSubmitted, time.Minute, StateUncertain, ReasonInconsistentResult, false, true, false},
		{"paste but not idle", WrittenPaste, OutcomeNotIdle, time.Minute, StateUncertain, ReasonInconsistentResult, false, true, false},
		{"paste and Enter but not submitted", WrittenPasteAndEnter, OutcomeNotSubmitted, time.Minute, StateUncertain, ReasonInconsistentResult, false, true, false},
		{"paste and Enter but blocked", WrittenPasteAndEnter, OutcomeBlocked, time.Minute, StateUncertain, ReasonInconsistentResult, false, true, false},
		{"zero result", "", "", time.Minute, StateUncertain, ReasonInconsistentResult, false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, j := newTest(t)
			mustSubmit(t, m, "m1", Daemon(), leadA, t0)
			mustBegin(t, m, leadA, at(time.Second))
			up, err := m.RecordFence("m1", FenceResult{Written: c.w, Outcome: c.o, Readings: "composer: empty; title: busy"}, at(c.after))
			if err != nil {
				t.Fatal(err)
			}
			if up.State != c.state || up.Reason != c.reason || (up.Notice != "") != c.wantNotice || up.ID != "m1" || up.Target != leadA {
				t.Errorf("update %+v", up)
			}
			if up.Notice != "" && up.Notice != "this lead is waiting for an approval" {
				t.Errorf("notice %q", up.Notice)
			}
			v, _ := m.Command("m1")
			if v.State != c.state || v.Reason != c.reason || v.Detail != "composer: empty; title: busy" || (!v.Submitted.IsZero()) != c.submitted {
				t.Errorf("command %+v", v)
			}
			if got := len(ids(m, leadA)) == 1; got != c.inQueue {
				t.Errorf("in queue = %v", got)
			}
			last := j.recs[len(j.recs)-1]
			if last.From != StateDispatching || last.To != c.state || last.Reason != c.reason || last.EvidenceKind != EvidenceObservation || last.Evidence != string(c.w) || last.Detail != "composer: empty; title: busy" {
				t.Errorf("record %+v", last)
			}
			// The command ends up uncertain or queued, or ended: it is never
			// dispatched again by itself.
			if _, err := m.Tick(at(c.after)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFenceResultRefusals(t *testing.T) {
	m, _ := newTest(t)
	_, err := m.RecordFence("nope", FenceResult{}, t0)
	wantRefusal(t, err, ReasonNoCommand)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	_, err = m.RecordFence("m1", FenceResult{Written: WrittenNothing, Outcome: OutcomeNotIdle}, t0)
	wantRefusal(t, err, ReasonWrongState)
	submitted(t, m, leadA, t0)
	_, err = m.RecordFence("m1", FenceResult{Written: WrittenNothing, Outcome: OutcomeNotIdle}, t0)
	wantRefusal(t, err, ReasonWrongState)
}

func TestNotIdleKeepsTheReadingsAsTheLastReason(t *testing.T) {
	m, _ := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustBegin(t, m, leadA, t0)
	if _, err := m.RecordFence("m1", FenceResult{Written: WrittenNothing, Outcome: OutcomeNotIdle, Readings: "state working; title spinner"}, t0); err != nil {
		t.Fatal(err)
	}
	// While it is dispatching again the last reason is still there.
	mustBegin(t, m, leadA, t0)
	c, _ := m.Command("m1")
	if c.State != StateDispatching || c.Reason != ReasonNotIdle || c.Detail != "state working; title spinner" {
		t.Errorf("command %+v", c)
	}
	// And a refusal at the limit by tick carries it.
	mustFence(t, m, "m1", WrittenNothing, OutcomeNotIdle, t0)
	recs, err := m.Tick(at(11 * time.Minute))
	if err != nil || len(recs) != 1 || recs[0].Reason != ReasonNotIdle || !strings.Contains(recs[0].Detail, "readings of m1") {
		t.Errorf("tick: %v %+v", err, recs)
	}
}

func TestBlockedNoticeOncePerSpell(t *testing.T) {
	m, _ := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustSubmit(t, m, "n1", Daemon(), leadB, t0)
	notice := func(id string, target Target, o FenceOutcome, now time.Time) string {
		mustBegin(t, m, target, now)
		return mustFence(t, m, id, WrittenNothing, o, now).Notice
	}
	if notice("m1", leadA, OutcomeBlocked, at(1*time.Second)) == "" {
		t.Error("no notice on the first blocked result")
	}
	if !m.View().Targets[0].BlockedNotified {
		t.Error("view does not show the blocked spell")
	}
	if n := notice("m1", leadA, OutcomeBlocked, at(2*time.Second)); n != "" {
		t.Errorf("second blocked result in the same spell has notice %q", n)
	}
	// Another lead has its own spell.
	if notice("n1", leadB, OutcomeBlocked, at(3*time.Second)) == "" {
		t.Error("no notice for the other lead")
	}
	if n := notice("m1", leadA, OutcomeBlocked, at(4*time.Second)); n != "" {
		t.Errorf("notice %q", n)
	}
	// The next result that is not blocked ends the spell.
	if n := notice("m1", leadA, OutcomeNotIdle, at(5*time.Second)); n != "" {
		t.Errorf("notice %q", n)
	}
	if m.View().Targets[0].BlockedNotified {
		t.Error("the spell is still shown after a result that is not blocked")
	}
	if notice("m1", leadA, OutcomeBlocked, at(6*time.Second)) == "" {
		t.Error("no notice at the start of the next spell")
	}
	wantState(t, m, "m1", StateQueued)
}

func TestConfirmDelivery(t *testing.T) {
	m, j := newTest(t)
	wantRefusal(t, m.ConfirmDelivery("nope", "x", t0), ReasonNoCommand)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	wantRefusal(t, m.ConfirmDelivery("m1", "x", t0), ReasonWrongState)
	mustBegin(t, m, leadA, t0)
	// Dispatching, but Enter was not written.
	wantRefusal(t, m.ConfirmDelivery("m1", "x", t0), ReasonNoEnter)
	mustFence(t, m, "m1", WrittenPasteAndEnter, OutcomeSubmitted, t0)
	n := len(j.recs)
	for _, bad := range []string{"", "   ", "\n\t", strings.Repeat("e", 513)} {
		wantRefusal(t, m.ConfirmDelivery("m1", bad, t0), ReasonBadEvidence)
	}
	if len(j.recs) != n {
		t.Error("a refused confirmation was journaled")
	}
	if err := m.ConfirmDelivery("m1", strings.Repeat("e", 512), at(time.Second)); err != nil {
		t.Fatal(err)
	}
	last := j.recs[len(j.recs)-1]
	if last.EvidenceKind != EvidenceProviderReport || last.Reason != ReasonConfirmed || last.To != StateDelivered {
		t.Errorf("record %+v", last)
	}
	// A delivered command is not confirmed twice.
	wantRefusal(t, m.ConfirmDelivery("m1", "again", t0), ReasonWrongState)
}

func TestTickConfirmationTimeout(t *testing.T) {
	m, _ := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustSubmit(t, m, "m2", Daemon(), leadA, t0)
	mustBegin(t, m, leadA, at(time.Second))
	mustFence(t, m, "m1", WrittenPasteAndEnter, OutcomeSubmitted, at(2*time.Second))
	recs, err := m.Tick(at(2*time.Second + 30*time.Second - time.Nanosecond))
	if err != nil || len(recs) != 0 {
		t.Fatalf("tick before the limit: %v %+v", err, recs)
	}
	wantState(t, m, "m1", StateDispatching)
	recs, err = m.Tick(at(32 * time.Second))
	if err != nil || len(recs) != 1 {
		t.Fatalf("tick at the limit: %v %+v", err, recs)
	}
	if r := recs[0]; r.Reason != ReasonSubmittedNotConfirmed || r.To != StateUncertain || r.Seq != m.seq || r.ID != "m1" {
		t.Errorf("record %+v", r)
	}
	wantState(t, m, "m1", StateUncertain)
	// The queue is frozen, and the second command is behind it.
	_, err = m.BeginDispatch(leadA, at(33*time.Second))
	ref := wantRefusal(t, err, ReasonQueueFrozen)
	if ref.CommandID != "m1" || !strings.Contains(ref.Detail, `"m1"`) {
		t.Errorf("the refusal does not name the uncertain command: %+v", ref)
	}
	v := m.View().Targets[0]
	if !v.Frozen || v.FrozenBy != "m1" || len(v.Commands) != 2 || v.Commands[1].ID != "m2" || v.Commands[1].State != StateQueued {
		t.Errorf("view %+v", v)
	}
	// Late evidence is an annotation: it does not move it.
	if err := m.ConfirmDelivery("m1", "pos:9", at(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantState(t, m, "m1", StateUncertain)
}

func TestNotSubmittedFreezesTheQueue(t *testing.T) {
	m, _ := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustSubmit(t, m, "m2", Daemon(), leadA, t0)
	mustBegin(t, m, leadA, t0)
	mustFence(t, m, "m1", WrittenPaste, OutcomeNotSubmitted, t0)
	_, err := m.BeginDispatch(leadA, t0)
	wantRefusal(t, err, ReasonQueueFrozen)
	// Another lead is not frozen.
	mustSubmit(t, m, "n1", Daemon(), leadB, t0)
	mustBegin(t, m, leadB, t0)
}

func TestTickRefusesAQueuedHeadPastTheLimit(t *testing.T) {
	m, j := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustSubmit(t, m, "m2", Daemon(), leadA, at(time.Minute))
	// At the limit, not past it.
	recs, err := m.Tick(at(10 * time.Minute))
	if err != nil || len(recs) != 0 {
		t.Fatalf("tick at the limit: %v %+v", err, recs)
	}
	recs, err = m.Tick(at(10*time.Minute + time.Nanosecond))
	if err != nil || len(recs) != 1 || recs[0].ID != "m1" || recs[0].Reason != ReasonNotIdle || recs[0].To != StateRefused {
		t.Fatalf("tick past the limit: %v %+v", err, recs)
	}
	wantState(t, m, "m1", StateRefused)
	wantState(t, m, "m2", StateQueued)
	// m2 is not behind the limit yet, and a tick never dispatches.
	n := len(j.recs)
	recs, _ = m.Tick(at(11 * time.Minute))
	if len(recs) != 0 || len(j.recs) != n {
		t.Errorf("tick wrote %+v", recs)
	}
	recs, _ = m.Tick(at(11*time.Minute + time.Second))
	if len(recs) != 1 || recs[0].ID != "m2" {
		t.Errorf("tick wrote %+v", recs)
	}
	for _, r := range j.recs {
		if r.To == StateDispatching {
			t.Errorf("a tick dispatched: %+v", r)
		}
	}
}

func TestTickDoesNotRefuseAHeadThatIsDispatching(t *testing.T) {
	m, _ := newTest(t)
	mustSubmit(t, m, "m1", Daemon(), leadA, t0)
	mustBegin(t, m, leadA, t0)
	// Within the fence timeout a tick leaves the dispatching head alone.
	if recs, _ := m.Tick(at(29 * time.Second)); len(recs) != 0 {
		t.Errorf("tick moved a dispatching command without Enter: %+v", recs)
	}
	// Past it the command is uncertain, never refused: the queue time limit
	// does not apply to a command that may have been written.
	recs, _ := m.Tick(at(time.Hour))
	if len(recs) != 1 || recs[0].To != StateUncertain || recs[0].Reason != ReasonNoFenceResult {
		t.Errorf("tick wrote %+v", recs)
	}
}

func TestNoAutomaticRetryOfAnUncertainCommand(t *testing.T) {
	for _, how := range []string{"not submitted", "submitted, not confirmed", "died"} {
		t.Run(how, func(t *testing.T) {
			m, j := newTest(t)
			mustSubmit(t, m, "m1", Daemon(), leadA, t0)
			mustSubmit(t, m, "m2", Daemon(), leadA, t0)
			mustBegin(t, m, leadA, t0)
			switch how {
			case "not submitted":
				mustFence(t, m, "m1", WrittenPaste, OutcomeNotSubmitted, t0)
			case "died":
				mustFence(t, m, "m1", WrittenUnknown, OutcomeDied, t0)
			default:
				mustFence(t, m, "m1", WrittenPasteAndEnter, OutcomeSubmitted, t0)
				if _, err := m.Tick(at(31 * time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			wantState(t, m, "m1", StateUncertain)
			n, before := len(j.recs), snap(m)
			// An hour of fake time, ticking every second: nothing moves, nothing is
			// written, and the queue never dispatches.
			for s := 32; s <= 3600+32; s++ {
				now := at(time.Duration(s) * time.Second)
				recs, err := m.Tick(now)
				if err != nil || len(recs) != 0 {
					t.Fatalf("tick at %ds: %v %+v", s, err, recs)
				}
				if s%600 == 0 {
					_, err := m.BeginDispatch(leadA, now)
					wantRefusal(t, err, ReasonQueueFrozen)
				}
			}
			if len(j.recs) != n {
				t.Errorf("the journal grew by %d records", len(j.recs)-n)
			}
			sameSnap(t, "an hour of ticks", before, snap(m))
			// No method moves it but Resolve. A fence result and evidence that
			// arrive are annotations: they are journaled and leave it uncertain.
			now := at(2 * time.Hour)
			if _, err := m.BeginDispatch(leadA, now); err == nil {
				t.Error("BeginDispatch dispatched past an uncertain command")
			}
			if _, err := m.RecordFence("m1", FenceResult{Written: WrittenPasteAndEnter, Outcome: OutcomeSubmitted}, now); err != nil {
				t.Errorf("a late fence result: %v", err)
			}
			if err := m.ConfirmDelivery("m1", "pos:1", now); err != nil {
				t.Errorf("late evidence: %v", err)
			}
			if _, err := m.BeginDispatch(leadA, now); err == nil {
				t.Error("BeginDispatch dispatched past an uncertain command after late evidence")
			}
			if _, err := m.Submit(msg("m3", Daemon(), leadA), now); err != nil {
				t.Errorf("a new message is queued behind it: %v", err)
			}
			wantState(t, m, "m1", StateUncertain)
			wantState(t, m, "m2", StateQueued)
			for _, r := range j.recs[n:] {
				if r.ID == "m1" && !(r.From == StateUncertain && r.To == StateUncertain && in(r.Reason, ReasonLateFenceResult, ReasonLateEvidence)) {
					t.Errorf("record for the uncertain command that is not an annotation: %+v", r)
				}
			}
		})
	}
}
