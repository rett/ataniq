//go:build ataniq_canary

package hookrender_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	lp "ataniq/internal/launchpolicy"
	"ataniq/internal/leadcanary/provision/hookrender"
	"ataniq/internal/pack"
	"ataniq/internal/pack/packtest"
)

const (
	settings  = "/etc/example/settings.json"
	agentFile = "/config/agents/reviewer.md"
	alphaDocs = `{
		"/etc/example/settings.json":{"role":"settings","format":"json","values":{"items":{"0":true}}},
		"/config/agents/reviewer.md":{"role":"agent","format":"frontmatter","values":{"name":"reviewer"},"body":"Review.\n"}
	}`
)

type trial struct {
	src hookrender.Source
	s   lp.Snapshot
	p   *lp.Policy
	inv lp.Inventory
}

func synthetic(t *testing.T, beta bool) trial {
	t.Helper()
	m := packtest.Alpha.Manifest()
	doc, help := packtest.AlphaPolicy(), packtest.AlphaHelp()
	s := lp.Snapshot{Version: "1.0", Role: "sample/lead", Transport: pack.Terminal, Agent: "reviewer", Mode: "m-plan", Recognizer: "plan", Model: "m-x"}
	launch := pack.Launch{Argv: []string{"alpha-cli", "--mode={permission_mode}", "--model={model}", "--agent={agent}"},
		Grammars:        map[pack.Slot]string{pack.ModelSlot: "^[a-z][a-z0-9.-]{0,31}$"},
		PermissionModes: []pack.PermissionMode{{Name: "m-plan", Lead: true}},
		Agents:          []pack.Agent{{Name: "reviewer", Definition: agentFile}},
		Env:             map[string]string{"ALPHA_OPT": "1"},
	}
	binds := []pack.Bind{{Role: pack.ProviderPolicy, Kind: "file", Inside: settings}, {Role: pack.ConfigOverlay, Kind: "folder", Inside: "/config/agents"}}
	inv := lp.Inventory{{PathRole: "settings", Dest: settings}, {PathRole: "agent", Dest: agentFile, Definition: true, AgentName: s.Agent}}
	template := alphaDocs
	if beta {
		m = packtest.Beta.Manifest()
		doc, help = packtest.BetaPolicy(), packtest.BetaHelp()
		s.Role, s.Mode, s.Transport, s.OwnerProtocol, s.Agent, s.Recognizer = "sample/coord/f1", "q-quiet", pack.Protocol, true, "", ""
		launch = pack.Launch{Argv: []string{"beta-cli", "--mode={permission_mode}", "--", "{model}"}, Grammars: launch.Grammars,
			PermissionModes: []pack.PermissionMode{{Name: "q-quiet"}}}
		binds, inv, template = nil, nil, "{}"
	}
	s.Pack = m.Name
	v := lp.View{Pack: s.Pack, Version: s.Version, Mode: s.Mode, Model: s.Model}
	s.Requested, _ = json.Marshal(v)
	s.ProfileDigest, _, _ = intent(s.Requested)
	p, err := lp.Decode(doc, help)
	if err != nil {
		t.Fatal(err)
	}
	src := hookrender.Source{
		Declaration: &pack.Declaration{Manifest: m, Hooks: map[string][]pack.HookRow{"1.0": {}},
			Sandbox: map[string]pack.Sandbox{"1.0": {Launch: &launch, Binds: binds}}},
		Layout: pack.BindLayout{Config: "/config", Tree: "/tree", CoreRows: []string{"/config", "/tree"},
			CheckExtraEnv: func(env map[string]string) error { return nil }},
		Templates: fstest.MapFS{"1.0.tmpl": {Data: []byte(template)}},
	}
	return trial{src, s, p, inv}
}

func intent(raw []byte) (string, lp.View, error) {
	var v lp.View
	err := json.Unmarshal(raw, &v)
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	if v.Pack == packtest.Beta.Name() {
		digest = "d1"
	} // Profile exercised by BetaPolicy.
	return digest, v, err
}

func (x trial) render() (hookrender.Launch, error) { return x.src.Render(x.s, x.p, intent, x.inv) }

func TestSyntheticRender(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil || len(entries) != 1 || entries[0].Name() != "synthetic.golden" {
		t.Fatalf("unexpected core render fixtures: %v (%v)", entries, err)
	}
	var buf strings.Builder
	buf.WriteString("Synthetic packtest launches only. These fixtures provide no evidence about a real build.\n")
	for _, beta := range []bool{false, true} {
		x := synthetic(t, beta)
		roles := []string{"sample/coord/f1"}
		if !beta {
			roles = append(roles, "sample/lead")
		}
		for _, role := range roles {
			x.s.Role = role
			result, err := x.render()
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&buf, "=== %s %s ===\n", x.s.Pack, role)
			b, err := json.Marshal(result.Spec)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&buf, "%s\nclass %s\n", b, result.Verdict.Class)
			for _, d := range result.Files.Documents {
				fmt.Fprintf(&buf, "%s %s\n%s", d.Role, d.Dest, d.Content)
			}
		}
	}
	want, err := os.ReadFile("testdata/synthetic.golden")
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Fatalf("synthetic render differs:\n%s", buf.String())
	}
}

