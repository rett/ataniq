package leaddaemon

import (
	"context"
	"errors"
	"sync/atomic"
	"syscall"

	"ataniq/internal/leadunit"
)

// failureCounter wraps the unit manager the daemon hands to the lifecycle and
// counts the calls that failed, so that the daemon can show a reconcile as not
// completed when any call to the manager did not give an answer. The
// lifecycle turns some of these errors into a state of the role (an Empty that
// fails leaves the role unknown) and returns no error for them, so the report
// alone cannot tell a role that was proven from one that could not be asked.
//
// An error is not a failure when it is an answer that the leadunit
// documentation defines as a result:
//
//   - Start: [leadunit.ErrUnitExists], the name is taken and nothing was
//     started. Every other error (an invalid spec, [leadunit.ErrUnavailable],
//     [leadunit.ErrReplyLost], a context that ended) is a failure.
//   - Inspect: none. A unit that is not loaded is the answer Loaded false with
//     a nil error, and does not come as an error at all; every error is a
//     failure.
//   - Signal: [leadunit.ErrNotLoaded] and [leadunit.ErrInvocationChanged], the
//     unit or the invocation is gone, which is what a stop wants to know.
//   - Empty: [leadunit.ErrControlGroupMismatch], a control group of another
//     identity, answered together with false. Every other error is a failure,
//     a context that ended in it included.
//
// The daemon calls only Inspect and Empty (through Reconcile); the others are
// counted so that the count stays whole if that changes.
type failureCounter struct {
	leadunit.Manager
	failed atomic.Int64
}

// note counts err unless it is nil or one of the answers in answers.
func (f *failureCounter) note(err error, answers ...error) {
	if err == nil {
		return
	}
	for _, a := range answers {
		if errors.Is(err, a) {
			return
		}
	}
	f.failed.Add(1)
}

func (f *failureCounter) Start(ctx context.Context, spec leadunit.Spec) (leadunit.State, error) {
	st, err := f.Manager.Start(ctx, spec)
	f.note(err, leadunit.ErrUnitExists)
	return st, err
}

func (f *failureCounter) Inspect(ctx context.Context, unit string) (leadunit.State, error) {
	st, err := f.Manager.Inspect(ctx, unit)
	f.note(err)
	return st, err
}

func (f *failureCounter) Signal(ctx context.Context, st leadunit.State, sig syscall.Signal) error {
	err := f.Manager.Signal(ctx, st, sig)
	f.note(err, leadunit.ErrNotLoaded, leadunit.ErrInvocationChanged)
	return err
}

func (f *failureCounter) Empty(ctx context.Context, st leadunit.State) (bool, error) {
	empty, err := f.Manager.Empty(ctx, st)
	f.note(err, leadunit.ErrControlGroupMismatch)
	return empty, err
}

// failures returns the number of calls that failed so far.
func (f *failureCounter) failures() int64 { return f.failed.Load() }

var _ leadunit.Manager = (*failureCounter)(nil)
