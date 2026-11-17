package gitenv

// These tests run the installed git on temporary repositories. The command
// under test always gets the argv and the environment from this package. The
// fixtures (a source repository with commits, a planted configuration) are
// made with a separate, explicit environment, so the developer's own git
// configuration plays no part.
//
// Most cases have a control: the same planted trap run with plain git and a
// plain environment, which must fire. A case without a control proves nothing,
// because a canary that was never able to fire stays absent for any reason.
// When a control does not fire, the test fails and says so.
//
// The cases prove that the settings and the environment of this package
// switch the traps off. They cannot prove that git is safe to run in a
// repository an attacker controls, and they do not try: owner-side code only
// uses repositories Ataniq wrote, and the settings are defence in depth.

import (
	"bytes"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// needGit returns the installed git, or fails the test with a clear message.
func needGit(t *testing.T) Git {
	t.Helper()
	g, err := Find()
	if err != nil {
		t.Fatalf("these tests need an installed git at one of %v: %v", fixedPaths, err)
	}
	return g
}

// fixtureEnv is the environment of the commands that build fixtures and
// controls. It names everything git could read from the machine.
func fixtureEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	home := t.TempDir()
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + home,
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Fixture",
		"GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=Fixture",
		"GIT_COMMITTER_EMAIL=fixture@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}
	return append(env, extra...)
}

// run starts argv with env in dir and returns stdout and stderr together.
func run(argv, env []string, dir string, stdin string) (string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// fixture runs plain git with the fixture environment and fails on error.
func fixture(t *testing.T, g Git, env []string, dir string, args ...string) string {
	t.Helper()
	return fixtureIn(t, g, env, dir, "", args...)
}

func fixtureIn(t *testing.T, g Git, env []string, dir, stdin string, args ...string) string {
	t.Helper()
	out, err := run(append([]string{g.Path}, args...), env, dir, stdin)
	if err != nil {
		t.Fatalf("fixture git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(out)
}

// owned runs a command built by this package and returns its output.
func owned(g Git, c Cmd) (string, error) {
	argv, err := g.Argv(c)
	if err != nil {
		return "", err
	}
	return run(argv, g.Env(c), c.Dir, "")
}

func mustOwned(t *testing.T, g Git, c Cmd) string {
	t.Helper()
	out, err := owned(g, c)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", c.Args, err, out)
	}
	return strings.TrimSpace(out)
}

// canary makes an executable script that records that it ran, and returns
// the script path and the path of the file it writes. The script prints
// nothing and succeeds.
func canary(t *testing.T) (script, mark string) {
	t.Helper()
	dir := t.TempDir()
	script = filepath.Join(dir, "canary.sh")
	mark = filepath.Join(dir, "ran")
	body := "#!/bin/sh\n: > '" + mark + "'\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, mark
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// mustFire fails when a control did not write its canary.
func mustFire(t *testing.T, mark, what string) {
	t.Helper()
	if !exists(mark) {
		t.Fatalf("control failed: %s did not run the canary with plain git, so the test could not show anything", what)
	}
	if err := os.Remove(mark); err != nil {
		t.Fatal(err)
	}
}

func mustNotFire(t *testing.T, mark, what string) {
	t.Helper()
	if exists(mark) {
		t.Fatalf("%s ran the canary although the settings of this package were in force", what)
	}
}

// source makes a repository with a working tree and two commits on main,
// and returns its path. file holds "one" and then "two".
func source(t *testing.T, g Git) string {
	t.Helper()
	env := fixtureEnv(t)
	dir := filepath.Join(t.TempDir(), "src")
	fixture(t, g, env, "", "init", "-q", "-b", "main", dir)
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture(t, g, env, dir, "add", "file")
	fixture(t, g, env, dir, "commit", "-q", "-m", "one")
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture(t, g, env, dir, "commit", "-q", "-a", "-m", "two")
	return dir
}

// bare makes an empty bare repository with this package's own "init".
func bare(t *testing.T, g Git) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mirror.git")
	mustOwned(t, g, Cmd{Args: []string{"init", "-q", "--bare", "--initial-branch=main", dir}})
	return dir
}

// fetchAll fetches every branch of src into the bare repository.
func fetchAll(g Git, gitDir, src string, protocols ...string) (string, error) {
	return owned(g, Cmd{GitDir: gitDir, Protocols: protocols, Args: []string{"fetch", "--end-of-options", src, "+refs/heads/*:refs/heads/*"}})
}

func appendConfig(t *testing.T, gitDir, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(gitDir, "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

// TestSettingsAreKeysOfThisGit checks every key of the fixed -c list against
// the list the installed git prints, so a misspelt or retired key fails here
// instead of doing nothing in production. It also checks that git reports
// each value back from the command line.
func TestSettingsAreKeysOfThisGit(t *testing.T) {
	g := needGit(t)
	out, err := run([]string{g.Path, "help", "--config"}, fixtureEnv(t), "", "")
	if err != nil {
		t.Fatalf("git help --config: %v\n%s", err, out)
	}
	known := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		known[strings.TrimSpace(line)] = true
	}
	if !known["core.hooksPath"] {
		t.Fatalf("git help --config does not list core.hooksPath; the list could not be read:\n%.200s", out)
	}
	repo := bare(t, g)
	for _, kv := range settings([]string{"file"}) {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			t.Errorf("setting %q has no =", kv)
			continue
		}
		listed := key
		if strings.HasPrefix(key, "protocol.") && strings.HasSuffix(key, ".allow") && key != "protocol.allow" {
			listed = "protocol.<name>.allow"
		}
		if strings.HasPrefix(key, "gpg.") && strings.HasSuffix(key, ".program") && key != "gpg.program" {
			listed = "gpg.<format>.program"
		}
		if !known[listed] {
			t.Errorf("git does not know the key %q", key)
		}
		got := mustOwned(t, g, Cmd{GitDir: repo, Protocols: []string{"file"}, Args: []string{"config", "--get-all", key}})
		if got != value {
			t.Errorf("git reports %s = %q, want %q", key, got, value)
		}
	}
}

// TestEnvironmentReachesGit asks git for its configuration with the planted
// variables of the test process around, and shows that no file outside the
// repository supplies any of it.
func TestEnvironmentReachesGit(t *testing.T) {
	g := needGit(t)
	repo := bare(t, g)
	out := mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"config", "--list", "--show-origin"}})
	for _, line := range strings.Split(out, "\n") {
		origin, _, _ := strings.Cut(line, "\t")
		if strings.HasPrefix(origin, "file:") && origin != "file:"+filepath.Join(repo, "config") {
			t.Errorf("a configuration file other than the repository's was read: %q", line)
		}
	}
}

