package door

import (
	"context"

	"ataniq/internal/approval/record"
)

// Door is the set of calls the host makes on the planner's store of
// approvals. The package comment says what each verb does.
//
// Every verb returns a [*record.Error] when the door refuses, and another
// error when the call itself failed. A Door is safe for use by several
// goroutines.
type Door interface {
	// Admit hands the door a lead's request. The refusals are
	// request-invalid, record-too-large, verb-not-requestable,
	// feature-name, not-assigned, previous-epoch, request-duplicate,
	// request-throttled, request-rate and request-pending-cap.
	Admit(ctx context.Context, request Record) (Admission, error)

	// Plan hands the door an admitted request with what the host built for
	// it, and returns the pending record the door wrote. The refusals
	// include request-invalid, not-assigned, previous-epoch,
	// feature-moved and binding-mismatch (the merge or the target is not
	// what the plan was made for).
	Plan(ctx context.Context, in PlanInput) (Record, error)

	// Decide hands the door the owner's decision. It is idempotent on the
	// decision's id and binding digest. The refusals are decision-invalid,
	// record-too-large, binding-mismatch, previous-epoch,
	// approval-expired, approval-void and approval-consumed.
	Decide(ctx context.Context, decision Record) (Decided, error)

	// Use consumes the approval of one pending record and returns its
	// effects. The refusals are approval-consumed, approval-expired,
	// approval-void, previous-epoch, binding-mismatch and
	// evidence-changed.
	Use(ctx context.Context, in UseInput) (Used, error)

	// Settle records how applying a used approval went. It is idempotent
	// on the id and the binding digest.
	Settle(ctx context.Context, in SettleInput) (Settled, error)

	// List returns one entry for every pending record, oldest first.
	List(ctx context.Context) ([]Entry, error)

	// Show returns one pending record with its entry, and its decision
	// when it has one. An id the door does not know is refused
	// approval-void.
	Show(ctx context.Context, id string) (Shown, error)
}

// Record is one record as it travels: its bytes and the digest of those
// bytes. The digest is [record.Digest] of Bytes; a door refuses a Record
// whose two parts disagree.
type Record struct {
	// Bytes are the record's bytes, never written again on the way.
	Bytes []byte
	// SHA256 is the SHA-256 of Bytes in lower-case hexadecimal.
	SHA256 string
}

// RecordOf returns b as a Record, with its digest. It keeps b and does not
// copy it.
func RecordOf(b []byte) Record {
	return Record{Bytes: b, SHA256: record.Digest(b)}
}

// Admission is the door's yes to a request.
type Admission struct {
	// RequestID is the request's own id, and RequestSHA256 the digest of
	// its bytes. The pending record will carry both.
	RequestID     string
	RequestSHA256 string
}

// PlanInput is what the host gives the door to plan from: the request, and
// the host's own findings (the design's host block). Nothing in it but the
// request and the note is the lead's.
type PlanInput struct {
	// Request is the request as it was admitted.
	Request Record
	// Note is the lead's note, which was sent beside the request. It is
	// shown and not approved.
	Note record.Text

	// TargetRef is the full name of the branch a feature finish moves,
	// such as refs/heads/develop. A prune has none.
	TargetRef string
	// Base is the target's tip the host resolved, Feature the feature's
	// tip, and Merge the merge commit the host built from the two. A prune
	// has none of them.
	Base    string
	Feature string
	Merge   string
	// Summary is the intake's review of Base to Merge. A prune has none.
	Summary *record.Summary
	// Evidence is the list of records the approval will rest on.
	Evidence []record.Evidence
	// Deletes are the effects of a prune, each deleting one merged branch.
	// A feature finish has none: its one effect follows from TargetRef,
	// Base and Merge.
	Deletes []record.Effect

	// MergeParents are the parents of Merge, and TargetNow the tip of
	// TargetRef, both as the repository shows them when the plan is asked
	// for. The real door reads them itself; a door that cannot is given
	// them here. A plan is refused unless the parents are exactly Base and
	// Feature and the target is still at Base.
	MergeParents []string
	TargetNow    string
}

