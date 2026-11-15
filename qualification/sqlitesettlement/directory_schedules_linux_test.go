//go:build linux

package sqlitesettlement

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func lifecycleAvailable(t *testing.T, r receipt) bool {
	t.Helper()
	if r.Incomplete != "" {
		if os.Getenv("ATANIQ_SQLITE_SETTLEMENT_GATE") == "1" {
			t.Fatalf("INCOMPLETE directory lifecycle: %s", r.Incomplete)
		}
		t.Logf("INCOMPLETE directory lifecycle: %s", r.Incomplete)
		return false
	}
	return true
}

func requireClosedBoundary(t *testing.T, r receipt) {
	t.Helper()
	assertClean(t, r)
	if !closedBoundary(r) {
		t.Fatal("missing closed-boundary evidence")
	}
	// This positive case specifically qualifies observed default last-close
	// removal. Other layouts are recorded, never manually unlinked as recovery.
	l := r.Lifecycle
	last := l.Observations[len(l.Observations)-1]
	if last.WAL.Present {
		t.Fatal("positive default last-close removal not observed")
	}
}

func requireSelectedControls(t *testing.T, r receipt, target string) {
	t.Helper()
	s := r.Lifecycle.Selector
	if s == nil || s.Target != target || !s.Revalidated || s.FD == s.OtherFD || s.Before != [2]string{"<nil>", "<nil>"} || s.Denied != [2]string{unix.EIO.Error(), unix.EIO.Error()} || s.Unaffected != [2]string{"<nil>", "<nil>"} {
		t.Fatal("missing exact selector controls")
	}
}

func requireBoundaryEIO(t *testing.T, r receipt, stage string) {
	t.Helper()
	for _, b := range r.Lifecycle.Boundaries {
		if b.Stage == stage && b.Before && b.Attempted && !b.Synced && b.After && b.Error != "" {
			for _, s := range r.Steps {
				if s.Stage == stage && s.Error == "sync directory-sync-alias: input/output error" {
					return
				}
			}
		}
	}
	t.Fatal("missing actual checked directory Sync EIO", stage)
}

