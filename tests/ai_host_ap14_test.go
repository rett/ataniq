package tests

// The checks of deploy/ai-host/ap14-level1.sh, the read-only AP14 level-1
// check of an AI host. No test runs it with --check, which runs sudo: every
// run is --self-test (on the cases beside the script, on a case a test
// writes with --fixtures, or on a tree a test makes with --register-tree),
// or an argument refusal, which comes before anything is read.

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
)

const (
	ap14Script = "deploy/ai-host/ap14-level1.sh"
	ap14Dir    = "deploy/ai-host"
	// ap14Delete, as the content of a file of ap14Case, leaves out a file the
	// case has by default.
	ap14Delete = "\x00delete"
)

// ap14Good is a sudo -l of agent that the check finds clean, and ap14Reg a
// clean register listing.
const (
	ap14Good = "Matching Defaults entries for agent on aihost:\n    env_reset, timestamp_timeout=0\n\n" +
		"User agent may run the following commands on aihost:\n    (ALL : ALL) ALL\n"
	ap14Reg = "d 0 755 /\nd 0 755 /etc\nd 0 755 /etc/ataniq\nd 0 755 /etc/ataniq/grants\nf 0 644 /etc/ataniq/grants/README\n"
)

// ap14Case writes a case folder: sudo-l.txt and register.txt are clean
// unless files says otherwise.
func ap14Case(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{"sudo-l.txt": ap14Good, "register.txt": ap14Reg}
	for name, data := range files {
		all[name] = data
	}
	for name, data := range all {
		if data == ap14Delete {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ap14Env is the environment of every run: a temporary folder of the test's
// own, and nothing of the caller's.
func ap14Env(t *testing.T) []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(), "LC_ALL=C"}
}

// ap14Unprivileged skips a test that needs the self-test when it runs as
// root, which the self-test refuses (TestAiHostAP14SelfTest checks that).
func ap14Unprivileged(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("the self-test refuses root")
	}
}

// ap14Report runs the self-test report of the case dir for agent and returns
// its output and exit status.
func ap14Report(t *testing.T, dir string, extra ...string) (string, int) {
	t.Helper()
	ap14Unprivileged(t)
	args := append([]string{"--self-test", "--fixtures", dir, "--user", "agent"}, extra...)
	return ahRun(t, ap14Script, t.TempDir(), ap14Env(t), args...)
}

func ap14Has(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("the report does not say %q:\n%s", w, out)
		}
	}
}

func ap14Lacks(t *testing.T, out string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(out, b) {
			t.Errorf("the report says %q:\n%s", b, out)
		}
	}
}

