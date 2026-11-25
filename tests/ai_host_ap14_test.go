package tests

// The checks of deploy/ai-host/ap14-level1.sh, the AP14 level-1 script for
// the AI hosts. No test runs it with --dry, and --apply or --verify only where
// it must be refused before anything is read (and never as root): every other
// run is --self-test, which reads the fake root a test writes (--fixtures) or
// the fixture folders beside the script, writes only temporary copies of them,
// and runs nothing on the host.

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	ap14Script = "deploy/ai-host/ap14-level1.sh"
	ap14Dir    = "deploy/ai-host"
	// ap14Delete, as the content of a file of ap14Root, leaves out a file the
	// fake root has by default.
	ap14Delete = "\x00delete"
)

// ap14Root writes files below a new fake root and returns it. The accounts
// are always there: agent (uid 1001, primary group agent 1001, in wheel 998)
// and other (uid 1002, in nothing), and agent has a usable password and a
// sudo -l that lists a grant asking for it, unless files says otherwise.
func ap14Root(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{
		"etc/passwd":   "root:x:0:0::/root:/bin/sh\nagent:x:1001:1001::/nonexistent:/bin/sh\nother:x:1002:1002::/nonexistent:/bin/sh\n",
		"etc/group":    "root:x:0:\nwheel:x:998:agent\nagent:x:1001:\nother:x:1002:\n",
		"passwd-S.txt": "agent P 2026-01-01 0 99999 7 -1\n",
		"sudo-l.txt":   "User agent may run the following commands on host:\n    (ALL : ALL) ALL\n",
	}
	for name, data := range files {
		all[name] = data
	}
	for name, data := range all {
		if data == ap14Delete {
			continue
		}
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
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

// ap14Report runs the self-test report of the fake root dir for the user and
// returns its output and exit status.
func ap14Report(t *testing.T, dir, name string) (string, int) {
	t.Helper()
	ap14Unprivileged(t)
	return ahRun(t, ap14Script, t.TempDir(), ap14Env(t), "--self-test", "--fixtures", dir, "--user", name)
}

// ap14Lines returns the lines of out that start with prefix.
func ap14Lines(out, prefix string) []string {
	var got []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			got = append(got, line)
		}
	}
	return got
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

// ap14Summary returns the summary line of a report.
func ap14Summary(t *testing.T, out string) string {
	t.Helper()
	got := ap14Lines(out, "changes: ")
	if len(got) != 1 {
		t.Fatalf("no summary line:\n%s", out)
	}
	return got[0]
}

// ap14Dropin is the drop-in the script writes for the user.
func ap14Dropin(name string) string {
	return "# AP14 level 1, written by ap14-level1.sh: sudo asks " + name + " for the password\n" +
		"# every time; no cached credentials.\n" +
		"Defaults:" + name + " timestamp_timeout=0\n"
}

// The built-in self-test passes from the repository root and from another
// folder: the audit of each fixture as expected, and --apply against
// temporary copies of the transaction cases, with a failure at every step. As
// root it refuses before it reads anything.
func TestAiHostAP14SelfTest(t *testing.T) {
	env := ap14Env(t)
	if os.Geteuid() == 0 {
		out, code := ahRun(t, ap14Script, t.TempDir(), env, "--self-test")
		if code != 2 || !strings.Contains(out, "--self-test runs unprivileged") {
			t.Errorf("as root: exit %d\n%s", code, out)
		}
		return
	}
	check := func(where, out string, code int) {
		t.Helper()
		if code != 0 || !strings.Contains(out, "self-test: ok, 13 checks; it read only the fixtures, wrote only temporary copies of them, and ran nothing from them") {
			t.Errorf("%s: exit %d\n%s", where, code, out)
		}
		for _, c := range []string{"clean", "routes", "stop", "sole", "shared", "mixed", "collision"} {
			if !strings.Contains(out, "self-test: ok "+c+" (audit)\n") {
				t.Errorf("%s: the audit of %s did not pass:\n%s", where, c, out)
			}
		}
		for _, c := range []string{"sole", "shared", "mixed", "collision"} {
			if !strings.Contains(out, "self-test: ok "+c+" (transaction: --apply refused, nothing written)\n") {
				t.Errorf("%s: --apply was not refused for %s:\n%s", where, c, out)
			}
		}
		if !regexp.MustCompile(`self-test: ok apply-new \(transaction of steps: (1[0-9]|[2-9][0-9]); each failure undone, each rerun recovers\)\n`).MatchString(out) {
			t.Errorf("%s: the transaction of apply-new did not pass with each of its steps failing:\n%s", where, out)
		}
	}
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
	// It left nothing in its temporary folder.
	for _, e := range env {
		if tmp, ok := strings.CutPrefix(e, "TMPDIR="); ok {
			if left, _ := os.ReadDir(tmp); len(left) != 0 {
				t.Errorf("the self-test left %d entries in its temporary folder", len(left))
			}
		}
	}
}

// The fixtures of a sole sudo grant, a grant shared with another account and
// a two-account rule that mixes passwordless and password-asking commands
// each stop the run: the owner gets the edit, and the warning that sudo would
// then refuse the user. Nothing is planned to move.
func TestAiHostAP14SoleSharedMixedStop(t *testing.T) {
	for _, c := range []string{"sole", "shared", "mixed"} {
		dir := filepath.Join(root(t), ap14Dir, "self-test", c)
		out, code := ap14Report(t, dir, "agent")
		if code != 1 {
			t.Errorf("%s: exit %d", c, code)
		}
		ap14Has(t, out,
			"stop: no other rule gives agent sudo with a password: once the passwordless rules are gone, sudo would refuse agent, not ask for a password\n",
			"by hand 1: /etc/sudoers.d/10-",
			"  do: as root, open the file with: visudo -f '/etc/sudoers.d/10-",
			"--apply refuses while a stop or a by-hand edit is listed",
		)
		if got := ap14Summary(t, out); !strings.Contains(got, "by hand: 1; stops: 1;") {
			t.Errorf("%s: %s", c, got)
		}
		ap14Lacks(t, out, "move ", "aside", "backup")
	}
	out, _ := ap14Report(t, filepath.Join(root(t), ap14Dir, "self-test", "shared"), "agent")
	ap14Has(t, out, "it also applies to others (agent,other), and the edit changes their sudo too")
}

// Sudoers: includes of every form, in sudo's order; aliases with negation;
// files of an includedir that sudo skips; where the drop-in would sit; what
// the audit cannot read is a stop.
func TestAiHostAP14SudoersIncludes(t *testing.T) {
	files := map[string]string{
		"etc/sudoers": "Defaults env_reset\n" +
			"User_Alias ADMINS = TEAM, !other\n" +
			"User_Alias TEAM = %wheel, other\n" +
			"#include /etc/sudoers.extra\n" +
			"@include local/one\n" +
			"@includedir /etc/sudoers.d\n",
		"etc/sudoers.extra":      "ADMINS ALL = (ALL) NOPASSWD: /usr/bin/a\n",
		"etc/local/one":          "Runas_Alias SVC = daemon, ROOTS\nRunas_Alias ROOTS = root\nagent ALL = (SVC) NOPASSWD: /usr/bin/b\n",
		"etc/sudoers.d/10-x":     "# only rule\nagent ALL=(ALL) NOPASSWD: ALL\n",
		"etc/sudoers.d/a.conf":   "agent ALL=(ALL) NOPASSWD: /ignored-dot\n",
		"etc/sudoers.d/b~":       "agent ALL=(ALL) NOPASSWD: /ignored-tilde\n",
		"etc/sudoers.d/zzz":      "@include /etc/sudoers.deep\n",
		"etc/sudoers.deep":       "%#998 ALL = NOPASSWD: /usr/bin/c\n",
		"etc/sudoers.d/30-other": "other ALL=(ALL) NOPASSWD: ALL\n",
	}
	dir := ap14Root(t, files)
	out, code := ap14Report(t, dir, "agent")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	ap14Has(t, out,
		"read: /etc/sudoers, /etc/sudoers.extra, /etc/local/one, /etc/sudoers.d/10-x, /etc/sudoers.d/30-other, /etc/sudoers.d/zzz, /etc/sudoers.deep\n",
		"rule: /etc/sudoers.extra:1: users ADMINS; applies to agent: yes; password: not asked (as root): ADMINS ALL = (ALL) NOPASSWD: /usr/bin/a\n",
		"rule: /etc/local/one:3: users agent; applies to agent: yes; password: not asked (as root): agent ALL = (SVC) NOPASSWD: /usr/bin/b\n",
		"rule: /etc/sudoers.d/10-x:2: users agent; applies to agent: yes; password: not asked (as root): agent ALL=(ALL) NOPASSWD: ALL\n",
		"rule: /etc/sudoers.deep:1: users %#998; applies to agent: yes; password: not asked (as root): %#998 ALL = NOPASSWD: /usr/bin/c\n",
		"rule: /etc/sudoers.d/30-other:1: users other; applies to agent: no; password: not asked (as root): other ALL=(ALL) NOPASSWD: ALL\n",
		"change 1: write /etc/sudoers.d/zz-ap14-level1-agent (a new file), mode 0440, owner root, with:\n",
		"    | Defaults:agent timestamp_timeout=0\n",
		"by hand 1: /etc/sudoers.extra:1: ADMINS ALL = (ALL) NOPASSWD: /usr/bin/a\n",
		"  do: as root, open the file with: visudo -f '/etc/sudoers.extra'\n",
		"by hand 2: /etc/local/one:3: ",
		"by hand 3: /etc/sudoers.d/10-x:2: ",
		"by hand 4: /etc/sudoers.deep:1: ",
		"--apply refuses while a stop or a by-hand edit is listed",
	)
	ap14Lacks(t, out, "/ignored-dot", "/ignored-tilde", "by hand 5:", "aside")
	if got := ap14Lines(out, "rule: "); len(got) != 5 {
		t.Errorf("want 5 rules, got %q", got)
	}

	// The negated member of the alias leaves other out; its own rule counts.
	out, _ = ap14Report(t, dir, "other")
	ap14Has(t, out, "rule: /etc/sudoers.d/30-other:1: users other; applies to other: yes; password: not asked (as root): other ALL=(ALL) NOPASSWD: ALL\n",
		"by hand 1: /etc/sudoers.d/30-other:1: ",
		"rule: /etc/sudoers.extra:1: users ADMINS; applies to other: no;")
	ap14Lacks(t, out, "by hand 2:")

	// Without an includedir of /etc/sudoers.d the drop-in would not be read.
	dir = ap14Root(t, map[string]string{"etc/sudoers": "root ALL=(ALL) ALL\n@includedir /etc/elsewhere\n", "etc/elsewhere/x": "root ALL=(ALL) ALL\n"})
	out, _ = ap14Report(t, dir, "agent")
	ap14Has(t, out, "stop: /etc/sudoers does not read /etc/sudoers.d (no includedir for it): the drop-in would have no effect\n")
	ap14Lacks(t, out, "change 1: write /etc/sudoers.d/zz-ap14-level1-agent")

	// What the audit cannot read is a stop, never a clean result: an include
	// loop, a missing include or includedir, one naming %h, one with a ..
	// part.
	for _, c := range []struct{ sudoers, want string }{
		{"@include /etc/loop\n", "stop: /etc/loop:0: includes nested more than 16 deep; not followed: the audit cannot tell what sudo reads there\n"},
		{"@include /etc/missing\n", "stop: /etc/missing:0: not a readable file; not read: the audit cannot tell what sudo reads there\n"},
		{"@includedir /etc/nothing\n", "stop: /etc/sudoers:1: includedir names a missing or unreadable folder; not read"},
		{"@include /etc/sudoers.%h\n", "stop: /etc/sudoers:1: an include names %h (the host name); not followed"},
		{"@include ../outside\n", "stop: /etc/sudoers:1: an include path with a .. part; not followed"},
		{"@include /etc/a/../loop\n", "stop: /etc/sudoers:1: an include path with a .. part; not followed"},
	} {
		dir = ap14Root(t, map[string]string{"etc/sudoers": c.sudoers + "%wheel ALL=(ALL) ALL\n@includedir /etc/sudoers.d\n", "etc/loop": "@include /etc/loop\n",
			"etc/sudoers.d/zz-ap14-level1-agent": ap14Dropin("agent"), "modes.txt": "/etc/sudoers.d/zz-ap14-level1-agent 0440\n", "etc/ataniq/grants/README": "x\n"})
		out, code = ap14Report(t, dir, "agent")
		ap14Has(t, out, c.want)
		if code != 1 || !strings.Contains(ap14Summary(t, out), "stops: 1;") {
			t.Errorf("%q: exit %d, %s", c.sudoers, code, ap14Summary(t, out))
		}
	}

	// A cache time set after the drop-in, or for a command or runas user
	// anywhere, is a finding; a general one before it is not.
	dir = ap14Root(t, map[string]string{
		"etc/sudoers": "Defaults timestamp_timeout=15\nDefaults!/usr/bin/x timestamp_timeout=7\nDefaults>root timestamp_timeout=8\nDefaults!/usr/bin/y timestamp_timeout=0\n" +
			"@includedir /etc/sudoers.d\nDefaults:agent timestamp_timeout=5\nDefaults:other timestamp_timeout=9\n",
		"etc/sudoers.d/00-a":  "Defaults timestamp_timeout=20\n",
		"etc/sudoers.d/zzz-b": "Defaults:%wheel timestamp_timeout=-1\n",
		"etc/sudoers.d/zzz-c": "Defaults timestamp_timeout=0\n",
	})
	out, _ = ap14Report(t, dir, "agent")
	ap14Has(t, out,
		"finding: sudo-timeout: /etc/sudoers:6: sets timestamp_timeout=5 after the drop-in",
		"finding: sudo-timeout: /etc/sudoers.d/zzz-b:1: sets timestamp_timeout=-1 after the drop-in",
		"finding: sudo-timeout: /etc/sudoers:2: sets timestamp_timeout=7 for a command or runas user, which sudo applies after the drop-in wherever it sits",
		"finding: sudo-timeout: /etc/sudoers:3: sets timestamp_timeout=8 for a command or runas user",
	)
	ap14Lacks(t, out, "timestamp_timeout=15 after", "timestamp_timeout=20 after", "timestamp_timeout=9 after", "timestamp_timeout=0 ")
}

// Each rule form, alone in a file of /etc/sudoers.d: who it gives sudo,
// whether it asks for a password, and the edit by hand for each one that
// gives agent sudo without one. Nothing is moved.
func TestAiHostAP14SudoersNopasswd(t *testing.T) {
	cases := []struct {
		rule string
		// want: the rule line's "applies; password" part, "" for no rule
		// line, or noauth, exempt or exempt-no; hand: whether it is an edit
		// by hand.
		want string
		hand bool
	}{
		{"agent ALL=(ALL) NOPASSWD: ALL", "yes; password: not asked (as root)", true},
		{"agent ALL=(ALL:ALL) NOPASSWD:ALL", "yes; password: not asked (as root)", true},
		{"agent ALL=NOPASSWD: /bin/a", "yes; password: not asked (as root)", true},
		{"agent ALL = (root) NOPASSWD: /bin/a, PASSWD: /bin/b", "yes; password: not asked for some commands (as root)", true},
		{"agent ALL = (root) PASSWD: /bin/b, NOPASSWD: /bin/a", "yes; password: not asked for some commands (as root)", true},
		{"agent ALL = (root) PASSWD: /bin/b, /bin/c", "yes; password: asked", false},
		{"agent ALL = (root) /bin/b", "yes; password: asked", false},
		{"agent ALL = (root) NOPASSWD: NOEXEC: /bin/a", "yes; password: not asked (as root)", true},
		{"agent ALL = (root) NOEXEC:NOPASSWD: /bin/a", "yes; password: not asked (as root)", true},
		{"agent ALL = (root) NOPASSWD: PASSWD: /bin/a", "yes; password: asked", false},
		{"agent ALL = (postgres) NOPASSWD: /bin/psql", "yes; password: not asked (as other)", true},
		{"agent ALL = (: wheel) NOPASSWD: /bin/x", "yes; password: not asked (as other)", true},
		{"agent ALL = (postgres) /bin/psql, (root) NOPASSWD: /bin/y", "yes; password: not asked for some commands (as root)", true},
		{"agent ALL = (root) NOPASSWD: /bin/y, (postgres) /bin/psql", "yes; password: not asked (as root)", true},
		{"agent ALL = (postgres) NOPASSWD: /bin/psql, (root) PASSWD: /bin/y", "yes; password: not asked for some commands (as other)", true},
		{"agent host1 = NOPASSWD: /a : host2 = PASSWD: /b", "yes; password: not asked for some commands (as root)", true},
		{"agent host1 = PASSWD: /a : host2 = NOPASSWD: /b", "yes; password: not asked for some commands (as root)", true},
		{"agent host1 = NOPASSWD: /a : host2 = /b", "yes; password: not asked for some commands (as root)", true},
		{"agent ALL = NOPASSWD: \\\n    /bin/a", "yes; password: not asked (as root)", true},
		{"agent ALL = CWD=/tmp NOPASSWD: /bin/a", "yes; password: not asked (as root)", true},
		{"agent ALL=(ALL) NOPASSWD: ALL, !/usr/bin/su", "yes; password: not asked (as root)", true},
		{"agent ALL=(ALL) ALL, NOPASSWD: !/usr/bin/su", "yes; password: asked", false},
		{"ALL ALL=(ALL) NOPASSWD: ALL", "yes; password: not asked (as root)", true},
		{"%wheel ALL=(ALL) NOPASSWD: ALL", "yes; password: not asked (as root)", true},
		{"%#998 ALL=(ALL) NOPASSWD: ALL", "yes; password: not asked (as root)", true},
		{"%#1001 ALL=(ALL) NOPASSWD: ALL", "yes; password: not asked (as root)", true},
		{"#1001 ALL=(ALL) NOPASSWD: ALL", "yes; password: not asked (as root)", true},
		{`"agent" ALL=(ALL) NOPASSWD: ALL`, "yes; password: not asked (as root)", true},
		{"other, agent ALL=(ALL) NOPASSWD: ALL", "yes; password: not asked (as root)", true},
		{"other ALL=(ALL) NOPASSWD: ALL", "no; password: not asked (as root)", false},
		{"%other ALL=(ALL) NOPASSWD: ALL", "no; ", false},
		{"%#1002 ALL=(ALL) NOPASSWD: ALL", "no; ", false},
		{"#1002 ALL=(ALL) NOPASSWD: ALL", "no; ", false},
		{"agentx ALL=(ALL) NOPASSWD: ALL", "no; ", false},
		{"ALL, !agent ALL=(ALL) NOPASSWD: ALL", "no; ", false},
		{"!agent, ALL ALL=(ALL) NOPASSWD: ALL", "yes; password: not asked (as root)", true},
		{"%:Domain_Admins ALL=(ALL) NOPASSWD: ALL", "maybe; password: not asked (as root)", true},
		{"+admins ALL=(ALL) NOPASSWD: ALL", "maybe; password: not asked (as root)", true},
		{"# agent ALL=(ALL) NOPASSWD: ALL", "", false},
		{"agent ALL=(ALL) ALL # NOPASSWD: in a comment", "yes; password: asked", false},
		{"Defaults:agent !authenticate", "noauth", true},
		{"Defaults !authenticate", "noauth", true},
		{"Defaults:%wheel !lecture, !authenticate", "noauth", true},
		{"Defaults!/usr/bin/x !authenticate", "noauth", true},
		{"Defaults:other !authenticate", "", false},
		{"Defaults:agent !requiretty", "", false},
		{"Defaults exempt_group=wheel", "exempt", true},
		{"Defaults exempt_group=\"%wheel\"", "exempt", true},
		{"Defaults exempt_group=docker", "exempt-no", false},
		{"Cmnd_Alias NOPASS = /bin/a", "", false},
	}
	files := map[string]string{"etc/sudoers": "root ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n"}
	name := func(i int) string { return "f" + string(rune('a'+i/26)) + string(rune('a'+i%26)) }
	for i, c := range cases {
		files["etc/sudoers.d/"+name(i)] = c.rule + "\n"
	}
	out, _ := ap14Report(t, ap14Root(t, files), "agent")
	hand := ap14Lines(out, "by hand ")
	for i, c := range cases {
		prefix := "rule: /etc/sudoers.d/" + name(i) + ":1: "
		got := ap14Lines(out, prefix)
		switch c.want {
		case "":
			if len(got) != 0 {
				t.Errorf("%q: unexpected %q", c.rule, got)
			}
		case "noauth":
			if len(got) != 1 || !strings.Contains(got[0], ": Defaults !authenticate for ") || !strings.Contains(got[0], "; password: not asked: ") {
				t.Errorf("%q: want a Defaults !authenticate rule, got %q", c.rule, got)
			}
		case "exempt", "exempt-no":
			applies := "yes"
			if c.want == "exempt-no" {
				applies = "no"
			}
			if len(got) != 1 || !strings.Contains(got[0], "(its members never type a password); applies to agent: "+applies+": ") {
				t.Errorf("%q: want an exempt_group rule that applies %s, got %q", c.rule, applies, got)
			}
		default:
			if len(got) != 1 || !strings.Contains(got[0], "; applies to agent: "+c.want) {
				t.Errorf("%q: want %q, got %q", c.rule, c.want, got)
			}
		}
		found := false
		for _, h := range hand {
			if strings.Contains(h, ": /etc/sudoers.d/"+name(i)+":1: ") {
				found = true
			}
		}
		if found != c.hand {
			t.Errorf("%q: an edit by hand %v, want %v", c.rule, found, c.hand)
		}
	}
	ap14Has(t, out, "why: it may give agent sudo without a password (+admins is a netgroup or non-Unix group: check whether agent is in it)")
	ap14Lacks(t, out, "aside", "backup", "move ")
}

// The warning that sudo would refuse the user once the passwordless rules are
// gone: it stays away only while another rule, which itself asks for the
// password, lets the user run ALL as root.
func TestAiHostAP14SudoersLockout(t *testing.T) {
	const stop = "stop: no other rule gives agent sudo with a password"
	for _, c := range []struct {
		other string
		stop  bool
	}{
		{"", true},
		{"agent ALL=(ALL:ALL) ALL\n", false},
		{"%wheel ALL=(ALL) ALL\n", false},
		{"agent ALL=(root) ALL\n", false},
		{"agent ALL=(postgres) ALL\n", true},
		{"agent ALL=(ALL) /usr/bin/b\n", true},
		{"agent ALL=(ALL) ALL, NOPASSWD: /usr/bin/b\n", true},
		{"+admins ALL=(ALL) ALL\n", true},
		{"other ALL=(ALL) ALL\n", true},
	} {
		files := map[string]string{
			"etc/sudoers":         "root ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n",
			"etc/sudoers.d/10-np": "agent ALL=(ALL) NOPASSWD: ALL\n",
		}
		if c.other != "" {
			files["etc/sudoers.d/20-pw"] = c.other
		}
		out, _ := ap14Report(t, ap14Root(t, files), "agent")
		if got := strings.Contains(out, stop); got != c.stop {
			t.Errorf("%q: the lockout stop %v, want %v:\n%s", c.other, got, c.stop, out)
		}
		ap14Has(t, out, "by hand 1: /etc/sudoers.d/10-np:1: ")
	}
	// No passwordless rule: no edit and no lockout stop, though the user has
	// no sudo at all.
	out, code := ap14Report(t, ap14Root(t, map[string]string{"etc/sudoers": "root ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n", "etc/sudoers.d/.keep": "",
		"sudo-l.txt": "User agent is not allowed to run sudo on host.\n", "sudo-l.rc": "1\n"}), "agent")
	ap14Has(t, out, "no rule gives agent sudo: sudo refuses agent (the drop-in changes nothing for it)\n",
		"sudo -l -U agent: agent is not allowed to run sudo\n",
		"nothing: no rule gives agent sudo without a password\n")
	ap14Lacks(t, out, stop)
	if code != 1 || !strings.Contains(ap14Summary(t, out), "by hand: 0; stops: 0;") {
		t.Errorf("exit %d, %s", code, ap14Summary(t, out))
	}
}

// What sudo itself says: a failed query is a stop, a passwordless command it
// lists that the files do not explain is a stop, a later cache time a finding.
func TestAiHostAP14SudoList(t *testing.T) {
	base := map[string]string{"etc/sudoers": "root ALL=(ALL:ALL) ALL\n%wheel ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n", "etc/sudoers.d/.keep": ""}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"sudo-l.txt": ap14Delete}, "stop: sudo -l -U agent failed (exit 127): the effective policy is unknown: (no sudo-l.txt in the fixtures)\n"},
		{map[string]string{"sudo-l.txt": "sudo: unable to resolve host\n", "sudo-l.rc": "1\n"}, "stop: sudo -l -U agent failed (exit 1): the effective policy is unknown: sudo: unable to resolve host\n"},
		{map[string]string{"sudo-l.txt": "User agent is not allowed to run sudo on host.\n", "sudo-l.rc": "1\n"}, "stop: sudo -l -U agent says agent may not run sudo, but the files hold rules for it: the audit and sudo disagree\n"},
		{map[string]string{"sudo-l.txt": "User agent may run the following commands on host:\n    (root) NOPASSWD: /usr/bin/a\n"}, "stop: sudo -l -U agent lists (root) NOPASSWD: /usr/bin/a, which needs no password, but the files read above hold no such rule for agent\n"},
		{map[string]string{"sudo-l.txt": "Matching Defaults entries for agent on host:\n    env_reset, !authenticate\n"}, "stop: sudo -l -U agent lists !authenticate, which needs no password"},
	} {
		out, code := ap14Report(t, ap14Root(t, with(c.files)), "agent")
		ap14Has(t, out, c.want)
		if code != 1 {
			t.Errorf("%q: exit %d", c.want, code)
		}
	}
	out, _ := ap14Report(t, ap14Root(t, with(map[string]string{"sudo-l.txt": "Matching Defaults entries for agent on host:\n    env_reset, timestamp_timeout=0,\n    timestamp_timeout=10\n"})), "agent")
	ap14Has(t, out, "finding: sudo-l: sudo -l -U agent shows timestamp_timeout=10 last: credentials are cached\n")
	ap14Lacks(t, out, "stop: sudo -l")
}

