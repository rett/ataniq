package pack_test

import (
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/pack"
	"ataniq/internal/pack/packtest"
)

const rootDocument = `{
	"schema":"ataniq.pack-declaration/1","name":"alpha","class":"runtime","vendor":"Example","display":"Alpha",
	"words":[{"text":"sample","match":"token"}],
	"builds":[{"version":"1.0","aliases":["1.0.0"],"transports":["terminal"],
		"verified_on":"","source":"example reference",
		"interfaces":[{"kind":"hooks","stability":"preview","minimum_version":"1.0"}]}]
}`

func buildDocument(build string) []byte {
	return []byte(`{"schema":"ataniq.pack-declaration/1","builds":{"1.0":` + build + `}}`)
}

const hookRows = `[{"id":"row","event":"Ready","fields":[{"name":"value","pointer":"/a~1b"}],
	"digests":[{"name":"digest","pointer":""}],"output":"none","async":false,"timeout_ms":100}]`

func sampleLayout() pack.BindLayout {
	return pack.BindLayout{
		Config: "/config", Tree: "/tree",
		CheckExtraEnv: checkProfileEnv,
		CoreRows:      []string{"/etc/hosts", "/config", "/tree", "/config/skills", "/config/instructions"},
	}
}

// Use a synthetic profile so the same validator judges every extra variable.
func checkProfileEnv(env map[string]string) error {
	in := profile.Inputs{
		StateRoot: "/state", Role: "sample", Project: "example",
		Runtime: profile.Runtimes()[0], RuntimeVersion: "1", Identity: profile.IdentityRoot,
		AgentInstall: "/owned/agent", ProbeBinary: "/owned/probe", SpecDir: "/owned/spec",
		ManagedSettings: "/owned/settings", ManagedSettingsSHA256: strings.Repeat("a", 64),
		LeadSkills: "/owned/skills", InstructionFile: "/owned/instructions",
		GitIdentityDir: "/owned/identity", QargiInstall: "/owned/qargi", QargiRecordsInside: "/records",
		AssignmentDir: "/owned/assignment", ProjectionDir: "/owned/projection", ResolvConf: "/owned/resolv",
		NeverMounted: []string{"/private"}, Term: "xterm",
	}
	if in.ManagedInside() == "" {
		in.ManagedSettingsInside = "/etc/example/settings"
	}
	in.GitMeta = in.Layout().GitMeta()
	for name, value := range env {
		in.ExtraEnv = append(in.ExtraEnv, profile.EnvVar{Name: name, Value: value})
	}
	return in.Validate()
}

func TestReadDeclaration(t *testing.T) {
	files := map[string][]byte{pack.ManifestPath: []byte(rootDocument)}
	root, err := pack.DecodeDeclaration(files, pack.BindLayout{})
	if err != nil {
		t.Fatal(err)
	}
	if root.Manifest.Name != packtest.Alpha.Name() || root.Manifest.Vendor != "Example" {
		t.Fatal(root.Manifest)
	}
	b := root.Manifest.Builds[0]
	if b.Interfaces[0].Kind != "hooks" || b.VerifiedOn != "" || b.Aliases[0] != "1.0.0" {
		t.Fatal(b)
	}

	files[pack.HooksPath] = buildDocument(hookRows)
	files[pack.SandboxPath] = buildDocument(`{"binds":[{"role":"config-overlay","inside":"/config/agent","kind":"file"}],
		"launch":{"argv":["run","--agent={agent}"],"env":{"SAMPLE":"on"},"agents":[{"name":"sample","definition":"/config/agent"}]}}`)
	d, err := pack.DecodeDeclaration(files, sampleLayout())
	if err != nil {
		t.Fatal(err)
	}
	if d.Hooks["1.0"][0].Event != "Ready" || d.Sandbox["1.0"].Launch.Env["SAMPLE"] != "on" {
		t.Fatal("lost build rows")
	}
}

func TestDeclarationByteLimit(t *testing.T) {
	if pack.MaxDeclarationBytes != 1<<20 {
		t.Fatalf("declaration limit = %d, want 1 MiB", pack.MaxDeclarationBytes)
	}
	hooks, sandbox := buildDocument(hookRows), buildDocument(`{}`)
	padding := pack.MaxDeclarationBytes - len(rootDocument) - len(hooks) - len(sandbox)
	files := map[string][]byte{
		pack.ManifestPath: []byte(strings.Repeat(" ", padding) + rootDocument),
		pack.HooksPath:    hooks, pack.SandboxPath: sandbox,
	}
	if _, err := pack.DecodeDeclaration(files, sampleLayout()); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	files[pack.HooksPath] = append([]byte(" "), hooks...)
	if _, err := pack.DecodeDeclaration(files, sampleLayout()); err == nil {
		t.Error("accepted a declaration one byte over the total limit")
	}
}

