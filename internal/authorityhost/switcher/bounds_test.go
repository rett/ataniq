package switcher

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestListBounds: a list holds at most MaxListCommands commands of at most
// MaxCommandArgs words.
func TestListBounds(t *testing.T) {
	ok := strings.Repeat("cmd\n", MaxListCommands)
	if cmds, err := ParseCommandList("list", []byte(ok), ""); err != nil || len(cmds) != MaxListCommands {
		t.Errorf("%d commands: %d, %v", MaxListCommands, len(cmds), err)
	}
	if _, err := ParseCommandList("list", []byte(ok+"cmd\n"), ""); err == nil || !strings.Contains(err.Error(), "more than 64 commands") {
		t.Errorf("%d commands: %v", MaxListCommands+1, err)
	}
	words := "cmd" + strings.Repeat(" a", MaxCommandArgs-1)
	if _, err := ParseCommandList("list", []byte(words+"\n"), ""); err != nil {
		t.Errorf("%d words: %v", MaxCommandArgs, err)
	}
	if _, err := ParseCommandList("list", []byte(words+" a\n"), ""); err == nil || !strings.Contains(err.Error(), "more than 32 words") {
		t.Errorf("%d words: %v", MaxCommandArgs+1, err)
	}
}

// TestSnapshotBounded: the package snapshot of a MANIFEST is 1 to 128 bytes
// of letters, digits and ._:-.
func TestSnapshotBounded(t *testing.T) {
	base := "id: 0003-ccccccc\ncounter: 3\ncommit: ccccccc0\nbuilt_on: dev\ngo_version: go\npackage_snapshot: "
	for _, tc := range []struct {
		snapshot string
		ok       bool
	}{
		{"2026-11-01", true},
		{"arch:2026.11.01_a-b", true},
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 129), false},
		{"a b", false},
		{"a/b", false},
		{"\x1b[2J", false},
	} {
		_, err := ParseManifest([]byte(base + tc.snapshot + "\n"))
		if (err == nil) != tc.ok {
			t.Errorf("snapshot %q: %v", tc.snapshot, err)
		}
	}
}

// TestHealthOneDeadline: the 60 seconds are one deadline for the whole
// health check: a slow command gets only what is left of it, and a round
// that passes after the deadline does not count.
func TestHealthOneDeadline(t *testing.T) {
	e := active(t)
	dir := e.release(rel3, "2026-11-01")
	e.write(filepath.Join(dir, HealthCmd), "slow-probe\nhealth-probe\n", 0o644)
	e.sums(dir)
	var limits []time.Duration
	var reloads []time.Time
	e.runner.hook = func(argv []string) {
		switch argv[0] {
		case "slow-probe":
			// Each run takes its whole time limit, as a hanging probe does.
			e.clock.sleep(HealthAttemptTimeout)
		case "reload-units":
			reloads = append(reloads, e.clock.now())
		}
	}
	e.runner.limits = &limits
	e.runner.fail["health-probe"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	// From the reload of rel3 to the reload of rel2 when going back: the
	// health check of rel3, within its window.
	if len(reloads) < 2 || reloads[1].Sub(reloads[0]) > HealthWindow {
		t.Errorf("reloads at %v: the health check of rel3 ran past its window", reloads)
	}
	// Every limit given to a probe fits what was left of the window.
	for _, l := range limits {
		if l <= 0 || l > HealthAttemptTimeout {
			t.Errorf("limit %s", l)
		}
	}
	if rec := e.record(); rec.Error != CodeHealthFailed {
		t.Errorf("record: %+v", rec)
	}

	// A round that passes only after the deadline is a failure.
	e = active(t)
	dir = e.release(rel3, "2026-11-01")
	e.write(filepath.Join(dir, HealthCmd), "slow-probe\n", 0o644)
	e.sums(dir)
	n := 0
	e.runner.hook = func(argv []string) {
		if argv[0] == "slow-probe" {
			n++
			e.clock.sleep(HealthWindow + time.Second)
		}
	}
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("a round past the deadline: exit %d", code)
	}
	if n != 1 {
		t.Errorf("slow-probe ran %d times", n)
	}
	if rec := e.record(); rec.Error != CodeHealthFailed || !strings.Contains(rec.Detail, "after the health window") {
		t.Errorf("record: %+v", rec)
	}
}

// TestBoundedOutput: a long command and a long detail are cut.
func TestBoundedOutput(t *testing.T) {
	argv := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		argv = append(argv, strings.Repeat("w", 100))
	}
	if got := qargv(argv); len(got) > maxArgvText+4 || !strings.HasSuffix(got, " ...") {
		t.Errorf("qargv: %d bytes", len(got))
	}
	if got := boundDetail(strings.Repeat("d", maxDetail+10)); len(got) != maxDetail+3 {
		t.Errorf("boundDetail: %d bytes", len(got))
	}
}

// TestStateWriteFitsItsReader: a state file larger than its reader's bound
// is never written.
func TestStateWriteFitsItsReader(t *testing.T) {
	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := writeJSONIn(r, "big.json", map[string]string{"x": strings.Repeat("x", maxStateFile)}, 0o600); err == nil {
		t.Errorf("an oversized state file was written")
	}
	if _, err := os.Stat(filepath.Join(dir, "big.json")); !os.IsNotExist(err) {
		t.Errorf("big.json: %v", err)
	}
	if err := writeJSONIn(r, "small.json", map[string]string{"x": "y"}, 0o600); err != nil {
		t.Errorf("small: %v", err)
	}
}