// TestGlobalConfigIsNotRead plants a global configuration at the HOME and
// XDG_CONFIG_HOME of the test process, with an alias and a core.fsmonitor
// command that would write a canary file.
//
// Proves: none of the planted files is read by a command from this package,
// whatever HOME and XDG_CONFIG_HOME the test process has. The control shows
// the planted files do work with plain git.
// Cannot prove: that no other file on a machine is read; the environment only
// sets GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM and HOME to places that hold none.
func TestGlobalConfigIsNotRead(t *testing.T) {
	g := needGit(t)
	script, mark := canary(t)

	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	config := "[alias]\n\tzz = version\n[core]\n\tfsmonitor = " + script + "\n"
	for _, p := range []string{
		filepath.Join(home, ".gitconfig"),
		filepath.Join(home, ".config", "git", "config"),
		filepath.Join(xdg, "git", "config"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	work := source(t, g)
	if err := os.WriteFile(filepath.Join(work, "file"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(work, ".git")

	// Control: plain git with the planted HOME knows the alias and runs the
	// monitor.
	planted := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=" + xdg, "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1"}
	out, err := run([]string{g.Path, "zz"}, planted, work, "")
	if err != nil || !strings.HasPrefix(out, "git version") {
		t.Fatalf("control failed: the planted alias does not work with plain git (%v): %q", err, out)
	}
	if out, err := run([]string{g.Path, "--git-dir=" + gitDir, "diff", "--stat"}, planted, work, ""); err != nil {
		t.Fatalf("control: git diff: %v\n%s", err, out)
	}
	mustFire(t, mark, "the planted core.fsmonitor")

	// This package: the alias is unknown and the monitor does not run.
	argv, err := g.Argv(Cmd{GitDir: gitDir, Args: []string{"version"}})
	if err != nil {
		t.Fatal(err)
	}
	argv = append(argv[:len(argv)-1], "zz")
	out, err = run(argv, g.Env(Cmd{GitDir: gitDir}), work, "")
	if err == nil || !strings.Contains(out, "'zz' is not a git command") {
		t.Fatalf("the alias from the planted global configuration is known (%v): %q", err, out)
	}
	diff := mustOwned(t, g, Cmd{GitDir: gitDir, Dir: work, Args: []string{"diff", "--stat"}})
	if !strings.Contains(diff, "file") {
		t.Fatalf("the diff did not run on the work tree: %q", diff)
	}
	mustNotFire(t, mark, "git diff")
	if out, err := owned(g, Cmd{GitDir: gitDir, Args: []string{"config", "--get", "alias.zz"}}); err == nil {
		t.Errorf("git config found the planted alias: %q", out)
	}
	if got := mustOwned(t, g, Cmd{GitDir: gitDir, Args: []string{"config", "--get", "core.fsmonitor"}}); got != "false" {
		t.Errorf("core.fsmonitor = %q, want the fixed false", got)
	}
}

// TestRepositoryConfigIsOverridden gives a repository whose own config sets
// a program for core.fsmonitor, core.hooksPath, core.pager and diff.external.
//
// Proves: for the commands that would use each setting, the command line of
// this package wins over the repository's config, and the canary is not
// written. Each case has a control that fires with plain git.
// Cannot prove: that every key in a repository's config is covered. Only keys
// named in the settings list, and the two diff options, are. Owner-side code
// only uses repositories Ataniq wrote, so a repository config with these keys
// is not expected; this is defence in depth.
func TestRepositoryConfigIsOverridden(t *testing.T) {
	g := needGit(t)

	t.Run("core.fsmonitor", func(t *testing.T) {
		// Only a command that refreshes an index uses the monitor, and a
		// bare repository has none. So this case runs "diff" in a work
		// tree, the one allowed command that does.
		script, mark := canary(t)
		work := source(t, g)
		env := fixtureEnv(t)
		fixture(t, g, env, work, "config", "core.fsmonitor", script)
		if err := os.WriteFile(filepath.Join(work, "file"), []byte("three\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitDir := filepath.Join(work, ".git")
		if out, err := run([]string{g.Path, "--git-dir=" + gitDir, "diff", "--stat"}, env, work, ""); err != nil {
			t.Fatalf("control: %v\n%s", err, out)
		}
		mustFire(t, mark, "core.fsmonitor in the repository config")
		mustOwned(t, g, Cmd{GitDir: gitDir, Dir: work, Args: []string{"diff", "--stat"}})
		mustNotFire(t, mark, "git diff")
	})

	t.Run("core.hooksPath", func(t *testing.T) {
		// update-ref runs the reference-transaction hook, from the folder
		// core.hooksPath names.
		script, mark := canary(t)
		work := source(t, g)
		env := fixtureEnv(t)
		hooks := t.TempDir()
		if err := os.Symlink(script, filepath.Join(hooks, "reference-transaction")); err != nil {
			t.Fatal(err)
		}
		fixture(t, g, env, work, "config", "core.hooksPath", hooks)
		gitDir := filepath.Join(work, ".git")
		fixture(t, g, env, work, "update-ref", "refs/heads/control", "HEAD")
		mustFire(t, mark, "core.hooksPath in the repository config")
		mustOwned(t, g, Cmd{GitDir: gitDir, Args: []string{"update-ref", "refs/heads/owned", "HEAD"}})
		mustNotFire(t, mark, "git update-ref")
		if got := mustOwned(t, g, Cmd{GitDir: gitDir, Args: []string{"rev-parse", "--verify", "refs/heads/owned"}}); !IsObjectID(got) {
			t.Fatalf("update-ref did not make the ref: %q", got)
		}
	})

	t.Run("core.pager", func(t *testing.T) {
		// A pager starts only when standard output is a terminal, so the
		// case runs git on a pseudo-terminal. GIT_PAGER takes precedence
		// over core.pager, so the case drops it from the environment to
		// show that the -c setting alone wins; the second run keeps it.
		script, mark := canary(t)
		work := source(t, g)
		env := fixtureEnv(t)
		fixture(t, g, env, work, "config", "core.pager", script)
		gitDir := filepath.Join(work, ".git")
		// The pager may exit before it reads, so the exit status of the
		// control is not looked at; the canary is.
		runOnTerminal([]string{g.Path, "--git-dir=" + gitDir, "log", "--oneline"}, env, work)
		mustFire(t, mark, "core.pager in the repository config")

		c := Cmd{GitDir: gitDir, Args: []string{"log", "--oneline"}}
		argv, err := g.Argv(c)
		if err != nil {
			t.Fatal(err)
		}
		var withoutPager []string
		for _, kv := range g.Env(c) {
			if !strings.HasPrefix(kv, "GIT_PAGER=") {
				withoutPager = append(withoutPager, kv)
			}
		}
		for name, e := range map[string][]string{"the -c setting alone": withoutPager, "the whole environment": g.Env(c)} {
			if out, err := runOnTerminal(argv, e, work); err != nil {
				t.Fatalf("%s: %v\n%s", name, err, out)
			}
			mustNotFire(t, mark, "git log with "+name)
		}
	})

	t.Run("diff.external", func(t *testing.T) {
		// "git diff" runs the program by default. The package adds
		// --no-ext-diff; the setting diff.external= cannot do this.
		script, mark := canary(t)
		work := source(t, g)
		env := fixtureEnv(t)
		fixture(t, g, env, work, "config", "diff.external", script)
		gitDir := filepath.Join(work, ".git")
		if out, err := run([]string{g.Path, "--git-dir=" + gitDir, "diff", "HEAD~1", "HEAD"}, env, work, ""); err != nil {
			t.Fatalf("control: %v\n%s", err, out)
		}
		mustFire(t, mark, "diff.external in the repository config")
		out := mustOwned(t, g, Cmd{GitDir: gitDir, Args: []string{"diff", "HEAD~1", "HEAD"}})
		if !strings.Contains(out, "+two") {
			t.Fatalf("git diff printed no patch: %q", out)
		}
		mustNotFire(t, mark, "git diff")
	})
}

// TestHooksDoNotRun plants hooks in a bare repository's hooks folder.
//
// Proves: neither update-ref nor fetch run a hook from the repository's own
// hooks folder when the command comes from this package. The control shows
// the same hook runs with plain git for both commands.
// Cannot prove: anything about post-update and the other hooks that only
// git-receive-pack runs; they are planted to show the trap, and no allowed
// command reaches them.
func TestHooksDoNotRun(t *testing.T) {
	g := needGit(t)
	src := source(t, g)
	tip := mustOwned(t, g, Cmd{GitDir: filepath.Join(src, ".git"), Args: []string{"rev-parse", "--verify", "refs/heads/main"}})

	plant := func(t *testing.T) (repo string, marks map[string]string) {
		repo = bare(t, g)
		marks = map[string]string{}
		for _, name := range []string{"reference-transaction", "post-update", "post-receive", "pre-receive", "update", "post-checkout", "pre-auto-gc"} {
			script, mark := canary(t)
			if err := os.Symlink(script, filepath.Join(repo, "hooks", name)); err != nil {
				t.Fatal(err)
			}
			marks[name] = mark
		}
		return repo, marks
	}
	ran := func(marks map[string]string) []string {
		var names []string
		for name, mark := range marks {
			if exists(mark) {
				names = append(names, name)
			}
		}
		return names
	}
	plainEnv := fixtureEnv(t, "GIT_ALLOW_PROTOCOL=file")

	t.Run("update-ref", func(t *testing.T) {
		repo, marks := plant(t)
		// The objects come first, without hooks, so update-ref has a target.
		if _, err := run([]string{g.Path, "--git-dir=" + repo, "fetch", "-q", "--end-of-options", src, "refs/heads/main:refs/remotes/seed"}, plainEnv, "", ""); err != nil {
			t.Fatal("seeding the repository failed")
		}
		for _, m := range marks {
			os.Remove(m)
		}
		if out, err := run([]string{g.Path, "--git-dir=" + repo, "update-ref", "refs/heads/control", tip}, plainEnv, "", ""); err != nil {
			t.Fatalf("control: %v\n%s", err, out)
		}
		mustFire(t, marks["reference-transaction"], "update-ref with a hook in the hooks folder")
		mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"update-ref", "refs/heads/owned", tip}})
		if got := ran(marks); len(got) != 0 {
			t.Fatalf("update-ref ran hooks %v", got)
		}
	})

	t.Run("fetch", func(t *testing.T) {
		repo, marks := plant(t)
		if out, err := run([]string{g.Path, "--git-dir=" + repo, "fetch", "-q", "--end-of-options", src, "refs/heads/main:refs/heads/control"}, plainEnv, "", ""); err != nil {
			t.Fatalf("control: %v\n%s", err, out)
		}
		mustFire(t, marks["reference-transaction"], "fetch with a hook in the hooks folder")
		for _, m := range marks {
			os.Remove(m)
		}
		if out, err := fetchAll(g, repo, src, "file"); err != nil {
			t.Fatalf("fetch: %v\n%s", err, out)
		}
		if got := ran(marks); len(got) != 0 {
			t.Fatalf("fetch ran hooks %v", got)
		}
		if got := mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"rev-parse", "--verify", "refs/heads/main"}}); got != tip {
			t.Fatalf("fetch did not bring the branch: %q, want %q", got, tip)
		}
	})
}

