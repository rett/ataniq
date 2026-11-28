package leadregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"ataniq/internal/leadrole"
)

// Bounds. An input over a bound is refused before anything is written.
const (
	// MaxRoles is the most roles the registry holds.
	MaxRoles = 256
	// MaxFieldBytes bounds every profile field and every observed value.
	MaxFieldBytes = 128
	// MaxReasonCodeBytes bounds a reason code.
	MaxReasonCodeBytes = 64
	// MaxReasonDetailBytes bounds a reason detail text.
	MaxReasonDetailBytes = 512
	// MaxProofBytes bounds a proof text.
	MaxProofBytes = 1024
	// MaxInvocationIDBytes bounds the unit invocation id.
	MaxInvocationIDBytes = 128
	// MaxControlGroupPathBytes bounds the control-group path.
	MaxControlGroupPathBytes = 1024
	// MaxControlGroupIdentityBytes bounds the control-group identity.
	MaxControlGroupIdentityBytes = 128
	// MaxBootIdentityBytes bounds the boot identity.
	MaxBootIdentityBytes = 128
	// MaxLineBytes bounds one journal line, newline excluded.
	MaxLineBytes = 16 << 10
)

// Named errors. Test with errors.Is.
var (
	// ErrInvalid is wrapped by every refusal of a malformed or over-bound input.
	ErrInvalid = errors.New("leadregistry: invalid input")
	// ErrNoRole means the role is not defined.
	ErrNoRole = errors.New("leadregistry: role not defined")
	// ErrTooManyRoles means defining the role would exceed MaxRoles.
	ErrTooManyRoles = errors.New("leadregistry: too many roles")
	// ErrTooLarge means the journal line would exceed MaxLineBytes.
	ErrTooLarge = errors.New("leadregistry: journal line too large")
	// ErrStaleEpoch means the call named an epoch other than the role's current one.
	ErrStaleEpoch = errors.New("leadregistry: stale epoch")
	// ErrStaleState means the shown state differs from the caller's expectation.
	ErrStaleState = errors.New("leadregistry: stale shown state")
	// ErrEpochExhausted means the epoch counter cannot grow.
	ErrEpochExhausted = errors.New("leadregistry: epoch exhausted")
	// ErrLaunchRefused is matched by every refusal of BeginLaunch.
	ErrLaunchRefused = errors.New("leadregistry: launch refused")
	// ErrLaunchState is a launch refusal: the shown state is not desired, stopped or ended.
	ErrLaunchState error = refusal("shown state does not admit a launch")
	// ErrLaunchManager is a launch refusal: the role is not run by ataniq.
	ErrLaunchManager error = refusal("role is not run by ataniq")
	// ErrLaunchPending is a launch refusal: a launch intent is still pending.
	ErrLaunchPending error = refusal("a launch intent is pending")
	// ErrLaunchNoParent is a launch refusal: the parent lead of a coordinator
	// is not defined.
	ErrLaunchNoParent error = refusal("the parent lead of the coordinator is not defined")
	// ErrLaunchParentManager is a launch refusal: the parent lead of a
	// coordinator is not run by ataniq.
	ErrLaunchParentManager error = refusal("the parent lead of the coordinator is not run by ataniq")
	// ErrLaunchParentState is a launch refusal: the parent lead of a
	// coordinator is not shown starting, ready or degraded, or its own launch
	// failed or was abandoned.
	ErrLaunchParentState error = refusal("the parent lead of the coordinator is not shown starting, ready or degraded, or its own launch failed or was abandoned")
	// ErrLaunchCoordinator is a launch refusal: a coordinator of the lead,
	// recorded under an earlier epoch of the lead, is not stopped, ended or
	// desired (never launched), so it is not proven stopped.
	ErrLaunchCoordinator error = refusal("a coordinator of the lead recorded under an earlier epoch is not proven stopped")
	// ErrLaunchProfile is a launch refusal: the requested profile was written
	// by another build with members this build does not know, so this build
	// cannot launch what was requested. Defining the role again with a profile
	// of this build lifts it. A record that holds such a profile, and a
	// launch that a newer build journaled for it, still replay.
	// It also refuses a profile that names a pack (Requested.Pack), which this
	// build does not launch, and one whose define record repeats a member
	// (exactly or as a case alias), which no build writes.
	ErrLaunchProfile error = refusal("the requested profile has members this build does not know or does not launch; define the role again")
	// ErrIntentMismatch means the intent id is not the role's current intent.
	ErrIntentMismatch = errors.New("leadregistry: not the current launch intent")
	// ErrIntentResolved means the launch intent already has an outcome.
	ErrIntentResolved = errors.New("leadregistry: launch intent already resolved")
	// ErrProofRequired means stopped and ended need a non-empty proof text.
	ErrProofRequired = errors.New("leadregistry: proof required")
	// ErrStateTransition means SetState or ResolveUnknown refused the transition.
	ErrStateTransition = errors.New("leadregistry: state change refused")
	// ErrOutcomePending is a state refusal: stopped, ended and an owner
	// decision are refused while the launch intent is pending. The caller
	// records the outcome first.
	ErrOutcomePending error = transition("stopped or ended while the launch intent is pending; record the outcome first")
	// ErrDecisionRequired means an owner decision needs a non-empty decision text.
	ErrDecisionRequired = errors.New("leadregistry: owner decision text required")
	// ErrNoRecovery means AcknowledgeRecovery was called with no
	// unacknowledged cut of the journal.
	ErrNoRecovery = errors.New("leadregistry: no recovery to acknowledge")
	// ErrCorrupt is matched by the error Open returns for a damaged journal.
	ErrCorrupt = errors.New("leadregistry: corrupt journal")
	// ErrLocked means another holder has the registry file locked.
	ErrLocked = errors.New("leadregistry: registry is locked by another writer")
	// ErrInsecure means the folder or file is a symlink, not owned by the
	// caller, not a regular file or folder, or has wider permission bits than
	// allowed.
	ErrInsecure = errors.New("leadregistry: insecure registry path")
	// ErrClosed means the registry is closed.
	ErrClosed = errors.New("leadregistry: closed")
	// ErrBroken means an earlier write or sync failed; reopen the registry.
	// The record of the failed call may or may not be on disk: a write that
	// completed and then failed to sync stays in the file, and a reopen in
	// the same boot reads it back. So the error of that call is not "nothing
	// happened"; the caller must reopen and read the state, and an epoch the
	// failed call took is consumed.
	ErrBroken = errors.New("leadregistry: earlier write failed, the record may or may not be on disk, reopen")
)

