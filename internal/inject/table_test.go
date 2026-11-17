package inject

import (
	"strings"
	"testing"
	"time"
)

// tableEvent is one scripted event of the table.
type tableEvent struct {
	name string
	do   func(*fake)
	// opensDialog is true when the event changes the agent itself to a
	// dialog. When it fires after the last reading of the fence and the
	// output is queued to the holder yet (at the Enter), the fence cannot see
	// it: that is the window of design 4.6, and an approval is allowed there,
	// and only there. Fired earlier, the output is in the PTY and Pending sees
	// it.
	opensDialog bool
	// skip lists the steps where the event cannot be told from the write
	// itself: a clock that jumps inside the write call is after the
	// deadline check by construction.
	skip []step
}

var tableEvents = []tableEvent{
	{name: "dialog opens", do: evOpenDialog, opensDialog: true},
	{name: "agent starts working", do: evStartWorking},
	{name: "session dies", do: evDie},
	{name: "session ends", do: evEnded},
	{name: "a write fails", do: evFailWrite},
	{name: "writer types", do: evType("typed during the fence")},
	{name: "stale report", do: evStaleReport},
	{name: "blocked report", do: evBlockedReport},
	{name: "parser leaves ground", do: evTerm(func(s *TermState) { s.ParserGround = false })},
	{name: "2026 frame opens", do: evTerm(func(s *TermState) { s.FrameOpen = true })},
	{name: "bracketed paste off", do: evTerm(func(s *TermState) { s.BracketedPaste = false })},
	{name: "title says Action Required", do: evTitle("[ ! ] Action Required | work")},
	{name: "screen becomes unknown", do: evUnknownScreen},
	{name: "cursor leaves the composer", do: evCursorAway},
	{name: "composer shows other text", do: evReplaceDraft},
	{name: "screen never goes quiet", do: evNoise(100, 30*time.Millisecond)},
	{name: "clock passes the deadline", do: evClockJump, skip: []step{stepPaste, stepEnter}},
	{name: "context is cancelled", do: evCancel},
}

// TestEveryEventAtEveryStep runs every scripted event at every step, for both
// runtimes and for a short and a long message, and checks the invariants of
// every scenario: the writes are a prefix of [paste, Enter]; Enter only after a
// passed second check; no approval is ever recorded (outside the window of
// design 4.6); Written matches the write log; the writer's keys come after the
// fence; nothing is written after the deadline.
func TestEveryEventAtEveryStep(t *testing.T) {
	msgs := map[string]Message{
		"short message": testMsg,
		"long message":  message("long9", strings.Repeat("a line of a long body of text\n", 30)),
	}
	for name, m := range msgs {
		for _, rt := range runtimes {
			for _, ev := range tableEvents {
				for _, at := range allSteps {
					skipped := false
					for _, s := range ev.skip {
						skipped = skipped || s == at
					}
					if skipped {
						continue
					}
					t.Run(rt+"/"+name+"/"+ev.name+" at "+string(at), func(t *testing.T) {
						f := newFake(t, rt).at(at, ev.do)
						res := runFence(t, f, m)
						residual := ev.opensDialog && at == stepEnter
						checkInvariants(t, f, m, res, invariants{deadline: f.deadline, residual: residual})
						if t.Failed() {
							t.Logf("result %q / %q: %s\n%s", res.Written, res.Outcome, res.Detail, res.Readings())
						}
					})
				}
			}
		}
	}
}

// Every scenario is also a fence that never retries: it makes at most one
// attempt, one paste and one Enter, whatever the session does.
func TestNoRetryWhateverHappens(t *testing.T) {
	for _, rt := range runtimes {
		for _, ev := range tableEvents {
			f := newFake(t, rt).at(stepFrozen1, ev.do).at(stepSettle2, ev.do).at(stepFrozen2, ev.do)
			res := runFence(t, f, testMsg)
			if len(f.writes) > 2 || f.settles > 2 || f.frozens > 2 || f.lockCalls != 1 {
				t.Errorf("%s/%s: writes %d settles %d frozens %d locks %d (%s)", rt, ev.name, len(f.writes), f.settles, f.frozens, f.lockCalls, res.Outcome)
			}
		}
	}
}
