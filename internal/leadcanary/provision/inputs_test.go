//go:build linux && ataniq_canary

package provision

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadholder/sandboxstart"
	"ataniq/internal/leadholder/screenrec"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadsandbox/profile"
)

// goodInputs returns an inputs file's content that matches the world's config
// and the paths provisioning made.
func goodInputs(t *testing.T, w *provisiontest.World) sandboxstart.Inputs {
	t.Helper()
	p, err := PathsOf(w.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The managed-settings file provisioning made, holding the guard hook.
	provisiontest.WriteManagedSettings(t, p.ManagedSettings, provisiontest.Marker)
	return sandboxstart.Inputs{
		Version: sandboxstart.InputsVersion, Canary: true,
		Project: p.Layout.Project, LayoutRole: p.Layout.Role,
		StateRoot: w.Cfg.StateRoot, RuntimeRoot: w.Cfg.RuntimeRoot,
		Runtime: w.Cfg.Agent.Runtime, RuntimeVersion: w.Cfg.Agent.RuntimeVersion,
		Executables: sandboxstart.Executables{Pasta: "/opt/sample/pasta", Bwrap: "/opt/sample/bwrap", Probe: "/opt/sample/probe"},
		Sources: sandboxstart.Sources{
			AgentInstall: "/opt/sample/agent", ManagedSettings: p.ManagedSettings,
			ManagedSettingsSHA256: hashHex([]byte("x")), ManagedMarker: provisiontest.Marker,
			LeadSkills: p.LeadSkills, InstructionFile: p.InstructionFile,
			GitIdentityDir: p.GitIdentityDir, QargiInstall: p.QargiInstall,
			QargiRecordsInside: RecordsInside,
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
		"sources.qargi_records_inside": func(in *sandboxstart.Inputs) {
			in.Sources.QargiRecordsInside = profile.HomeInside + "/records"
		},
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

// A live agent's command must be a file that exists in the install folder: the
// run's version step runs that file, and a wrapper that is not it would pass
// the config's checks and break the step.
func TestCheckInputsHoldsALiveAgentToAFileOfTheInstallFolder(t *testing.T) {
	w := provisiontest.New(t)
	w.Cfg.Agent.Kind = leadcanary.AgentLive
	w.Cfg.Agent.FooterMode = "manual"
	w.Cfg.Agent.Argv = []string{"/ataniq/agent/claude"}
	in := goodInputs(t, w)
	in.Sources.AgentInstall = t.TempDir()

	// No file of that name: refused, naming the field and not the path.
	err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
	if !errors.Is(err, ErrInputs) || !contains(err.Error(), "agent.argv[0]") || contains(err.Error(), in.Sources.AgentInstall) {
		t.Fatalf("error %v: want a mismatch that names agent.argv[0] and no path", err)
	}
	// A link, even to a file, is no file of the folder.
	other := filepath.Join(t.TempDir(), "real")
	provisiontest.WriteFile(t, other, []byte("x"), 0o700)
	if err := os.Symlink(other, filepath.Join(in.Sources.AgentInstall, "claude")); err != nil {
		t.Fatal(err)
	}
	if err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600)); !errors.Is(err, ErrInputs) {
		t.Fatalf("a link: error %v", err)
	}
	if err := os.Remove(filepath.Join(in.Sources.AgentInstall, "claude")); err != nil {
		t.Fatal(err)
	}
	provisiontest.WriteFile(t, filepath.Join(in.Sources.AgentInstall, "claude"), []byte("x"), 0o700)
	if err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600)); err != nil {
		t.Fatalf("the file is there: %v", err)
	}
}

// An inputs file as the prepare script wrote it before the marker: no
// sources.managed_marker. The holder refuses it at validate-config, so
// check-inputs must refuse it, naming the key.
func TestCheckInputsRefusesAClaudeFileWithoutAManagedMarker(t *testing.T) {
	w := provisiontest.New(t)
	in := goodInputs(t, w)
	if in.Runtime != string(profile.Claude) {
		t.Fatalf("the world's runtime is %q, the test is about claude", in.Runtime)
	}
	in.Sources.ManagedMarker = ""
	err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
	if !errors.Is(err, ErrInputs) || !contains(err.Error(), "sources.managed_marker") {
		t.Fatalf("error %v: want a mismatch that names sources.managed_marker", err)
	}
}

