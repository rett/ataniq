// Package rendertest runs the shared refusal contract in each shipped pack's
// sandbox tests. Its inputs are the real embedded sandbox and holder policy.
package rendertest

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

	lp "ataniq/internal/launchpolicy"
	"ataniq/internal/leadcanary/provision/hookrender"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/pack"
)

func read(t *testing.T, fsys fs.FS, p string) []byte {
	t.Helper()
	b, err := fs.ReadFile(fsys, p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func source(t *testing.T, rt profile.Runtime, files fs.FS) hookrender.Source {
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
	root := os.DirFS("..")
	src, err := hookrender.Decode(read(t, root, pack.ManifestPath), read(t, root, pack.HooksPath), files, layout)
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

func golden(t *testing.T, version, name, got string) {
	t.Helper()
	file := path.Join("testdata", version, name+".golden")
	if want := string(read(t, os.DirFS("."), file)); got != want {
		t.Fatalf("%s differs:\n%s", file, got)
	}
}

type mode struct {
	Name, Recognizer string
	Class            pack.ModeClass
}

func checkGoldenInventory(t *testing.T, version string, policy []byte) []mode {
	t.Helper()
	var doc struct {
		Fixture struct {
			SHA string `json:"sha256"`
		}
		Slots struct {
			Permission struct {
				Values []mode
			} `json:"permission_mode"`
		}
	}
	if err := json.Unmarshal(policy, &doc); err != nil {
		t.Fatal(err)
	}
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
		err := fs.WalkDir(os.DirFS("testdata"), ".", func(p string, e fs.DirEntry, err error) error {
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

// Pinned checks an exact build. Inventory is the test's independent statement
// of the destinations expected from provisioning.
func Pinned(t *testing.T, files fs.FS, policy, help []byte, rt profile.Runtime, version, agent string, inv lp.Inventory) {
	t.Helper()
	src := source(t, rt, files)
	d := src.Declaration
	s := lp.Snapshot{Pack: d.Manifest.Name, Version: version, Role: "sample/lead", Transport: pack.Terminal, Agent: agent}
	candidate, err := src.Preview(s)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, version, "files", formatFiles(candidate))
	modes := checkGoldenInventory(t, version, policy)
	decode := func(raw []byte) *lp.Policy {
		p, err := lp.Decode(raw, help)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := decode(policy)
	intent := func(raw []byte) (string, lp.View, error) {
		var v lp.View
		err := json.Unmarshal(raw, &v)
		return fmt.Sprintf("%x", sha256.Sum256(raw)), v, err
	}
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
	golden(t, version, "refusals", refusals.String())
}
