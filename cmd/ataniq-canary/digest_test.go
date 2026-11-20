//go:build linux && ataniq_canary

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadholder/sandboxstart"
)

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}\n$`)

// digestRun runs profile-digest on the world's config and the inputs file
// changed by mod.
func digestRun(t *testing.T, w *provisiontest.World, mod func(*sandboxstart.Inputs)) (int, string, string) {
	t.Helper()
	return do(t, w, "profile-digest", "--config", w.ConfigPath, "--inputs", inputsFile(t, w, mod))
}

func mustDigest(t *testing.T, w *provisiontest.World, mod func(*sandboxstart.Inputs)) string {
	t.Helper()
	code, out, errOut := digestRun(t, w, mod)
	if code != 0 || errOut != "" || !hex64.MatchString(out) {
		t.Fatalf("status %d, out %q, err %q", code, out, errOut)
	}
	return strings.TrimSuffix(out, "\n")
}

func TestProfileDigestIsOneHexLineAndStable(t *testing.T) {
	w := provisiontest.New(t)
	a := mustDigest(t, w, nil)
	if b := mustDigest(t, w, nil); a != b {
		t.Fatalf("not stable: %s then %s", a, b)
	}
}

// holderEnvPairs are the --env arguments of the holder command line that the
// daemon's real builder makes from the world's config: what the holder receives.
func holderEnvPairs(t *testing.T, w *provisiontest.World) []string {
	t.Helper()
	h, err := leadcanary.NewHolderCommand(w.Cfg, filepath.Join(w.State, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	role := w.Cfg.Role
	argv, _, _, err := h.Build(role,
		leadregistry.Entry{Role: role, Epoch: 1},
		leadregistry.Intent{ID: "0123456789abcdef0123456789abcdef", Epoch: 1, UnitName: role.UnitName()})
	if err != nil {
		t.Fatal(err)
	}
	var pairs []string
	for i := 0; i+1 < len(argv) && argv[i] != "--"; i++ {
		if argv[i] == "--env" {
			pairs = append(pairs, argv[i+1])
			i++
		}
	}
	return pairs
}

// The digest the command prints is the digest of the inputs with the
// environment the holder really receives: the --env arguments of the command
// line that the daemon's HolderCommand builds for this config, converted by the
// function the holder converts them with. The sandboxstart tests hold that the
// function's answer is the digest of the start's own plan.
func TestProfileDigestIsWhatTheHoldersFunctionSays(t *testing.T) {
	w := provisiontest.New(t)
	w.Cfg.Agent.Env = []string{"SAMPLE_FLAG=one", "SAMPLE_OTHER=two=three"}
	w.WriteConfig(t)
	got := mustDigest(t, w, nil)

	pairs := holderEnvPairs(t, w)
	if len(pairs) != 2 {
		t.Fatalf("the holder command has the --env pairs %q, want the config's two", pairs)
	}
	in, err := sandboxstart.ReadInputs(inputsFile(t, w, nil))
	if err != nil {
		t.Fatal(err)
	}
	in.AgentEnv = sandboxstart.AgentEnvOf(pairs)
	want, err := sandboxstart.ProfileDigest(in, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("the command printed %s, the holder's inputs give %s", got, want)
	}
	// The environment is part of the digest, and its value is not printed.
	w.Cfg.Agent.Env = nil
	w.WriteConfig(t)
	if without := mustDigest(t, w, nil); without == got {
		t.Error("the agent's environment did not change the digest")
	}
}

func TestProfileDigestChangesWithItsInputs(t *testing.T) {
	w := provisiontest.New(t)
	base := mustDigest(t, w, nil)
	for name, mod := range map[string]func(*sandboxstart.Inputs){
		"managed settings content": func(in *sandboxstart.Inputs) { in.Sources.ManagedSettingsSHA256 = strings.Repeat("0b", 32) },
		"identity mode":            func(in *sandboxstart.Inputs) { in.Identity, in.OwnerUser = "owner", "sampleowner" },
	} {
		t.Run(name, func(t *testing.T) {
			if d := mustDigest(t, w, mod); d == base {
				t.Error("the digest did not change")
			}
		})
	}
	// The runtime version is the config's and the file's together: a config
	// with another version gives another digest for a file that follows it.
	w.Cfg.Agent.RuntimeVersion = "9.9.9"
	w.WriteConfig(t)
	if d := mustDigest(t, w, func(in *sandboxstart.Inputs) { in.RuntimeVersion = "9.9.9" }); d == base {
		t.Error("the runtime version did not change the digest")
	}
}

func TestProfileDigestRefusesInputsThatDifferByFieldWithoutTheValue(t *testing.T) {
	w := provisiontest.New(t)
	for field, mod := range map[string]func(*sandboxstart.Inputs){
		"runtime_version":     func(in *sandboxstart.Inputs) { in.RuntimeVersion = "7.7.7-secret" },
		"sources.lead_skills": func(in *sandboxstart.Inputs) { in.Sources.LeadSkills = "/opt/sample/other-skills" },
		"project":             func(in *sandboxstart.Inputs) { in.Project = "other-project" },
	} {
		t.Run(field, func(t *testing.T) {
			code, out, errOut := digestRun(t, w, mod)
			if code != exitFailed || out != "" || !strings.Contains(errOut, field) {
				t.Fatalf("status %d, out %q, err %q", code, out, errOut)
			}
			for _, v := range []string{"7.7.7-secret", "other-skills", "other-project"} {
				if strings.Contains(errOut, v) {
					t.Errorf("the refusal repeats a value of the file: %q", errOut)
				}
			}
		})
	}
}

func TestProfileDigestRefusesAnInputsFileThatIsNotThere(t *testing.T) {
	w := provisiontest.New(t)
	code, out, _ := do(t, w, "profile-digest", "--config", w.ConfigPath, "--inputs", w.Dir+"/absent.json")
	if code != exitFailed || out != "" {
		t.Fatalf("status %d, out %q", code, out)
	}
}

func TestProfileDigestUsageErrors(t *testing.T) {
	w := provisiontest.New(t)
	for name, args := range map[string][]string{
		"no inputs":  {"profile-digest", "--config", w.ConfigPath},
		"no config":  {"profile-digest", "--inputs", "/opt/sample/i.json"},
		"empty path": {"profile-digest", "--config", w.ConfigPath, "--inputs", ""},
		"extra":      {"profile-digest", "--config", w.ConfigPath, "--inputs", "/opt/sample/i.json", "x"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errOut := do(t, w, args...)
			if code != exitUsage || out != "" || !strings.Contains(errOut, "usage:") {
				t.Fatalf("status %d, out %q, err %q", code, out, errOut)
			}
		})
	}
}
