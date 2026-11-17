package main

import (
	"errors"
	"fmt"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/intake"
)

// digestLen is the length of a review digest: a hex SHA-256.
const digestLen = 64

// cmdIntegrate moves the upstream branch to a reviewed, verified lead tip.
func (e *env) cmdIntegrate(args []string) int {
	fs := flagSet("integrate")
	var c common
	c.bind(fs)
	var (
		role, branch, target, tip, digest, records string
		acceptChange                               bool
		acceptPaths, rules                         listFlag
	)
	fs.StringVar(&role, "role", "", "")
	fs.StringVar(&branch, "branch", "", "")
	fs.StringVar(&target, "target", "", "")
	fs.StringVar(&tip, "tip", "", "")
	fs.StringVar(&digest, "digest", "", "")
	fs.BoolVar(&acceptChange, "accept-change", false, "")
	fs.Var(&acceptPaths, "accept-path", "")
	fs.Var(&rules, "path-rule", "")
	fs.StringVar(&records, "records", "", "")
	if code, done := e.parse("integrate", fs, args); done {
		return code
	}
	if err := e.check(c); err != nil {
		return e.fail("integrate", err)
	}
	if err := checkRole(role); err != nil {
		return e.fail("integrate", err)
	}
	b, err := branchArg("branch", branch)
	if err != nil {
		return e.fail("integrate", err)
	}
	t, err := branchArg("target", target)
	if err != nil {
		return e.fail("integrate", err)
	}
	if err := need("tip", tip, "digest", digest, "records", records); err != nil {
		return e.fail("integrate", err)
	}
	if !gitenv.IsObjectID(tip) {
		return e.fail("integrate", usagef("--tip must be the full commit id that review printed"))
	}
	if !isDigest(digest) {
		return e.fail("integrate", usagef("--digest must be the review digest that review printed"))
	}
	var paths []string
	for _, a := range acceptPaths {
		p, err := leadtext.Unescape(a)
		if err != nil {
			return e.fail("integrate", usagef("--accept-path: not a path as printed by review: %s", quote(a)))
		}
		paths = append(paths, p)
	}
	set, err := ruleSet(rules)
	if err != nil {
		return e.fail("integrate", err)
	}
	tipRef, baseRef, err := refs(role, b, t)
	if err != nil {
		return e.fail("integrate", err)
	}
	if err := owned("records", records, c.state); err != nil {
		return e.fail("integrate", err)
	}
	m, err := e.mirrorOf(c)
	if err != nil {
		return e.fail("integrate", err)
	}
	store, err := intake.OpenStore(records)
	if err != nil {
		return e.fail("integrate", err)
	}
	defer store.Close()

	rec, err := e.deps.integrate(e.ctx, sourceOf(e, m), intake.IntegrateRequest{
		Project: c.project,
		Review:  intake.Request{BaseRef: baseRef, TipRef: tipRef, Rules: set},
		SignOff: intake.SignOff{Tip: tip, Digest: digest, Change: acceptChange, Paths: paths},
		Store:   store,
		Now:     e.deps.Now,
	})
	var (
		sg *intake.SignOffError
		re *intake.RecordError
	)
	switch {
	case err == nil:
		e.printIntegrated(rec, role, b, t, m.Path())
		return exitOK
	case errors.As(err, &re):
		// The branch moved and the record is missing. The owner must know.
		e.printIntegrated(re.Record, role, b, t, m.Path())
		fmt.Fprintf(e.stderr, "%s integrate: the upstream branch moved, but its intake record was not written. Keep this output: it is the record.\n", program)
		return e.fail("integrate", err)
	case errors.As(err, &sg):
		e.printSignOffError(sg)
		return exitRefused
	case errors.Is(err, intake.ErrNothingToIntegrate):
		fmt.Fprintf(e.stderr, "%s integrate: nothing to integrate: the lead branch %s/%s is at the upstream branch %s already. Nothing was moved.\n",
			program, leadtext.Escape(role), leadtext.Escape(b), leadtext.Escape(t))
		return exitRefused
	case errors.Is(err, intake.ErrOutcomeUnknown):
		fmt.Fprintf(e.stderr, "%s integrate: the update of the upstream branch %s ended unclearly, and the branch could not be read afterwards. It may have moved, and no intake record was written. Look at the upstream branch with '%s branches' before you try again.\n",
			program, leadtext.Escape(t), program)
		fmt.Fprintf(e.stderr, "The error was: %s\n", safeText(err.Error()))
		return exitFailed
	case rec.Tip != "" && (errors.Is(err, intake.ErrRecovered) || errors.Is(err, intake.ErrNotSynced)):
		// The branch moved and the record is written: a success with a
		// warning.
		e.printIntegrated(rec, role, b, t, m.Path())
		e.warnRecorded(err)
		return exitOK
	}
	return e.fail("integrate", err)
}

