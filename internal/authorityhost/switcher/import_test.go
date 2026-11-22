package switcher

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func from(dir string) func(*Config) { return func(c *Config) { c.From = dir } }

// TestImport: --from copies the incoming folder into releases/<id> as new,
// read-only inodes and switches to that copy.
func TestImport(t *testing.T) {
	e := active(t)
	in := e.incoming(rel3, "2026-11-01")
	// Modes the import must not carry over.
	e.write(filepath.Join(in, "bin/odd"), "x\n", 0o640)
	os.Chmod(filepath.Join(in, "bin/odd"), 0o640|fs.ModeSetgid)
	os.Chmod(filepath.Join(in, "bin/app"), 0o4710)
	os.Chmod(filepath.Join(in, "etc"), 0o777)
	e.sums(in)
	before := digest(t, in)
	if code := e.switchTo(rel3, from(in)); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if got := e.link("current"); got != "releases/"+rel3 {
		t.Errorf("current -> %q", got)
	}
	dir := e.relDir(rel3)
	for path, want := range map[string]fs.FileMode{
		".":              0o555,
		"etc":            0o555,
		"bin":            0o555,
		"bin/app":        0o555,
		"bin/odd":        0o444,
		ManifestFile:     0o444,
		HealthCmd:        0o444,
		NoAICheckProgram: 0o555,
	} {
		if got := mode(t, filepath.Join(dir, path)); got != want {
			t.Errorf("%s: mode %v, want %v", path, got, want)
		}
	}
	// New inodes: the incoming folder can change without touching the
	// release.
	a, _ := os.Stat(filepath.Join(in, "bin/app"))
	b, _ := os.Stat(filepath.Join(dir, "bin/app"))
	if os.SameFile(a, b) {
		t.Errorf("bin/app is the incoming inode")
	}
	if digest(t, in) != before {
		t.Errorf("the import changed the incoming folder")
	}
	os.WriteFile(filepath.Join(in, "bin/app"), []byte("changed afterwards\n"), 0o755)
	if data, _ := os.ReadFile(filepath.Join(dir, "bin/app")); string(data) != "app of "+rel3+"\n" {
		t.Errorf("the release changed with the incoming folder: %q", data)
	}
	// The checks and programs ran on the copy.
	if want := filepath.Join(dir, NoAICheckProgram) + " --release " + dir; e.runner.calls[0] != want {
		t.Errorf("first command %q, want %q", e.runner.calls[0], want)
	}
	if _, err := os.Lstat(filepath.Join(e.root, BaseDir, "releases", importPrefix+rel3)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the import folder was left: %v", err)
	}
	// The imported release passes the check of an imported release: a
	// switch back to it later reads it without --from.
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("rollback: exit %d", code)
	}
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("rollback to the imported release: exit %d", code)
	}
}

// TestImportRefusals: what the incoming folder may not hold, and an id that
// is already imported. Nothing is left in releases/ and current stays.
func TestImportRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		setup      func(e *env, in string)
	}{
		{"symbolic link", CodeBadEntry, func(e *env, in string) {
			os.Symlink("/etc/passwd", filepath.Join(in, "etc/link"))
		}},
		{"fifo", CodeBadEntry, func(e *env, in string) {
			if err := syscall.Mkfifo(filepath.Join(in, "etc/fifo"), 0o644); err != nil {
				e.t.Skip(err)
			}
		}},
		{"hard link", CodeBadEntry, func(e *env, in string) {
			if err := os.Link(filepath.Join(in, "bin/app"), filepath.Join(in, "bin/app2")); err != nil {
				e.t.Skip(err)
			}
			e.sums(in)
		}},
		{"wrong sum", CodeSumsMismatch, func(e *env, in string) {
			os.WriteFile(filepath.Join(in, "bin/app"), []byte("changed\n"), 0o755)
		}},
		{"not listed", CodeFileNotListed, func(e *env, in string) {
			e.write(filepath.Join(in, "etc/extra"), "x", 0o644)
		}},
		{"incoming a link", CodeReleaseNotFolder, func(e *env, in string) {
			real := in + ".real"
			os.Rename(in, real)
			os.Symlink(real, in)
		}},
		{"already imported", CodeReleaseExists, func(e *env, in string) {
			e.release(rel3, "2026-11-01")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			in := e.incoming(rel3, "2026-11-01")
			tc.setup(e, in)
			existed := false
			if _, err := os.Lstat(e.relDir(rel3)); err == nil {
				existed = true
			}
			if code := e.switchTo(rel3, from(in)); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			rec := e.record()
			if rec.Error != tc.code {
				t.Errorf("record: %s: %s, want %s", rec.Error, rec.Detail, tc.code)
			}
			if got := e.link("current"); got != "releases/"+rel2 {
				t.Errorf("current -> %q", got)
			}
			entries, _ := os.ReadDir(filepath.Join(e.root, BaseDir, "releases"))
			var names []string
			for _, ent := range entries {
				names = append(names, ent.Name())
			}
			want := []string{rel1, rel2}
			if existed {
				want = append(want, rel3)
			}
			if strings.Join(names, " ") != strings.Join(want, " ") {
				t.Errorf("releases/ holds %v, want %v", names, want)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
		})
	}
}