// TestTransports fetches from URLs of transports that are not allowed.
//
// Proves: ext::, http, https, ssh and git URLs are refused by the protocol
// settings before anything is started: the ext:: command would write a
// canary file, and the control shows it does when the transport is allowed.
// Nothing is contacted, because the refusal comes before a connection.
// A local path works only when Protocols holds "file".
// Cannot prove: that a transport git adds in a later version stays refused;
// protocol.allow=never denies the unnamed ones.
func TestTransports(t *testing.T) {
	g := needGit(t)
	src := source(t, g)

	t.Run("ext", func(t *testing.T) {
		script, mark := canary(t)
		repo := bare(t, g)
		url := "ext::" + script + " %S"
		control := fixtureEnv(t, "GIT_ALLOW_PROTOCOL=ext")
		run([]string{g.Path, "--git-dir=" + repo, "fetch", "--end-of-options", url, "+refs/heads/*:refs/heads/*"}, control, "", "")
		mustFire(t, mark, "an ext:: URL with the transport allowed")

		for _, protocols := range [][]string{nil, {"file"}} {
			out, err := fetchAll(g, repo, url, protocols...)
			if err == nil {
				t.Fatalf("a fetch from an ext:: URL succeeded: %s", out)
			}
			if !strings.Contains(out, "transport 'ext' not allowed") {
				t.Fatalf("the fetch failed, but not for the protocol policy (Protocols %v): %q", protocols, out)
			}
			mustNotFire(t, mark, "a fetch from an ext:: URL")
		}
	})

	for _, tc := range []struct{ name, url, transport string }{
		{"http", "http://127.0.0.1:9/r.git", "http"},
		{"https", "https://127.0.0.1:9/r.git", "https"},
		{"ssh", "ssh://127.0.0.1:9/r.git", "ssh"},
		{"scp-like ssh", "127.0.0.1:r.git", "ssh"},
		{"git", "git://127.0.0.1:9/r.git", "git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := bare(t, g)
			for _, protocols := range [][]string{nil, {"file"}} {
				out, err := fetchAll(g, repo, tc.url, protocols...)
				if err == nil {
					t.Fatalf("a fetch from %s succeeded: %s", tc.url, out)
				}
				want := "transport '" + tc.transport + "' not allowed"
				if !strings.Contains(out, want) {
					t.Fatalf("the fetch failed, but not for the protocol policy (Protocols %v), want %q: %q", protocols, want, out)
				}
			}
		})
	}

	t.Run("file", func(t *testing.T) {
		repo := bare(t, g)
		out, err := fetchAll(g, repo, src)
		if err == nil || !strings.Contains(out, "transport 'file' not allowed") {
			t.Fatalf("a local fetch without the file protocol: %v\n%s", err, out)
		}
		for name, url := range map[string]string{"path": src, "file URL": "file://" + src} {
			repo := bare(t, g)
			if out, err := fetchAll(g, repo, url, "file"); err != nil {
				t.Fatalf("a fetch from a %s with Protocols file: %v\n%s", name, err, out)
			}
			refs := mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"for-each-ref", "--format=%(refname)"}})
			if refs != "refs/heads/main" {
				t.Fatalf("%s: refs after the fetch: %q", name, refs)
			}
		}
	})
}

