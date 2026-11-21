//go:build linux && ataniq_canary

package leadcontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ataniq/internal/leadrole"
)

// Status is the state of a command's receipt. in-progress is the only status
// that is not final.
type Status string

// The statuses of a receipt.
const (
	// StatusInProgress: the command is admitted and has no final answer.
	StatusInProgress Status = "in-progress"
	// StatusDone: the command was carried out; the result is in the receipt.
	StatusDone Status = "done"
	// StatusRefused: the command was refused and changed nothing that
	// matters; the code and the message say why.
	StatusRefused Status = "refused"
	// StatusUnavailable: the command could not be carried out.
	StatusUnavailable Status = "unavailable"
	// StatusUncertain: it is not known whether the command was carried out.
	// The state of the role is the authority.
	StatusUncertain Status = "uncertain"
)

func (s Status) final() bool {
	switch s {
	case StatusDone, StatusRefused, StatusUnavailable, StatusUncertain:
		return true
	}
	return false
}

// The bounds of the table, and the fixed words of a command that a restart
// interrupted.
const (
	// DefaultMaxReceipts and DefaultMaxTombstones are what [Options] means by
	// a zero bound.
	DefaultMaxReceipts   = 1024
	DefaultMaxTombstones = 4096
	// MaxLineBytes is the longest line of the file, its newline included.
	MaxLineBytes = 4096
	// MaxClientBytes, MaxIDBytes and MaxMessageBytes bound the client
	// string, the command id and the message of a receipt.
	MaxClientBytes  = 64
	MaxIDBytes      = 64
	MaxMessageBytes = 256
	// OpTicket is the operation whose interrupted receipt reads unavailable:
	// a ticket repeats harmlessly. Every other operation reads uncertain.
	OpTicket = "lead.ticket"
	// CodeInterrupted and MessageInterrupted are the code and the text of a
	// receipt that a restart ended.
	CodeInterrupted    = "interrupted"
	MessageInterrupted = "the daemon restarted before the command finished; read the state of the lead"
)

// Errors. Each is wrapped, so test with errors.Is.
var (
	// ErrConflict: the key is held with another operation, role or digest, or
	// the command id belongs to an open command of another client.
	ErrConflict = errors.New("leadcontrol: the command id is held for another command")
	// ErrFull: every slot of the table holds a receipt that is not final.
	ErrFull = errors.New("leadcontrol: the command table is full")
	// ErrExpired: the key was held once and has been dropped. The command is
	// not run again.
	ErrExpired = errors.New("leadcontrol: the command id is no longer held")
	// ErrInvalid: a binding or a finish is not well formed.
	ErrInvalid = errors.New("leadcontrol: invalid argument")
	// ErrNotOpen: no command that is not final has the id.
	ErrNotOpen = errors.New("leadcontrol: no open command has this id")
	// ErrTooLarge: a line would be longer than MaxLineBytes.
	ErrTooLarge = errors.New("leadcontrol: the line is too long")
	// ErrCorrupt: the file is damaged; nothing was repaired.
	ErrCorrupt = errors.New("leadcontrol: the command table is corrupt")
	// ErrInsecure: the file or its folder is not what a private file must be.
	ErrInsecure = errors.New("leadcontrol: the command table file is not private")
	// ErrLocked: another process holds the file.
	ErrLocked = errors.New("leadcontrol: the command table is locked by another process")
	// ErrBroken: a write or a sync failed; the table takes no more commands.
	ErrBroken = errors.New("leadcontrol: the command table cannot be written")
	// ErrClosed: the table was closed.
	ErrClosed = errors.New("leadcontrol: the command table is closed")
)

// Binding is what a command is bound to. The key is (Client, ID).
type Binding struct {
	// Client is 1 to 64 printable ASCII bytes without a space, from the
	// params. It is a namespace, not an identity.
	Client string
	// ID is 1 to 64 bytes of [A-Za-z0-9._:-], from the params.
	ID string
	// Op is the operation, for example "lead.start": 1 to 32 bytes of
	// [a-z0-9._-].
	Op string
	// Role is the role the command is about.
	Role leadrole.Role
	// Digest is the SHA-256 of the canonical payload.
	Digest [sha256.Size]byte
}

