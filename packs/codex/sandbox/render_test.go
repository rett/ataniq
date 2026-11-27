package sandbox_test

import (
	"testing"

	lp "ataniq/internal/launchpolicy"
	"ataniq/internal/leadcanary/provision/hookrender/rendertest"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/packs/codex/sandbox"
	"ataniq/packs/holder"
)

func TestPinnedRender(t *testing.T) {
	const version = "0.172.0"
	p, help, ok := holder.LaunchPolicy("codex", version)
	if !ok {
		t.Fatal("missing pinned policy")
	}
	inventory := lp.Inventory{
		{PathRole: "provider-policy", Dest: "/etc/codex/requirements.toml"},
		{PathRole: "managed-hooks", Dest: "/etc/codex/hooks.json"},
		{PathRole: "config", Dest: "/home/lead/.codex/config.toml"},
	}
	rendertest.Pinned(t, sandbox.Files(), p, help, profile.Codex, version, "", inventory)
}
