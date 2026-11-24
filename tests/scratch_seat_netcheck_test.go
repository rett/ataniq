package tests

import (
	"path/filepath"
	"regexp"
	"slices"
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

	// The runbook says the same.
	runbook := strings.Join(strings.Fields(string(mustRead(t, filepath.Join(root(t), "docs", "scratch-seat-proof.md")))), " ")
	for _, want := range []string{"`sandboxnet.PastaArgs()`", "the profile's flag set for the identity a scratch lead has"} {
		if !strings.Contains(runbook, want) {
			t.Errorf("the runbook does not say %q", want)
		}
	}
}
