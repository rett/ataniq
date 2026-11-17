package screen

import "strings"

// codex reads the screen of codex 0.169.
//
// Layout, from the recorded screens: the composer is a prompt row that starts
// with "› ", followed by continuation rows indented by two blanks, and below
// it, after a blank row, the status rows (model and folder, shortcut hint).
// The transcript above echoes the owner's earlier prompts with the same "› "
// start, so the composer is the last such row of the bottom rows, and it must
// be near the bottom. An open dialog has a numbered list whose selected row
// starts with "› " too ("› 1. Yes, proceed (y)"), which is not a composer.
//
// An empty composer shows the hint "Ask Codex to do anything" in faint style
// with the cursor on its first character; typed text is drawn plain with the
// cursor after it. The characters alone do not tell the two apart, so the
// reading uses Snapshot.Faint.
type codex struct{}

// maxCodexBelow is the most rows that may follow the composer.
const maxCodexBelow = 4

// codexMarks finds the marks of an open dialog in the bottom rows. The text
// marks count at the start of a row after at most two blanks; the choice list
// counts at the start of a row.
func codexMarks(g *grid) []string {
	var marks []string
	for i := g.windowStart(); i <= g.last; i++ {
		rs := trimRight(g.rows[i])
		lead := leadingSpaces(rs)
		if lead > 2 {
			continue
		}
		body := rs[lead:]
		if hasPrefix(body, "Press enter to confirm") {
			marks = addMark(marks, "Press enter to confirm")
		}
		if hasPrefix(body, "Would you like to") {
			marks = addMark(marks, "Would you like to")
		}
		if lead == 0 && choiceLine(body, '›') {
			marks = addMark(marks, "› N.")
		}
	}
	return marks
}

func (codex) Read(s Snapshot) Reading {
	g, note := parse(s)
	if g == nil {
		return unknown(note)
	}
	if marks := codexMarks(g); len(marks) > 0 {
		return Reading{Known: true, Dialog: true, DialogMarks: marks}
	}
	if !s.AltScreen {
		return unknown("not on the alternate screen")
	}
	prompt := -1
	for i := g.last; i >= g.windowStart(); i-- {
		rs := trimRight(g.rows[i])
		if (len(rs) == 1 && rs[0] == '›') || hasPrefix(rs, "› ") {
			prompt = i
			break
		}
	}
	if prompt < 0 {
		return unknown("no composer prompt row in the bottom rows")
	}
	end := prompt
	for i := prompt + 1; i <= g.last; i++ {
		rs := g.rows[i]
		if leadingSpaces(rs) < 2 || blank(rs) {
			break
		}
		end = i
	}
	if g.last == end {
		return unknown("no status row below the composer")
	}
	if g.last-end > maxCodexBelow {
		return unknown("composer is not at the bottom of the screen")
	}
	rows := g.rows[prompt : end+1]
	first := trimRight(rows[0])

	r := Reading{
		Known:            true,
		Composer:         true,
		CursorInComposer: cursorIn(s, prompt, end),
	}
	for i := g.windowStart(); i <= g.last; i++ {
		if (i < prompt || i > end) && strings.Contains(string(g.rows[i]), "esc to interrupt)") {
			r.Working = true
		}
	}
	text := composerText(rows, 2)
	switch {
	case len(rows) == 1 && text == "":
		r.ComposerEmpty = true
	case len(rows) == 1 && hintOnly(s, prompt, first, 2):
		r.ComposerEmpty = true
		r.Placeholder = true
	default:
		r.ComposerText = text
	}
	r.PastedMarker = strings.Contains(r.ComposerText, "[Pasted Content")
	return r
}
