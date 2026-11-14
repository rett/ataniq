package journal

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/url"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// This wrapper only spoils the synchronous readback, using a real SQLite
// setting change immediately before verification. No production fault switch.
type wrongSynchronous struct{ sqlite.ExecQuerierContext }

func (c wrongSynchronous) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query == "PRAGMA synchronous" {
		if _, err := c.ExecContext(ctx, "PRAGMA synchronous=OFF", nil); err != nil {
			return nil, err
		}
	}
	return c.ExecQuerierContext.QueryContext(ctx, query, args)
}

func TestConnectionMisconfigurationRefused(t *testing.T) {
	for _, fault := range []string{"synchronous", "defensive", "dqs"} {
		t.Run(fault, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			s.Close()
			o, _ = o.normalized()
			q := url.Values{"mode": {"rw"}, "_defensive": {"true"}, "_dqs": {"false"}}
			if fault == "defensive" {
				q.Set("_defensive", "false")
			}
			if fault == "dqs" {
				q.Set("_dqs", "true")
			}
			u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
			d := &sqlite.Driver{}
			conn, err := d.Open(u.String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			raw := conn.(sqlite.ExecQuerierContext)
			if fault == "synchronous" {
				raw = wrongSynchronous{raw}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := configure(ctx, raw, o, false); err == nil {
				t.Fatal("accepted misconfigured connection")
			}
		})
	}
}

func TestReplacementMustPassConnector(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	s.Close()
	owner, err := acquire(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	o, _ = o.normalized()
	d := &sqlite.Driver{}
	var count atomic.Int32
	fault := errors.New("replacement fixture fault")
	d.RegisterConnectionHook(func(sqlite.ExecQuerierContext, string) error {
		if count.Add(1) > 1 {
			return fault
		}
		return nil
	})
	db := pool(&connector{driver: d, owner: owner, options: o})
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatal(err)
	}
	conn.Close()
	if err := db.PingContext(ctx); !errors.Is(err, fault) {
		t.Fatalf("replacement used despite fault: %v", err)
	}
	if count.Load() != 2 {
		t.Fatalf("opens %d", count.Load())
	}
}

func TestOwnershipMetadataRejectsOtherUID(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := *info.Sys().(*syscall.Stat_t)
	st.Uid++
	if private(otherOwner{FileInfo: info, stat: st}, false) {
		t.Fatal("accepted foreign owner")
	}
}

type otherOwner struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (i otherOwner) Sys() any { return &i.stat }
