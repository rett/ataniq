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
	"strconv"
	"strings"
	"syscall"
	"time"

	"ataniq/internal/leadunit"
	"ataniq/internal/leadunit/systemdmgr"
)

const usage = `usage: leadunit-qualify [--run --state-dir DIR] [--memory-max SIZE]
       [--swap-max SIZE] [--tasks-max N] [--memory-lowered SIZE]

Without --run it prints what it would do and exits 0; it does not touch
systemd. With --run it starts one transient user unit with a unique name,
checks the unit manager against it, prints one JSON report and exits non-zero
on any failed check. --state-dir names an existing folder, owned by the
caller and not readable by others, for the unit-name record, the child's ready
marker and the report file.

The unit is started with memory, swap and task limits. They are read back with
systemctl and from the unit's control group files, the memory limit is lowered
once on the running unit and read back again, and after the stop nothing for
the unit may remain in the user manager's runtime folders. A SIZE is a number
of bytes with an optional suffix K, M or G (powers of 1024).
  --memory-max SIZE      the memory limit (default ` + defaultMemoryMax + `; 256M to 64G)
  --swap-max SIZE        the swap limit (default ` + defaultSwapMax + `, no swap; 0 to 64G)
  --tasks-max N          the task limit (default ` + defaultTasksMax + `; 16 to 65536)
  --memory-lowered SIZE  the memory limit the running unit is lowered to
                         (default ` + defaultMemoryLowered + `; below --memory-max, at least 256M)
`

// The defaults of the limit flags, as the flag text states them. They are the
// values of this command's own run and are not used anywhere else.
const (
	defaultMemoryMax     = "1G"
	defaultSwapMax       = "0"
	defaultTasksMax      = "128"
	defaultMemoryLowered = "768M"
)

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
	// The limit flags as typed; empty means the default of the flag.
	memoryMax, swapMax, tasksMax, memoryLowered string
}

