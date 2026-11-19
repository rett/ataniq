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

const project = "throwaway-one"

// markerEnv is an environment whose caller is the real user, so that the
// files a test creates are owned by it.
func markerEnv() env {
	e := systemEnv()
	e.home = func(int) (string, error) { return "/nonexistent-home", nil }
	return e
}

func stateDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(realpath(t, t.TempDir()), "state")
	mkdir(t, d)
	return d
}

func putMarker(t *testing.T, dir, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, MarkerName)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMarkerContent(t *testing.T) {
	want := `{"marker":"ataniq-canary-root","version":1,"project":"throwaway-one"}` + "\n"
	if got := markerContent(project); got != want {
		t.Fatalf("markerContent = %q", got)
	}
	if MarkerName != "canary-root.json" {
		t.Fatalf("MarkerName = %q", MarkerName)
	}
	if WaiverText != "assertion 10 waived: canary slice, no qualification record" {
		t.Fatalf("WaiverText = %q", WaiverText)
	}
}

func TestMarkerAccepted(t *testing.T) {
	d := stateDir(t)
	putMarker(t, d, markerContent(project), 0o600)
	if err := markerEnv().checkMarker(d, project); err != nil {
		t.Fatal(err)
	}
	if err := CheckMarker(d, project); err != nil {
		t.Fatal(err)
	}
	// The longest project name fits in the limit.
	long := "throwaway-" + strings.Repeat("a", 53)
	d2 := stateDir(t)
	putMarker(t, d2, markerContent(long), 0o600)
	if err := CheckMarker(d2, long); err != nil {
		t.Fatal(err)
	}
}

func TestMarkerFaults(t *testing.T) {
	e := markerEnv()
	t.Run("missing", func(t *testing.T) {
		wantRefusal(t, e.checkMarker(stateDir(t), project), CodeNoMarker)
	})
	t.Run("state root missing", func(t *testing.T) {
		wantRefusal(t, e.checkMarker(filepath.Join(stateDir(t), "nope"), project), CodeNoMarker)
	})
	t.Run("state root is a file", func(t *testing.T) {
		d := stateDir(t)
		f := filepath.Join(d, "file")
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		wantRefusal(t, e.checkMarker(f, project), CodeNoMarker)
	})
	t.Run("symlink to a good marker", func(t *testing.T) {
		d := stateDir(t)
		other := stateDir(t)
		target := putMarker(t, other, markerContent(project), 0o600)
		symlink(t, target, filepath.Join(d, MarkerName))
		wantRefusal(t, e.checkMarker(d, project), CodeBadMarker)
	})
	t.Run("dangling symlink", func(t *testing.T) {
		d := stateDir(t)
		symlink(t, filepath.Join(d, "nowhere"), filepath.Join(d, MarkerName))
		wantRefusal(t, e.checkMarker(d, project), CodeBadMarker)
	})
	t.Run("directory", func(t *testing.T) {
		d := stateDir(t)
		mkdir(t, filepath.Join(d, MarkerName))
		wantRefusal(t, e.checkMarker(d, project), CodeBadMarker)
	})
	t.Run("named pipe", func(t *testing.T) {
		d := stateDir(t)
		if err := unix.Mkfifo(filepath.Join(d, MarkerName), 0o600); err != nil {
			t.Fatal(err)
		}
		wantRefusal(t, e.checkMarker(d, project), CodeBadMarker)
	})
	t.Run("not owned by the caller", func(t *testing.T) {
		d := stateDir(t)
		putMarker(t, d, markerContent(project), 0o600)
		other := e
		other.uid = e.uid + 1
		wantRefusal(t, other.checkMarker(d, project), CodeBadMarker)
	})
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o400, 0o700, 0o660, 0o666, 0o200, 0o000, 0o600 | os.ModeSetuid} {
		mode := mode
		t.Run("mode "+mode.String(), func(t *testing.T) {
			d := stateDir(t)
			putMarker(t, d, markerContent(project), mode)
			wantRefusal(t, e.checkMarker(d, project), CodeBadMarker)
		})
	}
	t.Run("too long", func(t *testing.T) {
		d := stateDir(t)
		putMarker(t, d, markerContent(project)+strings.Repeat(" ", 300), 0o600)
		err := e.checkMarker(d, project)
		wantRefusal(t, err, CodeBadMarker)
		if !strings.Contains(err.Error(), "longer than") {
			t.Fatalf("not refused for its size: %v", err)
		}
	})
	t.Run("one byte over the limit", func(t *testing.T) {
		d := stateDir(t)
		c := markerContent(project)
		putMarker(t, d, c+strings.Repeat("x", maxMarkerSize+1-len(c)), 0o600)
		err := e.checkMarker(d, project)
		wantRefusal(t, err, CodeBadMarker)
		if !strings.Contains(err.Error(), "longer than") {
			t.Fatalf("not refused for its size: %v", err)
		}
	})
	contents := map[string]string{
		"empty":               "",
		"other project":       markerContent("throwaway-two"),
		"no line feed":        strings.TrimSuffix(markerContent(project), "\n"),
		"two line feeds":      markerContent(project) + "\n",
		"carriage return":     strings.TrimSuffix(markerContent(project), "\n") + "\r\n",
		"other version":       `{"marker":"ataniq-canary-root","version":2,"project":"throwaway-one"}` + "\n",
		"other marker":        `{"marker":"other","version":1,"project":"throwaway-one"}` + "\n",
		"spaces":              `{"marker": "ataniq-canary-root", "version": 1, "project": "throwaway-one"}` + "\n",
		"reordered":           `{"version":1,"marker":"ataniq-canary-root","project":"throwaway-one"}` + "\n",
		"extra key":           `{"marker":"ataniq-canary-root","version":1,"project":"throwaway-one","x":1}` + "\n",
		"prefix of the right": markerContent(project)[:20],
		"project is a prefix": markerContent("throwaway-on"),
		"binary":              "\x00\x01\x02",
	}
	for name, c := range contents {
		c := c
		t.Run("content "+name, func(t *testing.T) {
			d := stateDir(t)
			putMarker(t, d, c, 0o600)
			wantRefusal(t, e.checkMarker(d, project), CodeBadMarker)
		})
	}
}

