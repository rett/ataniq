//go:build linux && ataniq_canary

package standin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/inject/osc7501"
)

// Two replies to the support query give one idle report: the second is read
// and ignored.
func TestReplayTwoReplyAnswersGiveOneReport(t *testing.T) {
	for _, c := range []struct {
		name  string
		steps []string
	}{
		{"one write", []string{osc7501.QueryReply + osc7501.QueryReply}},
		{"two writes", []string{osc7501.QueryReply, osc7501.QueryReply}},
		{"a DA1 reply between", []string{osc7501.QueryReply, da1Reply, osc7501.QueryReply}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			writeReplay(t, r, true)
			run := startRun(t, r, 0)
			run.pty.waitFor(t, 0, bothQueries)
			for _, s := range c.steps {
				run.pty.send(t, s)
				time.Sleep(30 * time.Millisecond)
			}
			// The second screen is drawn after the replies were all read.
			two := mustLoad(t, filepath.Join(replayDir(r), SecondScreen))
			one := mustLoad(t, filepath.Join(replayDir(r), FirstScreen))
			run.pty.send(t, pasteOf(message("")))
			run.pty.waitFor(t, 0, string(two.draw(one.AltScreen)))
			if err := run.finish(t); err != nil {
				t.Fatal(err)
			}
			report := "\x1b]7501;" + sampleReport + "\x1b\\"
			if n := strings.Count(string(run.pty.output()), report); n != 1 {
				t.Fatalf("%d idle reports, want one", n)
			}
			if got := readOrder(t, r); got != Order7501First+"\n" {
				t.Fatalf("order.txt %q", got)
			}
		})
	}
}

// The DA1 reply starts the wait for the 7501 reply again: a 7501 reply that
// comes within one wait of the DA1 reply, though after one wait from the
// start, is not recorded as missing, and the order is da1-first.
func TestReplayDA1ReplyRestartsTheWait(t *testing.T) {
	const wait = time.Second
	r := newRepo(t)
	writeReplay(t, r, true)
	run := startRun(t, r, wait)
	run.pty.waitFor(t, 0, bothQueries)
	time.Sleep(400 * time.Millisecond)
	run.pty.send(t, da1Reply)
	sent := time.Now()
	// The wait from the start ends 600 ms later; the one from the DA1 reply
	// ends after 1000 ms. Between the two nothing is recorded yet.
	time.Sleep(time.Until(sent.Add(700 * time.Millisecond)))
	if _, err := os.Stat(filepath.Join(replayDir(r), OrderFile)); err == nil {
		t.Fatalf("order.txt is written %v after the DA1 reply, before its wait ended: %q", time.Since(sent), readOrder(t, r))
	}
	run.pty.send(t, osc7501.QueryReply)
	run.pty.waitFor(t, 0, "\x1b]7501;"+sampleReport+"\x1b\\")
	waitUntil(t, "order.txt", func() bool { return orderWritten(r) })
	if got := readOrder(t, r); got != OrderDA1First+"\n" {
		t.Fatalf("order.txt %q, want %q", got, OrderDA1First)
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

// A link at order.txt is not followed: the run ends with an error and the
// target keeps its content, whether the order is "no reply" or a reply.
func TestReplayRefusesAnOrderFileThatIsALink(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply string
	}{
		{"no reply", ""},
		{"a reply", osc7501.QueryReply},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			writeReplay(t, r, true)
			target := filepath.Join(t.TempDir(), "elsewhere")
			if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(replayDir(r), OrderFile)); err != nil {
				t.Fatal(err)
			}
			run := startRun(t, r, 50*time.Millisecond)
			if c.reply != "" {
				run.pty.waitFor(t, 0, bothQueries)
				run.pty.send(t, c.reply)
			}
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
		})
	}
}

// Nothing the repository holds runs when the stand-in commits: no hook of
// .git/hooks, no hooks folder named by the repository's configuration and no
// file system monitor.
func TestCommitRunsNothingTheRepositoryHolds(t *testing.T) {
	r := newRepo(t)
	marks := t.TempDir()
	script := func(name string) string {
		return "#!/bin/sh\ntouch '" + filepath.Join(marks, name) + "'\n"
	}
	hookNames := []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit", "post-checkout", "reference-transaction"}
	for _, name := range hookNames {
		if err := os.WriteFile(filepath.Join(r.Dir, ".git", "hooks", name), []byte(script(name)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A folder that the repository's own configuration names as the hooks
	// folder, and a monitor command, hold the same.
	other := filepath.Join(t.TempDir(), "hooks")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range hookNames {
		if err := os.WriteFile(filepath.Join(other, name), []byte(script("other-"+name)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	monitor := filepath.Join(t.TempDir(), "monitor")
	if err := os.WriteFile(monitor, []byte(script("monitor")), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, r.Dir, "config", "core.hooksPath", other)
	runGit(t, r.Dir, "config", "core.fsmonitor", monitor)
	// Without the stand-in's settings the hooks do run: the test needs to see
	// that, or it proves nothing.
	runGit(t, r.Dir, "switch", "--create", "sample/probe")
	if _, err := os.Stat(filepath.Join(marks, "other-post-checkout")); err != nil {
		t.Fatalf("the sample hooks do not run without the stand-in's settings: %v", err)
	}
	runGit(t, r.Dir, "switch", "develop")
	entries, _ := os.ReadDir(marks)
	for _, e := range entries {
		os.Remove(filepath.Join(marks, e.Name()))
	}

	if err := Commit(r.Dir, r.Home, testToken); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, r.Dir, "log", "-1", "--format=%s"); got != Subject {
		t.Fatalf("subject %q", got)
	}
	entries, _ = os.ReadDir(marks)
	for _, e := range entries {
		t.Errorf("%s ran", e.Name())
	}
}
