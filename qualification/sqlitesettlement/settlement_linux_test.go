//go:build linux

package sqlitesettlement

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type fixture struct {
	path     string
	owner    *os.File
	identity os.FileInfo
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// Use the checkout filesystem, not TMPDIR's potentially memory-only mount.
	dir, err := os.MkdirTemp(".", ".settlement-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{path: filepath.Join(dir, "fixture.db")}
	t.Cleanup(func() {
		// Keep original-inode ownership through cleanup, after all children wait.
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
		if f.owner != nil {
			if err := f.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	f.owner, err = os.OpenFile(f.path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.identity, err = f.owner.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(f.owner.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	assertClean(t, f.run(t, "initialize"))
	return f
}

func (f *fixture) command(t *testing.T, mode string) (*exec.Cmd, context.CancelFunc) {
	t.Helper()
	named, err := os.Lstat(f.path)
	if err != nil || !os.SameFile(f.identity, named) {
		t.Fatal("original fixture inode changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "ATANIQ_SETTLEMENT_CHILD="+mode, "ATANIQ_SETTLEMENT_FIXTURE="+f.path)
	cmd.ExtraFiles = []*os.File{f.owner}
	cmd.WaitDelay = time.Second
	return cmd, cancel
}

func (f *fixture) run(t *testing.T, mode string) receipt {
	t.Helper()
	cmd, cancel := f.command(t, mode)
	defer cancel()
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s helper failed: %v: %s", mode, err, output.String())
	}
	var r receipt
	if err := json.Unmarshal(output.Bytes(), &r); err != nil {
		t.Fatal(err, output.String())
	}
	r.Waited = true
	logReceipt(t, r)
	if r.Mode != mode || !r.ProfileVerified || !r.Closed {
		t.Fatal("helper did not complete the physical-connection protocol")
	}
	if err := checkFiles(f.path); err != nil {
		t.Fatal(err)
	}
	return r
}

func logReceipt(t *testing.T, r receipt) {
	t.Helper()
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(encoded))
}

func assertClean(t *testing.T, r receipt) {
	t.Helper()
	for _, s := range r.Steps {
		if s.Error != "" {
			t.Fatalf("%s: %s: %s", r.Mode, s.Stage, s.Error)
		}
	}
}

func requireCode(t *testing.T, r receipt, stage string, code int) {
	t.Helper()
	for _, s := range r.Steps {
		if s.Stage == stage && s.SQLiteCode == code && s.Error != "" {
			return
		}
	}
	t.Fatalf("missing %s SQLite code %d in %+v", stage, code, r.Steps)
}

func syncAvailable(t *testing.T, r receipt) bool {
	t.Helper()
	if r.Incomplete != "" {
		if os.Getenv("ATANIQ_SQLITE_SETTLEMENT_GATE") == "1" {
			t.Fatalf("INCOMPLETE native qualification: %s", r.Incomplete)
		}
		t.Logf("INCOMPLETE native qualification: %s; ordinary test success is not qualification", r.Incomplete)
		return false
	}
	if r.BeforeSync != [2]string{"<nil>", "<nil>"} || r.AfterSync != [2]string{unix.EIO.Error(), unix.EIO.Error()} {
		t.Fatal("missing sync-denial controls")
	}
	return true
}

// This is only the harness's result classifier, not a production algorithm.
// syncDependence comes from paired native filtered/unfiltered dirty commits.
func classify(r receipt, writerWaited, syncDependence bool) string {
	if !writerWaited || !syncDependence || !r.Waited || !r.Closed {
		return "unknown"
	}
	if !r.ProfileVerified || !r.CommitOK || !r.BeforeRead || !r.AfterRead {
		return "unknown"
	}
	if r.Mode != "barrier" && r.Mode != "barrier-open" {
		return "unknown"
	}
	for _, s := range r.Steps {
		if s.Error != "" {
			return "unknown"
		}
	}
	if r.Incomplete != "" || r.Before.Epoch < 0 || r.Before.Epoch >= 1024 || r.After.Epoch != r.Before.Epoch+1 {
		return "unknown"
	}
	applied := state{
		Commands: 1, Events: 1, Epoch: r.After.Epoch,
		Key: commandKey, Binding: binding, Result: result,
		Policy: "enabled", Cause: result, Revision: 1,
	}
	if r.After == applied {
		return "applied"
	}
	absent := state{Epoch: r.After.Epoch, Policy: "disabled"}
	if r.After == absent {
		return "absent"
	}
	return "unknown"
}

func TestNativeSettlement(t *testing.T) {
	// Obtain sync-dependence evidence before any case can report confirmation.
	syncDependence := true
	for _, mode := range []string{"barrier-filter", "barrier-open-filter"} {
		control := newFixture(t).run(t, strings.TrimSuffix(mode, "-filter"))
		assertClean(t, control)
		if !control.CommitOK || control.After.Epoch != control.Before.Epoch+1 {
			t.Fatal("unfiltered dirty commit control failed")
		}
		f := newFixture(t)
		r := f.run(t, mode)
		if !syncAvailable(t, r) {
			syncDependence = false
			continue
		}
		requireCode(t, r, "commit", 1034) // SQLITE_IOERR_FSYNC
		if classify(r, true, true) != "unknown" {
			t.Fatal("failed barrier confirmed a result")
		}
		recovered := f.run(t, "barrier")
		assertClean(t, recovered)
		if classify(recovered, true, true) != "absent" {
			t.Fatal("unfiltered recovery barrier failed")
		}
	}
	for _, mode := range []string{"mutation", "mutation-lost", "mutation-filter"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			mutated := f.run(t, mode)
			if mode == "mutation-filter" {
				if !syncAvailable(t, mutated) {
					return
				}
				requireCode(t, mutated, "commit", 1034)
			} else if !mutated.CommitOK {
				t.Fatal("normal mutation did not commit")
			}
			visible := f.run(t, "snapshot")
			assertClean(t, visible)
			if classify(visible, true, syncDependence) != "unknown" {
				t.Fatal("visibility alone cleared the fence")
			}
			barrier := f.run(t, "barrier")
			assertClean(t, barrier)
			got := classify(barrier, mutated.Waited, syncDependence)
			if !syncDependence {
				if got != "unknown" {
					t.Fatal("missing sync evidence confirmed")
				}
				return
			}
			if got == "unknown" || (mode != "mutation-filter" && got != "applied") {
				t.Fatalf("unexpected settlement: %s", got)
			}
			// A pre-sync failure does not prescribe present versus absent. Either
			// must have its entire exact binding/result/policy/event state intact.
			retry := f.run(t, "retry")
			assertClean(t, retry)
			if retry.After != barrier.After {
				t.Fatal("exact retry changed immutable evidence or reapplied mutation")
			}
		})
	}
	t.Run("empty-is-not-a-barrier", func(t *testing.T) {
		f := newFixture(t)
		r := f.run(t, "empty-filter")
		if !syncAvailable(t, r) {
			return
		}
		if !r.CommitOK || r.Before != r.After || classify(r, true, true) != "unknown" {
			t.Fatal("empty transaction control failed")
		}
	})
	for _, mode := range []string{"barrier-cancel", "barrier-full"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			assertClean(t, f.run(t, "mutation"))
			r := f.run(t, mode)
			if mode == "barrier-full" {
				requireCode(t, r, "write", 13) // SQLITE_FULL
			} else {
				found := false
				for _, s := range r.Steps {
					found = found || s.Stage == "commit" && s.Error == context.Canceled.Error()
				}
				if !found {
					t.Fatal("missing commit cancellation")
				}
			}
			if classify(r, true, syncDependence) != "unknown" {
				t.Fatal("failed barrier cleared fence")
			}
			recovered := f.run(t, "barrier")
			assertClean(t, recovered)
			if syncDependence && classify(recovered, true, true) != "applied" {
				t.Fatal("bounded recovery failed")
			}
		})
	}
	t.Run("already-open-wal", func(t *testing.T) {
		f := newFixture(t)
		assertClean(t, f.run(t, "mutation"))
		r := f.run(t, "barrier-open")
		assertClean(t, r)
		if syncDependence && classify(r, true, true) != "applied" {
			t.Fatal("open WAL barrier failed")
		}
	})
	t.Run("paused-writer", func(t *testing.T) {
		f := newFixture(t)
		child := f.pause(t, "mutation-paused")
		visible := f.run(t, "snapshot")
		assertClean(t, visible)
		if visible.After.Commands != 0 || classify(visible, false, syncDependence) != "unknown" {
			t.Fatal("racing snapshot treated as authoritative")
		}
		// The harness fence admits no mutation/barrier until this writer is
		// cancelled AND waited. Cancellation alone is not a settlement receipt.
		settled := child.cancelWriter(t)
		if settled.CommitOK {
			t.Fatal("cancelled writer committed")
		}
		r := f.run(t, "barrier")
		assertClean(t, r)
		if syncDependence && classify(r, settled.Waited, true) != "absent" {
			t.Fatal("paused writer outlived settlement")
		}
	})
	for _, mode := range []string{"mutation-before-death", "mutation-after-death", "barrier-before-death", "barrier-after-death"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			if strings.HasPrefix(mode, "barrier") {
				assertClean(t, f.run(t, "mutation"))
			}
			child := f.pause(t, mode)
			child.kill(t)
			r := f.run(t, "barrier")
			assertClean(t, r)
			want := "applied"
			if mode == "mutation-before-death" {
				want = "absent"
			}
			if syncDependence && classify(r, true, true) != want {
				t.Fatal("process-death recovery mismatch")
			}
		})
	}
	if syncDependence {
		t.Log("SUPPORTED: native pre-sync denial and dirty FULL commit sync dependence; successful settlement in tested schedules under SQLite/OS sync contract; no power-loss claim")
	}
}

