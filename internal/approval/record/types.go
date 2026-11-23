package record

// The schema names of the three records.
const (
	// SchemaRequest names a request, which the lead writes.
	SchemaRequest = "qargi.approval-request/1"
	// SchemaPending names a pending record, which the host computes.
	SchemaPending = "ataniq.approval-pending/1"
	// SchemaDecision names a decision, the owner's answer.
	SchemaDecision = "ataniq.owner-decision/1"
)

// The size caps, in bytes. A record is refused on decoding when its bytes
// are more than the cap of its kind, and on encoding when its canonical
// form is.
const (
	// MaxRequestBytes caps a request.
	MaxRequestBytes = 8 << 10
	// MaxNoteBytes caps the raw bytes of the lead's note.
	MaxNoteBytes = 2 << 10
	// MaxPendingBytes caps a pending record.
	MaxPendingBytes = 64 << 10
	// MaxDecisionBytes caps a decision.
	MaxDecisionBytes = 8 << 10
	// MaxReasonBytes caps the reason of a deny.
	MaxReasonBytes = 500
	// MaxEffects caps the effects of one binding: a prune covers at most
	// this many refs.
	MaxEffects = 200
)

// The verbs that can be asked for, and their one class.
const (
	// VerbFeatureFinish merges a feature branch into its target. Its argv
	// is the feature name and nothing else.
	VerbFeatureFinish = "feature finish"
	// VerbBranchPrune deletes merged upstream branches. Its argv is empty.
	VerbBranchPrune = "branch prune"
	// ClassRefMove is the class of both verbs: the effect is a ref update
	// upstream.
	ClassRefMove = "ref-move"
)

// The other closed words of the forms.
const (
	// SubjectSeat is the only kind of subject: a seat of the planner.
	SubjectSeat = "seat"
	// EffectRef is the only kind of effect: one ref moves.
	EffectRef = "ref"
	// BuiltByHost says the host built the merge. No other builder is
	// allowed.
	BuiltByHost = "host"
	// EvidenceSuite is the only kind of evidence: a whole-suite run.
	EvidenceSuite = "suite"
	// DecisionApprove and DecisionDeny are the two answers of a decision.
	DecisionApprove = "approve"
	DecisionDeny    = "deny"
	// ConfirmedCode says the owner confirmed by typing the card's code.
	// The code itself is in no record. A decision confirmed so has a
	// console block and no seat block.
	ConfirmedCode = "code"
	// ConfirmedPasskey says the owner confirmed on his phone, through the
	// seat app, with a fresh passkey assertion over a challenge the daemon
	// drew. A decision confirmed so has a seat block and no console block.
	ConfirmedPasskey = "passkey"
)

// The kinds of flag, as the intake names them. A flagged path needs its own
// sign-off.
const (
	FlagExecConfig  = "exec-config"
	FlagSymlink     = "symlink"
	FlagSubmodule   = "submodule"
	FlagAliased     = "aliased"
	FlagHistoryOnly = "history-only"
	// FlagOddPath marks a path the matcher cannot read safely: a colon, a
	// tilde and a digit, a combining mark or text not in NFC, an invisible
	// character, or bytes that are not UTF-8. It needs a sign-off.
	FlagOddPath = "odd-path"
)

// Request is what a lead asks for. Everything in it is the lead's claim:
// the host plans from its own reading of the repository, and uses the
// request only to learn what is meant. It names no project and no role;
// those come from the seat's assignment.
type Request struct {
	// Schema is [SchemaRequest].
	Schema string `json:"schema"`
	// RequestID is the lead's label for the request; see [ValidRequestID].
	// It is never used as a ref name or as part of a path.
	RequestID string `json:"request_id"`
	// Epoch is the seat's epoch as the lead read it.
	Epoch int64 `json:"epoch"`
	// Verb is [VerbFeatureFinish] or [VerbBranchPrune].
	Verb string `json:"verb"`
	// Argv is the verb's arguments in its closed grammar: one feature name
	// for a feature finish, nothing for a prune. It is not a shell line.
	Argv []string `json:"argv"`
	// FeatureSHA is the tip the lead means, a full object id. A prune has
	// none, and then the field is left out.
	FeatureSHA string `json:"feature_sha,omitempty"`
	// NoteSHA256 is the digest of the note the lead sent beside the
	// request.
	NoteSHA256 string `json:"note_sha256"`
	// At is when the lead says it asked.
	At string `json:"at"`
}

