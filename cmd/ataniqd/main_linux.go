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
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadunit"
	"ataniq/internal/leadunit/systemdmgr"
)

// version is what the daemon reports. The repository's VERSION file cannot be
// embedded from this folder (go:embed does not reach a parent folder), so a
// build that wants the real number sets it with -ldflags "-X main.version=N".
var version = leaddaemon.DefaultVersion

var usage = baseUsage + canaryUsage

const projectHomeUsage = "ataniqd project-home --state-dir DIR --home self|AUTHORITY [--replace] [--confirm-self] PROJECT"

const baseUsage = `usage: ataniqd run [--state-dir DIR] [--runtime-dir DIR]
       ataniqd init [--state-dir DIR] [--label TEXT | --restored [--leave-old-journal]]
       ` + projectHomeUsage + `
       ataniqd status [--socket PATH] [--json]
       ataniqd version

This build is read-only. The daemon answers hello and status; it starts,
stops and manages no unit and defines no lead, and no flag changes that.

  init     initialize the state root and bind its registry to this instance
           --label TEXT       optional display label on first initialization
           --restored         give a restored key a new store incarnation
           --leave-old-journal  acknowledge the old journal will be unusable
  project-home   enroll or correct a project home with the daemon stopped
                 --home self uses this root's authority ID
  run      take the daemon lock and the runtime folder lock, open the
           registry, reconcile once and serve until SIGTERM or SIGINT
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
16 reconcile, 17 socket, also another daemon serving the same runtime folder,
18 server); 19 the server did not stop within the shutdown grace (the registry
and the locks are left to the end of the process);
status: 20 no daemon, 21 unauthorized, 22 protocol error; 1 anything else.
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
	args := os.Args[1:]
	os.Exit(newApp(args).run(args))
}

// newApp is the command as main runs it: the real surroundings and the
// manager of the systemd user manager. Only the run command takes the
// termination signals (SIGTERM and SIGINT end the daemon); status and version
// keep the default, so that Ctrl-C ends them at once.
func newApp(args []string) *app {
	a := &app{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}
	a.newManager = a.systemdManager
	if len(args) > 0 && args[0] == "run" {
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		a.signals = ch
	}
	return a
}

// systemdManager builds the manager of the systemd user manager. When its
// tools are missing the daemon still runs, with a manager that answers
// nothing, and shows its reconcile as not completed.
func (a *app) systemdManager(context.Context) (leadunit.Manager, error) {
	m, err := systemdmgr.New(systemdmgr.Options{})
	if err != nil {
		return a.unavailable(err), nil
	}
	return m, nil
}

// unavailable is the manager of a daemon that cannot ask systemd: it says so
// on standard error and answers every call with an error. The daemon shows its
// reconcile as not completed for such a manager whatever the registry holds,
// also an empty one, so what the line says is true in every case.
func (a *app) unavailable(cause error) leadunit.Manager {
	fmt.Fprintf(a.stderr, "ataniqd: the systemd tools are unavailable (%s); the reconcile will be shown as not completed\n",
		leaddaemon.SafeText(cause.Error()))
	return leaddaemon.UnavailableManager(cause)
}

func (a *app) run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return leaddaemon.ExitUsage
	}
	switch args[0] {
	case "init":
		return a.initCmd(args[1:])
	case "project-home":
		return a.projectHomeCmd(args[1:])
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
	// The command that only a build with the ataniq_ownerchannel tag has.
	if code, ok := a.seatGateCommand(args); ok {
		return code
	}
	// The desk's genesis command, which only a build with the
	// ataniq_ownerchannel tag has too.
	if code, ok := a.genesisCommand(args); ok {
		return code
	}
	// The phone trial's command, which only a build with the
	// ataniq_ownerchannel tag has too.
	if code, ok := a.trialCommand(args); ok {
		return code
	}
	// Commands and options that only a build with the ataniq_canary tag has.
	if code, ok := a.canaryCommand(args); ok {
		return code
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

func (a *app) runConfig(args []string) (cfg leaddaemon.Config, owner ownerFlags, code int) {
	var stateDir, runtimeDir string
	var canary canaryFlags
	if code := a.flags("run", args, func(fs *flag.FlagSet) {
		fs.StringVar(&stateDir, "state-dir", "", "state folder")
		fs.StringVar(&runtimeDir, "runtime-dir", "", "runtime folder")
		canary.define(fs)
		owner.define(fs)
	}); code >= 0 {
		return cfg, owner, code
	}
	// Flags that exclude each other are refused before any file is read.
	if code := a.trialCheck(&owner, &canary); code >= 0 {
		return cfg, owner, code
	}
	cfg = leaddaemon.Config{
		NewManager: a.newManager,
		Signals:    a.signals,
		Version:    version,
		Log:        a.stderr,
	}
	// With --canary (a build with the tag only) the roots are the config's.
	applied, code := a.canaryRun(&canary, stateDir, runtimeDir, &cfg)
	if code >= 0 {
		return cfg, owner, code
	}
	var err error
	if !applied {
		if stateDir == "" {
			if stateDir, err = leaddaemon.DefaultStateDir(a.getenv); err != nil {
				fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
				return cfg, owner, leaddaemon.ExitConfig
			}
		}
		if runtimeDir == "" {
			if runtimeDir, err = leaddaemon.DefaultRuntimeDir(a.getenv); err != nil {
				fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
				return cfg, owner, leaddaemon.ExitConfig
			}
		}
		cfg.StateDir, cfg.RuntimeDir = stateDir, runtimeDir
	}
	return cfg, owner, -1
}

func (a *app) runCmd(args []string) int {
	cfg, owner, code := a.runConfig(args)
	if code >= 0 {
		return code
	}
	// With --owner-channel (a build with the ataniq_ownerchannel tag only) the
	// daemon serves the owner connection.
	if code := a.ownerRun(&owner, &cfg); code >= 0 {
		return code
	}
	err := a.requireInstance(cfg, nil)
	if err == nil {
		err = leaddaemon.Run(context.Background(), cfg)
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(startupMessage(err, cfg.StateDir)))
	}
	return leaddaemon.ExitCode(err)
}

func startupMessage(err error, state string) string {
	message := err.Error()
	var start *leaddaemon.StartupError
	if errors.As(err, &start) && start.Step == leaddaemon.StepRegistry {
		message = fmt.Sprintf("registry: %s: %v", journalPath(state), start.Err)
		if errors.Is(err, leadregistry.ErrForeignInstance) {
			message += "; follow the restore procedure in " + restoreGuide
		}
		if errors.Is(err, leadregistry.ErrUnbound) {
			message = fmt.Sprintf("registry: %s has no instance binding; run %s", journalPath(state), initHint(state))
		}
	}
	return message
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
