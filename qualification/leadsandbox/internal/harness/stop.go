//go:build linux && amd64

package harness

import (
	"syscall"
	"time"

	"ataniq/internal/leadsandbox/assert"
)

// StopEvidence is what the stop did.
type StopEvidence struct {
	// State is one of the StopState values.
	State string `json:"state"`
	// Signals are the signals sent, in order.
	Signals []string `json:"signals,omitempty"`
}

// The states of a stop. StopUnknown is assert.StopStateUnknown.
const (
	StopNotLaunched = "not-launched"
	StopNothingLeft = "nothing-left"
	StopByTerm      = "stopped-by-sigterm"
	StopByKill      = "stopped-by-sigkill"
	StopUnknown     = assert.StopStateUnknown
)

// stopProcess applies the stop rule of the lead-hosting design (section 3.8)
// to the process group, with the constants of package assert: SIGTERM, wait,
// SIGKILL, wait. The wait is for the whole group to be gone, not only the
// first process. A group still there after the second wait is in the state
// StopUnknown, and the harness says so; it does not wait longer. This is the
// harness's own cleanup of its process group (the launcher keeps the first
// process unreaped until Close, so the group id is not reused while it is
// signalled); the unit and control-group
// rules of the design belong to the holder's qualification.
func stopProcess(p Process, termWait, killWait, poll time.Duration) StopEvidence {
	var ev StopEvidence
	delivered, err := p.Signal(syscall.SIGTERM)
	if !delivered && err == nil {
		ev.State = StopNothingLeft
		return ev
	}
	ev.Signals = append(ev.Signals, "SIGTERM")
	if groupGone(p, termWait, poll) {
		ev.State = StopByTerm
		return ev
	}
	p.Signal(syscall.SIGKILL)
	ev.Signals = append(ev.Signals, "SIGKILL")
	if groupGone(p, killWait, poll) {
		ev.State = StopByKill
		return ev
	}
	ev.State = StopUnknown
	return ev
}

// groupGone waits up to wait for no process to be left in the group. It asks
// at once, on every tick, and when the first process exits.
func groupGone(p Process, wait, poll time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	done := p.Done()
	for {
		if there, err := p.Signal(0); !there && err == nil {
			return true
		}
		select {
		case <-done:
			done = nil // closed for good: do not spin on it
		case <-tick.C:
		case <-timer.C:
			there, err := p.Signal(0)
			return !there && err == nil
		}
	}
}
