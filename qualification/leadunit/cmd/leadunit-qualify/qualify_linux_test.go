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
	t      *testing.T
	fake   *leadunit.Fake
	q      *qualifier
	sleeps []time.Duration
	ready  error
}

func newQRig(t *testing.T) *qrig {
	t.Helper()
	r := &qrig{t: t, fake: leadunit.NewFake(leadunit.NewFakeClock(time.Unix(1000, 0)))}
	r.q = &qualifier{
		mgr:      fakeLife{r.fake},
		sleep:    func(_ context.Context, d time.Duration) error { r.sleeps = append(r.sleeps, d); return nil },
		ready:    func(context.Context, string) error { return r.ready },
		rand:     func() (string, error) { return testSuffix, nil },
		exe:      "/usr/bin/leadunit-qualify",
		stateDir: t.TempDir(),
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
	want := "record-unit-name start unit-active description-read-back child-ready second-start-refused inspect-matches sigterm-leaves-group-populated sigkill-sent empty-true unit-not-loaded cleanup"
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
