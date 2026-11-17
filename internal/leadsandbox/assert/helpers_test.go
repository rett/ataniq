package assert

import (
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// testSpec is a valid spec for a runtime. Every path is an invented example.
func testSpec(rt profile.Runtime) Spec {
	s := Spec{
		Version: SpecVersion,
		Digest:  strings.Repeat("ab", 32),
		Runtime: rt,
		Env: []EnvEntry{
			{"HOME", "/home/lead"}, {"PATH", "/usr/bin:/bin"}, {"TERM", "xterm-256color"},
		},
		HolderPaths:         []string{"/run/example/holder.sock", "/run/example/control.sock", "/var/lib/example/holder"},
		MountSubstrings:     []string{"/run/example", "/home/owner"},
		ForbiddenIdentities: []FileIdentity{{Device: 7, Inode: 70}, {Device: 7, Inode: 71}},
		OwnerPaths: []string{
			"/home/owner/.claude", "/home/owner/.claude.json", "/home/owner/.codex",
			"/home/owner/.ssh", "/home/owner/work/ataniq",
		},
		OtherConfigDir:  profile.ConfigInside(rt.Other()),
		LoopbackTCP:     "127.0.0.1:40123",
		AbstractSocket:  "example-canary-1",
		ProviderHost:    "api.example.org",
		Tree:            "/work/ataniq",
		GitReadOnly:     []string{"/work/ataniq/.git/config", "/work/ataniq/.git/hooks", "/work/ataniq/.git/info"},
		ManagedSettings: profile.ClaudeManagedInside,
		ManagedMarker:   "example-guard-hook",
	}
	if rt == profile.Codex {
		s.ManagedSettings = "/etc/codex/managed-config.toml"
		s.ManagedMarker = ""
	}
	return s
}

func refusalOf(t *testing.T, err error) *Refusal {
	t.Helper()
	r, ok := err.(*Refusal)
	if !ok {
		t.Fatalf("got %T %v, want a *Refusal", err, err)
	}
	return r
}