// TestCanonicalLinkTargets: current and previous must point at exactly
// releases/<id>; any other spelling, even one that cleans to a release, is
// refused, and a target is only ever shown escaped.
func TestCanonicalLinkTargets(t *testing.T) {
	for _, target := range []string{
		"releases/./" + rel2,
		"./releases/" + rel2,
		"releases//" + rel2,
		"releases/" + rel2 + "/",
		"", // set below to the absolute path
		"releases/" + rel2 + "\x1b[2J",
		"../authority-host/releases/" + rel2,
	} {
		t.Run(target, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-01")
			if target == "" {
				target = e.relDir(rel2)
			}
			cur := filepath.Join(e.root, BaseDir, "current")
			os.Remove(cur)
			if err := os.Symlink(target, cur); err != nil {
				t.Fatal(err)
			}
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			if rec := e.record(); rec.FailedStep != "state" || rec.Error != CodeStateInvalid {
				t.Errorf("record: %+v", rec)
			}
			if strings.Contains(e.last, "\x1b") || strings.Contains(e.deployJSON(), "\x1b") {
				t.Errorf("a control byte of a link target was passed on:\n%q", e.last)
			}
			if code := e.switcher().Status(); code != ExitFailed {
				t.Errorf("status: exit %d", code)
			}
			if strings.Contains(e.out.String(), "\x1b") {
				t.Errorf("status passed a control byte on")
			}
			e.out.Reset()
		})
	}
}

func TestEscapeLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`flag provided but not defined: -x`, `flag provided but not defined: -x`},
		{"bad \x1b[2J\nline\\", `bad \x1b[2J\x0aline\x5c`},
		{"\xc3\xa9", `\xc3\xa9`},
	} {
		if got := EscapeLine(tc.in); got != tc.want {
			t.Errorf("EscapeLine(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
	if got := EscapeLine(strings.Repeat("a", 2*maxLine)); len(got) != maxLine+3 {
		t.Errorf("long line: %d bytes", len(got))
	}
}

// TestStateFilesMustBeTrusted: the files of the state folder (the counter,
// deploy.json, the journal and the lock) must be regular files with one
// link, not writable by group or others, of an owner the owner check
// accepts, as the folder is; a migrated folder can hold files that are not.
func TestStateFilesMustBeTrusted(t *testing.T) {
	state := func(e *env, name string) string { return filepath.Join(e.root, StateDir, name) }
	for _, tc := range []struct {
		name  string
		setup func(e *env)
		mod   func(*Config)
	}{
		{"counter writable by its group", func(e *env) { os.Chmod(state(e, HighestFile), 0o664) }, nil},
		{"counter with a second link", func(e *env) { os.Link(state(e, HighestFile), filepath.Join(e.root, "counter-copy")) }, nil},
		{"deploy.json writable by others", func(e *env) { os.Chmod(state(e, DeployFile), 0o646) }, nil},
		{"deploy.json with a second link", func(e *env) { os.Link(state(e, DeployFile), filepath.Join(e.root, "deploy-copy")) }, nil},
		{"lock writable by others", func(e *env) { os.Chmod(state(e, LockFile), 0o606) }, nil},
		{"lock with a second link", func(e *env) { os.Link(state(e, LockFile), filepath.Join(e.root, "lock-copy")) }, nil},
		{"lock not empty", func(e *env) { os.WriteFile(state(e, LockFile), []byte(strings.Repeat("x", 100)), 0o600) }, nil},
		// A lock file others can read can be locked by them: flock needs
		// only a descriptor open for reading.
		{"lock readable by others", func(e *env) { os.Chmod(state(e, LockFile), 0o644) }, nil},
		{"lock readable by its group", func(e *env) { os.Chmod(state(e, LockFile), 0o640) }, nil},
		{"lock one byte", func(e *env) { os.WriteFile(state(e, LockFile), []byte("x"), 0o600) }, nil},
		{"lock not root's", func(e *env) {}, func(c *Config) {
			c.Owner = func(info fs.FileInfo) error {
				if info.Name() == LockFile {
					return errors.New("owned by uid 1000, not root")
				}
				return DefaultOwnerCheck(info)
			}
		}},
		{"counter not root's", func(e *env) {}, func(c *Config) {
			c.Owner = func(info fs.FileInfo) error {
				if info.Name() == HighestFile {
					return errors.New("owned by uid 1000, not root")
				}
				return DefaultOwnerCheck(info)
			}
		}},
		{"journal writable by its group", func(e *env) {
			e.crashAt(CrashPrevious, false, rel3)
			os.Chmod(state(e, JournalFile), 0o620)
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-01")
			tc.setup(e)
			e.runner.reset()
			cur, prev := e.link("current"), e.link("previous")
			mods := []func(*Config){}
			if tc.mod != nil {
				mods = append(mods, tc.mod)
			}
			if code := e.switchTo(rel3, mods...); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(e.last, "untrusted") {
				t.Errorf("output does not say why:\n%s", e.last)
			}
			if strings.HasPrefix(tc.name, "lock") && tc.name != "lock writable by others" && tc.mod == nil && !strings.Contains(e.last, "owner-only (mode 0600), empty and with one link") {
				t.Errorf("output does not say what the lock must be:\n%s", e.last)
			}
			if e.link("current") != cur || e.link("previous") != prev {
				t.Errorf("links moved: current -> %q, previous -> %q", e.link("current"), e.link("previous"))
			}
			if e.runner.called("reload-units") != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
		})
	}
}
