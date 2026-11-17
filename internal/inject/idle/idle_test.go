package idle

import (
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/inject/osc7501"
)

const (
	claudeVer = "2.1.325"
	codexVer  = "0.169"
)

func TestLookup(t *testing.T) {
	good := [][2]string{{"claude", "2.1.325"}, {"codex", "0.169"}, {"codex", "0.169.0"}}
	for _, g := range good {
		c, ok := Lookup(g[0], g[1])
		if !ok || c.Runtime() != g[0] {
			t.Errorf("Lookup(%q, %q) = %q, %v; want the %s classifier", g[0], g[1], c.Runtime(), ok, g[0])
		}
	}
	bad := [][2]string{
		{"", ""}, {"claude", ""}, {"", "2.1.325"}, {"claude", "2.1.326"}, {"claude", "2.1"},
		{"claude", "2.1.325 "}, {"claude", "0.169"}, {"codex", "2.1.325"}, {"codex", "0.169.1"},
		{"codex", "0.170"}, {"codex", "0.16"}, {"codex", "0.169.00"}, {"gemini", "1"},
		{"Claude", "2.1.325"}, {"claude-code", "2.1.325"}, {"codex", "v0.169"},
	}
	for _, b := range bad {
		if c, ok := Lookup(b[0], b[1]); ok || c.Runtime() != "" {
			t.Errorf("Lookup(%q, %q) found a classifier", b[0], b[1])
		}
	}
}

func TestTested(t *testing.T) {
	got := Tested()
	want := []Entry{
		{Runtime: "claude", Version: "2.1.325", App: "claude-code"},
		{Runtime: "codex", Version: "0.169", Aliases: []string{"0.169.0"}, App: "codex"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tested() = %+v, want %+v", got, want)
	}
	// Every entry has a classifier under each spelling, and nothing else
	// has: Tested and Lookup describe one table.
	for _, e := range got {
		for _, v := range append([]string{e.Version}, e.Aliases...) {
			if _, ok := Lookup(e.Runtime, v); !ok {
				t.Errorf("Tested lists %s %s but Lookup does not find it", e.Runtime, v)
			}
		}
	}
	// The result is a copy.
	got[1].Aliases[0] = "changed"
	got[0].Version = "changed"
	if again := Tested(); !reflect.DeepEqual(again, want) {
		t.Fatalf("Tested() shares memory with the table: %+v", again)
	}
}

func TestZeroClassifier(t *testing.T) {
	var c Classifier
	if c.Runtime() != "" {
		t.Errorf("zero Runtime = %q", c.Runtime())
	}
	idle := loadFixture(t, "claude", claudeVer, "idle-after-turn")
	inputs := []Input{
		{},
		{Ended: true},
		{Screen: idle.snap, ExpectedMode: "manual"},
		{Screen: idle.snap, ExpectedMode: "manual", Report: status(osc7501.StateIdle, "claude-code")},
		{Screen: idle.snap, Report: status(osc7501.StateBlocked, "claude-code")},
	}
	for i, in := range inputs {
		if cl := c.Classify(in); cl.State != StateUnknown {
			t.Errorf("zero classifier, input %d: state %q, want unknown", i, cl.State)
		}
	}
}

// override changes a loaded snapshot for one case.
type override func(*Input)

func withTitle(s string) override { return func(in *Input) { in.Screen.Title = s } }
func altScreen(on bool) override  { return func(in *Input) { in.Screen.AltScreen = on } }
func ended() override             { return func(in *Input) { in.Ended = true } }
func mode(m string) override      { return func(in *Input) { in.ExpectedMode = m } }
func cursorAt(x, y int) override {
	return func(in *Input) { in.Screen.CursorX, in.Screen.CursorY = x, y }
}
func cursorHidden() override { return func(in *Input) { in.Screen.CursorHidden = true } }
func noFaint() override      { return func(in *Input) { in.Screen.Faint = nil } }
func text(s string) override { return func(in *Input) { in.Screen.Text = s } }

func stale(st osc7501.Status) osc7501.Status { st.Stale = true; return st }

var (
	none         = osc7501.Status{}
	staleNothing = osc7501.Status{Stale: true}
)

type tableCase struct {
	name    string
	runtime string
	fixture string
	// report is the last status; claudeMode is ExpectedMode (claude only,
	// default "manual").
	report osc7501.Status
	with   []override
	state  State
	// empty is the wanted ComposerEmpty.
	empty bool
	// says maps a source to the word it must say, when the case checks it.
	says map[string]string
}

func claudeCase(name, fixture string, rep osc7501.Status, state State, empty bool, with ...override) tableCase {
	return tableCase{name: name, runtime: "claude", fixture: fixture, report: rep, with: with, state: state, empty: empty}
}

func codexCase(name, fixture string, rep osc7501.Status, state State, empty bool, with ...override) tableCase {
	return tableCase{name: name, runtime: "codex", fixture: fixture, report: rep, with: with, state: state, empty: empty}
}

func (c tableCase) withSays(kv ...string) tableCase {
	c.says = map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		c.says[kv[i]] = kv[i+1]
	}
	return c
}

