package ownerjournal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"ataniq/internal/approval/record"
)

// Kind is the kind of an entry. The list is fixed.
type Kind string

// The kinds of entry.
const (
	// KindDecision is a decision of the owner that goes to the door: an
	// approve inside the day's limit, or a deny.
	KindDecision Kind = "decision"
	// KindHeld is an approve made past the day's limit. It is never
	// handed to the door.
	KindHeld Kind = "held"
	// KindRefused is a decision the host refused before it was one.
	KindRefused Kind = "refused"
	// KindDelivered says that the door answered for a decision: it took
	// it, or refused it with a code.
	KindDelivered Kind = "delivered"
	// KindOutcome says what became of a delivered approval.
	KindOutcome Kind = "outcome"
	// KindExpired closes a held entry whose pending record ran out.
	KindExpired Kind = "expired"
	// KindLimit is a change of a project's daily card limit.
	KindLimit Kind = "limit"
	// KindUse is one answer of the door's Use verb for an approval.
	KindUse Kind = "use"
	// KindAlert marks a moment a project had an alert or reached its
	// limit.
	KindAlert Kind = "alert"
	// KindCount says whether a request would have met the terms of the
	// first standing approval.
	KindCount Kind = "count"
	// KindActivity goes with a decision, a held or a refused entry: where
	// it came from and the owner's last activity before it.
	KindActivity Kind = "activity"
	// KindLooked moves the "since he last looked" mark.
	KindLooked Kind = "looked"
)

// maxLine caps a host-written line kept in a body (a reason, a note), in
// bytes.
const maxLine = 500

// Entry is one entry of the journal as it was written.
type Entry struct {
	// Seq is the sequence number: 1 for the first entry, and one more for
	// each entry after it.
	Seq int64
	// Kind is the kind of the entry.
	Kind Kind
	// At is the time the entry speaks of, in UTC.
	At time.Time
	// PendingID is the id of the pending record the entry applies to, and
	// Project the project. Either is empty where the kind has none.
	PendingID string
	Project   string
	// Ref is the sequence number of the earlier entry this one belongs to
	// or closes, and 0 when there is none.
	Ref int64
	// Body is the entry's own fields: one JSON object in canonical form.
	Body []byte
	// Record holds the bytes of the decision record, exactly as they were
	// given, for a decision and a held entry. It is nil for every other
	// kind.
	Record []byte
}

// entryColumns are the columns of an entry in the order scanEntry reads.
const entryColumns = "seq, kind, at, pending_id, project, ref, body, record"

// querier is what a read needs; a transaction and the database both are.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// scanner is one row of a result.
type scanner interface{ Scan(dest ...any) error }

func scanEntry(row scanner) (Entry, error) {
	var e Entry
	var kind string
	var at int64
	if err := row.Scan(&e.Seq, &kind, &at, &e.PendingID, &e.Project, &e.Ref, &e.Body, &e.Record); err != nil {
		return Entry{}, err
	}
	e.Kind = Kind(kind)
	e.At = time.Unix(0, at).UTC()
	return e, nil
}

