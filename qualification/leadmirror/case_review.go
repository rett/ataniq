package leadmirror

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/exchange/safefs"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake"
	"ataniq/internal/intake/execconfig"
	"ataniq/internal/intake/verify"
	"ataniq/internal/mirror"
)

// reviewBranch is the lead branch of the review case.
const reviewBranch = "review"

// reviewCommits are the commits of the review branch, one for each kind of
// path the intake looks at, and a last one that it should not flag.
var reviewCommits = []struct {
	msg   string
	files []file
}{
	{"lead: agent settings with a hook", []file{{".claude/settings.json",
		`{"hooks":{"PostToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"echo hook"}]}]}}` + "\n"}}},
	{"lead: mcp servers", []file{{".mcp.json", `{"mcpServers":{"x":{"command":"/bin/true"}}}` + "\n"}}},
	{"lead: codex config", []file{{".codex/config.toml", "[mcp_servers.x]\ncommand = \"/bin/true\"\n"}}},
	{"lead: direnv", []file{{".envrc", "export X=1\n"}}},
	{"lead: editor tasks", []file{{".vscode/tasks.json",
		`{"version":"2.0.0","tasks":[{"label":"x","type":"shell","command":"echo x"}]}` + "\n"}}},
	{"lead: makefile", []file{{"Makefile", "all:\n\t@echo build\n\t@echo lead\n"}}},
	{"lead: script", []file{{"scripts/check.sh", "#!/bin/sh\necho check\necho lead\n"}}},
	{"lead: toolchain line", []file{{"go.mod", "module example.invalid/x\n\ngo 1.21\n\ntoolchain go1.99.0\n"}}},
	{"lead: a note", []file{{"docs/notes.md", "a note\n"}}},
}

func (r *runner) reviewLead(ctx context.Context, dir string) error {
	f := r.fx
	if _, err := f.run(ctx, dir, "checkout", "--quiet", "-b", reviewBranch); err != nil {
		return err
	}
	for _, c := range reviewCommits {
		if err := f.commit(ctx, dir, c.msg, c.files...); err != nil {
			return err
		}
	}
	_, err := f.run(ctx, dir, "checkout", "--quiet", upstreamBranch)
	return err
}

// reviewFacts turns a review into values. Paths are lead text and are
// escaped; the flag's pattern and reason are the package's own text.
func reviewFacts(rev *intake.Review) map[string]any {
	commits := make([]map[string]any, 0, len(rev.Commits))
	for _, c := range rev.Commits {
		commits = append(commits, map[string]any{"id": c.ID, "subject": leadtext.Escape(c.Subject)})
	}
	flagged := make([]map[string]any, 0, len(rev.Flagged))
	flaggedPaths := map[string]bool{}
	for _, fl := range rev.Flagged {
		flagged = append(flagged, map[string]any{
			"kind": string(fl.Kind), "path": leadtext.Escape(fl.Path), "pattern": fl.Pattern, "reason": fl.Reason,
		})
		flaggedPaths[leadtext.Escape(fl.Path)] = true
	}
	changes := make([]map[string]any, 0, len(rev.Changes))
	var unflagged []string
	for _, ch := range rev.Changes {
		p := leadtext.Escape(ch.Path)
		kinds := []string{}
		for _, fl := range ch.Flags {
			kinds = append(kinds, string(fl.Kind))
		}
		changes = append(changes, map[string]any{
			"path": p, "status": string(ch.Status),
			"old_mode": fmt.Sprintf("%o", ch.OldMode), "new_mode": fmt.Sprintf("%o", ch.NewMode),
			"flags": kinds,
		})
		if !flaggedPaths[p] {
			unflagged = append(unflagged, p)
		}
	}
	paths := make([]string, 0, len(flaggedPaths))
	for p := range flaggedPaths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if unflagged == nil {
		unflagged = []string{}
	}
	return map[string]any{
		"base": rev.Base, "tip": rev.Tip,
		"commits": commits, "changes": changes,
		"flagged": flagged, "flagged_paths": paths, "unflagged_changed_paths": unflagged,
		"rule_count": len(execconfig.Default().Rules()),
	}
}

