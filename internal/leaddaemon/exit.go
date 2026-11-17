package leaddaemon

import (
	"errors"
	"fmt"
)

// Step names one step of the start-up and shutdown of [Run].
type Step string

// The steps, in the order [Run] takes them.
const (
	// StepConfig checks the configuration and refuses root.
	StepConfig Step = "config"
	// StepState creates or checks the state folder.
	StepState Step = "state"
	// StepLock takes the daemon lock.
	StepLock Step = "lock"
	// StepRegistry opens the registry.
	StepRegistry Step = "registry"
	// StepManager reads the boot identity and builds the unit manager and
	// the lifecycle controller.
	StepManager Step = "manager"
	// StepReconcile reconciles the registry with the manager, once.
	StepReconcile Step = "reconcile"
	// StepStaleSocket removes a socket file an earlier daemon left.
	StepStaleSocket Step = "stale-socket"
	// StepSocket listens on the socket.
	StepSocket Step = "socket"
	// StepServe serves the client protocol.
	StepServe Step = "serve"
	// StepShutdown stops serving, closes the registry and releases the lock,
	// unless the server does not end within the grace.
	StepShutdown Step = "shutdown"
)

// The exit codes of the command. A step of [Run] that fails has its own.
const (
	// ExitOK means the command did what it was asked.
	ExitOK = 0
	// ExitFailure is a failure that has no code of its own (a status call
	// that timed out, for one).
	ExitFailure = 1
	// ExitUsage is a command line that is not valid.
	ExitUsage = 2
	// ExitConfig is a configuration the daemon refuses: root, a folder that
	// is not a clean absolute path, no manager factory.
	ExitConfig = 10
	// ExitState is a state folder that cannot be made, or that is not the
	// caller's or is accessible to group or others.
	ExitState = 11
	// ExitLockHeld is a daemon lock that another daemon holds.
	ExitLockHeld = 12
	// ExitLock is a daemon lock that could not be taken for another reason.
	ExitLock = 13
	// ExitRegistry is a registry that cannot be opened, corrupt or locked
	// for one. Nothing is repaired.
	ExitRegistry = 14
	// ExitManager is a unit manager or a controller that cannot be built, or
	// a boot identity that cannot be read.
	ExitManager = 15
	// ExitReconcile is a reconcile that failed in the registry.
	ExitReconcile = 16
	// ExitSocket is a socket that cannot be prepared or bound.
	ExitSocket = 17
	// ExitServe is a server that stopped by itself.
	ExitServe = 18
	// ExitShutdown is a server that did not end within the shutdown grace;
	// the registry and the lock are left to the end of the process.
	ExitShutdown = 19
	// ExitNoDaemon is, for status, no daemon at the socket (missing or
	// refused).
	ExitNoDaemon = 20
	// ExitUnauthorized is, for status, the daemon refusing this user.
	ExitUnauthorized = 21
	// ExitProtocol is, for status, an answer that is not a valid answer of
	// the protocol, or an error the daemon answered with.
	ExitProtocol = 22
)

// StartupError is the error [Run] returns for a step that failed. Its text
// names the step and says what went wrong, in one line.
type StartupError struct {
	// Step is the step that failed.
	Step Step
	// Err is the cause.
	Err error
}

// Error returns the step and the cause.
func (e *StartupError) Error() string { return string(e.Step) + ": " + e.Err.Error() }

// Unwrap returns the cause.
func (e *StartupError) Unwrap() error { return e.Err }

func fail(step Step, format string, a ...any) *StartupError {
	return &StartupError{Step: step, Err: fmt.Errorf(format, a...)}
}

// ExitCode returns the exit code for an error [Run] returned: 0 for nil, the
// code of the step for a [StartupError] (the lock step has two, told apart
// by whether the lock is held) and [ExitFailure] for anything else.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var se *StartupError
	if !errors.As(err, &se) {
		return ExitFailure
	}
	switch se.Step {
	case StepConfig:
		return ExitConfig
	case StepState:
		return ExitState
	case StepLock:
		if errors.Is(se.Err, errLockHeld) {
			return ExitLockHeld
		}
		return ExitLock
	case StepRegistry:
		return ExitRegistry
	case StepManager:
		return ExitManager
	case StepReconcile:
		return ExitReconcile
	case StepStaleSocket, StepSocket:
		return ExitSocket
	case StepServe:
		return ExitServe
	case StepShutdown:
		return ExitShutdown
	}
	return ExitFailure
}

// errLockHeld marks the error of a lock another daemon holds.
var errLockHeld = errors.New("the daemon lock is held by another process")