// queryEntries returns the entries a query selects. The query names
// entryColumns, of the table or of an alias of it.
func queryEntries(q querier, query string, args ...any) (_ []Entry, err error) {
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// columnsOf writes entryColumns for the alias a query gave the table.
func columnsOf(alias string) string {
	return alias + ".seq, " + alias + ".kind, " + alias + ".at, " + alias + ".pending_id, " +
		alias + ".project, " + alias + ".ref, " + alias + ".body, " + alias + ".record"
}

// sumOf is the digest an entry carries: SHA-256 over the digest of the
// entry before it (32 zero bytes for the first) and the entry's fields,
// each with its length in front so that no two entries give the same
// bytes.
func sumOf(prev []byte, e Entry) []byte {
	h := sha256.New()
	h.Write(prev)
	var n [8]byte
	number := func(v uint64) {
		binary.BigEndian.PutUint64(n[:], v)
		h.Write(n[:])
	}
	field := func(b []byte) {
		number(uint64(len(b)))
		h.Write(b)
	}
	number(uint64(e.Seq))
	field([]byte(e.Kind))
	number(uint64(e.At.UnixNano()))
	field([]byte(e.PendingID))
	field([]byte(e.Project))
	number(uint64(e.Ref))
	field(e.Body)
	if e.Record == nil {
		number(0)
	} else {
		number(1)
		field(e.Record)
	}
	return h.Sum(nil)
}

// zeroSum stands for the digest before the first entry.
var zeroSum = make([]byte, sha256.Size)

// verifyChain reads every entry in order and checks the sequence numbers
// and the digests.
func verifyChain(ctx context.Context, q querier) (err error) {
	rows, err := q.QueryContext(ctx, "SELECT "+entryColumns+", sum FROM entries ORDER BY seq")
	if err != nil {
		return fmt.Errorf("ownerjournal: reading the entries: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	prev, next := zeroSum, int64(1)
	for rows.Next() {
		var e Entry
		var kind string
		var at int64
		var sum []byte
		if err = rows.Scan(&e.Seq, &kind, &at, &e.PendingID, &e.Project, &e.Ref, &e.Body, &e.Record, &sum); err != nil {
			return fmt.Errorf("ownerjournal: reading the entries: %w", err)
		}
		e.Kind, e.At = Kind(kind), time.Unix(0, at).UTC()
		if e.Seq != next {
			return fmt.Errorf("%w: entry %d is missing", ErrDamaged, next)
		}
		if !bytes.Equal(sum, sumOf(prev, e)) {
			return fmt.Errorf("%w: entry %d is not as it was written", ErrDamaged, e.Seq)
		}
		prev, next = sum, next+1
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("ownerjournal: reading the entries: %w", err)
	}
	return nil
}

// txn is one write in progress: a transaction, and the end of the journal
// as the transaction sees it.
type txn struct {
	s     *Store
	tx    *sql.Tx
	seq   int64  // the last sequence number
	sum   []byte // the last digest
	added []Entry
}

// write runs fn inside one transaction and commits what it added. The
// entries are durable when write returns without an error. When fn returns
// an error nothing is written.
func (s *Store) write(fn func(t *txn) error) (_ []Entry, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if s.uncertain {
		return nil, ErrUncertain
	}
	ctx := context.Background()
	if err = s.hit("before:begin"); err != nil {
		return nil, err
	}
	// The connection begins every transaction with the write lock.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("ownerjournal: beginning a write: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()
	if err = s.hit("after:begin"); err != nil {
		return nil, err
	}
	t := &txn{s: s, tx: tx, sum: zeroSum}
	// The end of the journal is read anew for each write, so that nothing
	// kept in memory can differ from the file.
	err = tx.QueryRowContext(ctx, "SELECT seq, sum FROM entries ORDER BY seq DESC LIMIT 1").Scan(&t.seq, &t.sum)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("ownerjournal: reading the end of the journal: %w", err)
	}
	if err = fn(t); err != nil {
		return nil, err
	}
	if len(t.added) == 0 {
		return nil, nil
	}
	if err = s.hit("before:commit"); err != nil {
		return nil, err
	}
	committed = true
	err = tx.Commit()
	if err == nil {
		// The hook stands for a commit that returned an error.
		err = s.hit("commit")
	}
	if err != nil {
		// Whether the entries are in the file is not known here: the
		// commit may have reached the disk before it failed. Nothing more
		// is written or answered until the store is opened again.
		s.uncertain = true
		return nil, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	if err = s.hit("after:commit"); err != nil {
		return nil, err
	}
	return t.added, nil
}

// read runs fn against the store.
func (s *Store) read(fn func(q querier) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.uncertain {
		return ErrUncertain
	}
	return fn(s.db)
}

// add appends one entry to the write. The sequence number is the next one;
// e.Seq is ignored.
func (t *txn) add(e Entry) (Entry, error) {
	if !validTime(e.At) {
		return Entry{}, errNoTime
	}
	if e.PendingID != "" && !record.ValidPendingID(e.PendingID) {
		return Entry{}, errors.New("ownerjournal: the pending id is not ap- and 1 to 48 lower-case letters, digits and dashes")
	}
	if e.Project != "" && !validName(e.Project, 256) {
		return Entry{}, errors.New("ownerjournal: the project is not a name of at most 256 bytes without control characters")
	}
	if e.Ref < 0 || e.Ref > t.seq {
		return Entry{}, errors.New("ownerjournal: an entry can only point at an earlier entry")
	}
	e.Seq = t.seq + 1
	e.At = time.Unix(0, e.At.UnixNano()).UTC()
	sum := sumOf(t.sum, e)
	if err := t.s.hit("before:insert"); err != nil {
		return Entry{}, err
	}
	var rec any
	if e.Record != nil {
		rec = e.Record
	}
	_, err := t.tx.ExecContext(context.Background(),
		"INSERT INTO entries (seq, kind, at, pending_id, project, ref, body, record, sum) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		e.Seq, string(e.Kind), e.At.UnixNano(), e.PendingID, e.Project, e.Ref, e.Body, rec, sum)
	if err != nil {
		return Entry{}, fmt.Errorf("ownerjournal: writing entry %d: %w", e.Seq, err)
	}
	if err := t.s.hit("after:insert"); err != nil {
		return Entry{}, err
	}
	t.seq, t.sum = e.Seq, sum
	t.added = append(t.added, e)
	return e, nil
}

// errNoTime refuses an entry without a time.
var errNoTime = errors.New("ownerjournal: an entry needs a time")

// validTime reports whether t can be the time of an entry: not the zero
// time, and inside the years a count of nanoseconds can hold.
func validTime(t time.Time) bool {
	return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 2200
}

// validName reports whether s can stand in an entry as a name or a line:
// not empty, at most max bytes, valid UTF-8 and no control character.
func validName(s string, max int) bool {
	return s != "" && len(s) <= max && record.Text(s).Plain()
}

// validLine is validName for a line that may be empty.
func validLine(s string) bool {
	return s == "" || validName(s, maxLine)
}

// body writes the fields of an entry as one JSON object in canonical form:
// keys sorted by their bytes, no whitespace, strings with the escapes \"
// and \\ only. A value is a string without control characters, an int, an
// int64 or a bool.
func body(fields map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []byte{'{'}
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendString(out, k)
		out = append(out, ':')
		switch v := fields[k].(type) {
		case string:
			if !record.Text(v).Plain() {
				return nil, fmt.Errorf("ownerjournal: %s holds a control character or is not UTF-8", k)
			}
			out = appendString(out, v)
		case int:
			out = strconv.AppendInt(out, int64(v), 10)
		case int64:
			out = strconv.AppendInt(out, v, 10)
		case bool:
			out = strconv.AppendBool(out, v)
		default:
			return nil, fmt.Errorf("ownerjournal: %s has a value of a kind a body cannot hold", k)
		}
	}
	return append(out, '}'), nil
}

func appendString(out []byte, s string) []byte {
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '"' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return append(out, '"')
}

// parseBody reads a body this package wrote into dst, a pointer to a
// struct that names every field of the kind.
func parseBody(e Entry, dst any) error {
	d := json.NewDecoder(bytes.NewReader(e.Body))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("%w: the body of entry %d cannot be read", ErrDamaged, e.Seq)
	}
	return nil
}

// stamp writes a time for a body: RFC 3339 in UTC with the fraction it
// has, and the empty string for the zero time.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// unstamp reads what stamp wrote.
func unstamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: a time in a body cannot be read", ErrDamaged)
	}
	return t.UTC(), nil
}

