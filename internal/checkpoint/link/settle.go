package link

import (
	"context"
	"crypto/rand"
	"errors"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
)

// Retain the intent before handoff. A receipt or fence received while an
// exchange is winding down may already have cleared it when finish runs.
func (q *requestQueue) begin(ref mutationRef) (retry bool, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.submitting || q.pending != nil && *q.pending != ref {
		return false, errors.Join(ErrNotSent, cp.ErrBusy)
	}
	retry = q.pending != nil
	q.pending = &ref
	q.submitting = true
	return retry, nil
}
func (q *requestQueue) finish(retry bool, err error) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.submitting = false
	if err == nil || errors.Is(err, cp.ErrNotApplied) || !retry && !errors.Is(err, cp.ErrUnknownOutcome) {
		q.pending = nil
	}
	if retry && q.pending != nil {
		return cp.ErrUnknownOutcome
	}
	return err
}

func (q *requestQueue) resolve(id cp.ID, digest string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending != nil && q.pending.id == id && (digest == "" || q.pending.digest == digest) {
		q.pending = nil
	}
}

func (c *client) Settle(ctx context.Context, slot cp.Slot, id cp.ID) (cp.Receipt, error) {
	if slot != c.slot {
		return cp.Receipt{}, &cp.Reason{Kind: cp.Unauthorized}
	}
	q := cp.ReadRequest{Slot: slot, ReceiptID: id}
	rand.Read(q.Challenge[:])
	m, err := c.signedRead(frame.Settle, q)
	if err != nil {
		return cp.Receipt{}, err
	}
	return c.settle(ctx, q, m)
}
func (c *owner) Settle(ctx context.Context, slot cp.Slot, id cp.ID) (cp.Receipt, error) {
	q := cp.ReadRequest{Slot: slot, ReceiptID: id}
	rand.Read(q.Challenge[:])
	b, err := readBody(q, c.wid, "settle")
	if err != nil {
		return cp.Receipt{}, err
	}
	return c.settle(ctx, q, frame.New(frame.OwnerSettle, b))
}

func (t transport) settle(ctx context.Context, q cp.ReadRequest, m frame.Message) (cp.Receipt, error) {
	if q.ReceiptID == (cp.ID{}) {
		return cp.Receipt{}, &cp.Reason{Kind: cp.InvalidRequest}
	}
	t.recovery = true
	a, err := t.exchange(ctx, m)
	if err != nil {
		if t.queue != nil && errors.Is(err, cp.ErrNotApplied) {
			t.queue.resolve(q.ReceiptID, "")
		}
		return cp.Receipt{}, err
	}
	ack, err := cp.VerifyReceipt(t.WitnessPin, a.receipt)
	if err != nil || ack.ID != q.ReceiptID || ack.State.Slot != q.Slot || ack.State.Epoch != t.Epoch {
		return cp.Receipt{}, cp.ErrUnknownOutcome
	}
	if t.queue != nil {
		t.queue.resolve(ack.ID, ack.RequestDigest)
	}
	return a.receipt, nil
}
