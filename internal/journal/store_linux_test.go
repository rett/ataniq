package journal

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func fixture(t *testing.T) (string, Options) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "journal ?#%.db"), Options{Authority: "local-authority"}
}

func mustCreate(t *testing.T, path string, o Options) *Store {
	t.Helper()
	s, err := Create(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func mustInfo(t *testing.T, s *Store) Info {
	t.Helper()
	i, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestLifecycle(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	want := mustInfo(t, s)
	if len(want.StoreID) != 32 || len(want.IncarnationID) != 32 || want.StoreID == want.IncarnationID || want.Authority != o.Authority || want.SchemaVersion != 1 || want.SQLiteVersion != engineVersion || want.SQLiteSourceID != engineSource || want.PageSize != 4096 || want.PageCount != 6 || want.MaxBytes != 64<<20 {
		t.Fatalf("unexpected info: %+v", want)
	}
	if s.db.Stats().MaxOpenConnections != 1 {
		t.Fatal("unbounded pool")
	}
	if _, err := Create(context.Background(), path, o); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Info(context.Background()); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed info: %v", err)
	}
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := mustInfo(t, s); got != want {
		t.Fatalf("identity changed: %+v != %+v", got, want)
	}
	other := mustCreate(t, filepath.Join(filepath.Dir(path), "other.db"), o)
	if got := mustInfo(t, other); got.StoreID == want.StoreID || got.IncarnationID == want.IncarnationID {
		t.Fatal("reused random identity")
	}
}

func TestExistingOnlyAndCancellation(t *testing.T) {
	path, o := fixture(t)
	if _, err := Open(context.Background(), path, o); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing open: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Open created file")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, fn := range []func(context.Context, string, Options) (*Store, error){Create, Open} {
		if _, err := fn(ctx, path, o); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled Create created file")
	}
	s := mustCreate(t, path, o)
	s.Close()
	before := fileBytes(t, path)
	if _, err := Open(ctx, path, o); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !bytes.Equal(before, fileBytes(t, path)) {
		t.Fatal("cancelled open modified file")
	}
}

func TestOptions(t *testing.T) {
	for _, o := range []Options{
		{}, {Authority: "has space"}, {Authority: strings.Repeat("x", 257)},
		{Authority: "a", MaxBytes: -1}, {Authority: "a", MaxBytes: 4096}, {Authority: "a", MaxBytes: 65537}, {Authority: "a", MaxBytes: 2 << 30},
		{Authority: "a", OperationTimeout: -1}, {Authority: "a", OperationTimeout: 2 * time.Minute},
		{Authority: "a", BusyTimeout: -1}, {Authority: "a", BusyTimeout: 6 * time.Second}, {Authority: "a", BusyTimeout: time.Microsecond},
		{Authority: "a", OperationTimeout: time.Millisecond},
	} {
		path, _ := fixture(t)
		if _, err := Create(context.Background(), path, o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("bad options touched file")
		}
	}
	path, _ := fixture(t)
	o := Options{Authority: "opaque\x00\xff", MaxBytes: 128 << 10, BusyTimeout: 10 * time.Millisecond, OperationTimeout: time.Second}
	s := mustCreate(t, path, o)
	if mustInfo(t, s).MaxBytes != o.MaxBytes {
		t.Fatal("custom bound ignored")
	}
	s.Close()
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if mustInfo(t, s).Authority != o.Authority {
		t.Fatal("authority bytes changed")
	}
}

func TestOwnershipAndHardlinks(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	if _, err := Open(context.Background(), path, o); !errors.Is(err, ErrOwned) {
		t.Fatalf("same process owner: %v", err)
	}
	alias := filepath.Join(filepath.Dir(path), "alias.db")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), alias, o); !errors.Is(err, ErrOwned) {
		t.Fatalf("alias owner: %v", err)
	}
	s.Close()
	if _, err := Open(context.Background(), alias, o); err == nil {
		t.Fatal("accepted idle hardlink with ambiguous sidecars")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestPrivatePaths(t *testing.T) {
	for _, kind := range []string{"file symlink", "directory symlink", "public file", "public directory", "sidecar symlink", "public sidecar", "directory file", "relative", "unclean"} {
		t.Run(kind, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			s.Close()
			target := path
			switch kind {
			case "file symlink":
				target = filepath.Join(filepath.Dir(path), "link.db")
				if err := os.Symlink(path, target); err != nil {
					t.Fatal(err)
				}
			case "directory symlink":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(filepath.Dir(path), link); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(link, filepath.Base(path))
			case "public file":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "public directory":
				if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
			case "sidecar symlink":
				if err := os.Symlink(path, path+"-wal"); err != nil {
					t.Fatal(err)
				}
			case "public sidecar":
				if err := os.WriteFile(path+"-wal", nil, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory file":
				target = filepath.Dir(path)
			case "relative":
				target = "relative.db"
			case "unclean":
				target = filepath.Dir(path) + "/../" + filepath.Base(filepath.Dir(path)) + "/" + filepath.Base(path)
			}
			before := fileBytes(t, path)
			if _, err := Open(context.Background(), target, o); err == nil {
				t.Fatal("accepted unsafe path")
			}
			if !bytes.Equal(before, fileBytes(t, path)) {
				t.Fatal("refusal changed bytes")
			}
			if kind == "public directory" {
				info, _ := os.Stat(filepath.Dir(path))
				if info.Mode().Perm() != 0755 {
					t.Fatal("chmod directory")
				}
			}
			if kind == "public file" {
				info, _ := os.Stat(path)
				if info.Mode().Perm() != 0644 {
					t.Fatal("chmod file")
				}
			}
		})
	}
}

// fixtureDB bypasses the package only to manufacture narrowly scoped invalid
// databases. It is never used against anything except a fresh test directory.
func fixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if _, err := sqlite.OFDLocking(true); err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	db := sql.OpenDB(&fixtureConnector{dsn: u.String()})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

type fixtureConnector struct {
	driver sqlite.Driver
	dsn    string
}

func (c *fixtureConnector) Driver() driver.Driver { return &c.driver }
func (c *fixtureConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.driver.Open(c.dsn)
}

func execFixture(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Fatal(err)
	}
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRefusalDoesNotMutate(t *testing.T) {
	for _, fault := range []string{"authority", "authority WAL", "schema version", "old schema", "lifecycle-only schema", "application", "missing identity", "extra identity", "zero identity", "foreign table", "changed schema", "empty", "garbage"} {
		t.Run(fault, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			s.Close()
			db := fixtureDB(t, path)
			if fault != "authority WAL" {
				execFixture(t, db, "PRAGMA journal_mode=DELETE")
			}
			switch fault {
			case "authority", "authority WAL":
				o.Authority = "wrong-authority"
			case "schema version":
				execFixture(t, db, "PRAGMA user_version=2")
			case "old schema":
				execFixture(t, db, "PRAGMA user_version=0")
			case "lifecycle-only schema":
				for _, name := range []string{"events", "evidence", "grants", "attempts"} {
					execFixture(t, db, "DROP TABLE "+name)
				}
			case "application":
				execFixture(t, db, "PRAGMA application_id=0")
			case "missing identity":
				execFixture(t, db, "DELETE FROM journal_identity")
			case "extra identity":
				execFixture(t, db, "PRAGMA ignore_check_constraints=ON")
				execFixture(t, db, "INSERT INTO journal_identity SELECT 2, authority, store_id, incarnation_id FROM journal_identity")
			case "zero identity":
				execFixture(t, db, "UPDATE journal_identity SET store_id=zeroblob(16)")
			case "foreign table":
				execFixture(t, db, "CREATE TABLE foreign_data (value TEXT)")
			case "changed schema":
				execFixture(t, db, "ALTER TABLE journal_identity ADD COLUMN extra TEXT")
			}
			db.Close()
			if fault == "empty" || fault == "garbage" {
				b := []byte(nil)
				if fault == "garbage" {
					b = []byte("not a database")
				}
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := fileBytes(t, path)
			if _, err := Open(context.Background(), path, o); err == nil {
				t.Fatal("accepted invalid state")
			}
			if !bytes.Equal(before, fileBytes(t, path)) {
				t.Fatal("refusal changed database bytes")
			}
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				// SQLite mode=ro may create an empty WAL and shared-memory index
				// for a WAL database. Neither changes main bytes or journal mode.
				if fault == "authority WAL" && suffix != "-journal" {
					continue
				}
				if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("refusal created sidecar %s: %v", suffix, err)
				}
			}
			if fault == "authority WAL" {
				if info, err := os.Stat(path + "-wal"); err != nil || info.Size() != 0 {
					t.Fatalf("refusal wrote WAL: %v", err)
				}
			}
			if fault != "garbage" && fault != "empty" {
				db = fixtureDB(t, path)
				var mode string
				wantMode := "delete"
				if fault == "authority WAL" {
					wantMode = "wal"
				}
				if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != wantMode {
					t.Fatalf("mode %s: %v", mode, err)
				}
				db.Close()
			}
		})
	}
}