// Pending is the host's record of a request it accepted: what would happen
// if the owner approved, and what the owner is shown. It is never edited
// after it is written.
type Pending struct {
	// Schema is [SchemaPending].
	Schema string `json:"schema"`
	// ID is the host's id for the approval; see [ValidPendingID].
	ID string `json:"id"`
	// Planner names who planned the effects.
	Planner string `json:"planner"`
	// RequestID and RequestSHA256 are the lead's label and the digest of
	// the request's bytes as they arrived.
	RequestID     string `json:"request_id"`
	RequestSHA256 string `json:"request_sha256"`
	// Binding is the part the owner approves.
	Binding Binding `json:"binding"`
	// BindingSHA256 is [BindingDigest] of Binding. A decision names it.
	BindingSHA256 string `json:"binding_sha256"`
	// PlanText is the planner's plan as text. The binding covers it
	// through Binding.PlanSHA256.
	PlanText Text `json:"plan_text"`
	// Note is the lead's note, at most [MaxNoteBytes]. It is outside the
	// binding: it is shown, labelled as the lead's, and not approved.
	Note Text `json:"note"`
}

// Binding is everything an approval is bound to. Its digest is what a
// decision carries, so a change of any field here makes another approval.
type Binding struct {
	// Project is the project the seat belongs to.
	Project string `json:"project"`
	// Subject is who asked.
	Subject Subject `json:"subject"`
	// Verb and Argv are as in the request, checked by the host.
	Verb string   `json:"verb"`
	Argv []string `json:"argv"`
	// Class is [ClassRefMove].
	Class string `json:"class"`
	// PlanToken is the planner's token for the plan.
	PlanToken string `json:"plan_token"`
	// PlanSHA256 is [PlanDigest] of the pending record's plan text.
	PlanSHA256 string `json:"plan_sha256"`
	// Effects are the ref updates an approval allows, and nothing else.
	// Each is on a branch (a ref under refs/heads/). A feature finish
	// has exactly one, which moves the target from the first parent of
	// Merge to its commit; a prune has only effects that delete.
	Effects []Effect `json:"effects"`
	// Merge is the merge commit the host built. A feature finish has one;
	// a prune has none, and then the field is left out.
	Merge *Merge `json:"merge,omitempty"`
	// Summary is the intake's review of the change. A feature finish has
	// one; a prune changes no tree and has none.
	Summary *Summary `json:"summary,omitempty"`
	// Evidence is the list of records the approval rests on, frozen when
	// the pending record is written. A feature finish has a suite record.
	Evidence []Evidence `json:"evidence"`
	// RequestedAt is when the host accepted the request, and
	// PendingExpiresAt when the pending record ends unanswered.
	RequestedAt      string `json:"requested_at"`
	PendingExpiresAt string `json:"pending_expires_at"`
}

// Subject is the seat an approval is asked for and bound to.
type Subject struct {
	// Kind is [SubjectSeat].
	Kind string `json:"kind"`
	// RoleName is the seat's role.
	RoleName string `json:"role_name"`
	// Epoch is the seat's epoch, and SeatRev the revision of the seat
	// record. An approval ends when either moves.
	Epoch   int64 `json:"epoch"`
	SeatRev int64 `json:"seat_rev"`
	// Holder is who holds the seat. A pending record has it; a decision
	// repeats the subject without it.
	Holder *Holder `json:"holder,omitempty"`
}

// Holder is the session that holds a seat.
type Holder struct {
	// Kind is the kind of holder, such as "lead".
	Kind string `json:"kind"`
	// DisplayID is the holder's name for display.
	DisplayID string `json:"display_id"`
	// Runtime names the program the holder runs in.
	Runtime string `json:"runtime"`
}