// refusal is a launch refusal that also matches ErrLaunchRefused.
type refusal string

// Error returns the refusal text.
func (r refusal) Error() string { return "leadregistry: launch refused: " + string(r) }

// Is makes errors.Is(err, ErrLaunchRefused) true.
func (refusal) Is(target error) bool { return target == ErrLaunchRefused }

// transition is a state refusal that also matches ErrStateTransition.
type transition string

// Error returns the refusal text.
func (t transition) Error() string { return "leadregistry: state change refused: " + string(t) }

// Is makes errors.Is(err, ErrStateTransition) true.
func (transition) Is(target error) bool { return target == ErrStateTransition }

func refuseTransition(from, to State, why string) error {
	return transition(fmt.Sprintf("%s to %s: %s", from, to, why))
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Manager says which manager runs a role.
type Manager string

// The managers.
const (
	// ManagerAtaniq is the lead daemon.
	ManagerAtaniq Manager = "ataniq"
	// ManagerTmux is the older tmux-manager.
	ManagerTmux Manager = "tmux-manager"
)

func (m Manager) validate() error {
	if m != ManagerAtaniq && m != ManagerTmux {
		return invalid("manager %q", string(m))
	}
	return nil
}

// Provider names the runtime provider of a profile.
type Provider string

// The providers.
const (
	// ProviderClaude is Claude Code.
	ProviderClaude Provider = "claude"
	// ProviderCodex is Codex.
	ProviderCodex Provider = "codex"
)

// Desired is the state the owner wants.
type Desired string

// The desired states.
const (
	// DesiredRunning wants the role running.
	DesiredRunning Desired = "running"
	// DesiredStopped wants the role stopped.
	DesiredStopped Desired = "stopped"
)

func (d Desired) validate() error {
	if d != DesiredRunning && d != DesiredStopped {
		return invalid("desired state %q", string(d))
	}
	return nil
}

// Expectation is what a caller observed of a role's shown value before it
// decided on a write in the same epoch. The registry compares it under the
// write lock that commits the record, so a decision made from an old
// observation cannot overwrite a newer one. Name it in every call: use
// ExpectShown with the entry the decision was made from, or NoExpectation
// when no state the registry can change is part of the decision. The zero
// value is NoExpectation.
//
// The whole Shown value is compared, not the state alone: the state, the
// reason code and detail, the time it was set, the evidence kind and the
// proof. A caller that publishes a reason for the state it observed (the
// health poll's degraded with its reason) must see a same-state rewrite
// with another reason as a newer decision, and the time set stands in for a
// per-role revision without a new journal field: every write of the shown
// value stamps it.
type Expectation struct {
	shown Shown
	set   bool
}

// ExpectShown expects the shown value of the current epoch to be shown.
func ExpectShown(shown Shown) Expectation { return Expectation{shown: shown, set: true} }

// NoExpectation states that the write decides from nothing the registry can
// have changed since the caller looked, and compares nothing.
func NoExpectation() Expectation { return Expectation{} }

func (x Expectation) validate() error {
	if !x.set {
		return nil
	}
	return x.shown.State.validate()
}

func (s Shown) equal(o Shown) bool {
	return s.State == o.State && s.Reason == o.Reason && s.At.Equal(o.At) &&
		s.Evidence == o.Evidence && s.Proof == o.Proof
}

// StaleStateError reports the same-epoch mismatch of the shown value that
// refused a write.
type StaleStateError struct {
	Expected Shown
	Current  Shown
}

func (e *StaleStateError) Error() string {
	return fmt.Sprintf("%v: expected %s (%s), current %s (%s)", ErrStaleState,
		e.Expected.State, e.Expected.Reason.Code, e.Current.State, e.Current.Reason.Code)
}

func (e *StaleStateError) Unwrap() error { return ErrStaleState }

// State is the shown state of a role.
type State string

// The shown states.
const (
	// StateDesired means a launch is wanted and has not started.
	StateDesired State = "desired"
	// StateStarting means a launch intent was journaled.
	StateStarting State = "starting"
	// StateReady means the role is ready.
	StateReady State = "ready"
	// StateDegraded means the role runs but is impaired.
	StateDegraded State = "degraded"
	// StateStalled means a coordinator that was starting, ready or degraded
	// has stopped making progress. It is a state of coordinators only; no
	// launch is admitted from it, and it is left through a stop proven with
	// evidence (stopped or ended), unknown, or back to a live state.
	StateStalled State = "stalled"
	// StateStopping means a stop is under way.
	StateStopping State = "stopping"
	// StateStopped means the role is stopped, with proof.
	StateStopped State = "stopped"
	// StateEnded means the role ended on its own, with proof.
	StateEnded State = "ended"
	// StateUnknown means the state cannot be established.
	StateUnknown State = "unknown"
)

func (s State) validate() error {
	switch s {
	case StateDesired, StateStarting, StateReady, StateDegraded, StateStalled,
		StateStopping, StateStopped, StateEnded, StateUnknown:
		return nil
	}
	return invalid("shown state %q", string(s))
}

// Evidence is the record kind of the evidence behind a shown state.
type Evidence string

// The evidence kinds.
const (
	// EvidenceObservation is something ataniq itself observed.
	EvidenceObservation Evidence = "ataniq-observation"
	// EvidenceProvider is a report from the provider runtime.
	EvidenceProvider Evidence = "provider-report"
	// EvidenceOwner is a decision of the owner.
	EvidenceOwner Evidence = "owner-decision"
)

func (e Evidence) validate() error {
	switch e {
	case EvidenceObservation, EvidenceProvider, EvidenceOwner:
		return nil
	}
	return invalid("evidence kind %q", string(e))
}

// Outcome is the outcome of a launch intent.
type Outcome string

// The launch outcomes.
const (
	// OutcomePending means the intent was journaled and nothing else is known.
	OutcomePending Outcome = "pending"
	// OutcomeLaunched means the unit was started.
	OutcomeLaunched Outcome = "launched"
	// OutcomeAdopted means an existing unit was adopted.
	OutcomeAdopted Outcome = "adopted"
	// OutcomeFailed means the start failed.
	OutcomeFailed Outcome = "failed"
	// OutcomeAbandoned means the launch was given up.
	OutcomeAbandoned Outcome = "abandoned"
)

func (o Outcome) validate() error {
	switch o {
	case OutcomePending, OutcomeLaunched, OutcomeAdopted, OutcomeFailed, OutcomeAbandoned:
		return nil
	}
	return invalid("outcome %q", string(o))
}

// validText checks a bounded text: valid UTF-8, no control character.
func validText(what, s string, max int, required bool) error {
	if s == "" {
		if required {
			return invalid("%s is empty", what)
		}
		return nil
	}
	if len(s) > max {
		return invalid("%s is longer than %d bytes", what, max)
	}
	if !utf8.ValidString(s) {
		return invalid("%s is not valid UTF-8", what)
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return invalid("%s has a control character", what)
		}
	}
	return nil
}

