package ownerwire

import "ataniq/internal/approval/record"

// The size caps, in bytes of the JSON as it is written. A payload over its
// cap is refused record-too-large, on decoding and on encoding.
const (
	// MaxParamsBytes caps the params of every operation but the decision
	// frame. The request line of the client protocol is 64 KiB.
	MaxParamsBytes = 4 << 10
	// MaxSeatPairParamsBytes caps the params of seat.pair, which carry the
	// registration's authenticator data beside the credential id and key.
	MaxSeatPairParamsBytes = 12 << 10
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
	// NotListed is how many pending records were left out of Pending
	// because the listing did not fit one page: the newest ones, so that
	// the oldest, which end first, are shown. It is 0 when every pending
	// record is listed. The field is always written.
	NotListed int64 `json:"not_listed"`
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
	// has none, except a pending one whose last decision the door refused:
	// its outcome is the state pending with a host-written line saying so.
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
	// To is the new limit. A number under 1 is refused when the params are
	// read, before the limit's own rule is asked.
	To int `json:"to"`
	// Typed is whether the owner typed the number. A raise must be typed.
	Typed bool `json:"typed"`
}

// LimitView is the result of owner.limit and of owner.limit-set: the daily
// card limit of one project as its screen shows it. A limit under 1 and a
// negative suggestion are refused here, as is a limit-set whose To is under
// 1; any other rule of a lawful limit is the limit's own.
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

// The bounds of the pairing and key forms.
const (
	// PairCodeLength is the number of characters of a pairing code, the
	// length package seatpair makes (a test of the service holds the two
	// equal). The alphabet is that of Crockford's base32 without I, L, O
	// and U, in either case.
	PairCodeLength = 20
	// PairWords is the number of words of a credential's fingerprint, the
	// count package seatpair makes (a test of the service holds the two
	// equal).
	PairWords = 8
	// MinCredentialIDBytes and MaxCredentialIDBytes bound a credential id:
	// at most 1023 bytes, as WebAuthn level 3, section 4, says and as
	// package credkind's verifier takes (a test of the service holds the
	// two equal), so that no credential is paired that cannot be verified.
	MinCredentialIDBytes = 16
	MaxCredentialIDBytes = 1023
	// MaxPublicKeyBytes caps a credential's public key as the seat sends
	// it, a DER SubjectPublicKeyInfo. An ES256 key is 91 bytes; whether
	// the bytes are one is for the daemon.
	MaxPublicKeyBytes = 512
	// MinRegistrationDataBytes and MaxRegistrationDataBytes bound the
	// authenticator data of a registration as the seat relays it: the head
	// and the attested credential data's own head at least, and at most what
	// package credkind reads.
	MinRegistrationDataBytes = 37 + 18
	MaxRegistrationDataBytes = 4096
	// MaxRPIDBytes caps a relying party id, a host name.
	MaxRPIDBytes = 253
	// MaxKeys is the most credentials the list of deciding keys holds,
	// revoked ones included, which stay listed.
	MaxKeys = 64
	// KeyIDLength is the length of a credential's display id: 16
	// lower-case hexadecimal digits, the first 8 bytes of the SHA-256 of
	// the credential id.
	KeyIDLength = 16
	// MinKeyIDPrefix is the shortest prefix of a display id that
	// owner.key-revoke takes.
	MinKeyIDPrefix = 4
	// PendingIDLength is the length of a pending credential's pending id:
	// 16 lower-case hexadecimal digits, 8 random bytes the daemon makes when
	// the credential becomes pending.
	PendingIDLength = 16
)

// The phases of a pairing, the words of [PairStatus].Phase.
const (
	// PairNone: no pairing is open, and none ended unseen.
	PairNone = "none"
	// PairWaiting: a code was made and the phone has not presented it.
	PairWaiting = "waiting"
	// PairPending: the phone presented a good credential, and it waits at
	// the desk for the owner's word.
	PairPending = "pending"
	// PairExpired: the pairing ran out, or its code was burnt by wrong
	// tries, before it was confirmed or rejected.
	PairExpired = "expired"
)

// PairStartParams is the params of owner.pair-start.
type PairStartParams struct {
	// Replace says that a pairing that is open may be ended and replaced.
	// Without it, a start while one is open is refused.
	Replace bool `json:"replace"`
}

// PairStartResult is the result of owner.pair-start. It holds the pairing
// code, a secret that this answer alone carries: it is never in a journal, a
// log, an error or any other answer. A value of the type prints without it.
type PairStartResult struct {
	// Code is the one-time pairing code, [PairCodeLength] characters.
	Code string `json:"code"`
	// ExpiresAt is when the code ends, a record's time.
	ExpiresAt string `json:"expires_at"`
}

// String says what the value is, without the code.
func (r PairStartResult) String() string { return "ownerwire.PairStartResult{" + r.ExpiresAt + "}" }

// GoString is the same text for the %#v verb.
func (r PairStartResult) GoString() string { return r.String() }

