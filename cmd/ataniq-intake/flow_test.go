package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// refLines splits the output of for-each-ref into a map.
func refLines(s string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if name, id, ok := strings.Cut(l, " "); ok {
			m[name] = id
		}
	}
	return m
}

func TestInitCreatesOnlyTheMirror(t *testing.T) {
	w := newWorld(t)
	out := w.mustOK(w.sub("init"))
	if !strings.Contains(out, "Created the exchange mirror") || !strings.Contains(out, w.mirrorPath()) {
		t.Fatalf("output:\n%s", out)
	}
	fi, err := os.Stat(filepath.Join(w.state, "mirrors"))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("mirrors folder: %v %v", fi, err)
	}
	var names []string
	es, _ := os.ReadDir(w.state)
	for _, e := range es {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "mirrors" {
		t.Fatalf("init created %v in the state root", names)
	}
	// The second init refuses and changes nothing.
	code, _, errb := w.sub("init")
	if code != exitRefused || !strings.Contains(errb, "exists already") {
		t.Fatalf("second init: %d %s", code, errb)
	}
}

func TestInitKeepsAnExistingMirrorsFolder(t *testing.T) {
	w := newWorld(t)
	if err := os.Mkdir(filepath.Join(w.state, "mirrors"), 0o700); err != nil {
		t.Fatal(err)
	}
	w.initMirror()
}

func TestCommandsBeforeInitPointToInit(t *testing.T) {
	w := newWorld(t)
	code, out, errb := w.sub("branches")
	if code != exitRefused || out != "" || !strings.Contains(errb, "init") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
}

