package screen

import (
	"regexp"
	"strings"
	"unicode"
)

// claude reads the screen of claude 2.1.325.
//
// Layout, from the recorded screens: the composer is a box between two
// full-width rules of '─' at the bottom. Its first row starts with the prompt
// glyph '❯' and a blank (a no-break space in the empty composer); further
// rows are indented by two blanks. The footer is the one row directly below
// the lower rule and the last row of the screen: the mode line, or, while a
// collapsed paste ("[Pasted text #1 +20 lines]") is in the composer, the hint
// "paste again to expand" and no mode. An open dialog has no
// composer box: it ends in a row that starts with "Esc to cancel".
type claude struct{}

// maxClaudeBox is the tallest composer box that is read.
const maxClaudeBox = 30

// claudeModes maps the first part of the footer to a short mode name. These
// are the footers seen in the recorded screens; any other is unknown.
var claudeModes = map[string]string{
	"⏸ manual mode on":                     "manual",
	"⏵⏵ auto mode on (shift+tab to cycle)": "auto",
}

// claudePasteFooter is the whole footer while a collapsed paste is in the
// composer: the hint replaces the mode line, so the footer shows no mode
// (row1/claude-s2 mark-017 and mark-018, row2/cfg4-B-claude-r2 dump-f-paste).
// It is accepted only together with the pasted-text marker in the composer.
const claudePasteFooter = "paste again to expand"

// claudeHints are the other parts of the footer seen in the recorded screens,
// each separated from the others by " · ".
var claudeHints = map[string]bool{
	"? for shortcuts":  true,
	"esc to interrupt": true,
	"← for agents":     true,
	"↓ to manage":      true,
}

var (
	claudeShells       = regexp.MustCompile(`^[1-9][0-9]* shells?$`)
	claudeShellsStatus = regexp.MustCompile(` · [1-9][0-9]* shells? still running$`)
)

// claudeMarks finds the marks of an open dialog in the bottom rows. A mark
// counts only at the start of a row, after at most one blank: dialogs draw
// their lines like that, while the transcript starts its rows with a glyph or
// indents them by two blanks.
func claudeMarks(g *grid) []string {
	var marks []string
	for i := g.windowStart(); i <= g.last; i++ {
		rs := trimRight(g.rows[i])
		lead := leadingSpaces(rs)
		if lead > 1 {
			continue
		}
		body := rs[lead:]
		for _, seg := range strings.Split(string(body), " · ") {
			if strings.HasPrefix(seg, "Esc to cancel") {
				marks = addMark(marks, "Esc to cancel")
			}
		}
		if hasPrefix(body, "Do you want to") {
			marks = addMark(marks, "Do you want to")
		}
		if lead == 1 && choiceLine(body, '❯') {
			marks = addMark(marks, "❯ N.")
		}
	}
	return marks
}

// isClaudePasteFooter reports whether the footer row is the paste hint.
func isClaudePasteFooter(rs []rune) bool {
	rs = trimRight(rs)
	return leadingSpaces(rs) == 2 && string(rs[2:]) == claudePasteFooter
}

// parseClaudeFooter reads the footer row. It returns the mode, the number of
// background shells and whether the working hint is there; ok is false for
// any footer that is not one of the recorded kinds.
func parseClaudeFooter(rs []rune) (mode string, shells int, working, ok bool) {
	rs = trimRight(rs)
	if leadingSpaces(rs) != 2 {
		return "", 0, false, false
	}
	parts := strings.Split(string(rs[2:]), " · ")
	mode, ok = claudeModes[parts[0]]
	if !ok {
		return "", 0, false, false
	}
	for _, p := range parts[1:] {
		switch {
		case claudeHints[p]:
			if p == "esc to interrupt" {
				working = true
			}
		case claudeShells.MatchString(p):
			shells = 1
		default:
			return "", 0, false, false
		}
	}
	return mode, shells, working, true
}

func (claude) Read(s Snapshot) Reading {
	g, note := parse(s)
	if g == nil {
		return unknown(note)
	}
	if marks := claudeMarks(g); len(marks) > 0 {
		return Reading{Known: true, Dialog: true, DialogMarks: marks}
	}
	if !s.AltScreen {
		return unknown("not on the alternate screen")
	}
	lower := g.last - 1
	if lower < 1 || !g.isRule(lower) {
		return unknown("no composer box: no rule line directly above the last row")
	}
	upper := -1
	for i := lower - 1; i >= 0 && i >= lower-1-maxClaudeBox; i-- {
		if g.isRule(i) {
			upper = i
			break
		}
	}
	if upper < 0 {
		return unknown("no composer box: no upper rule line")
	}
	box := g.rows[upper+1 : lower]
	if len(box) == 0 {
		return unknown("composer box has no rows")
	}
	first := box[0]
	if len(first) == 0 || first[0] != '❯' || (len(first) > 1 && !unicode.IsSpace(first[1])) {
		return unknown("composer box has no prompt row")
	}
	pasteFooter := isClaudePasteFooter(g.rows[g.last])
	mode, shells, working := "", 0, false
	if !pasteFooter {
		var ok bool
		mode, shells, working, ok = parseClaudeFooter(g.rows[g.last])
		if !ok {
			return unknown("footer is not one of the recorded footers")
		}
	}

	r := Reading{
		Known:            true,
		Composer:         true,
		Mode:             mode,
		Working:          working,
		CursorInComposer: cursorIn(s, upper+1, lower-1),
		BackgroundTask:   shells > 0,
	}
	for i := g.windowStart(); i <= g.last; i++ {
		if claudeShellsStatus.MatchString(string(trimRight(g.rows[i]))) {
			r.BackgroundTask = true
		}
	}
	text := composerText(box, 2)
	switch {
	case len(box) == 1 && text == "" && emptyCursor(s, upper+1):
		r.ComposerEmpty = true
	case len(box) == 1 && hintOnly(s, upper+1, first, 2):
		r.ComposerEmpty = true
		r.Placeholder = true
	default:
		r.ComposerText = text
	}
	r.PastedMarker = strings.Contains(r.ComposerText, "[Pasted text #")
	if pasteFooter {
		if r.ComposerEmpty || !r.PastedMarker {
			return unknown("paste footer without a pasted-text marker in the composer")
		}
		r.StateHidden = true
		r.Notes = append(r.Notes, "paste footer: the working hint and the shell count are not shown, so Working and BackgroundTask are not observed; take the working state from the OSC 7501 report")
	}
	return r
}
