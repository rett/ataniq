//go:build linux && ataniq_canary

package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/wire"
)

// waitGateOpen waits until no delivery runs.
func (e *env) waitGateOpen() {
	e.t.Helper()
	poll(e.t, func() string { return "the delivery to end" }, func() bool { return !e.srv.inj.running.Load() })
}

// A deliverer that outlasts the time and then returns has what it returned
// logged as a third inject event: the late outcome, its reason, and the written
// count as the state, with the identity and the digest and never the text.
func TestInjectLateResultIsLogged(t *testing.T) {
	for name, tc := range map[string]struct {
		res                    wire.InjectResult
		outcome, reason, state string
	}{
		"submitted": {submitted(), "late-submitted", "", "written-whole"},
		"not now": {
			wire.InjectResult{Outcome: wire.InjectOutcomeNotNow, Reason: wire.InjectReasonBlocked, Written: wire.InjectWrittenNothing},
			"late-not-now", "blocked", "written-nothing",
		},
		"uncertain": {
			wire.InjectResult{Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonNotSubmitted, Written: wire.InjectWrittenPartial},
			"late-uncertain", "not-submitted", "written-partial",
		},
		"invalid": {wire.InjectResult{Outcome: "delivered", Reason: "other", Written: "all", Detail: canaryMark}, "late-invalid", "", ""},
		"zero":    {wire.InjectResult{}, "late-invalid", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			d := &fakeDeliverer{fn: func(ctx context.Context, _, _ string) wire.InjectResult {
				<-ctx.Done()
				<-release // ignores the end of its context
				return tc.res
			}}
			e := canaryEnv(t, d)
			e.srv.inj.timeout.Store(int64(100 * time.Millisecond))
			r := e.mustControl(injectReq("inj-late"))
			if want := injectUnknown("the delivery did not finish in time"); !r.OK || r.Inject == nil || *r.Inject != want {
				t.Fatalf("answer %+v, want ok with %+v", r, want)
			}
			if evs := e.injectEvents(2); len(evs) != 2 {
				t.Fatalf("before the late result: %+v", evs)
			}
			close(release)
			e.waitGateOpen()
			evs := e.injectEvents(3)
			if len(evs) != 3 {
				t.Fatalf("events %+v", evs)
			}
			for i, ev := range evs {
				if ev.MessageID != canaryID || ev.CommandID != "inj-late" || ev.Digest != digestOf(canaryText) {
					t.Errorf("event %d: %+v", i, ev)
				}
			}
			if evs[0].Outcome != "started" || evs[1].Outcome != "uncertain" || evs[1].Reason != "died" || evs[1].State != "written-unknown" {
				t.Errorf("the first two: %+v %+v", evs[0], evs[1])
			}
			if got := evs[2]; got.Outcome != tc.outcome || got.Reason != tc.reason || got.State != tc.state {
				t.Errorf("late: %+v, want %q %q %q", got, tc.outcome, tc.reason, tc.state)
			}
			e.noTextInLog()
			raw, err := os.ReadFile(filepath.Join(e.logDir, eventlog.FileName))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), canaryMark) {
				t.Error("a word of the result is in the log")
			}
		})
	}
}

// A deliverer that panics after the handler has answered is logged late too.
func TestInjectLatePanicIsLogged(t *testing.T) {
	release := make(chan struct{})
	d := &fakeDeliverer{fn: func(ctx context.Context, _, text string) wire.InjectResult {
		<-ctx.Done()
		<-release
		panic("sample panic: " + text)
	}}
	e := canaryEnv(t, d)
	e.srv.inj.timeout.Store(int64(100 * time.Millisecond))
	if r := e.mustControl(injectReq("inj-late")); !r.OK || r.Inject == nil {
		t.Fatalf("answer %+v", r)
	}
	close(release)
	e.waitGateOpen()
	evs := e.injectEvents(3)
	if got := evs[2]; got.Outcome != "late-uncertain" || got.Reason != "died" || got.State != "written-unknown" {
		t.Errorf("late: %+v", got)
	}
	e.noTextInLog()
}

// A deliverer that returns in time has no late event, and one that returns as
// the time ends has exactly one of the two: its result is the answer, or it is
// logged late, never both and never neither. Each delivery has its own command
// id, and a late event comes after the answer event of its delivery.
func TestInjectLateEventOnlyWhenTheAnswerDidNotWait(t *testing.T) {
	const timeout = 4 * time.Millisecond
	const iterations = 200
	var n atomic.Int32
	d := &fakeDeliverer{fn: func(context.Context, string, string) wire.InjectResult {
		// Returns about when the time ends, a little before or after.
		time.Sleep(timeout - 300*time.Microsecond + time.Duration(n.Add(1)%7)*100*time.Microsecond)
		return submitted()
	}}
	e := canaryEnv(t, d)
	e.srv.inj.timeout.Store(int64(timeout))
	var late, wantEvents int
	for i := 0; i < iterations; i++ {
		r := e.srv.inject(injectReq(fmt.Sprintf("inj-race-%d", i)))
		if !r.OK || r.Inject == nil {
			t.Fatalf("iteration %d: %+v", i, r)
		}
		wantEvents += 2
		if r.Inject.Outcome != wire.InjectOutcomeSubmitted {
			late++
			wantEvents++
		}
		e.waitGateOpen()
	}
	t.Logf("%d of %d answered at the time's end", late, iterations)
	evs := e.injectEvents(wantEvents)
	if len(evs) != wantEvents {
		t.Errorf("%d events, want %d (%d late)", len(evs), wantEvents, late)
	}
	got := 0
	for _, ev := range evs {
		if ev.Outcome == "late-submitted" {
			got++
		}
	}
	if got != late {
		t.Errorf("%d late events, want %d", got, late)
	}
	// A late event follows its delivery's answer event.
	answered := map[string]bool{}
	for _, ev := range evs {
		switch {
		case ev.Outcome == "late-submitted" && !answered[ev.CommandID]:
			t.Errorf("the late event of %s precedes its answer", ev.CommandID)
		case ev.Outcome != "started":
			answered[ev.CommandID] = true
		}
	}
	// A deliverer that is far inside its time has none.
	e.srv.inj.timeout.Store(0)
	d.fn = func(context.Context, string, string) wire.InjectResult { return submitted() }
	if r := e.srv.inject(injectReq("inj-fast")); !r.OK || r.Inject == nil || r.Inject.Outcome != wire.InjectOutcomeSubmitted {
		t.Fatalf("answer %+v", r)
	}
	e.waitGateOpen()
	e.waitEvent("the fast delivery's answer", func(ev eventlog.Event) bool {
		return ev.Event == "inject" && ev.CommandID == "inj-fast" && ev.Outcome == "submitted"
	})
	for _, ev := range e.eventsNow() {
		if ev.CommandID == "inj-fast" && strings.HasPrefix(ev.Outcome, "late-") {
			t.Errorf("a late event: %+v", ev)
		}
	}
}
