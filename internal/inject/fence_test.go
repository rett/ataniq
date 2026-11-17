package inject

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ataniq/internal/inject/idle"
	"ataniq/internal/inject/osc7501"
)

var testMsg = message("m1", "first line of the body\nsecond line of the body")

func wantResult(t *testing.T, res Result, w Written, o Outcome) {
	t.Helper()
	if res.Written != w || res.Outcome != o {
		t.Fatalf("result is %q / %q (%s), want %q / %q\n%s", res.Written, res.Outcome, res.Detail, w, o, res.Readings())
	}
}

func hasFailed(r *Reading, why string) bool {
	if r == nil {
		return false
	}
	for _, f := range r.Failed {
		if strings.Contains(f, why) {
			return true
		}
	}
	return false
}

// I-1: an idle composer gets the paste, then Enter, once.
func TestIdleComposerSubmits(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt)
		res := deliver(t, f, testMsg)
		wantResult(t, res, WrittenPasteAndEnter, OutcomeSubmitted)
		if res.State != idle.StateIdle {
			t.Errorf("state %q", res.State)
		}
		if len(f.writes) != 2 || f.writes[0].data != pasteBytes(testMsg) || f.writes[1].data != "\r" {
			t.Fatalf("writes %+v", f.writes)
		}
		if len(f.submissions) != 1 || f.submissions[0] != testMsg.Text {
			t.Errorf("submissions %q, want one with the text byte-exact", f.submissions)
		}
		if f.approvals != 0 {
			t.Errorf("approvals %d", f.approvals)
		}
		if !res.Before.Passed() || !res.After.Passed() {
			t.Errorf("a check did not pass: before %v after %v", res.Before.Failed, res.After.Failed)
		}
		if !res.Before.Screen.ComposerEmpty || !res.After.Screen.HeaderHeld || res.After.Screen.ComposerEmpty {
			t.Errorf("readings %+v / %+v", res.Before.Screen, res.After.Screen)
		}
		if res.Before.OwnerQuiet < 2*time.Second || res.Before.ScreenQuiet < 300*time.Millisecond {
			t.Errorf("quiet times %s %s", res.Before.OwnerQuiet, res.Before.ScreenQuiet)
		}
		if f.settles != 2 || f.frozens != 2 || f.lockCalls != 1 {
			t.Errorf("settles %d frozens %d locks %d", f.settles, f.frozens, f.lockCalls)
		}
		want := [][3]time.Duration{{0, 50 * time.Millisecond, time.Second}, {150 * time.Millisecond, 50 * time.Millisecond, time.Second}}
		if f.settleArg[0] != want[0] || f.settleArg[1] != want[1] {
			t.Errorf("settle arguments %v, want %v", f.settleArg, want)
		}
		if !strings.Contains(res.Readings(), "submitted") || strings.Contains(res.Readings(), "delivered") {
			t.Errorf("readings %q", res.Readings())
		}
	})
}

// The paste is one write of ESC [ 200 ~, the text with \n as \r, ESC [ 201 ~.
func TestPasteBytes(t *testing.T) {
	m := message("m2", "a\nb")
	want := "\x1b[200~" + "[ataniq-msg id=m2 from=ataniq kind=note]\ra\rb" + "\x1b[201~"
	if got := pasteBytes(m); got != want {
		t.Fatalf("pasteBytes %q, want %q", got, want)
	}
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt)
		wantResult(t, deliver(t, f, m), WrittenPasteAndEnter, OutcomeSubmitted)
		if f.writes[0].data != want {
			t.Errorf("paste %q", f.writes[0].data)
		}
	})
}

// I-2: a working agent, and a claude with only a background shell, get
// nothing.
func TestWorkingAndBackgroundGetNothing(t *testing.T) {
	cases := []struct {
		name string
		rt   string
		mode mode
	}{
		{"claude working", "claude", mWorking},
		{"codex working", "codex", mWorking},
		{"claude background shell only", "claude", mBackground},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, c.rt).setMode(c.mode)
			res := deliver(t, f, testMsg)
			wantResult(t, res, WrittenNothing, OutcomeNotIdle)
			if res.State != idle.StateWorking {
				t.Errorf("state %q, want working", res.State)
			}
			if len(f.writes) != 0 || res.After != nil || res.Before == nil {
				t.Errorf("writes %d, after %v", len(f.writes), res.After)
			}
			if !hasFailed(res.Before, "state working") {
				t.Errorf("failed %q", res.Before.Failed)
			}
		})
	}
}

