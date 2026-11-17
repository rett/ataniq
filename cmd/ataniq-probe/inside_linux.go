//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
)

// newSystem makes the System the inside checks run against. It is the one
// place the checks' view of the world can be replaced, by tests only, with a
// fake: the real checks are never weakened.
var newSystem = func(assert.Spec) assert.System { return assert.HostSystem{} }

// maxFailedLines is the most failed results listed on standard error.
const maxFailedLines = 40

// launchFunc runs the agent when the checks passed. It returns only the exit
// status of a failure, or the agent's status when the agent ran as a child of
// a waiting probe.
type launchFunc func(agent, env []string, reportFD int, stderr io.Writer) int

// insideDeps is what the inside mode needs from the world.
type insideDeps struct {
	system  func(assert.Spec) assert.System
	environ func() []string
	launch  launchFunc
}

func hostInsideDeps() insideDeps {
	return insideDeps{
		system:  func(s assert.Spec) assert.System { return newSystem(s) },
		environ: os.Environ,
		launch:  launchAgent,
	}
}

// runInside is the inside mode. In order: parse the command line; mark the
// report descriptor close-on-exec; read the spec; run the inside checks; write
// the report line, in full; decide from the report; give the agent its
// session; close the other descriptors; execute the agent. It never issues
// TIOCSTI, never creates or writes a file, and never changes the environment.
func runInside(args []string, stderr io.Writer, d insideDeps) int {
	a, err := parseInside(args)
	if err != nil {
		return fail(stderr, exitSetup, "usage: %v", err)
	}
	// First, so that no later step, and no exec, can leak the descriptor.
	if err := setCloexec(a.ReportFD); err != nil {
		return fail(stderr, exitSetup, "cannot use the report descriptor %d: %v", a.ReportFD, err)
	}
	spec, err := readSpec(a.Spec)
	if err != nil {
		// No report: the holder sees the exit status.
		return fail(stderr, exitSetup, "cannot read the spec %s: %v", quote(a.Spec), err)
	}
	rep := assert.RunInside(spec, d.system(spec))
	line := assert.EncodeLine(rep)
	fd := a.ReportFD
	if err := writeAll(func(p []byte) (int, error) { return unix.Write(fd, p) }, line); err != nil {
		return fail(stderr, exitSetup, "cannot write the report to descriptor %d: %v", fd, err)
	}
	if code := afterReport(rep, spec, stderr); code != 0 {
		return code
	}
	return d.launch(a.Agent, d.environ(), a.ReportFD, stderr)
}

// afterReport is the decision after the report is written: 0 when the agent
// may be started, exitFailed when assert.Failed says the report does not allow
// a start. Then the agent is not started, and the problems are listed on
// standard error, one line each, for a person: display only, the holder reads
// the report descriptor and the exit status.
func afterReport(rep assert.Report, spec assert.Spec, stderr io.Writer) int {
	if !assert.Failed(rep, spec) {
		return 0
	}
	problems := assert.Problems(rep, spec, assert.RequireInside)
	say(stderr, "the inside checks did not pass: %d problems; the agent is not started", len(problems))
	for i, p := range problems {
		if i == maxFailedLines {
			say(stderr, "  ... and %d more", len(problems)-i)
			break
		}
		say(stderr, "  %s", p)
	}
	return exitFailed
}

// readSpec reads the spec file: opened without following a link at the last
// component, without blocking and without becoming a controlling terminal,
// refused unless it is a regular file, then decoded with assert.DecodeSpec,
// which bounds what it reads. It uses plain system calls, so that no os.File
// (and no poller) is made.
func readSpec(path string) (assert.Spec, error) {
	fd, err := openRetry(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return assert.Spec{}, errors.New("it is a symbolic link")
		}
		return assert.Spec{}, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return assert.Spec{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return assert.Spec{}, fmt.Errorf("it is not a regular file")
	}
	return assert.DecodeSpec(fdReader{fd})
}
