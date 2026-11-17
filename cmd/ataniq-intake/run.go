package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake"
	"ataniq/internal/intake/verify"
)

// The exit statuses of the command.
const (
	exitOK      = 0
	exitRefused = 1 // a refusal that the owner can act on
	exitUsage   = 2
	exitFailed  = 3 // any other failure
)

// program is the name in messages.
const program = "ataniq-intake"

// deps is what run needs from the outside. Tests give fakes.
type deps struct {
	// Git is the git executable. A zero Git means none was found.
	Git gitenv.Git
	// LoadPlanner reads the sandbox template at a path and returns the
	// planner it describes.
	LoadPlanner func(path string) (sandboxplan.Planner, error)
	// Runner starts the verification command. Nil means verify.ExecRunner.
	Runner verify.Runner
	// Now gives the time of an intake record. Nil means time.Now.
	Now func() time.Time
	// Integrate moves the upstream branch. Nil means intake.Integrate. Tests
	// give a wrapper that adds the warnings and errors that the real call
	// returns only when something fails that a test cannot make fail.
	Integrate func(context.Context, intake.Source, intake.IntegrateRequest) (intake.IntakeRecord, error)
	// PutVerification stores a verification record. Nil means
	// (*intake.Store).PutVerification. Tests use it as Integrate.
	PutVerification func(*intake.Store, string, verify.Result, []verify.Gate) (intake.VerificationRecord, error)
}

// integrate calls the integration of the dependencies.
func (d deps) integrate(ctx context.Context, src intake.Source, req intake.IntegrateRequest) (intake.IntakeRecord, error) {
	if d.Integrate != nil {
		return d.Integrate(ctx, src, req)
	}
	return intake.Integrate(ctx, src, req)
}

// putVerification stores a verification record through the dependencies.
func (d deps) putVerification(st *intake.Store, project string, res verify.Result, gates []verify.Gate) (intake.VerificationRecord, error) {
	if d.PutVerification != nil {
		return d.PutVerification(st, project, res, gates)
	}
	return st.PutVerification(project, res, gates)
}

// env is what one call of run works with.
type env struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
	deps   deps
}

// usageError is an error in the arguments. It ends in exit status 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// run is the whole command. It never reads stdin: the command is not
// interactive and the sign-off is what the owner types as arguments.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, d deps) int {
	_ = stdin
	e := &env{ctx: ctx, stdout: stdout, stderr: stderr, deps: d}
	if len(args) == 0 {
		fmt.Fprint(stdout, usageText)
		return exitOK
	}
	name, rest := args[0], args[1:]
	switch name {
	case "help", "-h", "-help", "--help":
		return e.help(rest)
	}
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(stderr, "%s: unknown subcommand %s\nRun '%s help' for the usage.\n", program, quote(name), program)
		return exitUsage
	}
	return cmd(e, args[1:])
}

// commands maps a subcommand to its function.
var commands = map[string]func(e *env, args []string) int{
	"init":      (*env).cmdInit,
	"fetch":     (*env).cmdFetch,
	"branches":  (*env).cmdBranches,
	"review":    (*env).cmdReview,
	"verify":    (*env).cmdVerify,
	"integrate": (*env).cmdIntegrate,
}

// help prints the usage of the command or of one subcommand.
func (e *env) help(args []string) int {
	switch len(args) {
	case 0:
		fmt.Fprint(e.stdout, usageText)
		return exitOK
	case 1:
		if u, ok := subUsage[args[0]]; ok {
			fmt.Fprint(e.stdout, u)
			return exitOK
		}
	}
	fmt.Fprintf(e.stderr, "%s: help: no such subcommand\nRun '%s help' for the usage.\n", program, program)
	return exitUsage
}

// listFlag is a flag that may be repeated.
type listFlag []string

func (l *listFlag) String() string     { return "" }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// flagSet makes the flag set of a subcommand. The flag package prints
// nothing: this file prints every diagnostic itself.
func flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(program+" "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// common holds the two flags that every subcommand has.
type common struct{ state, project string }

func (c *common) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.state, "state", "", "")
	fs.StringVar(&c.project, "project", "", "")
}

// parse parses the arguments of a subcommand. When done is true the caller
// returns code at once: for -h the usage is printed and the code is 0, for an
// error in the flags the diagnostic is printed and the code is 2.
func (e *env) parse(name string, fs *flag.FlagSet, args []string) (code int, done bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(e.stdout, subUsage[name])
			return exitOK, true
		}
		return e.fail(name, usagef("%s", err.Error())), true
	}
	if fs.NArg() > 0 {
		return e.fail(name, usagef("unexpected argument %s", quote(fs.Arg(0)))), true
	}
	return 0, false
}

// need returns a usage error that names the flags that are required and
// empty. The pairs are a flag name and its value.
func need(pairs ...string) error {
	var missing []string
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] == "" {
			missing = append(missing, "--"+pairs[i])
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return usagef("missing required flags: %s", joinWords(missing))
}

func joinWords(w []string) string {
	out := ""
	for i, s := range w {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
