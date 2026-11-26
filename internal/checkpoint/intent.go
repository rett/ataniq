package checkpoint

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"

	"ataniq/internal/assignment"
)

// Intent is the canonical request saved before sending a mutation. ParseIntent
// returns Enrollment, Claim, Advance, Mutation (release), or Retirement.
// The owner operations release and retire bind the independently pinned witness
// fingerprint. The other operations derive the signer from the home identity.
type Intent interface {
	Body(witnessKeyID string) ([]byte, error)
}

func (q Enrollment) Body(_ string) ([]byte, error) {
	id, err := KeyID(q.HomePublicKey)
	if err != nil {
		return nil, err
	}
	return enrollmentBody(q, id)
}
func (q Claim) Body(_ string) ([]byte, error) {
	return mutationBody("claim", q.Expected.HomeKeyID, q.Mutation, q.NewSessionPublicKey, nil, "")
}
func (q Advance) Body(_ string) ([]byte, error) { return AdvanceBody(q) }
func (q Mutation) Body(witnessKeyID string) ([]byte, error) {
	if !textOK(witnessKeyID) {
		return nil, &Reason{Kind: InvalidRequest}
	}
	return mutationBody("release", witnessKeyID, q, nil, nil, "")
}
func (q Retirement) Body(witnessKeyID string) ([]byte, error) {
	if !textOK(witnessKeyID) {
		return nil, &Reason{Kind: InvalidRequest}
	}
	return mutationBody("retire", witnessKeyID, q.Mutation, nil, nil, q.DispositionDigest)
}

// ParseIntent selects the expected operation before decoding untrusted bytes.
// Signatures and live grants are checked separately by the authority.
func ParseIntent(op, witnessKeyID string, body []byte) (Intent, error) {
	if op == "enroll" {
		var r enrollRecord
		if decode(body, &r) != nil {
			return nil, &Reason{Kind: InvalidRequest}
		}
		var q Enrollment
		if !digestOK(r.ID) {
			return nil, &Reason{Kind: InvalidRequest}
		}
		_, err := hex.Decode(q.ID[:], []byte(r.ID))
		if err != nil {
			return nil, &Reason{Kind: InvalidRequest}
		}
		q.Slot = r.Slot
		q.HeaderBytes, err = base64.RawURLEncoding.Strict().DecodeString(r.HeaderBytes)
		if err != nil {
			return nil, &Reason{Kind: InvalidRequest}
		}
		q.HomePublicKey, err = base64.RawURLEncoding.Strict().DecodeString(r.HomePublicKey)
		if err != nil {
			return nil, &Reason{Kind: InvalidRequest}
		}
		return exactIntent(q, witnessKeyID, body)
	}
	var r mutationRecord
	if decode(body, &r) != nil || !digestOK(r.ID) {
		return nil, &Reason{Kind: InvalidRequest}
	}
	m := Mutation{Expected: r.Expected.state()}
	if _, err := hex.Decode(m.ID[:], []byte(r.ID)); err != nil {
		return nil, &Reason{Kind: InvalidRequest}
	}
	var q Intent
	switch op {
	case "claim":
		session, err := base64.RawURLEncoding.Strict().DecodeString(r.SessionPublicKey)
		if err != nil {
			return nil, &Reason{Kind: InvalidRequest}
		}
		if _, err := KeyID(session); err != nil {
			return nil, err
		}
		q = Claim{m, session}
	case "advance":
		if r.Next == nil {
			return nil, &Reason{Kind: InvalidRequest}
		}
		q = Advance{m, assignment.Checkpoint(*r.Next)}
	case "release":
		q = m
	case "retire":
		q = Retirement{m, r.DispositionDigest}
	default:
		return nil, &Reason{Kind: InvalidRequest}
	}
	return exactIntent(q, witnessKeyID, body)
}

func exactIntent(q Intent, witnessKeyID string, body []byte) (Intent, error) {
	encoded, err := q.Body(witnessKeyID)
	if err != nil || !bytes.Equal(encoded, body) {
		return nil, &Reason{Kind: InvalidRequest}
	}
	return q, nil
}

// enrollmentState validates the assignment's exact immutable header frame.
func enrollmentState(q Enrollment, homeID string, epoch uint64) (State, error) {
	ledger, err := assignment.NewLedgerFromHeader(q.HeaderBytes)
	var frame struct{ Header assignment.Header }
	if err != nil || json.Unmarshal(q.HeaderBytes, &frame) != nil {
		return State{}, &Reason{Kind: InvalidRequest}
	}
	h := frame.Header
	if h.KeyID != homeID || h.Authority != q.Slot.Authority || h.Store != q.Slot.Store {
		return State{}, &Reason{Kind: InvalidRequest}
	}
	return State{Slot: q.Slot, Header: h, Head: ledger.Checkpoint(), Epoch: epoch, Revision: 1, HomeKeyID: homeID, Status: Active}, nil
}

// MatchReceipt verifies the pinned witness, the expected epoch, the exact saved
// request and its resulting state. Any mismatch leaves the outcome unresolved.
func MatchReceipt(pin ed25519.PublicKey, epoch uint64, op string, body []byte, r Receipt) error {
	wid, err := KeyID(pin)
	if err != nil {
		return ErrUnknownOutcome
	}
	intent, err := ParseIntent(op, wid, body)
	if err != nil {
		return ErrUnknownOutcome
	}
	a, err := verifyReceipt(pin, wid, r)
	if err != nil || a.Operation != op || a.RequestDigest != RequestDigest(body) || a.State.Epoch != epoch {
		return ErrUnknownOutcome
	}
	var s State
	var m Mutation
	switch q := intent.(type) {
	case Enrollment:
		homeID, err := KeyID(q.HomePublicKey)
		if err != nil {
			return ErrUnknownOutcome
		}
		s, err = enrollmentState(q, homeID, epoch)
		if err != nil {
			return ErrUnknownOutcome
		}
		m.ID = q.ID
	case Claim:
		m = q.Mutation
		s = m.Expected
		s.Fence++
		s.SessionKeyID, err = KeyID(q.NewSessionPublicKey)
		if err != nil || s.Fence == 0 || m.Expected.SessionKeyID != "" || s.SessionKeyID == m.Expected.HomeKeyID || s.SessionKeyID == wid {
			return ErrUnknownOutcome
		}
	case Advance:
		m = q.Mutation
		s = m.Expected
		next := s.Head
		next.Sequence++
		next.Digest = q.Next.Digest
		if next != q.Next || next.Sequence == 0 || s.SessionKeyID == "" {
			return ErrUnknownOutcome
		}
		s.Head = q.Next
	case Mutation:
		m = q
		s = m.Expected
		if s.SessionKeyID == "" || s.Fence == math.MaxUint64 {
			return ErrUnknownOutcome
		}
		s.Fence++
		s.SessionKeyID = ""
	case Retirement:
		m = q.Mutation
		s = m.Expected
		if s.Fence == math.MaxUint64 {
			return ErrUnknownOutcome
		}
		s.Fence++
		s.SessionKeyID = ""
		s.Status = Retired
	}
	if op != "enroll" {
		if m.Expected.Status != Active || s.Revision == math.MaxUint64 {
			return ErrUnknownOutcome
		}
		s.Revision++
	}
	s.LastReceiptDigest = ReceiptDigest(r)
	if a.ID != m.ID || a.PredecessorDigest != m.Expected.LastReceiptDigest || a.State != s {
		return ErrUnknownOutcome
	}
	return nil
}
