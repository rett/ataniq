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
	"testing/synctest"
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

// sqliteBusy reports whether err is the engine's busy error: its busy
// handler gave up waiting for a lock.
func sqliteBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == 5
}

// TestRecognizeBusyDeadline: a database that another connection holds
// locked ends a recognition at a bound, with no Info, within time, and
// leaves no ownership behind. Which bound ends it is the case's:
//   - the caller's deadline, with the engine's busy wait and the operation's
//     timeout far beyond it: the context error, never the busy one;
//   - the busy timeout alone, the operation's timeout far beyond it: the
//     engine's busy error, never the context's;
//   - the busy timeout equal to the operation's timeout, the most that
//     Options allow (BusyTimeout may not exceed OperationTimeout): the two
//     bounds are one instant. The context's timer and the engine's busy
//     handler run on their own clocks and goroutines, so which of them ends
//     the wait first is not decided (under load the engine's gave up first
//     in about half the runs, with the context's timer not yet fired); either
//     is the end of the wait, and the case holds the result to be one of the
//     two, and the wait to be bounded.
func TestRecognizeBusyDeadline(t *testing.T) {
	const (
		ctxOK = 1 << iota
		busyOK
	)
	for _, tc := range []struct {
		name     string
		caller   time.Duration // the caller's deadline, or none
		op, busy time.Duration // Options, 0 for the defaults
		accept   int           // ctxOK, busyOK or both
	}{
		{"caller deadline", 30 * time.Millisecond, 5 * time.Second, 4 * time.Second, ctxOK},
		{"busy timeout alone", 0, 5 * time.Second, 30 * time.Millisecond, busyOK},
		{"operation timeout equal to the busy timeout", 0, 30 * time.Millisecond, 30 * time.Millisecond, ctxOK | busyOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, o := fixture(t)
			mustCreate(t, path, o).Close()
			db := fixtureDB(t, path)
			execFixture(t, db, "PRAGMA journal_mode=DELETE")
			execFixture(t, db, "BEGIN EXCLUSIVE")
			// The look for leaked ownership below uses the fixture's own
			// options: the case's short bounds would fail a healthy
			// recognition that a loaded host slows.
			reopenOptions := o
			o.OperationTimeout, o.BusyTimeout = tc.op, tc.busy
			ctx := context.Background()
			if tc.caller > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.caller)
				defer cancel()
			}
			start := time.Now()
			got, err := Recognize(ctx, path, o)
			if got != (Info{}) {
				t.Fatal("busy recognition returned an Info", got, err)
			}
			byCtx, byBusy := errors.Is(err, context.DeadlineExceeded), sqliteBusy(err)
			if !(byCtx && tc.accept&ctxOK != 0) && !(byBusy && !byCtx && tc.accept&busyOK != 0) {
				t.Fatalf("busy recognition ended with %v (context's deadline %v, engine's busy %v), accepted: context %v, busy %v",
					err, byCtx, byBusy, tc.accept&ctxOK != 0, tc.accept&busyOK != 0)
			}
			// Far below the bounds the case does not use.
			if time.Since(start) > 2*time.Second {
				t.Fatal("unbounded recognition")
			}
			execFixture(t, db, "ROLLBACK")
			db.Close()
			if _, err := Recognize(context.Background(), path, reopenOptions); err != nil {
				t.Fatal("deadline leaked ownership", err)
			}
		})
	}
}

// stalledConn is a connection whose first query after the connection is
// set up, the read of the application id, records the deadline of its
// context and then waits until the context ends, and reports the context's
// error and cause. noDeadline ends the wait, with errNoDeadline, when no
// deadline does.
type stalledConn struct {
	driver.Conn
	sqlite.ExecQuerierContext
	saw        chan stalledQuery
	noDeadline time.Duration
}

// stalledQuery is what the stalled query saw: its context's deadline, when
// it had one, and the error and cause it ended with.
type stalledQuery struct {
	deadline    time.Time
	hasDeadline bool
	err, cause  error
}

var errNoDeadline = errors.New("the query outlived every deadline")

