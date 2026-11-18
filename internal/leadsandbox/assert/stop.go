package assert

import "time"

// The stop rule of the lead-hosting design (section 3.8), as data. The holder
// implements it; nothing here sends a signal or starts a timer.
//
// To stop a lead the holder sends SIGTERM to the agent. If the agent is still
// there after StopTermWait it sends SIGKILL. Then the unit must be gone and
// its control group empty within StopKillWait more. If it is not, the lead's
// state is StopStateUnknown, and nothing is started for the role until a
// later check proves the unit gone and the control group empty (the
// replacement rule of the design, section 1.6).
const (
	// StopSignalTerm is the first signal.
	StopSignalTerm = "SIGTERM"
	// StopTermWait is how long the agent has to go after StopSignalTerm.
	StopTermWait = 2 * time.Second
	// StopSignalKill is the second signal.
	StopSignalKill = "SIGKILL"
	// StopKillWait is how long the unit and its control group have to be
	// gone after StopSignalKill.
	StopKillWait = 2 * time.Second
	// StopStateUnknown is the state of a lead whose unit or control group is
	// still there after StopKillWait.
	StopStateUnknown = "unknown"
)
