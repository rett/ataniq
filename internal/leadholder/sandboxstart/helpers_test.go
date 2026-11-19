//go:build linux && ataniq_canary

package sandboxstart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// sampleProject is the project of the test world; it carries the reserved
// prefix, which the root check of canary mode needs.
const (
	sampleProject = "throwaway-one"
	sampleRole    = "throwaway-one-lead"
)

// sampleInputs are valid inputs in canary mode for a host under base: the roots
// are base/state and base/run, and every source is a name below base/owned. The
// holder's fields (the agent's command line and the like) are not set.
func sampleInputs(base string) Inputs {
	rt := profile.Runtimes()[0]
	return Inputs{
		Version: InputsVersion, Canary: true,
		Project: sampleProject, LayoutRole: sampleRole,
		StateRoot: base + "/state", RuntimeRoot: base + "/run",
		Runtime: string(rt), RuntimeVersion: "2.1.0",
		NoLeadHasRun: true,
		Executables:  Executables{Pasta: base + "/bin/pasta", Bwrap: base + "/bin/bwrap", Probe: base + "/bin/probe"},
		Sources: Sources{
			AgentInstall:    base + "/owned/agent",
			ManagedSettings: base + "/owned/managed/settings.json", ManagedSettingsSHA256: strings.Repeat("ab", 32),
			ManagedMarker: "sample-guard",
			LeadSkills:    base + "/owned/skills", InstructionFile: base + "/owned/instructions/" + rt.InstructionFileName(),
			GitIdentityDir: base + "/owned/git", QargiInstall: base + "/owned/qargi",
			QargiRecordsInside: "/ataniq/records",
			AssignmentDir:      base + "/owned/assignment", ProjectionDir: base + "/owned/projection",
		},
		NeverMounted:     []string{base + "/owner/work", base + "/owner/.ssh"},
		ProviderHost:     "provider.example.test",
		Terminal:         Terminal{Term: "xterm-256color", Lang: "C.UTF-8"},
		TimeLimitSeconds: 5,
	}
}

func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mkdir(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(p, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}
