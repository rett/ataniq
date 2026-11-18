package leadlifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// Defaults of the options that have one.
const (
	// DefaultPollInterval is how often Stop looks at the control group.
	DefaultPollInterval = 100 * time.Millisecond
	// DefaultProofBound is how long Stop waits for the proof after the
	// signals.
	DefaultProofBound = 5 * time.Second
	// DefaultLateStartWindow is how long after a launch intent a start whose
	// reply was lost is still thought able to land.
	DefaultLateStartWindow = 30 * time.Second
	// DefaultLateStartMargin is what is added to the late-start window: the
	// time between the registry stamping a launch intent and the start
	// request being sent, and a step of the wall clock.
	DefaultLateStartMargin = 10 * time.Second

	// TermWait is how long Stop lets a control group run after SIGTERM before
	// it sends SIGKILL.
	TermWait = 2 * time.Second
)

// Named errors. Test with errors.Is.
var (
	// ErrNotManaged means the role is not run by ataniq (it belongs to the
	// tmux-manager). An undefined role gives leadregistry.ErrNoRole.
	ErrNotManaged = errors.New("leadlifecycle: role is not run by ataniq")
	// ErrPreflight means the preflight refused the start; it wraps the cause.
	ErrPreflight = errors.New("leadlifecycle: preflight refused the start")
	// ErrUnitLoaded means a unit with the role's name is loaded, in any state,
	// so nothing is started.
	ErrUnitLoaded = errors.New("leadlifecycle: a unit with the role's name is loaded")
	// ErrForeignUnit means the loaded unit is not the one the registry
	// records for the role (not ours, or ours with another epoch or intent).
	// It is never signalled.
	ErrForeignUnit = errors.New("leadlifecycle: the loaded unit is not the role's recorded unit")
	// ErrNoProof means the previous unit is not proven gone, so nothing was
	// started. It wraps the cause: a manager error, or a control group that is
	// not proven empty.
	ErrNoProof = errors.New("leadlifecycle: the previous unit is not proven gone")
	// ErrStartFailed means nothing was started and nothing will be for the
	// launch intent; it wraps the cause. The registry holds the outcome.
	ErrStartFailed = errors.New("leadlifecycle: start failed, nothing was started")
	// ErrLaunchUncertain means the outcome of the start is not known: the role
	// is shown unknown and Reconcile resolves it.
	ErrLaunchUncertain = errors.New("leadlifecycle: start outcome unknown")
	// ErrIntentPending means Stop could not yet settle the role's pending
	// launch intent, so it signalled nothing.
	ErrIntentPending = errors.New("leadlifecycle: the launch intent is not settled yet")
	// ErrParentNotLive means a coordinator is not started because its parent
	// lead is not defined, not run by ataniq, not shown starting, ready or
	// degraded, or its own launch failed or was abandoned. It is wrapped together with the registry's refusal
	// (leadregistry.ErrLaunchNoParent, ErrLaunchParentManager or
	// ErrLaunchParentState), and nothing is changed. ClearStalled returns it
	// too, for a coordinator whose parent has ended (and then shows the
	// coordinator unknown).
	ErrParentNotLive = errors.New("leadlifecycle: the parent lead of the coordinator is not live")
	// ErrCoordinatorNotStopped means a lead is not started because a
	// coordinator of it is not proven stopped. It is wrapped together with
	// leadregistry.ErrLaunchCoordinator, and nothing is changed.
	ErrCoordinatorNotStopped = errors.New("leadlifecycle: a coordinator of the lead is not proven stopped")
	// ErrStopUnproven means the stop did not end with the proof; the role is
	// shown unknown.
	ErrStopUnproven = errors.New("leadlifecycle: stop is not proven")
)

// Preflight is the check that must pass before a role is started: the
// qualification record is current, no codex daemon runs for the lead, a login
// is present. This package only defines it; the constructor requires one.
type Preflight interface {
	// Check returns nil when the role may be started, and the reason when it
	// may not. entry is the registry's record of the role.
	Check(ctx context.Context, role leadrole.Role, entry leadregistry.Entry) error
}

// HolderCommand builds the command of the holder. The holder binary and its
// flags belong to another feature; this package only defines the interface.
type HolderCommand interface {
	// Build returns the argument vector (an absolute executable first), the
	// absolute working directory and the whole environment of the holder for
	// one launch. entry is the record after the intent was journaled.
	Build(role leadrole.Role, entry leadregistry.Entry, intent leadregistry.Intent) (argv []string, workdir string, env []string, err error)
}

// Quiesce asks the delivery side to stop delivering to a role and to wait for
// it to be idle, up to a limit of its own. A failure does not prevent the
// stop.
type Quiesce interface {
	// Stop quiesces the role's delivery for the epoch.
	Stop(ctx context.Context, role leadrole.Role, epoch uint64) error
}