// I-3: an open dialog, and the Q13 veto: claude's screen looks idle and the
// report says blocked.
func TestBlockedGetsNothing(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		t.Run("dialog open", func(t *testing.T) {
			f := newFake(t, rt).setMode(mDialog)
			res := deliver(t, f, testMsg)
			wantResult(t, res, WrittenNothing, OutcomeBlocked)
			if res.State != idle.StateBlocked || !res.Before.Screen.Dialog || len(res.Before.Screen.DialogMarks) == 0 {
				t.Errorf("reading %+v", res.Before)
			}
			if f.approvals != 0 || len(f.dialogKeys) != 0 {
				t.Errorf("the dialog was touched")
			}
		})
		t.Run("idle screen, blocked report", func(t *testing.T) {
			f := newFake(t, rt).at(stepStart, evBlockedReport)
			res := deliver(t, f, testMsg)
			wantResult(t, res, WrittenNothing, OutcomeBlocked)
			if res.Before.Screen.Dialog || !res.Before.Screen.ComposerEmpty || res.Before.Report.State != osc7501.StateBlocked {
				t.Errorf("reading %+v", res.Before)
			}
		})
	})
}

// I-4: a dialog opens after the first check, before the paste is shown, or
// after it. The paste is written, Enter is not, nothing is approved.
func TestDialogOpensAfterFirstCheck(t *testing.T) {
	cases := []struct {
		name string
		at   step
	}{
		{"before the paste lands, swallowed", stepFrozen1},
		{"as the paste lands, swallowed", stepPaste},
		{"after the paste landed", stepSettle2},
	}
	each(t, func(t *testing.T, rt string) {
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				f := newFake(t, rt).at(c.at, evOpenDialog)
				res := deliver(t, f, testMsg)
				wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
				if res.State != idle.StateBlocked || !res.After.Screen.Dialog {
					t.Errorf("state %q, after %+v", res.State, res.After)
				}
				if len(f.writes) != 1 || f.approvals != 0 || len(f.submissions) != 0 {
					t.Errorf("writes %d approvals %d submissions %d", len(f.writes), f.approvals, len(f.submissions))
				}
				if c.at != stepSettle2 && f.swallowed != 1 {
					t.Errorf("the dialog should have swallowed the paste, swallowed %d", f.swallowed)
				}
			})
		}
	})
}

// I-5: the owner is typing.
func TestOwnerTyping(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		t.Run("a draft typed a second ago", func(t *testing.T) {
			f := newFake(t, rt).withDraft("half a sentence", time.Second)
			res := deliver(t, f, testMsg)
			wantResult(t, res, WrittenNothing, OutcomeNotIdle)
			if !hasFailed(res.Before, "owner typed") || !hasFailed(res.Before, "composer not empty") {
				t.Errorf("failed %q", res.Before.Failed)
			}
			if res.State != idle.StateIdle {
				t.Errorf("state %q: the draft is not a state", res.State)
			}
		})
		t.Run("an empty composer, typed a second ago", func(t *testing.T) {
			f := newFake(t, rt)
			f.ownerLast = f.clock.Add(-time.Second)
			res := deliver(t, f, testMsg)
			wantResult(t, res, WrittenNothing, OutcomeNotIdle)
			if !hasFailed(res.Before, "owner typed") || hasFailed(res.Before, "composer not empty") {
				t.Errorf("failed %q", res.Before.Failed)
			}
		})
		t.Run("a draft left alone", func(t *testing.T) {
			f := newFake(t, rt).withDraft("a draft nobody finished", time.Hour)
			res := deliver(t, f, testMsg)
			wantResult(t, res, WrittenNothing, OutcomeNotIdle)
			if hasFailed(res.Before, "owner typed") || !hasFailed(res.Before, "composer not empty") {
				t.Errorf("failed %q", res.Before.Failed)
			}
		})
		t.Run("the screen changed 100 ms ago", func(t *testing.T) {
			f := newFake(t, rt).at(stepStart, func(f *fake) {
				f.push(-100*time.Millisecond, func(e *emuState) {})
			})
			res := deliver(t, f, testMsg)
			// The settle waits for the quiet period, which is shorter than
			// the 300 ms the check needs.
			wantResult(t, res, WrittenNothing, OutcomeNotIdle)
			if !hasFailed(res.Before, "screen changed") {
				t.Errorf("failed %q", res.Before.Failed)
			}
		})
	})
}

