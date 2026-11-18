package gitenv

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// These tests cover the attribute pin: for "diff", "diff-tree", "merge-tree"
// and "log", Argv sets the attribute source to the empty tree, so that a
// ".gitattributes" in a tree a lead wrote changes nothing. The first two
// tests look at the command line. The others ask the installed git, in
// temporary bare repositories, and each has a control that shows the test can
// fail: the same command line with only the attribute source changed by
// hand gives another result.

// thePin is the setting as it stands in the command line.
const thePin = "attr.tree=" + emptyTreeID

// TestAttributePinIsOnTheseSubcommands asserts the list of subcommands that
// get the pin, and where it stands: once, as a -c setting among the
// builder's own, before --git-dir and the subcommand. No other subcommand
// gets it. The builder never writes "--attr-source" and never sets
// GIT_ATTR_SOURCE, both of which would come before the setting.
func TestAttributePinIsOnTheseSubcommands(t *testing.T) {
	want := []string{"diff", "diff-tree", "merge-tree", "log"}
	if !reflect.DeepEqual(attrPinned, want) {
		t.Fatalf("attrPinned = %q, want %q", attrPinned, want)
	}
	if emptyTree != emptyTreeID {
		t.Fatalf("emptyTree = %q, want %q", emptyTree, emptyTreeID)
	}
	g := Git{Path: "/usr/bin/git"}
	for _, sub := range append(slices.Clone(subcommands), hashObject) {
		c := Cmd{GitDir: "/srv/r.git", Args: []string{sub}}
		if sub == hashObject {
			c.Args = append(c.Args, hashObjectArgs...)
		}
		argv, err := g.Argv(c)
		if err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		at := slices.Index(argv, "--git-dir=/srv/r.git")
		if at < 0 || argv[at+1] != sub {
			t.Fatalf("%s: no --git-dir right before the subcommand in %q", sub, argv)
		}
		var pins []int
		for i, a := range argv {
			if strings.HasPrefix(a, "attr.") || strings.Contains(a, emptyTreeID) {
				pins = append(pins, i)
			}
			if strings.HasPrefix(a, "--attr") {
				t.Errorf("%s: the command line holds %q", sub, a)
			}
		}
		if !slices.Contains(want, sub) {
			if len(pins) != 0 {
				t.Errorf("%s: the command line holds an attribute setting: %q", sub, argv)
			}
		} else if len(pins) != 1 || argv[pins[0]] != thePin || argv[pins[0]-1] != "-c" || pins[0] != at-1 {
			t.Errorf("%s: want \"-c %s\" once, right before --git-dir, in %q", sub, thePin, argv)
		}
		// Before the setting stand only -c pairs, so it is one of the
		// builder's own settings and a later one of the same key cannot
		// follow it.
		for i := 1; i < at; i += 2 {
			if argv[i] != "-c" {
				t.Errorf("%s: argument %d before --git-dir is %q, not -c", sub, i, argv[i])
			}
		}
		for _, kv := range g.Env(c) {
			if strings.HasPrefix(kv, "GIT_ATTR_SOURCE=") {
				t.Errorf("%s: the environment holds %q", sub, kv)
			}
		}
	}
	// The pin does not depend on the arguments of the caller.
	for _, args := range [][]string{
		{"diff", "--no-renames", "--end-of-options", "a", "b"},
		{"diff-tree", "-r", "--stdin"},
		{"merge-tree", "--write-tree", "--end-of-options", "a", "b"},
		{"log", "--max-count=1", "--", "attr.tree=HEAD"},
	} {
		argv, err := g.Argv(Cmd{GitDir: "/srv/r.git", Args: args})
		if err != nil {
			t.Fatal(err)
		}
		if at := slices.Index(argv, thePin); at < 1 || argv[at-1] != "-c" || argv[at+1] != "--git-dir=/srv/r.git" {
			t.Errorf("%q: the pin is missing or misplaced in %q", args, argv)
		}
	}
}

