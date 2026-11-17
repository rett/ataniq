//go:build linux

// Command ataniq-holder is the holder process of one hosted agent (the
// lead-hosting design, section 1): the one process that owns the agent's
// terminal, the emulator that mirrors its screen, the attach and control
// sockets, the event log and the final record. It joins the packages
// internal/leadholder/session, serve and eventlog and adds only what a process
// must do: parse the command line, refuse what it must not run, write its state
// file, start, and end with the agent.
//
// In this version it hosts only a scratch agent: there is no sandbox yet, so it
// refuses to start without --scratch. It is provisional and unqualified, and it
// hosts no real lead. The development launcher ataniq-scratch starts it.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/ptyhost"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
)

// Exit statuses.
const (
	exitOK     = 0 // the session ended and the final record was written
	exitFail   = 1 // a holder failure after start
	exitRefuse = 2 // wrong usage or a start-up refusal
	exitLocked = 3 // the role is held by another holder
)

const usageText = `Usage:
  ataniq-holder --scratch --role ROLE --epoch N --launch-intent ID --runtime NAME
                --runtime-version V --profile-digest D --tree DIR --state-dir DIR
                --runtime-dir DIR --role-lock FILE --daemon-lock FILE --cols C --rows R
                [--env KEY=VALUE]... -- ARGV...

ataniq-holder is the holder process of one hosted agent. In this version it can
host only a SCRATCH agent (--scratch): the sandbox is not built. It is
provisional and unqualified, and it hosts no real lead. ataniq-scratch starts it.

Every flag is required except --env. The agent's environment is exactly the
--env list, in order: nothing of the holder's own environment is passed on.
ARGV[0] is an absolute path. The agent runs in --tree.

Exit status: 0 the session ended and its final record was written, 1 a holder
failure after start, 2 wrong usage or a start-up refusal, 3 the role is held.
`

// deps is what the command needs from outside, so that a test can run it in
// process.
type deps struct {
	stderr io.Writer
	// procRoot is where the sysctl dev.tty.legacy_tiocsti is read; empty is
	// /proc.
	procRoot string
	// now is the clock of the state file, the event log and the service.
	now func() time.Time
	// signals subscribes to SIGHUP, SIGINT, SIGTERM and SIGPIPE; stop gives
	// the subscription up.
	signals func() (<-chan os.Signal, func())
	// ignoredSignals lists the signals the process ignores.
	ignoredSignals func() ([]syscall.Signal, error)
	// tweakSession and tweakServe change the session's and the service's
	// settings (timings, the cgroup switch). They are for tests.
	tweakSession func(*session.Config)
	tweakServe   func(*serve.Config)
}

func realDeps() deps {
	return deps{
		stderr: os.Stderr,
		now:    time.Now,
		signals: func() (<-chan os.Signal, func()) {
			c := make(chan os.Signal, 8)
			// Notify, not Ignore: an ignored signal is inherited by the agent
			// (see ptyhost), a handled one is reset to its default by exec.
			signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE)
			return c, func() { signal.Stop(c) }
		},
		ignoredSignals: ptyhost.IgnoredSignals,
	}
}

func main() { os.Exit(run(os.Args[1:], realDeps())) }

// signalsNeeded are the signals the agent must not inherit as ignored. The
// holder refuses to start if it ignores any of them.
var signalsNeeded = []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGPIPE, syscall.SIGTERM}

// holderOut writes the holder's own diagnostics to stderr. A failure to write
// is ignored: the holder must not end because its journal is gone.
type holderOut struct{ w io.Writer }

func (o holderOut) line(format string, args ...any) {
	fmt.Fprintf(o.w, "ataniq-holder: "+format+"\n", args...)
}

