package leadunit

// ActiveState is the active state of a unit as systemd reports it, reduced to
// the values this package acts on.
type ActiveState string

// The active states. Any other state systemd reports is [ActiveUnknown].
const (
	// ActiveActive is "active".
	ActiveActive ActiveState = "active"
	// ActiveActivating is "activating".
	ActiveActivating ActiveState = "activating"
	// ActiveDeactivating is "deactivating".
	ActiveDeactivating ActiveState = "deactivating"
	// ActiveInactive is "inactive".
	ActiveInactive ActiveState = "inactive"
	// ActiveFailed is "failed".
	ActiveFailed ActiveState = "failed"
	// ActiveUnknown stands for any other reported state, and for a unit that
	// is not loaded.
	ActiveUnknown ActiveState = "unknown"
)

// ParseActiveState maps the text of systemd's ActiveState property to an
// [ActiveState]; any text it does not know gives [ActiveUnknown].
func ParseActiveState(s string) ActiveState {
	switch a := ActiveState(s); a {
	case ActiveActive, ActiveActivating, ActiveDeactivating, ActiveInactive, ActiveFailed:
		return a
	}
	return ActiveUnknown
}

// State is what the manager knows of one unit at one moment. A unit that is
// not loaded has Loaded false, no invocation and no control group: that is a
// positive answer, not an error.
type State struct {
	// Unit is the unit name.
	Unit string
	// Loaded is whether systemd has the unit in memory.
	Loaded bool
	// Active is the active state; [ActiveUnknown] for anything else.
	Active ActiveState
	// InvocationID identifies one run of the unit. A signal or a proof for a
	// State applies to that run only.
	InvocationID string
	// ControlGroup is the control group path of the unit.
	ControlGroup string
	// ControlGroupID is an opaque identity of the control group, such that two
	// control groups with the same path at different times differ.
	ControlGroupID string
	// MainPID is the main process, 0 when there is none.
	MainPID int
	// BootID identifies the boot of the machine the answer came from.
	BootID string
	// Description is the unit description as the manager reports it.
	Description string
	// Epoch is the epoch parsed from the description; 0 when it is not ours.
	Epoch uint64
	// Intent is the intent id parsed from the description; empty when it is
	// not ours.
	Intent string
	// Ours is whether the description is one that [Spec.Description] renders.
	Ours bool
	// Parent is the unit this unit is bound to (the value of its BindsTo
	// property), read back where the manager can: it is empty for a unit that
	// is bound to nothing, and also when the manager cannot tell or finds more
	// than one unit it is bound to, so an empty Parent is not proof that a
	// unit has none.
	Parent string
}

// SetDescription records the description and parses Epoch, Intent and Ours
// from it with [ParseDescription].
func (s *State) SetDescription(desc string) {
	s.Description = desc
	s.Epoch, s.Intent, s.Ours = 0, "", false
	if epoch, intent, ok := ParseDescription(desc); ok {
		s.Epoch, s.Intent, s.Ours = epoch, intent, true
	}
}

// Live reports whether the unit is loaded and active, activating or
// deactivating: it has, or is about to have, or is giving up, processes.
func (s State) Live() bool {
	if !s.Loaded {
		return false
	}
	switch s.Active {
	case ActiveActive, ActiveActivating, ActiveDeactivating:
		return true
	}
	return false
}