func TestDirectoryLifecycle(t *testing.T) {
	supported := true
	available := func(t *testing.T, r receipt) bool {
		if !lifecycleAvailable(t, r) {
			supported = false
			return false
		}
		return true
	}
	t.Run("fresh-main", func(t *testing.T) {
		f := newUninitializedFixture(t)
		r := f.run(t, "directory-fresh")
		if !available(t, r) {
			return
		}
		requireClosedBoundary(t, r)
		observer := f.run(t, "snapshot")
		assertClean(t, observer)
		if observer.After != r.After {
			t.Fatal("fresh observer fixed state")
		}
	})
	t.Run("new-wal-and-recreation", func(t *testing.T) {
		f := newFixture(t)
		assertClean(t, f.run(t, "mutation"))
		var previous receipt
		for i := 0; i < 2; i++ {
			r := f.run(t, "directory-complete")
			if !available(t, r) {
				return
			}
			requireClosedBoundary(t, r)
			if !liveBoundary(r) || !r.Lifecycle.WALAbsentBefore {
				t.Fatal("new WAL prerequisite/live receipt")
			}
			if i > 0 && (r.Before != previous.After || !r.Lifecycle.BoundMain.same(previous.Lifecycle.BoundMain) || !r.Lifecycle.BoundDirectory.same(previous.Lifecycle.BoundDirectory)) {
				t.Fatal("incarnation ownership/history changed")
			}
			// Filesystems may recycle the removed WAL's inode number. Absence
			// before reopen, followed by actual binding, proves recreation here.
			previous = r
		}
	})
	for _, mode := range []string{"directory-fresh-eio", "directory-pre-eio", "directory-live-eio", "directory-final-eio", "directory-wal-sync-eio", "directory-main-sync-eio", "directory-close-error", "directory-hidden-close"} {
		t.Run(mode, func(t *testing.T) {
			var f *fixture
			if mode == "directory-fresh-eio" {
				f = newUninitializedFixture(t)
			} else {
				f = newFixture(t)
				assertClean(t, f.run(t, "mutation"))
			}
			r := f.run(t, mode)
			if !available(t, r) {
				return
			}
			if closedBoundary(r) {
				t.Fatal("failed attempt confirmed")
			}
			switch mode {
			case "directory-fresh-eio", "directory-pre-eio", "directory-live-eio", "directory-final-eio":
				requireSelectedControls(t, r, "directory-alias")
				stage := map[string]string{"directory-fresh-eio": "init-complete", "directory-pre-eio": "pre-dirty", "directory-live-eio": "live", "directory-final-eio": "final"}[mode]
				requireBoundaryEIO(t, r, stage)
				if mode != "directory-final-eio" && (!r.Lifecycle.FailStop || r.Lifecycle.CloseAttempted || r.Closed || stageIndex(r, "physical-close") >= 0) {
					t.Fatal("failed prerequisite/boundary hid SQL Close")
				}
				if mode == "directory-pre-eio" && (r.CommitOK || stageIndex(r, "write") >= 0 || r.Lifecycle.Checkpoint.Attempted) {
					t.Fatal("work after failed pre-sync")
				}
			case "directory-wal-sync-eio":
				if !syncAvailable(t, r) {
					supported = false
					return
				}
				requireCode(t, r, "checkpoint-full", 1034)
			case "directory-main-sync-eio":
				requireSelectedControls(t, r, "sqlite-main")
				requireCode(t, r, "checkpoint-full", 1034)
			case "directory-close-error":
				if !r.Lifecycle.Checkpoint.complete() || !r.Closed || r.Lifecycle.CloseError == "" {
					t.Fatal("synthetic Close control")
				}
			case "directory-hidden-close":
				if !syncAvailable(t, r) {
					supported = false
					return
				}
				if !r.Closed || r.Lifecycle.Checkpoint.Attempted {
					t.Fatal("hidden close-time checkpoint control")
				}
			}
			wantLive := mode != "directory-fresh-eio" && mode != "directory-pre-eio" && mode != "directory-live-eio"
			if liveBoundary(r) != wantLive {
				t.Fatal("later failure rewrote live prefix, or incomplete prefix accepted")
			}
			failed, _ := json.Marshal(r)
			// f.run waited the failing writer. Keep that receipt permanently;
			// this new unfiltered incarnation is observational reconciliation.
			recovered := f.run(t, "directory-complete")
			if !available(t, recovered) {
				return
			}
			requireClosedBoundary(t, recovered)
			if r.AfterRead && recovered.Before != r.After {
				t.Fatal("fixed state changed during reconciliation")
			}
			again, _ := json.Marshal(r)
			if string(failed) != string(again) || closedBoundary(r) {
				t.Fatal("failed receipt overwritten")
			}
		})
	}
	t.Run("reader-blocked-checkpoint", func(t *testing.T) {
		f := newFixture(t)
		assertClean(t, f.run(t, "mutation"))
		writer := f.pause(t, "directory-reader-blocked")
		if !available(t, writer.receipt) {
			return
		}
		reader := f.pause(t, "directory-reader")
		if !available(t, reader.receipt) {
			return
		}
		r := writer.cancelWriter(t) // acknowledgement releases only the writer
		if !available(t, r) {
			return
		}
		if !r.Lifecycle.Checkpoint.Row || r.Lifecycle.Checkpoint.Busy != 1 || r.Lifecycle.Checkpoint.Log <= r.Lifecycle.Checkpoint.Checkpointed || closedBoundary(r) || !liveBoundary(r) {
			t.Fatal("reader did not prevent complete FULL checkpoint")
		}
		readDone := reader.cancelWriter(t)
		assertClean(t, readDone)
		if !readDone.Waited {
			t.Fatal("reader still live before recovery")
		}
		recovered := f.run(t, "directory-complete")
		if available(t, recovered) {
			requireClosedBoundary(t, recovered)
			if recovered.Before != r.After {
				t.Fatal("reader recovery changed state")
			}
		}
	})
	for _, mode := range []string{"directory-commit-death", "directory-close-death"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			child := f.pause(t, mode)
			if !available(t, child.receipt) {
				return
			}
			child.kill(t)
			r := child.receipt
			r.Waited = child.waited
			r.record("parent-wait-sigkill", nil)
			logReceipt(t, r)
			if closedBoundary(r) || liveBoundary(r) != (mode == "directory-close-death") {
				t.Fatal("missing crash boundary confirmed")
			}
			recovered := f.run(t, "directory-complete")
			if available(t, recovered) {
				requireClosedBoundary(t, recovered)
				if recovered.Before != r.After {
					t.Fatal("death recovery fixed state")
				}
			}
		})
	}
	for _, kind := range []string{"main", "directory", "wal"} {
		t.Run("replace-"+kind, func(t *testing.T) {
			f := newFixture(t)
			child := f.pause(t, "directory-replace-"+kind)
			if !available(t, child.receipt) {
				return
			}
			path := f.path
			if kind == "directory" {
				path = filepath.Dir(path)
			} else if kind == "wal" {
				path += "-wal"
			}
			old := path + ".retained"
			if err := os.Rename(path, old); err != nil {
				t.Fatal(err)
			}
			// Cleanup registered after the child cleanup must itself wait before
			// restoring names, including a fatal test exit at any later assertion.
			restored := false
			restore := func() {
				if restored {
					return
				}
				if !child.waited {
					child.cmd.Process.Kill()
					child.cmd.Wait()
					child.waited = true
				}
				if err := os.RemoveAll(path); err != nil {
					t.Error(err)
				}
				if err := os.Rename(old, path); err != nil {
					t.Error(err)
				}
				restored = true
			}
			t.Cleanup(restore)
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			// Decode/wait directly: ordinary completion checks inspect the
			// configured path, which is deliberately a replacement at this seam.
			if _, err := child.input.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			child.input.Close()
			var r receipt
			if err := child.decoder.Decode(&r); err != nil {
				t.Fatal(err)
			}
			if err := child.cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			child.waited = true
			child.cancel()
			r.Waited = true
			logReceipt(t, r)
			if !hasStep(r, "retained-directory-sync-control", "", 0) || !r.Lifecycle.FailStop || r.Lifecycle.CloseAttempted || r.CommitOK || r.Lifecycle.Checkpoint.Attempted || liveBoundary(r) || closedBoundary(r) {
				t.Fatal("replacement incorrectly confirmed or performed cleanup SQL")
			}
			if len(r.Lifecycle.Boundaries) != 1 || r.Lifecycle.Boundaries[0].Before || r.Lifecycle.Boundaries[0].Attempted {
				t.Fatal("identity failure did not stop boundary")
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "directory" && info.Size() != 0 {
				t.Fatal("replacement initialized")
			}
			if kind == "directory" {
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatal("replacement directory used")
				}
			}
			restore() // original namespace only after actual helper wait
			recovered := f.run(t, "directory-complete")
			if available(t, recovered) {
				requireClosedBoundary(t, recovered)
			}
		})
	}
	if supported && !t.Failed() {
		t.Log("SUPPORTED: bounded directory lifecycle; checked init/pre-dirty/live/final sync, native WAL/main checkpoint denial, immutable refusals, waited reconciliation; no power-loss claim")
	}
}

func TestDirectoryAcquisitionRefusal(t *testing.T) {
	f := newUninitializedFixture(t)
	dir := filepath.Dir(f.path)
	link := filepath.Join(dir, "symlink")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "missing"), f.path, link} {
		got, err := openPinnedDirectory(path)
		if err == nil {
			got.Close()
			t.Fatal("wrapper acquisition accepted missing/non-directory/symlink")
		}
	}
	t.Log("wrapper acquisition refusal only; no native SQLite directory-open interception")
}
