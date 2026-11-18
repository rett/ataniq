package gitenv

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestIsObjectID(t *testing.T) {
	sha1 := strings.Repeat("0123456789abcdef", 3)[:40]
	sha256 := strings.Repeat("0123456789abcdef", 4)
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"sha1", sha1, true},
		{"sha256", sha256, true},
		{"empty", "", false},
		{"short", sha1[:39], false},
		{"between", sha1 + "a", false},
		{"long", sha256 + "a", false},
		{"upper case", strings.ToUpper(sha1), false},
		{"not hex", sha1[:39] + "g", false},
		{"newline", sha1[:39] + "\n", false},
		{"ref name", "refs/heads/main", false},
		{"option", "--" + sha1[:38], false},
	} {
		if got := IsObjectID(tc.in); got != tc.want {
			t.Errorf("%s: IsObjectID(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestNewRefuses(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "exe")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		ok   bool
	}{
		{"executable", exe, true},
		{"link to an executable", link, true},
		{"empty", "", false},
		{"relative", "git", false},
		{"relative with a folder", "bin/git", false},
		{"dot path", "./exe", false},
		{"missing", filepath.Join(dir, "missing"), false},
		{"folder", dir, false},
		{"not executable", plain, false},
		{"NUL", exe + "\x00x", false},
		{"device", "/dev/null", false},
	} {
		g, err := New(tc.path)
		if (err == nil) != tc.ok {
			t.Errorf("%s: New(%q) error = %v, want ok = %v", tc.name, tc.path, err, tc.ok)
		}
		if tc.ok && g.Path != tc.path {
			t.Errorf("%s: Path = %q, want %q", tc.name, g.Path, tc.path)
		}
		if !tc.ok && g.Path != "" {
			t.Errorf("%s: a refused path left Path = %q", tc.name, g.Path)
		}
	}
}

// TestFindDoesNotReadPATH shows that a git placed first on $PATH is not
// used, and that an empty $PATH does not stop Find.
func TestFindDoesNotReadPATH(t *testing.T) {
	fake := t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "git"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake)
	g, err := Find()
	if err != nil {
		t.Fatalf("Find: %v (these tests need git at one of %v)", err, fixedPaths)
	}
	if !filepath.IsAbs(g.Path) || strings.HasPrefix(g.Path, fake) {
		t.Fatalf("Find returned %q, which came from $PATH or is not absolute", g.Path)
	}
	found := false
	for _, p := range fixedPaths {
		found = found || g.Path == p
	}
	if !found {
		t.Fatalf("Find returned %q, not one of %v", g.Path, fixedPaths)
	}
	t.Setenv("PATH", "")
	if _, err := Find(); err != nil {
		t.Fatalf("Find with an empty PATH: %v", err)
	}
}

func TestFixedPathsAreAbsolute(t *testing.T) {
	want := []string{"/usr/bin/git", "/bin/git", "/usr/local/bin/git"}
	if !reflect.DeepEqual(fixedPaths, want) {
		t.Fatalf("fixedPaths = %v, want %v", fixedPaths, want)
	}
}

// TestEnvIsExactlyTheDocumentedSet is the golden list of the environment.
func TestEnvIsExactlyTheDocumentedSet(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	base := []string{
		"GIT_ASKPASS=",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_EDITOR=:",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=/dev/null",
		"LC_ALL=C",
		"PATH=/usr/bin:/bin",
		"SSH_ASKPASS=",
	}
	build := func(allow, ceiling string) []string {
		out := append([]string{"GIT_ALLOW_PROTOCOL=" + allow, "GIT_CEILING_DIRECTORIES=" + ceiling}, base...)
		// The builder returns the list sorted.
		slices.Sort(out)
		return out
	}
	for _, tc := range []struct {
		name string
		cmd  Cmd
		want []string
	}{
		{"no protocol", Cmd{Args: []string{"version"}}, build("", defaultCeilings)},
		{"file", Cmd{Args: []string{"fetch"}, Protocols: []string{"file"}}, build("file", defaultCeilings)},
		{"file twice", Cmd{Args: []string{"fetch"}, Protocols: []string{"file", "file"}}, build("file", defaultCeilings)},
		{"working folder", Cmd{Args: []string{"init"}, Dir: "/srv/a/b"}, build("", "/srv/a")},
		{"working folder at the root", Cmd{Args: []string{"init"}, Dir: "/srv"}, build("", defaultCeilings)},
		{"unknown protocol allows none", Cmd{Args: []string{"fetch"}, Protocols: []string{"ext"}}, build("", defaultCeilings)},
	} {
		got := g.Env(tc.cmd)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: environment\n got  %q\n want %q", tc.name, got, tc.want)
		}
	}
}

