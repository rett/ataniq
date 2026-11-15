//go:build linux

package sqlitesettlement

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"modernc.org/sqlite"
)

type step struct {
	Stage      string
	Error      string
	SQLiteCode int
}

type receipt struct {
	Mode, Architecture, Go, Engine, SourceID, Profile string
	Filesystem                                        int64
	Steps                                             []step
	Incomplete                                        string
	BeforeSync, AfterSync                             [2]string
	Before, After                                     state
	BeforeRead, AfterRead                             bool
	ProfileVerified, CommitOK, Closed, Waited         bool
}

func (r *receipt) record(stage string, err error) {
	s := step{Stage: stage}
	if err != nil {
		s.Error = err.Error()
		var sqlErr *sqlite.Error
		if errors.As(err, &sqlErr) {
			s.SQLiteCode = sqlErr.Code()
		}
	}
	r.Steps = append(r.Steps, s)
}

// Returning an error on overflow stops copying; the parent's deadline still
// kills and waits for a child that subsequently blocks on its output pipe.
type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64*1024 {
		return 0, errors.New("helper output exceeded 64 KiB")
	}
	return b.Buffer.Write(p)
}

func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("ATANIQ_SETTLEMENT_CHILD")
	if mode == "" {
		return
	}
	r := runHelper(mode, os.Getenv("ATANIQ_SETTLEMENT_FIXTURE"))
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func runHelper(mode, path string) (r receipt) {
	r = receipt{Mode: mode, Architecture: runtime.GOOS + "/" + runtime.GOARCH, Go: runtime.Version()}
	var filesystem unix.Statfs_t
	if err := unix.Statfs(path, &filesystem); err != nil {
		r.record("filesystem", err)
		return r
	}
	r.Filesystem = int64(filesystem.Type)
	// This is a hard per-file safety bound, not the SQLite capacity experiment.
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 2 * 1024 * 1024, Max: 2 * 1024 * 1024}); err != nil {
		r.record("file-limit", err)
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := openFixture(ctx, path)
	r.record("open-profile", err)
	if err != nil {
		return r
	}
	r.ProfileVerified = true
	r.Engine, r.SourceID = engineVersion, engineSource
	r.Profile = "native-default;OFD;WAL;FULL=2;fsync-default;page_size=4096;max_page_count=64;busy_timeout=100;locking=NORMAL;foreign_keys=1;trusted_schema=0;query_only=0;defensive=1;DQS=0;wal_autocheckpoint=1000"
	defer func() {
		err := c.Close()
		r.record("physical-close", err)
		r.Closed = err == nil
		r.record("file-bounds", checkFiles(path))
	}()
	if mode == "initialize" {
		_, err := c.ExecContext(ctx, "BEGIN IMMEDIATE;"+schema+"COMMIT;", nil)
		r.record("schema", err)
		return r
	}
	if mode == "mutation" || mode == "mutation-lost" || mode == "mutation-filter" || mode == "empty-filter" || mode == "barrier-open-filter" || mode == "barrier-open" {
		// Leave a populated WAL open so the target is not just a WAL-header sync.
		_, err := c.ExecContext(ctx, "BEGIN IMMEDIATE; UPDATE barrier SET epoch=epoch+1; COMMIT;", nil)
		r.record("warm-wal", err)
		if err != nil {
			return r
		}
	}
	r.Before, err = readState(ctx, c)
	r.BeforeRead = err == nil
	r.record("read-before", err)
	if err != nil {
		return r
	}
	if mode == "snapshot" || mode == "retry" {
		r.After = r.Before
		r.AfterRead = true
		return r
	}
	tx, err := c.BeginTx(ctx, driver.TxOptions{})
	r.record("begin", err)
	if err != nil {
		return r
	}
	statement := ""
	switch mode {
	case "mutation", "mutation-filter", "mutation-lost", "mutation-before-death", "mutation-after-death", "mutation-paused":
		statement = mutation
	case "barrier", "barrier-filter", "barrier-open", "barrier-open-filter", "barrier-before-death", "barrier-after-death", "barrier-cancel":
		statement = "UPDATE barrier SET epoch=epoch+1 WHERE id=1"
	case "barrier-full":
		// Explicit allocation exceeds max_page_count without host pressure. This
		// is a capacity-failure control, not a claim that every epoch needs space.
		statement = "UPDATE barrier SET epoch=epoch+1 WHERE id=1; INSERT INTO capacity VALUES(zeroblob(524288))"
	case "empty-filter":
	default:
		r.record("mode", errors.New("unknown helper mode"))
		r.record("rollback", tx.Rollback())
		return r
	}
	if statement != "" {
		_, err := c.ExecContext(ctx, statement, nil)
		r.record("write", err)
		if err != nil {
			r.record("rollback", tx.Rollback())
			r.After, err = readState(ctx, c)
			r.AfterRead = err == nil
			r.record("read-after", err)
			return r
		}
	}
	filtered := mode == "mutation-filter" || mode == "barrier-filter" || mode == "barrier-open-filter" || mode == "empty-filter"
	if filtered && !denySync(&r, filepath.Dir(path)) {
		r.record("rollback", tx.Rollback())
		return r
	}
	if mode == "mutation-before-death" || mode == "barrier-before-death" || mode == "mutation-paused" {
		if err := pause(&r, "before-commit"); err != nil {
			r.record("pause", err)
			r.record("rollback", tx.Rollback())
			return r
		}
	}
	if mode == "barrier-cancel" || mode == "mutation-paused" {
		cancel()
	}
	err = tx.Commit()
	// Preserve this stage before Close's checkpoint can encounter further EIO.
	r.record("commit", err)
	r.CommitOK = err == nil
	if mode == "mutation-lost" && err == nil {
		r.record("application-observation", errors.New("synthetic lost response AFTER successful driver Commit; not a sync fault"))
	}
	if mode == "mutation-after-death" || mode == "barrier-after-death" {
		if err := pause(&r, "after-commit"); err != nil {
			r.record("pause", err)
		}
	}
	// A fresh BEGIN/ROLLBACK diagnoses whether the physical connection accepts
	// a new transaction. It does not recover the driver's ignored rollback error.
	cleanup, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_, err = c.ExecContext(cleanup, "BEGIN IMMEDIATE", nil)
	r.record("settlement-begin", err)
	if err == nil {
		_, err = c.ExecContext(cleanup, "ROLLBACK", nil)
		r.record("settlement-rollback", err)
	}
	r.After, err = readState(cleanup, c)
	r.AfterRead = err == nil
	r.record("read-after", err)
	return r
}

