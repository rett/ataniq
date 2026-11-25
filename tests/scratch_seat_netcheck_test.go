package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/sandboxnet"
	"ataniq/internal/leadunit"
)

// The owner's network check of the attended proof
// (qualification/scratch-seat/netcheck.sh.in) runs its connect test in the
// network shape of a scratch lead's sandbox, and this test fails when the
// two drift apart:
//   - the pasta arguments are sandboxnet.PastaArgs(), which the plan passes;
//   - the bubblewrap options that make the namespaces and the identity are
//     the profile's flag set for the identity a scratch lead has (root:
//     UID and GID 0), and no later option of the check changes the network,
//     the namespaces, the capabilities or adds a filter;
//   - scratch provisioning (BuildInputs) names no other identity and no
//     seccomp filter, so the holder's flag set for a scratch lead is that
//     one;
//   - the lead's user unit sets no network property, so pasta starts in
//     this host's network there as it does from the owner's shell.
//
// What still differs (the binds, the program inside, the resolv.conf) is
// stated in the template and in the runbook; none of it changes whether a
// connection to the gate's address is made.
func TestScratchSeatNetcheckMatchesTheLeadsNetwork(t *testing.T) {
	text := string(mustRead(t, filepath.Join(root(t), "qualification", "scratch-seat", "netcheck.sh.in")))
	// The words of core()'s command, the line continuations joined.
	i := strings.Index(text, "\ncore() {\n")
	if i < 0 {
		t.Fatal("netcheck.sh.in has no core()")
	}
	body := text[i:]
	body = body[:strings.Index(body, "\n}\n")]
	body = strings.ReplaceAll(body, "\\\n", " ")
	words := strings.Fields(body)
	p := slices.Index(words, `"$PASTA"`)
	b := slices.Index(words, `"$BWRAP"`)
	if p < 0 || b < p {
		t.Fatalf("core() does not run pasta, then bubblewrap:\n%s", body)
	}
	if got, want := words[p+1:b], sandboxnet.PastaArgs(); !slices.Equal(got, want) {
		t.Errorf("netcheck's pasta arguments %q are not the plan's %q", got, want)
	}

	in := profile.Inputs{
		StateRoot: "/srv/state", Role: "scratch-sample-lead", Project: "sample", Runtime: profile.Claude, RuntimeVersion: "2.1.0",
		Identity: profile.IdentityRoot, AgentInstall: "/srv/owned/agent", ProbeBinary: "/srv/owned/probe",
		SpecDir: "/srv/owned/spec", ManagedSettings: "/srv/owned/managed/settings.json", ManagedSettingsSHA256: strings.Repeat("c1", 32),
		LeadSkills: "/srv/owned/skills", InstructionFile: "/srv/owned/instructions/" + profile.Claude.InstructionFileName(),
		GitIdentityDir: "/srv/owned/git", GitMeta: "/srv/state/leads/scratch-sample-lead/gitmeta", QargiInstall: "/srv/owned/qargi",
		QargiRecordsInside: "/home/lead/.qargi", AssignmentDir: "/srv/owned/assignment", ProjectionDir: "/srv/owned/projection",
		ResolvConf: "/srv/owned/resolv.conf", NeverMounted: []string{"/srv/sample/checkout"},
		Term: "xterm-256color", ColorTerm: "truecolor", Lang: "C.UTF-8", TZ: "UTC",
	}
	flags, err := in.Flags()
	if err != nil {
		t.Fatalf("the profile's flags: %v", err)
	}
	rest := words[b+1:]
	if len(rest) < len(flags) || !slices.Equal(rest[:len(flags)], flags) {
		t.Errorf("netcheck's bubblewrap options %q do not start with the profile's flag set %q", rest[:min(len(rest), len(flags))], flags)
	}
	for _, w := range rest[min(len(rest), len(flags)):] {
		if strings.HasPrefix(w, "--unshare") || strings.HasPrefix(w, "--share") || strings.HasPrefix(w, "--cap-") || w == "--seccomp" ||
			w == "--add-seccomp-fd" || w == "--uid" || w == "--gid" || w == "--userns" || w == "--userns2" || w == "--pidns" ||
			slices.Contains(profile.ForbiddenFlags(), w) {
			t.Errorf("netcheck's bubblewrap has the option %s after the flag set", w)
		}
	}

	// Scratch provisioning sets neither an identity nor a seccomp filter in
	// the sandbox inputs, so the holder runs a scratch lead as root inside,
	// with the flag set above and no filter.
	src := string(mustRead(t, filepath.Join(root(t), "internal", "leadcanary", "provision", "scratch.go")))
	j := strings.Index(src, "\nfunc BuildInputs(")
	if j < 0 {
		t.Fatal("scratch.go has no BuildInputs")
	}
	fn := src[j:]
	fn = fn[:strings.Index(fn, "\n}\n")]
	for _, bad := range []string{"Identity", "Seccomp", "OwnerUser"} {
		if regexp.MustCompile(`(^|[^A-Za-z])` + bad + `\s*:`).MatchString(fn) {
			t.Errorf("BuildInputs of a scratch lead sets %s: netcheck.sh.in runs as root without a filter; make them match", bad)
		}
	}

	// The lead's user unit sets no property of the network.
	props := append(leadunit.FixedProperties(), leadunit.BoundProperties("parent.service")...)
	props = append(props, leadunit.LimitProperties(leadunit.Limits{MemoryMax: 1 << 32, TasksMax: 512})...)
	for _, p := range props {
		for _, net := range []string{"IP", "Network", "PrivateNetwork", "RestrictAddressFamilies", "SocketBind", "JoinsNamespaceOf", "PrivateIPC"} {
			if strings.HasPrefix(p.Name, net) {
				t.Errorf("the lead's unit sets the network property %s=%s, which netcheck.sh does not have", p.Name, p.Value)
			}
		}
	}

	// The throwaway unit of probe 3 has exactly the lead unit's fixed
	// properties, each as one -p of systemd-run.
	k := strings.Index(text, "\nunit() {\n")
	if k < 0 {
		t.Fatal("netcheck.sh.in has no unit()")
	}
	ubody := text[k:]
	ubody = ubody[:strings.Index(ubody, "\n}\n")]
	ubody = strings.ReplaceAll(ubody, "\\\n", " ")
	uwords := strings.Fields(ubody)
	var got []string
	for i, w := range uwords {
		if w == "-p" && i+1 < len(uwords) {
			got = append(got, uwords[i+1])
		}
	}
	var want []string
	for _, p := range leadunit.FixedProperties() {
		want = append(want, p.Name+"="+p.Value)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the unit of probe 3 has the properties %q, the lead unit's fixed ones are %q", got, want)
	}
	if !slices.Contains(uwords, "--user") || !slices.Contains(uwords, "--wait") || !slices.Contains(uwords, "--pipe") || !strings.Contains(ubody, `"$SH" "$SELF" --core`) {
		t.Errorf("unit() does not run --core in a user unit and wait for it:\n%s", ubody)
	}

	// The runbook says the same.
	runbook := strings.Join(strings.Fields(string(mustRead(t, filepath.Join(root(t), "docs", "scratch-seat-proof.md")))), " ")
	for _, want := range []string{"`sandboxnet.PastaArgs()`", "the profile's flag set for the identity a scratch lead has"} {
		if !strings.Contains(runbook, want) {
			t.Errorf("the runbook does not say %q", want)
		}
	}
}

