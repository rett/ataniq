package assignment

import "slices"

// Reason names why Admit refused an effect start. It is the error Admit
// returns, so a caller can match it with errors.Is or compare it directly.
type Reason string

const (
	// ReasonHeadChanged: the row's head differs from the head the request
	// was submitted against, in any field.
	ReasonHeadChanged Reason = "assignment: head changed since submission"
	// ReasonPhase: the heads match but the phase does not admit effects.
	ReasonPhase Reason = "assignment: phase does not admit effects"
	// ReasonHolder: the submitting binding is not the row's holder.
	ReasonHolder Reason = "assignment: submitter is not the holder"
	// ReasonDigest: the submitted public digest is not the row's.
	ReasonDigest Reason = "assignment: public digest mismatch"
	// ReasonActivationOff: the live activation is not on.
	ReasonActivationOff Reason = "assignment: activation is off"
	// ReasonSessionLost: no live session or witness fence is held.
	ReasonSessionLost Reason = "assignment: no live session or witness fence"
	// ReasonSessionChanged: the live session or witness fence is not the one
	// the request was bound to, even at the same head.
	ReasonSessionChanged Reason = "assignment: session or witness fence replaced"
	// ReasonRoleEpoch: the live role epoch differs from the holder's.
	ReasonRoleEpoch Reason = "assignment: role epoch changed"
	// ReasonRouteMissing: a required route has no live fence.
	ReasonRouteMissing Reason = "assignment: required route not live"
)

func (r Reason) Error() string { return string(r) }

// LiveFacts are the facts the caller observes at the moment of the start,
// under its own admission gate. Admit never reads them from the ledger.
type LiveFacts struct {
	// Activation is true while the enrollment is activated.
	Activation bool
	// Session identifies the live session and witness fence now held, empty
	// when none is held. BoundSession is the one the request was bound to.
	// A release that replaces the session leaves the head unchanged, so the
	// two must match exactly. A caller takes Session from the live read and
	// BoundSession from the request, never both from the live read: the
	// comparison is vacuous when one source sets both.
	Session, BoundSession string
	// RoleEpoch is the live role epoch the holder must still carry.
	RoleEpoch uint64
	// RequiredRoutes are the routes the effect needs, taken from the request.
	// LiveRoutes are the routes with a live fence now. Every required route
	// must be live; an empty list requires none, so a caller that needs a
	// route must refuse an empty list itself.
	RequiredRoutes, LiveRoutes []string
}

// Admit is a pure predicate: nil admits one effect start, anything else is a
// Reason. It checks the row's recorded admission against the head the request
// was submitted at (expected), never against the row's own head, then the
// holder, the public digest and the live facts, in that order. The first
// failing check decides the reason. A nil result is no permit: the caller
// must repeat the check under its gate at each actual start.
func Admit(row Row, expected Head, holder Binding, publicDigest string, live LiveFacts) error {
	if row.Head != expected {
		return ReasonHeadChanged
	}
	if !row.Permits(expected) {
		return ReasonPhase
	}
	if holder != row.Holder || holder.ID != expected.BindingID {
		return ReasonHolder
	}
	if publicDigest == "" || publicDigest != row.PublicDigest {
		return ReasonDigest
	}
	if !live.Activation {
		return ReasonActivationOff
	}
	if live.Session == "" {
		return ReasonSessionLost
	}
	if live.Session != live.BoundSession {
		return ReasonSessionChanged
	}
	if live.RoleEpoch != holder.RoleEpoch {
		return ReasonRoleEpoch
	}
	for _, route := range live.RequiredRoutes {
		if !slices.Contains(live.LiveRoutes, route) {
			return ReasonRouteMissing
		}
	}
	return nil
}