// PairStatus is the result of owner.pair-status. It never holds the code.
type PairStatus struct {
	// Phase is one of [PairNone], [PairWaiting], [PairPending] and
	// [PairExpired].
	Phase string `json:"phase"`
	// Words are the [PairWords] words of the pending credential's
	// fingerprint, which the phone shows too. They are there exactly when
	// the phase is pending.
	Words []string `json:"words,omitempty"`
	// PendingID names the pending credential: [PendingIDLength] lower-case
	// hexadecimal digits the daemon drew at random when the credential
	// became pending. It is there exactly when the phase is pending, and a
	// confirm or a reject has to give it back, so that the owner's word is
	// about the credential whose words he compared and not about one that
	// replaced it.
	PendingID string `json:"pending_id,omitempty"`
	// ExpiresAt is when the waiting code or the pending credential ends,
	// or ended, a record's time; for a code burnt by wrong tries it is the
	// time of the try that burnt it, in the past. It is left out for the
	// phase none.
	ExpiresAt string `json:"expires_at,omitempty"`
	// Kind, AAGUID, BackupEligible and BackupState are what the pending
	// credential's authenticator claimed at its registration, when the seat
	// relayed it: only for the phase pending, and then optional, with Kind
	// there when any other is. None of it is proven (the pairing uses
	// attestation "none"), and the desk shows it as claimed.
	//
	// Kind is the kind the AAGUID claims, a word of package credkind
	// ("platform-passkey", "vault-passkey" or "passkey"). AAGUID is the
	// authenticator's claimed model in the lower-case 8-4-4-4-12 form, left
	// out when it is all zero. BackupEligible is the BE flag (the credential
	// can be backed up: a synced passkey) and BackupState the BS flag (it is
	// backed up now), never set without BackupEligible.
	Kind           string `json:"kind,omitempty"`
	AAGUID         string `json:"aaguid,omitempty"`
	BackupEligible bool   `json:"backup_eligible,omitempty"`
	BackupState    bool   `json:"backup_state,omitempty"`
}

// PairPendingParams is the params of owner.pair-confirm and of
// owner.pair-reject: the pending id the owner saw in [PairStatus] with the
// words he compared. It is required, and the daemon refuses a call whose id is
// not that of the credential pending now (pairing-changed), changing nothing.
type PairPendingParams struct {
	PendingID string `json:"pending_id"`
}

// SeatPairParams is the params of seat.pair, the seat app's relay of what
// the phone said. The code is a secret: nothing here, no refusal, no log,
// holds it. A value of the type prints without it.
type SeatPairParams struct {
	// Code is the pairing code the phone read from the desk's QR.
	Code string `json:"code"`
	// CredentialID is the id of the credential the phone made.
	CredentialID []byte `json:"credential_id"`
	// PublicKey is the credential's public key, a DER SubjectPublicKeyInfo.
	PublicKey []byte `json:"public_key"`
	// AuthenticatorData is the registration's authenticator data as the
	// phone's browser gave it, which the seat checked against its relying
	// party, and RPID that relying party's id. They are given together or
	// not at all: the daemon registers the credential from them with
	// package credkind, reads the kind its AAGUID claims, its backup flags
	// and its sign counter, and checks that the credential id and the key in
	// them are the ones above. A pairing without them is of the kind
	// "unverified".
	AuthenticatorData []byte `json:"authenticator_data,omitempty"`
	RPID              string `json:"rp_id,omitempty"`
}

// String says what the value is, without the code or the key.
func (p SeatPairParams) String() string { return "ownerwire.SeatPairParams{}" }

// GoString is the same text for the %#v verb.
func (p SeatPairParams) GoString() string { return p.String() }

// SeatPairResult is the result of seat.pair: the fingerprint of the
// credential, which the desk shows and the owner compares.
type SeatPairResult struct {
	// Words are the [PairWords] words of the fingerprint.
	Words []string `json:"words"`
}

// KeyItem is one credential on the list of deciding keys.
type KeyItem struct {
	// ID is the display id: [KeyIDLength] lower-case hexadecimal digits,
	// the first 8 bytes of the SHA-256 of the credential id. It names the
	// credential for the owner and holds nothing of the key.
	ID string `json:"id"`
	// Kind is the kind of the key, a word and a claim: the kind its
	// authenticator's AAGUID claimed at the pairing ("platform-passkey",
	// "vault-passkey" or "passkey"), or "unverified" for a key paired
	// without what its authenticator claimed.
	Kind string `json:"kind"`
	// AAGUID, BackupEligible and BackupState are what the authenticator
	// claimed at the pairing, each optional, in the forms of [PairStatus].
	AAGUID         string `json:"aaguid,omitempty"`
	BackupEligible bool   `json:"backup_eligible,omitempty"`
	BackupState    bool   `json:"backup_state,omitempty"`
	// PairedAt is when the owner confirmed the pairing, a record's time.
	PairedAt string `json:"paired_at"`
	// Revoked says that the credential was revoked. A revoked credential
	// stays on the list and is never made good again.
	Revoked bool `json:"revoked"`
}

// KeysView is the result of owner.keys, of owner.key-revoke and of
// owner.keys-off: the list of deciding keys, in the order they were paired,
// at most [MaxKeys].
type KeysView struct {
	Keys []KeyItem `json:"keys"`
}

// KeyRevokeParams is the params of owner.key-revoke.
type KeyRevokeParams struct {
	// ID is a display id, or a prefix of one of at least [MinKeyIDPrefix]
	// digits, that names exactly one credential.
	ID string `json:"id"`
}
