package switcher

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestListProgramsResolve: a program of a release's lists is a file of the
// verified release, written {release}/..., in any of its lists; a bare
// name; or an absolute path of a protected host program (below the root),
// which runs as that path below the root. Anything else is refused when
// the release is verified, before anything runs: a program in a folder
// that is not root's (a deployment workspace, a home) or that others can
// write (/tmp), a link, a second link, a missing file, or a {release}
// path that is not a file of the release.
func TestListProgramsResolve(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		e := active(t)
		dir := e.release(rel3, "2026-11-01")
		makeWritable(dir)
		e.write(filepath.Join(dir, "bin/health"), "#!/bin/sh\nexit 0\n", 0o755)
		e.write(filepath.Join(dir, "bin/stop"), "#!/bin/sh\nexit 0\n", 0o755)
		e.write(filepath.Join(e.root, "usr/bin/reloader"), "#!/bin/sh\nexit 0\n", 0o755)
		e.write(filepath.Join(dir, ReloadList), "/usr/bin/reloader --all\n", 0o644)
		e.write(filepath.Join(dir, HealthCmd), "{release}/bin/health --ready\n", 0o644)
		e.write(filepath.Join(dir, StopList), "{release}/bin/stop\n", 0o644)
		e.sums(dir)
		if code := e.switchTo(rel3); code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		calls := strings.Join(e.runner.calls, "\n")
		for _, want := range []string{
			filepath.Join(e.root, "usr/bin/reloader") + " --all",
			filepath.Join(dir, "bin/health") + " --ready",
		} {
			if !strings.Contains(calls, want) {
				t.Errorf("no call %q in:\n%s", want, calls)
			}
		}
	})
	for _, tc := range []struct {
		name  string
		prog  string // the program of health.cmd
		setup func(e *env)
		mod   func(*Config)
	}{
		{"a workspace that is not root's", "/home/deploy/ws/health", func(e *env) {
			e.write(filepath.Join(e.root, "home/deploy/ws/health"), "#!/bin/sh\nexit 0\n", 0o755)
		}, func(c *Config) {
			c.Owner = func(info fs.FileInfo) error {
				if info.Name() == "deploy" {
					return errors.New("owned by uid 1000, not root")
				}
				return DefaultOwnerCheck(info)
			}
		}},
		{"a folder others can write", "/tmp/health", func(e *env) {
			e.write(filepath.Join(e.root, "tmp/health"), "#!/bin/sh\nexit 0\n", 0o755)
			os.Chmod(filepath.Join(e.root, "tmp"), 0o1777)
		}, nil},
		{"a program others can write", "/usr/bin/health", func(e *env) {
			e.write(filepath.Join(e.root, "usr/bin/health"), "#!/bin/sh\nexit 0\n", 0o755)
			os.Chmod(filepath.Join(e.root, "usr/bin/health"), 0o757)
		}, nil},
		{"a link", "/usr/bin/health", func(e *env) {
			e.write(filepath.Join(e.root, "usr/bin/real"), "#!/bin/sh\nexit 0\n", 0o755)
			os.Symlink("real", filepath.Join(e.root, "usr/bin/health"))
		}, nil},
		{"a second link", "/usr/bin/health", func(e *env) {
			e.write(filepath.Join(e.root, "usr/bin/health"), "#!/bin/sh\nexit 0\n", 0o755)
			os.Link(filepath.Join(e.root, "usr/bin/health"), filepath.Join(e.root, "usr/bin/other"))
		}, nil},
		{"not executable", "/usr/bin/health", func(e *env) {
			e.write(filepath.Join(e.root, "usr/bin/health"), "#!/bin/sh\nexit 0\n", 0o644)
		}, nil},
		{"missing", "/usr/bin/health", func(e *env) {}, nil},
		{"not in the release", "{release}/bin/health", func(e *env) {}, nil},
		{"out of the release", "{release}/../" + rel2 + "/bin/app", func(e *env) {}, nil},
		{"the placeholder inside a name", "/x{release}/bin/app", func(e *env) {}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			dir := e.release(rel3, "2026-11-01")
			tc.setup(e)
			makeWritable(dir)
			e.write(filepath.Join(dir, HealthCmd), tc.prog+"\n", 0o644)
			e.sums(dir)
			var mods []func(*Config)
			if tc.mod != nil {
				mods = append(mods, tc.mod)
			}
			if code := e.switchTo(rel3, mods...); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			e.unchanged("releases/"+rel2, "verify")
			if rec := e.record(); rec.Error != CodeListInvalid || !strings.Contains(rec.Detail, HealthCmd) {
				t.Errorf("record: %s: %s", rec.Error, rec.Detail)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
		})
	}
}

// TestNoAICheckOverrideRunsAPrivateCopy: the --noaicheck program runs from
// a private copy made from the descriptor that was checked and hashed
// again (a file of mode 0700 in a folder of mode 0700, removed after the
// run): an override replaced by another program while the switch runs
// the firewall listing (or anything else) does not change what runs.
func TestNoAICheckOverrideRunsAPrivateCopy(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	check := filepath.Join(e.root, override)
	original := "#!/bin/sh\nexit 0\n"
	e.write(check, original, 0o755)
	type run struct{ path, content, mode, dirMode string }
	var runs []run
	e.runner.hook = func(argv []string) {
		switch filepath.Base(argv[0]) {
		case "nft":
			// An ordinary replacement, renamed over the checked program.
			e.write(check+".new", "#!/bin/sh\nexit 0 # replaced\n", 0o755)
			os.Rename(check+".new", check)
		case "other-check":
			data, _ := os.ReadFile(argv[0])
			fi, _ := os.Stat(argv[0])
			di, _ := os.Stat(filepath.Dir(argv[0]))
			r := run{path: argv[0], content: string(data)}
			if fi != nil && di != nil {
				r.mode, r.dirMode = fi.Mode().String(), di.Mode().String()
			}
			runs = append(runs, r)
		}
	}
	if code := e.switchTo(rel3, overrideCheck); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if len(runs) != 2 {
		t.Fatalf("runs of the check: %+v", runs)
	}
	for _, r := range runs {
		if r.path == check || r.content != original || r.mode != "-rwx------" || r.dirMode != "drwx------" {
			t.Errorf("ran %+v, want a private copy of the checked program", r)
		}
		if _, err := os.Stat(r.path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the copy %s was left: %v", r.path, err)
		}
	}
}
