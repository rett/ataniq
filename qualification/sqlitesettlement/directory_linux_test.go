//go:build linux

package sqlitesettlement

import (
	"context"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
)

// Every early return deliberately leaves SQL and the inherited/alias directory
// descriptors live until TestHelperProcess exits. In particular no defer can
// turn a prerequisite refusal into an unrecorded checkpoint during Close.
func runLifecycle(ctx context.Context, path string, r receipt) receipt {
	r.Lifecycle = &lifecycleReceipt{Fresh: strings.HasPrefix(r.Mode, "directory-fresh")}
	l := r.Lifecycle
	failStop := func() receipt {
		l.FailStop = true
		r.record("disposable-exit-without-sql-close", nil)
		return r
	}
	o, err := acquireLifecycle(path, &r)
	r.record("acquire-directory", err)
	if err != nil {
		return failStop()
	}
	wal, err := namedIdentity(4, filepath.Base(path)+"-wal")
	if err != nil {
		r.record("initial-wal", err)
		return failStop()
	}
	l.WALAbsentBefore = !wal.Present
	if l.Fresh {
		shm, e := namedIdentity(4, filepath.Base(path)+"-shm")
		if e != nil || o.main.Size != 0 || wal.Present || shm.Present {
			r.record("fresh-exclusive-main", errors.New("fresh main/absent sidecar prerequisite"))
			return failStop()
		}
	} else if o.main.Size == 0 {
		r.Incomplete = "existing lifecycle requires initialized nonempty WAL main"
		return failStop()
	}
	c, err := openOwnedFixture(ctx, path, true, !l.Fresh)
	r.record("open-profile", err)
	if err != nil {
		return failStop()
	}
	r.ProfileVerified = true
	r.Engine, r.SourceID = engineVersion, engineSource
	r.Profile = verifiedProfile
	closeSQL := func() {
		l.CloseAttempted = true
		err := c.Close()
		r.record("physical-close", err)
		if err != nil {
			l.CloseError = err.Error()
		}
		r.Closed = err == nil
	}
	if r.Mode == "directory-reader" {
		_, err = c.ExecContext(ctx, "BEGIN", nil)
		r.record("reader-begin", err)
		if err != nil {
			return failStop()
		}
		r.Before, err = readState(ctx, c)
		r.BeforeRead = err == nil
		r.record("reader-snapshot", err)
		if err != nil {
			return failStop()
		}
		if err := pause(&r, "reader-pinned"); err != nil {
			r.record("pause", err)
			return failStop()
		}
		_, err = c.ExecContext(ctx, "ROLLBACK", nil)
		r.record("reader-release", err)
		closeSQL()
		return r
	}
	if l.Fresh {
		_, err = c.ExecContext(ctx, "BEGIN IMMEDIATE;"+schema+mutation+"COMMIT;", nil)
		r.record("initialization-commit", err)
		r.CommitOK = err == nil
		if err != nil {
			return failStop()
		}
		r.After, err = readState(ctx, c)
		r.AfterRead = err == nil
		r.record("read-after", err)
		if err != nil || !o.bind(&r) {
			return failStop()
		}
		if r.Mode == "directory-fresh-eio" && !o.denySelected(&r, "directory-alias") {
			return failStop()
		}
		if !o.boundary(&r, "init-complete", true) {
			return failStop()
		}
	} else {
		// Consumed read opens the actual WAL; binding refuses if it did not.
		_, err = readState(ctx, c)
		r.record("incarnation-read", err)
		if err != nil || !o.bind(&r) {
			return failStop()
		}
		if strings.HasPrefix(r.Mode, "directory-replace-") {
			if err := pause(&r, "identity-seam"); err != nil {
				r.record("pause", err)
				return failStop()
			}
			// Old-descriptor Sync can succeed despite replacement. This is a
			// control only; the following identity check must still refuse.
			r.record("retained-directory-sync-control", o.alias.Sync())
		}
		if r.Mode == "directory-pre-eio" && !o.denySelected(&r, "directory-alias") {
			return failStop()
		}
		if !o.boundary(&r, "pre-dirty", true) {
			return failStop()
		}
		r.Before, err = readState(ctx, c)
		r.BeforeRead = err == nil
		r.record("read-before", err)
		if err != nil {
			return failStop()
		}
		if r.Mode == "directory-reader-blocked" {
			if err := pause(&r, "before-reader"); err != nil {
				r.record("pause", err)
				return failStop()
			}
		}
		tx, err := c.BeginTx(ctx, driver.TxOptions{})
		r.record("begin", err)
		if err != nil {
			return failStop()
		}
		_, err = c.ExecContext(ctx, "UPDATE barrier SET epoch=epoch+1 WHERE id=1", nil)
		r.record("write", err)
		if err != nil {
			return failStop()
		}
		err = tx.Commit()
		r.record("commit", err)
		r.CommitOK = err == nil
		if err != nil {
			return failStop()
		}
		r.After, err = readState(ctx, c)
		r.AfterRead = err == nil
		r.record("read-after", err)
		if err != nil {
			return failStop()
		}
		if r.Mode == "directory-commit-death" {
			if err := pause(&r, "after-commit-before-directory"); err != nil {
				r.record("pause", err)
				return failStop()
			}
		}
		if r.Mode == "directory-live-eio" && !o.denySelected(&r, "directory-alias") {
			return failStop()
		}
		if !o.boundary(&r, "live", true) {
			return failStop()
		}
	}
	if !o.check(&r, "before-checkpoint", true) {
		return failStop()
	}
	switch r.Mode {
	case "directory-main-sync-eio":
		if !o.denySelected(&r, "sqlite-main") {
			return failStop()
		}
	case "directory-wal-sync-eio", "directory-hidden-close":
		if !denySync(&r, filepath.Dir(path)) {
			return failStop()
		}
	}
	if r.Mode != "directory-hidden-close" {
		l.Checkpoint, err = fullCheckpoint(ctx, c)
		r.record("checkpoint-full", err)
	}
	// No SQL reads, mutations, PRAGMAs or retries follow the checkpoint.
	if !o.check(&r, "before-close", true) {
		return failStop()
	}
	closeSQL()
	if r.Mode == "directory-close-error" {
		l.CloseError = "synthetic reported Close error after real physical Close"
		r.record("synthetic-close-report", errors.New(l.CloseError))
	}
	if !o.check(&r, "after-close", false) {
		return r
	}
	if !l.Checkpoint.complete() || l.CloseError != "" || !r.Closed {
		return r
	}
	if r.Mode == "directory-close-death" {
		if err := pause(&r, "after-close-before-directory"); err != nil {
			r.record("pause", err)
			return r
		}
	}
	if r.Mode == "directory-final-eio" && !o.denySelected(&r, "directory-alias") {
		return r
	}
	o.boundary(&r, "final", false)
	return r
}

