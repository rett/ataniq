//go:build linux && ataniq_canary

package standin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// within runs f and fails the test when it does not return in time: a refused
// file must not be waited for.
func within(t *testing.T, what string, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked", what)
		return nil
	}
}

// A pipe at the token file is refused, whether or not something reads it,
// and without blocking: Commit runs on the loop that handles the signals.
func TestCommitRefusesATokenFileThatIsAPipe(t *testing.T) {
	for _, reader := range []bool{false, true} {
		name := "no reader"
		if reader {
			name = "a reader"
		}
		t.Run(name, func(t *testing.T) {
			r := newRepo(t)
			path := filepath.Join(r.Dir, TokenFile)
			if err := unix.Mkfifo(path, 0o644); err != nil {
				t.Fatal(err)
			}
			if reader {
				fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer unix.Close(fd)
			}
			first := r.head(t)
			err := within(t, "Commit", func() error { return Commit(r.Dir, r.Home, testToken) })
			if err == nil {
				t.Fatal("Commit accepted a pipe")
			}
			// With a reader, ftruncate would refuse a pipe too; the check of
			// the kind must be what does.
			if reader && !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("refused for another reason: %v", err)
			}
			if r.head(t) != first {
				t.Fatal("a commit was made")
			}
			if st, err := os.Lstat(path); err != nil || st.Mode()&os.ModeNamedPipe == 0 {
				t.Fatalf("the pipe was replaced: %v, %v", st, err)
			}
		})
	}
}

// Any other kind of file is refused too, and nothing is truncated before the
// kind is known.
func TestCommitRefusesATokenFileThatIsADirectory(t *testing.T) {
	r := newRepo(t)
	if err := os.Mkdir(filepath.Join(r.Dir, TokenFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Commit(r.Dir, r.Home, testToken); err == nil {
		t.Fatal("Commit accepted a directory")
	}
}

// A regular file's old content is replaced, not appended to.
func TestWriteRegularReplacesTheContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	for _, s := range []string{"a long first content\n", "short\n"} {
		if err := writeRegular(path, s, 0o600); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(path); string(b) != s {
			t.Fatalf("file %q, want %q", b, s)
		}
	}
}

// order.txt is written in replay mode on the same loop: a pipe there ends the
// run with an error and does not block it.
func TestReplayRefusesAnOrderFileThatIsAPipe(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	path := filepath.Join(replayDir(r), OrderFile)
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	run := startRun(t, r, 50*time.Millisecond)
	select {
	case err := <-run.done:
		if err == nil {
			t.Fatal("Run wrote to a pipe")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run blocked on a pipe")
	}
}
