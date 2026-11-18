//go:build linux && ataniq_canary

package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
)

// fakeGit writes a program that fails with status 1 when one of its arguments
// is failOn and otherwise runs the real git, and returns its path.
func fakeGit(t testing.TB, failOn string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = '" + failOn + "' ]; then echo 'sample failure' >&2; exit 1; fi\ndone\nexec /usr/bin/git \"$@\"\n"
	provisiontest.WriteFile(t, p, []byte(script), 0o700)
	return p
}

// recordingGit writes a program that writes its whole environment, one entry
// per line, to the file the first time it runs, and then runs the real git.
func recordingGit(t testing.TB, record string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = clone ]; then /usr/bin/env > '" + record + "'; fi\ndone\nexec /usr/bin/git \"$@\"\n"
	provisiontest.WriteFile(t, p, []byte(script), 0o700)
	return p
}

func TestTheGitEnvironmentIsExplicitAndMinimal(t *testing.T) {
	// Poison the test process: none of this may reach git.
	for k, v := range map[string]string{
		"HOME": "/nonexistent-home", "GIT_DIR": "/nonexistent-git-dir", "GIT_ASKPASS": "/nonexistent-askpass",
		"SSH_AUTH_SOCK": "/nonexistent-agent", "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "credential.helper",
		"GIT_CONFIG_VALUE_0": "store", "XDG_CONFIG_HOME": "/nonexistent-xdg", "GIT_TEMPLATE_DIR": "/nonexistent-template",
		"EDITOR": "/nonexistent-editor", "GIT_EXEC_PATH": "/nonexistent-exec",
	} {
		t.Setenv(k, v)
	}
	cmd := gitCommand(context.Background(), "/", "version")
	if !slices.Equal(cmd.Env, GitEnvironment()) {
		t.Fatalf("env %q, want exactly %q", cmd.Env, GitEnvironment())
	}
	if filepath.Base(cmd.Path) != "git" || cmd.Path != GitPath {
		t.Fatalf("path %q", cmd.Path)
	}

	// And the process that really runs sees only that.
	w := provisiontest.New(t)
	record := filepath.Join(t.TempDir(), "env")
	old := GitPath
	t.Cleanup(func() { GitPath = old })
	GitPath = recordingGit(t, record)
	if _, err := provisionWorld(w); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(readString(t, record)), "\n") {
		if k, _, _ := strings.Cut(l, "="); k == "PWD" || k == "SHLVL" || k == "_" || k == "OLDPWD" {
			continue // added by the shell that runs the recording program
		}
		got = append(got, l)
	}
	want := slices.Clone(GitEnvironment())
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("git ran with %q, want %q", got, want)
	}
}

func TestGitIsRunWithSettingsThatKeepItToALocalClone(t *testing.T) {
	cmd := gitCommand(context.Background(), "/", "clone")
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"protocol.allow=never", "protocol.file.allow=always", "credential.helper=", "core.hooksPath=/dev/null"} {
		if !strings.Contains(joined, want) {
			t.Errorf("git runs without %s: %s", want, joined)
		}
	}
	if cmd.Stdin != nil {
		t.Error("git has an input")
	}
}

func TestTheTreesOnlyRemoteIsTheMirrorAndNoCredentialIsInItsConfig(t *testing.T) {
	// The caller's own git configuration is hostile: a credential helper, a
	// rewrite of every URL, an include, a template directory with a hook.
	home := t.TempDir()
	tmpl := filepath.Join(home, "template")
	if err := os.MkdirAll(filepath.Join(tmpl, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	provisiontest.WriteFile(t, filepath.Join(tmpl, "hooks", "post-checkout"), []byte("#!/bin/sh\ntouch "+filepath.Join(home, "hook-ran")+"\n"), 0o755)
	provisiontest.WriteFile(t, filepath.Join(home, ".gitconfig"), []byte("[credential]\n\thelper = store\n[url \"https://example.invalid/\"]\n\tinsteadOf = /\n[init]\n\ttemplateDir = "+tmpl+"\n[core]\n\thooksPath = "+filepath.Join(tmpl, "hooks")+"\n"), 0o600)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))

	w, p, _ := provisioned(t)
	git := func(args ...string) string {
		return provisiontest.Git(t, p.Tree, args...)
	}
	if got := git("remote"); got != "origin" {
		t.Fatalf("remotes %q, want only origin", got)
	}
	if got, want := git("config", "--local", "remote.origin.url"), profile.MirrorInside("throwaway-one"); got != want {
		t.Fatalf("origin is %q, want the mirror's place in the sandbox, %q", got, want)
	}
	if got := git("rev-parse", "--abbrev-ref", "HEAD"); got != provisiontest.Branch {
		t.Fatalf("HEAD %q", got)
	}
	if got := git("rev-parse", "HEAD"); got != w.Head {
		t.Fatalf("HEAD is %s, the mirror's branch is %s", got, w.Head)
	}
	cfg := readString(t, filepath.Join(p.Tree, ".git", "config"))
	for _, bad := range []string{"credential", "helper", "insteadOf", "include", "hooksPath", "example.invalid", "pushurl", "extraheader"} {
		if strings.Contains(strings.ToLower(cfg), strings.ToLower(bad)) {
			t.Errorf("the tree's config holds %q:\n%s", bad, cfg)
		}
	}
	// The same holds of Ataniq's copy, which is what the sandbox reads.
	if got := readString(t, filepath.Join(p.GitMeta, profile.GitMetaConfig)); got != cfg {
		t.Errorf("the git metadata copy differs from the tree's config")
	}
	// No hook: the template of the caller was not used, nor were hooks run.
	if exists(filepath.Join(home, "hook-ran")) {
		t.Fatal("a hook ran")
	}
	if entries, _ := os.ReadDir(filepath.Join(p.Tree, ".git", "hooks")); len(entries) != 0 {
		t.Errorf("the tree has hooks: %v", entries)
	}
	if entries, _ := os.ReadDir(filepath.Join(p.GitMeta, profile.GitMetaHooks)); len(entries) != 0 {
		t.Errorf("the metadata copy has hooks: %v", entries)
	}
}

