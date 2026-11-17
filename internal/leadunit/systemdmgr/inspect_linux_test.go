package systemdmgr

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadunit"
)

func TestInspectLoadedUnit(t *testing.T) {
	r := newRig(t)
	st, err := r.m.Inspect(context.Background(), unitName)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, st, leadunit.State{
		Unit: unitName, Loaded: true, Active: leadunit.ActiveActive, InvocationID: inv1,
		ControlGroup: cgUnit, ControlGroupID: groupID(idA), MainPID: 321, BootID: bootID,
		Description: goodDesc(), Epoch: 3, Intent: "intent-7", Ours: true,
	})
	mustEqual(t, r.run.recorded(), [][]string{{"systemctl", "--user", "show", "--all", "--property=" + showProperties, "--", unitName}})
	if want := "Id,LoadState,ActiveState,SubState,InvocationID,ControlGroup,MainPID,Description,Transient"; showProperties != want {
		t.Fatalf("property list %q", showProperties)
	}
}

func TestInspectNotLoadedIsAPositiveAnswer(t *testing.T) {
	cases := map[string]map[string]string{
		"not-found":        notFoundProps(),
		"empty load state": with(notFoundProps(), "LoadState", ""),
	}
	for name, kv := range cases {
		r := newRig(t)
		r.setUnit(kv)
		st, err := r.m.Inspect(context.Background(), unitName)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		mustEqual(t, st, leadunit.State{Unit: unitName, Active: leadunit.ActiveUnknown, BootID: bootID})
		if len(r.k.lookups) != 0 {
			t.Fatalf("%s: looked at a control group of a unit that is not loaded", name)
		}
	}
}

