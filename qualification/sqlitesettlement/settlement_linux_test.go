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
	f.completed(t, mode, r)
	return r
}

func (f *fixture) completed(t *testing.T, mode string, r receipt) {
	t.Helper()
	logReceipt(t, r)
	if r.Mode != mode || !r.ProfileVerified || !r.Closed || !r.Waited {
		t.Fatal("helper did not complete the physical-connection protocol")
	}
	if err := checkFiles(f.path); err != nil {
		t.Fatal(err)
	}
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

func hasStep(r receipt, stage, message string, code int) bool {
	for _, s := range r.Steps {
		if s.Stage == stage && s.Error == message && s.SQLiteCode == code {
			return true
		}
	}
	return false
}

func canceledCommit(r receipt) bool {
	return !r.CommitOK && hasStep(r, "commit", context.Canceled.Error(), 0)
}

func requireCanceledCommit(t *testing.T, r receipt) {
	t.Helper()
	if !canceledCommit(r) {
		t.Fatalf("missing exact pre-Commit cancellation: %+v", r)
	}
}

// Preserve the native receipt and its log label. Normalize only this copy so
// refusal must rely on failed/missing evidence, not the helper's mode spelling.
func requireEvidenceRefusal(t *testing.T, r receipt) {
	t.Helper()
	r.Mode = "barrier"
	if classify(r, true, true) != "unknown" {
		t.Fatal("failed or missing evidence confirmed after mode normalization")
	}
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
	expected := r.Before
	expected.Epoch++
	if expected != r.After {
		return "unknown"
	}
	applied := state{
		Commands: 1, Events: 1, Epoch: r.After.Epoch, CapacityRows: 0,
		Key: commandKey, Binding: binding, Result: result,
		Policy: "enabled", Cause: result, EventKey: commandKey, Revision: 1,
	}
	if r.After == applied {
		return "applied"
	}
	absent := state{Epoch: r.After.Epoch, Policy: "disabled", EventKey: "", CapacityRows: 0}
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
		if r.CommitOK {
			t.Fatal("failed barrier reported commit success")
		}
		requireEvidenceRefusal(t, r)
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
					syncDependence = false
					return
				}
				requireCode(t, mutated, "commit", 1034)
				if mutated.CommitOK {
					t.Fatal("failed mutation reported commit success")
				}
			} else if !mutated.CommitOK {
				t.Fatal("normal mutation did not commit")
			}
			if mode == "mutation-lost" && !hasStep(mutated, "application-observation", "synthetic lost response AFTER successful driver Commit; not a sync fault", 0) {
				t.Fatal("missing synthetic application-observation stage")
			}
			// Direct recovery after waiting for the old writer. No intervening
			// unfiltered open/Close may checkpoint or sync the failed mutation.
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
			readback := f.run(t, "post-settlement-readback")
			assertClean(t, readback)
			if !readback.BeforeRead || !readback.AfterRead || readback.Before != barrier.After || readback.After != barrier.After {
				t.Fatal("post-settlement readback changed fixed-fixture evidence")
			}
		})
	}
	t.Run("visibility-only", func(t *testing.T) {
		// Separate fixture: this unfiltered snapshot Close cannot settle the
		// failed-mutation fixture before its recovery barrier.
		f := newFixture(t)
		assertClean(t, f.run(t, "mutation"))
		visible := f.run(t, "snapshot")
		assertClean(t, visible)
		requireEvidenceRefusal(t, visible)
	})
	t.Run("empty-is-not-a-barrier", func(t *testing.T) {
		f := newFixture(t)
		r := f.run(t, "empty-filter")
		if !syncAvailable(t, r) {
			syncDependence = false
			return
		}
		assertClean(t, r)
		if !r.CommitOK || r.Before != r.After {
			t.Fatal("empty transaction control failed")
		}
		requireEvidenceRefusal(t, r)
	})
	for _, mode := range []string{"barrier-cancel", "barrier-full"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			assertClean(t, f.run(t, "mutation"))
			r := f.run(t, mode)
			if mode == "barrier-full" {
				requireCode(t, r, "write", 13)   // SQLITE_FULL
				requireCode(t, r, "rollback", 1) // SQLite already rolled back.
			} else {
				requireCanceledCommit(t, r)
			}
			requireEvidenceRefusal(t, r)
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
		requireEvidenceRefusal(t, visible)
		// The harness fence admits no mutation/barrier until this writer is
		// cancelled AND waited. Cancellation alone is not a settlement receipt.
		settled := child.cancelWriter(t)
		requireCanceledCommit(t, settled)
		requireEvidenceRefusal(t, settled)
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
			if strings.HasPrefix(mode, "barrier") {
				wantEpoch := int64(1) // The fixture mutation warmed WAL once.
				if mode == "barrier-after-death" {
					wantEpoch++
				}
				if r.Before.Epoch != wantEpoch || r.After.Epoch != wantEpoch+1 {
					t.Fatal("barrier death did not preserve the expected commit epoch")
				}
			}
			if syncDependence && classify(r, child.waited, true) != want {
				t.Fatal("process-death recovery mismatch")
			}
		})
	}
	if syncDependence && !t.Failed() {
		t.Log("SUPPORTED: native pre-sync denial and dirty FULL commit sync dependence; tested relational settlement under SQLite/OS sync contract; new-WAL directory persistence unqualified; no power-loss claim")
	}
}

