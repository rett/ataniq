package idle

import (
	"testing"

	"ataniq/internal/inject/osc7501"
	"ataniq/internal/inject/screen"
)

// replayCase says what one recorded screen classifies as, with the report an
// agent would plausibly have sent for what the screen shows.
type replayCase struct {
	report osc7501.State // empty: no report yet
	mode   string        // ExpectedMode for claude; default "manual"
	state  State
}

// replayExtra holds what a few rows need beyond replayCase.
type replayExtra struct {
	// title, when set, replaces the title of the recording.
	title string
	// reportOnly is the wanted Classification.ReportOnly.
	reportOnly bool
}

// replayExtras is by runtime and fixture name. A fixture without an entry
// keeps its recorded title and must not be ReportOnly.
var replayExtras = map[string]replayExtra{
	// The recorded title of -a and -b is U+FFFD: a capture defect of the
	// emulator (the title leak, row1 checklist item a), not claude's title,
	// which began with the idle glyph. The replay gives them that title and a
	// topic; the broken title is a case of the table in idle_test.go.
	"claude/collapsed-paste-footer-a": {"✳ topic", true},
	"claude/collapsed-paste-footer-b": {"✳ topic", true},
	"claude/collapsed-paste-footer-c": {"", true},
}

const noReport = osc7501.State("")

// replayWant is the table of the replay, by runtime and fixture name. A
// fixture without a row fails the test, so a new recording is classified on
// purpose.
var replayWant = map[string]replayCase{
	"claude/background-shell-a":         {osc7501.StateIdle, "", StateWorking},
	"claude/background-shell-b":         {osc7501.StateIdle, "", StateWorking},
	"claude/blank-main-screen":          {noReport, "", StateUnknown},
	"claude/collapsed-paste-footer-a":   {osc7501.StateIdle, "", StateIdle}, // title overridden, see replayExtras
	"claude/collapsed-paste-footer-b":   {osc7501.StateIdle, "", StateIdle}, // same
	"claude/collapsed-paste-footer-c":   {osc7501.StateIdle, "", StateIdle},
	"claude/dialog-write-after-paste":   {osc7501.StateBlocked, "", StateBlocked},
	"claude/dialog-write-before-paste":  {osc7501.StateBlocked, "", StateBlocked},
	"claude/dialog-write-open":          {osc7501.StateBlocked, "", StateBlocked},
	"claude/exit-resume":                {osc7501.StateClear, "", StateUnknown},
	"claude/idle-after-approval":        {osc7501.StateIdle, "", StateIdle},
	"claude/idle-after-background-done": {osc7501.StateIdle, "", StateIdle},
	"claude/idle-after-three-turns":     {osc7501.StateIdle, "", StateIdle},
	"claude/idle-after-turn":            {osc7501.StateIdle, "", StateIdle},
	"claude/idle-before-paste":          {osc7501.StateIdle, "", StateIdle},
	"claude/idle-ghost-suggestion":      {osc7501.StateIdle, "", StateIdle},
	"claude/idle-hint-manual":           {osc7501.StateIdle, "", StateIdle},
	"claude/pasted-header-accepted":     {osc7501.StateIdle, "", StateIdle},
	"claude/start-hint-auto":            {noReport, "auto", StateIdle},
	"claude/typed-long-prompt":          {osc7501.StateIdle, "", StateIdle},
	"claude/typed-one-row":              {osc7501.StateIdle, "", StateIdle},
	"claude/typed-slash-exit":           {osc7501.StateIdle, "", StateIdle},
	"claude/typed-two-rows":             {osc7501.StateIdle, "", StateIdle},
	"claude/working-spinner":            {osc7501.StateWorking, "", StateWorking},
	"codex/blank-alt-screen":            {noReport, "", StateUnknown},
	"codex/dialog-command-after-paste":  {osc7501.StateBlocked, "", StateBlocked},
	"codex/dialog-command-before-paste": {osc7501.StateBlocked, "", StateBlocked},
	"codex/dialog-command-open":         {osc7501.StateBlocked, "", StateBlocked},
	"codex/exit-disconnected":           {osc7501.StateClear, "", StateUnknown},
	"codex/idle-after-ack":              {osc7501.StateIdle, "", StateIdle},
	"codex/idle-after-approval":         {osc7501.StateIdle, "", StateIdle},
	"codex/idle-after-three-turns":      {osc7501.StateIdle, "", StateIdle},
	"codex/idle-after-turn":             {osc7501.StateIdle, "", StateIdle},
	"codex/idle-before-paste":           {osc7501.StateIdle, "", StateIdle},
	"codex/pasted-header-accepted":      {osc7501.StateIdle, "", StateIdle},
	"codex/pasted-header-accepted-b":    {osc7501.StateIdle, "", StateIdle},
	"codex/start-animation":             {noReport, "", StateUnknown},
	"codex/start-hint":                  {osc7501.StateIdle, "", StateIdle},
	"codex/tall-composer-a":             {osc7501.StateIdle, "", StateIdle},
	"codex/tall-composer-b":             {osc7501.StateIdle, "", StateIdle},
	"codex/typed-slash-popup":           {osc7501.StateIdle, "", StateIdle},
	"codex/typed-two-rows":              {osc7501.StateIdle, "", StateIdle},
	"codex/working-after-long-answer":   {osc7501.StateWorking, "", StateWorking},
	"codex/working-line":                {osc7501.StateWorking, "", StateWorking},
}

// replayInput builds the input of a fixture with a report state; noReport is
// no report at all.
func replayInput(rec recorded, st osc7501.State, mode string) Input {
	in := Input{Screen: rec.snap, ExpectedMode: mode}
	if st != noReport {
		in.Report = status(st, appOf(rec.runtime))
	}
	return in
}

