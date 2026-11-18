package terminalconsole

// A labelled view. With Options.Label set the cockpit is not the fixture: it
// shows what its source gives under that label, calls nothing a fixture and
// replays no recording, so it has no session terminal. Every control stays
// unavailable with its reason, as in the fixture. A source that has nothing
// to show says why in Projection.Problem, and the cockpit draws that and the
// next step where the projects would be.

// labelled reports whether the cockpit shows a labelled view and not the
// fixture.
func (m *Model) labelled() bool { return m.label != "" && !m.live.on() }

// noSessionText says that a labelled view has no session terminal.
const noSessionText = "This view has no session terminal"

// viewLabel is the label of a labelled view.
func (m *Model) viewLabel() string { return m.label }

// problemLines returns the lines shown in place of the projects when the
// source has a problem: the problem and its next step. They are wrapped to
// width; a width of zero leaves them whole.
func (m *Model) problemLines(width int) []string {
	parts := []string{m.problem}
	if m.nextStep != "" {
		parts = append(parts, "", "Next step: "+m.nextStep)
	}
	if width <= 0 {
		return parts
	}
	var out []string
	for _, s := range parts {
		if s == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapLines(s, width)...)
	}
	return out
}

// leadText is a lead as the tree and the lead list show it: its name, and
// its state when it has one.
func leadText(l Lead) string {
	if l.State == "" {
		return l.Name
	}
	return l.Name + " · " + l.State
}

// viewHelpLines returns the help of a labelled view.
func (m *Model) viewHelpLines(width int) (string, []line) {
	var out []line
	add := func(k lineKind, s string) {
		for _, w := range wrapLines(s, width) {
			out = append(out, line{w, k})
		}
	}
	add(lnHead, "Console focus")
	for _, s := range []string{
		"Tab, Shift+Tab: next or previous pane",
		"Up, Down, j, k: move in the pane",
		"Enter: select in the tree",
		"a: full attach (unavailable)",
		"Ctrl+K: actions, all unavailable with their reasons",
		"?: this help   Esc: close   q, Ctrl+C: quit",
	} {
		add(lnText, s)
	}
	add(lnHead, "This view")
	add(lnText, "This is "+m.label+". "+noSessionText+", and nothing typed here reaches a process.")
	return "HELP", out
}
