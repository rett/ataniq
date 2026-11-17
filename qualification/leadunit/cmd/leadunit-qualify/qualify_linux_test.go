//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadunit"
)

// These tests drive the check logic against the in-memory leadunit.Fake. No
// unit is started and systemd is not touched.

const testSuffix = "0123456789abcdef"

type fakeLife struct{ *leadunit.Fake }

func (f fakeLife) Populated(_ context.Context, st leadunit.State) (bool, error) {
	if !f.Loaded(st.Unit) {
		return false, errors.New("control group gone")
	}
	return len(f.Processes(st.Unit)) > 0, nil
}

type qrig struct {
	t         *testing.T
	fake      *leadunit.Fake
	q         *qualifier
	sleeps    []time.Duration
	ready     error
	childArgs func(sent []string) []string // what the child recorded; nil means what it was sent
	randCalls int
}

const freshSuffix = "fedcba9876543210"

func newQRig(t *testing.T) *qrig {
	t.Helper()
	r := &qrig{t: t, fake: leadunit.NewFake(leadunit.NewFakeClock(time.Unix(1000, 0)))}
	r.q = &qualifier{
		mgr:   fakeLife{r.fake},
		sleep: func(_ context.Context, d time.Duration) error { r.sleeps = append(r.sleeps, d); return nil },
		rand: func() (string, error) {
			r.randCalls++
			if r.randCalls == 1 {
				return testSuffix, nil
			}
			return freshSuffix, nil
		},
		exe:      "/usr/bin/leadunit-qualify",
		stateDir: t.TempDir(),
	}
	// The child is not run: the ready hook writes the argument record that the
	// real child writes before its ready file.
	r.q.ready = func(context.Context, string) error {
		if r.ready != nil {
			return r.ready
		}
		specs := r.fake.StartedSpecs()
		if len(specs) == 0 {
			return errors.New("no unit was started")
		}
		got := specs[0].Argv[2:]
		if r.childArgs != nil {
			got = r.childArgs(got)
		}
		b, err := json.Marshal(got)
		if err != nil {
			return err
		}
		return os.WriteFile(r.q.argsPath, append(b, '\n'), 0o600)
	}
	r.fake.SetProcs("ataniq-qualify-"+testSuffix+".service", leadunit.ProcScript{Behavior: leadunit.IgnoreTerm})
	return r
}

func results(rep report) map[string]string {
	m := map[string]string{}
	for _, c := range rep.Checks {
		m[c.Name] = c.Result
	}
	return m
}

func TestQualifyPassesAgainstAWellBehavedManager(t *testing.T) {
	r := newQRig(t)
	rep := r.q.qualify(context.Background())
	if rep.Verdict != resultPass {
		t.Fatalf("%+v", rep)
	}
	var names []string
	for _, c := range rep.Checks {
		if c.Result != resultPass {
			t.Errorf("%+v", c)
		}
		names = append(names, c.Name)
	}
	want := "record-unit-name start unit-active description-read-back child-ready arguments-reach-the-child-unchanged second-start-refused inspect-matches sigterm-leaves-group-populated sigkill-sent empty-true unit-not-loaded empty-true-for-a-fresh-name cleanup"
	if strings.Join(names, " ") != want {
		t.Fatalf("checks %v", names)
	}
	if rep.Unit != "ataniq-qualify-"+testSuffix+".service" || rep.Intent != "q-"+testSuffix || len(rep.NotAttempted) != 1 || !strings.Contains(rep.NotAttempted[0], "lost start reply") {
		t.Fatalf("%+v", rep)
	}
	if r.fake.Loaded(rep.Unit) {
		t.Fatal("the unit is still loaded")
	}
	// The 2 s between SIGTERM and SIGKILL.
	found := false
	for _, d := range r.sleeps {
		if d == 2*time.Second {
			found = true
		}
	}
	if !found {
		t.Fatalf("no 2 s wait: %v", r.sleeps)
	}
	b, err := os.ReadFile(filepath.Join(r.q.stateDir, "unit-"+testSuffix))
	if err != nil || string(b) != rep.Unit+"\n" {
		t.Fatalf("unit record %q %v", b, err)
	}
	specs := r.fake.StartedSpecs()
	if len(specs) != 1 || specs[0].Argv[1] != "child" || specs[0].Argv[2] != "--ignore-term" || specs[0].Epoch != 1 || len(specs[0].Env) != 1 {
		t.Fatalf("%+v", specs)
	}
	calls := r.fake.CallsOf(leadunit.MethodStart)
	if len(calls) != 2 || !errors.Is(calls[1].Err, leadunit.ErrUnitExists) {
		t.Fatalf("starts: %+v", calls)
	}
}

