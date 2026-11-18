//go:build linux && ataniq_canary

package standin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func readResult(t testing.TB, r testRepo) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(replayDir(r), ResultFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// resultWritten reports whether result.txt holds its line.
func resultWritten(r testRepo) bool {
	b, err := os.ReadFile(filepath.Join(replayDir(r), ResultFile))
	return err == nil && len(b) > 0
}

// submitToken pastes the canary message and a CR into a replay run.
func submitToken(t testing.TB, run *standinRun) {
	t.Helper()
	run.pty.waitFor(t, 0, bothQueries)
	run.pty.send(t, pasteOf(message("")))
	run.pty.send(t, "\r")
}

func TestReplayResultNamesTheCommit(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	run := startRun(t, r, 0)
	submitToken(t, run)
	waitUntil(t, "result.txt", func() bool { return resultWritten(r) })
	got := readResult(t, r)
	if !regexp.MustCompile(`^committed [0-9a-f]{4,40}\n$`).MatchString(got) {
		t.Fatalf("result.txt %q", got)
	}
	hash := strings.Fields(got)[1]
	if head := r.head(t); !strings.HasPrefix(head, hash) {
		t.Fatalf("result.txt names %s, HEAD is %s", hash, head)
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

// A second commit replaces the line of the first.
func TestReplayResultIsReplacedByTheNextCommit(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	run := startRun(t, r, 0)
	submitToken(t, run)
	waitUntil(t, "result.txt", func() bool { return resultWritten(r) })
	one := readResult(t, r)
	run.pty.send(t, pasteOf(message(""))+"\r")
	waitUntil(t, "the second commit", func() bool { return resultWritten(r) && readResult(t, r) != one })
	if n := strings.Count(runGit(t, r.Dir, "log", "--format=%s", "canary/one"), Subject); n != 2 {
		t.Fatalf("%d commits", n)
	}
	if got := readResult(t, r); strings.Count(got, "\n") != 1 || !strings.HasPrefix(runGit(t, r.Dir, "rev-parse", "HEAD"), strings.Fields(got)[1]) {
		t.Fatalf("result.txt %q", got)
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

// A commit that fails leaves a line with a fixed reason: nothing of git's
// output or of the tree's paths.
func TestReplayResultNamesAFailedCommitWithAFixedReason(t *testing.T) {
	t.Run("token file", func(t *testing.T) {
		r := newRepo(t)
		writeReplay(t, r, true)
		if err := unix.Mkfifo(filepath.Join(r.Dir, TokenFile), 0o644); err != nil {
			t.Fatal(err)
		}
		run := startRun(t, r, 0)
		submitToken(t, run)
		waitUntil(t, "result.txt", func() bool { return resultWritten(r) })
		if got := readResult(t, r); got != "failed: token file\n" {
			t.Fatalf("result.txt %q", got)
		}
		if err := run.finish(t); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no working tree", func(t *testing.T) {
		r := newRepo(t)
		writeReplay(t, r, true)
		r.Dir = t.TempDir()
		run := startRun(t, r, 0)
		submitToken(t, run)
		waitUntil(t, "result.txt", func() bool { return resultWritten(r) })
		if got := readResult(t, r); got != "failed: no working tree\n" {
			t.Fatalf("result.txt %q", got)
		}
		if err := run.finish(t); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("git add", func(t *testing.T) {
		r := newRepo(t)
		writeReplay(t, r, true)
		// A lock on the index makes git add fail.
		if err := os.WriteFile(filepath.Join(r.Dir, ".git", "index.lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		run := startRun(t, r, 0)
		submitToken(t, run)
		waitUntil(t, "result.txt", func() bool { return resultWritten(r) })
		if got := readResult(t, r); got != "failed: git add\n" {
			t.Fatalf("result.txt %q", got)
		}
		if err := run.finish(t); err != nil {
			t.Fatal(err)
		}
	})
}

// A paste without a token is not a commit and leaves no result.
func TestReplayNoResultWithoutACommit(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	run := startRun(t, r, 0)
	run.pty.waitFor(t, 0, bothQueries)
	run.pty.send(t, pasteOf("no token here")+"\r")
	run.pty.send(t, "\r")
	time.Sleep(200 * time.Millisecond)
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(replayDir(r), ResultFile)); err == nil {
		t.Fatal("result.txt exists")
	}
}

// Plain mode has no replay folder to write to.
func TestPlainWritesNoResultFile(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, false)
	run := startRun(t, r, 0)
	run.pty.waitFor(t, 0, "\x1b[?2004h")
	run.pty.send(t, pasteOf(message(""))+"\r")
	run.pty.waitFor(t, 0, "committed canary/one")
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(replayDir(r), ResultFile)); err == nil {
		t.Fatal("plain mode wrote result.txt")
	}
}

// result.txt is written without following a link: a link there ends the run
// with an error and the target keeps its content.
func TestReplayResultIsNotWrittenThroughALink(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(replayDir(r), ResultFile)); err != nil {
		t.Fatal(err)
	}
	run := startRun(t, r, 0)
	submitToken(t, run)
	select {
	case err := <-run.done:
		if err == nil {
			t.Fatal("Run wrote through a link")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep\n" {
		t.Fatalf("the link's target was changed: %q", b)
	}
}
