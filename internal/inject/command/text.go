package command

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// headerMarker is what a header line starts with and what no body line may
// start with.
const headerMarker = "[ataniq-msg"

// HeaderLine returns the header line of a message, without the newline:
//
//	[ataniq-msg id=<id> from=<from> kind=<kind>]
//
// The fence checks that the composer holds this line before it presses Enter.
// The id and kind must match ValidName and the sender must be valid; admission
// guarantees that for every command, and HeaderLine does not check it again.
func HeaderLine(id string, from Sender, kind string) string {
	return headerMarker + " id=" + id + " from=" + from.String() + " kind=" + kind + "]"
}

// RenderText returns the text to deliver: the header line, a newline and the
// body. It is the text a Dispatch carries. Its first line is the only line
// that starts with the header marker, which CheckBody guarantees for a body
// that passes it.
func RenderText(id string, from Sender, kind, body string) string {
	return HeaderLine(id, from, kind) + "\n" + body
}

// CheckBody applies the body rules of admission and returns the reason it is
// refused, with a short detail for a log, or an empty reason and detail when
// the body passes. The rules, in order:
//
//   - the body is valid UTF-8 (ReasonInvalidUTF8);
//   - every rune is a newline or satisfies unicode.IsPrint (ReasonBadCharacter).
//     That is letters, marks, numbers, punctuation, symbols and the ASCII
//     space. ESC (so the paste-end sequence too), every other C0 and C1
//     control, DEL, CR and tab are refused, and so are the Unicode line and
//     paragraph separators, no-break and other non-ASCII spaces, and the
//     bidirectional and other format controls, because IsPrint refuses them;
//   - the body has something besides spaces and newlines (ReasonEmptyBody);
//   - no line, after its leading blanks, starts with "[ataniq-msg", compared
//     without regard to ASCII case (ReasonForgedHeader). A body is split into
//     lines at newlines only, and a newline is the only line break that
//     passes the character rule.
//
// CheckBody does not check the size; Submit does, on the whole text.
func CheckBody(body string) (Reason, string) {
	if !utf8.ValidString(body) {
		return ReasonInvalidUTF8, "body is not valid UTF-8"
	}
	line := 1
	for off, r := range body {
		if r == '\n' {
			line++
			continue
		}
		if !unicode.IsPrint(r) {
			return ReasonBadCharacter, fmt.Sprintf("line %d byte %d: U+%04X is not printable", line, off, r)
		}
	}
	if strings.Trim(body, " \n") == "" {
		return ReasonEmptyBody, "body has no text"
	}
	for i, ln := range strings.Split(body, "\n") {
		if startsWithMarker(ln) {
			return ReasonForgedHeader, fmt.Sprintf("line %d starts with %s", i+1, headerMarker)
		}
	}
	return "", ""
}

// startsWithMarker reports whether a line, after leading blanks, starts with
// the header marker, ignoring ASCII case.
func startsWithMarker(line string) bool {
	line = strings.TrimLeftFunc(line, unicode.IsSpace)
	if len(line) < len(headerMarker) {
		return false
	}
	head := line[:len(headerMarker)]
	return asciiOnly(head) && strings.EqualFold(head, headerMarker)
}

func asciiOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// safeField makes text from a hostile source fit for a journal field: invalid
// UTF-8 and non-printable runes other than newline become U+FFFD, and the
// result is at most max bytes, cut at a rune boundary.
func safeField(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || (r != '\n' && !unicode.IsPrint(r)) {
			r = '\ufffd'
		}
		if b.Len()+utf8.RuneLen(r) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
