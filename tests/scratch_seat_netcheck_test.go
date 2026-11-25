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
// filtered/timed out, or as a probe that showed nothing, and only a timeout
// the probe itself reports passes the lead's network: curl's "could not
// connect" (7, no time to connect) shows nothing and is a finding, never a
// passing refusal, and so is a probe that did not run (a missing curl, a
// curl without telnet://, a sandbox that did not start, a line that is not
// the probe's) and a wrapper that ended with a status other than 0. The
// probe runs curl with no configuration file and no proxy and reports
// curl's own exit status. A probe of the lead's network counts only when
// the sandbox showed the route it takes to the gate's address and a source
// address for it first. Everything here runs on inert fixtures: the entries
// --classify, --class-of, --lead-class-of, --status-class and --verdict
// run nothing, --net-of runs only a stub ip and --probe-here only a stub
// curl, each the one program on its PATH; no test starts pasta,
// bubblewrap, a unit or a network request.
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
	// network must time out; a refusal there, curl's 7 with no time to
	// connect, shows nothing and is a finding (review finding 6), and so is a
	// wrapper that ended with a status other than 0, whatever it printed.
	for _, c := range []struct {
		host, shell, unit string
		findings          int
		want              []string
	}{
		{"connected", "filtered", "filtered", 0, []string{"PASS     this host: connected", "PASS     the lead's network, from this shell: filtered or timed out on the route to the gate", "PASS     the lead's network, in a user unit: filtered or timed out", "netcheck: no finding"}},
		{"connected", "refused", "filtered", 1, []string{"FINDING  the lead's network, from this shell: curl could not connect (status 7, no time to connect)", "so it shows nothing; only a probe that times out"}},
		{"connected", "filtered", "refused", 1, []string{"FINDING  the lead's network, in a user unit: curl could not connect (status 7, no time to connect)"}},
		{"connected", "connected", "filtered", 1, []string{"FINDING  the lead's network, from this shell: connected to the gate's port 192.0.2.1:9: do not start a lead"}},
		{"connected", "filtered", "connected", 1, []string{"FINDING  the lead's network, in a user unit: connected"}},
		{"connected", "error", "filtered", 1, []string{"FINDING  the lead's network, from this shell: the probe did not run"}},
		{"connected", "filtered", "error", 1, []string{"FINDING  the lead's network, in a user unit: the probe did not run"}},
		{"connected", "nonet", "filtered", 1, []string{"FINDING  the lead's network, from this shell: the probe did not run: no usable route and source address to the gate's address in the sandbox"}},
		{"connected", "filtered", "nonet", 1, []string{"FINDING  the lead's network, in a user unit: the probe did not run: no usable route and source address"}},
		{"connected", "wrapper-1", "filtered", 1, []string{"FINDING  the lead's network, from this shell: the probe's wrapper ended with exit status 1, so what it printed is not taken"}},
		{"connected", "filtered", "wrapper-125", 1, []string{"FINDING  the lead's network, in a user unit: the probe's wrapper ended with exit status 125"}},
		{"wrapper-2", "filtered", "filtered", 1, []string{"FINDING  this host: the probe's shell ended with exit status 2"}},
		{"refused", "filtered", "filtered", 1, []string{"FINDING  this host: refused at 192.0.2.1:9: nothing listens; run this while a daemon serves the gate (the trial at step e, the proof's daemon at step 7)"}},
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

	// The exit status of a probe's wrapper decides before what it printed: 0
	// keeps the class, any other status is wrapper-STATUS.
	for _, c := range []struct{ status, class, want string }{
		{"0", "filtered", "filtered"}, {"0", "connected", "connected"}, {"1", "filtered", "wrapper-1"},
		{"125", "refused", "wrapper-125"}, {"x", "filtered", "wrapper-unknown"}, {"", "filtered", "wrapper-unknown"},
	} {
		out, code := netcheckRun(t, script, "/usr/bin:/bin", "--status-class", c.status, c.class)
		if code != 0 || out != c.want+"\n" {
			t.Errorf("--status-class %q %q: exit %d, %q, want %q", c.status, c.class, code, out, c.want)
		}
	}

	// A probe of the lead's network counts only after the sandbox showed its
	// route to the gate: a net line with a route and an address, then the
	// probe's line; a net line of none is a probe that did not run.
	for _, c := range []struct{ text, want string }{
		{"net route=eth0 addr=192.0.2.5\nprobe rc=7 connect=0.000000", "refused"},
		{"net route=eth0 addr=192.0.2.5\nprobe rc=28 connect=0.000000", "filtered"},
		{"net route=eth0 addr=192.0.2.5\nprobe rc=0 connect=0.000300", "connected"},
		{"net route=none addr=192.0.2.5", "nonet"},
		{"net route=eth0 addr=none", "nonet"},
		{"net route=none addr=none\nprobe rc=7 connect=0.000000", "nonet"},
		{"net route=none addr=none note=reject", "nonet"},
		{"probe rc=7 connect=0.000000", "error"},
		{"", "error"},
		{"bwrap: setting up uid map: Permission denied", "error"},
		{"net route=eth0 addr=192.0.2.5\nprobe no-curl", "error"},
		{"net route=eth0 addr=192.0.2.5", "error"},
	} {
		out, code := netcheckRun(t, script, "/usr/bin:/bin", "--lead-class-of", c.text)
		if code != 0 || out != c.want+"\n" {
			t.Errorf("--lead-class-of %q: exit %d, %q, want %q", c.text, code, out, c.want)
		}
	}
	// NETUP itself, on answers of the kernel's lookup (review findings 6 and
	// 1 of two rounds): a stub ip, the only program on PATH, answers "ip -4
	// route get GATE" as ip does, and NETUP takes the route type, the output
	// interface and the source address from it, inferring nothing from
	// subnets or address lists. A loopback gate with a default route is the
	// kernel's local route and refused; a /32 address with an on-link peer
	// is a usable route with its source. An indeterminate lookup (no ip, a
	// lookup that fails, an answer out of its form) is refused before any
	// probe.
	ipStubs := filepath.Join(dir, "ip-stubs")
	if err := os.Mkdir(ipStubs, 0o700); err != nil {
		t.Fatal(err)
	}
	ipArgs := filepath.Join(dir, "ip-args")
	const gate = "198.51.100.7"
	for _, c := range []struct {
		name, gate, answer string
		rc                 int
		want               string
	}{
		{"a default route through a gateway", gate, gate + " via 192.0.2.1 dev eth0 src 192.0.2.15 uid 0 \n    cache \n", 0, "net route=eth0 addr=192.0.2.15"},
		{"a /32 address with an on-link peer", "192.0.2.99", "192.0.2.99 dev eth0 src 203.0.113.5 uid 0 \n    cache \n", 0, "net route=eth0 addr=203.0.113.5"},
		{"a loopback gate with a default route", "127.0.0.1", "local 127.0.0.1 dev lo src 127.0.0.1 uid 0 \n    cache <local> \n", 0, "net route=none addr=none note=local"},
		{"the gate's address the sandbox's own", "192.0.2.15", "local 192.0.2.15 dev lo table local src 192.0.2.15 uid 0 \n    cache <local> \n", 0, "net route=none addr=none note=local"},
		{"a unicast route whose source is the gate", gate, gate + " dev eth0 src " + gate + " uid 0 \n", 0, "net route=none addr=none note=local"},
		{"an unreachable route (the lookup fails)", gate, "RTNETLINK answers: Network is unreachable\n", 2, "net route=none addr=none note=lookup"},
		{"a lookup that fails after a well-formed line", gate, gate + " via 192.0.2.1 dev eth0 src 192.0.2.15 uid 0 \n", 2, "net route=none addr=none note=lookup"},
		{"a blackhole route the lookup names", gate, "blackhole " + gate + " dev lo uid 0 \n", 0, "net route=none addr=none note=reject"},
		{"a broadcast route", "192.0.2.255", "broadcast 192.0.2.255 dev eth0 table local src 192.0.2.15 uid 0 \n", 0, "net route=none addr=none note=type"},
		{"a unicast route on lo", gate, gate + " dev lo src 192.0.2.15 uid 0 \n", 0, "net route=none addr=none note=lo"},
		{"a gateway of 0.0.0.0", gate, gate + " via 0.0.0.0 dev eth0 src 192.0.2.15 uid 0 \n", 0, "net route=none addr=none note=nogateway"},
		{"no source address", gate, gate + " via 192.0.2.1 dev eth0 uid 0 \n", 0, "net route=none addr=none note=noaddress"},
		{"a loopback source on eth0", gate, gate + " dev eth0 src 127.0.0.2 uid 0 \n", 0, "net route=none addr=none note=noaddress"},
		{"no interface", gate, gate + " via 192.0.2.1 src 192.0.2.15 uid 0 \n", 0, "net route=none addr=none note=lookup"},
		{"an answer for another address", gate, "198.51.100.8 via 192.0.2.1 dev eth0 src 192.0.2.15 \n", 0, "net route=none addr=none note=lookup"},
		{"an empty answer", gate, "", 0, "net route=none addr=none note=lookup"},
		{"a gate that is not an address", "gate.example", gate + " via 192.0.2.1 dev eth0 src 192.0.2.15\n", 0, "net route=none addr=none note=gate"},
	} {
		stub := "#!/bin/sh\nfor a; do printf '%s\\n' \"$a\"; done >'" + ipArgs + "'\nprintf '%s' '" + c.answer + "'\nexit " + strconv.Itoa(c.rc) + "\n"
		if err := os.WriteFile(filepath.Join(ipStubs, "ip"), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
		os.Remove(ipArgs)
		out, code := netcheckRun(t, script, ipStubs, "--net-of", c.gate)
		if code != 0 || out != c.want+"\n" {
			t.Errorf("--net-of %s: exit %d, %q, want %q", c.name, code, out, c.want)
		}
		if c.want == "net route=none addr=none note=gate" {
			continue
		}
		if got := strings.Split(strings.TrimSpace(string(mustRead(t, ipArgs))), "\n"); !slices.Equal(got, []string{"-4", "route", "get", c.gate}) {
			t.Errorf("--net-of %s: ip was run with %q", c.name, got)
		}
	}
	if err := os.Remove(filepath.Join(ipStubs, "ip")); err != nil {
		t.Fatal(err)
	}
	if out, code := netcheckRun(t, script, ipStubs, "--net-of", gate); code != 0 || out != "net route=none addr=none note=noip\n" {
		t.Errorf("--net-of without ip: exit %d, %q", code, out)
	}
	// The sandbox finds ip in the host's /usr/bin or /usr/sbin.
	if !strings.Contains(string(mustRead(t, script)), "--setenv PATH /usr/bin:/usr/sbin") {
		t.Error("the sandbox's PATH does not hold /usr/sbin, where some hosts keep ip")
	}
	// The sandbox runs NETUP before the probe, and the verdict reads both
	// probes of the lead's network through it.
	text := string(mustRead(t, script))
	if !strings.Contains(text, `core /usr/bin/sh -c "$NETUP$PROBE" "$GATE_HOST" "$GATE_PORT"`) ||
		!strings.Contains(text, `verdict "$(status_class "$HRC" "$(class_of "$HOSTSIDE")")" "$(status_class "$SRC" "$(lead_class_of "$SHAPE")")" "$(status_class "$URC" "$(lead_class_of "$UNIT")")"`) {
		t.Error("the sandbox does not show its route to the gate before the probe, or the verdict does not read it with each wrapper's exit status")
	}
	for _, w := range []string{`|| HRC=$?`, `|| SRC=$?`, `UNIT=$(unit 2>&1) || URC=$?`} {
		if !strings.Contains(text, w) {
			t.Errorf("the script does not keep a wrapper's exit status: %s", w)
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
