package ownerjournal

import (
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
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
	"modernc.org/sqlite"
)

// The errors of Open and of a closed store, found with errors.Is.
var (
	// ErrLocked says that the store is open elsewhere, in this process or
	// in another, or that another Open is creating it right now. Open
	// refuses at once and does not wait.
	ErrLocked = errors.New("ownerjournal: the store is open elsewhere")
	// ErrNotPrivate says that the file, its folder or a file SQLite keeps
	// beside it belongs to another user, can be reached by the group or by
	// others, or is not a plain file or folder.
	ErrNotPrivate = errors.New("ownerjournal: the file or its folder is not private")
	// ErrNewerSchema says that the store was written by a newer version of
	// this package. The file is left as it was.
	ErrNewerSchema = errors.New("ownerjournal: the store was written by a newer schema")
	// ErrDamaged says that the file is not a whole store: it is empty, cut
	// short, of another kind, or an entry in it is changed or missing.
	ErrDamaged = errors.New("ownerjournal: the store is damaged")
	// ErrClosed says that the store was closed.
	ErrClosed = errors.New("ownerjournal: the store is closed")
	// ErrUncertain says that a commit returned an error, so it is not known
	// whether the entries are in the file, and that the store refuses every
	// later call until it is closed and opened again. See the package
	// comment.
	ErrUncertain = errors.New("ownerjournal: a write may or may not have been made; reopen the store")
)

// applicationID marks a file as a store of this package ("ATOJ"), and
// schemaVersion is the version of the layout below. Both are in the header
// of the file.
const (
	applicationID = 0x41544f4a
	schemaVersion = 1
)

// newSuffix is added to the store's name while a new store is made. The
// file gets its real name only once it is whole.
const newSuffix = ".new"

// sidecars are the files SQLite keeps beside a database.
var sidecars = []string{"-wal", "-shm", "-journal"}

// The layout. Open compares the text of every statement with what the file
// holds, so a file with another layout is refused and not read. The
// triggers and the indexes hold the rules of the journal a second time,
// beside the code: nothing is changed or removed, one decision for a
// pending id, one delivery for a decision, one activity entry for what it
// belongs to, and one expiry for a held entry.
var schema = []struct{ kind, name, ddl string }{
	{"table", "entries", `CREATE TABLE entries (
 seq INTEGER PRIMARY KEY CHECK (seq >= 1),
 kind TEXT NOT NULL CHECK (kind IN ('decision','held','refused','delivered','outcome','expired','limit','use','alert','count','activity','looked')),
 at INTEGER NOT NULL,
 pending_id TEXT NOT NULL CHECK (length(pending_id) <= 64),
 project TEXT NOT NULL CHECK (length(project) <= 256),
 ref INTEGER NOT NULL CHECK (ref >= 0 AND ref < seq),
 body BLOB NOT NULL CHECK (length(body) BETWEEN 2 AND 4096),
 record BLOB CHECK (record IS NULL OR length(record) BETWEEN 2 AND 8192),
 sum BLOB NOT NULL CHECK (length(sum) = 32)
) STRICT`},
	{"index", "entries_by_id", `CREATE INDEX entries_by_id ON entries (project, pending_id, seq)`},
	{"index", "entries_by_kind", `CREATE INDEX entries_by_kind ON entries (kind, project, at)`},
	{"index", "entries_by_ref", `CREATE INDEX entries_by_ref ON entries (ref, kind) WHERE ref > 0`},
	{"index", "one_decision", `CREATE UNIQUE INDEX one_decision ON entries (project, pending_id) WHERE kind = 'decision'`},
	{"index", "one_delivered", `CREATE UNIQUE INDEX one_delivered ON entries (ref) WHERE kind = 'delivered'`},
	{"index", "one_activity", `CREATE UNIQUE INDEX one_activity ON entries (ref) WHERE kind = 'activity'`},
	{"index", "one_expired", `CREATE UNIQUE INDEX one_expired ON entries (ref) WHERE kind = 'expired'`},
	{"trigger", "entries_no_update", `CREATE TRIGGER entries_no_update BEFORE UPDATE ON entries BEGIN SELECT RAISE(ABORT, 'the journal is append-only'); END`},
	{"trigger", "entries_no_delete", `CREATE TRIGGER entries_no_delete BEFORE DELETE ON entries BEGIN SELECT RAISE(ABORT, 'the journal is append-only'); END`},
}

