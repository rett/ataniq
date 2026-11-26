// Package assignment reduces verified home assignment commands to immutable
// ledger values. The caller owns authentication, evidence verification, durable
// append and the live admission barrier at each actual effect start.
package assignment

import (
	"errors"
	"slices"
)

var (
	ErrInvalid  = errors.New("assignment: invalid command")
	ErrStale    = errors.New("assignment: predecessor mismatch")
	ErrConflict = errors.New("assignment: conflicting ID reuse")
	ErrPhase    = errors.New("assignment: transition refused")
	ErrEvidence = errors.New("assignment: missing or incompatible evidence")
	ErrOverflow = errors.New("assignment: counter exhausted")
)

type Key struct{ Home, Project, Role string }

type Binding struct {
	Kind, ID, Authority, Store, Incarnation string
	Conversation, LaunchIntent              string
	RoleEpoch                               uint64
}

func (b Binding) valid() bool {
	return (b.Kind == "legacy" || b.Kind == "managed") && b.ID != "" &&
		b.Authority != "" && b.Store != "" && b.Incarnation != "" && b.Conversation != ""
}

type Phase string

const (
	PhaseReceiptDisputed Phase = "receipt-disputed"
	PhaseFrozen          Phase = "frozen"
	PhaseInventoried     Phase = "inventoried"
	PhaseBeliefVerified  Phase = "belief-verified"
	PhaseReserved        Phase = "reserved"
	PhasePartiallyFenced Phase = "partially-fenced"
	PhaseFenced          Phase = "fenced"
	PhaseCommittedClosed Phase = "committed-closed"
	PhaseConfirmed       Phase = "confirmed"
	PhaseAborting        Phase = "aborting"
	PhaseAborted         Phase = "aborted"
	PhaseAbortedClosed   Phase = "aborted-closed"
)

func (p Phase) open() bool { return p == PhaseConfirmed || p == PhaseAborted }

func (p Phase) precommit() bool {
	switch p {
	case PhaseFrozen, PhaseInventoried, PhaseBeliefVerified, PhaseReserved, PhasePartiallyFenced, PhaseFenced:
		return true
	default:
		return false
	}
}

type Kind string

const (
	Seed              Kind = "Seed"
	Freeze            Kind = "Freeze"
	Inventory         Kind = "Inventory"
	BeliefAccepted    Kind = "BeliefAccepted"
	Reserve           Kind = "Reserve"
	ReceiptReconciled Kind = "ReceiptReconciled"
	FenceReceipt      Kind = "FenceReceipt"
	Fenced            Kind = "Fenced"
	Commit            Kind = "Commit"
	ConfirmReceipt    Kind = "ConfirmReceipt"
	Open              Kind = "Open"
	HoldStarted       Kind = "HoldStarted"
	HoldReviewed      Kind = "HoldReviewed"
	AbortPrepared     Kind = "AbortPrepared"
	Aborted           Kind = "Aborted"
	AbortedClosed     Kind = "AbortedClosed"
	RollbackFreeze    Kind = "RollbackFreeze"
)

type Head struct {
	Key                   Key
	Generation, Revision  uint64
	BindingID, TransferID string
	Phase                 Phase
}

type Row struct {
	Head   Head
	Holder Binding
	// LastTransferID also identifies the record after its active ID clears.
	LastTransferID string
	// Previous is the binding displaced by the most recent commit.
	Previous Binding
}

// Permits tests the recorded admission at this exact head. The caller must
// establish current enrollment, witness and route availability under its gate.
func (r Row) Permits(h Head) bool { return r.Head == h && h.Phase.open() }

// Evidence identifies a verified immutable object. Scope is the expected row
// for decisions, the reservation for fences, or the commit for confirmations.
// TransferID also binds evidence for a new transfer against an older head.
// The issuer attests the outcome for the subject in that scope.
type Evidence struct {
	Route                                            string // Owning route for an effect reference.
	Kind, Issuer, Subject, Revision, Digest, Outcome string
	Scope                                            Head
	TransferID, ScopeEventID                         string
}

type Hold struct {
	// Absolute seconds supplied as verified evidence by the adapter.
	Start, EarliestRetirement uint64
	Preservation, Review      Evidence
}

type Effect struct{ ID, Route string }

type Transfer struct {
	ID, BeginDigest, Authorization       string
	Original                             Head
	Predecessor, Candidate               Binding
	Phase                                Phase
	PhaseRevision                        uint64
	Routes                               []string
	Outstanding                          []Effect
	Inventory, Note, Belief, Decision    Evidence
	PreviewMismatch                      bool
	Reservation, Committed               Head
	ReservationEventID, CommittedEventID string
	Fences, Confirmations, Disputes      []Evidence
	BeforeDispute                        Phase
	Disposition                          string
	Hold                                 Hold
}

func (t Transfer) clone() Transfer {
	t.Disputes = slices.Clone(t.Disputes)
	t.Routes = slices.Clone(t.Routes)
	t.Outstanding = slices.Clone(t.Outstanding)
	t.Fences = slices.Clone(t.Fences)
	t.Confirmations = slices.Clone(t.Confirmations)
	return t
}

type Command struct {
	Expected                                    Head
	Kind                                        Kind
	TransferID, RequestID, Actor, Authorization string
	Candidate                                   Binding
	Evidence                                    []Evidence
	HoldStart, EarliestRetirement               uint64
}

type Result struct {
	Head                            Head
	RequestID, Disposition, EventID string
}

type Event struct {
	Schema, Sequence            uint64
	PreviousHash, RequestDigest string
	Command                     Command
	Result                      Result
	Signature                   string
}
