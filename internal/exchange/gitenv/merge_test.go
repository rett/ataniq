package gitenv

import (
	"errors"
	"os/exec"
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