func TestQualifyChildThatDiesOfTermFailsAndCleansUp(t *testing.T) {
	r := newQRig(t)
	r.fake.SetProcs("ataniq-qualify-"+testSuffix+".service", leadunit.ProcScript{Behavior: leadunit.ExitOnTerm})
	rep := r.q.qualify(context.Background())
	res := results(rep)
	if rep.Verdict != resultFail || res["sigterm-leaves-group-populated"] != resultFail || res["sigkill-sent"] != resultSkipped || res["empty-true"] != resultSkipped {
		t.Fatalf("%+v", rep)
	}
	if res["cleanup"] != resultPass || r.fake.Loaded(rep.Unit) {
		t.Fatalf("%+v", rep)
	}
}

func TestQualifyGroupThatKillCannotEmptyFailsAndSaysItCouldNotProve(t *testing.T) {
	r := newQRig(t)
	r.fake.SetProcs("ataniq-qualify-"+testSuffix+".service", leadunit.ProcScript{Behavior: leadunit.SurviveKill})
	rep := r.q.qualify(context.Background())
	res := results(rep)
	if rep.Verdict != resultFail || res["empty-true"] != resultFail || res["unit-not-loaded"] != resultSkipped || res["cleanup"] != resultFail {
		t.Fatalf("%+v", rep)
	}
	last := rep.Checks[len(rep.Checks)-1]
	if !strings.Contains(last.Detail, "could not be proven empty") {
		t.Fatalf("%+v", last)
	}
}

func TestQualifyLostReplyIsAFailureAndTheUnitIsStillCleanedUp(t *testing.T) {
	r := newQRig(t)
	unit := "ataniq-qualify-" + testSuffix + ".service"
	r.fake.LoseNextStartReply(unit)
	rep := r.q.qualify(context.Background())
	res := results(rep)
	if rep.Verdict != resultFail || res["start"] != resultFail || res["unit-active"] != resultSkipped || res["cleanup"] != resultPass {
		t.Fatalf("%+v", rep)
	}
	if r.fake.Loaded(unit) {
		t.Fatal("the unit whose reply was lost was left running")
	}
}

func TestQualifyManagerUnavailableStartedNothing(t *testing.T) {
	r := newQRig(t)
	r.fake.MakeUnavailable(leadunit.MethodStart, 1)
	rep := r.q.qualify(context.Background())
	res := results(rep)
	if rep.Verdict != resultFail || res["start"] != resultFail || res["cleanup"] != resultPass {
		t.Fatalf("%+v", rep)
	}
	last := rep.Checks[len(rep.Checks)-1]
	if last.Detail != "nothing was started" || len(r.fake.Calls()) != 1 {
		t.Fatalf("%+v %v", last, r.fake.Calls())
	}
}

func TestQualifyChildNeverReadyFailsAndCleansUp(t *testing.T) {
	r := newQRig(t)
	r.ready = errors.New("the child did not write its ready file in time")
	rep := r.q.qualify(context.Background())
	res := results(rep)
	if rep.Verdict != resultFail || res["child-ready"] != resultFail || res["second-start-refused"] != resultSkipped || res["cleanup"] != resultPass {
		t.Fatalf("%+v", rep)
	}
	if r.fake.Loaded(rep.Unit) {
		t.Fatal("left running")
	}
}

func TestQualifyCleansUpAfterTheContextIsCancelled(t *testing.T) {
	r := newQRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.q.ready = func(context.Context, string) error { cancel(); return ctx.Err() }
	rep := r.q.qualify(ctx)
	res := results(rep)
	if rep.Verdict != resultFail || res["child-ready"] != resultFail || res["cleanup"] != resultPass || r.fake.Loaded(rep.Unit) {
		t.Fatalf("%+v", rep)
	}
}