// TestFetchChecksObjects fetches a commit whose tree holds a ".git" entry.
//
// Proves: fsck on fetch refuses the object, and the fetch leaves no ref and
// no object in the repository. The control shows the same fetch works with
// plain git, so the refusal comes from the settings.
// Cannot prove: that every kind of bad object is refused; git's fsck decides.
func TestFetchChecksObjects(t *testing.T) {
	g := needGit(t)
	for _, name := range []string{".git", ".GIT", ".Git"} {
		t.Run(name, func(t *testing.T) {
			env := fixtureEnv(t)
			evil := filepath.Join(t.TempDir(), "evil")
			fixture(t, g, env, "", "init", "-q", "--bare", "-b", "main", evil)

			empty := fixtureIn(t, g, env, evil, "", "hash-object", "-t", "tree", "-w", "--stdin")
			raw, err := hex.DecodeString(empty)
			if err != nil {
				t.Fatalf("object id %q: %v", empty, err)
			}
			tree := fixtureIn(t, g, env, evil, "40000 "+name+"\x00"+string(raw), "hash-object", "-t", "tree", "-w", "--literally", "--stdin")
			commit := fixture(t, g, env, evil, "commit-tree", "-m", "evil", tree)
			fixture(t, g, env, evil, "update-ref", "refs/heads/main", commit)

			control := bare(t, g)
			if out, err := run([]string{g.Path, "--git-dir=" + control, "fetch", "-q", "--end-of-options", evil, "+refs/heads/*:refs/heads/*"}, fixtureEnv(t, "GIT_ALLOW_PROTOCOL=file"), "", ""); err != nil {
				t.Fatalf("control: plain git refused the object, so the test shows nothing: %v\n%s", err, out)
			}

			repo := bare(t, g)
			out, err := fetchAll(g, repo, evil, "file")
			if err == nil {
				t.Fatalf("the fetch of a tree with a %q entry succeeded: %s", name, out)
			}
			if !strings.Contains(out, "fsck") && !strings.Contains(out, "treeHasDot") && !strings.Contains(out, "contains") {
				t.Fatalf("the fetch failed, but the message does not name the check: %q", out)
			}
			if refs := mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"for-each-ref"}}); refs != "" {
				t.Fatalf("the failed fetch left refs: %q", refs)
			}
			if out, err := owned(g, Cmd{GitDir: repo, Args: []string{"cat-file", "-e", commit}}); err == nil {
				t.Fatalf("the failed fetch left the commit %s: %q", commit, out)
			}
		})
	}
}

