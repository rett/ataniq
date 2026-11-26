package durable

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"math"
	"slices"

	"ataniq/internal/assignment"
	cp "ataniq/internal/checkpoint"
)

type admin struct{ w *Witness }

func (w *Witness) Administration() cp.Administration { return admin{w} }
func (a admin) Enroll(ctx context.Context, q cp.Enrollment) (cp.Receipt, error) {
	b, err := q.Body("")
	if err != nil {
		return cp.Receipt{}, err
	}
	w := a.w
	if err := w.enter(ctx); err != nil {
		return cp.Receipt{}, err
	}
	defer w.unlock()
	if err = w.checkGrant(q.Slot, q.HomePublicKey, false); err != nil {
		return cp.Receipt{}, err
	}
	return w.submit(ctx, record{Op: "enroll", Body: b, HomeProof: q.ProofOfPossession})
}
func (a admin) Release(ctx context.Context, q cp.Mutation) (cp.Receipt, error) {
	return a.end(ctx, "release", q)
}
func (a admin) Retire(ctx context.Context, q cp.Retirement) (cp.Receipt, error) {
	return a.end(ctx, "retire", q)
}
func (a admin) end(ctx context.Context, op string, q cp.Intent) (cp.Receipt, error) {
	b, err := q.Body(a.w.keyID)
	if err != nil {
		return cp.Receipt{}, err
	}
	if err := a.w.enter(ctx); err != nil {
		return cp.Receipt{}, err
	}
	defer a.w.unlock()
	return a.w.submit(ctx, record{Op: op, Body: b})
}

func (w *Witness) RawSubmit(ctx context.Context, slot cp.Slot, home ed25519.PublicKey, op string, b, hs, ss []byte) (cp.Receipt, error) {
	if op != "claim" && op != "advance" {
		return cp.Receipt{}, &cp.Reason{Kind: cp.InvalidRequest}
	}
	if err := w.enter(ctx); err != nil {
		return cp.Receipt{}, err
	}
	defer w.unlock()
	if err := w.authorize(slot, home, true); err != nil {
		return cp.Receipt{}, err
	}
	return w.submit(ctx, record{Op: op, Slot: &slot, Body: b, HomeProof: hs, SessionProof: ss})
}
func (a admin) Settle(ctx context.Context, epoch uint64, slot cp.Slot, id cp.ID) (cp.Receipt, error) {
	if err := a.w.enter(ctx); err != nil {
		return cp.Receipt{}, err
	}
	defer a.w.unlock()
	return a.w.settle(ctx, epoch, slot, id)
}
func (w *Witness) SettleHome(ctx context.Context, epoch uint64, slot cp.Slot, id cp.ID, home ed25519.PublicKey) (cp.Receipt, error) {
	if err := w.enter(ctx); err != nil {
		return cp.Receipt{}, err
	}
	defer w.unlock()
	if err := w.authorize(slot, home, true); err != nil {
		return cp.Receipt{}, err
	}
	return w.settle(ctx, epoch, slot, id)
}
func (w *Witness) settle(ctx context.Context, epoch uint64, slot cp.Slot, id cp.ID) (cp.Receipt, error) {
	if epoch != w.identity.Epoch {
		return cp.Receipt{}, cp.ErrStale
	}
	return w.submit(ctx, record{Op: "settle", Slot: &slot, ID: &id})
}

type change struct {
	target        item
	state         cp.State
	home, session ed25519.PublicKey
	ack           []byte
}

func (w *Witness) submit(ctx context.Context, r record) (cp.Receipt, error) {
	c, retry, err := w.prepare(r, false)
	if err != nil || retry.Body != nil {
		return retry, err
	}
	if ctx.Err() != nil {
		return cp.Receipt{}, cp.ErrUnavailable
	}
	return w.commit(r, c)
}

