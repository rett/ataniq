package intake

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func noChange(t *testing.T, r *Review, path string) {
	t.Helper()
	for _, c := range r.Changes {
		if c.Path == path {
			t.Errorf("%s is in the net change: %+v", path, c)
		}
	}
}

func TestHistoryOnlyAddedAndRemoved(t *testing.T) {
	s := newScene(t)
	s.put(".envrc", "export X=1\n")
	s.put("src/scratch.go", "package main\n")
	s.symlink("docs/link", "../.claude")
	s.gitlink("vendor/lib")
	s.commit("add several things\n")
	s.put("src/real.go", "package main\n")
	s.commit("add a real file\n")
	s.remove(".envrc")
	s.remove("src/scratch.go")
	s.remove("docs/link")
	s.remove("vendor/lib")
	s.commit("remove them again\n")
	s.finish()
	r := s.inspect()

	if len(r.Changes) != 1 || r.Changes[0].Path != "src/real.go" {
		t.Fatalf("net change: %+v", r.Changes)
	}
	if len(r.Flagged) != 3 {
		t.Fatalf("Flagged: %+v", r.Flagged)
	}
	for _, p := range []string{".envrc", "docs/link", "vendor/lib"} {
		f := flagFor(t, r.Flagged, FlagHistory, p)
		if !strings.Contains(f.Reason, "added and removed again") {
			t.Errorf("%s: reason %q", p, f.Reason)
		}
	}
	if f := flagFor(t, r.Flagged, FlagHistory, ".envrc"); f.Pattern != ".envrc" || !strings.Contains(f.Reason, "execution config") {
		t.Errorf(".envrc: %+v", f)
	}
	if f := flagFor(t, r.Flagged, FlagHistory, "docs/link"); !strings.Contains(f.Reason, "symlink") || f.Pattern != "" {
		t.Errorf("link: %+v", f)
	}
	if f := flagFor(t, r.Flagged, FlagHistory, "vendor/lib"); !strings.Contains(f.Reason, "submodule") {
		t.Errorf("gitlink: %+v", f)
	}
	for _, f := range r.Flagged {
		if f.Path == "src/scratch.go" {
			t.Errorf("an ordinary path is flagged: %+v", f)
		}
	}
	// A history flag is part of the digest.
	plain := *r
	plain.Flagged = nil
	if plain.Digest() == r.Digest() {
		t.Error("the digest ignores history flags")
	}
}

func TestHistoryOnlyEmptyNetChange(t *testing.T) {
	s := newScene(t)
	s.put(".mcp.json", "{}\n")
	s.commit("add\n")
	s.remove(".mcp.json")
	s.commit("remove\n")
	s.finish()
	r := s.inspect()
	if len(r.Changes) != 0 || len(r.Commits) != 2 {
		t.Fatalf("changes %+v, %d commits", r.Changes, len(r.Commits))
	}
	if len(r.Flagged) != 1 || r.Flagged[0].Kind != FlagHistory || r.Flagged[0].Path != ".mcp.json" {
		t.Errorf("Flagged: %+v", r.Flagged)
	}
}

func TestHistoryOnlyThroughAMerge(t *testing.T) {
	s := newScene(t)
	// A side branch adds a file that the merge then drops.
	s.put(".mcp.json", "{}\n")
	side := s.commit("side adds the MCP file\n")
	s.checkout(s.base)
	s.put("src/main.go", "package main\n// mainline\n")
	mainline := s.commit("mainline change\n")
	// The merge result has no .mcp.json.
	s.checkout(mainline)
	s.commit("merge, dropping the MCP file\n", mainline, side)
	s.finish()
	r := s.inspect()
	noChange(t, r, ".mcp.json")
	flagFor(t, r.Flagged, FlagHistory, ".mcp.json")
	if len(r.Commits) != 3 || len(r.Commits[0].Parents) != 2 {
		t.Errorf("commits: %+v", r.Commits)
	}
}

func TestHistoryOnlyInTheChangeOfAMergeCommit(t *testing.T) {
	// A merge commit brings a file in ("evil merge") and a second merge
	// takes it out. No non-merge commit has it in its change: it shows only
	// in the change of a merge against each parent.
	s := newScene(t)
	s.put("a.txt", "a\n")
	left := s.commit("left\n")
	s.checkout(s.base)
	s.put("b.txt", "b\n")
	right := s.commit("right\n")
	s.checkout(s.base)
	s.put("c.txt", "c\n")
	other := s.commit("other\n")

	s.checkout(left)
	s.put("b.txt", "b\n")
	s.put(".githooks/pre-commit", "#!/bin/sh\n")
	merge1 := s.commit("merge with an extra file\n", left, right)
	s.remove(".githooks/pre-commit")
	s.put("c.txt", "c\n")
	s.commit("second merge, without it\n", merge1, other)
	s.finish()
	r := s.inspect()
	noChange(t, r, ".githooks/pre-commit")
	flagFor(t, r.Flagged, FlagHistory, ".githooks/pre-commit")
	if len(r.Changes) != 3 || len(r.Commits) != 5 {
		t.Errorf("%d changes, %d commits", len(r.Changes), len(r.Commits))
	}
}

func TestHistoryOnlyPathChangedKind(t *testing.T) {
	// A file is a symlink in the middle of the branch and a file again at its
	// tip: the net change is an ordinary change.
	s := newScene(t, "docs/l", "plain\n")
	s.symlink("docs/l", "../.claude")
	s.commit("make it a symlink\n")
	s.put("docs/l", "plain, changed\n")
	s.commit("make it a file again\n")
	s.finish()
	r := s.inspect()
	c := byPath(t, r, "docs/l")
	if c.Status != 'M' || len(c.Flags) != 0 {
		t.Errorf("net change: %+v", c)
	}
	f := flagFor(t, r.Flagged, FlagHistory, "docs/l")
	if !strings.Contains(f.Reason, "not one at its tip") {
		t.Errorf("reason: %q", f.Reason)
	}

	// A path that is still a symlink at the tip has its own flag and needs no
	// history flag.
	s = newScene(t, "docs/l", "plain\n")
	s.symlink("docs/l", "../a")
	s.commit("symlink\n")
	s.symlink("docs/l", "../b")
	s.commit("another target\n")
	s.finish()
	r = s.inspect()
	if hasFlag(r.Flagged, FlagHistory) {
		t.Errorf("Flagged: %+v", r.Flagged)
	}
}

func TestHistoryOnlyLimit(t *testing.T) {
	s := newScene(t)
	for _, p := range []string{"a", "b", "c", "d", "e"} {
		s.put("t/"+p, p+"\n")
	}
	s.commit("add five\n")
	for _, p := range []string{"a", "b", "c", "d", "e"} {
		s.remove("t/" + p)
	}
	s.commit("remove five\n")
	s.finish()
	req := s.request()
	req.Limits.MaxPaths = 2 // 8 records of history allowed, 10 listed
	if _, err := Inspect(context.Background(), s.source(), req); !errors.Is(err, ErrLimit) {
		t.Errorf("error = %v", err)
	}
	req.Limits.MaxPaths = 3
	if _, err := Inspect(context.Background(), s.source(), req); err != nil {
		t.Errorf("at the limit: %v", err)
	}
}
