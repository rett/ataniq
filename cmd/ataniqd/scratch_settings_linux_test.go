//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/seatgate"
)

// Tests of run --owner-settings DIR and run --check (scratch_settings_linux.go):
// the scratch daemon reads the owner channel's settings from a folder of the
// proof's own and never from owner/scratch.json of the state folder, refuses a
// folder that is the trial's, the state folder's, the runtime folder's, within
// a lead's reach or the gate's, and --check prints every check and binds,
// locks, makes and starts nothing. The state folder is a phone trial's, made
// as the owner makes his; nothing here starts a unit, a sandbox or an agent.

// settingsWorld is a trial's state folder and a proof's folder beside it.
type settingsWorld struct {
	dir, state, root, config, settings string
	owner, runtime                     string
	gateFP                             string
	seat                               seatgate.Identity
	envMap                             map[string]string
	kept                               map[string][]byte
}

func (w *settingsWorld) env(k string) string { return w.envMap[k] }

// newSettingsWorld makes the trial's state folder (trial init, the gate's key,
// one pin, one paired key, the scratch root marker) and the proof's folder:
// the scratch config of prepare.sh for sitting 1, the stand-in lead only, and
// the proof's settings in ROOT/owner-settings/scratch.json.
func newSettingsWorld(t *testing.T) *settingsWorld {
	t.Helper()
	dir, err := filepath.EvalSymlinks(shortDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return newSettingsWorldIn(t, dir)
}

// newSettingsWorldIn is newSettingsWorld in the folder dir, which exists and
// is at its physical path.
func newSettingsWorldIn(t *testing.T, dir string) *settingsWorld {
	t.Helper()
	w := &settingsWorld{dir: dir, state: filepath.Join(dir, "trial-state"), root: filepath.Join(dir, "proof")}
	w.owner = filepath.Join(w.state, hostadapter.OwnerDirName)
	w.config = filepath.Join(w.root, "scratch.json")
	w.settings = filepath.Join(w.root, "owner-settings")
	w.runtime = filepath.Join(w.root, "runtime")
	w.envMap = map[string]string{
		"HOME": filepath.Join(dir, "home"), "XDG_STATE_HOME": filepath.Join(dir, "xdg-state"), "XDG_RUNTIME_DIR": filepath.Join(dir, "xdg-run"),
	}
	ti := newTestApp(t, nil)
	ti.getenv = w.env
	if code := ti.run([]string{"trial", "init", "--state-dir", w.state}); code != 0 {
		t.Fatalf("trial init: %d %s", code, ti.err.String())
	}
	w.gateFP = gateInit(t, w.state)
	w.seat = newSeat(t, "seat")
	writePins(t, w.state, w.seat.Fingerprint()+"\n", 0o600)
	keys, err := ownerjournal.OpenKeys(keysPathOf(w.state))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.AddKey(ownerjournal.NewKey{
		CredentialID: []byte(strings.Repeat("c", 16)), PublicKey: []byte("k"), Kind: "platform-passkey",
		PairedAt: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC), Words: keyWords(),
	}); err != nil {
		t.Fatal(err)
	}
	keys.Close()
	if err := canaryroot.WriteScratchMarker(w.state); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{w.root, w.settings, filepath.Join(w.root, "agent-install"), filepath.Join(w.root, "bin"),
		filepath.Join(w.root, "owner-channel"), filepath.Join(w.root, "owner-channel", "drop"), filepath.Join(dir, "home")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(w.root, "bin", "ataniq-probe"), []byte("#!/bin/sh\nexit 97\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.config, proofScript(t, "config", w.state, w.root, "1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.settings, "scratch.json"), proofScript(t, "settings", w.root), 0o600); err != nil {
		t.Fatal(err)
	}
	w.kept = w.trialBytes(t)
	return w
}

// trialBytes are the trial's gate identity, pins, paired keys and settings.
func (w *settingsWorld) trialBytes(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, name := range []string{"gate/cert.pem", "gate/key.pem", "gate/" + leaddaemon.SeatPinsFileName,
		ownerjournal.KeysFileName, ownerjournal.KeysHeadFileName, hostadapter.ScratchName} {
		out[name] = fileBytes(t, filepath.Join(w.owner, name))
	}
	return out
}

