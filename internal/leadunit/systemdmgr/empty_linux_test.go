package systemdmgr

import (
	"context"
	"errors"
	"testing"

	"ataniq/internal/leadunit"
)

const (
	oldGroup = cgBase + "/app.slice/old-" + unitName // a path the unit no longer has
)

// oldState is the State of an earlier run of the unit: invocation inv1, the
// control group at cgUnit with identity idA.
func oldState() leadunit.State {
	return leadunit.State{
		Unit: unitName, Loaded: true, Active: leadunit.ActiveActive, InvocationID: inv1,
		ControlGroup: cgUnit, ControlGroupID: groupID(idA), MainPID: 321, BootID: bootID,
	}
}

func (r *rig) empty(st leadunit.State) (bool, error) {
	r.t.Helper()
	return r.m.Empty(context.Background(), st)
}

func (r *rig) wantEmpty(st leadunit.State, want bool) {
	r.t.Helper()
	got, err := r.empty(st)
	if got != want || (want && err != nil) {
		r.t.Fatalf("Empty = %v, %v; want %v", got, err, want)
	}
}

func (r *rig) wantNotEmptyWithError(st leadunit.State) error {
	r.t.Helper()
	got, err := r.empty(st)
	if got || err == nil {
		r.t.Fatalf("Empty = %v, %v; want false with an error", got, err)
	}
	return err
}

// Case 1: the unit is not loaded and its group is gone.
func TestEmptyCase1NotLoadedGroupGone(t *testing.T) {
	r := newRig(t)
	r.setUnit(notFoundProps())
	delete(r.k.groups, cgUnit)
	r.wantEmpty(oldState(), true)
	mustEqual(t, r.k.lookups, []string{cgUnit})
}

// Case 1: not loaded, and the path now holds a group with another identity.
func TestEmptyCase1NotLoadedGroupReplaced(t *testing.T) {
	r := newRig(t)
	r.setUnit(notFoundProps())
	r.k.groups[cgUnit] = idB
	r.wantEmpty(oldState(), true)
}

// Case 1: loaded with another invocation and another identity, the old group
// is gone from its own path.
func TestEmptyCase1NewInvocationOldGroupGone(t *testing.T) {
	r := newRig(t)
	st := oldState()
	st.ControlGroup = cgBase + "/session.slice/" + unitName
	r.k.groups[cgUnit] = idB
	r.setUnit(with(loadedProps(), "InvocationID", inv2))
	r.wantEmpty(st, true)
}

// Case 1: loaded with another invocation, whose new group sits at the same
// path with another identity, which proves the old directory is gone.
func TestEmptyCase1NewInvocationSamePathNewIdentity(t *testing.T) {
	r := newRig(t)
	r.k.groups[cgUnit] = idB
	r.setUnit(with(loadedProps(), "InvocationID", inv2))
	r.wantEmpty(oldState(), true)
}

// A new invocation alone is never proof: the old group of the State is still
// there with its identity and a process in it.
func TestEmptyNewInvocationAloneIsNotProof(t *testing.T) {
	r := newRig(t)
	st := oldState()
	st.ControlGroup = oldGroup
	r.k.groups[oldGroup] = idA
	r.k.populated[oldGroup] = true
	r.setUnit(with(loadedProps(), "InvocationID", inv2)) // its group sits at cgUnit
	r.k.groups[cgUnit] = idB
	r.wantEmpty(st, false)
	if r.k.popCalls != 0 {
		t.Fatal("populated read for a unit with another invocation")
	}
}

// Another invocation that reports the very identity of the State is
// inconsistent, not proof.
func TestEmptyNewInvocationSameIdentityIsAnError(t *testing.T) {
	r := newRig(t)
	r.setUnit(with(loadedProps(), "InvocationID", inv2))
	r.wantNotEmptyWithError(oldState())
}

// Case 2: same identity, nothing in it.
func TestEmptyCase2SameIdentityNotPopulated(t *testing.T) {
	r := newRig(t)
	r.setUnit(notFoundProps())
	r.k.populated[cgUnit] = false
	r.wantEmpty(oldState(), true)
	if r.k.popCalls != 1 {
		t.Fatalf("populated read %d times", r.k.popCalls)
	}
}