// parseSize reads a number of bytes with an optional suffix K, M or G.
func parseSize(s string) (uint64, error) {
	mult := uint64(1)
	digits := s
	if s != "" {
		if i := strings.IndexByte("KMG", s[len(s)-1]); i >= 0 {
			mult = uint64(1) << (10 * (i + 1))
			digits = s[:len(s)-1]
		}
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || strings.TrimLeft(digits, "0123456789") != "" {
		return 0, fmt.Errorf("%q is not a number of bytes with an optional suffix K, M or G", s)
	}
	if n != 0 && mult > ^uint64(0)/n {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	return n * mult, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// limits resolves the limit flags, with their defaults, into the limits of the
// unit and the memory limit it is lowered to, and checks them against the
// bounds of the unit specification.
func (o options) limits() (l leadunit.Limits, lowered uint64, err error) {
	if l.MemoryMax, err = parseSize(orDefault(o.memoryMax, defaultMemoryMax)); err != nil {
		return l, 0, fmt.Errorf("--memory-max: %w", err)
	}
	if l.SwapMax, err = parseSize(orDefault(o.swapMax, defaultSwapMax)); err != nil {
		return l, 0, fmt.Errorf("--swap-max: %w", err)
	}
	if l.TasksMax, err = strconv.ParseUint(orDefault(o.tasksMax, defaultTasksMax), 10, 64); err != nil {
		return l, 0, fmt.Errorf("--tasks-max: %q is not a number", o.tasksMax)
	}
	if lowered, err = parseSize(orDefault(o.memoryLowered, defaultMemoryLowered)); err != nil {
		return l, 0, fmt.Errorf("--memory-lowered: %w", err)
	}
	// The bounds are the specification's own: the unit would be refused with
	// the same reason, so the command refuses it before it starts anything.
	if err = l.Validate(); err != nil {
		return l, 0, fmt.Errorf("limit flags (--memory-max, --swap-max, --tasks-max): %w", err)
	}
	if lowered < leadunit.MinMemoryMax || lowered >= l.MemoryMax {
		return l, 0, fmt.Errorf("--memory-lowered %d must be at least %d and below --memory-max %d", lowered, leadunit.MinMemoryMax, l.MemoryMax)
	}
	return l, lowered, nil
}

// parseArgs parses the harness arguments. It returns flag.ErrHelp for -h and
// an error for anything it does not take.
func parseArgs(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("leadunit-qualify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.run, "run", false, "start the qualification unit")
	fs.StringVar(&o.stateDir, "state-dir", "", "folder for the records and the report")
	fs.StringVar(&o.memoryMax, "memory-max", "", "memory limit of the unit")
	fs.StringVar(&o.swapMax, "swap-max", "", "swap limit of the unit")
	fs.StringVar(&o.tasksMax, "tasks-max", "", "task limit of the unit")
	fs.StringVar(&o.memoryLowered, "memory-lowered", "", "memory limit the running unit is lowered to")
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
	if _, _, err := o.limits(); err != nil {
		return options{}, err
	}
	return o, nil
}

// managerFactory builds the manager that --run uses. It is called only after
// every argument and the state folder were accepted, and never in a dry run.
type managerFactory func() (lifecycle, error)

// realManager is the real manager of the user's systemd user manager. It also
// gives the harness the host access of the limit checks (see [realLife]).
func realManager() (lifecycle, error) {
	runner, err := systemdmgr.NewExecRunner(systemdmgr.DefaultToolDirs)
	if err != nil {
		return nil, err
	}
	m, err := systemdmgr.New(systemdmgr.Options{Runner: runner})
	if err != nil {
		return nil, err
	}
	return realLife{Manager: m, runner: runner}, nil
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
	l, lowered, _ := o.limits() // parseArgs has accepted them
	fmt.Fprintf(w, `leadunit-qualify: dry run. Nothing is started and systemd is not touched.
With --run --state-dir %s it would:
  1. check that %s is an existing folder of the caller, mode 0700
  2. record the unit name in %s before the start
  3. start the transient user unit ataniq-qualify-<random>.service, which runs
     "<this executable> child --ignore-term --ready-file %s/ready-<random>",
     with the limits MemoryMax=%d, MemorySwapMax=%d and TasksMax=%d
  4. check: start, and the description read back
  5. check: the arguments, among them one with $HOME and one with %%h, reach the child unchanged
  6. check: a second start with the same name is refused
  7. check: Inspect matches the start
  8. check: "systemctl --user show" reads the three limits as specified, normalised to bytes and counts
  9. check: memory.max, memory.swap.max and pids.max in the unit's control group folder read as specified
 10. lower the memory limit once to %d with "systemctl --user set-property --runtime"; check: it is
     read back from "systemctl --user show" and from memory.max
 11. start a second unit, ataniq-qualify-<random>-child.service, bound to the first (BindsTo, PartOf
     and After), which runs "<this executable> child"; check: it is active and Inspect reads the
     first unit back as its parent
 12. check: SIGTERM to the first unit, whose process ignores it, leaves the control group populated
 13. wait 2 s, send SIGKILL, check: Empty becomes true and the unit is not loaded
 14. check: the bound child unit stops with its parent (it is not loaded, with no signal sent to it)
     and Empty is true for its control group
 15. check: nothing for the two units remains under systemd/transient and systemd/user.control of the
     user's runtime folder
 16. check: Empty is true for a fresh, never-used unit name (no invocation, no group)
 17. always clean up: SIGKILL its own units (never a unit whose description is not this run's
     intent) and report what was and was not sent and whether the groups were proven empty
 18. print one JSON report with a line per check and a verdict; exit non-zero on any failure
Not attempted: a lost start reply.
`, dir, dir, dir, dir, l.MemoryMax, l.SwapMax, l.TasksMax, lowered)
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
	limits, lowered, err := o.limits()
	if err != nil {
		fmt.Fprintf(stderr, "leadunit-qualify: %v\n", err)
		return exitUsage
	}
	q := &qualifier{
		mgr: mgr, sleep: realSleep, ready: waitReadyFile, rand: randomHex,
		exe: exe, stateDir: o.stateDir,
		limits: &limits, lowered: lowered,
		cgroupRoot: cgroupRoot, runtimeDir: userRuntimeDir(), pageSize: os.Getpagesize(),
	}
	// A manager that does not give host access leaves the limit checks to fail
	// with that reason, never to run against the machine by another way.
	if h, ok := mgr.(host); ok {
		q.host = h
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
