package leadmirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWithFakePlanner(t *testing.T) {
	rep, o := runFake(t, fakePlanner(t))
	if rep.Format != ReportFormat || rep.Role != "coder" || rep.Project != "proj" || rep.Template.SHA256 != "test-digest" {
		t.Errorf("report header: %+v", rep)
	}
	if !strings.HasPrefix(rep.Tools.Git, "git version ") || !strings.HasPrefix(rep.Tools.Go, "go") || rep.Tools.GOOS == "" || rep.Tools.GOARCH == "" {
		t.Errorf("tools: %+v", rep.Tools)
	}
	var ids []string
	for _, c := range rep.Cases {
		ids = append(ids, c.ID)
		if c.Title == "" || len(c.Facts) == 0 {
			t.Errorf("case %s: title %q, %d facts", c.ID, c.Title, len(c.Facts))
		}
		for _, f := range c.Facts {
			if !strings.HasPrefix(f.ID, c.ID+".") || f.Detail == "" || f.Observed == nil {
				t.Errorf("fact %+v", f)
			}
			if f.ID == c.ID+".harness-error" {
				t.Errorf("harness error: %v", f.Observed)
			}
		}
	}
	if got, want := strings.Join(ids, ","), "s16-fetch,oversize,planted,s19-review,verification"; got != want {
		t.Errorf("cases = %s, want %s", got, want)
	}
	// The hostile clone is where the design says.
	for _, p := range []string{"leads/coder/tree/.git/config", "mirrors/proj.git/ataniq-mirror", "canary"} {
		if _, err := os.Lstat(filepath.Join(o.StateRoot, p)); err != nil {
			t.Errorf("state root: %v", err)
		}
	}
}

func TestReportHasNoVerdictField(t *testing.T) {
	rep, _ := runFake(t, fakePlanner(t))
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				low := strings.ToLower(k)
				for _, bad := range []string{"pass", "support", "qualif", "verdict", "approved", "safe"} {
					if strings.Contains(low, bad) {
						t.Errorf("key %q at %s names a verdict", k, path)
					}
				}
				walk(vv, path+"."+k)
			}
		case []any:
			for _, vv := range x {
				walk(vv, path+"[]")
			}
		}
	}
	walk(asJSON(t, rep), "$")
}

func TestHostileCloneIsPlanted(t *testing.T) {
	_, o := runFake(t, fakePlanner(t))
	gitDir := filepath.Join(o.StateRoot, "leads", "coder", "tree", ".git")
	for _, h := range hookNames {
		for _, dir := range []string{"hooks", leadHooksFolder} {
			fi, err := os.Lstat(filepath.Join(gitDir, dir, h))
			if err != nil || fi.Mode().Perm()&0o100 == 0 {
				t.Errorf("hook %s/%s: %v", dir, h, err)
			}
		}
	}
	cfg, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(o.StateRoot, "canary")
	for _, want := range []string{"fsmonitor = ", "hooksPath = ", "packObjectsHook = ", "alternateRefsCommand = ", canary + "/config-fsmonitor"} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf(".git/config lacks %q", want)
		}
	}
	// A hook writes its canary, so a hook that runs is seen. The harness
	// does not run it: the canary is absent.
	hook, err := os.ReadFile(filepath.Join(gitDir, "hooks", "pre-commit"))
	if err != nil || !strings.Contains(string(hook), canary+"/hook-pre-commit") {
		t.Errorf("pre-commit hook: %q, %v", hook, err)
	}
}

func TestFetchFactsFromAHostileClone(t *testing.T) {
	rep, _ := runFake(t, fakePlanner(t))
	f := findFact(t, rep, "s16-fetch.fetch")
	if f.Observed["result"] != "ok" {
		t.Fatalf("fetch: %v", f.Observed)
	}
	up := findFact(t, rep, "s16-fetch.upstream-refs").Observed
	if up["identical"] != true {
		t.Errorf("upstream refs: %v", up)
	}
	before := up["before"].([]RefFact)
	if len(before) != 2 || before[0].Name != "refs/heads/main" || before[1].Name != "refs/tags/v1" {
		t.Errorf("upstream refs before: %+v", before)
	}
	lead := findFact(t, rep, "s16-fetch.lead-refs").Observed
	if after := lead["after"].([]RefFact); len(after) != 2 || after[0].Name != "refs/leads/coder/heads/feature" || after[1].Name != "refs/leads/coder/heads/main" {
		t.Errorf("lead refs after: %+v", after)
	}
	// With the fake planner nothing runs the planted hooks.
	canaries := findFact(t, rep, "s16-fetch.canaries").Observed["after"].(map[string]any)
	if present := canaries["present"].([]string); len(present) != 0 {
		t.Errorf("canaries present: %v", present)
	}
	if other := canaries["other_entries"].([]string); len(other) != 0 {
		t.Errorf("other entries: %v", other)
	}
}

