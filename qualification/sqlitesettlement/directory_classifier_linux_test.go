//go:build linux

package sqlitesettlement

import (
	"encoding/json"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// These are synthetic evidence controls, not additional native fault claims.
// Use the same accepted mode throughout; each refusal removes just one fact.
func syntheticLifecycleReceipt() receipt {
	main := fileIdentity{Present: true, Device: 1, Inode: 10, Mode: unix.S_IFREG | 0600, UID: uint32(os.Geteuid()), Links: 1}
	wal := main
	wal.Inode++
	dir := main
	dir.Inode++
	dir.Inode++
	dir.Mode = unix.S_IFDIR | 0700
	l := &lifecycleReceipt{WALAbsentBefore: true, CloseAttempted: true, MainFD: 9, WALFD: 10, DirectoryFD: 4, AliasFD: 8, BoundMain: main, BoundWAL: wal, BoundDirectory: dir,
		Checkpoint: checkpointReceipt{Attempted: true, Row: true, Integers: true, EOF: true, RowsClosed: true, Log: 1, Checkpointed: 1}}
	r := receipt{Mode: "directory-complete", ProfileVerified: true, CommitOK: true, Closed: true, Waited: true, BeforeRead: true, AfterRead: true, Before: state{Policy: "disabled"}, After: state{Policy: "disabled", Epoch: 1}, Lifecycle: l}
	for _, stage := range []string{"bind-incarnation", "pre-dirty-before", "pre-dirty", "pre-dirty-after", "read-before", "begin", "write", "commit", "read-after", "live-before", "live", "live-after", "before-checkpoint", "checkpoint-full", "before-close", "physical-close", "after-close", "final-before", "final", "final-after"} {
		r.record(stage, nil)
		// Only observation stages matter to the classifier; no metadata reads
		// or actual SQLite operations are performed by this synthetic fixture.
		live := stageIndex(r, "physical-close") < 0
		l.Observations = append(l.Observations, identityObservation{Stage: stage, Directory: dir, Alias: dir, NamedDirectory: dir, Main: main, NamedMain: main, WAL: wal, MainFD: 9, WALFD: 10, OK: true, Live: live, SQLiteMain: main, SQLiteWAL: wal})
	}
	for _, stage := range []string{"pre-dirty", "live", "final"} {
		l.Boundaries = append(l.Boundaries, directoryBoundary{Stage: stage, Before: true, Attempted: true, Synced: true, After: true})
	}
	return r
}

func cloneReceipt(t *testing.T, r receipt) receipt {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var result receipt
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDirectoryClassifierIncompleteFacts(t *testing.T) {
	good := syntheticLifecycleReceipt()
	if !liveBoundary(good) || !closedBoundary(good) {
		t.Fatal("bad synthetic positive control")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*receipt)
	}{
		{"profile", func(r *receipt) { r.ProfileVerified = false }},
		{"commit", func(r *receipt) { r.CommitOK = false }},
		{"before-read", func(r *receipt) { r.BeforeRead = false }},
		{"after-read", func(r *receipt) { r.AfterRead = false }},
		{"unchanged-epoch", func(r *receipt) { r.After.Epoch = r.Before.Epoch }},
		{"fixed-event", func(r *receipt) { r.Before.EventKey = "wrong"; r.After.EventKey = "wrong" }},
		{"fixed-capacity", func(r *receipt) { r.Before.CapacityRows = 1; r.After.CapacityRows = 1 }},
		{"changed-result", func(r *receipt) { r.After.Result = "wrong" }},
		{"main-binding", func(r *receipt) { r.Lifecycle.BoundMain.Present = false }},
		{"wal-binding", func(r *receipt) { r.Lifecycle.BoundWAL.Present = false }},
		{"directory-binding", func(r *receipt) { r.Lifecycle.BoundDirectory.Present = false }},
		{"descriptor-binding", func(r *receipt) { r.Lifecycle.MainFD = -1 }},
		{"directory-alias-binding", func(r *receipt) { r.Lifecycle.AliasFD = 4 }},
		{"missing-live-descriptor-observation", func(r *receipt) { r.Lifecycle.Observations[0].Live = false }},
		{"missing-pre-identity-observation", func(r *receipt) {
			r.Lifecycle.Observations = append(r.Lifecycle.Observations[:1], r.Lifecycle.Observations[2:]...)
		}},
		{"changed-wal", func(r *receipt) { r.Lifecycle.Observations[0].WAL.Inode++ }},
		{"changed-sqlite-wal", func(r *receipt) { r.Lifecycle.Observations[0].SQLiteWAL.Inode++ }},
		{"changed-main", func(r *receipt) { r.Lifecycle.Observations[0].NamedMain.Inode++ }},
		{"changed-directory", func(r *receipt) { r.Lifecycle.Observations[0].NamedDirectory.Inode++ }},
		{"missing-pre-sync", func(r *receipt) { r.Lifecycle.Boundaries = r.Lifecycle.Boundaries[1:] }},
		{"pre-sync-order", func(r *receipt) { r.Steps[2], r.Steps[6] = r.Steps[6], r.Steps[2] }},
		{"live-before-commit", func(r *receipt) { r.Steps[7], r.Steps[10] = r.Steps[10], r.Steps[7] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cloneReceipt(t, good)
			tc.mutate(&r)
			if liveBoundary(r) || closedBoundary(r) {
				t.Fatal("incomplete fact confirmed")
			}
		})
	}
	for index, stage := range []string{"pre-dirty", "live", "final"} {
		for _, field := range []string{"before", "attempted", "synced", "after", "error"} {
			t.Run(stage+"-"+field, func(t *testing.T) {
				r := cloneReceipt(t, good)
				b := &r.Lifecycle.Boundaries[index]
				switch field {
				case "before":
					b.Before = false
				case "attempted":
					b.Attempted = false
				case "synced":
					b.Synced = false
				case "after":
					b.After = false
				case "error":
					b.Error = "synthetic directory error"
				}
				if closedBoundary(r) || liveBoundary(r) != (stage == "final") {
					t.Fatal("independent directory evidence ignored")
				}
			})
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*receipt)
	}{
		{"checkpoint-absent", func(r *receipt) { r.Lifecycle.Checkpoint.Attempted = false }},
		{"checkpoint-row", func(r *receipt) { r.Lifecycle.Checkpoint.Row = false }},
		{"checkpoint-types", func(r *receipt) { r.Lifecycle.Checkpoint.Integers = false }},
		{"checkpoint-extra-row-or-next-error", func(r *receipt) { r.Lifecycle.Checkpoint.EOF = false }},
		{"checkpoint-rows-unclosed", func(r *receipt) { r.Lifecycle.Checkpoint.RowsClosed = false }},
		{"checkpoint-rows-close-error", func(r *receipt) { r.Lifecycle.Checkpoint.RowsCloseError = "synthetic error" }},
		{"checkpoint-error", func(r *receipt) { r.Lifecycle.Checkpoint.Error = "synthetic error" }},
		{"checkpoint-busy", func(r *receipt) { r.Lifecycle.Checkpoint.Busy = 1 }},
		{"checkpoint-negative", func(r *receipt) { r.Lifecycle.Checkpoint.Log = -1; r.Lifecycle.Checkpoint.Checkpointed = -1 }},
		{"checkpoint-zero", func(r *receipt) { r.Lifecycle.Checkpoint.Log = 0; r.Lifecycle.Checkpoint.Checkpointed = 0 }},
		{"checkpoint-partial", func(r *receipt) { r.Lifecycle.Checkpoint.Checkpointed = 0 }},
		{"checkpoint-order", func(r *receipt) { r.Steps[13], r.Steps[15] = r.Steps[15], r.Steps[13] }},
		{"close-missing", func(r *receipt) { r.Lifecycle.CloseAttempted = false }},
		{"close-error", func(r *receipt) { r.Lifecycle.CloseError = "synthetic error" }},
		{"physical-close", func(r *receipt) { r.Closed = false }},
		{"wait", func(r *receipt) { r.Waited = false }},
		{"fail-stop", func(r *receipt) { r.Lifecycle.FailStop = true }},
		{"incomplete", func(r *receipt) { r.Incomplete = "synthetic unsupported capability after live prefix" }},
		{"final-identity", func(r *receipt) { r.Lifecycle.Observations[len(r.Lifecycle.Observations)-1].OK = false }},
		{"final-missing", func(r *receipt) { r.Lifecycle.Boundaries = r.Lifecycle.Boundaries[:2] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cloneReceipt(t, good)
			tc.mutate(&r)
			if !liveBoundary(r) || closedBoundary(r) {
				t.Fatal("closed refusal lost immutable live boundary")
			}
		})
	}
	// An error followed by a successful final sync is still an error forever.
	r := cloneReceipt(t, good)
	r.Steps[13].Error = "synthetic failed checkpoint before later successful directory sync"
	if closedBoundary(r) || !liveBoundary(r) {
		t.Fatal("later successful sync erased checkpoint error")
	}
}

func TestDescriptorSelectorRefusesAmbiguity(t *testing.T) {
	f := newUninitializedFixture(t)
	want, err := descriptorIdentity(int(f.owner.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	fd, err := selectSQLiteFD(want, nil)
	if err != nil {
		if os.Getenv("ATANIQ_SQLITE_SETTLEMENT_GATE") == "1" {
			t.Fatal("INCOMPLETE procfs", err)
		}
		t.Skip("INCOMPLETE procfs", err)
	}
	if fd != int(f.owner.Fd()) {
		t.Fatal("wrong unique descriptor")
	}
	alias, err := unix.FcntlInt(f.owner.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(alias) // this is our owner alias, never a SQLite fd
	if _, err := selectSQLiteFD(want, nil); err == nil {
		t.Fatal("ambiguous descriptor accepted")
	}
	if _, err := selectSQLiteFD(want, map[int]bool{fd: true, alias: true}); err == nil {
		t.Fatal("missing descriptor accepted")
	}
}