// prepare is shared by replay and live submissions. Only live submissions
// consult today's inhibition and budget. Grants were checked by the entry
// point under this lock. Historical proofs are checked against the enrolled
// home and session at this position in the journal.
func (w *Witness) prepare(r record, replay bool) (change, cp.Receipt, error) {
	var c change
	var empty cp.Receipt
	var q cp.Intent
	var m cp.Mutation
	var err error
	if r.Op == "settle" {
		if r.Slot == nil || r.ID == nil || *r.ID == (cp.ID{}) || !w.valid(*r.Slot) || r.Body != nil || r.HomeProof != nil || r.SessionProof != nil {
			return c, empty, &cp.Reason{Kind: cp.InvalidRequest}
		}
		c.target = item{*r.Slot, *r.ID}
	} else {
		if r.ID != nil {
			return c, empty, ErrCorrupt
		}
		q, err = cp.ParseIntent(r.Op, w.keyID, r.Body)
		if err != nil {
			return c, empty, err
		}
		switch v := q.(type) {
		case cp.Enrollment:
			c.target = item{v.Slot, v.ID}
			c.home = v.HomePublicKey
		case cp.Claim:
			m = v.Mutation
			c.session = v.NewSessionPublicKey
		case cp.Advance:
			m = v.Mutation
		case cp.Mutation:
			m = v
		case cp.Retirement:
			m = v.Mutation
		}
		if r.Op != "enroll" {
			c.target = item{m.Expected.Slot, m.ID}
		}
		if !w.valid(c.target.slot) {
			return c, empty, &cp.Reason{Kind: cp.InvalidRequest}
		}
	}
	e := w.slots[c.target.slot]
	if err := w.checkProofs(r, &c, e); err != nil {
		return c, empty, err
	}
	if prior, ok := w.history[c.target]; ok {
		if replay {
			return c, empty, ErrCorrupt
		}
		if prior.receipt.Body == nil {
			return c, empty, cp.ErrNotApplied
		}
		if r.Op != "settle" {
			if prior.digest != cp.RequestDigest(r.Body) {
				return c, empty, &cp.Reason{Kind: cp.MutationConflict}
			}
			if cp.ReceiptDigest(prior.receipt) != e.state.LastReceiptDigest {
				return c, empty, &cp.Reason{Kind: cp.Superseded}
			}
		}
		return c, clone(prior.receipt), nil
	}
	if !replay {
		if w.inhibited {
			return c, empty, cp.ErrUnavailable
		}
		if r.Op != "release" && r.Op != "retire" && uint64(len(w.history)) >= w.limit {
			return c, empty, &CapacityError{w.limit}
		}
	}
	// At most one release per claim and one retirement per enrollment can
	// spend reserved capacity. Apply the same absolute bound during replay.
	if len(w.history) >= 2*maxReceipts {
		return c, empty, &CapacityError{2 * maxReceipts}
	}
	switch v := q.(type) {
	case nil:
		return c, empty, nil // new settlement fence
	case cp.Enrollment:
		c.state, err = w.genesis(v)
	default:
		c.state, err = successor(q, m, e, c.session)
	}
	if err != nil {
		return c, empty, err
	}
	c.ack, err = w.ackBody(r, c)
	return c, empty, err
}

func (w *Witness) checkProofs(r record, c *change, e *enrollment) error {
	denied := &cp.Reason{Kind: cp.Unauthorized}
	switch r.Op {
	case "enroll":
		if r.Slot != nil || r.SessionProof != nil || bytes.Equal(c.home, w.pin) || cp.VerifySignature(cp.HomeNamespace, c.home, r.Body, r.HomeProof) != nil {
			return denied
		}
	case "claim", "advance":
		if r.Slot == nil || *r.Slot != c.target.slot {
			return denied
		}
		if e == nil {
			return &cp.Reason{Kind: cp.NotEnrolled}
		}
		if e.state.Status == cp.Retired {
			return cp.ErrRetired
		}
		if r.Op == "advance" {
			c.session = e.session
		}
		if bytes.Equal(c.session, w.pin) || bytes.Equal(c.session, e.home) {
			return &cp.Reason{Kind: cp.KeyRoleConflict}
		}
		if cp.VerifySignature(cp.HomeNamespace, e.home, r.Body, r.HomeProof) != nil || cp.VerifySignature(cp.SessionNamespace, c.session, r.Body, r.SessionProof) != nil {
			return denied
		}
	case "release", "retire":
		if r.Slot != nil || r.HomeProof != nil || r.SessionProof != nil {
			return denied
		}
		if e == nil {
			return &cp.Reason{Kind: cp.NotEnrolled}
		}
	}
	return nil
}