// The key is there but the managed-settings file does not hold its text:
// assertion 8 would fail inside.
func TestCheckInputsRefusesASettingsFileThatDoesNotHoldTheMarker(t *testing.T) {
	w := provisiontest.New(t)
	in := goodInputs(t, w)
	provisiontest.WriteManagedSettings(t, in.Sources.ManagedSettings, "a-different-text")
	err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
	if !errors.Is(err, ErrInputs) || !contains(err.Error(), "sources.managed_settings") || !contains(err.Error(), "sources.managed_marker") {
		t.Fatalf("error %v: want a mismatch that names the settings file's field and the marker's", err)
	}
	for _, leak := range []string{provisiontest.Marker, "a-different-text", in.Sources.ManagedSettings, w.Dir} {
		if contains(err.Error(), leak) {
			t.Errorf("the refusal holds %q: %v", leak, err)
		}
	}
}

func TestCheckInputsRefusesASettingsFileThatCannotBeRead(t *testing.T) {
	for name, prep := range map[string]func(t *testing.T, path string){
		"absent": func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		},
		"a link": func(t *testing.T, path string) {
			target := path + ".target"
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		},
		"too large": func(t *testing.T, path string) {
			provisiontest.WriteFile(t, path, []byte(provisiontest.Marker+strings.Repeat(" ", MaxManagedSettingsBytes)), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := provisiontest.New(t)
			in := goodInputs(t, w)
			prep(t, in.Sources.ManagedSettings)
			err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
			if !errors.Is(err, ErrInputs) || !contains(err.Error(), "sources.managed_settings") {
				t.Fatalf("error %v: want a mismatch that names sources.managed_settings", err)
			}
			if contains(err.Error(), w.Dir) {
				t.Errorf("the refusal holds a path: %v", err)
			}
		})
	}
}

// Before provisioning the lead's copy of the managed settings is not there
// and neither is its folder: the check reads the file the config's
// provision.managed_settings names, which provisioning copies. After
// provisioning it reads the provisioned copy. A missing marker is refused in
// both cases.
func TestCheckInputsReadsTheConfigsSettingsBeforeProvisioningAndTheCopyAfter(t *testing.T) {
	// removeCopyFolder makes the state of a root that is not provisioned: no
	// folder of the lead.
	removeCopyFolder := func(t *testing.T, in sandboxstart.Inputs) {
		t.Helper()
		if err := os.RemoveAll(filepath.Dir(in.Sources.ManagedSettings)); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("before provisioning, the config's file holds the marker", func(t *testing.T) {
		w := provisiontest.New(t)
		in := goodInputs(t, w)
		removeCopyFolder(t, in)
		provisiontest.WriteManagedSettings(t, w.Cfg.Provision.ManagedSettings, provisiontest.Marker)
		if err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600)); err != nil {
			t.Fatalf("error %v: the config's own file holds the marker", err)
		}
	})
	t.Run("before provisioning, the config's file lacks the marker", func(t *testing.T) {
		w := provisiontest.New(t)
		in := goodInputs(t, w)
		removeCopyFolder(t, in)
		provisiontest.WriteManagedSettings(t, w.Cfg.Provision.ManagedSettings, "a-different-text")
		err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
		if !errors.Is(err, ErrInputs) || !contains(err.Error(), "sources.managed_marker") {
			t.Fatalf("error %v: want a refusal that names the marker", err)
		}
		for _, leak := range []string{provisiontest.Marker, "a-different-text", w.Cfg.Provision.ManagedSettings, w.Dir} {
			if contains(err.Error(), leak) {
				t.Errorf("the refusal holds %q: %v", leak, err)
			}
		}
	})
	t.Run("before provisioning, the config's file is not there", func(t *testing.T) {
		w := provisiontest.New(t)
		in := goodInputs(t, w)
		removeCopyFolder(t, in)
		if err := os.Remove(w.Cfg.Provision.ManagedSettings); err != nil {
			t.Fatal(err)
		}
		err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
		if !errors.Is(err, ErrInputs) || !contains(err.Error(), "sources.managed_settings") {
			t.Fatalf("error %v: want a refusal that names sources.managed_settings", err)
		}
	})
	t.Run("after provisioning, the copy holds the marker", func(t *testing.T) {
		w := provisiontest.New(t)
		in := goodInputs(t, w)
		// The config's file lacks the marker: only the copy is read now.
		provisiontest.WriteManagedSettings(t, w.Cfg.Provision.ManagedSettings, "a-different-text")
		if err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600)); err != nil {
			t.Fatalf("error %v: the copy holds the marker", err)
		}
	})
	t.Run("after provisioning, the copy lacks the marker", func(t *testing.T) {
		w := provisiontest.New(t)
		in := goodInputs(t, w)
		// The config's file holds it: it must not be read in the copy's place.
		provisiontest.WriteManagedSettings(t, w.Cfg.Provision.ManagedSettings, provisiontest.Marker)
		provisiontest.WriteManagedSettings(t, in.Sources.ManagedSettings, "a-different-text")
		err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
		if !errors.Is(err, ErrInputs) || !contains(err.Error(), "sources.managed_marker") {
			t.Fatalf("error %v: want a refusal that names the marker", err)
		}
	})
}