var (
	ccIdle    = status(osc7501.StateIdle, "claude-code")
	ccDone    = status(osc7501.StateDone, "claude-code")
	ccWork    = status(osc7501.StateWorking, "claude-code")
	ccBlock   = status(osc7501.StateBlocked, "claude-code")
	ccError   = status(osc7501.StateError, "claude-code")
	ccClear   = status(osc7501.StateClear, "claude-code")
	cxIdle    = status(osc7501.StateIdle, "codex")
	cxDone    = status(osc7501.StateDone, "codex")
	cxWork    = status(osc7501.StateWorking, "codex")
	cxBlock   = status(osc7501.StateBlocked, "codex")
	cxError   = status(osc7501.StateError, "codex")
	cxClear   = status(osc7501.StateClear, "codex")
	otherApp  = status(osc7501.StateIdle, "something-else")
	otherBlk  = status(osc7501.StateBlocked, "something-else")
	noState   = osc7501.Status{Has: true, Report: osc7501.Report{App: "codex"}}
	garbledCC = osc7501.Status{Has: true, Report: osc7501.Report{State: "napping", App: "claude-code"}}
)

var tableCases = []tableCase{
	// claude: the recogniser decides, the report only vetoes.
	claudeCase("claude idle screen, no report", "idle-after-turn", none, StateIdle, true),
	claudeCase("claude idle screen, report idle", "idle-after-turn", ccIdle, StateIdle, true),
	claudeCase("claude idle screen, report done", "idle-after-turn", ccDone, StateIdle, true),
	claudeCase("claude idle screen, ghost suggestion", "idle-ghost-suggestion", ccIdle, StateIdle, true),
	claudeCase("claude Q13 veto: screen looks idle, report blocked", "idle-after-turn", ccBlock, StateBlocked, true).
		withSays("report", "blocked", "screen", "composer"),
	claudeCase("claude report working vetoes idle", "idle-after-turn", ccWork, StateWorking, true),
	claudeCase("claude report error", "idle-after-turn", ccError, StateUnknown, true),
	claudeCase("claude report clear", "idle-after-turn", ccClear, StateUnknown, true),
	claudeCase("claude unreadable report state", "idle-after-turn", garbledCC, StateUnknown, true),
	claudeCase("claude stale status", "idle-after-turn", stale(ccIdle), StateUnknown, true).withSays("report", "stale"),
	claudeCase("claude stale status, nothing valid ever", "idle-after-turn", staleNothing, StateUnknown, true),
	claudeCase("claude stale blocked report is not the state", "idle-after-turn", stale(ccBlock), StateUnknown, true),
	claudeCase("claude report from another app", "idle-after-turn", otherApp, StateUnknown, true).withSays("report", "other app"),
	claudeCase("claude blocked report from another app", "idle-after-turn", otherBlk, StateUnknown, true),
	claudeCase("claude report from codex", "idle-after-turn", cxIdle, StateUnknown, true),
	claudeCase("claude empty expected mode never matches", "idle-after-turn", ccIdle, StateUnknown, true, mode("")),
	claudeCase("claude wrong expected mode", "idle-after-turn", ccIdle, StateUnknown, true, mode("auto")),
	claudeCase("claude expected mode is exact", "idle-after-turn", none, StateUnknown, true, mode("Manual")),
	claudeCase("claude auto footer with auto expected", "start-hint-auto", none, StateIdle, true, mode("auto")),
	claudeCase("claude auto footer with manual expected", "start-hint-auto", none, StateUnknown, true),
	claudeCase("claude busy title vetoes idle", "idle-after-turn", ccIdle, StateWorking, true, withTitle("◐ ataniq-msg acknowledgment protocol")),
	claudeCase("claude busy title, no report", "idle-after-turn", none, StateWorking, true, withTitle("◑ topic")),
	claudeCase("claude unrecorded glyph in the title reads busy", "idle-after-turn", ccIdle, StateWorking, true, withTitle("★ topic")),
	claudeCase("claude title without a glyph proves nothing", "idle-after-turn", ccIdle, StateIdle, true, withTitle("topic")),
	claudeCase("claude empty title proves nothing", "idle-after-turn", none, StateIdle, true, withTitle("")),
	claudeCase("claude working line", "working-spinner", none, StateWorking, true).withSays("screen", "working"),
	claudeCase("claude working line, report idle", "working-spinner", ccIdle, StateWorking, true),
	claudeCase("claude background shell", "background-shell-a", ccIdle, StateWorking, true).withSays("screen", "background task"),
	claudeCase("claude background shell, no report", "background-shell-b", none, StateWorking, true),
	claudeCase("claude idle again after the background task", "idle-after-background-done", ccIdle, StateIdle, true),
	claudeCase("claude dialog, no report", "dialog-write-open", none, StateBlocked, false).withSays("screen", "dialog"),
	claudeCase("claude dialog, report idle", "dialog-write-open", ccIdle, StateBlocked, false),
	claudeCase("claude dialog, report blocked", "dialog-write-before-paste", ccBlock, StateBlocked, false).withSays("report", "blocked", "screen", "dialog"),
	claudeCase("claude dialog, stale status", "dialog-write-after-paste", stale(ccIdle), StateBlocked, false),
	claudeCase("claude dialog, report from another app", "dialog-write-open", otherApp, StateBlocked, false),
	claudeCase("claude dialog off the alternate screen is unknown", "dialog-write-open", none, StateUnknown, false, altScreen(false)),
	claudeCase("claude blocked report, dialog off the alternate screen", "dialog-write-open", ccBlock, StateBlocked, false, altScreen(false)),
	claudeCase("claude unknown screen (I-11), report idle", "blank-main-screen", ccIdle, StateUnknown, false).withSays("screen", "unknown"),
	claudeCase("claude unknown screen, no report", "blank-main-screen", none, StateUnknown, false),
	claudeCase("claude unknown screen with a blocked report", "blank-main-screen", ccBlock, StateBlocked, false),
	claudeCase("claude unknown screen with a working report", "blank-main-screen", ccWork, StateUnknown, false),
	claudeCase("claude exit screen off the alternate screen", "exit-resume", ccIdle, StateUnknown, false),
	claudeCase("claude idle screen off the alternate screen", "idle-after-turn", ccIdle, StateUnknown, false, altScreen(false)),
	claudeCase("claude blocked report off the alternate screen", "idle-after-turn", ccBlock, StateBlocked, false, altScreen(false)),
	claudeCase("claude text of another layout", "idle-after-turn", ccIdle, StateUnknown, false, text("hello\nworld\n")),
	claudeCase("claude cursor outside the composer", "idle-after-turn", ccIdle, StateUnknown, false, cursorAt(2, 0)),
	claudeCase("claude cursor hidden", "idle-after-turn", ccIdle, StateUnknown, false, cursorHidden()),
	claudeCase("claude ended", "idle-after-turn", ccIdle, StateEnded, true, ended()).withSays("process", "ended"),
	claudeCase("claude ended beats a dialog", "dialog-write-open", ccBlock, StateEnded, false, ended()),
	claudeCase("claude ended on an unknown screen", "exit-resume", ccClear, StateEnded, false, ended()),
	claudeCase("claude owner draft at an idle prompt", "typed-one-row", ccIdle, StateIdle, false),
	claudeCase("claude owner draft, no report", "typed-two-rows", none, StateIdle, false),
	claudeCase("claude owner draft and a blocked report", "typed-one-row", ccBlock, StateBlocked, false),
	claudeCase("claude faint ghost without style data is text", "idle-ghost-suggestion", ccIdle, StateIdle, false, noFaint()),
	// The collapsed-paste footer: the state is not on screen.
	claudeCase("claude paste footer, report idle", "collapsed-paste-footer-c", ccIdle, StateIdle, false),
	claudeCase("claude paste footer, report done", "collapsed-paste-footer-c", ccDone, StateIdle, false),
	claudeCase("claude paste footer, no report is unknown", "collapsed-paste-footer-c", none, StateUnknown, false).withSays("screen", "composer"),
	claudeCase("claude paste footer, stale status", "collapsed-paste-footer-c", stale(ccIdle), StateUnknown, false),
	claudeCase("claude paste footer, report working", "collapsed-paste-footer-c", ccWork, StateWorking, false),
	claudeCase("claude paste footer, report blocked", "collapsed-paste-footer-c", ccBlock, StateBlocked, false),
	claudeCase("claude paste footer, report error", "collapsed-paste-footer-c", ccError, StateUnknown, false),
	claudeCase("claude paste footer, report clear", "collapsed-paste-footer-c", ccClear, StateUnknown, false),
	claudeCase("claude paste footer is not a mode mismatch", "collapsed-paste-footer-c", ccIdle, StateIdle, false, mode("")),
	claudeCase("claude paste footer, report from another app", "collapsed-paste-footer-c", otherApp, StateUnknown, false),
	claudeCase("claude paste footer, busy title", "collapsed-paste-footer-c", ccIdle, StateWorking, false, withTitle("◐ topic")),
	claudeCase("claude paste footer with a recorded broken title reads busy", "collapsed-paste-footer-a", ccIdle, StateWorking, false),

	// codex: the report is required, the second signal must agree.
	codexCase("codex idle screen, report idle", "idle-after-turn", cxIdle, StateIdle, true),
	codexCase("codex idle screen, report done", "idle-after-turn", cxDone, StateIdle, true),
	codexCase("codex no report", "idle-after-turn", none, StateUnknown, true).withSays("report", "none"),
	codexCase("codex report clear", "idle-after-turn", cxClear, StateUnknown, true),
	codexCase("codex report error", "idle-after-turn", cxError, StateUnknown, true),
	codexCase("codex report without a state", "idle-after-turn", noState, StateUnknown, true),
	codexCase("codex report working", "idle-after-turn", cxWork, StateWorking, true),
	codexCase("codex report blocked, screen idle", "idle-after-turn", cxBlock, StateBlocked, true).withSays("report", "blocked", "screen", "composer"),
	codexCase("codex I-12: report idle, title Action Required", "idle-after-turn", cxIdle, StateUnknown, true,
		withTitle("[ ! ] Action Required | Run approval test command | work")).withSays("title", "action required"),
	codexCase("codex I-12: report idle, blinked title Action Required", "idle-after-turn", cxIdle, StateUnknown, true,
		withTitle("[ . ] Action Required | Run a command | work")),
	codexCase("codex report idle, busy title", "idle-after-turn", cxIdle, StateUnknown, true, withTitle("⠙ Acknowledge tagged messages | work")).withSays("title", "busy"),
	codexCase("codex report idle, unrecorded glyph in the title", "idle-after-turn", cxIdle, StateUnknown, true, withTitle("★ x")),
	codexCase("codex report idle, working line on screen", "working-line", cxIdle, StateUnknown, true).withSays("screen", "working"),
	codexCase("codex report done, working line on screen", "working-after-long-answer", cxDone, StateUnknown, true),
	codexCase("codex report working, working line on screen", "working-line", cxWork, StateWorking, true),
	codexCase("codex no report, working line on screen", "working-line", none, StateUnknown, true),
	codexCase("codex report idle, empty title", "idle-after-turn", cxIdle, StateIdle, true, withTitle("")),
	codexCase("codex dialog, report blocked", "dialog-command-open", cxBlock, StateBlocked, false).withSays("report", "blocked", "screen", "dialog"),
	codexCase("codex dialog, report idle", "dialog-command-open", cxIdle, StateBlocked, false),
	codexCase("codex dialog, no report", "dialog-command-before-paste", none, StateBlocked, false),
	codexCase("codex dialog, report working", "dialog-command-after-paste", cxWork, StateBlocked, false),
	codexCase("codex dialog, stale status", "dialog-command-open", stale(cxIdle), StateBlocked, false),
	codexCase("codex dialog off the alternate screen is unknown", "dialog-command-open", cxIdle, StateUnknown, false, altScreen(false)),
	codexCase("codex blocked report, dialog off the alternate screen", "dialog-command-open", cxBlock, StateBlocked, false, altScreen(false)),
	codexCase("codex stale status", "idle-after-turn", stale(cxIdle), StateUnknown, true),
	codexCase("codex stale blocked report is not the state", "idle-after-turn", stale(cxBlock), StateUnknown, true),
	codexCase("codex stale status, nothing valid ever", "idle-after-turn", staleNothing, StateUnknown, true),
	codexCase("codex report from claude", "idle-after-turn", ccIdle, StateUnknown, true).withSays("report", "other app"),
	codexCase("codex blocked report from claude", "idle-after-turn", ccBlock, StateUnknown, true),
	codexCase("codex working report from another app", "idle-after-turn", status(osc7501.StateWorking, "x"), StateUnknown, true),
	codexCase("codex unknown screen (I-11), report idle", "blank-alt-screen", cxIdle, StateUnknown, false).withSays("screen", "unknown"),
	codexCase("codex unknown screen, report working", "blank-alt-screen", cxWork, StateUnknown, false),
	codexCase("codex unknown screen, report blocked", "blank-alt-screen", cxBlock, StateBlocked, false),
	codexCase("codex exit screen", "exit-disconnected", cxIdle, StateUnknown, false),
	codexCase("codex idle screen off the alternate screen", "idle-after-turn", cxIdle, StateUnknown, false, altScreen(false)),
	codexCase("codex text of another layout", "idle-after-turn", cxIdle, StateUnknown, false, text("hello\n\n  world\n")),
	codexCase("codex expected mode is not used", "idle-after-turn", cxIdle, StateIdle, true, mode("anything")),
	codexCase("codex ended", "idle-after-turn", cxIdle, StateEnded, true, ended()),
	codexCase("codex ended beats a blocked report", "dialog-command-open", cxBlock, StateEnded, false, ended()),
	codexCase("codex owner draft at an idle prompt", "typed-two-rows", cxIdle, StateIdle, false),
	codexCase("codex pasted content at an idle prompt", "tall-composer-a", cxIdle, StateIdle, false),
	codexCase("codex faint hint without style data is text", "idle-after-turn", cxIdle, StateIdle, false, noFaint()),
	codexCase("codex start animation, no report yet", "start-animation", none, StateUnknown, true),
}

