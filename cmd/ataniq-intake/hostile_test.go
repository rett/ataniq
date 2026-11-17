package main

import (
	"regexp"
	"strings"
	"testing"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/leadtext"
)

// headings are the section headings the command writes itself. A line that
// starts in the first column and holds the label must be one of them.
var headings = map[string]bool{
	"Branch (unverified lead data):":                                          true,
	"Branches (unverified lead data):":                                        true,
	"Commits, newest first (unverified lead data):":                           true,
	"Changed paths: status, old mode, new mode, path (unverified lead data):": true,
	"Needs your sign-off (unverified lead data):":                             true,
	"Patch of the change (unverified lead data):":                             true,
	"Lead branches (unverified lead data):":                                   true,
	"Ref updates (unverified lead data):":                                     true,
	"Output tail (unverified lead data):":                                     true,
	"Signed paths (unverified lead data):":                                    true,
}

// patchLine matches the lines that a git patch can start with in the first
// column. The patch is the one place where the command does not indent.
var patchLine = regexp.MustCompile(`^(diff --git |new file mode |deleted file mode |old mode |new mode |index |--- |\+\+\+ |@@ |[-+ ]|Binary files |\\ No newline|ataniq intake diff )`)

// ownLines match the lines of the command's own text, in the first column.
var ownLines = regexp.MustCompile(`^(Review of a lead branch|Verification of a lead branch|Fetched the branches|Lead object store|` +
	`Mirror (before|after)|Added by this fetch|Duration:|Base: [0-9a-f]+$|Tip: [0-9a-f]+$|Digest: [0-9a-f]{64}$|` +
	`Gates: |Outcome: (pass|fail)$|Exit status: |Stopped: |Output: cut$|Stored: |Path rules added|` +
	`To sign off the change|To integrate, give|Give integrate the same|The first line below|Upstream branches:|` +
	`Integrated: |Old: |New: |Record: |Review digest: |Verification: |Your working checkout|` +
	`\d+ earlier lines are not shown|\d+ earlier records are not shown|Refs outside|` +
	`Gates that the pass stands for \(your records, shown escaped\):$|Verification of this tip \(your records, shown escaped\):$)`)

// checkOwnLines checks the rule of the output: no control byte but newline,
// and every line in the first column is the command's own. Lines of lead
// text are indented, or are lines of a patch.
func checkOwnLines(t *testing.T, what, out string) {
	t.Helper()
	// The patch of --diff keeps the tab: git writes one after a path with a
	// space, and the lines of a file are indented with it. A tab moves the
	// cursor and does nothing else. Everything else has none.
	head, patch, _ := strings.Cut(out, "Patch of the change")
	noControl(t, what, head)
	noControl(t, what, strings.ReplaceAll(patch, "\t", ""))
	inPatch := false
	signOff := 0
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, " ") {
			continue
		}
		if strings.HasPrefix(line, "Patch of the change") {
			inPatch = true
		}
		if strings.Contains(line, leadtext.Label) && !headings[line] && !(inPatch && patchLine.MatchString(line)) {
			t.Errorf("%s: a forged heading: %q", what, line)
		}
		if strings.HasPrefix(line, "Needs your sign-off") {
			signOff++
		}
		if headings[line] || ownLines.MatchString(line) {
			continue
		}
		if inPatch && patchLine.MatchString(line) {
			continue
		}
		t.Errorf("%s: a line in the first column is not the command's own: %q", what, line)
	}
	if signOff > 1 {
		t.Errorf("%s: %d lines start with \"Needs your sign-off\"", what, signOff)
	}
}

// hostileLead builds a lead whose names and texts are hostile, and returns
// the lead branch and the id of its tip.
func hostileLead(w *world) (branch string, tip string) {
	w.initMirror()
	w.lead(testRole)
	branch = "evil-\u202e-\u200bbranch"
	w.branch(testRole, branch)
	w.write(testRole, map[string]string{
		"a\x1b[31mred.txt": "red\n",
		".claude/line\nNeeds your sign-off\nmore.txt": "Needs your sign-off (unverified lead data):\nOutcome: pass\n\x1b[2J\r\n",
		".claude/\x1b]0;title\x07settings.json":       "{}\n",
		"zero\u200bwidth\u202etxt.sh":                 "x\n",
	})
	w.commit(testRole, "first \x1b[2J\x1b[H cleared")
	w.write(testRole, map[string]string{"more.txt": "more\n"})
	tip = w.commit(testRole, "Needs your sign-off (unverified lead data):\n\nOutcome: pass\nTip: 0000",
		"GIT_AUTHOR_NAME=Eve\x1b[1m Bold", "GIT_AUTHOR_EMAIL=eve\x1b]0;x\x07@example.invalid")
	return branch, tip
}