func (w *Witness) genesis(q cp.Enrollment) (cp.State, error) {
	var s cp.State
	if e := w.slots[q.Slot]; e != nil {
		if e.state.Status == cp.Retired {
			return s, cp.ErrRetired
		}
		return s, &cp.Reason{Kind: cp.EnrollmentConflict}
	}
	var frame struct{ Header assignment.Header }
	ledger, err := assignment.NewLedgerFromHeader(q.HeaderBytes)
	homeID, _ := cp.KeyID(q.HomePublicKey) // checked by ParseIntent
	if err != nil || json.Unmarshal(q.HeaderBytes, &frame) != nil || frame.Header.Authority != q.Slot.Authority || frame.Header.Store != q.Slot.Store || frame.Header.KeyID != homeID {
		return s, &cp.Reason{Kind: cp.InvalidRequest}
	}
	for slot, e := range w.slots {
		h := e.state.Header
		if slot.Authority == q.Slot.Authority && (e.state.Status == cp.Active || h.LedgerID == frame.Header.LedgerID || h.Incarnation == frame.Header.Incarnation || h.KeyID == homeID) {
			return s, &cp.Reason{Kind: cp.EnrollmentConflict}
		}
	}
	return cp.State{Slot: q.Slot, Header: frame.Header, Head: ledger.Checkpoint(), Epoch: w.identity.Epoch, Revision: 1, HomeKeyID: homeID, Status: cp.Active}, nil
}

func successor(q cp.Intent, m cp.Mutation, e *enrollment, session ed25519.PublicKey) (cp.State, error) {
	s := e.state
	if s.Status == cp.Retired {
		return s, cp.ErrRetired
	}
	if m.Expected != s {
		return s, &cp.Reason{Kind: cp.PredecessorMismatch}
	}
	if s.Revision == math.MaxUint64 {
		return s, &cp.Reason{Kind: cp.CounterExhausted}
	}
	s.Revision++
	if a, ok := q.(cp.Advance); ok {
		next := s.Head
		next.Sequence++
		next.Digest = a.Next.Digest
		if next.Sequence == 0 || next != a.Next {
			return s, &cp.Reason{Kind: cp.InvalidSuccessor}
		}
		s.Head = next
		return s, nil
	}
	if s.Fence == math.MaxUint64 {
		return s, &cp.Reason{Kind: cp.CounterExhausted}
	}
	s.Fence++
	switch q.(type) {
	case cp.Claim:
		id, err := cp.KeyID(session)
		if err != nil {
			return s, err
		}
		if s.SessionKeyID != "" || e.claimed[id] {
			return s, &cp.Reason{Kind: cp.SessionConflict}
		}
		s.SessionKeyID = id
	case cp.Mutation:
		if s.SessionKeyID == "" {
			return s, &cp.Reason{Kind: cp.SessionConflict}
		}
		s.SessionKeyID = ""
	case cp.Retirement:
		s.Status = cp.Retired
		s.SessionKeyID = ""
	}
	return s, nil
}

func (w *Witness) apply(r record, c change, sig []byte) cp.Receipt {
	if r.Op == "create" {
		return cp.Receipt{}
	}
	if r.Op == "settle" {
		w.history[c.target] = accepted{}
		return cp.Receipt{}
	}
	receipt := cp.Receipt{Body: c.ack, Signature: sig}
	e := w.slots[c.target.slot]
	if e == nil {
		e = &enrollment{home: slices.Clone(c.home), claimed: make(map[string]bool)}
		w.slots[c.target.slot] = e
	}
	e.state = c.state
	e.state.LastReceiptDigest = cp.ReceiptDigest(receipt)
	e.session = slices.Clone(c.session)
	if r.Op == "claim" {
		e.claimed[c.state.SessionKeyID] = true
	}
	w.history[c.target] = accepted{cp.RequestDigest(r.Body), receipt}
	return clone(receipt)
}