// A stale invocation id on a unit that is not found is not an answer to act
// on, and the State of an unloaded unit never carries it.
func TestInspectNotFoundDropsAnInvocation(t *testing.T) {
	r := newRig(t)
	r.setUnit(with(notFoundProps(), "InvocationID", inv1))
	st, err := r.m.Inspect(context.Background(), unitName)
	if err != nil || st.InvocationID != "" || st.Loaded {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestInspectContradictionsAreErrorsNeverNotLoaded(t *testing.T) {
	cases := map[string]map[string]string{
		"not-found with a control group":   with(notFoundProps(), "ControlGroup", cgUnit),
		"not-found with a process":         with(notFoundProps(), "MainPID", "5"),
		"not-found but active":             with(notFoundProps(), "ActiveState", "active"),
		"not-found but failed":             with(notFoundProps(), "ActiveState", "failed"),
		"empty load state with invocation": with(notFoundProps(), "LoadState", "", "InvocationID", inv1),
		"masked":                           with(loadedProps(), "LoadState", "masked"),
		"error":                            with(loadedProps(), "LoadState", "error"),
		"bad-setting":                      with(loadedProps(), "LoadState", "bad-setting"),
		"other unit":                       with(loadedProps(), "Id", "other.service"),
		"short invocation":                 with(loadedProps(), "InvocationID", "abc"),
		"upper invocation":                 with(loadedProps(), "InvocationID", strings.Repeat("A", 32)),
		"relative control group":           with(loadedProps(), "ControlGroup", "x/y"),
		"root control group":               with(loadedProps(), "ControlGroup", "/"),
		"main pid text":                    with(loadedProps(), "MainPID", "x"),
		"negative main pid":                with(loadedProps(), "MainPID", "-1"),
		"transient text":                   with(loadedProps(), "Transient", "maybe"),
	}
	for name, kv := range cases {
		r := newRig(t)
		r.setUnit(kv)
		st, err := r.m.Inspect(context.Background(), unitName)
		if err == nil || st != (leadunit.State{}) {
			t.Errorf("%s: %+v %v", name, st, err)
		}
	}
}

func TestInspectUnparsableOutput(t *testing.T) {
	good := show(loadedProps())
	cases := map[string]string{
		"empty":         "",
		"garbage":       "hello\n",
		"partial":       good[:len(good)/2],
		"missing key":   strings.Replace(good, "MainPID=321\n", "", 1),
		"extra key":     good + "Extra=1\n",
		"duplicate key": good + "MainPID=321\n",
		"no equals":     good + "oops\n",
		"too large":     good + strings.Repeat("x", maxOutput),
	}
	for name, out := range cases {
		r := newRig(t)
		r.showFn = func() (string, error) { return out, nil }
		st, err := r.m.Inspect(context.Background(), unitName)
		if err == nil || st != (leadunit.State{}) {
			t.Errorf("%s: %+v %v", name, st, err)
		}
		if errors.Is(err, leadunit.ErrUnavailable) {
			t.Errorf("%s: unparsable output is not 'unavailable'", name)
		}
	}
}

func TestInspectToolFailureIsUnavailable(t *testing.T) {
	r := newRig(t)
	r.showFn = func() (string, error) { return "Failed to connect to bus\n", exitErr() }
	_, err := r.m.Inspect(context.Background(), unitName)
	if !errors.Is(err, leadunit.ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestInspectContextDone(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.m.Inspect(ctx, unitName); !errors.Is(err, context.Canceled) || len(r.run.recorded()) != 0 {
		t.Fatal(err)
	}
	// Ending during the call reports the caller's context, not 'unavailable'.
	run := &blockingRunner{}
	m := newManager(run, newFakeKernel(), 0)
	ctx2 := deadlineCtx(t, 20*time.Millisecond)
	if _, err := m.Inspect(ctx2, unitName); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, leadunit.ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestInspectRefusesAnInvalidUnitName(t *testing.T) {
	r := newRig(t)
	for _, name := range []string{"", "x", "a b.service", "-.slice", "../x.service", "x;y.service"} {
		if _, err := r.m.Inspect(context.Background(), name); !errors.Is(err, leadunit.ErrInvalidSpec) {
			t.Errorf("%q: %v", name, err)
		}
	}
	if len(r.run.recorded()) != 0 {
		t.Fatal("a tool was run for an invalid name")
	}
}

func TestInspectForeignUnitIsNotOurs(t *testing.T) {
	cases := map[string]map[string]string{
		"other description":          with(loadedProps(), "Description", "my own service"),
		"description of a unit file": with(loadedProps(), "Transient", "no"),
		"lenient epoch":              with(loadedProps(), "Description", "ataniq lead epoch=03 intent=x"),
		"no intent":                  with(loadedProps(), "Description", "ataniq lead epoch=3"),
	}
	for name, kv := range cases {
		r := newRig(t)
		r.setUnit(kv)
		st, err := r.m.Inspect(context.Background(), unitName)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Ours || st.Epoch != 0 || st.Intent != "" || !st.Loaded || st.InvocationID != inv1 {
			t.Errorf("%s: %+v", name, st)
		}
	}
}

func TestInspectLoadedUnitWhoseControlGroupCannotBeRead(t *testing.T) {
	r := newRig(t)
	r.k.lookupErr[cgUnit] = errors.New("permission denied")
	if st, err := r.m.Inspect(context.Background(), unitName); err == nil || st.Loaded {
		t.Fatalf("%+v %v", st, err)
	}
	r = newRig(t)
	delete(r.k.groups, cgUnit)
	if st, err := r.m.Inspect(context.Background(), unitName); err == nil || st.Loaded {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestInspectLoadedUnitWithoutAControlGroup(t *testing.T) {
	r := newRig(t)
	r.setUnit(with(loadedProps(), "ActiveState", "inactive", "ControlGroup", "", "MainPID", "0"))
	st, err := r.m.Inspect(context.Background(), unitName)
	if err != nil || !st.Loaded || st.ControlGroup != "" || st.ControlGroupID != "" || st.Active != leadunit.ActiveInactive {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestInspectBootIDFailure(t *testing.T) {
	for _, kv := range []map[string]string{loadedProps(), notFoundProps()} {
		r := newRig(t)
		r.setUnit(kv)
		r.k.bootErr = errBoom
		if _, err := r.m.Inspect(context.Background(), unitName); err == nil {
			t.Fatal("no error for a missing boot identity")
		}
	}
}

func TestInspectUnknownActiveStateIsActiveUnknown(t *testing.T) {
	r := newRig(t)
	r.setUnit(with(loadedProps(), "ActiveState", "reloading"))
	st, err := r.m.Inspect(context.Background(), unitName)
	if err != nil || st.Active != leadunit.ActiveUnknown {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestGroupIDRoundTrip(t *testing.T) {
	id, ok := parseGroupID(groupID(idA))
	if !ok || id != idA {
		t.Fatal(id, ok)
	}
	for _, s := range []string{"", "1:2", "1:2:3:4", "a:b:c", "1:2:-3", "01:2:3", "1:2:99999999999"} {
		if _, ok := parseGroupID(s); ok {
			t.Errorf("%q parsed", s)
		}
	}
}

func TestSignalGolden(t *testing.T) {
	for sig, name := range map[syscall.Signal]string{syscall.SIGTERM: "SIGTERM", syscall.SIGKILL: "SIGKILL"} {
		r := newRig(t)
		st, err := r.m.Inspect(context.Background(), unitName)
		if err != nil {
			t.Fatal(err)
		}
		before := len(r.run.recorded())
		if err := r.m.Signal(context.Background(), st, sig); err != nil {
			t.Fatal(err)
		}
		calls := r.run.recorded()[before:]
		mustEqual(t, calls, [][]string{
			{"systemctl", "--user", "show", "--all", "--property=" + showProperties, "--", unitName},
			{"systemctl", "--user", "kill", "--kill-whom=all", "--signal=" + name, "--", unitName},
		})
	}
}

func TestSignalRefusesOtherSignals(t *testing.T) {
	r := newRig(t)
	st, _ := r.m.Inspect(context.Background(), unitName)
	n := len(r.run.recorded())
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGUSR1, syscall.SIGSTOP, 0, 64} {
		if err := r.m.Signal(context.Background(), st, sig); !errors.Is(err, ErrUnsupportedSignal) {
			t.Errorf("%d: %v", sig, err)
		}
	}
	if len(r.run.recorded()) != n {
		t.Fatal("a tool was run for a refused signal")
	}
}

func TestSignalNotLoaded(t *testing.T) {
	r := newRig(t)
	st, _ := r.m.Inspect(context.Background(), unitName)
	r.setUnit(notFoundProps())
	err := r.m.Signal(context.Background(), st, syscall.SIGTERM)
	if !errors.Is(err, leadunit.ErrNotLoaded) {
		t.Fatal(err)
	}
	for _, c := range r.run.recorded() {
		if c[2] == "kill" {
			t.Fatal("kill sent to a unit that is not loaded")
		}
	}
}

func TestSignalInvocationChanged(t *testing.T) {
	r := newRig(t)
	st, _ := r.m.Inspect(context.Background(), unitName)
	r.setUnit(with(loadedProps(), "InvocationID", inv2))
	if err := r.m.Signal(context.Background(), st, syscall.SIGKILL); !errors.Is(err, leadunit.ErrInvocationChanged) {
		t.Fatal(err)
	}
	// A State with no invocation id never signals.
	r = newRig(t)
	if err := r.m.Signal(context.Background(), leadunit.State{Unit: unitName}, syscall.SIGKILL); !errors.Is(err, leadunit.ErrInvocationChanged) {
		t.Fatal(err)
	}
	// A loaded unit that reports no invocation is not matched either.
	r = newRig(t)
	r.setUnit(with(loadedProps(), "InvocationID", ""))
	if err := r.m.Signal(context.Background(), leadunit.State{Unit: unitName, InvocationID: inv1}, syscall.SIGKILL); !errors.Is(err, leadunit.ErrInvocationChanged) {
		t.Fatal(err)
	}
	for _, c := range r.run.recorded() {
		if c[2] == "kill" {
			t.Fatal("kill sent")
		}
	}
}

func TestSignalControlGroupIdentityMismatch(t *testing.T) {
	r := newRig(t)
	st, _ := r.m.Inspect(context.Background(), unitName)
	st.ControlGroupID = groupID(idB)
	if err := r.m.Signal(context.Background(), st, syscall.SIGKILL); !errors.Is(err, leadunit.ErrControlGroupMismatch) {
		t.Fatal(err)
	}
}

func TestSignalKillFailures(t *testing.T) {
	r := newRig(t)
	st, _ := r.m.Inspect(context.Background(), unitName)
	r.kill = func() (string, error) {
		return "Failed to kill unit " + unitName + ": Unit " + unitName + " not loaded.\n", exitErr()
	}
	if err := r.m.Signal(context.Background(), st, syscall.SIGTERM); !errors.Is(err, leadunit.ErrNotLoaded) {
		t.Fatal(err)
	}
	r.kill = func() (string, error) { return "boom\n", exitErr() }
	err := r.m.Signal(context.Background(), st, syscall.SIGTERM)
	if err == nil || errors.Is(err, leadunit.ErrNotLoaded) {
		t.Fatal(err)
	}
}

func TestSignalInspectFailure(t *testing.T) {
	r := newRig(t)
	st, _ := r.m.Inspect(context.Background(), unitName)
	r.showFn = func() (string, error) { return "", errBoom }
	err := r.m.Signal(context.Background(), st, syscall.SIGTERM)
	if !errors.Is(err, leadunit.ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestSignalContextDone(t *testing.T) {
	r := newRig(t)
	st, _ := r.m.Inspect(context.Background(), unitName)
	n := len(r.run.recorded())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.m.Signal(ctx, st, syscall.SIGTERM); !errors.Is(err, context.Canceled) || len(r.run.recorded()) != n {
		t.Fatal(err)
	}
}

func TestSignalInvalidUnitName(t *testing.T) {
	r := newRig(t)
	err := r.m.Signal(context.Background(), leadunit.State{Unit: "a b.service", InvocationID: inv1}, syscall.SIGTERM)
	if !errors.Is(err, leadunit.ErrInvalidSpec) || len(r.run.recorded()) != 0 {
		t.Fatal(err)
	}
}