// Options configures a [Controller].
type Options struct {
	// Registry is the durable record. Required. The controller never closes
	// it.
	Registry *leadregistry.Registry
	// Manager manages the units. Required.
	Manager leadunit.Manager
	// Clock is the time source; default leadunit.SystemClock. It must be the
	// same source as the registry's Options.Now: the late-start window is
	// measured from the time the registry stamped on the launch intent, and
	// is compared with this clock. Two sources that differ make the window
	// shorter or longer than configured.
	Clock leadunit.Clock
	// Preflight checks that a start may go ahead. Required.
	Preflight Preflight
	// Holder builds the holder command. Required.
	Holder HolderCommand
	// Quiesce is the delivery side's quiesce; nil means there is none to ask.
	Quiesce Quiesce
	// PollInterval is the step of every wait; default DefaultPollInterval.
	PollInterval time.Duration
	// ProofBound is how long Stop waits for the proof after its signals;
	// default DefaultProofBound.
	ProofBound time.Duration
	// LateStartWindow is how long after a launch intent a start that lost its
	// reply is still thought able to land; default DefaultLateStartWindow. It
	// must be longer than any manager call timeout. It is a heuristic, not a
	// bound: see the package documentation.
	LateStartWindow time.Duration
	// LateStartMargin is added to LateStartWindow before a launch intent is
	// closed as abandoned; default DefaultLateStartMargin. The registry stamps
	// the intent before the command is built and the request is sent, and
	// across a restart only the wall clock is available, so a step of it
	// (a time adjustment, a suspend and resume) can close the window early.
	// The margin covers both; it is not a guarantee. Zero means the default;
	// NoLateStartMargin asks for no margin at all.
	LateStartMargin time.Duration
	// NoLateStartMargin sets the margin to zero instead of the default. It
	// cannot be combined with a LateStartMargin above zero. It is for a
	// registry and a controller that share one monotonic time source and a
	// request path that is not delayed between the intent and the send.
	NoLateStartMargin bool
	// Limits is optional. Start asks it once per launch, for the role that is
	// being started, and puts the answer in the unit specification as
	// [leadunit.Spec].Limits, so that the unit is created with those memory,
	// swap and task limits. Nil, or an answer of nil, gives a unit with none,
	// as before. The values come from the caller's configuration and are
	// checked by [leadunit.Limits.Validate] before the registry changes: an
	// answer outside the bounds refuses the start, changing nothing.
	Limits func(role leadrole.Role) *leadunit.Limits
}

// Controller starts, stops and reconciles lead roles. Its methods are safe for
// concurrent use; operations on one role run one at a time.
type Controller struct {
	reg    *leadregistry.Registry
	mgr    leadunit.Manager
	clock  leadunit.Clock
	pre    Preflight
	holder HolderCommand
	quiet  Quiesce
	limits func(role leadrole.Role) *leadunit.Limits
	poll   time.Duration
	bound  time.Duration
	window time.Duration
	margin time.Duration

	mu        sync.Mutex
	locks     map[leadrole.Role]chan struct{}
	recovered bool
	ackDue    bool
}

// New returns a Controller. It fails when a required option is missing or a
// duration is negative.
func New(o Options) (*Controller, error) {
	switch {
	case o.Registry == nil:
		return nil, errors.New("leadlifecycle: Options.Registry is required")
	case o.Manager == nil:
		return nil, errors.New("leadlifecycle: Options.Manager is required")
	case o.Preflight == nil:
		return nil, errors.New("leadlifecycle: Options.Preflight is required")
	case o.Holder == nil:
		return nil, errors.New("leadlifecycle: Options.Holder is required")
	case o.PollInterval < 0 || o.ProofBound < 0 || o.LateStartWindow < 0 || o.LateStartMargin < 0:
		return nil, errors.New("leadlifecycle: a duration option is negative")
	case o.NoLateStartMargin && o.LateStartMargin != 0:
		return nil, errors.New("leadlifecycle: Options.NoLateStartMargin and Options.LateStartMargin are both set")
	}
	c := &Controller{
		reg: o.Registry, mgr: o.Manager, clock: o.Clock, pre: o.Preflight,
		holder: o.Holder, quiet: o.Quiesce, limits: o.Limits, poll: o.PollInterval, bound: o.ProofBound,
		window: o.LateStartWindow, margin: o.LateStartMargin, locks: map[leadrole.Role]chan struct{}{},
	}
	if c.clock == nil {
		c.clock = leadunit.SystemClock{}
	}
	if c.poll == 0 {
		c.poll = DefaultPollInterval
	}
	if c.bound == 0 {
		c.bound = DefaultProofBound
	}
	if c.window == 0 {
		c.window = DefaultLateStartWindow
	}
	if c.margin == 0 && !o.NoLateStartMargin {
		c.margin = DefaultLateStartMargin
	}
	return c, nil
}