// Store is one open journal. Make one with [Open] and end it with
// [Store.Close]. Its methods may be called from several goroutines; they
// run one after the other. Do not copy a Store.
type Store struct {
	mu       sync.Mutex
	path     string
	dir      *os.File    // the folder, held open so that it can be synced and compared
	file     *os.File    // the store's file; its lock is what keeps a second Open out
	identity os.FileInfo // the file as it was when it was locked
	db       *sql.DB
	wal      atomic.Bool // set once the file is known to be a store; see verify
	closed   bool
	// uncertain is set when a commit returned an error; see [ErrUncertain].
	uncertain bool
	closeErr  error
	// fault is called before and after each step of a write, with the
	// step's name. It is nil outside tests. An error from it ends the
	// write as a failed step would.
	fault func(point string) error
}

// Open opens the store in the file at path, and makes a new, empty store
// when there is no such file. The path must be absolute and clean (no "."
// or ".." part, no doubled slash), and its last part must not be a symbolic
// link, nor must the folder's.
//
// It refuses with [ErrNotPrivate] a folder or a file that is not the
// user's own or that the group or others can reach, with [ErrLocked] a
// store that is open elsewhere, with [ErrNewerSchema] a store of a newer
// schema, and with [ErrDamaged] a file that is not a whole store. See the
// package comment for what it checks.
func Open(path string) (*Store, error) {
	return openWithFault(path, nil)
}

// openWithFault is Open with the fault hook of the tests.
func openWithFault(path string, fault func(string) error) (_ *Store, err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return nil, errors.New("ownerjournal: the path must be absolute and clean")
	}
	// The store keeps a descriptor of its own on the file, for the lock.
	// With the locks SQLite uses by default, closing any descriptor of a
	// file drops SQLite's locks on it; with these they stay.
	if err = requireOFDLocking(); err != nil {
		return nil, err
	}
	s := &Store{path: path, fault: fault}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.release())
		}
	}()
	if err = s.openDir(); err != nil {
		return nil, err
	}
	if err = s.acquire(); err != nil {
		return nil, err
	}
	s.db = sql.OpenDB(&connector{path: path, check: s.checkPath, wal: &s.wal})
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	if err = s.verify(); err != nil {
		return nil, err
	}
	if err = s.checkPath(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close ends the hold on the file. Calling it again returns what the first
// call returned. Every other method of a closed store returns [ErrClosed].
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	s.closeErr = s.release()
	return s.closeErr
}

// release closes SQLite first and the locked descriptor after it, so that
// the file is never open for writing without the lock.
func (s *Store) release() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.file != nil {
		err = errors.Join(err, s.file.Close())
	}
	if s.dir != nil {
		err = errors.Join(err, s.dir.Close())
	}
	return err
}

// hit calls the fault hook of the tests, when there is one.
func (s *Store) hit(point string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(point)
}

// private reports whether info is the user's own and closed to the group
// and to others.
func private(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0o077 == 0
}

// privateFile reports whether info is a plain file of the user with mode
// 0600 and one name.
func privateFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && private(info) && info.Mode().Perm() == 0o600 &&
		info.Sys().(*syscall.Stat_t).Nlink == 1
}

func (s *Store) openDir() error {
	name := filepath.Dir(s.path)
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("ownerjournal: the folder: %w", err)
	}
	s.dir = os.NewFile(uintptr(fd), name)
	info, err := s.dir.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() || !private(info) || info.Mode().Perm()&0o700 != 0o700 {
		return fmt.Errorf("%w: the folder must be the user's own, with mode 0700", ErrNotPrivate)
	}
	return nil
}

// errRetry says that the file appeared while a new one was being made.
var errRetry = errors.New("ownerjournal: the file appeared meanwhile")