// Another runtime has no guard hook to name: no marker and no file to search.
func TestCheckInputsNeedsNoMarkerForAnotherRuntime(t *testing.T) {
	w := provisiontest.New(t)
	w.Cfg.Agent.Runtime = string(profile.Claude.Other())
	in := goodInputs(t, w)
	in.Sources.ManagedMarker = ""
	if err := os.Remove(in.Sources.ManagedSettings); err != nil {
		t.Fatal(err)
	}
	// The profile names no place for this runtime's settings: the inputs do.
	in.Sources.ManagedSettingsInside = "/etc/sample/managed.toml"
	if err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600)); err != nil {
		t.Fatalf("the runtime %s is refused without a marker and a settings file: %v", w.Cfg.Agent.Runtime, err)
	}
}

// The spec trial of the holder's validate step runs on the file: a refusal it
// makes that no field check above makes is a refusal here, by the field, with
// no value of the file in it.
func TestCheckInputsRunsTheSpecTrialOfTheHolder(t *testing.T) {
	w := provisiontest.New(t)
	in := goodInputs(t, w)
	in.ProviderHost = "192.0.2.77" // an address: the spec wants a name to resolve
	err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
	if !errors.Is(err, ErrInputs) || !contains(err.Error(), "provider_host") {
		t.Fatalf("error %v: want a mismatch that names provider_host", err)
	}
	if contains(err.Error(), "192.0.2.77") {
		t.Errorf("the refusal holds the value: %v", err)
	}
}

// What the holder sets from the config before its sequence is part of the
// trial: a pinned digest that is not 64 hex digits, and no agent command
// line, are refused as at the holder.
func TestCheckInputsHoldsTheConfigsFieldsTheHolderChecks(t *testing.T) {
	for name, mod := range map[string]func(c *leadcanary.Config){
		"a malformed pin":       func(c *leadcanary.Config) { c.Holder.ProfileDigest = "not-a-digest" },
		"no agent command line": func(c *leadcanary.Config) { c.Agent.Argv = nil },
	} {
		t.Run(name, func(t *testing.T) {
			w := provisiontest.New(t)
			in := goodInputs(t, w)
			mod(&w.Cfg)
			err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
			if !errors.Is(err, ErrInputs) || !contains(err.Error(), "refused by the holder's validate step") {
				t.Fatalf("error %v: want the holder's refusal", err)
			}
		})
	}
}