// TestAttrSourceInArgsIsRefused shows that a caller cannot set, change or
// remove the attribute source. "--attr-source" is refused in every place of
// Args: before the subcommand (where every option is refused), among the
// options, and after the end of the options; with a value, without one, and
// abbreviated. A caller has no way to pass a -c setting or a variable
// either.
func TestAttrSourceInArgsIsRefused(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	ok := "/srv/r.git"
	forms := []string{
		"--attr-source=HEAD", "--attr-source=" + emptyTreeID, "--attr-source=", "--attr-source",
		"--attr-sourc=HEAD", "--attr-s=HEAD", "--attr=HEAD", "--attr", "--att=HEAD", "--att",
	}
	for _, sub := range subcommands {
		gitDir := ok
		for _, form := range forms {
			for _, args := range [][]string{
				{form, sub},
				{form, "HEAD", sub},
				{sub, form},
				{sub, form, "HEAD"},
				{sub, "-z", form, "--end-of-options", "a"},
				{sub, "--end-of-options", form},
				{sub, "--", form},
				{sub, "--end-of-options", "a", form},
			} {
				if slices.Contains(markerNotEnd[sub], "--") && slices.Contains(args, "--") {
					continue
				}
				argv, err := g.Argv(Cmd{GitDir: gitDir, Args: args})
				if err == nil {
					t.Errorf("Argv accepted %q: %q", args, argv)
					continue
				}
				if argv != nil {
					t.Errorf("%q: Argv returned %q with its error", args, argv)
				}
				if args[0] == sub && !strings.Contains(err.Error(), "the attribute source is the builder's alone") {
					t.Errorf("%q: error %q does not say whose the attribute source is", args, err)
				}
			}
		}
	}
	// The other ways to name a source are closed by the rules that were
	// there: no option before the subcommand, and no setting among Args.
	for _, args := range [][]string{
		{"-c", "attr.tree=HEAD", "diff", "a", "b"},
		{"-cattr.tree=HEAD", "diff", "a", "b"},
		{"--config-env=attr.tree=X", "diff", "a", "b"},
		{"diff", "-c", "attr.tree=HEAD", "a", "b"},
		{"merge-tree", "-c", "attr.tree=HEAD", "a", "b"},
	} {
		if argv, err := g.Argv(Cmd{GitDir: ok, Args: args}); err == nil {
			t.Errorf("Argv accepted %q: %q", args, argv)
		}
	}
	// Names that only look alike are not refused by this rule.
	for _, args := range [][]string{
		{"fetch", "--atomic", "--all"},
		{"log", "--end-of-options", "refs/heads/--attr-source"},
		{"log", "--max-count=1", "--", "attr-source"},
	} {
		if _, err := g.Argv(Cmd{GitDir: ok, Args: args}); err != nil {
			t.Errorf("Argv refused %q: %v", args, err)
		}
	}
}

// pinned is what the pinned commands give for one merge scene. Every field
// is free of the ids of the scene's commits and trees, so two scenes whose
// file "f.txt" has the same history give equal values.
type pinned struct {
	diff     string // "diff" of base and theirs, as a patch
	diffTree string // "diff-tree -r -p" of the same
	numstat  string // "diff-tree -r --numstat" of the same
	diffStat string // "diff --numstat" of the same
	log      string // "log -p" of theirs
	// The merge of ours and theirs, which change different lines: the exit
	// status, and the blob of the merged file when the status is 0.
	cleanStatus int
	cleanBlob   string
	// The exit status of the merge of the two sides that change the same
	// line: 1 for a conflict.
	conflictStatus int
}

// change is a change made by hand to a command line that Argv built, for a
// control. nil leaves the line as Argv built it.
type change func(argv []string) []string

// sourceIs replaces the tree of the pin, the only thing that changes.
func sourceIs(tree string) change {
	return func(argv []string) []string {
		out := slices.Clone(argv)
		out[slices.Index(out, thePin)] = "attr.tree=" + tree
		return out
	}
}

