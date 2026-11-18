//go:build linux && ataniq_canary

package sandboxstart

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadsandbox/assert"
)

// TestWaiverInCanaryMode shows the start that succeeds with the waiver: Started
// says so, carries the text in full, and keeps the true result of assertion 10.
func TestWaiverInCanaryMode(t *testing.T) {
	w := newWorld(t)
	st, err := w.run_()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Waived || st.Waiver != canaryroot.WaiverText {
		t.Fatalf("waived %v, waiver %q", st.Waived, st.Waiver)
	}
	if st.Waiver != "assertion 10 waived: canary slice, no qualification record" {
		t.Errorf("the waiver's wording changed: %q", st.Waiver)
	}
	// Every assertion has a result, and the one of 10 is what the check found,
	// not the waiver's: it is not ok.
	if len(st.Results) != assert.NumAssertions+4 {
		t.Errorf("%d results, want one for each assertion and the four outside halves (%d)", len(st.Results), assert.NumAssertions+4)
	}
	ten := resultOf(t, st.Results, 10)
	if ten.Status != assert.StatusNotChecked {
		t.Errorf("assertion 10 is %s (%s): the waiver must not change a result", ten.Status, ten.Detail)
	}
	for _, r := range st.Results {
		if r.Number != 10 && r.Status != assert.StatusOK {
			t.Errorf("assertion %d (%s) is %s: %s", r.Number, r.ID, r.Status, r.Detail)
		}
	}
}

// TestNoWaiverWithoutCanary shows that with canary false the waiver is not
// applied and the start fails at assertion 10, and only there.
func TestNoWaiverWithoutCanary(t *testing.T) {
	w := newWorld(t)
	w.in.Canary = false
	st, err := w.run_()
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepOutside {
		t.Fatalf("error %v, want a refusal at %s", err, StepOutside)
	}
	if st.Waived || st.Waiver != "" || st.Child != nil {
		t.Errorf("a failed start returned %+v", st)
	}
	msg := err.Error()
	if !strings.Contains(msg, "assertion 10 (qualification-record): not checked") {
		t.Errorf("error does not name assertion 10: %v", err)
	}
	for n := 1; n <= 11; n++ {
		if n != 10 && strings.Contains(msg, fmt.Sprintf("assertion %d (", n)) {
			t.Errorf("error names assertion %d, which holds: %v", n, err)
		}
	}
	if strings.Contains(msg, "waived") {
		t.Errorf("the error speaks of a waiver: %v", err)
	}
	// The child was launched (the probe's report is read after the launch) and is
	// stopped and reaped.
	k := w.lch.kids[0]
	if len(k.stops) != 1 || k.closes != 1 {
		t.Errorf("child stopped %v times, closed %d times", k.stops, k.closes)
	}
	assertNothingOpen(t, w)
}

// TestWaiverExcusesOnlyAssertionTen makes each other assertion fail, and then
// not be checked, in canary mode: the waiver excuses none of them.
func TestWaiverExcusesOnlyAssertionTen(t *testing.T) {
	type mutation struct {
		name   string
		number int
		set    func(w *world)
	}
	var cases []mutation
	// The inside half of every assertion the probe reports.
	for _, a := range assert.Assertions() {
		if !a.HasInside() {
			continue
		}
		for _, st := range []assert.Status{assert.StatusFailed, assert.StatusNotChecked} {
			a, st := a, st
			cases = append(cases, mutation{fmt.Sprintf("inside %d %s", a.Number, st), a.Number, func(w *world) {
				w.sys.probeStatus[a.Number] = st
			}})
		}
	}
	cases = append(cases,
		mutation{"outside 1: a holder path is a source", 1, func(w *world) {
			w.in.HolderPaths = []string{w.in.Sources.LeadSkills}
		}},
		mutation{"outside 3: the canary was reached", 3, func(w *world) { w.sys.hitCanary = true }},
		mutation{"outside 5: the sysctl is 1", 5, func(w *world) { w.sys.sysctl = "1\n" }},
		mutation{"outside 5: the sysctl is unreadable", 5, func(w *world) { w.sys.sysctl = "" }},
		mutation{"outside 7: no statement that no lead has run", 7, func(w *world) { w.in.NoLeadHasRun = false }},
		mutation{"outside 9: the lock is not held", 9, func(w *world) { w.sys.lock.held = false }},
		mutation{"outside 9: the lock cannot be asked", 9, func(w *world) { w.sys.lock.err = errors.New("no lock") }},
		mutation{"outside 9: lingering is off", 9, func(w *world) { w.sys.linger.on = false }},
		mutation{"outside 9: lingering cannot be asked", 9, func(w *world) { w.sys.linger.err = errors.New("no login manager") }},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			c.set(w)
			st, err := w.run_()
			if err == nil {
				t.Fatalf("the start went through with assertion %d broken (waived %v)", c.number, st.Waived)
			}
			var se *StepError
			if !errors.As(err, &se) {
				t.Fatalf("error %v is no step refusal", err)
			}
			// An assertion of the table is named in the refusal, by the check
			// that found it: at outside-checks for the judgement, or at an
			// earlier step for a check that the sequence makes sooner.
			if se.Step == StepOutside && !strings.Contains(err.Error(), fmt.Sprintf("assertion %d (", c.number)) {
				t.Errorf("the refusal does not name assertion %d: %v", c.number, err)
			}
			if st.Waived {
				t.Error("a refused start says the waiver was applied")
			}
			if w.lch.launches() == 1 {
				if k := w.lch.kids[0]; len(k.stops) != 1 || k.closes != 1 {
					t.Errorf("child stopped %v, closed %d", k.stops, k.closes)
				}
			}
			assertNothingOpen(t, w)
		})
	}
}