func TestClassifyTable(t *testing.T) {
	for _, tc := range tableCases {
		t.Run(tc.name, func(t *testing.T) {
			version := claudeVer
			if tc.runtime == "codex" {
				version = codexVer
			}
			rec := loadFixture(t, tc.runtime, version, tc.fixture)
			in := Input{Screen: rec.snap, Report: tc.report}
			if tc.runtime == "claude" {
				in.ExpectedMode = "manual"
			}
			for _, o := range tc.with {
				o(&in)
			}
			cl := lookup(t, tc.runtime, version).Classify(in)
			if cl.State != tc.state {
				t.Errorf("state %q, want %q\nsignals: %+v", cl.State, tc.state, cl.Signals)
			}
			if cl.ComposerEmpty != tc.empty {
				t.Errorf("ComposerEmpty %v, want %v", cl.ComposerEmpty, tc.empty)
			}
			if cl.ComposerEmpty != cl.Screen.ComposerEmpty {
				t.Errorf("ComposerEmpty %v is not the screen's %v", cl.ComposerEmpty, cl.Screen.ComposerEmpty)
			}
			for src, want := range tc.says {
				got, ok := signal(cl, src)
				if !ok || got.Says != want {
					t.Errorf("signal %s = %+v (found %v), want it to say %q", src, got, ok, want)
				}
			}
			checkSignalShape(t, cl)
		})
	}
}

