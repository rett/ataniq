package sandbox_test

import (
	"testing"

	lp "ataniq/internal/launchpolicy"
	"ataniq/internal/leadcanary/provision/hookrender/rendertest"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/packs/claude/sandbox"
	"ataniq/packs/holder"
)

func TestPinnedRender(t *testing.T) {
	const version = "2.1.332"
	p, help, ok := holder.LaunchPolicy("claude", version)
	if !ok {
		t.Fatal("missing pinned policy")
	}
	inventory := lp.Inventory{
		{PathRole: "provider-policy", Dest: "/etc/claude-code/managed-settings.json"},
		{PathRole: "user-settings", Dest: "/home/lead/.claude/settings.json"},
		{PathRole: "local-settings", Dest: "/home/lead/.claude/settings.local.json"},
		{PathRole: "agent", Dest: "/home/lead/.claude/agents/reviewer.md", Definition: true, AgentName: "reviewer"},
		{PathRole: "trust-entry", Dest: "/home/lead/.claude/.claude.json"},
	}
	rendertest.Pinned(t, sandbox.Files(), p, help, profile.Claude, version, "reviewer", inventory)
}
