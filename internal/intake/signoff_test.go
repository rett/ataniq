package intake

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// flaggedScene has a change with flags of every kind: an execution-config
// path that is also an alias target, a symlink, a submodule, an aliased path
// and a history-only path.
func flaggedScene(t *testing.T) *scene {
	t.Helper()
	s := newScene(t, "cfg/settings.json", "{}\n")
	s.symlink(".codex", "cfg")
	s.commit("link in upstream\n")
	s.publish(upstreamRef, s.cur)
	s.base = s.cur
	s.put("Makefile", "all:\n")
	s.put("cfg/settings.json", `{"hooks":1}`+"\n")
	s.symlink("docs/link", "../x")
	s.symlink(".claude", "../outside") // exec-config, symlink and aliased at once
	s.gitlink("vendor/lib")
	s.put(".envrc", "x\n")
	s.put("src/ok.go", "package main\n")
	s.commit("work\n")
	s.remove(".envrc")
	s.commit("drop one again\n")
	s.finish()
	return s
}

func okSignOff(r *Review) SignOff {
	var paths []string
	for _, f := range r.Flagged {
		if !slices.Contains(paths, f.Path) {
			paths = append(paths, f.Path)
		}
	}
	return SignOff{Tip: r.Tip, Digest: r.Digest(), Change: true, Paths: paths}
}

func signOffErr(t *testing.T, r *Review, so SignOff) *SignOffError {
	t.Helper()
	err := r.CheckSignOff(so)
	var se *SignOffError
	if !errors.As(err, &se) {
		t.Fatalf("CheckSignOff = %v, want a *SignOffError", err)
	}
	return se
}

func TestCheckSignOff(t *testing.T) {
	s := flaggedScene(t)
	r := s.inspect()
	for _, kind := range []FlagKind{FlagExecConfig, FlagSymlink, FlagSubmodule, FlagAliased, FlagHistory} {
		if !hasFlag(r.Flagged, kind) {
			t.Fatalf("the scene has no %s flag: %+v", kind, r.Flagged)
		}
	}
	full := okSignOff(r)
	if err := r.CheckSignOff(full); err != nil {
		t.Fatalf("a full sign-off: %v", err)
	}
	// A path that is flagged for two kinds is accepted by one entry.
	two := 0
	for i := 1; i < len(r.Flagged); i++ {
		if r.Flagged[i].Path == r.Flagged[i-1].Path {
			two++
		}
	}
	if two == 0 {
		t.Fatal("the scene has no path with two flags")
	}
	if len(full.Paths) >= len(r.Flagged) {
		t.Errorf("%d entries for %d flags", len(full.Paths), len(r.Flagged))
	}

	// Each wrong part alone.
	so := full
	so.Change = false
	if se := signOffErr(t, r, so); !se.ChangeMissing || se.TipMismatch || se.DigestMismatch || len(se.Missing) != 0 || len(se.Unknown) != 0 {
		t.Errorf("change not accepted: %+v", se)
	}
	so = full
	so.Tip = strings.Repeat("0", 40)
	if se := signOffErr(t, r, so); !se.TipMismatch || se.DigestMismatch || se.ChangeMissing {
		t.Errorf("other tip: %+v", se)
	}
	so = full
	so.Tip = r.Tip[:12] // a short id is not the tip
	if se := signOffErr(t, r, so); !se.TipMismatch {
		t.Errorf("short tip: %+v", se)
	}
	so = full
	so.Digest = strings.Repeat("0", 64)
	if se := signOffErr(t, r, so); !se.DigestMismatch || se.TipMismatch {
		t.Errorf("other digest: %+v", se)
	}
	so = full
	so.Digest = ""
	if se := signOffErr(t, r, so); !se.DigestMismatch {
		t.Errorf("empty digest: %+v", se)
	}

	// All but one path: the error names it, once for each of its flags.
	for _, drop := range full.Paths {
		so = full
		so.Paths = slices.DeleteFunc(slices.Clone(full.Paths), func(p string) bool { return p == drop })
		se := signOffErr(t, r, so)
		if len(se.Missing) == 0 || len(se.Unknown) != 0 || se.TipMismatch || se.DigestMismatch || se.ChangeMissing {
			t.Errorf("without %q: %+v", drop, se)
		}
		for _, f := range se.Missing {
			if f.Path != drop {
				t.Errorf("without %q the error misses %q", drop, f.Path)
			}
		}
		if !strings.Contains(se.Error(), drop) {
			t.Errorf("without %q the message lacks it: %v", drop, se)
		}
	}

	// An unknown extra path, also a near miss of a flagged one.
	so = full
	so.Paths = append(slices.Clone(full.Paths), "src/ok.go", "Makefile/", "makefile", "src/ok.go")
	se := signOffErr(t, r, so)
	if !slices.Equal(se.Unknown, []string{"Makefile/", "makefile", "src/ok.go"}) || len(se.Missing) != 0 {
		t.Errorf("unknown paths: %+v", se)
	}

	// Everything wrong at once.
	se = signOffErr(t, r, SignOff{Tip: "x", Digest: "y"})
	if !se.TipMismatch || !se.DigestMismatch || !se.ChangeMissing || len(se.Missing) != len(r.Flagged) {
		t.Errorf("all wrong: %+v", se)
	}
	for _, want := range []string{"another tip", "another review", "change as a whole", "not accepted", "Makefile"} {
		if !strings.Contains(se.Error(), want) {
			t.Errorf("message lacks %q: %v", want, se)
		}
	}
}