// Every key of the inputs file that the holder's validate step needs, for the
// runtime claude, is refused when it is missing: the test derives the keys
// from the holder's own trial (TrialValidate, which validate runs), by
// blanking each key in turn, so a key that the spec starts to require is
// found here and not at the start of a real run. The keys it must find are
// named, so that the derivation cannot go quiet.
func TestEveryKeyTheHolderRequiresForClaudeIsCheckedByCheckInputs(t *testing.T) {
	w := provisiontest.New(t)
	if w.Cfg.Agent.Runtime != string(profile.Claude) {
		t.Fatalf("the world's runtime is %q, the test is about claude", w.Cfg.Agent.Runtime)
	}
	// The fields the holder sets from the config, as it sets them.
	holderView := func(in sandboxstart.Inputs) sandboxstart.Inputs {
		in.AgentArgv = append([]string(nil), w.Cfg.Agent.Argv...)
		in.AgentEnv = sandboxstart.AgentEnvOf(w.Cfg.Agent.Env)
		in.ExpectedDigest = w.Cfg.Holder.ProfileDigest
		rt := sandboxstart.RuntimeFolder(in.RuntimeRoot, in.LayoutRole)
		in.HolderPaths = []string{filepath.Join(rt, serve.AttachSocket), filepath.Join(rt, serve.ControlSocket)}
		return in
	}
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	base := goodInputs(t, w)
	if err := sandboxstart.TrialValidate(holderView(base), uid, gid); err != nil {
		t.Fatalf("the holder's trial refuses the good inputs: %v", err)
	}

	var keys []string
	var walk func(prefix string, v reflect.Value)
	walk = func(prefix string, v reflect.Value) {
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			keys = append(keys, prefix+name)
			if f.Type.Kind() == reflect.Struct {
				walk(prefix+name+".", v.Field(i))
			}
		}
	}
	walk("", reflect.ValueOf(base))
	if len(keys) < 20 {
		t.Fatalf("only %d keys found in the inputs", len(keys))
	}

	// blank returns a copy of base with the key emptied.
	blank := func(key string) sandboxstart.Inputs {
		in := base
		v := reflect.ValueOf(&in).Elem()
		parts := strings.Split(key, ".")
		for _, part := range parts {
			for i := 0; i < v.NumField(); i++ {
				if n, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ","); n == part {
					v = v.Field(i)
					break
				}
			}
		}
		v.Set(reflect.Zero(v.Type()))
		return in
	}
	required := map[string]bool{}
	for _, key := range keys {
		in := blank(key)
		if sandboxstart.TrialValidate(holderView(in), uid, gid) != nil {
			required[key] = true
			if err := CheckInputs(w.Cfg, writeInputs(t, in, 0o600)); err == nil {
				t.Errorf("the holder's validate step refuses the inputs without %s and check-inputs accepts them", key)
			}
		}
	}
	// By hand, because the derivation depends on the trial: the keys known to
	// be needed for claude. The marker is the one the first run missed.
	for _, key := range []string{"sources.managed_marker", "sources.managed_settings", "provider_host", "sources.qargi_install"} {
		if !required[key] {
			t.Errorf("the derivation did not find %s among the keys the holder requires (found %v)", key, sortedKeys(required))
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// provision keeps the names of the holder's two sockets itself and does not
// import the serving package (which pulls in the session pane): this test
// file, which is built only with the tag, is where the two meet, so that a
// rename in the serving package fails here.
func TestTheHolderSocketNamesAreThoseOfTheServingPackage(t *testing.T) {
	if attachSocket != serve.AttachSocket || controlSocket != serve.ControlSocket {
		t.Errorf("provision names %q and %q, the serving package %q and %q",
			attachSocket, controlSocket, serve.AttachSocket, serve.ControlSocket)
	}
	rt := sandboxstart.RuntimeFolder("/run/sample", "sample-lead")
	want := []string{filepath.Join(rt, serve.AttachSocket), filepath.Join(rt, serve.ControlSocket)}
	if got := holderSocketPaths("/run/sample", "sample-lead", false); !reflect.DeepEqual(got, want) {
		t.Errorf("the socket paths are %v, the holder's are %v", got, want)
	}
	// A lead that records adds the recording's folder, by the holder's own
	// name for it.
	if leadcanary.RecordDirName != screenrec.DirName {
		t.Errorf("the config names the recording %q, the holder %q", leadcanary.RecordDirName, screenrec.DirName)
	}
	want = append(want, filepath.Join(rt, screenrec.DirName))
	if got := holderSocketPaths("/run/sample", "sample-lead", true); !reflect.DeepEqual(got, want) {
		t.Errorf("the paths of a recording lead are %v, the holder's are %v", got, want)
	}
}

// The holder adds its two socket paths to the spec's mount substrings, whose
// list is capped, so a never_mounted list that fits without them can be
// refused at the start. check-inputs must agree with the holder's trial for
// every length around the cap, and the range must hold a length each way.
func TestCheckInputsAgreesWithTheHolderAtTheEdgeOfTheMountSubstringLimit(t *testing.T) {
	w := provisiontest.New(t)
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	var accepted, refused []int
	for n := 55; n <= 66; n++ {
		in := goodInputs(t, w)
		in.NeverMounted = nil
		for i := 0; i < n; i++ {
			in.NeverMounted = append(in.NeverMounted, "/opt/sample/never-"+strconv.Itoa(i))
		}
		// The holder's view: the fields it sets from the config, including
		// its two sockets as the serving package names them.
		view := in
		view.AgentArgv = append([]string(nil), w.Cfg.Agent.Argv...)
		view.AgentEnv = sandboxstart.AgentEnvOf(w.Cfg.Agent.Env)
		view.ExpectedDigest = w.Cfg.Holder.ProfileDigest
		rt := sandboxstart.RuntimeFolder(in.RuntimeRoot, in.LayoutRole)
		view.HolderPaths = []string{filepath.Join(rt, serve.AttachSocket), filepath.Join(rt, serve.ControlSocket)}
		holderErr := sandboxstart.TrialValidate(view, uid, gid)
		checkErr := CheckInputs(w.Cfg, writeInputs(t, in, 0o600))
		if (holderErr == nil) != (checkErr == nil) {
			t.Errorf("never_mounted of %d entries: the holder's trial says %v, check-inputs says %v", n, holderErr, checkErr)
		}
		if holderErr == nil {
			accepted = append(accepted, n)
		} else {
			refused = append(refused, n)
		}
	}
	if len(accepted) == 0 || len(refused) == 0 {
		t.Fatalf("the range holds no edge: accepted %v, refused %v", accepted, refused)
	}
	// Lengths that check-inputs accepted while the sockets were left out of
	// the trial (the other holder paths alone allow 61 entries).
	for _, n := range []int{60, 61, 62} {
		if !containsInt(refused, n) {
			t.Errorf("a never_mounted list of %d entries is not refused by the holder's trial (accepted %v)", n, accepted)
		}
	}
}

func containsInt(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}
