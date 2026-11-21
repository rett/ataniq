package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPin = PendingPrefix + "k1"

// mergeMirror is a mirror that also holds a host-built merge under a pin:
// root is the commit of newMirror, target a commit on it (the target's tip
// the merge was built against), side a commit on root, and merge the merge of
// side into target (target first).
type mergeMirror struct {
	mirror
	root, target, side, merge string
}

func commitFile(t *testing.T, work, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "--quiet", "-m", name)
	return git(t, work, "rev-parse", "HEAD")
}

func newMergeMirror(t *testing.T) mergeMirror {
	t.Helper()
	m := newMirror(t)
	mm := mergeMirror{mirror: m, root: m.Tip}
	w := m.Work
	mm.target = commitFile(t, w, "target.txt")
	git(t, w, "checkout", "--quiet", "-b", "side", mm.root)
	mm.side = commitFile(t, w, "side.txt")
	git(t, w, "checkout", "--quiet", "-b", "built", mm.target)
	git(t, w, "merge", "--quiet", "--no-ff", "-m", "merge", mm.side)
	mm.merge = git(t, w, "rev-parse", "HEAD")
	git(t, w, "push", "--quiet", m.Path, "HEAD:"+testPin)
	return mm
}

// pin pushes the commit under a new pin and returns the pin's name.
func (m mergeMirror) pin(t *testing.T, name, id string) string {
	t.Helper()
	ref := PendingPrefix + name
	git(t, m.Work, "push", "--quiet", m.Path, id+":"+ref)
	return ref
}

var oneGate = []Gate{{Name: "one", Argv: []string{"/bin/sh", "-c", "echo ran"}}}

func (m mergeMirror) run(t *testing.T, ref, tip, base string) (string, int) {
	t.Helper()
	script, err := MergeScript(ref, tip, base, m.Path, t.TempDir(), oneGate)
	if err != nil {
		t.Fatal(err)
	}
	return runScript(t, script)
}

func TestMergeScriptGolden(t *testing.T) {
	tip, base := fullTip, strings.Repeat("ef", 20)
	got, err := MergeScript(testPin, tip, base, "/in/mirror.git", "/in/scratch", []Gate{{Name: "lint", Argv: []string{"make", "lint"}}})
	if err != nil {
		t.Fatal(err)
	}
	lead, err := Script(testRef, tip, "/in/mirror.git", "/in/scratch", []Gate{{Name: "lint", Argv: []string{"make", "lint"}}})
	if err != nil {
		t.Fatal(err)
	}
	check := "set -- $(git rev-list --parents -n 1 refs/verify/tip 2>/dev/null || true)\n" +
		"[ \"$#\" -eq 3 ] && [ \"$2\" = '" + base + "' ] || { echo 'ataniq-verify: the tip is not a merge whose first parent is the base'; exit 93; }\n"
	want := strings.Replace(lead, "refs/leads/lead/heads/main", testPin, 1)
	want = strings.Replace(want, "git checkout --quiet --detach", check+"git checkout --quiet --detach", 1)
	if got != want {
		t.Fatalf("script differs\n--- got\n%s\n--- want\n%s", got, want)
	}
	if strings.Contains(lead, "rev-list") {
		t.Errorf("the lead-mode script checks parents:\n%s", lead)
	}
}

func TestMergeScriptRunsGatesOnAMergeOfTheBase(t *testing.T) {
	m := newMergeMirror(t)
	out, exit := m.run(t, testPin, m.merge, m.target)
	if exit != 0 || !strings.Contains(out, "ran\n") {
		t.Fatalf("exit %d\n%s", exit, out)
	}
}

