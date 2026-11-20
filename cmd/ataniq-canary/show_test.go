//go:build linux && ataniq_canary

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary/provision"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadholder/sandboxstart"
)

// shown runs show and returns its lines as a map, failing on a line that is
// not key=value or a key printed twice.
func shown(t *testing.T, w *provisiontest.World) map[string]string {
	t.Helper()
	code, out, errOut := do(t, w, "show", "--config", w.ConfigPath)
	if code != 0 || errOut != "" {
		t.Fatalf("show: status %d: %s", code, errOut)
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if _, twice := got[k]; !ok || k == "" || twice {
			t.Fatalf("line %q", l)
		}
		got[k] = v
	}
	return got
}

func TestShowPrintsTheValuesTheRunScriptNeeds(t *testing.T) {
	w := provisiontest.New(t)
	got := shown(t, w)
	p, err := provision.PathsOf(w.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"role":                       "throwaway-one/lead",
		"project":                    "throwaway-one",
		"layout_role":                "throwaway-one-lead",
		"unit":                       "ataniq-lead-throwaway-one.service",
		"state_root":                 w.State,
		"runtime_root":               w.Runtime,
		"marker":                     filepath.Join(w.State, "canary-root.json"),
		"holder_executable":          "/opt/sample/holder",
		"holder_state_dir":           p.Holder,
		"holder_runtime_dir":         filepath.Join(w.Runtime, "leads", "throwaway-one-lead"),
		"evidence":                   filepath.Join(w.State, "canary-evidence.jsonl"),
		"sandbox_inputs":             w.Cfg.Holder.SandboxInputs,
		"inject":                     "true",
		"limit_memory_max_bytes":     "4294967296",
		"limit_swap_max_bytes":       "0",
		"limit_tasks_max":            "512",
		"limit_memory_floor_bytes":   "1073741824",
		"limit_memory_share_percent": "50",
		"agent_kind":                 "stand-in",
		"agent_runtime":              w.Cfg.Agent.Runtime,
		"agent_runtime_version":      "1.2.3",
		"agent_footer_mode":          "sample mode on",
		"agent_argc":                 "1",
		"agent_argv_0":               "/opt/sample/agent",
		"mirror":                     w.Mirror,
		"upstream_branch":            provisiontest.Branch,
		"lead_dir":                   p.LeadDir,
		"lead_home":                  p.Home,
		"lead_config_dir":            p.ConfigDir,
		"lead_tree":                  p.Tree,
		"replay_dir":                 filepath.Join(p.Home, "canary-replay"),
		"replay_switch":              filepath.Join(p.Home, "canary-replay", "replay.on"),
		"source_managed_settings":    p.ManagedSettings,
		"source_lead_skills":         p.LeadSkills,
		"source_instruction_file":    p.InstructionFile,
		"source_git_identity_dir":    p.GitIdentityDir,
		"source_qargi_install":       p.QargiInstall,
		"source_assignment_dir":      p.AssignmentDir,
		"source_projection_dir":      p.ProjectionDir,
	} {
		if got[k] != want {
			t.Errorf("%s=%q, want %q", k, got[k], want)
		}
		delete(got, k)
	}
	if len(got) != 0 {
		t.Errorf("lines the test does not know: %v", got)
	}
}

func TestShowLeavesOutWhatTheScriptMustNotSee(t *testing.T) {
	w := provisiontest.New(t)
	w.Cfg.Agent.Env = []string{"SAMPLE_VARIABLE=sample-env-value"}
	w.WriteConfig(t)
	code, out, errOut := do(t, w, "show", "--config", w.ConfigPath)
	if code != 0 {
		t.Fatalf("status %d: %s", code, errOut)
	}
	for _, hidden := range []string{
		"sample-env-value", "SAMPLE_VARIABLE", w.Cfg.Holder.ProfileDigest,
		w.Cfg.Provision.GitEmail, "Quoted", provisiontest.Secret,
		w.Settings, w.Hook, w.Screens,
	} {
		if strings.Contains(out, hidden) {
			t.Errorf("show printed %q", hidden)
		}
	}
	// It made nothing.
	if _, err := os.Lstat(filepath.Join(w.State, "leads")); err == nil {
		t.Error("show made something")
	}
	if _, err := os.Lstat(w.Runtime); err == nil {
		t.Error("show made the runtime root")
	}
}

func TestShowRefusesAValueWithAControlCharacter(t *testing.T) {
	w := provisiontest.New(t)
	w.Cfg.Agent.Argv = []string{"/opt/sample/agent", "two\nlines=1"}
	if _, err := showLines(w.Cfg); err == nil || !strings.Contains(err.Error(), "agent_argv_1") {
		t.Fatalf("error %v", err)
	}
}