// Requested is the launch profile as REQUESTED by the owner. It is a distinct
// type from Observed so the two cannot be mixed up; nothing converts one into
// the other.
type Requested struct {
	// Provider is claude or codex.
	Provider Provider `json:"provider"`
	// Version is the pinned provider version.
	Version string `json:"version"`
	// Model is the requested model.
	Model string `json:"model"`
	// Effort is the requested effort.
	Effort string `json:"effort"`
	// PermissionMode is the requested permission mode.
	PermissionMode string `json:"permission_mode"`
	// AccessMode is the requested access mode.
	AccessMode string `json:"access_mode"`
	// Pack is the provider pack the profile asks for. It is reserved: no
	// build sets it yet (packs are not wired), Define refuses a profile that
	// sets it, and BeginLaunch refuses a role whose recorded profile sets it,
	// with ErrLaunchProfile. The zero PackRef is left out of the encoding
	// (omitzero), so the encoding and the digest of every profile that does
	// not set it are what they were before it existed (testdata/v1-base).
	Pack PackRef `json:"pack,omitzero"`
}

// PackRef refers to one provider pack: its name and the SHA-256 of its
// manifest. The zero PackRef refers to none. A PackRef that is set has a name
// of 1 to MaxFieldBytes bytes of text and a manifest digest of 64 lower-case
// hex characters.
type PackRef struct {
	// Name is the pack's name.
	Name string `json:"name"`
	// ManifestDigest is the hex SHA-256 of the pack's manifest.
	ManifestDigest string `json:"manifest_digest"`
}

