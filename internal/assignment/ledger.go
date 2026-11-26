package assignment

import (
	"errors"
	"maps"
	"math"
	"reflect"
	"slices"
)

var (
	ErrChain      = errors.New("assignment: invalid event chain")
	ErrCheckpoint = errors.New("assignment: checkpoint mismatch")
	ErrLimit      = errors.New("assignment: record too large")
)

type Header struct {
	Authority   string `json:"authority"`
	Incarnation string `json:"incarnation"`
	KeyID       string `json:"key_id"`
	LedgerID    string `json:"ledger_id"`
	Schema      uint64 `json:"schema"`
	Store       string `json:"store"`
}

type Checkpoint struct {
	Authority, Store, Incarnation, LedgerID, Digest string
	Sequence                                        uint64
}

// Signer and Verifier belong to the authenticated adapter. Its verifier must
// use the independently enrolled key for the supplied header.
type Signer interface {
	Sign(Header, []byte) (string, error)
}
type Verifier interface {
	Verify(Header, []byte, string) error
}

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

// Event IDs bind accepted receipts across inventory resets and later transfers.
type receiptTarget struct {
	kind         Kind
	event, route string
}

func routeReport(c Command) (receiptTarget, Evidence) {
	if c.Kind == FenceReceipt || c.Kind == ConfirmReceipt {
		for _, e := range c.Evidence {
			if e.Kind == "route" {
				return receiptTarget{c.Kind, e.ScopeEventID, e.Subject}, e
			}
		}
	}
	return receiptTarget{}, Evidence{}
}

type Ledger struct {
	receipts   map[receiptTarget]Evidence
	header     Header
	checkpoint Checkpoint
	rows       map[Key]Row
	transfers  map[transferKey]Transfer
	history    map[operation]disposition
	latest     map[transferKey]Result
}

// NewLedger starts at the header hash. The caller establishes enrollment.
func NewLedger(h Header) (Ledger, error) {
	raw, err := h.Bytes()
	if err != nil {
		return Ledger{}, err
	}
	return initialLedger(h, raw), nil
}
func initialLedger(h Header, raw []byte) Ledger {
	hash := sha256Hex(raw)
	return Ledger{
		header: h, receipts: make(map[receiptTarget]Evidence),
		checkpoint: Checkpoint{h.Authority, h.Store, h.Incarnation, h.LedgerID, hash, 0},
		rows:       make(map[Key]Row),
		transfers:  make(map[transferKey]Transfer),
		history:    make(map[operation]disposition),
		latest:     make(map[transferKey]Result),
	}
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

// Lookup with an empty request ID returns the latest result recorded for, or
// applied to, the transfer. A correction of a historical receipt is requested
// under its original ID and changes the disposition of the current transfer.
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
	id := r.LastTransferID
	if c.Kind == HoldStarted || c.Kind == HoldReviewed {
		id = c.TransferID
	}
	tr := l.transfers[transferKey{key, id}]
	target, _ := routeReport(c)
	r, tr, err := reduce(r, tr.clone(), c, digest, l.receipts[target])
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
	if c.Kind == Seed || c.Kind == Commit {
		r.CommitRevision = r.Head.Revision
	}
	projected := tr
	if tr.ID != r.LastTransferID {
		projected = l.transfers[transferKey{key, r.LastTransferID}]
	}
	e.Public = project(l.header, l.rows[key], r, projected, e)
	return change{e, r, tr}, nil
}

// Decide returns a nil event and the original result for an exact retry.
// Refused commands consume no ID. Recorded evidence of uncertainty or a
// rejected belief does consume the operation ID.
func Decide(l Ledger, c Command) (*Event, Result, error) {
	digest, err := l.intakeDigest(c)
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
	if err := ch.event.checkSignedFrameSize(); err != nil {
		return nil, Result{}, err
	}
	return &ch.event, ch.event.Result, nil
}

// Size the exact LF-framed journal with the schema-1 SSHSIG field.
// Its 196-byte container occupies 262 unescaped base64url characters.
func (e Event) checkSignedFrameSize() error {
	frame, err := e.recordFrame()
	if err != nil {
		return err
	}
	if len(frame)+len(`,"signature":""`)+262 > recordLimit {
		return ErrLimit
	}
	return nil
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
	if e.Signature == "" || e.Schema != 1 || e.Sequence != l.checkpoint.Sequence+1 || e.PreviousHash != l.checkpoint.Digest {
		return l, ErrChain
	}
	hash, err := ledgerDigest(e)
	if err != nil {
		return l, err
	}
	digest, err := l.intakeDigest(e.Command)
	if errors.Is(err, ErrCapacity) {
		return l, err
	}
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
	if ch.event.Result != e.Result || !reflect.DeepEqual(ch.event.Public, e.Public) {
		return l, ErrChain
	}
	signed, err := e.SignedPublicBytes()
	if err != nil {
		return l, err
	}
	ch.row.PublicDigest = sha256Hex(signed)
	if ch.row.Head.Phase == PhaseQuarantined && ch.row.QuarantineEventID == "" {
		ch.row.QuarantineEventID = e.Result.EventID
	}
	switch e.Command.Kind {
	case Reserve:
		ch.transfer.ReservationEventID = e.Result.EventID
	case Seed, Commit:
		ch.transfer.CommittedEventID = e.Result.EventID
	}
	if !privateIndexes {
		l.rows, l.transfers = maps.Clone(l.rows), maps.Clone(l.transfers)
		l.history, l.latest = maps.Clone(l.history), maps.Clone(l.latest)
	}
	if target, report := routeReport(e.Command); report.Kind != "" && ch.row.Head.Phase != PhaseQuarantined {
		if !privateIndexes {
			l.receipts = maps.Clone(l.receipts)
		}
		l.receipts[target] = report
	}
	affected := transferKey{id.key, ch.transfer.ID}
	l.rows[id.key], l.transfers[affected] = ch.row, ch.transfer
	l.history[op], l.latest[id] = disposition{digest, e.Result}, e.Result
	l.latest[affected] = e.Result
	l.checkpoint.Sequence, l.checkpoint.Digest = e.Sequence, hash
	return l, nil
}