// TestImportedReleaseKeptAfterLaterFailure: a release that was imported and
// verified stays imported when a later step fails, and is switched to
// without --from after that.
func TestImportedReleaseKeptAfterLaterFailure(t *testing.T) {
	e := active(t)
	in := e.incoming(rel3, "2026-11-01")
	e.runner.fail["validate-rules"] = 1
	if code := e.switchTo(rel3, from(in)); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "validate")
	if code := e.switchTo(rel3, from(in)); code != ExitFailed || e.record().Error != CodeReleaseExists {
		t.Fatalf("second import: exit %d, %+v", code, e.record())
	}
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("switch without --from: exit %d", code)
	}
}

// TestImportDry: a dry run with --from checks the incoming folder and
// imports nothing.
func TestImportDry(t *testing.T) {
	e := active(t)
	in := e.incoming(rel3, "2026-11-01")
	os.Chmod(filepath.Join(in, "etc"), 0o777) // the import would fix this
	before := digest(t, e.root)
	if code := e.switchTo(rel3, from(in), func(c *Config) { c.Dry = true }); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if digest(t, e.root) != before {
		t.Errorf("a dry run changed the root")
	}
	if !strings.Contains(e.last, "step 0/8 import: would copy") {
		t.Errorf("output:\n%s", e.last)
	}
	os.WriteFile(filepath.Join(in, "bin/app"), []byte("changed\n"), 0o755)
	if code := e.switchTo(rel3, from(in), func(c *Config) { c.Dry = true }); code != ExitFailed {
		t.Fatalf("wrong sum, dry: exit %d", code)
	}
}

// TestExistingReleaseMustBeTrusted: without --from the release folder must
// be as an import leaves it: nothing writable by group or others, no set-id
// bit, an owner the owner check accepts.
func TestExistingReleaseMustBeTrusted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *env, dir string)
		mod   func(*Config)
	}{
		{"folder writable by others", func(e *env, dir string) { os.Chmod(filepath.Join(dir, "etc"), 0o777) }, nil},
		{"release writable by its group", func(e *env, dir string) { os.Chmod(dir, 0o775) }, nil},
		{"file writable by others", func(e *env, dir string) { os.Chmod(filepath.Join(dir, "bin/app"), 0o757) }, nil},
		{"set-user-id file", func(e *env, dir string) { os.Chmod(filepath.Join(dir, "bin/app"), 0o755|fs.ModeSetuid) }, nil},
		{"owner refused", func(e *env, dir string) {}, func(c *Config) {
			c.Owner = func(info fs.FileInfo) error {
				if info.Name() == "app" {
					return errors.New("owned by uid 1000, not root")
				}
				return DefaultOwnerCheck(info)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			dir := e.release(rel3, "2026-11-01")
			tc.setup(e, dir)
			mods := []func(*Config){}
			if tc.mod != nil {
				mods = append(mods, tc.mod)
			}
			if code := e.switchTo(rel3, mods...); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			e.unchanged("releases/"+rel2, "verify")
			if rec := e.record(); rec.Error != CodeUntrusted {
				t.Errorf("record: %s: %s", rec.Error, rec.Detail)
			}
		})
	}
}
