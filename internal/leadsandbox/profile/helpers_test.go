package profile

import (
	"errors"
	"strings"
	"testing"
)

// fixture returns valid inputs for a runtime. Every path is an invented
// example: the owner's folders under /home/owner, the state under /srv/state
// and the folders Ataniq owns under /srv/owned.
func fixture(rt Runtime) Inputs {
	in := Inputs{
		StateRoot:             "/srv/state",
		Role:                  "arc-lead",
		Project:               "ataniq",
		Runtime:               rt,
		RuntimeVersion:        "2.1.0",
		Identity:              IdentityRoot,
		AgentInstall:          "/srv/owned/agent/" + string(rt) + "/2.1.0",
		ProbeBinary:           "/srv/owned/bin/probe",
		SpecDir:               "/srv/owned/spec/arc-lead",
		ManagedSettings:       "/srv/owned/managed/arc-lead/settings.json",
		ManagedSettingsSHA256: strings.Repeat("c1", 32),
		LeadSkills:            "/srv/owned/skills/arc-lead",
		InstructionFile:       "/srv/owned/instructions/arc-lead/" + rt.InstructionFileName(),
		GitIdentityDir:        "/srv/owned/git/arc-lead",
		GitMeta:               "/srv/state/leads/arc-lead/gitmeta",
		QargiInstall:          "/srv/owned/qargi",
		QargiRecordsInside:    "/home/lead/.qargi",
		AssignmentDir:         "/srv/owned/assignment/ataniq",
		ProjectionDir:         "/srv/owned/projection/ataniq",
		ResolvConf:            "/srv/owned/resolv/arc-lead/resolv.conf",
		NeverMounted: []string{
			"/home/owner/work/ataniq",
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
	if rt == Codex {
		in.ManagedSettingsInside = "/etc/codex/managed-config.toml"
	}
	return in
}

// refusalOf returns the *Refusal inside err, or fails the test.
func refusalOf(t *testing.T, err error) *Refusal {
	t.Helper()
	var ref *Refusal
	if !errors.As(err, &ref) {
		t.Fatalf("got %v, want a *Refusal", err)
	}
	return ref
}

func mustBinds(t *testing.T, in Inputs) []Bind {
	t.Helper()
	b, err := in.Binds()
	if err != nil {
		t.Fatalf("Binds: %v", err)
	}
	return b
}

// row renders a bind as one line of the golden tables.
func row(b Bind) string {
	s := string(b.Kind) + " " + b.Inside
	switch {
	case b.Kind == KindSymlink:
		s += " -> " + b.Target
	case b.SourceRole != RoleNone:
		s += " src=" + b.SourceRole
	}
	if b.LeadWritable {
		s += " writable-in=" + b.Within
	}
	if b.Optional {
		s += " optional"
	}
	return s
}

func rows(t []Bind) []string {
	out := make([]string, len(t))
	for i, b := range t {
		out[i] = row(b)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func containsSub(list []string, sub string) bool {
	for _, x := range list {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}
