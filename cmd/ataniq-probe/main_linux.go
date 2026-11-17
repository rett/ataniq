//go:build linux

package main

import (
	"fmt"
	"io"

	"ataniq/internal/leadsandbox/assert"
)

// execute runs the command in the inside mode. It returns the exit status;
// where the agent is executed it does not return. Nothing here writes to
// standard output.
func execute(args []string, stderr io.Writer) int {
	if len(args) < 1 {
		return fail(stderr, exitSetup, "usage: no program name")
	}
	return runInside(args[1:], stderr, hostInsideDeps())
}

// say writes one line to standard error, after the command name. The text may
// quote a path or a resolver error, so it is escaped and bounded like a detail
// of the report: it is display only.
func say(w io.Writer, format string, args ...any) {
	fmt.Fprintln(w, assert.EscapeDetail("ataniq-probe: "+fmt.Sprintf(format, args...)))
}

// fail says one line and returns the status.
func fail(w io.Writer, code int, format string, args ...any) int {
	say(w, format, args...)
	return code
}
