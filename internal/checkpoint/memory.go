package checkpoint

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"math"
	"slices"
	"sync"

	"ataniq/internal/assignment"
)

type grant struct {
	key   ed25519.PublicKey
	keyID string
	write bool
}
type accepted struct {
	request []byte
	receipt Receipt
}
type enrollment struct {
	state         State
	headerBytes   []byte
	home, session ed25519.PublicKey
	history       map[ID]accepted
	claimed       map[string]bool
}

// Memory is an explicitly configured, deterministic test witness. Its handles
// share one serialized enrollment directory. It has no persistence or clock.
type Memory struct {
	mu                         sync.Mutex
	witness                    string
	epoch                      uint64
	key                        *memoryKey
	keyID                      string
	grants                     map[Slot]grant
	slots                      map[Slot]*enrollment
	fenced                     map[Slot]map[ID]bool
	inhibited, loseReply       bool
	transformNextReply         func(Receipt) Receipt
	receiptCount, receiptLimit uint64
}

func NewMemory(witness string, epoch uint64, key ed25519.PrivateKey) (*Memory, error) {
	if !textOK(witness) || epoch == 0 || !validPrivate(key) {
		return nil, &Reason{Kind: InvalidRequest}
	}
	id, err := KeyID(key.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}
	return &Memory{witness: witness, epoch: epoch, key: newMemoryKey(key, witnessProfile), keyID: id, grants: make(map[Slot]grant), slots: make(map[Slot]*enrollment), fenced: make(map[Slot]map[ID]bool)}, nil
}

// Grant installs an independently selected home pin for the exact slot. A
// read-only grant can reconcile receipts but cannot claim or advance.
func (w *Memory) Grant(slot Slot, pin ed25519.PublicKey, write bool) error {
	id, err := KeyID(pin)
	if err != nil || id == w.keyID || !slot.valid() || slot.Witness != w.witness {
		return &Reason{Kind: InvalidRequest}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.grants[slot] = grant{slices.Clone(pin), id, write}
	return nil
}
func (w *Memory) Revoke(slot Slot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.grants, slot)
}
func (w *Memory) Inhibit(inhibited bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inhibited = inhibited
}

// LoseNextReply drops the next successful new mutation response after commit.
func (w *Memory) LoseNextReply() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loseReply = true
}

// TransformNextReply installs a one-shot delivery fault for adapter qualification.
// The callback receives a detached receipt under the witness lock and must not
// call back into Memory. It cannot alter the committed evidence.
func (w *Memory) TransformNextReply(f func(Receipt) Receipt) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.transformNextReply = f
}

// SetReceiptLimit bounds the number of accepted mutations across the
// directory. Owner release and retirement may exceed this budget to fence a
// writer. Zero is unlimited. Recovery reads and retries consume no space.
func (w *Memory) SetReceiptLimit(limit uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.receiptLimit = limit
}

type admin struct{ w *Memory }

func (w *Memory) Administration() Administration { return admin{w} }

type client struct {
	w             *Memory
	slot          Slot
	home, session *memoryKey
	witnessPin    ed25519.PublicKey
}

// Both ends of a claim require three independently held key roles.
func checkSessionRole(session, home, witness ed25519.PublicKey) error {
	if bytes.Equal(session, home) || bytes.Equal(session, witness) {
		return &Reason{Kind: KeyRoleConflict}
	}
	return nil
}

// Connect creates an authenticated handle scoped to one home and process
// session. Mutations and reads recheck the live grant under the witness lock.
func (w *Memory) Connect(slot Slot, home, session ed25519.PrivateKey, witnessPin ed25519.PublicKey) (Client, error) {
	if !bytes.Equal(witnessPin, w.key.public) {
		return nil, &Reason{Kind: Unauthorized}
	}
	if !validPrivate(home) || !validPrivate(session) {
		return nil, &Reason{Kind: InvalidRequest}
	}
	h := home.Public().(ed25519.PublicKey)
	if err := w.CheckHome(slot, h); err != nil {
		return nil, err
	}
	if err := checkSessionRole(session.Public().(ed25519.PublicKey), h, witnessPin); err != nil {
		return nil, err
	}
	_, err := KeyID(session.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}
	return &client{w, slot, newMemoryKey(home, homeProfile), newMemoryKey(session, sessionProfile), slices.Clone(witnessPin)}, nil
}

// CheckHome checks the provisioned home pin independently of enrollment.
// Success grants no access to a prior enrollment bound to another key.
func (w *Memory) CheckHome(slot Slot, key ed25519.PublicKey) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	g, ok := w.grants[slot]
	if !ok || !bytes.Equal(key, g.key) {
		return &Reason{Kind: Unauthorized}
	}
	return nil
}
func cloneReceipt(r Receipt) Receipt { return Receipt{slices.Clone(r.Body), slices.Clone(r.Signature)} }