func TestOversizeHelperTable(t *testing.T) {
	x := synthetic(t, false)
	x.src.Declaration.Hooks[x.s.Version] = []pack.HookRow{{
		ID: "start", Event: strings.Repeat("e", pack.MaxDeclarationBytes), Output: "none", TimeoutMS: 1,
	}}
	out, err := x.src.Preview(x.s)
	if err == nil || !reflect.DeepEqual(out, hookrender.Files{}) {
		t.Fatal("oversize helper table must refuse the whole preview")
	}
}

func TestMalformedFileTemplates(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"extra file", settings, "/etc/example/other.json"},
		{"missing selected definition", agentFile, "/config/other.md"},
		{"wrong format", `"format":"json"`, `"format":"toml"`},
		{"unknown member", `"role":"settings"`, `"role":"settings","extra":true`},
		{"duplicate key", `"0":true`, `"0":true,"0":false`},
		{"fractional field", `"0":true`, `"0":1.00000000000000000001`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := synthetic(t, false)
			x.replace(tc.old, tc.replacement)
			if out, err := x.render(); err == nil || !reflect.DeepEqual(out, hookrender.Launch{}) {
				t.Fatalf("got launch=%+v error=%v", out, err)
			}
		})
	}
}

func TestRefusedRenderReturnsNothing(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		stage      int
		change     func(*trial)
	}{
		{"written token", "literal-in-slot", 1, func(x *trial) { x.src.Declaration.Sandbox["1.0"].Launch.Argv[1] = "--mode=m-plan" }},
		{"environment selection", "selection-outside-policy", 0, func(x *trial) { x.src.Declaration.Sandbox["1.0"].Launch.Env["EXTRA"] = "1" }},
		{"wrong requested mode", "expected-differs", 0, func(x *trial) { x.s.Mode = "m-edit" }},
		{"footer differs", "expected-recognizer", 3, func(x *trial) { x.s.Recognizer = "manual" }},
		{"required setting absent", "file-refused", 2, func(x *trial) { x.replace(`"items":{"0":true}`, `"other":true`) }},
		{"unlisted setting", "file-refused", 2, func(x *trial) { x.replace(`"0":true`, `"0":true,"1":false`) }},
		{"array index is not a string key", "file-refused", 2, func(x *trial) { x.replace(`{"0":true}`, `[true]`) }},
		{"frontmatter key", "agent-key-refused", 2, func(x *trial) { x.replace(`"name":"reviewer"`, `"name":"reviewer","tools":"all"`) }},
		{"definition identity", "file-refused", 2, func(x *trial) { x.replace(`"name":"reviewer"`, `"name":"builder"`) }},
		{"missing from independent inventory", "file-refused", 2, func(x *trial) { x.inv = x.inv[:1] }},
		{"inventory needs another file", "file-refused", 2, func(x *trial) {
			x.inv = append(x.inv, lp.ExpectedFile{Dest: "/etc/example/extra.json", PathRole: "extra"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := synthetic(t, false)
			tc.change(&x)
			out, err := x.render()
			var r *lp.Refusal
			if !errors.As(err, &r) || r.Code != tc.code || r.Stage != tc.stage || !reflect.DeepEqual(out, hookrender.Launch{}) {
				t.Fatalf("got launch=%+v error=%v", out, err)
			}
		})
	}
}

func (x *trial) replace(old, next string) {
	x.src.Templates = fstest.MapFS{"1.0.tmpl": {Data: []byte(strings.Replace(alphaDocs, old, next, 1))}}
}

func TestFileBinding(t *testing.T) {
	x := synthetic(t, false)
	f, err := x.src.Preview(x.s)
	if err != nil {
		t.Fatal(err)
	}
	f.Documents[0].Content += "substituted"
	var r *lp.Refusal
	if err := f.Check(x.p, x.s, x.inv); !errors.As(err, &r) || r.Code != "file-binding" {
		t.Fatal(err)
	}
}

func TestTOMLFieldsMatchEncodedKeys(t *testing.T) {
	x := synthetic(t, false)
	const dest = "/etc/example/settings.toml"
	x.src.Declaration.Sandbox["1.0"].Binds[0].Inside = dest
	x.inv[0].Dest = dest
	data := strings.NewReplacer(settings, dest, `"format":"json"`, `"format":"toml"`).Replace(alphaDocs)
	x.src.Templates = fstest.MapFS{"1.0.tmpl": {Data: []byte(data)}}
	out, err := x.render()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range out.Files.Documents {
		if file.Dest == dest && file.Content == "\"items\".\"0\" = true\n" {
			return
		}
	}
	t.Fatal("missing admitted TOML settings")
}