// checkSignalShape checks the fixed order of the signals.
func checkSignalShape(t *testing.T, cl Classification) {
	t.Helper()
	var got []string
	for _, s := range cl.Signals {
		got = append(got, s.Source)
		if s.Says == "" {
			t.Errorf("signal %s says nothing", s.Source)
		}
	}
	want := []string{SourceReport, SourceTitle, SourceScreen, SourceProcess}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("signal order %v, want %v", got, want)
	}
}

// A blocked report and a dialog are told apart in the signals.
func TestBlockedFromReportAndFromScreen(t *testing.T) {
	c := lookup(t, "claude", claudeVer)
	idle := loadFixture(t, "claude", claudeVer, "idle-after-turn")
	dlg := loadFixture(t, "claude", claudeVer, "dialog-write-open")

	fromReport := c.Classify(Input{Screen: idle.snap, Report: ccBlock, ExpectedMode: "manual"})
	fromScreen := c.Classify(Input{Screen: dlg.snap, Report: none, ExpectedMode: "manual"})
	both := c.Classify(Input{Screen: dlg.snap, Report: ccBlock, ExpectedMode: "manual"})
	for name, cl := range map[string]Classification{"report": fromReport, "screen": fromScreen, "both": both} {
		if cl.State != StateBlocked {
			t.Fatalf("%s: state %q", name, cl.State)
		}
	}
	rs, _ := signal(fromReport, SourceReport)
	ss, _ := signal(fromReport, SourceScreen)
	if rs.Says != "blocked" || ss.Says == "dialog" {
		t.Errorf("blocked from the report: report %+v, screen %+v", rs, ss)
	}
	rs, _ = signal(fromScreen, SourceReport)
	ss, _ = signal(fromScreen, SourceScreen)
	if rs.Says == "blocked" || ss.Says != "dialog" || !strings.Contains(ss.Detail, "Esc to cancel") {
		t.Errorf("blocked from the screen: report %+v, screen %+v", rs, ss)
	}
	rs, _ = signal(both, SourceReport)
	ss, _ = signal(both, SourceScreen)
	if rs.Says != "blocked" || ss.Says != "dialog" {
		t.Errorf("blocked from both: report %+v, screen %+v", rs, ss)
	}
}