// Effect is one ref update.
type Effect struct {
	// Kind is [EffectRef].
	Kind string `json:"kind"`
	// Ref is the full ref name.
	Ref string `json:"ref"`
	// Old is the object id the ref must be at, and New the id it moves
	// to. New of all zeros deletes the ref.
	Old string `json:"old"`
	New string `json:"new"`
	// MergedInto says where a deleted branch's commits stay reachable. A
	// delete has it and no other effect does.
	MergedInto *MergedInto `json:"merged_into,omitempty"`
}

// MergedInto names the branch, and its tip, that holds the commits of a
// branch an effect deletes.
type MergedInto struct {
	Ref string `json:"ref"`
	Tip string `json:"tip"`
}

// Merge is the merge commit the host built for a feature finish.
type Merge struct {
	// Commit is the merge commit's id.
	Commit string `json:"commit"`
	// Parents are its two parents: the target's tip, then the feature's.
	Parents []string `json:"parents"`
	// BuiltBy is [BuiltByHost].
	BuiltBy string `json:"built_by"`
}

// Summary is the intake's review of a change, as the card shows it.
type Summary struct {
	// ReviewDigest is the intake's digest of the whole review, which
	// covers the full path list even when Paths is cut.
	ReviewDigest string `json:"review_digest"`
	// Base and Tip are the commits the review compares: the first parent
	// of the binding's merge and the merge commit, so that what is shown
	// is what lands.
	Base string `json:"base"`
	Tip  string `json:"tip"`
	// Files, Added, Removed and NewBytes are the totals of the change:
	// changed paths, lines added and removed, and bytes of new content.
	Files    int64 `json:"files"`
	Added    int64 `json:"added"`
	Removed  int64 `json:"removed"`
	NewBytes int64 `json:"new_bytes"`
	// Paths are the changed paths, or the first part of them when
	// PathsTruncated is true. The flag is for this list only.
	Paths          []PathChange `json:"paths"`
	PathsTruncated bool         `json:"paths_truncated"`
	// Flagged are the paths that need their own sign-off.
	Flagged []Flag `json:"flagged"`
	// Binary, Symlinks, ModeChanges and Submodules are four lists of
	// paths. They are never cut: a record they do not fit in is refused.
	Binary      []Text `json:"binary"`
	Symlinks    []Text `json:"symlinks"`
	ModeChanges []Text `json:"mode_changes"`
	Submodules  []Text `json:"submodules"`
	// ExecConfigSet names the rule set the flags were made with.
	ExecConfigSet ExecConfigSet `json:"exec_config_set"`
}

// PathChange is one changed path of a summary.
type PathChange struct {
	// Path is the path as git stores it.
	Path Text `json:"path"`
	// Status is "A" (added), "M" (modified), "D" (deleted) or "T" (the
	// type of the entry changed).
	Status string `json:"status"`
	// OldMode and NewMode are the git file modes as six octal digits,
	// "000000" where there is no entry.
	OldMode string `json:"old_mode"`
	NewMode string `json:"new_mode"`
	// Added and Removed count lines, and NewBytes the bytes of new
	// content.
	Added    int64 `json:"added"`
	Removed  int64 `json:"removed"`
	NewBytes int64 `json:"new_bytes"`
}

// Flag marks one path that needs its own sign-off.
type Flag struct {
	// Kind is one of the Flag constants.
	Kind string `json:"kind"`
	// Path is the flagged path.
	Path Text `json:"path"`
	// Pattern is the rule that matched, in the rule's own text, or empty
	// when the flag comes from no rule.
	Pattern string `json:"pattern"`
}

// Evidence names one record an approval rests on.
type Evidence struct {
	// Kind is [EvidenceSuite].
	Kind string `json:"kind"`
	// RecordID and RecordSHA256 are the record's id and digest.
	RecordID     string `json:"record_id"`
	RecordSHA256 string `json:"record_sha256"`
}

// ExecConfigSet names the set of execution-config rules a review was
// flagged with.
type ExecConfigSet struct {
	// Matcher is the matcher's version.
	Matcher string `json:"matcher"`
	// SHA256 is the digest of the rules in matching order.
	SHA256 string `json:"sha256"`
	// Additions counts the rules the project added to the built-in set.
	Additions int64 `json:"additions"`
}

