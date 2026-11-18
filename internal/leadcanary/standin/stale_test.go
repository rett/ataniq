//go:build linux && ataniq_canary

package standin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Replay mode starts from a replay folder without the files of an earlier
// run: order.txt and result.txt are removed at the start, so that a file read
// after the run starts is this run's.
func TestReplayRemovesTheFilesOfAnEarlierRun(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	for name, s := range map[string]string{OrderFile: OrderDA1First + "\n", ResultFile: "committed 0123abc\n"} {
		if err := os.WriteFile(filepath.Join(replayDir(r), name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A long wait, so that the run records nothing of its own meanwhile.
	run := startRun(t, r, time.Minute)
	run.pty.waitFor(t, 0, bothQueries)
	for _, name := range []string{OrderFile, ResultFile} {
		if _, err := os.Lstat(filepath.Join(replayDir(r), name)); !os.IsNotExist(err) {
			t.Fatalf("%s is still there: %v", name, err)
		}
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

// Files of the replay folder other than the two are left alone.
func TestReplayKeepsTheOtherFilesOfTheFolder(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	run := startRun(t, r, time.Minute)
	run.pty.waitFor(t, 0, bothQueries)
	for _, name := range []string{FirstScreen, SecondScreen, ReportFile, SwitchFile} {
		if _, err := os.Lstat(filepath.Join(replayDir(r), name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

// A link at either name is not followed and not removed: it stays, its target
// keeps its content, and the run refuses it when it writes there.
func TestReplayLeavesALinkOfAnEarlierRun(t *testing.T) {
	for _, name := range []string{OrderFile, ResultFile} {
		t.Run(name, func(t *testing.T) {
			r := newRepo(t)
			writeReplay(t, r, true)
			target := filepath.Join(t.TempDir(), "elsewhere")
			if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(replayDir(r), name)
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			run := startRun(t, r, time.Minute)
			run.pty.waitFor(t, 0, bothQueries)
			if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the link was removed or replaced: %v, %v", st, err)
			}
			if b, _ := os.ReadFile(target); string(b) != "keep\n" {
				t.Fatalf("the link's target was changed: %q", b)
			}
			if err := run.finish(t); err != nil {
				t.Fatal(err)
			}
		})
	}
}
