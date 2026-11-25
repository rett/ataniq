package tests

// The checks of deploy/ai-host/ap14-level1.sh, the AP14 level-1 script for
// the AI hosts. No test runs it with --dry, and --apply only where it must be
// refused before anything is read: every other run is --self-test, which
// reads the fake root a test writes (--fixtures) or the fixture folders
// beside the script, and runs nothing on the host.

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
)

// ap14Root writes files below a new fake root and returns it. The accounts
// are always there: agent (uid 1001, primary group agent 1001, in wheel 998)
// and other (uid 1002, in nothing), unless files names them.
func ap14Root(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{
		"etc/passwd": "root:x:0:0::/root:/bin/sh\nagent:x:1001:1001::/nonexistent:/bin/sh\nother:x:1002:1002::/nonexistent:/bin/sh\n",
		"etc/group":  "root:x:0:\nwheel:x:998:agent\nagent:x:1001:\nother:x:1002:\n",
	}
	for name, data := range files {
		all[name] = data
	}
	for name, data := range all {
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

// ap14Report runs the self-test report of the fake root dir for the user and
// returns its output and exit status.
func ap14Report(t *testing.T, dir, name string) (string, int) {
	t.Helper()
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LC_ALL=C"}
	return ahRun(t, ap14Script, t.TempDir(), env, "--self-test", "--fixtures", dir, "--user", name)
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

// The built-in self-test passes from the repository root and from another
// folder, and says that it read and ran nothing on the host.
func TestAiHostAP14SelfTest(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LC_ALL=C"}
	check := func(where, out string, code int) {
		t.Helper()
		if code != 0 || !strings.Contains(out, "self-test: ok, 3 cases; nothing on this host was read or run") {
			t.Errorf("%s: exit %d\n%s", where, code, out)
		}
		for _, c := range []string{"clean", "move", "stop"} {
			if !strings.Contains(out, "self-test: ok "+c+"\n") {
				t.Errorf("%s: case %s did not pass:\n%s", where, c, out)
			}
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
}

// Sudoers: includes of every form, in sudo's order; aliases with negation;
// files of an includedir that sudo skips; where the drop-in would sit.
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
		"rule: /etc/sudoers.extra:1: NOPASSWD, as root: ADMINS ALL = (ALL) NOPASSWD: /usr/bin/a\n",
		"rule: /etc/local/one:3: NOPASSWD, as root: agent ALL = (SVC) NOPASSWD: /usr/bin/b\n",
		"rule: /etc/sudoers.d/10-x:2: NOPASSWD, as root: agent ALL=(ALL) NOPASSWD: ALL\n",
		"rule: /etc/sudoers.deep:1: NOPASSWD, as root: %#998 ALL = NOPASSWD: /usr/bin/c\n",
		"change 1: write /etc/sudoers.d/zz-ap14-level1, mode 0440, owner root, with:\n",
		"    | Defaults:agent timestamp_timeout=0\n",
		"change 2: move /etc/sudoers.d/10-x aside into /etc/ap14-level1/backup-<UTC time of the run>/ (its one rule, line 2: agent ALL=(ALL) NOPASSWD: ALL)\n",
		"stop: /etc/sudoers.extra:1: ADMINS ALL = (ALL) NOPASSWD: /usr/bin/a\n  do: edit it with visudo -f /etc/sudoers.extra",
		"stop: /etc/local/one:3: ",
		"stop: /etc/sudoers.deep:1: ",
		"--apply refuses while a stop is listed",
	)
	ap14Lacks(t, out, "/ignored-dot", "/ignored-tilde", "rule: /etc/sudoers.d/30-other")
	if got := ap14Lines(out, "rule: "); len(got) != 4 {
		t.Errorf("want 4 rules, got %q", got)
	}

	// The negated member of the alias leaves other out; its own rule counts.
	out, _ = ap14Report(t, dir, "other")
	ap14Has(t, out, "rule: /etc/sudoers.d/30-other:1: NOPASSWD, as root: other ALL=(ALL) NOPASSWD: ALL\n",
		"change 2: move /etc/sudoers.d/30-other aside")
	ap14Lacks(t, out, "rule: /etc/sudoers.extra", "rule: /etc/sudoers.deep", "rule: /etc/local/one", "rule: /etc/sudoers.d/10-x")

	// Without an includedir of /etc/sudoers.d the drop-in would not be read.
	dir = ap14Root(t, map[string]string{"etc/sudoers": "root ALL=(ALL) ALL\n@includedir /etc/elsewhere\n", "etc/elsewhere/x": "root ALL=(ALL) ALL\n"})
	out, _ = ap14Report(t, dir, "agent")
	ap14Has(t, out, "stop: /etc/sudoers does not read /etc/sudoers.d (no includedir for it): the drop-in would have no effect\n")
	ap14Lacks(t, out, "change 1: write /etc/sudoers.d/zz-ap14-level1")

	// An include loop ends, with a line to check by hand.
	dir = ap14Root(t, map[string]string{"etc/sudoers": "@include /etc/loop\n@includedir /etc/sudoers.d\n", "etc/loop": "@include /etc/loop\n", "etc/sudoers.d/.keep": ""})
	out, _ = ap14Report(t, dir, "agent")
	ap14Has(t, out, "check by hand: /etc/loop:0: includes nested more than 16 deep; not followed\n")

	// A cache time set after the drop-in is a finding; one before it is not.
	dir = ap14Root(t, map[string]string{
		"etc/sudoers":         "Defaults timestamp_timeout=15\n@includedir /etc/sudoers.d\nDefaults:agent timestamp_timeout=5\nDefaults:other timestamp_timeout=9\n",
		"etc/sudoers.d/00-a":  "Defaults timestamp_timeout=20\n",
		"etc/sudoers.d/zzz-b": "Defaults:%wheel timestamp_timeout=-1\n",
		"etc/sudoers.d/zzz-c": "Defaults timestamp_timeout=0\n",
	})
	out, _ = ap14Report(t, dir, "agent")
	ap14Has(t, out,
		"finding: sudo-timeout: /etc/sudoers:3: sets timestamp_timeout=5 after the drop-in",
		"finding: sudo-timeout: /etc/sudoers.d/zzz-b:1: sets timestamp_timeout=-1 after the drop-in",
	)
	ap14Lacks(t, out, "timestamp_timeout=15 after", "timestamp_timeout=20 after", "timestamp_timeout=9 after", "timestamp_timeout=0 after")
}

// NOPASSWD and !authenticate in the forms sudoers allows, each alone in a
// file of /etc/sudoers.d.
func TestAiHostAP14SudoersNopasswd(t *testing.T) {
	cases := []struct {
		rule string
		want string // "root", "other", "maybe", "noauth" or "" for no rule
	}{
		{"agent ALL=(ALL) NOPASSWD: ALL", "root"},
		{"agent ALL=(ALL:ALL) NOPASSWD:ALL", "root"},
		{"agent ALL=NOPASSWD: /bin/a", "root"},
		{"agent ALL = (root) NOPASSWD: /bin/a, PASSWD: /bin/b", "root"},
		{"agent ALL = (root) PASSWD: /bin/b, NOPASSWD: /bin/a", "root"},
		{"agent ALL = (root) PASSWD: /bin/b, /bin/c", ""},
		{"agent ALL = (root) /bin/b", ""},
		{"agent ALL = (root) NOPASSWD: NOEXEC: /bin/a", "root"},
		{"agent ALL = (root) NOEXEC:NOPASSWD: /bin/a", "root"},
		{"agent ALL = (root) NOPASSWD: PASSWD: /bin/a", ""},
		{"agent ALL = (postgres) NOPASSWD: /bin/psql", "other"},
		{"agent ALL = (: wheel) NOPASSWD: /bin/x", "other"},
		{"agent ALL = (postgres) /bin/psql, (root) NOPASSWD: /bin/y", "root"},
		{"agent host1 = NOPASSWD: /a : host2 = PASSWD: /b", "root"},
		{"agent host1 = PASSWD: /a : host2 = NOPASSWD: /b", "root"},
		{"agent host1 = NOPASSWD: /a : host2 = /b", "root"},
		{"agent ALL = NOPASSWD: \\\n    /bin/a", "root"},
		{"agent ALL = CWD=/tmp NOPASSWD: /bin/a", "root"},
		{"ALL ALL=(ALL) NOPASSWD: ALL", "root"},
		{"%wheel ALL=(ALL) NOPASSWD: ALL", "root"},
		{"%#998 ALL=(ALL) NOPASSWD: ALL", "root"},
		{"%#1001 ALL=(ALL) NOPASSWD: ALL", "root"},
		{"#1001 ALL=(ALL) NOPASSWD: ALL", "root"},
		{`"agent" ALL=(ALL) NOPASSWD: ALL`, "root"},
		{"other, agent ALL=(ALL) NOPASSWD: ALL", "root"},
		{"other ALL=(ALL) NOPASSWD: ALL", ""},
		{"%other ALL=(ALL) NOPASSWD: ALL", ""},
		{"%#1002 ALL=(ALL) NOPASSWD: ALL", ""},
		{"#1002 ALL=(ALL) NOPASSWD: ALL", ""},
		{"agentx ALL=(ALL) NOPASSWD: ALL", ""},
		{"ALL, !agent ALL=(ALL) NOPASSWD: ALL", ""},
		{"!agent, ALL ALL=(ALL) NOPASSWD: ALL", "root"},
		{"%:Domain_Admins ALL=(ALL) NOPASSWD: ALL", "maybe"},
		{"+admins ALL=(ALL) NOPASSWD: ALL", "maybe"},
		{"# agent ALL=(ALL) NOPASSWD: ALL", ""},
		{"agent ALL=(ALL) ALL # NOPASSWD: in a comment", ""},
		{"Defaults:agent !authenticate", "noauth"},
		{"Defaults !authenticate", "noauth"},
		{"Defaults:%wheel !lecture, !authenticate", "noauth"},
		{"Defaults!/usr/bin/x !authenticate", "noauth"},
		{"Defaults:other !authenticate", ""},
		{"Defaults:agent !requiretty", ""},
		{"Cmnd_Alias NOPASS = /bin/a", ""},
	}
	files := map[string]string{"etc/sudoers": "root ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n"}
	name := func(i int) string { return "f" + string(rune('a'+i/26)) + string(rune('a'+i%26)) }
	for i, c := range cases {
		files["etc/sudoers.d/"+name(i)] = c.rule + "\n"
	}
	out, _ := ap14Report(t, ap14Root(t, files), "agent")
	for i, c := range cases {
		prefix := "rule: /etc/sudoers.d/" + name(i) + ":1: "
		got := ap14Lines(out, prefix)
		move := "move /etc/sudoers.d/" + name(i) + " aside"
		maybe := "finding: sudo-nopasswd: /etc/sudoers.d/" + name(i) + ":1: may apply to agent"
		switch c.want {
		case "":
			if len(got) != 0 {
				t.Errorf("%q: unexpected %q", c.rule, got)
			}
			continue
		case "noauth":
			if len(got) != 1 || !strings.HasPrefix(got[0], prefix+"Defaults !authenticate: ") {
				t.Errorf("%q: want a Defaults !authenticate rule, got %q", c.rule, got)
			}
		case "maybe":
			if len(got) != 1 || !strings.Contains(out, maybe) {
				t.Errorf("%q: want a rule that may apply, got %q", c.rule, got)
			}
			if strings.Contains(out, move) {
				t.Errorf("%q: a rule that may not apply is moved", c.rule)
			}
			continue
		default:
			if len(got) != 1 || !strings.HasPrefix(got[0], prefix+"NOPASSWD, as "+c.want+": ") {
				t.Errorf("%q: want NOPASSWD as %s, got %q", c.rule, c.want, got)
			}
		}
		if !strings.Contains(out, move) {
			t.Errorf("%q: alone in its file but not moved aside", c.rule)
		}
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

// Setuid files, root's PATH, writable paths, access control lists, and what
// sudo -l says.
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
		"setuid.txt":    "/usr/bin/sudo\n/usr/bin/passwd\n/opt/tool/helper\n/usr/bin/sudo\n",
		"packaged.txt":  "/usr/bin/sudo\n/usr/bin/passwd\n",
		"root-path.txt": "/usr/bin::.:bin:/usr/local/bin",
		"writable.txt":  "/usr/local/bin\n/etc/cron.d/job\n",
		"getfacl.txt": "# file: /etc/a\nuser::rw-\nuser:agent:rw-\ngroup::r--\nmask::rw-\nother::r--\n\n" +
			"# file: /etc/b\nuser::rw-\nuser:1001:rwx\t#effective:r--\nmask::r-x\nother::r--\n\n" +
			"# file: /etc/c\nuser::rw-\ngroup:wheel:rw-\nmask::rwx\nother::r--\n\n" +
			"# file: /etc/d\nuser::rw-\ngroup:other:rw-\nuser:other:rw-\nmask::rwx\nother::r--\n" +
			"default:user:agent:rwx\n",
		"sudo-l.txt": "Matching Defaults entries for agent on host:\n" +
			"    env_reset, timestamp_timeout=0, !authenticate,\n    timestamp_timeout=10\n\n" +
			"Runas and Command-specific defaults for agent:\n    Defaults!/usr/bin/x !authenticate\n\n" +
			"User agent may run the following commands on host:\n    (ALL : ALL) ALL\n    (root) NOPASSWD: /usr/bin/a\n",
	}
	out, _ := ap14Report(t, ap14Root(t, files), "agent")
	ap14Has(t, out,
		"note: setuid search covers: / /srv\n",
		"note: setuid search leaves out /a\\040b\n",
		"note: setuid search leaves out /.snapshots\n",
		"finding: setuid: /opt/tool/helper is setuid or setgid root and no package owns it\n",
		"finding: root-path: root's PATH holds '', a relative or empty entry",
		"finding: root-path: root's PATH holds '.', a relative or empty entry",
		"finding: root-path: root's PATH holds 'bin', a relative or empty entry",
		"finding: writable: agent may write /usr/local/bin, which root runs",
		"finding: writable: agent may write /etc/cron.d/job, which root runs",
		"finding: acl: /etc/a grants user:agent:rw-: agent may write it\n  remedy: setfacl -x user:agent /etc/a\n",
		"finding: acl: /etc/c grants group:wheel:rw-: agent may write it\n",
		"finding: sudo-l: sudo -l -U agent lists !authenticate: !authenticate\n",
		"finding: sudo-l: sudo -l -U agent lists !authenticate: Defaults!/usr/bin/x !authenticate\n",
		"finding: sudo-l: sudo -l -U agent lists a command without a password: (root) NOPASSWD: /usr/bin/a\n",
		"finding: sudo-l: sudo -l -U agent shows timestamp_timeout=10 last: credentials are cached\n",
	)
	ap14Lacks(t, out, "/usr/bin/sudo is setuid", "/usr/bin/passwd is setuid", "/net", "overlay2", "/etc/b grants", "/etc/d grants", "default:user")
	if got := ap14Lines(out, "finding: setuid: "); len(got) != 1 {
		t.Errorf("want one setuid finding, got %q", got)
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

// The plan: a usable password, a pre-existing register or drop-in, a
// backup folder someone else owns.
func TestAiHostAP14Plan(t *testing.T) {
	base := map[string]string{
		"etc/sudoers":              "@includedir /etc/sudoers.d\n",
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
	out, _ := ap14Report(t, ap14Root(t, with(map[string]string{"passwd-S.txt": "agent PS 2026-01-01\n"})), "agent")
	ap14Has(t, out, "agent has a usable password: sudo can still ask for it\n",
		"no change: the standing-grants register /etc/ataniq/grants exists, owned by root\n")
	ap14Lacks(t, out, "stop: ")

	// A drop-in with other text is replaced; its old copy goes to the backup.
	out, _ = ap14Report(t, ap14Root(t, with(map[string]string{"etc/sudoers.d/zz-ap14-level1": "Defaults:agent timestamp_timeout=5\n"})), "agent")
	ap14Has(t, out, "change 1: replace /etc/sudoers.d/zz-ap14-level1 (the old copy goes to the backup folder)")

	// Folders someone else owns stop the plan.
	for _, p := range []string{"/etc/ataniq", "/etc/ataniq/grants", "/etc/ap14-level1"} {
		m := with(map[string]string{"not-root.txt": p + "\n", "etc/ap14-level1/.keep": ""})
		out, _ = ap14Report(t, ap14Root(t, m), "agent")
		ap14Has(t, out, "stop: "+p+" exists but is not a folder owned by root that only root may write")
	}
	out, _ = ap14Report(t, ap14Root(t, with(map[string]string{"not-root.txt": "/etc/ataniq/grants/README\n"})), "agent")
	ap14Has(t, out, "stop: /etc/ataniq/grants/README is not a file owned by root that only root may write")

	// No sudo at all: nothing to change for it.
	out, _ = ap14Report(t, ap14Root(t, nil), "agent")
	ap14Has(t, out, "sudo is not installed (no /etc/sudoers): nothing to change for sudo\n", "change 1: create the standing-grants register /etc/ataniq/grants")
}

// --apply is refused, before anything is read or run, without root and
// without --confirm equal to --user. The PATH holds only recording stubs, and
// `id` says uid 1000, so nothing reaches the host even when the test runs as
// root; the only program the script may run before refusing is `id -u`.
func TestAiHostAP14Refusals(t *testing.T) {
	stubs := []string{"id", "visudo", "sudo", "runuser", "su", "find", "getent", "pacman", "dpkg", "rpm",
		"mv", "cp", "mkdir", "chown", "chmod", "rm", "mktemp", "cat", "grep", "awk", "sed", "ls", "sort",
		"date", "passwd", "getfacl", "cut", "head", "env", "gpasswd", "setfacl", "install", "tee"}
	bin, calls := ahStubs(t, stubs...)
	idStub := "#!/bin/sh\nprintf 'id' >>'" + calls + "'\nfor a in \"$@\"; do printf ' [%s]' \"$a\" >>'" + calls + "'; done\nprintf '\\n' >>'" + calls + "'\necho 1000\n"
	if err := os.WriteFile(filepath.Join(bin, "id"), []byte(idStub), 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin, "HOME=" + t.TempDir()}
	cases := []struct {
		args []string
		want string
		id   bool // whether the root check ran
	}{
		{nil, "usage: ap14-level1.sh --dry --user NAME", false},
		{[]string{"--apply"}, "--user NAME is required", false},
		{[]string{"--apply", "--confirm", "agent"}, "--user NAME is required", false},
		{[]string{"--apply", "--user", "agent"}, "--apply needs --confirm NAME, repeating the --user name", false},
		{[]string{"--apply", "--user", "agent", "--confirm", "agent2"}, "--confirm must repeat the --user name exactly", false},
		{[]string{"--apply", "--user", "agent", "--confirm", "Agent"}, "--confirm must repeat the --user name exactly", false},
		{[]string{"--apply", "--user", "agent", "--confirm", ""}, "--confirm must repeat the --user name exactly", false},
		{[]string{"--apply", "--user", "agent", "--confirm"}, "--confirm needs a value", false},
		{[]string{"--apply", "--user", "root", "--confirm", "root"}, "--user must be the everyday user, not root", false},
		{[]string{"--apply", "--user", "Bad Name", "--confirm", "Bad Name"}, "--user must be a login name", false},
		{[]string{"--apply", "--user", "a;b", "--confirm", "a;b"}, "--user must be a login name", false},
		{[]string{"--apply", "--user", strings.Repeat("a", 33), "--confirm", strings.Repeat("a", 33)}, "--user must be a login name", false},
		{[]string{"--apply", "--user", "agent", "--confirm", "agent", "--fixtures", "/x"}, "--fixtures goes with --self-test only", false},
		{[]string{"--apply", "--self-test", "--user", "agent", "--confirm", "agent"}, "give one of --dry, --apply and --self-test", false},
		{[]string{"--apply", "--user", "agent", "--confirm", "agent", "--extra"}, "unknown argument", false},
		{[]string{"--apply", "--user", "agent", "--confirm", "agent"}, "--apply runs as root", true},
	}
	for _, c := range cases {
		_ = os.Remove(calls)
		out, code := ahRun(t, ap14Script, t.TempDir(), env, c.args...)
		if code != 2 || !strings.Contains(out, c.want) {
			t.Errorf("%q: exit %d, want 2 and %q:\n%s", c.args, code, c.want, out)
		}
		if c.want != "usage: ap14-level1.sh --dry --user NAME" && !strings.Contains(out, "refused: ") {
			t.Errorf("%q: the output does not say refused:\n%s", c.args, out)
		}
		got := ahCalls(t, calls)
		var want []string
		if c.id {
			want = []string{"id [-u]"}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q: called %q, want %q", c.args, got, want)
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