func (w *Memory) available(ctx context.Context) error {
	if ctx.Err() != nil || w.inhibited {
		return ErrUnavailable
	}
	return nil
}
func (w *Memory) authorized(slot Slot, home ed25519.PublicKey, write bool) (*enrollment, error) {
	g, ok := w.grants[slot]
	e := w.slots[slot]
	if !ok || write && !g.write || !bytes.Equal(g.key, home) {
		return nil, &Reason{Kind: Unauthorized}
	}
	if e == nil {
		return nil, &Reason{Kind: NotEnrolled}
	}
	if !bytes.Equal(g.key, e.home) {
		return nil, &Reason{Kind: Unauthorized}
	}
	if write && e.state.Status == Retired {
		return nil, ErrRetired
	}
	return e, nil
}

func (c *client) Read(ctx context.Context, q ReadRequest) (View, error) {
	return c.w.readHome(ctx, q, c.slot, c.home.public, c.witnessPin)
}

// ReadHome checks the current read grant for an already authenticated home key.
// The transport must verify the caller's signature over the read request first.
func (w *Memory) ReadHome(ctx context.Context, q ReadRequest, home ed25519.PublicKey) (View, error) {
	return w.readHome(ctx, q, q.Slot, home, w.key.public)
}

func (w *Memory) readHome(ctx context.Context, q ReadRequest, slot Slot, home, pin ed25519.PublicKey) (View, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ctx.Err() != nil {
		return View{}, ErrUnavailable
	}
	if q.Slot != slot {
		return View{}, &Reason{Kind: Unauthorized}
	}
	e, err := w.authorized(q.Slot, home, false)
	if err != nil {
		return View{}, err
	}
	return w.read(q, e, pin)
}

func (a admin) Read(ctx context.Context, q ReadRequest) (View, error) {
	w := a.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if ctx.Err() != nil {
		return View{}, ErrUnavailable
	}
	if !q.Slot.valid() || q.Slot.Witness != w.witness || q.Challenge == (ID{}) {
		return View{}, &Reason{Kind: InvalidRequest}
	}
	e := w.slots[q.Slot]
	if e == nil {
		return View{}, &Reason{Kind: NotEnrolled}
	}
	return w.read(q, e, w.key.public)
}

// HasReceipt probes the immutable history index for admission accounting.
// A fence without a committed receipt is absent from this index.
func (w *Memory) HasReceipt(ctx context.Context, slot Slot, id ID) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	e := w.slots[slot]
	if e == nil {
		return false, nil
	}
	_, ok := e.history[id]
	return ok, nil
}

// read snapshots both ends of the challenged view under the witness lock.
func (w *Memory) read(q ReadRequest, e *enrollment, pin ed25519.PublicKey) (View, error) {
	// Current comes from the live slot even when Prior names an older head.
	v := View{Current: e.state}
	if history, ok := e.history[q.ReceiptID]; ok {
		r := cloneReceipt(history.receipt)
		v.Prior = &r
	}
	guard := "ready"
	if w.inhibited {
		guard = "inhibited"
	}
	read, err := expectedRead(w.keyID, q, v, guard)
	if err != nil {
		return View{}, err
	}
	b, err := encode(read)
	if err != nil {
		return View{}, err
	}
	sig, err := w.key.sign(b)
	if err != nil {
		return View{}, err
	}
	v.Fresh = Receipt{b, sig}
	if err := verifyView(pin, w.keyID, q, v); err == ErrUnavailable {
		return v, err
	} else if err != nil {
		return View{}, err
	}
	return v, nil
}

func (e *enrollment) retry(id ID, b []byte) (Receipt, bool, error) {
	saved, ok := e.history[id]
	if !ok {
		return Receipt{}, false, nil
	}
	if !bytes.Equal(saved.request, b) {
		return Receipt{}, true, &Reason{Kind: MutationConflict}
	}
	if ReceiptDigest(saved.receipt) != e.state.LastReceiptDigest {
		return Receipt{}, true, &Reason{Kind: Superseded}
	}
	return cloneReceipt(saved.receipt), true, nil
}

