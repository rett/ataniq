// Package journal owns guarded local dispatch storage on Linux. It does not
// authorize an external effect at its actual execution boundary.
package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"modernc.org/sqlite"
)

// Options configures trusted local storage. Authority is required; zero bounds
// select defaults. MaxBytes limits main database pages, not total disk usage.
type Options struct {
	Authority        string
	MaxBytes         int64         // default 64 MiB; 64 KiB..1 GiB, a multiple of 4096
	OperationTimeout time.Duration // default 5s; 1ms..1m
	BusyTimeout      time.Duration // default 250ms; 1ms..5s, <= OperationTimeout
}

func (o Options) normalized() (Options, error) {
	if o.Authority == "" || len(o.Authority) > 256 || strings.IndexFunc(o.Authority, unicode.IsSpace) >= 0 {
		return o, errors.New("journal: invalid authority")
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 64 << 20
	}
	if o.OperationTimeout == 0 {
		o.OperationTimeout = 5 * time.Second
	}
	if o.BusyTimeout == 0 {
		o.BusyTimeout = 250 * time.Millisecond
	}
	if o.MaxBytes < 64<<10 || o.MaxBytes > 1<<30 || o.MaxBytes%4096 != 0 ||
		o.OperationTimeout < time.Millisecond || o.OperationTimeout > time.Minute ||
		o.BusyTimeout < time.Millisecond || o.BusyTimeout > 5*time.Second ||
		o.BusyTimeout > o.OperationTimeout || o.BusyTimeout%time.Millisecond != 0 {
		return o, errors.New("journal: invalid storage or time bounds")
	}
	return o, nil
}

// Store owns one database inode until Close finishes. Do not copy a Store.
// The path and its private directory must remain under trusted local control.
type Store struct {
	mu       sync.RWMutex
	db       *sql.DB
	owner    *ownership
	options  Options
	closed   bool
	closeErr error
	gate     chan struct{}
	views    map[string]*Attempt
	now      func() time.Time
	fault    func(string) error // unexported, per-store deterministic test seam
}

// Info is a bounded local storage description, not a client snapshot or cursor.
type Info struct {
	Authority, StoreID, IncarnationID string
	SchemaVersion                     int
	SQLiteVersion, SQLiteSourceID     string
	PageCount, PageSize, MaxBytes     int64
}

// Create creates a fresh 0600 file in an existing private owned directory.
// It refuses overwrite. Failure can leave an incomplete file; it is never
// adopted, removed or silently initialized by Open or a subsequent Create.
func Create(ctx context.Context, path string, options Options) (*Store, error) {
	return open(ctx, path, options, true)
}

// Open only opens recognized existing storage for the configured authority.
// Ordinary reopen preserves identity; it is not a backup restore operation.
func Open(ctx context.Context, path string, options Options) (*Store, error) {
	return open(ctx, path, options, false)
}

func open(ctx context.Context, path string, options Options, create bool) (_ *Store, err error) {
	return openWithFault(ctx, path, options, create, nil)
}

func openWithFault(ctx context.Context, path string, options Options, create bool, fault func(string) error) (_ *Store, err error) {
	o, err := options.normalized()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, o.OperationTimeout)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	owner, err := acquire(path, create)
	if err != nil {
		return nil, err
	}
	s := &Store{owner: owner, options: o, gate: make(chan struct{}, 1), views: make(map[string]*Attempt), now: time.Now, fault: fault}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
		}
	}()
	d := &sqlite.Driver{}
	if !create {
		// No persistent writer PRAGMA is executed until this read-only preflight
		// accepts the complete schema and identity under the inode lock.
		preflight := pool(&connector{driver: d, owner: owner, options: o, readOnly: true})
		_, checkErr := readIdentity(ctx, preflight, o.Authority)
		err = errors.Join(checkErr, preflight.Close())
		if err != nil {
			return nil, fmt.Errorf("journal: preflight: %w", err)
		}
	}
	s.db = pool(&connector{driver: d, owner: owner, options: o})
	if err = s.db.PingContext(ctx); err != nil {
		return nil, err
	}
	if create {
		if err = initialize(ctx, s.db, o.Authority); err != nil {
			return nil, err
		}
		// The database commit syncs SQLite state; also persist the new directory entry.
		if err = owner.dir.Sync(); err != nil {
			return nil, err
		}
	}
	if _, err = readIdentity(ctx, s.db, o.Authority); err != nil {
		return nil, err
	}
	if err = s.recover(ctx); err != nil {
		return nil, err
	}
	if err = owner.check(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

// Info respects caller cancellation and the configured operation deadline.
func (s *Store) Info(ctx context.Context) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, s.options.OperationTimeout)
	defer cancel()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return Info{}, os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	i, err := readIdentity(ctx, s.db, s.options.Authority)
	if err != nil {
		return Info{}, err
	}
	err = s.db.QueryRowContext(ctx, "SELECT (SELECT page_count FROM pragma_page_count), (SELECT page_size FROM pragma_page_size)").Scan(&i.PageCount, &i.PageSize)
	if err != nil {
		return Info{}, err
	}
	i.MaxBytes = s.options.MaxBytes
	i.SQLiteVersion, i.SQLiteSourceID = engineVersion, engineSource
	return i, nil
}

// Close waits for bounded local operations, closes SQL, then releases ownership.
// It is idempotent and establishes no external-executor quiescence or fencing.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	if s.db != nil {
		// Only successfully opened views belong to this process. A failed Open
		// must not clean another session's guards after recovery failed.
		if len(s.views) != 0 {
			ctx, cancel := context.WithTimeout(context.Background(), s.options.OperationTimeout)
			s.closeErr = s.retireAll(ctx)
			cancel()
		}
		s.closeErr = errors.Join(s.closeErr, s.db.Close())
	}
	s.closeErr = errors.Join(s.closeErr, s.owner.close())
	return s.closeErr
}

func pool(c *connector) *sql.DB {
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db
}