// Decided is the door's answer to a decision.
type Decided struct {
	// State is the state the decision put the record in: approved or
	// denied.
	State record.State
	// DecisionSHA256 is the digest of the decision the door keeps for the
	// record. When Decide answers for a decision it already had, this is
	// the digest of that first one.
	DecisionSHA256 string
}

// UseInput names the approval to use and carries what the host read just
// before.
type UseInput struct {
	// ID is the pending record's id.
	ID string
	// Fresh is what the repository and the evidence show now.
	Fresh Fresh
}

// Fresh is what the host found when it inspected again, just before a use.
// The door compares it with the binding.
type Fresh struct {
	// Refs are the present values of the refs the effects name. Every ref
	// of the binding's effects must be here, at the effect's old value.
	Refs []RefValue
	// ReviewDigest is the digest of the review as made again, and
	// RuleSetSHA256 the digest of the execution-config rules in force.
	// Both are empty for a prune, which has no review.
	ReviewDigest  string
	RuleSetSHA256 string
	// Evidence says, for each record the binding rests on, how it stands.
	Evidence []EvidenceState
}

// RefValue is one ref and the object id it is at.
type RefValue struct {
	Ref   string
	Value string
}

// EvidenceState is how one bound evidence record stands at a use.
type EvidenceState struct {
	// RecordID and RecordSHA256 are the record's id and its digest as read
	// now.
	RecordID     string
	RecordSHA256 string
	// Newest is whether the record is still the newest of its kind for
	// what it is evidence of. A later failed run makes it false.
	Newest bool
}

// Used is the door's yes to a use.
type Used struct {
	// State is consumed.
	State record.State
	// BindingSHA256 is the digest of the binding that was used.
	BindingSHA256 string
	// Effects are the ref updates the approval allows, and nothing else.
	Effects []record.Effect
}

// Outcome is how applying a used approval went.
type Outcome string

// The outcomes of a settle. Each is also the name of the state it leads to.
const (
	// OutcomeApplied says the effects were applied.
	OutcomeApplied Outcome = "applied"
	// OutcomeNotApplied says nothing changed, for one because a ref had
	// moved. The approval is not revived: the lead asks again.
	OutcomeNotApplied Outcome = "not-applied"
	// OutcomeUnknown says the host cannot tell. The approval stays spent
	// until the owner disposes of it, which is a later settle with one of
	// the other two.
	OutcomeUnknown Outcome = "unknown"
)

// Valid reports whether o is one of the three outcomes.
func (o Outcome) Valid() bool {
	return o == OutcomeApplied || o == OutcomeNotApplied || o == OutcomeUnknown
}

// State returns the state of a record settled with o.
func (o Outcome) State() record.State {
	return record.State(o)
}

// SettleInput names a used approval and how applying it went.
type SettleInput struct {
	// ID is the pending record's id, and BindingSHA256 the digest of the
	// binding that was used.
	ID            string
	BindingSHA256 string
	// Outcome is what happened.
	Outcome Outcome
}

// Settled is the door's answer to a settle.
type Settled struct {
	// State is the state the record is in after the settle.
	State record.State
}

// Entry is one pending record in a listing: who it is and where it stands,
// without its bytes.
type Entry struct {
	// ID is the pending record's id, and RequestID the id of the request
	// it answers.
	ID        string
	RequestID string
	// Verb is the verb that was asked for.
	Verb string
	// State is the record's state.
	State record.State
	// ReasonCode says why the door ended the record, when a refusal did
	// (a use that did not match, say). It is empty otherwise.
	ReasonCode record.Code
	// ExpiresAt is when the record ends by itself, as a record's time: the
	// approval's end once it is approved, the pending record's before.
	ExpiresAt string
	// BindingSHA256 is the digest of the binding, PendingSHA256 the digest
	// of the pending record's bytes, and DecisionSHA256 the digest of the
	// decision's bytes, or empty while there is none.
	BindingSHA256  string
	PendingSHA256  string
	DecisionSHA256 string
}

// Shown is one pending record as the door keeps it.
type Shown struct {
	// Entry is the record's line of the listing.
	Entry Entry
	// Pending is the pending record.
	Pending Record
	// Decision is the decision, or the zero Record while there is none.
	Decision Record
}
