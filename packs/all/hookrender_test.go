//go:build ataniq_canary

package all_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/agentsession/hookwire"
	lp "ataniq/internal/launchpolicy"
	"ataniq/internal/leadcanary/provision/hookrender"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/pack"
	"ataniq/packs/all"
	"ataniq/packs/holder"
)

func source(t *testing.T, rt profile.Runtime) hookrender.Source {
	t.Helper()
	layout := pack.BindLayout{Config: profile.ConfigInside(rt), Tree: profile.TreeInside("sample"),
		CoreRows: []string{profile.HomeInside, profile.ConfigInside(rt), profile.SkillsInside(rt), profile.InstructionInside(rt), profile.TreeInside("sample")},
		CheckExtraEnv: func(m map[string]string) error {
			for k, v := range m {
				if err := profile.CheckEnvName(k); err != nil {
					return err
				}
				if err := profile.CheckValue(k, v); err != nil {
					return err
				}
			}
			return nil
		},
	}
	r := pack.New()
	if err := all.Register(r); err != nil {
		t.Fatal(err)
	}
	r.Freeze()
	p, ok := r.Lookup(pack.Name(rt))
	if !ok {
		t.Fatal("missing registered pack", rt)
	}
	src, err := hookrender.FromPack(p, layout)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func formatFiles(candidate hookrender.Files) string {
	var artifacts strings.Builder
	for _, doc := range candidate.Documents {
		fmt.Fprintf(&artifacts, "=== %s %s ===\n%s", doc.Role, doc.Dest, doc.Content)
	}
	artifacts.WriteString("=== empty tree overlays ===\n")
	for _, b := range candidate.TreeOverlays {
		fmt.Fprintf(&artifacts, "%s %s\n", b.Kind, b.Inside)
	}
	fmt.Fprintf(&artifacts, "=== helper table ===\n%s\n", candidate.HelperTable)
	return artifacts.String()
}

func golden(t *testing.T, fixtures fs.FS, version, name, got string) {
	t.Helper()
	file := path.Join(version, name+".golden")
	want, err := fs.ReadFile(fixtures, file)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s differs:\n%s", file, got)
	}
}