type pausedChild struct {
	fixture *fixture
	mode    string
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
	p := &pausedChild{fixture: f, mode: mode, cmd: cmd, cancel: cancel, stderr: &boundedOutput{}}
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
	wantStage := "before-commit"
	after := strings.Contains(mode, "after-death")
	if after {
		wantStage = "after-commit"
	}
	if r.Mode != mode || !r.ProfileVerified || r.Closed || r.Waited || r.CommitOK != after || len(r.Steps) == 0 || r.Steps[len(r.Steps)-1].Stage != wantStage {
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
	r.Waited = p.waited
	p.fixture.completed(t, p.mode, r)
	return r
}

func TestClassifierRefusesIncompleteEvidence(t *testing.T) {
	// Classifier-only synthetic controls: no native fault or interleaving is
	// injected by these receipts. Every negative starts in an accepted mode.
	good := receipt{Mode: "barrier", ProfileVerified: true, CommitOK: true, Closed: true, Waited: true, BeforeRead: true, AfterRead: true,
		Before: state{Policy: "disabled"}, After: state{Epoch: 1, Policy: "disabled"}}
	if classify(good, true, true) != "absent" {
		t.Fatal("bad classifier control")
	}
	for _, epoch := range []int64{-1, 1023, 1024} {
		r := good
		r.Before.Epoch, r.After.Epoch = epoch, epoch+1
		want := "unknown"
		if epoch == 1023 {
			want = "absent"
		}
		if classify(r, true, true) != want {
			t.Fatal("epoch range control failed despite stable increment", epoch)
		}
	}
	for _, failure := range []string{"rollback", "physical-close", "write", "commit"} {
		r := good
		r.Steps = []step{{Stage: failure, Error: "synthetic protocol error, not native fault injection"}}
		if classify(r, true, true) != "unknown" {
			t.Fatal("error confirmed", failure)
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*receipt)
	}{
		{"epoch-unchanged", func(r *receipt) { r.After.Epoch = r.Before.Epoch }},
		{"epoch-negative", func(r *receipt) { r.Before.Epoch = -1 }},
		{"epoch-exhausted", func(r *receipt) { r.Before.Epoch = 1024 }},
		{"commit", func(r *receipt) { r.CommitOK = false }},
		{"before-read", func(r *receipt) { r.BeforeRead = false }},
		{"after-read", func(r *receipt) { r.AfterRead = false }},
		{"closed", func(r *receipt) { r.Closed = false }},
		{"waited", func(r *receipt) { r.Waited = false }},
		{"profile", func(r *receipt) { r.ProfileVerified = false }},
		{"incomplete", func(r *receipt) { r.Incomplete = "synthetic missing capability" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := good
			tc.change(&r)
			if classify(r, true, true) != "unknown" {
				t.Fatal("incomplete synthetic evidence confirmed")
			}
		})
	}
	for _, oldWaited := range []bool{false, true} {
		if classify(good, oldWaited, false) != "unknown" {
			t.Fatal("missing sync evidence confirmed")
		}
	}
	if classify(good, false, true) != "unknown" {
		t.Fatal("unsettled writer confirmed")
	}
	partial := good
	partial.Before.Commands, partial.After.Commands = 1, 1
	if classify(partial, true, true) != "unknown" {
		t.Fatal("incomplete binding confirmed")
	}
	absent := good
	good.After = state{Commands: 1, Events: 1, Epoch: 1, Key: commandKey, Binding: binding, Result: result, Policy: "enabled", Cause: result, EventKey: commandKey, CapacityRows: 0, Revision: 1}
	good.Before = good.After
	good.Before.Epoch = 0
	if classify(good, true, true) != "applied" {
		t.Fatal("bad applied control")
	}
	for _, tc := range []struct {
		name          string
		before, after state
	}{
		{"absent-to-applied", absent.Before, good.After},
		{"applied-to-absent", good.Before, absent.After},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := good
			r.Before, r.After = tc.before, tc.after
			if classify(r, true, true) != "unknown" {
				t.Fatal("synthetic interleaving confirmed")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*state)
	}{
		{"command-count", func(s *state) { s.Commands++ }},
		{"key", func(s *state) { s.Key = "different-command" }},
		{"binding", func(s *state) { s.Binding = "policy=disabled;revision=1" }},
		{"result", func(s *state) { s.Result = "different-result" }},
		{"event-count", func(s *state) { s.Events++ }},
		{"event-key", func(s *state) { s.EventKey = "different-command" }},
		{"cause", func(s *state) { s.Cause = "different-cause" }},
		{"policy", func(s *state) { s.Policy = "different-policy" }},
		{"revision", func(s *state) { s.Revision++ }},
		{"capacity", func(s *state) { s.CapacityRows++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, control := range []receipt{good, absent} {
				r := control
				// Keep the barrier stable so fixed-fixture equality itself is
				// exercised, rather than rejection solely by the stability rule.
				tc.change(&r.Before)
				tc.change(&r.After)
				if classify(r, true, true) != "unknown" {
					t.Fatal("inconsistent fixed-fixture evidence confirmed")
				}
			}
		})
	}
}

func TestCanceledCommitProof(t *testing.T) {
	// Classifier-only controls for the exact cancellation receipt.
	good := receipt{Steps: []step{{Stage: "commit", Error: context.Canceled.Error()}}}
	if !canceledCommit(good) {
		t.Fatal("bad cancellation control")
	}
	for _, r := range []receipt{
		{},
		{Steps: []step{{Stage: "commit", Error: context.DeadlineExceeded.Error()}}},
		{Steps: []step{{Stage: "write", Error: context.Canceled.Error()}}},
		{Steps: []step{{Stage: "commit", Error: context.Canceled.Error(), SQLiteCode: 1}}},
		{CommitOK: true, Steps: good.Steps},
	} {
		if canceledCommit(r) {
			t.Fatalf("non-cancellation receipt accepted: %+v", r)
		}
	}
}