// TestPlantedCanariesAreArmed is the positive control of the S16 canaries:
// git porcelain that runs in a planted clone writes them. It runs plain git in
// a clone in a test folder, with an explicit environment and no sandbox. The
// absent canaries of the other tests mean something only because of it.
func TestPlantedCanariesAreArmed(t *testing.T) {
	fx := fixtures{git: needGit(t)}
	ctx := context.Background()
	dir := t.TempDir()
	up, tree, canary := filepath.Join(dir, "up"), filepath.Join(dir, "tree"), filepath.Join(dir, "canary")
	if err := os.Mkdir(canary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fx.buildUpstream(ctx, up); err != nil {
		t.Fatal(err)
	}
	if err := fx.buildLead(ctx, up, tree); err != nil {
		t.Fatal(err)
	}
	if err := plantHostile(tree, canary, tree); err != nil {
		t.Fatal(err)
	}
	exists := func(name string) bool {
		_, err := os.Lstat(filepath.Join(canary, name))
		return err == nil
	}
	for _, n := range canaryNames() {
		if exists(n) {
			t.Fatalf("canary %s exists before git ran in the clone", n)
		}
	}
	// git status runs the configured fsmonitor and the post-index-change hook
	// of core.hooksPath.
	_, _ = fx.run(ctx, tree, "status")
	for _, n := range []string{"config-fsmonitor", "hookspath-post-index-change"} {
		if !exists(n) {
			t.Errorf("git status did not write canary %s: the fixture is not armed", n)
		}
	}
	// git commit runs the hooks of core.hooksPath. The hooks of .git/hooks
	// do not run while core.hooksPath is set.
	if _, err := fx.run(ctx, tree, "commit", "--quiet", "--no-gpg-sign", "--allow-empty", "-m", "x"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"hookspath-pre-commit", "hookspath-prepare-commit-msg", "hookspath-commit-msg", "hookspath-post-commit", "hookspath-reference-transaction"} {
		if !exists(n) {
			t.Errorf("git commit did not write canary %s: the fixture is not armed", n)
		}
	}
	for _, n := range []string{"hook-pre-commit", "hook-post-commit"} {
		if exists(n) {
			t.Errorf("canary %s was written although core.hooksPath is set", n)
		}
	}
}

// TestControlFact: the report holds the control. Plain git status in the
// control clone fires the same fixture, and the control writes into a canary
// folder of its own, so the S16 canary folder stays empty.
func TestControlFact(t *testing.T) {
	rep, o := runFake(t, fakePlanner(t))
	c := findFact(t, rep, "s16-fetch.control").Observed
	if c["result"] != "ok" {
		t.Fatalf("control: %v", c)
	}
	present, _ := c["present"].([]string)
	for _, n := range []string{"config-fsmonitor", "hookspath-post-index-change"} {
		if !contains(present, n) {
			t.Errorf("control: canary %s did not fire: %v", n, present)
		}
	}
	// The report names the control after the fetch facts.
	var ids []string
	for _, c := range rep.Cases {
		if c.ID == "s16-fetch" {
			for _, f := range c.Facts {
				ids = append(ids, f.ID)
			}
		}
	}
	if last := ids[len(ids)-1]; last != "s16-fetch.control" {
		t.Errorf("s16-fetch facts end with %s", last)
	}
	entries, err := os.ReadDir(filepath.Join(o.StateRoot, "canary"))
	if err != nil || len(entries) != 0 {
		t.Errorf("the S16 canary folder holds %v, %v", entries, err)
	}
}

func TestOversizeFacts(t *testing.T) {
	rep, _ := runFake(t, fakePlanner(t))
	for id, what := range map[string]string{"oversize.measure": "source", "oversize.staged": "staged"} {
		o := findFact(t, rep, id).Observed
		if o["result"] != "error" {
			t.Errorf("%s: %v", id, o)
			continue
		}
		lim, ok := o["limit"].(map[string]any)
		if !ok || lim["what"] != what {
			t.Errorf("%s: limit %v, want %s", id, o["limit"], what)
		}
		classes := o["error_classes"].([]string)
		if len(classes) != 1 || classes[0] != "mirror.LimitError" {
			t.Errorf("%s: classes %v", id, classes)
		}
		b, a := o["mirror_before"].(state), o["mirror_after"].(state)
		if b.Size != a.Size && id == "oversize.measure" {
			t.Errorf("%s: the mirror changed: %+v %+v", id, b.Size, a.Size)
		}
		if len(a.Lead) != 0 {
			t.Errorf("%s: lead refs after a refused fetch: %+v", id, a.Lead)
		}
		if names := o["mirrors_folder"].([]string); len(names) > 2 {
			t.Errorf("%s: leftovers: %v", id, names)
		}
	}
	if o := findFact(t, rep, "oversize.control").Observed; o["result"] != "ok" {
		t.Errorf("control: %v", o)
	}
}

func TestPlantedFacts(t *testing.T) {
	rep, _ := runFake(t, fakePlanner(t))
	for _, id := range []string{"planted.control-before", "planted.control-after"} {
		if o := findFact(t, rep, id).Observed; o["result"] != "ok" || o["serve_requests"] != int64(1) {
			t.Errorf("%s: %v", id, o)
		}
	}
	for _, id := range []string{"planted.alternates", "planted.git-symlink", "planted.tree-symlink"} {
		o := findFact(t, rep, id).Observed
		classes, _ := o["error_classes"].([]string)
		if o["result"] != "error" || len(classes) != 1 || classes[0] != "mirror.ErrHostileTree" {
			t.Errorf("%s: %v", id, o)
		}
		if o["serve_requests"] != int64(0) {
			t.Errorf("%s: the planner was asked: %v", id, o["serve_requests"])
		}
	}
}

func TestReviewFacts(t *testing.T) {
	rep, _ := runFake(t, fakePlanner(t))
	if o := findFact(t, rep, "s19-review.fetch").Observed; o["result"] != "ok" {
		t.Fatalf("fetch: %v", o)
	}
	o := findFact(t, rep, "s19-review.inspect").Observed
	if o["result"] != "ok" {
		t.Fatalf("inspect: %v", o)
	}
	flagged := o["flagged_paths"].([]string)
	want := []string{".claude/settings.json", ".codex/config.toml", ".envrc", ".mcp.json", ".vscode/tasks.json", "Makefile", "go.mod", "scripts/check.sh"}
	if strings.Join(flagged, ",") != strings.Join(want, ",") {
		t.Errorf("flagged paths = %v, want %v", flagged, want)
	}
	if un := o["unflagged_changed_paths"].([]string); len(un) != 1 || un[0] != "docs/notes.md" {
		t.Errorf("unflagged = %v", un)
	}
	if n := len(o["commits"].([]map[string]any)); n != len(reviewCommits) {
		t.Errorf("%d commits", n)
	}
	if up := findFact(t, rep, "s19-review.upstream-refs").Observed; up["identical"] != true {
		t.Errorf("upstream moved: %v", up)
	}
}

func TestVerificationFacts(t *testing.T) {
	leak := "leadmirror-test-secret-value"
	t.Setenv("LEADMIRROR_TEST_LEAK", leak)
	p := fakePlanner(t)
	p.VerifyEnv = append(p.VerifyEnv, "LEADMIRROR_TEST_LEAK="+leak)
	rep, o := runFake(t, p)

	cg := findFact(t, rep, "verification.canary-gate").Observed
	if cg["result"] != "ok" || cg["exit"] != 0 {
		t.Fatalf("canary gate: %v", cg)
	}
	// With the fake planner there is no sandbox, so the gate can write the
	// canary: this is the positive control for the fact.
	if cg["canary_exists_on_host"] != true {
		t.Errorf("canary_exists_on_host = %v, want true without a sandbox", cg["canary_exists_on_host"])
	}
	if _, err := os.Lstat(filepath.Join(o.StateRoot, "cases", "review", "canary", "verify-gate")); err != nil {
		t.Errorf("the canary file: %v", err)
	}
	if out := cg["output"].([]string); !contains(out, "canary-write-succeeded") {
		t.Errorf("output: %v", out)
	}

	eg := findFact(t, rep, "verification.environment-gate").Observed
	envLines := eg["environment"].([]string)
	if !contains(envLines, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Errorf("environment: %v", envLines)
	}
	same := eg["same_as_harness_env"].([]string)
	if !contains(same, "LEADMIRROR_TEST_LEAK") {
		t.Errorf("same_as_harness_env = %v", same)
	}
	for _, l := range envLines {
		if strings.Contains(l, leak) {
			t.Errorf("a value from the harness's environment was recorded: %q", l)
		}
	}
	if !contains(envLines, "LEADMIRROR_TEST_LEAK=<same value as in the harness's own environment; not recorded>") {
		t.Errorf("environment: %v", envLines)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestRepositoryOnHostFact: the clone fact says whether the template's
// repository path exists on the host. With the fake planner the path is the
// clone's own; with a path that is a bundle file the fetch is refused and the
// fact and the fetch fact say so.
func TestRepositoryOnHostFact(t *testing.T) {
	rep, _ := runFake(t, fakePlanner(t))
	if got := findFact(t, rep, "s16-fetch.clone").Observed["repository_on_host"]; got != "the clone's own path" {
		t.Errorf("repository_on_host = %v", got)
	}

	p := fakePlanner(t)
	p.Repository = filepath.Join(t.TempDir(), "work")
	if err := os.WriteFile(p.Repository, []byte("not a bundle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, _ = runFake(t, p)
	if got := findFact(t, rep, "s16-fetch.clone").Observed["repository_on_host"]; got != "exists: a regular file" {
		t.Errorf("repository_on_host = %v", got)
	}
	f := findFact(t, rep, "s16-fetch.fetch").Observed
	if f["result"] != "error" || !strings.Contains(strings.Join(f["error_classes"].([]string), ","), "mirror.ErrRepositoryPath") {
		t.Errorf("fetch fact: %v", f)
	}

	p = fakePlanner(t)
	p.Repository = filepath.Join(t.TempDir(), "absent")
	rep, _ = runFake(t, p)
	if got := findFact(t, rep, "s16-fetch.clone").Observed["repository_on_host"]; got != "absent" {
		t.Errorf("repository_on_host = %v", got)
	}
}
