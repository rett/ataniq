//go:build linux && ataniq_ownerchannel

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

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

// guardDeadline bounds a run of the daemon that a guard should have refused.
// A refused configuration ends the run at once, so a run that is still serving
// after this time means a guard let it through.
const guardDeadline = 5 * time.Second

// runBounded runs the daemon's run command with the args and stops it with
// SIGTERM after guardDeadline. A guard that regressed would let a serving daemon
// start, which would only end at the go test timeout; here it ends the run and
// fails the test at once, with the code the daemon returned.
func runBounded(t *testing.T, ta *testApp, args []string) int {
	t.Helper()
	var late atomic.Bool
	timer := time.AfterFunc(guardDeadline, func() {
		late.Store(true)
		ta.signals <- syscall.SIGTERM
	})
	code := ta.run(args)
	timer.Stop()
	if late.Load() {
		t.Errorf("the daemon served until the test stopped it: a guard did not refuse (exit %d)", code)
	}
	return code
}

// runGuarded runs the daemon's run command with the owner flag, the suite in
// place, on the folders, with the environment of a fake account.
func runGuarded(t *testing.T, state, home string) (int, string) {
	t.Helper()
	useSuite(t, cmdSuite{})
	runtime := shortDir(t)
	ta := newTestApp(t, map[string]string{"HOME": home})
	code := runBounded(t, ta, []string{"run", "--state-dir", state, "--runtime-dir", runtime, "--owner-channel"})
	if _, err := os.Lstat(filepath.Join(runtime, "daemon.sock")); err == nil {
		t.Errorf("a socket was made for a refused configuration")
	}
	return code, ta.err.String()
}

// scratchFolder makes a scratch folder that is apart from the fake account's
// home folder, with a drop folder in it, so that the settings written with it
// as the root, the mirror's and the drop folder's place are refused by the one
// rule a test is about and by no other.
func scratchFolder(t *testing.T) string {
	t.Helper()
	scratch := shortDir(t)
	if err := os.Mkdir(scratch+"/drop", 0o700); err != nil {
		t.Fatal(err)
	}
	return scratch
}

// refusedBy checks the settings of state with checkOwnerScratch and requires
// an error that holds the text of the one rule under test. The text is the
// rule's own: another rule's refusal does not hold it.
func refusedBy(t *testing.T, name, state, rule string) {
	t.Helper()
	_, err := checkOwnerScratch(state, ownerGetenv)
	if err == nil || !strings.Contains(err.Error(), rule) {
		t.Errorf("%s: want a refusal by %q, got %v", name, rule, err)
	}
}

// The state folder that is the account's default, lies inside it or holds it
// is refused by the state folder's rule alone (the scratch root, the mirror and
// the drop folder are in a folder of their own), with the configuration's exit
// code by the daemon before it starts, and by the builder itself, and nothing
// is opened.
func TestTheOwnerChannelRefusesTheDefaultStateFolder(t *testing.T) {
	home := fakeAccount(t)
	def := filepath.Join(home, ".local", "state", "ataniq")
	const rule = "refuses the state folder"
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
		writeScratch(t, state, scratchFolder(t), nil)
		refusedBy(t, name, state, rule)
		code, stderr := runGuarded(t, state, home)
		if code != leaddaemon.ExitConfig || !strings.Contains(stderr, rule) {
			t.Errorf("%s: exit %d: %q", name, code, stderr)
		}
		if _, _, err := buildOwnerService(state); err == nil || !strings.Contains(err.Error(), rule) {
			t.Errorf("%s: the builder: %v", name, err)
		}
		if _, err := os.Lstat(filepath.Join(state, "owner", hostadapter.JournalName)); err == nil {
			t.Errorf("%s: a journal was made", name)
		}
		_ = os.Remove(filepath.Join(home, "alias"))
	}
}

// The default state folder named by the environment (XDG_STATE_HOME) and the
// default runtime folder are refused too, whatever the account says, by the
// state folder's rule alone.
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
		writeScratch(t, state, scratchFolder(t), nil)
		runtime := shortDir(t)
		ta := newTestApp(t, map[string]string{"HOME": home, "XDG_STATE_HOME": xdg, "XDG_RUNTIME_DIR": xrun})
		code := runBounded(t, ta, []string{"run", "--state-dir", state, "--runtime-dir", runtime, "--owner-channel"})
		if code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "refuses the state folder") {
			t.Errorf("%s: exit %d: %q", name, code, ta.err.String())
		}
	}
}

