package journal

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"ataniq/internal/dispatch"
)

const snapshotRows = 4096
const snapshotBytes = 16 << 20

// Record is a trusted local inspection, with original evidence and a separate
// effective projection. Started does not mean completed.
type Record struct {
	Intent                               dispatch.Intent
	ValidUntilMillis, GrantUntilMillis   int64
	Revoked, Prepared, Uncertain, Active bool
	Current                              dispatch.Receipt
	Receipts                             []dispatch.Receipt
}

// Event identity is Snapshot's store/incarnation plus Sequence. Detail retains
// exact binary conflict bindings or receipt content; it is never JSON-normalized.
type Event struct {
	Sequence       int64
	IntentID, Kind string
	Detail         []byte
}

// Snapshot owns all returned bytes. It is eager and bounded, with no reader or
// cursor left open. This is not a public disclosure authorization mechanism.
type Snapshot struct {
	Authority, StoreID, IncarnationID string
	Watermark                         int64
	Records                           []Record
	ConsumedGrants                    []dispatch.Intent
	Events                            []Event
}

func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	ctx, leave, err := s.enter(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer leave()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	var out Snapshot
	var authority, store, incarnation []byte
	if err = tx.QueryRowContext(ctx, "SELECT authority,store_id,incarnation_id FROM journal_identity WHERE singleton=1").Scan(&authority, &store, &incarnation); err != nil {
		return Snapshot{}, err
	}
	out.Authority, out.StoreID, out.IncarnationID = string(authority), hex.EncodeToString(store), hex.EncodeToString(incarnation)
	remaining, rowsLeft := snapshotBytes, snapshotRows
	consume := func(n int) error {
		if n > remaining || rowsLeft == 0 {
			return ErrLimit
		}
		remaining -= n
		rowsLeft--
		return nil
	}
	indices := make(map[string]int)
	err = scanRows(ctx, tx, "SELECT id,binding,expires,grant_expires,revoked,prepared,uncertain,active,state,receipt_id,evidence FROM attempts ORDER BY id", func(rows *sql.Rows) error {
		var r Record
		var id, b, receiptID, evidence []byte
		var state dispatch.State
		if err := rows.Scan(&id, &b, &r.ValidUntilMillis, &r.GrantUntilMillis, &r.Revoked, &r.Prepared, &r.Uncertain, &r.Active, &state, &receiptID, &evidence); err != nil {
			return err
		}
		// Account both Intent and Current copies and fixed per-row overhead.
		if err := consume(2*len(b) + len(id) + len(receiptID) + len(evidence) + 512); err != nil {
			return err
		}
		i, err := decodeIntent(b)
		if err != nil {
			return err
		}
		if i.ID != string(id) {
			return ErrConflict
		}
		r.Intent = i
		if a := s.views[i.ID]; a != nil && a.fenced.Load() {
			r.Uncertain = true
		}
		if r.Uncertain {
			state = dispatch.Uncertain
		}
		r.Current = dispatch.Receipt{Intent: cloneIntent(i), ID: string(receiptID), State: state, Evidence: string(evidence)}
		indices[i.ID] = len(out.Records)
		out.Records = append(out.Records, r)
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	err = scanRows(ctx, tx, "SELECT attempt_id,receipt_id,state,evidence FROM evidence ORDER BY sequence", func(rows *sql.Rows) error {
		var id, rid, evidence []byte
		var state dispatch.State
		if err := rows.Scan(&id, &rid, &state, &evidence); err != nil {
			return err
		}
		n, ok := indices[string(id)]
		if !ok {
			return ErrConflict
		}
		r := &out.Records[n]
		if err := consume(len(id) + len(rid) + len(evidence) + len(r.Intent.Payload) + 4096); err != nil {
			return err
		}
		r.Receipts = append(r.Receipts, dispatch.Receipt{Intent: cloneIntent(r.Intent), ID: string(rid), State: state, Evidence: string(evidence)})
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	err = scanRows(ctx, tx, "SELECT id,binding FROM grants ORDER BY id", func(rows *sql.Rows) error {
		var id, b []byte
		if err := rows.Scan(&id, &b); err != nil {
			return err
		}
		if err := consume(len(id) + len(b) + 512); err != nil {
			return err
		}
		i, err := decodeIntent(b)
		if err != nil {
			return err
		}
		if i.Grant != string(id) {
			return ErrConflict
		}
		out.ConsumedGrants = append(out.ConsumedGrants, i)
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	err = scanRows(ctx, tx, "SELECT sequence,attempt_id,kind,detail FROM events ORDER BY sequence", func(rows *sql.Rows) error {
		var e Event
		var id []byte
		if err := rows.Scan(&e.Sequence, &id, &e.Kind, &e.Detail); err != nil {
			return err
		}
		if err := consume(len(id) + len(e.Kind) + len(e.Detail) + 128); err != nil {
			return err
		}
		e.IntentID = string(id)
		out.Events = append(out.Events, e)
		out.Watermark = e.Sequence
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	if err = s.hit("snapshot"); err != nil {
		return Snapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

func scanRows(ctx context.Context, tx *sql.Tx, query string, scan func(*sql.Rows) error) (err error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		if err = scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Inspect has the same eager snapshot bounds and consistency as Snapshot.
func (s *Store) Inspect(ctx context.Context, id string) (Record, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return Record{}, err
	}
	for _, r := range snapshot.Records {
		if r.Intent.ID == id {
			return r, nil
		}
	}
	return Record{}, sql.ErrNoRows
}
