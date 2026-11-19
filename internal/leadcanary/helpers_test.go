//go:build linux && ataniq_canary

package leadcanary_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadrole"
)

// sampleDigest is a neutral 64-character lower-case hexadecimal digest.
var sampleDigest = strings.Repeat("ab", 32)

// sampleRole returns the role of the slice, throwaway-one/lead.
func sampleRole(t testing.TB) leadrole.Role {
	t.Helper()
	r, err := leadrole.Parse("throwaway-one/lead")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// fixture is a private tree: a state root, a runtime root and a folder for the
// config, none of them a default root of the account.
type fixture struct {
	dir     string // resolved
	state   string
	runtime string
	conf    string // folder of the config file
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{
		dir: dir, state: filepath.Join(dir, "state-root"),
		runtime: filepath.Join(dir, "runtime-root"), conf: filepath.Join(dir, "conf"),
	}
	for _, d := range []string{f.state, f.runtime, f.conf} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// config returns a valid config whose paths lie in the fixture.
func (f fixture) config(t testing.TB) leadcanary.Config {
	t.Helper()
	return leadcanary.Config{
		Version:     1,
		Role:        sampleRole(t),
		StateRoot:   f.state,
		RuntimeRoot: f.runtime,
		Holder: leadcanary.HolderConfig{
			Executable:    "/opt/sample/holder",
			Cols:          120,
			Rows:          40,
			SandboxInputs: filepath.Join(f.dir, "inputs", "sandbox.json"),
			ProfileDigest: sampleDigest,
		},
		Agent: leadcanary.AgentConfig{
			Kind:           leadcanary.AgentStandIn,
			Runtime:        "sample-runtime",
			RuntimeVersion: "1.2.3",
			FooterMode:     "sample mode on",
			Argv:           []string{"/opt/sample/agent", "--flag", "value with space"},
			Env:            []string{"SAMPLE_ONE=1", "SAMPLE_TWO=two words"},
		},
		Inject: true,
		Limits: leadcanary.Limits{
			MemoryMaxBytes:     4 << 30,
			SwapMaxBytes:       0,
			TasksMax:           512,
			MemoryFloorBytes:   1 << 30,
			MemorySharePercent: 50,
		},
		Provision: leadcanary.ProvisionConfig{
			Mirror:          filepath.Join(f.dir, "mirror.git"),
			UpstreamBranch:  "main",
			GitName:         "Sample Name",
			GitEmail:        "sample@example.invalid",
			ManagedSettings: "/opt/sample/managed.json",
			GuardHook:       "/opt/sample/guard",
			ReplayScreens:   "/opt/sample/screens",
		},
	}
}

// writeFile writes data to path with the given mode, exactly (umask or not).
func writeFile(t testing.TB, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// configPath writes cfg as JSON in the fixture's config folder, mode 0600.
func (f fixture) writeConfig(t testing.TB, cfg leadcanary.Config) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(f.conf, "canary.json")
	writeFile(t, p, data, 0o600)
	return p
}

// tree returns cfg as a generic JSON tree, for a test that edits the file.
func tree(t testing.TB, cfg leadcanary.Config) map[string]any {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// writeTree writes a generic tree as the config file.
func (f fixture) writeTree(t testing.TB, m map[string]any) string {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(f.conf, "canary.json")
	writeFile(t, p, data, 0o600)
	return p
}
