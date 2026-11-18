package consoleclient

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxInspect is the most bytes of a string that clean looks at, so that a
// very long string costs no more than a short one.
const maxInspect = 4096

// cutMark ends a string that clean cut.
const cutMark = "…"

// clean returns s made safe to show and at most MaxText bytes long. It drops
// every byte that is not valid UTF-8 and every character that is not a
// visible one: control characters (so no escape sequence survives as one),
// format characters (direction marks and overrides, zero-width characters),
// private-use and unassigned code points, and characters that print as
// nothing or as a blank. Every kind of space becomes one ASCII space, so a
// value stays on one line. A string that is cut ends in "…".
func clean(s string) string {
	var b strings.Builder
	cut := false
	for inspected := 0; len(s) > 0; {
		r, n := utf8.DecodeRuneInString(s)
		if inspected+n > maxInspect {
			cut = true
			break
		}
		s = s[n:]
		inspected += n
		if r == utf8.RuneError && n == 1 {
			continue
		}
		if unicode.IsSpace(r) {
			r, n = ' ', 1
		} else if !visible(r) {
			continue
		}
		if b.Len()+n > MaxText-len(cutMark) {
			cut = true
			break
		}
		b.WriteRune(r)
	}
	if cut {
		b.WriteString(cutMark)
	}
	return b.String()
}

// visible reports whether r is a character that shows as itself.
func visible(r rune) bool {
	if !unicode.IsPrint(r) {
		// Controls, format characters, separators, private use, surrogates
		// and unassigned code points.
		return false
	}
	if unicode.Is(unicode.Variation_Selector, r) || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) {
		// Printable by category, yet they print as nothing.
		return false
	}
	return r != 0x2800 // the blank braille pattern
}