// sameTrial fails the test when a file of the trial is not the bytes it was.
func (w *settingsWorld) sameTrial(t *testing.T, when string) {
	t.Helper()
	for name, want := range w.kept {
		if got := fileBytes(t, filepath.Join(w.owner, name)); !bytes.Equal(got, want) {
			t.Errorf("%s: owner/%s is not the bytes it was", when, name)
		}
	}
}

// runArgs are the flags of the proof's daemon with the settings folder given.
func (w *settingsWorld) runArgs(dir string, more ...string) []string {
	return append([]string{"run", "--scratch", w.config, "--owner-channel", "--seat-gate", "127.0.0.1:7443", "--owner-settings", dir}, more...)
}

// run runs the command to its end and returns the exit code, the output and
// the error output.
func (w *settingsWorld) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	ta := canaryApp(t)
	ta.getenv = w.env
	code := runBounded(t, ta, args)
	return code, ta.out.String(), ta.err.String()
}

// noListen fails the test when a gate listens.
func noListen(t *testing.T) {
	t.Helper()
	old := seatGateListen
	seatGateListen = func(string, string) (net.Listener, error) {
		t.Error("the gate listened")
		return nil, os.ErrPermission
	}
	t.Cleanup(func() { seatGateListen = old })
}

// The proof's daemon on the trial's state folder, with the trial's settings
// left in owner/scratch.json: it reads the settings of the folder it is given,
// says so at start, serves the trial's gate identity and paired key, and a
// trial daemon started afterwards on the same folder serves its samples again.
// The trial's gate key, certificate, pins, key list and settings are the same
// bytes after the proof's daemon ran as before, and its settings the same
// file.
func TestOwnerSettingsReadsTheProofsSettingsAndKeepsTheTrials(t *testing.T) {
	useSuite(t, cmdSuite{})
	last := gatePorts(t)
	w := newSettingsWorld(t)
	old := ownerInspector
	ownerInspector = ownerCmdTerminal{}
	t.Cleanup(func() { ownerInspector = old })
	trialID := inodeOf(t, filepath.Join(w.owner, hostadapter.ScratchName))

	// Without the flag the daemon reads the trial's settings and refuses.
	if code, _, msg := w.run(t, "run", "--scratch", w.config, "--owner-channel", "--seat-gate", "127.0.0.1:7443"); code != leaddaemon.ExitConfig ||
		!strings.Contains(msg, `the scratch settings are a phone trial's ("trial": true)`) {
		t.Fatalf("without --owner-settings: exit %d: %s", code, msg)
	}

	sa := canaryApp(t)
	sa.getenv = w.env
	errs := &gateBuf{}
	sa.stderr = errs
	socket := filepath.Join(w.runtime, "daemon.sock")
	done := startApp(t, sa, w.runArgs(w.settings)[1:], socket)
	addr := last()
	log := errs.String()
	for _, want := range []string{
		"ataniqd: " + ownerBanner + "\n",
		"ataniqd: owner channel: settings read from " + w.settings + "/scratch.json (--owner-settings); owner/scratch.json of the state folder is not read\n",
		"ataniqd: seat gate: listening on " + addr + ", key fingerprint " + w.gateFP,
		"ataniqd: seat gate: " + scratchChatNote + "\n",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the start does not say %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "TRIAL") || strings.Contains(log, "cannot be opened") {
		t.Errorf("the start:\n%s", log)
	}
	if v := ownerKeys(t, socket); len(v.Keys) != 1 || v.Keys[0].Revoked {
		t.Errorf("the paired key: %+v", v)
	}
	c := dialSeat(t, addr, w.seat, w.gateFP)
	leads, code := seatChat(t, c, `{"verb":"leads"}`)
	if code != "" || leads.StandIn || len(leads.Leads) != 1 || leads.Leads[0].Lead != "daemon:standin-1" {
		t.Errorf("the leads on the proof's gate: %+v %q", leads, code)
	}
	c.Close()
	stopApp(t, sa, errs, done)
	w.sameTrial(t, "after the proof's daemon")
	if inodeOf(t, filepath.Join(w.owner, hostadapter.ScratchName)) != trialID {
		t.Error("the trial's settings are not the same file")
	}

	// The trial again, on the same folder, by its own command.
	trialRun := filepath.Join(shortDir(t), "tr")
	if err := os.Mkdir(trialRun, 0o700); err != nil {
		t.Fatal(err)
	}
	ta := canaryApp(t)
	ta.getenv = w.env
	terrs := &gateBuf{}
	ta.stderr = terrs
	tdone := startApp(t, ta, []string{"--state-dir", w.state, "--runtime-dir", trialRun, "--owner-channel", "--trial", "--seat-gate", "127.0.0.1:7443"}, trialRun+"/daemon.sock")
	if tlog := terrs.String(); !strings.Contains(tlog, "key fingerprint "+w.gateFP) || !strings.Contains(tlog, trialNote) {
		t.Errorf("the trial after the proof:\n%s", tlog)
	}
	stopApp(t, ta, terrs, tdone)
	w.sameTrial(t, "after the trial ran again")
}