func TestEmptyCase2SameIdentityPopulatedIsFalse(t *testing.T) {
	r := newRig(t)
	r.setUnit(notFoundProps())
	r.k.populated[cgUnit] = true
	r.wantEmpty(oldState(), false)
}

func TestEmptyCase2PopulatedReadFailsIsFalseWithError(t *testing.T) {
	r := newRig(t)
	r.setUnit(notFoundProps())
	r.k.popErr = errors.New("cgroup identity missing or changed")
	r.wantNotEmptyWithError(oldState())
}

// Case 2 for a loaded unit with the same invocation: only once it is no
// longer active or activating.
func TestEmptyCase2LoadedSameInvocation(t *testing.T) {
	for _, active := range []string{"inactive", "failed", "deactivating"} {
		r := newRig(t)
		r.setUnit(with(loadedProps(), "ActiveState", active, "MainPID", "0"))
		r.k.populated[cgUnit] = false
		r.wantEmpty(oldState(), true)
	}
	for _, active := range []string{"active", "activating", "reloading", "maintenance"} {
		r := newRig(t)
		r.setUnit(with(loadedProps(), "ActiveState", active))
		r.k.populated[cgUnit] = false
		r.wantEmpty(oldState(), false)
		if r.k.popCalls != 0 {
			t.Fatalf("%s: populated read for a unit that can still gain a process", active)
		}
	}
	r := newRig(t)
	r.setUnit(with(loadedProps(), "ActiveState", "deactivating"))
	r.k.populated[cgUnit] = true
	r.wantEmpty(oldState(), false)
}

// systemd clears ControlGroup of an ended unit that is still loaded. The State's
// recorded group then decides, and anything doubtful stays false.
func TestEmptyLoadedSameInvocationWithTheControlGroupCleared(t *testing.T) {
	for _, active := range []string{"inactive", "failed"} {
		cleared := with(loadedProps(), "ActiveState", active, "MainPID", "0", "ControlGroup", "")

		// The group is gone: true.
		r := newRig(t)
		r.setUnit(cleared)
		delete(r.k.groups, cgUnit)
		r.wantEmpty(oldState(), true)

		// The path now has another identity: true.
		r = newRig(t)
		r.setUnit(cleared)
		r.k.groups[cgUnit] = idB
		r.wantEmpty(oldState(), true)
		if r.k.popCalls != 0 {
			t.Fatalf("%s: populated read for another identity", active)
		}

		// The same group, no process: true. With a process: false, no error.
		r = newRig(t)
		r.setUnit(cleared)
		r.k.populated[cgUnit] = false
		r.wantEmpty(oldState(), true)
		r = newRig(t)
		r.setUnit(cleared)
		r.k.populated[cgUnit] = true
		r.wantEmpty(oldState(), false)

		// Any doubt is false with the error: the group cannot be looked up,
		// the populated flag cannot be read, a main process is reported.
		r = newRig(t)
		r.setUnit(cleared)
		r.k.lookupErr[cgUnit] = errors.New("permission denied")
		r.wantNotEmptyWithError(oldState())
		r = newRig(t)
		r.setUnit(cleared)
		r.k.popErr = errors.New("unreadable")
		r.wantNotEmptyWithError(oldState())
		r = newRig(t)
		r.setUnit(with(cleared, "MainPID", "321"))
		delete(r.k.groups, cgUnit)
		if err := r.wantNotEmptyWithError(oldState()); !errors.Is(err, leadunit.ErrControlGroupMismatch) {
			t.Fatal(err)
		}
	}
	// Still active, activating or deactivating with no group: not over, false.
	for _, active := range []string{"active", "activating", "deactivating", "reloading", "maintenance"} {
		r := newRig(t)
		r.setUnit(with(loadedProps(), "ActiveState", active, "MainPID", "0", "ControlGroup", ""))
		delete(r.k.groups, cgUnit)
		if err := r.wantNotEmptyWithError(oldState()); !errors.Is(err, leadunit.ErrControlGroupMismatch) {
			t.Fatalf("%s: %v", active, err)
		}
	}
	// Another invocation with no group is still case 1: not this change.
	r := newRig(t)
	r.setUnit(with(loadedProps(), "InvocationID", inv2, "ActiveState", "failed", "MainPID", "0", "ControlGroup", ""))
	delete(r.k.groups, cgUnit)
	r.wantEmpty(oldState(), true)
}

