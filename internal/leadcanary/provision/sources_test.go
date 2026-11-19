//go:build linux && ataniq_canary

package provision

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadcanary/standin"
)

// refusedUntouched runs Provision in a world that mod changed and requires an
// error that names the field, no change to the state root, no runtime root,
// and nothing of the settings file's content in the error or the log.
func refusedUntouched(t *testing.T, w *provisiontest.World, field string) {
	t.Helper()
	before := snapshot(t, w.State)
	lines, err := provisionWorld(w)
	if err == nil {
		t.Fatalf("accepted\n%q", lines)
	}
	if !strings.Contains(err.Error(), field) {
		t.Errorf("the error does not name %s: %v", field, err)
	}
	if strings.Contains(err.Error(), provisiontest.Secret) {
		t.Errorf("the error holds the content of a file: %v", err)
	}
	if len(lines) != 0 || snapshot(t, w.State) != before || exists(w.Runtime) {
		t.Errorf("a refused run made something: %q", lines)
	}
}

func TestTheManagedSettingsAndTheHookAreCheckedBeforeAnythingIsMade(t *testing.T) {
	type tweak func(t *testing.T, w *provisiontest.World, path *string)
	chmod := func(mode os.FileMode) tweak {
		return func(t *testing.T, w *provisiontest.World, path *string) {
			if err := os.Chmod(*path, mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	replace := func(make func(t *testing.T, path string)) tweak {
		return func(t *testing.T, w *provisiontest.World, path *string) {
			if err := os.Remove(*path); err != nil {
				t.Fatal(err)
			}
			make(t, *path)
		}
	}
	both := map[string]tweak{
		"group writable":  chmod(0o770),
		"others writable": chmod(0o707),
		"world writable":  chmod(0o777),
		"setuid":          chmod(os.ModeSetuid | 0o700),
		"setgid":          chmod(os.ModeSetgid | 0o700),
		"sticky":          chmod(os.ModeSticky | 0o700),
		"a symbolic link at the file": replace(func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target")
			provisiontest.WriteFile(t, target, []byte("x"), 0o700)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}),
		"a folder": replace(func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}),
		"a named pipe": replace(func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}),
		"empty": replace(func(t *testing.T, path string) { provisiontest.WriteFile(t, path, nil, 0o700) }),
		"a symbolic link in the folder above": func(t *testing.T, w *provisiontest.World, path *string) {
			dir := filepath.Join(filepath.Dir(*path), "linked")
			real := dir + "-real"
			if err := os.Mkdir(real, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(*path, filepath.Join(real, filepath.Base(*path))); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, dir); err != nil {
				t.Fatal(err)
			}
			*path = filepath.Join(dir, filepath.Base(*path))
			w.WriteConfig(t)
		},
		"missing": replace(func(t *testing.T, path string) {}),
	}
	for _, f := range []struct {
		field string
		path  func(w *provisiontest.World) *string
		// extra are the refusals that belong to this file only.
		extra map[string]tweak
	}{
		{"provision.managed_settings", func(w *provisiontest.World) *string { return &w.Cfg.Provision.ManagedSettings }, map[string]tweak{
			"larger than the bound": replace(func(t *testing.T, path string) {
				provisiontest.WriteFile(t, path, make([]byte, MaxManagedSettingsBytes+1), 0o600)
			}),
		}},
		{"provision.guard_hook", func(w *provisiontest.World) *string { return &w.Cfg.Provision.GuardHook }, map[string]tweak{
			"not executable by its owner": chmod(0o600),
			"larger than the bound": replace(func(t *testing.T, path string) {
				provisiontest.WriteFile(t, path, nil, 0o700)
				if err := os.Truncate(path, MaxGuardHookBytes+1); err != nil {
					t.Fatal(err)
				}
			}),
		}},
	} {
		cases := map[string]tweak{}
		for k, v := range both {
			cases[k] = v
		}
		for k, v := range f.extra {
			cases[k] = v
		}
		for name, tw := range cases {
			t.Run(f.field+"/"+name, func(t *testing.T) {
				w := provisiontest.New(t)
				tw(t, w, f.path(w))
				refusedUntouched(t, w, f.field)
			})
		}
	}
}

func TestAFileOfAnotherOwnerIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the caller is root: every file here is the caller's")
	}
	// /etc/passwd is a root-owned regular file every system has.
	for _, field := range []string{"provision.managed_settings", "provision.guard_hook"} {
		t.Run(field, func(t *testing.T) {
			w := provisiontest.New(t)
			if field == "provision.managed_settings" {
				w.Cfg.Provision.ManagedSettings = "/etc/passwd"
			} else {
				w.Cfg.Provision.GuardHook = "/usr/bin/true"
			}
			w.WriteConfig(t)
			refusedUntouched(t, w, field)
		})
	}
}

func TestFilesThatOthersCanReadAreAccepted(t *testing.T) {
	w := provisiontest.New(t)
	for _, c := range []struct {
		path string
		mode os.FileMode
	}{{w.Settings, 0o444}, {w.Hook, 0o755}} {
		if err := os.Chmod(c.path, c.mode); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provisionWorld(w); err != nil {
		t.Fatalf("a readable file was refused: %v", err)
	}
}

func TestTheReplayScreensAreCheckedBeforeAnythingIsMade(t *testing.T) {
	cases := map[string]func(t *testing.T, w *provisiontest.World){
		"2.txt is missing":  func(t *testing.T, w *provisiontest.World) { remove(t, filepath.Join(w.Screens, "2.txt")) },
		"1.json is missing": func(t *testing.T, w *provisiontest.World) { remove(t, filepath.Join(w.Screens, "1.json")) },
		"1.json is a folder": func(t *testing.T, w *provisiontest.World) {
			remove(t, filepath.Join(w.Screens, "1.json"))
			if err := os.Mkdir(filepath.Join(w.Screens, "1.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"2.json is a symbolic link": func(t *testing.T, w *provisiontest.World) {
			remove(t, filepath.Join(w.Screens, "2.json"))
			if err := os.Symlink(filepath.Join(w.Screens, "1.json"), filepath.Join(w.Screens, "2.json")); err != nil {
				t.Fatal(err)
			}
		},
		"1.txt is a named pipe": func(t *testing.T, w *provisiontest.World) {
			remove(t, filepath.Join(w.Screens, "1.txt"))
			if err := syscall.Mkfifo(filepath.Join(w.Screens, "1.txt"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"1.txt is too large": func(t *testing.T, w *provisiontest.World) {
			provisiontest.WriteFile(t, filepath.Join(w.Screens, "1.txt"), make([]byte, MaxReplayFileBytes+1), 0o600)
		},
		"the report is a symbolic link": func(t *testing.T, w *provisiontest.World) {
			if err := os.Symlink(filepath.Join(w.Screens, "1.json"), filepath.Join(w.Screens, standin.ReportFile)); err != nil {
				t.Fatal(err)
			}
		},
		"the report is too large": func(t *testing.T, w *provisiontest.World) {
			provisiontest.WriteFile(t, filepath.Join(w.Screens, standin.ReportFile), make([]byte, MaxReplayFileBytes+1), 0o600)
		},
		"the folder is a symbolic link": func(t *testing.T, w *provisiontest.World) {
			real := w.Screens + "-real"
			if err := os.Rename(w.Screens, real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, w.Screens); err != nil {
				t.Fatal(err)
			}
		},
		"the folder is missing": func(t *testing.T, w *provisiontest.World) {
			if err := os.RemoveAll(w.Screens); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			w := provisiontest.New(t)
			mod(t, w)
			refusedUntouched(t, w, "provision.replay_screens")
		})
	}
}

func TestAScreenOfTheLargestSizeIsCopied(t *testing.T) {
	w := provisiontest.New(t)
	provisiontest.WriteFile(t, filepath.Join(w.Screens, "1.txt"), make([]byte, MaxReplayFileBytes), 0o600)
	if _, err := provisionWorld(w); err != nil {
		t.Fatal(err)
	}
	p, _ := PathsOf(w.Cfg)
	if fi := lstat(t, filepath.Join(p.ReplayDir, "1.txt")); fi.Size() != MaxReplayFileBytes {
		t.Fatalf("size %d", fi.Size())
	}
}

func remove(t *testing.T, p string) {
	t.Helper()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
}
