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
	// ErrBusy identifies a competing call refused before delivery.
	ErrBusy        = errors.New("checkpoint: endpoint busy")
	ErrRefused     = errors.New("checkpoint: refused")
	ErrStale       = errors.New("checkpoint: stale or conflicting request")
	ErrRetired     = errors.New("checkpoint: retired")
	ErrCapacity    = errors.New("checkpoint: history capacity exhausted")
	ErrUnavailable = errors.New("checkpoint: unavailable")
	// ErrNotApplied proves that this slot and ID are permanently fenced.
	ErrNotApplied     = errors.New("checkpoint: mutation fenced without commit")
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
	JournalSequence           uint64 // Witness-wide position of the transition that last changed this slot.
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

// Settler reconciles one mutation against the authoritative history.
type Settler interface {
	// Settle atomically returns the original receipt (including historical
	// receipts) or records a permanent tombstone and returns ErrNotApplied.
	// The expected epoch must match under the commit lock before either action.
	// Durable implementations must persist the fence before answering and
	// consult it in the same transaction as every mutation commit.
	Settle(context.Context, uint64, Slot, ID) (Receipt, error)
}

// Client is scoped to a single authorized slot and signs with its configured
// keys. Exact retry returns the original receipt without repeating the change.
// Superseded receipts are available by ReceiptID through challenged Read.
// Mutation retries still require current authorization and valid signatures.
type Client interface {
	Read(context.Context, ReadRequest) (View, error)
	Settler
	Claim(context.Context, Claim) (Receipt, error)
	Advance(context.Context, Advance) (Receipt, error)
}

// Administration is the owner capability. Its challenged Read works without
// a home grant or session and supplies the exact Expected for owner mutations.
type Administration interface {
	Read(context.Context, ReadRequest) (View, error)
	Settler
	Enroll(context.Context, Enrollment) (Receipt, error)
	Release(context.Context, Mutation) (Receipt, error)
	Retire(context.Context, Retirement) (Receipt, error)
}
