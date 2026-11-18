package gitenv

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestNoDiffProgramsIsOnTheseSubcommands asserts which subcommands get the
// options "--no-ext-diff" and "--no-textconv", and where they stand: right
// after the subcommand, before the arguments of the caller. No other
// subcommand gets them.
func TestNoDiffProgramsIsOnTheseSubcommands(t *testing.T) {
	want := []string{"diff", "log"}
	if !reflect.DeepEqual(noDiffPrograms, want) {
		t.Fatalf("noDiffPrograms = %q, want %q", noDiffPrograms, want)
	}
	g := Git{Path: "/usr/bin/git"}
	for _, sub := range subcommands {
		argv, err := g.Argv(Cmd{GitDir: "/srv/r.git", Args: []string{sub, "--end-of-options", "a"}})
		if err != nil {
			continue // a subcommand that takes no such arguments is not asked
		}
		at := slices.Index(argv, sub)
		got := argv[at+1:]
		if slices.Contains(want, sub) {
			if tail := []string{"--no-ext-diff", "--no-textconv", "--end-of-options", "a"}; !reflect.DeepEqual(got, tail) {
				t.Errorf("%s: after the subcommand stands %q, want %q", sub, got, tail)
			}
		} else if slices.Contains(got, "--no-ext-diff") || slices.Contains(got, "--no-textconv") {
			t.Errorf("%s: the command line holds the two options: %q", sub, argv)
		}
	}
	for _, sub := range want {
		if _, err := g.Argv(Cmd{GitDir: "/srv/r.git", Args: []string{sub, "--end-of-options", "a"}}); err != nil {
			t.Errorf("%s: %v", sub, err)
		}
	}
}

// withoutNoDiffPrograms removes the two options from a command line that
// Argv built, for a control: the line is then what Argv would build without
// them, and nothing else changes.
func withoutNoDiffPrograms(t *testing.T, argv []string) []string {
	t.Helper()
	at := slices.Index(argv, "--no-ext-diff")
	if at < 0 || argv[at+1] != "--no-textconv" {
		t.Fatalf("the command line does not hold the two options: %q", argv)
	}
	return slices.Concat(argv[:at], argv[at+2:])
}

// TestTextconvProgramsDoNotRun gives a repository whose own config names a
// textconv program for the diff driver "x", and whose "info/attributes"
// gives every file that driver. The attribute pin does not reach that file,
// so git does know the driver. "log -p" and "diff", built through Argv and
// Env, print the patch as text and do not start the program.
//
// The controls show the test can fail: the same two command lines with only
// "--no-ext-diff" and "--no-textconv" taken out start the program. So
// without the two options a caller of "log -p" would run a program named in
// the repository's config.
//
// "diff-tree -p" gets no such options and does not start the program either,
// because it is plumbing; the test records that, so a git that changes it is
// seen.
//
// Proves: with this git, the two options keep "log -p" and "diff" from
// running a textconv program that the config and "info/attributes" name, and
// "diff-tree -p" runs none. Cannot prove: anything about other git versions,
// or about other programs a config can name.
func TestTextconvProgramsDoNotRun(t *testing.T) {
	g := needGit(t)
	s := newMergeScene(t, g, "")
	script, mark := canary(t)
	appendConfig(t, s.repo, "[diff \"x\"]\n\ttextconv = "+script+"\n")
	writeInfoAttributes(t, s.repo, "* diff=x\n")

	for _, tc := range []struct {
		name    string
		args    []string
		options bool // Argv adds the two options
	}{
		{"log -p", []string{"log", "-p", "--max-count=1", "--format=%s", "--no-color", "--end-of-options", s.theirs}, true},
		{"diff", []string{"diff", "--no-color", "--end-of-options", s.base, s.theirs}, true},
		{"diff-tree -p", []string{"diff-tree", "-r", "-p", "--no-color", "--end-of-options", s.base, s.theirs}, false},
	} {
		c := Cmd{GitDir: s.repo, Args: tc.args}
		argv, err := g.Argv(c)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.options {
			// The control: without the two options the program runs.
			if out, err := run(withoutNoDiffPrograms(t, argv), g.Env(c), "", ""); err != nil {
				t.Fatalf("control: %s without the two options: %v\n%s", tc.name, err, out)
			}
			mustFire(t, mark, "git "+tc.name+" without --no-ext-diff and --no-textconv")
		} else if slices.Contains(argv, "--no-textconv") {
			t.Fatalf("%s: Argv added the options, so this case shows nothing: %q", tc.name, argv)
		}
		out, err := run(argv, g.Env(c), "", "")
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, out)
		}
		mustNotFire(t, mark, "git "+tc.name)
		if !strings.Contains(out, "\n-8\n+eight-theirs\n") {
			t.Errorf("%s does not show the changed line as text:\n%s", tc.name, out)
		}
	}
}
