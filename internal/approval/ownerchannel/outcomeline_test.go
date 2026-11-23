package ownerchannel

import (
	"context"
	"strings"
	"testing"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
)

// noApplyIntake is an intake whose Apply moves nothing and says so with its
// own outcome line, as the phone trial's does.
type noApplyIntake struct {
	*fakeIntake
	text string
}

func (n noApplyIntake) Apply(context.Context, ApplyInput) (door.Outcome, error) {
	return door.OutcomeNotApplied, nil
}

func (n noApplyIntake) OutcomeLine(door.Outcome) string { return n.text }

// startWith makes the service on the env's journal with another intake.
func (e *env) startWith(in Intake) {
	e.t.Helper()
	st, err := ownerjournal.Open(e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	cfg := e.config(st)
	cfg.Intake = in
	svc, err := New(context.Background(), cfg)
	if err != nil {
		_ = st.Close()
		e.t.Fatalf("New: %v", err)
	}
	e.svc = svc
	e.t.Cleanup(func() { _ = svc.Close() })
}

// An intake that is an OutcomeLiner gives the line of the outcome: the
// decision's answer, the journal's outcome, the lead's reply and the listing
// carry it, and the state is the one its Apply returned. A line with a
// control character is made one line; an empty one keeps the host's.
func TestAnIntakeThatAppliesNothingGivesTheOutcomeLine(t *testing.T) {
	e := newEnv(t)
	e.startWith(noApplyIntake{fakeIntake: e.intake, text: "applied: nothing\n(trial)"})
	p := e.card("feature-a")
	res, err := e.decide(p, true)
	if err != nil {
		t.Fatal(err)
	}
	const want = "applied: nothing (trial)"
	if res.State != record.StateNotApplied || res.Line != want {
		t.Fatalf("the result is %+v", res)
	}
	if got := e.intake.ref(); got == mergeID {
		t.Errorf("the target moved")
	}
	outs := e.entries(ownerjournal.KindOutcome)
	if len(outs) != 1 || !strings.Contains(string(outs[0].Body), want) || !strings.Contains(string(outs[0].Body), string(record.StateNotApplied)) {
		t.Fatalf("the journal holds %d outcomes", len(outs))
	}
	replies := e.mail.repliesFor("rq-test-01")
	if len(replies) != 1 || replies[0].Line != want || replies[0].State != record.StateNotApplied {
		t.Errorf("the lead was told %+v", replies)
	}
	l, err := e.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Recent) != 1 || l.Recent[0].Line != want || l.Recent[0].State != record.StateNotApplied {
		t.Errorf("the listing shows %+v", l.Recent)
	}

	// An empty line keeps the host's.
	e2 := newEnv(t)
	e2.startWith(noApplyIntake{fakeIntake: e2.intake})
	res, err = e2.decide(e2.card("feature-a"), true)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != record.StateNotApplied || res.Line != lineNotApplied {
		t.Fatalf("with no line of its own the result is %+v", res)
	}
}
