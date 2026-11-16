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
	mu        sync.RWMutex
	failureMu sync.Mutex // bounded volatile publication/overlay only; never waits for mu, gate or SQL
	db        *sql.DB
	owner     *ownership
	options   Options
	closed    bool
	closeErr  error
	gate      chan struct{}
	views     map[string]*Attempt
	now       func() time.Time
	fault     func(string) error // unexported, per-store deterministic test seam
	observe   func(string)       // unexported, per-store scheduling test seam
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
		_, err = recognizeOwned(ctx, owner, o, d)
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
	if err := s.readLifetime(ctx); err != nil {
		return Info{}, err
	}
	defer s.mu.RUnlock()
	return readInfo(ctx, s.db, s.options)
}

func readInfo(ctx context.Context, db *sql.DB, o Options) (Info, error) {
	i, err := readIdentity(ctx, db, o.Authority)
	if err != nil {
		return Info{}, err
	}
	err = db.QueryRowContext(ctx, "SELECT (SELECT page_count FROM pragma_page_count), (SELECT page_size FROM pragma_page_size)").Scan(&i.PageCount, &i.PageSize)
	if err != nil {
		return Info{}, err
	}
	i.MaxBytes = o.MaxBytes
	i.SQLiteVersion, i.SQLiteSourceID = engineVersion, engineSource
	return i, nil
}

// Close is the only lifetime writer. Refuse admission as soon as a writer
// waits, without delaying cancellation behind Close's SQL/ownership cleanup.
// Successful readers still hold the lock until their operation finishes.
func (s *Store) readLifetime(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.mu.TryRLock() {
		if err := ctx.Err(); err != nil {
			return err
		}
		return os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		s.mu.RUnlock()
		return err
	}
	if s.closed {
		s.mu.RUnlock()
		return os.ErrClosed
	}
	return nil
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
