package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadunit"
	"ataniq/internal/leadunit/systemdmgr"
)

// version is what the daemon reports. The repository's VERSION file cannot be
// embedded from this folder (go:embed does not reach a parent folder), so a
// build that wants the real number sets it with -ldflags "-X main.version=N".
var version = leaddaemon.DefaultVersion

const usage = `usage: ataniqd run [--state-dir DIR] [--runtime-dir DIR]
       ataniqd status [--socket PATH] [--json]
       ataniqd version

This build is read-only. The daemon answers hello and status; it starts,
stops and manages no unit and defines no lead, and no flag changes that.

  run      take the daemon lock, open the registry, reconcile once and serve
           until SIGTERM or SIGINT
           --state-dir DIR    default $XDG_STATE_HOME/ataniq, else
                              $HOME/.local/state/ataniq
           --runtime-dir DIR  folder of daemon.sock; default
                              $XDG_RUNTIME_DIR/ataniq, none without
                              XDG_RUNTIME_DIR
  status   ask a running daemon for its status
           --socket PATH      default $XDG_RUNTIME_DIR/ataniq/daemon.sock
           --json             print the status result as JSON
  version  print the version

Exit codes: 0 done; 2 usage; 10-18 start-up refused (10 config or root, 11
state folder, 12 daemon lock held, 13 lock, 14 registry, 15 unit manager,
16 reconcile, 17 socket, 18 server); status: 20 no daemon, 21 unauthorized,
22 protocol error; 1 anything else.
`

// app is the command with its surroundings, so that tests run it in process.
type app struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	// signals delivers the signals that end the daemon.
	signals <-chan os.Signal
	// newManager builds the unit manager of the daemon.
	newManager func(ctx context.Context) (leadunit.Manager, error)
}

func main() {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	a := &app{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, signals: ch}
	a.newManager = a.systemdManager
	os.Exit(a.run(os.Args[1:]))
}

// systemdManager builds the manager of the systemd user manager. When its
// tools are missing the daemon still runs, with a manager that answers
// nothing, and shows its reconcile as not completed.
func (a *app) systemdManager(context.Context) (leadunit.Manager, error) {
	m, err := systemdmgr.New(systemdmgr.Options{})
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd: the systemd tools are unavailable (%s); the reconcile will be shown as not completed\n",
			leaddaemon.SafeText(err.Error()))
		return leaddaemon.UnavailableManager(err), nil
	}
	return m, nil
}

func (a *app) run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return leaddaemon.ExitUsage
	}
	switch args[0] {
	case "run":
		return a.runCmd(args[1:])
	case "status":
		return a.statusCmd(args[1:])
	case "version":
		if len(args) != 1 {
			fmt.Fprint(a.stderr, usage)
			return leaddaemon.ExitUsage
		}
		fmt.Fprintf(a.stdout, "ataniqd %s\n", version)
		return leaddaemon.ExitOK
	case "-h", "--help":
		fmt.Fprint(a.stdout, usage)
		return leaddaemon.ExitOK
	}
	fmt.Fprint(a.stderr, usage)
	return leaddaemon.ExitUsage
}

// flags parses the arguments of a subcommand, which take no operand. It
// returns the exit code to end with, or -1 to go on.
func (a *app) flags(name string, args []string, define func(*flag.FlagSet)) int {
	fs := flag.NewFlagSet("ataniqd "+name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() { fmt.Fprint(a.stderr, usage) }
	define(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return leaddaemon.ExitOK
		}
		return leaddaemon.ExitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(a.stderr, "ataniqd %s: unexpected argument %q\n", name, leaddaemon.SafeText(fs.Arg(0)))
		fs.Usage()
		return leaddaemon.ExitUsage
	}
	return -1
}

func (a *app) runCmd(args []string) int {
	var stateDir, runtimeDir string
	if code := a.flags("run", args, func(fs *flag.FlagSet) {
		fs.StringVar(&stateDir, "state-dir", "", "state folder")
		fs.StringVar(&runtimeDir, "runtime-dir", "", "runtime folder")
	}); code >= 0 {
		return code
	}
	var err error
	if stateDir == "" {
		if stateDir, err = leaddaemon.DefaultStateDir(a.getenv); err != nil {
			fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
			return leaddaemon.ExitConfig
		}
	}
	if runtimeDir == "" {
		if runtimeDir, err = leaddaemon.DefaultRuntimeDir(a.getenv); err != nil {
			fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
			return leaddaemon.ExitConfig
		}
	}
	err = leaddaemon.Run(context.Background(), leaddaemon.Config{
		StateDir:   stateDir,
		RuntimeDir: runtimeDir,
		NewManager: a.newManager,
		Signals:    a.signals,
		Version:    version,
		Log:        a.stderr,
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
	}
	return leaddaemon.ExitCode(err)
}

func (a *app) statusCmd(args []string) int {
	var socket string
	var asJSON bool
	if code := a.flags("status", args, func(fs *flag.FlagSet) {
		fs.StringVar(&socket, "socket", "", "socket path")
		fs.BoolVar(&asJSON, "json", false, "print the status result as JSON")
	}); code >= 0 {
		return code
	}
	if socket == "" {
		var err error
		if socket, err = leaddaemon.DefaultSocketPath(a.getenv); err != nil {
			fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
			return leaddaemon.ExitUsage
		}
	}
	return leaddaemon.RunStatus(context.Background(), leaddaemon.StatusOptions{Socket: socket, JSON: asJSON}, a.stdout, a.stderr)
}