// I-6: the writer types during the fence. The keys are held, then delivered in
// order after the fence, and none is inside the paste.
func TestWriterTypesDuringFence(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt).
			at(stepSettle2, evType("abc")).
			at(stepFrozen2, evType("def"))
		res := deliver(t, f, testMsg)
		wantResult(t, res, WrittenPasteAndEnter, OutcomeSubmitted)
		want := []string{"paste:" + strings.ReplaceAll(testMsg.Text, "\n", "\r"), "enter", "keys:abc", "keys:def"}
		if strings.Join(f.agentLog, "|") != strings.Join(want, "|") {
			t.Fatalf("what reached the agent: %q, want %q", f.agentLog, want)
		}
		if len(f.held) != 0 {
			t.Errorf("held keys were not released")
		}
	})
	// Typed before the first check: held, and the fence does not inject.
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt).at(stepStart, evType("x"))
		res := deliver(t, f, testMsg)
		wantResult(t, res, WrittenNothing, OutcomeNotIdle)
		if len(f.agentLog) != 1 || f.agentLog[0] != "keys:x" {
			t.Errorf("what reached the agent: %q", f.agentLog)
		}
		if !hasFailed(res.Before, "owner typed") {
			t.Errorf("failed %q", res.Before.Failed)
		}
	})
}

// I-9: a version that is not in the tested table; the session is not touched.
func TestUntestedVersion(t *testing.T) {
	cases := []struct{ rt, ver string }{
		{"claude", "2.1.326"}, {"claude", "2.1"}, {"claude", ""}, {"codex", "0.170.0"}, {"codex", "0.16"},
		{"gemini", "2.1.325"}, {"", ""}, {"claude\x1b[2J", "2.1.325"},
	}
	for _, c := range cases {
		t.Run(c.rt+" "+c.ver, func(t *testing.T) {
			f := newFake(t, "claude")
			f.rt, f.version = c.rt, c.ver
			res := Deliver(context.Background(), f, testMsg, Options{ExpectedMode: "manual"})
			wantResult(t, res, WrittenNothing, OutcomeUntestedVersion)
			if f.touched() {
				t.Errorf("the session was touched: lock %d settle %d frozen %d snapshot %d", f.lockCalls, f.settles, f.frozens, f.snapshots)
			}
			if res.Before != nil || res.After != nil || res.State != "" {
				t.Errorf("a check was reported")
			}
			if strings.ContainsAny(res.Detail, "\x1b") || len(res.Detail) > 300 {
				t.Errorf("detail %q", res.Detail)
			}
		})
	}
	each(t, func(t *testing.T, rt string) {
		for _, v := range []string{"0.169", "0.169.0", "2.1.325"} {
			if (rt == "claude") != (v == "2.1.325") {
				continue
			}
			f := newFake(t, rt)
			f.version = v
			wantResult(t, deliver(t, f, testMsg), WrittenPasteAndEnter, OutcomeSubmitted)
		}
	})
}

// I-10: the text rule at the fence. The session is not touched.
func TestTextRules(t *testing.T) {
	h := "[ataniq-msg id=m1 from=ataniq kind=note]"
	big := h + "\n" + strings.Repeat("x", MaxTextBytes)
	cases := []struct {
		name string
		m    Message
	}{
		{"escape", Message{Text: h + "\nbody \x1b[31m red", Header: h}},
		{"paste end sequence", Message{Text: h + "\nbody \x1b[201~ more", Header: h}},
		{"carriage return", Message{Text: h + "\nbody\r more", Header: h}},
		{"tab", Message{Text: h + "\nbody\tmore", Header: h}},
		{"NUL", Message{Text: h + "\nbody\x00more", Header: h}},
		{"DEL", Message{Text: h + "\nbody\x7fmore", Header: h}},
		{"line separator", Message{Text: h + "\nbody more", Header: h}},
		{"no-break space", Message{Text: h + "\nbody more", Header: h}},
		{"bidi control", Message{Text: h + "\nbody‮more", Header: h}},
		{"invalid UTF-8", Message{Text: h + "\nbody \xff\xfe", Header: h}},
		{"empty text", Message{}},
		{"over 4096 bytes", Message{Text: big, Header: h}},
		{"empty header", Message{Text: "\nbody", Header: ""}},
		{"blank header", Message{Text: "  \nbody", Header: "  "}},
		{"header is not the first line", Message{Text: h + "\nbody", Header: "[ataniq-msg id=m2 from=ataniq kind=note]"}},
		{"header is only part of the first line", Message{Text: h + " more\nbody", Header: h}},
		{"header with a newline", Message{Text: h + "\nbody", Header: h + "\nbody"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, "claude")
			res := Deliver(context.Background(), f, c.m, Options{ExpectedMode: "manual"})
			wantResult(t, res, WrittenNothing, OutcomeRefusedText)
			if f.touched() {
				t.Errorf("the session was touched")
			}
			if res.Detail == "" {
				t.Errorf("no detail")
			}
		})
	}
	ok := h + "\n" + strings.Repeat("é", (MaxTextBytes-len(h)-1)/2)
	if len(ok) > MaxTextBytes || len(ok) < MaxTextBytes-2 {
		t.Fatalf("test text is %d bytes", len(ok))
	}
	if why := checkText(Message{Text: ok, Header: h}); why != "" {
		t.Errorf("a text of %d bytes was refused: %s", len(ok), why)
	}
	exact := h + "\n" + strings.Repeat("x", MaxTextBytes-len(h)-1)
	if why := checkText(Message{Text: exact, Header: h}); why != "" || len(exact) != MaxTextBytes {
		t.Errorf("a text of exactly 4096 bytes was refused: %s", why)
	}
	if why := checkText(Message{Text: "a\n\nb  c ünïcode ✓ 日本語", Header: "a"}); why != "" {
		t.Errorf("printable text was refused: %s", why)
	}
}