func checkGoldenInventory(t *testing.T, fixtures fs.FS, version string, policy []byte) []mode {
	t.Helper()
	doc := parse(t, policy)
	modes := doc.Slots.Permission.Values
	leadUsable := false
	for _, m := range modes {
		switch m.Class {
		case pack.Ask, pack.Plan, pack.AcceptEdits:
			leadUsable = true
		}
	}
	if doc.Fixture.SHA == "" || !leadUsable {
		// Inventory the entire golden directory, including nested files. A
		// positive fixture cannot be added by just giving it another name.
		err := fs.WalkDir(fixtures, ".", func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				return nil
			}
			if p != path.Join(version, "files.golden") && p != path.Join(version, "refusals.golden") {
				return fmt.Errorf("unexpected fixture for unqualified build: %s", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return modes
}

func TestClaudePinnedRender(t *testing.T) {
	const version = "2.1.332"
	inventory := lp.Inventory{
		{PathRole: "provider-policy", Dest: "/etc/claude-code/managed-settings.json"},
		{PathRole: "user-settings", Dest: "/home/lead/.claude/settings.json"},
		{PathRole: "local-settings", Dest: "/home/lead/.claude/settings.local.json"},
		{PathRole: "agent", Dest: "/home/lead/.claude/agents/reviewer.md", Definition: true, AgentName: "reviewer"},
		{PathRole: "trust-entry", Dest: "/home/lead/.claude/.claude.json"},
	}
	checkPinned(t, profile.Claude, version, "reviewer", inventory)
}

func TestCodexPinnedRender(t *testing.T) {
	const version = "0.172.0"
	inventory := lp.Inventory{
		{PathRole: "provider-policy", Dest: "/etc/codex/requirements.toml"},
		{PathRole: "managed-hooks", Dest: "/etc/codex/hooks.json"},
		{PathRole: "config", Dest: "/home/lead/.codex/config.toml"},
	}
	checkPinned(t, profile.Codex, version, "", inventory)
}

// checkPinned checks an exact build. Inventory is the test's independent statement
// of the destinations expected from provisioning.
func checkPinned(t *testing.T, rt profile.Runtime, version, agent string, inv lp.Inventory) {
	t.Helper()
	fixtures := os.DirFS(path.Join("..", string(rt), "sandbox/testdata"))
	policy, help, ok := holder.LaunchPolicy(pack.Name(rt), version)
	if !ok {
		t.Fatal("missing pinned policy")
	}
	src := source(t, rt)
	d := src.Declaration
	s := lp.Snapshot{Pack: d.Manifest.Name, Version: version, Role: "sample/lead", Transport: pack.Terminal, Agent: agent}
	candidate, err := src.Preview(s)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := hookwire.DecodeTable([]byte(candidate.HelperTable))
	if err != nil || len(tables) != 1 || !reflect.DeepEqual(tables[version], hookwire.Table(d.Hooks[version])) {
		t.Fatalf("helper table differs from declared rows: %v", err)
	}
	golden(t, fixtures, version, "files", formatFiles(candidate))
	modes := checkGoldenInventory(t, fixtures, version, policy)
	decode := func(raw []byte) *lp.Policy {
		p, err := lp.Decode(raw, help)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := decode(policy)
	bind := func() {
		var err error
		s.Requested, err = json.Marshal(lp.View{Pack: s.Pack, Version: s.Version, Mode: s.Mode})
		if err != nil {
			t.Fatal(err)
		}
		s.ProfileDigest, _, _ = intent(s.Requested)
	}
	var refusals strings.Builder
	refused := func(label string, err error) {
		t.Helper()
		var r *lp.Refusal
		if !errors.As(err, &r) || r == nil {
			t.Fatalf("%s: expected matcher refusal, got %v", label, err)
		}
		fmt.Fprintf(&refusals, "%s: %s\n", label, r)
	}
	render := func(label string) {
		t.Helper()
		out, err := src.Render(s, p, intent, inv)
		if !reflect.DeepEqual(out, hookrender.Launch{}) {
			t.Fatal("refusal exposed a partial render")
		}
		refused(label, err)
	}
	bind()
	render("shipped")
	// Decode itself refuses recorded help with pending evidence locators.
	// Supply documentary placeholders in this copy only. Keep the original
	// help, pending vectors, mode classes, forms, and file rules intact.
	recorded := bytes.Replace(policy, []byte(`"sha256": ""`), fmt.Appendf(nil, `"sha256": "%x"`, sha256.Sum256(help)), 1)
	if _, err := lp.Decode(recorded, help); err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("hash alone: %v", err)
	}
	recorded = bytes.ReplaceAll(recorded, []byte(`"source": "pending"`), []byte(`"source": "doc", "ref":"test-only"`))
	p = decode(recorded)
	l := *d.Sandbox[version].Launch
	for _, m := range modes {
		if !slices.ContainsFunc(l.PermissionModes, func(entry pack.PermissionMode) bool { return entry.Name == m.Name }) {
			t.Fatal("missing declared mode", m.Name)
		}
		s.Mode, s.Recognizer = m.Name, m.Recognizer
		for _, role := range []string{"sample/lead", "sample/coord/f1"} {
			s.Role = role
			bind()
			label := role + " " + m.Name
			render(label + " render")
			// Selection refuses the lead first. Check the very same rendered
			// argv independently to pin the later class refusal for both roles.
			spec, r := lp.Expand(p, l, s)
			if r != nil {
				t.Fatal(r)
			}
			_, r = lp.Recheck(p, spec, s, intent)
			refused(label+" expanded", r)
		}
	}
	refused("candidate files", candidate.Check(p, s, inv))
	// Exercise the first refused destination in isolation too. In a full
	// inventory the matcher prints only its ordinal for an absolute path.
	first := slices.MinFunc(inv, func(a, b lp.ExpectedFile) int { return strings.Compare(a.Dest, b.Dest) })
	for _, file := range candidate.Documents {
		if file.Dest == first.Dest {
			only := hookrender.Files{Documents: []hookrender.Document{file}}
			refused("isolated file "+first.Dest, only.Check(p, s, lp.Inventory{first}))
		}
	}
	golden(t, fixtures, version, "refusals", refusals.String())
}