func TestTheCloneSharesNoFileWithTheMirror(t *testing.T) {
	w, p, _ := provisioned(t)
	count := 0
	err := filepath.WalkDir(filepath.Join(p.Tree, ".git", "objects"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		count++
		if fi.Sys().(*syscall.Stat_t).Nlink != 1 {
			t.Errorf("%s has %d links", path, fi.Sys().(*syscall.Stat_t).Nlink)
		}
		return nil
	})
	if err != nil || count == 0 {
		t.Fatalf("objects: %d, %v", count, err)
	}
	if exists(filepath.Join(p.Tree, ".git", "objects", "info", "alternates")) {
		t.Fatal("the tree has alternates")
	}
	_ = w
}

func TestCheckConfigAcceptsACloneAndRefusesEverythingElse(t *testing.T) {
	const url = "/ataniq/mirror/x.git"
	ok := "core.repositoryformatversion\n0\x00core.filemode\ntrue\x00core.bare\nfalse\x00core.logallrefupdates\ntrue\x00" +
		"remote.origin.url\n" + url + "\x00remote.origin.fetch\n+refs/heads/*:refs/remotes/origin/*\x00" +
		"branch.main.remote\norigin\x00branch.main.merge\nrefs/heads/main\x00"
	if err := checkConfig([]byte(ok), url); err != nil {
		t.Fatalf("a clone's config: %v", err)
	}
	for name, extra := range map[string]string{
		"credential helper":   "credential.helper\nstore\x00",
		"scoped credential":   "credential.https://example.invalid.helper\nstore\x00",
		"url rewrite":         "url.https://example.invalid/.insteadof\n/\x00",
		"push url":            "remote.origin.pushurl\nhttps://example.invalid/x\x00",
		"second remote":       "remote.other.url\n/x\x00",
		"extra header":        "http.extraheader\nAuthorization: x\x00",
		"include":             "include.path\n/x\x00",
		"include if":          "includeif.gitdir:/.path\n/x\x00",
		"hooks path":          "core.hookspath\n/x\x00",
		"fsmonitor":           "core.fsmonitor\n/x\x00",
		"ssh command":         "core.sshcommand\n/x\x00",
		"user":                "user.name\nsample\x00",
		"alias":               "alias.p\npush\x00",
		"branch to elsewhere": "branch.x.remote\nother\x00",
		"branch merge":        "branch.x.merge\nnot-a-ref\x00",
		"a key with no value": "credential.helper\x00",
	} {
		if err := checkConfig([]byte(ok+extra), url); !errors.Is(err, ErrBadCloneConfig) {
			t.Errorf("%s: %v", name, err)
		} else if strings.Contains(err.Error(), "store") || strings.Contains(err.Error(), "example.invalid") || strings.Contains(err.Error(), "Authorization") {
			t.Errorf("%s: the error holds a value: %v", name, err)
		}
	}
	for name, bad := range map[string]string{
		"wrong url":   strings.Replace(ok, url, "/elsewhere.git", 1),
		"wrong fetch": strings.Replace(ok, "+refs/heads/*:refs/remotes/origin/*", "+refs/*:refs/*", 1),
		"no url":      "core.bare\nfalse\x00",
		"empty":       "",
	} {
		if err := checkConfig([]byte(bad), url); !errors.Is(err, ErrBadCloneConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTheGitIdentityIsWrittenFromTheConfigAndNothingCanEscapeTheQuoting(t *testing.T) {
	w, p, _ := provisioned(t)
	get := func(key string) string {
		return provisiontest.Git(t, p.Tree, "config", "--file", p.GitIdentityFile, key)
	}
	if got := get("user.name"); got != w.Cfg.Provision.GitName {
		t.Errorf("name %q, want %q", got, w.Cfg.Provision.GitName)
	}
	if got := get("user.email"); got != w.Cfg.Provision.GitEmail {
		t.Errorf("email %q, want %q", got, w.Cfg.Provision.GitEmail)
	}
	if got := provisiontest.Git(t, p.Tree, "config", "--file", p.GitIdentityFile, "--list"); strings.Count(got, "\n") != 1 {
		t.Errorf("the identity file holds more than the name and the address:\n%s", got)
	}
	// Hostile text in either cannot start a section or end the value early.
	for _, name := range []string{`a"b`, `a\b`, `a\"b`, `a#b;c`, `]`, `"], [credential] helper = store #`, `trailing\`} {
		content := identityConfig(name, "x@y.invalid")
		f := filepath.Join(t.TempDir(), "config")
		provisiontest.WriteFile(t, f, content, 0o600)
		if got := provisiontest.Git(t, p.Tree, "config", "--file", f, "user.name"); got != name {
			t.Errorf("name %q read back as %q", name, got)
		}
		lines := strings.Split(provisiontest.Git(t, p.Tree, "config", "--file", f, "--list"), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "user.name=") || !strings.HasPrefix(lines[1], "user.email=") {
			t.Errorf("name %q changed the entries: %q", name, lines)
		}
	}
}

func TestAMissingMirrorOrBranchRefusesBeforeAnythingIsMade(t *testing.T) {
	cases := map[string]func(w *provisiontest.World){
		"no mirror": func(w *provisiontest.World) {
			if err := os.RemoveAll(w.Mirror); err != nil {
				t.Fatal(err)
			}
		},
		"no such branch": func(w *provisiontest.World) {
			w.Cfg.Provision.UpstreamBranch = "other"
			w.WriteConfig(t)
		},
		"not a mirror": func(w *provisiontest.World) {
			if err := os.Remove(filepath.Join(w.Mirror, "ataniq-mirror")); err != nil {
				t.Fatal(err)
			}
		},
		"mirror elsewhere": func(w *provisiontest.World) {
			w.Cfg.Provision.Mirror = filepath.Join(w.Dir, "other.git")
			w.WriteConfig(t)
		},
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			w := provisiontest.New(t)
			mod(w)
			before := snapshot(t, w.State)
			lines, err := provisionWorld(w)
			if !errors.Is(err, ErrMirror) {
				t.Fatalf("Provision: %v, want ErrMirror", err)
			}
			if len(lines) != 0 || snapshot(t, w.State) != before || exists(w.Runtime) {
				t.Fatal("the refused run made something")
			}
		})
	}
}

func TestGitIsNotFoundThenTheRunFailsWithoutMakingAnything(t *testing.T) {
	w := provisiontest.New(t)
	old := GitPath
	t.Cleanup(func() { GitPath = old })
	GitPath = filepath.Join(t.TempDir(), "no-git")
	before := snapshot(t, w.State)
	if _, err := provisionWorld(w); err == nil || !errors.Is(err, ErrMirror) {
		t.Fatalf("Provision: %v", err)
	}
	if snapshot(t, w.State) != before {
		t.Fatal("made something")
	}
}

func TestTheCloneGoesIntoTheFolderBehindTheHandleWhateverItsNameBecomes(t *testing.T) {
	w := provisiontest.New(t)
	parent := filepath.Join(w.Dir, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(parent, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := safefile.OpenRootNoLinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	h, err := root.OpenDirHandle("tree")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	// After the handle is opened, the name is moved away and a link to some
	// other folder takes its place.
	elsewhere := filepath.Join(w.Dir, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(parent, "tree"), filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(parent, "tree")); err != nil {
		t.Fatal(err)
	}
	if err := cloneInto(context.Background(), h, w.Mirror, provisiontest.Branch, profile.MirrorInside("throwaway-one")); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(parent, "moved", ".git")) {
		t.Fatal("the clone is not in the folder behind the handle")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("the clone followed the name: %v", entries)
	}
}

func TestACloneOfABranchThatIsNotThereFails(t *testing.T) {
	w := provisiontest.New(t)
	root, err := safefile.OpenRootNoLinks(w.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Mkdir(filepath.Join(w.Dir, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	h, err := root.OpenDirHandle("tree")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := cloneInto(context.Background(), h, w.Mirror, "no-such-branch", "/x"); err == nil {
		t.Fatal("cloned a branch that is not there")
	}
}
