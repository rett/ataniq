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
//
// The state folder. The holder writes holder.json (its pid and start ticks, the
// epoch and launch intent it was given) before the agent starts, and
// session.Start's final record final.json when the session is over. Before it
// writes holder.json for a new epoch it renames a final.json that an earlier
// epoch left to final.json.previous, replacing an older one, so that final.json
// is never the record of an epoch other than the last one that ended. After a
// start that was refused past that point, that failed or that a stop signal
// ended, final.json is absent and the last record is in final.json.previous. A
// start that fails after holder.json was written leaves it in place, and says
// in its diagnostic that holder.json names a holder that is gone; a reader
// tells that from a running holder by the pid and its start ticks.
//
// The role lock is taken once, early, and held until the holder exits: after
// the session closes its descriptor, a duplicate stays until the service and
// the event log are closed.
//
// The holder keeps exact control of its descriptors, so it turns off the Go
// runtime's cgroup-aware default for GOMAXPROCS (containermaxprocs=0). With
// it on, a runtime in a cgroup that has the cpu controller opens that cgroup's
// cpu.max and keeps it open, close-on-exec, at the lowest free number. A
// GODEBUG setting in the holder's environment overrides the directive.
//
//go:debug containermaxprocs=0
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

	"golang.org/x/sys/unix"

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

Before a new epoch writes holder.json in --state-dir, a final.json that an
earlier epoch left there is renamed to final.json.previous.

SIGTERM, SIGINT, SIGQUIT, SIGUSR1 and SIGUSR2 stop the agent and end the holder
with its final record; SIGHUP and SIGPIPE change nothing.

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
	// signals subscribes to SIGHUP, SIGPIPE and the signals that stop the
	// holder (see stopSignals); stop gives the subscription up.
	signals func() (<-chan os.Signal, func())
	// ignoredSignals lists the signals the process ignores.
	ignoredSignals func() ([]syscall.Signal, error)
	// tweakSession and tweakServe change the session's and the service's
	// settings (timings, the cgroup switch). They are for tests.
	tweakSession func(*session.Config)
	tweakServe   func(*serve.Config)
	// beforeRoleLock is called just before the role lock is taken, once the
	// start-up checks have passed. It is for tests, which hold two holders
	// there to make them take the lock together.
	beforeRoleLock func()
	// beforeLockRelease is called when the holder has closed its service and
	// its event log and keeps the role lock only through the descriptor that
	// it keeps for the end. It is for tests, which start a second holder
	// there.
	beforeLockRelease func()
}

func realDeps() deps {
	return deps{
		stderr: os.Stderr,
		now:    time.Now,
		signals: func() (<-chan os.Signal, func()) {
			c := make(chan os.Signal, 8)
			// Notify, not Ignore: an ignored signal is inherited by the agent
			// (see ptyhost), a handled one is reset to its default by exec.
			signal.Notify(c, syscall.SIGHUP, syscall.SIGPIPE)
			signal.Notify(c, stopSignals...)
			return c, func() { signal.Stop(c) }
		},
		ignoredSignals: ptyhost.IgnoredSignals,
	}
}

func main() { os.Exit(run(os.Args[1:], realDeps())) }

// stopSignals are the signals that stop the agent and end the holder with its
// final record. Besides SIGTERM and SIGINT they hold SIGQUIT, SIGUSR1 and
// SIGUSR2, which would otherwise end the holder at once (the Go runtime dumps
// its goroutines on SIGQUIT) with no final record: no signal a user can send
// by accident ends the holder without one. SIGKILL cannot be handled.
var stopSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2}

func isStopSignal(s os.Signal) bool {
	for _, st := range stopSignals {
		if s == st {
			return true
		}
	}
	return false
}

// signalsNeeded are the signals the agent must not inherit as ignored. The
// holder refuses to start if it ignores any of them.
var signalsNeeded = []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGPIPE, syscall.SIGTERM}