// netcheckScript writes the template with a header as prepare.sh writes it,
// naming a pasta and a bubblewrap that do not exist and the documentation
// address 192.0.2.1, so that nothing the entries below might start can start.
func netcheckScript(t *testing.T) (dir, script string) {
	t.Helper()
	dir = t.TempDir()
	script = filepath.Join(dir, "netcheck.sh")
	head := "#!/bin/sh\n# A test's copy.\nSELF='" + script + "'\nPASTA='" + dir + "/no-pasta'\nBWRAP='" + dir +
		"/no-bwrap'\nGATE_HOST='192.0.2.1'\nGATE_PORT='9'\n"
	body := mustRead(t, filepath.Join(root(t), "qualification", "scratch-seat", "netcheck.sh.in"))
	if err := os.WriteFile(script, append([]byte(head), body...), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, script
}

// netcheckRun runs an entry of the script that runs nothing, with the PATH
// given, and returns its output and exit status.
func netcheckRun(t *testing.T, script, path string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
	cmd.Env = []string{"PATH=" + path, "HOME=/nonexistent"}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("the script did not run: %v", err)
		}
		code = exit.ExitCode()
	}
	return string(out), code
}

// The network check classifies each probe as connected, refused, or
// filtered/timed out, or as a probe that showed nothing, and only a refusal
// or a timeout the probe itself reports passes the lead's network; a probe
// that did not run is a finding (review finding 7: a missing curl, a curl
// without telnet://, a sandbox that did not start, a line that is not the
// probe's). The probe runs curl with no configuration file and no proxy and
// reports curl's own exit status. Everything here runs on inert fixtures:
// the entries --classify, --class-of and --verdict run nothing, and
// --probe-here runs only a stub curl, the one program on its PATH; no test
// starts pasta, bubblewrap, a unit or a network request.
func TestScratchSeatNetcheckClassifiesEachProbe(t *testing.T) {
	dir, script := netcheckScript(t)
	for _, c := range []struct{ rc, connect, want string }{
		{"0", "0.000312", "connected"},
		{"28", "0.001200", "connected"}, // telnet:// waits for the gate's first byte until -m: connected all the same
		{"7", "0.000000", "refused"},
		{"28", "0.000000", "filtered"},
		{"0", "0.000000", "error"},
		{"1", "0.000000", "error"}, // telnet:// not built in
		{"2", "0.000000", "error"},
		{"127", "0.000000", "error"},
		{"7", "none", "error"},
		{"7", "", "error"},
		{"x", "0.1", "error"},
		{"7", "1.2.3", "error"},
		{"7", ".5", "error"},
		{"7", "-1", "error"},
	} {
		out, code := netcheckRun(t, script, "/usr/bin:/bin", "--classify", c.rc, c.connect)
		if code != 0 || out != c.want+"\n" {
			t.Errorf("--classify %q %q: exit %d, %q, want %q", c.rc, c.connect, code, out, c.want)
		}
	}
	for _, c := range []struct{ text, want string }{
		{"probe rc=7 connect=0.000000", "refused"},
		{"pasta: started\nprobe rc=28 connect=0.000000\n", "filtered"},
		{"probe rc=0 connect=0.004000", "connected"},
		{"probe no-curl", "error"},
		{"", "error"},
		{"connect 0.000000", "error"}, // the old form, which a missing curl printed
		{"bwrap: setting up uid map: Permission denied", "error"},
		{"probe rc=7 connect=0.000000 extra", "error"},
		{"probe rc= connect=0.000000", "error"},
		{"probe rc=7 connect=0.000000\nprobe rc=0 connect=0.1", "connected"},
	} {
		out, code := netcheckRun(t, script, "/usr/bin:/bin", "--class-of", c.text)
		if code != 0 || out != c.want+"\n" {
			t.Errorf("--class-of %q: exit %d, %q, want %q", c.text, code, out, c.want)
		}
	}
	// The verdict: this host must connect, and both probes of the lead's
	// network must be refused or time out.
	for _, c := range []struct {
		host, shell, unit string
		findings          int
		want              []string
	}{
		{"connected", "refused", "filtered", 0, []string{"PASS     this host: connected", "PASS     the lead's network, from this shell: refused", "PASS     the lead's network, in a user unit: filtered or timed out", "netcheck: no finding"}},
		{"connected", "connected", "refused", 1, []string{"FINDING  the lead's network, from this shell: connected to the gate's port 192.0.2.1:9: do not start a lead"}},
		{"connected", "refused", "connected", 1, []string{"FINDING  the lead's network, in a user unit: connected"}},
		{"connected", "error", "refused", 1, []string{"FINDING  the lead's network, from this shell: the probe did not run"}},
		{"connected", "refused", "error", 1, []string{"FINDING  the lead's network, in a user unit: the probe did not run"}},
		{"refused", "refused", "refused", 1, []string{"FINDING  this host: refused at 192.0.2.1:9: nothing listens; run this while a daemon serves the gate (the trial at step e, the proof's daemon at step 7)"}},
		{"filtered", "filtered", "filtered", 1, []string{"FINDING  this host: no answer from 192.0.2.1:9 (filtered or timed out)"}},
		{"error", "error", "error", 3, []string{"FINDING  this host: the probe did not run", "netcheck: 3 finding(s)"}},
	} {
		out, code := netcheckRun(t, script, "/usr/bin:/bin", "--verdict", c.host, c.shell, c.unit)
		if code != c.findings || strings.Count(out, "FINDING ") != c.findings || strings.Count(out, "PASS ")+c.findings != 3 {
			t.Errorf("--verdict %s %s %s: exit %d, want %d:\n%s", c.host, c.shell, c.unit, code, c.findings, out)
		}
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("--verdict %s %s %s does not say %q:\n%s", c.host, c.shell, c.unit, w, out)
			}
		}
	}

	// The probe itself, with a stub curl that records its arguments and
	// answers as curl would; the stub is the only program on PATH.
	stubs := filepath.Join(dir, "stubs")
	if err := os.Mkdir(stubs, 0o700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "curl-args")
	for _, c := range []struct {
		time string
		rc   int
		want string
	}{
		{"0.000000", 7, "probe rc=7 connect=0.000000\n"},
		{"0.000000", 28, "probe rc=28 connect=0.000000\n"},
		{"0.000420", 28, "probe rc=28 connect=0.000420\n"},
		{"0.000000", 1, "probe rc=1 connect=0.000000\n"},
	} {
		stub := "#!/bin/sh\nfor a; do printf '%s\\n' \"$a\"; done >'" + argsFile + "'\nprintf '%s' '" + c.time + "'\nexit " + strconv.Itoa(c.rc) + "\n"
		if err := os.WriteFile(filepath.Join(stubs, "curl"), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
		out, code := netcheckRun(t, script, stubs, "--probe-here", "192.0.2.1", "9")
		if code != 0 || out != c.want {
			t.Errorf("--probe-here with curl ending %d: exit %d, %q, want %q", c.rc, code, out, c.want)
		}
	}
	args := strings.Split(strings.TrimSpace(string(mustRead(t, argsFile))), "\n")
	if len(args) == 0 || args[0] != "-q" || !slices.Contains(args, "--noproxy") || args[slices.Index(args, "--noproxy")+1] != "*" ||
		args[len(args)-1] != "telnet://192.0.2.1:9" || !slices.Contains(args, "--connect-timeout") {
		t.Errorf("the probe's curl arguments: %q (want -q first, --noproxy *, a connect timeout and telnet://192.0.2.1:9 last)", args)
	}
	if err := os.Remove(filepath.Join(stubs, "curl")); err != nil {
		t.Fatal(err)
	}
	if out, code := netcheckRun(t, script, stubs, "--probe-here", "192.0.2.1", "9"); code != 0 || out != "probe no-curl\n" {
		t.Errorf("--probe-here without curl: exit %d, %q", code, out)
	}
	if out, code := netcheckRun(t, script, "/usr/bin:/bin", "--no-such-entry"); code != 2 || out != "" {
		t.Errorf("an unknown entry: exit %d, %q", code, out)
	}
	// The host's probe runs with a cleared environment, so no proxy or
	// configuration of the caller's reaches its curl.
	if text := string(mustRead(t, script)); !strings.Contains(text, `HOSTSIDE=$(/usr/bin/env -i PATH=/usr/bin:/bin "$SH" -c "$PROBE"`) {
		t.Error("the host's probe does not clear the environment")
	}
}