// IsZero reports whether p refers to no pack.
func (p PackRef) IsZero() bool { return p == PackRef{} }

func (p PackRef) validate() error {
	if p.IsZero() {
		return nil
	}
	if err := validText("pack name", p.Name, MaxFieldBytes, true); err != nil {
		return err
	}
	if len(p.ManifestDigest) != hashHexLen || !isLowerHex([]byte(p.ManifestDigest)) {
		return invalid("pack manifest digest is not a hex SHA-256")
	}
	return nil
}

// packSessionRef is RESERVED and unused: the shape of the reference to a pack
// session that the federation review asks for (ID-11), kept here so that the
// binding record of the provider packs starts from it. Nothing builds,
// stores, encodes or compares one; it has no wire form, and its members and
// their names may still change when the binding record is designed. A test
// keeps the member list as reviewed.
//
// The reference is qualified by instance: the authority and its store
// incarnation come first, and native ids of a provider stay at home (they are
// not members).
type packSessionRef struct {
	// authority is the fingerprint of the instance that holds the session.
	authority string
	// storeIncarnation is that instance's store incarnation.
	storeIncarnation string
	// project and homeGeneration name the project at its home instance.
	project        string
	homeGeneration uint64
	// role is the role the session runs, and conversation its conversation.
	role         string
	conversation string
	// roleEpoch is the role's launch epoch, named apart from a seat epoch.
	roleEpoch uint64
	// bindingGeneration counts the bindings of the pack to the role.
	bindingGeneration uint64
	// pack is the pack's name with its manifest digest, and signerGeneration
	// the generation of the key that signed the manifest.
	pack             PackRef
	signerGeneration uint64
	// profileDigest is the digest of the launch profile.
	profileDigest string
	// transport is how the session is carried (terminal, headless).
	transport string
	// custody says who holds the session.
	custody string
}

