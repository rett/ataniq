package main

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Exit statuses of the command itself. An agent's own status is its own once
// it is executed; the probe's 96, 97 and 98 mean nothing was started, except
// that the waiting parent of a new session passes the agent's status on.
const (
	// exitWalk: the pass-through mode refused a bind source: the folder a
	// walk starts from could not be opened without following a link, or the
	// path beneath it is a link, is missing, is not what the walk names, lies
	// at a mount point or could not be walked by handle. Nothing was executed.
	// It is a status of its own so that a holder can tell a source that is not
	// what the plan binds from a failure to set the step up.
	exitWalk = 96
	// exitFailed: the inside checks did not pass; the agent is not started.
	exitFailed = 97
	// exitSetup: a usage error or any failure to do what the command is for
	// (read the spec, write the report, open a file, get the terminal,
	// execute the program). Nothing was executed.
	exitSetup = 98
)

// The command line.
const (
	// modePassFDs is the first argument of the pass-through mode. Anything
	// else is the inside mode.
	modePassFDs = "pass-fds"

	optSpec     = "--spec"
	optReportFD = "--report-fd"
	optRead     = "--read"
	optWrite    = "--write"
	optWalkDir  = "--walk-dir"
	optWalkFile = "--walk-file"
	optEnd      = "--"

	// minFD and maxFD bound every descriptor number given: 0 to 2 are the
	// standard streams, and the plan refuses a number above 1023.
	minFD = 3
	maxFD = 1023
	// maxPassFDs is the most descriptors the pass-through mode places, of
	// every kind together, and a hard cap. The launch plan asks for one per
	// bind row Ataniq supplies (23 in the bind table of the profile today),
	// one per extra placeholder of the tree (the profile allows 64), the
	// seccomp file and the report channel: 89 at most. 128 is the next power
	// of two, which leaves 39 for rows the table gains before the number has
	// to be looked at again, and stays far below the 1024 descriptors a
	// process usually may have open (the step holds two per walk for a
	// moment, and one per folder the walks start from).
	maxPassFDs = 128
	// maxWalkPath bounds how much of a walk's folder or path a message
	// repeats.
	maxWalkPath = 150
)

// usageError is a refused command line. Its text is one line, without the
// command name.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// quote shows an argument in a message, clipped.
func quote(s string) string { return quoteN(s, 60) }

