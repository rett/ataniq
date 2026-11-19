package gitenv

import (
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Lines of the file "f.txt" in the merge fixtures. The base holds the lines
// 1 to 9. Two sides change different lines, so they merge cleanly; two
// other sides change the same line, so they conflict.
const (
	mergeBaseText    = "1\n2\n3\n4\n5\n6\n7\n8\n9\n"
	mergeOursText    = "1\ntwo-ours\n3\n4\n5\n6\n7\n8\n9\n"
	mergeTheirsText  = "1\n2\n3\n4\n5\n6\n7\neight-theirs\n9\n"
	mergeBothText    = "1\ntwo-ours\n3\n4\n5\n6\n7\neight-theirs\n9\n"
	mergeClashOurs   = "1\n2\n3\n4\nfive-ours\n6\n7\n8\n9\n"
	mergeClashTheirs = "1\n2\n3\n4\nfive-theirs\n6\n7\n8\n9\n"
)

// mergeScene is a bare repository with five commits: a base, two sides that
// merge cleanly (ours, theirs) and two that conflict (clashOurs,
// clashTheirs). HEAD is the branch main, at ours.
type mergeScene struct {
	repo                                       string
	base, ours, theirs, clashOurs, clashTheirs string
	baseTree, oursTree, theirsTree             string
	env                                        []string
	g                                          Git
	attributes                                 string
}

// newMergeScene builds the repository with plain git and the fixture
// environment. When attributes is not empty, every tree, HEAD's included,
// holds it as the file ".gitattributes".
func newMergeScene(t *testing.T, g Git, attributes string) *mergeScene {
	t.Helper()
	return newMergeSceneIn(t, g, bare(t, g), attributes)
}

// newMergeSceneIn builds the scene in an empty bare repository the caller
// made, for example one with another object format.
func newMergeSceneIn(t *testing.T, g Git, repo, attributes string) *mergeScene {
	t.Helper()
	s := &mergeScene{repo: repo, env: fixtureEnv(t), g: g, attributes: attributes}
	s.baseTree = s.tree(t, mergeBaseText)
	s.oursTree = s.tree(t, mergeOursText)
	s.theirsTree = s.tree(t, mergeTheirsText)
	s.base = s.plain(t, "", "commit-tree", "-m", "base", s.baseTree)
	side := func(text, tree string) string {
		return s.plain(t, "", "commit-tree", "-m", text, "-p", s.base, tree)
	}
	s.ours = side("ours", s.oursTree)
	s.theirs = side("theirs", s.theirsTree)
	s.clashOurs = side("clash ours", s.tree(t, mergeClashOurs))
	s.clashTheirs = side("clash theirs", s.tree(t, mergeClashTheirs))
	s.plain(t, "", "update-ref", "refs/heads/main", s.ours)
	if got := s.plain(t, "", "rev-parse", "HEAD"); got != s.ours {
		t.Fatalf("HEAD of the fixture is %q, want the commit ours %q", got, s.ours)
	}
	return s
}

// plain runs plain git on the repository with the fixture environment.
func (s *mergeScene) plain(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	return fixtureIn(t, s.g, s.env, "", stdin, append([]string{"--git-dir=" + s.repo}, args...)...)
}

// blob writes text as a blob and returns its id.
func (s *mergeScene) blob(t *testing.T, text string) string {
	t.Helper()
	return s.plain(t, text, "hash-object", "-w", "--stdin")
}

// tree writes a tree that holds text as "f.txt", and the attribute file
// when the scene has one.
func (s *mergeScene) tree(t *testing.T, text string) string {
	t.Helper()
	list := ""
	if s.attributes != "" {
		list = "100644 blob " + s.blob(t, s.attributes) + "\t.gitattributes\n"
	}
	list += "100644 blob " + s.blob(t, text) + "\tf.txt\n"
	return s.plain(t, list, "mktree")
}

// exitStatus returns the exit status a finished command reported, 0 for no
// error, and fails the test when the command did not run to its end.
func exitStatus(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("the command did not run: %v", err)
	}
	return exit.ExitCode()
}

// fileInTree returns the id of the blob "f.txt" in a tree, asked through
// this package.
func (s *mergeScene) fileInTree(t *testing.T, tree string) string {
	t.Helper()
	out := mustOwned(t, s.g, Cmd{GitDir: s.repo, Args: []string{"ls-tree", "--format=%(objectname)", "--end-of-options", tree, "f.txt"}})
	if !IsObjectID(out) {
		t.Fatalf("ls-tree of %s printed %q, not one object id", tree, out)
	}
	return out
}