// ap14Cases lists the case folders beside the script.
func ap14Cases(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(root(t), ap14Dir, "self-test"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []string
	for _, e := range ents {
		if e.IsDir() {
			cases = append(cases, e.Name())
		}
	}
	return cases
}

// The built-in self-test passes from the repository root and from another
// folder, with every case and the capture check; it writes nothing (its
// temporary folder stays empty and the fixtures do not change). As root it
// refuses before it reads anything.
func TestAiHostAP14SelfTest(t *testing.T) {
	env := ap14Env(t)
	if os.Geteuid() == 0 {
		out, code := ahRun(t, ap14Script, t.TempDir(), env, "--self-test")
		if code != 2 || !strings.Contains(out, "--self-test runs unprivileged") {
			t.Errorf("as root: exit %d\n%s", code, out)
		}
		return
	}
	cases := ap14Cases(t)
	if len(cases) < 27 {
		t.Fatalf("only %d cases", len(cases))
	}
	check := func(where, out string, code int) {
		t.Helper()
		want := fmt.Sprintf("self-test: ok, %d checks; it read only the fixtures, in place, never ran sudo, and wrote nothing\n", len(cases)+1)
		if code != 0 || !strings.HasSuffix(out, want) {
			t.Errorf("%s: exit %d, want 0 and %q\n%s", where, code, want, out)
		}
		for _, c := range append([]string{"capture"}, cases...) {
			if !strings.Contains(out, "self-test: ok "+c+"\n") {
				t.Errorf("%s: the case %s did not pass:\n%s", where, c, out)
			}
		}
		if strings.Contains(out, "FAIL") {
			t.Errorf("%s: a FAIL line:\n%s", where, out)
		}
	}
	before := ahSnapshot(t, filepath.Join(root(t), ap14Dir))
	// By its absolute path, from another folder.
	out, code := ahRun(t, ap14Script, t.TempDir(), env, "--self-test")
	check("absolute", out, code)
	// By a relative path, from the repository root, as the DONE check runs it.
	cmd := exec.Command("/bin/sh", ap14Script, "--self-test")
	cmd.Dir = root(t)
	cmd.Env = env
	data, err := cmd.CombinedOutput()
	code = 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	check("relative", string(data), code)
	for _, e := range env {
		if tmp, ok := strings.CutPrefix(e, "TMPDIR="); ok {
			if left, _ := os.ReadDir(tmp); len(left) != 0 {
				t.Errorf("the self-test left %d entries in its temporary folder", len(left))
			}
		}
	}
	if after := ahSnapshot(t, filepath.Join(root(t), ap14Dir)); !slices.Equal(before, after) {
		t.Error("the self-test changed deploy/ai-host")
	}
}

// Every case beside the script has the files the self-test reads, and its
// expected.txt ends with the exit status its findings and stops call for.
func TestAiHostAP14CaseFiles(t *testing.T) {
	for _, c := range ap14Cases(t) {
		dir := filepath.Join(root(t), ap14Dir, "self-test", c)
		for _, f := range []string{"user.txt", "sudo-l.txt", "register.txt", "expected.txt"} {
			info, err := os.Lstat(filepath.Join(dir, f))
			if err != nil || !info.Mode().IsRegular() {
				t.Errorf("%s: %s is not a regular file", c, f)
			}
		}
		data, err := os.ReadFile(filepath.Join(dir, "expected.txt"))
		if err != nil {
			continue
		}
		exp := string(data)
		want := "exit: 0\n"
		switch {
		case strings.Contains(exp, "\nstop: "):
			want = "exit: 3\n"
		case strings.Contains(exp, "\nfinding: "):
			want = "exit: 1\n"
		}
		if !strings.HasSuffix(exp, want) {
			t.Errorf("%s: expected.txt does not end with %q", c, want)
		}
	}
}

// What sudo prints that leaves a way to run commands without NAME's own
// password at each prompt is a finding, with the owner's steps in prose.
func TestAiHostAP14SudoFindings(t *testing.T) {
	grant := "User agent may run the following commands on aihost:\n    (ALL : ALL) ALL\n"
	for _, c := range []struct {
		sudo  string
		want  []string
		steps string
	}{
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nUser agent may run the following commands on aihost:\n    (root) NOPASSWD: /usr/bin/a\n",
			[]string{"finding: nopasswd: sudo runs this for agent without a password: (root) NOPASSWD: /usr/bin/a\n"},
			"remove the NOPASSWD: tag; keep one password-asked grant for you"},
		// A tag on a wrapped command line, and a second entry.
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nUser agent may run the following commands on aihost:\n    (ALL) ALL\n    (root) /usr/bin/a,\n        NOPASSWD: /usr/bin/b\n",
			[]string{"finding: nopasswd: sudo runs this for agent without a password: (root) /usr/bin/a, NOPASSWD: /usr/bin/b\n"},
			"Change only the tag: do not delete the rule"},
		{"Matching Defaults entries for agent on aihost:\n    targetpw, timestamp_timeout=0\n\n" + grant,
			[]string{"finding: otherpw: sudo asks for the target user's password (usually root's), not agent's: Matching Defaults has targetpw\n"},
			"sudo asks for another account's password"},
		{"Matching Defaults entries for agent on aihost:\n    env_reset,\n    rootpw,\n    timestamp_timeout=0\n\n" + grant,
			[]string{"finding: otherpw: sudo asks for root's password, not agent's: Matching Defaults has rootpw\n"}, "remove the setting"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nRunas and Command-specific defaults for agent:\n    Defaults>root runaspw\n\n" + grant,
			[]string{"finding: otherpw: sudo asks for the password of the runas_default user, not agent's: Defaults>root has runaspw\n"}, "remove the setting"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nRunas and Command-specific defaults for agent:\n    Defaults!/usr/bin/a, /usr/bin/b env_reset, targetpw\n\n" + grant,
			[]string{"finding: otherpw: sudo asks for the target user's password (usually root's), not agent's: Defaults!/usr/bin/a, /usr/bin/b has targetpw\n"}, "remove the setting"},
		// A binding of several commands, wrapped, is named whole.
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nRunas and Command-specific defaults for agent:\n    Defaults!/usr/bin/a,\n    /usr/bin/b timestamp_timeout=15\n\n" + grant,
			[]string{"finding: timeout: a cached ticket lets commands run unprompted after one password: Defaults!/usr/bin/a, /usr/bin/b has timestamp_timeout=15\n"}, "remove the setting"},
		{"Matching Defaults entries for agent on aihost:\n    !authenticate, timestamp_timeout=0\n\n" + grant,
			[]string{"finding: noauth: sudo asks no password at all: Matching Defaults has !authenticate\n"}, "!authenticate: sudo asks no password at all"},
		{"Matching Defaults entries for agent on aihost:\n    exempt_group=wheel, timestamp_timeout=0\n\n" + grant,
			[]string{"finding: exempt: members of that group are never asked for a password, and sudo -l does not say whether agent is one: Matching Defaults has exempt_group=wheel\n"},
			"make sure agent is not a member of that group"},
		// A quoted value with a blank: every word is judged, not the last.
		{"Matching Defaults entries for agent on aihost:\n    exempt_group=\"wheel admins\", timestamp_timeout=0\n\n" + grant,
			[]string{"finding: exempt: members of that group are never asked for a password, and sudo -l does not say whether agent is one: Matching Defaults has exempt_group=\"wheel admins\"\n"},
			"make sure agent is not a member of that group"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nRunas and Command-specific defaults for agent:\n    Defaults>root exempt_group=\"a b\"\n\n" + grant,
			[]string{"finding: exempt: members of that group are never asked for a password, and sudo -l does not say whether agent is one: Defaults>root has exempt_group=\"a b\"\n"},
			"make sure agent is not a member of that group"},
		// A word is judged without its quotes, wherever it stands.
		{"Matching Defaults entries for agent on aihost:\n    \"targetpw\", timestamp_timeout=0\n\n" + grant,
			[]string{"finding: otherpw: sudo asks for the target user's password (usually root's), not agent's: Matching Defaults has targetpw\n"}, "remove the setting"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nRunas and Command-specific defaults for agent:\n    Defaults>root \"exempt_group=a b\"\n\n" + grant,
			[]string{"finding: exempt: members of that group are never asked for a password, and sudo -l does not say whether agent is one: Defaults>root has exempt_group=a b\"\n"},
			"make sure agent is not a member of that group"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0, timestamp_timeout=15\n\n" + grant,
			[]string{"finding: timeout: a cached ticket lets commands run unprompted after one password: Matching Defaults has timestamp_timeout=15\n"},
			"an agent session included"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nRunas and Command-specific defaults for agent:\n    Defaults!/usr/bin/a timestamp_timeout=2.5\n\n" + grant,
			[]string{"finding: timeout: a cached ticket lets commands run unprompted after one password: Defaults!/usr/bin/a has timestamp_timeout=2.5\n"}, "remove the setting"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=-1\n\n" + grant,
			[]string{"finding: timeout: ", "Matching Defaults has timestamp_timeout=-1\n",
				"finding: dropin: sudo -l lists no timestamp_timeout=0 for agent"}, "write the drop-in /etc/sudoers.d/zz-ap14-level1-agent by hand"},
		// The drop-in's zero counts only in Matching Defaults.
		{"Runas and Command-specific defaults for agent:\n    Defaults!/usr/bin/a timestamp_timeout=0\n\n" + grant,
			[]string{"finding: dropin: sudo -l lists no timestamp_timeout=0 for agent: sudo's built-in default keeps a ticket for some minutes, so commands run unprompted after one password\n"},
			"check the whole configuration with visudo -c"},
	} {
		out, code := ap14Report(t, ap14Case(t, map[string]string{"sudo-l.txt": c.sudo}))
		if code != 1 {
			t.Errorf("exit %d, want 1:\n%s", code, out)
		}
		ap14Has(t, out, c.want...)
		ap14Has(t, out, "== by hand, for the owner\n", c.steps, "result: findings remain: fix them by hand, then run --check again\n")
		ap14Lacks(t, out, "stop: ", "result: clean")
	}
}