// --owner-settings and --check go with --scratch and --owner-channel only;
// --check needs --owner-settings; and every refusal comes before anything is
// made.
func TestOwnerSettingsFlagsNeedTheirCompanions(t *testing.T) {
	w := newSettingsWorld(t)
	other := filepath.Join(w.dir, "other-state")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "--scratch", w.config, "--owner-settings", w.settings}, "--owner-settings and --check need --owner-channel"},
		{[]string{"run", "--scratch", w.config, "--check"}, "--owner-settings and --check need --owner-channel"},
		{[]string{"run", "--state-dir", other, "--runtime-dir", other + "-run", "--owner-channel", "--owner-settings", w.settings}, "need --scratch CONFIG"},
		{[]string{"run", "--state-dir", other, "--runtime-dir", other + "-run", "--owner-channel", "--trial", "--owner-settings", w.settings}, "need --scratch CONFIG"},
		{[]string{"run", "--scratch", w.config, "--owner-channel", "--check"}, "--check needs --owner-settings DIR"},
	} {
		code, _, msg := w.run(t, c.args...)
		if code != leaddaemon.ExitUsage || !strings.Contains(msg, c.want) {
			t.Errorf("%v: exit %d, want %d and %q: %s", c.args, code, leaddaemon.ExitUsage, c.want, msg)
		}
	}
	for _, p := range []string{w.runtime, other, other + "-run"} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("a refused run made %s", p)
		}
	}
	w.sameTrial(t, "after the refusals")
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"--help"}); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"--owner-settings DIR", "--check ", "owner channel: settings read from"} {
		if !strings.Contains(ta.out.String(), want) {
			t.Errorf("the usage does not name %q", want)
		}
	}
}

// settingsCase is one folder given to --owner-settings that the daemon
// refuses, and the rule whose line of --check fails.
type settingsCase struct {
	name  string
	setup func(t *testing.T, w *settingsWorld) string
	rule  string
	want  string
}