func (b Binding) validate() error {
	switch {
	case !validClient(b.Client):
		return fmt.Errorf("%w: the client is not 1 to %d printable bytes", ErrInvalid, MaxClientBytes)
	case !validID(b.ID):
		return fmt.Errorf("%w: the command id is not 1 to %d bytes of A-Z, a-z, 0-9 and . _ : -", ErrInvalid, MaxIDBytes)
	case !validOp(b.Op):
		return fmt.Errorf("%w: the operation is not 1 to 32 bytes of a-z, 0-9 and . _ -", ErrInvalid)
	case b.Role.IsZero():
		return fmt.Errorf("%w: the role is the zero role", ErrInvalid)
	}
	return nil
}

// Receipt is the answer to a command, and the JSON of the wire.
type Receipt struct {
	CommandID  string          `json:"command_id"`
	Op         string          `json:"op"`
	Role       string          `json:"role"`
	Status     Status          `json:"status"`
	Code       string          `json:"code,omitempty"`    // reason code of refused and unavailable
	Message    string          `json:"message,omitempty"` // fixed text
	Result     json.RawMessage `json:"result,omitempty"`  // the operation's result when done
	AdmittedAt time.Time       `json:"admitted_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// clone copies the parts of a receipt that a caller could change through the
// copy.
func (r Receipt) clone() Receipt {
	if r.Result != nil {
		r.Result = append(json.RawMessage(nil), r.Result...)
	}
	if r.FinishedAt != nil {
		at := *r.FinishedAt
		r.FinishedAt = &at
	}
	return r
}

// Options are the settings of [Open].
type Options struct {
	// MaxReceipts is the number of receipts held; zero means
	// DefaultMaxReceipts.
	MaxReceipts int
	// MaxTombstones is the number of tombstones held; zero means
	// DefaultMaxTombstones.
	MaxTombstones int
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type key struct{ client, id string }

// hash is the tombstone of a key: SHA-256 over the client, a NUL (which a
// client cannot hold) and the id.
func (k key) hash() [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(k.client))
	h.Write([]byte{0})
	h.Write([]byte(k.id))
	var out [sha256.Size]byte
	h.Sum(out[:0])
	return out
}

// entry is one held receipt with what the file needs to write it again.
type entry struct {
	key         key
	role        leadrole.Role
	digest      [sha256.Size]byte
	rec         Receipt
	interrupted bool // the final line is an interrupted line, not a finish
}

// Table is the command table. It is safe for concurrent use.
type Table struct {
	mu            sync.Mutex
	f             *os.File
	now           func() time.Time
	maxReceipts   int
	maxTombstones int
	receipts      map[key]*entry
	order         []*entry // admission order, oldest first
	open          map[string]*entry
	tombs         map[[sha256.Size]byte]struct{}
	tombOrder     [][sha256.Size]byte // oldest first
	trimmed       bool                // a tombstone was forgotten
	// fileTombs holds the tombstones that the file has since Open. Only Open
	// writes tombstones, so the set never grows, and it keeps a key that the
	// running table has forgotten from being admitted after its tombstone, which
	// the next Open would refuse as corrupt.
	fileTombs map[[sha256.Size]byte]struct{}
	write     func([]byte) (int, error)
	sync      func() error
	closed    bool
	broken    error
}

// Open opens the table at path, creating the file with mode 0600 when it is
// not there. The folder must exist and must not be a symbolic link. It
// replays the file, cuts an unfinished last line, ends every command that was
// never finished with an interrupted line, and compacts the file when more
// than MaxReceipts receipts are held.
func Open(path string, o Options) (*Table, error) {
	if o.MaxReceipts < 0 || o.MaxTombstones < 0 {
		return nil, fmt.Errorf("%w: a bound is negative", ErrInvalid)
	}
	t := &Table{
		now:           o.Now,
		maxReceipts:   o.MaxReceipts,
		maxTombstones: o.MaxTombstones,
		receipts:      map[key]*entry{},
		open:          map[string]*entry{},
		tombs:         map[[sha256.Size]byte]struct{}{},
	}
	if t.now == nil {
		t.now = time.Now
	}
	if t.maxReceipts == 0 {
		t.maxReceipts = DefaultMaxReceipts
	}
	if t.maxTombstones == 0 {
		t.maxTombstones = DefaultMaxTombstones
	}
	path = filepath.Clean(path)
	dirFD, err := openDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer closeFD(dirFD)
	f, err := openFile(dirFD, filepath.Base(path), path)
	if err != nil {
		return nil, err
	}
	t.f = f
	if err := t.start(dirFD, path); err != nil {
		_ = t.f.Close()
		return nil, err
	}
	t.write, t.sync = t.f.Write, t.f.Sync
	t.fileTombs = make(map[[sha256.Size]byte]struct{}, len(t.tombs))
	for h := range t.tombs {
		t.fileTombs[h] = struct{}{}
	}
	return t, nil
}

// Admit binds the command to its key and makes the binding durable before it
// returns. For a key that is held with the same operation, role and digest it
// returns the stored receipt and false, and the command must not run again,
// whatever the status, also on a table that can no longer be written
// ([ErrBroken]): a command that was admitted keeps its receipt, in progress
// when its finish could not be written. For a new key it returns a receipt in
// progress and true, and the caller may act. A key held with anything else is
// [ErrConflict], a dropped key is [ErrExpired], and a table whose every slot is
// open is [ErrFull]; none of them admits anything.
func (t *Table) Admit(b Binding) (r Receipt, fresh bool, err error) {
	if err := b.validate(); err != nil {
		return Receipt{}, false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return Receipt{}, false, ErrClosed
	}
	k := key{b.Client, b.ID}
	if e, ok := t.receipts[k]; ok {
		// A held key is answered even when the table cannot be written: the
		// receipt is what the table knows, and a client that asks again must not
		// be told that nothing was admitted.
		if e.rec.Op != b.Op || e.role != b.Role || e.digest != b.Digest {
			return Receipt{}, false, ErrConflict
		}
		return e.rec.clone(), false, nil
	}
	if t.broken != nil {
		// Nothing new is admitted.
		return Receipt{}, false, t.broken
	}
	h := k.hash()
	if _, ok := t.tombs[h]; ok {
		return Receipt{}, false, ErrExpired
	}
	if _, ok := t.fileTombs[h]; ok {
		// The running table forgot this tombstone, the file still has it: an
		// admit line after it would make the next Open fail.
		return Receipt{}, false, ErrExpired
	}
	if _, ok := t.open[b.ID]; ok {
		// Finish names a command by its id alone, so an open id is unique.
		return Receipt{}, false, ErrConflict
	}
	if len(t.receipts) >= t.maxReceipts && len(t.open) == len(t.receipts) {
		return Receipt{}, false, ErrFull
	}
	at := t.now().UTC()
	l := line{
		Kind: kindAdmit, At: formatTime(at), Client: b.Client, ID: b.ID, Op: b.Op,
		Role: b.Role.String(), Digest: fmt.Sprintf("%x", b.Digest[:]),
	}
	data, err := encodeLine(l)
	if err != nil {
		return Receipt{}, false, err
	}
	if err := t.appendLocked(data); err != nil {
		return Receipt{}, false, err
	}
	e := &entry{
		key: k, role: b.Role, digest: b.Digest,
		rec: Receipt{
			CommandID: b.ID, Op: b.Op, Role: b.Role.String(), Status: StatusInProgress, AdmittedAt: at,
		},
	}
	t.add(e)
	for len(t.receipts) > t.maxReceipts && t.evictOne() {
	}
	return e.rec.clone(), true, nil
}

// Finish gives the open command with the id its final answer and makes it
// durable. st must be final (done, refused, unavailable or uncertain), code is
// empty or a short token of a-z, 0-9 and . _ -, msg is empty or printable
// ASCII of at most 256 bytes, and result is any value that marshals to JSON or
// nil. A line that would be over MaxLineBytes is [ErrTooLarge] and leaves the
// command open. An id that no open command has is [ErrNotOpen].
func (t *Table) Finish(id string, st Status, code, msg string, result any) (Receipt, error) {
	switch {
	case !st.final():
		return Receipt{}, fmt.Errorf("%w: the status is not final", ErrInvalid)
	case !validToken(code):
		return Receipt{}, fmt.Errorf("%w: the code is not a short token", ErrInvalid)
	case !validMessage(msg):
		return Receipt{}, fmt.Errorf("%w: the message is not printable ASCII of at most %d bytes", ErrInvalid, MaxMessageBytes)
	}
	var raw json.RawMessage
	if result != nil {
		data, err := marshalCompact(result)
		if err != nil {
			return Receipt{}, fmt.Errorf("%w: the result does not marshal: %v", ErrInvalid, err)
		}
		if !bytes.Equal(data, []byte("null")) {
			raw = data
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.usable(); err != nil {
		return Receipt{}, err
	}
	e, ok := t.open[id]
	if !ok {
		return Receipt{}, ErrNotOpen
	}
	at := t.now().UTC()
	data, err := encodeLine(line{
		Kind: kindFinish, At: formatTime(at), Client: e.key.client, ID: id,
		Status: st, Code: code, Message: msg, Result: raw,
	})
	if err != nil {
		return Receipt{}, err
	}
	if err := t.appendLocked(data); err != nil {
		return Receipt{}, err
	}
	e.rec.Status, e.rec.Code, e.rec.Message, e.rec.Result = st, code, msg, raw
	e.rec.FinishedAt = &at
	delete(t.open, id)
	return e.rec.clone(), nil
}

// Last returns the receipt of the newest command admitted for the role that
// the table still holds.
func (t *Table) Last(role leadrole.Role) (Receipt, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.order) - 1; i >= 0; i-- {
		if t.order[i].role == role {
			return t.order[i].rec.clone(), true
		}
	}
	return Receipt{}, false
}

// Close closes the file and releases its lock. Every later call but Close
// fails with [ErrClosed].
func (t *Table) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	return t.f.Close()
}

func (t *Table) usable() error {
	switch {
	case t.closed:
		return ErrClosed
	case t.broken != nil:
		return t.broken
	}
	return nil
}

// appendLocked writes one whole line and syncs it. A failure of either closes
// the table for good: the file may hold any part of the line, and a line after
// it would sit behind damage.
func (t *Table) appendLocked(data []byte) error {
	n, err := t.write(data)
	if err == nil && n != len(data) {
		err = errors.New("short write")
	}
	if err == nil {
		err = t.sync()
	}
	if err != nil {
		t.broken = fmt.Errorf("%w: %v", ErrBroken, err)
		return t.broken
	}
	return nil
}

// add holds a new entry.
func (t *Table) add(e *entry) {
	t.receipts[e.key] = e
	t.order = append(t.order, e)
	if !e.rec.Status.final() {
		t.open[e.key.id] = e
	}
}

// evictOne drops the oldest receipt that is final and leaves its tombstone. It
// reports whether there was one.
func (t *Table) evictOne() bool {
	for i, e := range t.order {
		if !e.rec.Status.final() {
			continue
		}
		t.order = append(t.order[:i], t.order[i+1:]...)
		delete(t.receipts, e.key)
		t.addTombstone(e.key.hash())
		return true
	}
	return false
}

// addTombstone remembers a dropped key and forgets the oldest tombstone when
// there are too many.
func (t *Table) addTombstone(h [sha256.Size]byte) {
	t.tombs[h] = struct{}{}
	t.tombOrder = append(t.tombOrder, h)
	for len(t.tombOrder) > t.maxTombstones {
		delete(t.tombs, t.tombOrder[0])
		t.trimmed = true
		t.tombOrder = t.tombOrder[1:]
	}
}

func validClient(s string) bool {
	if len(s) < 1 || len(s) > MaxClientBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func validID(s string) bool {
	if len(s) < 1 || len(s) > MaxIDBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

func validOp(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// validToken reports an empty string or 1 to 64 bytes of a-z, 0-9 and . _ -.
func validToken(s string) bool {
	if len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// validMessage reports an empty string or printable ASCII of at most
// MaxMessageBytes.
func validMessage(s string) bool {
	if len(s) > MaxMessageBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
