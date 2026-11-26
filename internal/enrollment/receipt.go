package enrollment

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"ataniq/internal/assignment"
	"ataniq/internal/checkpoint"
)

// AdvanceReceipt carries the signed acknowledgement and its historical lookup ID.
type AdvanceReceipt struct {
	ID      checkpoint.ID
	Receipt checkpoint.Receipt
}

func (r *AdvanceReceipt) clone() *AdvanceReceipt {
	if r == nil {
		return nil
	}
	return &AdvanceReceipt{r.ID, checkpoint.Receipt{Body: slices.Clone(r.Receipt.Body), Signature: slices.Clone(r.Receipt.Signature)}}
}

// LastReceipt returns the latest retained advance, or nil before the first one.
// Stores advanced by versions predating receipt retention can also have no copy.
func (s *Store) LastReceipt() (*AdvanceReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil || s.stopped {
		return nil, refuse(Closed, nil)
	}
	return s.lastReceipt.clone(), nil
}

func (s *Store) saveReceipt(r AdvanceReceipt) error {
	raw, err := json.Marshal(r.Receipt)
	if err != nil {
		return refuse(Interrupted, err)
	}
	if len(raw) > stageLimit {
		return refuse(Interrupted, errors.New("oversize receipt"))
	}
	if err := s.files.saveReceipt(raw); err != nil {
		return refuse(Interrupted, err)
	}
	s.lastReceipt = r.clone()
	return nil
}

func (s *Store) loadReceipt(ctx context.Context, head assignment.Checkpoint, last []byte, recovered *AdvanceReceipt) error {
	raw, err := s.files.receipt()
	if err != nil || raw == nil {
		return err
	}
	var r checkpoint.Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return refuse(Interrupted, err)
	}
	ack, err := checkpoint.VerifyReceipt(s.b.WitnessPin, r)
	if err != nil || ack.Operation != "advance" || ack.State.Slot != s.b.Slot || ack.State.Epoch != s.b.Epoch || ack.State.Header != s.b.Header {
		return refuse(Interrupted, errors.New("invalid stored advance receipt"))
	}
	if ack.State.Head != head {
		// A committed stage can coexist with the previous receipt until rename.
		if recovered == nil {
			return refuse(Interrupted, errors.New("stale advance receipt"))
		}
		e, err := assignment.DecodeRecord(last)
		if err != nil || ack.State.Head.Sequence != head.Sequence-1 || ack.State.Head.Digest != e.PreviousHash {
			return refuse(Interrupted, errors.New("receipt does not match staged predecessor"))
		}
	} else if recovered != nil && checkpoint.ReceiptDigest(recovered.Receipt) != checkpoint.ReceiptDigest(r) {
		return refuse(Interrupted, errors.New("stored and staged receipts disagree"))
	}
	v, err := s.read(ctx, ack.ID)
	if err != nil {
		return err
	}
	if v.Prior == nil || checkpoint.ReceiptDigest(*v.Prior) != checkpoint.ReceiptDigest(r) {
		return refuse(Interrupted, errors.New("witness receipt missing or changed"))
	}
	s.lastReceipt = &AdvanceReceipt{ack.ID, r}
	return nil
}
