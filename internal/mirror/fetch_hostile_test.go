package mirror

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/exchange/sandboxplan/plantest"
)

// started fails the test when anything of the armed environment started, and
// when the planner was asked for a plan.
func (e *fetchEnv) refusedBeforeAnything(t *testing.T, c *canaries, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("Fetch = %v, want %v", err, want)
	}
	c.none(t)
	if got := e.planner.ServeRequests(); len(got) != 0 {
		t.Errorf("the planner was asked for a plan: %+v", got)
	}
}

func TestFetchPlantedTreesAreRefused(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, e *fetchEnv)
	}{
		{"the tree is a symlink to another repository", func(t *testing.T, e *fetchEnv) {
			other := newLeadClone(t, e.g, t.TempDir(), "coder")
			other.commit(t, "other.txt", "x\n", "other")
			if err := os.RemoveAll(e.lead.tree); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other.tree, e.lead.tree); err != nil {
				t.Fatal(err)
			}
		}},
		{".git is a symlink", func(t *testing.T, e *fetchEnv) { swapForLink(t, e.state, "leads/coder/tree/.git") }},
		{".git is a file with a gitdir line", func(t *testing.T, e *fetchEnv) {
			elsewhere := swapForLink(t, e.state, "leads/coder/tree/.git")
			if err := os.Remove(e.lead.gitDir()); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(e.lead.gitDir(), []byte("gitdir: "+elsewhere+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{".git/objects is a symlink", func(t *testing.T, e *fetchEnv) { swapForLink(t, e.state, "leads/coder/tree/.git/objects") }},
		{"leads/<role> is a symlink", func(t *testing.T, e *fetchEnv) { swapForLink(t, e.state, "leads/coder") }},
		{"leads is a symlink", func(t *testing.T, e *fetchEnv) { swapForLink(t, e.state, "leads") }},
		{"alternates", func(t *testing.T, e *fetchEnv) {
			other := newSource(t, e.g)
			p := filepath.Join(e.lead.gitDir(), "objects", "info", "alternates")
			if err := os.WriteFile(p, []byte(filepath.Join(other.dir, ".git", "objects")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"alternates as a symlink", func(t *testing.T, e *fetchEnv) {
			target := filepath.Join(t.TempDir(), "alt")
			if err := os.WriteFile(target, []byte("/elsewhere/objects\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(e.lead.gitDir(), "objects", "info", "alternates")); err != nil {
				t.Fatal(err)
			}
		}},
		{"http alternates", func(t *testing.T, e *fetchEnv) {
			p := filepath.Join(e.lead.gitDir(), "objects", "info", "http-alternates")
			if err := os.WriteFile(p, []byte("http://example.invalid/\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"a shallow clone", func(t *testing.T, e *fetchEnv) {
			e.lead.commit(t, "two.txt", "2\n", "two")
			src := e.lead.tree
			dst := filepath.Join(t.TempDir(), "shallow")
			fixtureGit(t, e.g, t.TempDir(), "clone", "--quiet", "--depth=1", "file://"+src, dst)
			if _, err := os.Lstat(filepath.Join(dst, ".git", "shallow")); err != nil {
				t.Fatalf("the clone is not shallow: %v", err)
			}
			if err := os.RemoveAll(e.lead.tree); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(dst, e.lead.tree); err != nil {
				t.Fatal(err)
			}
		}},
		{"a shallow file", func(t *testing.T, e *fetchEnv) {
			if err := os.WriteFile(filepath.Join(e.lead.gitDir(), "shallow"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"commondir", func(t *testing.T, e *fetchEnv) {
			if err := os.WriteFile(filepath.Join(e.lead.gitDir(), "commondir"), []byte("../x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newFetchEnv(t)
			tc.plant(t, e)
			c := e.arm(t)
			_, err := e.m.Fetch(context.Background(), e.req())
			e.refusedBeforeAnything(t, c, err, ErrHostileTree)
			noLeftovers(t, e.m)
		})
	}
}

func TestFetchServeRequestNamesOnlyTheCloneRoot(t *testing.T) {
	e := newFetchEnv(t)
	if _, err := e.m.Fetch(context.Background(), e.req()); err != nil {
		t.Fatal(err)
	}
	want := sandboxplan.ServeRequest{Role: "coder", Project: "proj", Tree: e.lead.tree}
	if got := e.planner.ServeRequests(); len(got) != 1 || got[0] != want {
		t.Errorf("serve requests = %+v, want [%+v]", got, want)
	}
}

func TestFetchWithoutASandbox(t *testing.T) {
	for name, planner := range map[string]*plantest.Fake{
		"not configured": {ServeErr: sandboxplan.ErrNotConfigured},
		"another error":  {ServeErr: errors.New("the sandbox is down")},
	} {
		t.Run(name, func(t *testing.T) {
			e := newFetchEnv(t)
			c := e.arm(t)
			planner.UploadPack = e.planner.UploadPack
			e.planner = planner
			_, err := e.m.Fetch(context.Background(), e.req())
			if err == nil || (name == "not configured" && !errors.Is(err, sandboxplan.ErrNotConfigured)) {
				t.Fatalf("Fetch = %v", err)
			}
			c.none(t)
			noLeftovers(t, e.m)
		})
	}
	t.Run("a plan that is not valid", func(t *testing.T) {
		// A planner of another kind than the fake, which hands back a plan
		// that the real code would refuse.
		e := newFetchEnv(t)
		c := e.arm(t)
		e.planner = nil
		req := e.req()
		req.Planner = badPlanner{}
		if _, err := e.m.Fetch(context.Background(), req); err == nil || !strings.Contains(err.Error(), "not valid") {
			t.Errorf("Fetch = %v", err)
		}
		c.none(t)
	})
	t.Run("no planner", func(t *testing.T) {
		e := newFetchEnv(t)
		req := e.req()
		req.Planner = nil
		if _, err := e.m.Fetch(context.Background(), req); err == nil {
			t.Error("Fetch without a planner succeeded")
		}
	})
}

type badPlanner struct{}

func (badPlanner) Serve(sandboxplan.ServeRequest) (sandboxplan.ServePlan, error) {
	return sandboxplan.ServePlan{UploadPack: []string{"relative"}}, nil
}

func (badPlanner) Verify(sandboxplan.VerifyRequest) (sandboxplan.VerifyPlan, error) {
	return sandboxplan.VerifyPlan{}, errors.New("not used")
}

func TestFetchRequestChecks(t *testing.T) {
	e := newFetchEnv(t)
	c := e.arm(t)
	ctx := context.Background()
	for name, mod := range map[string]func(r *FetchRequest){
		"role empty":          func(r *FetchRequest) { r.Role = "" },
		"role upper case":     func(r *FetchRequest) { r.Role = "Coder" },
		"role with a slash":   func(r *FetchRequest) { r.Role = "a/b" },
		"role dot dot":        func(r *FetchRequest) { r.Role = ".." },
		"project other":       func(r *FetchRequest) { r.Project = "other" },
		"project invalid":     func(r *FetchRequest) { r.Project = "p/q" },
		"state root relative": func(r *FetchRequest) { r.StateRoot = "state" },
		"state root unclean":  func(r *FetchRequest) { r.StateRoot += "/../" + filepath.Base(r.StateRoot) },
		"state root empty":    func(r *FetchRequest) { r.StateRoot = "" },
		"negative limit":      func(r *FetchRequest) { r.Limits.MaxStaged = -1 },
		"negative timeout":    func(r *FetchRequest) { r.Limits.Timeout = -1 },
	} {
		req := e.req()
		mod(&req)
		if _, err := e.m.Fetch(ctx, req); err == nil {
			t.Errorf("%s: Fetch succeeded", name)
		}
	}
	// A mirror that no Create or Open made.
	if _, err := (&Mirror{git: e.m.git, path: e.m.path}).Fetch(ctx, e.req()); err == nil {
		t.Error("a mirror without a project was accepted")
	}
	// A role without a clone.
	req := e.req()
	req.Role = "nobody"
	if _, err := e.m.Fetch(ctx, req); !errors.Is(err, ErrHostileTree) {
		t.Errorf("Fetch for a role without a clone = %v", err)
	}
	c.none(t)
	if got := e.planner.ServeRequests(); len(got) != 0 {
		t.Errorf("the planner was asked: %+v", got)
	}
}

// cloneShared clones source into the lead's tree with an alternates file, so
// that the objects live in source only.
func (e *fetchEnv) cloneShared(t *testing.T, source string) string {
	t.Helper()
	if err := os.RemoveAll(e.lead.tree); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, e.g, t.TempDir(), "clone", "--quiet", "--shared", source, e.lead.tree)
	alt := filepath.Join(e.lead.gitDir(), "objects", "info", "alternates")
	data, err := os.ReadFile(alt)
	if err != nil {
		t.Fatalf("the clone has no alternates file: %v", err)
	}
	if err := os.Remove(alt); err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFetchWhenAlternatesAppearAfterTheCheck(t *testing.T) {
	// The owner-side check can be raced: a lead that adds the alternates
	// file after the check and before the sandboxed upload-pack runs. Here
	// the measure command, which runs after the check, plants it. What the
	// fetch then does is what the owner relies on: upload-pack sends a
	// pack that holds every object, so the mirror stands on its own.
	e := newFetchEnv(t)
	src := newSource(t, e.g)
	more := filepath.Join(src.dir, "more.txt")
	if err := os.WriteFile(more, []byte("more\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, e.g, src.dir, "add", "more.txt")
	fixtureGit(t, e.g, src.dir, "commit", "--quiet", "--no-gpg-sign", "-m", "more")
	tip := fixtureGit(t, e.g, src.dir, "rev-parse", "HEAD")
	altText := e.cloneShared(t, src.dir)
	if hasObject(t, e.g, e.lead.gitDir(), tip) {
		t.Fatal("the clone has the objects itself: the case proves nothing")
	}
	plant := filepath.Join(t.TempDir(), "alternates-text")
	if err := os.WriteFile(plant, []byte(altText), 0o600); err != nil {
		t.Fatal(err)
	}
	e.planner.Measure = sandboxplan.Command{
		Argv: []string{"/bin/sh", "-c", "cp \"$0\" \"$1\" && echo 100", plant, filepath.Join(e.lead.gitDir(), "objects", "info", "alternates")},
		Env:  []string{"PATH=/usr/bin:/bin"},
	}

	// The control: before the plant, the check refuses the same file.
	if err := os.WriteFile(filepath.Join(e.lead.gitDir(), "objects", "info", "alternates"), []byte(altText), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Fetch(context.Background(), e.req()); !errors.Is(err, ErrHostileTree) {
		t.Fatalf("the control: Fetch = %v, want ErrHostileTree", err)
	}
	if err := os.Remove(filepath.Join(e.lead.gitDir(), "objects", "info", "alternates")); err != nil {
		t.Fatal(err)
	}

	rep, err := e.m.Fetch(context.Background(), e.req())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(rep.Updates) != 1 || rep.Updates[0].New != tip {
		t.Errorf("updates = %+v", rep.Updates)
	}
	if _, err := os.Lstat(filepath.Join(e.m.Path(), "objects", "info", "alternates")); err == nil {
		t.Error("the mirror has an alternates file")
	}
	if _, err := os.Lstat(filepath.Join(e.m.Path(), "objects", "info", "http-alternates")); err == nil {
		t.Error("the mirror has an http-alternates file")
	}
	// The objects are in the mirror itself. gitenv never sets
	// GIT_ALTERNATE_OBJECT_DIRECTORIES, so nothing else is consulted.
	if got := mirrorGit(t, e.g, e.m.Path(), nil, "rev-list", "--objects", "--end-of-options", tip); !strings.Contains(got, src.blob) || !strings.Contains(got, src.tree) {
		t.Errorf("rev-list --objects: %q", got)
	}
	for _, id := range []string{tip, src.commit, src.tree, src.blob} {
		if !hasObject(t, e.g, e.m.Path(), id) {
			t.Errorf("object %s is not in the mirror", id)
		}
	}
	noLeftovers(t, e.m)
}

func TestFetchHostileCloneConfiguration(t *testing.T) {
	// A lead can write the clone's hooks and config. None of this may run in
	// the owner's half of the fetch. This test runs the fake planner's plain
	// git-upload-pack, which is the serving half and is not sandboxed here.
	// Nothing of the following starts in it either, but if one did, it
	// would be the sandbox's to contain, not the owner's.
	e := newFetchEnv(t)
	// The second branch is made before the hooks are armed: git branch would
	// run reference-transaction.
	e.lead.git(t, "branch", "second")
	canaries := t.TempDir()
	script := func(name string) string {
		return "#!/bin/sh\ntouch '" + filepath.Join(canaries, name) + "'\nexit 0\n"
	}
	hooks := []string{
		"applypatch-msg", "commit-msg", "fsmonitor-watchman", "post-update",
		"pre-applypatch", "pre-commit", "pre-merge-commit", "pre-push",
		"pre-rebase", "pre-receive", "prepare-commit-msg", "push-to-checkout",
		"sendemail-validate", "update", "reference-transaction", "post-receive",
		"post-commit", "post-checkout", "post-merge", "pre-auto-gc", "proc-receive",
		"pre-upload-pack", "upload-pack", "post-upload-pack",
	}
	hookDir := filepath.Join(e.lead.gitDir(), "hooks")
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, h := range hooks {
		for _, name := range []string{h, h + ".sample"} {
			if err := os.WriteFile(filepath.Join(hookDir, name), []byte(script("hook-"+name)), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	other := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, h := range hooks {
		if err := os.WriteFile(filepath.Join(other, h), []byte(script("hookspath-"+h)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := func(name string) string { return "touch '" + filepath.Join(canaries, name) + "'" }
	for k, v := range map[string]string{
		"core.hooksPath":             other,
		"core.fsmonitor":             cmd("fsmonitor"),
		"uploadpack.packObjectsHook": cmd("packobjectshook"),
		"core.alternateRefsCommand":  cmd("alternaterefscommand"),
		"core.sshCommand":            cmd("sshcommand"),
		"core.pager":                 cmd("pager"),
		"core.editor":                cmd("editor"),
		"credential.helper":          "!" + cmd("credential"),
		"gc.auto":                    "1",
		"uploadpack.allowFilter":     "true",
		"transfer.hideRefs":          "refs/heads/nothing",
		"core.attributesFile":        "/nonexistent",
		"filter.lfs.process":         cmd("filter"),
		"diff.external":              cmd("diffexternal"),
		"remote.origin.url":          "/nonexistent",
		"url./elsewhere/.insteadOf":  e.lead.tree + "/",
		"include.path":               "/nonexistent",
		"safe.directory":             "*",
		"receive.denyCurrentBranch":  "ignore",
		"uploadpack.hideRefs":        "refs/heads/nothing",
	} {
		e.lead.git(t, "config", k, v)
	}
	rep, err := e.m.Fetch(context.Background(), e.req())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(rep.Updates) != 2 {
		t.Errorf("updates = %+v", rep.Updates)
	}
	entries, err := os.ReadDir(canaries)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		t.Errorf("canary %s was written by the fetch", ent.Name())
	}
	noLeftovers(t, e.m)

	// The control: the canaries are armed. Plain git in the clone runs the
	// hooks and the file system monitor the lead set up. (The hook path of
	// the config wins over the hooks folder, so the commit runs those of
	// core.hooksPath.) uploadpack.packObjectsHook and
	// core.alternateRefsCommand are not read from a repository's config by
	// upload-pack, or only when the repository has alternates, which the
	// owner-side check refuses; the fetch above shows they did not fire.
	e.lead.git(t, "commit", "--quiet", "--no-gpg-sign", "--allow-empty", "-m", "control")
	e.lead.git(t, "status", "--short")
	got, err := os.ReadDir(canaries)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, ent := range got {
		have[ent.Name()] = true
	}
	for _, want := range []string{"hookspath-pre-commit", "fsmonitor"} {
		if !have[want] {
			t.Errorf("the control did not fire %s, so the canary is not armed", want)
		}
	}
}

func TestFetchLeavesNoProcessBehindTheUploadPack(t *testing.T) {
	// A hostile upload-pack that forks a process which outlives it must not
	// outlive the fetch: boundedexec kills the whole group.
	e := newFetchEnv(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	e.planner.UploadPack = e.wrapper("(sleep 120 & echo $! > '" + pidFile + "') ")
	if _, err := e.m.Fetch(context.Background(), e.req()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(string(data))
	out, _ := exec.Command("/bin/sh", "-c", "kill -0 "+pid+" 2>&1; echo $?").Output()
	if !strings.HasSuffix(strings.TrimSpace(string(out)), "1") {
		t.Errorf("the process %s of the upload-pack is still running: %s", pid, out)
		_ = exec.Command("/bin/kill", pid).Run()
	}
}