type pausedChild struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	input   io.WriteCloser
	decoder *json.Decoder
	stderr  *boundedOutput
	waited  bool
}

func (f *fixture) pause(t *testing.T, mode string) *pausedChild {
	t.Helper()
	cmd, cancel := f.command(t, mode)
	p := &pausedChild{cmd: cmd, cancel: cancel, stderr: &boundedOutput{}}
	t.Cleanup(func() {
		cancel()
		if cmd.Process != nil && !p.waited {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	cmd.Stderr = p.stderr
	var err error
	p.input, err = cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.decoder = json.NewDecoder(io.LimitReader(output, 64*1024))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var r receipt
	if err := p.decoder.Decode(&r); err != nil {
		t.Fatalf("pause receipt: %v: %s", err, p.stderr.String())
	}
	logReceipt(t, r)
	assertClean(t, r)
	if len(r.Steps) == 0 || (r.Steps[len(r.Steps)-1].Stage != "before-commit" && r.Steps[len(r.Steps)-1].Stage != "after-commit") {
		t.Fatal("missing deterministic pause seam")
	}
	return p
}

func (p *pausedChild) kill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := p.cmd.Wait()
	p.waited = true
	p.cancel()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != -1 {
		t.Fatalf("expected killed helper: %v", err)
	}
	t.Log("process SIGKILL waited before recovery; kernel page cache retained")
}

func (p *pausedChild) cancelWriter(t *testing.T) receipt {
	t.Helper()
	if _, err := p.input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	p.input.Close()
	var r receipt
	if err := p.decoder.Decode(&r); err != nil {
		t.Fatal(err)
	}
	err := p.cmd.Wait()
	p.waited = true
	p.cancel()
	if err != nil {
		t.Fatalf("cancelled helper: %v: %s", err, p.stderr.String())
	}
	r.Waited = true
	logReceipt(t, r)
	return r
}

func TestClassifierRefusesIncompleteEvidence(t *testing.T) {
	good := receipt{Mode: "barrier", ProfileVerified: true, CommitOK: true, Closed: true, Waited: true, BeforeRead: true, AfterRead: true,
		After: state{Epoch: 1, Policy: "disabled"}}
	if classify(good, true, true) != "absent" {
		t.Fatal("bad classifier control")
	}
	for _, failure := range []string{"rollback", "physical-close", "write", "commit"} {
		r := good
		r.Steps = []step{{Stage: failure, Error: "synthetic protocol error, not native fault injection"}}
		if classify(r, true, true) != "unknown" {
			t.Fatal("error confirmed", failure)
		}
	}
	for _, oldWaited := range []bool{false, true} {
		if classify(good, oldWaited, false) != "unknown" {
			t.Fatal("missing sync evidence confirmed")
		}
	}
	if classify(good, false, true) != "unknown" {
		t.Fatal("unsettled writer confirmed")
	}
	good.After.Commands = 1
	if classify(good, true, true) != "unknown" {
		t.Fatal("incomplete binding confirmed")
	}
	good.After = state{Commands: 1, Events: 1, Epoch: 1, Key: commandKey, Binding: binding, Result: result, Policy: "enabled", Cause: result, Revision: 1}
	if classify(good, true, true) != "applied" {
		t.Fatal("bad applied control")
	}
	for _, field := range []string{"key", "binding", "result", "event", "policy"} {
		r := good
		switch field {
		case "key":
			r.After.Key = "different-command"
		case "binding":
			r.After.Binding = "policy=disabled;revision=1"
		case "result":
			r.After.Result = "different-result"
		case "event":
			r.After.Events++
		case "policy":
			r.After.Revision++
		}
		if classify(r, true, true) != "unknown" {
			t.Fatal("inconsistent immutable evidence confirmed", field)
		}
	}
}