// What an agent can shape never reaches a Signal as free text.
func TestSignalsCarryNoAgentText(t *testing.T) {
	c := lookup(t, "codex", codexVer)
	rec := loadFixture(t, "codex", codexVer, "idle-after-turn")
	hostile := "evil\x1b[31m\n" + strings.Repeat("x", 500)
	st := osc7501.Status{Has: true, Report: osc7501.Report{State: osc7501.StateIdle, App: hostile, Msg: "hostile msg", Title: "hostile title", Kind: "hostile kind"}}
	cl := c.Classify(Input{Screen: rec.snap, Report: st})
	if cl.State != StateUnknown {
		t.Fatalf("state %q", cl.State)
	}
	for _, s := range cl.Signals {
		for _, f := range []string{s.Source, s.Says, s.Detail} {
			if len(f) > 200 || strings.ContainsAny(f, "\x1b\n") || strings.Contains(f, "hostile") {
				t.Errorf("signal %+v carries agent text", s)
			}
		}
	}
}

// Report, Msg and Unknown are not compared by the package, so a status with
// extra fields classifies like one without.
func TestReportExtrasDoNotMatter(t *testing.T) {
	c := lookup(t, "codex", codexVer)
	rec := loadFixture(t, "codex", codexVer, "idle-after-turn")
	st := cxIdle
	st.Report.Msg, st.Report.Title, st.Report.Kind, st.Report.Unknown = "m", "t", "k", 3
	if got := c.Classify(Input{Screen: rec.snap, Report: st}).State; got != StateIdle {
		t.Errorf("state %q, want idle", got)
	}
}