// TestCommandsInConfigAreNotRun covers two commands that git-fetch can start
// from configuration.
//
// core.alternateRefsCommand, in the receiving repository's config, runs when
// the repository has an alternate object store. The control shows it runs
// with plain git. With this package it does not, and the alternate still
// serves its objects.
//
// uploadpack.packObjectsHook, in the source repository's config, is
// protected configuration: git reads it from the command line and the user
// and system files only, never from a repository. There is no control, since
// no plain command runs it either; the case shows that the hook stays
// unread and the fetch still works. This is why the package does not set the
// key: an empty value would run an empty command in git-upload-pack.
func TestCommandsInConfigAreNotRun(t *testing.T) {
	g := needGit(t)
	src := source(t, g)

	t.Run("alternateRefsCommand", func(t *testing.T) {
		script, mark := canary(t)
		// The alternate is a second copy of the source's objects.
		withAlternate := func() string {
			repo := bare(t, g)
			alt := filepath.Join(src, ".git", "objects")
			if err := os.WriteFile(filepath.Join(repo, "objects", "info", "alternates"), []byte(alt+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			appendConfig(t, repo, "[core]\n\talternateRefsCommand = "+script+"\n")
			return repo
		}
		other := bare(t, g)
		if out, err := fetchAll(g, other, src, "file"); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		control := withAlternate()
		if out, err := run([]string{g.Path, "--git-dir=" + control, "fetch", "-q", "--end-of-options", other, "+refs/heads/*:refs/heads/*"}, fixtureEnv(t, "GIT_ALLOW_PROTOCOL=file"), "", ""); err != nil {
			t.Fatalf("control: %v\n%s", err, out)
		}
		mustFire(t, mark, "core.alternateRefsCommand with an alternate")

		repo := withAlternate()
		if out, err := fetchAll(g, repo, other, "file"); err != nil {
			t.Fatalf("fetch: %v\n%s", err, out)
		}
		mustNotFire(t, mark, "a fetch into a repository with an alternate")
		tip := mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"rev-parse", "--verify", "refs/heads/main"}})
		mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"cat-file", "-e", tip + "^{commit}"}})
	})

	// The setting is the empty string. git does not skip an empty command: it
	// tries to start it, fails, prints "error: cannot run : No such file or
	// directory" for each use and goes on without the alternate's refs. The
	// fetch succeeds and the alternate's objects still serve. This case pins
	// that behaviour, so the comment in settings stays true: the error line
	// is the expected, harmless sign that no command ran.
	t.Run("alternateRefsCommand is an empty command", func(t *testing.T) {
		script, mark := canary(t)
		env := fixtureEnv(t, "GIT_ALLOW_PROTOCOL=file")
		alt := bare(t, g)
		if out, err := fetchAll(g, alt, src, "file"); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		// A commit that the alternate does not have yet.
		fixture(t, g, env, src, "commit", "-q", "--allow-empty", "-m", "three")
		stage := func() string {
			repo := bare(t, g)
			if err := os.WriteFile(filepath.Join(repo, "objects", "info", "alternates"), []byte(filepath.Join(alt, "objects")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			appendConfig(t, repo, "[core]\n\talternateRefsCommand = "+script+"\n")
			return repo
		}
		const empty = "cannot run : "
		control := stage()
		out, err := run([]string{g.Path, "--git-dir=" + control, "fetch", "--end-of-options", src, "+refs/heads/*:refs/heads/*"}, env, "", "")
		if err != nil {
			t.Fatalf("control: %v\n%s", err, out)
		}
		mustFire(t, mark, "core.alternateRefsCommand with an alternate that has refs")
		if strings.Contains(out, empty) {
			t.Fatalf("control: plain git printed %q:\n%s", empty, out)
		}

		repo := stage()
		out, err = fetchAll(g, repo, src, "file")
		if err != nil {
			t.Fatalf("fetch: %v\n%s", err, out)
		}
		mustNotFire(t, mark, "a fetch into a repository whose alternate has refs")
		if !strings.Contains(out, "error: "+empty) {
			t.Errorf("git did not try to start the empty command (the comment in settings says it does):\n%s", out)
		}
		tip := mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"rev-parse", "--verify", "refs/heads/main"}})
		want := fixture(t, g, env, src, "rev-parse", "main")
		if tip != want {
			t.Errorf("refs/heads/main = %s, want %s", tip, want)
		}
	})

	t.Run("packObjectsHook", func(t *testing.T) {
		script, mark := canary(t)
		fixture(t, g, fixtureEnv(t), src, "config", "uploadpack.packObjectsHook", script)
		repo := bare(t, g)
		if out, err := fetchAll(g, repo, src, "file"); err != nil {
			t.Fatalf("fetch: %v\n%s", err, out)
		}
		mustNotFire(t, mark, "git-upload-pack")
		if refs := mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"for-each-ref", "--format=%(refname)"}}); refs != "refs/heads/main" {
			t.Fatalf("refs after the fetch: %q", refs)
		}
	})
}