func boundaryComplete(l *lifecycleReceipt, stage string) bool {
	for _, b := range l.Boundaries {
		if b.Stage == stage {
			return b.Before && b.Attempted && b.Synced && b.After && b.Error == "" && identityStage(l, stage+"-before", stage != "final") && identityStage(l, stage+"-after", stage != "final")
		}
	}
	return false
}

func identityStage(l *lifecycleReceipt, stage string, live bool) bool {
	for _, o := range l.Observations {
		if o.Stage == stage {
			return o.OK && o.Live == live
		}
	}
	return false
}

func stageIndex(r receipt, stage string) int {
	for i, s := range r.Steps {
		if s.Stage == stage {
			return i
		}
	}
	return -1
}

func orderedStages(r receipt, stages ...string) bool {
	last := -1
	for _, stage := range stages {
		i := stageIndex(r, stage)
		if i <= last {
			return false
		}
		last = i
	}
	return true
}

func fixedState(s state) bool {
	absent := state{Epoch: s.Epoch, Policy: "disabled"}
	applied := state{Commands: 1, Events: 1, Epoch: s.Epoch, Key: commandKey, Binding: binding, Result: result, Policy: "enabled", Cause: result, EventKey: commandKey, Revision: 1}
	return s.Epoch >= 0 && s.Epoch <= 1024 && (s == absent || s == applied)
}