func TestClassifyIsRepeatable(t *testing.T) {
	c := lookup(t, "claude", claudeVer)
	rec := loadFixture(t, "claude", claudeVer, "idle-after-turn")
	in := Input{Screen: rec.snap, Report: ccIdle, ExpectedMode: "manual"}
	a, b := c.Classify(in), c.Classify(in)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two classifications of one input differ:\n%+v\n%+v", a, b)
	}
}

// Hostile snapshots: nothing panics and nothing is idle.
func TestHostileInput(t *testing.T) {
	huge := strings.Repeat("x", 1<<20)
	snaps := []Input{
		{},
		{Screen: bareScreen(huge, 80, 24)},
		{Screen: bareScreen("\x00\xff\xfe", 80, 24)},
		{Screen: bareScreen("a\nb\nc", -1, -1)},
		{Screen: bareScreen(strings.Repeat("\n", 5000), 1<<20, 1<<20)},
	}
	for _, rt := range []struct{ name, ver string }{{"claude", claudeVer}, {"codex", codexVer}} {
		c := lookup(t, rt.name, rt.ver)
		for i, in := range snaps {
			in.ExpectedMode = "manual"
			in.Report = status(osc7501.StateIdle, appOf(rt.name))
			in.Screen.Title = huge
			if got := c.Classify(in).State; got != StateUnknown {
				t.Errorf("%s hostile input %d: state %q, want unknown", rt.name, i, got)
			}
		}
	}
}
