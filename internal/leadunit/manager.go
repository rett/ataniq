package leadunit

import (
	"context"
	"errors"
	"syscall"
)

var (
	// ErrUnitExists means the unit name is taken: systemd refused a second
	// unit for the role. Nothing was started.
	ErrUnitExists = errors.New("leadunit: unit already exists")
	// ErrReplyLost means the outcome of a start is unknown: the unit may or
	// may not be running. The caller must [Manager.Inspect] and must not call
	// [Manager.Start] again for that intent.
	//
	// A residual window remains after the caller has inspected and found the
	// unit not loaded: a request that was already on the bus when the reply
	// was lost can still be executed a little later, and the unit then
	// appears. A caller that finds "not loaded" after a lost reply therefore
	// waits a short settle time and inspects again before it treats that as
	// final, and never takes the first answer as the end of the intent.
	ErrReplyLost = errors.New("leadunit: start reply lost, outcome unknown")
	// ErrInvocationChanged means the unit now has another invocation than the
	// [State] the caller holds (or the caller holds none), so the call did
	// nothing.
	ErrInvocationChanged = errors.New("leadunit: invocation changed")
	// ErrNotLoaded means the unit is not loaded, so there is nothing to
	// signal.
	ErrNotLoaded = errors.New("leadunit: unit not loaded")
	// ErrUnavailable means the manager cannot be reached. It says nothing
	// about the unit.
	ErrUnavailable = errors.New("leadunit: manager unavailable")
	// ErrControlGroupMismatch means a control group has the identity of
	// another one than the [State] names. [Manager.Empty] answers false with
	// it.
	ErrControlGroupMismatch = errors.New("leadunit: control group identity differs")
)

// Manager manages the transient user unit of a lead role. Every method takes
// a context and returns at once, with the context's error, when the context is
// done before the call does anything; for [Manager.Start] the rule for a
// context that ends later is its own, below. An implementation is safe for
// concurrent use.
//
// Two obligations come from what [Spec.Validate] accepts, which is wider than
// what is safe to hand to systemd:
//
//   - An implementation that talks to systemd passes the argument vector
//     through ExecStartEx with the no-env-expand flag. Validate accepts a $ in
//     an argument, and systemd would otherwise expand $VAR and ${VAR} in the
//     arguments of ExecStart when it runs the command, so that a crafted
//     argument could read the unit's environment.
//   - Validate accepts any unit name of the allowed characters ending in
//     .service, a template name such as x@.service included. The caller builds
//     the unit names of the contract itself (ataniq-lead-PROJECT.service and
//     ataniq-supervisor.service) and never takes a name from a request.
type Manager interface {
	// Start starts the unit the spec describes and returns its state. It
	// returns [ErrUnitExists] when the unit name is loaded and an error
	// wrapping [ErrInvalidSpec] for a spec that does not validate. A spec
	// with a Parent starts a unit bound to that unit ([BoundProperties]): it
	// can become active only while the parent is, and it is stopped when the
	// parent stops.
	//
	// An error that wraps [ErrReplyLost] (with a zero State) is returned
	// whenever the outcome is unknown: the context ends, or anything else
	// fails, after the start request may have been sent. When the cause is a
	// context that ended, the error wraps both [ErrReplyLost] and the
	// context's error, so errors.Is finds either; a bare context error is
	// never returned for a request that may have been sent. Every other error
	// means nothing was started and nothing will be: a context that was
	// already done, an invalid spec, [ErrUnitExists], and [ErrUnavailable]
	// (which Start returns only when the request was not sent). After
	// [ErrReplyLost] the caller inspects and never starts again for that
	// intent.
	Start(ctx context.Context, spec Spec) (State, error)

	// Inspect returns the state of the unit. A unit that is not loaded is
	// answered with Loaded false and a nil error. [ErrUnavailable] means no
	// answer was obtained.
	Inspect(ctx context.Context, unit string) (State, error)

	// Signal sends sig to every process of the control group of the
	// invocation that st names. It returns [ErrNotLoaded] when the unit is not
	// loaded and [ErrInvocationChanged] when it has another invocation.
	Signal(ctx context.Context, st State, sig syscall.Signal) error

	// Empty reports whether the control group of the invocation that st names
	// is positively proven to hold no process: it is gone together with a unit
	// that is not loaded, it is gone because the unit has another invocation
	// in a control group of another identity than the one st names, or it is
	// present with the same identity and not populated. A new invocation that
	// runs in the control group of the old one, with or without leftover
	// processes, proves nothing. In every case of doubt it returns false, with
	// an error or without, never true.
	//
	// When st names no invocation (the answer of an Inspect that found the
	// unit not loaded, or a State holding only the unit name), a true answer
	// means exactly this and no more: no unit of that name is loaded now AND
	// no control group exists at the unit's fixed path. It frees the name for
	// the replacement rule, and it proves nothing about a start whose reply
	// was lost and whose request has not yet been executed: that start can
	// land after the answer. A caller must not record a lost intent as ended,
	// or stopped, on that answer alone. The only further evidence this
	// interface offers is a changed boot identity ([State.BootID]), or the
	// passing of a bound the caller chose, with the unit still not loaded at
	// its end. That bound is a heuristic and not the proof the contract
	// requires: the interface has no way to show that the request is gone, so
	// a caller that relies on a bound accepts that a request still on the bus
	// can land after it. A unit that lands late may carry the current epoch
	// and intent (when no newer intent was journaled): it is then the
	// caller's own unit, to adopt, and not a stale one. A late unit with an
	// older epoch is stale and is stopped by an explicit stop.
	Empty(ctx context.Context, st State) (bool, error)
}
