package idle

import (
	"strings"

	"ataniq/internal/inject/osc7501"
	"ataniq/internal/inject/screen"
	"ataniq/internal/inject/title"
)

// State is what a session is doing.
type State string

// The five states of a classification.
const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateBlocked State = "blocked"
	StateEnded   State = "ended"
	StateUnknown State = "unknown"
)

// The sources of a Signal.
const (
	SourceReport  = "report"
	SourceTitle   = "title"
	SourceScreen  = "screen"
	SourceProcess = "process"
)

// maxDetail bounds the text copied from outside into a Signal.
const maxDetail = 64

// Input is what the holder knows about one session at one moment.
type Input struct {
	// Report is the last OSC 7501 status of the agent.
	Report osc7501.Status
	// Screen is the snapshot of the agent's screen. Its Title is the window
	// title.
	Screen screen.Snapshot
	// Ended is true when the agent's process has ended.
	Ended bool
	// ExpectedMode is the permission mode the footer of a claude screen must
	// show for the session to be idle. The caller says which mode it
	// expects; an empty ExpectedMode never matches. It is not used for
	// codex.
	ExpectedMode string
}

// Signal is one reading and what it said. The order of the signals of a
// Classification is fixed: report, title, screen, process.
type Signal struct {
	// Source is one of SourceReport, SourceTitle, SourceScreen and
	// SourceProcess.
	Source string
	// Says is a short fixed word for what the source said.
	Says string
	// Detail adds the particulars. It holds fixed text and at most a short
	// token of the agent's own.
	Detail string
}

// Classification is the verdict and the readings behind it.
type Classification struct {
	// State is idle only when every signal required for the runtime says so.
	State State
	// ComposerEmpty is copied from the screen reading. It is separate from
	// State: a composer holding the owner's draft at an otherwise idle
	// prompt is idle with ComposerEmpty false. Test this field, never the
	// text of the composer.
	ComposerEmpty bool
	// Screen is what the recogniser read.
	Screen screen.Reading
	// Title is what the title rules read.
	Title title.Reading
	// Signals lists every signal that was read, in a fixed order.
	Signals []Signal
}

// Entry is one row of the tested table.
type Entry struct {
	// Runtime is "claude" or "codex".
	Runtime string
	// Version is the version the row was recorded from.
	Version string
	// Aliases are other spellings of the version that Lookup accepts.
	Aliases []string
	// App is the app name the runtime's OSC 7501 reports carry.
	App string
}

// table is the one table of tested runtimes and versions. A version enters it
// only with its recogniser fixtures and its title rules.
var table = []Entry{
	{Runtime: "claude", Version: "2.1.325", App: "claude-code"},
	{Runtime: "codex", Version: "0.169", Aliases: []string{"0.169.0"}, App: "codex"},
}

// Tested returns the table: every runtime and version that has a classifier.
// The result is a copy.
func Tested() []Entry {
	out := make([]Entry, len(table))
	for i, e := range table {
		e.Aliases = append([]string(nil), e.Aliases...)
		out[i] = e
	}
	return out
}

// Classifier classifies the sessions of one runtime at one version. The zero
// Classifier has no rules and classifies everything as unknown; get one from
// Lookup.
type Classifier struct {
	runtime string
	app     string
	rec     screen.Recogniser
	rules   title.Rules
}

// Lookup returns the classifier for a runtime and version from the table.
// Anything not in the table, and any empty value, is not found.
func Lookup(runtime, version string) (Classifier, bool) {
	for _, e := range table {
		if e.Runtime != runtime || !e.has(version) {
			continue
		}
		rec, ok := screen.For(runtime, version)
		if !ok {
			return Classifier{}, false
		}
		rules, ok := title.For(runtime, version)
		if !ok {
			return Classifier{}, false
		}
		return Classifier{runtime: runtime, app: e.App, rec: rec, rules: rules}, true
	}
	return Classifier{}, false
}

func (e Entry) has(version string) bool {
	if version == "" {
		return false
	}
	if version == e.Version {
		return true
	}
	for _, a := range e.Aliases {
		if version == a {
			return true
		}
	}
	return false
}

