package mirror

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/exchange/gitenv"
)

// leadClone is a lead's clone, a plain repository with a working tree, at
// <state>/leads/<role>/tree.
type leadClone struct {
	g     gitenv.Git
	state string
	role  string
	tree  string
}

// newLeadClone makes the clone with one commit on main and returns it. The
// folders "leads" and "leads/<role>" are the owner's, mode 0700.
func newLeadClone(t *testing.T, g gitenv.Git, state, role string) *leadClone {
	t.Helper()
	tree, err := LeadTree(state, role)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(tree), 0o700); err != nil {
		t.Fatal(err)
	}
	c := &leadClone{g: g, state: state, role: role, tree: tree}
	if err := os.Mkdir(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, g, tree, "init", "--quiet", "--initial-branch=main")
	c.commit(t, "file.txt", "one\n", "one")
	return c
}

// commit writes a file and commits it on the current branch. It returns the
// new commit's id.
func (c *leadClone) commit(t *testing.T, name, text, msg string) string {
	t.Helper()
	p := filepath.Join(c.tree, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, c.g, c.tree, "add", name)
	fixtureGit(t, c.g, c.tree, "commit", "--quiet", "--no-gpg-sign", "-m", msg)
	return fixtureGit(t, c.g, c.tree, "rev-parse", "HEAD")
}

func (c *leadClone) git(t *testing.T, args ...string) string {
	t.Helper()
	return fixtureGit(t, c.g, c.tree, args...)
}

func (c *leadClone) gitDir() string { return filepath.Join(c.tree, ".git") }

// swapForLink moves the folder or file at rel (relative to the state root)
// to a place outside the state root and puts a symlink to it in its place.
// It returns the new place.
func swapForLink(t *testing.T, state, rel string) string {
	t.Helper()
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	p := filepath.Join(state, rel)
	if err := os.Rename(p, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, p); err != nil {
		t.Fatal(err)
	}
	return elsewhere
}