// reviewEnv is what the verification case takes over from the review case.
type reviewEnv struct {
	c   *caseEnv
	tip string
	ref string
}

// caseReviewAndVerify is design case S19, and the verification case on its
// mirror.
func (r *runner) caseReviewAndVerify(ctx context.Context) error {
	cs := r.add("s19-review", "Fetch of a lead branch that changes execution-config paths, then the intake review of it.")
	root := filepath.Join(r.opts.StateRoot, "cases", "review")
	c, err := r.newCase(ctx, root, r.reviewLead, false)
	var re *reviewEnv
	if err != nil {
		if herr := cs.harnessError(ctx, err); herr != nil {
			return herr
		}
	} else {
		re, err = r.runReview(ctx, cs, c)
		if err != nil {
			if herr := cs.harnessError(ctx, err); herr != nil {
				return herr
			}
		}
	}
	return r.caseVerify(ctx, re)
}

func (r *runner) runReview(ctx context.Context, cs *Case, c *caseEnv) (*reviewEnv, error) {
	role := r.opts.Role
	before, err := readState(ctx, c.m, role, maxEntries)
	if err != nil {
		return nil, err
	}
	rep, serves, ferr := r.fetch(ctx, c, mirror.FetchLimits{})
	afterFetch, err := readState(ctx, c.m, role, maxEntries)
	if err != nil {
		return nil, err
	}
	cs.fact("fetch", "The result of Mirror.Fetch of the review branch, through the template's serving half.",
		merge(fetchFacts(rep, ferr), map[string]any{"serve_requests": serves, "lead_refs": afterFetch.Lead}))

	tipRef, err := mirror.LeadRef(role, reviewBranch)
	if err != nil {
		return nil, err
	}
	baseRef, err := mirror.UpstreamRef(upstreamBranch)
	if err != nil {
		return nil, err
	}
	rev, ierr := intake.Inspect(ctx, intake.Source{Git: r.opts.Git, GitDir: c.m.Path()}, intake.Request{
		BaseRef: baseRef, TipRef: tipRef, Rules: execconfig.Default(),
	})
	after, err := readState(ctx, c.m, role, maxEntries)
	if err != nil {
		return nil, err
	}
	obs := errorFacts(ierr)
	if rev != nil {
		obs = merge(obs, reviewFacts(rev))
	}
	cs.fact("inspect", "The result of intake.Inspect of refs/leads/<role>/heads/"+reviewBranch+" against refs/heads/"+upstreamBranch+" in the mirror, with the default execution-config rules. Paths are escaped lead text.", obs)
	cs.fact("upstream-refs", "refs/heads/* and refs/tags/* of the mirror before the fetch and after the review.", map[string]any{
		"before":    before.Upstream,
		"after":     after.Upstream,
		"identical": sameRefs(before.Upstream, after.Upstream),
	})
	if ferr != nil || ierr != nil || rev == nil {
		return nil, nil
	}
	return &reviewEnv{c: c, tip: rev.Tip, ref: tipRef}, nil
}

// verifyOutput turns the captured output of a gate run into escaped lines,
// at most 80 lines of 200 bytes.
func verifyOutput(out []byte) []string {
	const maxLines, maxLine = 80, 200
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	res := make([]string, 0, len(lines))
	for _, l := range lines {
		e, _ := leadtext.Line(l, maxLine)
		res = append(res, e)
	}
	return res
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// benignEnv are the names whose value is recorded even when the harness's own
// environment has the same value: a path list, a locale, a terminal type.
var benignEnv = map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "TERM": true, "SHLVL": true}

