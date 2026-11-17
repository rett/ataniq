//go:build linux && amd64

package harness_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

// mutate returns the fixture's configuration with a change made to the decoded
// document.
func mutate(t *testing.T, f *harnesstest.Fixture, change func(m map[string]any)) []byte {
	t.Helper()
	data, err := json.Marshal(f.Config)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	change(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func refused(t *testing.T, f *harnesstest.Fixture, data []byte, want string) {
	t.Helper()
	_, err := harness.LoadConfig(f.WriteConfigBytes(data))
	if err == nil {
		t.Fatalf("the configuration was accepted, want a refusal naming %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("refusal %q does not name %q", err, want)
	}
}

func sub(m map[string]any, key string) map[string]any { return m[key].(map[string]any) }

func TestConfigIsStrict(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	if _, err := harness.LoadConfig(f.WriteConfig()); err != nil {
		t.Fatalf("the fixture's configuration is refused: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(m map[string]any)
		want   string
	}{
		{"unknown key", func(m map[string]any) { m["extra"] = 1 }, `unknown key "extra"`},
		{"unknown nested key", func(m map[string]any) { sub(m, "executables")["gdb"] = "/usr/bin/gdb" }, `executables has the unknown key "gdb"`},
		{"key in another case", func(m map[string]any) { m["Campaign"] = m["campaign"]; delete(m, "campaign") }, `unknown key "Campaign"`},
		{"wrong version", func(m map[string]any) { m["version"] = 2 }, "version"},
		{"relative campaign", func(m map[string]any) { m["campaign"] = "campaign" }, "campaign"},
		{"relative executable", func(m map[string]any) { sub(m, "executables")["bwrap"] = "bwrap" }, "executables.bwrap"},
		{"unclean path", func(m map[string]any) { sub(m, "sources")["agent_install"] = "/a/../b" }, "agent install"},
		{"relative source", func(m map[string]any) { sub(m, "sources")["qargi_install"] = "qargi" }, "Qargi install"},
		{"same executable twice", func(m map[string]any) { sub(m, "executables")["pasta"] = sub(m, "executables")["bwrap"] }, "three different files"},
		{"no never-mounted path", func(m map[string]any) { m["never_mounted"] = []string{} }, "never-mounted"},
		{"empty cases", func(m map[string]any) { m["cases"] = []string{} }, "cases"},
		{"attended case", func(m map[string]any) { m["cases"] = []string{"S5"} }, "not an automated case"},
		{"duplicate case", func(m map[string]any) { m["cases"] = []string{"S1", "S1"} }, "listed twice"},
		{"no time limit", func(m map[string]any) { m["time_limit_seconds"] = 0 }, "time_limit_seconds"},
		{"huge time limit", func(m map[string]any) { m["time_limit_seconds"] = 100000 }, "time_limit_seconds"},
		{"no terminal size", func(m map[string]any) { sub(m, "terminal")["rows"] = 0 }, "rows and cols"},
		{"no TERM", func(m map[string]any) { sub(m, "terminal")["term"] = "" }, "TERM"},
		{"relative scratch command", func(m map[string]any) { m["scratch_command"] = []string{"sh", "-c", "true"} }, "scratch_command[0]"},
		{"scratch argument with a NUL", func(m map[string]any) { m["scratch_command"] = []string{"/bin/sh", "-c", "a\x00b"} }, "scratch_command[2]"},
		{"unknown runtime", func(m map[string]any) { m["runtime"] = "gemini" }, "runtime"},
		{"owner name with root identity", func(m map[string]any) { m["owner_user"] = "someone" }, "owner_user"},
		{"unknown identity", func(m map[string]any) { m["identity"] = "nobody" }, "identity"},
		{"provider host is an address", func(m map[string]any) { m["provider_host"] = "192.0.2.1" }, "provider_host"},
		{"no managed marker for claude", func(m map[string]any) { delete(sub(m, "sources"), "managed_marker") }, "managed_marker"},
		{"bad managed digest", func(m map[string]any) { sub(m, "sources")["managed_settings_sha256"] = "abc" }, "managed-settings digest"},
		{"seccomp file without the statement", func(m map[string]any) {
			sub(m, "sources")["seccomp"] = map[string]any{"file": f.Owned + "/filter.bpf", "sha256": strings.Repeat("a", 64)}
		}, "reviewed_bpf"},
		{"seccomp file with the statement false", func(m map[string]any) {
			sub(m, "sources")["seccomp"] = map[string]any{"file": f.Owned + "/filter.bpf", "sha256": strings.Repeat("a", 64), "reviewed_bpf": false}
		}, "reviewed_bpf"},
		{"campaign listed as never-mounted", func(m map[string]any) {
			m["never_mounted"] = append(m["never_mounted"].([]any), f.Campaign)
		}, "campaign"},
		{"campaign inside the state root", func(m map[string]any) { m["campaign"] = f.State + "/campaign" }, "state root"},
		{"source inside the campaign", func(m map[string]any) { sub(m, "sources")["agent_install"] = f.Campaign + "/agent" }, "campaign"},
		{"other runtime's folder as a source", func(m map[string]any) {
			sub(m, "sources")["agent_install"] = f.State + "/leads/qual-lead/config/codex"
		}, "agent"},
	} {
		t.Run(tc.name, func(t *testing.T) { refused(t, f, mutate(t, f, tc.change), tc.want) })
	}
}

func TestConfigRefusesDuplicateKeysAndTrailingData(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	good, _ := json.Marshal(f.Config)
	dup := strings.Replace(string(good), `"version":1`, `"version":1,"version":1`, 1)
	refused(t, f, []byte(dup), `the key "version" twice`)
	nested := strings.Replace(string(good), `"rows":24`, `"rows":24,"rows":25`, 1)
	refused(t, f, []byte(nested), `terminal has the key "rows" twice`)
	refused(t, f, append(good, []byte(` {}`)...), "data after")
	refused(t, f, []byte(`[]`), "")
	refused(t, f, []byte(`{"version": 1, "campaign": {"a": 1}}`), "")
	refused(t, f, []byte("\xff\xfe"), "UTF-8")
}

func TestConfigFileChecks(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	good := f.WriteConfig()
	for _, mode := range []os.FileMode{0o644, 0o640, 0o660, 0o400, 0o700} {
		if err := os.Chmod(good, mode); err != nil {
			t.Fatal(err)
		}
		_, err := harness.LoadConfig(good)
		if err == nil || !strings.Contains(err.Error(), "must be 0600") {
			t.Errorf("mode %v: error %v", mode, err)
		}
	}
	if err := os.Chmod(good, 0o600); err != nil {
		t.Fatal(err)
	}
	link := f.Base + "/link.json"
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.LoadConfig(link); err == nil {
		t.Error("a symbolic link was accepted as the configuration file")
	}
	if _, err := harness.LoadConfig(f.Base); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Errorf("a folder as the configuration file: %v", err)
	}
	if _, err := harness.LoadConfig(f.Base + "/missing.json"); err == nil {
		t.Error("a missing file was accepted")
	}
	huge := make([]byte, harness.MaxConfigBytes+1)
	for i := range huge {
		huge[i] = ' '
	}
	big := f.WriteConfigBytes(huge)
	if _, err := harness.LoadConfig(big); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("an oversized file: %v", err)
	}
}

func TestNothingIsDefaultedFromTheEnvironment(t *testing.T) {
	// A configuration without a state root, a campaign or a source is refused;
	// the harness fills none of them from the home folder or the environment.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := harnesstest.New(t, profile.Claude)
	for _, tc := range []struct {
		name   string
		change func(m map[string]any)
	}{
		{"campaign", func(m map[string]any) { delete(m, "campaign") }},
		{"state root", func(m map[string]any) { delete(m, "state") }},
		{"agent install", func(m map[string]any) { delete(sub(m, "sources"), "agent_install") }},
		{"executables", func(m map[string]any) { delete(m, "executables") }},
		{"probe", func(m map[string]any) { delete(sub(m, "executables"), "probe") }},
		{"runtime version", func(m map[string]any) { delete(m, "runtime_version") }},
		{"never-mounted", func(m map[string]any) { delete(m, "never_mounted") }},
		{"provider host", func(m map[string]any) { delete(m, "provider_host") }},
		{"terminal", func(m map[string]any) { delete(m, "terminal") }},
	} {
		t.Run(tc.name, func(t *testing.T) { refused(t, f, mutate(t, f, tc.change), "") })
	}
}

func TestOptionalKeysMayBeAbsent(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	data := mutate(t, f, func(m map[string]any) {
		delete(m, "coordinator_reviewed")
		delete(m, "no_lead_has_run")
		delete(m, "scratch_command")
		delete(m, "identity")
	})
	cfg, err := harness.LoadConfig(f.WriteConfigBytes(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CoordinatorReviewed || cfg.NoLeadHasRun || !cfg.DefaultCommand() || cfg.Command()[0] != "/bin/sh" {
		t.Errorf("defaults: %+v", cfg)
	}
}

func TestOwnerIdentityNeedsTheOwnersName(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the owner identity needs a non-zero user id")
	}
	f := harnesstest.New(t, profile.Claude)
	f.Config.Identity = "owner"
	refused(t, f, mutate(t, f, func(m map[string]any) {}), "login name")
	f.Config.OwnerUser = "someone"
	if _, err := harness.LoadConfig(f.WriteConfig()); err != nil {
		t.Errorf("owner identity: %v", err)
	}
}

func TestCodexNeedsItsManagedSettingsPath(t *testing.T) {
	f := harnesstest.New(t, profile.Codex)
	refused(t, f, mutate(t, f, func(m map[string]any) { delete(sub(m, "sources"), "managed_settings_inside") }), "codex")
}

// The checks of the host that Execute makes before it creates anything.
func TestHostChecksRefuseBeforeAnythingIsCreated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(f *harnesstest.Fixture)
		want   string
	}{
		{"campaign missing", func(f *harnesstest.Fixture) { os.Remove(f.Campaign) }, "must exist"},
		{"campaign too open", func(f *harnesstest.Fixture) { os.Chmod(f.Campaign, 0o750) }, "must be 0700"},
		{"campaign is a link", func(f *harnesstest.Fixture) {
			os.Remove(f.Campaign)
			f.Mkdir(f.Base+"/real-campaign", 0o700)
			os.Symlink(f.Base+"/real-campaign", f.Campaign)
		}, "not a folder"},
		{"executable writable by the group", func(f *harnesstest.Fixture) { os.Chmod(f.Config.Executables.Bwrap, 0o775) }, "writable by its group or by others"},
		{"executable writable by others", func(f *harnesstest.Fixture) { os.Chmod(f.Config.Executables.Pasta, 0o757) }, "writable by its group or by others"},
		{"executable is a link", func(f *harnesstest.Fixture) {
			os.Remove(f.Config.Executables.Probe)
			os.Symlink(f.Config.Executables.Pasta, f.Config.Executables.Probe)
		}, "cannot open"},
		{"executable is a folder", func(f *harnesstest.Fixture) {
			os.Remove(f.Config.Executables.Probe)
			os.Mkdir(f.Config.Executables.Probe, 0o755)
		}, "not a regular file"},
		{"executable is missing", func(f *harnesstest.Fixture) { os.Remove(f.Config.Executables.Bwrap) }, "cannot open"},
		{"managed settings changed", func(f *harnesstest.Fixture) { f.Write(f.Config.Sources.ManagedSettings, "changed", 0o644) }, "SHA-256"},
		{"state root is a link", func(f *harnesstest.Fixture) {
			link := f.Base + "/state-link"
			os.Symlink(f.State, link)
			f.Config.State.Root = link
		}, "not a real path"},
		{"records folder is a link", func(f *harnesstest.Fixture) {
			f.Mkdir(f.Owned+"/records-real", 0o700)
			if err := os.Symlink(f.Owned+"/records-real", f.Layout().Records()); err != nil {
				t.Fatal(err)
			}
			f.Config.Sources.QargiRecordsInside = "/ataniq/records"
		}, "not a folder"},
		{"records folder below a link", func(f *harnesstest.Fixture) {
			f.Mkdir(f.Owned+"/lead-real/records", 0o700)
			if err := os.RemoveAll(f.Layout().LeadDir()); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f.Owned+"/lead-real", f.Layout().LeadDir()); err != nil {
				t.Fatal(err)
			}
			f.Config.Sources.QargiRecordsInside = "/ataniq/records"
		}, "not a real path"},
		{"records folder too open", func(f *harnesstest.Fixture) {
			f.Mkdir(f.Layout().Records(), 0o755)
			f.Config.Sources.QargiRecordsInside = "/ataniq/records"
		}, "must be 0700"},
		{"records folder is missing", func(f *harnesstest.Fixture) {
			f.Config.Sources.QargiRecordsInside = "/ataniq/records"
		}, "it must exist"},
		{"seccomp digest wrong", func(f *harnesstest.Fixture) {
			f.Write(f.Owned+"/filter.bpf", "filter", 0o644)
			f.Config.Sources.Seccomp = &harness.Seccomp{File: f.Owned + "/filter.bpf", SHA256: strings.Repeat("a", 64), ReviewedBPF: true}
		}, "SHA-256"},
		{"seccomp file writable by others", func(f *harnesstest.Fixture) {
			f.Write(f.Owned+"/filter.bpf", "filter", 0o666)
			f.Config.Sources.Seccomp = &harness.Seccomp{File: f.Owned + "/filter.bpf", SHA256: sha(t, "filter"), ReviewedBPF: true}
		}, "writable by its group or by others"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := harnesstest.New(t, profile.Claude)
			f.Config.CoordinatorReviewed = true
			tc.break_(f)
			cfg := f.Config
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate refuses before the host is looked at: %v", err)
			}
			fake := newFake(okProbe(assert.StatusOK, 0, 0))
			opt := f.Options(fake)
			opt.Run = true
			out, err := harness.Execute(context.Background(), cfg, opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
			if out != nil || fake.Calls() != 0 {
				t.Errorf("outcome %v, launcher calls %d", out, fake.Calls())
			}
			if names, e := os.ReadDir(f.Campaign); e == nil && len(names) != 0 {
				t.Errorf("the refused run made %d entries in the campaign", len(names))
			}
		})
	}
}