// Decision is the owner's answer to one pending record. A surface sends
// only the answer; the daemon writes this record and fills the rest.
type Decision struct {
	// Schema is [SchemaDecision].
	Schema string `json:"schema"`
	// ID and Planner are the pending record's.
	ID      string `json:"id"`
	Planner string `json:"planner"`
	// Decision is [DecisionApprove] or [DecisionDeny].
	Decision string `json:"decision"`
	// BindingSHA256 is the digest of the binding the card showed.
	BindingSHA256 string `json:"binding_sha256"`
	// Project, Subject and PlanToken repeat what the digest covers, so
	// that a refusal can name the field that differs. The subject has no
	// holder here.
	Project   string  `json:"project"`
	Subject   Subject `json:"subject"`
	PlanToken string  `json:"plan_token"`
	// SignedOff are the flagged paths the owner signed off, in the order
	// of the binding's flagged list; see [SignOffsEqual]. A deny confirmed
	// by passkey has none.
	SignedOff []SignOff `json:"signed_off"`
	// Uses is 1 for an approve and 0 for a deny.
	Uses int64 `json:"uses"`
	// DecidedAt is when the owner decided, and ExpiresAt when the
	// approval ends unused.
	DecidedAt string `json:"decided_at"`
	ExpiresAt string `json:"expires_at"`
	// Confirmed is [ConfirmedCode] or [ConfirmedPasskey], and says which of
	// the two blocks below the decision carries: exactly one of them.
	Confirmed string `json:"confirmed"`
	// Remark is the owner's remark, which may be empty.
	Remark string `json:"remark"`
	// Reason is why the owner denied, at most [MaxReasonBytes]. An
	// approve has none, and a deny may have none; then the field is left
	// out.
	Reason string `json:"reason,omitempty"`
	// Console says where the decision was made, for a decision confirmed
	// by code. A decision confirmed by passkey has none, and then the field
	// is left out.
	Console Console `json:"console,omitempty"`
	// Seat says which passkey assertion confirmed the decision and over
	// which challenge, for a decision confirmed by passkey. A decision
	// confirmed by code has none, and then the field is left out.
	Seat SeatBlock `json:"seat,omitempty"`
}

// SignOff is the owner's sign-off of one flagged path.
type SignOff struct {
	// Kind and Path are those of the flag that is signed off.
	Kind string `json:"kind"`
	Path Text   `json:"path"`
}

// Console says which surface a decision came from. The daemon fills all of
// it from what it knows of the connection; a surface never sends it.
type Console struct {
	// PID and TTY are the surface's process and its terminal.
	PID int64  `json:"pid"`
	TTY string `json:"tty"`
	// Client is the surface's program name and version.
	Client string `json:"client"`
	// Decider is whether the surface was the one that may decide.
	Decider bool `json:"decider"`
	// Surface is the kind of surface: "console" today.
	Surface string `json:"surface"`
	// Session is the id the surface made for this run of itself: 16
	// lower-case hexadecimal digits.
	Session string `json:"session"`
}

// SeatBlock says how a decision made on the owner's phone, through the seat
// app and the seat gate, was confirmed. The daemon fills all of it from the
// challenge it drew and the assertion it verified; the seat never sends it.
// The record keeps only the digest of the assertion: the journal entry of
// the decision keeps its bytes.
type SeatBlock struct {
	// AssertionSHA256 is [AssertionDigest] of the assertion that confirmed
	// the decision: 64 lower-case hexadecimal digits.
	AssertionSHA256 string `json:"assertion_sha256"`
	// ChallengeID is the id of the challenge the assertion signed: 16
	// lower-case hexadecimal digits.
	ChallengeID string `json:"challenge_id"`
	// Credential is the display id of the credential that signed: 16
	// lower-case hexadecimal digits, the first 8 bytes of the SHA-256 of
	// the credential id.
	Credential string `json:"credential"`
	// Session is the gate session the decision came through: the first 16
	// hexadecimal digits of the seat's pinned key fingerprint.
	Session string `json:"session"`
}
