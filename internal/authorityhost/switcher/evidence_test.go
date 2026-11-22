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
