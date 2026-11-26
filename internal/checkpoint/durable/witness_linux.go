package durable

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"os"
	"slices"
	"syscall"
	"unicode/utf8"

	"ataniq/internal/assignment"
	cp "ataniq/internal/checkpoint"
)

// StorageError classifies startup refusals. Callers can use errors.Is for a
// specific reason, or errors.As to distinguish them from ordinary I/O errors.
type StorageError string

func (e StorageError) Error() string { return "durable witness: " + string(e) }

const (
	ErrNotInitialized StorageError = "not initialised"
	ErrPoisoned       StorageError = "storage error requires owner check"
	ErrUnsafe         StorageError = "unsafe path, ownership or permissions"
	ErrLocked         StorageError = "already locked"
	ErrIdentity       StorageError = "identity or signing key mismatch"
	ErrCorrupt        StorageError = "corrupt journal or receipt index"
	ErrTorn           StorageError = "incomplete journal or receipt index"
)

// CapacityError reports the exhausted history allowance. A zero Limit indicates
// that the filesystem could not reserve space for the transition.
type CapacityError struct{ Limit uint64 }

func (*CapacityError) Error() string { return "durable witness: history capacity exhausted" }
func (*CapacityError) Unwrap() error { return cp.ErrCapacity }

// Identity is independently provisioned for the journal, distinct from the
// home ledger identities it can witness.
type Identity struct {
	Witness     string `json:"witness"`
	Store       string `json:"store"`
	Incarnation string `json:"incarnation"`
	Epoch       uint64 `json:"epoch,string"`
}

// Config provides the local paths, limits and independently pinned identity.
// KeyPath contains exactly one raw 32-byte Ed25519 seed in a service-owned
// 0600 regular file. Pin identifies the witness, and HomePin its sole home.
// Both come from independent configuration. Directory is
// pre-provisioned durably. Both paths are clean and absolute. ReceiptLimit
// charges one unit per new receipt or Settle fence. Zero uses the maximum.
// Release and retirement have reserved space to fence existing writers.
type Config struct {
	Identity           Identity
	Directory, KeyPath string
	Pin, HomePin       ed25519.PublicKey
	ReceiptLimit       uint64
}

const maxReceipts = 65536

type item struct {
	slot cp.Slot
	id   cp.ID
}
type accepted struct {
	digest  string
	receipt cp.Receipt
}
type enrollment struct {
	state         cp.State
	home, session ed25519.PublicKey
	claimed       map[string]bool
}

// Witness owns the files and the serialized transaction and replay indexes.
type Witness struct {
	transaction                     chan struct{}
	dir, journal, lockFile, keyFile *os.File
	identity                        Identity
	pin, homePin                    ed25519.PublicKey
	stagingPrefix                   string // nonempty only until Create publishes the pair
	keyID                           string
	signRecord                      func([]byte) []byte
	sign                            func([]byte) ([]byte, error)
	seq                             uint64
	previous                        [32]byte
	limit, reservedSettlements      uint64
	inhibited, stopped              bool
	slots                           map[cp.Slot]*enrollment
	history                         map[item]accepted // a zero receipt denotes a permanent Settle fence
	grants                          map[cp.Slot]bool
	fault                           func(string) error
	closingRecords                  int64 // one retirement per live slot and one release per active writer
	journalSpace, indexSpace        int64 // ends of ranges reserved on these descriptors
	allocate                        func(*os.File, int64, int64) error
	writeEntry                      func([]byte) (int, error)
	writeFrame                      func([]byte) (int, error)
	syncFile                        func(*os.File) error
	armed, recoveryRequired         bool
}

// Create exclusively initializes both files. A failed creation leaves its
// evidence in place. Creating again cannot replace an existing witness.
func Create(c Config) (*Witness, error) { return start(c, true) }

// Open replays existing storage. Missing state is never initialized.
func Open(c Config) (*Witness, error) { return start(c, false) }

func textOK(s string) bool {
	return len(s) > 0 && len(s) <= assignment.MaxReferenceBytes && utf8.ValidString(s)
}
func (w *Witness) valid(s cp.Slot) bool {
	return s.Witness == w.identity.Witness && textOK(s.Authority) && textOK(s.Store)
}

