// Package leadtext makes text that a lead wrote safe to show to the owner.
//
// A lead is a model session. Anything it produced is untrusted: ref names,
// paths, commit subjects, author names, diff content and command output.
// Owner-side code must not print that text as it is. The danger is terminal
// control sequences, text that imitates the tool's own output lines,
// bidirectional overrides and invisible characters. This package rewrites such
// text so that every dangerous byte or rune becomes visible escape text, and
// the result can be turned back into the original with Unescape.
//
// Escape and Line work on one line. Writer wraps an io.Writer and cleans a
// stream of many lines, such as a diff or command output. Label names what the
// text is. Callers show it next to every block of escaped text.
//
// The package does only what its name says. It does no I/O of its own beyond
// the writer a caller hands to NewWriter. It does not decide what to show, it
// does not prefix or indent lines, and it does not make the text true. Escaped
// text is still the lead's text: a lead can write a sentence that reads as an
// instruction or as a status line, and that stays readable. Callers must keep
// the label near it and must not put escaped text where a reader would take it
// for the tool's own words.
package leadtext
