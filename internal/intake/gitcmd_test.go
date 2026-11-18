package intake

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/exchange/gitenv"
)

// valuelessFlags are the flags without a value that gitenv lets pass before
// the end of the options. Any other option must be written --name=value.
var valuelessFlags = map[string]bool{
	"--no-tags": true, "--prune": true, "--no-recurse-submodules": true, "--no-write-fetch-head": true,
	"--no-auto-maintenance": true, "--no-auto-gc": true, "--bare": true, "--quiet": true, "-q": true,
	"-z": true, "-r": true, "-m": true, "--raw": true, "--root": true, "--no-renames": true,
	"--full-index": true, "--no-color": true, "--no-ext-diff": true, "--no-textconv": true,
	"--verify": true, "--is-ancestor": true, "--no-deref": true, "--stdin": true, "--list": true,
	"--batch-check": true, "--batch": true, "--objects": true, "--no-walk": true, "--first-parent": true,
	"--no-mailmap": true, "--no-notes": true, "--no-decorate": true, "--no-show-signature": true,
	"-v": true, "--count": true, "--all": true, "--local": true,
}

// recordingGit writes a script that logs its arguments and runs the real git.
func recordingGit(t *testing.T, real gitenv.Git) (gitenv.Git, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\nprintf '%s\\037' \"$@\" >> " + log + "\nprintf '\\n' >> " + log + "\nexec " + real.Path + " \"$@\"\n"
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	g, err := gitenv.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return g, log
}

var (
	idPat    = `[0-9a-f]{40}`
	revPatRE = regexp.MustCompile(`^` + idPat + `(\.\.` + idPat + `|\^\{commit\})?$`)
)

// TestGitCommandLinesFollowTheArgumentRule runs every operation of the
// package with a recording git and checks each command line: before the end
// of the options only valueless flags of the allowed list and --name=value
// options, and after it only object ids, ranges of ids and the two full ref
// names, so that no lead text is ever read by git as an option.
func TestGitCommandLinesFollowTheArgumentRule(t *testing.T) {
	s := aliasScene(t)
	s.symlink(".claude", "cfg")
	s.put(".envrc", "x\n")
	s.commit("one\n")
	s.remove(".envrc")
	changeAll(s)
	s.commit("two\n")
	s.finish()
	rec, logPath := recordingGit(t, s.git)
	src := Source{Git: rec, GitDir: s.bare}
	ctx := context.Background()
	r, err := Inspect(ctx, src, s.request())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Diff(ctx, src, r, &out, Limits{}); err != nil {
		t.Fatal(err)
	}
	// The failing paths too: not a fast-forward.
	s.checkout(s.base)
	s.put("diverge.txt", "x\n")
	up := s.commit("upstream moves\n")
	s.publish(upstreamRef, up)
	var nf *NotFastForwardError
	if _, err := Inspect(ctx, src, s.request()); !errors.As(err, &nf) {
		t.Fatalf("error = %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		args := strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f")
		i := 0
		for i < len(args) && args[i] == "-c" {
			i += 2
		}
		if i >= len(args) || !strings.HasPrefix(args[i], "--git-dir=") {
			t.Fatalf("no --git-dir in %q", args)
		}
		args = args[i+1:]
		sub := args[0]
		seen[sub] = true
		afterEnd := false
		for _, a := range args[1:] {
			switch {
			case afterEnd:
				if !revPatRE.MatchString(a) && a != upstreamRef && a != leadRef {
					t.Errorf("git %s: argument %q after the end of the options is not an id or a full ref name", sub, a)
				}
			case a == "--end-of-options" || a == "--":
				afterEnd = true
			case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
			case valuelessFlags[a]:
			default:
				t.Errorf("git %s: option %q is neither --name=value nor an allowed flag", sub, a)
			}
		}
		// Commands that take a revision or a ref name end the options first.
		// "diff-tree --stdin" takes none: its ids come on standard input.
		switch sub {
		case "for-each-ref", "rev-parse", "merge-base", "rev-list", "log", "diff-tree", "ls-tree", "diff":
			if !afterEnd && !slices.Contains(args, "--stdin") {
				t.Errorf("git %s has no --end-of-options: %q", sub, args)
			}
		}
	}
	for _, sub := range []string{"for-each-ref", "rev-parse", "merge-base", "rev-list", "log", "diff-tree", "ls-tree", "cat-file", "diff"} {
		if !seen[sub] {
			t.Errorf("git %s was not run", sub)
		}
	}
}