// warnRecorded says on standard error what is doubtful about a write that
// succeeded: an update that ended unclearly, or a records folder that could
// not be synced. The texts are the command's own.
func (e *env) warnRecorded(err error) {
	if errors.Is(err, intake.ErrRecovered) {
		fmt.Fprintf(e.stderr, "%s integrate: warning: the update of the upstream branch ended unclearly. The branch is at the new tip and the intake is recorded, as printed above. It cannot be told whether this call or another writer moved it.\n", program)
	}
	if errors.Is(err, intake.ErrNotSynced) {
		fmt.Fprintf(e.stderr, "%s: warning: the record was written, but the records folder could not be synced to disk. The record is there, and it may not survive a crash of the machine. Do not repeat the command.\n", program)
	}
}

// isDigest reports whether s is a hex SHA-256 in lower case.
func isDigest(s string) bool {
	if len(s) != digestLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// printSignOffError says everything that is wrong with a sign-off, with the
// exact arguments to add or to remove.
func (e *env) printSignOffError(sg *intake.SignOffError) {
	w := e.stderr
	fmt.Fprintf(w, "%s integrate: refused: the sign-off is incomplete. Nothing was moved.\n", program)
	if sg.TipMismatch {
		fmt.Fprintf(w, "%s- the tip is not the tip of this review: use the --tip that review printed\n", indent)
	}
	if sg.DigestMismatch {
		fmt.Fprintf(w, "%s- the digest is not the digest of this review: use the --digest that review printed\n", indent)
	}
	if sg.ChangeMissing {
		fmt.Fprintf(w, "%s- the change as a whole is not accepted: add --accept-change\n", indent)
	}
	seen := map[string]bool{}
	for _, f := range sg.Missing {
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		fmt.Fprintf(w, "%s- a flagged path is not accepted (%s): add %s\n", indent, f.Kind, acceptArg(f.Path))
	}
	for _, p := range sg.Unknown {
		fmt.Fprintf(w, "%s- a path you accepted is not flagged in this review: remove %s\n", indent, acceptArg(p))
	}
	fmt.Fprintf(w, "Paths above are %s, shown escaped.\n", leadtext.Label)
}

// printIntegrated prints what an integration did.
func (e *env) printIntegrated(rec intake.IntakeRecord, role, branch, target, mirrorPath string) {
	p := &printer{w: e.stdout}
	p.f("Integrated: the upstream branch moved, fast-forward only.")
	p.f("%s", heading("Branches"))
	p.f("%supstream branch: %s", indent, leadtext.Escape(target))
	p.f("%slead branch: %s/%s", indent, leadtext.Escape(role), leadtext.Escape(branch))
	p.f("Old: %s", rec.Base)
	p.f("New: %s", rec.Tip)
	p.f("Record: intake of %s, %d commits, %d changed paths, %d signed paths", utc(rec.Time), rec.Commits, rec.Paths, len(rec.SignedPaths))
	p.f("Review digest: %s", rec.ReviewDigest)
	p.f("Verification: a pass of %s, plan digest %s, output sha256 %s", utc(rec.Verification.Started), rec.Verification.PlanDigest, rec.Verification.OutputSHA256)
	p.f("%s", gatesHeading)
	printGates(p, indent, rec.Verification.Gates)
	if len(rec.SignedPaths) > 0 {
		p.f("%s", heading("Signed paths"))
		for _, sp := range rec.SignedPaths {
			p.f("%s%s", indent, leadtext.Escape(sp))
		}
	}
	p.f("Your working checkout was not touched. It gets the commits with its own fetch from the mirror at %s.", safeText(mirrorPath))
}
