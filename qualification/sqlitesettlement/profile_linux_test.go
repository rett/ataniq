//go:build linux

package sqlitesettlement

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"modernc.org/sqlite"
)

const engineVersion = "3.54.1"
const engineSource = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
const binding = "policy=enabled;revision=1"
const result = "policy-enabled:1"
const commandKey = "command-1"

type connection interface {
	driver.Conn
	driver.ConnBeginTx
	sqlite.ExecQuerierContext
}

func scalar(ctx context.Context, c connection, query string) (_ string, err error) {
	rows, err := c.QueryContext(ctx, query, nil)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if len(rows.Columns()) != 1 {
		return "", errors.New("unexpected scalar shape")
	}
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		return "", err
	}
	value := fmt.Sprint(values[0])
	if err := rows.Next(values); err != io.EOF {
		return "", fmt.Errorf("extra row or read failure: %v", err)
	}
	return value, nil
}

func expect(ctx context.Context, c connection, query, want string) error {
	got, err := scalar(ctx, c, query)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s: got %q, want %q", query, got, want)
	}
	return nil
}

// This repeats the qualified physical-connection boundary without importing
// production internals or using the application's schema. No VFS is replaced.
func openFixture(ctx context.Context, path string) (connection, error) {
	if _, err := sqlite.OFDLocking(true); err != nil {
		return nil, err
	}
	owner := os.NewFile(3, "inherited-owner")
	defer owner.Close()
	pinned, err := owner.Stat()
	if err != nil {
		return nil, err
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(pinned, named) {
		return nil, errors.New("fixture ownership identity mismatch")
	}
	// The parent's reference keeps the same flock held after this fd is closed.
	if err := unix.Flock(3, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	u.RawQuery = url.Values{"mode": {"rw"}, "_defensive": {"true"}, "_dqs": {"false"}, "_txlock": {"immediate"}}.Encode()
	raw, err := (&sqlite.Driver{}).Open(u.String())
	if err != nil {
		return nil, err
	}
	c := raw.(connection)
	if err := configure(ctx, c); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}

func configure(ctx context.Context, c connection) error {
	if _, err := c.ExecContext(ctx, "PRAGMA busy_timeout=100", nil); err != nil {
		return err
	}
	if _, err := scalar(ctx, c, "SELECT count(*) FROM main.sqlite_schema"); err != nil {
		return err
	}
	if !sqlite.OFDLockingEnabled() {
		return errors.New("OFD fallback after real schema read")
	}
	if sqlite.FdatasyncEnabled() {
		return errors.New("unexpected opt-in fdatasync mode")
	}
	for _, statement := range []string{
		"PRAGMA query_only=OFF", "PRAGMA trusted_schema=OFF", "PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=FULL", "PRAGMA locking_mode=NORMAL",
	} {
		if _, err := c.ExecContext(ctx, statement, nil); err != nil {
			return err
		}
	}
	for _, check := range [][2]string{
		{"SELECT sqlite_version()", engineVersion}, {"SELECT sqlite_source_id()", engineSource},
		{"PRAGMA synchronous", "2"}, {"PRAGMA foreign_keys", "1"}, {"PRAGMA trusted_schema", "0"},
		{"PRAGMA query_only", "0"}, {"PRAGMA locking_mode", "normal"}, {"PRAGMA busy_timeout", "100"},
		{"PRAGMA page_size", "4096"}, {"PRAGMA journal_mode=WAL", "wal"},
		{"PRAGMA journal_mode", "wal"}, {"PRAGMA max_page_count=64", "64"},
		{"PRAGMA max_page_count", "64"}, {"PRAGMA wal_autocheckpoint", "1000"},
	} {
		if err := expect(ctx, c, check[0], check[1]); err != nil {
			return err
		}
	}
	if _, err := c.ExecContext(ctx, "PRAGMA writable_schema=ON", nil); err != nil {
		return err
	}
	if err := expect(ctx, c, "PRAGMA writable_schema", "0"); err != nil {
		return err
	}
	rows, err := c.QueryContext(ctx, `SELECT "settlement_dqs_probe"`, nil)
	if err == nil {
		rows.Close()
		return errors.New("DQS unexpectedly enabled")
	}
	var sqlErr *sqlite.Error
	if !errors.As(err, &sqlErr) || sqlErr.Code() != 1 || err.Error() != `SQL logic error: no such column: "settlement_dqs_probe" - should this be a string literal in single-quotes? (1)` {
		return fmt.Errorf("DQS diagnostic mismatch: %w", err)
	}
	return nil
}

const schema = `
CREATE TABLE command (key TEXT PRIMARY KEY, binding TEXT NOT NULL, result TEXT NOT NULL) STRICT;
CREATE TABLE policy (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL, value TEXT NOT NULL) STRICT;
CREATE TABLE event (key TEXT PRIMARY KEY REFERENCES command(key), cause TEXT NOT NULL) STRICT;
CREATE TABLE barrier (id INTEGER PRIMARY KEY CHECK(id=1), epoch INTEGER NOT NULL CHECK(epoch BETWEEN 0 AND 1024)) STRICT;
CREATE TABLE capacity (payload BLOB NOT NULL) STRICT;
INSERT INTO policy VALUES(1,0,'disabled');
INSERT INTO barrier VALUES(1,0);`

const mutation = `
INSERT INTO command VALUES('command-1','policy=enabled;revision=1','policy-enabled:1');
UPDATE policy SET revision=revision+1,value='enabled' WHERE id=1;
INSERT INTO event VALUES('command-1','policy-enabled:1');`

type state struct {
	Commands, Events, Epoch             int64
	Key, Binding, Result, Policy, Cause string
	Revision                            int64
}

func readState(ctx context.Context, c connection) (_ state, err error) {
	var s state
	rows, err := c.QueryContext(ctx, `SELECT
 (SELECT count(*) FROM command), (SELECT count(*) FROM event),
 (SELECT epoch FROM barrier), COALESCE((SELECT key FROM command),''),
 COALESCE((SELECT binding FROM command),''), COALESCE((SELECT result FROM command),''),
 (SELECT value FROM policy), COALESCE((SELECT cause FROM event),''), (SELECT revision FROM policy)`, nil)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	v := make([]driver.Value, 9)
	if err := rows.Next(v); err != nil {
		return s, err
	}
	s = state{
		Commands: v[0].(int64), Events: v[1].(int64), Epoch: v[2].(int64),
		Key: v[3].(string), Binding: v[4].(string), Result: v[5].(string),
		Policy: v[6].(string), Cause: v[7].(string), Revision: v[8].(int64),
	}
	if err := rows.Next(v); err != io.EOF {
		return s, fmt.Errorf("state cardinality: %v", err)
	}
	return s, nil
}

// Metadata only: never open SHM while SQLite is live.
func checkFiles(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) && suffix != "" {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 2*1024*1024 {
			return fmt.Errorf("fixture file bounds/mode: %s", filepath.Base(path+suffix))
		}
	}
	return nil
}
