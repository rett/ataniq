package switcher

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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
	if want := preCall(filepath.Join(dir, NoAICheckProgram), e.root, dir); e.runner.calls[0] != want {
		t.Errorf("check command %q, want %q", e.runner.calls[0], want)
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
		{"user service", "user-service", func(e *env, in string) {
			e.write(filepath.Join(in, "etc/systemd/user/app.service"), "[Service]\n", 0o644)
			e.sums(in)
		}},
		{"user template drop-in", "user-service", func(e *env, in string) {
			e.write(filepath.Join(in, "etc/systemd/user/app@.service.d/10-start.conf"), "[Service]\n", 0o644)
			e.sums(in)
		}},
		{"user service type drop-in", "user-service", func(e *env, in string) {
			e.write(filepath.Join(in, "etc/systemd/user/service.d/env.conf"), "[Service]\n", 0o644)
			e.sums(in)
		}},
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
	if !strings.Contains(e.last, "step 0/9 import: would copy") {
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

// TestLimits: a release with more entries or bytes than the limits, a file
// larger than the limit, or an import and hashing that take longer than the
// budget fail with their own code, whether imported or already in
// releases/; the import's copy is removed and the lock is free afterwards.
func TestLimits(t *testing.T) {
	ticking := func(c *Config) {
		var n time.Duration
		start := c.Now()
		c.Now = func() time.Time { n += time.Minute; return start.Add(n) }
	}
	for _, tc := range []struct {
		name, code string
		mod        func(*Config)
	}{
		{"entries", CodeTooManyEntries, func(c *Config) { c.Limits.Entries = 5 }},
		{"bytes", CodeTooLarge, func(c *Config) { c.Limits.Bytes = 200 }},
		{"file bytes", CodeFileTooLarge, func(c *Config) { c.Limits.FileBytes = 64 }},
		{"time", CodeTimeBudget, ticking},
	} {
		for _, imported := range []bool{false, true} {
			name := tc.name + "/existing"
			if imported {
				name = tc.name + "/import"
			}
			t.Run(name, func(t *testing.T) {
				e := active(t)
				mods := []func(*Config){tc.mod}
				dir := e.relDir(rel3)
				if imported {
					dir = e.incoming(rel3, "2026-11-01")
					mods = append(mods, from(dir))
				} else {
					e.release(rel3, "2026-11-01")
				}
				e.write(filepath.Join(dir, "share/data"), strings.Repeat("x", 100), 0o644)
				e.sums(dir)
				if code := e.switchTo(rel3, mods...); code != ExitFailed {
					t.Fatalf("exit %d", code)
				}
				if rec := e.record(); rec.Error != tc.code {
					t.Errorf("record: %s: %s, want %s", rec.Error, rec.Detail, tc.code)
				}
				if got := e.link("current"); got != "releases/"+rel2 {
					t.Errorf("current -> %q", got)
				}
				if _, err := os.Lstat(filepath.Join(e.root, BaseDir, "releases", importPrefix+rel3)); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("the import's copy was left: %v", err)
				}
				// The lock was released: the same switch within the limits
				// passes.
				mods = mods[1:]
				if code := e.switchTo(rel3, mods...); code != ExitOK {
					t.Fatalf("within the limits: exit %d", code)
				}
			})
		}
	}
}

// TestImportChargesWrittenBytes: a file that is replaced or grows between
// the walk and the copy is refused, one that grows while it is copied is
// refused after the one byte more its read allows, and the bytes written
// are what is charged to the total: one byte below the release's size stops
// the copy. The partial copy is removed each time.
func TestImportChargesWrittenBytes(t *testing.T) {
	for _, tc := range []struct {
		name, stage, code string
		change            func(path string)
		less              int64
	}{
		// The replacement is made while the file it replaces is still
		// there (and held open), then renamed over it: an inode number
		// freed first could be given to the new file at once.
		{"replaced", "open", CodeFileChanged, func(path string) {
			old, _ := os.Open(path)
			defer old.Close()
			data, _ := os.ReadFile(path)
			os.WriteFile(path+".new", data, 0o644)
			os.Rename(path+".new", path)
		}, 0},
		{"grown before the copy", "open", CodeFileChanged, func(path string) {
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("more")
			f.Close()
		}, 0},
		{"grown while copied", "copy", CodeFileChanged, func(path string) {
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString(strings.Repeat("y", 4096))
			f.Close()
		}, 0},
		{"written bytes over the total", "", CodeTooLarge, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			in := e.incoming(rel3, "2026-11-01")
			e.write(filepath.Join(in, "share/data"), strings.Repeat("x", 100), 0o644)
			e.sums(in)
			var total int64
			filepath.WalkDir(in, func(path string, d fs.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() {
					info, _ := d.Info()
					total += info.Size()
				}
				return nil
			})
			importHook = func(stage, rel string) {
				if tc.change != nil && stage == tc.stage && rel == "share/data" {
					tc.change(filepath.Join(in, rel))
				}
			}
			t.Cleanup(func() { importHook = nil })
			// The bytes of the release as it was listed, less tc.less.
			limits := func(c *Config) { c.Limits.Bytes = total - tc.less }
			if code := e.switchTo(rel3, from(in), limits); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			if rec := e.record(); rec.Error != tc.code {
				t.Errorf("record: %s: %s, want %s", rec.Error, rec.Detail, tc.code)
			}
			if _, err := os.Lstat(filepath.Join(e.root, BaseDir, "releases", importPrefix+rel3)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the partial copy was left: %v", err)
			}
			if got := e.link("current"); got != "releases/"+rel2 {
				t.Errorf("current -> %q", got)
			}
		})
	}
}