// lock takes the role's lock, or returns the context's error.
func (c *Controller) lock(ctx context.Context, role leadrole.Role) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	ch := c.locks[role]
	if ch == nil {
		ch = make(chan struct{}, 1)
		c.locks[role] = ch
	}
	c.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// load reads the role and refuses a role that ataniq does not run.
func (c *Controller) load(role leadrole.Role) (leadregistry.Entry, error) {
	e, err := c.reg.Get(role)
	if err != nil {
		return leadregistry.Entry{}, fmt.Errorf("leadlifecycle: %w", err)
	}
	if e.Manager != leadregistry.ManagerAtaniq {
		return e, fmt.Errorf("%w: %s is run by %s", ErrNotManaged, role, e.Manager)
	}
	return e, nil
}

func (c *Controller) setUnknown(e leadregistry.Entry, code, detail string) (leadregistry.Entry, error) {
	// Idempotent by reason code: a repeated Reconcile does not append a line
	// for a role that is already unknown for the same reason.
	if e.Shown.State == leadregistry.StateUnknown && e.Shown.Reason.Code == code {
		return e, nil
	}
	return c.reg.SetState(e.Role, leadregistry.StateChange{
		Epoch: e.Epoch, State: leadregistry.StateUnknown, Evidence: leadregistry.EvidenceObservation,
		Reason: leadregistry.Reason{Code: code, Detail: clip(detail, leadregistry.MaxReasonDetailBytes)},
	})
}

func (c *Controller) setProven(e leadregistry.Entry, state leadregistry.State, code, detail, proof string) (leadregistry.Entry, error) {
	return c.reg.SetState(e.Role, leadregistry.StateChange{
		Epoch: e.Epoch, State: state, Evidence: leadregistry.EvidenceObservation,
		Reason: leadregistry.Reason{Code: code, Detail: clip(detail, leadregistry.MaxReasonDetailBytes)},
		Proof:  clip(proof, leadregistry.MaxProofBytes),
	})
}

// clip makes s fit a registry text: control characters become spaces and the
// text is cut at max bytes on a character boundary.
func clip(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ' ' || r == ' ' {
			return ' '
		}
		if r == utf8.RuneError {
			return '?'
		}
		return r
	}, s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// evidenceOf is the unit evidence a registry outcome stores for a state.
func evidenceOf(st leadunit.State) leadregistry.UnitEvidence {
	return leadregistry.UnitEvidence{
		InvocationID: st.InvocationID, ControlGroupPath: st.ControlGroup,
		ControlGroupID: st.ControlGroupID, BootID: st.BootID,
	}
}

// stateOf is the manager state that names the invocation of recorded
// evidence. With no recorded invocation it names only the unit.
func stateOf(unit string, ev leadregistry.UnitEvidence) leadunit.State {
	return leadunit.State{
		Unit: unit, Active: leadunit.ActiveUnknown, InvocationID: ev.InvocationID,
		ControlGroup: ev.ControlGroupPath, ControlGroupID: ev.ControlGroupID, BootID: ev.BootID,
	}
}

// matchesIntent reports whether a loaded unit is the one of the intent: ours,
// with its epoch and its intent id.
func matchesIntent(st leadunit.State, in *leadregistry.Intent) bool {
	return in != nil && st.Loaded && st.Ours && st.Epoch == in.Epoch && st.Intent == in.ID
}

// matchesEntry reports whether a loaded unit is the unit the registry
// records for the role's current epoch: the intent matches, the intent ended
// in launched or adopted, and the recorded invocation, if any, is this one.
func matchesEntry(st leadunit.State, e leadregistry.Entry) bool {
	if !matchesIntent(st, e.Intent) {
		return false
	}
	if e.Intent.Outcome != leadregistry.OutcomeLaunched && e.Intent.Outcome != leadregistry.OutcomeAdopted {
		return false
	}
	return e.Unit.InvocationID == "" || e.Unit.InvocationID == st.InvocationID
}

// describeLoaded says what a loaded unit is, for a reason detail.
func describeLoaded(st leadunit.State) string {
	if st.Ours {
		return fmt.Sprintf("a unit of ataniq with epoch %d intent %s is loaded (invocation %s)", st.Epoch, st.Intent, st.InvocationID)
	}
	return fmt.Sprintf("a unit that is not ataniq's is loaded (invocation %s, description %q)", st.InvocationID, st.Description)
}

func foreignCode(st leadunit.State) string {
	if st.Ours {
		return "stale-unit"
	}
	return "foreign-unit"
}