// Defaults of the launch profile of a coordinator, used by Define for the
// runtime and the model when the caller leaves them empty.
const (
	// DefaultCoordinatorProvider is the runtime of a coordinator by default.
	DefaultCoordinatorProvider = ProviderClaude
	// DefaultCoordinatorModel is the model of a coordinator by default.
	DefaultCoordinatorModel = "fable"
)

// withCoordinatorDefaults returns r with an empty provider and an empty model
// replaced by the defaults of a coordinator.
func (r Requested) withCoordinatorDefaults() Requested {
	if r.Provider == "" {
		r.Provider = DefaultCoordinatorProvider
	}
	if r.Model == "" {
		r.Model = DefaultCoordinatorModel
	}
	return r
}

// validateFor checks the profile for the kind of role: a lead needs every
// field; a coordinator needs the provider and the model, and the other fields
// are optional.
func (r Requested) validateFor(role leadrole.Role) error {
	if role.Kind() != leadrole.KindCoordinator {
		return r.validate()
	}
	if r.Provider != ProviderClaude && r.Provider != ProviderCodex {
		return invalid("provider %q", string(r.Provider))
	}
	if err := r.Pack.validate(); err != nil {
		return err
	}
	for _, f := range []struct {
		name, v  string
		required bool
	}{
		{"version", r.Version, false}, {"model", r.Model, true}, {"effort", r.Effort, false},
		{"permission mode", r.PermissionMode, false}, {"access mode", r.AccessMode, false},
	} {
		if err := validText("requested "+f.name, f.v, MaxFieldBytes, f.required); err != nil {
			return err
		}
	}
	return nil
}

func (r Requested) validate() error {
	if r.Provider != ProviderClaude && r.Provider != ProviderCodex {
		return invalid("provider %q", string(r.Provider))
	}
	if err := r.Pack.validate(); err != nil {
		return err
	}
	for _, f := range []struct{ name, v string }{
		{"version", r.Version}, {"model", r.Model}, {"effort", r.Effort},
		{"permission mode", r.PermissionMode}, {"access mode", r.AccessMode},
	} {
		if err := validText("requested "+f.name, f.v, MaxFieldBytes, true); err != nil {
			return err
		}
	}
	return nil
}