func start(c Config, create bool) (*Witness, error) {
	id := c.Identity
	if !textOK(id.Witness) || !textOK(id.Store) || !textOK(id.Incarnation) || id.Epoch == 0 || c.ReceiptLimit > maxReceipts {
		return nil, &cp.Reason{Kind: cp.InvalidRequest}
	}
	homeID, err := cp.KeyID(c.HomePin)
	if err != nil || bytes.Equal(c.HomePin, c.Pin) {
		return nil, ErrIdentity
	}
	keyFile, err := openKey(c.KeyPath)
	if err != nil {
		return nil, err
	}
	// Transfer the descriptor to the witness only on successful startup.
	defer func() {
		if keyFile != nil {
			keyFile.Close()
		}
	}()
	key, err := readKey(keyFile)
	if err != nil {
		return nil, err
	}
	wid, err := cp.KeyID(c.Pin)
	if err != nil || !bytes.Equal(key.Public().(ed25519.PublicKey), c.Pin) {
		return nil, ErrIdentity
	}
	w := &Witness{
		transaction: make(chan struct{}, 1),
		identity:    id,
		pin:         slices.Clone(c.Pin),
		homePin:     slices.Clone(c.HomePin),
		keyID:       wid,
		limit:       c.ReceiptLimit,
		inhibited:   true,
		sign:        func(b []byte) ([]byte, error) { return cp.Sign(cp.WitnessNamespace, key, b) },
		syncFile:    (*os.File).Sync,
		signRecord:  func(b []byte) []byte { return ed25519.Sign(key, b) },
		slots:       make(map[cp.Slot]*enrollment),
		history:     make(map[item]accepted),
		grants:      make(map[cp.Slot]bool),
	}
	if w.limit == 0 {
		w.limit = maxReceipts
	}
	w.dir, err = directory(c.Directory, true)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			w.stopped = true // Leave any recovery marker in place after a failed start.
			w.Close()
		}
	}()
	if err = w.openPair(create, keyFile); err != nil {
		return nil, err
	}
	w.writeFrame = w.journal.Write
	w.writeEntry = w.lockFile.Write
	w.allocate = keepSize
	if create {
		if err = w.arm(); err != nil {
			return nil, err
		}
		_, err = w.commit(record{Seq: 1, Identity: &id, HomeKeyID: homeID, Op: "create"}, change{})
	} else {
		err = w.replay(homeID)
	}
	if err != nil {
		return nil, err
	}
	var probe [32]byte
	rand.Read(probe[:])
	sig, err := w.SignDisposition(context.Background(), probe, "startup-probe")
	if err != nil || !cp.VerifyDisposition(c.Pin, id.Epoch, probe, "startup-probe", sig) {
		return nil, ErrIdentity
	}
	if create {
		if err = w.publishPair(); err != nil {
			return nil, err
		}
	}
	w.keyFile, keyFile = keyFile, nil
	ok = true
	return w, nil
}

func (w *Witness) Close() error {
	if !w.lock(context.Background()) {
		return nil
	}
	defer w.unlock()
	clean := w.armed && !w.stopped && !w.recoveryRequired
	w.stopped = true
	var err error
	if w.journal != nil {
		err = w.journal.Close()
	}
	if clean && err == nil {
		// Finish index writeback under its exclusive lock before signalling a clean close.
		if err = w.lockFile.Sync(); err == nil {
			err = syscall.Unlinkat(int(w.dir.Fd()), recoveryName)
			// A lost unlink after a crash conservatively requires acknowledgement again.
		} else {
			err = w.poison(err)
		}
	}
	for _, f := range []*os.File{w.lockFile, w.dir, w.keyFile} {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
	}
	w.dir, w.lockFile, w.journal, w.keyFile = nil, nil, nil, nil
	w.armed = false
	w.sign, w.signRecord, w.writeFrame = nil, nil, nil
	return err
}
func (w *Witness) WitnessEpoch() uint64 { return w.identity.Epoch }
func (w *Witness) SignDisposition(ctx context.Context, digest [32]byte, result string) ([]byte, error) {
	if err := w.enter(ctx); err != nil {
		return nil, err
	}
	defer w.unlock()
	b := binary.BigEndian.AppendUint64([]byte("checkpoint disposition v2\x00"), w.identity.Epoch)
	b = append(b, digest[:]...)
	return w.sign(append(b, result...))
}

// Callers queued behind file I/O can abandon the queue at their deadline.
func (w *Witness) lock(ctx context.Context) bool {
	if w.transaction == nil {
		return false
	}
	select {
	case w.transaction <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
func (w *Witness) unlock() { <-w.transaction }
func (w *Witness) enter(ctx context.Context) error {
	if !w.lock(ctx) {
		return cp.ErrUnavailable
	}
	if ctx.Err() != nil || w.stopped {
		w.unlock()
		return cp.ErrUnavailable
	}
	return nil
}
func (w *Witness) Grant(slot cp.Slot, pin ed25519.PublicKey, write bool) error {
	if err := w.enter(context.Background()); err != nil {
		return err
	}
	defer w.unlock()
	if !bytes.Equal(pin, w.homePin) {
		return &cp.Reason{Kind: cp.HomeMismatch}
	}
	if !w.valid(slot) {
		return &cp.Reason{Kind: cp.InvalidRequest}
	}
	w.grants[slot] = write
	return nil
}
func (w *Witness) Revoke(slot cp.Slot) {
	if w.enter(context.Background()) != nil {
		return
	}
	defer w.unlock()
	delete(w.grants, slot)
}
func (w *Witness) Inhibit(on bool) {
	if w.enter(context.Background()) != nil {
		return
	}
	defer w.unlock()
	w.inhibited = on
}
func (w *Witness) CheckHome(ctx context.Context, slot cp.Slot, pin ed25519.PublicKey) error {
	if err := w.enter(ctx); err != nil {
		return err
	}
	defer w.unlock()
	return w.checkGrant(slot, pin, false)
}
func (w *Witness) checkGrant(slot cp.Slot, pin ed25519.PublicKey, write bool) error {
	if !bytes.Equal(pin, w.homePin) {
		return &cp.Reason{Kind: cp.HomeMismatch}
	}
	writable, ok := w.grants[slot]
	if !ok || (write && !writable) {
		return &cp.Reason{Kind: cp.Unauthorized}
	}
	return nil
}
func (w *Witness) HasReceipt(ctx context.Context, slot cp.Slot, id cp.ID) (bool, error) {
	if err := w.enter(ctx); err != nil {
		return false, err
	}
	defer w.unlock()
	return w.history[item{slot, id}].receipt.Body != nil, nil
}
func clone(r cp.Receipt) cp.Receipt {
	return cp.Receipt{Body: slices.Clone(r.Body), Signature: slices.Clone(r.Signature)}
}
func (w *Witness) after(stage string) error {
	if w.fault != nil {
		return w.fault(stage)
	}
	return nil
}