// proofKind says how far a proof of absence got.
type proofKind int

const (
	proofDone proofKind = iota
	proofLacking
	proofUnavailable
)

// Bases of a proof of absence.
const (
	basisReboot = "reboot"
	basisEmpty  = "empty"
)

// absence is the result of looking for a unit that should be gone.
type absence struct {
	kind  proofKind
	basis string
	text  string
	why   string
	err   error
}

// proveGone returns the proof that the unit, which st says is not loaded, is
// gone: the boot identity differs from the evidence's, or the control group of
// the evidence's invocation (or, with no evidence, any control group at the
// unit's fixed path) is proven empty.
func (c *Controller) proveGone(ctx context.Context, st leadunit.State, ev leadregistry.UnitEvidence) absence {
	if ev.InvocationID != "" && ev.BootID != "" && st.BootID != "" && ev.BootID != st.BootID {
		return absence{kind: proofDone, basis: basisReboot, text: fmt.Sprintf(
			"boot identity changed from %s to %s; unit %s is not loaded, so its control group %s of invocation %s is gone",
			ev.BootID, st.BootID, st.Unit, ev.ControlGroupPath, ev.InvocationID)}
	}
	target := stateOf(st.Unit, ev)
	empty, err := c.mgr.Empty(ctx, target)
	switch {
	case err != nil && errors.Is(err, leadunit.ErrUnavailable):
		return absence{kind: proofUnavailable, why: "manager unavailable for the control group proof", err: err}
	case err != nil:
		return absence{kind: proofLacking, why: "control group not proven empty: " + err.Error(), err: err}
	case !empty:
		return absence{kind: proofLacking, why: "control group of the unit is not proven empty"}
	}
	if ev.InvocationID == "" {
		return absence{kind: proofDone, basis: basisEmpty, text: fmt.Sprintf(
			"unit %s is not loaded and no control group exists at its path (boot %s)", st.Unit, st.BootID)}
	}
	return absence{kind: proofDone, basis: basisEmpty, text: fmt.Sprintf(
		"unit %s is not loaded and control group %s (%s) of invocation %s is proven empty (boot %s)",
		st.Unit, ev.ControlGroupPath, ev.ControlGroupID, ev.InvocationID, st.BootID)}
}

// pollUntil calls check until it reports ok or the bound has passed, waiting
// the poll interval between calls on the injected clock. Only a check that
// reported ok gives ok true; a check that never did gives ok false with the
// last reason it was given, which may be empty, so callers decide on ok and
// never on the reason. A context error stops it at once and gives ok false.
func (c *Controller) pollUntil(ctx context.Context, check func(context.Context) (ok bool, why string)) (ok bool, why string, err error) {
	deadline := c.clock.Now().Add(c.bound)
	for {
		passed, w := check(ctx)
		if passed {
			return true, "", nil
		}
		why = w
		if err := ctx.Err(); err != nil {
			return false, why, err
		}
		left := deadline.Sub(c.clock.Now())
		if left <= 0 {
			return false, why, nil
		}
		if err := c.clock.Sleep(ctx, min(c.poll, left)); err != nil {
			return false, why, err
		}
	}
}

// inspectRetry inspects the unit, retrying an unavailable manager on the poll
// interval until the proof bound has passed.
func (c *Controller) inspectRetry(ctx context.Context, unit string) (leadunit.State, error) {
	deadline := c.clock.Now().Add(c.bound)
	for {
		st, err := c.mgr.Inspect(ctx, unit)
		if err == nil || !errors.Is(err, leadunit.ErrUnavailable) {
			return st, err
		}
		left := deadline.Sub(c.clock.Now())
		if left <= 0 {
			return st, err
		}
		if serr := c.clock.Sleep(ctx, min(c.poll, left)); serr != nil {
			return st, serr
		}
	}
}

// maxLateStartClose is how far past a launch intent the window can close at
// the latest. A window and a margin that add up to more than this (or that
// would overflow a duration, or the time) are cut to it: a role is then
// never settled because of an arithmetic wrap.
const maxLateStartClose = 100 * 365 * 24 * time.Hour

// windowClosesAt is the time after which a launch intent whose start reply was
// lost is closed as abandoned: the intent's time, the window, then the margin.
// Each is added on its own and the result is clamped, so that a large option
// cannot make the sum wrap into the past and close the window at once.
func (c *Controller) windowClosesAt(in leadregistry.Intent) time.Time {
	limit := in.At.Add(maxLateStartClose)
	t := in.At
	for _, d := range []time.Duration{c.window, c.margin} {
		if d < 0 || d > maxLateStartClose {
			return limit
		}
		t = t.Add(d)
		if t.Before(in.At) || t.After(limit) {
			return limit
		}
	}
	return t
}