// acquire opens and locks the file, making a new store when there is none.
func (s *Store) acquire() error {
	base := filepath.Base(s.path)
	for attempt := 0; attempt < 3 && s.file == nil; attempt++ {
		fd, err := unix.Openat(int(s.dir.Fd()), base, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		switch {
		case errors.Is(err, unix.ENOENT):
			f, err := s.create()
			if errors.Is(err, errRetry) {
				continue
			}
			if err != nil {
				return err
			}
			s.file = f
		case errors.Is(err, unix.ELOOP):
			return fmt.Errorf("%w: the file is a symbolic link", ErrNotPrivate)
		case err != nil:
			return fmt.Errorf("ownerjournal: the file: %w", err)
		default:
			f := os.NewFile(uintptr(fd), s.path)
			if err := lockExisting(f); err != nil {
				return errors.Join(err, f.Close())
			}
			s.file = f
		}
	}
	if s.file == nil {
		return errRetry
	}
	info, err := s.file.Stat()
	if err != nil {
		return err
	}
	s.identity = info
	return s.checkPath()
}

// lockExisting checks a file that was there and takes its lock.
func lockExisting(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !private(info) || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%w: the file must be a plain file of the user with mode 0600", ErrNotPrivate)
	}
	if err := flock(f); err != nil {
		return err
	}
	// SQLite finds the files it keeps beside a database by the database's
	// name. A second name for the same file would have its own, and the
	// two would not agree.
	if info, err = f.Stat(); err != nil {
		return err
	}
	if info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return fmt.Errorf("%w: the file has a second name", ErrNotPrivate)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%w: the file is empty", ErrDamaged)
	}
	return nil
}

func flock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}