// TestDiscoveryStaysInTheWorkingFolder runs the discovery path, which the
// package never uses for a command with a GitDir: git is started without
// --git-dir inside a work tree, in a folder below it.
//
// Proves: with Dir set, git does not walk up out of the working folder, and
// the control shows it finds the repository without. A bare repository in
// the working folder is not used, because of safe.bareRepository=explicit.
func TestDiscoveryStaysInTheWorkingFolder(t *testing.T) {
	g := needGit(t)
	work := source(t, g)
	sub := filepath.Join(work, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// The command line is the one for "version", with the subcommand
	// replaced, so it carries the package's settings and no --git-dir.
	argv, err := g.Argv(Cmd{Args: []string{"version"}})
	if err != nil {
		t.Fatal(err)
	}
	argv = append(argv[:len(argv)-1], "rev-parse", "--git-dir")
	out, err := run(argv, g.Env(Cmd{}), sub, "")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(out), ".git") {
		t.Fatalf("control: git did not find the repository above the folder (%v): %q", err, out)
	}
	out, err = run(argv, g.Env(Cmd{Dir: sub}), sub, "")
	if err == nil {
		t.Fatalf("git walked up out of the working folder: %q", out)
	}

	repo := bare(t, g)
	out, err = run(argv, g.Env(Cmd{Dir: repo}), repo, "")
	if err == nil || !strings.Contains(out, "bare repository") {
		t.Fatalf("git used a bare repository found by discovery (%v): %q", err, out)
	}
	plain := fixtureEnv(t)
	if out, err := run([]string{g.Path, "rev-parse", "--git-dir"}, plain, repo, ""); err != nil {
		t.Fatalf("control: plain git does not use the bare repository: %v\n%s", err, out)
	}
}

