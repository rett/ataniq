package title

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTitleBytes is the number of bytes of a title that Read looks at. It is
// the holder's title cap; a longer title is read up to this length.
const MaxTitleBytes = 256

// actionRequired is the text codex puts in its title while an approval prompt
// is open. The prompt blinks, so the leading mark alternates between "[ ! ]"
// and "[ . ]"; the words stay.
const actionRequired = "Action Required"

// claudeIdleGlyph is the glyph claude shows both when idle and while an
// approval prompt is open. It proves nothing and sets nothing.
const claudeIdleGlyph = '✳'

// Reading says what one title shows.
type Reading struct {
	// Busy is true when the title starts with a spinner. It is also true
	// when the first rune is a symbol that was not recorded, or is not valid
	// UTF-8 (see Notes), because when the rules cannot tell, the answer is
	// not idle. The zero Rules reads every title as busy.
	Busy bool
	// ActionRequired is true when the title says an approval prompt is open.
	// Only codex titles can set it.
	ActionRequired bool
	// Notes explains a reading that rests on a guess or on a cut title.
	// It is empty for a plain reading.
	Notes []string
}

// Rules reads titles for one runtime and version. The zero value has no
// rules and reads every title as busy, with a note; get a Rules from For.
type Rules struct {
	runtime string
	read    func(title string) Reading
}

// For returns the rules for a runtime and version. It knows claude 2.1.325,
// and codex as 0.169 or as 0.169.0, the spelling codex reports itself with;
// for anything else it returns false.
func For(runtime, version string) (Rules, bool) {
	switch {
	case runtime == "claude" && version == "2.1.325":
		return Rules{runtime: runtime, read: readClaude}, true
	case runtime == "codex" && (version == "0.169" || version == "0.169.0"):
		return Rules{runtime: runtime, read: readCodex}, true
	}
	return Rules{}, false
}

// Runtime returns the runtime these rules read, or an empty string for the
// zero Rules.
func (r Rules) Runtime() string { return r.runtime }

// Read says what a title shows. It looks at the first MaxTitleBytes bytes
// only and does not panic on any input. The zero Rules reads every title as
// busy, with a note.
func (r Rules) Read(title string) Reading {
	if r.read == nil {
		return Reading{Busy: true, Notes: []string{"no rules for this runtime and version: treated as busy, because the rules cannot tell"}}
	}
	var notes []string
	if len(title) > MaxTitleBytes {
		title = title[:MaxTitleBytes]
		notes = append(notes, "title longer than 256 bytes: only the first 256 were read")
	}
	rd := r.read(title)
	rd.Notes = append(notes, rd.Notes...)
	return rd
}

// readCodex reads a codex 0.169 title. While a turn runs the title starts
// with one braille pattern glyph (U+2800 to U+28FF), a spinner. While an
// approval prompt is open it contains "Action Required". A first rune that is
// a symbol other than a braille pattern, or is not valid UTF-8, was not
// recorded: the title is busy with a note, because when the rules cannot
// tell, the answer is not idle. Titles that start with a letter, a digit or
// "[", and the empty title, are not busy.
func readCodex(title string) Reading {
	var rd Reading
	r, size := utf8.DecodeRuneInString(title)
	switch {
	case title == "":
	case isBraille(r):
		rd.Busy = true
	case r == utf8.RuneError && size <= 1:
		rd.Busy = true
		rd.Notes = append(rd.Notes, "title does not start with valid text: treated as busy, because the rules cannot tell")
	case unicode.IsSymbol(r):
		rd.Busy = true
		rd.Notes = append(rd.Notes, "first glyph was not recorded: treated as busy, because when the rules cannot tell, the answer is not idle")
	}
	if strings.Contains(title, actionRequired) {
		rd.ActionRequired = true
	}
	return rd
}

// readClaude reads a claude 2.1.325 title. The recorded spinner glyphs are
// U+25D0 and U+25D1; the title is the glyph, a space and a topic. The glyph
// U+2733 shows when idle and at an approval prompt, so it sets nothing.
// ActionRequired is never set: claude gives no title signal for a prompt.
func readClaude(title string) Reading {
	var rd Reading
	if title == "" {
		rd.Notes = append(rd.Notes, "title is empty: it shows neither idle nor busy")
		return rd
	}
	r, size := utf8.DecodeRuneInString(title)
	switch {
	case r == claudeIdleGlyph:
		// Idle or an approval prompt: the title cannot tell which.
	case r == utf8.RuneError && size <= 1:
		rd.Busy = true
		rd.Notes = append(rd.Notes, "title does not start with valid text: treated as busy, because the rules cannot tell")
	case isClaudeSpinner(r):
		rd.Busy = true
	case unicode.IsSymbol(r):
		rd.Busy = true
		rd.Notes = append(rd.Notes, "first glyph was not recorded: treated as busy, because when the rules cannot tell, the answer is not idle")
	default:
		rd.Notes = append(rd.Notes, "title has no leading glyph: it shows neither idle nor busy")
	}
	return rd
}

// isClaudeSpinner reports the spinner glyphs recorded in claude 2.1.325
// titles: the half-filled circles U+25D0 and U+25D1.
func isClaudeSpinner(r rune) bool { return r == '◐' || r == '◑' }

// isBraille reports a braille pattern rune, U+2800 to U+28FF.
func isBraille(r rune) bool { return r >= 0x2800 && r <= 0x28FF }