// I-11: a screen the recogniser does not know.
func TestUnknownScreen(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt).at(stepStart, evUnknownScreen)
		res := deliver(t, f, testMsg)
		wantResult(t, res, WrittenNothing, OutcomeNotIdle)
		if res.State != idle.StateUnknown || res.Before.Screen.Known {
			t.Errorf("state %q reading %+v", res.State, res.Before.Screen)
		}
	})
}

// I-12: codex reports idle but its title says Action Required.
func TestCodexActionRequired(t *testing.T) {
	f := newFake(t, "codex").at(stepStart, evTitle("[ ! ] Action Required | Run approval test command | work"))
	res := deliver(t, f, testMsg)
	wantResult(t, res, WrittenNothing, OutcomeNotIdle)
	if res.State != idle.StateUnknown || res.Before.Report.State != osc7501.StateIdle {
		t.Errorf("state %q report %+v", res.State, res.Before.Report)
	}
}

// The signals say idle only when the report and the screen agree; a stale
// report, and a report that is not the runtime's own, are not the state.
func TestStaleReport(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt).at(stepStart, evStaleReport)
		res := deliver(t, f, testMsg)
		wantResult(t, res, WrittenNothing, OutcomeNotIdle)
		if res.State != idle.StateUnknown || !res.Before.Report.Stale {
			t.Errorf("state %q report %+v", res.State, res.Before.Report)
		}
	})
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt).at(stepSettle2, evStaleReport)
		res := deliver(t, f, testMsg)
		wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
		if len(f.writes) != 1 || !res.After.Report.Stale {
			t.Errorf("writes %d report %+v", len(f.writes), res.After.Report)
		}
	})
}

// The terminal state must be right at both checks.
func TestTerminalState(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*TermState)
		fail string
	}{
		{"parser not at ground", func(s *TermState) { s.ParserGround = false }, "parser not at ground"},
		{"2026 frame open", func(s *TermState) { s.FrameOpen = true }, "2026 frame open"},
		{"bracketed paste off", func(s *TermState) { s.BracketedPaste = false }, "bracketed paste off"},
	}
	for _, c := range cases {
		each(t, func(t *testing.T, rt string) {
			t.Run(c.name+" at the first check", func(t *testing.T) {
				f := newFake(t, rt).at(stepStart, evTerm(c.mut))
				res := deliver(t, f, testMsg)
				wantResult(t, res, WrittenNothing, OutcomeNotIdle)
				if !hasFailed(res.Before, c.fail) {
					t.Errorf("failed %q", res.Before.Failed)
				}
			})
			t.Run(c.name+" at the second check", func(t *testing.T) {
				f := newFake(t, rt).at(stepSettle2, evTerm(c.mut))
				res := deliver(t, f, testMsg)
				wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
				if !hasFailed(res.After, c.fail) || len(f.writes) != 1 {
					t.Errorf("failed %q writes %d", res.After.Failed, len(f.writes))
				}
			})
		})
	}
}

