package assignment

import (
	"errors"
	"maps"
	"math"
	"slices"
)

var (
	ErrChain      = errors.New("assignment: invalid event chain")
	ErrCheckpoint = errors.New("assignment: checkpoint mismatch")
	ErrLimit      = errors.New("assignment: record too large")
)

type Header struct {
	Schema                                         uint64
	Authority, Store, Incarnation, KeyID, LedgerID string
}

type Checkpoint struct {
	Authority, Store, Incarnation, LedgerID, Digest string
	Sequence                                        uint64
}

// Signer and Verifier belong to the authenticated adapter. Its verifier must
// use the independently enrolled key for the supplied header.
type Signer interface {
	Sign(Header, Event) (string, error)
}
type Verifier interface{ Verify(Header, Event) error }

type transferKey struct {
	key Key
	id  string
}

type operation struct {
	transferKey
	request string
}

type disposition struct {
	digest string
	result Result
}

type Ledger struct {
	header     Header
	checkpoint Checkpoint
	rows       map[Key]Row
	transfers  map[transferKey]Transfer
	history    map[operation]disposition
	latest     map[transferKey]Result
}

// NewLedger starts at the header hash. The caller establishes enrollment.
func NewLedger(h Header) (Ledger, error) {
	if h.Schema != 1 || h.Authority == "" || h.Store == "" || h.Incarnation == "" || h.KeyID == "" || h.LedgerID == "" {
		return Ledger{}, ErrInvalid
	}
	var w encoding
	w.number(h.Schema)
	w.strings(h.Authority, h.Store, h.Incarnation, h.KeyID, h.LedgerID)
	hash, err := w.digest("ataniq.assignment.header.v1")
	if err != nil {
		return Ledger{}, err
	}
	return Ledger{
		header:     h,
		checkpoint: Checkpoint{h.Authority, h.Store, h.Incarnation, h.LedgerID, hash, 0},
		rows:       make(map[Key]Row),
		transfers:  make(map[transferKey]Transfer),
		history:    make(map[operation]disposition),
		latest:     make(map[transferKey]Result),
	}, nil
}

func (l Ledger) Checkpoint() Checkpoint { return l.checkpoint }

func (l Ledger) Row(key Key) (Row, bool) {
	r, ok := l.rows[key]
	return r, ok
}

func (l Ledger) Transfer(key Key, id string) (Transfer, bool) {
	t, ok := l.transfers[transferKey{key, id}]
	return t.clone(), ok
}

// Lookup with an empty request ID returns the last recorded result for T.
func Lookup(l Ledger, key Key, transferID, requestID string) (Result, bool) {
	t := transferKey{key, transferID}
	if requestID == "" {
		r, ok := l.latest[t]
		return r, ok
	}
	d, ok := l.history[operation{t, requestID}]
	return d.result, ok
}

type change struct {
	event    Event
	row      Row
	transfer Transfer
}

func decideNew(l Ledger, c Command, digest string) (change, error) {
	key := c.Expected.Key
	if l.header.Schema != 1 || key.Home != l.header.Authority {
		return change{}, ErrInvalid
	}
	if l.checkpoint.Sequence == math.MaxUint64 {
		return change{}, ErrOverflow
	}
	r, exists := l.rows[key]
	if !exists {
		r.Head.Key = key
	}
	switch c.Kind {
	case Seed, Freeze, RollbackFreeze:
		if _, used := l.transfers[transferKey{key, c.TransferID}]; used {
			return change{}, ErrConflict
		}
	}
	tr := l.transfers[transferKey{key, r.LastTransferID}]
	r, tr, err := reduce(r, tr.clone(), c, digest)
	if err != nil {
		return change{}, err
	}
	c.Evidence = slices.Clone(c.Evidence)
	e := Event{Schema: 1, Sequence: l.checkpoint.Sequence + 1, PreviousHash: l.checkpoint.Digest, RequestDigest: digest, Command: c,
		Result: Result{Head: r.Head, RequestID: c.RequestID, Disposition: tr.Disposition}}
	e.Result.EventID, err = publicID(e)
	if err != nil {
		return change{}, err
	}
	return change{e, r, tr}, nil
}

// Decide returns a nil event and the original result for an exact retry.
// Refused commands consume no ID. Recorded evidence of uncertainty or a
// rejected belief does consume the operation ID.
func Decide(l Ledger, c Command) (*Event, Result, error) {
	digest, err := requestDigest(c)
	op := operation{transferKey{c.Expected.Key, c.TransferID}, c.RequestID}
	if old, ok := l.history[op]; ok {
		if err != nil || old.digest != digest {
			return nil, Result{}, ErrConflict
		}
		return nil, old.result, nil
	}
	if err != nil {
		return nil, Result{}, err
	}
	ch, err := decideNew(l, c, digest)
	if err != nil {
		return nil, Result{}, err
	}
	if _, err := ledgerDigest(ch.event); err != nil {
		return nil, Result{}, err
	}
	return &ch.event, ch.event.Result, nil
}

// Apply checks the chain and reproduces the decision from the prior row.
// It accepts an already authenticated event. Replay verifies the signatures
// of events received from outside this boundary.
func Apply(l Ledger, e Event) (Ledger, error) {
	return apply(l, e, false)
}

func apply(l Ledger, e Event, privateIndexes bool) (Ledger, error) {
	if l.checkpoint.Sequence == math.MaxUint64 {
		return l, ErrOverflow
	}
	if e.Schema != 1 || e.Sequence != l.checkpoint.Sequence+1 || e.PreviousHash != l.checkpoint.Digest {
		return l, ErrChain
	}
	hash, err := ledgerDigest(e)
	if err != nil {
		return l, err
	}
	digest, err := requestDigest(e.Command)
	if err != nil || digest != e.RequestDigest {
		return l, ErrChain
	}
	id := transferKey{e.Command.Expected.Key, e.Command.TransferID}
	op := operation{id, e.Command.RequestID}
	if _, exists := l.history[op]; exists {
		return l, ErrConflict
	}
	ch, err := decideNew(l, e.Command, digest)
	if err != nil {
		return l, err
	}
	if ch.event.Result != e.Result {
		return l, ErrChain
	}
	if !privateIndexes {
		l.rows, l.transfers = maps.Clone(l.rows), maps.Clone(l.transfers)
		l.history, l.latest = maps.Clone(l.history), maps.Clone(l.latest)
	}
	l.rows[id.key], l.transfers[id] = ch.row, ch.transfer
	l.history[op], l.latest[id] = disposition{digest, e.Result}, e.Result
	l.checkpoint.Sequence, l.checkpoint.Digest = e.Sequence, hash
	return l, nil
}
