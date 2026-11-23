package ownerchannel

import (
	"context"
	"errors"
	"io"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
)

// This file declares everything outside the service that it talks to. The
// service opens no socket, runs no git and reads no file but the journal;
// each of these interfaces stands for something that does. The daemon's
// adapters implement them in Part 2, and the tests of this package use
// fakes.

// Journal is the part of [ownerjournal.Store] the service uses, as an
// interface so that a test can make a store fail as a failed commit does.
// An opened *ownerjournal.Store is one.
type Journal interface {
	AppendDecision(decision []byte, seen ownerjournal.Seen) (*ownerjournal.Decision, error)
	AppendHeld(decision, pending []byte, seen ownerjournal.Seen) (ownerjournal.HeldCard, error)
	AppendRefused(r ownerjournal.Refused) error
	AppendCount(c ownerjournal.Count) error
	AppendLimit(c ownerjournal.LimitChange) error
	AppendAlert(a ownerjournal.Alert) error
	AppendLooked(at time.Time) error
	CardUses(project string, from time.Time) ([]time.Time, error)
	Days(project string, now time.Time) ([]ownerjournal.Day, error)
	Limit(project string) (ownerjournal.Limit, error)
	LimitChanges(project string) ([]ownerjournal.LimitChange, error)
	HeldCards(project string) ([]ownerjournal.HeldCard, error)
	ExpireHeld(now time.Time) ([]ownerjournal.HeldCard, error)
	Undelivered() ([]*ownerjournal.Decision, error)
	OpenUses() ([]*ownerjournal.Delivery, error)
	UnknownOutcomes() ([]*ownerjournal.Delivery, error)
	Unjournaled(decided []ownerjournal.DoorDecision) ([]ownerjournal.DoorDecision, error)
	Entries(after int64, max int) ([]ownerjournal.Entry, error)
	LookedAt() (time.Time, error)
	Close() error
}

var _ Journal = (*ownerjournal.Store)(nil)

// HandleWrites is the part of a store that is written through the handles
// of a decision and of a delivery, which the [Journal] methods return and
// which belong to the opened store, so the Journal interface cannot make
// them fail. The service writes through this interface, and a test puts its
// own in [Config.Handles] to make each of the four fail as a failed commit
// does, with [ownerjournal.ErrUncertain]. The default is the handles' own
// methods.
type HandleWrites interface {
	// MarkDelivered is [ownerjournal.Decision.MarkDelivered].
	MarkDelivered(d *ownerjournal.Decision, at time.Time) (*ownerjournal.Delivery, error)
	// MarkRefusedByDoor is [ownerjournal.Decision.MarkRefusedByDoor].
	MarkRefusedByDoor(d *ownerjournal.Decision, at time.Time, code record.Code) error
	// AppendUse is [ownerjournal.Delivery.AppendUse].
	AppendUse(u *ownerjournal.Delivery, at time.Time, code record.Code) error
	// AppendOutcome is [ownerjournal.Delivery.AppendOutcome].
	AppendOutcome(u *ownerjournal.Delivery, at time.Time, state record.State, line string) error
}

// directHandles writes through the handles themselves.
type directHandles struct{}

func (directHandles) MarkDelivered(d *ownerjournal.Decision, at time.Time) (*ownerjournal.Delivery, error) {
	return d.MarkDelivered(at)
}

func (directHandles) MarkRefusedByDoor(d *ownerjournal.Decision, at time.Time, code record.Code) error {
	return d.MarkRefusedByDoor(at, code)
}

func (directHandles) AppendUse(u *ownerjournal.Delivery, at time.Time, code record.Code) error {
	return u.AppendUse(at, code)
}

func (directHandles) AppendOutcome(u *ownerjournal.Delivery, at time.Time, state record.State, line string) error {
	return u.AppendOutcome(at, state, line)
}

// Source says where a frame of the owner connection came from, as the
// caller knows it. The service inspects no peer: the caller decides whether
// the connection may decide, and passes the answer in Decider.
type Source struct {
	// Surface is the kind of surface (1 to 32 lower-case letters, digits
	// and dashes), "console" today, and Session the id of its run: 16
	// lower-case hexadecimal digits.
	Surface string
	Session string
	// Decider says that this connection is the one that may decide. It is
	// the caller's judgement and the service takes it as given.
	Decider bool
	// PID, TTY and Client are what the caller saw of the peer. They are
	// written into the decision record's console block and nowhere else.
	PID    int64
	TTY    string
	Client string
}

