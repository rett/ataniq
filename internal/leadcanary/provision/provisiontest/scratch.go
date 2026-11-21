//go:build linux && ataniq_canary

package provisiontest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadsandbox/profile"
)

// ScratchWorld is a temporary world for the tests of a scratch place: a state
// root that carries the scratch root marker and holds no mirror, a runtime root
// that is not made yet, the files the scratch config names, and one local
// repository with the branch each lead's mirror is made from. It makes no
// namespace, no unit and no agent.
type ScratchWorld struct {
	// Dir holds everything; State, Runtime and Conf lie in it.
	Dir     string
	State   string
	Runtime string
	Conf    string
	// Settings, Hook and Screens are the files and the folder the scratch
	// config names under provision.
	Settings string
	Hook     string
	Screens  string
	// Source is a repository with one commit on Branch; Head is that commit.
	Source string
	Head   string
	// Config is the scratch config with the two leads of Keys; WriteConfig
	// writes it to ConfigPath.
	Config     leadcanary.ScratchConfig
	ConfigPath string
}

// ScratchKeys are the keys of the two leads of a scratch world.
var ScratchKeys = [2]string{"alpha", "beta"}

// NewScratch makes a scratch world. The state root exists with mode 0700 and
// carries the scratch root marker; it holds no mirrors folder, and the runtime
// root does not exist.
func NewScratch(t testing.TB) *ScratchWorld {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &ScratchWorld{
		Dir: dir, State: filepath.Join(dir, "state-root"), Runtime: filepath.Join(dir, "runtime-root"),
		Conf: filepath.Join(dir, "conf"),
	}
	mkdir(t, w.State)
	mkdir(t, w.Conf)
	if err := canaryroot.WriteScratchMarker(w.State); err != nil {
		t.Fatal(err)
	}
	srcs := filepath.Join(dir, "src")
	mkdir(t, srcs)
	w.Settings = filepath.Join(srcs, "managed.json")
	WriteFile(t, w.Settings, []byte(`{"note":"`+Secret+`","guard":"`+Marker+`"}`+"\n"), 0o600)
	w.Hook = filepath.Join(srcs, "guard")
	WriteFile(t, w.Hook, []byte("#!/bin/sh\nexit 0\n"), 0o700)
	w.Screens = filepath.Join(srcs, "screens")
	mkdir(t, w.Screens)
	for name, body := range map[string]string{
		"1.json": `{"cols":80,"rows":24}` + "\n", "1.txt": "first screen\n",
		"2.json": `{"cols":80,"rows":24}` + "\n", "2.txt": "second screen\n",
	} {
		WriteFile(t, filepath.Join(w.Screens, name), []byte(body), 0o600)
	}
	w.Source = filepath.Join(dir, "upstream")
	mkdir(t, w.Source)
	Git(t, w.Source, "init", "--quiet", "--initial-branch="+Branch)
	WriteFile(t, filepath.Join(w.Source, "README"), []byte("sample\n"), 0o644)
	Git(t, w.Source, "add", "README")
	Git(t, w.Source, "commit", "--quiet", "--message", "sample: first")
	w.Head = Git(t, w.Source, "rev-parse", "HEAD")

	lead := func(key string) leadcanary.ScratchLead {
		return leadcanary.ScratchLead{
			Key: key, Source: w.Source, UpstreamBranch: Branch,
			Agent: leadcanary.AgentConfig{
				Kind: leadcanary.AgentStandIn, Runtime: string(profile.Runtimes()[0]), RuntimeVersion: "1.2.3",
				FooterMode: "sample mode on", Argv: []string{"/opt/sample/agent"},
			},
			Inject: true,
			Limits: leadcanary.Limits{
				MemoryMaxBytes: 4 << 30, SwapMaxBytes: 0, TasksMax: 512,
				MemoryFloorBytes: 1 << 30, MemorySharePercent: 50,
			},
		}
	}
	w.Config = leadcanary.ScratchConfig{
		Version: 1, StateRoot: w.State, RuntimeRoot: w.Runtime, MaxRunning: 2,
		Holder: leadcanary.ScratchHolder{Executable: "/opt/sample/holder", Cols: 120, Rows: 40},
		Sandbox: leadcanary.ScratchSandbox{
			AgentInstall: "/opt/sample/agent-install", Pasta: "/opt/sample/pasta",
			Bwrap: "/opt/sample/bwrap", Probe: "/opt/sample/probe",
			ManagedMarker: Marker, NeverMounted: []string{"/opt/sample/never"},
			ProviderHost:     "provider.example.invalid",
			Terminal:         leadcanary.ScratchTerminal{Term: "xterm"},
			TimeLimitSeconds: 60,
		},
		Provision: leadcanary.ScratchProvision{
			GitName: `Sample "Quoted" \ Name #1`, GitEmail: "sample@example.invalid",
			ManagedSettings: w.Settings, GuardHook: w.Hook, ReplayScreens: w.Screens,
		},
		Entries: []leadcanary.ScratchLead{lead(ScratchKeys[0]), lead(ScratchKeys[1])},
	}
	w.WriteConfig(t)
	return w
}

// WriteConfig writes Config to ConfigPath with mode 0600, replacing the file.
func (w *ScratchWorld) WriteConfig(t testing.TB) {
	t.Helper()
	data, err := json.Marshal(w.Config)
	if err != nil {
		t.Fatal(err)
	}
	w.ConfigPath = filepath.Join(w.Conf, "scratch.json")
	_ = os.Remove(w.ConfigPath)
	WriteFile(t, w.ConfigPath, data, 0o600)
}

// Leads returns the derived config of each lead.
func (w *ScratchWorld) Leads(t testing.TB) []leadcanary.Config {
	t.Helper()
	leads, err := w.Config.Leads()
	if err != nil {
		t.Fatal(err)
	}
	return leads
}

// Getenv is the environment of the default-root check: HOME and the XDG
// variables point into the world, at folders that do not exist.
func (w *ScratchWorld) Getenv() func(string) string {
	env := map[string]string{
		"HOME":            filepath.Join(w.Dir, "home"),
		"XDG_STATE_HOME":  filepath.Join(w.Dir, "xdg-state"),
		"XDG_RUNTIME_DIR": filepath.Join(w.Dir, "xdg-run"),
	}
	return func(k string) string { return env[k] }
}