// withoutPin removes the pin, so the line is what Argv built before.
func withoutPin(argv []string) []string {
	at := slices.Index(argv, thePin)
	return slices.Concat(argv[:at-1], argv[at+1:])
}

// withAttrSourceOption adds git's own option before --git-dir and leaves the
// pin in place.
func withAttrSourceOption(tree string) change {
	return func(argv []string) []string {
		at := slices.IndexFunc(argv, func(a string) bool { return strings.HasPrefix(a, "--git-dir=") })
		return slices.Insert(slices.Clone(argv), at, "--attr-source="+tree)
	}
}

// runPinned builds one of the pinned commands with Argv and Env, applies the
// change and the extra environment of a control, and runs it.
func (s *mergeScene) runPinned(t *testing.T, ch change, extraEnv []string, args ...string) (string, int) {
	t.Helper()
	c := Cmd{GitDir: s.repo, Args: args}
	argv, err := s.g.Argv(c)
	if err != nil {
		t.Fatalf("Argv %q: %v", args, err)
	}
	if !slices.Contains(argv, thePin) {
		t.Fatalf("git %s is not pinned: %q", args[0], argv)
	}
	if ch != nil {
		argv = ch(argv)
	}
	out, err := run(argv, append(s.g.Env(c), extraEnv...), "", "")
	return out, exitStatus(t, err)
}

// pinned runs the pinned commands on the scene.
func (s *mergeScene) pinned(t *testing.T, ch change, extraEnv ...string) pinned {
	t.Helper()
	must := func(args ...string) string {
		t.Helper()
		out, status := s.runPinned(t, ch, extraEnv, args...)
		if status != 0 {
			t.Fatalf("git %q ended with status %d:\n%s", args, status, out)
		}
		return out
	}
	var p pinned
	p.diff = must("diff", "--no-color", "--no-renames", "--full-index", "--end-of-options", s.base, s.theirs)
	p.diffTree = must("diff-tree", "-r", "-p", "--no-color", "--no-renames", "--full-index", "--end-of-options", s.base, s.theirs)
	p.numstat = must("diff-tree", "-r", "--numstat", "--no-renames", "--end-of-options", s.base, s.theirs)
	p.diffStat = must("diff", "--numstat", "--no-renames", "--end-of-options", s.base, s.theirs)
	p.log = must("log", "--max-count=1", "--format=%s", "-p", "--no-color", "--no-renames", "--full-index", "--end-of-options", s.theirs)

	out, status := s.runPinned(t, ch, extraEnv, "merge-tree", "--write-tree", "--end-of-options", s.ours, s.theirs)
	p.cleanStatus = status
	if status == 0 {
		tree := strings.TrimSpace(out)
		if !IsObjectID(tree) {
			t.Fatalf("a clean merge printed %q, want one tree id", out)
		}
		p.cleanBlob = s.fileInTree(t, tree)
	} else if status != 1 {
		t.Fatalf("merge-tree ended with status %d:\n%s", status, out)
	}
	out, p.conflictStatus = s.runPinned(t, ch, extraEnv, "merge-tree", "--write-tree", "--name-only", "--no-messages", "--end-of-options", s.clashOurs, s.clashTheirs)
	if p.conflictStatus != 0 && p.conflictStatus != 1 {
		t.Fatalf("merge-tree ended with status %d:\n%s", p.conflictStatus, out)
	}
	return p
}