// The second check wants the composer to hold the header (or a new marker) and
// the cursor in it, and does not leave that to the classification alone.
func TestSecondCheckReadsTheComposer(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		t.Run("the cursor is out of the composer", func(t *testing.T) {
			f := newFake(t, rt).at(stepSettle2, evCursorAway)
			res := deliver(t, f, testMsg)
			wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
			if !hasFailed(res.After, "cursor outside") {
				t.Errorf("failed %q", res.After.Failed)
			}
		})
		t.Run("the composer shows other text", func(t *testing.T) {
			f := newFake(t, rt).at(stepSettle2, evReplaceDraft)
			res := deliver(t, f, testMsg)
			wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
			if !hasFailed(res.After, "does not hold the header") {
				t.Errorf("failed %q", res.After.Failed)
			}
		})
	})
}

// Nothing is written after the deadline, at any step, and the outcome follows
// the paste.
func TestDeadlineAtEveryStep(t *testing.T) {
	cases := []struct {
		at step
		w  Written
		o  Outcome
	}{
		{stepStart, WrittenNothing, OutcomeNotIdle},
		{stepSettle1, WrittenNothing, OutcomeNotIdle},
		{stepFrozen1, WrittenNothing, OutcomeNotIdle},
		{stepSettle2, WrittenPaste, OutcomeNotSubmitted},
		{stepFrozen2, WrittenPaste, OutcomeNotSubmitted},
	}
	each(t, func(t *testing.T, rt string) {
		for _, c := range cases {
			t.Run(string(c.at), func(t *testing.T) {
				f := newFake(t, rt).at(c.at, evClockJump)
				res := deliver(t, f, testMsg)
				wantResult(t, res, c.w, c.o)
				if res.Detail != "deadline passed" {
					t.Errorf("detail %q", res.Detail)
				}
			})
		}
	})
	t.Run("already passed: the session is not touched", func(t *testing.T) {
		f := newFake(t, "claude")
		res := Deliver(context.Background(), f, testMsg, Options{ExpectedMode: "manual", Deadline: f.clock.Add(-time.Second), Now: f.now})
		wantResult(t, res, WrittenNothing, OutcomeNotIdle)
		if f.touched() {
			t.Errorf("the session was touched")
		}
	})
	t.Run("exactly at the deadline", func(t *testing.T) {
		f := newFake(t, "claude")
		res := Deliver(context.Background(), f, testMsg, Options{ExpectedMode: "manual", Deadline: f.clock, Now: f.now})
		wantResult(t, res, WrittenNothing, OutcomeNotIdle)
	})
}

// A cancelled context is a not idle before the paste and a not submitted
// after it.
func TestContextCancelled(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		t.Run("before the call", func(t *testing.T) {
			f := newFake(t, rt)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			res := Deliver(ctx, f, testMsg, Options{ExpectedMode: "manual", Now: f.now})
			wantResult(t, res, WrittenNothing, OutcomeNotIdle)
			if f.touched() {
				t.Errorf("the session was touched")
			}
		})
		for _, c := range []struct {
			at step
			w  Written
			o  Outcome
		}{
			{stepStart, WrittenNothing, OutcomeNotIdle},
			{stepSettle1, WrittenNothing, OutcomeNotIdle},
			{stepFrozen1, WrittenNothing, OutcomeNotIdle},
			{stepSettle2, WrittenPaste, OutcomeNotSubmitted},
			{stepFrozen2, WrittenPaste, OutcomeNotSubmitted},
		} {
			t.Run("at "+string(c.at), func(t *testing.T) {
				f := newFake(t, rt).at(c.at, evCancel)
				res := deliver(t, f, testMsg)
				wantResult(t, res, c.w, c.o)
			})
		}
	})
}

// I-14: the session dies during the fence. What was written is nothing before
// the paste was attempted, and unknown after.
func TestDeathDuringFence(t *testing.T) {
	cases := []struct {
		at step
		w  Written
	}{
		{stepStart, WrittenNothing},
		{stepSettle1, WrittenNothing},
		{stepFrozen1, WrittenNothing},
		{stepPaste, WrittenUnknown},
		{stepSettle2, WrittenUnknown},
		{stepFrozen2, WrittenUnknown},
		{stepEnter, WrittenUnknown},
	}
	each(t, func(t *testing.T, rt string) {
		for _, c := range cases {
			t.Run("dies at "+string(c.at), func(t *testing.T) {
				f := newFake(t, rt).at(c.at, evDie)
				res := deliver(t, f, testMsg)
				wantResult(t, res, c.w, OutcomeDied)
				if res.Detail == "" || len(f.submissions) != 0 {
					t.Errorf("detail %q submissions %d", res.Detail, len(f.submissions))
				}
			})
		}
		t.Run("the paste write fails", func(t *testing.T) {
			f := newFake(t, rt).at(stepFrozen1, evFailWrite)
			res := deliver(t, f, testMsg)
			wantResult(t, res, WrittenUnknown, OutcomeDied)
			if len(f.writes) != 1 || f.writes[0].ok {
				t.Errorf("writes %+v", f.writes)
			}
		})
		t.Run("the Enter write fails", func(t *testing.T) {
			f := newFake(t, rt).at(stepFrozen2, evFailWrite)
			res := deliver(t, f, testMsg)
			wantResult(t, res, WrittenUnknown, OutcomeDied)
			if len(f.writes) != 2 || f.writes[1].ok || len(f.submissions) != 0 {
				t.Errorf("writes %+v", f.writes)
			}
		})
	})
}

