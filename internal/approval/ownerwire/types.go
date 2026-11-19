package ownerwire

import "ataniq/internal/approval/record"

// The size caps, in bytes of the JSON as it is written. A payload over its
// cap is refused record-too-large, on decoding and on encoding.
const (
	// MaxParamsBytes caps the params of every operation but the decision
	// frame. The request line of the client protocol is 64 KiB.
	MaxParamsBytes = 4 << 10
	// MaxFrameBytes caps a decision frame. It is the cap of the decision
	// record the daemon makes from it, which holds the same sign-offs.
	MaxFrameBytes = record.MaxDecisionBytes
	// MaxResultBytes caps the small results: the decider's answer, an
	// outcome, a limit's view, and nothing.
	MaxResultBytes = 16 << 10
	// MaxPageBytes caps a page of paths, a page of a patch, the listing and
	// the result of a show. The response line of the client protocol is
	// 1 MiB, so each fits in one line with room for the envelope.
	MaxPageBytes = 256 << 10
)

// The other bounds of the forms.
const (
	// MaxRecent is the most recent decisions a listing carries.
	MaxRecent = 100
	// MaxPages is the most pages one path list or one patch may have.
	MaxPages = 1 << 16
	// MaxLinePieces is the most pieces one line of a patch may travel in.
	// The console's pager ends the reading of a line in more.
	MaxLinePieces = 4096
	// MaxLineBytes is the longest line of a patch, in raw bytes. The
	// console's pager ends the reading of a longer one.
	MaxLineBytes = 16 << 20
	// MaxHostLineBytes caps a line the host writes for the owner's eyes: an
	// outcome's line, the reason a connection is not the decider, a rule in
	// words.
	MaxHostLineBytes = 1 << 10
	// MaxNameBytes caps a client's name and a rule's id.
	MaxNameBytes = 128
	// MaxLimitDays is the most days a limit's view carries.
	MaxLimitDays = 64
)

// Nothing is the params of an operation that takes none and the result of
// one that answers nothing: an empty JSON object.
type Nothing struct{}

// Listing is the result of owner.list: what the approvals list shows. It
// carries no record's bytes; owner.show returns those. Encoded, it is at
// most [MaxPageBytes].
type Listing struct {
	// Pending are the pending records, one row each.
	Pending []PendingItem `json:"pending"`
	// Recent are the latest decisions and outcomes, newest first, at most
	// [MaxRecent].
	Recent []RecentItem `json:"recent"`
	// Rules are the standing rules.
	Rules []Rule `json:"rules"`
	// Count is the count from the first card.
	Count Count `json:"count"`
	// LookedAt is the "since he last looked" mark, a record's time. It is
	// left out when he never looked.
	LookedAt string `json:"looked_at,omitempty"`
}

// PendingItem is one pending record in a listing.
type PendingItem struct {
	// ID is the pending record's id, and SHA256 the digest of its bytes.
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	// Project is the project the request belongs to.
	Project string `json:"project"`
	// Verb is the verb asked for, one of the record package's two.
	Verb string `json:"verb"`
	// Feature is the feature name of a feature finish; it passed
	// [record.ValidFeatureName]. A prune has none, and then the field is
	// left out.
	Feature string `json:"feature,omitempty"`
	// RequestedAt and PendingExpiresAt are the binding's two times.
	RequestedAt      string `json:"requested_at"`
	PendingExpiresAt string `json:"pending_expires_at"`
	// Held is the mark "decided past the limit": an approve was recorded
	// and not applied, and the card is pending again.
	Held bool `json:"held"`
}

// RecentItem is one decided or ended record in a listing.
type RecentItem struct {
	// ID is the pending record's id.
	ID string `json:"id"`
	// Project, Verb and Feature are as in [PendingItem].
	Project string `json:"project"`
	Verb    string `json:"verb"`
	Feature string `json:"feature,omitempty"`
	// State is the record's state now.
	State record.State `json:"state"`
	// Line says what happened, in the host's words.
	Line string `json:"line"`
	// At is when it happened, a record's time.
	At string `json:"at"`
}

