package verify

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fullTip = "0123456789abcdef0123456789abcdef01234567"

func TestScriptGolden(t *testing.T) {
	got, err := Script(testRef, fullTip, "/in/mirror.git", "/in/scratch", []Gate{
		{Name: "lint", Argv: []string{"make", "lint"}},
		{Name: "unit-2", Argv: []string{"/bin/sh", "-c", "echo it's $HOME"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `set -eu
exec 2>&1
GIT_CONFIG_NOSYSTEM=1
GIT_TERMINAL_PROMPT=0
export GIT_CONFIG_NOSYSTEM GIT_TERMINAL_PROMPT
tree='/in/scratch/tree'
git -c init.defaultBranch=verify init --quiet --template= --object-format=sha1 -- "$tree" || { echo 'ataniq-verify: cannot make the scratch repository'; exit 91; }
cd "$tree" || { echo 'ataniq-verify: cannot enter the scratch repository'; exit 91; }
git fetch --quiet --no-tags -- '/in/mirror.git' '+refs/leads/lead/heads/main:refs/verify/tip' || { echo 'ataniq-verify: cannot fetch the tip ref from the mirror'; exit 91; }
[ "$(git rev-parse --verify --quiet refs/verify/tip)" = '0123456789abcdef0123456789abcdef01234567' ] || { echo 'ataniq-verify: the tip ref does not resolve to the reviewed tip'; exit 90; }
git checkout --quiet --detach refs/verify/tip || { echo 'ataniq-verify: cannot check out the tip'; exit 92; }
unset GIT_CONFIG_NOSYSTEM GIT_TERMINAL_PROMPT
printf '%s\n' 'ataniq-verify: gate lint'
'make' 'lint' || exit $?
printf '%s\n' 'ataniq-verify: gate unit-2'
'/bin/sh' '-c' 'echo it'\''s $HOME' || exit $?
`
	if got != want {
		t.Fatalf("script differs\n--- got\n%s\n--- want\n%s", got, want)
	}
}

func TestScriptSHA256ObjectFormat(t *testing.T) {
	tip := strings.Repeat("ab", 32)
	got, err := Script(testRef, tip, "/m", "/s", []Gate{{Name: "g", Argv: []string{"true"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "--object-format=sha256") || !strings.Contains(got, "'"+tip+"'") {
		t.Fatalf("script: %s", got)
	}
}

func TestScriptRefusals(t *testing.T) {
	ok := []Gate{{Name: "g", Argv: []string{"true"}}}
	long := strings.Repeat("a", 64)
	cases := []struct {
		name          string
		ref, tip      string
		mirror, scrat string
		gates         []Gate
	}{
		{"ref not under leads", "refs/heads/main", fullTip, "/m", "/s", ok},
		{"ref is the prefix", "refs/leads/", fullTip, "/m", "/s", ok},
		{"ref with dots", "refs/leads/a/heads/x..y", fullTip, "/m", "/s", ok},
		{"ref with space", "refs/leads/a/heads/x y", fullTip, "/m", "/s", ok},
		{"ref with newline", "refs/leads/a/heads/x\ny", fullTip, "/m", "/s", ok},
		{"ref with colon", "refs/leads/a/heads/x:y", fullTip, "/m", "/s", ok},
		{"ref with glob", "refs/leads/a/heads/*", fullTip, "/m", "/s", ok},
		{"ref ending in lock", "refs/leads/a/heads/x.lock", fullTip, "/m", "/s", ok},
		{"ref component with dot", "refs/leads/a/.x", fullTip, "/m", "/s", ok},
		{"ref ending in slash", "refs/leads/a/heads/", fullTip, "/m", "/s", ok},
		{"ref with at brace", "refs/leads/a/heads/x@{1}", fullTip, "/m", "/s", ok},
		{"empty ref", "", fullTip, "/m", "/s", ok},
		{"short tip", testRef, "abc123", "/m", "/s", ok},
		{"upper case tip", testRef, strings.ToUpper(fullTip), "/m", "/s", ok},
		{"tip of 50", testRef, strings.Repeat("a", 50), "/m", "/s", ok},
		{"tip with a quote", testRef, long[:39] + "'", "/m", "/s", ok},
		{"relative mirror", testRef, fullTip, "m", "/s", ok},
		{"unclean mirror", testRef, fullTip, "/a/../m", "/s", ok},
		{"mirror with newline", testRef, fullTip, "/m\n", "/s", ok},
		{"root mirror", testRef, fullTip, "/", "/s", ok},
		{"relative scratch", testRef, fullTip, "/m", "s", ok},
		{"scratch with trailing slash", testRef, fullTip, "/m", "/s/", ok},
		{"scratch with NUL", testRef, fullTip, "/m", "/s\x00", ok},
		{"no gates", testRef, fullTip, "/m", "/s", nil},
		{"bad gate name", testRef, fullTip, "/m", "/s", []Gate{{Name: "Lint", Argv: []string{"x"}}}},
		{"empty gate name", testRef, fullTip, "/m", "/s", []Gate{{Name: "", Argv: []string{"x"}}}},
		{"gate name with space", testRef, fullTip, "/m", "/s", []Gate{{Name: "a b", Argv: []string{"x"}}}},
		{"leading dash name", testRef, fullTip, "/m", "/s", []Gate{{Name: "-a", Argv: []string{"x"}}}},
		{"long gate name", testRef, fullTip, "/m", "/s", []Gate{{Name: long, Argv: []string{"x"}}}},
		{"empty argv", testRef, fullTip, "/m", "/s", []Gate{{Name: "g"}}},
		{"empty command", testRef, fullTip, "/m", "/s", []Gate{{Name: "g", Argv: []string{""}}}},
		{"NUL in argument", testRef, fullTip, "/m", "/s", []Gate{{Name: "g", Argv: []string{"x", "a\x00b"}}}},
		{"newline in argument", testRef, fullTip, "/m", "/s", []Gate{{Name: "g", Argv: []string{"x", "a\nb"}}}},
		{"repeated name", testRef, fullTip, "/m", "/s", []Gate{{Name: "g", Argv: []string{"x"}}, {Name: "g", Argv: []string{"y"}}}},
	}
	for _, c := range cases {
		if s, err := Script(c.ref, c.tip, c.mirror, c.scrat, c.gates); err == nil {
			t.Errorf("%s: accepted:\n%s", c.name, s)
		}
	}
}

// scriptEnv is the environment of the unsandboxed script runs.
func scriptEnv() []string { return gitEnv() }

// runScript runs the script as the sandbox would, without a sandbox, and
// returns the combined output and the exit status.
func runScript(t *testing.T, script string) (string, int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = scriptEnv()
	cmd.Dir = "/"
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &ee):
		return out.String(), ee.ExitCode()
	}
	t.Fatal(err)
	return "", -1
}

func TestScriptRunsGatesInOrderFromTheRoot(t *testing.T) {
	m := newMirror(t)
	scratch := t.TempDir()
	script, err := Script(testRef, m.Tip, m.Path, scratch, []Gate{
		{Name: "files", Argv: []string{"/bin/sh", "-c", "pwd && cat hello.txt && ./run.sh"}},
		{Name: "second", Argv: []string{"/bin/sh", "-c", "git rev-parse HEAD && git status --porcelain && echo clean"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, exit := runScript(t, script)
	if exit != 0 {
		t.Fatalf("exit %d\n%s", exit, out)
	}
	want := MarkerPrefix + "files\n" + filepath.Join(scratch, "tree") + "\nhello\nran\n" +
		MarkerPrefix + "second\n" + m.Tip + "\nclean\n"
	if out != want {
		t.Fatalf("output\n%q\nwant\n%q", out, want)
	}
	// The scratch repository has no alternates, no tags and no hardlinks to the mirror.
	if _, err := os.Stat(filepath.Join(scratch, "tree", ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("alternates: %v", err)
	}
}

func TestScriptGateArgumentIsOneWord(t *testing.T) {
	m := newMirror(t)
	scratch := t.TempDir()
	hostile := `a'b "c" $(touch pwned) ` + "`touch pwned2` ;touch pwned3 $HOME *"
	script, err := Script(testRef, m.Tip, m.Path, scratch, []Gate{
		{Name: "words", Argv: []string{"/bin/sh", "-c", `printf '[%s]' "$#" "$1"; test ! -e pwned -a ! -e pwned2 -a ! -e pwned3`, "arg0", hostile}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, exit := runScript(t, script)
	if exit != 0 {
		t.Fatalf("exit %d\n%s", exit, out)
	}
	if want := MarkerPrefix + "words\n[1][" + hostile + "]"; out != want {
		t.Fatalf("output %q want %q", out, want)
	}
}

func TestScriptStopsAtFirstFailingGate(t *testing.T) {
	m := newMirror(t)
	scratch := t.TempDir()
	script, err := Script(testRef, m.Tip, m.Path, scratch, []Gate{
		{Name: "one", Argv: []string{"true"}},
		{Name: "two", Argv: []string{"/bin/sh", "-c", "echo failing; exit 7"}},
		{Name: "three", Argv: []string{"/bin/sh", "-c", "echo never"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, exit := runScript(t, script)
	if exit != 7 {
		t.Fatalf("exit %d\n%s", exit, out)
	}
	if want := MarkerPrefix + "one\n" + MarkerPrefix + "two\nfailing\n"; out != want {
		t.Fatalf("output %q want %q", out, want)
	}
}

func TestScriptTipMismatch(t *testing.T) {
	m := newMirror(t)
	moved := m.advance(t)
	scratch := t.TempDir()
	// The review was made for the first commit; the ref has moved on.
	script, err := Script(testRef, m.Tip, m.Path, scratch, []Gate{{Name: "one", Argv: []string{"/bin/sh", "-c", "echo ran"}}})
	if err != nil {
		t.Fatal(err)
	}
	out, exit := runScript(t, script)
	if exit != ExitTipMismatch || moved == m.Tip {
		t.Fatalf("exit %d\n%s", exit, out)
	}
	if want := "ataniq-verify: the tip ref does not resolve to the reviewed tip\n"; out != want {
		t.Fatalf("output %q want %q", out, want)
	}
}

func TestScriptFetchFailures(t *testing.T) {
	m := newMirror(t)
	gates := []Gate{{Name: "one", Argv: []string{"true"}}}

	// A ref the mirror does not have.
	script, err := Script("refs/leads/lead/heads/none", m.Tip, m.Path, t.TempDir(), gates)
	if err != nil {
		t.Fatal(err)
	}
	if out, exit := runScript(t, script); exit != ExitFetchFailed || !strings.Contains(out, "cannot fetch the tip ref") {
		t.Fatalf("exit %d\n%s", exit, out)
	}

	// A mirror that does not exist.
	script, err = Script(testRef, m.Tip, filepath.Join(t.TempDir(), "absent.git"), t.TempDir(), gates)
	if err != nil {
		t.Fatal(err)
	}
	if out, exit := runScript(t, script); exit != ExitFetchFailed {
		t.Fatalf("exit %d\n%s", exit, out)
	}

	// A scratch "folder" that is a file cannot hold the tree.
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	script, err = Script(testRef, m.Tip, m.Path, notDir, gates)
	if err != nil {
		t.Fatal(err)
	}
	if out, exit := runScript(t, script); exit != ExitFetchFailed || !strings.Contains(out, "cannot make the scratch repository") {
		t.Fatalf("exit %d\n%s", exit, out)
	}
}

func TestPlanDigest(t *testing.T) {
	a := []Gate{{Name: "lint", Argv: []string{"make", "lint"}}, {Name: "test", Argv: []string{"make", "test"}}}
	base := PlanDigest(a)
	if len(base) != 64 || PlanDigest(a) != base {
		t.Fatalf("digest %q", base)
	}
	// Pinned: a change here is a change of the encoding.
	const pinned = "e7ffa74ebe8cd7d572a2d5e044d9203f81856b6e9fca28bc84e797c9463ecc2a"
	if base != pinned {
		t.Errorf("digest is %s, pinned %s", base, pinned)
	}
	variants := map[string][]Gate{
		"order":       {a[1], a[0]},
		"name":        {{Name: "lint2", Argv: a[0].Argv}, a[1]},
		"argument":    {{Name: "lint", Argv: []string{"make", "lint", "-j"}}, a[1]},
		"command":     {{Name: "lint", Argv: []string{"gmake", "lint"}}, a[1]},
		"fewer":       {a[0]},
		"none":        nil,
		"split":       {{Name: "lint", Argv: []string{"make lint"}}, a[1]},
		"moved arg":   {{Name: "lint", Argv: []string{"make"}}, {Name: "test", Argv: []string{"lint", "make", "test"}}},
		"joined name": {{Name: "linttest", Argv: []string{"make", "lint", "make", "test"}}},
	}
	seen := map[string]string{base: "base"}
	for name, g := range variants {
		d := PlanDigest(g)
		if other, dup := seen[d]; dup {
			t.Errorf("%s has the digest of %s", name, other)
		}
		seen[d] = name
	}
}