// create makes a new, empty store. It is built under another name, synced,
// and only then given its real name, in one step that fails when a file of
// that name is there. So the file at the store's path is always a whole
// store. The lock is taken on the new file before anything is written, and
// goes with the file when it is renamed.
func (s *Store) create() (_ *os.File, err error) {
	base := filepath.Base(s.path)
	dirfd := int(s.dir.Fd())
	if err = s.hit("before:create"); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(dirfd, base+newSuffix, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if errors.Is(err, unix.ELOOP) {
		return nil, fmt.Errorf("%w: %s is a symbolic link", ErrNotPrivate, base+newSuffix)
	}
	if err != nil {
		return nil, fmt.Errorf("ownerjournal: the new file: %w", err)
	}
	f := os.NewFile(uintptr(fd), s.path+newSuffix)
	mine := false // whether the file under the new name is this call's to remove
	defer func() {
		if err == nil {
			return
		}
		if mine {
			for _, suffix := range sidecars {
				os.Remove(s.path + newSuffix + suffix)
			}
			os.Remove(s.path + newSuffix)
		}
		f.Close()
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("%w: %s is not a plain file of the user", ErrNotPrivate, base+newSuffix)
	}
	if err = flock(f); err != nil {
		return nil, err
	}
	// With the lock held, the file is this call's only while it still has
	// the new name and no other: an Open that finished meanwhile has
	// renamed it, and it is then a store that must not be emptied.
	named, e := os.Lstat(s.path + newSuffix)
	if e != nil || !os.SameFile(named, info) {
		return nil, errRetry
	}
	if info, err = f.Stat(); err != nil {
		return nil, err
	}
	if info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil, fmt.Errorf("%w: %s has a second name", ErrNotPrivate, base+newSuffix)
	}
	mine = true
	if _, e := os.Lstat(s.path); e == nil {
		return nil, errRetry
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	// What an Open that crashed left under the new name is thrown away.
	for _, suffix := range sidecars {
		if e := os.Remove(s.path + newSuffix + suffix); e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
	}
	if err = f.Chmod(0o600); err != nil {
		return nil, err
	}
	if err = f.Truncate(0); err != nil {
		return nil, err
	}
	if err = s.hit("after:create"); err != nil {
		return nil, err
	}
	if err = s.hit("before:schema"); err != nil {
		return nil, err
	}
	if err = initialize(s.path + newSuffix); err != nil {
		return nil, fmt.Errorf("ownerjournal: the new store: %w", err)
	}
	if err = s.hit("after:schema"); err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	if err = s.hit("before:rename"); err != nil {
		return nil, err
	}
	err = unix.Renameat2(dirfd, base+newSuffix, dirfd, base, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EEXIST) {
		return nil, errRetry
	}
	if err != nil {
		return nil, fmt.Errorf("ownerjournal: naming the new store: %w", err)
	}
	mine = false
	// The new name is in the folder; make the folder durable too.
	if err = s.dir.Sync(); err != nil {
		return nil, err
	}
	if err = s.hit("after:rename"); err != nil {
		return nil, err
	}
	return f, nil
}

// initialize writes the layout into the empty file at path, in one
// transaction, and closes it again.
func initialize(path string) (err error) {
	db := sql.OpenDB(&connector{path: path})
	db.SetMaxOpenConns(1)
	defer func() { err = errors.Join(err, db.Close()) }()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, object := range schema {
		if _, err = tx.ExecContext(ctx, object.ddl); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		fmt.Sprintf("PRAGMA application_id=%d", applicationID),
		fmt.Sprintf("PRAGMA user_version=%d", schemaVersion),
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// checkPath checks that the path still names the file that was locked, in
// the folder that was opened, and that the files SQLite keeps beside it
// are private. SQLite opens the file by its path, so this is asked again
// around each time it does.
func (s *Store) checkPath() error {
	info, err := os.Lstat(s.path)
	if err != nil {
		return err
	}
	if s.identity != nil && !os.SameFile(info, s.identity) {
		return errors.New("ownerjournal: the path names another file than the one that was locked")
	}
	if !privateFile(info) {
		return fmt.Errorf("%w: the file must be a plain file of the user with mode 0600 and one name", ErrNotPrivate)
	}
	dir, err := os.Lstat(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	held, err := s.dir.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(dir, held) || !private(dir) {
		return fmt.Errorf("%w: the folder changed", ErrNotPrivate)
	}
	for _, suffix := range sidecars {
		info, err := os.Lstat(s.path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !privateFile(info) {
			return fmt.Errorf("%w: the file %s beside the store", ErrNotPrivate, filepath.Base(s.path)+suffix)
		}
	}
	return nil
}

// requireOFDLocking selects, for the whole process, the file locks that
// survive the closing of another descriptor. The dispatch journal selects
// the same; the choice cannot be changed once a database is open.
func requireOFDLocking() error {
	if _, err := sqlite.OFDLocking(true); err != nil {
		return fmt.Errorf("ownerjournal: the file locks the store needs: %w", err)
	}
	return nil
}

// connector opens the connections of one store and sets each of them up
// before it is used.
type connector struct {
	driver sqlite.Driver
	path   string
	check  func() error // asked before and after SQLite opens the path; may be nil
	// wal says whether a new connection sets the write-ahead mode. It is
	// nil while a new store is written, and false until the file was found
	// to be a store: setting the mode writes to the file.
	wal *atomic.Bool
}

func (c *connector) Driver() driver.Driver { return &c.driver }

func (c *connector) Connect(ctx context.Context) (_ driver.Conn, err error) {
	if err = requireOFDLocking(); err != nil {
		return nil, err
	}
	if c.check != nil {
		if err = c.check(); err != nil {
			return nil, err
		}
	}
	u := url.URL{Scheme: "file", Path: c.path}
	// mode=rw never creates a file. A transaction takes the write lock when
	// it begins. Defensive mode keeps SQL from rewriting the layout.
	u.RawQuery = url.Values{"mode": {"rw"}, "_defensive": {"true"}, "_dqs": {"false"}, "_txlock": {"immediate"}}.Encode()
	conn, err := c.driver.Open(u.String())
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, conn.Close())
		}
	}()
	raw, ok := conn.(sqlite.ExecQuerierContext)
	if !ok {
		return nil, errors.New("ownerjournal: the SQLite connection cannot be set up")
	}
	settings := []string{
		"PRAGMA busy_timeout=250",
		"PRAGMA trusted_schema=OFF",
		"PRAGMA foreign_keys=ON",
		// FULL: a commit returns only after its bytes were synced.
		"PRAGMA synchronous=FULL",
	}
	wal := c.wal != nil && c.wal.Load()
	if wal {
		settings = append(settings, "PRAGMA journal_mode=WAL")
	}
	for _, statement := range settings {
		if _, err = raw.ExecContext(ctx, statement, nil); err != nil {
			return nil, err
		}
	}
	want := []struct{ query, value string }{{"PRAGMA synchronous", "2"}}
	if wal {
		want = append(want, struct{ query, value string }{"PRAGMA journal_mode", "wal"})
	}
	for _, w := range want {
		got, err := scalar(ctx, raw, w.query)
		if err != nil {
			return nil, err
		}
		if got != w.value {
			return nil, fmt.Errorf("ownerjournal: %s is %s, expected %s", w.query, got, w.value)
		}
	}
	// SQLite falls back to the other kind of lock without an error when
	// the kernel or the file system refuses these; by now it has locked.
	if !sqlite.OFDLockingEnabled() {
		return nil, errors.New("ownerjournal: the file locks the store needs are not in use")
	}
	if c.check != nil {
		if err = c.check(); err != nil {
			return nil, err
		}
	}
	return conn, nil
}

// scalar runs a query that answers with one value and returns it as text.
func scalar(ctx context.Context, raw sqlite.ExecQuerierContext, query string) (_ string, err error) {
	rows, err := raw.QueryContext(ctx, query, nil)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if len(rows.Columns()) != 1 {
		return "", errors.New("ownerjournal: unexpected answer from SQLite")
	}
	values := make([]driver.Value, 1)
	if err = rows.Next(values); err != nil {
		return "", err
	}
	return fmt.Sprint(values[0]), nil
}

// damaged wraps an error of SQLite that says the file is not a database or
// is malformed, so that it is found as ErrDamaged. Other errors pass.
func damaged(err error) error {
	var e *sqlite.Error
	if errors.As(err, &e) {
		switch e.Code() & 0xff {
		case 11, 26: // SQLITE_CORRUPT, SQLITE_NOTADB
			return fmt.Errorf("%w: %v", ErrDamaged, err)
		}
	}
	return err
}

// verify checks that the file is a store of this package and schema, and
// that it is whole. Until the file is known to be a store nothing is
// written to it: a file that is refused is left as it was.
func (s *Store) verify() (err error) {
	ctx := context.Background()
	// One connection for all of it, so that the mode set below is the mode
	// of the connection that goes on to be used.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return damaged(fmt.Errorf("ownerjournal: reading the store: %w", err))
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	var app, version int64
	if err = conn.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); err != nil {
		return damaged(fmt.Errorf("ownerjournal: reading the store: %w", err))
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return damaged(fmt.Errorf("ownerjournal: reading the store: %w", err))
	}
	if app != applicationID {
		return fmt.Errorf("%w: the file is not an owner journal", ErrDamaged)
	}
	if version > schemaVersion {
		return fmt.Errorf("%w: schema %d, and this build reads up to %d", ErrNewerSchema, version, schemaVersion)
	}
	if version != schemaVersion {
		return fmt.Errorf("%w: schema %d is not a schema of this package", ErrDamaged, version)
	}
	if err = verifyLayout(ctx, conn); err != nil {
		return damaged(err)
	}
	// Write-ahead mode: a commit is one synced write to the file beside
	// the store. Every later connection sets it as it opens.
	var mode string
	if err = conn.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return damaged(fmt.Errorf("ownerjournal: setting the write-ahead mode: %w", err))
	}
	if mode != "wal" {
		return fmt.Errorf("ownerjournal: the write-ahead mode is %s", mode)
	}
	s.wal.Store(true)
	var answer string
	if err = conn.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&answer); err != nil {
		return damaged(fmt.Errorf("ownerjournal: checking the store: %w", err))
	}
	if answer != "ok" {
		return fmt.Errorf("%w: SQLite finds the file malformed", ErrDamaged)
	}
	return damaged(verifyChain(ctx, conn))
}

func verifyLayout(ctx context.Context, q querier) (err error) {
	rows, err := q.QueryContext(ctx, "SELECT type, name, sql FROM sqlite_schema WHERE length(sql) <= 4096 OR sql IS NULL")
	if err != nil {
		return fmt.Errorf("ownerjournal: reading the layout: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	found := make(map[string]string)
	count := 0
	for rows.Next() {
		var kind, name string
		var ddl sql.NullString
		if err = rows.Scan(&kind, &name, &ddl); err != nil {
			return err
		}
		found[kind+" "+name] = ddl.String
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	var all int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&all); err != nil {
		return err
	}
	if count != len(schema) || all != len(schema) {
		return fmt.Errorf("%w: the layout is not this package's", ErrDamaged)
	}
	for _, object := range schema {
		if found[object.kind+" "+object.name] != object.ddl {
			return fmt.Errorf("%w: the layout is not this package's", ErrDamaged)
		}
	}
	return nil
}
