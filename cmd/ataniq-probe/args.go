package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Exit statuses of the command itself. An agent's own status is its own once
// it is executed; the probe's 97 and 98 mean the agent was not started, except
// that the waiting parent of a new session passes the agent's status on.
const (
	// exitFailed: the inside checks did not pass; the agent is not started.
	exitFailed = 97
	// exitSetup: a usage error or any failure to do what the command is for
	// (read the spec, write the report, open a file, get the terminal,
	// execute the program). Nothing was executed.
	exitSetup = 98
)

// The command line.
const (
	optSpec     = "--spec"
	optReportFD = "--report-fd"
	optEnd      = "--"

	// minFD and maxFD bound a descriptor number given: 0 to 2 are the
	// standard streams, and the plan refuses a number above 1023.
	minFD = 3
	maxFD = 1023
)

// usageError is a refused command line. Its text is one line, without the
// command name.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// quote shows an argument in a message, clipped.
func quote(s string) string {
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return strconv.Quote(s)
}

// insideArgs is the command line of the inside mode.
type insideArgs struct {
	// Spec is the absolute path of the spec file.
	Spec string
	// ReportFD is the descriptor the report line is written to.
	ReportFD int
	// Agent is the argument vector of the agent: Agent[0] is an absolute
	// path and is executed.
	Agent []string
}

// parseInside reads the arguments after the program name:
//
//	--spec PATH --report-fd N -- AGENT ARGV...
//
// strictly. Each option is required once, in either order. The spec path is
// absolute, N is 3 to 1023 (decimal digits only), the agent argument vector is
// not empty and its first element is an absolute path. An unknown option, a
// missing value, a missing "--" and anything else is a *usageError.
func parseInside(args []string) (insideArgs, error) {
	var a insideArgs
	var haveSpec, haveFD bool
	end := -1
	for i := 0; i < len(args) && end < 0; i++ {
		switch args[i] {
		case optEnd:
			end = i
		case optSpec, optReportFD:
			opt := args[i]
			if i+1 >= len(args) || args[i+1] == optEnd {
				return a, usagef("%s needs a value", opt)
			}
			i++
			val := args[i]
			if opt == optSpec {
				if haveSpec {
					return a, usagef("%s is given twice", opt)
				}
				if !absolute(val) {
					return a, usagef("%s must be an absolute path, not %s", opt, quote(val))
				}
				a.Spec, haveSpec = val, true
				continue
			}
			if haveFD {
				return a, usagef("%s is given twice", opt)
			}
			n, err := parseFD(val)
			if err != nil {
				return a, usagef("%s %s: %v", opt, quote(val), err)
			}
			a.ReportFD, haveFD = n, true
		default:
			if strings.HasPrefix(args[i], "-") {
				return a, usagef("unknown option %s", quote(args[i]))
			}
			return a, usagef("unexpected argument %s before %s", quote(args[i]), optEnd)
		}
	}
	switch {
	case !haveSpec:
		return a, usagef("%s is required", optSpec)
	case !haveFD:
		return a, usagef("%s is required", optReportFD)
	case end < 0:
		return a, usagef("%s and the agent command are required after the options", optEnd)
	}
	agent := args[end+1:]
	if err := checkProgram("agent", agent); err != nil {
		return a, err
	}
	a.Agent = append([]string(nil), agent...)
	return a, nil
}

// checkProgram says argv can be executed: not empty, and a first element that
// is an absolute path (the probe never looks a program up in PATH).
func checkProgram(what string, argv []string) error {
	if len(argv) == 0 {
		return usagef("the %s command is empty", what)
	}
	if !absolute(argv[0]) {
		return usagef("the %s path must be absolute, not %s", what, quote(argv[0]))
	}
	return nil
}

func absolute(p string) bool { return strings.HasPrefix(p, "/") }

// parseFD reads a descriptor number: decimal digits only (no sign, no space,
// no leading "+"), 3 to 1023.
func parseFD(s string) (int, error) {
	if s == "" || len(s) > 4 {
		return 0, fmt.Errorf("is not a descriptor number from %d to %d", minFD, maxFD)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("is not a descriptor number from %d to %d", minFD, maxFD)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < minFD || n > maxFD {
		return 0, fmt.Errorf("is not a descriptor number from %d to %d", minFD, maxFD)
	}
	return n, nil
}