// No subprocess is started below this point. The sentinel controls are on a
// disposable regular file, never a SQLite or SHM descriptor.
func denySync(r *receipt, dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, "sentinel"), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		r.record("sentinel-open", err)
		return false
	}
	defer f.Close()
	if _, err := f.Write([]byte("sync control")); err != nil {
		r.record("sentinel-write", err)
		return false
	}
	r.BeforeSync = [2]string{fmt.Sprint(unix.Fsync(int(f.Fd()))), fmt.Sprint(unix.Fdatasync(int(f.Fd())))}
	if r.BeforeSync != [2]string{"<nil>", "<nil>"} {
		r.Incomplete = "unfiltered sentinel sync failed"
		return false
	}
	stage, err := installSyncDenial()
	r.record(stage, err)
	if err != nil {
		r.Incomplete = "sync-denial capability: " + err.Error()
		return false
	}
	r.AfterSync = [2]string{fmt.Sprint(unix.Fsync(int(f.Fd()))), fmt.Sprint(unix.Fdatasync(int(f.Fd())))}
	if r.AfterSync != [2]string{unix.EIO.Error(), unix.EIO.Error()} {
		r.Incomplete = "filtered sentinel did not return EIO for both calls"
		return false
	}
	return true
}

// A pipe handshake, not a sleep, defines the crash/cancellation seam. The
// parent either kills and waits, or sends one byte to cancel the old writer.
func pause(r *receipt, stage string) error {
	r.record(stage, nil)
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		return err
	}
	var signal [1]byte
	_, err := io.ReadFull(os.Stdin, signal[:])
	return err
}