// goldenSettings is the literal -c list for a command with no transport. A
// change to settings() has to change this list on purpose.
var goldenSettings = []string{
	"core.hooksPath=/dev/null",
	"core.fsmonitor=false",
	"core.untrackedCache=false",
	"core.pager=cat",
	"core.attributesFile=/dev/null",
	"core.excludesFile=/dev/null",
	"core.sshCommand=false",
	"core.askPass=",
	"core.editor=:",
	"core.alternateRefsCommand=",
	"core.protectNTFS=true",
	"core.protectHFS=true",
	"credential.helper=",
	"protocol.allow=never",
	"protocol.version=2",
	"transfer.fsckObjects=true",
	"fetch.fsckObjects=true",
	"receive.fsckObjects=true",
	"fetch.recurseSubmodules=false",
	"submodule.recurse=false",
	"fetch.writeCommitGraph=false",
	"gc.auto=0",
	"maintenance.auto=false",
	"safe.bareRepository=explicit",
	"advice.detachedHead=false",
	"log.showSignature=false",
	"log.mailmap=false",
	"mailmap.blob=",
	"mailmap.file=",
	"gpg.program=false",
	"gpg.x509.program=false",
	"gpg.ssh.program=false",
}

// TestSettingsAreExactlyTheDocumentedSet is the golden list of the -c
// settings, and checks that Argv passes exactly that list, in that order.
func TestSettingsAreExactlyTheDocumentedSet(t *testing.T) {
	if got := settings(nil); !reflect.DeepEqual(got, goldenSettings) {
		t.Errorf("settings(nil)\n got  %q\n want %q", got, goldenSettings)
	}
	// One allowed protocol goes after "protocol.allow=never" and before
	// "protocol.version=2".
	at := slices.Index(goldenSettings, "protocol.allow=never") + 1
	withFile := slices.Concat(goldenSettings[:at], []string{"protocol.file.allow=always"}, goldenSettings[at:])
	if got := settings([]string{"file"}); !reflect.DeepEqual(got, withFile) {
		t.Errorf("settings(file)\n got  %q\n want %q", got, withFile)
	}
	g := Git{Path: "/usr/bin/git"}
	for _, tc := range []struct {
		name  string
		cmd   Cmd
		wantC []string
	}{
		{"no protocol", Cmd{GitDir: "/srv/r.git", Args: []string{"for-each-ref"}}, goldenSettings},
		{"file", Cmd{GitDir: "/srv/r.git", Args: []string{"for-each-ref"}, Protocols: []string{"file"}}, withFile},
	} {
		argv, err := g.Argv(tc.cmd)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		i := 1
		for ; argv[i] == "-c"; i += 2 {
			got = append(got, argv[i+1])
		}
		if !reflect.DeepEqual(got, tc.wantC) {
			t.Errorf("%s: -c list in Argv\n got  %q\n want %q", tc.name, got, tc.wantC)
		}
		if want := []string{"--git-dir=/srv/r.git", "for-each-ref"}; !reflect.DeepEqual(argv[i:], want) {
			t.Errorf("%s: after the settings %q, want %q", tc.name, argv[i:], want)
		}
	}
}

