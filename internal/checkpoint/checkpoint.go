// Package checkpoint defines the independent witness for an assignment ledger.
// Callers supply mutation IDs, fresh read challenges and process-lifetime session
// keys. Only the owner can create a slot, release a writer, or retire a store.
package checkpoint

import (
	"context"
	"errors"

	"ataniq/internal/assignment"
)

// A conflicting mutation ID or failed compare-and-swap returns ErrStale.
// ErrUnavailable means this attempt made no change. Non-commit is guaranteed
// only for a request absent from witness history. An authorized exact retry of
// the latest mutation returns its receipt even during inhibition. ErrUnknownOutcome requires
// reconciling the saved intent by its ID before publishing a result.
var (
	ErrRefused        = errors.New("checkpoint: refused")
	ErrStale          = errors.New("checkpoint: stale or conflicting request")
	ErrRetired        = errors.New("checkpoint: retired")
	ErrUnavailable    = errors.New("checkpoint: unavailable")
	ErrUnknownOutcome = errors.New("checkpoint: unknown mutation outcome")
)

const (
	Active  = "active"
	Retired = "retired"
)

type Slot struct {
	Witness   string `json:"witness"`
	Authority string `json:"authority"`
	Store     string `json:"store"`
}
type ID [32]byte

type State struct {
	Slot                      Slot
	Header                    assignment.Header
	Head                      assignment.Checkpoint
	Epoch, Revision, Fence    uint64
	HomeKeyID, SessionKeyID   string
	Status, LastReceiptDigest string
}

type ReadRequest struct {
	Slot      Slot
	Challenge ID
	ReceiptID ID // Zero requests current state only.
}
type Mutation struct {
	ID       ID
	Expected State
}
type Claim struct {
	Mutation
	NewSessionPublicKey []byte
}
type Advance struct {
	Mutation
	Next assignment.Checkpoint
}
type Enrollment struct {
	ID                         ID
	Slot                       Slot
	HeaderBytes, HomePublicKey []byte
	ProofOfPossession          []byte
}
type Retirement struct {
	Mutation
	DispositionDigest string
}
type Receipt struct {
	Body, Signature []byte // Canonical JSON and unpadded base64url SSHSIG.
}
type View struct {
	Current State
	Fresh   Receipt
	Prior   *Receipt
}

// Client is scoped to a single authorized slot and signs with its configured
// keys. Exact retry returns the original receipt without repeating the change.
// Superseded receipts are available by ReceiptID through challenged Read.
// Mutation retries still require current authorization and valid signatures.
type Client interface {
	Read(context.Context, ReadRequest) (View, error)
	Claim(context.Context, Claim) (Receipt, error)
	Advance(context.Context, Advance) (Receipt, error)
}
type Administration interface {
	Enroll(context.Context, Enrollment) (Receipt, error)
	Release(context.Context, Mutation) (Receipt, error)
	Retire(context.Context, Retirement) (Receipt, error)
}