// The forms that are clean: a grant that asks for the password, a negated
// setting, every zero, and no grant at all once the owner has answered the
// question.
func TestAiHostAP14Clean(t *testing.T) {
	for _, c := range []struct {
		sudo  string
		rc    string
		extra []string
		want  []string
	}{
		{ap14Good, "", nil, []string{
			"check: no command sudo lists for agent skips the password\n",
			"check: timestamp_timeout=0 is listed for agent, and no other value\n",
			"check: no default makes sudo ask for another account's password, or for none (targetpw, rootpw, runaspw, !authenticate, exempt_group)\n",
		}},
		{"Matching Defaults entries for agent on aihost:\n    !targetpw, !rootpw, !runaspw, authenticate, timestamp_timeout=0.0, timestamp_timeout=00\n\n" +
			"Runas and Command-specific defaults for agent:\n    Defaults!/usr/bin/a timestamp_timeout=0, !targetpw\n\n" +
			"User agent may run the following commands on aihost:\n    (ALL : ALL) ALL\n    (root) PASSWD: /usr/bin/b\n", "", nil,
			[]string{"check: timestamp_timeout=0 is listed for agent, and no other value\n"}},
		// Quoted values with blanks whose words are no option stay clean.
		{"Matching Defaults entries for agent on aihost:\n    env_reset, env_keep+=\"LANG LC_ALL TZ\", timestamp_timeout=0\n\n" +
			"Runas and Command-specific defaults for agent:\n    Defaults!/usr/bin/sudoedit env_keep+=\"EDITOR VISUAL\", !env_reset\n    Defaults>backup env_keep=\"HOME TERM\"\n\n" +
			"User agent may run the following commands on aihost:\n    (ALL : ALL) ALL\n", "", nil,
			[]string{"check: no default makes sudo ask for another account's password, or for none (targetpw, rootpw, runaspw, !authenticate, exempt_group)\n"}},
		{"User agent is not allowed to run sudo on aihost.\n", "1\n", []string{"--other-root-login"}, []string{
			"note: no sudo grant: make sure root can log in another way\n",
			"note: you answered that it can (--other-root-login), so agent has no sudo route to root\n",
		}},
		{"User agent may not run sudo on aihost.\n", "0\n", []string{"--other-root-login"}, []string{"note: no sudo grant: make sure root can log in another way\n"}},
	} {
		files := map[string]string{"sudo-l.txt": c.sudo}
		if c.rc != "" {
			files["sudo-l.rc"] = c.rc
		}
		out, code := ap14Report(t, ap14Case(t, files), c.extra...)
		if code != 0 {
			t.Errorf("exit %d, want 0:\n%s", code, out)
		}
		ap14Has(t, out, c.want...)
		ap14Has(t, out, "findings: 0; stops: 0\n", "result: clean: sudo itself shows no way for agent to skip the password, and the register is root's\n")
		ap14Lacks(t, out, "finding: ", "stop: ", "== by hand")
	}
}

