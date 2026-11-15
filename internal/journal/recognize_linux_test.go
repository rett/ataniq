package journal

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/dispatch"
	"modernc.org/sqlite"
)

// Read every column of the small disposable fixtures, including schema,
// generations, binary evidence, grants and events. This deliberately does not
// call Open or Snapshot: Open would recover the guards being measured.
func recognitionState(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := make(map[string][][]any)
	for _, query := range []string{
		"PRAGMA application_id", "PRAGMA user_version", "PRAGMA journal_mode",
		"SELECT * FROM sqlite_schema ORDER BY name",
		"SELECT * FROM journal_identity ORDER BY singleton",
		"SELECT * FROM attempts ORDER BY id", "SELECT * FROM grants ORDER BY id",
		"SELECT * FROM evidence ORDER BY sequence", "SELECT * FROM events ORDER BY sequence",
	} {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, destinations := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				t.Fatal(err)
			}
			out[query] = append(out[query], values)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func readRecognitionState(t *testing.T, path string, o Options) (Info, map[string][][]any) {
	t.Helper()
	owner, err := acquire(path, false)
	if err != nil {
		t.Fatal(err)
	}
	o, _ = o.normalized()
	db := pool(&connector{driver: &sqlite.Driver{}, owner: owner, options: o, readOnly: true})
	ctx, cancel := context.WithTimeout(context.Background(), o.OperationTimeout)
	defer cancel()
	info, err := readInfo(ctx, db, o)
	if err != nil {
		t.Fatal(err)
	}
	state := recognitionState(t, db)
	if err := errors.Join(db.Close(), owner.close()); err != nil {
		t.Fatal(err)
	}
	return info, state
}

func TestRecognizePreservesCommittedStateAndRecovery(t *testing.T) {
	for _, state := range []string{"clean", "dirty-closed", "identity-wal", "dirty-wal"} {
		for _, missingSHM := range []bool{false, true} {
			name := state
			if missingSHM {
				name += "/reconstruct-shm"
			}
			t.Run(name, func(t *testing.T) {
				path, o := fixture(t)
				ctx := context.Background()
				if state == "identity-wal" {
					cmd, _ := childAtSeam(t, path, "after-commit")
					killAndWait(t, cmd)
				} else {
					s := mustCreate(t, path, o)
					if state == "dirty-closed" {
						i := testIntent()
						i.Grant = "synthetic-grant"
						a := register(t, s, i)
						if _, err := (dispatch.Coordinator{Journal: a, Boundary: &fakeSink{}}).Dispatch(ctx, i); err != nil {
							t.Fatal(err)
						}
					}
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					if state == "dirty-closed" {
						// Manufacture a checkpointed pending-recovery guard without
						// changing the released schema or original receipt/grant.
						db := fixtureDB(t, path)
						execFixture(t, db, "UPDATE attempts SET active=1,uncertain=0")
						if err := db.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if state == "dirty-wal" {
						killAndWait(t, guardedChild(t, path, "save-after"))
					}
				}
				main := fileBytes(t, path)
				if strings.HasSuffix(state, "wal") && len(fileBytes(t, path+"-wal")) <= 32 {
					t.Fatal("fixture lacks committed WAL frames")
				}
				want, before := readRecognitionState(t, path, o)
				if missingSHM {
					if err := os.Remove(path + "-shm"); err != nil && !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
				}
				// Both success and authority refusal must leave recovery untouched.
				wrong := o
				wrong.Authority += "-wrong"
				if got, err := Recognize(ctx, path, wrong); err == nil || got != (Info{}) {
					t.Fatal("authority refusal", got, err)
				}
				for n := 0; n < 2; n++ {
					got, err := Recognize(ctx, path, o)
					if err != nil || got != want {
						t.Fatal("recognition metadata", got, want, err)
					}
				}
				_, after := readRecognitionState(t, path, o)
				if !reflect.DeepEqual(before, after) || !bytes.Equal(main, fileBytes(t, path)) {
					t.Fatal("recognition changed main bytes or logical application state")
				}
				s, err := Open(ctx, path, o)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if got := mustInfo(t, s); got != want {
					t.Fatal("ordinary Open changed metadata", got, want)
				}
				if strings.HasPrefix(state, "dirty") {
					r := inspect(t, s, testIntent().ID)
					if r.Active || !r.Uncertain || !r.Prepared || len(r.Receipts) != 1 || r.Receipts[0].State != dispatch.Started {
						t.Fatal("ordinary recovery lost dirty evidence", r)
					}
					recovered := recognitionState(t, s.db)
					for _, query := range []string{"SELECT * FROM journal_identity ORDER BY singleton", "SELECT * FROM grants ORDER BY id", "SELECT * FROM evidence ORDER BY sequence", "SELECT * FROM sqlite_schema ORDER BY name"} {
						if !reflect.DeepEqual(before[query], recovered[query]) {
							t.Fatal("recovery lost original state", query)
						}
					}
					events := recovered["SELECT * FROM events ORDER BY sequence"]
					prior := before["SELECT * FROM events ORDER BY sequence"]
					if len(events) != len(prior)+1 || !reflect.DeepEqual(events[:len(prior)], prior) || events[len(prior)][2] != "recovered-uncertain" {
						t.Fatal("recovery event not preserved exactly", events)
					}
					i := r.Intent
					boundary := &fakeSink{}
					if result, err := (dispatch.Coordinator{Journal: activate(t, s, i), Boundary: boundary}).Dispatch(ctx, i); err != nil || result.State != dispatch.Uncertain || boundary.calls.Load() != 0 {
						t.Fatal("recovery replayed guarded attempt", result, err)
					}
				}
			})
		}
	}
}

func TestRecognizeAdmission(t *testing.T) {
	path, o := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, c := range []context.Context{ctx, expired} {
		if got, err := Recognize(c, path, o); !errors.Is(err, c.Err()) || got != (Info{}) {
			t.Fatal("pre-cancelled recognition", got, err)
		}
	}
	if got, err := Recognize(context.Background(), path, Options{}); err == nil || got != (Info{}) {
		t.Fatal("invalid options accepted", got, err)
	}
	if _, err := Recognize(context.Background(), path, o); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing database", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 0 {
		t.Fatal("admission touched storage", entries, err)
	}
	s := mustCreate(t, path, o)
	if _, err := Recognize(context.Background(), path, o); !errors.Is(err, ErrOwned) {
		t.Fatal("active owner accepted", err)
	}
	s.Close()
	cmd, _ := childAtSeam(t, path, "hold")
	if _, err := Recognize(context.Background(), path, o); !errors.Is(err, ErrOwned) {
		t.Fatal("active subprocess accepted", err)
	}
	killAndWait(t, cmd)
	if _, err := Recognize(context.Background(), path, o); err != nil {
		t.Fatal("owner not released", err)
	}
}

func TestRecognizeConfiguredPageBound(t *testing.T) {
	path, o := fixture(t)
	o.Authority = "opaque\x00\xff"
	s := mustCreate(t, path, o)
	i := testIntent()
	i.Authority = o.Authority
	i.Payload = make([]byte, 96<<10)
	register(t, s, i)
	want := mustInfo(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	main := fileBytes(t, path)
	o.MaxBytes = 64 << 10
	if got, err := Recognize(context.Background(), path, o); err == nil || got != (Info{}) || !strings.Contains(err.Error(), "exceeds page bound") {
		t.Fatal("oversized recognition", got, err)
	}
	// MaxBytes describes the caller's accepted bound, without persisting it.
	o.MaxBytes = 512 << 10
	want.MaxBytes = o.MaxBytes
	got, err := Recognize(context.Background(), path, o)
	if err != nil || got != want {
		t.Fatal("configured recognition metadata", got, want, err)
	}
	if !bytes.Equal(main, fileBytes(t, path)) {
		t.Fatal("page bound changed main bytes")
	}
}

func TestRecognizeBusyDeadline(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		path, o := fixture(t)
		mustCreate(t, path, o).Close()
		db := fixtureDB(t, path)
		execFixture(t, db, "PRAGMA journal_mode=DELETE")
		execFixture(t, db, "BEGIN EXCLUSIVE")
		ctx := context.Background()
		if callerDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
			defer cancel()
		} else {
			o.OperationTimeout = 30 * time.Millisecond
			o.BusyTimeout = 30 * time.Millisecond
		}
		start := time.Now()
		if got, err := Recognize(ctx, path, o); !errors.Is(err, context.DeadlineExceeded) || got != (Info{}) {
			t.Fatal("busy recognition", got, err)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("unbounded recognition")
		}
		execFixture(t, db, "ROLLBACK")
		db.Close()
		if _, err := Recognize(context.Background(), path, o); err != nil {
			t.Fatal("deadline leaked ownership", err)
		}
	}
}