// inputsFile writes a sandbox inputs file for the world's config, changed by
// mod, and returns its path.
func inputsFile(t *testing.T, w *provisiontest.World, mod func(*sandboxstart.Inputs)) string {
	t.Helper()
	p, err := provision.PathsOf(w.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The managed-settings file provisioning made, holding the guard hook.
	provisiontest.WriteManagedSettings(t, p.ManagedSettings, provisiontest.Marker)
	in := sandboxstart.Inputs{
		Version: sandboxstart.InputsVersion, Canary: true,
		Project: p.Layout.Project, LayoutRole: p.Layout.Role,
		StateRoot: w.Cfg.StateRoot, RuntimeRoot: w.Cfg.RuntimeRoot,
		Runtime: w.Cfg.Agent.Runtime, RuntimeVersion: w.Cfg.Agent.RuntimeVersion,
		Executables: sandboxstart.Executables{Pasta: "/opt/sample/pasta", Bwrap: "/opt/sample/bwrap", Probe: "/opt/sample/probe"},
		Sources: sandboxstart.Sources{
			AgentInstall: "/opt/sample/agent", ManagedSettings: p.ManagedSettings,
			ManagedSettingsSHA256: strings.Repeat("0a", 32), ManagedMarker: provisiontest.Marker,
			LeadSkills: p.LeadSkills, InstructionFile: p.InstructionFile,
			GitIdentityDir: p.GitIdentityDir, QargiInstall: p.QargiInstall,
			QargiRecordsInside: "/ataniq/records",
			AssignmentDir:      p.AssignmentDir, ProjectionDir: p.ProjectionDir,
		},
		NeverMounted: []string{"/opt/sample/never"}, ProviderHost: "provider.example.invalid",
		Terminal: sandboxstart.Terminal{Term: "xterm"}, TimeLimitSeconds: 60,
	}
	if mod != nil {
		mod(&in)
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "inputs.json")
	provisiontest.WriteFile(t, file, data, 0o600)
	return file
}

func TestCheckInputsAcceptsAFileThatMatchesTheConfig(t *testing.T) {
	w := provisiontest.New(t)
	code, out, errOut := do(t, w, "check-inputs", "--config", w.ConfigPath, "--inputs", inputsFile(t, w, nil))
	if code != 0 || errOut != "" || out != "checked sandbox inputs: they match the config\n" {
		t.Fatalf("status %d, out %q, err %q", code, out, errOut)
	}
}

func TestCheckInputsRefusesAMismatchByFieldAndWithoutTheValue(t *testing.T) {
	w := provisiontest.New(t)
	file := inputsFile(t, w, func(in *sandboxstart.Inputs) { in.Sources.LeadSkills = "/opt/sample/other-skills" })
	code, out, errOut := do(t, w, "check-inputs", "--config", w.ConfigPath, "--inputs", file)
	if code != exitFailed || out != "" || !strings.Contains(errOut, "sources.lead_skills") {
		t.Fatalf("status %d, out %q, err %q", code, out, errOut)
	}
	if strings.Contains(errOut, "other-skills") {
		t.Errorf("the refusal repeats a value of the file: %q", errOut)
	}
}

// The inputs file the prepare script wrote before it knew the marker has no
// sources.managed_marker: the holder refuses it at validate-config, so step 8
// refuses it, by the key and without a path of the world.
func TestCheckInputsRefusesAClaudeFileWithoutTheManagedMarker(t *testing.T) {
	w := provisiontest.New(t)
	file := inputsFile(t, w, func(in *sandboxstart.Inputs) { in.Sources.ManagedMarker = "" })
	code, out, errOut := do(t, w, "check-inputs", "--config", w.ConfigPath, "--inputs", file)
	if code != exitFailed || out != "" || !strings.Contains(errOut, "sources.managed_marker") {
		t.Fatalf("status %d, out %q, err %q", code, out, errOut)
	}
	if strings.Contains(errOut, w.Dir) {
		t.Errorf("the refusal names a path: %q", errOut)
	}
}

// The marker is named but the managed-settings file does not hold it.
func TestCheckInputsRefusesASettingsFileThatLacksTheMarker(t *testing.T) {
	w := provisiontest.New(t)
	file := inputsFile(t, w, func(in *sandboxstart.Inputs) {
		provisiontest.WriteManagedSettings(t, in.Sources.ManagedSettings, "another-text")
	})
	code, out, errOut := do(t, w, "check-inputs", "--config", w.ConfigPath, "--inputs", file)
	if code != exitFailed || out != "" || !strings.Contains(errOut, "sources.managed_settings") {
		t.Fatalf("status %d, out %q, err %q", code, out, errOut)
	}
	for _, leak := range []string{w.Dir, provisiontest.Marker, "another-text"} {
		if strings.Contains(errOut, leak) {
			t.Errorf("the refusal holds %q: %q", leak, errOut)
		}
	}
}

func TestCheckInputsRefusesAFileThatIsNotThere(t *testing.T) {
	w := provisiontest.New(t)
	code, out, _ := do(t, w, "check-inputs", "--config", w.ConfigPath, "--inputs", filepath.Join(w.Dir, "absent.json"))
	if code != exitFailed || out != "" {
		t.Fatalf("status %d, out %q", code, out)
	}
}

func TestUsageErrorsOfShowAndCheckInputs(t *testing.T) {
	w := provisiontest.New(t)
	for name, args := range map[string][]string{
		"show without a config":           {"show"},
		"show with --inputs":              {"show", "--config", w.ConfigPath, "--inputs", "/opt/sample/i.json"},
		"show with a further argument":    {"show", "--config", w.ConfigPath, "extra"},
		"check-inputs without --inputs":   {"check-inputs", "--config", w.ConfigPath},
		"check-inputs without a config":   {"check-inputs", "--inputs", "/opt/sample/i.json"},
		"check-inputs with an empty path": {"check-inputs", "--config", w.ConfigPath, "--inputs", ""},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errOut := do(t, w, args...)
			if code != exitUsage || out != "" || !strings.Contains(errOut, "usage:") {
				t.Fatalf("status %d, out %q, err %q", code, out, errOut)
			}
		})
	}
}