// An ended session is not idle before the paste and not submitted after it.
func TestSessionEnded(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt).at(stepStart, evEnded)
		res := deliver(t, f, testMsg)
		wantResult(t, res, WrittenNothing, OutcomeNotIdle)
		if res.State != idle.StateEnded || !hasFailed(res.Before, "session ended") {
			t.Errorf("state %q failed %q", res.State, res.Before.Failed)
		}
		f = newFake(t, rt).at(stepFrozen2, evEnded)
		res = deliver(t, f, testMsg)
		wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
		if res.State != idle.StateEnded {
			t.Errorf("state %q", res.State)
		}
	})
}

// I-17: output is still in the PTY buffer when the check runs. The first
// Settle reads it, so the check sees the dialog.
func TestPendingOutputIsRead(t *testing.T) {
	dialogAt := func(delay time.Duration) func(*fake) {
		return func(f *fake) {
			f.mode = mDialog
			f.draw(delay, true)
		}
	}
	each(t, func(t *testing.T, rt string) {
		cases := []struct {
			name string
			ev   func(*fake)
		}{
			{"drawn just before the fence", dialogAt(-20 * time.Millisecond)},
			{"in the buffer as the fence starts", dialogAt(0)},
			{"arrives within the quiet period of earlier output", func(f *fake) {
				f.push(-20*time.Millisecond, func(e *emuState) {})
				dialogAt(30 * time.Millisecond)(f)
			}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				f := newFake(t, rt).at(stepStart, c.ev)
				res := deliver(t, f, testMsg)
				wantResult(t, res, WrittenNothing, OutcomeBlocked)
				if !res.Before.Screen.Dialog || len(f.writes) != 0 {
					t.Errorf("reading %+v writes %d", res.Before, len(f.writes))
				}
			})
		}
	})
}

// A screen that never goes quiet does not drain: not idle before the paste,
// not submitted after it.
func TestNoisyScreen(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt).at(stepStart, evNoise(100, 30*time.Millisecond))
		res := deliver(t, f, testMsg)
		wantResult(t, res, WrittenNothing, OutcomeNotIdle)
		if res.Detail != "the screen did not settle in time" || f.frozens != 0 {
			t.Errorf("detail %q frozens %d", res.Detail, f.frozens)
		}
		f = newFake(t, rt).at(stepSettle2, evNoise(100, 30*time.Millisecond))
		res = deliver(t, f, testMsg)
		wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
		if res.Detail != "the screen did not settle after the paste" || f.frozens != 1 || res.After != nil {
			t.Errorf("detail %q frozens %d", res.Detail, f.frozens)
		}
	})
}

// A paste that is not on screen after the settle is not followed by Enter.
func TestPasteNotVisible(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt)
		f.echoDelay = 2 * time.Second
		res := deliver(t, f, testMsg)
		wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
		if !hasFailed(res.After, "does not hold the header") || len(f.writes) != 1 {
			t.Errorf("failed %q", res.After.Failed)
		}
	})
}

// A long message is shown by the runtime as its pasted-text marker; the
// marker is accepted when it was not there before.
func TestLongMessageShowsMarker(t *testing.T) {
	body := strings.Repeat("a line of a long body of text\n", 30)
	m := message("long1", body)
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt)
		res := deliver(t, f, m)
		wantResult(t, res, WrittenPasteAndEnter, OutcomeSubmitted)
		if !res.After.Screen.PastedMarker || res.After.Screen.HeaderHeld || res.Before.Screen.PastedMarker {
			t.Errorf("after %+v", res.After.Screen)
		}
		if len(f.submissions) != 1 || f.submissions[0] != m.Text {
			t.Errorf("submissions %q", f.submissions)
		}
		if rt == "claude" && !res.After.Screen.StateHidden {
			t.Errorf("claude's footer should hide the state")
		}
	})
}