// Wrap the real driver to observe the DSN, local settings and SQL-close order,
// and inject failures or path changes without global hooks or an API bypass.
type recognitionDriver struct {
	driver.Driver
	wrap func(driver.Conn) driver.Conn
}

func (d recognitionDriver) Open(dsn string) (driver.Conn, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if q.Get("mode") != "ro" || q.Has("immutable") || q.Has("readonly_shm") || q.Has("vfs") || q.Has("_pragma") {
		return nil, errors.New("unexpected recognition DSN")
	}
	c, err := d.Driver.Open(dsn)
	if err != nil {
		return nil, err
	}
	return d.wrap(c), nil
}

type recognitionConn struct {
	driver.Conn
	sqlite.ExecQuerierContext
	close func() error
	query func(string) error
}

func (c *recognitionConn) Close() error { return errors.Join(c.Conn.Close(), c.close()) }

func (c *recognitionConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, "PRAGMA ") || strings.HasPrefix(q, "PRAGMA journal_mode=") || strings.HasPrefix(q, "PRAGMA max_page_count=") || q == "PRAGMA query_only=0" {
		return nil, errors.New("recognition attempted a writer statement")
	}
	return c.ExecQuerierContext.ExecContext(ctx, q, args)
}

func (c *recognitionConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.query(q); err != nil {
		return nil, err
	}
	return c.ExecQuerierContext.QueryContext(ctx, q, args)
}

func TestRecognizeConnectorAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "query", "configuration", "close", "query-and-close", "cancel-at-close", "replace-at-close", "directory-at-close"} {
		t.Run(mode, func(t *testing.T) {
			path, o := fixture(t)
			mustCreate(t, path, o).Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			queryErr, closeErr := errors.New("synthetic query failure"), errors.New("synthetic SQL close failure")
			closed, readOnly := 0, false
			d := recognitionDriver{Driver: &sqlite.Driver{}, wrap: func(raw driver.Conn) driver.Conn {
				c := &recognitionConn{Conn: raw, ExecQuerierContext: raw.(sqlite.ExecQuerierContext)}
				c.query = func(q string) error {
					if q == "PRAGMA application_id" {
						if err := expect(ctx, c.ExecQuerierContext, "PRAGMA query_only", "1"); err != nil {
							return err
						}
						readOnly = true
						if strings.HasPrefix(mode, "query") {
							return queryErr
						}
					}
					if mode == "configuration" && q == "PRAGMA synchronous" {
						return queryErr
					}
					return nil
				}
				c.close = func() error {
					closed++
					if other, err := acquire(path, false); !errors.Is(err, ErrOwned) {
						if other != nil {
							other.close()
						}
						t.Errorf("SQL closed without original ownership: %v", err)
					}
					switch mode {
					case "close", "query-and-close":
						return closeErr
					case "cancel-at-close":
						cancel()
					case "replace-at-close":
						if err := os.Rename(path, path+".original"); err != nil {
							return err
						}
						return os.WriteFile(path, nil, 0600)
					case "directory-at-close":
						dir := filepath.Dir(path)
						if err := os.Rename(dir, dir+".original"); err != nil {
							return err
						}
						if err := os.Mkdir(dir, 0700); err != nil {
							return err
						}
						// Restore the same main inode under a replaced parent.
						return os.Rename(filepath.Join(dir+".original", filepath.Base(path)), path)
					}
					return nil
				}
				return c
			}}
			got, err := recognizeWithDriver(ctx, path, o, d)
			if closed != 1 || (mode != "configuration" && !readOnly) {
				t.Fatal("connection verification/cleanup missing", closed, readOnly)
			}
			if mode == "success" {
				if err != nil || got.SchemaVersion != 1 {
					t.Fatal(got, err)
				}
			} else if err == nil || got != (Info{}) {
				t.Fatal("failure returned recognition", got, err)
			}
			if (strings.HasPrefix(mode, "query") || mode == "configuration") && !errors.Is(err, queryErr) {
				t.Fatal("lost query error", err)
			}
			if (mode == "close" || mode == "query-and-close") && !errors.Is(err, closeErr) {
				t.Fatal("lost SQL cleanup error", err)
			}
			if mode == "cancel-at-close" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation", err)
			}
			original := path
			if mode == "replace-at-close" {
				original += ".original"
			}
			owner, err := acquire(original, false)
			if err != nil {
				t.Fatal("recognition leaked ownership", err)
			}
			owner.close()
		})
	}
}

func TestRecognizeOwnedRetainsOwnershipAndReportsCleanup(t *testing.T) {
	path, o := fixture(t)
	mustCreate(t, path, o).Close()
	o, _ = o.normalized()
	owner, err := acquire(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	ctx, cancel := context.WithTimeout(context.Background(), o.OperationTimeout)
	defer cancel()
	for n := 0; n < 2; n++ {
		if _, err := recognizeOwned(ctx, owner, o, &sqlite.Driver{}); err != nil {
			t.Fatal(err)
		}
		if _, err := Recognize(ctx, path, o); !errors.Is(err, ErrOwned) {
			t.Fatal("helper released original ownership", err)
		}
	}
	// An invalidated owned directory is a real ownership check/close failure,
	// independent of the injected SQL close failures above.
	if err := owner.dir.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := recognizeOwned(ctx, owner, o, &sqlite.Driver{}); err == nil || got != (Info{}) {
		t.Fatal("invalidated owner accepted", got, err)
	}
}
