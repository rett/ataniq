package switcher

import (
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