// specOf is the spec a start of the world wrote.
func specOf(t *testing.T, w *world) assert.Spec {
	t.Helper()
	if _, err := w.run_(); err != nil {
		t.Fatal(err)
	}
	spec, err := assert.DecodeSpecBytes(w.sys.bounds[0].spec)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// fullReport is the whole report of a start that succeeded, as a test can
// change it.
func fullReport(t *testing.T, w *world, spec assert.Spec) assert.Report {
	t.Helper()
	st, err := w.run_()
	if err != nil {
		t.Fatal(err)
	}
	return assert.Report{Version: assert.ReportVersion, Digest: spec.Digest, Results: st.Results}
}

// TestJudge tests the judgement on its own, on reports built from a real start.
func TestJudge(t *testing.T) {
	w := newWorld(t)
	spec := specOf(t, w)
	good := fullReport(t, newWorld(t), spec)
	if good.Digest != spec.Digest {
		t.Fatal("test bug: digests differ")
	}
	only10 := func(problems []string) bool {
		for _, p := range problems {
			if !strings.Contains(p, "assertion 10 (") {
				return false
			}
		}
		return len(problems) > 0
	}

	t.Run("canary excuses a not-checked 10", func(t *testing.T) {
		waived, probs := judge(good, spec, true)
		if !waived || len(probs) != 0 {
			t.Fatalf("waived %v, problems %v", waived, probs)
		}
	})
	t.Run("no canary: 10 is the only problem", func(t *testing.T) {
		waived, probs := judge(good, spec, false)
		if waived || !only10(probs) {
			t.Fatalf("waived %v, problems %v", waived, probs)
		}
	})
	t.Run("canary does not excuse a failed 10", func(t *testing.T) {
		rep := withResult(good, 10, assert.StatusFailed)
		waived, probs := judge(rep, spec, true)
		if waived || !only10(probs) {
			t.Fatalf("waived %v, problems %v", waived, probs)
		}
	})
	t.Run("an ok 10 needs no waiver", func(t *testing.T) {
		rep := withResult(good, 10, assert.StatusOK)
		waived, probs := judge(rep, spec, true)
		if waived || len(probs) != 0 {
			t.Fatalf("waived %v, problems %v", waived, probs)
		}
	})
	t.Run("a missing 10 is not excused", func(t *testing.T) {
		rep := good
		rep.Results = nil
		for _, r := range good.Results {
			if r.Number != 10 {
				rep.Results = append(rep.Results, r)
			}
		}
		waived, probs := judge(rep, spec, true)
		if waived || !only10(probs) {
			t.Fatalf("waived %v, problems %v", waived, probs)
		}
	})
	t.Run("two results of 10 are not excused", func(t *testing.T) {
		rep := good
		rep.Results = append(append([]assert.Result(nil), good.Results...), resultOf(t, good.Results, 10))
		waived, probs := judge(rep, spec, true)
		if waived || !only10(probs) {
			t.Fatalf("waived %v, problems %v", waived, probs)
		}
	})
	t.Run("another digest is not excused", func(t *testing.T) {
		rep := good
		rep.Digest = strings.Repeat("1", 64)
		_, probs := judge(rep, spec, true)
		if len(probs) == 0 {
			t.Fatal("a report for another digest was accepted")
		}
	})
	t.Run("the waiver changes no result of the caller", func(t *testing.T) {
		rep := withResult(good, 10, assert.StatusNotChecked)
		judge(rep, spec, true)
		if resultOf(t, rep.Results, 10).Status != assert.StatusNotChecked {
			t.Error("judge changed the report it was given")
		}
	})
	// Every other assertion, in every state but ok, stays a problem in canary
	// mode, and the only problem.
	for _, a := range assert.Assertions() {
		if a.Number == 10 {
			continue
		}
		for _, st := range []assert.Status{assert.StatusFailed, assert.StatusNotChecked} {
			for _, id := range resultIDs(good, a.Number) {
				a, st, id := a, st, id
				t.Run(fmt.Sprintf("%d %s %s", a.Number, id, st), func(t *testing.T) {
					rep := withResultID(good, id, st)
					waived, probs := judge(rep, spec, true)
					if !waived {
						t.Error("the waiver of 10 was not applied")
					}
					if len(probs) != 1 || !strings.Contains(probs[0], fmt.Sprintf("assertion %d (%s)", a.Number, id)) {
						t.Fatalf("problems %v, want exactly assertion %d (%s)", probs, a.Number, id)
					}
				})
			}
		}
	}
}

// resultIDs are the ids of the results of one assertion (the inside and the
// outside half).
func resultIDs(rep assert.Report, number int) []string {
	var out []string
	for _, r := range rep.Results {
		if r.Number == number {
			out = append(out, r.ID)
		}
	}
	return out
}

func withResult(rep assert.Report, number int, st assert.Status) assert.Report {
	out := rep
	out.Results = append([]assert.Result(nil), rep.Results...)
	for i := range out.Results {
		if out.Results[i].Number == number {
			out.Results[i].Status = st
		}
	}
	return out
}

func withResultID(rep assert.Report, id string, st assert.Status) assert.Report {
	out := rep
	out.Results = append([]assert.Result(nil), rep.Results...)
	for i := range out.Results {
		if out.Results[i].ID == id {
			out.Results[i].Status = st
		}
	}
	return out
}