func TestReplay(t *testing.T) {
	all := loadAll(t)
	if len(all) < 40 {
		t.Fatalf("only %d fixtures, want at least 40", len(all))
	}
	seen := map[string]bool{}
	for _, rec := range all {
		key := rec.runtime + "/" + rec.name
		seen[key] = true
		t.Run(key, func(t *testing.T) {
			want, ok := replayWant[key]
			if !ok {
				t.Fatalf("no row for %s in replayWant", key)
			}
			mode := want.mode
			if mode == "" {
				mode = "manual"
			}
			c := lookup(t, rec.runtime, rec.version)
			in := replayInput(rec, want.report, mode)
			extra := replayExtras[key]
			if extra.title != "" {
				in.Screen.Title = extra.title
			}
			cl := c.Classify(in)
			if cl.ReportOnly != extra.reportOnly {
				t.Errorf("ReportOnly %v, want %v", cl.ReportOnly, extra.reportOnly)
			}
			if cl.State != want.state {
				t.Fatalf("state %q, want %q\nscreen: %+v\ntitle: %+v\nsignals: %+v", cl.State, want.state, cl.Screen, cl.Title, cl.Signals)
			}
			if cl.ComposerEmpty != cl.Screen.ComposerEmpty {
				t.Errorf("ComposerEmpty %v differs from the screen's %v", cl.ComposerEmpty, cl.Screen.ComposerEmpty)
			}
			checkSignalShape(t, cl)

			// The rules the task states, whatever the table says.
			rd := readScreen(rec, t)
			if rd.Known && rd.Dialog && cl.State != StateBlocked {
				t.Errorf("a fixture showing a dialog is %q, want blocked", cl.State)
			}
			if rd.Known && rd.ComposerEmpty && !rd.Working && !rd.BackgroundTask && want.report == osc7501.StateIdle &&
				!cl.Title.Busy && cl.State != StateIdle {
				t.Errorf("an empty composer with an idle report is %q, want idle", cl.State)
			}
			if (want.report == osc7501.StateWorking || want.report == osc7501.StateBlocked) && cl.State == StateIdle {
				t.Errorf("idle with a %s report", want.report)
			}
		})
	}
	for key := range replayWant {
		if !seen[key] {
			t.Errorf("replayWant has a row for %s, which is not a fixture", key)
		}
	}
}

// readScreen is the screen package's own reading of a fixture.
func readScreen(rec recorded, t testing.TB) screen.Reading {
	t.Helper()
	r, ok := screen.For(rec.runtime, rec.version)
	if !ok {
		t.Fatalf("no recogniser for %s %s", rec.runtime, rec.version)
	}
	return r.Read(rec.snap)
}

// reportVariants are the statuses every fixture is classified with in the
// sweep.
func reportVariants(runtime string) map[string]osc7501.Status {
	app := appOf(runtime)
	v := map[string]osc7501.Status{
		"none":       {},
		"stale-none": {Stale: true},
		"other-app":  status(osc7501.StateIdle, "other"),
		"other-blk":  status(osc7501.StateBlocked, "other"),
	}
	for _, st := range []osc7501.State{
		osc7501.StateIdle, osc7501.StateDone, osc7501.StateWorking,
		osc7501.StateBlocked, osc7501.StateError, osc7501.StateClear,
	} {
		v[string(st)] = status(st, app)
		s := status(st, app)
		s.Stale = true
		v["stale-"+string(st)] = s
	}
	return v
}

// The sweep classifies every fixture with every kind of report, with and
// without Ended, and holds each result to the rules of the package.
func TestReplaySweep(t *testing.T) {
	for _, rec := range loadAll(t) {
		c := lookup(t, rec.runtime, rec.version)
		rd := readScreen(rec, t)
		dialog := rd.Known && rd.Dialog && rec.snap.AltScreen
		for name, st := range reportVariants(rec.runtime) {
			for _, end := range []bool{false, true} {
				in := Input{Screen: rec.snap, Report: st, Ended: end, ExpectedMode: "manual"}
				cl := c.Classify(in)
				where := rec.runtime + "/" + rec.name + " report " + name
				switch {
				case end:
					if cl.State != StateEnded {
						t.Errorf("%s ended: state %q", where, cl.State)
					}
				case st.Has && !st.Stale && st.Report.App == appOf(rec.runtime) && st.Report.State == osc7501.StateBlocked:
					if cl.State != StateBlocked {
						t.Errorf("%s: a blocked report gave %q", where, cl.State)
					}
				case dialog:
					if cl.State != StateBlocked {
						t.Errorf("%s: a dialog gave %q", where, cl.State)
					}
				}
				checkProperty(t, rec.runtime, in, cl, where)
			}
		}
	}
}

// For claude the report is not needed: with no report the recogniser decides
// as with an idle report, except under the paste footer, where the screen does
// not show the state and the answer is unknown.
func TestClaudeNoReportEqualsIdleReport(t *testing.T) {
	for _, rec := range loadAll(t) {
		if rec.runtime != "claude" {
			continue
		}
		c := lookup(t, rec.runtime, rec.version)
		rd := readScreen(rec, t)
		withIdle := c.Classify(replayInput(rec, osc7501.StateIdle, "manual"))
		without := c.Classify(replayInput(rec, noReport, "manual"))
		hidden := rd.StateHidden
		switch {
		case hidden && withIdle.State == StateIdle:
			if without.State != StateUnknown {
				t.Errorf("%s under the paste footer, no report: %q, want unknown", rec.name, without.State)
			}
		case withIdle.State != without.State:
			t.Errorf("%s: no report %q, report idle %q", rec.name, without.State, withIdle.State)
		}
	}
}
