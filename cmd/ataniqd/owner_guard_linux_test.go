//go:build linux && ataniq_ownerchannel

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/leaddaemon"
)

// fakeAccount makes a home folder for an account the tests stand in, with the
// default state folder of the account made and holding the owner settings, and
// points the builder's lookups at it. It returns the home folder.
func fakeAccount(t *testing.T) string {
	t.Helper()
	home := shortDir(t)
	oldHome, oldEnv := ownerAccountHome, ownerGetenv
	ownerAccountHome = func(int) (string, error) { return home, nil }
	ownerGetenv = func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	t.Cleanup(func() { ownerAccountHome, ownerGetenv = oldHome, oldEnv })
	return home
}

// runGuarded runs the daemon's run command with the owner flag, the suite in
// place, on the folders, with the environment of a fake account.
func runGuarded(t *testing.T, state, home string) (int, string) {
	t.Helper()
	useSuite(t, cmdSuite{})
	runtime := shortDir(t)
	ta := newTestApp(t, map[string]string{"HOME": home})
	code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", runtime, "--owner-channel"})
	if _, err := os.Lstat(filepath.Join(runtime, "daemon.sock")); err == nil {
		t.Errorf("a socket was made for a refused configuration")
	}
	return code, ta.err.String()
}

// The state folder that is the account's default, lies inside it or holds it
// is refused with the configuration's exit code, by the daemon before it
// starts and by the builder itself, and nothing is opened.
func TestTheOwnerChannelRefusesTheDefaultStateFolder(t *testing.T) {
	home := fakeAccount(t)
	def := filepath.Join(home, ".local", "state", "ataniq")
	for name, state := range map[string]string{
		"the default":              def,
		"inside the default":       filepath.Join(def, "sub"),
		"holding the default":      filepath.Join(home, ".local", "state"),
		"the home folder":          home,
		"a link to the default":    filepath.Join(home, "alias"),
		"a link below the default": filepath.Join(home, "alias", "sub"),
	} {
		if err := os.MkdirAll(def, 0o700); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(name, "a link") {
			if err := os.Symlink(def, filepath.Join(home, "alias")); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Join(state, "owner"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeScratch(t, state, home, nil)
		code, stderr := runGuarded(t, state, home)
		if code != leaddaemon.ExitConfig || !strings.Contains(stderr, "default folder") {
			t.Errorf("%s: exit %d: %q", name, code, stderr)
		}
		if _, _, err := buildOwnerService(state); err == nil || !strings.Contains(err.Error(), "default folder") {
			t.Errorf("%s: the builder: %v", name, err)
		}
		if _, err := os.Lstat(filepath.Join(state, "owner", hostadapter.JournalName)); err == nil {
			t.Errorf("%s: a journal was made", name)
		}
		_ = os.Remove(filepath.Join(home, "alias"))
	}
}

// The default state folder named by the environment (XDG_STATE_HOME) and the
// default runtime folder are refused too, whatever the account says.
func TestTheOwnerChannelRefusesTheEnvironmentsDefaults(t *testing.T) {
	useSuite(t, cmdSuite{})
	home := fakeAccount(t)
	xdg, xrun := shortDir(t), shortDir(t)
	for name, state := range map[string]string{
		"XDG_STATE_HOME":  filepath.Join(xdg, "ataniq"),
		"XDG_RUNTIME_DIR": filepath.Join(xrun, "ataniq"),
	} {
		if err := os.MkdirAll(filepath.Join(state, "owner"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeScratch(t, state, home, nil)
		runtime := shortDir(t)
		ta := newTestApp(t, map[string]string{"HOME": home, "XDG_STATE_HOME": xdg, "XDG_RUNTIME_DIR": xrun})
		code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", runtime, "--owner-channel"})
		if code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "default folder") {
			t.Errorf("%s: exit %d: %q", name, code, ta.err.String())
		}
	}
}

// A scratch root that is a default folder or holds one, and a mirror that is
// not under a scratch root, are refused.
func TestTheOwnerChannelRefusesARealRootAndAMirrorOutsideTheScratchRoots(t *testing.T) {
	home := fakeAccount(t)
	for name, mutate := range map[string]func(*hostadapter.Scratch){
		"a root that is the default":  func(s *hostadapter.Scratch) { s.Roots = []string{filepath.Join(home, ".local", "state", "ataniq")} },
		"a root that holds a default": func(s *hostadapter.Scratch) { s.Roots = []string{home} },
		"the root of the file system": func(s *hostadapter.Scratch) { s.Roots = []string{"/"} },
		"a mirror outside the roots": func(s *hostadapter.Scratch) {
			s.Roots = []string{filepath.Join(filepath.Dir(s.Mirror), "elsewhere")}
		},
		"a mirror in a real project": func(s *hostadapter.Scratch) { s.Mirror = filepath.Join(home, "project", "mirror.git") },
	} {
		state, _ := scratchSettings(t, mutate)
		if err := os.MkdirAll(filepath.Join(home, ".local", "state", "ataniq"), 0o700); err != nil {
			t.Fatal(err)
		}
		code, stderr := runGuarded(t, state, home)
		if code != leaddaemon.ExitConfig || stderr == "" {
			t.Errorf("%s: exit %d: %q", name, code, stderr)
		}
		if _, _, err := buildOwnerService(state); err == nil {
			t.Errorf("%s: the builder built a service", name)
		}
		if _, err := os.Lstat(filepath.Join(state, "owner", hostadapter.JournalName)); err == nil {
			t.Errorf("%s: a journal was made", name)
		}
	}
	// The control: a mirror under the root, with the defaults elsewhere.
	state, _ := scratchSettings(t, nil)
	if _, closer, err := buildOwnerService(state); err != nil {
		t.Errorf("the scratch settings were refused: %v", err)
	} else {
		_ = closer()
	}
}

// The settings must select the stand-in door: a missing key, another door and
// a missing scratch root are refused with the configuration's exit code.
func TestTheOwnerChannelRequiresTheSettingsToSelectTheStandInDoor(t *testing.T) {
	home := fakeAccount(t)
	for name, mutate := range map[string]func(*hostadapter.Scratch){
		"no door":         func(s *hostadapter.Scratch) { s.Door = "" },
		"a real door":     func(s *hostadapter.Scratch) { s.Door = "real" },
		"no scratch root": func(s *hostadapter.Scratch) { s.Roots = nil },
		"a relative root": func(s *hostadapter.Scratch) { s.Roots = []string{"scratch"} },
	} {
		state, _ := scratchSettings(t, mutate)
		code, stderr := runGuarded(t, state, home)
		if code != leaddaemon.ExitConfig || stderr == "" {
			t.Errorf("%s: exit %d: %q", name, code, stderr)
		}
		if _, _, err := buildOwnerService(state); err == nil {
			t.Errorf("%s: the builder built a service", name)
		}
	}
	// An unknown key stays refused (the settings are strict).
	state, _ := scratchSettings(t, nil)
	path := filepath.Join(state, "owner", hostadapter.ScratchName)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(good), `"door"`, `"doors":1,"door"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := runGuarded(t, state, home); code != leaddaemon.ExitConfig {
		t.Errorf("an unknown key: exit %d", code)
	}
}

// A build with a builder of its own (a test's) is not held to the host
// adapter's guard, and prints no stand-in line.
func TestTheGuardAndTheStartLineBelongToTheHostAdapterBuilder(t *testing.T) {
	if ownerGuard == nil || ownerGuard.banner != ownerBanner {
		t.Fatalf("the host adapter's builder has no guard: %+v", ownerGuard)
	}
}