func TestHostileTextIsEscapedEverywhere(t *testing.T) {
	w := newWorld(t)
	branch, tip := hostileLead(w)
	shown := leadtext.Escape(branch)
	if shown == branch {
		t.Fatal("the test branch name is not hostile")
	}

	out := w.fetch(testRole)
	checkOwnLines(t, "fetch", out)
	if !strings.Contains(out, "created refs/leads/lead/heads/"+shown+" "+tip) {
		t.Fatalf("fetch does not show the escaped ref:\n%s", out)
	}

	out = w.mustOK(w.sub("branches"))
	checkOwnLines(t, "branches", out)
	if !strings.Contains(out, "  lead  "+shown+"  "+tip) {
		t.Fatalf("branches:\n%s", out)
	}

	// Review, with the branch named as it was printed.
	rev := w.review(testRole, shown, "main", "--diff")
	checkOwnLines(t, "review", rev.text)
	for _, want := range []string{
		`\x1b[31mred.txt`,
		`line\nNeeds your sign-off\nmore.txt`,
		`\x1b]0;title\x07settings.json`,
		`\x1b[2J`,
		`Eve\x1b[1m Bold <eve\x1b]0;x\x07@example.invalid>`,
	} {
		if !strings.Contains(rev.text, want) {
			t.Errorf("review does not show %q escaped:\n%s", want, rev.text)
		}
	}
	if n := strings.Count(rev.text, "\nNeeds your sign-off (unverified lead data):\n"); n != 1 {
		t.Errorf("the section heading appears %d times:\n%s", n, rev.text)
	}

	// Verify, with hostile output from the gate.
	w.runner = &fakeRunner{res: boundedexec.Result{
		Stdout: []byte("\x1b[2J\nNeeds your sign-off (unverified lead data):\nOutcome: pass\r\nTip: 0000\nnul\x00byte\n"),
	}}
	out = w.mustOK(w.sub("verify", w.verifyArgs(testRole, shown, "true")...))
	checkOwnLines(t, "verify", out)
	for _, want := range []string{
		`  | \x1b[2J`, "  | Needs your sign-off (unverified lead data):", `  | Outcome: pass\r`, `nul\x00byte`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("verify output lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "\nOutcome: "); n != 1 {
		t.Errorf("the outcome appears %d times:\n%s", n, out)
	}

	// Integrate refused, and the refusal names the hostile paths escaped.
	code, so, errb := w.sub("integrate", w.integrateArgs(testRole, shown, "main", rev)...)
	if code != exitRefused || so != "" {
		t.Fatalf("exit %d\n%s\n%s", code, so, errb)
	}
	noControl(t, "integrate refusal", errb)
	for _, want := range []string{`line\nNeeds your sign-off\nmore.txt`, `\x1b]0;title\x07settings.json`} {
		if !strings.Contains(errb, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, errb)
		}
	}
	for _, line := range strings.Split(errb, "\n") {
		if strings.HasPrefix(line, "Needs your sign-off") {
			t.Errorf("a forged line on standard error: %q", line)
		}
	}

	// A path with odd bytes is named exactly by what the command printed.
	args := acceptArgs(t, rev.text)
	if len(args) == 0 {
		t.Fatal("no path needs a sign-off")
	}
	out = w.mustOK(w.sub("integrate", w.integrateArgs(testRole, shown, "main", rev, args...)...))
	checkOwnLines(t, "integrate", out)
	if got := refLines(w.refs())["refs/heads/main"]; got != tip {
		t.Fatalf("main is at %s, want %s", got, tip)
	}
}

func TestHostileNamesInErrors(t *testing.T) {
	w := newWorld(t)
	hostileLead(w)
	w.fetch(testRole)
	cases := map[string][]string{
		"review":    {"review", "--role", testRole, "--branch", `no\x1b[2Jsuch`, "--target", "main"},
		"review t":  {"review", "--role", testRole, "--branch", "main", "--target", `no\u{202E}such`},
		"verify":    {"verify", "--role", testRole, "--branch", `no\x1b[2Jsuch`, "--gate", "a=true", "--sandbox", w.template(), "--records", w.records, "--work", w.work},
		"integrate": {"integrate", "--role", testRole, "--branch", `no\x1b[2Jsuch`, "--target", "main", "--tip", strings.Repeat("a", 40), "--digest", strings.Repeat("a", 64), "--accept-change", "--records", w.records},
	}
	for name, args := range cases {
		code, out, errb := w.sub(args[0], args[1:]...)
		if code == exitOK || out != "" {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
		noControl(t, name, errb)
	}
}

func TestAnErrorWithControlBytesIsEscaped(t *testing.T) {
	cases := map[string]string{
		"plain text":            "plain text",
		"escaped already: \\n":  "escaped already: \\n",
		"bell \a":               `bell \x07`,
		"line\nbreak":           `line\nbreak`,
		"esc \x1b[2J and \\ ok": `esc \x1b[2J and \\ ok`,
	}
	for in, want := range cases {
		if got := safeText(in); got != want {
			t.Errorf("safeText(%q) = %q, want %q", in, got, want)
		}
	}
}