// TestCheckSignOffNoFlags: a review with no flag needs the change, the tip and
// the digest, and an empty list; any path is unknown.
func TestCheckSignOffNoFlags(t *testing.T) {
	s := newScene(t)
	s.put("src/a.go", "package main\n")
	s.commit("work\n")
	s.finish()
	r := s.inspect()
	if len(r.Flagged) != 0 {
		t.Fatalf("Flagged: %+v", r.Flagged)
	}
	so := SignOff{Tip: r.Tip, Digest: r.Digest(), Change: true}
	if err := r.CheckSignOff(so); err != nil {
		t.Error(err)
	}
	so.Paths = []string{"src/a.go"}
	if se := signOffErr(t, r, so); len(se.Unknown) != 1 {
		t.Errorf("%+v", se)
	}
}

// TestCheckSignOffHostilePaths: the paths are compared as raw bytes, and the
// message escapes them and names no more than a few.
func TestCheckSignOffHostilePaths(t *testing.T) {
	s := newScene(t)
	for _, p := range []string{".envrc", "a/.envrc", "b/.envrc", "c/.envrc", "d/.envrc", "e/.envrc", "f/.envrc", "g/.envrc", "h/.envrc", "i/.envrc"} {
		s.put(p, "x\n")
	}
	s.put("sub\x1b[31m/.mcp.json", "{}\n")
	s.commit("work\n")
	s.finish()
	r := s.inspect()
	so := SignOff{Tip: r.Tip, Digest: r.Digest(), Change: true}
	se := signOffErr(t, r, so)
	msg := se.Error()
	if strings.ContainsAny(msg, "\x1b\n") {
		t.Errorf("the message holds raw lead text: %q", msg)
	}
	if !strings.Contains(msg, "and 3 more") {
		t.Errorf("the message lists too many paths: %q", msg)
	}
	so.Paths = []string{"sub/.mcp.json"} // the path without its escape bytes
	if se := signOffErr(t, r, so); !slices.Equal(se.Unknown, []string{"sub/.mcp.json"}) {
		t.Errorf("%+v", se)
	}
	for _, f := range r.Flagged {
		so.Paths = append(so.Paths, f.Path)
	}
	if err := r.CheckSignOff(so); err == nil {
		t.Error("one unknown path was accepted")
	}
	so.Paths = so.Paths[1:]
	if err := r.CheckSignOff(so); err != nil {
		t.Errorf("exact bytes: %v", err)
	}
}