// commit runs under mu. All fallible encoding precedes the state change.
// The signed result links to the previous receipt. The new receipt digest
// then becomes part of the next mutation's exact CAS predecessor.
func (w *Memory) commit(e *enrollment, op string, id ID, body []byte, s State, session ed25519.PublicKey) (Receipt, error) {
	if w.fenced[s.Slot][id] {
		return Receipt{}, ErrNotApplied
	}
	if op != "release" && op != "retire" && w.receiptLimit != 0 && w.receiptCount >= w.receiptLimit {
		return Receipt{}, ErrCapacity
	}
	// Position one is witness creation. Every receipt and new settlement
	// fence consumes one subsequent position, across all slots.
	if w.receiptCount >= math.MaxUint64-1 {
		return Receipt{}, &Reason{Kind: CounterExhausted}
	}
	s.JournalSequence = w.receiptCount + 2
	a := ackRecord{recordHeader{1, op, s.Slot.Authority, w.keyID}, idString(id), RequestDigest(body), recordState(s)}
	b, err := encode(a)
	if err != nil {
		return Receipt{}, err
	}
	sig, err := w.key.sign(b)
	if err != nil {
		return Receipt{}, err
	}
	r := Receipt{b, sig}
	s.LastReceiptDigest = ReceiptDigest(r)
	w.receiptCount++
	if op == "claim" {
		e.claimed[s.SessionKeyID] = true
	}
	e.state = s
	e.session = slices.Clone(session)
	e.history[id] = accepted{slices.Clone(body), r}
	w.slots[s.Slot] = e
	if w.loseReply {
		w.loseReply = false
		return Receipt{}, ErrUnknownOutcome
	}
	return cloneReceipt(r), nil
}

