//go:build linux && ataniq_canary

package provision

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadholder/sandboxstart"
)

// goodInputs returns an inputs file's content that matches the world's config
// and the paths provisioning made.
func goodInputs(t *testing.T, w *provisiontest.World) sandboxstart.Inputs {
	t.Helper()
	p, err := PathsOf(w.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sandboxstart.Inputs{
		Version: sandboxstart.InputsVersion, Canary: true,
		Project: p.Layout.Project, LayoutRole: p.Layout.Role,
		StateRoot: w.Cfg.StateRoot, RuntimeRoot: w.Cfg.RuntimeRoot,
		Runtime: w.Cfg.Agent.Runtime, RuntimeVersion: w.Cfg.Agent.RuntimeVersion,
		Executables: sandboxstart.Executables{Pasta: "/opt/sample/pasta", Bwrap: "/opt/sample/bwrap", Probe: "/opt/sample/probe"},
		Sources: sandboxstart.Sources{
			AgentInstall: "/opt/sample/agent", ManagedSettings: p.ManagedSettings,
			ManagedSettingsSHA256: hashHex([]byte("x")),
			LeadSkills:            p.LeadSkills, InstructionFile: p.InstructionFile,
			GitIdentityDir: p.GitIdentityDir, QargiInstall: p.QargiInstall,
			QargiRecordsInside: "/ataniq/records",
			AssignmentDir:      p.AssignmentDir, ProjectionDir: p.ProjectionDir,
		},
		NeverMounted: []string{"/opt/sample/never"}, ProviderHost: "provider.example.invalid",
		Terminal: sandboxstart.Terminal{Term: "xterm"}, TimeLimitSeconds: 60,
	}
}

func writeInputs(t *testing.T, in sandboxstart.Inputs, mode os.FileMode) string {
	t.Helper()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "inputs.json")
	provisiontest.WriteFile(t, p, data, mode)
	return p
}

func TestCheckInputsAcceptsAFileThatMatchesTheConfig(t *testing.T) {
	w := provisiontest.New(t)
	if err := CheckInputs(w.Cfg, writeInputs(t, goodInputs(t, w), 0o600)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckInputsRefusesAFileThatDoesNotMatch(t *testing.T) {
	for field, mod := range map[string]func(in *sandboxstart.Inputs){
		"canary":                   func(in *sandboxstart.Inputs) { in.Canary = false },
		"project":                  func(in *sandboxstart.Inputs) { in.Project = "throwaway-two" },
		"layout_role":              func(in *sandboxstart.Inputs) { in.LayoutRole = "throwaway-two-lead" },
		"state_root":               func(in *sandboxstart.Inputs) { in.StateRoot = in.StateRoot + "-other" },
		"runtime_root":             func(in *sandboxstart.Inputs) { in.RuntimeRoot = in.RuntimeRoot + "-other" },
		"runtime":                  func(in *sandboxstart.Inputs) { in.Runtime = otherRuntime(in.Runtime) },
		"runtime_version":          func(in *sandboxstart.Inputs) { in.RuntimeVersion = "9.9.9" },
		"sources.managed_settings": func(in *sandboxstart.Inputs) { in.Sources.ManagedSettings = "/opt/sample/m.json" },
		"sources.lead_skills":      func(in *sandboxstart.Inputs) { in.Sources.LeadSkills = "/opt/sample/skills" },
		"sources.instruction_file": func(in *sandboxstart.Inputs) { in.Sources.InstructionFile = "/opt/sample/i.md" },
		"sources.git_identity_dir": func(in *sandboxstart.Inputs) { in.Sources.GitIdentityDir = "/opt/sample/git" },
		"sources.qargi_install":    func(in *sandboxstart.Inputs) { in.Sources.QargiInstall = "/opt/sample/qargi" },
		"sources.assignment_dir":   func(in *sandboxstart.Inputs) { in.Sources.AssignmentDir = "/opt/sample/a" },
		"sources.projection_dir":   func(in *sandboxstart.Inputs) { in.Sources.ProjectionDir = "/opt/sample/p" },
		"sources.extra_placeholders": func(in *sandboxstart.Inputs) {
			in.Sources.ExtraPlaceholders = []sandboxstart.Placeholder{{Rel: "x", Source: "/opt/sample/x"}}
		},
	} {
		t.Run(field, func(t *testing.T) {
			w := provisiontest.New(t)
			in := goodInputs(t, w)
			mod(&in)
			err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
			if err == nil {
				// A change to the roots may fail the file's own checks first.
				t.Fatal("accepted")
			}
			var ie *sandboxstart.InputsError
			if !errors.Is(err, ErrInputs) && !errors.As(err, &ie) {
				t.Fatalf("error %v is neither a mismatch nor the file's own refusal", err)
			}
			if errors.Is(err, ErrInputs) && !contains(err.Error(), field) {
				t.Errorf("the error does not name %s: %v", field, err)
			}
		})
	}
}

func otherRuntime(rt string) string {
	if rt == "" {
		return "x"
	}
	return rt + "-other"
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestCheckInputsHoldsTheFileToReadInputsOwnRules(t *testing.T) {
	w := provisiontest.New(t)
	in := goodInputs(t, w)
	for name, f := range map[string]string{
		"mode 0640":  writeInputs(t, in, 0o640),
		"not there":  filepath.Join(t.TempDir(), "none.json"),
		"not a file": t.TempDir(),
	} {
		var ie *sandboxstart.InputsError
		if err := CheckInputs(w.Cfg, f); !errors.As(err, &ie) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTheInputsOfTheConfigPassTheProfile(t *testing.T) {
	// The sources the run kit names are the ones in PathsOf, and the profile
	// accepts them: the inputs provisioning builds are valid.
	w := provisiontest.New(t)
	p, _ := PathsOf(w.Cfg)
	in := profileInputs(w.Cfg, p, hashHex([]byte("x")))
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Binds(); err != nil {
		t.Fatal(err)
	}
}
