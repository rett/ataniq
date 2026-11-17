package main

import (
	"strings"

	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake"
	"ataniq/internal/intake/execconfig"
)

// Bounds for the commit lines of a review. Paths are never cut: the owner
// types them back.
const (
	maxNameBytes    = 80
	maxSubjectBytes = 200
)

// ruleSet builds the execution-config set with the owner's additions.
func ruleSet(patterns []string) (execconfig.Set, error) {
	s, err := execconfig.Default().With(patterns)
	if err != nil {
		return s, usagef("--path-rule: %v", err)
	}
	return s, nil
}

// cmdReview shows what a lead branch changes and what needs a sign-off.
func (e *env) cmdReview(args []string) int {
	fs := flagSet("review")
	var c common
	c.bind(fs)
	var (
		role, branch, target string
		rules                listFlag
		diff                 bool
	)
	fs.StringVar(&role, "role", "", "")
	fs.StringVar(&branch, "branch", "", "")
	fs.StringVar(&target, "target", "", "")
	fs.Var(&rules, "path-rule", "")
	fs.BoolVar(&diff, "diff", false, "")
	if code, done := e.parse("review", fs, args); done {
		return code
	}
	if err := e.check(c); err != nil {
		return e.fail("review", err)
	}
	if err := checkRole(role); err != nil {
		return e.fail("review", err)
	}
	b, err := branchArg("branch", branch)
	if err != nil {
		return e.fail("review", err)
	}
	t, err := branchArg("target", target)
	if err != nil {
		return e.fail("review", err)
	}
	set, err := ruleSet(rules)
	if err != nil {
		return e.fail("review", err)
	}
	tipRef, baseRef, err := refs(role, b, t)
	if err != nil {
		return e.fail("review", err)
	}
	m, err := e.mirrorOf(c)
	if err != nil {
		return e.fail("review", err)
	}
	src := sourceOf(e, m)
	rev, err := intake.Inspect(e.ctx, src, intake.Request{BaseRef: baseRef, TipRef: tipRef, Rules: set})
	if err != nil {
		return e.fail("review", err)
	}

	p := &printer{w: e.stdout}
	p.f("Review of a lead branch against an upstream branch")
	p.f("%s", heading("Branches"))
	p.f("%slead branch: %s/%s", indent, leadtext.Escape(role), leadtext.Escape(b))
	p.f("%supstream branch: %s", indent, leadtext.Escape(t))
	p.f("Base: %s", rev.Base)
	p.f("Tip: %s", rev.Tip)
	p.f("Digest: %s", rev.Digest())
	if len(rules) > 0 {
		p.f("Path rules added by you (give the same list to integrate):")
		for _, r := range rules {
			p.f("%s%s", indent, leadtext.Escape(r))
		}
	}
	printCommits(p, rev)
	printChanges(p, rev)
	printSignOff(p, rev, rules)
	if diff {
		p.f("%s", heading("Patch of the change"))
		p.f("The first line below is the command's own. The rest is the patch.")
	}
	if p.err != nil {
		return exitFailed
	}
	if diff {
		if err := intake.Diff(e.ctx, src, rev, e.stdout, intake.Limits{}); err != nil {
			return e.fail("review", err)
		}
	}
	return exitOK
}

// printCommits prints the commits of the review.
func printCommits(p *printer, rev *intake.Review) {
	p.f("%s", heading("Commits, newest first"))
	for _, c := range rev.Commits {
		p.f("%s%s  %s  %s <%s>  %s", indent, c.ID, utc(c.AuthorTime),
			capLine(c.AuthorName, maxNameBytes), capLine(c.AuthorEmail, maxNameBytes),
			capLine(c.Subject, maxSubjectBytes))
	}
	if len(rev.Commits) == 0 {
		p.f("%s(none)", indent)
	}
}

// printChanges prints the changed paths with their status and modes.
func printChanges(p *printer, rev *intake.Review) {
	p.f("%s", heading("Changed paths: status, old mode, new mode, path"))
	for _, ch := range rev.Changes {
		p.f("%s%c  %06o  %06o  %s", indent, ch.Status, ch.OldMode, ch.NewMode, leadtext.Escape(ch.Path))
	}
	if len(rev.Changes) == 0 {
		p.f("%s(none)", indent)
	}
}

// printSignOff prints the section of paths that need their own sign-off.
func printSignOff(p *printer, rev *intake.Review, rules []string) {
	paths := signPaths(rev)
	p.f("%s", heading("Needs your sign-off"))
	for _, sp := range paths {
		p.f("%spath: %s", indent, leadtext.Escape(sp.path))
		for _, f := range sp.flags {
			p.f("%s%s%s: %s", indent, indent, f.Kind, safeText(f.Reason))
		}
		p.f("%s%sto accept it, add: %s", indent, indent, acceptArg(sp.path))
	}
	if len(paths) == 0 {
		p.f("%s(none)", indent)
	}
	p.f("To sign off the change as a whole, add: --accept-change")
	p.f("To integrate, give integrate this tip and this digest: --tip %s --digest %s", rev.Tip, rev.Digest())
	if len(rules) > 0 {
		p.f("Give integrate the same --path-rule list.")
	}
}

// signPath is one flagged path with all its flags.
type signPath struct {
	path  string
	flags []intake.Flag
}

// signPaths groups the flags of a review by path, in the order of the review.
func signPaths(rev *intake.Review) []signPath {
	var out []signPath
	for _, f := range rev.Flagged {
		if n := len(out); n > 0 && out[n-1].path == f.Path {
			out[n-1].flags = append(out[n-1].flags, f)
			continue
		}
		out = append(out, signPath{path: f.Path, flags: []intake.Flag{f}})
	}
	return out
}

// acceptArg returns the exact argument to type for a flagged path: the
// escaped path, as one shell word.
func acceptArg(path string) string {
	word, err := sandboxplan.ShellQuote([]string{leadtext.Escape(path)})
	if err != nil {
		// The escaped text holds no NUL byte; this cannot happen.
		word = "'" + strings.ReplaceAll(leadtext.Escape(path), "'", `'\''`) + "'"
	}
	return "--accept-path " + word
}