func TestEmptyLoadedSameInvocationOtherGroupIsAMismatch(t *testing.T) {
	r := newRig(t)
	st := oldState()
	st.ControlGroupID = groupID(idB)
	if err := r.wantNotEmptyWithError(st); !errors.Is(err, leadunit.ErrControlGroupMismatch) {
		t.Fatal(err)
	}
	st = oldState()
	st.ControlGroup = cgBase + "/other.slice/" + unitName
	if err := r.wantNotEmptyWithError(st); !errors.Is(err, leadunit.ErrControlGroupMismatch) {
		t.Fatal(err)
	}
}

// Case 3: no invocation, no group: not loaded and no directory of that name
// below the user manager's group.
func TestEmptyCase3NoGroupAnywhere(t *testing.T) {
	r := newRig(t)
	r.setUnit(notFoundProps())
	st := leadunit.State{Unit: unitName, Active: leadunit.ActiveUnknown, BootID: bootID}
	r.wantEmpty(st, true)
	mustEqual(t, r.run.recorded()[1], []string{"systemctl", "--user", "show", "--all", "--property=ControlGroup", "--", "-.slice"})
	mustEqual(t, r.k.finds, []string{cgBase + " " + unitName})
}

func TestEmptyCase3GroupExistsSomewhere(t *testing.T) {
	r := newRig(t)
	r.setUnit(notFoundProps())
	r.k.named[cgBase+" "+unitName] = []string{cgUnit}
	r.wantEmpty(leadunit.State{Unit: unitName}, false)
}

func TestEmptyCase3UnitLoadedIsFalseWithoutSearching(t *testing.T) {
	r := newRig(t)
	r.wantEmpty(leadunit.State{Unit: unitName}, false)
	if len(r.k.finds) != 0 || len(r.run.recorded()) != 1 {
		t.Fatalf("%v %v", r.k.finds, r.callNames())
	}
}

