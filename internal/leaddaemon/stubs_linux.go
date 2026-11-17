package leaddaemon

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// ErrNotAvailable is what the daemon's Preflight and HolderCommand answer: this
// build has neither a qualification record nor a holder command line, so
// nothing can be started. They are never reached, because nothing calls Start.
var ErrNotAvailable = errors.New("not available in this build")

type refusePreflight struct{}

func (refusePreflight) Check(context.Context, leadrole.Role, leadregistry.Entry) error {
	return fmt.Errorf("preflight: %w", ErrNotAvailable)
}

type refuseHolder struct{}

func (refuseHolder) Build(leadrole.Role, leadregistry.Entry, leadregistry.Intent) ([]string, string, []string, error) {
	return nil, "", nil, fmt.Errorf("holder command: %w", ErrNotAvailable)
}

var (
	_ leadlifecycle.Preflight     = refusePreflight{}
	_ leadlifecycle.HolderCommand = refuseHolder{}
)

// unavailableManager is a [leadunit.Manager] that answers every call with
// [leadunit.ErrUnavailable].
type unavailableManager struct{ cause error }

// UnavailableManager returns a unit manager that answers every call with an
// error that matches [leadunit.ErrUnavailable] and wraps cause. A command
// that cannot build the real manager (the tools are missing) passes it, so
// that the daemon still serves the registry and shows its reconcile as not
// completed.
func UnavailableManager(cause error) leadunit.Manager { return unavailableManager{cause: cause} }

func (m unavailableManager) err() error {
	return fmt.Errorf("%w: %v", leadunit.ErrUnavailable, m.cause)
}

func (m unavailableManager) Start(context.Context, leadunit.Spec) (leadunit.State, error) {
	return leadunit.State{}, m.err()
}

func (m unavailableManager) Inspect(context.Context, string) (leadunit.State, error) {
	return leadunit.State{}, m.err()
}

func (m unavailableManager) Signal(context.Context, leadunit.State, syscall.Signal) error {
	return m.err()
}

func (m unavailableManager) Empty(context.Context, leadunit.State) (bool, error) {
	return false, m.err()
}
