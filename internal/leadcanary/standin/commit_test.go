//go:build linux && ataniq_canary

package standin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitWritesTheFileAndCommitsOnTheBranch(t *testing.T) {
	r := newRepo(t)
	first := r.head(t)
	if err := Commit(r.Dir, r.Home, testToken); err != nil {
		t.Fatal(err)
	}
	if got := r.branch(t); got != "canary/one" {
		t.Fatalf("branch %q, want canary/one", got)
	}
	b, err := os.ReadFile(filepath.Join(r.Dir, "canary-token.txt"))
	if err != nil || string(b) != testToken+"\n" {
		t.Fatalf("file %q, %v; want the token and one LF", b, err)
	}
	if got := runGit(t, r.Dir, "log", "-1", "--format=%s"); got != "canary: token" {
		t.Fatalf("subject %q", got)
	}
	if got := runGit(t, r.Dir, "log", "-1", "--format=%b"); got != "" {
		t.Fatalf("body %q, want none", got)
	}
	if got := runGit(t, r.Dir, "rev-parse", "HEAD^"); got != first {
		t.Fatalf("parent %s, want the commit it was made from, %s", got, first)
	}
	if got := runGit(t, r.Dir, "show", "--name-only", "--format=", "HEAD"); got != "canary-token.txt" {
		t.Fatalf("the commit holds %q", got)
	}
	if got := runGit(t, r.Dir, "rev-parse", "develop"); got != first {
		t.Fatalf("develop moved to %s", got)
	}
	if got := runGit(t, r.Dir, "status", "--porcelain"); got != "" {
		t.Fatalf("the tree is not clean: %q", got)
	}
}

func TestCommitFromASubfolderWritesAtTheTop(t *testing.T) {
	r := newRepo(t)
	sub := filepath.Join(r.Dir, "deep", "er")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Commit(sub, r.Home, testToken); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "canary-token.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sub, "canary-token.txt")); err == nil {
		t.Fatal("the file was written in the subfolder")
	}
}

func TestCommitTwiceStaysOnTheBranch(t *testing.T) {
	r := newRepo(t)
	if err := Commit(r.Dir, r.Home, testToken); err != nil {
		t.Fatal(err)
	}
	first := r.head(t)
	second := "second-" + testToken
	if err := Commit(r.Dir, r.Home, second); err != nil {
		t.Fatal(err)
	}
	if r.head(t) == first || runGit(t, r.Dir, "rev-parse", "HEAD^") != first {
		t.Fatal("the second commit is not on top of the first")
	}
	if got := r.branch(t); got != "canary/one" {
		t.Fatalf("branch %q", got)
	}
	// The same token again is committed too.
	if err := Commit(r.Dir, r.Home, second); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, r.Dir, "rev-list", "--count", "develop..canary/one"); got != "3" {
		t.Fatalf("%s commits on the branch, want 3", got)
	}
}

func TestCommitFromAnotherBranchSwitchesToIt(t *testing.T) {
	r := newRepo(t)
	if err := Commit(r.Dir, r.Home, testToken); err != nil {
		t.Fatal(err)
	}
	runGit(t, r.Dir, "switch", "develop")
	if err := Commit(r.Dir, r.Home, "other-"+testToken); err != nil {
		t.Fatal(err)
	}
	if got := r.branch(t); got != "canary/one" {
		t.Fatalf("branch %q", got)
	}
	if got := runGit(t, r.Dir, "rev-list", "--count", "develop..canary/one"); got != "2" {
		t.Fatalf("%s commits on the branch, want 2", got)
	}
}

func TestCommitRefusesABadTokenAndTouchesNothing(t *testing.T) {
	r := newRepo(t)
	first := r.head(t)
	for _, tok := range []string{"", "short", testToken + "\n", testToken + ";touch pwned", "$(touch pwned)0123456789", strings.Repeat("a", 65), "UPPER" + testToken} {
		if err := Commit(r.Dir, r.Home, tok); err == nil {
			t.Errorf("Commit accepted %q", tok)
		}
	}
	if r.head(t) != first || r.branch(t) != "develop" {
		t.Fatal("a refused token changed the repository")
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "canary-token.txt")); err == nil {
		t.Fatal("a refused token wrote the file")
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "pwned")); err == nil {
		t.Fatal("text of a token ran")
	}
}

func TestCommitFailsOutsideARepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	if err := Commit(dir, dir, testToken); err == nil {
		t.Fatal("Commit succeeded outside a repository")
	}
	if _, err := os.Stat(filepath.Join(dir, "canary-token.txt")); err == nil {
		t.Fatal("the file was written outside a repository")
	}
}

func TestCommitRefusesATokenFileThatIsALink(t *testing.T) {
	r := newRepo(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(r.Dir, "canary-token.txt")); err != nil {
		t.Fatal(err)
	}
	if err := Commit(r.Dir, r.Home, testToken); err == nil {
		t.Fatal("Commit wrote through a link")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep\n" {
		t.Fatalf("the link's target was changed: %q", b)
	}
}

// GitPath is replaced by a script that records its arguments and its whole
// environment, then runs the real git: the environment holds only what the
// stand-in sets, and the arguments carry no text of a message.
func TestCommitUsesGitPathWithAMinimalEnvironment(t *testing.T) {
	r := newRepo(t)
	real := GitPath
	rec := filepath.Join(t.TempDir(), "record")
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\n{ echo \"ARGS $*\"; env | sort; echo END; } >> " + rec + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATANIQ_SAMPLE_SECRET", "leak")
	GitPath = wrapper
	defer func() { GitPath = real }()
	if err := Commit(r.Dir, r.Home, testToken); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	record := string(b)
	if strings.Contains(record, "leak") || strings.Contains(record, "ATANIQ_SAMPLE_SECRET") {
		t.Fatalf("git inherited the environment:\n%s", record)
	}
	var calls int
	for _, block := range strings.Split(record, "END\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		calls++
		lines := strings.Split(strings.TrimSpace(block), "\n")
		got := map[string]bool{}
		for _, l := range lines[1:] {
			name, _, _ := strings.Cut(l, "=")
			got[name] = true
		}
		for _, name := range []string{"PATH", "LC_ALL", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "HOME"} {
			if !got[name] {
				t.Errorf("git call %q lacks %s", lines[0], name)
			}
			delete(got, name)
		}
		// The shell the script runs in adds PWD, SHLVL and _ ; nothing else may be there.
		for name := range got {
			switch name {
			case "PWD", "SHLVL", "_", "OLDPWD":
			default:
				t.Errorf("git call %q has the variable %s", lines[0], name)
			}
		}
		if strings.Contains(lines[0], testToken) {
			t.Errorf("the token is an argument of git: %q", lines[0])
		}
	}
	if calls < 4 {
		t.Fatalf("%d git calls recorded", calls)
	}
	if !strings.Contains(record, "ARGS -c core.hooksPath=/dev/null") {
		t.Fatalf("no call carries the fixed configuration:\n%s", record)
	}
}
