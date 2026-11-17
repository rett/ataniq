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
//   - the body does not contain the marker word "ataniq-msg" anywhere
//     (ReasonForgedHeader). The body is compared in a folded form: every
//     letter is lower-cased and every rune that is not a letter or a decimal
//     digit is removed, newlines and all, and the fold of the body must not
//     contain "ataniqmsg". So "ataniq-msg", "ATANIQ MSG", "ataniq\u2011msg",
//     "[ataniq_msg", a marker split by a combining mark or by a line break, and
//     a marker after any run of blank-looking runes all fold to "ataniqmsg"
//     and are refused. That is stricter than a rule about the start of a
//     line, on purpose: what looks blank or like a bracket on a screen is not
//     decided by the code points. The rule is about the body only: the id
//     and kind of a header are checked by the name grammar, not by this rule.
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
	if containsMarker(body) {
		return ReasonForgedHeader, "body contains " + markerWord
	}
	return "", ""
}

// markerWord is the marker word the body rule looks for, as it is written in
// a header.
const markerWord = "ataniq-msg"

// foldedMarker is markerWord after fold: lower case, letters and digits only.
const foldedMarker = "ataniqmsg"

// containsMarker reports whether the fold of s contains the folded marker.
// The fold keeps letters and decimal digits only, lower-cased, so every
// separator, mark, blank and format rune between the letters is dropped.
func containsMarker(s string) bool {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return strings.Contains(b.String(), foldedMarker)
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