// quoteN shows an argument in a message, clipped to n bytes.
func quoteN(s string, n int) string {
	if len(s) > n {
		s = s[:n] + "..."
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
// absolute, N is 3 to 1023 (decimal digits only, no leading zero), the agent argument vector is
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
// no leading "+", no leading zero), 3 to 1023.
func parseFD(s string) (int, error) {
	if s == "" || len(s) > 4 || (len(s) > 1 && s[0] == '0') {
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

// fdFile is one file the pass-through mode opens: --read or --write FD=PATH.
type fdFile struct {
	FD    int
	Path  string
	Write bool
}

// fdWalk is one bind source the pass-through mode walks by handle:
// --walk-dir or --walk-file FD ROOT REL.
type fdWalk struct {
	FD int
	// Root is the folder the walk starts from, opened by name: absolute and
	// clean.
	Root string
	// Rel is the path of the source beneath Root: relative and clean, without
	// "..", and "." only for a folder that is Root itself.
	Rel string
	// Dir says the source must be a folder; else it must be a regular file.
	Dir bool
}

// option is the option that names the walk.
func (w fdWalk) option() string {
	if w.Dir {
		return optWalkDir
	}
	return optWalkFile
}

// passArgs is the command line of the pass-through mode.
type passArgs struct {
	Files []fdFile
	Walks []fdWalk
	// Command is the argument vector executed: Command[0] is an absolute
	// path.
	Command []string
}

// targets are the descriptor numbers the command line asks for, the walks
// first, as they are opened.
func (a passArgs) targets() []int {
	out := make([]int, 0, len(a.Walks)+len(a.Files))
	for _, w := range a.Walks {
		out = append(out, w.FD)
	}
	for _, f := range a.Files {
		out = append(out, f.FD)
	}
	return out
}

// parsePassFDs reads the arguments after "pass-fds":
//
//	[--read FD=PATH]... [--write FD=PATH]... [--walk-dir FD ROOT REL]... [--walk-file FD ROOT REL]... -- COMMAND ARGV...
//
// strictly. Options may come in any order. FD is 3 to 1023 and each at most
// once, whatever the option; PATH is absolute; ROOT is absolute and clean; REL
// is relative and clean, without "..", and "." only with --walk-dir; there are
// at most 128 descriptors in all; COMMAND is an absolute path. Anything else
// is a *usageError.
func parsePassFDs(args []string) (passArgs, error) {
	var a passArgs
	seen := map[int]bool{}
	end := -1
	for i := 0; i < len(args) && end < 0; i++ {
		switch args[i] {
		case optEnd:
			end = i
		case optWalkDir, optWalkFile:
			opt := args[i]
			if i+3 >= len(args) || args[i+1] == optEnd || args[i+2] == optEnd || args[i+3] == optEnd {
				return a, usagef("%s needs three values: FD ROOT REL", opt)
			}
			w, err := parseWalk(opt, args[i+1], args[i+2], args[i+3])
			if err != nil {
				return a, err
			}
			i += 3
			if seen[w.FD] {
				return a, usagef("descriptor %d is given twice", w.FD)
			}
			if len(a.Files)+len(a.Walks) >= maxPassFDs {
				return a, usagef("more than %d descriptors", maxPassFDs)
			}
			seen[w.FD] = true
			a.Walks = append(a.Walks, w)
		case optRead, optWrite:
			opt := args[i]
			if i+1 >= len(args) || args[i+1] == optEnd {
				return a, usagef("%s needs a value of the form FD=PATH", opt)
			}
			i++
			f, err := parseFDFile(opt, args[i])
			if err != nil {
				return a, err
			}
			if seen[f.FD] {
				return a, usagef("descriptor %d is given twice", f.FD)
			}
			if len(a.Files)+len(a.Walks) >= maxPassFDs {
				return a, usagef("more than %d descriptors", maxPassFDs)
			}
			seen[f.FD] = true
			a.Files = append(a.Files, f)
		default:
			if strings.HasPrefix(args[i], "-") {
				return a, usagef("unknown option %s", quote(args[i]))
			}
			return a, usagef("unexpected argument %s before %s", quote(args[i]), optEnd)
		}
	}
	if end < 0 {
		return a, usagef("%s and the command are required after the options", optEnd)
	}
	cmd := args[end+1:]
	if err := checkProgram("command", cmd); err != nil {
		return a, err
	}
	a.Command = append([]string(nil), cmd...)
	return a, nil
}

func parseFDFile(opt, val string) (fdFile, error) {
	num, path, ok := strings.Cut(val, "=")
	if !ok {
		return fdFile{}, usagef("%s %s: the form is FD=PATH", opt, quote(val))
	}
	fd, err := parseFD(num)
	if err != nil {
		return fdFile{}, usagef("%s %s: the descriptor %v", opt, quote(val), err)
	}
	if !absolute(path) {
		return fdFile{}, usagef("%s %s: the path must be absolute", opt, quote(val))
	}
	return fdFile{FD: fd, Path: path, Write: opt == optWrite}, nil
}

// parseWalk reads the three values of --walk-dir or --walk-file. The paths are
// checked as text only: what they name is looked at when the walk is made.
func parseWalk(opt, num, root, rel string) (fdWalk, error) {
	fd, err := parseFD(num)
	if err != nil {
		return fdWalk{}, usagef("%s %s: the descriptor %v", opt, quote(num), err)
	}
	w := fdWalk{FD: fd, Root: root, Rel: rel, Dir: opt == optWalkDir}
	if !absolute(root) || path.Clean(root) != root {
		return fdWalk{}, usagef("%s %d: the folder %s must be an absolute, clean path", opt, fd, quote(root))
	}
	switch {
	case rel == "" || absolute(rel):
		return fdWalk{}, usagef("%s %d: the path %s must be relative to the folder", opt, fd, quote(rel))
	case rel == ".":
		if !w.Dir {
			return fdWalk{}, usagef("%s %d: the path \".\" names the folder, not a file", opt, fd)
		}
	case path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../"):
		// A clean relative path holds ".." only at its start.
		return fdWalk{}, usagef("%s %d: the path %s must be clean and must not leave the folder", opt, fd, quote(rel))
	}
	return w, nil
}