func (a admin) Enroll(ctx context.Context, q Enrollment) (Receipt, error) {
	w := a.w
	w.mu.Lock()
	defer w.mu.Unlock()
	g, ok := w.grants[q.Slot]
	if !ok || !bytes.Equal(g.key, q.HomePublicKey) {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	b, err := enrollmentBody(q, g.keyID)
	if err != nil {
		return Receipt{}, err
	}
	if !homeProfile.verify(g.key, b, q.ProofOfPossession) {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	old := w.slots[q.Slot]
	if old != nil {
		if r, found, err := old.retry(q.ID, b); found {
			return r, err
		}
	}
	if err := w.available(ctx); err != nil {
		return Receipt{}, err
	}
	if old != nil {
		if old.state.Status == Retired {
			return Receipt{}, ErrRetired
		}
		return Receipt{}, &Reason{Kind: EnrollmentConflict}
	}
	s, err := enrollmentState(q, g.keyID, w.epoch)
	if err != nil {
		return Receipt{}, err
	}
	h := s.Header
	for slot, old := range w.slots {
		if slot.Authority == q.Slot.Authority && (old.state.Status == Active || h.LedgerID == old.state.Header.LedgerID ||
			h.Incarnation == old.state.Header.Incarnation || h.KeyID == old.state.HomeKeyID) {
			return Receipt{}, &Reason{Kind: EnrollmentConflict}
		}
	}
	e := &enrollment{headerBytes: slices.Clone(q.HeaderBytes), home: slices.Clone(g.key), history: make(map[ID]accepted), claimed: make(map[string]bool)}
	return w.commit(e, "enroll", q.ID, b, s, nil)
}

func (c *client) Claim(ctx context.Context, q Claim) (Receipt, error) {
	if !bytes.Equal(q.NewSessionPublicKey, c.session.public) {
		return Receipt{}, &Reason{Kind: InvalidRequest}
	}
	b, err := q.Body("")
	if err != nil {
		return Receipt{}, err
	}
	return c.mutate(ctx, "claim", q.Mutation, b)
}
func (c *client) Advance(ctx context.Context, q Advance) (Receipt, error) {
	b, err := AdvanceBody(q)
	if err != nil {
		return Receipt{}, err
	}
	return c.mutate(ctx, "advance", q.Mutation, b)
}
func (c *client) mutate(ctx context.Context, op string, m Mutation, b []byte) (Receipt, error) {
	if m.Expected.Slot != c.slot {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	homeSig, err := c.home.sign(b)
	if err != nil {
		return Receipt{}, err
	}
	sessionSig, err := c.session.sign(b)
	if err != nil {
		return Receipt{}, err
	}
	r, err := c.w.RawSubmit(ctx, c.slot, c.home.public, op, b, homeSig, sessionSig)
	if err != nil {
		return Receipt{}, err
	}
	if err := MatchReceipt(c.witnessPin, m.Expected.Epoch, op, b, r); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// RawSubmit consumes only the signed bytes and independently pinned identities.
// The method selects the operation before interpreting its record.
func (w *Memory) RawSubmit(ctx context.Context, slot Slot, home ed25519.PublicKey, op string, b, homeSig, sessionSig []byte) (Receipt, error) {
	return w.accept(ctx, slot, home, op, b, homeSig, sessionSig)
}

func (w *Memory) accept(ctx context.Context, slot Slot, home ed25519.PublicKey, op string, b, homeSig, sessionSig []byte) (reply Receipt, err error) {
	intent, err := ParseIntent(op, w.keyID, b)
	if err != nil {
		return Receipt{}, err
	}
	var m Mutation
	var claimed ed25519.PublicKey
	var successor assignment.Checkpoint
	switch q := intent.(type) {
	case Claim:
		m = q.Mutation
		claimed = q.NewSessionPublicKey
	case Advance:
		m = q.Mutation
		successor = q.Next
	default:
		return Receipt{}, &Reason{Kind: InvalidRequest}
	}
	if m.Expected.Slot != slot {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	defer func() {
		if err == nil && w.transformNextReply != nil {
			change := w.transformNextReply
			w.transformNextReply = nil
			reply = change(reply)
		}
	}()
	e, err := w.authorized(slot, home, true)
	if err != nil {
		return Receipt{}, err
	}
	session := e.session
	sessionID := e.state.SessionKeyID
	if op == "claim" {
		session = claimed
		if err := checkSessionRole(session, e.home, w.key.public); err != nil {
			return Receipt{}, err
		}
		sessionID, err = KeyID(session)
		if err != nil {
			return Receipt{}, err
		}
	}
	if len(session) == 0 || !homeProfile.verify(e.home, b, homeSig) || !sessionProfile.verify(session, b, sessionSig) {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	// A past claim or advance cannot return success on a later fence.
	// Read retains the old receipt.
	if r, ok, err := e.retry(m.ID, b); ok {
		if err != nil {
			return Receipt{}, err
		}
		fence := m.Expected.Fence
		if op == "claim" {
			fence++
		}
		if fence != e.state.Fence || e.state.SessionKeyID != sessionID {
			return Receipt{}, &Reason{Kind: SessionConflict}
		}
		return r, nil
	}
	if err := w.available(ctx); err != nil {
		return Receipt{}, err
	}
	if m.Expected != e.state {
		return Receipt{}, &Reason{Kind: PredecessorMismatch}
	}
	if e.state.Revision == math.MaxUint64 {
		return Receipt{}, &Reason{Kind: CounterExhausted}
	}
	s := e.state
	s.Revision++
	if op == "claim" {
		if s.Fence == math.MaxUint64 {
			return Receipt{}, &Reason{Kind: CounterExhausted}
		}
		if len(e.session) != 0 || e.claimed[sessionID] {
			return Receipt{}, &Reason{Kind: SessionConflict}
		}
		s.Fence++
		s.SessionKeyID = sessionID
	} else {
		if s.Head.Sequence == math.MaxUint64 {
			return Receipt{}, &Reason{Kind: CounterExhausted}
		}
		expected := s.Head
		expected.Sequence++
		expected.Digest = successor.Digest
		if expected != successor {
			return Receipt{}, &Reason{Kind: InvalidSuccessor}
		}
		s.Head = successor
	}
	return w.commit(e, op, m.ID, b, s, session)
}
func (a admin) Release(ctx context.Context, m Mutation) (Receipt, error) {
	return a.end(ctx, "release", m, "")
}
func (a admin) Retire(ctx context.Context, q Retirement) (Receipt, error) {
	return a.end(ctx, "retire", q.Mutation, q.DispositionDigest)
}
func (a admin) end(ctx context.Context, op string, m Mutation, disposition string) (Receipt, error) {
	w := a.w
	w.mu.Lock()
	defer w.mu.Unlock()
	e := w.slots[m.Expected.Slot]
	if e == nil {
		return Receipt{}, &Reason{Kind: NotEnrolled}
	}
	b, err := mutationBody(op, w.keyID, m, nil, nil, disposition)
	if err != nil {
		return Receipt{}, err
	}
	if r, ok, err := e.retry(m.ID, b); ok {
		return r, err
	}
	if err := w.available(ctx); err != nil {
		return Receipt{}, err
	}
	if e.state.Status == Retired {
		return Receipt{}, ErrRetired
	}
	s := e.state
	if m.Expected != s {
		return Receipt{}, &Reason{Kind: PredecessorMismatch}
	}
	if s.Revision == math.MaxUint64 || s.Fence == math.MaxUint64 {
		return Receipt{}, &Reason{Kind: CounterExhausted}
	}
	if op == "release" && len(e.session) == 0 {
		return Receipt{}, &Reason{Kind: SessionConflict}
	}
	s.Revision++
	s.Fence++
	s.SessionKeyID = ""
	if op == "retire" {
		s.Status = Retired
	}
	return w.commit(e, op, m.ID, b, s, nil)
}