func TestQualifyUnitNameIsNeverARoleName(t *testing.T) {
	r := newQRig(t)
	rep := r.q.qualify(context.Background())
	if !strings.HasPrefix(rep.Unit, "ataniq-qualify-") || strings.HasPrefix(rep.Unit, "ataniq-lead-") || strings.HasPrefix(rep.Unit, "ataniq-supervisor") {
		t.Fatal(rep.Unit)
	}
	if err := leadunit.ValidateUnitName(rep.Unit); err != nil {
		t.Fatal(err)
	}
	// A failing random source fails the run before anything is started.
	r2 := newQRig(t)
	r2.q.rand = func() (string, error) { return "", errors.New("no entropy") }
	rep = r2.q.qualify(context.Background())
	if rep.Verdict != resultFail || len(r2.fake.Calls()) != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestReportIsOneJSONObjectWithALinePerCheck(t *testing.T) {
	r := newQRig(t)
	rep := r.q.qualify(context.Background())
	var buf bytes.Buffer
	if err := writeReport(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var back report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	if back.Verdict != rep.Verdict || len(back.Checks) != len(rep.Checks) || back.Unit != rep.Unit {
		t.Fatalf("%+v", back)
	}
	lines := 0
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, `"name"`) {
			lines++
		}
	}
	if lines != len(rep.Checks) {
		t.Fatalf("%d lines for %d checks:\n%s", lines, len(rep.Checks), buf.String())
	}
	// The report file is written 0600 beside the records.
	if err := r.q.saveReport(rep); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(r.q.stateDir, "report-"+testSuffix+".json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
}

// tweakLife is a lifecycle that changes some answers of the one it wraps.
type tweakLife struct {
	lifecycle
	inspect   func(ctx context.Context, unit string, real func() (leadunit.State, error)) (leadunit.State, error)
	signalErr error // when set, Signal returns it and sends nothing
	empty     func(st leadunit.State) (ok bool, handled bool, err error)
}

func (w *tweakLife) Inspect(ctx context.Context, unit string) (leadunit.State, error) {
	real := func() (leadunit.State, error) { return w.lifecycle.Inspect(ctx, unit) }
	if w.inspect != nil {
		return w.inspect(ctx, unit, real)
	}
	return real()
}

func (w *tweakLife) Signal(ctx context.Context, st leadunit.State, sig syscall.Signal) error {
	if w.signalErr != nil {
		return w.signalErr
	}
	return w.lifecycle.Signal(ctx, st, sig)
}

func (w *tweakLife) Empty(ctx context.Context, st leadunit.State) (bool, error) {
	if w.empty != nil {
		if ok, handled, err := w.empty(st); handled {
			return ok, err
		}
	}
	return w.lifecycle.Empty(ctx, st)
}

// cleanupRig is a qualifier set up as it is after a start whose outcome is
// not known: the unit may exist and nothing was settled.
func cleanupRig(t *testing.T) (*qrig, string) {
	t.Helper()
	r := newQRig(t)
	unit := "ataniq-qualify-" + testSuffix + ".service"
	r.q.unit, r.q.intent, r.q.suffix = unit, "q-"+testSuffix, testSuffix
	r.q.mayExist = true
	return r, unit
}

func signalCalls(r *qrig) int { return len(r.fake.CallsOf(leadunit.MethodSignal)) }

func TestArgumentsReachTheChildUnchanged(t *testing.T) {
	r := newQRig(t)
	rep := r.q.qualify(context.Background())
	if results(rep)["arguments-reach-the-child-unchanged"] != resultPass {
		t.Fatalf("%+v", rep)
	}
	specs := r.fake.StartedSpecs()
	argv := specs[0].Argv
	count := 0
	for _, a := range argv {
		if a == literalDollar || a == literalPercent {
			count++
		}
	}
	if count != 2 || !strings.Contains(literalDollar, "$HOME") || !strings.Contains(literalDollar, "${HOME}") ||
		!strings.Contains(literalPercent, "%h") || !strings.Contains(literalPercent, "%%") {
		t.Fatalf("%q %q %q", argv, literalDollar, literalPercent)
	}
}

func TestChangedArgumentsFailTheCheckAndTheRestIsSkippedButCleanedUp(t *testing.T) {
	for name, change := range map[string]func([]string) []string{
		"expanded": func(a []string) []string {
			b := append([]string(nil), a...)
			for i := range b {
				b[i] = strings.ReplaceAll(b[i], "$HOME", "/home/x")
			}
			return b
		},
		"specifier": func(a []string) []string {
			b := append([]string(nil), a...)
			for i := range b {
				b[i] = strings.ReplaceAll(b[i], "%%", "%")
			}
			return b
		},
		"missing": func(a []string) []string { return a[:len(a)-1] },
		"extra":   func(a []string) []string { return append(append([]string(nil), a...), "x") },
	} {
		r := newQRig(t)
		r.childArgs = change
		rep := r.q.qualify(context.Background())
		res := results(rep)
		if rep.Verdict != resultFail || res["arguments-reach-the-child-unchanged"] != resultFail || res["second-start-refused"] != resultSkipped || res["cleanup"] != resultPass {
			t.Errorf("%s: %+v", name, rep)
		}
		if r.fake.Loaded(rep.Unit) {
			t.Errorf("%s: left running", name)
		}
	}
	// No record at all (the child died before it wrote it) fails too.
	r := newQRig(t)
	ready := r.q.ready
	r.q.ready = func(ctx context.Context, p string) error {
		if err := ready(ctx, p); err != nil {
			return err
		}
		return os.Remove(r.q.argsPath)
	}
	if rep := r.q.qualify(context.Background()); results(rep)["arguments-reach-the-child-unchanged"] != resultFail {
		t.Fatalf("%+v", rep)
	}
}

func TestEmptyOnAFreshNameIsACheck(t *testing.T) {
	r := newQRig(t)
	rep := r.q.qualify(context.Background())
	if results(rep)["empty-true-for-a-fresh-name"] != resultPass {
		t.Fatalf("%+v", rep)
	}
	fresh := "ataniq-qualify-" + freshSuffix + ".service"
	var asked []leadunit.State
	for _, c := range r.fake.CallsOf(leadunit.MethodEmpty) {
		if c.Unit == fresh {
			asked = append(asked, leadunit.State{Unit: c.Unit, InvocationID: c.Invocation})
		}
	}
	if len(asked) != 1 || asked[0].InvocationID != "" {
		t.Fatalf("Empty for the fresh name: %+v", asked)
	}

	// A false answer, an error and a repeated random name each fail it.
	for name, setup := range map[string]func(r *qrig){
		"false": func(r *qrig) {
			r.q.mgr = &tweakLife{lifecycle: r.q.mgr, empty: func(st leadunit.State) (bool, bool, error) {
				return false, st.Unit == fresh, nil
			}}
		},
		"error": func(r *qrig) {
			r.q.mgr = &tweakLife{lifecycle: r.q.mgr, empty: func(st leadunit.State) (bool, bool, error) {
				return false, st.Unit == fresh, errors.New("cannot derive the group of the user manager")
			}}
		},
		"repeated name": func(r *qrig) { r.q.rand = func() (string, error) { return testSuffix, nil } },
	} {
		r := newQRig(t)
		setup(r)
		rep := r.q.qualify(context.Background())
		if rep.Verdict != resultFail || results(rep)["empty-true-for-a-fresh-name"] != resultFail || results(rep)["cleanup"] != resultPass {
			t.Errorf("%s: %+v", name, rep)
		}
	}
}

func TestCleanupNeverSignalsAUnitThatIsNotItsOwnIntent(t *testing.T) {
	for name, desc := range map[string]string{
		"plain":        "some other service",
		"other intent": "ataniq lead epoch=1 intent=somebody-else",
		"other epoch":  "ataniq lead epoch=2 intent=q-" + testSuffix,
	} {
		r, unit := cleanupRig(t)
		if err := r.fake.AddForeign(unit, desc, leadunit.ProcScript{Behavior: leadunit.IgnoreTerm}); err != nil {
			t.Fatal(err)
		}
		c := r.q.cleanup(context.Background())
		if c.Result != resultFail || !strings.Contains(c.Detail, "not signalled") || !strings.Contains(c.Detail, "not this run's intent") {
			t.Errorf("%s: %+v", name, c)
		}
		if signalCalls(r) != 0 || len(r.fake.CallsOf(leadunit.MethodEmpty)) != 0 || !r.fake.Loaded(unit) {
			t.Errorf("%s: the foreign unit was touched: %v", name, r.fake.Calls())
		}
	}
}

func TestCleanupSignalsItsOwnUnitAndSaysSo(t *testing.T) {
	r, unit := cleanupRig(t)
	st, err := r.fake.Start(context.Background(), r.q.spec(r.q.intent))
	if err != nil || !st.Ours {
		t.Fatal(st, err)
	}
	c := r.q.cleanup(context.Background())
	if c.Result != resultPass || !strings.HasPrefix(c.Detail, "SIGKILL sent;") || signalCalls(r) != 1 || r.fake.Loaded(unit) {
		t.Fatalf("%+v %v", c, r.fake.Calls())
	}
}

func TestCleanupSaysWhenNothingWasSent(t *testing.T) {
	// Already gone: nothing sent, and it says so.
	r, _ := cleanupRig(t)
	c := r.q.cleanup(context.Background())
	if c.Result != resultPass || !strings.Contains(c.Detail, "nothing was sent") || strings.Contains(c.Detail, "SIGKILL sent") || signalCalls(r) != 0 {
		t.Fatalf("%+v", c)
	}
	// A signal that fails is not reported as sent, whatever the reason.
	for name, serr := range map[string]error{
		"changed":    leadunit.ErrInvocationChanged,
		"not loaded": leadunit.ErrNotLoaded,
		"other":      errors.New("bus gone"),
	} {
		r, _ := cleanupRig(t)
		if _, err := r.fake.Start(context.Background(), r.q.spec(r.q.intent)); err != nil {
			t.Fatal(err)
		}
		r.q.mgr = &tweakLife{lifecycle: r.q.mgr, signalErr: serr}
		c := r.q.cleanup(context.Background())
		if strings.Contains(c.Detail, "SIGKILL sent") || !strings.Contains(c.Detail, "SIGKILL not sent") {
			t.Errorf("%s: %+v", name, c)
		}
		if c.Result != resultFail || !strings.Contains(c.Detail, "could not be proven empty") {
			t.Errorf("%s: a unit that is still there is reported as cleaned: %+v", name, c)
		}
	}
}

// A run interrupted right after Start can find the unit loaded with no
// invocation id, which Signal refuses.
func TestCleanupWaitsForTheInvocationBeforeSignalling(t *testing.T) {
	r, unit := cleanupRig(t)
	if _, err := r.fake.Start(context.Background(), r.q.spec(r.q.intent)); err != nil {
		t.Fatal(err)
	}
	partial := 0
	r.q.mgr = &tweakLife{lifecycle: r.q.mgr, inspect: func(_ context.Context, _ string, real func() (leadunit.State, error)) (leadunit.State, error) {
		st, err := real()
		if err == nil && partial < 3 {
			partial++
			st.InvocationID, st.ControlGroup, st.ControlGroupID, st.MainPID = "", "", "", 0
			st.Active = leadunit.ActiveActivating
		}
		return st, err
	}}
	c := r.q.cleanup(context.Background())
	if c.Result != resultPass || !strings.HasPrefix(c.Detail, "SIGKILL sent;") || signalCalls(r) != 1 || r.fake.Loaded(unit) {
		t.Fatalf("%+v %v", c, r.fake.Calls())
	}
	if len(r.sleeps) < 3 {
		t.Fatalf("did not wait: %v", r.sleeps)
	}
	if sc := r.fake.CallsOf(leadunit.MethodSignal)[0]; sc.Invocation == "" {
		t.Fatalf("signalled with no invocation: %+v", sc)
	}

	// An invocation that never comes: not signalled, and it says so.
	r, unit = cleanupRig(t)
	if _, err := r.fake.Start(context.Background(), r.q.spec(r.q.intent)); err != nil {
		t.Fatal(err)
	}
	r.q.mgr = &tweakLife{lifecycle: r.q.mgr, inspect: func(_ context.Context, _ string, real func() (leadunit.State, error)) (leadunit.State, error) {
		st, err := real()
		st.InvocationID, st.ControlGroup, st.ControlGroupID, st.MainPID = "", "", "", 0
		return st, err
	}}
	c = r.q.cleanup(context.Background())
	if c.Result != resultFail || !strings.Contains(c.Detail, "not signalled") || strings.Contains(c.Detail, "SIGKILL sent") || signalCalls(r) != 0 || !r.fake.Loaded(unit) {
		t.Fatalf("%+v", c)
	}
}

func TestCleanupInspectFailureIsAFailure(t *testing.T) {
	r, _ := cleanupRig(t)
	r.q.mgr = &tweakLife{lifecycle: r.q.mgr, inspect: func(context.Context, string, func() (leadunit.State, error)) (leadunit.State, error) {
		return leadunit.State{}, errors.New("manager unreachable")
	}}
	c := r.q.cleanup(context.Background())
	if c.Result != resultFail || !strings.Contains(c.Detail, "could not inspect") || signalCalls(r) != 0 {
		t.Fatalf("%+v", c)
	}
}
