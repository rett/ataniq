package switcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCounterBelowEvidence: a highest-counter that is present and well
// formed, but below what the host shows it used (restored from an older
// state: the links, the last switch deploy.json records as having used its
// counter), stops every switch before anything is recorded, so that the
// evidence stays for recover; a release folder of a higher counter refuses
// a forward switch below it.
func TestCounterBelowEvidence(t *testing.T) {
	t.Run("links", func(t *testing.T) {
		e := active(t)
		e.write(filepath.Join(e.root, StateDir, HighestFile), "1\n", 0o644)
		before := e.deployJSON()
		e.release(rel3, "2026-11-01")
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(e.last, "counter-invalid: highest-counter holds 1, below 2, the counter of current") || !strings.Contains(e.last, "nothing changed or recorded") {
			t.Errorf("output:\n%s", e.last)
		}
		if e.deployJSON() != before {
			t.Errorf("deploy.json was recorded over")
		}
		if code := e.rollBack("previous"); code != ExitFailed {
			t.Errorf("rollback: exit %d", code)
		}
		if got := e.status(); !strings.Contains(got, "highest counter: 1, below 2 (current)") {
			t.Errorf("status:\n%s", got)
		}
	})
	t.Run("deploy.json", func(t *testing.T) {
		e := active(t)
		// A switch to 0009 that used its counter and was rolled back; its
		// folder is gone and the counter restored from before it.
		e.release("0009-fffffff", "2026-11-01")
		e.runner.fail["health-probe"] = 30
		if code := e.switchTo("0009-fffffff"); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		e.links(rel2, rel1)
		makeWritable(e.relDir("0009-fffffff"))
		os.RemoveAll(e.relDir("0009-fffffff"))
		e.write(filepath.Join(e.root, StateDir, HighestFile), "2\n", 0o644)
		e.release(rel3, "2026-11-01")
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(e.last, "below 9, the counter of deploy.json") {
			t.Errorf("output:\n%s", e.last)
		}
		// The refusal kept the evidence: recover must be at least 9.
		if code := e.recover(RecoverOptions{SetCounter: true, Counter: 5}); code != ExitFailed || !strings.Contains(e.last, "below 9") {
			t.Fatalf("recover below: exit %d\n%s", code, e.last)
		}
		if code := e.recover(RecoverOptions{SetCounter: true, Counter: 9}); code != ExitOK {
			t.Fatalf("recover: exit %d", code)
		}
		if code := e.switchTo(rel3); code != ExitFailed || e.record().Error != CodeCounterNotAbove {
			t.Fatalf("switch below 9: exit %d, %+v", code, e.record())
		}
	})
	t.Run("release folder", func(t *testing.T) {
		e := active(t)
		e.release("0007-eeeeeee", "2026-11-01")
		// Its MANIFEST cannot be read: its name still counts.
		os.Remove(filepath.Join(e.relDir("0007-eeeeeee"), ManifestFile))
		e.release(rel3, "2026-11-01")
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if rec := e.record(); rec.Error != CodeCounterNotAbove || !strings.Contains(rec.Detail, "releases/0007-eeeeeee") {
			t.Errorf("record: %+v", rec)
		}
		// A MANIFEST whose counter is higher than its folder's name counts
		// too; a name that is no release id does not.
		e2 := active(t)
		dir := e2.release("0004-ddddddd", "2026-11-01")
		data, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
		e2.write(filepath.Join(dir, ManifestFile), strings.Replace(string(data), "counter: 4", "counter: 12", 1), 0o644)
		os.MkdirAll(filepath.Join(e2.root, BaseDir, "releases", ".import-0099-aaaaaaa"), 0o755)
		e2.release(rel3, "2026-11-01")
		if code := e2.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if rec := e2.record(); !strings.Contains(rec.Detail, "not above 12") {
			t.Errorf("record: %+v", rec)
		}
	})
	t.Run("recover", func(t *testing.T) {
		e := active(t)
		e.write(filepath.Join(e.root, StateDir, HighestFile), "1\n", 0o644)
		if code := e.recover(RecoverOptions{SetCounter: true, Counter: 1}); code != ExitFailed || !strings.Contains(e.last, "below 2, the counter of current") {
			t.Fatalf("recover below: exit %d\n%s", code, e.last)
		}
		if code := e.recover(RecoverOptions{SetCounter: true, Counter: 2}); code != ExitOK || e.highest() != "2" {
			t.Fatalf("recover: exit %d, %q", code, e.highest())
		}
		if code := e.recover(RecoverOptions{SetCounter: true, Counter: 3}); code != ExitFailed || !strings.Contains(e.last, "nothing to recover") {
			t.Fatalf("recover a counter that is right: exit %d", code)
		}
		e.release(rel3, "2026-11-01")
		if code := e.switchTo(rel3); code != ExitOK {
			t.Fatalf("switch: exit %d", code)
		}
	})
}