// TestMergeTreeBuildsAMerge runs the two commands a host-built merge needs,
// through this package, with the installed git. Two sides that change
// different lines give a tree whose file holds both changes. Two sides that
// change the same line give exit status 1 and name the file, and git writes
// no ref.
//
// Proves: Argv accepts these command lines and this git runs them in a bare
// repository with the settings and the environment of this package. Cannot
// prove: anything about other git versions.
func TestMergeTreeBuildsAMerge(t *testing.T) {
	g := needGit(t)
	s := newMergeScene(t, g, "")
	refsBefore := s.plain(t, "", "for-each-ref")

	out := mustOwned(t, g, Cmd{GitDir: s.repo, Args: []string{"merge-tree", "--write-tree", "--end-of-options", s.ours, s.theirs}})
	if !IsObjectID(out) {
		t.Fatalf("a clean merge printed %q, want one tree id", out)
	}
	if got, want := s.fileInTree(t, out), s.blob(t, mergeBothText); got != want {
		t.Errorf("the merged file is the blob %s, want %s (both changes)", got, want)
	}

	out, err := owned(g, Cmd{GitDir: s.repo, Args: []string{"merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", "--end-of-options", s.clashOurs, s.clashTheirs}})
	if status := exitStatus(t, err); status != 1 {
		t.Fatalf("a conflicting merge ended with status %d, want 1:\n%q", status, out)
	}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(fields) != 2 || !IsObjectID(fields[0]) || fields[1] != "f.txt" {
		t.Errorf("a conflicting merge printed %q, want a tree id and the path f.txt", fields)
	}

	if after := s.plain(t, "", "for-each-ref"); after != refsBefore {
		t.Errorf("merge-tree changed the refs:\nbefore %q\nafter  %q", refsBefore, after)
	}
}

// mergeTreeFlagOrders returns every way to write the optional flags of the
// one form: each subset of [mergeTreeFlags], in each order.
func mergeTreeFlagOrders() [][]string {
	orders := [][]string{{}}
	var extend func(have []string)
	extend = func(have []string) {
		for _, f := range mergeTreeFlags {
			if slices.Contains(have, f) {
				continue
			}
			next := append(slices.Clone(have), f)
			orders = append(orders, next)
			extend(next)
		}
	}
	extend(nil)
	return orders
}