// TestMailmapFromTheMirrorIsNotRead puts a .mailmap in the tree of the
// branch that HEAD of the bare mirror names. Plain git reads it from there
// and prints the identity the lead chose for %aN, %aE and the default log
// format. The commands of this package print the identity in the commit.
//
// Proves: a lead-written .mailmap cannot change the author or committer
// that owner-side log output shows. The control shows git does read it.
// Cannot prove: that no other lead-written file is read as configuration.
func TestMailmapFromTheMirrorIsNotRead(t *testing.T) {
	g := needGit(t)
	src := source(t, g)
	env := fixtureEnv(t)
	mailmap := "Somebody Else <boss@example.invalid> <fixture@example.invalid>\n"
	if err := os.WriteFile(filepath.Join(src, ".mailmap"), []byte(mailmap), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture(t, g, env, src, "add", ".mailmap")
	fixture(t, g, env, src, "commit", "-q", "-m", "mailmap")
	mirror := bare(t, g)
	if out, err := fetchAll(g, mirror, src, "file"); err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if got := fixture(t, g, env, "", "--git-dir="+mirror, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD of the mirror is %q, not main", got)
	}

	const format = "--format=%an <%ae> / %aN <%aE> / %cN <%cE>"
	const plain = "Fixture <fixture@example.invalid>"
	const mapped = "Somebody Else <boss@example.invalid>"
	control := fixture(t, g, env, "", "--git-dir="+mirror, "log", "-1", format)
	if want := plain + " / " + mapped + " / " + mapped; control != want {
		t.Fatalf("control failed: plain git printed %q, want %q, so the test could not show anything", control, want)
	}
	if out := fixture(t, g, env, "", "--git-dir="+mirror, "log", "-1"); !strings.Contains(out, "Author: "+mapped) {
		t.Fatalf("control failed: plain git log did not map the author:\n%s", out)
	}

	got := mustOwned(t, g, Cmd{GitDir: mirror, Args: []string{"log", "--max-count=1", format}})
	if want := plain + " / " + plain + " / " + plain; got != want {
		t.Errorf("log format printed %q, want %q", got, want)
	}
	out := mustOwned(t, g, Cmd{GitDir: mirror, Args: []string{"log", "--max-count=1"}})
	if !strings.Contains(out, "Author: Fixture <fixture@example.invalid>") || strings.Contains(out, "Somebody Else") {
		t.Errorf("default log output used the lead's .mailmap:\n%s", out)
	}
}

// TestSignaturePrograms puts commits with a signature of each format into a
// repository, and points the program setting of that format at a canary.
// git picks the program from the format of the signature itself, so a lead
// commit with an x509 or ssh signature must not start a program either.
//
// Proves: no signature program starts when a command of this package shows
// the signature status of a commit. The control shows each canary does run
// with plain git, so the setting names are the right ones.
// Cannot prove: that a signature check is safe in general; it is switched
// off, not made harmless.
func TestSignaturePrograms(t *testing.T) {
	g := needGit(t)
	for _, tc := range []struct {
		name   string
		armor  string
		key    string
		extras []string
	}{
		{"openpgp", "-----BEGIN PGP SIGNATURE-----", "gpg.program", nil},
		{"x509", "-----BEGIN SIGNED MESSAGE-----", "gpg.x509.program", nil},
		{"ssh", "-----BEGIN SSH SIGNATURE-----", "gpg.ssh.program", []string{"gpg.ssh.allowedSignersFile"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, mark := canary(t)
			repo := bare(t, g)
			env := fixtureEnv(t, "GIT_DIR="+repo)
			fixture(t, g, env, "", "config", tc.key, script)
			for _, extra := range tc.extras {
				// The file need not exist; git only needs the setting.
				fixture(t, g, env, "", "config", extra, filepath.Join(t.TempDir(), "signers"))
			}
			tree := fixtureIn(t, g, env, "", "", "mktree")
			commit := "tree " + tree + "\n" +
				"author Fixture <fixture@example.invalid> 1767225600 +0000\n" +
				"committer Fixture <fixture@example.invalid> 1767225600 +0000\n" +
				"gpgsig " + tc.armor + "\n" +
				" AAAA\n" +
				" -----END SIGNATURE-----\n" +
				"\nsigned\n"
			id := fixtureIn(t, g, env, "", commit, "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
			fixture(t, g, env, "", "update-ref", "refs/heads/main", id)

			run([]string{g.Path, "--git-dir=" + repo, "log", "-1", "--show-signature", "--format=%G? %GS"}, env, "", "")
			mustFire(t, mark, tc.key+" in the repository config")

			for _, args := range [][]string{
				{"log", "--max-count=1", "--show-signature", "--format=%G? %GS %GK"},
				{"log", "--max-count=1", "--format=%G?"},
				{"for-each-ref", "--format=%(contents:signature)", "refs/heads/"},
				{"show", "--show-signature", "-s", id},
			} {
				// A bad or unknown signature may exit non-zero or not;
				// the canary is what matters.
				owned(g, Cmd{GitDir: repo, Args: args})
				mustNotFire(t, mark, "git "+strings.Join(args, " "))
			}
		})
	}
}