// A marker that is already in the composer is the owner's paste, and the
// composer is not empty: the first check refuses.
func TestMarkerAlreadyPresent(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt).withCollapsedPaste()
		res := deliver(t, f, testMsg)
		wantResult(t, res, WrittenNothing, OutcomeNotIdle)
		if !hasFailed(res.Before, "composer not empty") || !hasFailed(res.Before, "pasted-text marker present") {
			t.Errorf("failed %q", res.Before.Failed)
		}
	})
}

// A collapsed paste without an idle report does not pass the second check on
// claude: the footer hides the state, so the report must give it.
func TestCollapsedPasteNeedsReport(t *testing.T) {
	body := strings.Repeat("a line of a long body of text\n", 30)
	f := newFake(t, "claude").at(stepSettle2, evStaleReport)
	res := deliver(t, f, message("long2", body))
	wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
	if res.State != idle.StateUnknown {
		t.Errorf("state %q", res.State)
	}
}

// What a Reading carries is bounded and holds no free text of the agent, the
// owner or the message.
func TestReadingsAreBounded(t *testing.T) {
	hostile := strings.Repeat("A", 5000) + "\x1b[2J\u202e"
	each(t, func(t *testing.T, rt string) {
		f := newFake(t, rt).
			withDraft("SECRET-OWNER-DRAFT", time.Second).
			at(stepStart, func(f *fake) {
				f.push(0, func(e *emuState) {
					e.status.Report.State = osc7501.StateBlocked
					e.status.Report.Msg = "MSG-SECRET"
					e.status.Report.Title = "TITLE-SECRET"
					e.status.Report.Kind = hostile
					e.status.Report.App += hostile
				})
			})
		m := message("m9", "body that must not show up in the readings: BODY-SECRET")
		res := deliver(t, f, m)
		line := res.Readings()
		for _, bad := range []string{"SECRET-OWNER-DRAFT", "BODY-SECRET", "MSG-SECRET", "TITLE-SECRET", "\x1b", strings.Repeat("A", 70)} {
			if strings.Contains(line, bad) {
				t.Errorf("readings contain %q: %q", bad, line)
			}
		}
		if len(line) > maxReadingText {
			t.Errorf("readings are %d bytes", len(line))
		}
		for _, c := range []byte(line) {
			if c < 0x20 || c > 0x7e {
				t.Fatalf("readings hold byte %#x: %q", c, line)
			}
		}
		if len(res.Before.Report.App) > maxToken || len(res.Before.Report.Kind) > maxToken {
			t.Errorf("report tokens %d %d", len(res.Before.Report.App), len(res.Before.Report.Kind))
		}
		for _, s := range res.Before.Signals {
			if len(s.Detail) > maxToken+80 {
				t.Errorf("signal detail %d bytes", len(s.Detail))
			}
		}
	})
}

func TestTimingDefaults(t *testing.T) {
	d := DefaultTiming()
	want := Timing{OwnerQuiet: 2 * time.Second, ScreenQuiet: 300 * time.Millisecond, CheckQuiet: 50 * time.Millisecond,
		FirstSettleMax: time.Second, PasteSettleMin: 150 * time.Millisecond, PasteSettleMax: time.Second}
	if d != want {
		t.Errorf("DefaultTiming %+v", d)
	}
	if (Timing{}).normalised() != want {
		t.Errorf("a zero Timing is not the default")
	}
	got := Timing{OwnerQuiet: -time.Second, PasteSettleMin: time.Second, PasteSettleMax: time.Millisecond, CheckQuiet: time.Millisecond}.normalised()
	if got.OwnerQuiet != 0 || got.PasteSettleMax != time.Second {
		t.Errorf("normalised %+v", got)
	}
	// A timing given by the caller is used.
	f := newFake(t, "claude")
	f.ownerLast = f.clock.Add(-time.Second)
	res := Deliver(context.Background(), f, testMsg, Options{ExpectedMode: "manual", Now: f.now,
		Timing: Timing{OwnerQuiet: 500 * time.Millisecond, ScreenQuiet: 10 * time.Millisecond, CheckQuiet: 5 * time.Millisecond, FirstSettleMax: 100 * time.Millisecond, PasteSettleMin: 20 * time.Millisecond, PasteSettleMax: 200 * time.Millisecond}})
	wantResult(t, res, WrittenPasteAndEnter, OutcomeSubmitted)
	if f.settleArg[0] != [3]time.Duration{0, 5 * time.Millisecond, 100 * time.Millisecond} || f.settleArg[1] != [3]time.Duration{20 * time.Millisecond, 5 * time.Millisecond, 200 * time.Millisecond} {
		t.Errorf("settle arguments %v", f.settleArg)
	}
}