// Rule is one standing rule in a listing.
type Rule struct {
	// ID names the rule, and Project the project it is for.
	ID      string `json:"id"`
	Project string `json:"project"`
	// Line is the rule in the host's words.
	Line string `json:"line"`
	// Uses counts how often the rule was used, and On is its switch.
	Uses int64 `json:"uses"`
	On   bool  `json:"on"`
}

// Count is the count from the first card.
type Count struct {
	// Requests is how many requests there were, and WouldMeet how many of
	// them would have met the terms of the first template.
	Requests  int64 `json:"requests"`
	WouldMeet int64 `json:"would_meet"`
}

// ShowParams is the params of owner.show.
type ShowParams struct {
	// ID is the pending record's id.
	ID string `json:"id"`
}

// Record is a record as it travels: its bytes, and the digest of those
// bytes. The bytes are base64 in the JSON. A decoder checks that the digest
// is the bytes' own, and nothing here parses the bytes or writes them
// again, so the record that arrives is the record that was sent. Make one
// with [NewRecord].
type Record struct {
	// Bytes are the record's bytes, at most [record.MaxPendingBytes].
	Bytes []byte `json:"bytes"`
	// SHA256 is [record.Digest] of Bytes.
	SHA256 string `json:"sha256"`
}

// NewRecord returns data with its digest. It keeps data and does not copy
// it.
func NewRecord(data []byte) Record {
	return Record{Bytes: data, SHA256: record.Digest(data)}
}

// ShowResult is the result of owner.show: one pending record and what a
// card needs that is not in the record.
type ShowResult struct {
	// Record is the pending record.
	Record Record `json:"record"`
	// Held is the mark "decided past the limit".
	Held bool `json:"held"`
	// Outcome is what came after the decision. A record not yet decided
	// has none.
	Outcome *Outcome `json:"outcome,omitempty"`
	// Earlier is the earlier record for the same verb and target that the
	// owner opened in the last 24 hours, when there is one.
	Earlier *Earlier `json:"earlier,omitempty"`
}

// Outcome is what came after a decision.
type Outcome struct {
	// State is the record's state.
	State record.State `json:"state"`
	// Line says what happened, in the host's words.
	Line string `json:"line"`
}

// Earlier is an earlier pending record the owner has read.
type Earlier struct {
	// Record is the earlier pending record.
	Record Record `json:"record"`
	// ReadAt is when he read it, a record's time.
	ReadAt string `json:"read_at"`
}

// PageParams is the params of owner.paths and of owner.patch: which page of
// which review.
type PageParams struct {
	// ID is the pending record's id.
	ID string `json:"id"`
	// ReviewDigest is the digest of the review the card was made from. The
	// daemon refuses the read review-changed when the review behind ID is
	// another.
	ReviewDigest string `json:"review_digest"`
	// Page is the page's number, from 0.
	Page int `json:"page"`
}

// PathsPage is the result of owner.paths: one page of the full path list.
// Encoded, it is at most [MaxPageBytes].
type PathsPage struct {
	// ID and ReviewDigest name the pending record and the review the page
	// belongs to.
	ID           string `json:"id"`
	ReviewDigest string `json:"review_digest"`
	// Page is this page's number, from 0, and Pages how many there are.
	Page  int `json:"page"`
	Pages int `json:"pages"`
	// Paths are the page's paths. Only the one page of an empty list has
	// none.
	Paths []record.PathChange `json:"paths"`
}

// PatchPage is the result of owner.patch: one page of the patch. Encoded,
// it is at most [MaxPageBytes].
type PatchPage struct {
	// ID and ReviewDigest name the pending record and the review the page
	// belongs to.
	ID           string `json:"id"`
	ReviewDigest string `json:"review_digest"`
	// Page is this page's number, from 0, and Pages how many there are.
	Page  int `json:"page"`
	Pages int `json:"pages"`
	// Lines are the page's lines, or pieces of lines. Only the one page of
	// an empty patch has none.
	Lines []PatchLine `json:"lines"`
}