// TestCountersLog: every forward switch that uses its counter appends
// "counter id time" to counters.log, and the log is evidence of the
// counters attempted that no later record, rollback or pruned release
// folder erases: a highest-counter restored from an older state is refused
// against it, so the release of an attempted counter cannot come back.
func TestCountersLog(t *testing.T) {
	e := active(t)
	logPath := filepath.Join(e.root, StateDir, CountersLog)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "1 " + rel1 + " 2026-11-21T12:00:00Z\n2 " + rel2 + " 2026-11-21T12:00:00Z\n"
	if string(data) != want {
		t.Errorf("counters.log:\n%s\nwant\n%s", data, want)
	}
	if got := mode(t, logPath); got != 0o644 {
		t.Errorf("counters.log mode %v", got)
	}

	// rel3 is attempted and goes back; a rollback is recorded after it.
	e.release(rel3, "2026-11-01")
	e.runner.fail["health-probe"] = 30
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	delete(e.runner.fail, "health-probe")
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("rollback: exit %d", code)
	}
	e.links(rel1, rel2)
	// highest-counter restored from before rel3, and rel3 pruned: the
	// links and deploy.json show 2 at most.
	makeWritable(e.relDir(rel3))
	os.RemoveAll(e.relDir(rel3))
	os.Remove(filepath.Join(e.root, StateDir, ImportedDir, rel3))
	e.write(filepath.Join(e.root, StateDir, HighestFile), "2\n", 0o644)
	e.release(rel3, "2026-11-01")
	e.runner.reset()
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("a replayed counter: exit %d", code)
	}
	if !strings.Contains(e.last, "below 3, the counter of "+CountersLog) {
		t.Errorf("output:\n%s", e.last)
	}
	e.links(rel1, rel2)
	if len(e.runner.calls) != 0 {
		t.Errorf("commands run: %v", e.runner.calls)
	}
	if code := e.recover(RecoverOptions{SetCounter: true, Counter: 2}); code != ExitFailed {
		t.Errorf("recover below the log: exit %d", code)
	}
	if code := e.recover(RecoverOptions{SetCounter: true, Counter: 3}); code != ExitOK {
		t.Errorf("recover: exit %d", code)
	}
	if code := e.switchTo(rel3); code != ExitFailed || e.record().Error != CodeCounterNotAbove {
		t.Errorf("after recover: exit %d, %+v", code, e.record())
	}

	// A line cut short by a crash is not taken as an attempt and is cut
	// off before the next one is appended; any other line that does not
	// parse stops the switch.
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("9 0009-")
	f.Close()
	e.release("0004-ddddddd", "2026-11-01")
	if code := e.switchTo("0004-ddddddd"); code != ExitOK {
		t.Fatalf("after a torn line: exit %d", code)
	}
	data, _ = os.ReadFile(logPath)
	if lines := strings.Split(string(data), "\n"); len(lines) != 5 || lines[2] != "3 "+rel3+" 2026-11-21T12:00:00Z" || !strings.HasPrefix(lines[3], "4 0004-ddddddd 2026-11-21T") || lines[4] != "" {
		t.Errorf("counters.log:\n%s", data)
	}
	e.write(logPath, "x y z\n", 0o644)
	e.release("0005-eeeeeee", "2026-11-01")
	if code := e.switchTo("0005-eeeeeee"); code != ExitFailed || !strings.Contains(e.last, CountersLog) {
		t.Errorf("a broken log: exit %d\n%s", code, e.last)
	}
}
