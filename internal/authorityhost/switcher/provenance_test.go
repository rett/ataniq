package switcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// provenancePath is the import record of the release id.
func (e *env) provenancePath(id string) string {
	return filepath.Join(e.root, StateDir, ImportedDir, id)
}

// TestImportWritesProvenance: the import records the digest of the tree it
// made, and a switch back to it later, without --from, accepts it.
func TestImportWritesProvenance(t *testing.T) {
	e := active(t)
	in := e.incoming(rel3, "2026-11-01")
	if code := e.switchTo(rel3, from(in)); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	data, err := os.ReadFile(e.provenancePath(rel3))
	if err != nil {
		t.Fatal(err)
	}
	var p provenance
	if err := json.Unmarshal(data, &p); err != nil || p.ID != rel3 || !digestPattern.MatchString(p.Digest) || p.Files == 0 {
		t.Errorf("record: %s, %v", data, err)
	}
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("rollback: exit %d", code)
	}
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("rollback to the import: exit %d", code)
	}
}

// TestExistingReleaseNeedsProvenance: a folder in releases/ that the import
// did not make (no record), or that is no longer the tree it made (a file
// replaced by another inode of the same content, a file added and listed),
// is refused, whatever its sums say, by a switch, a dry run and a
// rollback; as the old release it is no fallback.
func TestExistingReleaseNeedsProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		spoil      func(e *env, dir string)
	}{
		{"no record", CodeProvenanceMissing, func(e *env, dir string) { os.Remove(e.provenancePath(filepath.Base(dir))) }},
		{"file replaced", CodeProvenanceMismatch, func(e *env, dir string) {
			p := filepath.Join(dir, "bin/app")
			before, err := os.Stat(p)
			if err != nil {
				e.t.Fatal(err)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				e.t.Fatal(err)
			}
			// Allocate the replacement while the original inode is still in use.
			if err := os.WriteFile(p+".new", data, before.Mode().Perm()); err != nil {
				e.t.Fatal(err)
			}
			if err := os.Rename(p+".new", p); err != nil {
				e.t.Fatal(err)
			}
			after, err := os.Stat(p)
			if err != nil {
				e.t.Fatal(err)
			}
			if os.SameFile(before, after) {
				e.t.Fatal("replacement still has the original inode")
			}
		}},
		{"file added", CodeProvenanceMismatch, func(e *env, dir string) {
			record, _ := os.ReadFile(e.provenancePath(filepath.Base(dir)))
			e.write(filepath.Join(dir, "etc/added"), "x\n", 0o644)
			e.sums(dir)
			e.write(e.provenancePath(filepath.Base(dir)), string(record), 0o644)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			dir := e.release(rel3, "2026-11-01")
			tc.spoil(e, dir)
			if code := e.switchTo(rel3, func(c *Config) { c.Dry = true }); code != ExitFailed || !strings.Contains(e.last, tc.code) {
				t.Errorf("dry run: exit %d\n%s", code, e.last)
			}
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			e.unchanged("releases/"+rel2, "verify")
			if rec := e.record(); rec.Error != tc.code || !strings.Contains(rec.Detail, "import it again with --from") {
				t.Errorf("record: %+v", rec)
			}
			e.ran(map[string]int{"ah-noaicheck": 0})
		})
	}
	t.Run("rollback", func(t *testing.T) {
		e := active(t)
		os.Remove(e.provenancePath(rel1))
		if code := e.rollBack("previous"); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if rec := e.record(); rec.Error != CodeProvenanceMissing {
			t.Errorf("record: %+v", rec)
		}
		e.links(rel2, rel1)
	})
	t.Run("fallback", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		os.Remove(e.provenancePath(rel2))
		e.runner.fail["health-probe"] = -1
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(e.last, "fallback: none") {
			t.Errorf("output:\n%s", e.last)
		}
		if rec := e.record(); rec.Result != ResultFailedRecovery || !rec.ServicesStopped {
			t.Errorf("record: %+v", rec)
		}
	})
	t.Run("record not trusted", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		os.Chmod(e.provenancePath(rel3), 0o666)
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if rec := e.record(); rec.Error != CodeUntrusted {
			t.Errorf("record: %+v", rec)
		}
	})
}