// checkPlain fails unless p is the result for a scene without attributes:
// a patch that shows the changed line, line counts of one and one, a clean
// merge that holds both changes, and a conflict.
func (s *mergeScene) checkPlain(t *testing.T, p pinned) {
	t.Helper()
	for name, patch := range map[string]string{"diff": p.diff, "diff-tree": p.diffTree, "log": p.log} {
		if !strings.Contains(patch, "\n-8\n+eight-theirs\n") || strings.Contains(patch, "Binary files") {
			t.Errorf("the control: %s does not show the changed line as text:\n%s", name, patch)
		}
	}
	if p.numstat != "1\t1\tf.txt\n" || p.diffStat != p.numstat {
		t.Errorf("the control: line counts %q (diff-tree) and %q (diff), want one added and one removed", p.numstat, p.diffStat)
	}
	if want := s.blob(t, mergeBothText); p.cleanStatus != 0 || p.cleanBlob != want {
		t.Errorf("the control: the clean merge ended with status %d and the blob %q, want 0 and %s", p.cleanStatus, p.cleanBlob, want)
	}
	if p.conflictStatus != 1 {
		t.Errorf("the control: the conflicting merge ended with status %d, want 1", p.conflictStatus)
	}
}

// attributeCases are the attribute lines of the fact note. Each stands as
// the file ".gitattributes" in every tree of its scene, HEAD's included.
var attributeCases = []string{"* -diff", "* binary", "* merge=union", "* merge=binary"}

// TestAttributesInTreesChangeNothing is the start-up test of the pin. For
// each attribute line, "diff", "diff-tree -p", the line counts, "log -p"
// and "merge-tree --write-tree", built through Argv and Env, give exactly
// what the same trees without the file give: the same patch, the same
// counts, the same blob for a clean merge and the same exit status for a
// conflicting one.
//
// The positive control runs the same command lines with the setting pointed
// by hand at the id of HEAD's tree ("-c attr.tree=<id>"), which Argv cannot
// build. Every line then changes the result: "* binary" turns the counts of
// "diff" and "diff-tree" into "-", "* merge=union" makes the conflict of
// "merge-tree" disappear, "* -diff" hides the patch, and "* merge=binary"
// turns the clean merge into a conflict. So the fixtures are able to change
// a result, the comparison is able to see it, and this git honours the
// setting "attr.tree" in every pinned command, "merge-tree" included. With
// nothing set at all, this git reads no attributes from a tree in a bare
// repository, so a control that only dropped the pin could not fail here;
// TestThePinComesBeforeTheRepositoryConfig has the case where it can.
//
// Proves: in this git, with the pin, an attribute file in the compared or
// merged trees and in HEAD changes none of these results. Cannot prove:
// anything for other git versions, for other attributes, or for
// "info/attributes", which the pin does not reach.
func TestAttributesInTreesChangeNothing(t *testing.T) {
	g := needGit(t)
	plain := newMergeScene(t, g, "")
	want := plain.pinned(t, nil)
	plain.checkPlain(t, want)

	for _, line := range attributeCases {
		t.Run(line, func(t *testing.T) {
			s := newMergeScene(t, g, line+"\n")
			if got := s.plain(t, "", "cat-file", "blob", "HEAD:.gitattributes"); got != line {
				t.Fatalf("HEAD's tree holds %q as its attribute file, want %q", got, line)
			}
			if got := s.pinned(t, nil); !reflect.DeepEqual(got, want) {
				t.Errorf("with %q in every tree the pinned commands differ from the same trees without it:\n got  %+v\n want %+v", line, got, want)
			}

			headTree := s.plain(t, "", "rev-parse", "HEAD^{tree}")
			if headTree != s.oursTree || !IsObjectID(headTree) {
				t.Fatalf("HEAD's tree is %q, want the tree of ours %q", headTree, s.oursTree)
			}
			control := s.pinned(t, sourceIs(headTree))
			if reflect.DeepEqual(control, want) {
				t.Fatalf("control failed: with attr.tree set to HEAD's tree, %q changed nothing, so the test could not show anything", line)
			}
			switch line {
			case "* -diff":
				if !strings.Contains(control.diff, "Binary files") || !strings.Contains(control.diffTree, "Binary files") || !strings.Contains(control.log, "Binary files") {
					t.Errorf("control: %q read from HEAD did not hide the patch:\n%+v", line, control)
				}
			case "* binary":
				if changed := "-\t-\tf.txt\n"; control.numstat != changed || control.diffStat != changed {
					t.Errorf("control: %q read from HEAD's tree gave the line counts %q (diff-tree) and %q (diff), want %q for both", line, control.numstat, control.diffStat, changed)
				}
			case "* merge=union":
				if control.conflictStatus != 0 {
					t.Errorf("control: %q read from HEAD's tree left the conflict of merge-tree (status %d), want a clean merge (0): merge-tree does not honour attr.tree in this git", line, control.conflictStatus)
				}
			case "* merge=binary":
				if control.cleanStatus != 1 {
					t.Errorf("control: %q read from HEAD left the clean merge clean (status %d), want a conflict (1)", line, control.cleanStatus)
				}
			}
		})
	}
}