// Whatever the check cannot judge whole is a stop, never clean: exit 3,
// with the reason, and no check line for sudo's part.
func TestAiHostAP14SudoStops(t *testing.T) {
	for _, c := range []struct {
		sudo, err, rc string
		want          string
	}{
		{"", "sudo: unknown user agent\n", "1\n", "stop: sudo -n -l -U agent failed (exit 1)\n"},
		{"", "sudo: unknown user agent\n", "1\n", "stop: sudo printed on its error output: sudo: unknown user agent\n"},
		{ap14Good, "", "1\n", "stop: sudo -n -l -U agent failed (exit 1)\n"},
		{"User agent is not allowed to run sudo on aihost.\n", "sudo: a warning\n", "1\n", "stop: sudo -n -l -U agent failed (exit 1)\n"},
		{ap14Good, "sudo: unable to resolve host aihost\n", "", "stop: sudo printed on its error output: sudo: unable to resolve host aihost\n"},
		{ap14Good + "Sudoers entry:\n", "", "", "stop: a line of sudo -l this check does not know: Sudoers entry:\n"},
		{"User other may run the following commands on aihost:\n    (ALL) ALL\n", "", "", "stop: a line of sudo -l this check does not know: User other may run the following commands on aihost:\n"},
		{"    env_reset\n" + ap14Good, "", "", "stop: a line of sudo -l this check does not know:     env_reset\n"},
		{"User agent may run the following commands on aihost:\n        /usr/bin/a\n    (ALL) ALL\n", "", "", "stop: a line of sudo -l this check does not know:         /usr/bin/a\n"},
		{"User agent may run the following commands on aihost:\n    ALL\n", "", "", "stop: a line of sudo -l this check does not know:     ALL\n"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n", "", "", "stop: sudo -l -U agent lists neither a grant nor that agent is not allowed to run sudo\n"},
		{"", "", "", "stop: sudo -l -U agent lists neither a grant nor that agent is not allowed to run sudo\n"},
		{"User agent may run the following commands on aihost:\n", "", "", "stop: sudo -l -U agent has a grant heading with no command under it\n"},
		{ap14Good + "\nUser agent is not allowed to run sudo on aihost.\n", "", "", "stop: sudo -l -U agent says both that agent may run commands and that agent is not allowed to run sudo\n"},
		{"Matching Defaults entries for agent on aihost:\n    !timestamp_timeout, timestamp_timeout=0\n\nUser agent may run the following commands on aihost:\n    (ALL) ALL\n", "", "",
			"stop: a timestamp_timeout this check does not know: Matching Defaults has !timestamp_timeout\n"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nRunas and Command-specific defaults for agent:\n    Defaults!/usr/bin/a timestamp_timeout=soon\n\nUser agent may run the following commands on aihost:\n    (ALL) ALL\n", "", "",
			"stop: a timestamp_timeout this check does not know: Defaults!/usr/bin/a has timestamp_timeout=soon\n"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nRunas and Command-specific defaults for agent:\n    targetpw\n\nUser agent may run the following commands on aihost:\n    (ALL) ALL\n", "", "",
			"stop: a command-specific default this check does not know: targetpw\n"},
		{"Matching Defaults entries for agent on aihost:\n    timestamp_timeout=0\n\nRunas and Command-specific defaults for agent:\n    Defaults!/usr/bin/a, /usr/bin/b\n\nUser agent may run the following commands on aihost:\n    (ALL) ALL\n", "", "",
			"stop: a command-specific default this check does not know: Defaults!/usr/bin/a, /usr/bin/b\n"},
		{"User agent is not allowed to run sudo on aihost.\n", "", "1\n", "stop: no sudo grant: make sure root can log in another way\n"},
	} {
		files := map[string]string{"sudo-l.txt": c.sudo}
		if c.err != "" {
			files["sudo-l.err"] = c.err
		}
		if c.rc != "" {
			files["sudo-l.rc"] = c.rc
		}
		out, code := ap14Report(t, ap14Case(t, files))
		if code != 3 {
			t.Errorf("%q: exit %d, want 3:\n%s", c.want, code, out)
		}
		ap14Has(t, out, c.want, "result: STOPPED: the check could not see everything, so it is not clean; fix what each stop says and run --check again\n")
		ap14Lacks(t, out, "check: no command sudo lists", "check: timestamp_timeout=0", "check: no default", "result: clean")
	}
	// The question for the owner, when the user has no grant.
	out, _ := ap14Report(t, ap14Case(t, map[string]string{"sudo-l.txt": "User agent is not allowed to run sudo on aihost.\n", "sudo-l.rc": "1\n"}))
	ap14Has(t, out, "question: agent has no sudo grant. How does root log in on this host without it",
		"If there is such a way and you have written it down, run --check again with --other-root-login.")
}

// The register: the folders down to /etc/ataniq/grants must be root's
// folders, not writable by group or others, and its entries regular files
// of root's, not writable by group or others; a missing register is a
// finding, and a listing the check cannot read whole is a stop.
func TestAiHostAP14Register(t *testing.T) {
	head := "d 0 755 /\nd 0 755 /etc\nd 0 755 /etc/ataniq\nd 0 755 /etc/ataniq/grants\n"
	for _, c := range []struct {
		reg  string
		code int
		want string
	}{
		{head, 0, "check: /etc/ataniq/grants and every folder above it are root's and not writable by group or others; its 0 entries are regular files of root's, not writable by group or others\n"},
		{head + "f 0 600 /etc/ataniq/grants/x\nf 0 1644 /etc/ataniq/grants/y\n", 0, "its 2 entries are regular files"},
		{"d 0 755 /\nd 0 755 /etc\nmissing - - /etc/ataniq\n", 1, "finding: register-missing: there is no register: /etc/ataniq is missing\n"},
		{"d 0 755 /\nd 0 755 /etc\nd 0 755 /etc/ataniq\nmissing - - /etc/ataniq/grants\n", 1, "finding: register-missing: there is no register: /etc/ataniq/grants is missing\n"},
		{"d 0 755 /\nd 0 775 /etc\nd 0 755 /etc/ataniq\nd 0 755 /etc/ataniq/grants\n", 1, "finding: register-folder: /etc is writable by its group or by others (mode 775)\n"},
		{"d 0 755 /\nd 0 755 /etc\nd 0 757 /etc/ataniq\nd 0 755 /etc/ataniq/grants\n", 1, "finding: register-folder: /etc/ataniq is writable by its group or by others (mode 757)\n"},
		{"d 0 755 /\nd 0 755 /etc\nd 1001 755 /etc/ataniq\nd 0 755 /etc/ataniq/grants\n", 1, "finding: register-folder: /etc/ataniq is owned by uid 1001, not root\n"},
		{"d 0 755 /\nd 0 755 /etc\nl 0 777 /etc/ataniq\n", 1, "finding: register-folder: /etc/ataniq is not a folder of its own (a link)\n"},
		{"d 0 755 /\nd 0 755 /etc\nd 0 755 /etc/ataniq\nf 0 644 /etc/ataniq/grants\n", 1, "finding: register-folder: /etc/ataniq/grants is not a folder of its own (a file or another kind)\n"},
		{head + "f 1001 644 /etc/ataniq/grants/x\n", 1, "finding: register-entry: /etc/ataniq/grants/x is owned by uid 1001, not root\n"},
		{head + "f 0 664 /etc/ataniq/grants/x\n", 1, "finding: register-entry: /etc/ataniq/grants/x is writable by its group or by others (mode 664)\n"},
		{head + "f 0 646 /etc/ataniq/grants/x y\n", 1, "finding: register-entry: /etc/ataniq/grants/x y is writable by its group or by others (mode 646)\n"},
		{head + "l 0 777 /etc/ataniq/grants/x\n", 1, "finding: register-entry: /etc/ataniq/grants/x is not a regular file (a link)\n"},
		{head + "d 0 755 /etc/ataniq/grants/x\n", 1, "finding: register-entry: /etc/ataniq/grants/x is not a regular file (a folder)\n"},
		{head + "o 0 644 /etc/ataniq/grants/x\n", 1, "finding: register-entry: /etc/ataniq/grants/x is not a regular file (another kind)\n"},
		{head + "unreadable - - /etc/ataniq/grants\n", 3, "stop: the register cannot be read: /etc/ataniq/grants (run --check as root)\n"},
		{"d 0 755 /\nd 0 755 /etc\nd 0 0 /etc/ataniq\nunreadable - - /etc/ataniq\n", 3, "stop: the register cannot be read: /etc/ataniq (run --check as root)\n"},
		{head + "badname - - /etc/ataniq/grants/a?b\n", 3, "stop: a register entry's name holds a character this check cannot print: /etc/ataniq/grants/a?b\n"},
		{head + "f 0 644 /etc/shadow\n", 3, "stop: a register listing line this check cannot judge: f 0 644 /etc/shadow\n"},
		{head + "f 0 644 /etc/ataniq/grants/a/b\n", 3, "stop: a register listing line this check cannot judge: f 0 644 /etc/ataniq/grants/a/b\n"},
		{head + "f 0 rw /etc/ataniq/grants/a\n", 3, "stop: a register listing line this check cannot judge: f 0 rw /etc/ataniq/grants/a\n"},
		{head + "missing - - /etc\n", 3, "stop: a register listing line this check cannot judge: missing - - /etc\n"},
		{"", 3, "stop: the register listing is empty\n"},
		{"d 0 755 /\nd 0 755 /etc\nd 0 755 /etc/ataniq\n", 3, "stop: the register listing does not reach /etc/ataniq/grants\n"},
		{"d 1001 755 /\nd 0 755 /etc\n", 3, "stop: the register listing does not reach /etc/ataniq/grants\n"},
	} {
		out, code := ap14Report(t, ap14Case(t, map[string]string{"register.txt": c.reg}))
		if code != c.code {
			t.Errorf("%q: exit %d, want %d:\n%s", c.want, code, c.code, out)
		}
		ap14Has(t, out, c.want)
	}
	// The owner's steps for an entry: a fresh file renamed over the old one,
	// never a change of the old file in place.
	out, _ := ap14Report(t, ap14Case(t, map[string]string{"register.txt": head + "f 1001 644 /etc/ataniq/grants/x\n"}))
	ap14Has(t, out, "read it and confirm every word is yours", "write that text into a new file of root's (mode 0644) beside it and rename the new file over the old name",
		"another process may still hold it open for writing", "Never change the owner or the mode of the old file in place.")
}

// The register listing --check makes, read from a tree that stands for /:
// one line per folder down to the register, then each entry, a link not
// followed, a name it cannot print shown as a stop line, and a folder it
// cannot enter or read as unreadable.
func TestAiHostAP14RegisterTree(t *testing.T) {
	ap14Unprivileged(t)
	list := func(tree string) (string, int) {
		return ahRun(t, ap14Script, t.TempDir(), ap14Env(t), "--self-test", "--register-tree", tree)
	}
	uid := fmt.Sprint(os.Getuid())
	tree := t.TempDir()
	reg := filepath.Join(tree, "etc/ataniq/grants")
	if err := os.MkdirAll(filepath.Join(reg, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{tree, filepath.Join(tree, "etc"), filepath.Join(tree, "etc/ataniq"), reg} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, mode := range map[string]os.FileMode{"a": 0o664, "b c": 0o644, "n\tl": 0o644} {
		if err := os.WriteFile(filepath.Join(reg, name), []byte("x\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(reg, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/shadow", filepath.Join(reg, ".l")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(reg, "f"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(reg, "f"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := list(tree)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	got := strings.Split(strings.TrimSpace(out), "\n")
	want := []string{
		"d " + uid + " 755 /", "d " + uid + " 755 /etc", "d " + uid + " 755 /etc/ataniq", "d " + uid + " 755 /etc/ataniq/grants",
		"f " + uid + " 664 /etc/ataniq/grants/a", "f " + uid + " 644 /etc/ataniq/grants/b c", "o " + uid + " 644 /etc/ataniq/grants/f",
		"badname - - /etc/ataniq/grants/n?l", "d " + uid + " 755 /etc/ataniq/grants/sub", "l " + uid + " 777 /etc/ataniq/grants/.l",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the listing:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A folder it cannot enter ends the listing as unreadable.
	if err := os.Chmod(filepath.Join(tree, "etc/ataniq"), 0); err != nil {
		t.Fatal(err)
	}
	out, _ = list(tree)
	if err := os.Chmod(filepath.Join(tree, "etc/ataniq"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "d "+uid+" 0 /etc/ataniq\nunreadable - - /etc/ataniq\n") {
		t.Errorf("an unreadable folder:\n%s", out)
	}
	// A register it can enter but not read.
	if err := os.Chmod(reg, 0o311); err != nil {
		t.Fatal(err)
	}
	out, _ = list(tree)
	if err := os.Chmod(reg, 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "d "+uid+" 311 /etc/ataniq/grants\nunreadable - - /etc/ataniq/grants\n") {
		t.Errorf("an unreadable register:\n%s", out)
	}
	// A link in place of a folder ends it, not followed.
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tree, "etc/ataniq"), filepath.Join(other, "etc/ataniq")); err != nil {
		t.Fatal(err)
	}
	out, _ = list(other)
	if !strings.HasSuffix(out, "l "+uid+" 777 /etc/ataniq\n") {
		t.Errorf("a linked folder:\n%s", out)
	}
	// No register.
	out, _ = list(t.TempDir())
	if !strings.HasSuffix(out, "missing - - /etc\n") {
		t.Errorf("an empty tree:\n%s", out)
	}
}

// --fixtures and --register-tree refuse the filesystem root by its identity,
// whatever the spelling, and a case whose files are missing, links or not
// usable; nothing is read then.
func TestAiHostAP14FixtureRefusals(t *testing.T) {
	ap14Unprivileged(t)
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, opt := range []string{"--fixtures", "--register-tree"} {
		for _, dir := range []string{"/", "//", "/.", "/../", "///.//", link} {
			args := []string{"--self-test", opt, dir}
			if opt == "--fixtures" {
				args = append(args, "--user", "agent")
			}
			out, code := ahRun(t, ap14Script, t.TempDir(), ap14Env(t), args...)
			if code != 2 || !strings.Contains(out, "refused: "+opt+" must not be the filesystem root (by any spelling or link)") {
				t.Errorf("%s %q: exit %d\n%s", opt, dir, code, out)
			}
		}
		for _, dir := range []string{file, filepath.Join(t.TempDir(), "none")} {
			args := []string{"--self-test", opt, dir}
			if opt == "--fixtures" {
				args = append(args, "--user", "agent")
			}
			out, code := ahRun(t, ap14Script, t.TempDir(), ap14Env(t), args...)
			if code != 2 || !strings.Contains(out, "refused: "+opt+" must name a folder") {
				t.Errorf("%s %q: exit %d\n%s", opt, dir, code, out)
			}
		}
	}
	linked := ap14Case(t, map[string]string{"sudo-l.txt": ap14Delete})
	if err := os.Symlink("/etc/passwd", filepath.Join(linked, "sudo-l.txt")); err != nil {
		t.Fatal(err)
	}
	linkedReg := ap14Case(t, map[string]string{"register.txt": ap14Delete})
	if err := os.Symlink(filepath.Join(linked, "x"), filepath.Join(linkedReg, "register.txt")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		dir, want string
	}{
		{ap14Case(t, map[string]string{"sudo-l.txt": ap14Delete}), "--fixtures: sudo-l.txt is missing or not a readable regular file"},
		{ap14Case(t, map[string]string{"register.txt": ap14Delete}), "--fixtures: register.txt is missing or not a readable regular file"},
		{linked, "--fixtures: sudo-l.txt is a link; a fixture file must be a regular file of its case"},
		{linkedReg, "--fixtures: register.txt is a link; a fixture file must be a regular file of its case"},
		{ap14Case(t, map[string]string{"sudo-l.rc": "one\n"}), "--fixtures: sudo-l.rc is not a number"},
		{ap14Case(t, map[string]string{"sudo-l.rc": "-1\n"}), "--fixtures: sudo-l.rc is not a number"},
	} {
		out, code := ap14Report(t, c.dir)
		if code != 2 || !strings.Contains(out, "refused: "+c.want) {
			t.Errorf("%q: exit %d\n%s", c.want, code, out)
		}
		ap14Lacks(t, out, "== sudo")
	}
}

// The arguments: every refusal comes before anything is read or run; the
// modes that wrote files are gone, each with the pointer to the README.
func TestAiHostAP14Refusals(t *testing.T) {
	env := []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir()}
	cases := []struct {
		args []string
		want string
	}{
		{nil, "usage: ap14-level1.sh --check --user NAME [--other-root-login]"},
		{[]string{"--help"}, "usage: ap14-level1.sh --check --user NAME [--other-root-login]"},
		{[]string{"--check"}, "refused: --user NAME is required"},
		{[]string{"--check", "--user"}, "refused: --user needs a value"},
		{[]string{"--check", "--user", "root"}, "refused: --user must be the everyday user, not root"},
		{[]string{"--check", "--user", "Bad Name"}, "refused: --user must be a login name"},
		{[]string{"--check", "--user", "a;b"}, "refused: --user must be a login name"},
		{[]string{"--check", "--user", ""}, "refused: --user must be a login name"},
		{[]string{"--check", "--user", "first.last"}, "refused: --user must be a login name (lowercase letters, digits, _ -; no dot, which sudo skips in a drop-in's name)"},
		{[]string{"--check", "--user", strings.Repeat("a", 33)}, "refused: --user must be a login name"},
		{[]string{"--check", "--user", "agent", "--fixtures", "/x"}, "refused: --fixtures and --register-tree go with --self-test only"},
		{[]string{"--check", "--user", "agent", "--register-tree", "/x"}, "refused: --fixtures and --register-tree go with --self-test only"},
		{[]string{"--check", "--self-test", "--user", "agent"}, "refused: give one of --check and --self-test"},
		{[]string{"--check", "--user", "agent", "--extra"}, "refused: unknown argument"},
		{[]string{"--self-test", "--user", "agent"}, "refused: --user goes with --fixtures in a self-test"},
		{[]string{"--self-test", "--other-root-login"}, "refused: --other-root-login goes with --check, or with --fixtures in a self-test"},
		{[]string{"--self-test", "--fixtures", "/x"}, "refused: --user NAME is required"},
		{[]string{"--self-test", "--register-tree", "/x", "--user", "agent"}, "refused: --register-tree goes alone with --self-test"},
		{[]string{"--self-test", "--register-tree", "/x", "--fixtures", "/y"}, "refused: --register-tree goes alone with --self-test"},
		{[]string{"--self-test", "--register-tree"}, "refused: --register-tree needs a value"},
	}
	for _, gone := range []string{"--dry", "--apply", "--verify", "--confirm"} {
		cases = append(cases, struct {
			args []string
			want string
		}{[]string{gone, "--user", "agent"}, "refused: " + gone + " is gone: the script only checks (--check); the owner writes the drop-in and the register by hand, as README.md says"})
	}
	for _, c := range cases {
		out, code := ahRun(t, ap14Script, t.TempDir(), env, c.args...)
		if code != 2 || !strings.Contains(out, c.want) {
			t.Errorf("%q: exit %d, want 2 and %q:\n%s", c.args, code, c.want, out)
		}
	}
}

// ap14Stops are the fixed parts of every stop the script prints. Each is in
// the script, each appears in the expected.txt of a case beside it, and the
// self-test fails when one is removed or turned into a plain line.
var ap14Stops = []string{
	" failed (exit ",
	"sudo printed on its error output: ",
	"a line of sudo -l this check does not know: ",
	" lists neither a grant nor that ",
	" has a grant heading with no command under it",
	" says both that ",
	"a timestamp_timeout this check does not know: ",
	"a command-specific default this check does not know: ",
	"no sudo grant: make sure root can log in another way",
	"the register cannot be read: ",
	"name holds a character this check cannot print: ",
	"a register listing line this check cannot judge: ",
	"the register listing is empty",
	"the register listing does not reach ",
}

// ap14ExpectedText is a stop's fixed part as it reads in a report.
func ap14ExpectedText(s string) string {
	return strings.ReplaceAll(s, `\047`, "'")
}

func TestAiHostAP14StopsHaveCases(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), ap14Script))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	var expected strings.Builder
	for _, c := range ap14Cases(t) {
		data, err := os.ReadFile(filepath.Join(root(t), ap14Dir, "self-test", c, "expected.txt"))
		if err != nil {
			t.Fatal(err)
		}
		expected.Write(data)
	}
	// Every stop( call of the script names one of ap14Stops.
	calls := regexp.MustCompile(`stop\("[^\n]*`).FindAllString(script, -1)
	if len(calls) < len(ap14Stops) {
		t.Errorf("only %d stop calls in the script", len(calls))
	}
	for _, call := range calls {
		if !slices.ContainsFunc(ap14Stops, func(s string) bool { return strings.Contains(call, s) }) {
			t.Errorf("a stop the test does not list: %s", call)
		}
	}
	for _, s := range ap14Stops {
		if !strings.Contains(script, s) {
			t.Errorf("the script has no stop %q", s)
		}
		lines := strings.Split(expected.String(), "\n")
		if !slices.ContainsFunc(lines, func(l string) bool {
			return strings.HasPrefix(l, "stop: ") && strings.Contains(l, ap14ExpectedText(s))
		}) {
			t.Errorf("no case beside the script stops with %q", ap14ExpectedText(s))
		}
	}
}

// The self-test fails when a stop is removed: with the stop's text taken
// out, and with its stop( call turned into a plain print, a copy of the
// script run against a copy of the cases fails.
func TestAiHostAP14SelfTestCatchesRemovedStops(t *testing.T) {
	ap14Unprivileged(t)
	data, err := os.ReadFile(filepath.Join(root(t), ap14Script))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	work := t.TempDir()
	if err := os.CopyFS(filepath.Join(work, "self-test"), os.DirFS(filepath.Join(root(t), ap14Dir, "self-test"))); err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, name, text string) (string, int) {
		path := filepath.Join(work, name)
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("/bin/sh", path, "--self-test")
		cmd.Dir = t.TempDir()
		cmd.Env = ap14Env(t)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return string(out), 0
		}
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		return string(out), exit.ExitCode()
	}
	// The control: the copy passes as it is.
	if out, code := run(t, "control.sh", script); code != 0 {
		t.Fatalf("the unchanged copy fails: exit %d\n%s", code, out)
	}
	for i, s := range ap14Stops {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			removed := strings.ReplaceAll(script, s, "")
			var demoted []string
			for _, line := range strings.Split(script, "\n") {
				if strings.Contains(line, s) {
					line = strings.ReplaceAll(line, "stop(", "print(")
				}
				demoted = append(demoted, line)
			}
			for kind, text := range map[string]string{"removed": removed, "demoted": strings.Join(demoted, "\n")} {
				if text == script {
					t.Errorf("%q: the %s copy is unchanged", s, kind)
					continue
				}
				out, code := run(t, fmt.Sprintf("%s-%d.sh", kind, i), text)
				if code != 1 || !strings.Contains(out, "self-test: FAIL ") {
					t.Errorf("%q %s: the self-test did not fail: exit %d\n%s", s, kind, code, out)
				}
			}
		})
	}
}