func TestCancelledOpenWhileSQLiteBusy(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	s.Close()
	db := fixtureDB(t, path)
	execFixture(t, db, "PRAGMA journal_mode=DELETE")
	execFixture(t, db, "BEGIN EXCLUSIVE")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if s, err := Open(ctx, path, o); err == nil {
		s.Close()
		t.Fatal("opened locked database")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled busy open: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("unbounded busy wait")
	}
	execFixture(t, db, "ROLLBACK")
	db.Close()
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestInfoCancellationAndClose(t *testing.T) {
	path, o := fixture(t)
	o.OperationTimeout = 300 * time.Millisecond
	s := mustCreate(t, path, o)
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := s.Info(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pool wait: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("unbounded wait")
	}
	conn.Close()
	var wg sync.WaitGroup
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Info(context.Background())
			if err != nil && !errors.Is(err, os.ErrClosed) {
				t.Error(err)
			}
		}()
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestPhysicalReplacementAndLimits(t *testing.T) {
	path, o := fixture(t)
	o.MaxBytes = 64 << 10
	s := mustCreate(t, path, o)
	want := mustInfo(t, s)
	for n := 0; n < 3; n++ {
		conn, err := s.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
			t.Fatal(err)
		}
		conn.Close()
		if got := mustInfo(t, s); got != want {
			t.Fatal("replacement changed info")
		}
		var pages, busy, syncMode int
		if err := s.db.QueryRow("SELECT (SELECT max_page_count FROM pragma_max_page_count), (SELECT timeout FROM pragma_busy_timeout), (SELECT synchronous FROM pragma_synchronous)").Scan(&pages, &busy, &syncMode); err != nil {
			t.Fatal(err)
		}
		if pages != 16 || busy != 250 || syncMode != 2 {
			t.Fatalf("replacement settings %d %d %d", pages, busy, syncMode)
		}
	}
	// Exercise SQLite's actual page ceiling in a rollback-only fixture mutation.
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("CREATE TABLE page_bound_probe (data BLOB)"); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec("INSERT INTO page_bound_probe VALUES (zeroblob(131072))")
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 13 {
		t.Fatalf("expected SQLITE_FULL: %v", err)
	}
	tx.Rollback()
	if got := mustInfo(t, s); got != want {
		t.Fatalf("failed transaction changed identity: %+v", got)
	}
}

func TestCreateRefusesOrphanSidecars(t *testing.T) {
	path, o := fixture(t)
	if err := os.WriteFile(path+"-wal", []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), path, o); err == nil {
		t.Fatal("adopted orphan state")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created main file")
	}
	if got := string(fileBytes(t, path+"-wal")); got != "orphan" {
		t.Fatal(fmt.Sprintf("changed orphan: %q", got))
	}
}
