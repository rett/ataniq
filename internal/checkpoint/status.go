package checkpoint

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
)

// RecoveryAdministration adds directory-wide operations to the local owner capability.
type RecoveryAdministration interface {
	Administration
	Status(context.Context, ID) (Receipt, error)
	AcknowledgeStorageError(context.Context) error
}

type Inhibition string

const (
	Ready                  Inhibition = "ready"
	ServiceInhibited       Inhibition = "service inhibited"
	StorageFailure         Inhibition = "storage error"
	UncleanEnd             Inhibition = "unclean process end"
	InterruptedPublication Inhibition = "interrupted publication"
)

// DirectoryStatus describes the whole verified journal, including fences made
// before enrollment. Sequence counts genesis, receipts and settlement fences.
type DirectoryStatus struct {
	Epoch      uint64     `json:"epoch,string"`
	Sequence   uint64     `json:"journal_sequence,string"`
	Digest     string     `json:"previous_digest"`
	Inhibition Inhibition `json:"inhibition"`
	Slots      uint64     `json:"slots,string"`
	Receipts   uint64     `json:"receipts,string"`
	Fences     uint64     `json:"fences,string"`
}

type statusRecord struct {
	Operation string `json:"operation"`
	Challenge string `json:"challenge"`
	DirectoryStatus
}

func (s DirectoryStatus) Body(challenge ID) ([]byte, error) {
	switch s.Inhibition {
	case Ready, ServiceInhibited, StorageFailure, UncleanEnd, InterruptedPublication:
	default:
		return nil, &Reason{Kind: InvalidRequest}
	}
	if challenge == (ID{}) || s.Epoch == 0 || !digestOK(s.Digest) || s.Sequence <= s.Receipts ||
		s.Sequence-1-s.Receipts != s.Fences || s.Slots > s.Receipts {
		return nil, &Reason{Kind: InvalidRequest}
	}
	return encode(statusRecord{"owner-status", hex.EncodeToString(challenge[:]), s})
}

func VerifyDirectoryStatus(pin ed25519.PublicKey, epoch uint64, challenge ID, r Receipt) (DirectoryStatus, error) {
	var body statusRecord
	if VerifySignature(WitnessNamespace, pin, r.Body, r.Signature) != nil || decode(r.Body, &body) != nil ||
		body.Operation != "owner-status" || body.Challenge != hex.EncodeToString(challenge[:]) {
		return DirectoryStatus{}, ErrRefused
	}
	if _, err := body.Body(challenge); err != nil {
		return DirectoryStatus{}, err
	}
	if body.Epoch != epoch {
		return DirectoryStatus{}, ErrStale
	}
	return body.DirectoryStatus, nil
}
