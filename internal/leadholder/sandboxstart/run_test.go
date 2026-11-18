//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
)

func stepNames(recs []StepRecord) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.Name)
	}
	return out
}

func stepStatus(t *testing.T, recs []StepRecord, name string) StepStatus {
	t.Helper()
	for _, r := range recs {
		if r.Name == name {
			return r.Status
		}
	}
	t.Fatalf("no step %s in %v", name, stepNames(recs))
	return ""
}

func resultOf(t *testing.T, rs []assert.Result, number int) assert.Result {
	t.Helper()
	for _, r := range rs {
		if r.Number == number {
			return r
		}
	}
	t.Fatalf("no result for assertion %d", number)
	return assert.Result{}
}

// TestRunStepsInTheHarnessOrder runs a start that succeeds and checks that the
// steps ran in the harness's order, with its names, and that the system was
// called in that order.
func TestRunStepsInTheHarnessOrder(t *testing.T) {
	w := newWorld(t)
	st, err := w.run_()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stepNames(st.Steps), StepNames(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	for _, r := range st.Steps {
		want := StepOK
		if r.Name == StepStop {
			want = StepSkipped
		}
		if r.Status != want {
			t.Errorf("step %s is %s (%s), want %s", r.Name, r.Status, r.Error, want)
		}
	}
	wantCalls := []string{
		"CheckHost", "ToolVersions", "CheckOwnClone", "PrepareMountPoints", "CheckMountPoints",
		"DeriveResolv", "MakeBound", "WriteResolv", "ResolveSources", "OpenReport", "NewCanary",
		"WriteSpec", "CheckMountPoints", "ResolveSources", "CheckBindSources", "ReadLine", "Trailing",
		"Channel.Close", "Bound.Close",
	}
	if got := w.sys.callList(); strings.Join(got, " ") != strings.Join(wantCalls, " ") {
		t.Fatalf("calls = %v\nwant    %v", got, wantCalls)
	}
	if st.Child == nil || w.lch.launches() != 1 {
		t.Fatalf("child %v after %d launches", st.Child, w.lch.launches())
	}
	k := w.lch.kids[0]
	if len(k.stops) != 0 || k.closes != 0 {
		t.Errorf("the child of a start that succeeded was stopped %v / closed %d times: it is the holder's", k.stops, k.closes)
	}
	// What is launched is the plan's argument vector.
	if strings.Join(w.lch.argv[0], "\x00") != strings.Join(st.Plan.Argv, "\x00") {
		t.Errorf("the launcher got %v, the plan says %v", w.lch.argv[0], st.Plan.Argv)
	}
	if st.ProfileVersion != profile.Version || len(st.ProfileDigest) != 64 || st.ProfileDigest != st.Plan.Digest {
		t.Errorf("profile %d %q, plan digest %q", st.ProfileVersion, st.ProfileDigest, st.Plan.Digest)
	}
	// Nothing the sequence opened is left open.
	assertNothingOpen(t, w)
}

// assertNothingOpen checks that every folder, channel and canary the fake
// system handed out was closed, and exactly once by the sequence.
func assertNothingOpen(t *testing.T, w *world) {
	t.Helper()
	for i, b := range w.sys.bounds {
		if b.closes != 1 {
			t.Errorf("bound folder %d closed %d times", i, b.closes)
		}
	}
	for i, c := range w.sys.channels {
		if c.closes != 1 {
			t.Errorf("report channel %d closed %d times", i, c.closes)
		}
	}
	for i, c := range w.sys.canaries {
		if !c.Stopped() {
			t.Errorf("canary %d is still open", i)
		}
	}
}

// TestRunSkipsPrepareWithoutTheStatement shows the mount points are made only
// when the inputs state that no lead has run, and that the walk is then not
// proof that no earlier instance remains.
func TestRunSkipsPrepareWithoutTheStatement(t *testing.T) {
	w := newWorld(t)
	w.in.NoLeadHasRun = false
	_, err := w.run_()
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepOutside {
		t.Fatalf("error %v, want a refusal at %s (assertion 7 is not checked without the statement)", err, StepOutside)
	}
	if !strings.Contains(err.Error(), "assertion 7") {
		t.Errorf("error does not name assertion 7: %v", err)
	}
	if stepStatus(t, se.Steps, StepPrepare) != StepSkipped {
		t.Errorf("prepare is %s", stepStatus(t, se.Steps, StepPrepare))
	}
	for _, c := range w.sys.callList() {
		if c == "PrepareMountPoints" {
			t.Error("the mount points were made without the statement")
		}
	}
}

// TestRunSecondEpoch shows the start of an epoch after the first: no statement
// that no lead has run, so the mount points are checked and not made, and the
// caller's statement that the previous instance has stopped is what assertion 7
// rests on.
func TestRunSecondEpoch(t *testing.T) {
	w := newWorld(t)
	w.in.NoLeadHasRun, w.in.PreviousInstanceStopped = false, true
	st, err := w.run_()
	if err != nil {
		t.Fatal(err)
	}
	if stepStatus(t, st.Steps, StepPrepare) != StepSkipped {
		t.Errorf("prepare is %s", stepStatus(t, st.Steps, StepPrepare))
	}
	for _, c := range w.sys.callList() {
		if c == "PrepareMountPoints" {
			t.Error("the mount points were made for a second epoch")
		}
	}
	seven := resultOf(t, st.Results, 7)
	if seven.Status != assert.StatusOK {
		t.Errorf("assertion 7 (outside half) is %s: %s", seven.Status, seven.Detail)
	}
	var outside7 assert.Result
	for _, r := range st.Results {
		if r.ID == assert.IDTreeGitReadOnly+assert.OutsideSuffix {
			outside7 = r
		}
	}
	if outside7.Status != assert.StatusOK || !strings.Contains(outside7.Detail, "PreviousInstanceStopped") {
		t.Errorf("the outside half of assertion 7 is %s: %s", outside7.Status, outside7.Detail)
	}
}

// TestRunPassesWhatTheHolderOwns checks the role lock and lingering are the
// system's, asked for the layout role and the user.
func TestRunPassesWhatTheHolderOwns(t *testing.T) {
	w := newWorld(t)
	if _, err := w.run_(); err != nil {
		t.Fatal(err)
	}
	if w.sys.lock.asked != sampleRole {
		t.Errorf("the role lock was asked about %q, want %q", w.sys.lock.asked, sampleRole)
	}
	if w.sys.linger.asked != "someone" {
		t.Errorf("lingering was asked about %q, want the system's user", w.sys.linger.asked)
	}
	// An owner login name in the inputs wins.
	w2 := newWorld(t)
	w2.in.Identity, w2.in.OwnerUser = string(profile.IdentityOwner), "owner-login"
	if _, err := w2.run_(); err != nil {
		t.Fatal(err)
	}
	if w2.sys.linger.asked != "owner-login" {
		t.Errorf("lingering was asked about %q, want the inputs' owner", w2.sys.linger.asked)
	}
}

// TestRunFailsWithoutTheLock shows a role lock that is not held fails assertion
// 9, waiver or not: Run asks the lock, it never takes it.
func TestRunFailsWithoutTheLock(t *testing.T) {
	w := newWorld(t)
	w.sys.lock.held = false
	_, err := w.run_()
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepOutside || !strings.Contains(err.Error(), "assertion 9") {
		t.Fatalf("error %v, want a refusal at %s on assertion 9", err, StepOutside)
	}
}

// TestRunPinnedDigest checks the pin: a digest that is not the plan's refuses
// the start at verify-plan, before the launch, and the right one passes.
func TestRunPinnedDigest(t *testing.T) {
	w := newWorld(t)
	st, err := w.run_()
	if err != nil {
		t.Fatal(err)
	}
	// The digest does not depend on where the host keeps its files, so the
	// digest of one start pins another's.
	w2 := newWorld(t)
	w2.in.ExpectedDigest = st.ProfileDigest
	if _, err := w2.run_(); err != nil {
		t.Fatalf("the digest of the same profile was refused: %v", err)
	}
	w3 := newWorld(t)
	w3.in.ExpectedDigest = strings.Repeat("0", 64)
	_, err = w3.run_()
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepVerify || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("error %v, want a refusal at %s naming the pin", err, StepVerify)
	}
	if w3.lch.launches() != 0 {
		t.Error("a plan with another digest than the pin was launched")
	}
	// Not 64 hex digits: refused at the first step.
	w4 := newWorld(t)
	w4.in.ExpectedDigest = "abc"
	if _, err := w4.run_(); !errors.As(err, &se) || se.Step != StepValidate {
		t.Fatalf("error %v, want a refusal at %s", err, StepValidate)
	}
}

