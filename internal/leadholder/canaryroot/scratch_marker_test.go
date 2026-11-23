//go:build linux && ataniq_canary

package canaryroot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const scratchProject = "scratch-alpha"

func putScratchMarker(t *testing.T, dir, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, ScratchMarkerName)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScratchMarkerContent(t *testing.T) {
	want := `{"marker":"ataniq-scratch-root","version":1}` + "\n"
	if scratchMarkerContent != want {
		t.Fatalf("scratchMarkerContent = %q", scratchMarkerContent)
	}
	if ScratchMarkerName != "scratch-root.json" {
		t.Fatalf("ScratchMarkerName = %q", ScratchMarkerName)
	}
	if CodeNotThrowaway != CodeNotReserved || CodeNotReserved != "not-throwaway" {
		t.Fatalf("the not-reserved code is %q (alias %q)", CodeNotReserved, CodeNotThrowaway)
	}
}

// One marker serves every scratch project: it names none.
func TestScratchMarkerAccepted(t *testing.T) {
	d := stateDir(t)
	putScratchMarker(t, d, scratchMarkerContent, 0o600)
	for _, p := range []string{scratchProject, "scratch-beta", "scratch-a", "scratch-" + strings.Repeat("a", 55)} {
		if err := markerEnv().checkMarker(d, p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
		if err := CheckMarker(d, p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
}

// The scratch marker is judged with the rules of the canary marker.
func TestScratchMarkerFaults(t *testing.T) {
	e := markerEnv()
	t.Run("missing", func(t *testing.T) {
		wantRefusal(t, e.checkMarker(stateDir(t), scratchProject), CodeNoMarker)
	})
	t.Run("state root missing", func(t *testing.T) {
		wantRefusal(t, e.checkMarker(filepath.Join(stateDir(t), "nope"), scratchProject), CodeNoMarker)
	})
	t.Run("state root is a file", func(t *testing.T) {
		f := filepath.Join(stateDir(t), "file")
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		wantRefusal(t, e.checkMarker(f, scratchProject), CodeNoMarker)
	})
	t.Run("symlink to a good marker", func(t *testing.T) {
		d := stateDir(t)
		target := putScratchMarker(t, stateDir(t), scratchMarkerContent, 0o600)
		symlink(t, target, filepath.Join(d, ScratchMarkerName))
		wantRefusal(t, e.checkMarker(d, scratchProject), CodeBadMarker)
	})
	t.Run("dangling symlink", func(t *testing.T) {
		d := stateDir(t)
		symlink(t, filepath.Join(d, "nowhere"), filepath.Join(d, ScratchMarkerName))
		wantRefusal(t, e.checkMarker(d, scratchProject), CodeBadMarker)
	})
	t.Run("directory", func(t *testing.T) {
		d := stateDir(t)
		mkdir(t, filepath.Join(d, ScratchMarkerName))
		wantRefusal(t, e.checkMarker(d, scratchProject), CodeBadMarker)
	})
	t.Run("named pipe", func(t *testing.T) {
		d := stateDir(t)
		if err := unix.Mkfifo(filepath.Join(d, ScratchMarkerName), 0o600); err != nil {
			t.Fatal(err)
		}
		wantRefusal(t, e.checkMarker(d, scratchProject), CodeBadMarker)
	})
	t.Run("not owned by the caller", func(t *testing.T) {
		d := stateDir(t)
		putScratchMarker(t, d, scratchMarkerContent, 0o600)
		other := e
		other.uid = e.uid + 1
		wantRefusal(t, other.checkMarker(d, scratchProject), CodeBadMarker)
	})
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o400, 0o700, 0o660, 0o666, 0o200, 0o000, 0o600 | os.ModeSetuid} {
		mode := mode
		t.Run("mode "+mode.String(), func(t *testing.T) {
			d := stateDir(t)
			putScratchMarker(t, d, scratchMarkerContent, mode)
			wantRefusal(t, e.checkMarker(d, scratchProject), CodeBadMarker)
		})
	}
	t.Run("larger than the limit", func(t *testing.T) {
		d := stateDir(t)
		putScratchMarker(t, d, scratchMarkerContent+strings.Repeat(" ", maxMarkerSize), 0o600)
		wantRefusal(t, e.checkMarker(d, scratchProject), CodeBadMarker)
	})
	contents := map[string]string{
		"empty":               "",
		"no newline":          strings.TrimSuffix(scratchMarkerContent, "\n"),
		"two newlines":        scratchMarkerContent + "\n",
		"spaces":              `{"marker": "ataniq-scratch-root", "version": 1}` + "\n",
		"wrong marker word":   `{"marker":"ataniq-canary-root","version":1}` + "\n",
		"version 2":           `{"marker":"ataniq-scratch-root","version":2}` + "\n",
		"a project in it":     `{"marker":"ataniq-scratch-root","version":1,"project":"scratch-alpha"}` + "\n",
		"extra key":           `{"marker":"ataniq-scratch-root","version":1,"x":1}` + "\n",
		"prefix of the right": scratchMarkerContent[:20],
		"the canary content":  markerContent(project),
		"binary":              "\x00\x01\x02",
	}
	for name, c := range contents {
		c := c
		t.Run("content "+name, func(t *testing.T) {
			d := stateDir(t)
			putScratchMarker(t, d, c, 0o600)
			wantRefusal(t, e.checkMarker(d, scratchProject), CodeBadMarker)
		})
	}
}

// A scratch marker does not satisfy a throwaway project and the canary marker
// does not satisfy a scratch project.
func TestMarkersAreNotInterchangeable(t *testing.T) {
	e := markerEnv()
	d := stateDir(t)
	putScratchMarker(t, d, scratchMarkerContent, 0o600)
	wantRefusal(t, e.checkMarker(d, project), CodeWrongKind)
	wantRefusal(t, CheckMarker(d, project), CodeWrongKind)

	d2 := stateDir(t)
	putMarker(t, d2, markerContent(scratchProject), 0o600)
	wantRefusal(t, e.checkMarker(d2, scratchProject), CodeWrongKind)
}

// A root that carries the marker of the other kind is refused even when it
// also carries a good marker of the project's own kind, and whatever sits at
// the other marker's path: a bad file, a link, a directory or a named pipe.
func TestWrongKindRefusedWhateverIsThere(t *testing.T) {
	e := markerEnv()
	plant := map[string]func(t *testing.T, p string){
		"one byte": func(t *testing.T, p string) {
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"empty file": func(t *testing.T, p string) {
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"dangling symlink": func(t *testing.T, p string) { symlink(t, p+".nowhere", p) },
		"directory":        func(t *testing.T, p string) { mkdir(t, p) },
		"named pipe": func(t *testing.T, p string) {
			if err := unix.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, f := range plant {
		f := f
		t.Run("scratch project, canary marker: "+name, func(t *testing.T) {
			d := stateDir(t)
			putScratchMarker(t, d, scratchMarkerContent, 0o600)
			f(t, filepath.Join(d, MarkerName))
			wantRefusal(t, e.checkMarker(d, scratchProject), CodeWrongKind)
		})
		t.Run("throwaway project, scratch marker: "+name, func(t *testing.T) {
			d := stateDir(t)
			putMarker(t, d, markerContent(project), 0o600)
			f(t, filepath.Join(d, ScratchMarkerName))
			wantRefusal(t, e.checkMarker(d, project), CodeWrongKind)
		})
	}
}

// A state root that cannot be examined at the other marker's path is refused:
// the check fails closed.
func TestWrongKindUnexaminableRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode of a folder")
	}
	d := stateDir(t)
	putMarker(t, d, markerContent(project), 0o600)
	if err := os.Chmod(d, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(d, 0o700)
	wantRefusal(t, markerEnv().checkMarker(d, project), CodeWrongKind)
	wantRefusal(t, markerEnv().checkMarker(d, scratchProject), CodeWrongKind)
}

func TestCheckMarkerNeitherPrefix(t *testing.T) {
	d := stateDir(t)
	putScratchMarker(t, d, scratchMarkerContent, 0o600)
	putMarker(t, d, markerContent("ataniq"), 0o600)
	for _, p := range []string{"ataniq", "", "scratch-", "scratch", "Scratch-one", "my-scratch-one", "scratchy-one", "scratch-One"} {
		wantRefusal(t, markerEnv().checkMarker(d, p), CodeNotReserved)
		wantRefusal(t, CheckMarker(d, p), CodeNotReserved)
	}
}

func TestWriteScratchMarker(t *testing.T) {
	d := stateDir(t)
	oldMask := syscall.Umask(0o277)
	defer syscall.Umask(oldMask)

	if err := WriteScratchMarker(d); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, ScratchMarkerName)
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("mode %v, want a regular file with mode 0600 whatever the umask", st.Mode())
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != scratchMarkerContent {
		t.Fatalf("content %q", got)
	}
	if len(got) > maxMarkerSize {
		t.Fatalf("the marker is %d bytes, the limit is %d", len(got), maxMarkerSize)
	}
	for _, pr := range []string{scratchProject, "scratch-beta"} {
		if err := CheckMarker(d, pr); err != nil {
			t.Fatalf("the marker it wrote is refused for %q: %v", pr, err)
		}
	}
	// It wrote no canary marker.
	if _, err := os.Lstat(filepath.Join(d, MarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the canary marker is there: %v", err)
	}

	// It fails when the marker exists, and leaves it alone.
	if err := WriteScratchMarker(d); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second write: %v, want an exists error", err)
	}
	if got, _ := os.ReadFile(p); string(got) != scratchMarkerContent {
		t.Fatalf("the marker changed: %q", got)
	}
}

func TestWriteScratchMarkerDoesNotFollowOrReplace(t *testing.T) {
	d := stateDir(t)
	victim := filepath.Join(stateDir(t), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink(t, victim, filepath.Join(d, ScratchMarkerName))
	if err := WriteScratchMarker(d); !errors.Is(err, os.ErrExist) {
		t.Fatalf("write over a link: %v, want an exists error", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Fatalf("the link target was written: %q", got)
	}

	d2 := stateDir(t)
	missing := filepath.Join(stateDir(t), "missing")
	symlink(t, missing, filepath.Join(d2, ScratchMarkerName))
	if err := WriteScratchMarker(d2); err == nil {
		t.Fatal("write over a dangling link succeeded")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the link target was created: %v", err)
	}

	d3 := stateDir(t)
	p := putScratchMarker(t, d3, "mine", 0o600)
	if err := WriteScratchMarker(d3); !errors.Is(err, os.ErrExist) {
		t.Fatalf("write over a file: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "mine" {
		t.Fatalf("the file changed: %q", got)
	}
}

func TestWriteScratchMarkerStateRootMissing(t *testing.T) {
	d := filepath.Join(stateDir(t), "nope")
	if err := WriteScratchMarker(d); err == nil {
		t.Fatal("no error for a state root that does not exist")
	}
	if _, err := os.Lstat(d); err == nil {
		t.Fatal("WriteScratchMarker made the state root")
	}
}

// One root is a canary root or a scratch root: each writer refuses a root that
// carries the other's marker and writes nothing.
func TestWritersRefuseTheOtherKindOfRoot(t *testing.T) {
	d := stateDir(t)
	if err := WriteMarker(d, project); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, WriteScratchMarker(d), CodeWrongKind)
	if _, err := os.Lstat(filepath.Join(d, ScratchMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the scratch marker was written: %v", err)
	}

	d2 := stateDir(t)
	if err := WriteScratchMarker(d2); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, WriteMarker(d2, project), CodeWrongKind)
	if _, err := os.Lstat(filepath.Join(d2, MarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the canary marker was written: %v", err)
	}
}

// WriteMarker stays the canary's: a scratch project is not a throwaway project.
func TestWriteMarkerRefusesAScratchProject(t *testing.T) {
	d := stateDir(t)
	wantRefusal(t, WriteMarker(d, scratchProject), CodeNotThrowaway)
	if _, err := os.Lstat(filepath.Join(d, MarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a marker was written: %v", err)
	}
}

// Check chooses the rule by the prefix: the roots rules are the same, the
// marker is the kind's, and the other kind's marker is refused.
func TestCheckScratch(t *testing.T) {
	f := newFixture(t, true)
	f.env.uid = markerEnv().uid
	state, runtime := f.good[0], f.good[1]
	mkdir(t, state)
	mkdir(t, runtime)

	wantRefusal(t, f.env.check(state, runtime, scratchProject, f.getenv), CodeNoMarker)
	if err := WriteScratchMarker(state); err != nil {
		t.Fatal(err)
	}
	// The one root marker serves every lead of the place.
	for _, p := range []string{scratchProject, "scratch-beta"} {
		if err := f.env.check(state, runtime, p, f.getenv); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	// A throwaway project does not pass on a scratch root.
	wantRefusal(t, f.env.check(state, runtime, project, f.getenv), CodeWrongKind)
	// A project with neither prefix is refused before anything else.
	wantRefusal(t, f.env.check("relative", runtime, "ataniq", f.getenv), CodeNotReserved)
	wantRefusal(t, f.env.check(state, runtime, "scratch-", f.getenv), CodeNotReserved)
	// The roots are refused before the marker is looked at, even when the
	// default root carries a good scratch marker.
	if err := WriteScratchMarker(f.stateDef); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, f.env.check(f.stateDef, runtime, scratchProject, f.getenv), CodeDefaultRoot)
	wantRefusal(t, f.env.check(state, state, scratchProject, f.getenv), CodeNestedRoot)
	wantRefusal(t, f.env.check("state", runtime, scratchProject, f.getenv), CodeDefaultRoot)
	wantRefusal(t, f.env.check(state, f.runtimeDef, scratchProject, f.getenv), CodeDefaultRoot)

	// A canary marker beside the scratch marker: refused for both kinds.
	if err := os.WriteFile(filepath.Join(state, MarkerName), []byte(markerContent(project)), 0o600); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, f.env.check(state, runtime, scratchProject, f.getenv), CodeWrongKind)
	wantRefusal(t, f.env.check(state, runtime, project, f.getenv), CodeWrongKind)
}

// A canary root is not a scratch root: the throwaway rule is as it was, and a
// scratch project is refused on it.
func TestCheckThrowawayRootRefusesScratch(t *testing.T) {
	f := newFixture(t, true)
	f.env.uid = markerEnv().uid
	state, runtime := f.good[0], f.good[1]
	mkdir(t, state)
	mkdir(t, runtime)
	if err := WriteMarker(state, project); err != nil {
		t.Fatal(err)
	}
	if err := f.env.check(state, runtime, project, f.getenv); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, f.env.check(state, runtime, scratchProject, f.getenv), CodeWrongKind)
	wantRefusal(t, f.env.check(state, runtime, "throwaway-two", f.getenv), CodeBadMarker)
}

func TestCheckScratchPublic(t *testing.T) {
	if _, err := accountHome(os.Geteuid()); err != nil {
		t.Skipf("no account here: %v", err)
	}
	base := realpath(t, t.TempDir())
	state, runtime := filepath.Join(base, "s"), filepath.Join(base, "r")
	mkdir(t, state)
	mkdir(t, runtime)
	wantRefusal(t, Check(state, runtime, scratchProject, os.Getenv), CodeNoMarker)
	if err := WriteScratchMarker(state); err != nil {
		t.Fatal(err)
	}
	if err := Check(state, runtime, scratchProject, os.Getenv); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, Check(state, runtime, "ataniq", os.Getenv), CodeNotReserved)
	wantRefusal(t, Check(state, runtime, project, os.Getenv), CodeWrongKind)
}
