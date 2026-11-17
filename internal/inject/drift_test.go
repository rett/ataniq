package inject

import (
	"testing"

	"ataniq/internal/inject/command"
)

// The spellings of Written and Outcome are those of command.Written and
// command.FenceOutcome: the daemon passes them through as plain strings. This
// test fails when one of them drifts.
func TestSpellingsMatchCommand(t *testing.T) {
	pairs := []struct{ mine, theirs string }{
		{string(WrittenNothing), string(command.WrittenNothing)},
		{string(WrittenPaste), string(command.WrittenPaste)},
		{string(WrittenPasteAndEnter), string(command.WrittenPasteAndEnter)},
		{string(WrittenUnknown), string(command.WrittenUnknown)},
		{string(OutcomeNotIdle), string(command.OutcomeNotIdle)},
		{string(OutcomeBlocked), string(command.OutcomeBlocked)},
		{string(OutcomeUntestedVersion), string(command.OutcomeUntestedVersion)},
		{string(OutcomeRefusedText), string(command.OutcomeRefusedText)},
		{string(OutcomeNotSubmitted), string(command.OutcomeNotSubmitted)},
		{string(OutcomeSubmitted), string(command.OutcomeSubmitted)},
		{string(OutcomeDied), string(command.OutcomeDied)},
	}
	for _, p := range pairs {
		if p.mine != p.theirs {
			t.Errorf("%q here, %q in command", p.mine, p.theirs)
		}
	}
	// The same lists the other way round: no spelling here that command does
	// not know, and none of command's that is not here.
	allWritten := []Written{WrittenNothing, WrittenPaste, WrittenPasteAndEnter, WrittenUnknown}
	allOutcomes := []Outcome{OutcomeNotIdle, OutcomeBlocked, OutcomeUntestedVersion, OutcomeRefusedText, OutcomeNotSubmitted, OutcomeSubmitted, OutcomeDied}
	if len(pairs) != len(allWritten)+len(allOutcomes) {
		t.Errorf("the table lists %d spellings, there are %d", len(pairs), len(allWritten)+len(allOutcomes))
	}
}

// Every pair that Deliver may return is a pair RecordFence takes as it should:
// none of them is recorded as an inconsistent result, and each lands in the
// state the design's table of outcomes says.
func TestEveryPairIsUnderstoodByCommand(t *testing.T) {
	type want struct {
		state  command.State
		reason command.Reason
	}
	expect := map[[2]string]want{
		{"nothing", "not idle"}:          {command.StateQueued, command.ReasonNotIdle},
		{"nothing", "blocked"}:           {command.StateQueued, command.ReasonBlocked},
		{"nothing", "untested version"}:  {command.StateRefused, command.ReasonUntestedVersion},
		{"nothing", "refused text"}:      {command.StateRefused, command.ReasonTextRefused},
		{"nothing", "died"}:              {command.StateUncertain, command.ReasonHolderDied},
		{"paste", "not submitted"}:       {command.StateUncertain, command.ReasonNotSubmitted},
		{"paste and Enter", "submitted"}: {command.StateDispatching, command.ReasonEnterWritten},
		{"unknown", "died"}:              {command.StateUncertain, command.ReasonHolderDied},
	}
	n := 0
	for _, w := range []Written{WrittenNothing, WrittenPaste, WrittenPasteAndEnter, WrittenUnknown} {
		for _, o := range []Outcome{OutcomeNotIdle, OutcomeBlocked, OutcomeUntestedVersion, OutcomeRefusedText, OutcomeNotSubmitted, OutcomeSubmitted, OutcomeDied} {
			r := Result{Written: w, Outcome: o}
			if !validPair(r) {
				continue
			}
			n++
			d := newDaemon(t)
			d.submit("a1", "a body")
			disp := d.begin()
			up, err := d.m.RecordFence(disp.ID, fenceResult(r), d.now)
			if err != nil {
				t.Fatalf("%s / %s: %v", w, o, err)
			}
			if up.Reason == command.ReasonInconsistentResult {
				t.Errorf("%s / %s is recorded as inconsistent", w, o)
			}
			ex, ok := expect[[2]string{string(w), string(o)}]
			if !ok {
				t.Fatalf("no expectation for %s / %s", w, o)
			}
			if up.State != ex.state || up.Reason != ex.reason {
				t.Errorf("%s / %s gives %s (%s), want %s (%s)", w, o, up.State, up.Reason, ex.state, ex.reason)
			}
		}
	}
	if n != len(expect) {
		t.Errorf("%d valid pairs, %d expectations", n, len(expect))
	}
}
