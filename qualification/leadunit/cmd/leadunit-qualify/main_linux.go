//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"ataniq/internal/leadunit/systemdmgr"
)

const usage = `usage: leadunit-qualify [--run --state-dir DIR]

Without --run it prints what it would do and exits 0; it does not touch
systemd. With --run it starts one transient user unit with a unique name,
checks the unit manager against it, prints one JSON report and exits non-zero
on any failed check. --state-dir names an existing folder, owned by the
caller and not readable by others, for the unit-name record, the child's ready
marker and the report file.
`

// Exit codes.
const (
	exitOK      = 0
	exitFailed  = 1
	exitUsage   = 2
	exitRuntime = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// options are the parsed command line of the harness.
type options struct {
	run      bool
	stateDir string
}

// parseArgs parses the harness arguments. It returns flag.ErrHelp for -h and
// an error for anything it does not take.
func parseArgs(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("leadunit-qualify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.run, "run", false, "start the qualification unit")
	fs.StringVar(&o.stateDir, "state-dir", "", "folder for the records and the report")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if o.run && o.stateDir == "" {
		return options{}, errors.New("--run needs --state-dir")
	}
	if o.stateDir != "" && (!filepath.IsAbs(o.stateDir) || filepath.Clean(o.stateDir) != o.stateDir) {
		return options{}, errors.New("--state-dir must be a clean absolute path")
	}
	return o, nil
}

// managerFactory builds the manager that --run uses. It is called only after
// every argument and the state folder were accepted, and never in a dry run.
type managerFactory func() (lifecycle, error)

// realManager is the real manager of the user's systemd user manager.
func realManager() (lifecycle, error) {
	return systemdmgr.New(systemdmgr.Options{})
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWith(args, stdout, stderr, realManager)
}

// runWith is run with the manager factory injected. Every test goes through it
// with a factory that must not be called or that returns a fake, so that no
// test can reach the real manager by construction.
func runWith(args []string, stdout, stderr io.Writer, newManager managerFactory) int {
	if len(args) > 0 && args[0] == "child" {
		return runChild(args[1:], stderr)
	}
	o, err := parseArgs(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, usage)
		return exitOK
	case err != nil:
		fmt.Fprintf(stderr, "leadunit-qualify: %v\n%s", err, usage)
		return exitUsage
	}
	if !o.run {
		dryRun(stdout, o)
		return exitOK
	}
	return runQualification(o, stdout, stderr, newManager)
}

// dryRun prints the plan without touching systemd or the file system.
func dryRun(w io.Writer, o options) {
	dir := "DIR"
	if o.stateDir != "" {
		dir = o.stateDir
	}
	fmt.Fprintf(w, `leadunit-qualify: dry run. Nothing is started and systemd is not touched.
With --run --state-dir %s it would:
  1. check that %s is an existing folder of the caller, mode 0700
  2. record the unit name in %s before the start
  3. start the transient user unit ataniq-qualify-<random>.service, which runs
     "<this executable> child --ignore-term --ready-file %s/ready-<random>"
  4. check: start, and the description read back
  5. check: the arguments, among them one with $HOME and one with %%h, reach the child unchanged
  6. check: a second start with the same name is refused
  7. check: Inspect matches the start
  8. start a second unit, ataniq-qualify-<random>-child.service, bound to the first (BindsTo, PartOf
     and After), which runs "<this executable> child"; check: it is active and Inspect reads the
     first unit back as its parent
  9. check: SIGTERM to the first unit, whose process ignores it, leaves the control group populated
 10. wait 2 s, send SIGKILL, check: Empty becomes true and the unit is not loaded
 11. check: the bound child unit stops with its parent (it is not loaded, with no signal sent to it)
     and Empty is true for its control group
 12. check: Empty is true for a fresh, never-used unit name (no invocation, no group)
 13. always clean up: SIGKILL its own units (never a unit whose description is not this run's
     intent) and report what was and was not sent and whether the groups were proven empty
 14. print one JSON report with a line per check and a verdict; exit non-zero on any failure
Not attempted: a lost start reply.
`, dir, dir, dir, dir)
}

// runQualification runs the checks against the real manager.
func runQualification(o options, stdout, stderr io.Writer, newManager managerFactory) int {
	if err := checkStateDir(o.stateDir); err != nil {
		fmt.Fprintf(stderr, "leadunit-qualify: %v\n", err)
		return exitUsage
	}
	exe, err := os.Executable()
	if err != nil || !filepath.IsAbs(exe) {
		fmt.Fprintln(stderr, "leadunit-qualify: cannot find this executable")
		return exitRuntime
	}
	mgr, err := newManager()
	if err != nil {
		fmt.Fprintf(stderr, "leadunit-qualify: %v\n", err)
		return exitRuntime
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, overallTimeout)
	defer cancel()
	q := &qualifier{
		mgr: mgr, sleep: realSleep, ready: waitReadyFile, rand: randomHex,
		exe: exe, stateDir: o.stateDir,
	}
	rep := q.qualify(ctx)
	if err := writeReport(stdout, rep); err != nil {
		fmt.Fprintf(stderr, "leadunit-qualify: report: %v\n", err)
		return exitRuntime
	}
	if err := q.saveReport(rep); err != nil {
		fmt.Fprintf(stderr, "leadunit-qualify: report file: %v\n", err)
	}
	if rep.Verdict != "pass" {
		return exitFailed
	}
	return exitOK
}

// checkStateDir requires an existing folder owned by the caller that no one
// else can read or write.
func checkStateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("state folder: %w", err)
	}
	if !fi.IsDir() {
		return errors.New("state folder is not a folder")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return errors.New("state folder is not owned by the caller")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return errors.New("state folder must not be accessible to group or others (mode 0700)")
	}
	return nil
}

const overallTimeout = 90 * time.Second
