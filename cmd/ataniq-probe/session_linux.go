//go:build linux

package main

import (
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// execFunc is execve(2) through the runtime (unix.Exec). It returns only on
// failure.
type execFunc func(path string, argv, env []string) error

// execProgram closes the descriptors above 2 (see closeOnExecAbove2) and
// executes argv[0] with env, which the caller takes from the process
// (os.Environ: a name that appears twice is passed on once, with its first
// value). It returns the exit status of a failure, with one line on
// standard error; on success it does not return.
func execProgram(what string, argv, env []string, stderr io.Writer, exec execFunc, closeAbove2 func() error) int {
	if err := closeAbove2(); err != nil {
		return fail(stderr, exitSetup, "cannot close the descriptors above 2 before the %s: %v", what, err)
	}
	if err := exec(argv[0], argv, env); err != nil {
		return fail(stderr, exitSetup, "cannot execute the %s %s: %v", what, quote(argv[0]), err)
	}
	return 0 // unreachable: a successful exec does not return
}

// launchAgent is the end of the inside mode. When descriptor 0 is a terminal
// the agent gets a new session with that terminal as its controlling terminal
// (variant B of the lead-hosting design, section 1.1): setsid, then
// TIOCSCTTY. When setsid is refused because the probe leads a process group,
// the agent is started as a child in a new session (the child's setsid and
// TIOCSCTTY are done by the runtime before its execve) and the probe waits.
// When descriptor 0 is not a terminal it says so and starts the agent as it is.
func launchAgent(agent, env []string, reportFD int, stderr io.Writer) int {
	tty, err := stdinIsTerminal()
	if err != nil {
		return fail(stderr, exitSetup, "cannot tell whether descriptor 0 is a terminal: %v", err)
	}
	exec := func() int {
		return execProgram("agent", agent, env, stderr, unix.Exec, func() error { return hostCloseOnExecAbove2(nil) })
	}
	if !tty {
		say(stderr, "descriptor 0 is not a terminal: the agent gets no new session and no controlling terminal")
		return exec()
	}
	if _, err := unix.Setsid(); err != nil {
		if err != unix.EPERM {
			return fail(stderr, exitSetup, "cannot start a new session: %v", err)
		}
		// EPERM: the probe leads a process group, so it cannot lead a session.
		return runInNewSession(agent, env, reportFD, stderr)
	}
	if err := unix.IoctlSetInt(0, unix.TIOCSCTTY, 0); err != nil {
		return fail(stderr, exitSetup, "cannot make descriptor 0 the controlling terminal of the new session: %v", err)
	}
	return exec()
}

// stdinIsTerminal asks the kernel whether descriptor 0 is a terminal. A closed
// descriptor 0 is not one. Any other failure is an error: the probe does not
// guess.
func stdinIsTerminal() (bool, error) {
	for {
		_, err := unix.IoctlGetTermios(0, unix.TCGETS)
		switch err {
		case nil:
			return true, nil
		case unix.ENOTTY, unix.EBADF:
			return false, nil
		case unix.EINTR:
			continue
		}
		return false, err
	}
}

// forwarded are the signals the waiting probe passes on to the agent.
var forwarded = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT}

// runInNewSession starts the agent as a child in a new session whose
// controlling terminal is descriptor 0 (the runtime does setsid and
// TIOCSCTTY in the child), waits, forwards the signals of forwarded, and
// returns the child's exit status, or 128 plus the signal number when a signal
// ended it. The descriptors above 2 are close-on-exec before the start, so the
// child has only 0 to 2; the probe closes its own copy of the report
// descriptor once the child has started. It writes nothing to that descriptor
// here: the one report line is already written, and the holder takes that line,
// not end-of-file, as its signal. Where this probe is itself the agent's
// parent, the agent's status 97 or 98 cannot be told from the probe's own.
func runInNewSession(agent, env []string, reportFD int, stderr io.Writer) int {
	if err := hostCloseOnExecAbove2(nil); err != nil {
		return fail(stderr, exitSetup, "cannot close the descriptors above 2 before the agent: %v", err)
	}
	// Before the start, so that a signal that comes early is held, not lost.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, forwarded...)
	defer signal.Stop(sigs)
	p, err := os.StartProcess(agent[0], agent, &os.ProcAttr{
		Env:   env,
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Sys:   &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0},
	})
	if err != nil {
		return fail(stderr, exitSetup, "cannot start the agent %s in a new session: %v", quote(agent[0]), err)
	}
	unix.Close(reportFD)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				p.Signal(s)
			case <-done:
				return
			}
		}
	}()
	st, err := p.Wait()
	close(done)
	if err != nil {
		return fail(stderr, exitSetup, "cannot wait for the agent: %v", err)
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}
