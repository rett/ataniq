//go:build linux && amd64

// Command qualify is the opt-in harness of the lead sandbox proposal. It is
// for a coordinator, on a host the coordinator chose: with --run it launches
// the generated plan for real (pasta, bubblewrap, the probe and a scratch
// command) and records facts in a private campaign folder. Without --run it
// validates, builds and verifies the plan, prints it with its digest and
// starts nothing.
//
// It says what happened, never that the profile passed, is qualified or is
// supported. Nobody runs it as part of the repository's checks: see
// docs/qualification-lead-sandbox.md.
//
// Usage:
//
//	qualify --config FILE [--run [--unattended]]
//
// Exit status: 0 when every step did its work, 3 when a step failed, timed
// out or was interrupted (the evidence is still written), 2 for a usage
// error, a refused configuration or a closed gate, 4 when evidence could not
// be written.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"ataniq/qualification/leadsandbox/internal/harness"
)

// deps are the parts a test replaces.
type deps struct {
	launcher        harness.Launcher
	stdinIsTerminal func() bool
	options         func(*harness.Options)
}

const (
	exitOK       = 0
	exitStep     = 3
	exitUsage    = 2
	exitEvidence = 4
)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	f := flag.NewFlagSet("qualify", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	config := f.String("config", "", "the configuration file (mode 0600)")
	doRun := f.Bool("run", false, "launch the plan for real (without it: a dry run that starts nothing)")
	unattended := f.Bool("unattended", false, "with --run, do not require a terminal on standard input")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *config == "" {
		fmt.Fprintln(stderr, "usage: qualify --config FILE [--run [--unattended]]")
		return exitUsage
	}
	if *unattended && !*doRun {
		fmt.Fprintln(stderr, "qualify: --unattended applies to --run only")
		return exitUsage
	}
	cfg, err := harness.LoadConfig(*config)
	if err != nil {
		fmt.Fprintf(stderr, "qualify: %v\n", err)
		return exitUsage
	}
	opt := harness.Options{Run: *doRun, Unattended: *unattended, Launcher: d.launcher, StdinIsTerminal: d.stdinIsTerminal}
	if d.options != nil {
		d.options(&opt)
	}
	out, err := harness.Execute(ctx, cfg, opt)
	if out == nil {
		fmt.Fprintf(stderr, "qualify: %v\n", err)
		var start *harness.StartError
		if errors.As(err, &start) && start.Step == harness.StepAttempt {
			return exitEvidence
		}
		return exitUsage
	}
	report(stdout, out)
	if err != nil {
		var gate *harness.GateError
		if errors.As(err, &gate) {
			fmt.Fprintf(stderr, "qualify: %v\n", err)
			return exitUsage
		}
		fmt.Fprintf(stderr, "qualify: evidence: %v\n", err)
		return exitEvidence
	}
	if out.StepFailed {
		return exitStep
	}
	return exitOK
}

// report prints what was done. A dry run prints the plan and its digest.
func report(w io.Writer, o *harness.Outcome) {
	if o.DryRun {
		fmt.Fprintln(w, "dry run: nothing was started")
		if len(o.Plan.Argv) > 0 {
			data, _ := json.MarshalIndent(o.Plan, "", "  ")
			fmt.Fprintf(w, "%s\n", data)
			fmt.Fprintf(w, "digest: %s\n", o.Plan.Digest)
		}
	}
	fmt.Fprintf(w, "attempt folder: %s\n", o.Dir)
	for _, s := range o.Summary.Steps {
		line := fmt.Sprintf("step %s: %s", s.Name, s.Status)
		if s.Error != "" {
			line += ": " + s.Error
		}
		fmt.Fprintln(w, line)
	}
	if !o.DryRun {
		fmt.Fprintf(w, "report: %s\n", o.Summary.Report)
		fmt.Fprintf(w, "exit: launched %t, code %d, signal %q\n", o.Summary.Exit.Launched, o.Summary.Exit.Code, o.Summary.Exit.Signal)
	}
}

func stdinIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, deps{launcher: harness.ExecLauncher{}, stdinIsTerminal: stdinIsTerminal})
	stop()
	os.Exit(code)
}
