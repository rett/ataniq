package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// makeBundle writes a git bundle of the lead's branch at path. A fetch of a
// bundle file by the owner's git needs no upload-pack.
func (e *fetchEnv) makeBundle(t *testing.T, path string) {
	t.Helper()
	e.lead.git(t, "bundle", "create", path, "main")
}

// TestFetchRefusesARepositoryPathThatExists: the plan's repository path is
// given to the owner's git, which reads a bundle file there itself. A path
// that exists on the host is refused before anything starts: no sandboxed
// command, no measure and no git of the mirror.
func TestFetchRefusesARepositoryPathThatExists(t *testing.T) {
	cases := []struct {
		name string
		make func(t *testing.T, e *fetchEnv, dir string) string
		kind string
	}{
		{"a bundle file", func(t *testing.T, e *fetchEnv, dir string) string {
			p := filepath.Join(dir, "work")
			e.makeBundle(t, p)
			return p
		}, "a regular file"},
		{"a regular file that is not a bundle", func(t *testing.T, e *fetchEnv, dir string) string {
			p := filepath.Join(dir, "work")
			if err := os.WriteFile(p, []byte("not a bundle\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, "a regular file"},
		{"an empty file", func(t *testing.T, e *fetchEnv, dir string) string {
			p := filepath.Join(dir, "work")
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}, "a regular file"},
		{"a symlink to a bundle file", func(t *testing.T, e *fetchEnv, dir string) string {
			b := filepath.Join(dir, "elsewhere.bundle")
			e.makeBundle(t, b)
			p := filepath.Join(dir, "work")
			if err := os.Symlink(b, p); err != nil {
				t.Fatal(err)
			}
			return p
		}, "a symbolic link"},
		{"a symlink to a folder", func(t *testing.T, e *fetchEnv, dir string) string {
			p := filepath.Join(dir, "work")
			if err := os.Symlink(e.lead.tree, p); err != nil {
				t.Fatal(err)
			}
			return p
		}, "a symbolic link"},
		{"a dangling symlink", func(t *testing.T, e *fetchEnv, dir string) string {
			p := filepath.Join(dir, "work")
			if err := os.Symlink(filepath.Join(dir, "nothing"), p); err != nil {
				t.Fatal(err)
			}
			return p
		}, "a symbolic link"},
		{"the mirror itself, a bare repository", func(t *testing.T, e *fetchEnv, dir string) string {
			return e.m.Path()
		}, "a folder"},
		{"a bare repository that is not the mirror", func(t *testing.T, e *fetchEnv, dir string) string {
			p := filepath.Join(dir, "work")
			fixtureGit(t, e.g, dir, "init", "--quiet", "--bare", p)
			return p
		}, "a folder"},
		{"a folder that is not a repository", func(t *testing.T, e *fetchEnv, dir string) string {
			p := filepath.Join(dir, "work")
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
			return p
		}, "a folder"},
		{"another clone", func(t *testing.T, e *fetchEnv, dir string) string {
			other := newLeadClone(t, e.g, dir, "other")
			return other.tree
		}, "a folder"},
		{"a named pipe", func(t *testing.T, e *fetchEnv, dir string) string {
			p := filepath.Join(dir, "work")
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Skipf("no named pipe here: %v", err)
			}
			return p
		}, "a file of another kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newFetchEnv(t)
			c := e.arm(t)
			e.planner.Repository = tc.make(t, e, t.TempDir())
			_, err := e.m.Fetch(context.Background(), e.req())
			if !errors.Is(err, ErrRepositoryPath) {
				t.Fatalf("Fetch = %v, want ErrRepositoryPath", err)
			}
			if !strings.Contains(err.Error(), tc.kind) || !strings.Contains(err.Error(), "does not exist on the host") {
				t.Errorf("the message does not say what was found and what to do: %v", err)
			}
			c.none(t)
		})
	}
}

// TestFetchRepositoryPathThatCannotBeChecked: a path whose state cannot be
// told is refused as well: here a file where a folder should be on the way.
func TestFetchRepositoryPathThatCannotBeChecked(t *testing.T) {
	e := newFetchEnv(t)
	c := e.arm(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.planner.Repository = filepath.Join(file, "work")
	_, err := e.m.Fetch(context.Background(), e.req())
	if !errors.Is(err, ErrRepositoryPath) || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("Fetch = %v, want ErrRepositoryPath over ENOTDIR", err)
	}
	if !strings.Contains(err.Error(), "cannot be checked") {
		t.Errorf("message: %v", err)
	}
	c.none(t)
}

// TestFetchRepositoryPathThatDoesNotExistIsUnchanged: a path that is not on
// the host, which is a path inside the sandbox, is the case the fetch was
// made for. The sandboxed command here serves the clone whatever path git
// gives it, as a sandbox that maps the clone to that path does.
func TestFetchRepositoryPathThatDoesNotExistIsUnchanged(t *testing.T) {
	e := newFetchEnv(t)
	tip := e.lead.git(t, "rev-parse", "refs/heads/main")
	inside := filepath.Join(t.TempDir(), "absent", "work")
	e.planner.Repository = inside
	e.planner.UploadPack = []string{"/bin/sh", "-c", "exec '" + e.gup + "' '" + e.lead.tree + "'", "sh"}
	rep, err := e.m.Fetch(context.Background(), e.req())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(rep.Updates) != 1 || rep.Updates[0].Ref != "refs/leads/coder/heads/main" || rep.Updates[0].New != tip {
		t.Errorf("updates = %+v", rep.Updates)
	}
	if _, err := os.Lstat(inside); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the path exists after the fetch: %v", err)
	}
}

// TestFetchRepositoryPathOfTheCloneItselfIsUnchanged: the clone's own path
// passes. The owner has just checked it is a real folder, and no bundle is a
// folder. The plans of the other tests give this path.
func TestFetchRepositoryPathOfTheCloneItselfIsUnchanged(t *testing.T) {
	e := newFetchEnv(t)
	e.planner.Repository = e.lead.tree
	if _, err := e.m.Fetch(context.Background(), e.req()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

// TestFetchRefusesARepositoryPathThatAppearsLater: a path that is missing
// when the plan arrives and a bundle at some later time must not be read by
// the owner's git. There are two later times, and a case for each.
//
// Before the second check (step 6): the check refuses the path before git is
// started, and the mirror is as it was.
//
// After the second check, right before git is started (the hook 60): the
// check cannot see it, and the fetch names the source as a "file://" URL, for
// which git never opens a bundle. The sandboxed command is started instead
// and serves the lead's clone, so the fetch takes the lead's tip and not the
// tip that the bundle holds. The bundle is of another clone, with a commit of
// its own, so a read of it would show as a ref at that commit.
func TestFetchRefusesARepositoryPathThatAppearsLater(t *testing.T) {
	t.Run("before the second check", func(t *testing.T) {
		e := newFetchEnv(t)
		started := filepath.Join(t.TempDir(), "upload-pack")
		e.planner.UploadPack = e.wrapper("touch '" + started + "'")
		path := filepath.Join(t.TempDir(), "work")
		e.planner.Repository = path
		was := e.state0(t)
		fetchHook = func(step int) {
			if step == 6 {
				e.makeBundle(t, path)
			}
		}
		t.Cleanup(func() { fetchHook = nil })
		_, err := e.m.Fetch(context.Background(), e.req())
		if !errors.Is(err, ErrRepositoryPath) {
			t.Fatalf("Fetch = %v, want ErrRepositoryPath", err)
		}
		if _, serr := os.Stat(started); serr == nil {
			t.Error("the sandboxed command started")
		}
		e.unchanged(t, was)
	})

	t.Run("after the second check", func(t *testing.T) {
		e := newFetchEnv(t)
		tip := e.lead.git(t, "rev-parse", "refs/heads/main")
		other := newLeadClone(t, e.g, t.TempDir(), "other")
		otherTip := other.commit(t, "other.txt", "not the lead's\n", "the bundle's commit")
		if otherTip == tip {
			t.Fatal("the bundle holds the lead's tip: the case proves nothing")
		}
		started := filepath.Join(t.TempDir(), "upload-pack")
		e.planner.UploadPack = []string{"/bin/sh", "-c", "touch '" + started + "'; exec '" + e.gup + "' '" + e.lead.tree + "'", "sh"}
		path := filepath.Join(t.TempDir(), "work")
		e.planner.Repository = path
		fetchHook = func(step int) {
			if step == 60 {
				other.git(t, "bundle", "create", path, "main")
			}
		}
		t.Cleanup(func() { fetchHook = nil })
		rep, err := e.m.Fetch(context.Background(), e.req())
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if fi, serr := os.Lstat(path); serr != nil || !fi.Mode().IsRegular() {
			t.Fatalf("the bundle was not at the path when git started (%v): the case proves nothing", serr)
		}
		if _, serr := os.Stat(started); serr != nil {
			t.Error("the sandboxed command did not start: git took the path itself")
		}
		if len(rep.Updates) != 1 || rep.Updates[0].Ref != "refs/leads/coder/heads/main" || rep.Updates[0].New != tip {
			t.Errorf("updates = %+v, want the lead's tip %s", rep.Updates, tip)
		}
		for _, line := range mirrorRefs(t, e.m) {
			if strings.Contains(line, otherTip) {
				t.Errorf("the mirror holds the bundle's commit: %s", line)
			}
		}
	})
}

// TestFetchRepositoryPathWithAPercentSignReachesTheSandbox: git decodes "%"
// and two hexadecimal digits in the path of a "file://" URL, so the fetch
// writes each "%" of the plan's path as "%25". The sandboxed command gets the
// path as the plan has it, and the fetch works.
func TestFetchRepositoryPathWithAPercentSignReachesTheSandbox(t *testing.T) {
	for _, name := range []string{"100%41", "50%", "a%2Fb", "%25"} {
		t.Run(name, func(t *testing.T) {
			e := newFetchEnv(t)
			tip := e.lead.git(t, "rev-parse", "refs/heads/main")
			inside := filepath.Join(t.TempDir(), "absent", name, "work")
			record := filepath.Join(t.TempDir(), "path")
			e.planner.Repository = inside
			e.planner.UploadPack = []string{"/bin/sh", "-c", "printf '%s' \"$1\" > '" + record + "'; exec '" + e.gup + "' '" + e.lead.tree + "'", "sh"}
			rep, err := e.m.Fetch(context.Background(), e.req())
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(rep.Updates) != 1 || rep.Updates[0].New != tip {
				t.Errorf("updates = %+v", rep.Updates)
			}
			if got, err := os.ReadFile(record); err != nil || string(got) != inside {
				t.Errorf("the sandboxed command got %q (%v), want %q", got, err, inside)
			}
		})
	}
}
