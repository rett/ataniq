package screen

import "strings"

// codex reads the screen of codex 0.169.
//
// Layout, from the recorded screens: the composer is a prompt row that starts
// with "› ", followed by continuation rows indented by two blanks, and below
// it, after a blank row, the status rows (model and folder, shortcut hint).
// The composer can be tall: a pasted text of twenty lines shows all of them
// (21 rows in the recorded screens), so it is found from the bottom and not
// from a fixed window: skip the status rows, then walk up through the
// continuation rows to the prompt row. The transcript above echoes the
// owner's earlier prompts with the same "› " start, but the walk stops at the
// first prompt row. A blank row inside the walk, a row that is neither a
// continuation nor the prompt, or the top of the screen before a prompt row,
// make the screen unknown. An open dialog has a numbered list whose selected
// row starts with "› " too ("› 1. Yes, proceed (y)"), which is not a composer.
//
// An empty composer shows the hint "Ask Codex to do anything" in faint style
// with the cursor on its first character; typed text is drawn plain with the
// cursor after it. The characters alone do not tell the two apart, so the
// reading uses Snapshot.Faint, and it accepts only the recorded hint text:
// every recorded codex hint is exactly this one, and faint style alone is not
// enough to call other text a hint.
type codex struct{}

// maxCodexBelow is the most rows that may follow the composer: the blank row
// and the status rows.
const maxCodexBelow = 4

// codexHint is the hint of an empty codex composer, as recorded (eight screens
// in the fixtures, and every codex screen of the evidence that shows one).
const codexHint = "Ask Codex to do anything"

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

// codexComposer finds the composer from the bottom of the screen. It returns
// the first and the last row of the composer, or -1 and the reason the screen
// is unknown.
func codexComposer(g *grid) (prompt, end int, why string) {
	// The status rows are the rows after the last blank row, each indented
	// by two blanks at least; at most maxCodexBelow rows, that blank row
	// included, follow the composer.
	sep := -1
	for i := g.last; i >= 0 && g.last-i < maxCodexBelow; i-- {
		if blank(g.rows[i]) {
			sep = i
			break
		}
		if leadingSpaces(g.rows[i]) < 2 {
			break
		}
	}
	if sep < 0 {
		return -1, -1, "no blank row and indented status rows below the composer"
	}
	end = sep - 1
	// Walk up through the continuation rows to the prompt row. The walk is
	// bounded by the height of the screen.
	for i := end; i >= 0; i-- {
		rs := trimRight(g.rows[i])
		switch {
		case len(rs) == 0:
			return -1, -1, "blank row inside the composer"
		case isCodexPrompt(rs):
			if choiceLine(rs, '›') {
				return -1, -1, "composer prompt row is a choice"
			}
			return i, end, ""
		case leadingSpaces(rs) < 2:
			return -1, -1, "no composer prompt row above the status rows"
		}
	}
	return -1, -1, "no composer prompt row above the status rows"
}

// isCodexPrompt reports whether a row (trailing blanks removed) is the prompt
// row: "›" alone, or "› " and text.
func isCodexPrompt(rs []rune) bool {
	return (len(rs) == 1 && rs[0] == '›') || hasPrefix(rs, "› ")
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
	prompt, end, why := codexComposer(g)
	if prompt < 0 {
		return unknown(why)
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
	case len(rows) == 1 && text == "" && emptyCursor(s, prompt):
		r.ComposerEmpty = true
	case len(rows) == 1 && text == codexHint && hintOnly(s, prompt, first, 2):
		r.ComposerEmpty = true
		r.Placeholder = true
	default:
		r.ComposerText = text
	}
	r.PastedMarker = strings.Contains(r.ComposerText, "[Pasted Content")
	return r
}
