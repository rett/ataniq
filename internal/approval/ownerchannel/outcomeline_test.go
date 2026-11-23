package ownerchannel

import (
	"context"
	"errors"
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

// unknownIntake is a no-apply intake whose Apply fails, so that the outcome
// is unknown.
type unknownIntake struct{ noApplyIntake }

func (u unknownIntake) Apply(context.Context, ApplyInput) (door.Outcome, error) {
	return door.OutcomeUnknown, errors.New("update-ref was killed")
}

// restartWith stops the service and starts a new one on the same journal with
// another intake.
func (e *env) restartWith(in Intake) {
	e.t.Helper()
	e.stop()
	e.startWith(in)
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

// An unknown outcome keeps the host's line whatever the intake says: the
// liner speaks for an intake that moved nothing, and an outcome nobody knows
// is not one it can speak for. The decision's answer, the journal's outcome
// and the listing all carry the host's line, and a restart that finds the
// outcome unknown again from the mirror writes the same.
func TestAnUnknownOutcomeKeepsTheHostsLineUnderAnOutcomeLiner(t *testing.T) {
	const trial = "applied: nothing (trial)"
	e := newEnv(t)
	e.startWith(unknownIntake{noApplyIntake{fakeIntake: e.intake, text: trial}})
	p := e.card("feature-a")
	res, err := e.decide(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != record.StateUnknown || res.Line != lineUnknown {
		t.Fatalf("the result is %+v, want unknown with the host's line", res)
	}
	outs := e.entries(ownerjournal.KindOutcome)
	if len(outs) != 1 || !strings.Contains(string(outs[0].Body), lineUnknown) || strings.Contains(string(outs[0].Body), "nothing (trial)") {
		t.Fatalf("the journal holds %d outcomes: %v", len(outs), outs)
	}
	l, err := e.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Recent) != 1 || l.Recent[0].State != record.StateUnknown || l.Recent[0].Line != lineUnknown {
		t.Errorf("the listing shows %+v", l.Recent)
	}
	if got := e.mail.all(); len(got) != 0 {
		t.Errorf("the lead was told of an outcome that is not final: %v", got)
	}

	// Only the owner's word closes it, and that line is the intake's again:
	// the outcome is then a known one.
	e.beat()
	res, err = e.svc.Dispose(context.Background(), src, p.ID, record.StateNotApplied)
	if err != nil || res.State != record.StateNotApplied || res.Line != trial {
		t.Errorf("the owner's word gave %+v, %v", res, err)
	}
}

// A use left open and found unknown at a restart keeps the host's line under
// a liner, as the first answer does; and a card that ended at the door but not
// in the journal, closed out by the restart's read of the door, is told with
// the liner's line for a known outcome.
func TestARestartKeepsTheHostsLineForUnknownAndTheLinersForKnown(t *testing.T) {
	const trial = "applied: nothing (trial)"
	t.Run("a use left open, the mirror unreadable", func(t *testing.T) {
		e := newEnv(t)
		liner := noApplyIntake{fakeIntake: e.intake, text: trial}
		e.startWith(liner)
		p := e.card("feature-a")
		e.fault = "door-used"
		if _, err := e.decide(p, true); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
		e.intake.mu.Lock()
		e.intake.mirrorErr = errors.New("no mirror")
		e.intake.mu.Unlock()
		e.restartWith(liner)
		if got := e.state(p.ID); got != record.StateUnknown {
			t.Fatalf("the door holds %s, want unknown", got)
		}
		outs := e.entries(ownerjournal.KindOutcome)
		if len(outs) != 1 || !strings.Contains(string(outs[0].Body), lineUnknown) || strings.Contains(string(outs[0].Body), "nothing (trial)") {
			t.Fatalf("the journal holds %d outcomes: %v", len(outs), outs)
		}
		l, err := e.svc.List(context.Background())
		if err != nil || len(l.Recent) != 1 || l.Recent[0].Line != lineUnknown {
			t.Errorf("the listing shows %+v, %v", l.Recent, err)
		}
	})
	t.Run("a card closed out from the door's list", func(t *testing.T) {
		e := newEnv(t)
		liner := noApplyIntake{fakeIntake: e.intake, text: trial}
		e.startWith(liner)
		p := e.card("feature-a")
		// The outcome is settled at the door and journaled, and the service
		// stops before it answers the lead: only the door's list says the card
		// ended.
		e.fault = "outcome-journaled"
		if _, err := e.decide(p, true); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
		if got := e.mail.repliesFor("rq-test-01"); len(got) != 0 {
			t.Fatalf("the lead was told before the stop: %+v", got)
		}
		e.restartWith(liner)
		if err := e.svc.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		replies := e.mail.repliesFor("rq-test-01")
		if len(replies) != 1 || replies[0].State != record.StateNotApplied || replies[0].Line != trial {
			t.Errorf("the lead was told %+v, want not-applied with the liner's line", replies)
		}
	})
}