// Merger builds the merge commit of a feature on the host and pins it. The
// real one is package mergebuild behind an adapter.
type Merger interface {
	// Build merges the feature's tip into the target's tip, writes the
	// merge commit and pins it under in.Key. A merge with a conflict is
	// refused with a *record.Error whose code is merge-conflict, and a
	// target that already holds the feature with [ErrNothingToMerge]. When
	// the state of the pin is unclear the merge is still returned, with an
	// error that wraps [ErrPinUnclear]: the service then looks at the pin
	// and unpins before it goes on.
	Build(ctx context.Context, in MergeInput) (BuiltMerge, error)
	// Pinned returns the commit the pin of key holds, or the empty string
	// when there is no pin.
	Pinned(ctx context.Context, key string) (string, error)
	// Unpin removes the pin of key, which must hold commit. A key with no
	// pin is not an error for the service: it asks Pinned first.
	Unpin(ctx context.Context, key, commit string) error
}

// The errors of a [Merger] that the service tells apart, found with
// errors.Is.
var (
	// ErrNothingToMerge says the target already holds the feature.
	ErrNothingToMerge = errors.New("ownerchannel: the target already holds the feature")
	// ErrPinUnclear says the pin may or may not have been made.
	ErrPinUnclear = errors.New("ownerchannel: the pin may or may not have been made")
)

// MergeInput is what a [Merger] builds a merge from. Every value was made
// or checked by the host: the feature name and the request id passed the
// grammars of package record, and the key is derived from a digest, never
// from the lead's request id.
type MergeInput struct {
	Project   string
	Feature   string
	Target    string
	TargetTip string
	// FeatureTip is the tip the lead's request named, which the host
	// resolved again and found the same.
	FeatureTip string
	RequestID  string
	// Collected is the time the daemon collected the request. It is the
	// merge commit's date; the clock is not read again.
	Collected time.Time
	// Key names the pin: refs/ataniq/pending/<key>.
	Key string
}

// BuiltMerge is the merge commit a [Merger] wrote.
type BuiltMerge struct {
	Commit  string
	Parents [2]string
}

// Verifier runs the project's whole suite on a fresh checkout of a merge.
// The service calls it for one request at a time.
type Verifier interface {
	Verify(ctx context.Context, in VerifyInput) (Verification, error)
}

// VerifyInput names what to verify.
type VerifyInput struct {
	Project   string
	RequestID string
	// Merge is the commit to check out, and Base the target's tip it was
	// built on.
	Merge string
	Base  string
}

// The words of a verification's detail, a closed list as in the design.
const (
	VerifyFailed      = "failed"
	VerifyStopped     = "stopped"
	VerifyOutputCut   = "output-cut"
	VerifyCouldNotRun = "could-not-run"
)

// Verification is the result of a verification run.
type Verification struct {
	// Clean says the run passed, was not stopped and its output was not
	// cut. Anything else is not a pass.
	Clean bool
	// Detail says why it was not clean: one of the words above. Any other
	// word is taken as could-not-run.
	Detail string
	// Evidence is the record of the run, bound into the pending record. It
	// is used only when Clean.
	Evidence record.Evidence
}

// Intake is the host's reading of the mirror and its one writer of a
// ref. The real one is the intake program behind an adapter.
type Intake interface {
	// Resolve refreshes the lead's feature branch and resolves it and the
	// upstream target once, to object ids.
	Resolve(ctx context.Context, in ResolveInput) (Resolved, error)
	// Review inspects Base to Merge and returns the summary of the
	// review: the changed paths (as many as the record holds), the flags,
	// the review digest and the digest of the rule set.
	Review(ctx context.Context, in ReviewInput) (record.Summary, error)
	// TargetTip returns the present tip of a target ref.
	TargetTip(ctx context.Context, project, ref string) (string, error)
	// Paths returns the full path list of a review, with the review
	// digest it was made under.
	Paths(ctx context.Context, ref ReviewRef) (ReviewPaths, error)
	// Patch returns the patch of a review as raw lines, with the review
	// digest it was made under. The lines are not escaped.
	Patch(ctx context.Context, ref ReviewRef) (ReviewPatch, error)
	// Fresh inspects again, just before a use, and returns what the door
	// compares with the binding.
	Fresh(ctx context.Context, in FreshInput) (door.Fresh, error)
	// Apply moves the refs of the effects in one transaction, each from
	// its old value. It never applies an effect twice: an effect whose ref
	// is not at its old value is not applied.
	Apply(ctx context.Context, in ApplyInput) (door.Outcome, error)
	// Mirror says what the mirror shows for the effects: applied when
	// every ref is at its new value, not-applied when every ref is at its
	// old value, and anything else is unknown. The service calls it at a
	// restart, for an approval whose use was left open, and never applies
	// again.
	Mirror(ctx context.Context, in ApplyInput) (door.Outcome, error)
}

// ResolveInput names the request whose branch and target are resolved.
type ResolveInput struct {
	Project string
	Request record.Request
}

