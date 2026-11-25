// Command ataniq-scratch is a development launcher for a scratch agent: it
// starts a lead holder for an agent that has no project, no lead role and a
// disposable configuration, and it stands in for the daemon where the
// lead-hosting design needs one (section 2.1: the test harness issues the
// attach tickets where no daemon does). A scratch holder is not managed by the
// daemon (ataniqd), which starts no holder and issues no ticket. This command
// is provisional, unqualified and not for a real lead.
package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ataniq/internal/leadholder/buildid"
)

// Exit statuses.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

const usageText = `Usage:
  ataniq-scratch start NAME [--cols C --rows R] [--env KEY=VALUE]... [--holder PATH] [--direct] -- ARGV...
  ataniq-scratch ticket NAME [--viewer]
  ataniq-scratch status NAME
  ataniq-scratch release-writer NAME
  ataniq-scratch stop NAME

ataniq-scratch is a development launcher for a SCRATCH agent only: no project,
no lead role, a disposable home. It is provisional and unqualified, and it is
not for a real lead. It stands in for the daemon: a scratch holder is not
managed by the daemon (ataniqd), which starts no holder and issues no ticket,
so this command issues attach tickets over the holder's control socket.

NAME is 1 to 31 characters of a-z, 0-9 and -, starting with a letter or digit.

start    Start a holder for a scratch agent that runs ARGV. ARGV[0] must be an
         absolute path: the holder does no PATH search. The agent's
         environment is only TERM, HOME (a disposable home), PATH, LANG and each
         --env; nothing else is inherited. --env values appear in the holder's
         command line: put no secret in them. By default the holder runs in a
         transient systemd user unit (this needs systemd 254 or newer; the unit
         never expands $VAR in an argument); --direct runs it as a child of this
         command in the foreground until it exits, and the process-tree proof is
         then the weaker one. --holder PATH names the holder binary (default:
         ataniq-holder next to this command). --cols and --rows set the first
         screen size (default 100 by 30).
ticket   Mint an attach ticket, register it with the holder, and write it to
         the file named in the output (mode 0600). The ticket is never printed.
         The ticket is for the writer unless --viewer is given.
status   Print the holder's status.
release-writer
         Remove the current writer.
stop     Ask the holder to stop; stop its unit if it is still active after 4 s.

start and ticket first check that the holder is the build of this command:
the same module version, VCS revision and modified flag, read from the holder
binary before start and from the build the running holder recorded in its
holder.json before ticket. A holder of another build, one that records no
build (an older holder) and one whose build cannot be read are refused with
"refused: holder-version-skew: ...", which names both builds, and nothing is
started or attached. Build both from one checkout.

Exit status: 0 done, 1 failed or refused, 2 wrong usage.
`

// limits are the waits of the command; tests shorten them.
type limits struct {
	lockWait  time.Duration // for the daemon-lock file and the epoch file
	exchange  time.Duration // one control conversation
	startWait time.Duration // start waits for the holder to answer
	startPoll time.Duration
	stopWait  time.Duration // stop waits for the unit to end before it stops it
	stopPoll  time.Duration
	termWait  time.Duration // a direct holder that is told to end
}

func defaultLimits() limits {
	return limits{
		lockWait:  5 * time.Second,
		exchange:  5 * time.Second,
		startWait: 5 * time.Second,
		startPoll: 50 * time.Millisecond,
		stopWait:  4 * time.Second,
		stopPoll:  100 * time.Millisecond,
		termWait:  3 * time.Second,
	}
}

// app is the command with what it needs from the outside, so that a test can
// give it a fake launcher, an environment and short waits.
type app struct {
	out, errout io.Writer
	getenv      func(string) string
	executable  func() (string, error)
	uid         int
	launch      launcher
	lim         limits
	// signals, when set, replaces the real signal subscription: it returns the
	// channel of SIGINT, SIGTERM and SIGHUP and a function that stops it.
	signals func() (<-chan os.Signal, func())
	// build is the holder build this command expects and how it reads a
	// holder's; nil is this command's own build (buildid.NewChecker).
	build *buildid.Checker
}

// checker returns the build check of the command.
func (a *app) checker() (*buildid.Checker, error) {
	if a.build != nil {
		return a.build, nil
	}
	return buildid.NewChecker()
}

// buildRefusal checks the holder's build with check and, when it is not the
// expected one, prints the refusal and returns the failure status; done is
// false when the holder is the expected build.
func (a *app) buildRefusal(check func(*buildid.Checker) error) (code int, done bool) {
	ch, err := a.checker()
	if err != nil {
		return a.fail("%v", err), true
	}
	if err := check(ch); err != nil {
		fmt.Fprintf(a.errout, "refused: %v\n", err)
		return exitFail, true
	}
	return 0, false
}

// notifySignals subscribes to the signals that a --direct start forwards to
// the holder.
func (a *app) notifySignals() (<-chan os.Signal, func()) {
	if a.signals != nil {
		return a.signals()
	}
	c := make(chan os.Signal, 4)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	return c, func() { signal.Stop(c) }
}

func (a *app) usage(w io.Writer) { fmt.Fprint(w, usageText) }

// usageFail prints a short diagnostic, then the usage, and returns the usage
// exit status. The diagnostic never repeats the text of an argument.
func (a *app) usageFail(msg string) int {
	fmt.Fprintln(a.errout, "error: "+msg)
	a.usage(a.errout)
	return exitUsage
}

func (a *app) fail(format string, args ...any) int {
	fmt.Fprintf(a.errout, "error: "+format+"\n", args...)
	return exitFail
}

func (a *app) run(args []string) int {
	if len(args) == 0 {
		return a.usageFail("expected a subcommand")
	}
	switch args[0] {
	case "-h", "--help", "help":
		a.usage(a.out)
		return exitOK
	case "start":
		cfg, err := parseStart(args[1:], a.getenv)
		if code, done := a.parseResult(err); done {
			return code
		}
		return a.cmdStart(cfg)
	case "ticket":
		cfg, err := parseTicket(args[1:])
		if code, done := a.parseResult(err); done {
			return code
		}
		return a.cmdTicket(cfg)
	case "status":
		name, err := parseNamed("status", args[1:], nil)
		if code, done := a.parseResult(err); done {
			return code
		}
		return a.cmdStatus(name)
	case "release-writer":
		name, err := parseNamed("release-writer", args[1:], nil)
		if code, done := a.parseResult(err); done {
			return code
		}
		return a.cmdReleaseWriter(name)
	case "stop":
		name, err := parseNamed("stop", args[1:], nil)
		if code, done := a.parseResult(err); done {
			return code
		}
		return a.cmdStop(name)
	}
	return a.usageFail("unknown subcommand")
}

// parseResult turns a parse error into an exit status; done is false when the
// arguments were good.
func (a *app) parseResult(err error) (code int, done bool) {
	switch e := err.(type) {
	case nil:
		return 0, false
	case helpRequest:
		a.usage(a.out)
		return exitOK, true
	case usageError:
		return a.usageFail(string(e)), true
	default:
		return a.usageFail("invalid arguments"), true
	}
}

func main() {
	a := &app{
		out:        os.Stdout,
		errout:     os.Stderr,
		getenv:     os.Getenv,
		executable: os.Executable,
		uid:        os.Getuid(),
		launch:     execLauncher{},
		lim:        defaultLimits(),
	}
	os.Exit(a.run(os.Args[1:]))
}
