package main

import (
	"errors"
	"flag"
	"strings"
	"time"

	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake"
	"ataniq/internal/intake/verify"
	"ataniq/internal/mirror"
)

// The tail of the output of a run that verify prints.
const (
	tailLines     = 40
	tailLineBytes = 400
)

// parseGate reads one --gate argument, NAME=COMMAND. The command runs as
// /bin/sh -c COMMAND inside the verification sandbox.
func parseGate(v string) (verify.Gate, error) {
	name, command, ok := strings.Cut(v, "=")
	if !ok || !sandboxplan.ValidName(name) {
		return verify.Gate{}, usagef("--gate: write NAME=COMMAND, where NAME is a lower-case letter or digit followed by letters, digits or dashes")
	}
	if command == "" || strings.ContainsAny(command, "\x00\n") {
		return verify.Gate{}, usagef("--gate %s: the command must not be empty or hold a NUL byte or a newline", name)
	}
	return verify.Gate{Name: name, Argv: []string{"/bin/sh", "-c", command}}, nil
}

// cmdVerify runs the gates on a lead branch in the verification sandbox and
// stores the result.
func (e *env) cmdVerify(args []string) int {
	fs := flagSet("verify")
	var c common
	c.bind(fs)
	var (
		role, branch, sandbox, records, work string
		gates                                listFlag
		timeout                              time.Duration
	)
	fs.StringVar(&role, "role", "", "")
	fs.StringVar(&branch, "branch", "", "")
	fs.Var(&gates, "gate", "")
	fs.StringVar(&sandbox, "sandbox", "", "")
	fs.StringVar(&records, "records", "", "")
	fs.StringVar(&work, "work", "", "")
	fs.DurationVar(&timeout, "timeout", 0, "")
	if code, done := e.parse("verify", fs, args); done {
		return code
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if err := e.check(c); err != nil {
		return e.fail("verify", err)
	}
	if err := checkRole(role); err != nil {
		return e.fail("verify", err)
	}
	b, err := branchArg("branch", branch)
	if err != nil {
		return e.fail("verify", err)
	}
	if len(gates) == 0 {
		return e.fail("verify", usagef("at least one --gate NAME=COMMAND is required"))
	}
	var plan []verify.Gate
	for _, g := range gates {
		gate, err := parseGate(g)
		if err != nil {
			return e.fail("verify", err)
		}
		plan = append(plan, gate)
	}
	if err := need("records", records, "work", work); err != nil {
		return e.fail("verify", err)
	}
	if err := positiveDuration("timeout", timeout, given["timeout"]); err != nil {
		return e.fail("verify", err)
	}
	if err := owned("records", records, c.state); err != nil {
		return e.fail("verify", err)
	}
	if err := owned("work", work, c.state); err != nil {
		return e.fail("verify", err)
	}
	planner, err := e.planner(sandbox, c.state)
	if err != nil {
		return e.fail("verify", err)
	}
	tipRef, err := mirror.LeadRef(role, b)
	if err != nil {
		return e.fail("verify", usagef("--branch: not a valid branch name"))
	}
	m, err := e.mirrorOf(c)
	if err != nil {
		return e.fail("verify", err)
	}
	ref, err := m.Resolve(e.ctx, tipRef)
	if err != nil {
		return e.fail("verify", err)
	}
	if ref.Type != "commit" {
		return e.fail("verify", refusalf("the lead branch does not point at a commit"))
	}
	store, err := intake.OpenStore(records)
	if err != nil {
		return e.fail("verify", err)
	}
	defer store.Close()

	res, runErr := verify.Run(e.ctx, verify.Request{
		Project:  c.project,
		Mirror:   m.Path(),
		TipRef:   tipRef,
		Tip:      ref.ID,
		Gates:    plan,
		Planner:  planner,
		Runner:   e.deps.Runner,
		WorkRoot: work,
		Limits:   verify.Limits{Timeout: timeout},
	})
	// A run that started has a result, also when its folders could not be
	// removed afterwards. The result is stored whatever it says: a failure
	// withdraws an earlier pass.
	if res.Started.IsZero() {
		if runErr == nil {
			runErr = errors.New("the run did not start")
		}
		return e.fail("verify", runErr)
	}
	rec, putErr := store.PutVerification(c.project, res, plan)

	p := &printer{w: e.stdout}
	p.f("Verification of a lead branch")
	p.f("%s", heading("Branch"))
	p.f("%s%s/%s", indent, leadtext.Escape(role), leadtext.Escape(b))
	p.f("Tip: %s", res.Tip)
	p.f("Gates: %d, plan digest %s", len(plan), res.PlanDigest)
	if res.Passed {
		p.f("Outcome: pass")
	} else {
		p.f("Outcome: fail")
	}
	p.f("Exit status: %d", res.Exit)
	if res.Stopped != "" {
		p.f("Stopped: %s", safeText(res.Stopped))
	}
	if res.OutputCut {
		p.f("Output: cut")
	}
	p.f("Duration: %s", res.Duration.Round(time.Millisecond))
	if putErr == nil {
		p.f("Stored: a verification record for the tip, output %d bytes, sha256 %s", rec.OutputBytes, rec.OutputSHA256)
	}
	printTail(p, res.Output)
	if p.err != nil {
		return exitFailed
	}
	if putErr != nil {
		return e.fail("verify", putErr)
	}
	if runErr != nil {
		return e.fail("verify", runErr)
	}
	if !res.Passed {
		return exitRefused
	}
	return exitOK
}

// printTail prints the last lines of the output of a run. The output is text
// that a lead influenced. Each line is escaped, cut, and starts with a bar
// after the indent, so that no line of it can be taken for the command's own.
func printTail(p *printer, out []byte) {
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(out) == 0 {
		lines = nil
	}
	skipped := 0
	if len(lines) > tailLines {
		skipped = len(lines) - tailLines
		lines = lines[skipped:]
	}
	p.f("%s", heading("Output tail"))
	for _, l := range lines {
		s, cut := leadtext.Line(l, tailLineBytes)
		if cut {
			s += " [cut]"
		}
		p.f("%s| %s", indent, s)
	}
	if len(lines) == 0 {
		p.f("%s(no output)", indent)
	}
	if skipped > 0 {
		p.f("%d earlier lines are not shown. The whole output is stored beside the record.", skipped)
	}
}