func TestMarkerNotThrowaway(t *testing.T) {
	d := stateDir(t)
	putMarker(t, d, markerContent("ataniq"), 0o600)
	for _, p := range []string{"ataniq", "", "throwaway-", "throwaway", "Throwaway-one", "my-throwaway-one"} {
		wantRefusal(t, markerEnv().checkMarker(d, p), CodeNotThrowaway)
		wantRefusal(t, CheckMarker(d, p), CodeNotThrowaway)
		wantRefusal(t, WriteMarker(d+"-unused", p), CodeNotThrowaway)
	}
}

func TestWriteMarker(t *testing.T) {
	d := stateDir(t)
	oldMask := syscall.Umask(0o277)
	defer syscall.Umask(oldMask)

	if err := WriteMarker(d, project); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, MarkerName)
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
	if string(got) != markerContent(project) {
		t.Fatalf("content %q", got)
	}
	if err := CheckMarker(d, project); err != nil {
		t.Fatalf("the marker it wrote is refused: %v", err)
	}
	wantRefusal(t, CheckMarker(d, "throwaway-two"), CodeBadMarker)

	// It fails when the marker exists, and leaves it alone.
	err = WriteMarker(d, project)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("second write: %v, want an exists error", err)
	}
	err = WriteMarker(d, "throwaway-two")
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("write for another project: %v, want an exists error", err)
	}
	if got, _ := os.ReadFile(p); string(got) != markerContent(project) {
		t.Fatalf("the marker changed: %q", got)
	}
}

func TestWriteMarkerDoesNotFollowOrReplace(t *testing.T) {
	d := stateDir(t)
	victim := filepath.Join(stateDir(t), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink(t, victim, filepath.Join(d, MarkerName))
	if err := WriteMarker(d, project); !errors.Is(err, os.ErrExist) {
		t.Fatalf("write over a link: %v, want an exists error", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Fatalf("the link target was written: %q", got)
	}

	// A dangling link is not followed either.
	d2 := stateDir(t)
	missing := filepath.Join(stateDir(t), "missing")
	symlink(t, missing, filepath.Join(d2, MarkerName))
	if err := WriteMarker(d2, project); err == nil {
		t.Fatal("write over a dangling link succeeded")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the link target was created: %v", err)
	}

	// A plain file already there is not replaced.
	d3 := stateDir(t)
	p := putMarker(t, d3, "mine", 0o600)
	if err := WriteMarker(d3, project); !errors.Is(err, os.ErrExist) {
		t.Fatalf("write over a file: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "mine" {
		t.Fatalf("the file changed: %q", got)
	}
}

func TestWriteMarkerStateRootMissing(t *testing.T) {
	d := filepath.Join(stateDir(t), "nope")
	if err := WriteMarker(d, project); err == nil {
		t.Fatal("no error for a state root that does not exist")
	}
	if _, err := os.Lstat(d); err == nil {
		t.Fatal("WriteMarker made the state root")
	}
}

// Check runs the project, the roots and the marker in that order.
func TestCheck(t *testing.T) {
	f := newFixture(t, true)
	f.env.uid = markerEnv().uid
	state, runtime := f.good[0], f.good[1]
	mkdir(t, state)
	mkdir(t, runtime)

	wantRefusal(t, f.env.check(state, runtime, project, f.getenv), CodeNoMarker)
	if err := WriteMarker(state, project); err != nil {
		t.Fatal(err)
	}
	if err := f.env.check(state, runtime, project, f.getenv); err != nil {
		t.Fatal(err)
	}

	// Another project's marker.
	wantRefusal(t, f.env.check(state, runtime, "throwaway-two", f.getenv), CodeBadMarker)
	// A project without the prefix is refused before anything else.
	wantRefusal(t, f.env.check("relative", runtime, "ataniq", f.getenv), CodeNotThrowaway)
	wantRefusal(t, f.env.check(state, runtime, "", f.getenv), CodeNotThrowaway)
	// The roots are refused before the marker is looked at, even when the
	// default root carries a good marker.
	if err := WriteMarker(f.stateDef, project); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, f.env.check(f.stateDef, runtime, project, f.getenv), CodeDefaultRoot)
	wantRefusal(t, f.env.check(state, state, project, f.getenv), CodeNestedRoot)
	wantRefusal(t, f.env.check("state", runtime, project, f.getenv), CodeDefaultRoot)
}

func TestCheckPublic(t *testing.T) {
	if _, err := accountHome(os.Geteuid()); err != nil {
		t.Skipf("no account here: %v", err)
	}
	base := realpath(t, t.TempDir())
	state, runtime := filepath.Join(base, "s"), filepath.Join(base, "r")
	mkdir(t, state)
	mkdir(t, runtime)
	wantRefusal(t, Check(state, runtime, project, os.Getenv), CodeNoMarker)
	if err := WriteMarker(state, project); err != nil {
		t.Fatal(err)
	}
	if err := Check(state, runtime, project, os.Getenv); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, Check(state, runtime, "ataniq", os.Getenv), CodeNotThrowaway)
}
