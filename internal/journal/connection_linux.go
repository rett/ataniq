package journal

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"

	"modernc.org/sqlite"
)

const engineVersion = "3.54.1"
const engineSource = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"

// Connect is the verification boundary for EVERY physical connection, including
// database/sql replacements. Configuration runs on the raw connection with the
// caller's bounded context; it never recursively enters the SQL pool.
type connector struct {
	driver   driver.Driver
	owner    *ownership
	options  Options
	readOnly bool
}

func (c *connector) Driver() driver.Driver { return c.driver }

func (c *connector) Connect(ctx context.Context) (_ driver.Conn, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.options.OperationTimeout)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = requireOFDLocking(); err != nil {
		return nil, err
	}
	if err = c.owner.check(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: c.owner.path}
	q := url.Values{"mode": {"rw"}, "_defensive": {"true"}, "_dqs": {"false"}, "_txlock": {"immediate"}}
	if c.readOnly {
		q.Set("mode", "ro")
	}
	u.RawQuery = q.Encode()
	conn, err := c.driver.Open(u.String())
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, conn.Close())
		}
	}()
	raw := conn.(sqlite.ExecQuerierContext)
	if err = configure(ctx, raw, c.options, c.readOnly); err != nil {
		return nil, err
	}
	if err = c.owner.check(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return conn, nil
}

func configure(ctx context.Context, raw sqlite.ExecQuerierContext, o Options, readOnly bool) error {
	// Bound the first real main-schema read too. Driver.Open's DSN must not
	// execute writer PRAGMAs before this boundary. Merely enabling OFD is not
	// proof: the first unsupported OFD fcntl can silently fall back to POSIX.
	if _, err := raw.ExecContext(ctx, "PRAGMA busy_timeout="+strconv.FormatInt(o.BusyTimeout.Milliseconds(), 10), nil); err != nil {
		return err
	}
	if _, err := scalar(ctx, raw, "SELECT count(*) FROM main.sqlite_schema"); err != nil {
		return err
	}
	if !sqlite.OFDLockingEnabled() {
		return errors.New("journal: main-file OFD locking required after schema read")
	}
	// These settings are connection-local. In particular, read-only preflight
	// never changes journal_mode or max_page_count on an unrecognized database.
	queryOnly := "0"
	if readOnly {
		queryOnly = "1"
	}
	for _, statement := range []string{
		"PRAGMA query_only=" + queryOnly,
		"PRAGMA trusted_schema=OFF", "PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=FULL", "PRAGMA locking_mode=NORMAL",
	} {
		if _, err := raw.ExecContext(ctx, statement, nil); err != nil {
			return err
		}
	}
	for _, check := range []struct{ query, want string }{
		{"SELECT sqlite_version()", engineVersion}, {"SELECT sqlite_source_id()", engineSource},
		{"PRAGMA synchronous", "2"}, {"PRAGMA foreign_keys", "1"},
		{"PRAGMA trusted_schema", "0"}, {"PRAGMA query_only", queryOnly},
		{"PRAGMA locking_mode", "normal"},
		{"PRAGMA busy_timeout", strconv.FormatInt(o.BusyTimeout.Milliseconds(), 10)},
		{"PRAGMA page_size", "4096"},
	} {
		if err := expect(ctx, raw, check.query, check.want); err != nil {
			return err
		}
	}
	// Behavioral readback for db_config flags, which have no readback PRAGMA.
	// With defensive mode, writable_schema remains disabled. DQS must reject
	// an unresolved quoted identifier instead of treating it as a string.
	if _, err := raw.ExecContext(ctx, "PRAGMA writable_schema=ON", nil); err != nil {
		return err
	}
	if err := expect(ctx, raw, "PRAGMA writable_schema", "0"); err != nil {
		return err
	}
	rows, err := raw.QueryContext(ctx, `SELECT "journal_dqs_probe"`, nil)
	if err == nil {
		rows.Close()
		return errors.New("journal: DQS unexpectedly enabled")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// This diagnostic is qualified against the exact pinned engine above.
	// BUSY, I/O and unrelated SQL errors are failures, not DQS readback.
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 1 || sqliteErr.Error() != `SQL logic error: no such column: "journal_dqs_probe" - should this be a string literal in single-quotes? (1)` {
		return fmt.Errorf("journal: DQS probe: %w", err)
	}
	pages, err := scalar(ctx, raw, "PRAGMA page_count")
	if err != nil {
		return err
	}
	n, ok := pages.(int64)
	if !ok || n > o.MaxBytes/4096 {
		return errors.New("journal: database exceeds page bound")
	}
	if !readOnly {
		if err := expect(ctx, raw, "PRAGMA journal_mode=WAL", "wal"); err != nil {
			return err
		}
		if err := expect(ctx, raw, "PRAGMA max_page_count="+strconv.FormatInt(o.MaxBytes/4096, 10), strconv.FormatInt(o.MaxBytes/4096, 10)); err != nil {
			return err
		}
		if err := expect(ctx, raw, "PRAGMA journal_mode", "wal"); err != nil {
			return err
		}
		if err := expect(ctx, raw, "PRAGMA max_page_count", strconv.FormatInt(o.MaxBytes/4096, 10)); err != nil {
			return err
		}
	}
	return nil
}

// Select the process-wide mode before SQLite or independent main-file opens.
// Repeating true is supported after use; never switch a live process's mode.
func requireOFDLocking() error {
	if _, err := sqlite.OFDLocking(true); err != nil {
		return fmt.Errorf("journal: main-file OFD locking required: %w", err)
	}
	return nil
}

func expect(ctx context.Context, raw sqlite.ExecQuerierContext, query, want string) error {
	got, err := scalar(ctx, raw, query)
	if err != nil {
		return err
	}
	if fmt.Sprint(got) != want {
		return fmt.Errorf("journal: %s readback %v, expected %s", query, got, want)
	}
	return nil
}

func scalar(ctx context.Context, raw sqlite.ExecQuerierContext, query string) (_ driver.Value, err error) {
	rows, err := raw.QueryContext(ctx, query, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if len(rows.Columns()) != 1 {
		return nil, errors.New("journal: unexpected result shape")
	}
	values := make([]driver.Value, 1)
	if err = rows.Next(values); err != nil {
		return nil, err
	}
	value := values[0]
	if err = rows.Next(values); err != io.EOF {
		return nil, fmt.Errorf("journal: extra row or read failure: %v", err)
	}
	return value, nil
}