// TestMergeScriptFailsClosedWhenTheTipIsNotAMergeOnTheBase: each shape that is
// not "two parents, the first the base" ends with exit 93 before any gate.
func TestMergeScriptFailsClosedWhenTheTipIsNotAMergeOnTheBase(t *testing.T) {
	m := newMergeMirror(t)
	w := m.Work
	// The same two parents, the other way round: side first.
	git(t, w, "checkout", "--quiet", "-b", "swapped", m.side)
	git(t, w, "merge", "--quiet", "--no-ff", "-m", "swapped", m.target)
	swapped := git(t, w, "rev-parse", "HEAD")
	// Three parents.
	third := func() string {
		git(t, w, "checkout", "--quiet", "-b", "third", m.root)
		return commitFile(t, w, "third.txt")
	}()
	git(t, w, "checkout", "--quiet", "-b", "octopus", m.target)
	git(t, w, "merge", "--quiet", "--no-ff", "-m", "octopus", m.side, third)
	octopus := git(t, w, "rev-parse", "HEAD")
	if n := len(strings.Fields(git(t, w, "rev-list", "--parents", "-n", "1", octopus))) - 1; n != 3 {
		t.Fatalf("the octopus has %d parents", n)
	}
	// A root commit.
	git(t, w, "checkout", "--quiet", "--orphan", "orphan")
	git(t, w, "rm", "-rf", "--quiet", ".")
	rootless := commitFile(t, w, "orphan.txt")

	cases := []struct {
		name, tip, base string
	}{
		{"first parent is not the base (the other parent)", m.merge, m.side},
		{"first parent is not the base (an ancestor)", m.merge, m.root},
		{"parents swapped", swapped, m.target},
		{"one parent", m.target, m.root},
		{"three parents", octopus, m.target},
		{"no parent", rootless, m.target},
		{"base is the merge itself", m.merge, m.merge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref := m.pin(t, strings.NewReplacer(" ", "-", "(", "", ")", "").Replace(c.name), c.tip)
			out, exit := m.run(t, ref, c.tip, c.base)
			if exit != ExitNotMerge {
				t.Fatalf("exit %d, want %d\n%s", exit, ExitNotMerge, out)
			}
			if strings.Contains(out, "ran") || strings.Contains(out, MarkerPrefix) {
				t.Fatalf("a gate ran:\n%s", out)
			}
			if want := "ataniq-verify: the tip is not a merge whose first parent is the base\n"; out != want {
				t.Fatalf("output %q want %q", out, want)
			}
		})
	}
	// The same script of lead mode runs a one-parent commit: the parent
	// check is the merge mode's.
	script, err := Script(testRef, m.Tip, m.Path, t.TempDir(), oneGate)
	if err != nil {
		t.Fatal(err)
	}
	if _, exit := runScript(t, script); exit != 0 {
		t.Errorf("lead mode, exit %d", exit)
	}
}

// TestMergeScriptKeepsTheTipCheck: a pin that moved is still exit 90, before
// the parents are looked at.
func TestMergeScriptKeepsTheTipCheck(t *testing.T) {
	m := newMergeMirror(t)
	out, exit := m.run(t, testPin, m.target, m.root) // the pin is at the merge
	if exit != ExitTipMismatch {
		t.Fatalf("exit %d\n%s", exit, out)
	}
}

// TestTipRefByMode: a pin only in merge mode, a lead ref only in lead mode.
func TestTipRefByMode(t *testing.T) {
	base := strings.Repeat("ef", 20)
	good := []Gate{{Name: "g", Argv: []string{"true"}}}
	if _, err := Script(testRef, fullTip, "/m", "/s", good); err != nil {
		t.Errorf("lead ref in lead mode: %v", err)
	}
	if _, err := MergeScript(testPin, fullTip, base, "/m", "/s", good); err != nil {
		t.Errorf("pin in merge mode: %v", err)
	}
	if _, err := Script(testPin, fullTip, "/m", "/s", good); err == nil {
		t.Errorf("a pin in lead mode")
	}
	if _, err := MergeScript(testRef, fullTip, base, "/m", "/s", good); err == nil {
		t.Errorf("a lead ref in merge mode")
	}
	for name, ref := range map[string]string{
		"the prefix alone":    PendingPrefix,
		"another namespace":   "refs/heads/main",
		"a bad name":          PendingPrefix + "a b",
		"a dot dot":           PendingPrefix + "a..b",
		"a lock suffix":       PendingPrefix + "k.lock",
		"a leads ref beneath": PendingPrefix + "../leads/x",
	} {
		if _, err := MergeScript(ref, fullTip, base, "/m", "/s", good); err == nil {
			t.Errorf("merge mode accepted %s", name)
		}
	}
	// Base: required and well formed in merge mode, absent in lead mode.
	for name, b := range map[string]string{
		"no base":         "",
		"short base":      "abc",
		"upper-case":      strings.Repeat("EF", 20),
		"other format":    strings.Repeat("ef", 32),
		"base with quote": strings.Repeat("e", 39) + "'",
	} {
		if _, err := MergeScript(testPin, fullTip, b, "/m", "/s", good); err == nil {
			t.Errorf("merge mode accepted %s", name)
		}
	}
	if err := checkBase(false, fullTip, base); err == nil {
		t.Errorf("lead mode accepted a base")
	}
	if err := checkBase(false, fullTip, ""); err != nil {
		t.Errorf("lead mode without a base: %v", err)
	}
}