// A completed live prefix is immutable even when a later checkpoint/Close fails.
// No mode name grants evidence; pure controls remove individual facts below.
func liveBoundary(r receipt) bool {
	l := r.Lifecycle
	if l == nil || l.Fresh || !r.ProfileVerified || !r.CommitOK || !r.BeforeRead || !r.AfterRead || !fixedState(r.Before) || !fixedState(r.After) {
		return false
	}
	expected := r.Before
	expected.Epoch++
	if r.After != expected || !boundaryComplete(l, "pre-dirty") || !boundaryComplete(l, "live") {
		return false
	}
	if !orderedStages(r, "bind-incarnation", "pre-dirty-before", "pre-dirty", "pre-dirty-after", "read-before", "begin", "write", "commit", "read-after", "live-before", "live", "live-after") {
		return false
	}
	for _, s := range r.Steps[:stageIndex(r, "live-after")+1] {
		if s.Error != "" {
			return false
		}
	}
	return boundEvidence(l, "live-after")
}

func boundEvidence(l *lifecycleReceipt, through string) bool {
	if l.MainFD < 0 || l.WALFD < 0 || l.MainFD == l.WALFD || l.DirectoryFD != 4 || l.AliasFD <= 4 || l.MainFD == l.AliasFD || l.WALFD == l.AliasFD || !regularIdentity(l.BoundMain) || !regularIdentity(l.BoundWAL) || !l.BoundDirectory.Present || !identityStage(l, "bind-incarnation", true) {
		return false
	}
	for _, o := range l.Observations {
		if !o.OK || !l.BoundDirectory.same(o.Directory) || !l.BoundDirectory.same(o.Alias) || !l.BoundDirectory.same(o.NamedDirectory) || !l.BoundMain.same(o.Main) || !l.BoundMain.same(o.NamedMain) {
			return false
		}
		if o.Live && (!l.BoundMain.same(o.SQLiteMain) || !l.BoundWAL.same(o.SQLiteWAL) || !l.BoundWAL.same(o.WAL) || o.MainFD != l.MainFD || o.WALFD != l.WALFD) {
			return false
		}
		if o.Stage == through {
			return true
		}
	}
	return false
}

func closedBoundary(r receipt) bool {
	l := r.Lifecycle
	if l == nil || !r.Waited || r.Incomplete != "" || l.FailStop || !r.Closed || !l.CloseAttempted || l.CloseError != "" || !l.Checkpoint.complete() || !boundaryComplete(l, "final") {
		return false
	}
	if l.Fresh {
		if !r.ProfileVerified || !r.CommitOK || !r.AfterRead || !fixedState(r.After) || !l.WALAbsentBefore || !boundaryComplete(l, "init-complete") || !orderedStages(r, "initialization-commit", "read-after", "bind-incarnation", "init-complete-before", "init-complete", "init-complete-after", "before-checkpoint") {
			return false
		}
	} else if !liveBoundary(r) || !orderedStages(r, "live-after", "before-checkpoint") {
		return false
	}
	if !orderedStages(r, "before-checkpoint", "checkpoint-full", "before-close", "physical-close", "after-close", "final-before", "final", "final-after") {
		return false
	}
	if !identityStage(l, "before-checkpoint", true) || !identityStage(l, "before-close", true) || !identityStage(l, "after-close", false) {
		return false
	}
	for _, s := range r.Steps {
		if s.Error != "" {
			return false
		}
	}
	return boundEvidence(l, "final-after")
}
