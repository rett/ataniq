package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake"
	"ataniq/internal/mirror"
)

// Rule of the output: a line that starts in the first column is the
// command's own text. Lead text is escaped with leadtext, is written on a
// line that starts with two spaces, and sits under a heading that names
// leadtext.Label. A line of lead text therefore can never be taken for one
// of the command's own headings.

// indent starts every line of lead text.
const indent = "  "

// heading returns a heading for a section that holds lead text.
func heading(title string) string {
	return title + " (" + leadtext.Label + "):"
}

// quote returns s escaped and in double quotes, for text that is not trusted
// but is not a lead's, such as an argument the owner typed.
func quote(s string) string {
	return `"` + leadtext.Escape(s) + `"`
}

// safeText returns s as one line that holds no control byte. Text that the
// packages already escaped, such as a ref name inside an error, is left as it
// is: it is escaped again only when it holds something that Escape changes
// besides the backslash.
func safeText(s string) string {
	plain := strings.ReplaceAll(s, `\`, "")
	if leadtext.Escape(plain) == plain {
		return s
	}
	return leadtext.Escape(s)
}

// capLine escapes s and cuts it to max bytes.
func capLine(s string, max int) string {
	out, cut := leadtext.Line(s, max)
	if cut {
		out += " [cut]"
	}
	return out
}

// printer writes lines and remembers the first write error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) f(format string, args ...any) {
	if p.err != nil {
		return
	}
	_, p.err = fmt.Fprintf(p.w, format+"\n", args...)
}

// utc formats a time as the output shows it.
func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// fail prints an error to standard error and returns the exit status for it.
func (e *env) fail(sub string, err error) int {
	code := classify(err)
	prefix := program
	if sub != "" {
		prefix += " " + sub
	}
	fmt.Fprintf(e.stderr, "%s: %s\n", prefix, safeText(err.Error()))
	var ue *usageError
	if errors.As(err, &ue) && sub != "" {
		fmt.Fprintf(e.stderr, "Run '%s %s -h' for the usage.\n", program, sub)
	}
	if hint := hintFor(sub, err); hint != "" {
		fmt.Fprintf(e.stderr, "%s\n", hint)
	}
	return code
}

// classify returns the exit status for an error.
func classify(err error) int {
	var (
		ue  *usageError
		le  *mirror.LimitError
		sg  *intake.SignOffError
		nff *intake.NotFastForwardError
		re  *refusalError
	)
	switch {
	case errors.As(err, &ue):
		return exitUsage
	case errors.As(err, &le), errors.As(err, &sg), errors.As(err, &nff),
		errors.Is(err, mirror.ErrBusy),
		errors.Is(err, mirror.ErrHostileTree),
		errors.Is(err, mirror.ErrRefused),
		errors.Is(err, mirror.ErrFetchTimeout),
		errors.Is(err, mirror.ErrNotMirror),
		errors.Is(err, mirror.ErrNoRef),
		errors.Is(err, mirror.ErrTooManyRefs),
		errors.Is(err, intake.ErrNoRef),
		errors.Is(err, intake.ErrLimit),
		errors.Is(err, intake.ErrNotVerified),
		errors.Is(err, intake.ErrMoved),
		errors.Is(err, sandboxplan.ErrNotConfigured),
		errors.As(err, &re):
		return exitRefused
	}
	return exitFailed
}

// refusalError is an error of this command that the owner can act on. It
// ends in exit status 1.
type refusalError struct{ msg string }

func (e *refusalError) Error() string { return e.msg }

func refusalf(format string, args ...any) error {
	return &refusalError{msg: fmt.Sprintf(format, args...)}
}

// hintFor says what the owner can do about an error, in the command's own
// words.
func hintFor(sub string, err error) string {
	switch {
	case errors.Is(err, mirror.ErrNotMirror):
		return "Run '" + program + " init' first."
	case errors.Is(err, intake.ErrNotVerified):
		return "Run '" + program + " verify' for this branch, and then integrate again."
	case errors.Is(err, intake.ErrMoved):
		return "Run '" + program + " review' again and sign off the new review."
	case errors.Is(err, sandboxplan.ErrNotConfigured):
		return "The sandbox template has no section for this step. Nothing was run."
	}
	return ""
}