// TestWholeFlow is the reviewed intake from init to the moved branch.
func TestWholeFlow(t *testing.T) {
	w := newWorld(t)
	w.initMirror()
	base := w.lead(testRole)
	w.branch(testRole, "feature")
	w.write(testRole, map[string]string{"feature.txt": "feature\n", ".claude/settings.json": "{}\n"})
	tip := w.commit(testRole, "add a feature")

	// Fetch.
	out := w.fetch(testRole)
	for _, want := range []string{
		"Lead object store (the lead's own statement): 1000 bytes",
		"created refs/leads/lead/heads/feature " + tip,
		"created refs/leads/lead/heads/main " + base,
		"unverified lead data",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("fetch output lacks %q:\n%s", want, out)
		}
	}
	if reqs := w.planner.ServeRequests(); len(reqs) != 1 || reqs[0].Tree != w.tree(testRole) || reqs[0].Role != testRole {
		t.Fatalf("serve requests: %+v", reqs)
	}

	// Branches.
	out = w.mustOK(w.sub("branches"))
	for _, want := range []string{"  lead  feature  " + tip, "  lead  main  " + base, "Upstream branches:", "  main  " + base} {
		if !strings.Contains(out, want) {
			t.Fatalf("branches output lacks %q:\n%s", want, out)
		}
	}
	out = w.mustOK(w.sub("branches", "--role", "nobody"))
	if !strings.Contains(out, "  (none)") {
		t.Fatalf("branches of a role without refs:\n%s", out)
	}

	// Review.
	rev := w.review(testRole, "feature", "main", "--diff")
	if rev.tip != tip {
		t.Fatalf("tip %s, want %s", rev.tip, tip)
	}
	for _, want := range []string{
		"Base: " + base,
		"  " + tip, "add a feature",
		"  A  000000  100644  feature.txt",
		"  A  000000  100644  .claude/settings.json",
		"Needs your sign-off (unverified lead data):",
		"  path: .claude/settings.json",
		"exec-config: ",
		"to accept it, add: --accept-path '.claude/settings.json'",
		"To sign off the change as a whole, add: --accept-change",
		"+feature",
	} {
		if !strings.Contains(rev.text, want) {
			t.Fatalf("review output lacks %q:\n%s", want, rev.text)
		}
	}
	if strings.Contains(rev.text, "path: feature.txt") {
		t.Fatalf("an ordinary path is listed for sign-off:\n%s", rev.text)
	}
	if got := acceptArgs(t, rev.text); len(got) != 1 || got[0] != ".claude/settings.json" {
		t.Fatalf("accept args: %q", got)
	}

	before := refLines(w.refs())
	integrate := func(args ...string) (int, string, string) { return w.sub("integrate", args...) }

	// Refused without the sign-off, and the message says what to add.
	code, out, errb := integrate(w.integrateArgs(testRole, "feature", "main", rev)...)
	if code != exitRefused || out != "" {
		t.Fatalf("without the path: exit %d\n%s\n%s", code, out, errb)
	}
	if !strings.Contains(errb, "add --accept-path '.claude/settings.json'") || !strings.Contains(errb, "Nothing was moved") {
		t.Fatalf("stderr:\n%s", errb)
	}
	code, _, errb = integrate("--role", testRole, "--branch", "feature", "--target", "main",
		"--tip", rev.tip, "--digest", rev.digest, "--records", w.records)
	if code != exitRefused || !strings.Contains(errb, "add --accept-change") || !strings.Contains(errb, "add --accept-path '.claude/settings.json'") {
		t.Fatalf("without any sign-off: exit %d\n%s", code, errb)
	}
	// An accepted path that is not flagged is a stale list.
	code, _, errb = integrate(w.integrateArgs(testRole, "feature", "main", rev, ".claude/settings.json", "feature.txt")...)
	if code != exitRefused || !strings.Contains(errb, "remove --accept-path 'feature.txt'") {
		t.Fatalf("stale path: exit %d\n%s", code, errb)
	}

	// Refused without a verification.
	signed := w.integrateArgs(testRole, "feature", "main", rev, ".claude/settings.json")
	code, out, errb = integrate(signed...)
	if code != exitRefused || out != "" || !strings.Contains(errb, "no passing verification") || !strings.Contains(errb, "verify") {
		t.Fatalf("not verified: exit %d\n%s\n%s", code, out, errb)
	}

	// A failing gate: exit 1, a stored failure, and still no integration.
	code, out, errb = w.sub("verify", w.verifyArgs(testRole, "feature", "echo failing; exit 3")...)
	if code != exitRefused || !strings.Contains(out, "Outcome: fail") || !strings.Contains(out, "Exit status: 3") ||
		!strings.Contains(out, "  | failing") {
		t.Fatalf("failing gate: exit %d\n%s\n%s", code, out, errb)
	}
	recs, err := w.store().Verifications(tip)
	if err != nil || len(recs) != 1 || recs[0].Passed || recs[0].Exit != 3 || recs[0].Project != testProject {
		t.Fatalf("records after a failure: %+v %v", recs, err)
	}
	if code, _, _ = integrate(signed...); code != exitRefused {
		t.Fatalf("integrate after a failed verification: %d", code)
	}

	// A passing gate. It runs in a scratch clone of the tip, in the fake
	// sandbox.
	out = w.mustOK(w.sub("verify", w.verifyArgs(testRole, "feature", "test -f feature.txt && echo checked")...))
	for _, want := range []string{"Outcome: pass", "Exit status: 0", "Tip: " + tip, "  | " + "ataniq-verify: gate check", "  | checked"} {
		if !strings.Contains(out, want) {
			t.Fatalf("passing gate output lacks %q:\n%s", want, out)
		}
	}
	recs, err = w.store().Verifications(tip)
	if err != nil || len(recs) != 2 || !recs[1].Passed {
		t.Fatalf("records after a pass: %+v %v", recs, err)
	}
	if entries, _ := os.ReadDir(w.work); len(entries) != 0 {
		t.Fatalf("the work folder is not empty: %v", entries)
	}

	// A wrong tip or digest is refused as moved, and nothing changes.
	wrongTip := rev
	wrongTip.tip = base
	code, _, errb = integrate(w.integrateArgs(testRole, "feature", "main", wrongTip, ".claude/settings.json")...)
	if code != exitRefused || !strings.Contains(errb, "moved") || !strings.Contains(errb, "review") {
		t.Fatalf("wrong tip: exit %d\n%s", code, errb)
	}
	wrongDigest := rev
	wrongDigest.digest = strings.Repeat("0", 64)
	code, _, errb = integrate(w.integrateArgs(testRole, "feature", "main", wrongDigest, ".claude/settings.json")...)
	if code != exitRefused || !strings.Contains(errb, "moved") {
		t.Fatalf("wrong digest: exit %d\n%s", code, errb)
	}
	if got := refLines(w.refs()); !equalMaps(got, before) {
		t.Fatalf("a refused integration changed refs:\nbefore %v\nafter  %v", before, got)
	}

	// Accepted.
	out = w.mustOK(integrate(signed...))
	for _, want := range []string{
		"Integrated: the upstream branch moved, fast-forward only.",
		"Old: " + base, "New: " + tip,
		"Review digest: " + rev.digest,
		"  .claude/settings.json",
		"Your working checkout was not touched. It gets the commits with its own fetch from the mirror at " + w.mirrorPath(),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("integrate output lacks %q:\n%s", want, out)
		}
	}
	after := refLines(w.refs())
	if after["refs/heads/main"] != tip {
		t.Fatalf("main is at %s, want %s", after["refs/heads/main"], tip)
	}
	delete(after, "refs/heads/main")
	delete(before, "refs/heads/main")
	if !equalMaps(after, before) {
		t.Fatalf("integration changed other refs:\nbefore %v\nafter  %v", before, after)
	}
	w.mirrorGit("merge-base", "--is-ancestor", base, tip)
	intakes, err := w.store().Intakes()
	if err != nil || len(intakes) != 1 || intakes[0].Tip != tip || intakes[0].Base != base || !intakes[0].Time.Equal(w.now) {
		t.Fatalf("intake records: %+v %v", intakes, err)
	}

	// The same integration again: the upstream branch is at the tip already
	// and the package refuses.
	code, _, errb = integrate(signed...)
	if code == exitOK || !strings.Contains(errb, "nothing to integrate") {
		t.Fatalf("a repeat: exit %d\n%s", code, errb)
	}
	if got := refLines(w.refs()); got["refs/heads/main"] != tip {
		t.Fatalf("main moved on a repeat: %v", got)
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if b[k] != a[k] {
			return false
		}
	}
	return true
}