func TestStrictDeclaration(t *testing.T) {
	sandbox := `{"launch":{"argv":["run"],"permission_modes":[{"name":"m-plan","lead":true}]}}`
	for _, tc := range []struct{ name, path, data string }{
		{"unknown", pack.ManifestPath, strings.Replace(rootDocument, "vendor", "family", 1)},
		{"wrong case", pack.ManifestPath, strings.Replace(rootDocument, "vendor", "Vendor", 1)},
		{"duplicate", pack.ManifestPath, strings.Replace(rootDocument, `"vendor":`, `"vendor":"other","vend\u006fr":`, 1)},
		{"nested duplicate", pack.HooksPath, string(buildDocument(strings.Replace(hookRows, `"pointer":`, `"pointer":"","pointer":`, 1)))},
		{"duplicate version", pack.SandboxPath, `{"schema":"ataniq.pack-declaration/1","builds":{"1.0":{},"1.0":{}}}`},
		{"trailing object", pack.ManifestPath, rootDocument + "{}"},
		{"trailing byte", pack.ManifestPath, rootDocument + "\n"},
		{"invalid utf8", pack.ManifestPath, strings.Replace(rootDocument, "Example", "\xff", 1)},
		{"missing verification", pack.ManifestPath, strings.Replace(rootDocument, `"verified_on":"",`, "", 1)},
		{"missing async", pack.HooksPath, string(buildDocument(strings.Replace(hookRows, `"async":false,`, "", 1)))},
		{"null value", pack.ManifestPath, strings.Replace(rootDocument, `"verified_on":""`, `"verified_on":null`, 1)},
		{"null launch", pack.SandboxPath, string(buildDocument(`{"launch":null}`))},
		{"null part", pack.SandboxPath, string(buildDocument("null"))},
		{"wrong type", pack.ManifestPath, strings.Replace(rootDocument, `["terminal"]`, `"terminal"`, 1)},
		{"wrong schema", pack.ManifestPath, strings.Replace(rootDocument, "declaration/1", "declaration/2", 1)},
		{"unknown file", "launchpolicy/1.0.json", "{}"},
		{"reference to policy", pack.ManifestPath, strings.Replace(rootDocument, `"builds":`, `"launch_policy":"other","builds":`, 1)},
		{"mode class", pack.SandboxPath, string(buildDocument(strings.Replace(sandbox, `"lead":`, `"class":"ask","lead":`, 1)))},
		{"footer mode", pack.SandboxPath, string(buildDocument(strings.Replace(sandbox, `"lead":`, `"footer_mode":"manual","lead":`, 1)))},
		{"mode-setting options", pack.SandboxPath, string(buildDocument(strings.Replace(sandbox, `"argv":`, `"mode_setting_options":[],"argv":`, 1)))},
		{"alias for hook rows", pack.HooksPath, strings.Replace(string(buildDocument(hookRows)), `"1.0"`, `"1.0.0"`, 1)},
		{"alias for sandbox", pack.SandboxPath, strings.Replace(string(buildDocument(sandbox)), `"1.0"`, `"1.0.0"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{pack.ManifestPath: []byte(rootDocument)}
			files[tc.path] = []byte(tc.data)
			if d, err := pack.DecodeDeclaration(files, sampleLayout()); err == nil || d != nil {
				t.Errorf("decoded %v, %v", d, err)
			}
		})
	}
}

func TestManifestValidation(t *testing.T) {
	for _, tc := range []struct{ old, replacement string }{
		{"runtime", "other"}, {"token", "prefix"}, {"terminal", "headless"}, {"terminal", "hooks"},
		{`["terminal"]`, "[]"}, {`["terminal"]`, `["protocol","protocol"]`}, {"preview", "alpha"},
		{"hooks", "hook"}, {`"verified_on":""`, `"verified_on":"yesterday"`},
		{`"minimum_version":"1.0"`, `"minimum_version":" \t"`},
		{"1.0.0", "1.0"}, {`"minimum_version":`, `"schema_digest":"invalid","minimum_version":`},
		{`"words":[{"text":"sample","match":"token"}]`, `"words":[]`},
	} {
		data := strings.Replace(rootDocument, tc.old, tc.replacement, 1)
		if _, err := pack.DecodeDeclaration(map[string][]byte{pack.ManifestPath: []byte(data)}, pack.BindLayout{}); err == nil {
			t.Errorf("accepted replacing %q by %q", tc.old, tc.replacement)
		}
	}
}

func TestBoundaryWordMinimums(t *testing.T) {
	for _, tc := range []struct {
		match, text string
		valid       bool
	}{
		{"substring", "café", false}, {"substring", "café2", false}, {"substring", "cafés", true},
		{"token", "é2", false}, {"token", "é23", true}, {"token", "x-y", false},
		{"literal", "abc", false}, {"literal", "éé", true},
	} {
		m := packtest.Alpha.Manifest()
		m.Words = []pack.Word{{Text: tc.text, Match: tc.match}}
		err := (pack.Declaration{Manifest: m}).Validate(sampleLayout())
		if (err == nil) != tc.valid {
			t.Errorf("%s %q: %v", tc.match, tc.text, err)
		}
	}
}

func TestAgentFilesBuilds(t *testing.T) {
	data := strings.Replace(rootDocument, `"runtime"`, `"agent-files"`, 1)
	files := map[string][]byte{pack.ManifestPath: []byte(data)}
	if _, err := pack.DecodeDeclaration(files, sampleLayout()); err == nil {
		t.Error("agent-files pack declared a runtime build")
	}
	header, _, _ := strings.Cut(data, `"builds":`)
	files[pack.ManifestPath] = []byte(header + `"builds":[]}`)
	if _, err := pack.DecodeDeclaration(files, sampleLayout()); err != nil {
		t.Fatal(err)
	}
	for path, companion := range map[string][]byte{
		pack.HooksPath: buildDocument(hookRows), pack.SandboxPath: buildDocument(`{}`),
	} {
		files[path] = companion
		if _, err := pack.DecodeDeclaration(files, sampleLayout()); err == nil {
			t.Errorf("agent-files pack declared %s build rows", path)
		}
		delete(files, path)
	}
}

func TestHookDeclaration(t *testing.T) {
	for _, tc := range []struct{ old, replacement string }{
		{"row", "Bad Row"}, {"none", "other"}, {"100", "0"}, {"/a~1b", "bad"}, {"/a~1b", "/~x"},
		{`"value"`, `"bad-name"`},
	} {
		bad := strings.Replace(hookRows, tc.old, tc.replacement, 1)
		if err := decodeHookRows(bad); err == nil {
			t.Errorf("accepted hook rows %s", bad)
		}
	}
	if err := decodeHookRows(strings.Replace(hookRows, `,"pointer":"/a~1b"`, "", 1)); err == nil {
		t.Error("accepted missing pointer")
	}
	for _, tc := range []struct {
		name string
		max  int
	}{{"fields", 16}, {"digests", 4}} {
		fields := make([]string, 0, tc.max+1)
		for i := 0; i <= tc.max; i++ {
			fields = append(fields, fmt.Sprintf(`{"name":"%c","pointer":""}`, 'a'+i))
		}
		for _, n := range []int{tc.max, tc.max + 1} {
			rows := fmt.Sprintf(`[{"id":"row","event":"Ready","output":"none","async":false,"timeout_ms":100,"%s":[%s]}]`, tc.name, strings.Join(fields[:n], ","))
			if err := decodeHookRows(rows); (err != nil) != (n > tc.max) {
				t.Errorf("%d %s: %v", n, tc.name, err)
			}
		}
	}
}

func TestHookTimeoutRange(t *testing.T) {
	for _, tc := range []struct {
		ms    string
		valid bool
	}{
		{"0", false}, {"1", true}, {"3600000", true}, {"3600001", false},
	} {
		rows := strings.Replace(hookRows, `"timeout_ms":100`, `"timeout_ms":`+tc.ms, 1)
		if err := decodeHookRows(rows); (err == nil) != tc.valid {
			t.Errorf("timeout_ms %s: %v", tc.ms, err)
		}
	}
}

func decodeHookRows(rows string) error {
	_, err := pack.DecodeDeclaration(map[string][]byte{pack.ManifestPath: []byte(rootDocument), pack.HooksPath: buildDocument(rows)}, sampleLayout())
	return err
}

func TestDeclarationDigest(t *testing.T) {
	files := map[string][]byte{pack.SandboxPath: []byte("C"), pack.ManifestPath: []byte("A"), pack.HooksPath: []byte("B")}
	// Independent vector for the three raw byte strings in path order.
	const want = "4f8748aecb6eac3144a43b889d8c379ff87ac51ef649fc5864e07c655c1281f6"
	got, err := pack.DeclarationDigest(files)
	if err != nil || got != want {
		t.Fatalf("digest = %s, %v", got, err)
	}
	files[pack.HooksPath] = []byte("B ")
	if changed, err := pack.DeclarationDigest(files); err != nil || changed == got {
		t.Fatalf("edited digest = %s, %v", changed, err)
	}
	files["launchpolicy/1.json"] = []byte("D")
	if _, err := pack.DeclarationDigest(files); err == nil {
		t.Error("accepted policy file in digest")
	}
}

func TestMissingManifest(t *testing.T) {
	files := map[string][]byte{pack.HooksPath: buildDocument(hookRows)}
	want := "missing " + pack.ManifestPath
	if _, err := pack.DecodeDeclaration(files, sampleLayout()); err == nil || err.Error() != want {
		t.Errorf("DecodeDeclaration error = %v, want %q", err, want)
	}
	if _, err := pack.DeclarationDigest(files); err == nil || err.Error() != want {
		t.Errorf("DeclarationDigest error = %v, want %q", err, want)
	}
}