func TestEmptyCase3CannotDeriveTheManagerGroup(t *testing.T) {
	cases := map[string]func(r *rig){
		"show fails":       func(r *rig) { r.show["-.slice"] = ""; r.run.handle = failOnSlice(r.run.handle) },
		"empty":            func(r *rig) { r.show["-.slice"] = "ControlGroup=\n" },
		"root":             func(r *rig) { r.show["-.slice"] = "ControlGroup=/\n" },
		"relative":         func(r *rig) { r.show["-.slice"] = "ControlGroup=user.slice\n" },
		"trailing slash":   func(r *rig) { r.show["-.slice"] = "ControlGroup=" + cgBase + "/\n" },
		"unparsable":       func(r *rig) { r.show["-.slice"] = "nonsense\n" },
		"extra property":   func(r *rig) { r.show["-.slice"] = "ControlGroup=" + cgBase + "\nId=-.slice\n" },
		"group missing":    func(r *rig) { delete(r.k.groups, cgBase) },
		"group unreadable": func(r *rig) { r.k.lookupErr[cgBase] = errors.New("permission denied") },
		"search fails":     func(r *rig) { r.k.findErr = errors.New("control group search is over its bound") },
	}
	for name, setup := range cases {
		r := newRig(t)
		r.setUnit(notFoundProps())
		setup(r)
		got, err := r.empty(leadunit.State{Unit: unitName})
		if got || err == nil {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}

func failOnSlice(next func([]string) (string, error)) func([]string) (string, error) {
	return func(argv []string) (string, error) {
		if argv[len(argv)-1] == "-.slice" {
			return "", errBoom
		}
		return next(argv)
	}
}

func TestEmptyInspectFailureIsFalseWithError(t *testing.T) {
	cases := map[string]func(r *rig){
		"tool fails":        func(r *rig) { r.showFn = func() (string, error) { return "", errBoom } },
		"unparsable":        func(r *rig) { r.showFn = func() (string, error) { return "Id=x\n", nil } },
		"cgroup unreadable": func(r *rig) { r.k.lookupErr[cgUnit] = errors.New("permission denied") },
	}
	for name, setup := range cases {
		r := newRig(t)
		setup(r)
		for _, st := range []leadunit.State{oldState(), {Unit: unitName}} {
			if got, err := r.empty(st); got || err == nil {
				t.Errorf("%s: %v %v", name, got, err)
			}
		}
	}
}

func TestEmptyLookupFailureIsFalseWithError(t *testing.T) {
	r := newRig(t)
	r.setUnit(notFoundProps())
	r.k.lookupErr[cgUnit] = errors.New("permission denied")
	r.wantNotEmptyWithError(oldState())
}

func TestEmptyIncompleteOrUnreadableStates(t *testing.T) {
	r := newRig(t)
	r.setUnit(notFoundProps())
	for name, st := range map[string]leadunit.State{
		"group without identity": {Unit: unitName, InvocationID: inv1, ControlGroup: cgUnit},
		"identity without group": {Unit: unitName, InvocationID: inv1, ControlGroupID: groupID(idA)},
		"invocation only":        {Unit: unitName, InvocationID: inv1},
		"bad identity":           {Unit: unitName, InvocationID: inv1, ControlGroup: cgUnit, ControlGroupID: "x"},
	} {
		if got, err := r.empty(st); got || err == nil {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}

func TestEmptyLoadedUnitAndStateWithoutInvocation(t *testing.T) {
	r := newRig(t)
	st := oldState()
	st.InvocationID = ""
	if err := r.wantNotEmptyWithError(st); !errors.Is(err, leadunit.ErrInvocationChanged) {
		t.Fatal(err)
	}
	r = newRig(t)
	r.setUnit(with(loadedProps(), "InvocationID", ""))
	r.wantNotEmptyWithError(oldState())
}

func TestEmptyContextDoneAndInvalidName(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := r.m.Empty(ctx, oldState()); got || !errors.Is(err, context.Canceled) {
		t.Fatal(got, err)
	}
	if got, err := r.m.Empty(context.Background(), leadunit.State{Unit: "a b.service"}); got || !errors.Is(err, leadunit.ErrInvalidSpec) {
		t.Fatal(got, err)
	}
	if len(r.run.recorded()) != 0 {
		t.Fatal("a tool ran")
	}
}

// The control group text read back from a unit never reaches a command line:
// the only text in argv besides fixed words is the validated unit name.
func TestNoUnitTextInAnyCommandLine(t *testing.T) {
	r := newRig(t)
	r.setUnit(with(notFoundProps()))
	r.show["-.slice"] = "ControlGroup=/user.slice/x y\n"
	_, _ = r.empty(leadunit.State{Unit: unitName})
	for _, c := range r.run.recorded() {
		for _, a := range c {
			if a == "/user.slice/x y" {
				t.Fatalf("control group text in %q", c)
			}
		}
	}
}

func TestPopulatedReadsTheGroupOfTheState(t *testing.T) {
	r := newRig(t)
	st := oldState()
	r.k.populated[cgUnit] = true
	if got, err := r.m.Populated(context.Background(), st); err != nil || !got {
		t.Fatal(got, err)
	}
	r.k.populated[cgUnit] = false
	if got, err := r.m.Populated(context.Background(), st); err != nil || got {
		t.Fatal(got, err)
	}
	if len(r.run.recorded()) != 0 {
		t.Fatal("Populated runs no tool")
	}
}

func TestPopulatedFailsWithoutAReadableGroup(t *testing.T) {
	r := newRig(t)
	other := oldState()
	other.ControlGroupID = groupID(idB) // another identity than the directory has
	noGroup := oldState()
	noGroup.ControlGroup = ""
	badID := oldState()
	badID.ControlGroupID = "x"
	for name, st := range map[string]leadunit.State{"other identity": other, "no group": noGroup, "bad identity": badID} {
		if got, err := r.m.Populated(context.Background(), st); got || err == nil {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := r.m.Populated(ctx, oldState()); got || !errors.Is(err, context.Canceled) {
		t.Fatal(got, err)
	}
}