// TestMergeTreeHasOneForm covers the rule for "merge-tree": "--write-tree",
// then any of "--name-only", "--no-messages" and "-z" in any order, then
// "--end-of-options", then exactly two full object ids. Every other form is
// refused with a message that names the one form. The refused table holds
// the options that change what a merge does, in every spelling.
func TestMergeTreeHasOneForm(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	ok := "/srv/r.git"
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	long1, long2 := strings.Repeat("1", 64), strings.Repeat("2", 64)

	if want := []string{"--name-only", "--no-messages", "-z"}; !reflect.DeepEqual(mergeTreeFlags, want) {
		t.Fatalf("mergeTreeFlags = %q, want %q", mergeTreeFlags, want)
	}
	orders := mergeTreeFlagOrders()
	if len(orders) != 16 {
		t.Fatalf("%d ways to write three optional flags, want 16", len(orders))
	}
	for _, ids := range [][]string{{a, b}, {b, a}, {a, a}, {long1, long2}} {
		for _, flags := range orders {
			args := slices.Concat([]string{"merge-tree", "--write-tree"}, flags, []string{"--end-of-options"}, ids)
			argv, err := g.Argv(Cmd{GitDir: ok, Args: args})
			if err != nil {
				t.Errorf("%q: %v", args, err)
				continue
			}
			// The arguments stand as written, right after --git-dir.
			if want := append([]string{"--git-dir=" + ok}, args...); !reflect.DeepEqual(argv[len(argv)-len(want):], want) {
				t.Errorf("the command line ends %q, want %q", argv[len(argv)-len(want):], want)
			}
		}
	}

	form := func(flags ...string) []string {
		return slices.Concat([]string{"--write-tree"}, flags, []string{"--end-of-options", a, b})
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		// A chosen merge base makes a conflicting merge end cleanly.
		{"--merge-base=", form("--merge-base=" + c)},
		{"--merge-base= with the marker --", []string{"--write-tree", "--merge-base=" + c, "--", a, b}},
		{"--merge-base with a separate value", form("--merge-base", c)},
		{"--merge-base takes the marker", []string{"--write-tree", "--merge-base", "--end-of-options", "--zz", a, b}},
		{"--merge-base abbreviated", form("--merge-b=" + c)},
		{"--merge-base as short as git takes it", form("--me=" + c)},
		{"--merge-base after the marker", []string{"--write-tree", "--end-of-options", "--merge-base=" + c, a, b}},
		{"--merge-base after the ids", []string{"--write-tree", "--end-of-options", a, b, "--merge-base=" + c}},
		{"--merge-base before --write-tree", append([]string{"--merge-base=" + c}, form()...)},
		// A strategy option does the same.
		{"--strategy-option=", form("--strategy-option=ours")},
		{"--strategy-option with a separate value", form("--strategy-option", "ours")},
		{"--strategy-option takes the marker", []string{"--strategy-option", "--", "--zz", a, b}},
		{"--strategy-option abbreviated", form("--strat=theirs")},
		{"--strategy-option as short as git takes it", form("--str=ours")},
		{"-X with a separate value", form("-X", "ours")},
		{"-X takes the marker", []string{"--write-tree", "-X", "--", "--zz", a, b}},
		{"-X joined", form("-Xours")},
		{"-X with =", form("-X=ours")},
		{"-X before --write-tree", append([]string{"-Xours"}, form()...)},
		// Merging two commits that share no history.
		{"--allow-unrelated-histories", form("--allow-unrelated-histories")},
		{"--all, which git reads as its abbreviation", form("--all")},
		{"--allow abbreviated", form("--allow")},
		// Other modes and inputs.
		{"--stdin", form("--stdin")},
		{"--stdin alone", []string{"--write-tree", "--stdin"}},
		{"--stdin with the marker", []string{"--write-tree", "--stdin", "--end-of-options"}},
		{"--trivial-merge", []string{"--trivial-merge", "--end-of-options", a, b}},
		{"--trivial-merge with three ids", []string{"--trivial-merge", "--end-of-options", c, a, b}},
		{"--trivial-merge after --write-tree", form("--trivial-merge")},
		{"no mode", []string{"--end-of-options", a, b}},
		{"three plain ids", []string{c, a, b}},
		// Other flags, among them ones the valueless list holds.
		{"--quiet", form("--quiet")},
		{"-q", form("-q")},
		{"--messages", form("--messages")},
		{"--no-name-only", form("--no-name-only")},
		{"--name-status", form("--name-status")},
		{"--name-only abbreviated", form("--name")},
		{"--no-messages abbreviated", form("--no-mess")},
		{"--write-tree abbreviated", []string{"--write", "--end-of-options", a, b}},
		{"--write-tree with a value", []string{"--write-tree=1", "--end-of-options", a, b}},
		{"-z with a value", form("-z=1")},
		{"an option with =", form("--zz=1")},
		{"an empty argument among the flags", form("")},
		{"a dash among the flags", form("-")},
		// A flag at most once.
		{"--write-tree twice", form("--write-tree")},
		{"-z twice", form("-z", "-z")},
		{"--name-only twice", form("--name-only", "-z", "--name-only")},
		// The marker.
		{"no marker", []string{"--write-tree", a, b}},
		{"no marker, with flags", []string{"--write-tree", "--name-only", a, b}},
		{"the marker --", []string{"--write-tree", "--", a, b}},
		{"the marker before --write-tree", []string{"--end-of-options", "--write-tree", a, b}},
		{"the marker twice", []string{"--write-tree", "--end-of-options", "--end-of-options", a, b}},
		{"the marker between the ids", []string{"--write-tree", "--end-of-options", a, "--end-of-options", b}},
		{"a flag after the marker", []string{"--write-tree", "--end-of-options", "-z", a, b}},
		// Exactly two full object ids.
		{"no argument", nil},
		{"only --write-tree", []string{"--write-tree"}},
		{"no id", []string{"--write-tree", "--end-of-options"}},
		{"one id", []string{"--write-tree", "--end-of-options", a}},
		{"a third id", []string{"--write-tree", "--end-of-options", a, b, c}},
		{"a branch name first", []string{"--write-tree", "--end-of-options", "main", b}},
		{"a branch name second", []string{"--write-tree", "--end-of-options", a, "main"}},
		{"a full ref name", []string{"--write-tree", "--end-of-options", "refs/heads/main", b}},
		{"HEAD", []string{"--write-tree", "--end-of-options", "HEAD", b}},
		{"an abbreviated id", []string{"--write-tree", "--end-of-options", a[:39], b}},
		{"an id with a suffix", []string{"--write-tree", "--end-of-options", a + "^", b}},
		{"an id with a path", []string{"--write-tree", "--end-of-options", a, b + ":f.txt"}},
		{"an id in upper case", []string{"--write-tree", "--end-of-options", strings.ToUpper(a), b}},
		{"an id before the marker", []string{"--write-tree", a, "--end-of-options", b}},
		{"both ids before the marker", []string{"--write-tree", a, b, "--end-of-options"}},
		{"an empty id", []string{"--write-tree", "--end-of-options", a, ""}},
		{"two ids in one argument", []string{"--write-tree", "--end-of-options", a + " " + b}},
	} {
		argv, err := g.Argv(Cmd{GitDir: ok, Args: append([]string{"merge-tree"}, tc.args...)})
		if err == nil {
			t.Errorf("%s: Argv accepted %q", tc.name, argv)
			continue
		}
		if argv != nil {
			t.Errorf("%s: Argv returned %q with its error", tc.name, argv)
		}
		if !strings.Contains(err.Error(), `"`+mergeTreeForm+`"`) || !strings.Contains(err.Error(), "one form only") {
			t.Errorf("%s: error %q does not name the one allowed form", tc.name, err)
		}
	}
	if want := "merge-tree --write-tree [--name-only] [--no-messages] [-z] --end-of-options <id> <id>"; mergeTreeForm != want {
		t.Errorf("mergeTreeForm = %q, want %q", mergeTreeForm, want)
	}

	// The rules for every command hold for this one too.
	valid := append([]string{"merge-tree"}, form()...)
	for _, tc := range []struct {
		name string
		cmd  Cmd
	}{
		{"no GitDir", Cmd{Args: valid}},
		{"a relative GitDir", Cmd{GitDir: "r.git", Args: valid}},
		{"an unknown protocol", Cmd{GitDir: ok, Protocols: []string{"ext"}, Args: valid}},
		{"UploadPack", Cmd{GitDir: ok, Protocols: []string{"file"}, UploadPack: []string{"/usr/bin/wrap"}, Args: valid}},
		{"an option before the subcommand", Cmd{GitDir: ok, Args: append([]string{"--no-pager"}, valid...)}},
		{"a NUL in an id", Cmd{GitDir: ok, Args: append(slices.Clone(valid[:len(valid)-1]), b[:39]+"\x00")}},
	} {
		if argv, err := g.Argv(tc.cmd); err == nil {
			t.Errorf("%s: Argv accepted %q", tc.name, argv)
		}
	}
}

