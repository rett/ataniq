package pack_test

import (
	"fmt"
	"testing"

	"ataniq/internal/pack"
)

func TestBindRows(t *testing.T) {
	for _, tc := range []struct {
		role       pack.BindRole
		path, kind string
		valid      bool
	}{
		{pack.ProviderPolicy, "/etc/example/policy", "file", true},
		{pack.ProviderPolicy, "/etc", "file", false}, {pack.ProviderPolicy, "/etc2/p", "file", false},
		{pack.ProviderPolicy, "/etc/hosts", "file", false}, {pack.ProviderPolicy, "/etc/p", "folder", false},
		{pack.ProviderPolicy, "/etc/../etc/p", "file", false}, {pack.ProviderPolicy, "/etc/p\x00", "file", false},
		{pack.ConfigOverlay, "/config/agents", "folder", true}, {pack.ConfigOverlay, "/config", "folder", false},
		{pack.ConfigOverlay, "/config-old/agents", "folder", false}, {pack.ConfigOverlay, "/config/skills/x", "file", false},
		{pack.ConfigOverlay, "/config/instructions", "file", false}, {pack.ConfigOverlay, "/config/instructions-old", "file", true},
		{pack.TreeOverlay, "project/agents", "folder", true}, {pack.TreeOverlay, ".git", "file", false},
		{pack.TreeOverlay, ".git/config", "file", false}, {pack.TreeOverlay, ".github", "folder", true},
		{pack.TreeOverlay, ".", "folder", false}, {pack.TreeOverlay, "../other", "folder", false},
		{pack.TreeOverlay, "/absolute", "file", false}, {pack.TreeOverlay, "dir//file", "file", false},
		{pack.SessionStore, "/config/sessions", "folder", true}, {pack.SessionStore, "/config/sessions", "file", false},
		{pack.SessionStore, "/etc/sessions", "folder", false}, {pack.SessionStore, "/config/skills", "folder", false},
		{"helper", "/config/helper", "file", false}, {pack.ConfigOverlay, "/config/agent", "link", false},
	} {
		row := pack.Bind{Role: tc.role, Inside: tc.path, Kind: tc.kind}
		if err := validateSandbox(pack.Sandbox{Binds: []pack.Bind{row}}); (err == nil) != tc.valid {
			t.Errorf("%+v: %v", row, err)
		}
	}
	binds := make([]pack.Bind, 13)
	for i := range binds {
		binds[i] = pack.Bind{Role: pack.TreeOverlay, Inside: fmt.Sprintf("row%d", i), Kind: "folder"}
	}
	if err := validateSandbox(pack.Sandbox{Binds: binds[:12]}); err != nil {
		t.Fatal(err)
	}
	if err := validateSandbox(pack.Sandbox{Binds: binds}); err == nil {
		t.Error("accepted 13 binds")
	}
	for _, target := range []string{"row0", "row0/file"} {
		binds[1].Inside = target
		if err := validateSandbox(pack.Sandbox{Binds: binds[:2]}); err == nil {
			t.Errorf("accepted overlapping bind %q", target)
		}
	}
}

func TestAgentDefinition(t *testing.T) {
	for _, tc := range []struct {
		definition     string
		builtin, valid bool
	}{
		{"", true, true}, {"/config/agents/a", false, true}, {"/tree/.example/a", false, true},
		{"/config/file", false, true}, {"/config/file/a", false, false}, {"/config/agents", false, false},
		{"/config/agents/a", true, false}, {"/config/agents/../a", false, false},
		{"/config/sessions/a", false, false}, {"/config/agents-other/a", false, false},
		{"/config/optional/a", false, false}, {"/tree/a", false, false}, {"", false, false},
	} {
		l := sampleLaunch()
		l.Agents = []pack.Agent{{Name: "sample", Builtin: tc.builtin, Definition: tc.definition}}
		s := pack.Sandbox{Launch: &l, Binds: []pack.Bind{
			{Role: pack.ConfigOverlay, Kind: "folder", Inside: "/config/agents"},
			{Role: pack.ConfigOverlay, Kind: "file", Inside: "/config/file"},
			{Role: pack.TreeOverlay, Kind: "folder", Inside: ".example"},
			{Role: pack.ConfigOverlay, Kind: "folder", Inside: "/config/optional", Optional: true},
			{Role: pack.SessionStore, Kind: "folder", Inside: "/config/sessions"},
		}}
		if err := validateSandbox(s); (err == nil) != tc.valid {
			t.Errorf("agent %+v: %v", l.Agents[0], err)
		}
	}
}
