package leadmirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ataniq/internal/exchange/sandboxplan"
)

func validConfigJSON(root, tmpl string) string {
	return fmt.Sprintf(`{"state_root": %q, "template": %q, "role": "coder", "project": "proj", "i_run_this_on_purpose": true}`, root, tmpl)
}

func TestReadConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	writeFile(t, cfg, validConfigJSON("/srv/scratch/root", "/srv/template.json"))
	c, err := ReadConfig(cfg)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	want := Config{StateRoot: "/srv/scratch/root", Template: "/srv/template.json", Role: "coder", Project: "proj", Acknowledged: true}
	if c != want {
		t.Errorf("config = %+v", c)
	}
}

func TestReadConfigRefusals(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		content string
		want    error
	}{
		"no acknowledgement": {`{"state_root": "/srv/scratch/root", "template": "/srv/t.json", "role": "coder", "project": "proj"}`, ErrNotAcknowledged},
		"false":              {`{"state_root": "/srv/scratch/root", "template": "/srv/t.json", "role": "coder", "project": "proj", "i_run_this_on_purpose": false}`, ErrNotAcknowledged},
		"no template":        {`{"state_root": "/srv/scratch/root", "role": "coder", "project": "proj", "i_run_this_on_purpose": true}`, ErrNoTemplate},
		"relative template":  {`{"state_root": "/srv/scratch/root", "template": "t.json", "role": "coder", "project": "proj", "i_run_this_on_purpose": true}`, ErrNoTemplate},
		"no state root":      {`{"template": "/srv/t.json", "role": "coder", "project": "proj", "i_run_this_on_purpose": true}`, ErrStateRoot},
		"relative root":      {`{"state_root": "scratch", "template": "/srv/t.json", "role": "coder", "project": "proj", "i_run_this_on_purpose": true}`, ErrStateRoot},
		"root of the disk":   {`{"state_root": "/", "template": "/srv/t.json", "role": "coder", "project": "proj", "i_run_this_on_purpose": true}`, ErrStateRoot},
		"top folder":         {`{"state_root": "/srv", "template": "/srv/t.json", "role": "coder", "project": "proj", "i_run_this_on_purpose": true}`, ErrStateRoot},
		"odd characters":     {`{"state_root": "/srv/a b/c", "template": "/srv/t.json", "role": "coder", "project": "proj", "i_run_this_on_purpose": true}`, ErrStateRoot},
		"quote in the path":  {`{"state_root": "/srv/a'b/c", "template": "/srv/t.json", "role": "coder", "project": "proj", "i_run_this_on_purpose": true}`, ErrStateRoot},
		"bad role":           {`{"state_root": "/srv/scratch/root", "template": "/srv/t.json", "role": "Coder", "project": "proj", "i_run_this_on_purpose": true}`, ErrConfig},
		"bad project":        {`{"state_root": "/srv/scratch/root", "template": "/srv/t.json", "role": "coder", "project": "a/b", "i_run_this_on_purpose": true}`, ErrConfig},
		"unknown field":      {`{"state_root": "/srv/scratch/root", "template": "/srv/t.json", "role": "coder", "project": "proj", "i_run_this_on_purpose": true, "extra": 1}`, ErrConfig},
		"trailing data":      {validConfigJSON("/srv/scratch/root", "/srv/t.json") + " {}", ErrConfig},
		"not json":           {`state_root=/srv`, ErrConfig},
		"empty":              {``, ErrConfig},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
			writeFile(t, path, tc.content)
			if _, err := ReadConfig(path); !errors.Is(err, tc.want) {
				t.Errorf("ReadConfig = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := ReadConfig(filepath.Join(dir, "missing.json")); !errors.Is(err, ErrConfig) {
		t.Errorf("a missing config: %v", err)
	}
	if _, err := ReadConfig(""); !errors.Is(err, ErrConfig) {
		t.Errorf("no config: %v", err)
	}
	// A config that is a symlink is not followed.
	real := filepath.Join(dir, "real.json")
	writeFile(t, real, validConfigJSON("/srv/scratch/root", "/srv/t.json"))
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(link); !errors.Is(err, ErrConfig) {
		t.Errorf("a symlinked config: %v", err)
	}
}

func TestLoadTemplate(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "t.json")
	body := `{"version": 1, "serve": {"upload_pack": ["/usr/bin/true"], "repository": "/work/{project}", "measure": ["/usr/bin/true"], "measure_env": []}}`
	writeFile(t, good, body)
	tmpl, digest, err := LoadTemplate(good)
	if err != nil || tmpl == nil || len(digest) != 64 {
		t.Fatalf("LoadTemplate = %v, %q, %v", tmpl, digest, err)
	}
	// The digest is the digest of the bytes: another file, another digest.
	other := filepath.Join(dir, "t2.json")
	writeFile(t, other, body+"\n")
	if _, d2, err := LoadTemplate(other); err != nil || d2 == digest {
		t.Errorf("digest of a changed file: %q, %v", d2, err)
	}
	for name, path := range map[string]string{
		"missing":  filepath.Join(dir, "none.json"),
		"empty":    "",
		"not json": func() string { p := filepath.Join(dir, "bad.json"); writeFile(t, p, "x"); return p }(),
	} {
		if _, _, err := LoadTemplate(path); !errors.Is(err, ErrNoTemplate) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRunRefusals(t *testing.T) {
	good := func(t *testing.T) Options { return options(t, fakePlanner(t)) }
	ctx := context.Background()
	t.Run("root holds a file", func(t *testing.T) {
		o := good(t)
		if err := os.Mkdir(o.StateRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(o.StateRoot, "keep"), "x")
		rep, err := Run(ctx, o)
		if !errors.Is(err, ErrStateRoot) || rep != nil {
			t.Fatalf("Run = %v, %v", rep, err)
		}
		// Nothing was added beside the file.
		if entries, _ := os.ReadDir(o.StateRoot); len(entries) != 1 {
			t.Errorf("the root holds %d entries", len(entries))
		}
	})
	t.Run("root is a symlink", func(t *testing.T) {
		o := good(t)
		target := t.TempDir()
		if err := os.Symlink(target, o.StateRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(ctx, o); !errors.Is(err, ErrStateRoot) {
			t.Fatalf("Run = %v", err)
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("the link's target holds %d entries", len(entries))
		}
	})
	t.Run("root is a file", func(t *testing.T) {
		o := good(t)
		writeFile(t, o.StateRoot, "x")
		if _, err := Run(ctx, o); !errors.Is(err, ErrStateRoot) {
			t.Fatalf("Run = %v", err)
		}
	})
	t.Run("root can be written by others", func(t *testing.T) {
		o := good(t)
		if err := os.Mkdir(o.StateRoot, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(o.StateRoot, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(ctx, o); !errors.Is(err, ErrStateRoot) {
			t.Fatalf("Run = %v", err)
		}
	})
	t.Run("parent is missing", func(t *testing.T) {
		o := good(t)
		o.StateRoot = filepath.Join(o.StateRoot, "a", "b")
		if _, err := Run(ctx, o); !errors.Is(err, ErrStateRoot) {
			t.Fatalf("Run = %v", err)
		}
	})
	t.Run("no planner", func(t *testing.T) {
		o := good(t)
		o.Planner = nil
		if _, err := Run(ctx, o); !errors.Is(err, ErrNoTemplate) {
			t.Fatalf("Run = %v", err)
		}
		if _, err := os.Lstat(o.StateRoot); err == nil {
			t.Error("the root was made")
		}
	})
	t.Run("no digest", func(t *testing.T) {
		o := good(t)
		o.TemplateDigest = ""
		if _, err := Run(ctx, o); !errors.Is(err, ErrNoTemplate) {
			t.Fatalf("Run = %v", err)
		}
	})
	t.Run("bad names", func(t *testing.T) {
		for _, f := range []func(*Options){
			func(o *Options) { o.Role = "Bad/Role" },
			func(o *Options) { o.Project = "" },
			func(o *Options) { o.StateRoot = "relative" },
			func(o *Options) { o.Git.Path = "" },
		} {
			o := good(t)
			f(&o)
			if _, err := Run(ctx, o); err == nil {
				t.Errorf("accepted %+v", o)
			}
		}
	})
	t.Run("an empty root is accepted", func(t *testing.T) {
		o := good(t)
		if err := os.Mkdir(o.StateRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(ctx, o); err != nil {
			t.Fatalf("Run = %v", err)
		}
	})
}

// noSandboxPlanner stands for a template with no sandbox for fetches. It
// answers every serve request with ErrNotConfigured and counts the requests of
// each kind.
type noSandboxPlanner struct {
	mu               sync.Mutex
	serves, verifies int
}

func (p *noSandboxPlanner) Serve(sandboxplan.ServeRequest) (sandboxplan.ServePlan, error) {
	p.mu.Lock()
	p.serves++
	p.mu.Unlock()
	return sandboxplan.ServePlan{}, fmt.Errorf("%w for serve", sandboxplan.ErrNotConfigured)
}

func (p *noSandboxPlanner) Verify(sandboxplan.VerifyRequest) (sandboxplan.VerifyPlan, error) {
	p.mu.Lock()
	p.verifies++
	p.mu.Unlock()
	return sandboxplan.VerifyPlan{}, errors.New("unexpected verify request")
}

// TestNothingRunsWithoutASandbox: a planner that has no sandbox for fetches
// ends every fetch with ErrNotConfigured, so no lead ref arrives and no canary
// is written, and the library starts nothing in its place. It also asks for no
// verification, because no fetch gave a tip to verify. That last check can
// fire: a case that asked for a verification without a tip would count here.
// What the library asks the planner in other runs is checked by
// TestOnlyTheGivenPlannerIsAsked and TestNoVerifySection.
func TestNothingRunsWithoutASandbox(t *testing.T) {
	p := &noSandboxPlanner{}
	rep, o := runFake(t, p)
	for _, id := range []string{"s16-fetch.fetch", "oversize.measure", "oversize.staged", "oversize.control", "planted.control-before", "s19-review.fetch"} {
		f := findFact(t, rep, id).Observed
		classes, _ := f["error_classes"].([]string)
		if f["result"] != "error" || len(classes) != 1 || classes[0] != "sandboxplan.ErrNotConfigured" {
			t.Errorf("%s: %v", id, f)
		}
	}
	if f := findFact(t, rep, "verification.not-run"); f.Observed == nil {
		t.Error("no verification fact")
	}
	if p.serves == 0 {
		t.Error("the planner was never asked")
	}
	if p.verifies != 0 {
		t.Errorf("a verification was asked for %d times without a fetched tip", p.verifies)
	}
	// No lead ref arrived, and no canary file was written.
	for _, id := range []string{"s16-fetch.lead-refs"} {
		after := findFact(t, rep, id).Observed["after"].([]RefFact)
		if len(after) != 0 {
			t.Errorf("%s: %+v", id, after)
		}
	}
	entries, err := os.ReadDir(filepath.Join(o.StateRoot, "canary"))
	if err != nil || len(entries) != 0 {
		t.Errorf("canary folder: %v, %v", entries, err)
	}
}

// requestPlanner passes requests to a Fake and checks that every request is
// for the scratch state root the run was given: the library names nothing
// else to the planner.
type requestPlanner struct {
	t             *testing.T
	inner         sandboxplan.Planner
	root          string
	mu            sync.Mutex
	serves, verif int
}

func (p *requestPlanner) Serve(r sandboxplan.ServeRequest) (sandboxplan.ServePlan, error) {
	p.mu.Lock()
	p.serves++
	p.mu.Unlock()
	if !strings.HasPrefix(r.Tree, p.root+"/") || !strings.HasSuffix(r.Tree, "/leads/coder/tree") || r.Role != "coder" || r.Project != "proj" {
		p.t.Errorf("serve request outside the scratch root or the lead's clone: %+v", r)
	}
	return p.inner.Serve(r)
}

func (p *requestPlanner) Verify(r sandboxplan.VerifyRequest) (sandboxplan.VerifyPlan, error) {
	p.mu.Lock()
	p.verif++
	p.mu.Unlock()
	for _, path := range []string{r.Mirror, r.Scratch, r.Home} {
		if !strings.HasPrefix(path, p.root+"/") {
			p.t.Errorf("verify request outside the scratch root: %+v", r)
		}
	}
	if !strings.HasSuffix(r.Mirror, "/mirrors/proj.git") || r.Project != "proj" {
		p.t.Errorf("verify request for another mirror: %+v", r)
	}
	return p.inner.Verify(r)
}

func TestOnlyTheGivenPlannerIsAsked(t *testing.T) {
	o := options(t, nil)
	p := &requestPlanner{t: t, inner: fakePlanner(t), root: o.StateRoot}
	o.Planner = p
	rep, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if p.serves == 0 || p.verif != 2 {
		t.Errorf("requests: %d serve, %d verify", p.serves, p.verif)
	}
	if f := findFact(t, rep, "verification.environment-gate"); f.Observed["result"] != "ok" {
		t.Errorf("verification: %v", f.Observed)
	}
}

// TestNoVerifySection: a template without a verify section gives one fact and
// no verification run.
func TestNoVerifySection(t *testing.T) {
	p := fakePlanner(t)
	p.VerifyErr = fmt.Errorf("%w for verify", sandboxplan.ErrNotConfigured)
	rep, _ := runFake(t, p)
	f := findFact(t, rep, "verification.no-verify-section").Observed
	classes, _ := f["error_classes"].([]string)
	if f["result"] != "error" || len(classes) != 1 || classes[0] != "sandboxplan.ErrNotConfigured" {
		t.Errorf("fact: %v", f)
	}
	for _, c := range rep.Cases {
		if c.ID != "verification" {
			continue
		}
		if len(c.Facts) != 1 {
			t.Errorf("verification facts: %d", len(c.Facts))
		}
	}
	if got := len(p.VerifyRequests()); got != 1 {
		t.Errorf("verify requests: %d", got)
	}
}

func TestHookNamesCoverTheSampleHooks(t *testing.T) {
	g := needGit(t)
	dir := t.TempDir()
	fx := fixtures{git: g}
	// A plain init copies the sample hooks of the installed git's templates.
	if _, err := fx.run(context.Background(), dir, "init", "--quiet"); err != nil {
		t.Fatal(err)
	}
	samples, _ := filepath.Glob(filepath.Join(dir, ".git", "hooks", "*.sample"))
	if len(samples) == 0 {
		t.Skip("the installed git has no sample hooks in its templates")
	}
	for _, s := range samples {
		name := strings.TrimSuffix(filepath.Base(s), ".sample")
		if !contains(hookNames, name) {
			t.Errorf("sample hook %s is not in hookNames", name)
		}
	}
}

func TestCanaryNames(t *testing.T) {
	names := canaryNames()
	if len(names) != 2*len(hookNames)+len(configCanaries) {
		t.Errorf("%d canary names", len(names))
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] || strings.ContainsAny(n, "/ ") {
			t.Errorf("canary name %q", n)
		}
		seen[n] = true
	}
}