// mkSettings makes a private folder holding a copy of the proof's settings.
func mkSettings(t *testing.T, w *settingsWorld, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scratch.json"), fileBytes(t, filepath.Join(w.settings, "scratch.json")), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The folders the daemon refuses, each with --check (the line of its rule
// says FAIL and why, the others are still checked, exit 10) and without it
// (exit 10 and the reason, before any lock and with nothing made): the
// trial's settings folder, a folder in it, either reached through a link or a
// link whose target climbs with "..", the state folder, the runtime folder, a
// folder in a lead's home, a folder in the agent install, the folder that
// holds the probe, the gate's folder, the drop folder its own settings name,
// a folder that holds it, a folder in the mirror or the work folder they name
// or at the name of their sandbox template, a folder others can enter, a settings
// file that is a link, that is the key list by a second name, that others can
// read or that holds the trial's settings, a relative path and a folder that
// is not there. The trial's files are the same bytes after all of them.
func TestOwnerSettingsRefusesTheTrialsTheLeadsAndTheGatesFolders(t *testing.T) {
	useSuite(t, cmdSuite{})
	noListen(t)
	cases := []settingsCase{
		{"the trial's settings folder", func(t *testing.T, w *settingsWorld) string { return w.owner }, ruleTrial, "is or lies in owner/ of the state folder"},
		{"a folder in the trial's settings folder", func(t *testing.T, w *settingsWorld) string { return mkSettings(t, w, filepath.Join(w.owner, "proof")) },
			ruleTrial, "is or lies in owner/ of the state folder"},
		{"a link to the trial's settings folder", func(t *testing.T, w *settingsWorld) string {
			l := filepath.Join(w.dir, "to-owner")
			if err := os.Symlink(w.owner, l); err != nil {
				t.Fatal(err)
			}
			return l
		}, ruleTrial, "is or lies in owner/ of the state folder"},
		{"a link that climbs with ..", func(t *testing.T, w *settingsWorld) string {
			if err := os.Mkdir(filepath.Join(w.dir, "x"), 0o700); err != nil {
				t.Fatal(err)
			}
			l := filepath.Join(w.dir, "x", "up")
			if err := os.Symlink("../trial-state/./owner/../owner", l); err != nil {
				t.Fatal(err)
			}
			return l
		}, ruleTrial, "is or lies in owner/ of the state folder"},
		{"the state folder", func(t *testing.T, w *settingsWorld) string { return w.state }, ruleRoots, "is or lies in the state folder"},
		{"the runtime folder", func(t *testing.T, w *settingsWorld) string { return mkSettings(t, w, w.runtime) }, ruleRoots, "is or lies in the runtime folder"},
		{"a folder in a lead's home", func(t *testing.T, w *settingsWorld) string {
			return mkSettings(t, w, filepath.Join(w.state, "leads", "scratch-standin-1-lead", "home", "s"))
		}, ruleLead, "is or lies in the lead standin-1: its folder"},
		{"a folder in the agent install", func(t *testing.T, w *settingsWorld) string {
			return mkSettings(t, w, filepath.Join(w.root, "agent-install", "s"))
		}, ruleLead, "is or lies in the agent install"},
		{"the folder of the probe", func(t *testing.T, w *settingsWorld) string { return mkSettings(t, w, filepath.Join(w.root, "bin")) },
			ruleLead, "holds the probe"},
		{"the proof's root, which holds the agent install", func(t *testing.T, w *settingsWorld) string { return w.root }, ruleLead, "holds the agent install"},
		{"the gate's folder", func(t *testing.T, w *settingsWorld) string { return filepath.Join(w.owner, leaddaemon.SeatGateDirName) },
			ruleSecret, "is or lies in the gate's identity and pins"},
		{"the drop folder its settings name", func(t *testing.T, w *settingsWorld) string {
			return mkSettings(t, w, filepath.Join(w.root, "owner-channel", "drop"))
		}, ruleNamed, "is or lies in the drop folder the settings name"},
		{"a folder that holds the folders its settings name", func(t *testing.T, w *settingsWorld) string {
			return mkSettings(t, w, filepath.Join(w.root, "owner-channel"))
		}, ruleNamed, "holds the drop folder the settings name"},
		{"a folder in the mirror its settings name", func(t *testing.T, w *settingsWorld) string {
			return mkSettings(t, w, filepath.Join(w.root, "owner-channel", "mirror.git", "s"))
		}, ruleNamed, "is or lies in the mirror the settings name"},
		{"a folder in the work folder its settings name", func(t *testing.T, w *settingsWorld) string {
			return mkSettings(t, w, filepath.Join(w.root, "owner-channel", "work", "s"))
		}, ruleNamed, "is or lies in the work folder the settings name"},
		{"a folder at the name of the sandbox template its settings name", func(t *testing.T, w *settingsWorld) string {
			return mkSettings(t, w, filepath.Join(w.root, "owner-channel", "no-template.json"))
		}, ruleNamed, "is or lies in the sandbox template the settings name"},
		{"a folder others can enter", func(t *testing.T, w *settingsWorld) string {
			d := mkSettings(t, w, filepath.Join(w.dir, "open"))
			if err := os.Chmod(d, 0o755); err != nil {
				t.Fatal(err)
			}
			return d
		}, "a private folder of the daemon's user", "can be reached by the group or others (mode 0755)"},
		{"settings that are a link", func(t *testing.T, w *settingsWorld) string {
			d := filepath.Join(w.dir, "linked-file")
			if err := os.Mkdir(d, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(w.settings, "scratch.json"), filepath.Join(d, "scratch.json")); err != nil {
				t.Fatal(err)
			}
			return d
		}, "the settings file", "scratch.json is a link"},
		{"settings that are the key list by a second name", func(t *testing.T, w *settingsWorld) string {
			d := filepath.Join(w.dir, "hard")
			if err := os.Mkdir(d, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(keysPathOf(w.state), filepath.Join(d, "scratch.json")); err != nil {
				t.Fatal(err)
			}
			return d
		}, "the settings file", "is the key list (owner/keys.log)"},
		{"settings others can read", func(t *testing.T, w *settingsWorld) string {
			d := mkSettings(t, w, filepath.Join(w.dir, "readable"))
			if err := os.Chmod(filepath.Join(d, "scratch.json"), 0o644); err != nil {
				t.Fatal(err)
			}
			return d
		}, "the settings file", "can be read by the group or others"},
		{"the trial's settings in a folder of their own", func(t *testing.T, w *settingsWorld) string {
			d := filepath.Join(w.dir, "trial-copy")
			if err := os.Mkdir(d, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "scratch.json"), w.kept[hostadapter.ScratchName], 0o600); err != nil {
				t.Fatal(err)
			}
			return d
		}, "the settings file", "are a phone trial's"},
		{"no settings file", func(t *testing.T, w *settingsWorld) string {
			d := filepath.Join(w.dir, "empty")
			if err := os.Mkdir(d, 0o700); err != nil {
				t.Fatal(err)
			}
			return d
		}, "the settings file", "there is no"},
		{"a relative path", func(t *testing.T, w *settingsWorld) string { return "proof/owner-settings" }, "the settings folder", "not a clean absolute path"},
		{"a folder that is not there", func(t *testing.T, w *settingsWorld) string { return filepath.Join(w.dir, "nowhere") }, "the settings folder", "cannot be resolved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newSettingsWorld(t)
			dir := c.setup(t, w)
			before := treeOf(t, w.state)
			code, out, msg := w.run(t, w.runArgs(dir, "--check")...)
			failLine := "FAIL  " + c.rule + ": "
			if code != leaddaemon.ExitConfig || !strings.Contains(out, failLine) || msg != "" {
				t.Fatalf("--check: exit %d, want %d and %q:\n%s%s", code, leaddaemon.ExitConfig, failLine, out, msg)
			}
			i := strings.Index(out, failLine)
			if l := out[i : i+strings.Index(out[i:], "\n")]; !strings.Contains(l, c.want) {
				t.Errorf("--check's line %q does not say %q", l, c.want)
			}
			for _, rest := range []string{"the root marker", "the seat gate", "the socket paths"} {
				if !strings.Contains(out, "pass  "+rest+": ") {
					t.Errorf("--check did not go on to %q:\n%s", rest, out)
				}
			}
			if !strings.Contains(out, "refusal(s): the daemon would not start (exit 10)") {
				t.Errorf("--check's last line:\n%s", out)
			}
			code, _, msg = w.run(t, w.runArgs(dir)...)
			if code != leaddaemon.ExitConfig || !strings.Contains(msg, c.want) || !strings.HasPrefix(msg, "ataniqd run: --owner-settings: ") &&
				!strings.HasPrefix(msg, "ataniqd run: --owner-channel: ") {
				t.Errorf("run: exit %d, want %d and %q: %s", code, leaddaemon.ExitConfig, c.want, msg)
			}
			if strings.Count(msg, "\n") != 1 {
				t.Errorf("run printed more than its refusal: %s", msg)
			}
			if _, err := os.Lstat(filepath.Join(w.runtime, "daemon.sock")); err == nil {
				t.Error("a refused daemon made its socket")
			}
			if !sameTreeOf(before, treeOf(t, w.state)) {
				t.Error("a refused daemon changed the state folder")
			}
			w.sameTrial(t, "after the refusal")
		})
	}
}