func TestCheckLeadTree(t *testing.T) {
	g := needGit(t)
	mk := func(t *testing.T) *leadClone { return newLeadClone(t, g, t.TempDir(), "coder") }
	write := func(rel, text string) func(t *testing.T, c *leadClone) {
		return func(t *testing.T, c *leadClone) {
			p := filepath.Join(c.gitDir(), rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	symlink := func(rel string) func(t *testing.T, c *leadClone) {
		return func(t *testing.T, c *leadClone) {
			p := filepath.Join(c.gitDir(), rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/nonexistent/x", p); err != nil {
				t.Fatal(err)
			}
		}
	}
	folder := func(rel string) func(t *testing.T, c *leadClone) {
		return func(t *testing.T, c *leadClone) {
			if err := os.MkdirAll(filepath.Join(c.gitDir(), rel), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []struct {
		name   string
		plant  func(t *testing.T, c *leadClone)
		reason string
	}{
		{"leads a symlink", func(t *testing.T, c *leadClone) { swapForLink(t, c.state, "leads") }, "the folder leads is not a real folder"},
		{"leads missing", func(t *testing.T, c *leadClone) {
			if err := os.RemoveAll(filepath.Join(c.state, "leads")); err != nil {
				t.Fatal(err)
			}
		}, "the folder leads is missing"},
		{"lead folder a symlink", func(t *testing.T, c *leadClone) { swapForLink(t, c.state, "leads/coder") }, "the lead's folder is not a real folder"},
		{"tree a symlink to another repository", func(t *testing.T, c *leadClone) {
			other := newLeadClone(t, g, t.TempDir(), "coder")
			if err := os.RemoveAll(c.tree); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other.tree, c.tree); err != nil {
				t.Fatal(err)
			}
		}, "the lead's clone is not a real folder"},
		{"tree a file", func(t *testing.T, c *leadClone) {
			if err := os.RemoveAll(c.tree); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(c.tree, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "the lead's clone is not a real folder"},
		{"tree missing", func(t *testing.T, c *leadClone) {
			if err := os.RemoveAll(c.tree); err != nil {
				t.Fatal(err)
			}
		}, "the lead's clone is missing"},
		{".git a symlink", func(t *testing.T, c *leadClone) { swapForLink(t, c.state, "leads/coder/tree/.git") }, "the .git folder of the clone is not a real folder"},
		{".git a file with a gitdir line", func(t *testing.T, c *leadClone) {
			elsewhere := swapForLink(t, c.state, "leads/coder/tree/.git")
			if err := os.Remove(c.gitDir()); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(c.gitDir(), []byte("gitdir: "+elsewhere+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "the .git folder of the clone is not a real folder"},
		{".git missing", func(t *testing.T, c *leadClone) {
			if err := os.RemoveAll(c.gitDir()); err != nil {
				t.Fatal(err)
			}
		}, "the .git folder of the clone is missing"},
		{".git/objects a symlink", func(t *testing.T, c *leadClone) { swapForLink(t, c.state, "leads/coder/tree/.git/objects") }, "the object folder of the clone is not a real folder"},
		{".git/objects a file", func(t *testing.T, c *leadClone) {
			if err := os.RemoveAll(filepath.Join(c.gitDir(), "objects")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(c.gitDir(), "objects"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "the object folder of the clone is not a real folder"},
		{"alternates", write("objects/info/alternates", "/elsewhere/objects\n"), "the clone has an alternates file"},
		{"alternates empty", write("objects/info/alternates", ""), "the clone has an alternates file"},
		{"alternates a symlink", symlink("objects/info/alternates"), "the clone has an alternates file"},
		{"alternates a folder", folder("objects/info/alternates"), "the clone has an alternates file"},
		{"http alternates", write("objects/info/http-alternates", "http://example.invalid/\n"), "the clone has an http-alternates file"},
		{"http alternates a symlink", symlink("objects/info/http-alternates"), "the clone has an http-alternates file"},
		{"objects/info a symlink", func(t *testing.T, c *leadClone) {
			swapForLink(t, c.state, "leads/coder/tree/.git/objects/info")
		}, "cannot be checked for an alternates file"},
		{"objects/info a file", func(t *testing.T, c *leadClone) {
			p := filepath.Join(c.gitDir(), "objects", "info")
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "cannot be checked for an alternates file"},
		{"commondir", write("commondir", "../elsewhere\n"), "the clone has a commondir file"},
		{"commondir a symlink", symlink("commondir"), "the clone has a commondir file"},
		{"gitdir", write("gitdir", "/elsewhere/.git\n"), "the clone has a gitdir file"},
		{"shallow", write("shallow", ""), "the clone is shallow"},
		{"shallow with content", write("shallow", strings.Repeat("0", 40)+"\n"), "the clone is shallow"},
		{"shallow a symlink", symlink("shallow"), "the clone is shallow"},
		{"shallow a folder", folder("shallow"), "the clone is shallow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mk(t)
			tc.plant(t, c)
			err := checkLeadTree(c.state, c.role)
			if !errors.Is(err, ErrHostileTree) {
				t.Fatalf("checkLeadTree = %v, want ErrHostileTree", err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error %q does not say %q", err, tc.reason)
			}
		})
	}

	t.Run("a plain clone passes", func(t *testing.T) {
		c := mk(t)
		if err := checkLeadTree(c.state, c.role); err != nil {
			t.Errorf("checkLeadTree = %v", err)
		}
	})
	t.Run("things the check leaves alone", func(t *testing.T) {
		// Hooks and config are not read by the owner side, and an
		// objects/info folder with other files is fine.
		c := mk(t)
		write("hooks/pre-commit", "#!/bin/sh\n")(t, c)
		write("objects/info/packs", "\n")(t, c)
		write("info/exclude", "x\n")(t, c)
		if err := checkLeadTree(c.state, c.role); err != nil {
			t.Errorf("checkLeadTree = %v", err)
		}
	})
	t.Run("the state root a symlink", func(t *testing.T) {
		c := mk(t)
		link := filepath.Join(t.TempDir(), "state")
		if err := os.Symlink(c.state, link); err != nil {
			t.Fatal(err)
		}
		if err := checkLeadTree(link, c.role); err == nil {
			t.Error("a symlink as the state root was accepted")
		}
	})
	t.Run("the text holds no name from the clone", func(t *testing.T) {
		c := mk(t)
		write("objects/info/alternates", "SECRET-NAME\n")(t, c)
		if err := checkLeadTree(c.state, c.role); err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), c.state) {
			t.Errorf("error = %v", err)
		}
	})
}