// run is the command. It returns the exit status.
func run(args []string, d deps) int {
	out := holderOut{d.stderr}
	if d.now == nil {
		d.now = time.Now
	}
	cfg, err := parse(args)
	switch e := err.(type) {
	case nil:
	case helpRequest:
		fmt.Fprint(d.stderr, usageText)
		return exitOK
	case notScratch:
		out.line("refused: this build can host only a scratch agent (--scratch): the sandbox is not built")
		return exitRefuse
	case usageError:
		fmt.Fprintln(d.stderr, "error: "+string(e))
		fmt.Fprint(d.stderr, usageText)
		return exitRefuse
	default:
		fmt.Fprintln(d.stderr, "error: invalid arguments")
		fmt.Fprint(d.stderr, usageText)
		return exitRefuse
	}
	refuse := func(format string, args ...any) int {
		out.line("refused: "+format, args...)
		return exitRefuse
	}

	// Nothing the holder holds open may reach the agent.
	if err := ptyhost.CloseOnExecFrom(3); err != nil {
		return refuse("cannot mark the inherited descriptors close-on-exec: %v", err)
	}
	// Nor an ignored signal the agent needs.
	ignored, err := d.ignoredSignals()
	if err != nil {
		return refuse("cannot read which signals this process ignores: %v", err)
	}
	for _, s := range ignored {
		for _, need := range signalsNeeded {
			if s == need {
				return refuse("this process ignores %s (%d), which the agent needs: start the holder with the default signal handling", signalName(s), int(s))
			}
		}
	}
	// The kernel must not let a process push bytes into its own terminal's
	// input (design 1.1).
	if enabled, known := ptyhost.LegacyTIOCSTI(d.procRoot); enabled {
		if known {
			return refuse("dev.tty.legacy_tiocsti is 1: a process on a terminal could push input into it; set it to 0")
		}
		return refuse("dev.tty.legacy_tiocsti cannot be read as 0 or 1, so the TIOCSTI ioctl cannot be shown to be off")
	}
	for _, f := range []struct{ what, path string }{{"the state folder", cfg.stateDir}, {"the runtime folder", cfg.runtimeDir}} {
		if err := checkPrivateDir(f.path); err != nil {
			return refuse("%s %q %v (it must be a real directory of this user with mode 0700)", f.what, f.path, err)
		}
	}
	if fi, err := os.Stat(cfg.tree); err != nil || !fi.IsDir() {
		return refuse("the work tree %q is not a directory", cfg.tree)
	}
	ticks, err := selfStartTicks()
	if err != nil {
		return refuse("cannot read this process's start time: %v", err)
	}

	// A second holder for a role that is held changes nothing of the first's:
	// look at the lock before anything is written. session.Start takes it for
	// good below.
	if roleLockHeld(cfg.roleLock) {
		out.line("refused: the role is already held by another holder (%q is locked)", cfg.roleLock)
		return exitLocked
	}

	// From here on SIGTERM and SIGINT end the agent, and SIGHUP changes
	// nothing: the holder outlives its starter's terminal.
	sigc, stopSignals := d.signals()
	defer stopSignals()

	log, err := eventlog.Open(cfg.stateDir, eventlog.Options{Clock: d.now})
	if err != nil {
		return refuse("cannot open the event log in the state folder: %v", err)
	}
	defer log.Close()

	// The state file is written before the agent starts (design 1.1).
	if err := writeStateFile(cfg.stateDir, stateFile{
		Version: stateFileVersion, Role: cfg.role, Epoch: cfg.epoch, LaunchIntent: cfg.launchIntent,
		Runtime: cfg.runtime, RuntimeVersion: cfg.runtimeVersion, ProfileDigest: cfg.profileDigest,
		Tree: cfg.tree, Pid: os.Getpid(), PidStartTicks: ticks, Started: d.now().UTC(),
		Cols: cfg.cols, Rows: cfg.rows, Agent: cfg.argv[0], Scratch: true,
	}); err != nil {
		return refuse("cannot write the state file in the state folder: %v", err)
	}

	scfg := session.Config{
		Role: cfg.role, Epoch: strconv.FormatUint(cfg.epoch, 10),
		Argv: cfg.argv, Env: cfg.env, Dir: cfg.tree, Cols: cfg.cols, Rows: cfg.rows,
		RoleLock: cfg.roleLock, StateDir: cfg.stateDir, Log: log,
	}
	if d.tweakSession != nil {
		d.tweakSession(&scfg)
	}
	sess, err := session.Start(scfg)
	if err != nil {
		if errors.Is(err, session.ErrRoleLocked) {
			out.line("refused: the role is already held by another holder (%q is locked)", cfg.roleLock)
			return exitLocked
		}
		_ = log.Append(eventlog.KindHolder, eventlog.EventRefusal, eventlog.Fields{Role: cfg.role, Epoch: strconv.FormatUint(cfg.epoch, 10), Reason: "start-failed"})
		out.line("cannot start the agent: %v", err)
		return exitFail
	}
	defer sess.Close()

	vcfg := serve.Config{
		Session: sess, Dir: cfg.runtimeDir, Lead: cfg.role, Epoch: cfg.epoch,
		Runtime: cfg.runtime, RuntimeVersion: cfg.runtimeVersion, Log: log, DaemonLock: cfg.daemonLock,
		Now: d.now,
	}
	if d.tweakServe != nil {
		d.tweakServe(&vcfg)
	}
	srv, err := serve.New(vcfg)
	if err != nil {
		out.line("cannot start the attach and control service: %v", err)
		return exitFail
	}
	defer srv.Close()
	served := make(chan error, 1)
	go func() { served <- srv.Serve() }()
	out.line("ready: role %q epoch %d, agent pid %d, sockets in the runtime folder", cfg.role, cfg.epoch, sess.Pid())

	// Run until the session is over, whoever ends it.
	var stopOnce sync.Once
	var serveErr error
wait:
	for {
		select {
		case s := <-sigc:
			if s == syscall.SIGTERM || s == syscall.SIGINT {
				// Stop returns when the tree is gone; the lifetime path then
				// writes the final record and the service sends the Exit.
				stopOnce.Do(func() {
					sg, _ := s.(syscall.Signal)
					out.line("stopping the agent (%s)", signalName(sg))
					go sess.Stop()
				})
			}
			// SIGHUP and SIGPIPE change nothing.
		case serveErr = <-served:
			break wait
		}
	}

	// The service returned: the session is over, or the service failed. Close
	// stops an agent that still runs, waits for the final record and releases
	// the role lock.
	sess.Close()
	code := exitOK
	if serveErr != nil {
		out.line("the attach and control service ended with an error: %v", serveErr)
		code = exitFail
	}
	if _, err := sess.Final(); err != nil {
		out.line("the final record was not written: %v", err)
		code = exitFail
	}
	if err := log.Sync(); err != nil {
		out.line("cannot sync the event log: %v", err)
	}
	return code
}

func signalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGTERM:
		return "SIGTERM"
	}
	return "signal " + strconv.Itoa(int(s))
}
