package checkpoint

import "ataniq/internal/assignment"

// ReasonKind distinguishes refusal and conflict causes for the startup gate.
type ReasonKind string

const (
	InvalidRequest      ReasonKind = "invalid request"
	Unauthorized        ReasonKind = "unauthorized"
	KeyRoleConflict     ReasonKind = "overlapping key roles"
	NotEnrolled         ReasonKind = "slot not enrolled"
	MutationConflict    ReasonKind = "mutation ID reused"
	Superseded          ReasonKind = "receipt superseded"
	PredecessorMismatch ReasonKind = "predecessor mismatch"
	SessionConflict     ReasonKind = "session conflict"
	EnrollmentConflict  ReasonKind = "enrollment conflict"
	CounterExhausted    ReasonKind = "counter exhausted"
	InvalidSuccessor    ReasonKind = "invalid successor"
)

// Reason supports errors.As and unwraps to ErrRefused or ErrStale.
type Reason struct{ Kind ReasonKind }

func (r *Reason) Unwrap() error {
	switch r.Kind {
	case MutationConflict, Superseded, PredecessorMismatch, SessionConflict, EnrollmentConflict, CounterExhausted, InvalidSuccessor:
		return ErrStale
	default:
		return ErrRefused
	}
}
func (r *Reason) Error() string { return r.Unwrap().Error() + ": " + string(r.Kind) }

// Comparison describes two heads at startup. Retired uses the same spelling
// as the slot status. A sequence difference alone cannot establish ancestry.
type Comparison string

const (
	Equal      Comparison = "equal"
	HomeBehind Comparison = "home behind"
	HomeAhead  Comparison = "home ahead"
	Fork       Comparison = "fork"
	Unknown    Comparison = "unknown"
)

// Compare takes an independently replayed home head and an authenticated
// witness state. Missing or malformed evidence is Unknown. A valid tombstone
// takes precedence over the local head. Conflicting ledger identities or
// digests at a known sequence (including genesis) are forks.
func Compare(home assignment.Checkpoint, witness State) Comparison {
	if !validState(witness) {
		return Unknown
	}
	if witness.Status == Retired {
		return Retired
	}
	if !textOK(home.Authority) || !textOK(home.Store) || !textOK(home.Incarnation) || !textOK(home.LedgerID) || !digestOK(home.Digest) {
		return Unknown
	}
	w := witness.Head
	if home.Authority != w.Authority || home.Store != w.Store || home.Incarnation != w.Incarnation || home.LedgerID != w.LedgerID {
		return Fork
	}
	if home.Sequence == 0 {
		ledger, _ := assignment.NewLedger(witness.Header) // validState checked the header.
		if home != ledger.Checkpoint() {
			return Fork
		}
	}
	switch {
	case home == w:
		return Equal
	case home.Sequence < w.Sequence:
		return HomeBehind
	case home.Sequence > w.Sequence:
		return HomeAhead
	default:
		return Fork
	}
}