// The script reads only: its first commands fix PATH to the system folders;
// it runs no program that writes, moves or removes a file, never redirects
// into a file, and runs sudo once, as `sudo -n -l -U NAME`.
func TestAiHostAP14Source(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), ap14Script))
	if err != nil {
		t.Fatal(err)
	}
	var code []string
	for _, line := range strings.Split(string(data), "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		code = append(code, trim)
	}
	if len(code) < 2 || code[0] != "PATH=/usr/sbin:/usr/bin:/sbin:/bin" || code[1] != "export PATH" {
		t.Errorf("the script does not start by fixing PATH: %q", code[:2])
	}
	cmdPos := `(^|[;&|({]\s*|\$\(\s*|\b(then|do|else|elif|if|!)\s+)`
	writers := regexp.MustCompile(cmdPos + `(rm|mv|ln|cp|chmod|chown|chgrp|mkdir|rmdir|touch|install|tee|truncate|dd|mktemp|mkfifo|visudo|setfacl|gpasswd|usermod|sed|perl|python3?|runuser|su|doas)(\s|$)`)
	for _, line := range code {
		if m := writers.FindString(line); m != "" {
			t.Errorf("the code runs a program it must not (%q): %s", strings.TrimSpace(m), line)
		}
		// Every > is a descriptor copy, /dev/null, or an awk comparison
		// with =; never a file.
		for i := strings.Index(line, ">"); i >= 0; {
			rest := strings.TrimLeft(line[i+1:], " ")
			if !strings.HasPrefix(rest, "&") && !strings.HasPrefix(rest, "/dev/null") && !strings.HasPrefix(rest, "=") {
				t.Errorf("a redirection into a file: %s", line)
				break
			}
			j := strings.Index(line[i+1:], ">")
			if j < 0 {
				break
			}
			i += j + 1
		}
	}
	var sudo []string
	for _, line := range code {
		if strings.Contains(line, "sudo -n -l -U \"$NAME\"") || regexp.MustCompile(`(^|[;&|(]\s*|LC_ALL=C\s+)sudo\s`).MatchString(line) {
			sudo = append(sudo, line)
		}
	}
	want := `if sq_o=$(env -i PATH="$SYS_PATH" LC_ALL=C sudo -n -l -U "$NAME" </dev/null 2>&3); then sq_r=0; else sq_r=$?; fi`
	if len(sudo) != 1 || sudo[0] != want {
		t.Errorf("the script must run sudo once, as %q; it does: %q", want, sudo)
	}
	// Only --check reaches it: sudo_query is defined once and called once,
	// from check_main, which only the check mode runs.
	var uses []string
	for _, line := range code {
		if strings.Contains(line, "sudo_query") || strings.Contains(line, "check_main") {
			uses = append(uses, line)
		}
	}
	if !slices.Equal(uses, []string{"sudo_query() {", "check_main() {", "sudo_query", "check) check_main ;;"}) {
		t.Errorf("sudo_query is not reached by --check alone: %q", uses)
	}
}