// TestMergeTreeFlagsServeNoOtherSubcommand keeps the rule to its one
// subcommand: "merge-tree" is not on the list that the option rule serves,
// and the flags that only it needs are not flags another subcommand may use.
// The options that the one form refuses stay usable where they mean
// something else: "--all" and "--stdin" in "fetch" and "diff-tree".
func TestMergeTreeFlagsServeNoOtherSubcommand(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	ok := "/srv/r.git"
	if slices.Contains(subcommands, mergeTree) {
		t.Errorf("%q is in subcommands; it has its own rule", mergeTree)
	}
	for _, flag := range []string{"--write-tree", "--no-messages", "--trivial-merge", "-X"} {
		if slices.Contains(valuelessFlags, flag) {
			t.Errorf("valuelessFlags holds %q", flag)
		}
		for _, sub := range subcommands {
			c := Cmd{GitDir: ok, Args: []string{sub, flag}}
			if slices.Contains(noGitDir, sub) {
				c.GitDir = ""
			}
			if argv, err := g.Argv(c); err == nil {
				t.Errorf("Argv accepted %q", argv)
			}
		}
	}
	for _, args := range [][]string{
		{"fetch", "--atomic", "--all"},
		{"diff-tree", "-r", "--stdin"},
		{"update-ref", "--stdin", "-z"},
		{"diff-tree", "-r", "--name-only", "-z", "--end-of-options", "a", "b"},
	} {
		if _, err := g.Argv(Cmd{GitDir: ok, Args: args}); err != nil {
			t.Errorf("Argv refused %q: %v", args, err)
		}
	}
}