// TestEnvInheritsNothing sets variables in the test process that git would
// act on, and shows none of them reaches the environment.
func TestEnvInheritsNothing(t *testing.T) {
	banned := []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS",
		"GIT_EXEC_PATH", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT",
		"GIT_PROXY_COMMAND", "GIT_EXTERNAL_DIFF", "GIT_CONFIG", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_TRACE",
		"XDG_CONFIG_HOME", "TMPDIR", "LANG", "TERM",
		"ATANIQ_GITENV_MARKER",
	}
	for _, name := range banned {
		t.Setenv(name, "from-the-test-process")
	}
	t.Setenv("HOME", "/from/the/test/process")
	t.Setenv("PATH", "/from/the/test/process")
	t.Setenv("GIT_ALLOW_PROTOCOL", "ext")
	g := Git{Path: "/usr/bin/git"}
	for _, c := range []Cmd{{Args: []string{"version"}}, {Args: []string{"fetch"}, Protocols: []string{"file"}, Dir: "/srv/x/y"}} {
		for _, kv := range g.Env(c) {
			name, value, ok := strings.Cut(kv, "=")
			if !ok {
				t.Errorf("entry %q has no =", kv)
			}
			if strings.Contains(value, "from-the-test-process") || strings.Contains(value, "/from/the/test/process") {
				t.Errorf("entry %q comes from the test process", kv)
			}
			for _, b := range banned {
				if name == b {
					t.Errorf("entry %q must not be set", kv)
				}
			}
		}
		for _, kv := range g.Env(c) {
			if kv == "GIT_ALLOW_PROTOCOL=ext" {
				t.Errorf("GIT_ALLOW_PROTOCOL was inherited")
			}
		}
	}
}