// --self-test --fixtures reads only below the fixture folder: / is refused, a
// link that leads out of it is refused before anything is read, a link inside
// it is fine, an include with a .. part is not followed, and a name with a
// newline is refused.
func TestAiHostAP14FixtureConfinement(t *testing.T) {
	ap14Unprivileged(t)
	env := ap14Env(t)
	if out, code := ahRun(t, ap14Script, t.TempDir(), env, "--self-test", "--fixtures", "/", "--user", "agent"); code != 2 || !strings.Contains(out, "--fixtures must not be /") {
		t.Errorf("/: exit %d\n%s", code, out)
	}
	if out, code := ahRun(t, ap14Script, t.TempDir(), env, "--self-test", "--fixtures", t.TempDir()+"/missing", "--user", "agent"); code != 2 || !strings.Contains(out, "--fixtures must name a folder") {
		t.Errorf("missing: exit %d\n%s", code, out)
	}
	// A file outside the fixtures that a link or a .. include would read.
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("agent ALL=(ALL) NOPASSWD: /OUTSIDE-RULE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sudoers := "root ALL=(ALL:ALL) ALL\n%wheel ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n"
	for _, link := range []string{"etc/sudoers.d/10-link", "etc/elsewhere"} {
		dir := ap14Root(t, map[string]string{"etc/sudoers": sudoers, "etc/sudoers.d/.keep": ""})
		if err := os.Symlink(outside, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
		out, code := ahRun(t, ap14Script, t.TempDir(), env, "--self-test", "--fixtures", dir, "--user", "agent")
		if code != 2 || !strings.Contains(out, "refused: a link in the fixtures leads out of them (or cannot be resolved): /"+link) {
			t.Errorf("%s: exit %d\n%s", link, code, out)
		}
		ap14Lacks(t, out, "OUTSIDE-RULE", "== sudo")
	}
	// A link to a file of the fixtures is fine, and read as sudo would.
	dir := ap14Root(t, map[string]string{"etc/sudoers": sudoers + "@include /etc/inside\n", "etc/real": "agent ALL=(ALL) NOPASSWD: /INSIDE-RULE\n", "etc/sudoers.d/.keep": ""})
	if err := os.Symlink("real", filepath.Join(dir, "etc/inside")); err != nil {
		t.Fatal(err)
	}
	out, _ := ap14Report(t, dir, "agent")
	ap14Has(t, out, "rule: /etc/inside:1: users agent; applies to agent: yes; password: not asked (as root): agent ALL=(ALL) NOPASSWD: /INSIDE-RULE\n")
	// An include with a .. part is not followed.
	dir = ap14Root(t, map[string]string{"etc/sudoers": sudoers + "@include ../../" + filepath.Base(filepath.Dir(outside)) + "/outside\n", "etc/sudoers.d/.keep": ""})
	out, _ = ap14Report(t, dir, "agent")
	ap14Has(t, out, "stop: /etc/sudoers:4: an include path with a .. part; not followed")
	ap14Lacks(t, out, "OUTSIDE-RULE")
	// A name with a newline.
	dir = ap14Root(t, map[string]string{"etc/sudoers": sudoers, "etc/sudoers.d/a\nb": "x\n"})
	if out, code := ahRun(t, ap14Script, t.TempDir(), env, "--self-test", "--fixtures", dir, "--user", "agent"); code != 2 || !strings.Contains(out, "a file name in the fixtures holds a newline or a tab") {
		t.Errorf("newline: exit %d\n%s", code, out)
	}
}

// Polkit: what a YES applies to, comments, a file of /etc hiding the one of
// the same name in /usr/share.
func TestAiHostAP14Polkit(t *testing.T) {
	rule := func(body string) string {
		return "polkit.addRule(function(action, subject) {\n" + body + "\n});\n"
	}
	files := map[string]string{
		"etc/polkit-1/rules.d/10-group.rules": rule(`    if (action.id == "org.example.a" && subject.isInGroup("wheel")) {
        return polkit.Result.YES;
    }`),
		"etc/polkit-1/rules.d/11-user.rules":       rule(`    if (subject.user === 'agent') return polkit.Result.YES;`),
		"etc/polkit-1/rules.d/12-other.rules":      rule(`    if (subject.user == "other") return polkit.Result.YES;`),
		"etc/polkit-1/rules.d/13-anyone.rules":     rule(`    if (action.id == "org.example.d" && subject.local) return polkit.Result.YES;`),
		"etc/polkit-1/rules.d/14-variable.rules":   "var g = \"wheel\";\n" + rule(`    if (subject.isInGroup(g)) return polkit.Result.YES;`),
		"etc/polkit-1/rules.d/15-negated.rules":    rule(`    if (subject.user != "other") return polkit.Result.YES;`),
		"etc/polkit-1/rules.d/16-comment.rules":    rule("    // return polkit.Result.YES;\n    /* return polkit.Result.YES;\n       polkit.Result.YES */\n    return polkit.Result.NOT_HANDLED;"),
		"etc/polkit-1/rules.d/17-admin.rules":      rule(`    if (subject.isInGroup("wheel")) return polkit.Result.AUTH_ADMIN;`),
		"etc/polkit-1/rules.d/18-list.rules":       rule(`    if (["other", "agent"].indexOf(subject.user) >= 0) return polkit.Result.YES;`),
		"etc/polkit-1/rules.d/19-others.rules":     rule(`    if (["other", "third"].indexOf(subject.user) >= 0) return polkit.Result.YES;`),
		"etc/polkit-1/rules.d/README":              "polkit.Result.YES\n",
		"etc/polkit-1/rules.d/50-hide.rules":       rule(`    return polkit.Result.NO;`),
		"usr/share/polkit-1/rules.d/50-hide.rules": rule(`    return polkit.Result.YES;`),
		"usr/share/polkit-1/rules.d/60-shown.rules": rule(`    if (subject.isInGroup("wheel"))
        return polkit.Result.YES;`),
		"usr/share/polkit-1/rules.d/70-admin.rules": "polkit.addAdminRule(function(action, subject) {\n    return [\"unix-group:wheel\"];\n});\n",
		"usr/share/polkit-1/rules.d/80-loose.rules": "function f() { return polkit.Result.YES; }\n",
	}
	out, _ := ap14Report(t, ap14Root(t, files), "agent")
	const (
		any    = "returns YES without authentication for any user"
		match  = "returns YES without authentication for agent or one of its groups"
		unsure = "returns YES without authentication, for users the script cannot tell; read it"
	)
	want := []string{
		"finding: polkit: /etc/polkit-1/rules.d/10-group.rules:3: " + match + " (actions: org.example.a)",
		"finding: polkit: /etc/polkit-1/rules.d/11-user.rules:2: " + match + " (actions: -)",
		"finding: polkit: /etc/polkit-1/rules.d/13-anyone.rules:2: " + any + " (actions: org.example.d)",
		"finding: polkit: /etc/polkit-1/rules.d/14-variable.rules:3: " + unsure,
		"finding: polkit: /etc/polkit-1/rules.d/15-negated.rules:2: " + unsure,
		"finding: polkit: /etc/polkit-1/rules.d/18-list.rules:2: " + match,
		"finding: polkit: /usr/share/polkit-1/rules.d/60-shown.rules:3: " + match,
		"finding: polkit: /usr/share/polkit-1/rules.d/80-loose.rules:1: " + unsure,
	}
	ap14Has(t, out, want...)
	if got := ap14Lines(out, "finding: polkit: "); len(got) != len(want) {
		t.Errorf("want %d polkit findings, got %d: %q", len(want), len(got), got)
	}
	ap14Lacks(t, out, "12-other.rules", "16-comment.rules", "17-admin.rules", "19-others.rules", "50-hide.rules", "70-admin.rules", "rules.d/README")

	// For a user outside wheel the group rules do not apply.
	out, _ = ap14Report(t, ap14Root(t, files), "other")
	ap14Lacks(t, out, "10-group.rules", "60-shown.rules", "11-user.rules")
	ap14Has(t, out, "finding: polkit: /etc/polkit-1/rules.d/12-other.rules:2: returns YES without authentication for other or one of its groups")
}

// Group lists: the primary group, member lists (no substring matches), the
// root-equivalent groups and gid 0.
func TestAiHostAP14Groups(t *testing.T) {
	files := map[string]string{
		"etc/passwd": "root:x:0:0::/root:/bin/sh\nagent:x:1001:1500::/nonexistent:/bin/sh\n",
		"etc/group": "root:x:0:agent\n" +
			"docker:x:970:other,agent\n" +
			"lxd:x:971:agent\n" +
			"incus:x:972:agent\n" +
			"incus-admin:x:973:agent\n" +
			"libvirt:x:974:agent,other\n" +
			"disk:x:6:agent\n" +
			"kmem:x:9:agent\n" +
			"shadow:x:42:agent\n" +
			"input:x:97:agent\n" +
			"video:x:985:agent\n" +
			"wheel:x:998:agent\n" +
			"agents:x:2000:agent2,xagent,agentx\n" +
			"podman:x:2001:\n" +
			"devs:x:1500:\n",
	}
	out, _ := ap14Report(t, ap14Root(t, files), "agent")
	line := ap14Lines(out, "user: agent, uid 1001; groups: ")
	if len(line) != 1 {
		t.Fatalf("no user line:\n%s", out)
	}
	got := strings.Fields(strings.TrimPrefix(line[0], "user: agent, uid 1001; groups: "))
	slices.Sort(got)
	wantGroups := []string{"devs", "disk", "docker", "incus", "incus-admin", "input", "kmem", "libvirt", "lxd", "root", "shadow", "video", "wheel"}
	if !slices.Equal(got, wantGroups) {
		t.Errorf("groups %q, want %q", got, wantGroups)
	}
	for _, g := range []string{"docker", "lxd", "incus", "incus-admin", "libvirt", "disk", "kmem", "shadow", "input"} {
		ap14Has(t, out, "finding: group: agent is in "+g+": ", "  remedy: gpasswd -d agent "+g+" (it takes effect at the next login)")
	}
	ap14Has(t, out,
		"finding: group: agent is in the group with gid 0: ",
		"note: agent is in wheel: sudo and polkit admin actions ask for agent's password",
	)
	ap14Lacks(t, out, "agent is in video:", "agent is in agents", "agent is in devs:", "agent is in podman")
	expr := ap14Lines(out, "note: user-writable test: ")
	if len(expr) != 1 {
		t.Fatalf("no writable test line:\n%s", out)
	}
	for _, w := range []string{"-user 1001 ", "-perm -0002", "-perm -0020", "-group 1500 ", "-group 0 ", "-group 970 ", "-group 985 "} {
		if !strings.Contains(expr[0], w) {
			t.Errorf("the writable test %q lacks %q", expr[0], w)
		}
	}
	if strings.Contains(expr[0], "-group 2000") || strings.Contains(expr[0], "-group 2001") {
		t.Errorf("the writable test names a group agent is not in: %q", expr[0])
	}
	// A user the files do not know is refused.
	if out, code := ap14Report(t, ap14Root(t, files), "nobody"); code != 2 || !strings.Contains(out, "there is no user named nobody") {
		t.Errorf("unknown user: exit %d\n%s", code, out)
	}
	// A user with uid 0 is refused.
	zero := ap14Root(t, map[string]string{"etc/passwd": "toor:x:0:0::/root:/bin/sh\n"})
	if out, code := ap14Report(t, zero, "toor"); code != 2 || !strings.Contains(out, "has uid 0") {
		t.Errorf("uid 0: exit %d\n%s", code, out)
	}
}

// doas.conf: permit rules with nopass or persist for the user or its groups.
func TestAiHostAP14Doas(t *testing.T) {
	files := map[string]string{
		"etc/doas.conf": "# doas\n" +
			"permit nopass agent\n" +
			"permit persist :wheel\n" +
			"permit agent as root cmd /usr/bin/true\n" +
			"permit nopass other\n" +
			"permit nopass :docker\n" +
			"permit setenv { FOO=1 BAR } nopass keepenv agent as root\n" +
			"deny agent\n" +
			"  permit nolog nopass \\\n    :1001 cmd /bin/x  # a comment\n" +
			"# permit nopass agent\n",
		"usr/local/etc/doas.conf": "permit persist agent\n",
	}
	out, _ := ap14Report(t, ap14Root(t, files), "agent")
	want := []string{
		"finding: doas: /etc/doas.conf:2: permits agent with nopass: permit nopass agent\n",
		"finding: doas: /etc/doas.conf:3: permits agent with persist: permit persist :wheel\n",
		"finding: doas: /etc/doas.conf:7: permits agent with nopass: permit setenv { FOO=1 BAR } nopass keepenv agent as root\n",
		"finding: doas: /etc/doas.conf:9: permits agent with nopass: permit nolog nopass :1001 cmd /bin/x\n",
		"finding: doas: /usr/local/etc/doas.conf:1: permits agent with persist: permit persist agent\n",
	}
	ap14Has(t, out, want...)
	ap14Has(t, out, "  remedy: remove nopass from the rule in /etc/doas.conf")
	if got := ap14Lines(out, "finding: doas: "); len(got) != len(want) {
		t.Errorf("want %d doas findings, got %q", len(want), got)
	}
}

// Setuid files, root's PATH, writable paths and access control lists, with
// their remedies' operands quoted.
func TestAiHostAP14OtherRoutes(t *testing.T) {
	files := map[string]string{
		"proc/self/mounts": "/dev/vda2 / ext4 rw 0 0\n" +
			"/dev/vda3 /home ext4 rw,nosuid 0 0\n" +
			"/dev/vda4 /srv xfs rw 0 0\n" +
			"/dev/vda4 /srv xfs rw 0 0\n" +
			"server:/x /net nfs rw 0 0\n" +
			"/dev/vda5 /a\\040b ext4 rw 0 0\n" +
			"/dev/vda2 /.snapshots btrfs rw 0 0\n" +
			"overlay /var/lib/docker/overlay2/x/merged overlay rw 0 0\n",
		"setuid.txt":    "/usr/bin/sudo\n/usr/bin/passwd\n/opt/tool/helper\n/usr/bin/sudo\n/opt/it's\n",
		"packaged.txt":  "/usr/bin/sudo\n/usr/bin/passwd\n",
		"root-path.txt": "/usr/bin::.:bin:/usr/local/bin",
		"writable.txt":  "/usr/local/bin\n/etc/cron.d/job\n",
		"getfacl.txt": "# file: /etc/a\nuser::rw-\nuser:agent:rw-\ngroup::r--\nmask::rw-\nother::r--\n\n" +
			"# file: /etc/b\nuser::rw-\nuser:1001:rwx\t#effective:r--\nmask::r-x\nother::r--\n\n" +
			"# file: /etc/c\nuser::rw-\ngroup:wheel:rw-\nmask::rwx\nother::r--\n\n" +
			"# file: /etc/d\nuser::rw-\ngroup:other:rw-\nuser:other:rw-\nmask::rwx\nother::r--\n" +
			"default:user:agent:rwx\n",
	}
	out, _ := ap14Report(t, ap14Root(t, files), "agent")
	ap14Has(t, out,
		"note: setuid search covers: / /srv\n",
		"note: setuid search leaves out /a\\040b\n",
		"note: setuid search leaves out /.snapshots\n",
		"finding: setuid: /opt/tool/helper is setuid or setgid root and no package owns it\n  remedy: remove it, or chmod u-s,g-s '/opt/tool/helper', unless",
		`chmod u-s,g-s '/opt/it'\''s'`,
		"finding: root-path: root's PATH holds '', a relative or empty entry",
		"finding: root-path: root's PATH holds '.', a relative or empty entry",
		"finding: root-path: root's PATH holds 'bin', a relative or empty entry",
		"finding: writable: agent may write /usr/local/bin, which root runs",
		"  remedy: chown root:root '/usr/local/bin' and chmod go-w '/usr/local/bin'",
		"finding: writable: agent may write /etc/cron.d/job, which root runs",
		"finding: acl: /etc/a grants user:agent:rw-: agent may write it\n  remedy: setfacl -x 'user:agent' '/etc/a'\n",
		"finding: acl: /etc/c grants group:wheel:rw-: agent may write it\n",
	)
	ap14Lacks(t, out, "/usr/bin/sudo is setuid", "/usr/bin/passwd is setuid", "/net", "overlay2", "/etc/b grants", "/etc/d grants", "default:user")
	if got := ap14Lines(out, "finding: setuid: "); len(got) != 2 {
		t.Errorf("want two setuid findings, got %q", got)
	}

	// Without a package query every setuid file is listed, once noted.
	delete(files, "packaged.txt")
	out, _ = ap14Report(t, ap14Root(t, files), "agent")
	ap14Has(t, out, "note: no package query (pacman, dpkg, rpm) here: every setuid file is listed\n",
		"finding: setuid: /usr/bin/sudo is setuid or setgid root; its package is unknown\n")
	if got := ap14Lines(out, "note: no package query"); len(got) != 1 {
		t.Errorf("the note is printed %d times", len(got))
	}
}

// The plan: the password, the drop-in (new, exactly ours, or someone else's),
// the register and each of its entries, a folder someone else owns.
func TestAiHostAP14Plan(t *testing.T) {
	base := map[string]string{
		"etc/sudoers":              "%wheel ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n",
		"etc/sudoers.d/10-a":       "agent ALL=(ALL) NOPASSWD: ALL\n",
		"etc/ataniq/grants/README": "x\n",
	}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, st := range []string{"L", "LK", "NP"} {
		out, _ := ap14Report(t, ap14Root(t, with(map[string]string{"passwd-S.txt": "agent " + st + " 2026-01-01\n"})), "agent")
		ap14Has(t, out, "stop: agent has no usable password (passwd -S says "+st+")")
	}
	// An unknown password status is a stop.
	for _, pw := range []string{ap14Delete, "agent ? 2026-01-01\n", ""} {
		out, _ := ap14Report(t, ap14Root(t, with(map[string]string{"passwd-S.txt": pw})), "agent")
		ap14Has(t, out, "stop: passwd -S did not say whether agent has a usable password")
	}
	out, _ := ap14Report(t, ap14Root(t, with(map[string]string{"passwd-S.txt": "agent PS 2026-01-01\n"})), "agent")
	ap14Has(t, out, "agent has a usable password: sudo can still ask for it\n",
		"no change: the standing-grants register /etc/ataniq/grants exists, owned by root, and every entry is root's\n")
	ap14Lacks(t, out, "stop: ")

	// The drop-in: exactly the script's (text, owner, mode 0440) is no
	// change; anything else at that name is a stop, never replaced.
	out, _ = ap14Report(t, ap14Root(t, with(map[string]string{"etc/sudoers.d/zz-ap14-level1-agent": ap14Dropin("agent"), "modes.txt": "/etc/sudoers.d/zz-ap14-level1-agent 0440\n"})), "agent")
	ap14Has(t, out, "no change: /etc/sudoers.d/zz-ap14-level1-agent already says: Defaults:agent timestamp_timeout=0 (owner root, mode 0440)\n")
	collision := "stop: /etc/sudoers.d/zz-ap14-level1-agent exists and is not the file this script writes (its text, owner root, mode 0440): the script never replaces it\n"
	for name, extra := range map[string]map[string]string{
		"other text":                    {"etc/sudoers.d/zz-ap14-level1-agent": "Defaults:agent timestamp_timeout=5\n", "modes.txt": "/etc/sudoers.d/zz-ap14-level1-agent 0440\n"},
		"a restriction of someone else": {"etc/sudoers.d/zz-ap14-level1-agent": "Defaults:other !lecture\n", "modes.txt": "/etc/sudoers.d/zz-ap14-level1-agent 0440\n"},
		"mode 0644":                     {"etc/sudoers.d/zz-ap14-level1-agent": ap14Dropin("agent"), "modes.txt": "/etc/sudoers.d/zz-ap14-level1-agent 0644\n"},
		"no mode set":                   {"etc/sudoers.d/zz-ap14-level1-agent": ap14Dropin("agent")},
		"not root's":                    {"etc/sudoers.d/zz-ap14-level1-agent": ap14Dropin("agent"), "modes.txt": "/etc/sudoers.d/zz-ap14-level1-agent 0440\n", "not-root.txt": "/etc/sudoers.d/zz-ap14-level1-agent\n"},
	} {
		out, _ = ap14Report(t, ap14Root(t, with(extra)), "agent")
		if !strings.Contains(out, collision) {
			t.Errorf("%s: no collision stop:\n%s", name, out)
		}
	}
	out, _ = ap14Report(t, ap14Root(t, with(map[string]string{"etc/sudoers.d/zz-ap14-level1-agent/x": ""})), "agent")
	ap14Has(t, out, "stop: /etc/sudoers.d/zz-ap14-level1-agent exists and is not a regular file: the script never replaces or removes it\n")
	// Another account's drop-in is left alone: each user has its own name.
	out, _ = ap14Report(t, ap14Root(t, with(map[string]string{"etc/sudoers.d/zz-ap14-level1-other": ap14Dropin("other")})), "agent")
	ap14Has(t, out, "change 1: write /etc/sudoers.d/zz-ap14-level1-agent (a new file)")
	ap14Lacks(t, out, "stop: /etc/sudoers.d/zz-ap14-level1-other")

	// Folders someone else owns stop the plan.
	for _, p := range []string{"/etc/ataniq", "/etc/ataniq/grants", "/etc/sudoers.d"} {
		out, _ = ap14Report(t, ap14Root(t, with(map[string]string{"not-root.txt": p + "\n"})), "agent")
		ap14Has(t, out, "stop: "+p+" ")
	}
	// Every register entry: a regular file root owns and only root writes.
	out, _ = ap14Report(t, ap14Root(t, with(map[string]string{"not-root.txt": "/etc/ataniq/grants/README\n"})), "agent")
	ap14Has(t, out, "stop: the register entry /etc/ataniq/grants/README is not owned by root or may be written by another account")
	out, _ = ap14Report(t, ap14Root(t, with(map[string]string{"etc/ataniq/grants/standing-1": "words\n", "not-root.txt": "/etc/ataniq/grants/standing-1\n"})), "agent")
	ap14Has(t, out, "stop: the register entry /etc/ataniq/grants/standing-1 is not owned by root or may be written by another account: that account can change the grant\n",
		"  do: check its words with the owner, then chown root:root '/etc/ataniq/grants/standing-1' and chmod go-w '/etc/ataniq/grants/standing-1'")
	ap14Lacks(t, out, "no change: the standing-grants register")
	dir := ap14Root(t, with(map[string]string{"etc/ataniq/grants/real": "words\n", "etc/ataniq/grants/sub/x": "y\n"}))
	if err := os.Symlink("real", filepath.Join(dir, "etc/ataniq/grants/standing-2")); err != nil {
		t.Fatal(err)
	}
	out, _ = ap14Report(t, dir, "agent")
	ap14Has(t, out, "stop: the register entry /etc/ataniq/grants/standing-2 is a link",
		"stop: the register entry /etc/ataniq/grants/sub is not a regular file")
	// A register without its README gets one, a new file.
	out, _ = ap14Report(t, ap14Root(t, with(map[string]string{"etc/ataniq/grants/README": ap14Delete, "etc/ataniq/grants/standing-1": "words\n"})), "agent")
	ap14Has(t, out, "change 2: write /etc/ataniq/grants/README (a new file), mode 0644, owner root\n")

	// No sudo at all: nothing to change for it.
	out, _ = ap14Report(t, ap14Root(t, nil), "agent")
	ap14Has(t, out, "sudo is not installed (no /etc/sudoers): nothing to change for sudo\n",
		"change 1: create the folder /etc/ataniq (a new folder)",
		"change 2: create the standing-grants register /etc/ataniq/grants (a new folder)",
		"change 3: write /etc/ataniq/grants/README (a new file)")
	// A temporary file of an earlier run is noted, never removed.
	out, _ = ap14Report(t, ap14Root(t, with(map[string]string{"etc/sudoers.d/.ap14-level1.tmp.Ab12Cd": "x\n"})), "agent")
	ap14Has(t, out, "note: /etc/sudoers.d/.ap14-level1.tmp.Ab12Cd is a temporary file of an earlier run (sudo skips it): remove it by hand: rm -f '/etc/sudoers.d/.ap14-level1.tmp.Ab12Cd'\n")
}

// The checks of --verify, read from the fixtures when they hold sudo-n.txt:
// a password required, not allowed, and a probe that could not run are told
// apart, and sudo -l -U must still list the grant that asks for the password.
func TestAiHostAP14Verify(t *testing.T) {
	good := "Matching Defaults entries for agent on host:\n    env_reset, timestamp_timeout=0\n\nUser agent may run the following commands on host:\n    (ALL : ALL) ALL\n"
	base := map[string]string{
		"etc/sudoers":                        "%wheel ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n",
		"etc/sudoers.d/zz-ap14-level1-agent": ap14Dropin("agent"),
		"modes.txt":                          "/etc/sudoers.d/zz-ap14-level1-agent 0440\n",
		"etc/ataniq/grants/README":           "x\n",
		"sudo-l.txt":                         good,
	}
	for _, c := range []struct {
		files map[string]string
		code  int
		want  string
	}{
		{map[string]string{"sudo-n.txt": "sudo: a password is required\n", "sudo-n.rc": "1\n"}, 0, "check: sudo -n true run as agent fails: a password is required\n"},
		{map[string]string{"sudo-n.txt": "", "sudo-n.rc": "0\n"}, 1, "finding: sudo-n: sudo -n true run as agent succeeds: agent still has sudo without a password\n"},
		{map[string]string{"sudo-n.txt": "agent is not in the sudoers file.\n", "sudo-n.rc": "1\n"}, 1, "finding: sudo-n: sudo refuses agent outright (not allowed, no password asked): agent is not in the sudoers file.\n"},
		{map[string]string{"sudo-n.txt": "Sorry, user agent is not allowed to execute '/usr/bin/true' as root on host.\n", "sudo-n.rc": "1\n"}, 1, "finding: sudo-n: sudo refuses agent outright"},
		{map[string]string{"sudo-n.txt": "runuser: user agent does not exist\n", "sudo-n.rc": "1\n"}, 1, "finding: sudo-n: the probe sudo -n true as agent could not run or said something else (exit 1): runuser: user agent does not exist\n"},
		{map[string]string{"sudo-n.txt": "sudo: a password is required\n", "sudo-l.txt": "User agent may run the following commands on host:\n    (ALL) /usr/bin/b\n"}, 1, "finding: verify: sudo -l -U agent lists no grant of ALL as root that asks for the password: agent's intended grant is not there\n"},
		{map[string]string{"sudo-n.txt": "sudo: a password is required\n", "sudo-l.txt": "User agent may run the following commands on host:\n    (ALL : ALL) ALL\n"}, 1, "finding: verify: sudo -l -U agent shows no timestamp_timeout: is the drop-in read?\n"},
		{map[string]string{"sudo-n.txt": "sudo: a password is required\n", "sudo-l.txt": "sudo: you are not root\n", "sudo-l.rc": "1\n"}, 1, "finding: verify: sudo -l -U agent failed (exit 1): sudo: you are not root\n"},
	} {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range c.files {
			m[k] = v
		}
		out, code := ap14Report(t, ap14Root(t, m), "agent")
		ap14Has(t, out, "== checks of the result\n", c.want)
		if code != c.code {
			t.Errorf("%q: exit %d, want %d\n%s", c.want, code, c.code, out)
		}
	}
	m := map[string]string{"sudo-n.txt": "sudo: a password is required\n"}
	for k, v := range base {
		m[k] = v
	}
	out, _ := ap14Report(t, ap14Root(t, m), "agent")
	ap14Has(t, out, "check: sudo -l -U agent lists a grant that asks for the password: (ALL : ALL) ALL\n", "check: sudo -l -U agent: the last timestamp_timeout is 0\n")
}

// --apply and --verify are refused, before anything is read or run, without
// root and without --confirm equal to --user; --self-test is refused as root
// (TestAiHostAP14SelfTest). The script sets its own PATH, so no stub can stand
// in for a program: every case is refused on its arguments alone, and the
// cases that reach the root check run only when the test does not run as
// root (else --apply would run for real).
func TestAiHostAP14Refusals(t *testing.T) {
	env := []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir()}
	cases := []struct {
		args []string
		want string
		root bool // whether the root check is reached
	}{
		{nil, "usage: ap14-level1.sh --dry --user NAME", false},
		{[]string{"--apply"}, "--user NAME is required", false},
		{[]string{"--verify"}, "--user NAME is required", false},
		{[]string{"--dry"}, "--user NAME is required", false},
		{[]string{"--apply", "--confirm", "agent"}, "--user NAME is required", false},
		{[]string{"--apply", "--user", "agent"}, "--apply needs --confirm NAME, repeating the --user name", false},
		{[]string{"--apply", "--user", "agent", "--confirm", "agent2"}, "--confirm must repeat the --user name exactly", false},
		{[]string{"--apply", "--user", "agent", "--confirm", "Agent"}, "--confirm must repeat the --user name exactly", false},
		{[]string{"--apply", "--user", "agent", "--confirm", ""}, "--confirm must repeat the --user name exactly", false},
		{[]string{"--apply", "--user", "agent", "--confirm"}, "--confirm needs a value", false},
		{[]string{"--verify", "--user", "agent", "--confirm", "agent"}, "--confirm goes with --apply only", false},
		{[]string{"--apply", "--user", "root", "--confirm", "root"}, "--user must be the everyday user, not root", false},
		{[]string{"--apply", "--user", "Bad Name", "--confirm", "Bad Name"}, "--user must be a login name", false},
		{[]string{"--apply", "--user", "a;b", "--confirm", "a;b"}, "--user must be a login name", false},
		{[]string{"--apply", "--user", "first.last", "--confirm", "first.last"}, "--user must be a login name (lowercase letters, digits, _ -; no dot, which sudo skips in a drop-in's name)", false},
		{[]string{"--apply", "--user", strings.Repeat("a", 33), "--confirm", strings.Repeat("a", 33)}, "--user must be a login name", false},
		{[]string{"--apply", "--user", "agent", "--confirm", "agent", "--fixtures", "/x"}, "--fixtures goes with --self-test only", false},
		{[]string{"--self-test", "--user", "agent"}, "--user goes with --fixtures in a self-test", false},
		{[]string{"--apply", "--self-test", "--user", "agent", "--confirm", "agent"}, "give one of --dry, --apply, --verify and --self-test", false},
		{[]string{"--apply", "--user", "agent", "--confirm", "agent", "--extra"}, "unknown argument", false},
		{[]string{"--apply", "--user", "agent", "--confirm", "agent"}, "--apply runs as root", true},
		{[]string{"--verify", "--user", "agent"}, "--verify runs as root", true},
	}
	for _, c := range cases {
		if c.root && os.Geteuid() == 0 {
			continue
		}
		out, code := ahRun(t, ap14Script, t.TempDir(), env, c.args...)
		if code != 2 || !strings.Contains(out, c.want) {
			t.Errorf("%q: exit %d, want 2 and %q:\n%s", c.args, code, c.want, out)
		}
		if c.want != "usage: ap14-level1.sh --dry --user NAME" && !strings.Contains(out, "refused: ") {
			t.Errorf("%q: the output does not say refused:\n%s", c.args, out)
		}
	}
}

// The script's first commands fix PATH to the system folders, before any
// external program; it never moves, renames or edits a file in place, keeps
// no journal or backup folder, and removes only what its own run created.
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
	if len(code) < 3 || code[0] != "CALLER_PATH=${PATH-}" || code[1] != "PATH=/usr/sbin:/usr/bin:/sbin:/bin" || code[2] != "export PATH" {
		t.Errorf("the script does not start by fixing PATH: %q", code[:3])
	}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(^|[;&|({ ]|\$\()mv( |$)`),
		regexp.MustCompile(`sed -i`),
		regexp.MustCompile(`MOVED|BACKUP|backup-|journal`),
		regexp.MustCompile(`\bln -[a-z]*[fs]`),
	} {
		for _, line := range code {
			if re.MatchString(line) {
				t.Errorf("the code matches %s: %s", re, line)
			}
		}
	}
	rm := regexp.MustCompile(`(^|[;&|({ ]|\$\()rm( |$)`)
	for _, line := range code {
		if !rm.MatchString(line) {
			continue
		}
		switch {
		case strings.Contains(line, `rm -f -- "$(hp "$tr_p")"`),
			strings.Contains(line, `rm -f -- "$tp_tmp"`),
			strings.Contains(line, `rm -rf -- "$st_w"`),
			strings.Contains(line, "remove it by hand"):
		default:
			t.Errorf("an rm the rules do not allow: %s", line)
		}
	}
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
		if len(w) < 3 || w == "root" || w == "agent" || w == "other" || w == "localhost" {
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