// Runtime returns the runtime of the classifier, or an empty string for the
// zero Classifier.
func (c Classifier) Runtime() string { return c.runtime }

// reportKind is how the last report is usable.
type reportKind int

const (
	reportNone       reportKind = iota // no report yet
	reportStale                        // an unreadable payload came after the last report
	reportOtherApp                     // the report is from another app
	reportUnreadable                   // the state of the report is not one of the six
	reportUsable                       // a report of this runtime's app
)

// report is the last status as the rules use it.
type report struct {
	kind  reportKind
	state osc7501.State
	app   string
}

func readReport(st osc7501.Status, app string) report {
	switch {
	case st.Stale:
		return report{kind: reportStale}
	case !st.Has:
		return report{kind: reportNone}
	case st.Report.App != app:
		return report{kind: reportOtherApp, app: st.Report.App}
	case !st.Report.State.Valid():
		return report{kind: reportUnreadable}
	}
	return report{kind: reportUsable, state: st.Report.State, app: st.Report.App}
}

// says reports whether the report is usable and has this state.
func (r report) says(states ...osc7501.State) bool {
	if r.kind != reportUsable {
		return false
	}
	for _, s := range states {
		if r.state == s {
			return true
		}
	}
	return false
}

// Classify joins the report, the title and the screen of one session into a
// classification. It never panics and keeps no state.
func (c Classifier) Classify(in Input) Classification {
	if c.rec == nil {
		return Classification{State: StateUnknown}
	}
	scr := c.rec.Read(in.Screen)
	tit := c.rules.Read(in.Screen.Title)
	rep := readReport(in.Report, c.app)
	return Classification{
		State:         c.decide(in, scr, tit, rep),
		ComposerEmpty: scr.ComposerEmpty,
		Screen:        scr,
		Title:         tit,
		Signals:       c.signals(in, scr, tit, rep),
	}
}

// onScreen reports whether the screen is one the rules may read: the
// recogniser knows it and it is on the alternate screen, where every recorded
// composer and dialog is.
func onScreen(in Input, scr screen.Reading) bool {
	return scr.Known && in.Screen.AltScreen
}

func (c Classifier) decide(in Input, scr screen.Reading, tit title.Reading, rep report) State {
	if in.Ended {
		return StateEnded
	}
	// Blocked wins over every idle signal (Q13: production vetoes on a
	// blocked report before it pastes).
	if rep.says(osc7501.StateBlocked) || (onScreen(in, scr) && scr.Dialog) {
		return StateBlocked
	}
	// A report that is not the agent's current state decides nothing.
	if rep.kind == reportStale || rep.kind == reportOtherApp || rep.kind == reportUnreadable {
		return StateUnknown
	}
	if !onScreen(in, scr) {
		return StateUnknown
	}
	switch c.runtime {
	case "codex":
		return decideCodex(scr, tit, rep)
	case "claude":
		return decideClaude(in, scr, tit, rep)
	}
	return StateUnknown
}

// decideCodex: the report is required, and the second signal must agree with
// an idle report.
func decideCodex(scr screen.Reading, tit title.Reading, rep report) State {
	switch {
	case rep.says(osc7501.StateWorking):
		return StateWorking
	case rep.says(osc7501.StateIdle, osc7501.StateDone):
	default:
		// None, clear or error.
		return StateUnknown
	}
	if tit.Busy || tit.ActionRequired || !scr.Composer || scr.Dialog ||
		scr.Working || scr.BackgroundTask {
		// The report says idle and another signal says otherwise (I-12):
		// the signals disagree, so the answer is unknown.
		return StateUnknown
	}
	return StateIdle
}

// claudePasteHidden reports whether the reading is the collapsed-paste footer,
// where the screen shows neither the working line, nor the shell count, nor a
// mode.
func claudePasteHidden(scr screen.Reading) bool {
	return scr.PastedMarker && scr.Mode == ""
}

