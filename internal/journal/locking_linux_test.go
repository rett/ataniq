package journal

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// The child only tries a nonblocking whole-main-file record lock. It never
// writes data, and flock cannot cause the expected EACCES/EAGAIN result.
func TestMainFcntlProbeHelper(t *testing.T) {
	path := os.Getenv("ATANIQ_JOURNAL_FCNTL_PATH")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock)
	switch err {
	case syscall.EACCES, syscall.EAGAIN:
		fmt.Println("CONTENDED")
	case nil:
		lock.Type = syscall.F_UNLCK
		if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
			t.Fatal(err)
		}
		fmt.Println("ACQUIRED")
	default:
		t.Fatalf("unexpected fcntl error: %v", err)
	}
}

func lockChild(t *testing.T, test string, env ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+test+"$", "-test.timeout=12s")
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", test, err, output)
	}
	return string(output)
}

func expectMainLock(t *testing.T, path, want string) {
	t.Helper()
	got := lockChild(t, "TestMainFcntlProbeHelper", "ATANIQ_JOURNAL_FCNTL_PATH="+path)
	if !strings.HasPrefix(got, want+"\n") {
		t.Fatalf("main lock: want %s, got %s", want, got)
	}
}

func holdIdentityRead(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM journal_identity").Scan(&count); err != nil || count != 1 {
		t.Fatalf("identity read: count %d, error %v", count, err)
	}
	return tx
}

func TestStoreMainLockSurvivesDescriptorClose(t *testing.T) {
	t.Log(lockChild(t, "TestStoreLockModeHelper", "ATANIQ_JOURNAL_LOCK_CASE=store"))
}

func TestLegacyPOSIXRefusedBeforeMainOpen(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"open", "create", "acquire", "connector"} {
		t.Run(mode, func(t *testing.T) {
			t.Log(lockChild(t, "TestStoreLockModeHelper", "ATANIQ_JOURNAL_LOCK_CASE="+mode, "ATANIQ_JOURNAL_LOCK_PATH="+path))
		})
	}
}

func TestStoreLockModeHelper(t *testing.T) {
	mode := os.Getenv("ATANIQ_JOURNAL_LOCK_CASE")
	if mode == "" {
		return
	}
	if mode == "store" {
		path, o := fixture(t)
		s := mustCreate(t, path, o)
		tx := holdIdentityRead(t, s.db)
		expectMainLock(t, path, "CONTENDED")
		if _, err := Open(context.Background(), path, o); !errors.Is(err, ErrOwned) {
			t.Fatalf("competing Open: %v", err)
		}
		// No SQLite call between the rejected Open/close and either probe:
		// reacquiring a lost SQL lock would conceal this regression.
		expectMainLock(t, path, "CONTENDED")
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		expectMainLock(t, path, "CONTENDED")
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
		expectMainLock(t, path, "ACQUIRED")
		if _, err := Open(context.Background(), path, o); !errors.Is(err, ErrOwned) {
			t.Fatalf("owner flock must outlive SQL closure: %v", err)
		}
		return
	}

	// This is the first SQLite use in this fresh process. Do not change the
	// process-wide mode after a lock exists, even to manufacture a test fault.
	if _, err := sqlite.OFDLocking(false); err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("ATANIQ_JOURNAL_LOCK_PATH")
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	db := sql.OpenDB(&fixtureConnector{dsn: u.String()})
	defer db.Close()
	tx := holdIdentityRead(t, db)
	expectMainLock(t, path, "CONTENDED")
	o := Options{Authority: "local-authority"}
	var err error
	switch mode {
	case "open":
		_, err = Open(context.Background(), path, o)
	case "create":
		_, err = Create(context.Background(), path, o)
	case "acquire":
		_, err = acquire(path, false)
	case "connector":
		o, _ = o.normalized()
		// The mode refusal must precede even the ownership check. A connector
		// cannot safely open SQLite when an earlier caller froze POSIX mode.
		c := &connector{driver: &sqlite.Driver{}, options: o}
		_, err = c.Connect(context.Background())
	default:
		t.Fatalf("unknown case %q", mode)
	}
	if !errors.Is(err, sqlite.ErrOFDLockingTooLate) {
		t.Errorf("legacy POSIX constructor: %v", err)
	}
	expectMainLock(t, path, "CONTENDED")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	expectMainLock(t, path, "ACQUIRED")
}

