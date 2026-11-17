package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/exchange/boundedexec"
)

// TestPathRulesMustMatchBetweenReviewAndIntegrate: a project rule adds a
// path to the sign-off, it changes the digest, and integrate with another
// list is refused.
func TestPathRulesMustMatchBetweenReviewAndIntegrate(t *testing.T) {
	w := newWorld(t)
	w.runner = &fakeRunner{res: boundedexec.Result{Stdout: []byte("ok\n")}}
	w.initMirror()
	w.lead(testRole)
	w.branch(testRole, "gen")
	w.write(testRole, map[string]string{"docs/gen/out.txt": "x\n", "plain.txt": "y\n"})
	tip := w.commit(testRole, "generated docs")
	w.fetch(testRole)

	plain := w.review(testRole, "gen", "main")
	if strings.Contains(plain.text, "path: docs/gen/out.txt") {
		t.Fatalf("flagged without a rule:\n%s", plain.text)
	}
	ruled := w.review(testRole, "gen", "main", "--path-rule", "docs/gen/")
	if ruled.tip != tip || ruled.tip != plain.tip {
		t.Fatalf("tips differ")
	}
	if ruled.digest == plain.digest {
		t.Fatal("the digest does not cover the path rules")
	}
	for _, want := range []string{
		"Path rules added by you (give the same list to integrate):",
		"  docs/gen/",
		"  path: docs/gen/out.txt",
		"exec-config: project addition",
		"to accept it, add: --accept-path 'docs/gen/out.txt'",
		"Give integrate the same --path-rule list.",
	} {
		if !strings.Contains(ruled.text, want) {
			t.Fatalf("review lacks %q:\n%s", want, ruled.text)
		}
	}
	w.mustOK(w.sub("verify", w.verifyArgs(testRole, "gen", "true")...))
	before := w.refs()

	// The digest of the ruled review, but the rule is not given.
	code, _, errb := w.sub("integrate", w.integrateArgs(testRole, "gen", "main", ruled)...)
	if code != exitRefused || !strings.Contains(errb, "moved") {
		t.Fatalf("without the rule: exit %d\n%s", code, errb)
	}
	// The digest of the plain review, but a rule is given.
	code, _, errb = w.sub("integrate", append(w.integrateArgs(testRole, "gen", "main", plain), "--path-rule", "docs/gen/")...)
	if code != exitRefused || !strings.Contains(errb, "moved") {
		t.Fatalf("with a rule the review did not have: exit %d\n%s", code, errb)
	}
	// Another list than the one reviewed.
	code, _, errb = w.sub("integrate", append(w.integrateArgs(testRole, "gen", "main", ruled, "docs/gen/out.txt"), "--path-rule", "docs/")...)
	if code != exitRefused {
		t.Fatalf("with another rule: exit %d\n%s", code, errb)
	}
	if w.refs() != before {
		t.Fatal("a refusal changed refs")
	}
	// The same list: the path still needs its own sign-off, then it goes.
	same := append(w.integrateArgs(testRole, "gen", "main", ruled), "--path-rule", "docs/gen/")
	code, _, errb = w.sub("integrate", same...)
	if code != exitRefused || !strings.Contains(errb, "add --accept-path 'docs/gen/out.txt'") {
		t.Fatalf("without the path: exit %d\n%s", code, errb)
	}
	same = append(w.integrateArgs(testRole, "gen", "main", ruled, "docs/gen/out.txt"), "--path-rule", "docs/gen/")
	w.mustOK(w.sub("integrate", same...))
	if got := refLines(w.refs())["refs/heads/main"]; got != tip {
		t.Fatalf("main is at %s, want %s", got, tip)
	}
}

// TestRecordsAndWorkFoldersAreOutsideTheState: a lead can read or write
// folders of the state root, so the owner's folders must not be there.
func TestRecordsAndWorkFoldersAreOutsideTheState(t *testing.T) {
	w := newWorld(t)
	fr := &fakeRunner{}
	w.runner = fr
	w.initMirror()
	w.lead(testRole)
	w.fetch(testRole)
	tmpl := w.template()
	w.loads = nil

	inside := filepath.Join(w.state, "records")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	insideDeep := filepath.Join(inside, "deep")
	if err := os.Mkdir(insideDeep, 0o700); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(w.other, "via-link")
	if err := os.Symlink(inside, viaLink); err != nil {
		t.Fatal(err)
	}
	viaParentLink := filepath.Join(w.other, "state-link")
	if err := os.Symlink(w.state, viaParentLink); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(w.other, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"the state root":          w.state,
		"a folder in the state":   inside,
		"a deep folder":           insideDeep,
		"a symlink into it":       viaLink,
		"a path through a link":   filepath.Join(viaParentLink, "records"),
		"a relative path":         "records",
		"an unclean path":         w.other + "/../other",
		"a folder that is absent": filepath.Join(w.other, "absent"),
		"a file":                  file,
	}
	verifyWith := func(records, work string) (int, string, string) {
		return w.sub("verify", "--role", testRole, "--branch", "main", "--gate", "a=true",
			"--sandbox", tmpl, "--records", records, "--work", work)
	}
	integrateWith := func(records string) (int, string, string) {
		return w.sub("integrate", "--role", testRole, "--branch", "main", "--target", "main",
			"--tip", strings.Repeat("a", 40), "--digest", strings.Repeat("a", 64), "--accept-change", "--records", records)
	}
	for name, dir := range bad {
		for what, call := range map[string]func() (int, string, string){
			"verify records":    func() (int, string, string) { return verifyWith(dir, w.work) },
			"verify work":       func() (int, string, string) { return verifyWith(w.records, dir) },
			"integrate records": func() (int, string, string) { return integrateWith(dir) },
		} {
			code, out, errb := call()
			if code != exitUsage || out != "" || errb == "" {
				t.Errorf("%s as %s: exit %d\n%s\n%s", name, what, code, out, errb)
			}
		}
	}
	if fr.calls != 0 || len(w.loads) != 0 || len(w.planner.VerifyRequests()) != 0 {
		t.Error("a refused folder still started a run")
	}
	// The state folders stay as they were: nothing was written into the
	// folders that were refused.
	for _, d := range []string{inside, insideDeep} {
		if es, _ := os.ReadDir(d); len(es) > 1 {
			t.Errorf("%s was written: %v", d, es)
		}
	}
}
