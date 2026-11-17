package mirror

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
)

func TestValidName(t *testing.T) {
	good := []string{"a", "0", "lead", "my-project", "a-", "x1-y2", strings.Repeat("a", 63)}
	bad := []string{
		"", "-a", "A", "Lead", "a_b", "a.b", "a b", "a/b", "é", "a\x00", "a\n", "../x",
		strings.Repeat("a", 64), " a", "a ",
	}
	for _, s := range good {
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ValidName(s) {
			t.Errorf("ValidName(%q) = true, want false", s)
		}
	}
}

// gitRefOK asks git whether refs/heads/<name> is a valid reference name.
func gitRefOK(t *testing.T, g gitenv.Git, name string) bool {
	t.Helper()
	c := gitenv.Cmd{GitDir: t.TempDir(), Args: []string{"check-ref-format", "refs/heads/" + name}}
	argv, err := g.Argv(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = boundedexec.Run(context.Background(), boundedexec.Cmd{
		Argv: argv, Env: g.Env(c), Timeout: 10 * time.Second, MaxStdout: 1 << 10, MaxStderr: 1 << 10,
	})
	if err == nil {
		return true
	}
	var ee *boundedexec.ExitError
	if errors.As(err, &ee) && ee.Exit == 1 {
		return false
	}
	t.Fatalf("check-ref-format: %v", err)
	return false
}

// TestRefNameRulesMatchGit compares validRefName with git check-ref-format,
// first on a table, then on every combination of a few tokens.
func TestRefNameRulesMatchGit(t *testing.T) {
	g := needGit(t)
	table := []string{
		"main", "develop", "feature/x", "a/b/c", "x-y", "x_y", "a.b", "v1.0", "é", "日本",
		"a/.b", ".a", "a/b.lock", "a.lock/b", "a.lock", "a..b", "..", "a/..", "a@{b", "a@b", "@", "@{",
		"a b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", "a\x01b", "a\x1fb", "a\x7fb", "a\tb", "a\nb",
		"/a", "a/", "a//b", "a.", "a/b.", "-a", "a-", "HEAD", "refs/heads/x", "a/-b", ".", "a/./b", "a/b/", "",
		"\xff", "a\xc3",
	}
	for _, n := range table {
		if got, want := validRefName("refs/heads/"+n), gitRefOK(t, g, n); got != want {
			t.Errorf("validRefName(refs/heads/%q) = %v, git says %v", n, got, want)
		}
	}
	if testing.Short() {
		return
	}
	tokens := []string{"a", "/", ".", "..", ".lock", "@", "{", " ", "~", ":", "*", "\x01", "-"}
	var walk func(prefix string, depth int)
	walk = func(prefix string, depth int) {
		if prefix != "" {
			if got, want := validRefName("refs/heads/"+prefix), gitRefOK(t, g, prefix); got != want {
				t.Errorf("validRefName(refs/heads/%q) = %v, git says %v", prefix, got, want)
			}
		}
		if depth == 0 {
			return
		}
		for _, tok := range tokens {
			walk(prefix+tok, depth-1)
		}
	}
	walk("", 3)
}

func TestValidBranch(t *testing.T) {
	good := []string{"main", "develop", "feature/x", "fix-1", "a.b", "é", strings.Repeat("a", 255), "refs", "heads/x", "HEAD2", "a/-b"}
	bad := []string{
		"", "-a", "--end-of-options", "-", "HEAD", "refs/heads/x", "refs/x", "a..b", "a/../b", "a b", "a\x00b", "a\x01b",
		"a\x7fb", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", "a@{b", "/a", "a/", "a//b", "a.", "a.lock", "a/b.lock",
		".a", "a/.b", "@", "\xff", strings.Repeat("a", 256), "a\nb",
	}
	for _, s := range good {
		if !ValidBranch(s) {
			t.Errorf("ValidBranch(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ValidBranch(s) {
			t.Errorf("ValidBranch(%q) = true, want false", s)
		}
	}
}

func TestRefHelpers(t *testing.T) {
	if got, err := UpstreamRef("develop"); err != nil || got != "refs/heads/develop" {
		t.Errorf("UpstreamRef = %q, %v", got, err)
	}
	if got, err := LeadRef("coder", "develop"); err != nil || got != "refs/leads/coder/heads/develop" {
		t.Errorf("LeadRef = %q, %v", got, err)
	}
	if got, err := LeadPrefix("coder"); err != nil || got != "refs/leads/coder/heads/" {
		t.Errorf("LeadPrefix = %q, %v", got, err)
	}
	if got, err := LeadRefspec("coder"); err != nil || got != "+refs/heads/*:refs/leads/coder/heads/*" {
		t.Errorf("LeadRefspec = %q, %v", got, err)
	}
	for _, b := range []string{"", "-x", "a..b", "refs/heads/x", "a\x01", "a b", "HEAD"} {
		if _, err := UpstreamRef(b); !errors.Is(err, ErrBadName) {
			t.Errorf("UpstreamRef(%q) err = %v, want ErrBadName", b, err)
		}
		if _, err := LeadRef("coder", b); !errors.Is(err, ErrBadName) {
			t.Errorf("LeadRef(coder, %q) err = %v, want ErrBadName", b, err)
		}
	}
	for _, r := range []string{"", "Coder", "-c", "a/b", "a..b", "a b", "a\x00", "../x", strings.Repeat("a", 64)} {
		if _, err := LeadRef(r, "x"); !errors.Is(err, ErrBadName) {
			t.Errorf("LeadRef(%q, x) err = %v, want ErrBadName", r, err)
		}
		if _, err := LeadPrefix(r); !errors.Is(err, ErrBadName) {
			t.Errorf("LeadPrefix(%q) err = %v, want ErrBadName", r, err)
		}
		if _, err := LeadRefspec(r); !errors.Is(err, ErrBadName) {
			t.Errorf("LeadRefspec(%q) err = %v, want ErrBadName", r, err)
		}
	}
	// An error text never holds the hostile name.
	if _, err := UpstreamRef("evil\x1b[31m"); err == nil || strings.Contains(err.Error(), "evil") {
		t.Errorf("error text holds the name: %v", err)
	}
}

func TestSplitLeadRef(t *testing.T) {
	type c struct {
		ref, role, branch string
		ok                bool
	}
	for _, tc := range []c{
		{"refs/leads/coder/heads/develop", "coder", "develop", true},
		{"refs/leads/coder/heads/feature/x", "coder", "feature/x", true},
		{"refs/leads/a/heads/-x", "a", "-x", true}, // a listing names every ref git holds
		{"refs/heads/develop", "", "", false},
		{"refs/leads/coder/tags/v1", "", "", false},
		{"refs/leads/Coder/heads/x", "", "", false},
		{"refs/leads//heads/x", "", "", false},
		{"refs/leads/coder/heads/", "", "", false},
		{"refs/leads/coder/heads", "", "", false},
		{"refs/leads/coder", "", "", false},
		{"refs/leads/coder/heads/a..b", "", "", false},
		{"refs/leads/coder/heads/a\x01", "", "", false},
		{"refs/leads/co/der/heads/x", "", "", false},
		{"refs/leads/coder/heads/" + strings.Repeat("a", 256), "", "", false},
		{"x/refs/leads/coder/heads/x", "", "", false},
		{"", "", "", false},
	} {
		role, branch, ok := SplitLeadRef(tc.ref)
		if role != tc.role || branch != tc.branch || ok != tc.ok {
			t.Errorf("SplitLeadRef(%q) = %q, %q, %v; want %q, %q, %v", tc.ref, role, branch, ok, tc.role, tc.branch, tc.ok)
		}
	}
	// What LeadRef builds, SplitLeadRef takes apart.
	ref, _ := LeadRef("coder-2", "feature/y")
	if r, b, ok := SplitLeadRef(ref); !ok || r != "coder-2" || b != "feature/y" {
		t.Errorf("round trip failed: %q %q %v", r, b, ok)
	}
}

func TestPathAndLeadTree(t *testing.T) {
	if got, err := Path("/var/state", "proj-1"); err != nil || got != "/var/state/mirrors/proj-1.git" {
		t.Errorf("Path = %q, %v", got, err)
	}
	if got, err := LeadTree("/var/state", "coder"); err != nil || got != "/var/state/leads/coder/tree" {
		t.Errorf("LeadTree = %q, %v", got, err)
	}
	for _, root := range []string{"", "state", "./state", "/var/state/", "/var//state", "/var/../state", "/", "/a\x00b"} {
		if _, err := Path(root, "p"); err == nil {
			t.Errorf("Path(%q) accepted a bad state root", root)
		}
		if _, err := LeadTree(root, "p"); err == nil {
			t.Errorf("LeadTree(%q) accepted a bad state root", root)
		}
	}
	for _, name := range []string{"", "P", "a/b", "..", "a.git", "-a"} {
		if _, err := Path("/s", name); !errors.Is(err, ErrBadName) {
			t.Errorf("Path(/s, %q) err = %v", name, err)
		}
		if _, err := LeadTree("/s", name); !errors.Is(err, ErrBadName) {
			t.Errorf("LeadTree(/s, %q) err = %v", name, err)
		}
	}
}