// auditDriver keeps real SQLite connections and rows. It observes consumption
// through EOF and Close before any later configuration. The negative profile
// selects POSIX before the first SQLite open in a fresh process; this exercises
// the post-read refusal, not unsupported-filesystem hardware qualification.
type auditDriver struct {
	driver.Driver
	negative    bool
	connections []*auditConn
}

func (d *auditDriver) Open(dsn string) (driver.Conn, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	if u.Query().Has("_pragma") {
		return nil, errors.New("unexpected DSN PRAGMA before lock verification")
	}
	if d.negative {
		if _, err := sqlite.OFDLocking(false); err != nil {
			return nil, err
		}
	}
	raw, err := d.Driver.Open(dsn)
	if err != nil {
		return nil, err
	}
	c := &auditConn{Conn: raw, ExecQuerierContext: raw.(sqlite.ExecQuerierContext)}
	d.connections = append(d.connections, c)
	return c, nil
}

type auditConn struct {
	driver.Conn
	sqlite.ExecQuerierContext
	read, eof, rowsClosed, closed, configured bool
}

func (c *auditConn) Close() error {
	c.closed = true
	return c.Conn.Close()
}

func (c *auditConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, "PRAGMA busy_timeout=") {
		if !c.eof || !c.rowsClosed {
			return nil, errors.New("configuration before consumed schema read")
		}
		c.configured = true
	}
	return c.ExecQuerierContext.ExecContext(ctx, query, args)
}

func (c *auditConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query != "SELECT count(*) FROM main.sqlite_schema" && (!c.eof || !c.rowsClosed) {
		return nil, errors.New("query before consumed schema read")
	}
	rows, err := c.ExecQuerierContext.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if query == "SELECT count(*) FROM main.sqlite_schema" {
		c.read = true
		return &auditRows{Rows: rows, conn: c}, nil
	}
	return rows, nil
}

type auditRows struct {
	driver.Rows
	conn *auditConn
}

func (r *auditRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if err == io.EOF {
		r.conn.eof = true
	}
	return err
}

func (r *auditRows) Close() error {
	r.conn.rowsClosed = true
	return r.Rows.Close()
}

func TestOFDConnectionBoundary(t *testing.T) {
	for _, mode := range []string{"preflight", "writer", "negative-preflight", "negative-writer"} {
		t.Run(mode, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			s.Close()
			// Give the negative profile something that writer setup would mutate.
			db := fixtureDB(t, path)
			execFixture(t, db, "PRAGMA journal_mode=DELETE")
			db.Close()
			t.Log(lockChild(t, "TestOFDConnectionBoundaryHelper", "ATANIQ_JOURNAL_PROFILE_CASE="+mode, "ATANIQ_JOURNAL_LOCK_PATH="+path))
		})
	}
}

func TestOFDConnectionBoundaryHelper(t *testing.T) {
	mode := os.Getenv("ATANIQ_JOURNAL_PROFILE_CASE")
	if mode == "" {
		return
	}
	path := os.Getenv("ATANIQ_JOURNAL_LOCK_PATH")
	before := fileBytes(t, path)
	owner, err := acquire(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	o, _ := (Options{Authority: "local-authority"}).normalized()
	d := &auditDriver{Driver: &sqlite.Driver{}, negative: strings.HasPrefix(mode, "negative-")}
	db := pool(&connector{driver: d, owner: owner, options: o, readOnly: strings.HasSuffix(mode, "preflight")})
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if d.negative {
		err := db.PingContext(ctx)
		if err == nil || !strings.Contains(err.Error(), "OFD locking required after schema read") {
			t.Fatalf("negative profile: %v", err)
		}
		if len(d.connections) != 1 {
			t.Fatalf("opens: %d", len(d.connections))
		}
		c := d.connections[0]
		if !c.read || !c.eof || !c.rowsClosed || !c.closed || c.configured {
			t.Fatalf("negative profile escaped read/close boundary: %+v", c)
		}
		if !bytes.Equal(before, fileBytes(t, path)) {
			t.Fatal("negative profile changed main file")
		}
		return
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatal(err)
	}
	conn.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if len(d.connections) != 2 {
		t.Fatalf("expected actual physical replacement: %d", len(d.connections))
	}
	for _, c := range d.connections {
		if !c.read || !c.eof || !c.rowsClosed || !c.closed || !c.configured {
			t.Fatalf("unverified physical connection: %+v", c)
		}
	}
}
