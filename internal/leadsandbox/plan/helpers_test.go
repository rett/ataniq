package plan

import (
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// fixture returns valid inputs for a runtime. Every path is an invented
// example: the state under /state, the folders Ataniq owns under /opt/ataniq
// and the agent under /opt/agent.
func fixture(rt profile.Runtime) profile.Inputs {
	in := profile.Inputs{
		StateRoot:             "/state",
		Role:                  "arc-lead",
		Project:               "demo",
		Runtime:               rt,
		RuntimeVersion:        "2.1.0",
		Identity:              profile.IdentityRoot,
		AgentInstall:          "/opt/agent/" + string(rt) + "/2.1.0",
		ProbeBinary:           "/opt/ataniq/bin/probe",
		SpecDir:               "/opt/ataniq/spec/arc-lead",
		ManagedSettings:       "/opt/ataniq/managed/arc-lead/settings.json",
		ManagedSettingsSHA256: strings.Repeat("c1", 32),
		LeadSkills:            "/opt/ataniq/skills/arc-lead",
		InstructionFile:       "/opt/ataniq/instructions/arc-lead/" + rt.InstructionFileName(),
		GitIdentityFile:       "/opt/ataniq/git/arc-lead/config",
		GitMeta:               "/state/leads/arc-lead/gitmeta",
		QargiInstall:          "/opt/ataniq/qargi",
		AssignmentDir:         "/opt/ataniq/assignment/demo",
		ProjectionDir:         "/opt/ataniq/projection/demo",
		ResolvConf:            "/opt/ataniq/resolv/arc-lead/resolv.conf",
		NeverMounted: []string{
			"/home/owner/work/demo",
			"/home/owner/.claude",
			"/home/owner/.claude.json",
			"/home/owner/.codex",
			"/home/owner/.ssh",
		},
		Term:      "xterm-256color",
		ColorTerm: "truecolor",
		Lang:      "C.UTF-8",
		TZ:        "UTC",
	}
	if rt == profile.Codex {
		in.ManagedSettingsInside = "/etc/codex/managed-config.toml"
	}
	return in
}

func ownerMode(in profile.Inputs) profile.Inputs {
	in.Identity = profile.IdentityOwner
	in.OwnerUID, in.OwnerGID, in.OwnerUser = 1000, 1001, "owner"
	return in
}

func withSeccomp(in profile.Inputs) profile.Inputs {
	in.SeccompFile = "/opt/ataniq/seccomp/lead.bpf"
	in.SeccompSHA256 = strings.Repeat("5e", 32)
	return in
}

// allPresent says the host has every optional row of the table.
func allPresent(t *testing.T, in profile.Inputs) map[string]bool {
	t.Helper()
	binds, err := in.Binds()
	if err != nil {
		t.Fatalf("Binds: %v", err)
	}
	m := map[string]bool{}
	for _, b := range binds {
		if b.Optional {
			m[b.Inside] = true
		}
	}
	return m
}

func options(in profile.Inputs, present map[string]bool) Options {
	o := Options{
		PastaPath:   "/usr/bin/pasta",
		BwrapPath:   "/usr/bin/bwrap",
		AgentArgv:   []string{"/ataniq/agent/bin/" + string(in.Runtime), "--model", "example"},
		HostPresent: present,
		HostSources: hostSources(in, present),
		ReportPath:  "/opt/ataniq/run/arc-lead/report.fifo",
		ReportDir:   "/opt/ataniq/run/arc-lead",
		ReportFD:    3,
	}
	if in.SeccompEnabled() {
		o.SeccompFD = 7
	}
	return o
}

// hostSources gives, for each row of the host the plan emits, the row's own
// path as its real path: a host whose paths hold no link.
func hostSources(in profile.Inputs, present map[string]bool) map[string]string {
	binds, err := in.Binds()
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, b := range binds {
		if b.Kind == profile.KindRoBind && b.SourceRole == profile.RoleHost && (!b.Optional || present[b.Inside]) {
			m[b.Inside] = b.Inside
		}
	}
	return m
}

func mustBuild(t *testing.T, in profile.Inputs, o Options) *Plan {
	t.Helper()
	p, err := Build(in, o)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p
}

// basePlan is a plan for claude, root mode, no seccomp, every optional row
// present.
func basePlan(t *testing.T) (*Plan, profile.Inputs, Options) {
	t.Helper()
	in := fixture(profile.Claude)
	o := options(in, allPresent(t, in))
	return mustBuild(t, in, o), in, o
}

func clonePlan(p *Plan) *Plan {
	c := *p
	c.Argv = append([]string(nil), p.Argv...)
	c.Pasta = append([]string(nil), p.Pasta...)
	c.PassFDs = append([]string(nil), p.PassFDs...)
	c.Bwrap = append([]string(nil), p.Bwrap...)
	c.Inner = append([]string(nil), p.Inner...)
	c.Env = append([]profile.EnvVar(nil), p.Env...)
	c.Files = append([]File(nil), p.Files...)
	c.Mounts = append([]Mount(nil), p.Mounts...)
	c.Notes = append([]string(nil), p.Notes...)
	return &c
}

// resync rebuilds Argv from the parts after a test changed one of them.
func resync(p *Plan) *Plan {
	p.Argv = join(p.Pasta, p.PassFDs, p.Bwrap, p.Inner)
	return p
}

// render writes a plan as the text of a golden file: one option with its
// operands per line. It splits at arguments that start with "--", which no
// operand of a valid plan does, and writes the agent argument list on one
// line.
func render(p *Plan) string {
	var b strings.Builder
	line := func(s ...string) { b.WriteString(strings.Join(s, " ") + "\n") }
	b.WriteString("# pasta\n")
	line(p.Pasta...)
	b.WriteString("# pass-fds\n")
	line(p.PassFDs...)
	b.WriteString("# bubblewrap\n")
	line(p.Bwrap[0])
	var cur []string
	for _, a := range p.Bwrap[1:] {
		if strings.HasPrefix(a, "--") && len(cur) > 0 {
			line(cur...)
			cur = nil
		}
		cur = append(cur, a)
	}
	line(cur...)
	b.WriteString("# inner\n")
	line(p.Inner...)
	b.WriteString("# env\n")
	for _, e := range p.Env {
		line(e.Name + "=" + e.Value)
	}
	b.WriteString("# mounts\n")
	for _, m := range p.Mounts {
		cols := []string{string(m.Kind), m.Inside}
		if m.Source != "" {
			cols = append(cols, m.Source)
		}
		if m.LeadWritable {
			cols = append(cols, "[lead-writable]")
		}
		if m.Optional {
			cols = append(cols, "[optional]")
		}
		line(cols...)
	}
	b.WriteString("# files\n")
	for _, f := range p.Files {
		line(strings.TrimSpace(strings.Join([]string{"fd", strconv.Itoa(f.FD), string(f.Mode), f.Purpose, f.Path, f.SHA256}, " ")))
	}
	b.WriteString("# digest\n")
	line(p.Digest)
	b.WriteString("# workdir\n")
	line(p.WorkDir)
	b.WriteString("# set-controlling-terminal\n")
	line(boolText(p.SetControllingTerminal))
	b.WriteString("# notes\n")
	for _, n := range p.Notes {
		line("-", n)
	}
	return b.String()
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func hasKind(probs []Problem, k ProblemKind) bool {
	for _, p := range probs {
		if p.Kind == k {
			return true
		}
	}
	return false
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}