// staleStateNote ends the diagnostic of a start that failed after the state
// file was written: the file stays, and a reader must not take it for a
// running holder. The pid and its start ticks show that it is gone.
const staleStateNote = StateFileName + " names a holder that is gone"

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
	// A refusal names the flag, never the path it was given: the path is the
	// caller's own, and the journal that takes stderr has no need of it.
	for _, f := range []struct{ flag, path string }{{"--state-dir", cfg.stateDir}, {"--runtime-dir", cfg.runtimeDir}} {
		if err := checkPrivateDir(f.path); err != nil {
			return refuse("%s %v (it must be a real directory of this user with mode 0700)", f.flag, err)
		}
	}
	if fi, err := os.Stat(cfg.tree); err != nil || !fi.IsDir() {
		return refuse("--tree is not a directory")
	}
	ticks, err := selfStartTicks()
	if err != nil {
		return refuse("cannot read this process's start time: %v", err)
	}

	// From here on the stop signals end the agent, and SIGHUP changes
	// nothing: the holder outlives its starter's terminal. One that comes
	// before the agent starts is acted on just before it would (below).
	sigc, stopSignals := d.signals()
	defer stopSignals()

	// The role lock is taken once, here, before anything is opened or written,
	// and kept until the holder ends: a second holder for a held role is
	// refused having changed nothing, and two that start together cannot both
	// write the state file (the lead-hosting design, section 1.6). The lock
	// belongs to session.Start from the call on.
	if d.beforeRoleLock != nil {
		d.beforeRoleLock()
	}
	lock, err := session.TakeRoleLock(cfg.roleLock)
	if errors.Is(err, session.ErrRoleLocked) {
		out.line("refused: the role is already held by another holder (--role-lock is locked)")
		return exitLocked
	} else if err != nil {
		// The error names the path; its cause (an errno) does not.
		reason := "it is not a regular file of this user"
		if cause := errors.Unwrap(err); cause != nil {
			reason = cause.Error()
		}
		return refuse("--role-lock cannot be taken as the role lock: %s", reason)
	}
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()
	// The lock belongs to the open file description, so a duplicate of the
	// descriptor holds it as well: the session closes its own when it is
	// closed, and this one stays until the holder ends, after the service
	// and the event log are closed (a deferred call runs after those below,
	// which are deferred later). A next holder then cannot take the role while
	// this one still has the log open (the lead-hosting design, section 1.6).
	// It is close-on-exec, so that the agent never has it.
	keep, err := unix.FcntlInt(lock.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return refuse("cannot duplicate the role lock descriptor: %v", err)
	}
	defer unix.Close(keep)
	defer func() {
		if d.beforeLockRelease != nil {
			d.beforeLockRelease()
		}
	}()

	log, err := eventlog.Open(cfg.stateDir, eventlog.Options{Clock: d.now})
	if err != nil {
		return refuse("cannot open the event log in the state folder: %v", err)
	}
	defer log.Close()

	// A final record of an earlier epoch is moved aside first, so that
	// final.json never says "ended" for the agent that is about to run.
	if err := retireFinalRecord(cfg.stateDir); err != nil {
		return refuse("cannot move the final record of an earlier epoch aside: %v", err)
	}
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
		RoleLock: cfg.roleLock, HeldRoleLock: lock, StateDir: cfg.stateDir, Log: log,
	}
	if d.tweakSession != nil {
		d.tweakSession(&scfg)
	}
	// A stop signal that came during start-up ends the holder here,
	// before the agent exists: there is nothing to stop and nothing to record
	// but the refusal, and the state file, which names a holder that is going,
	// is removed. SIGHUP and SIGPIPE change nothing.
	if s := stopSignalPending(sigc); s != nil {
		_ = log.Append(eventlog.KindHolder, eventlog.EventRefusal, eventlog.Fields{Role: cfg.role, Epoch: strconv.FormatUint(cfg.epoch, 10), Reason: "signal-before-start"})
		if err := removeStateFile(cfg.stateDir); err != nil {
			out.line("cannot remove the state file: %v", err)
		}
		out.line("stopped by %s before the agent was started", signalName(*s))
		return exitRefuse
	}
	// Start owns the lock now, whether it succeeds or not.
	lock = nil
	sess, err := session.Start(scfg)
	if err != nil {
		if errors.Is(err, session.ErrRoleLocked) {
			out.line("refused: the role is already held by another holder (--role-lock is locked)")
			return exitLocked
		}
		_ = log.Append(eventlog.KindHolder, eventlog.EventRefusal, eventlog.Fields{Role: cfg.role, Epoch: strconv.FormatUint(cfg.epoch, 10), Reason: "start-failed"})
		// The error names the agent's path, which the journal has no need of.
		out.line("cannot start the agent: %s (%s)", startReason(err), staleStateNote)
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
		out.line("cannot start the attach and control service: %v (%s)", err, staleStateNote)
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
			if isStopSignal(s) {
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

// stopSignalPending takes every signal that is waiting in sigc and returns the
// first one that ends the holder (see stopSignals), or nil.
func stopSignalPending(sigc <-chan os.Signal) *syscall.Signal {
	var stop *syscall.Signal
	for {
		select {
		case s := <-sigc:
			if sg, ok := s.(syscall.Signal); ok && isStopSignal(sg) && stop == nil {
				stop = &sg
			}
		default:
			return stop
		}
	}
}

// startReason is why the session could not start, as far as the operating
// system says: the errno's text (no such file or directory, permission denied)
// and nothing of the paths in the error.
func startReason(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return "the session did not start"
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
	case syscall.SIGUSR1:
		return "SIGUSR1"
	case syscall.SIGUSR2:
		return "SIGUSR2"
	}
	return "signal " + strconv.Itoa(int(s))
}