func TestArgvLayout(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	got, err := g.Argv(Cmd{GitDir: "/srv/r.git", Args: []string{"fetch", "--end-of-options", "/srv/src", "refs/heads/*:refs/heads/*"}, Protocols: []string{"file"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "/usr/bin/git" {
		t.Fatalf("argv[0] = %q", got[0])
	}
	var keys []string
	i := 1
	for ; i+1 < len(got) && got[i] == "-c"; i += 2 {
		keys = append(keys, got[i+1])
	}
	if want := settings([]string{"file"}); !reflect.DeepEqual(keys, want) {
		t.Fatalf("-c settings\n got  %q\n want %q", keys, want)
	}
	rest := got[i:]
	want := []string{"--git-dir=/srv/r.git", "fetch", "--end-of-options", "/srv/src", "refs/heads/*:refs/heads/*"}
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("after the settings\n got  %q\n want %q", rest, want)
	}
	// The protocol policy: never first, then one always per protocol.
	if !contains(keys, "protocol.allow=never") || !contains(keys, "protocol.file.allow=always") {
		t.Fatalf("protocol settings missing from %q", keys)
	}
	if indexOf(keys, "protocol.allow=never") > indexOf(keys, "protocol.file.allow=always") {
		t.Fatalf("protocol.allow=never must come before the allowed protocols: %q", keys)
	}
	// Without a protocol there is no always.
	none, err := g.Argv(Cmd{GitDir: "/srv/r.git", Args: []string{"for-each-ref"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range none {
		if strings.HasSuffix(a, ".allow=always") {
			t.Fatalf("no protocol was asked for but %q is set", a)
		}
	}
	// The slice is the caller's: a second call is not affected by a change.
	got[0] = "changed"
	again, _ := g.Argv(Cmd{GitDir: "/srv/r.git", Args: []string{"for-each-ref"}})
	if again[0] != "/usr/bin/git" {
		t.Fatal("Argv shares memory between calls")
	}
	// Arguments are copied, not aliased.
	args := []string{"for-each-ref"}
	v, _ := g.Argv(Cmd{GitDir: "/srv/r.git", Args: args})
	v[len(v)-1] = "changed"
	if args[0] != "for-each-ref" {
		t.Fatal("Argv wrote into the caller's Args")
	}
}

// TestDiffNeverRunsAnExternalProgram checks the two options that replace the
// diff.external setting.
func TestDiffNeverRunsAnExternalProgram(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	got, err := g.Argv(Cmd{GitDir: "/srv/r.git", Args: []string{"diff", "a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	tail := got[len(got)-6:]
	want := []string{"diff", "--no-ext-diff", "--no-textconv", "a", "b"}
	if !reflect.DeepEqual(tail[1:], want) {
		t.Fatalf("tail %q, want %q", tail[1:], want)
	}
	for _, kv := range settings(nil) {
		if strings.HasPrefix(kv, "diff.external") {
			t.Fatalf("diff.external=%q breaks git diff; use the options", kv)
		}
	}
}

func TestArgvRefuses(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	ok := "/srv/r.git"
	for _, tc := range []struct {
		name string
		g    Git
		cmd  Cmd
	}{
		{"no Args", g, Cmd{GitDir: ok}},
		{"empty Args", g, Cmd{GitDir: ok, Args: []string{}}},
		{"empty subcommand", g, Cmd{GitDir: ok, Args: []string{""}}},
		{"NUL in the subcommand", g, Cmd{GitDir: ok, Args: []string{"fetch\x00"}}},
		{"NUL in an argument", g, Cmd{GitDir: ok, Args: []string{"fetch", "a\x00b"}}},
		{"NUL in GitDir", g, Cmd{GitDir: "/srv/\x00r.git", Args: []string{"fetch"}}},
		{"NUL in Dir", g, Cmd{GitDir: ok, Dir: "/srv/\x00", Args: []string{"fetch"}}},
		{"relative GitDir", g, Cmd{GitDir: "r.git", Args: []string{"for-each-ref"}}},
		{"dot GitDir", g, Cmd{GitDir: ".", Args: []string{"for-each-ref"}}},
		{"relative GitDir with init", g, Cmd{GitDir: "r.git", Args: []string{"init"}}},
		{"relative Dir", g, Cmd{GitDir: ok, Dir: "work", Args: []string{"for-each-ref"}}},
		{"empty GitDir for fetch", g, Cmd{Args: []string{"fetch"}}},
		{"empty GitDir for config", g, Cmd{Args: []string{"config", "--list"}}},
		{"empty GitDir for update-ref", g, Cmd{Args: []string{"update-ref", "refs/heads/a", "b"}}},
		{"empty GitDir for merge-tree", g, Cmd{Args: []string{"merge-tree", "--write-tree", "a", "b"}}},
		{"merge-tree with a separate merge base", g, Cmd{GitDir: ok, Args: []string{"merge-tree", "--write-tree", "--merge-base", "a", "b", "c"}}},
		{"merge-tree with a strategy option", g, Cmd{GitDir: ok, Args: []string{"merge-tree", "--write-tree", "-X", "ours", "a", "b"}}},
		{"merge-tree with a joined strategy option", g, Cmd{GitDir: ok, Args: []string{"merge-tree", "--write-tree", "-Xours", "a", "b"}}},
		{"unknown protocol ext", g, Cmd{GitDir: ok, Args: []string{"fetch"}, Protocols: []string{"ext"}}},
		{"unknown protocol http", g, Cmd{GitDir: ok, Args: []string{"fetch"}, Protocols: []string{"http"}}},
		{"unknown protocol ssh", g, Cmd{GitDir: ok, Args: []string{"fetch"}, Protocols: []string{"ssh"}}},
		{"empty protocol name", g, Cmd{GitDir: ok, Args: []string{"fetch"}, Protocols: []string{""}}},
		{"protocol name in another case", g, Cmd{GitDir: ok, Args: []string{"fetch"}, Protocols: []string{"File"}}},
		{"one good and one bad protocol", g, Cmd{GitDir: ok, Args: []string{"fetch"}, Protocols: []string{"file", "ext"}}},
		{"option -c first", g, Cmd{GitDir: ok, Args: []string{"-c", "core.pager=x", "fetch"}}},
		{"option --exec-path first", g, Cmd{GitDir: ok, Args: []string{"--exec-path=/tmp", "fetch"}}},
		{"option -C first", g, Cmd{GitDir: ok, Args: []string{"-C", "/tmp", "fetch"}}},
		{"option --git-dir first", g, Cmd{GitDir: ok, Args: []string{"--git-dir=/tmp", "fetch"}}},
		{"dash alone first", g, Cmd{GitDir: ok, Args: []string{"-"}}},
		{"subcommand outside the list: status", g, Cmd{GitDir: ok, Args: []string{"status"}}},
		{"subcommand outside the list: clone", g, Cmd{GitDir: ok, Args: []string{"clone", "a", "b"}}},
		{"subcommand outside the list: checkout", g, Cmd{GitDir: ok, Args: []string{"checkout", "x"}}},
		{"subcommand outside the list: gc", g, Cmd{GitDir: ok, Args: []string{"gc"}}},
		{"subcommand outside the list: submodule", g, Cmd{GitDir: ok, Args: []string{"submodule", "update"}}},
		{"subcommand outside the list: an alias", g, Cmd{GitDir: ok, Args: []string{"zz"}}},
		{"subcommand in another case", g, Cmd{GitDir: ok, Args: []string{"Fetch"}}},
		{"subcommand with a space", g, Cmd{GitDir: ok, Args: []string{"fetch --all"}}},
		{"subcommand with a path", g, Cmd{GitDir: ok, Args: []string{"/usr/bin/git"}}},
		{"git-prefixed subcommand", g, Cmd{GitDir: ok, Args: []string{"git-fetch"}}},
		{"--upload-pack", g, Cmd{GitDir: ok, Args: []string{"fetch", "--upload-pack=x", "/a"}}},
		{"--upload-pack abbreviated", g, Cmd{GitDir: ok, Args: []string{"fetch", "--upl=x", "/a"}}},
		{"--receive-pack", g, Cmd{GitDir: ok, Args: []string{"fetch", "--receive-pack=x"}}},
		{"--exec", g, Cmd{GitDir: ok, Args: []string{"fetch", "--exec=x"}}},
		{"--ext-diff", g, Cmd{GitDir: ok, Args: []string{"diff", "--ext-diff", "a", "b"}}},
		{"--ext-diff abbreviated", g, Cmd{GitDir: ok, Args: []string{"diff", "--ext", "a", "b"}}},
		{"no git path", Git{}, Cmd{GitDir: ok, Args: []string{"fetch"}}},
		{"relative git path", Git{Path: "git"}, Cmd{GitDir: ok, Args: []string{"fetch"}}},
	} {
		argv, err := tc.g.Argv(tc.cmd)
		if err == nil {
			t.Errorf("%s: Argv accepted %q", tc.name, argv)
		}
		if argv != nil {
			t.Errorf("%s: Argv returned %q with its error", tc.name, argv)
		}
	}
}

func TestArgvAccepts(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	ok := "/srv/r.git"
	for _, sub := range subcommands {
		c := Cmd{GitDir: ok, Args: []string{sub}}
		if sub == "init" || sub == "version" {
			c.GitDir = ""
		}
		if _, err := g.Argv(c); err != nil {
			t.Errorf("%s: %v", sub, err)
		}
	}
	// Both of them also take a GitDir.
	for _, sub := range []string{"init", "version"} {
		if _, err := g.Argv(Cmd{GitDir: ok, Args: []string{sub}}); err != nil {
			t.Errorf("%s with a GitDir: %v", sub, err)
		}
	}
	// What an untrusted ref name needs after the end of options is allowed,
	// even when it looks like a refused option.
	for _, args := range [][]string{
		{"rev-parse", "--end-of-options", "--upload-pack=x"},
		{"log", "--", "--ext-diff"},
		{"fetch", "--no-ext-diff"},
		{"fetch", "--recurse-submodules=no"},
		{"fetch", "--update-head-ok"},
		{"log", "--exit-code"},
		{"diff", "--exit-code", "a", "b"},
		{"diff-tree", "-r", "--name-only", "a", "b"},
		{"for-each-ref", "--format=%(refname)", "refs/heads/"},
		{"merge-tree", "--write-tree", "--end-of-options", "a", "b"},
	} {
		if _, err := g.Argv(Cmd{GitDir: ok, Args: args}); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
	// The allowed list is exactly the one the task names.
	want := []string{"init", "version", "fetch", "for-each-ref", "rev-parse", "rev-list", "merge-base", "diff-tree", "diff", "log", "cat-file", "update-ref", "symbolic-ref", "show-ref", "count-objects", "config", "check-ref-format", "ls-tree", "merge-tree"}
	if !reflect.DeepEqual(subcommands, want) {
		t.Errorf("subcommands = %q, want %q", subcommands, want)
	}
	if !reflect.DeepEqual(noGitDir, []string{"init", "version"}) {
		t.Errorf("noGitDir = %q", noGitDir)
	}
}

func contains(s []string, v string) bool { return indexOf(s, v) >= 0 }

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
