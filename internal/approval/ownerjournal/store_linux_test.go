package ownerjournal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// newPath returns the path of a store that does not exist yet, in a new
// folder that is the user's own.
func newPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A name with characters that mean something in a URL: the path is
	// handed to SQLite as one.
	return filepath.Join(dir, "owner ?#%.journal")
}

func mustOpen(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustClose(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func mustEntries(t *testing.T, s *Store) []Entry {
	t.Helper()
	entries, err := s.Entries(0, 0)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	return entries
}

// base is the time the tests count from.
var base = time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)

// addLooked appends one looked entry through the write path, whatever the
// mark is, and returns it.
func addLooked(s *Store, at time.Time) (Entry, error) {
	added, err := s.write(func(t *txn) error {
		_, err := t.add(Entry{Kind: KindLooked, At: at, Body: []byte("{}")})
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	return added[0], nil
}

// fill appends n looked entries and returns them. They are made in one
// write, so that a test that only needs entries to be there pays for one
// synced write and not for n.
func fill(t *testing.T, s *Store, n int) []Entry {
	t.Helper()
	first := int64(len(mustEntries(t, s)))
	out, err := s.write(func(tx *txn) error {
		for i := 0; i < n; i++ {
			at := base.Add(time.Duration(first+int64(i)) * time.Second)
			if _, err := tx.add(Entry{Kind: KindLooked, At: at, Body: []byte("{}")}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("appending %d entries: %v", n, err)
	}
	return out
}

// rawDB opens the file with SQLite directly, as a tool that is not this
// package would.
func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := requireOFDLocking(); err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rwc"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func rawExec(t *testing.T, path string, statements ...string) {
	t.Helper()
	db := rawDB(t, path)
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// leftovers lists the files in the store's folder other than the store.
func leftovers(t *testing.T, path string) []string {
	t.Helper()
	found, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range found {
		if f.Name() != filepath.Base(path) {
			names = append(names, f.Name())
		}
	}
	return names
}

func TestOpenMakesAPrivateWholeStore(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("the new file has mode %v, want a plain file with 0600", info.Mode())
	}
	if got := mustEntries(t, s); len(got) != 0 {
		t.Fatalf("a new store has %d entries", len(got))
	}
	want := fill(t, s, 3)
	mustClose(t, s)
	if got := leftovers(t, path); len(got) != 0 {
		t.Fatalf("a closed store leaves %v beside it", got)
	}
	s = mustOpen(t, path)
	if got := mustEntries(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a reopen:\n got %+v\nwant %+v", got, want)
	}
	for i, e := range want {
		if e.Seq != int64(i+1) {
			t.Fatalf("entry %d has sequence number %d", i, e.Seq)
		}
	}
}

func TestOpenRefusesWhatIsNotPrivate(t *testing.T) {
	cases := []struct {
		name string
		// spoil changes what is on the disk and returns the path to open.
		spoil func(t *testing.T, path string) string
		want  error // nil: any error
	}{
		{"folder open to the group", func(t *testing.T, path string) string {
			return path + chmod(t, filepath.Dir(path), 0o750)
		}, ErrNotPrivate},
		{"folder open to others", func(t *testing.T, path string) string {
			return path + chmod(t, filepath.Dir(path), 0o701)
		}, ErrNotPrivate},
		{"file open to the group", func(t *testing.T, path string) string {
			return path + chmod(t, path, 0o640)
		}, ErrNotPrivate},
		{"file open to others", func(t *testing.T, path string) string {
			return path + chmod(t, path, 0o604)
		}, ErrNotPrivate},
		{"file is a symbolic link", func(t *testing.T, path string) string {
			link := filepath.Join(filepath.Dir(path), "link")
			if err := os.Symlink(path, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, ErrNotPrivate},
		{"folder is a symbolic link", func(t *testing.T, path string) string {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(filepath.Dir(path), link); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(link, filepath.Base(path))
		}, nil},
		{"file has a second name", func(t *testing.T, path string) string {
			if err := os.Link(path, filepath.Join(filepath.Dir(path), "second")); err != nil {
				t.Fatal(err)
			}
			return path
		}, ErrNotPrivate},
		{"a file beside the store is open to others", func(t *testing.T, path string) string {
			if err := os.WriteFile(path+"-wal", nil, 0o644); err != nil {
				t.Fatal(err)
			}
			return path + chmod(t, path+"-wal", 0o644)
		}, ErrNotPrivate},
		{"a file beside the store is a symbolic link", func(t *testing.T, path string) string {
			if err := os.Symlink(path, path+"-journal"); err != nil {
				t.Fatal(err)
			}
			return path
		}, ErrNotPrivate},
		{"the path is a folder", func(t *testing.T, path string) string {
			return filepath.Dir(path)
		}, nil},
		{"the path is relative", func(t *testing.T, path string) string {
			return "relative.journal"
		}, nil},
		{"the path is not clean", func(t *testing.T, path string) string {
			return filepath.Dir(path) + "/./" + filepath.Base(path)
		}, nil},
		{"the folder is missing", func(t *testing.T, path string) string {
			return filepath.Join(filepath.Dir(path), "missing", "store")
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := newPath(t)
			s := mustOpen(t, path)
			fill(t, s, 2)
			mustClose(t, s)
			before := readFile(t, path)
			target := c.spoil(t, path)
			got, err := Open(target)
			if err == nil {
				got.Close()
				t.Fatal("Open accepted it")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("Open: %v, want %v", err, c.want)
			}
			if !bytes.Equal(before, readFile(t, path)) {
				t.Fatal("the refused Open changed the file")
			}
		})
	}
}

// chmod sets the mode and returns "", so that a case can write it in one
// line.
func chmod(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return ""
}

// A new store is not made in a folder others can reach, and nothing is left
// there.
func TestOpenMakesNothingInAFolderThatIsNotPrivate(t *testing.T) {
	path := newPath(t)
	chmod(t, filepath.Dir(path), 0o755)
	if s, err := Open(path); !errors.Is(err, ErrNotPrivate) {
		if err == nil {
			s.Close()
		}
		t.Fatalf("Open: %v, want %v", err, ErrNotPrivate)
	}
	if got := leftovers(t, path); len(got) != 0 {
		t.Fatalf("the refused Open left %v", got)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused Open made the file: %v", err)
	}
}

func TestASecondOpenIsRefusedAtOnce(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	fill(t, s, 1)
	start := time.Now()
	second, err := Open(path)
	if !errors.Is(err, ErrLocked) {
		if err == nil {
			second.Close()
		}
		t.Fatalf("the second Open: %v, want %v", err, ErrLocked)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("the second Open waited %v", waited)
	}
	// The refused Open closed a descriptor of the file; the first store's
	// hold and its writes are as before.
	fill(t, s, 1)
	if _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("a third Open: %v, want %v", err, ErrLocked)
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	if got := mustEntries(t, s); len(got) != 2 {
		t.Fatalf("%d entries after the reopen, want 2", len(got))
	}
}

func TestAClosedStoreAnswersClosed(t *testing.T) {
	s := mustOpen(t, newPath(t))
	mustClose(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("the second Close: %v", err)
	}
	if _, err := s.Entries(0, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("Entries: %v, want %v", err, ErrClosed)
	}
	if err := s.AppendLooked(base); !errors.Is(err, ErrClosed) {
		t.Fatalf("AppendLooked: %v, want %v", err, ErrClosed)
	}
	if _, err := s.LookedAt(); !errors.Is(err, ErrClosed) {
		t.Fatalf("LookedAt: %v, want %v", err, ErrClosed)
	}
}

func TestANewerSchemaIsRefused(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	fill(t, s, 2)
	mustClose(t, s)
	rawExec(t, path, "PRAGMA user_version=2")
	before := readFile(t, path)
	if got, err := Open(path); !errors.Is(err, ErrNewerSchema) {
		if err == nil {
			got.Close()
		}
		t.Fatalf("Open: %v, want %v", err, ErrNewerSchema)
	}
	if !bytes.Equal(before, readFile(t, path)) {
		t.Fatal("the refused Open changed the file")
	}
	// A schema number that never was is damage, not a newer schema.
	rawExec(t, path, "PRAGMA user_version=0")
	if _, err := Open(path); !errors.Is(err, ErrDamaged) {
		t.Fatalf("Open with schema 0: %v, want %v", err, ErrDamaged)
	}
}

func TestAFileThatIsNotAStoreIsRefused(t *testing.T) {
	noise := make([]byte, 3*4096)
	for i := range noise {
		noise[i] = byte(i*7 + i/251)
	}
	cases := []struct {
		name string
		make func(t *testing.T, path string)
	}{
		{"zero bytes", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"one byte", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte{0}, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"noise", func(t *testing.T, path string) {
			if err := os.WriteFile(path, noise, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"text", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(strings.Repeat("not a store\n", 400)), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"another database", func(t *testing.T, path string) {
			rawExec(t, path, "CREATE TABLE other (x)", "INSERT INTO other VALUES (1)")
			chmod(t, path, 0o600)
		}},
		{"another layout under this package's mark", func(t *testing.T, path string) {
			rawExec(t, path, "CREATE TABLE entries (seq INTEGER PRIMARY KEY)", "PRAGMA application_id=1096044362", "PRAGMA user_version=1")
			chmod(t, path, 0o600)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := newPath(t)
			c.make(t, path)
			before := readFile(t, path)
			got, err := Open(path)
			if !errors.Is(err, ErrDamaged) {
				if err == nil {
					got.Close()
				}
				t.Fatalf("Open: %v, want %v", err, ErrDamaged)
			}
			if !bytes.Equal(before, readFile(t, path)) {
				t.Fatal("the refused Open changed the file")
			}
			if c.name == "zero bytes" && !strings.Contains(err.Error(), "the file is empty") {
				t.Fatalf("Open of an empty file: %v, want it to say that the file is empty", err)
			}
		})
	}
}

// A store whose file was cut short at any point is refused. The store has
// enough entries to fill several pages, and every cut is tried on a copy.
func TestAFileCutShortIsRefused(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	fill(t, s, 300)
	mustClose(t, s)
	whole := readFile(t, path)
	if len(whole) < 5*4096 {
		t.Fatalf("the store is %d bytes; the test wants several pages", len(whole))
	}
	cuts := []int{1, 15, 16, 99, 100, 511, 4095, 4097, len(whole) - 4097, len(whole) - 100, len(whole) - 1}
	for cut := 512; cut < len(whole); cut += 512 {
		cuts = append(cuts, cut)
	}
	for _, cut := range cuts {
		copyPath := newPath(t)
		if err := os.WriteFile(copyPath, whole[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Open(copyPath)
		if err == nil {
			got.Close()
			t.Fatalf("cut at %d of %d bytes: Open accepted the file", cut, len(whole))
		}
		if !errors.Is(err, ErrDamaged) {
			t.Fatalf("cut at %d of %d bytes: %v, want %v", cut, len(whole), err, ErrDamaged)
		}
	}
	// The copy that is not cut opens.
	copyPath := newPath(t)
	if err := os.WriteFile(copyPath, whole, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := mustEntries(t, mustOpen(t, copyPath)); len(got) != 300 {
		t.Fatalf("the whole copy has %d entries", len(got))
	}
}

// The file's own rules refuse a change and a removal, whoever asks.
func TestTheFileRefusesAChangeAndARemoval(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	fill(t, s, 3)
	mustClose(t, s)
	db := rawDB(t, path)
	for _, statement := range []string{
		"UPDATE entries SET at = at + 1 WHERE seq = 2",
		"DELETE FROM entries WHERE seq = 2",
		"DELETE FROM entries",
	} {
		if _, err := db.Exec(statement); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: %v, want a refusal", statement, err)
		}
	}
	db.Close()
	if got := mustEntries(t, mustOpen(t, path)); len(got) != 3 {
		t.Fatalf("%d entries, want 3", len(got))
	}
}

// With the file's rules taken away and put back, a changed entry and a
// missing one are still found, by the digests and the sequence numbers.
func TestAChangedOrMissingEntryIsFound(t *testing.T) {
	var dropUpdate, dropDelete, makeUpdate, makeDelete string
	for _, object := range schema {
		switch object.name {
		case "entries_no_update":
			dropUpdate, makeUpdate = "DROP TRIGGER entries_no_update", object.ddl
		case "entries_no_delete":
			dropDelete, makeDelete = "DROP TRIGGER entries_no_delete", object.ddl
		}
	}
	cases := []struct {
		name       string
		statements []string
	}{
		{"a changed time", []string{dropUpdate, "UPDATE entries SET at = at + 1 WHERE seq = 2", makeUpdate}},
		{"a changed body", []string{dropUpdate, `UPDATE entries SET body = CAST('{"a":1}' AS BLOB) WHERE seq = 3`, makeUpdate}},
		{"a changed kind", []string{dropUpdate, "UPDATE entries SET kind = 'alert' WHERE seq = 1", makeUpdate}},
		{"a changed project", []string{dropUpdate, "UPDATE entries SET project = 'project-b' WHERE seq = 4", makeUpdate}},
		{"a changed digest", []string{dropUpdate, "UPDATE entries SET sum = zeroblob(32) WHERE seq = 2", makeUpdate}},
		{"a missing entry in the middle", []string{dropDelete, "DELETE FROM entries WHERE seq = 2", makeDelete}},
		{"a missing first entry", []string{dropDelete, "DELETE FROM entries WHERE seq = 1", makeDelete}},
		{"a rule taken away", []string{dropUpdate}},
		{"an index added", []string{"CREATE INDEX extra ON entries (at)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := newPath(t)
			s := mustOpen(t, path)
			fill(t, s, 4)
			mustClose(t, s)
			rawExec(t, path, c.statements...)
			got, err := Open(path)
			if !errors.Is(err, ErrDamaged) {
				if err == nil {
					got.Close()
				}
				t.Fatalf("Open: %v, want %v", err, ErrDamaged)
			}
		})
	}
}

func TestEntriesAfterAndAtMost(t *testing.T) {
	s := mustOpen(t, newPath(t))
	want := fill(t, s, 5)
	got, err := s.Entries(2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want[2:4]) {
		t.Fatalf("Entries(2, 2):\n got %+v\nwant %+v", got, want[2:4])
	}
	if got, err = s.Entries(5, 0); err != nil || len(got) != 0 {
		t.Fatalf("Entries(5, 0): %d entries, %v", len(got), err)
	}
	for _, e := range want {
		if e.At.Location() != time.UTC {
			t.Fatalf("entry %d has its time in %v", e.Seq, e.At.Location())
		}
		if e.Record != nil || string(e.Body) != "{}" || e.Kind != KindLooked {
			t.Fatalf("entry %d: %+v", e.Seq, e)
		}
	}
}

func TestAnEntryNeedsATimeAndProperNames(t *testing.T) {
	s := mustOpen(t, newPath(t))
	fill(t, s, 1)
	bad := []Entry{
		{Kind: KindLooked, Body: []byte("{}")},
		{Kind: KindLooked, At: base, Body: []byte("{}"), PendingID: "rq-1"},
		{Kind: KindLooked, At: base, Body: []byte("{}"), PendingID: "ap-UPPER"},
		{Kind: KindLooked, At: base, Body: []byte("{}"), Project: "a\x1b[2Jb"},
		{Kind: KindLooked, At: base, Body: []byte("{}"), Project: "a\xffb"},
		{Kind: KindLooked, At: base, Body: []byte("{}"), Project: strings.Repeat("p", 257)},
		{Kind: KindLooked, At: base, Body: []byte("{}"), Ref: 2},
		{Kind: KindLooked, At: base, Body: []byte("{}"), Ref: -1},
		{Kind: Kind("other"), At: base, Body: []byte("{}")},
		{Kind: KindLooked, At: base, Body: []byte("")},
	}
	for i, e := range bad {
		_, err := s.write(func(t *txn) error {
			_, err := t.add(e)
			return err
		})
		if err == nil {
			t.Errorf("entry %d was written: %+v", i, e)
		}
	}
	if got := mustEntries(t, s); len(got) != 1 {
		t.Fatalf("%d entries, want the first one only", len(got))
	}
}

func TestBodyIsCanonical(t *testing.T) {
	got, err := body(map[string]any{"b": "x\"y\\z <&> é ", "a": 7, "c": true, "ab": int64(-3), "B": false})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"B\":false,\"a\":7,\"ab\":-3,\"b\":\"x\\\"y\\\\z <&> é \",\"c\":true}"
	if string(got) != want {
		t.Fatalf("body:\n got %s\nwant %s", got, want)
	}
	for _, v := range []any{"line\nbreak", "\x1b[0m", "bad\xff", "c1\u0085", 1.5, nil, []string{"a"}} {
		if _, err := body(map[string]any{"k": v}); err == nil {
			t.Errorf("body took %#v", v)
		}
	}
	if got, _ := body(nil); string(got) != "{}" {
		t.Fatalf("an empty body is %s", got)
	}
}

func TestTheLookedMarkOnlyMovesForward(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	if mark, err := s.LookedAt(); err != nil || !mark.IsZero() {
		t.Fatalf("a new store's mark: %v, %v", mark, err)
	}
	local := time.FixedZone("local", -9*3600)
	first := base.Add(1500 * time.Millisecond).In(local)
	if err := s.AppendLooked(first); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{first, first.Add(-time.Hour), base} {
		if err := s.AppendLooked(at); err != nil {
			t.Fatal(err)
		}
	}
	if got := mustEntries(t, s); len(got) != 1 {
		t.Fatalf("%d entries, want 1: a look that is not later writes nothing", len(got))
	}
	if mark, err := s.LookedAt(); err != nil || !mark.Equal(first) || mark.Location() != time.UTC {
		t.Fatalf("mark %v, %v; want %v in UTC", mark, err, first)
	}
	later := first.Add(time.Nanosecond)
	if err := s.AppendLooked(later); err != nil {
		t.Fatal(err)
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	if mark, err := s.LookedAt(); err != nil || !mark.Equal(later) {
		t.Fatalf("after a reopen: mark %v, %v; want %v", mark, err, later)
	}
	if err := s.AppendLooked(time.Time{}); err == nil {
		t.Fatal("a look with no time was taken")
	}
}

// rawInsert writes entries into the file past this package, each with a
// digest that holds for the entry before it in the list.
func rawInsert(t *testing.T, path string, entries ...Entry) {
	t.Helper()
	db := rawDB(t, path)
	sum := zeroSum
	for _, e := range entries {
		sum = sumOf(sum, e)
		if _, err := db.Exec("INSERT INTO entries (seq, kind, at, pending_id, project, ref, body, record, sum) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)",
			e.Seq, string(e.Kind), e.At.UnixNano(), e.PendingID, e.Project, e.Ref, e.Body, sum); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// The sequence numbers are checked by themselves, beside the digests: a
// file whose digests all hold and whose numbers have a gap, or do not
// start at 1, is refused.
func TestAGapInTheSequenceIsFoundEvenWhenTheDigestsHold(t *testing.T) {
	entry := func(seq int64) Entry {
		return Entry{Seq: seq, Kind: KindLooked, At: base.Add(time.Duration(seq) * time.Second), Body: []byte("{}")}
	}
	cases := []struct {
		name  string
		seqs  []int64
		whole bool
	}{
		{"1, 2, 3", []int64{1, 2, 3}, true},
		{"1, 3", []int64{1, 3}, false},
		{"2, 3", []int64{2, 3}, false},
		{"1, 2, 4", []int64{1, 2, 4}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := newPath(t)
			mustClose(t, mustOpen(t, path))
			var entries []Entry
			for _, seq := range c.seqs {
				entries = append(entries, entry(seq))
			}
			rawInsert(t, path, entries...)
			s, err := Open(path)
			if c.whole {
				if err != nil {
					t.Fatalf("the test's own digests do not hold: %v", err)
				}
				if got := mustEntries(t, s); !reflect.DeepEqual(got, entries) {
					t.Fatalf("the entries: %+v", got)
				}
				mustClose(t, s)
				return
			}
			if !errors.Is(err, ErrDamaged) {
				if err == nil {
					s.Close()
				}
				t.Fatalf("Open: %v, want %v", err, ErrDamaged)
			}
		})
	}
}

// The package's promise is that an append is on the disk when it returns.
// A crash test that copies files or kills a process cannot see a write that
// was never synced, because the kernel keeps the pages. So this test reads
// the settings of the store's own connection, after Open and after a reopen,
// and fails if either is lowered.
func TestEveryCommitIsSynced(t *testing.T) {
	path := newPath(t)
	for i := 0; i < 2; i++ {
		s := mustOpen(t, path)
		err := s.read(func(q querier) error {
			var synced int
			if err := q.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&synced); err != nil || synced != 2 {
				t.Errorf("opening %d: synchronous is %d, %v; want 2 (FULL)", i+1, synced, err)
			}
			var mode string
			if err := q.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
				t.Errorf("opening %d: journal_mode is %q, %v; want wal", i+1, mode, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		fill(t, s, 1)
		mustClose(t, s)
	}
}
