// Package assignment reduces verified home assignment commands to immutable
// ledger values. The caller owns authentication, evidence verification, durable
// append and the live admission barrier at each actual effect start. In
// particular it verifies owner authorization for the exact command and head.
//
// A project supports up to eight enabled routes and eight outstanding effects.
// Capture, inventory refresh and receipt correction report ErrCapacity with
// the observed count and operating limit when either budget is exceeded.
//
// Adapter obligation: authenticate each issuer and enroll exactly one issuer
// identity per route per transfer. The ledger records that identity as a
// string. A contradictory receipt from a different issuer returns ErrEvidence.
// The adapter must report the refused contradiction to the owner out of band.
//
// A same-issuer correction of an accepted fence or confirmation permanently
// quarantines this key in this store, including during a later transfer. Both
// bindings lose admission. The owner may acknowledge the quarantine once,
// recording a disposition against its event. Recovery requires retiring the
// doubtful identity and freshly enrolling in a new store.
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
	ErrCapacity = errors.New("assignment: capacity exceeded")
)

type Key struct {
	Home    string `json:"home"`
	Project string `json:"project"`
	Role    string `json:"role"`
}

type Binding struct {
	Authority    string `json:"authority"`
	Conversation string `json:"conversation"`
	ID           string `json:"id"`
	Incarnation  string `json:"incarnation"`
	Kind         string `json:"kind"`
	LaunchIntent string `json:"launch_intent"`
	RoleEpoch    uint64 `json:"role_epoch,string"`
	Store        string `json:"store"`
}

func (b Binding) valid() bool {
	if b.Kind == "managed" && (b.LaunchIntent == "") != (b.RoleEpoch == 0) {
		return false
	}
	return (b.Kind == "legacy" || b.Kind == "managed") && b.ID != "" &&
		b.Authority != "" && b.Store != "" && b.Incarnation != "" && b.Conversation != ""
}

type Phase string

const (
	PhaseQuarantined     Phase = "quarantined"
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
	Seed           Kind = "Seed"
	Freeze         Kind = "Freeze"
	Inventory      Kind = "Inventory"
	BeliefAccepted Kind = "BeliefAccepted"
	Reserve        Kind = "Reserve"
	FenceReceipt   Kind = "FenceReceipt"
	Fenced         Kind = "Fenced"
	Commit         Kind = "Commit"
	ConfirmReceipt Kind = "ConfirmReceipt"
	Open           Kind = "Open"
	HoldStarted    Kind = "HoldStarted"
	HoldReviewed   Kind = "HoldReviewed"
	AbortPrepared  Kind = "AbortPrepared"
	Aborted        Kind = "Aborted"
	AbortedClosed  Kind = "AbortedClosed"
	RollbackFreeze Kind = "RollbackFreeze"
)

// QuarantineAcknowledged carries one verified owner-disposition proof. The
// actor issued it for the command's authorization reference at QuarantineEventID.
// Outcome records the owner's disposition. It never changes admission.
const QuarantineAcknowledged Kind = "QuarantineAcknowledged"

type Head struct {
	BindingID  string `json:"binding_id"`
	Generation uint64 `json:"generation,string"`
	Key        Key    `json:"key"`
	Phase      Phase  `json:"phase"`
	Revision   uint64 `json:"revision,string"`
	TransferID string `json:"transfer_id"`
}

type Row struct {
	QuarantineEventID         string
	QuarantineAcknowledgement Evidence
	Head                      Head
	CommitRevision            uint64
	PublicDigest              string
	Holder                    Binding
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
	Digest       string `json:"digest"`
	Issuer       string `json:"issuer"`
	Kind         string `json:"kind"`
	Outcome      string `json:"outcome"`
	Revision     string `json:"revision"`
	Route        string `json:"route"`
	Scope        Head   `json:"scope"`
	ScopeEventID string `json:"scope_event_id"`
	Subject      string `json:"subject"`
	TransferID   string `json:"transfer_id"`
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
	Fences, Confirmations                []Evidence
	Disposition                          string
	Hold                                 Hold
}

func (t Transfer) clone() Transfer {
	t.Routes = slices.Clone(t.Routes)
	t.Outstanding = slices.Clone(t.Outstanding)
	t.Fences = slices.Clone(t.Fences)
	t.Confirmations = slices.Clone(t.Confirmations)
	return t
}

type Command struct {
	Actor              string     `json:"actor"`
	Authorization      string     `json:"authorization"`
	Candidate          Binding    `json:"candidate"`
	EarliestRetirement uint64     `json:"earliest_retirement,string"`
	Evidence           []Evidence `json:"evidence"`
	Expected           Head       `json:"expected"`
	HoldStart          uint64     `json:"hold_start,string"`
	Kind               Kind       `json:"kind"`
	RequestID          string     `json:"request_id"`
	TransferID         string     `json:"transfer_id"`
}

type Result struct {
	Disposition string `json:"disposition"`
	EventID     string `json:"event_id"`
	Head        Head   `json:"head"`
	RequestID   string `json:"request_id"`
}

type Event struct {
	Schema, Sequence            uint64
	PreviousHash, RequestDigest string
	Command                     Command
	Result                      Result
	Signature                   string
	Public                      PublicEnvelope
}
