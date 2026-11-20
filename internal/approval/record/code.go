package record

import "errors"

// Code is the short, fixed word a refusal is known by. It is what a lead is
// answered with and what a surface shows; the words are part of the contract
// with the planner's project. Every refusal code of the approval packages is
// declared in this file, and no other package declares one.
type Code string

// The refusal codes of the planner's store and of the host (the design's
// list of states and codes).
const (
	// CodeApprovalRefused answers free text given as an approval from a
	// managed seat.
	CodeApprovalRefused Code = "approval-refused"
	// CodeRequestInvalid answers a request that breaks the record rules or
	// its form.
	CodeRequestInvalid Code = "request-invalid"
	// CodeRequestRate answers a request past the per-minute cap of a seat.
	CodeRequestRate Code = "request-rate"
	// CodeRequestPendingCap answers a request while too many of the seat's
	// requests are not yet final.
	CodeRequestPendingCap Code = "request-pending-cap"
	// CodeRequestDuplicate answers a second live request for one verb and
	// target.
	CodeRequestDuplicate Code = "request-duplicate"
	// CodeRequestThrottled answers a request for a verb and target asked
	// too often within an hour.
	CodeRequestThrottled Code = "request-throttled"
	// CodePreviousEpoch answers a request that names an epoch that ended.
	CodePreviousEpoch Code = "previous-epoch"
	// CodeNotAssigned answers a request from a seat with no assignment.
	CodeNotAssigned Code = "not-assigned"
	// CodeVerbNotRequestable answers a verb that cannot be asked for
	// through the channel.
	CodeVerbNotRequestable Code = "verb-not-requestable"
	// CodeFeatureName answers a feature name outside the grammar of
	// [ValidFeatureName], or a feature finish that does not name exactly
	// one feature.
	CodeFeatureName Code = "feature-name"
	// CodeFeatureMoved answers a request whose branch is not where the
	// lead said it was.
	CodeFeatureMoved Code = "feature-moved"
	// CodeMergeConflict answers a merge the host could not build.
	CodeMergeConflict Code = "merge-conflict"
	// CodeVerificationFailed answers a merge whose verification run did
	// not pass cleanly.
	CodeVerificationFailed Code = "verification-failed"
	// CodePruneUnmerged answers a prune of a branch that is not merged.
	CodePruneUnmerged Code = "prune-unmerged"
	// CodePruneProtected answers a prune of a branch that is never
	// deleted.
	CodePruneProtected Code = "prune-protected"
	// CodeDecisionInvalid answers a decision that breaks the record rules
	// or its form, or whose sign-offs are not the flagged list.
	CodeDecisionInvalid Code = "decision-invalid"
	// CodeBindingMismatch answers a digest that does not match what it is
	// said to cover.
	CodeBindingMismatch Code = "binding-mismatch"
	// CodeEvidenceChanged answers a use whose bound evidence changed.
	CodeEvidenceChanged Code = "evidence-changed"
	// CodeApprovalExpired answers a use after the approval's time ran out.
	CodeApprovalExpired Code = "approval-expired"
	// CodeApprovalVoid answers a use of an approval that was voided.
	CodeApprovalVoid Code = "approval-void"
	// CodeApprovalConsumed answers a second use of an approval.
	CodeApprovalConsumed Code = "approval-consumed"
)

// The codes the design keeps for standing grants. Nothing in this build
// answers with them yet.
const (
	CodeDecisionSignature Code = "decision-signature"
	CodeGrantRevoked      Code = "grant-revoked"
	CodeGrantNoMatch      Code = "grant-no-match"
	CodeVerbNotGrantable  Code = "verb-not-grantable"
)

// The codes of this host's own decoding.
const (
	// CodeRecordTooLarge answers a record, or the note inside one, that is
	// larger than its cap.
	CodeRecordTooLarge Code = "record-too-large"
	// CodeRecordInvalid answers a pending record that breaks the record
	// rules or its form. A pending record is written by the host, so this
	// code never answers a lead.
	CodeRecordInvalid Code = "record-invalid"
)

// The codes of the owner connection.
const (
	// CodeReviewChanged answers a page read whose review is no longer the
	// one the card was made from.
	CodeReviewChanged Code = "review-changed"
	// CodeOwnerAway answers a decision made while the owner is not
	// present.
	CodeOwnerAway Code = "owner-away"
	// CodeNotDecider answers a decision from a console that is not the
	// decider.
	CodeNotDecider Code = "not-decider"
	// CodeLimitRefused answers a change of the limit that the limit's own
	// rules do not allow.
	CodeLimitRefused Code = "limit-refused"
)

// Error is a refusal: a code, and a detail for the owner's eyes.
type Error struct {
	// Code is the refusal code.
	Code Code
	// Detail says what was wrong. It is written by the host and never
	// holds text from a lead: a field is named by its place in the form,
	// never by what the record said.
	Detail string
}

// Error returns the code, followed by the detail when there is one.
func (e *Error) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Detail
}

// CodeOf returns the code of err when err is, or wraps, an [*Error].
func CodeOf(err error) (Code, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return "", false
}

// State is the state of a pending record in the planner's store. The list
// is closed, and no state returns to pending or approved. There is no state
// for a decision held back by the limit: such a record stays pending.
type State string

// The states of a pending record.
const (
	StatePending         State = "pending"
	StateApproved        State = "approved"
	StateApprovedByRule  State = "approved-by-rule"
	StateApprovedByGrant State = "approved-by-grant"
	StateDenied          State = "denied"
	StateWithdrawn       State = "withdrawn"
	StateExpired         State = "expired"
	StateVoid            State = "void"
	StateConsumed        State = "consumed"
	StateApplied         State = "applied"
	StateNotApplied      State = "not-applied"
	StateUnknown         State = "unknown"
)

// Valid reports whether s is one of the states of the closed list.
func (s State) Valid() bool {
	switch s {
	case StatePending, StateApproved, StateApprovedByRule, StateApprovedByGrant,
		StateDenied, StateWithdrawn, StateExpired, StateVoid, StateConsumed,
		StateApplied, StateNotApplied, StateUnknown:
		return true
	}
	return false
}
