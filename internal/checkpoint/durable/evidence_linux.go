package durable

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"ataniq/internal/assignment/wire"
	cp "ataniq/internal/checkpoint"
)

// These are the checkpoint v1 acknowledgement and challenged-read bodies.
// The protocol verifier checks the encoding independently on replay and read.
func protocolState(s cp.State) map[string]any {
	h := s.Head
	decimal := func(n uint64) string { return strconv.FormatUint(n, 10) }
	return map[string]any{
		"slot":   s.Slot,
		"header": s.Header,
		"head": map[string]any{
			"authority":   h.Authority,
			"store":       h.Store,
			"incarnation": h.Incarnation,
			"ledger_id":   h.LedgerID,
			"sequence":    decimal(h.Sequence),
			"digest":      h.Digest,
		},
		"epoch":               decimal(s.Epoch),
		"journal_sequence":    decimal(s.JournalSequence),
		"revision":            decimal(s.Revision),
		"fence":               decimal(s.Fence),
		"home_key_id":         s.HomeKeyID,
		"session_key_id":      s.SessionKeyID,
		"status":              s.Status,
		"last_receipt_digest": s.LastReceiptDigest,
	}
}
func canonical(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return wire.Canonicalize(b)
}
func (w *Witness) ackBody(r record, c change) ([]byte, error) {
	b, err := canonical(map[string]any{
		"schema":         1,
		"operation":      r.Op,
		"audience":       c.target.slot.Authority,
		"key_id":         w.keyID,
		"id":             hex.EncodeToString(c.target.id[:]),
		"request_digest": cp.RequestDigest(r.Body),
		"result":         protocolState(c.state),
	})
	if len(b) > cp.MaxFrame {
		return nil, &cp.Reason{Kind: cp.InvalidRequest}
	}
	return b, err
}

func (a admin) Read(ctx context.Context, q cp.ReadRequest) (cp.View, error) {
	if err := a.w.enter(ctx); err != nil {
		return cp.View{}, err
	}
	defer a.w.unlock()
	return a.w.read(q)
}
func (w *Witness) ReadHome(ctx context.Context, q cp.ReadRequest, home ed25519.PublicKey) (cp.View, error) {
	if err := w.enter(ctx); err != nil {
		return cp.View{}, err
	}
	defer w.unlock()
	if err := w.authorize(q.Slot, home, false); err != nil {
		return cp.View{}, err
	}
	return w.read(q)
}
func (w *Witness) read(q cp.ReadRequest) (cp.View, error) {
	var v cp.View
	if !w.valid(q.Slot) || q.Challenge == (cp.ID{}) {
		return v, &cp.Reason{Kind: cp.InvalidRequest}
	}
	e := w.slots[q.Slot]
	if e == nil {
		return v, &cp.Reason{Kind: cp.NotEnrolled}
	}
	v.Current = e.state
	var id, prior string
	if q.ReceiptID != (cp.ID{}) {
		id = hex.EncodeToString(q.ReceiptID[:])
		if r := w.history[item{q.Slot, q.ReceiptID}].receipt; r.Body != nil {
			saved := clone(r)
			v.Prior = &saved
			prior = cp.ReceiptDigest(saved)
		}
	}
	guard := "ready"
	if w.inhibited {
		guard = "inhibited"
	}
	b, err := canonical(map[string]any{"schema": 1, "operation": "read", "audience": q.Slot.Authority, "key_id": w.keyID,
		"challenge": hex.EncodeToString(q.Challenge[:]), "guard": guard, "current": protocolState(v.Current), "receipt_id": id, "prior_digest": prior})
	if err != nil {
		return cp.View{}, err
	}
	sig, err := w.sign(b)
	if err != nil {
		return cp.View{}, err
	}
	v.Fresh = cp.Receipt{Body: b, Signature: sig}
	return v, cp.VerifyView(w.pin, q, v)
}