// TestRunRefusesAnOldBubblewrap shows the release check of the harness.
func TestRunRefusesAnOldBubblewrap(t *testing.T) {
	w := newWorld(t)
	w.sys.versions.Bwrap = "bubblewrap 0.9.0"
	_, err := w.run_()
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepVersions || !strings.Contains(err.Error(), "older than") {
		t.Fatalf("error %v, want a refusal at %s", err, StepVersions)
	}
	// A line that gives no release decides nothing (the launch goes on).
	w2 := newWorld(t)
	w2.sys.versions.Bwrap = "something else"
	if _, err := w2.run_(); err != nil {
		t.Fatalf("an unreadable release refused the start: %v", err)
	}
	for _, c := range []struct {
		line string
		ok   bool
	}{{"bubblewrap 0.10.0", true}, {"bubblewrap 0.13.1", true}, {"bubblewrap 1.0", true}, {"bubblewrap 0.9.9", false}, {"bubblewrap 0.10", true}} {
		_, err := checkBwrapBindFD(c.line)
		if (err == nil) != c.ok {
			t.Errorf("%q: error %v, want ok=%v", c.line, err, c.ok)
		}
	}
}

// TestRunRefusesAnInterruptedStart shows an ended context stops the sequence
// before the next step and leaves nothing behind.
func TestRunRefusesAnInterruptedStart(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, w.in, w.sys, w.lch)
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepValidate || !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want a refusal at %s wrapping the cancellation", err, StepValidate)
	}
	if len(w.sys.callList()) != 0 {
		t.Errorf("a cancelled start made calls: %v", w.sys.callList())
	}
}