// TestImportNeverDeletesItsInput: an incoming folder that is, lies beneath
// or contains the folder of releases/ (a staging folder of an import, a
// release, releases/ itself, also through a symbolic link of a folder
// above it) is refused before anything is created or removed: the
// incoming files stay as they were and releases/ holds what it held.
func TestImportNeverDeletesItsInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		// in returns the incoming folder, written below e.root.
		in func(e *env) string
	}{
		{"the staging name of the id", func(e *env) string {
			return e.releaseIn(filepath.Join(e.root, BaseDir, "releases", importPrefix+rel3), rel3, "2026-11-01")
		}},
		{"beneath a staging folder", func(e *env) string {
			return e.releaseIn(filepath.Join(e.root, BaseDir, "releases", importPrefix+rel3+"-0123", "inner", rel3), rel3, "2026-11-01")
		}},
		{"beneath a release", func(e *env) string {
			return e.releaseIn(filepath.Join(e.relDir(rel1), "incoming", rel3), rel3, "2026-11-01")
		}},
		{"through a link to a staging folder", func(e *env) string {
			held := filepath.Join(e.root, BaseDir, "releases", importPrefix+rel3)
			e.releaseIn(filepath.Join(held, rel3), rel3, "2026-11-01")
			link := filepath.Join(e.t.TempDir(), "link")
			if err := os.Symlink(held, link); err != nil {
				e.t.Fatal(err)
			}
			return filepath.Join(link, rel3)
		}},
		{"containing releases", func(e *env) string {
			return filepath.Join(e.root, BaseDir)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			in := tc.in(e)
			releases := filepath.Join(e.root, BaseDir, "releases")
			names := func() string {
				entries, _ := os.ReadDir(releases)
				var n []string
				for _, ent := range entries {
					n = append(n, ent.Name())
				}
				return strings.Join(n, " ")
			}
			before, list := digest(t, in), names()
			if code := e.switchTo(rel3, from(in)); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			if rec := e.record(); rec.Error != CodeIncomingOverlaps || rec.FailedStep != "import" {
				t.Errorf("record: %s at %s: %s", rec.Error, rec.FailedStep, rec.Detail)
			}
			if _, err := os.Stat(in); err != nil {
				t.Fatalf("the incoming folder is gone: %v", err)
			}
			if digest(t, in) != before {
				t.Errorf("the incoming folder changed")
			}
			if got := names(); got != list {
				t.Errorf("releases/ holds %s, want %s", got, list)
			}
			if got := e.link("current"); got != "releases/"+rel2 {
				t.Errorf("current -> %q", got)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
		})
	}
}

// TestImportStagingIsItsOwn: an import stages in a folder of its own,
// made new for it, and removes only that one: a folder an earlier import
// left (under the old fixed name or any other .import- name) is neither
// used nor removed, and the switch says it is there.
func TestImportStagingIsItsOwn(t *testing.T) {
	e := active(t)
	releases := filepath.Join(e.root, BaseDir, "releases")
	left := []string{importPrefix + rel3, importPrefix + rel3 + "-feedc0de"}
	for _, name := range left {
		e.write(filepath.Join(releases, name, "kept"), "kept\n", 0o644)
	}
	var staged []string
	importHook = func(stage, rel string) {
		if stage == "open" && rel == ManifestFile {
			entries, _ := os.ReadDir(releases)
			for _, ent := range entries {
				if strings.HasPrefix(ent.Name(), importPrefix) && ent.Name() != left[0] && ent.Name() != left[1] {
					staged = append(staged, ent.Name())
				}
			}
		}
	}
	t.Cleanup(func() { importHook = nil })
	e.runner.fail["validate-rules"] = 1
	in := e.incoming(rel3, "2026-11-01")
	if code := e.switchTo(rel3, from(in)); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if len(staged) != 1 || !strings.HasPrefix(staged[0], importPrefix+rel3+"-") {
		t.Fatalf("staging folders seen during the copy: %v", staged)
	}
	if _, err := os.Lstat(filepath.Join(releases, staged[0])); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the import's own staging folder was left: %v", err)
	}
	for _, name := range left {
		if data, err := os.ReadFile(filepath.Join(releases, name, "kept")); err != nil || string(data) != "kept\n" {
			t.Errorf("%s was touched: %v", name, err)
		}
		if !strings.Contains(e.last, "releases/"+name+" was left by an earlier import") {
			t.Errorf("%s not reported:\n%s", name, e.last)
		}
	}
	// A failing import removes its own staging folder only.
	in4 := e.incoming("0004-ddddddd", "2026-11-01")
	e.write(filepath.Join(in4, "etc/extra"), "x", 0o644)
	if code := e.switchTo("0004-ddddddd", from(in4)); code != ExitFailed || e.record().Error != CodeFileNotListed {
		t.Fatalf("exit %d, %+v", code, e.record())
	}
	entries, _ := os.ReadDir(releases)
	var got []string
	for _, ent := range entries {
		got = append(got, ent.Name())
	}
	if want := strings.Join(append(append([]string{}, left...), rel1, rel2, rel3), " "); strings.Join(got, " ") != want {
		t.Errorf("releases/ holds %v, want %s", got, want)
	}
}

