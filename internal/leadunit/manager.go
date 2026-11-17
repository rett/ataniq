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
// a context and returns its error at once when the context is done. An
// implementation is safe for concurrent use.
type Manager interface {
	// Start starts the unit the spec describes and returns its state. It
	// returns [ErrUnitExists] when the unit name is loaded, an error wrapping
	// [ErrInvalidSpec] for a spec that does not validate, and [ErrReplyLost]
	// (with a zero State) when the outcome is unknown. After [ErrReplyLost]
	// the caller inspects and never starts again for that intent.
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
	// that is not loaded or that has another invocation, or it is present
	// with the same identity and not populated. In every case of doubt it
	// returns false, with an error or without, never true.
	Empty(ctx context.Context, st State) (bool, error)
}