// gateEnvironment reads the output of the environment gate: the lines after
// its marker line. A value that equals the harness's own environment is not
// recorded (the name is listed in same), unless the name is in benignEnv.
func gateEnvironment(out []byte, host []string) (lines, names, same []string) {
	hostEnv := map[string]string{}
	for _, kv := range host {
		if k, v, ok := strings.Cut(kv, "="); ok {
			hostEnv[k] = v
		}
	}
	marker := verify.MarkerPrefix + "environment"
	all := strings.Split(string(out), "\n")
	start := -1
	for i, l := range all {
		if l == marker {
			start = i + 1
			break
		}
	}
	lines, names, same = []string{}, []string{}, []string{}
	if start < 0 {
		return
	}
	for _, l := range all[start:] {
		if l == "" {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || !envName.MatchString(k) {
			e, _ := leadtext.Line(l, 200)
			lines = append(lines, e)
			continue
		}
		names = append(names, k)
		if hv, found := hostEnv[k]; found && hv == v {
			same = append(same, k)
			if !benignEnv[k] {
				lines = append(lines, k+"=<same value as in the harness's own environment; not recorded>")
				continue
			}
		}
		e, _ := leadtext.Line(l, 200)
		lines = append(lines, e)
	}
	sort.Strings(names)
	sort.Strings(same)
	return
}

// caseVerify runs two verification runs on the review case's mirror: a gate
// that writes a canary outside the scratch folder, and a gate that prints its
// environment. A template without a verify section ends it after the first
// run, with one fact.
func (r *runner) caseVerify(ctx context.Context, re *reviewEnv) error {
	cs := r.add("verification", "Verification of the review branch: gates run in the template's verification sandbox.")
	if re == nil {
		cs.fact("not-run", "The review case did not give a fetched tip to verify.", nil)
		return ctx.Err()
	}
	c := re.c
	canaryFile := filepath.Join(c.canary, "verify-gate")
	run := func(id, detail string, gate verify.Gate) (verify.Result, error) {
		before := r.rec.verifies.Load()
		res, err := verify.Run(ctx, verify.Request{
			Project: r.opts.Project, Mirror: c.m.Path(), TipRef: re.ref, Tip: re.tip,
			Gates: []verify.Gate{gate}, Planner: r.rec,
			WorkRoot: filepath.Join(c.root, "work"),
			Limits:   verify.Limits{Timeout: r.opts.VerifyTimeout},
		})
		if errors.Is(err, sandboxplan.ErrNotConfigured) {
			cs.fact("no-verify-section", "The template has no verify section. No verification ran.", errorFacts(err))
			return res, err
		}
		obs := errorFacts(err)
		if !res.Started.IsZero() {
			obs = merge(obs, map[string]any{
				"exit": res.Exit, "stopped": res.Stopped, "output_cut": res.OutputCut,
				"gate_plan_digest": res.PlanDigest, "verify_requests": r.rec.verifies.Load() - before,
			})
		}
		cs.fact(id, detail, obs)
		return res, err
	}

	gate := verify.Gate{Name: "canary-write", Argv: []string{"/bin/sh", "-c",
		"if : > '" + canaryFile + "'; then echo canary-write-succeeded; else echo canary-write-failed; fi"}}
	res, err := run("canary-gate", "A gate that tries to write a file outside the scratch folder, in the canary folder on the host. The gate always exits 0 and prints what happened; that line is the sandbox's own statement.", gate)
	if err != nil {
		if herr := ctx.Err(); herr != nil {
			return herr
		}
		return nil
	}
	f := &cs.Facts[len(cs.Facts)-1]
	exists, cerr := canaryExists(c.canary, "verify-gate")
	f.Observed["canary_exists_on_host"] = exists
	if cerr != nil {
		f.Observed["canary_check_error"] = leadtext.Escape(cerr.Error())
	}
	f.Observed["output"] = verifyOutput(res.Output)

	gate = verify.Gate{Name: "environment", Argv: []string{"/bin/sh", "-c", "env"}}
	res, err = run("environment-gate", "A gate that prints its environment. A value equal to the harness's own environment is not recorded; its name is listed in same_as_harness_env.", gate)
	if err != nil {
		return ctx.Err()
	}
	f = &cs.Facts[len(cs.Facts)-1]
	lines, names, same := gateEnvironment(res.Output, os.Environ())
	f.Observed["environment"] = lines
	f.Observed["environment_names"] = names
	f.Observed["same_as_harness_env"] = same
	return ctx.Err()
}

// canaryExists reports whether name exists in the canary folder, without
// following a symlink.
func canaryExists(dir, name string) (bool, error) {
	root, err := safefs.OpenRoot(dir)
	if err != nil {
		return false, err
	}
	defer root.Close()
	_, err = root.Lstat(name)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}