// With no clock and no deadline given, the fence runs and the deadline never
// stops it.
func TestNoClockNoDeadline(t *testing.T) {
	f := newFake(t, "codex")
	res := Deliver(context.Background(), f, testMsg, Options{ExpectedMode: "manual"})
	wantResult(t, res, WrittenPasteAndEnter, OutcomeSubmitted)
	// A nil context is tolerated.
	f = newFake(t, "codex")
	var noCtx context.Context
	res = Deliver(noCtx, f, testMsg, Options{ExpectedMode: "manual"})
	wantResult(t, res, WrittenPasteAndEnter, OutcomeSubmitted)
}

// A claude that expects another mode is not idle.
func TestExpectedMode(t *testing.T) {
	f := newFake(t, "claude")
	res := Deliver(context.Background(), f, testMsg, Options{ExpectedMode: "auto", Now: f.now})
	wantResult(t, res, WrittenNothing, OutcomeNotIdle)
	if res.State != idle.StateUnknown {
		t.Errorf("state %q", res.State)
	}
	f = newFake(t, "claude")
	res = Deliver(context.Background(), f, testMsg, Options{Now: f.now})
	wantResult(t, res, WrittenNothing, OutcomeNotIdle)
}

// wrapper makes a session misbehave around the real one.
type wrapper struct {
	Session
	skipFn   bool
	afterErr error
}

func (w wrapper) WithInputLock(ctx context.Context, fn func(Locked) error) error {
	if w.skipFn {
		return nil
	}
	err := w.Session.WithInputLock(ctx, fn)
	if err == nil {
		err = w.afterErr
	}
	return err
}

func TestSessionMisbehaviour(t *testing.T) {
	t.Run("nil session", func(t *testing.T) {
		wantResult(t, Deliver(context.Background(), nil, testMsg, Options{}), WrittenNothing, OutcomeDied)
	})
	t.Run("the session does not run the fence", func(t *testing.T) {
		f := newFake(t, "claude")
		wantResult(t, Deliver(context.Background(), wrapper{Session: f, skipFn: true}, testMsg, Options{ExpectedMode: "manual"}), WrittenNothing, OutcomeDied)
	})
	t.Run("the lock fails to release after Enter", func(t *testing.T) {
		f := newFake(t, "claude")
		res := Deliver(context.Background(), wrapper{Session: f, afterErr: errors.New("holder gone")}, testMsg, Options{ExpectedMode: "manual", Now: f.now})
		wantResult(t, res, WrittenUnknown, OutcomeDied)
	})
	t.Run("the lock fails to release after a check that wrote nothing", func(t *testing.T) {
		f := newFake(t, "claude").setMode(mWorking)
		res := Deliver(context.Background(), wrapper{Session: f, afterErr: errors.New("holder gone")}, testMsg, Options{ExpectedMode: "manual", Now: f.now})
		wantResult(t, res, WrittenNothing, OutcomeNotIdle)
	})
	t.Run("the lock cannot be taken", func(t *testing.T) {
		f := newFake(t, "claude")
		f.dead = true
		wantResult(t, deliver(t, f, testMsg), WrittenNothing, OutcomeDied)
	})
}

// Design 4.6, the one window the fence cannot close: the agent opens a dialog
// after the last reading and before the Enter lands, and then the Enter
// answers it. This test states the limit; it does not accept it. Every other
// step is covered by the table in table_test.go with no approval allowed.
func TestKnownLimitDialogAfterLastReading(t *testing.T) {
	for _, at := range []step{stepFrozen2, stepEnter} {
		each(t, func(t *testing.T, rt string) {
			t.Run(string(at), func(t *testing.T) {
				f := newFake(t, rt).at(at, evOpenDialog)
				res := runFence(t, f, testMsg)
				checkInvariants(t, f, testMsg, res, invariants{deadline: f.deadline, residual: true})
				wantResult(t, res, WrittenPasteAndEnter, OutcomeSubmitted)
				if f.approvals != 1 {
					t.Errorf("approvals %d: the residual window of design 4.6 was expected to show", f.approvals)
				}
			})
		})
	}
}