// TestRefusedMergeTreeOptionsChangeTheMerge is the reason for the one form.
// It runs plain git, with the settings and the environment of this package,
// on command lines that Argv refuses, and shows what each refused option
// does to a merge:
//
//   - With a strategy option, in the long form, abbreviated or as "-X", the
//     two sides that change the same line merge with exit status 0 and no
//     conflict.
//   - With "--merge-base" set to one side, the same merge ends with status 0
//     and gives the tree of the other side.
//   - Two commits that share no history are refused as they stand, and merged
//     with "--allow-unrelated-histories", which git also takes as "--all".
//   - With "--stdin" git reads the merges to do from standard input and
//     needs no commit on the command line.
//
// The same merges in the one form, through Argv, report the conflict
// (status 1) and refuse the unrelated commits.
//
// Proves: with this git, each of these options changes the result as said,
// and Argv refuses each. Cannot prove: that these are all the options of
// "merge-tree" that change a merge; the form is exact so that it need not
// know them.
func TestRefusedMergeTreeOptionsChangeTheMerge(t *testing.T) {
	g := needGit(t)
	s := newMergeScene(t, g, "")
	// A commit with no parent, so it shares no history with the scene.
	other := s.plain(t, "", "commit-tree", "-m", "other", s.tree(t, "other\n"))

	// plainMerge runs "merge-tree --write-tree <options> --end-of-options
	// <ids>" without the check of the arguments, after it has shown that
	// Argv refuses that command line.
	plainMerge := func(options []string, stdin string, ids ...string) (string, int) {
		t.Helper()
		rest := slices.Concat([]string{"--write-tree"}, options)
		if len(ids) > 0 {
			rest = slices.Concat(rest, []string{"--end-of-options"}, ids)
		}
		if argv, err := g.Argv(Cmd{GitDir: s.repo, Args: append([]string{"merge-tree"}, rest...)}); err == nil {
			t.Fatalf("Argv accepted %q", argv)
		}
		argv, err := g.Argv(Cmd{GitDir: s.repo, Args: []string{"merge-tree", "--write-tree", "--end-of-options", s.ours, s.theirs}})
		if err != nil {
			t.Fatal(err)
		}
		argv = append(argv[:slices.Index(argv, "merge-tree")+1:slices.Index(argv, "merge-tree")+1], rest...)
		out, err := run(argv, g.Env(Cmd{GitDir: s.repo}), "", stdin)
		return out, exitStatus(t, err)
	}
	// oneForm runs the one form through Argv.
	oneForm := func(first, second string) (string, int) {
		t.Helper()
		out, err := owned(g, Cmd{GitDir: s.repo, Args: []string{"merge-tree", "--write-tree", "--name-only", "--no-messages", "--end-of-options", first, second}})
		return out, exitStatus(t, err)
	}

	if out, status := oneForm(s.clashOurs, s.clashTheirs); status != 1 {
		t.Fatalf("the one form: the conflicting merge ended with status %d, want 1:\n%s", status, out)
	}
	for _, option := range []string{"--strategy-option=ours", "--strategy-option=theirs", "--strat=ours", "-Xours", "-Xtheirs"} {
		out, status := plainMerge([]string{option}, "", s.clashOurs, s.clashTheirs)
		if status != 0 || !IsObjectID(strings.TrimSpace(out)) {
			t.Errorf("control: with %s the conflicting merge ended with status %d, want 0 and one tree id: the option no longer hides a conflict in this git\n%s", option, status, out)
		}
	}
	for _, option := range []string{"--merge-base=" + s.clashOurs, "--merge-b=" + s.clashOurs} {
		out, status := plainMerge([]string{option}, "", s.clashOurs, s.clashTheirs)
		want := s.plain(t, "", "rev-parse", s.clashTheirs+"^{tree}")
		if status != 0 || strings.TrimSpace(out) != want {
			t.Errorf("control: with %s the conflicting merge ended with status %d and printed %q, want 0 and the tree %s of the other side", option, status, out, want)
		}
	}

	if out, status := oneForm(s.ours, other); status == 0 || status == 1 {
		t.Errorf("the one form merged two commits that share no history (status %d):\n%s", status, out)
	}
	for _, option := range []string{"--allow-unrelated-histories", "--all"} {
		if out, status := plainMerge([]string{option}, "", s.ours, other); status != 0 && status != 1 {
			t.Errorf("control: with %s git did not merge two commits that share no history (status %d):\n%s", option, status, out)
		}
	}

	out, status := plainMerge([]string{"--stdin"}, s.ours+" "+s.theirs+"\n")
	if status != 0 || !strings.Contains(out, s.plain(t, "", "merge-tree", "--write-tree", s.ours, s.theirs)) {
		t.Errorf("control: with --stdin git did not do the merge named on standard input (status %d):\n%q", status, out)
	}
}