// TestThePinComesBeforeTheRepositoryConfig shows a case where the pin
// changes the result with this git, so the test fails when the pin is
// removed. A bare repository whose own config names HEAD as the attribute
// tree reads the attribute file of HEAD: the command line as Argv built it
// before the pin (the pin removed by hand) gives the changed result. With
// the pin, the setting on the command line comes first and the results are
// those of the trees without the file.
func TestThePinComesBeforeTheRepositoryConfig(t *testing.T) {
	g := needGit(t)
	plain := newMergeScene(t, g, "")
	want := plain.pinned(t, nil)
	for _, line := range attributeCases {
		t.Run(line, func(t *testing.T) {
			s := newMergeScene(t, g, line+"\n")
			appendConfig(t, s.repo, "[attr]\n\ttree = HEAD\n")
			if control := s.pinned(t, withoutPin); reflect.DeepEqual(control, want) {
				t.Fatalf("control failed: without the pin, attr.tree = HEAD in the config and %q in HEAD changed nothing, so the test could not show anything", line)
			}
			if got := s.pinned(t, nil); !reflect.DeepEqual(got, want) {
				t.Errorf("attr.tree = HEAD in the repository's config reached the pinned commands:\n got  %+v\n want %+v", got, want)
			}
		})
	}
}

// TestAttrSourceWouldOverrideThePin is the reason Argv refuses
// "--attr-source" and Env never sets GIT_ATTR_SOURCE: git puts both before
// the setting "attr.tree". The command line of Argv, pin included, with the
// option added by hand, reads the attribute file of HEAD; so does the same
// line with the variable added to the environment by hand.
func TestAttrSourceWouldOverrideThePin(t *testing.T) {
	g := needGit(t)
	plain := newMergeScene(t, g, "")
	want := plain.pinned(t, nil)
	s := newMergeScene(t, g, "* binary\n")
	if got := s.pinned(t, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("the pinned commands differ before anything is added:\n got  %+v\n want %+v", got, want)
	}
	const changed = "-\t-\tf.txt\n"
	if got := s.pinned(t, withAttrSourceOption("HEAD")); got.numstat != changed {
		t.Errorf("with --attr-source=HEAD added by hand the line counts are %q, want %q: the option no longer comes before attr.tree", got.numstat, changed)
	}
	if got := s.pinned(t, nil, "GIT_ATTR_SOURCE=HEAD"); got.numstat != changed {
		t.Errorf("with GIT_ATTR_SOURCE=HEAD added by hand the line counts are %q, want %q: the variable no longer comes before attr.tree", got.numstat, changed)
	}
	// Neither can come from a caller.
	c := Cmd{GitDir: s.repo, Args: []string{"diff-tree", "-r", "--numstat", "--attr-source=HEAD", "--end-of-options", s.base, s.theirs}}
	if argv, err := g.Argv(c); err == nil {
		t.Errorf("Argv accepted %q", argv)
	}
	for _, kv := range g.Env(c) {
		if strings.HasPrefix(kv, "GIT_ATTR_SOURCE") {
			t.Errorf("the environment holds %q", kv)
		}
	}
}