// Entries returns the entries with a sequence number above after, in
// order, and at most max of them (all of them when max is 0 or less). It
// is the journal as it was written, for a caller that wants to show or
// check it; the other queries answer the questions of the service.
func (s *Store) Entries(after int64, max int) ([]Entry, error) {
	if max <= 0 {
		max = -1
	}
	var out []Entry
	err := s.read(func(q querier) (err error) {
		out, err = queryEntries(q, "SELECT "+entryColumns+" FROM entries WHERE seq > ? ORDER BY seq LIMIT ?", after, max)
		return err
	})
	return out, err
}

// AppendLooked moves the "since he last looked" mark to at. The mark never
// moves back: a time that is not after the mark writes nothing.
func (s *Store) AppendLooked(at time.Time) error {
	if !validTime(at) {
		return errNoTime
	}
	_, err := s.write(func(t *txn) error {
		mark, err := lookedAt(t.tx)
		if err != nil {
			return err
		}
		if !mark.IsZero() && !at.After(mark) {
			return nil
		}
		_, err = t.add(Entry{Kind: KindLooked, At: at, Body: []byte("{}")})
		return err
	})
	return err
}

// LookedAt returns the "since he last looked" mark, and the zero time when
// the owner never looked. It is one mark for every surface and project.
func (s *Store) LookedAt() (time.Time, error) {
	var mark time.Time
	err := s.read(func(q querier) (err error) {
		mark, err = lookedAt(q)
		return err
	})
	return mark, err
}

func lookedAt(q querier) (time.Time, error) {
	var at sql.NullInt64
	err := q.QueryRowContext(context.Background(), "SELECT max(at) FROM entries WHERE kind = 'looked'").Scan(&at)
	if err != nil || !at.Valid {
		return time.Time{}, err
	}
	return time.Unix(0, at.Int64).UTC(), nil
}
