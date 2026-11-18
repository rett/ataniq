package gitenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// infoCase makes a folder that stands for a bare repository, with an "info"
// folder in it, and returns the repository path and the path of the
// attribute file, which does not exist yet.
func infoCase(t *testing.T) (repo, attributes string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "mirror.git")
	if err := os.MkdirAll(filepath.Join(repo, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, filepath.Join(repo, "info", "attributes")
}

func TestCheckInfoAttributesPasses(t *testing.T) {
	// No file.
	repo, attributes := infoCase(t)
	if err := CheckInfoAttributes(repo); err != nil {
		t.Errorf("no file: %v", err)
	}
	// An empty regular file.
	if err := os.WriteFile(attributes, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckInfoAttributes(repo); err != nil {
		t.Errorf("an empty file: %v", err)
	}
	// An empty file nobody may read is still empty.
	if err := os.Chmod(attributes, 0); err != nil {
		t.Fatal(err)
	}
	if err := CheckInfoAttributes(repo); err != nil {
		t.Errorf("an empty file with no permission bits: %v", err)
	}
	// No "info" folder at all.
	bare := filepath.Join(t.TempDir(), "mirror.git")
	if err := os.Mkdir(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckInfoAttributes(bare); err != nil {
		t.Errorf("no info folder: %v", err)
	}
	// A path that is not clean names the same folder.
	if err := CheckInfoAttributes(bare + "/./"); err != nil {
		t.Errorf("a path with a dot and a slash: %v", err)
	}
}

func TestCheckInfoAttributesRefusesContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"an attribute line", "* binary\n"},
		{"a merge attribute", "* merge=union\n"},
		{"a line without a newline", "*.txt -diff"},
		{"a comment", "# nothing here\n"},
		{"a blank line", "\n"},
		{"a space", " "},
		{"a NUL", "\x00"},
	} {
		repo, attributes := infoCase(t)
		if err := os.WriteFile(attributes, []byte(tc.text), 0o644); err != nil {
			t.Fatal(err)
		}
		err := CheckInfoAttributes(repo)
		if err == nil {
			t.Errorf("%s: the repository passed", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "is not empty") || !strings.Contains(err.Error(), attributes) {
			t.Errorf("%s: error %q does not say that the file is not empty, or does not name it", tc.name, err)
		}
	}
}

// TestCheckInfoAttributesRefusesOtherFileTypes covers what stands in the
// place of the file and is not a regular file. A symbolic link is refused
// without being followed: one that leads to an empty file, to /dev/null or
// to nothing is refused like one that leads to attribute lines.
func TestCheckInfoAttributesRefusesOtherFileTypes(t *testing.T) {
	outside := t.TempDir()
	empty := filepath.Join(outside, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(outside, "full")
	if err := os.WriteFile(full, []byte("* binary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		make func(t *testing.T, attributes string)
	}{
		{"a link to an empty file", func(t *testing.T, a string) { mustSymlink(t, empty, a) }},
		{"a link to a file with lines", func(t *testing.T, a string) { mustSymlink(t, full, a) }},
		{"a link to /dev/null", func(t *testing.T, a string) { mustSymlink(t, "/dev/null", a) }},
		{"a link to nothing", func(t *testing.T, a string) { mustSymlink(t, filepath.Join(outside, "missing"), a) }},
		{"a relative link", func(t *testing.T, a string) { mustSymlink(t, "exclude", a) }},
		{"a link to itself", func(t *testing.T, a string) { mustSymlink(t, "attributes", a) }},
		{"an empty folder", func(t *testing.T, a string) {
			if err := os.Mkdir(a, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"a FIFO", makeFIFO},
	} {
		repo, attributes := infoCase(t)
		tc.make(t, attributes)
		if _, err := os.Lstat(attributes); err != nil {
			t.Logf("%s: not made on this platform, skipped", tc.name)
			continue
		}
		err := CheckInfoAttributes(repo)
		if err == nil {
			t.Errorf("%s: the repository passed", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "is not a regular file") {
			t.Errorf("%s: error %q does not say that it is not a regular file", tc.name, err)
		}
	}
}

// makeFIFO makes a FIFO at path with the mkfifo program, so that the test
// needs no system call of one platform. Where the program is missing the
// case is skipped, except on Linux, where it must run.
func makeFIFO(t *testing.T, path string) {
	t.Helper()
	out, err := exec.Command("/usr/bin/mkfifo", path).CombinedOutput()
	if err != nil && runtime.GOOS == "linux" {
		t.Fatalf("mkfifo: %v\n%s", err, out)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// TestCheckInfoAttributesRefusesWhatItCannotCheck covers the paths the check
// cannot look at: it refuses them and does not pass by default.
func TestCheckInfoAttributesRefusesWhatItCannotCheck(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// "info" is a file, so nothing can be looked up below it.
	infoIsFile := filepath.Join(dir, "odd.git")
	if err := os.Mkdir(infoIsFile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infoIsFile, "info"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"relative", "mirror.git"},
		{"dot", "."},
		{"NUL", dir + "\x00"},
		{"missing", filepath.Join(dir, "missing.git")},
		{"a file", file},
		{"info is a file", infoIsFile},
	} {
		if err := CheckInfoAttributes(tc.path); err == nil {
			t.Errorf("%s: CheckInfoAttributes(%q) passed", tc.name, tc.path)
		}
	}
}

// TestCheckInfoAttributesOnARepositoryOfGit runs the check on a bare
// repository as this package's "init" leaves it, which must pass, and after a
// line is written to the file, which must not.
func TestCheckInfoAttributesOnARepositoryOfGit(t *testing.T) {
	g := needGit(t)
	repo := bare(t, g)
	if err := CheckInfoAttributes(repo); err != nil {
		t.Fatalf("a new bare repository is refused: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "info", "attributes"), []byte("* binary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckInfoAttributes(repo); err == nil {
		t.Fatal("a repository with a line in info/attributes passed")
	}
}

// emptyTreeID is the id of the tree with no entry, in a SHA-1 repository. It
// is written out here, so that the tests do not take it from the code they
// test.
const emptyTreeID = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// TestGitReadsInfoAttributesWhateverTheSource is the reason for the check.
// It asks the installed git, by hand and outside Argv, with the attribute
// source set to the empty tree in both ways git offers ("--attr-source" and
// the setting "attr.tree"): a line in "info/attributes" still changes the
// result. "* binary" turns the line counts of a text file into "-", and
// "* merge=union" makes a conflicting merge end with status 0. The same
// commands in the same repository without the file give the plain result, so
// the file is what changed it. [CheckInfoAttributes] refuses each of these
// repositories.
//
// Proves: in this git, neither way of naming the attribute source stops the
// file from being read. Cannot prove: that no other option would.
func TestGitReadsInfoAttributesWhateverTheSource(t *testing.T) {
	g := needGit(t)
	s := newMergeScene(t, g, "")
	attributes := filepath.Join(s.repo, "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(attributes), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, source := range [][]string{
		{"--attr-source=" + emptyTreeID},
		{"-c", "attr.tree=" + emptyTreeID},
	} {
		git := func(args ...string) (string, error) {
			argv := append([]string{g.Path, "-c", "core.attributesFile=/dev/null"}, source...)
			argv = append(argv, "--git-dir="+s.repo)
			return run(append(argv, args...), s.env, "", "")
		}
		numstat := func() string {
			out, err := git("diff-tree", "-r", "--numstat", "--no-renames", s.base, s.theirs)
			if err != nil {
				t.Fatalf("%v: diff-tree: %v\n%s", source, err, out)
			}
			return strings.TrimSpace(out)
		}
		conflict := func() int {
			_, err := git("merge-tree", "--write-tree", "--name-only", s.clashOurs, s.clashTheirs)
			return exitStatus(t, err)
		}

		// Without the file.
		if err := os.RemoveAll(attributes); err != nil {
			t.Fatal(err)
		}
		if got := numstat(); got != "1\t1\tf.txt" {
			t.Fatalf("%v: without the file the line counts are %q", source, got)
		}
		if got := conflict(); got != 1 {
			t.Fatalf("%v: without the file the conflicting merge ended with status %d", source, got)
		}
		if err := CheckInfoAttributes(s.repo); err != nil {
			t.Errorf("without the file the repository is refused: %v", err)
		}

		// "* binary" changes the diff.
		if err := os.WriteFile(attributes, []byte("* binary\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := numstat(); got != "-\t-\tf.txt" {
			t.Errorf("%v: with \"* binary\" in info/attributes the line counts are %q, want \"-\\t-\\tf.txt\": git did not read the file, so the check may no longer be needed", source, got)
		}
		if err := CheckInfoAttributes(s.repo); err == nil {
			t.Errorf("a repository with \"* binary\" in info/attributes passed the check")
		}

		// "* merge=union" hides the conflict.
		if err := os.WriteFile(attributes, []byte("* merge=union\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := conflict(); got != 0 {
			t.Errorf("%v: with \"* merge=union\" in info/attributes the conflicting merge ended with status %d, want 0: git did not read the file, so the check may no longer be needed", source, got)
		}
		if err := CheckInfoAttributes(s.repo); err == nil {
			t.Errorf("a repository with \"* merge=union\" in info/attributes passed the check")
		}
	}
}