// TestRunNeedsASystemAndALauncher shows the two seams are required.
func TestRunNeedsASystemAndALauncher(t *testing.T) {
	w := newWorld(t)
	if _, err := Run(context.Background(), w.in, nil, w.lch); err == nil {
		t.Error("no system was accepted")
	}
	if _, err := Run(context.Background(), w.in, w.sys, nil); err == nil {
		t.Error("no launcher was accepted")
	}
	if len(w.sys.callList()) != 0 {
		t.Errorf("calls %v", w.sys.callList())
	}
}

// TestRunRefusesBeforeAnyStep shows the three refusals of canary mode that come
// before any step: a project without the reserved prefix, a default root, and a
// root without the marker. No step runs and nothing is called.
func TestRunRefusesBeforeAnyStep(t *testing.T) {
	cases := []struct {
		name string
		code canaryroot.Code
		set  func(w *world)
	}{
		{"no prefix", canaryroot.CodeNotThrowaway, func(w *world) { w.in.Project = "plain-one" }},
		{"default root", canaryroot.CodeDefaultRoot, func(w *world) {
			xdg := w.base + "/xdg"
			mkdir(t, xdg+"/ataniq", 0o700)
			w.sys.env = map[string]string{"XDG_STATE_HOME": xdg}
			w.in.StateRoot = xdg + "/ataniq"
		}},
		{"default runtime root", canaryroot.CodeDefaultRoot, func(w *world) {
			xdg := w.base + "/xrun"
			mkdir(t, xdg+"/ataniq", 0o700)
			w.sys.env = map[string]string{"XDG_RUNTIME_DIR": xdg}
			w.in.RuntimeRoot = xdg + "/ataniq"
		}},
		{"no marker", canaryroot.CodeNoMarker, func(w *world) {
			other := w.base + "/other"
			mkdir(t, other, 0o700)
			w.in.StateRoot = other
		}},
		{"nested roots", canaryroot.CodeNestedRoot, func(w *world) {
			mkdir(t, w.state+"/run", 0o700)
			w.in.RuntimeRoot = w.state + "/run"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			c.set(w)
			_, err := w.run_()
			var re *canaryroot.RefusedError
			if !errors.As(err, &re) || re.Code != c.code {
				t.Fatalf("error %v, want a root refusal with code %s", err, c.code)
			}
			var se *StepError
			if errors.As(err, &se) {
				t.Errorf("the refusal came from step %s: it must come before any", se.Step)
			}
			if calls := w.sys.callList(); len(calls) != 0 {
				t.Errorf("calls before any step: %v", calls)
			}
			if w.lch.launches() != 0 || len(w.sys.bounds)+len(w.sys.channels)+len(w.sys.canaries) != 0 {
				t.Error("something was launched or opened")
			}
		})
	}
}

// TestRunWithoutCanaryChecksNoRoot shows that with canary false the root
// check is not made here: a project without the prefix and a root without the
// marker go on to the steps (and fail at assertion 10, see the waiver tests).
func TestRunWithoutCanaryChecksNoRoot(t *testing.T) {
	w := newWorld(t)
	w.in.Canary = false
	w.in.Project = "plain-one"
	// The marker is for the other project; with canary false nobody reads it.
	_, err := w.run_()
	var re *canaryroot.RefusedError
	if errors.As(err, &re) {
		t.Fatalf("a root check was made without canary: %v", err)
	}
	var se *StepError
	if !errors.As(err, &se) {
		t.Fatalf("error %v, want a step refusal", err)
	}
	if len(w.sys.callList()) == 0 {
		t.Error("no step ran")
	}
}