// The owner's steps are prose: no edit command the owner would paste, and
// no advice to delete a rule.
func TestAiHostAP14StepsAreProse(t *testing.T) {
	ap14Unprivileged(t)
	sudo := "Matching Defaults entries for agent on aihost:\n    targetpw, !authenticate, exempt_group=x, timestamp_timeout=0, timestamp_timeout=5\n\n" +
		"User agent may run the following commands on aihost:\n    (ALL) NOPASSWD: ALL\n"
	reg := "d 0 755 /\nd 0 755 /etc\nd 0 777 /etc/ataniq\nd 0 755 /etc/ataniq/grants\nf 5 644 /etc/ataniq/grants/x\n"
	out, code := ap14Report(t, ap14Case(t, map[string]string{"sudo-l.txt": sudo, "register.txt": reg}))
	if code != 1 {
		t.Errorf("exit %d\n%s", code, out)
	}
	_, steps, ok := strings.Cut(out, "== by hand, for the owner\n")
	if !ok {
		t.Fatalf("no steps:\n%s", out)
	}
	steps, _, _ = strings.Cut(steps, "== summary\n")
	lines := strings.Split(strings.TrimSpace(steps), "\n")
	if len(lines) != 7 {
		t.Errorf("%d steps, want one for each of the 7 kinds:\n%s", len(lines), steps)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "- ") {
			t.Errorf("a step that is not a prose item: %s", line)
		}
	}
	for _, bad := range []*regexp.Regexp{
		regexp.MustCompile(`visudo -f '`), regexp.MustCompile("`"), regexp.MustCompile(`\b(rm|chmod|chown|sed|mv|ln) -?`),
		regexp.MustCompile(`&&|\|\|`),
	} {
		if m := bad.FindString(steps); m != "" {
			t.Errorf("the steps hold %q:\n%s", m, steps)
		}
	}
	if strings.Count(strings.ToLower(steps), "delete") != strings.Count(steps, "do not delete") {
		t.Errorf("the steps advise deleting something:\n%s", steps)
	}
	ap14Has(t, steps, "edit that file with visudo -f; remove the NOPASSWD: tag; keep one password-asked grant for you", "do not delete the rule")
}