// Resolved is what Resolve found.
type Resolved struct {
	// TargetRef is the full name of the target branch, TargetTip its tip
	// (D) and FeatureTip the feature's tip (F).
	TargetRef  string
	TargetTip  string
	FeatureTip string
}

// ReviewInput names the two commits of a review.
type ReviewInput struct {
	Project string
	Base    string
	Merge   string
}

// ReviewRef names a review: Base to Merge, as the summary of a pending
// record has them.
type ReviewRef struct {
	Project string
	Base    string
	Merge   string
}

// ReviewPaths is the full path list of a review.
type ReviewPaths struct {
	ReviewDigest string
	Paths        []record.PathChange
}

// ReviewPatch is the patch of a review.
type ReviewPatch struct {
	ReviewDigest string
	Lines        []string
}

// FreshInput is what the intake reads again for one use.
type FreshInput struct {
	Project string
	Pending record.Pending
}

// ApplyInput is a list of ref updates.
type ApplyInput struct {
	Project string
	Effects []record.Effect
}

// Mailbox puts a reply in the lead's mailbox. A reply may be sent more
// than once for one request and state (after a restart, say), so the
// mailbox keeps the first and drops the rest.
type Mailbox interface {
	Reply(ctx context.Context, r Reply) error
}

// Reply says how a request ended. A request refused before it had a
// pending record has a Code and no State; every other has the State it
// ended in.
type Reply struct {
	Project   string
	RequestID string
	// PendingID is the pending record's id, empty for a refused request.
	PendingID string
	State     record.State
	Code      record.Code
	// Line is one host-written line.
	Line string
}

// RequestEnder ends a request the host refused before the door planned it.
// It is not one of the door's seven verbs: such a request still stands at
// the door, and counts against the seat's caps, until something ends it. A
// point for the joint contract. The stand-in's Withdraw does this.
type RequestEnder interface {
	EndRequest(ctx context.Context, requestID string) error
}

// Collected is a request the daemon read from the spool.
type Collected struct {
	// Project is the project whose spool it came from.
	Project string
	// Request is the request's bytes, as the lead wrote them.
	Request []byte
	// Note is the lead's note, which travels beside the request.
	Note record.Text
	// At is when the daemon collected the request.
	At time.Time
}

// Config is what a [Service] is made from.
type Config struct {
	// Journal is the opened store. The service owns it from here on and
	// closes it with itself.
	Journal Journal
	// Reopen opens the same store again. The service calls it when the
	// store answers [ownerjournal.ErrUncertain], once for each failure:
	// when the reopened store is uncertain again while the service takes up
	// what the journal says was under way, the service stops with
	// [ErrStopped]. Without Reopen, such an answer stops the service.
	Reopen func() (Journal, error)
	// Handles is optional. It is how the writes made through the handles
	// of a decision and of a delivery reach the store; see [HandleWrites].
	// The default writes through the handles themselves.
	Handles HandleWrites

	Door     door.Door
	Merger   Merger
	Intake   Intake
	Verifier Verifier
	Mailbox  Mailbox
	// Ender is optional; see [RequestEnder].
	Ender RequestEnder

	// Now is the clock. It is required.
	Now func() time.Time
	// PresenceWindow is how long after an action the owner counts as
	// present. The zero value is the presence package's default.
	PresenceWindow time.Duration
	// ApprovalLife is how long an approval lasts from its decision. The
	// zero value is 30 minutes, as the design has it.
	ApprovalLife time.Duration

	// Keys is the list of the owner's deciding keys, the credentials of the
	// phones he paired; the service owns it from here and closes it with
	// itself. It is optional: without it every pairing and key operation is
	// refused with [ErrKeysUnavailable], and the rest of the service is as
	// it was. A daemon whose list could not be opened gives none, so a
	// damaged list refuses the operations and is never taken for an empty
	// one.
	Keys KeyStore
	// PairRand is the random source of the pairing codes; the default is
	// the system's. A test gives its own.
	PairRand io.Reader
	// ChallengeRand is the random source of the seat gate's challenge ids
	// and nonces; the default is the system's (crypto/rand). A test gives
	// its own.
	ChallengeRand io.Reader
	// SeatLog, when set, is called with one host-written line for each
	// outstanding challenge of the seat gate that expires or is dropped,
	// and for each refusal of the seat's calls the gate cannot see the
	// cause of (which check of an assertion failed). A line names the gate
	// session and never holds anything the seat sent. It is telemetry, not
	// the journal: nothing of it is kept.
	SeatLog func(line string)

	// Notice, when set, is called with a host-written line for each thing
	// the operator should see: an alert, a failed reply.
	Notice func(line string)
	// Fault is for tests. When set it is called with the name of a point
	// between two steps; a non-nil answer stops the service there, as a
	// crash would, and every later call is refused with [ErrStopped].
	Fault func(point string) error
}