// decideClaude: the recogniser is required, the report and the title can only
// veto.
func decideClaude(in Input, scr screen.Reading, tit title.Reading, rep report) State {
	switch {
	case rep.says(osc7501.StateWorking):
		return StateWorking
	case rep.says(osc7501.StateError, osc7501.StateClear):
		return StateUnknown
	}
	if tit.Busy || scr.Working || scr.BackgroundTask {
		return StateWorking
	}
	if !scr.Composer || scr.Dialog || !scr.CursorInComposer {
		return StateUnknown
	}
	if claudePasteHidden(scr) {
		// The state is not shown on screen: it comes from the report.
		if !rep.says(osc7501.StateIdle, osc7501.StateDone) {
			return StateUnknown
		}
		return StateIdle
	}
	if in.ExpectedMode == "" || scr.Mode != in.ExpectedMode {
		return StateUnknown
	}
	return StateIdle
}

// signals lists every signal that was read, in the fixed order report, title,
// screen, process.
func (c Classifier) signals(in Input, scr screen.Reading, tit title.Reading, rep report) []Signal {
	out := make([]Signal, 0, 4)

	rs := Signal{Source: SourceReport}
	switch rep.kind {
	case reportNone:
		rs.Says = "none"
	case reportStale:
		rs.Says = "stale"
		rs.Detail = "an unreadable report came after the last valid one"
	case reportOtherApp:
		rs.Says = "other app"
		rs.Detail = "app " + clip(rep.app) + ", not " + c.app
	case reportUnreadable:
		rs.Says = "unreadable"
		rs.Detail = "the state is not one of the six"
	default:
		rs.Says = string(rep.state)
		rs.Detail = "app " + rep.app
	}
	out = append(out, rs)

	ts := Signal{Source: SourceTitle, Says: "no mark"}
	switch {
	case tit.Busy && tit.ActionRequired:
		ts.Says = "busy, action required"
	case tit.Busy:
		ts.Says = "busy"
	case tit.ActionRequired:
		ts.Says = "action required"
	}
	if len(tit.Notes) > 0 {
		ts.Detail = tit.Notes[0]
	}
	out = append(out, ts)

	out = append(out, c.screenSignal(in, scr))

	ps := Signal{Source: SourceProcess, Says: "running"}
	if in.Ended {
		ps.Says = "ended"
	}
	return append(out, ps)
}

// screenSignal says what the screen showed, first match first: unknown, dialog,
// working, background task, composer.
func (c Classifier) screenSignal(in Input, scr screen.Reading) Signal {
	ss := Signal{Source: SourceScreen}
	switch {
	case !scr.Known:
		ss.Says = "unknown"
		if len(scr.Notes) > 0 {
			ss.Detail = scr.Notes[0]
		}
	case !in.Screen.AltScreen:
		ss.Says = "unknown"
		ss.Detail = "not on the alternate screen"
	case scr.Dialog:
		ss.Says = "dialog"
		ss.Detail = strings.Join(scr.DialogMarks, ", ")
	case scr.Working:
		ss.Says = "working"
	case scr.BackgroundTask:
		ss.Says = "background task"
	case !scr.Composer:
		ss.Says = "no composer"
	default:
		ss.Says = "composer"
		ss.Detail = c.composerDetail(in, scr)
	}
	return ss
}

// composerDetail describes the composer of a screen that shows one.
func (c Classifier) composerDetail(in Input, scr screen.Reading) string {
	var parts []string
	if scr.ComposerEmpty {
		parts = append(parts, "empty")
	} else {
		parts = append(parts, "holds text")
	}
	if !scr.CursorInComposer {
		parts = append(parts, "cursor outside")
	}
	if c.runtime == "claude" {
		switch {
		case claudePasteHidden(scr):
			parts = append(parts, "paste footer: state not shown")
		case in.ExpectedMode == "":
			parts = append(parts, "no expected mode")
		case scr.Mode != in.ExpectedMode:
			parts = append(parts, "mode "+clip(scr.Mode)+", not "+clip(in.ExpectedMode))
		default:
			parts = append(parts, "mode "+clip(scr.Mode))
		}
	}
	return strings.Join(parts, "; ")
}

// clip bounds text that comes from outside and drops anything that is not
// printable ASCII, so a Signal never carries text an agent could shape.
func clip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < maxDetail; i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		b.WriteByte(c)
	}
	if b.Len() == 0 {
		return `""`
	}
	return b.String()
}
