package seatapp

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text is the one function that renders text nobody on the owner's side
// wrote: a lead's note, a word from the daemon, later a message. It returns
// plain text, for a page template (which escapes it as HTML, so no markup and
// no link in it comes alive) or for a script's textContent.
//
//   - Control characters other than the line feed, bidirectional controls
//     and every other invisible format character, line and paragraph
//     separators, spaces other than the plain space, private-use and
//     non-characters, and characters that draw as blank are shown as a visible
//     escape: \u202e, or \U0001f600 above the first plane. A byte that is not
//     UTF-8 is shown as \xNN. The line feed is kept: the caller shows it with
//     white-space: pre-wrap. A backslash is not escaped, so text that spells
//     an escape is shown as it is spelt.
//   - Text of more than max characters is cut after max, and
//     "(N more characters not shown)" says how many were left out. The
//     characters are counted before escaping, so a cut never falls inside an
//     escape. A max of 0 or less shows none.
//
// Text makes no link: an address in the text is only text. Nothing else in
// the app shows such text, and this is the only function that does.
func Text(s string, max int) string {
	if max < 0 {
		max = 0
	}
	var b strings.Builder
	n := 0
	i := 0
	for i < len(s) && n < max {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case hidden(r):
			if r > 0xffff {
				fmt.Fprintf(&b, `\U%08x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
		i += size
		n++
	}
	if i < len(s) {
		left := utf8.RuneCountInString(s[i:])
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		if left == 1 {
			b.WriteString("(1 more character not shown)")
		} else {
			fmt.Fprintf(&b, "(%d more characters not shown)", left)
		}
	}
	return b.String()
}

// blankLooking are letters and marks that draw as nothing.
var blankLooking = map[rune]bool{
	0x034f: true, 0x115f: true, 0x1160: true, 0x17b4: true, 0x17b5: true,
	0x2800: true, 0x3164: true, 0xffa0: true,
}

// hidden reports whether Text shows r as an escape.
func hidden(r rune) bool {
	switch {
	case r == '\n' || r == ' ':
		return false
	case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zs, r),
		unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r), unicode.Is(unicode.Co, r),
		unicode.Is(unicode.Cs, r), blankLooking[r]:
		return true
	case r >= 0xfdd0 && r <= 0xfdef, r&0xfffe == 0xfffe:
		return true // non-characters
	}
	return false
}

// oneLine is Text for a value that is a single token: the line feed is
// escaped too.
func oneLine(s string, max int) string {
	return strings.ReplaceAll(Text(s, max), "\n", `\u000a`)
}