// TestAnAttributeTreeGitCannotResolveReadsNoAttributes records what git does
// with an "attr.tree" that names no object, which is the case of the pin in
// a SHA-256 repository (the id of the pin is the SHA-1 one). git does not
// fail and does not fall back to HEAD or to the repository's config: it
// reads no attributes from any tree. So the pin holds there as well.
//
// Each repository names HEAD as the attribute tree in its own config and
// holds the attribute file in HEAD, and a control without any setting on the
// command line shows that the file is then read. With that, "reads nothing"
// and "falls back" give different results.
func TestAnAttributeTreeGitCannotResolveReadsNoAttributes(t *testing.T) {
	g := needGit(t)

	t.Run("a made-up id", func(t *testing.T) {
		plain := newMergeScene(t, g, "")
		want := plain.pinned(t, nil)
		for _, line := range attributeCases {
			s := newMergeScene(t, g, line+"\n")
			appendConfig(t, s.repo, "[attr]\n\ttree = HEAD\n")
			if control := s.pinned(t, withoutPin); reflect.DeepEqual(control, want) {
				t.Fatalf("control failed: %q in HEAD changed nothing without the pin", line)
			}
			for _, tree := range []string{strings.Repeat("12345678", 5), "refs/heads/missing", ""} {
				if got := s.pinned(t, sourceIs(tree)); !reflect.DeepEqual(got, want) {
					t.Errorf("%q with attr.tree=%q: git read attributes from somewhere:\n got  %+v\n want %+v", line, tree, got, want)
				}
			}
		}
	})

	t.Run("the pin in a SHA-256 repository", func(t *testing.T) {
		sha256 := func(attributes string) *mergeScene {
			repo := filepath.Join(t.TempDir(), "mirror.git")
			mustOwned(t, g, Cmd{Args: []string{"init", "-q", "--bare", "--initial-branch=main", "--object-format=sha256", repo}})
			s := newMergeSceneIn(t, g, repo, attributes)
			if len(s.base) != 64 {
				t.Fatalf("the repository is not a SHA-256 one: commit id %q", s.base)
			}
			return s
		}
		plain := sha256("")
		want := plain.pinned(t, nil)
		plain.checkPlain(t, want)
		for _, line := range attributeCases {
			s := sha256(line + "\n")
			appendConfig(t, s.repo, "[attr]\n\ttree = HEAD\n")
			if control := s.pinned(t, withoutPin); reflect.DeepEqual(control, want) {
				t.Fatalf("control failed: %q in HEAD changed nothing without the pin", line)
			}
			if got := s.pinned(t, nil); !reflect.DeepEqual(got, want) {
				t.Errorf("%q: the pin did not hold in a SHA-256 repository:\n got  %+v\n want %+v", line, got, want)
			}
		}
	})
}

// TestThePinDoesNotReachInfoAttributes shows the limit of the pin and the
// use of the check: a line in the repository's own "info/attributes" changes
// what the pinned commands give, through Argv and Env as they are, and
// [CheckInfoAttributes] refuses that repository. Without the file the same
// repository passes the check and gives the plain results.
func TestThePinDoesNotReachInfoAttributes(t *testing.T) {
	g := needGit(t)
	s := newMergeScene(t, g, "")
	want := s.pinned(t, nil)
	s.checkPlain(t, want)
	if err := CheckInfoAttributes(s.repo); err != nil {
		t.Fatalf("the repository is refused before the file is written: %v", err)
	}
	for _, line := range attributeCases {
		writeInfoAttributes(t, s.repo, line+"\n")
		if got := s.pinned(t, nil); reflect.DeepEqual(got, want) {
			t.Errorf("%q in info/attributes changed nothing: git no longer reads the file under the pin, so the check may no longer be needed", line)
		}
		if err := CheckInfoAttributes(s.repo); err == nil {
			t.Errorf("a repository with %q in info/attributes passed the check", line)
		}
	}
	writeInfoAttributes(t, s.repo, "")
	if err := CheckInfoAttributes(s.repo); err != nil {
		t.Errorf("the repository is refused with an empty file: %v", err)
	}
	if got := s.pinned(t, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("with an empty info/attributes the results differ:\n got  %+v\n want %+v", got, want)
	}
}