// Digest returns the hex SHA-256 of the canonical encoding of the requested
// profile (json.Marshal), as recorded in a launch intent. A member that is
// empty and marked omitempty or omitzero is not in the encoding, so a member
// added that way leaves the digest of every profile that does not set it as
// it was. The registry takes the digest of a defined role from the bytes its
// define record stores, which for a profile this build wrote is this digest.
func (r Requested) Digest() string {
	b, err := json.Marshal(r)
	if err != nil { // plain strings only; cannot happen
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Field is one observed profile value, or an explicit statement that it was
// not observed. The zero Field is "not observed".
type Field struct {
	// Observed is true when Value was observed.
	Observed bool `json:"observed"`
	// Value is the observed value; empty unless Observed.
	Value string `json:"value,omitempty"`
}

// Seen returns an observed Field holding v.
func Seen(v string) Field { return Field{Observed: true, Value: v} }

func (f Field) validate(name string) error {
	if !f.Observed {
		if f.Value != "" {
			return invalid("observed %s has a value but is not marked observed", name)
		}
		return nil
	}
	return validText("observed "+name, f.Value, MaxFieldBytes, true)
}

// Observed is the launch profile as OBSERVED: each field is an observed value
// or explicitly not observed. A requested value is never copied into it.
type Observed struct {
	// Provider is the observed provider.
	Provider Field `json:"provider"`
	// Version is the observed version.
	Version Field `json:"version"`
	// Model is the observed model.
	Model Field `json:"model"`
	// Effort is the observed effort.
	Effort Field `json:"effort"`
	// PermissionMode is the observed permission mode.
	PermissionMode Field `json:"permission_mode"`
	// AccessMode is the observed access mode.
	AccessMode Field `json:"access_mode"`
}

func (o Observed) fields() []struct {
	name string
	f    *Field
} {
	return []struct {
		name string
		f    *Field
	}{
		{"provider", &o.Provider}, {"version", &o.Version}, {"model", &o.Model},
		{"effort", &o.Effort}, {"permission mode", &o.PermissionMode}, {"access mode", &o.AccessMode},
	}
}

func (o Observed) validate() error {
	for _, f := range o.fields() {
		if err := f.f.validate(f.name); err != nil {
			return err
		}
	}
	return nil
}

func (o Observed) any() bool {
	for _, f := range o.fields() {
		if f.f.Observed {
			return true
		}
	}
	return false
}

// merge returns o with every observed field of patch written over it.
func (o Observed) merge(patch Observed) Observed {
	pick := func(cur, p Field) Field {
		if p.Observed {
			return p
		}
		return cur
	}
	return Observed{
		Provider:       pick(o.Provider, patch.Provider),
		Version:        pick(o.Version, patch.Version),
		Model:          pick(o.Model, patch.Model),
		Effort:         pick(o.Effort, patch.Effort),
		PermissionMode: pick(o.PermissionMode, patch.PermissionMode),
		AccessMode:     pick(o.AccessMode, patch.AccessMode),
	}
}

// Reason is a short machine code with a bounded detail text.
type Reason struct {
	// Code is lower-case words joined by hyphens, at most 64 bytes.
	Code string `json:"code"`
	// Detail is free text, at most 512 bytes, without control characters.
	Detail string `json:"detail,omitempty"`
}

func (r Reason) validate() error {
	if r.Code == "" || len(r.Code) > MaxReasonCodeBytes {
		return invalid("reason code must be 1 to %d bytes", MaxReasonCodeBytes)
	}
	prevHyphen := true // forbids a leading hyphen
	for i := 0; i < len(r.Code); i++ {
		c := r.Code[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			prevHyphen = false
		case c == '-' && !prevHyphen:
			prevHyphen = true
		default:
			return invalid("reason code %q is not lower-case words joined by hyphens", r.Code)
		}
	}
	if prevHyphen {
		return invalid("reason code %q ends with a hyphen", r.Code)
	}
	return validText("reason detail", r.Detail, MaxReasonDetailBytes, false)
}

// Shown is the state a role is shown in.
type Shown struct {
	// State is the shown state.
	State State
	// Reason says why.
	Reason Reason
	// At is when the state was set (UTC).
	At time.Time
	// Evidence is the record kind of the evidence behind the state.
	Evidence Evidence
	// Proof is the proof text given with the state; required for stopped and
	// ended, optional otherwise.
	Proof string
}

// UnitEvidence is the unit evidence of one epoch, as opaque bounded strings
// the caller supplies. The registry does not interpret them.
type UnitEvidence struct {
	// InvocationID is the unit invocation id.
	InvocationID string `json:"invocation_id,omitempty"`
	// ControlGroupPath is the control-group path.
	ControlGroupPath string `json:"control_group_path,omitempty"`
	// ControlGroupID is the control-group identity.
	ControlGroupID string `json:"control_group_id,omitempty"`
	// BootID is the boot identity.
	BootID string `json:"boot_id,omitempty"`
}

func (u UnitEvidence) validate() error {
	for _, f := range []struct {
		name, v string
		max     int
	}{
		{"invocation id", u.InvocationID, MaxInvocationIDBytes},
		{"control-group path", u.ControlGroupPath, MaxControlGroupPathBytes},
		{"control-group identity", u.ControlGroupID, MaxControlGroupIdentityBytes},
		{"boot identity", u.BootID, MaxBootIdentityBytes},
	} {
		if err := validText(f.name, f.v, f.max, false); err != nil {
			return err
		}
	}
	return nil
}

// Intent is a launch intent: journaled before the caller touches systemd.
type Intent struct {
	// ID is the intent id: 128 random bits, 32 hex characters.
	ID string
	// Epoch is the epoch this launch takes.
	Epoch uint64
	// UnitName is the systemd unit name of the role.
	UnitName string
	// ProfileDigest is Requested.Digest of the profile at the time.
	ProfileDigest string
	// At is when the intent was journaled (UTC).
	At time.Time
	// Outcome is pending until RecordOutcome resolves it.
	Outcome Outcome
	// OutcomeAt is when the outcome was recorded; zero while pending.
	OutcomeAt time.Time
	// ParentEpoch is the epoch of the parent lead at launch, for the intent
	// of a coordinator; 0 for every other role.
	ParentEpoch uint64
	// Identity is the identity of the coordinator instance this launch
	// starts, "<project>-lead@e<parent epoch>:coord:<feature>:<n>" (see
	// leadrole.Role.Identity); empty for every other role. It is an
	// identifier only.
	Identity string
	// BootIdentity is the boot identity the caller recorded with the intent
	// (BeginLaunchWith); empty when it recorded none. The registry stores it
	// as given and does not compare it with anything.
	BootIdentity string
}

// Entry is everything the registry holds for one role. Entries returned by
// the registry are deep copies.
type Entry struct {
	// Role is the role; its project and kind come from it.
	Role leadrole.Role
	// Manager is the manager that runs the role.
	Manager Manager
	// Requested is the launch profile as requested.
	Requested Requested
	// Observed is the launch profile as observed in the current epoch.
	Observed Observed
	// Epoch is the current epoch: 0 means never launched. For a coordinator
	// it is the instance number n.
	Epoch uint64
	// Desired is the desired state.
	Desired Desired
	// Shown is the shown state.
	Shown Shown
	// Intent is the current launch intent, nil before the first launch.
	Intent *Intent
	// Unit is the unit evidence of the current epoch.
	Unit UnitEvidence

	// profileDigest is the digest of the requested profile as its define
	// record stores it (requestedDigest): the digest a launch intent of the
	// role carries. foreignProfile is true when that record's profile is not
	// what this build writes for Requested, which is what this build knows of
	// it (a newer build wrote a member this one does not know): such a role
	// replays, and BeginLaunch refuses it with ErrLaunchProfile.
	profileDigest  string
	foreignProfile bool
}

// ProfileLaunchable reports whether this build launches the role's stored
// profile: nil, or ErrLaunchProfile for a profile that is not what this build
// writes for it (a member it does not know, a repeated member) or that names a
// pack. It is the profile rule of BeginLaunch alone, which checks it again
// with every other rule; a caller checks it before it changes anything else
// for a start, so that a start that BeginLaunch would refuse for its profile
// changes nothing.
func (e Entry) ProfileLaunchable() error {
	if e.foreignProfile || !e.Requested.Pack.IsZero() {
		return ErrLaunchProfile
	}
	return nil
}

// pending reports whether the current launch intent has no outcome yet.
func (e Entry) pending() bool {
	return e.Intent != nil && e.Intent.Outcome == OutcomePending
}

func (e Entry) clone() Entry {
	if e.Intent != nil {
		in := *e.Intent
		e.Intent = &in
	}
	return e
}