// No file of deploy/ai-host names a real host, user or home path: no home
// folder, no mail address, no local host name, and nothing of the account or
// machine the test runs on.
func TestAiHostAP14NoPrivateNames(t *testing.T) {
	var private []string
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		private = append(private, home)
	}
	words := map[string]bool{}
	if u, err := user.Current(); err == nil {
		words[u.Username] = true
	}
	if h, err := os.Hostname(); err == nil {
		words[h] = true
		words[strings.SplitN(h, ".", 2)[0]] = true
	}
	for w := range words {
		// Generic names (root, and the fixtures' own) say nothing private.
		if len(w) < 3 || w == "root" || w == "agent" || w == "other" || w == "aihost" || w == "localhost" {
			delete(words, w)
		}
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`/home/|/Users/|/var/home/`),
		regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`),
		regexp.MustCompile(`\b[A-Za-z0-9-]+\.(lan|local|internal|home\.arpa|localdomain)\b`),
	}
	n := 0
	err := filepath.WalkDir(filepath.Join(root(t), ap14Dir), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root(t), p)
		for _, s := range append([]string{rel}, string(data)) {
			for _, re := range patterns {
				if m := re.FindString(s); m != "" {
					t.Errorf("%s holds %q", rel, m)
				}
			}
			for _, priv := range private {
				if strings.Contains(s, priv) {
					t.Errorf("%s names the home folder of this machine", rel)
				}
			}
			for w := range words {
				if regexp.MustCompile(`\b` + regexp.QuoteMeta(w) + `\b`).MatchString(s) {
					t.Errorf("%s names this machine or its user", rel)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 {
		t.Fatalf("only %d files in %s", n, ap14Dir)
	}
	// The script takes the user's name as an argument only.
	data, err := os.ReadFile(filepath.Join(root(t), ap14Script))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `NAME=$2`) {
		t.Error("the script does not take the user's name from --user")
	}
}

// The script is POSIX sh and shellcheck-clean, when shellcheck is installed;
// it parses as sh in any case, and is executable.
func TestAiHostAP14Shell(t *testing.T) {
	script := filepath.Join(root(t), ap14Script)
	if out, err := exec.Command("/bin/sh", "-n", script).CombinedOutput(); err != nil {
		t.Errorf("sh -n: %v\n%s", err, out)
	}
	info, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("%s is not executable", ap14Script)
	}
	sc, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck is not installed")
	}
	if out, err := exec.Command(sc, "-s", "sh", script).CombinedOutput(); err != nil {
		t.Errorf("shellcheck: %v\n%s", err, out)
	}
}