func (c *stalledConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if q != "PRAGMA application_id" {
		return c.ExecQuerierContext.QueryContext(ctx, q, args)
	}
	var s stalledQuery
	s.deadline, s.hasDeadline = ctx.Deadline()
	select {
	case <-ctx.Done():
		s.err, s.cause = ctx.Err(), context.Cause(ctx)
	case <-time.After(c.noDeadline):
		s.err = errNoDeadline
	}
	c.saw <- s
	return nil, s.err
}

// TestRecognizeOperationDeadline: Recognize bounds its own work by
// OperationTimeout, counted from the operation's start. Nothing else can end
// the stalled query: the caller gives no deadline, no other connection holds
// a lock (so the engine's busy timeout has nothing to wait for), and the
// query waits on its context alone.
//
// The recognition runs in a synctest bubble, whose clock moves only when
// every goroutine of the bubble is blocked: the work before the query
// (ownership, connection, configuration) takes no time on it, however slowly
// the host runs it, and a pause the setup sleeps is counted exactly. The
// ownership's acquisition is given such a pause, acquireTakes, so that the
// operation's start is located: a budget started after the ownership was
// acquired would end acquireTakes late. So the query's deadline is checked
// to be exactly OperationTimeout after the operation's start, and then the
// query is left to wait: the bubble's clock moves to the deadline, which
// ends it with context.DeadlineExceeded as its error and its cause, and
// Recognize returns that error with a zero Info.
// The ownership is then released: a recognition with the fixture's ordinary
// options, outside the bubble, succeeds.
func TestRecognizeOperationDeadline(t *testing.T) {
	path, o := fixture(t)
	mustCreate(t, path, o).Close()
	reopenOptions := o
	// Far longer than the setup: on the bubble's clock the setup takes only
	// the acquisition's pause, and this costs no real time.
	o.OperationTimeout = 20 * time.Second
	// The time the ownership's acquisition takes on the bubble's clock: a
	// slow host or file system, as the operation's budget must count it.
	const acquireTakes = 75 * time.Millisecond
	synctest.Test(t, func(t *testing.T) {
		saw := make(chan stalledQuery, 1)
		d := recognitionDriver{Driver: &sqlite.Driver{}, wrap: func(raw driver.Conn) driver.Conn {
			return &stalledConn{Conn: raw, ExecQuerierContext: raw.(sqlite.ExecQuerierContext), saw: saw, noDeadline: time.Hour}
		}}
		acquired := false
		slowAcquire := func(path string, create bool) (*ownership, error) {
			owner, err := acquire(path, create)
			time.Sleep(acquireTakes)
			acquired = err == nil
			return owner, err
		}
		start := time.Now()
		got, err := recognizeWithDriver(context.Background(), path, o, d, slowAcquire)
		if !acquired {
			t.Fatalf("the recognition did not acquire the ownership: %v", err)
		}
		var q stalledQuery
		select {
		case q = <-saw:
		default:
			t.Fatalf("the stalled query was never made: %v", err)
		}
		if !q.hasDeadline {
			t.Fatalf("the stalled query had no deadline (it ended with %v)", q.err)
		}
		if want := start.Add(o.OperationTimeout); !q.deadline.Equal(want) {
			t.Fatalf("the stalled query's deadline is %v after the operation's start, want OperationTimeout, %v", q.deadline.Sub(start), o.OperationTimeout)
		}
		if !errors.Is(q.err, context.DeadlineExceeded) || !errors.Is(q.cause, context.DeadlineExceeded) {
			t.Fatalf("the stalled query ended with %v (cause %v), want the operation deadline", q.err, q.cause)
		}
		if got != (Info{}) {
			t.Fatal("a recognition past its deadline returned an Info", got, err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the recognition ended with %v, want its operation deadline", err)
		}
		if ended := time.Since(start); ended != o.OperationTimeout {
			t.Fatalf("the recognition ended %v after its start, want at its deadline, %v", ended, o.OperationTimeout)
		}
	})
	if _, err := Recognize(context.Background(), path, reopenOptions); err != nil {
		t.Fatal("the deadline leaked ownership", err)
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
			got, err := recognizeWithDriver(ctx, path, o, d, acquire)
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