// The drop folder is held to the default folders and the state folder too: one
// that is, lies inside or holds a default folder, or is, lies in or holds the
// state folder (so its owner folder as well), is refused by the drop folder's
// rule alone. The state folder is a scratch one, apart from the account's.
func TestTheOwnerChannelRefusesADropFolderInTheRealFolders(t *testing.T) {
	home := fakeAccount(t)
	def := filepath.Join(home, ".local", "state", "ataniq")
	if err := os.MkdirAll(def, 0o700); err != nil {
		t.Fatal(err)
	}
	type tc struct {
		drop func(state string) string
		rule string
	}
	for name, c := range map[string]tc{
		"the default state folder":     {func(string) string { return def }, "default folder"},
		"inside the default":           {func(string) string { return filepath.Join(def, "drop") }, "default folder"},
		"a folder holding the default": {func(string) string { return home }, "default folder"},
		"the state folder":             {func(s string) string { return s }, "state folder"},
		"the owner folder of state":    {func(s string) string { return s + "/owner" }, "state folder"},
		"inside the state folder":      {func(s string) string { return s + "/drop" }, "state folder"},
	} {
		scratch := scratchFolder(t)
		state := scratch + "/s"
		if err := os.MkdirAll(state+"/owner", 0o700); err != nil {
			t.Fatal(err)
		}
		d := c.drop(state)
		writeScratch(t, state, scratch, func(s *hostadapter.Scratch) { s.Drop = d })
		refusedBy(t, name, state, "refuses the drop folder")
		refusedBy(t, name, state, c.rule)
	}
	// The control: the drop folder in the scratch folder is accepted.
	scratch := scratchFolder(t)
	if err := os.MkdirAll(scratch+"/s/owner", 0o700); err != nil {
		t.Fatal(err)
	}
	writeScratch(t, scratch+"/s", scratch, nil)
	if _, err := checkOwnerScratch(scratch+"/s", ownerGetenv); err != nil {
		t.Errorf("the control was refused: %v", err)
	}
}

// The sandbox template is not inside the drop folder, the mirror or the work
// folder, and the work folder does not hold or lie in the drop folder; each is
// refused by that rule alone.
func TestTheOwnerChannelKeepsTheTemplateAndTheWorkFolderApartFromTheDropFolderAndTheMirror(t *testing.T) {
	fakeAccount(t)
	type tc struct {
		mutate func(scratch string, s *hostadapter.Scratch)
		rule   string
	}
	for name, c := range map[string]tc{
		"a template in the drop folder":    {func(sc string, s *hostadapter.Scratch) { s.Sandbox, s.Work = sc+"/drop/t.json", sc+"/work" }, "inside the drop folder or the mirror"},
		"a template in the mirror":         {func(sc string, s *hostadapter.Scratch) { s.Sandbox, s.Work = sc+"/mirror.git/t.json", sc+"/work" }, "inside the drop folder or the mirror"},
		"a template in the work folder":    {func(sc string, s *hostadapter.Scratch) { s.Sandbox, s.Work = sc+"/work/t.json", sc+"/work" }, "inside the work folder"},
		"a work folder in the drop folder": {func(sc string, s *hostadapter.Scratch) { s.Sandbox, s.Work = sc+"/t.json", sc+"/drop/work" }, "contains the drop folder"},
		"a work folder holding the drop folder": {func(sc string, s *hostadapter.Scratch) {
			s.Sandbox, s.Work, s.Drop = sc+"/t.json", sc+"/w", sc+"/w/drop"
		}, "contains the drop folder"},
	} {
		scratch := scratchFolder(t)
		state := scratch + "/s"
		if err := os.MkdirAll(state+"/owner", 0o700); err != nil {
			t.Fatal(err)
		}
		writeScratch(t, state, scratch, func(s *hostadapter.Scratch) { c.mutate(scratch, s) })
		refusedBy(t, name, state, c.rule)
	}
	// The control: a template and a work folder of their own.
	scratch := scratchFolder(t)
	if err := os.MkdirAll(scratch+"/s/owner", 0o700); err != nil {
		t.Fatal(err)
	}
	writeScratch(t, scratch+"/s", scratch, func(s *hostadapter.Scratch) { s.Sandbox, s.Work = scratch+"/t.json", scratch+"/work" })
	if _, err := checkOwnerScratch(scratch+"/s", ownerGetenv); err != nil {
		t.Errorf("the control was refused: %v", err)
	}
}

// A scratch root that is a default folder or holds one, and a mirror that is
// not under a scratch root, are refused.
func TestTheOwnerChannelRefusesARealRootAndAMirrorOutsideTheScratchRoots(t *testing.T) {
	home := fakeAccount(t)
	type tc struct {
		mutate func(*hostadapter.Scratch)
		rule   string
	}
	for name, c := range map[string]tc{
		"a root that is the default":  {func(s *hostadapter.Scratch) { s.Roots = []string{filepath.Join(home, ".local", "state", "ataniq")} }, "refuses the scratch root"},
		"a root that holds a default": {func(s *hostadapter.Scratch) { s.Roots = []string{home} }, "refuses the scratch root"},
		"the root of the file system": {func(s *hostadapter.Scratch) { s.Roots = []string{"/"} }, "refuses the scratch root"},
		"a mirror outside the roots": {func(s *hostadapter.Scratch) {
			s.Roots = []string{filepath.Join(filepath.Dir(s.Mirror), "elsewhere")}
		}, "refuses the mirror"},
		"a mirror in a real project": {func(s *hostadapter.Scratch) { s.Mirror = filepath.Join(home, "project", "mirror.git") }, "refuses the mirror"},
	} {
		state, _ := scratchSettings(t, c.mutate)
		if err := os.MkdirAll(filepath.Join(home, ".local", "state", "ataniq"), 0o700); err != nil {
			t.Fatal(err)
		}
		code, stderr := runGuarded(t, state, home)
		if code != leaddaemon.ExitConfig || !strings.Contains(stderr, c.rule) {
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