// A link planted after the folder was checked changes nothing: the folder
// read is the one opened, and the settings file is read beneath its handle.
// A folder whose path becomes a link to owner/ after it was opened still
// gives the proof's settings; a settings file replaced by a link, or by
// another file, between its check and its read is refused.
func TestOwnerSettingsALinkPlantedAfterTheCheckChangesNothing(t *testing.T) {
	useSuite(t, cmdSuite{})
	noListen(t)
	plant := func(t *testing.T, stage string, do func()) {
		old := ownerSettingsHook
		done := false
		ownerSettingsHook = func(s string) {
			if s == stage && !done {
				done = true
				do()
			}
		}
		t.Cleanup(func() { ownerSettingsHook = old })
	}

	w := newSettingsWorld(t)
	plant(t, "opened", func() {
		if err := os.Rename(w.settings, w.settings+".moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(w.owner, w.settings); err != nil {
			t.Fatal(err)
		}
	})
	code, out, msg := w.run(t, w.runArgs(w.settings, "--check")...)
	if code != 0 || !strings.Contains(out, "pass  the settings: not a phone trial's; the stand-in door; project proof-project") {
		t.Fatalf("a link planted after the folder was opened: exit %d:\n%s%s", code, out, msg)
	}

	w = newSettingsWorld(t)
	file := filepath.Join(w.settings, "scratch.json")
	plant(t, "file", func() {
		if err := os.Rename(file, file+".moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(w.owner, hostadapter.ScratchName), file); err != nil {
			t.Fatal(err)
		}
	})
	code, out, _ = w.run(t, w.runArgs(w.settings, "--check")...)
	if code != leaddaemon.ExitConfig || !strings.Contains(out, "FAIL  the settings file: ") || strings.Contains(out, "pass  the settings:") {
		t.Fatalf("a link planted after the file was checked: exit %d:\n%s", code, out)
	}

	w = newSettingsWorld(t)
	file = filepath.Join(w.settings, "scratch.json")
	plant(t, "file", func() {
		if err := os.WriteFile(file+".new", fileBytes(t, file), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(file+".new", file); err != nil {
			t.Fatal(err)
		}
	})
	code, out, _ = w.run(t, w.runArgs(w.settings, "--check")...)
	if code != leaddaemon.ExitConfig || !strings.Contains(out, "FAIL  the settings file: "+w.settings+"/scratch.json changed while it was read") {
		t.Fatalf("a file replaced after it was checked: exit %d:\n%s", code, out)
	}
}

// --check on a world the daemon would start on: every line passes, it names
// what the daemon reads (the settings of the folder, the gate's identity and
// pins, the keys, the journal) and that owner/scratch.json is not read, and it
// ends 0 having bound, locked, made and started nothing; the state folder is
// the same, to its times. Without the root marker the same check ends 10 with
// that line failed.
func TestOwnerSettingsCheckPrintsWhatTheDaemonWouldRead(t *testing.T) {
	useSuite(t, cmdSuite{})
	noListen(t)
	w := newSettingsWorld(t)
	before := treeOf(t, w.state)
	code, out, msg := w.run(t, w.runArgs(w.settings, "--check")...)
	if code != 0 || msg != "" {
		t.Fatalf("--check: exit %d:\n%s%s", code, out, msg)
	}
	for _, want := range []string{
		"pass  the settings folder: " + w.settings + ", resolved to " + w.settings + ", opened by its handle",
		"pass  a private folder of the daemon's user: ",
		"pass  " + ruleTrial + ": ", "pass  " + ruleRoots + ": ", "pass  " + ruleLead + ": ", "pass  " + ruleSecret + ": ",
		"pass  the settings file: " + w.settings + "/scratch.json: a regular file of the daemon's user, mode 0600, one name",
		"pass  the settings: not a phone trial's",
		"pass  " + ruleNamed + ": is not, lies not in and holds not the drop folder, the mirror, the work folder or the sandbox template",
		"pass  the scratch roots, the drop folder and the mirror: ",
		"pass  the suite: ",
		"pass  the root marker: ",
		"pass  the seat gate: 127.0.0.1:7443, key fingerprint " + w.gateFP + ", 1 seat pin(s)",
		"pass  the socket paths: ",
		"read  " + w.settings + "/scratch.json: the owner channel's settings, read only",
		"read  " + filepath.Join(w.owner, "gate") + ": the gate's key, certificate and seat pins, read only, never written",
		"read  " + filepath.Join(w.owner, "keys.log") + " and keys.head",
		"open  " + filepath.Join(w.owner, "journal.db"),
		"skip  " + filepath.Join(w.owner, "scratch.json") + ": the state folder's own settings are not read",
		"check: no refusal: the daemon would read these and start; nothing was bound, locked, made or started\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--check does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("--check failed a line:\n%s", out)
	}
	if !sameTreeOf(before, treeOf(t, w.state)) {
		t.Error("--check changed the state folder")
	}
	for _, p := range []string{w.runtime, filepath.Join(w.owner, "genesis"), filepath.Join(w.owner, "records")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("--check made %s", p)
		}
	}
	w.sameTrial(t, "after --check")

	if err := os.Remove(filepath.Join(w.state, canaryroot.ScratchMarkerName)); err != nil {
		t.Fatal(err)
	}
	code, out, _ = w.run(t, w.runArgs(w.settings, "--check")...)
	if code != leaddaemon.ExitConfig || !strings.Contains(out, "FAIL  the root marker: ") || !strings.Contains(out, "check: 1 refusal(s)") {
		t.Fatalf("--check without the marker: exit %d:\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "pass  the settings: ") }) {
		t.Errorf("--check stopped at the marker:\n%s", out)
	}
}

// A bind mount of a guarded folder itself gives a second name that a path
// cannot show and the identities of the open folder and of the folders above
// it catch: a folder opened under the name of a place no guard names, whose
// identity is owner/ of the state folder or the gate's folder, or a folder
// that holds the gate's folder, is refused with "by another name". (A test
// cannot make a bind mount without privileges; the folder's name is set apart
// from its identity here instead, which is what a bind mount of the folder
// itself shows. A bind of a folder within a guard is not caught this way, as
// ".." from a mount's root leads above its mount point: the places of
// TestOwnerSettingsPlacesTheFolderInItsFilesystem catch it.)
func TestOwnerSettingsCatchesASecondNameOfAFolder(t *testing.T) {
	w := newSettingsWorld(t)
	guards := []settingsGuard{
		{ruleTrial, w.owner, "owner/ of the state folder, the trial's settings folder"},
		{ruleSecret, filepath.Join(w.owner, leaddaemon.SeatGateDirName), "the gate's identity and pins (owner/gate)"},
	}
	for _, c := range []struct {
		open string
		rule string
		want string
	}{
		{w.owner, ruleTrial, "is or lies in owner/ of the state folder, the trial's settings folder (" + w.owner + "), by another name (a bind mount)"},
		{filepath.Join(w.owner, leaddaemon.SeatGateDirName), ruleSecret, "is or lies in the gate's identity and pins (owner/gate) (" + filepath.Join(w.owner, "gate") + "), by another name (a bind mount)"},
		{w.state, ruleSecret, "holds the gate's identity and pins (owner/gate) (" + filepath.Join(w.owner, "gate") + "), by another name (a bind mount)"},
	} {
		d, err := openSettingsDir(c.open)
		if err != nil {
			t.Fatal(err)
		}
		// The folder as it would be seen through a bind mount elsewhere: its
		// name is a place no guard names; its identity is the folder's.
		d.given, d.real = "/srv/elsewhere/view", "/srv/elsewhere/view"
		rules := d.rules(guards)
		if err := rules[c.rule]; err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s seen by another name: %v, want %q", c.open, err, c.want)
		}
		d.f.Close()
	}
	// The proof's own folder by another name is still refused by nothing.
	d, err := openSettingsDir(w.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer d.f.Close()
	d.given, d.real = "/srv/elsewhere/view", "/srv/elsewhere/view"
	for r, err := range d.rules(guards) {
		if err != nil {
			t.Errorf("the proof's folder by another name fails %s: %v", r, err)
		}
	}
}

// The places of the folder and of the guards in their filesystems, from the
// daemon's mount table, catch what a path and an identity cannot: a bind
// mount of a folder within owner/ given as the folder (".." from the root of
// that mount leads above its mount point, so no identity above it is
// owner/'s), a bind mount of the settings folder into a lead's folder, and a
// bind mount of owner/ into the settings folder. A test cannot make a mount
// without privileges, so each mount is a line added to the daemon's own mount
// table, the input the check reads, with the root and the device the kernel
// would write; the folder of a bind given as the folder is opened by its own
// path and given the identities ".." of the mount's root would give. With the
// table as it is, the same folders pass.
func TestOwnerSettingsPlacesTheFolderInItsFilesystem(t *testing.T) {
	w := newSettingsWorld(t)
	lead := filepath.Join(w.state, "leads", "scratch-standin-1-lead")
	guards := []settingsGuard{
		{ruleTrial, w.owner, "owner/ of the state folder, the trial's settings folder"},
		{ruleLead, lead, "the lead standin-1: its folder"},
	}
	sub := filepath.Join(w.owner, "below")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	tbl, err := readMountTable()
	if err != nil {
		t.Fatal(err)
	}
	next := 0
	for _, m := range tbl {
		next = max(next, m.id+1)
	}
	// placed is the mount and the place of a real path, as the check finds
	// them.
	placed := func(p string) (mountEntry, fsPlace) {
		t.Helper()
		id, err := mountIDOfPath(p)
		if err != nil {
			t.Fatal(err)
		}
		m, ok := tbl.byID(id)
		if !ok {
			t.Fatalf("the mount %d of %s is not in the table", id, p)
		}
		pl, err := tbl.placeOf(id, p)
		if err != nil {
			t.Fatal(err)
		}
		return m, pl
	}
	with := func(e mountEntry) mountTable { return append(append(mountTable(nil), tbl...), e) }
	clean := func(t *testing.T, d *settingsDir) {
		t.Helper()
		for r, err := range d.rules(guards) {
			if err != nil {
				t.Errorf("%s with the table as it is fails %s: %v", d.given, r, err)
			}
		}
	}

	// owner/below bound at a place no guard names, given as the folder.
	m, subPl := placed(sub)
	view := "/srv/elsewhere/view"
	t1 := with(mountEntry{id: next, parent: m.id, dev: m.dev, root: subPl.path, point: view})
	d, err := openSettingsDir(sub)
	if err != nil {
		t.Fatal(err)
	}
	defer d.f.Close()
	d.given, d.real = view, view
	d.up = append([]fileID{d.id}, aboveIDs(view)...)
	if d.place, err = t1.placeOf(next, view); err != nil {
		t.Fatal(err)
	}
	d.mnt = t1
	if err := d.rules(guards)[ruleTrial]; err == nil || !strings.Contains(err.Error(), "is or lies in owner/ of the state folder, the trial's settings folder ("+w.owner+"), by another name: in its filesystem (device "+m.dev+")") {
		t.Errorf("the bind of owner/below given as the folder: %v", err)
	}

	// The settings folder bound into the lead's home: its place is its own,
	// but a mount shows it within the lead's folder.
	m, setPl := placed(w.settings)
	into := filepath.Join(lead, "home", "x")
	ds, err := openSettingsDir(w.settings)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.f.Close()
	clean(t, ds)
	ds.mnt = with(mountEntry{id: next, parent: m.id, dev: m.dev, root: setPl.path, point: into})
	if err := ds.rules(guards)[ruleLead]; err == nil || !strings.Contains(err.Error(), "is or lies in the lead standin-1: its folder ("+lead+"), by another name: a mount shows the folder, or a part of it, at "+into+", within "+lead) {
		t.Errorf("the settings folder bound into the lead's home: %v", err)
	}
	// A part of it is enough.
	ds.mnt = with(mountEntry{id: next, parent: m.id, dev: m.dev, root: setPl.path + "/part", point: into})
	if err := ds.rules(guards)[ruleLead]; err == nil || !strings.Contains(err.Error(), "a mount shows the folder, or a part of it, at "+into) {
		t.Errorf("a folder in the settings folder bound into the lead's home: %v", err)
	}

	// owner/ bound into the settings folder.
	m, ownPl := placed(w.owner)
	ds.mnt = with(mountEntry{id: next, parent: m.id, dev: m.dev, root: ownPl.path, point: filepath.Join(w.settings, "o")})
	if err := ds.rules(guards)[ruleTrial]; err == nil || !strings.Contains(err.Error(), "holds owner/ of the state folder, the trial's settings folder ("+w.owner+"), by another name: a mount shows the guard") {
		t.Errorf("owner/ bound into the settings folder: %v", err)
	}
	ds.mnt = tbl
	clean(t, ds)
}

// A runtime root so long that a holder's socket would not fit in a Unix
// socket address is a refusal of --check, made before the trial is stopped;
// the daemon itself would only meet it when a holder starts (review finding
// 8). The daemon's own socket still fits here.
func TestOwnerSettingsCheckFailsASocketPathThatDoesNotFit(t *testing.T) {
	useSuite(t, cmdSuite{})
	noListen(t)
	dir, err := filepath.EvalSymlinks(shortDir(t))
	if err != nil {
		t.Fatal(err)
	}
	// ROOT is DIR/F/proof, 80 bytes: its daemon socket (ROOT/runtime/
	// daemon.sock) fits, a holder's (ROOT/runtime/leads/
	// scratch-standin-1-lead/control.sock) does not.
	fill := 80 - len(dir) - len("/") - len("/proof")
	if fill < 1 {
		t.Skipf("the temporary folder %s is too long for this test", dir)
	}
	base := filepath.Join(dir, strings.Repeat("d", fill))
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	w := newSettingsWorldIn(t, base)
	code, out, msg := w.run(t, w.runArgs(w.settings, "--check")...)
	if code != leaddaemon.ExitConfig || msg != "" || !strings.Contains(out, "FAIL  the socket paths: the holder socket "+w.runtime+"/leads/scratch-standin-1-lead/") ||
		!strings.Contains(out, "over the 107 a Unix socket address holds") || !strings.Contains(out, "check: 1 refusal(s)") {
		t.Fatalf("--check with a long runtime root: exit %d:\n%s%s", code, out, msg)
	}
}
