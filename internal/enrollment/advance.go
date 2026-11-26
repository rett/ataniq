package enrollment

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"

	"ataniq/internal/assignment"
	"ataniq/internal/checkpoint"
)

// The canonical signed intent binds ID, session, exact predecessor and next
// head. Next.Digest commits to the entire assignment record including its LF.
type staged struct {
	Intent, Proof, Record []byte
	Session               ed25519.PublicKey
}

func (p staged) advance() (checkpoint.Advance, error) {
	intent, err := checkpoint.ParseIntent("advance", "", p.Intent)
	if err != nil {
		return checkpoint.Advance{}, err
	}
	q := intent.(checkpoint.Advance)
	key, err := checkpoint.KeyID(p.Session)
	if err != nil || key != q.Expected.SessionKeyID {
		return q, checkpoint.ErrRefused
	}
	err = checkpoint.VerifySignature(checkpoint.SessionNamespace, p.Session, p.Intent, p.Proof)
	return q, err
}

func (s *Store) inspectStage(ctx context.Context, raw, last []byte, head assignment.Checkpoint, v checkpoint.View) (*AdvanceReceipt, error) {
	equal := checkpoint.Compare(head, v.Current) == checkpoint.Equal
	var p staged
	if err := json.Unmarshal(raw, &p); err != nil {
		if equal && !json.Valid(raw) {
			return nil, nil
		}
		return nil, refuse(Interrupted, err)
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(raw, canonical) {
		return nil, refuse(Interrupted, err)
	}
	q, err := p.advance()
	if err == nil && equal && q.Expected == v.Current && !bytes.Equal(last, p.Record) {
		// Only local cleanup is eligible, and only after a successful claim.
		return nil, nil
	}
	if err != nil || q.Next != head || !bytes.Equal(last, p.Record) {
		return nil, refuse(Interrupted, err)
	}
	// Replay authenticated last. Check its actual predecessor, even when the
	// home is only one record ahead of the witness but on a different branch.
	e, err := assignment.DecodeRecord(last)
	if err != nil || e.PreviousHash != q.Expected.Head.Digest || e.Sequence == 0 || e.Sequence-1 != q.Expected.Head.Sequence ||
		q.Expected.Header != s.b.Header || q.Expected.Slot != s.b.Slot || q.Expected.Epoch != s.b.Epoch {
		return nil, refuse(Interrupted, err)
	}
	switch checkpoint.Compare(head, v.Current) {
	case checkpoint.HomeAhead:
		if v.Current != q.Expected || !bytes.Equal(p.Session, s.session.Public().(ed25519.PublicKey)) {
			return nil, refuse(Interrupted, checkpoint.ErrStale)
		}
		// The signed predecessor has an active session. Claim would return
		// SessionConflict, even for the same public key. Release changes the
		// predecessor and fence, so replay cannot transplant this intent.
		return nil, refuse(Occupied, checkpoint.ErrStale)
	case checkpoint.Equal:
	default:
		return nil, compare(head, v.Current)
	}
	v, err = s.read(ctx, q.ID)
	if err != nil {
		return nil, err
	}
	if v.Prior == nil {
		return nil, refuse(Interrupted, nil)
	}
	if err := checkpoint.MatchReceipt(s.b.WitnessPin, s.b.Epoch, "advance", p.Intent, *v.Prior); err != nil {
		return nil, refuse(Interrupted, err)
	}
	return &AdvanceReceipt{ID: q.ID, Receipt: *v.Prior}, compare(head, v.Current)
}

// AppendAndAdvance prepares and authenticates the event before writing. The
// caller may publish the result only after success. An uncertain write or CAS
// disables the handle. Close it and start again to inspect the durable state.
func AppendAndAdvance(ctx context.Context, s *Store, event assignment.Event) (*AdvanceReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.files == nil {
		return nil, refuse(Closed, nil)
	}
	frame, err := event.RecordBytes()
	if err != nil {
		return nil, ledgerError(err)
	}
	// Own the decoded record, including any evidence slices the caller owns.
	event, err = assignment.DecodeRecord(frame)
	if err != nil {
		return nil, ledgerError(err)
	}
	body, err := event.SigningBytes()
	if err != nil {
		return nil, ledgerError(err)
	}
	if err := s.verifier.Verify(s.b.Header, body, event.Signature); err != nil {
		return nil, ledgerError(err)
	}
	next, err := assignment.Apply(s.ledger, event)
	if err != nil {
		return nil, ledgerError(err)
	}
	v, err := s.read(ctx, checkpoint.ID{})
	if err != nil {
		return nil, err
	}
	if v.Current != s.state {
		// The challenged read proves this session has lost its fence.
		s.stopped = true
		return nil, refuse(Occupied, checkpoint.ErrStale)
	}
	q := checkpoint.Advance{Mutation: checkpoint.Mutation{ID: id(), Expected: s.state}, Next: next.Checkpoint()}
	intent, err := checkpoint.AdvanceBody(q)
	if err != nil {
		return nil, refuse(Interrupted, err)
	}
	proof, err := checkpoint.Sign(checkpoint.SessionNamespace, s.session, intent)
	if err != nil {
		return nil, refuse(Interrupted, err)
	}
	raw, err := json.Marshal(staged{intent, proof, frame, s.session.Public().(ed25519.PublicKey)})
	if err != nil {
		return nil, refuse(Interrupted, err)
	}
	if err := s.files.check(); err != nil {
		return nil, err
	}
	if err := s.files.saveStage(raw); err != nil {
		// Exclusive creation may already have made a partial stage visible.
		s.stopped = true
		return nil, refuse(Interrupted, err)
	}
	s.stopped = true
	if err := s.files.append(frame); err != nil {
		return nil, refuse(Interrupted, err)
	}
	if err := s.files.check(); err != nil {
		return nil, err
	}
	r, err := s.client.Advance(ctx, q)
	if err != nil {
		return nil, refuse(Interrupted, err)
	}
	if err := after(s.fault, "advance.committed"); err != nil {
		return nil, refuse(Interrupted, err)
	}
	state, err := s.confirm(ctx, "advance", q, r)
	if err != nil {
		return nil, err
	}
	if err := compare(next.Checkpoint(), state); err != nil {
		return nil, err
	}
	if err := s.files.check(); err != nil {
		return nil, err
	}
	evidence := AdvanceReceipt{ID: q.ID, Receipt: r}
	if err := s.saveReceipt(evidence); err != nil {
		return nil, err
	}
	if err := s.files.clearStage(); err != nil {
		return nil, refuse(Interrupted, err)
	}
	if err := s.files.check(); err != nil {
		return nil, err
	}
	s.ledger, s.state, s.stopped = next, state, false
	return evidence.clone(), nil
}