// swapForFifo is an incomingHook that replaces the incoming folder named
// by from with a FIFO nobody writes to, as an unprivileged owner could do
// between the switch's look at it and its open. It returns the path of the
// FIFO. The blocked open of a switch that waits on it is released when the
// test ends, so that a failing run does not leave it hanging.
func swapForFifo(t *testing.T) {
	t.Helper()
	incomingHook = func(from string) {
		if err := os.Rename(from, from+".gone"); err != nil {
			t.Error(err)
			return
		}
		if err := syscall.Mkfifo(from, 0o644); err != nil {
			t.Error(err)
			return
		}
		t.Cleanup(func() {
			// Wakes an open that waits for a writer; refused (ENXIO) when
			// nothing waits.
			if f, err := os.OpenFile(from, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				f.Close()
			}
		})
	}
	t.Cleanup(func() { incomingHook = nil })
}

// TestIncomingSwappedForFifo: the incoming folder replaced by a FIFO
// without a writer between the switch's look at it and its open is
// refused at once, with the code of a path that is not a folder, and
// never waited on: the open is O_DIRECTORY, O_NOFOLLOW and O_NONBLOCK, and
// the root is made from that checked descriptor, so there is no second
// open that could block with the lock held.
func TestIncomingSwappedForFifo(t *testing.T) {
	t.Run("openIncoming", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "incoming")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		swapForFifo(t)
		done := make(chan error, 1)
		go func() {
			r, err := openIncoming(dir)
			if r != nil {
				r.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("openIncoming accepted it")
			}
			if code, _ := codeOf(err); code != CodeReleaseNotFolder {
				t.Errorf("err %v, want code %s", err, CodeReleaseNotFolder)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("openIncoming waits on a FIFO")
		}
	})
	t.Run("a switch", func(t *testing.T) {
		e := active(t)
		in := e.incoming(rel3, "2026-11-01")
		swapForFifo(t)
		done := make(chan int, 1)
		go func() { done <- e.switcher(from(in)).Switch(context.Background(), rel3) }()
		select {
		case code := <-done:
			if code != ExitFailed {
				t.Fatalf("exit %d\n%s", code, e.out.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the switch waits on a FIFO with the lock held")
		}
		e.out.Reset()
		if rec := e.record(); rec.Error != CodeReleaseNotFolder {
			t.Errorf("record: %s: %s", rec.Error, rec.Detail)
		}
		e.links(rel2, rel1)
		if _, err := os.Lstat(e.relDir(rel3)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a release folder was made: %v", err)
		}
		// The lock is free: another command runs.
		incomingHook = nil
		if code := e.switcher().Status(); code != ExitOK {
			t.Errorf("status after the refusal: exit %d", code)
		}
	})
	t.Run("a FIFO named as the incoming folder", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "incoming")
		if err := syscall.Mkfifo(dir, 0o644); err != nil {
			t.Skip(err)
		}
		done := make(chan error, 1)
		go func() {
			r, err := openIncoming(dir)
			if r != nil {
				r.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("openIncoming accepted it")
			}
			if code, _ := codeOf(err); code != CodeReleaseNotFolder {
				t.Errorf("err %v, want code %s", err, CodeReleaseNotFolder)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("openIncoming waits on a FIFO")
		}
	})
	t.Run("swapped for a link", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "incoming")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		incomingHook = func(from string) {
			os.Rename(from, from+".gone")
			os.Symlink(from+".gone", from)
		}
		t.Cleanup(func() { incomingHook = nil })
		r, err := openIncoming(dir)
		if r != nil {
			r.Close()
		}
		if err == nil {
			t.Fatalf("openIncoming accepted it")
		}
		if code, _ := codeOf(err); code != CodeReleaseNotFolder {
			t.Errorf("err %v, want code %s", err, CodeReleaseNotFolder)
		}
	})
	t.Run("swapped for another folder", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "incoming")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		incomingHook = func(from string) {
			os.Rename(from, from+".gone")
			os.Mkdir(from, 0o755)
		}
		t.Cleanup(func() { incomingHook = nil })
		r, err := openIncoming(dir)
		if r != nil {
			r.Close()
		}
		if err == nil {
			t.Fatalf("openIncoming accepted it")
		}
		if code, _ := codeOf(err); code != CodeFileChanged {
			t.Errorf("err %v, want code %s", err, CodeFileChanged)
		}
	})
}
