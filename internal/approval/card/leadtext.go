package card

import (
	"fmt"
	"io"
	"strings"

	"ataniq/internal/exchange/leadtext"
)

// LeadText is text that a lead wrote, or that may hold bytes a lead wrote. It
// keeps the raw bytes and has no exported field, so the only way to get text
// out of it is [LeadText.Line], which escapes it with the leadtext package.
//
// Printing a LeadText with the fmt package, or writing it as JSON, gives the
// escaped text as well, never the raw bytes. The bytes are kept behind a
// pointer, so that a printer that reads unexported fields, as the fmt
// package does for a verb that does not fit, finds an address and no text.
// For that reason two values are compared with [LeadText.Equal], not with
// the == operator.
//
// Escaped text is still the lead's: a surface draws [leadtext.Label] near it
// and never puts it where a reader would take it for the host's own words.
type LeadText struct {
	raw *string
}

// NewLeadText wraps raw bytes that came from a lead. Empty text is the zero
// LeadText.
func NewLeadText(raw string) LeadText {
	if raw == "" {
		return LeadText{}
	}
	return LeadText{raw: &raw}
}

// bytes returns the raw bytes. It is not exported: outside this package the
// bytes leave a LeadText only escaped.
func (t LeadText) bytes() string {
	if t.raw == nil {
		return ""
	}
	return *t.raw
}

// Equal reports whether t and other hold the same bytes.
func (t LeadText) Equal(other LeadText) bool {
	return t.bytes() == other.bytes()
}

// Line returns the text escaped as one line, cut to at most max bytes. It
// never splits an escape or a character. The second result says whether
// anything was cut; a surface then says so beside the text.
func (t LeadText) Line(max int) (text string, cut bool) {
	return leadtext.Line(t.bytes(), max)
}

// Len returns the number of raw bytes. The escaped text may be longer.
func (t LeadText) Len() int {
	return len(t.bytes())
}

// Lines splits the text at its line feeds, for a block of several lines such
// as the plan or the note. A line feed at the very end starts no further
// line, and empty text has no line at all. Every other byte stays in its
// line, a carriage return included, and is escaped when the line is drawn.
func (t LeadText) Lines() []LeadText {
	raw := t.bytes()
	if raw == "" {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	lines := make([]LeadText, len(parts))
	for i, part := range parts {
		lines[i] = NewLeadText(part)
	}
	return lines
}

// Format writes the escaped text for every verb of the fmt package, so that
// printing a card, or a value that holds one, never writes a raw lead byte.
func (t LeadText) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, leadtext.Escape(t.bytes()))
}

// MarshalText returns the escaped text, so that a card written as JSON or
// as text holds no raw lead byte either.
func (t LeadText) MarshalText() ([]byte, error) {
	return []byte(leadtext.Escape(t.bytes())), nil
}

// leadTexts wraps each text of a record's list.
func leadTexts[T ~string](in []T) []LeadText {
	out := make([]LeadText, len(in))
	for i, s := range in {
		out[i] = NewLeadText(string(s))
	}
	return out
}

// hostText reports whether s can stand on the model as a plain string: the
// escaping would change nothing in it. That rules out control characters,
// bytes that are not UTF-8, the backslash, direction overrides and invisible
// characters.
func hostText(s string) bool {
	return leadtext.Escape(s) == s
}
