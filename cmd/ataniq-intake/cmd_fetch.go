package main

import (
	"errors"
	"flag"
	"time"

	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/mirror"
)

// positiveDuration refuses a time limit that was given and is not positive.
func positiveDuration(name string, d time.Duration, set bool) error {
	if set && d <= 0 {
		return usagef("--%s must be positive", name)
	}
	return nil
}

// cmdFetch fetches a lead's branches into the mirror.
func (e *env) cmdFetch(args []string) int {
	fs := flagSet("fetch")
	var c common
	c.bind(fs)
	var (
		role, sandbox                   string
		timeout                         time.Duration
		maxSource, maxStaged, maxMirror int64
	)
	fs.StringVar(&role, "role", "", "")
	fs.StringVar(&sandbox, "sandbox", "", "")
	fs.DurationVar(&timeout, "timeout", 0, "")
	fs.Int64Var(&maxSource, "max-source", 0, "")
	fs.Int64Var(&maxStaged, "max-staged", 0, "")
	fs.Int64Var(&maxMirror, "max-mirror", 0, "")
	if code, done := e.parse("fetch", fs, args); done {
		return code
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if err := e.check(c); err != nil {
		return e.fail("fetch", err)
	}
	if err := checkRole(role); err != nil {
		return e.fail("fetch", err)
	}
	if err := positiveDuration("timeout", timeout, given["timeout"]); err != nil {
		return e.fail("fetch", err)
	}
	for _, s := range []struct {
		name string
		v    int64
	}{{"max-source", maxSource}, {"max-staged", maxStaged}, {"max-mirror", maxMirror}} {
		if given[s.name] && s.v <= 0 {
			return e.fail("fetch", usagef("--%s must be positive", s.name))
		}
	}
	planner, err := e.planner(sandbox, c.state)
	if err != nil {
		return e.fail("fetch", err)
	}
	m, err := e.mirrorOf(c)
	if err != nil {
		return e.fail("fetch", err)
	}
	rep, err := m.Fetch(e.ctx, mirror.FetchRequest{
		StateRoot: c.state,
		Role:      role,
		Project:   c.project,
		Planner:   planner,
		Limits: mirror.FetchLimits{
			Timeout:   timeout,
			MaxSource: maxSource,
			MaxStaged: maxStaged,
			MaxMirror: maxMirror,
		},
	})
	// A mirror over its limit after the move comes back with the report:
	// the refs have moved, so the owner sees what moved.
	var le *mirror.LimitError
	moved := err == nil || (errors.As(err, &le) && le.What == "mirror" && rep.MirrorAfter > 0)
	if moved {
		e.printFetch(role, rep)
	}
	if err != nil {
		return e.fail("fetch", err)
	}
	return exitOK
}

// printFetch prints the report of a fetch.
func (e *env) printFetch(role string, rep mirror.FetchReport) {
	p := &printer{w: e.stdout}
	p.f("Fetched the branches of the lead %s into the mirror.", quote(role))
	p.f("Lead object store (the lead's own statement): %d bytes", rep.Source)
	p.f("Mirror before: %d bytes", rep.MirrorBefore)
	p.f("Added by this fetch: %d bytes", rep.Staged)
	p.f("Mirror after: %d bytes", rep.MirrorAfter)
	p.f("Duration: %s", rep.Duration.Round(time.Millisecond))
	p.f("%s", heading("Ref updates"))
	for _, u := range rep.Updates {
		ref := leadtext.Escape(u.Ref)
		switch {
		case u.Old == "":
			p.f("%screated %s %s", indent, ref, u.New)
		case u.New == "":
			p.f("%spruned %s (was %s)", indent, ref, u.Old)
		default:
			p.f("%supdated %s %s -> %s", indent, ref, u.Old, u.New)
		}
	}
	if len(rep.Updates) == 0 {
		p.f("%s(none)", indent)
	}
	if len(rep.OutsideChanged) > 0 {
		p.f("Refs outside the lead's namespace that changed meanwhile (another intake or a push, not this fetch):")
		for _, n := range rep.OutsideChanged {
			p.f("%s%s", indent, safeText(n))
		}
	}
}