// PatchLine is one line of a patch, or one piece of a line too long for
// the room a page had.
type PatchLine struct {
	// Text is the raw bytes, without the line's end. They are lead-written
	// and not yet escaped: show them only through the lead-text escaping.
	Text record.Text `json:"text"`
	// Cont is true when the line continues in the next piece, which may be
	// the first of the next page. A piece that continues is never empty.
	Cont bool `json:"cont"`
}

// DeciderParams is the params of owner.decider.
type DeciderParams struct {
	// Client is the surface's program name and version.
	Client string `json:"client"`
	// Session is the id the surface made for this run of itself: 16
	// lower-case hexadecimal digits. The daemon keeps it for the connection
	// and writes it into every decision made over it.
	Session string `json:"session"`
}

// DeciderResult is the result of owner.decider.
type DeciderResult struct {
	// Decider is whether this connection is the one that may decide.
	Decider bool `json:"decider"`
	// Reason says why not, in the host's words. It is there exactly when
	// Decider is false.
	Reason string `json:"reason,omitempty"`
}

// DecisionFrame is the params of owner.decide: the owner's answer to one
// card, and nothing the daemon knows better. The daemon writes the decision
// record from it and fills everything else itself (the planner, the bound
// fields, the uses, the times, how it was confirmed, the console block), so
// a frame that carries any of those is refused decision-invalid.
type DecisionFrame struct {
	// ID is the pending record's id.
	ID string `json:"id"`
	// BindingSHA256 is the digest of the binding the card showed.
	BindingSHA256 string `json:"binding_sha256"`
	// Decision is [record.DecisionApprove] or [record.DecisionDeny].
	Decision string `json:"decision"`
	// SignedOff are the flagged paths the owner signed off, in the order of
	// the binding's flagged list. A deny has none, and the list is empty.
	SignedOff []record.SignOff `json:"signed_off"`
	// Reason is why the owner denied, at most [record.MaxReasonBytes]. An
	// approve has none, and a deny may have none; then the field is left
	// out.
	Reason string `json:"reason,omitempty"`
}

// DecideResult is the result of owner.decide: the outcome the daemon
// answers a decision with.
type DecideResult struct {
	// State is the record's state after the decision.
	State record.State `json:"state"`
	// Line says what happened, in the host's words.
	Line string `json:"line"`
	// Held is true when an approve came past the day's limit: it was
	// recorded and not applied, and the record is still pending.
	Held bool `json:"held"`
}

// LimitParams is the params of owner.limit.
type LimitParams struct {
	// Project is the project whose limit is read.
	Project string `json:"project"`
}

// LimitSetParams is the params of owner.limit-set.
type LimitSetParams struct {
	// Project is the project whose limit is changed.
	Project string `json:"project"`
	// To is the new limit.
	To int `json:"to"`
	// Typed is whether the owner typed the number. A raise must be typed.
	Typed bool `json:"typed"`
}

// LimitView is the result of owner.limit and of owner.limit-set: the daily
// card limit of one project as its screen shows it. Whether a number is a
// lawful limit is the limit's own rule and is not checked here.
type LimitView struct {
	// Project is the project the limit is for.
	Project string `json:"project"`
	// Limit is the present limit.
	Limit int `json:"limit"`
	// Days are the days the view shows, at most [MaxLimitDays].
	Days []LimitDay `json:"days"`
	// Suggestion is the number the host suggests.
	Suggestion int `json:"suggestion"`
	// RaisedToday is whether the limit was raised today already.
	RaisedToday bool `json:"raised_today"`
}

// LimitDay is one day of a limit's view.
type LimitDay struct {
	// Date is the day as YYYY-MM-DD, in the daemon's clock's location.
	Date string `json:"date"`
	// Uses is how many cards were used that day.
	Uses int `json:"uses"`
	// ReachedLimit and Alert say whether the day reached the limit and
	// whether it had an alert.
	ReachedLimit bool `json:"reached_limit"`
	Alert        bool `json:"alert"`
}
