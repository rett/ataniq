//go:build linux && ataniq_canary

package injectbridge

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/inject"
	"ataniq/internal/inject/osc7501"
	"ataniq/internal/leadholder/session"
)

// What a fake agent prints: the support query, a request for the device
// attributes (the sentinel an agent sends after the query), and reports. The
// app name is a neutral sample.
const (
	oscQuery   = "\x1b]7501;?\x07"
	daRequest  = "\x1b[c"
	oscIdle    = "\x1b]7501;state=idle:app=sample-app\x07"
	oscWorking = "\x1b]7501;state=working:app=sample-app\x1b\\"
	oscBad     = "\x1b]7501;state=no-such-state\x07"
)

// fakeClock is a clock a test sets; it is read from the applying side.
type fakeClock struct{ ns atomic.Int64 }

func newFakeClock(at time.Time) *fakeClock {
	c := &fakeClock{}
	c.set(at)
	return c
}

func (c *fakeClock) set(at time.Time) { c.ns.Store(at.UnixNano()) }
func (c *fakeClock) now() time.Time   { return time.Unix(0, c.ns.Load()).UTC() }

var clockStart = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

func withOSC(h session.OSCHook) func(*session.Config) {
	return func(c *session.Config) { c.OSC = []session.OSCHook{h} }
}

// report reads the view's report through the bridge, inside a Frozen call.
func report(t *testing.T, b *Bridge) osc7501.Status {
	t.Helper()
	var st osc7501.Status
	inFrozen(t, b, func(v inject.View, _ func([]byte) error) {
		st = v.Report()
		if again := v.Report(); again != st {
			t.Errorf("two reads of the report in one Frozen call differ: %+v and %+v", st, again)
		}
	})
	return st
}

// The hook alone: it is for command 7501, answers the query with the
// library's reply and nothing else, consumes every payload and gives each to
// the tracker with the clock's time; the stale flag is the tracker's.
func TestHookAnswersTheQueryAndFeedsTheTracker(t *testing.T) {
	var tr osc7501.Tracker
	clock := newFakeClock(clockStart)
	h := Hook(&tr, clock.now)
	if h.Cmd != 7501 || h.Fn == nil {
		t.Fatalf("Hook = command %d, function set %t", h.Cmd, h.Fn != nil)
	}
	call := func(payload string) string {
		t.Helper()
		reply, handled := h.Fn([]byte(payload))
		if !handled {
			t.Errorf("the hook did not consume %q", payload)
		}
		return string(reply)
	}

	want, ok := osc7501.Reply([]byte("7501;?"))
	if !ok {
		t.Fatal("the library has no reply to the query")
	}
	// The session gives the string with its command number; a payload
	// without it is the same query.
	for _, q := range []string{"7501;?", "?"} {
		if got := call(q); got != string(want) {
			t.Errorf("reply to %q = %q, want the library's %q", q, got, want)
		}
	}
	if got := Counts(&tr); got != (Totals{Queries: 2}) {
		t.Errorf("Counts after two queries = %+v", got)
	}
	if st := tr.Status(); st.Has || st.Stale {
		t.Errorf("Status after queries only = %+v", st)
	}

	at1 := clockStart.Add(time.Minute)
	clock.set(at1)
	if got := call("7501;state=idle:app=sample-app"); got != "" {
		t.Errorf("a report was answered with %q", got)
	}
	if st := tr.Status(); !st.Has || st.Stale || st.Report.State != osc7501.StateIdle || st.Report.App != "sample-app" || !st.At.Equal(at1) {
		t.Errorf("Status after a report = %+v, want idle at %v", st, at1)
	}

	clock.set(at1.Add(time.Minute))
	if got := call("7501;state=no-such-state"); got != "" {
		t.Errorf("an unreadable payload was answered with %q", got)
	}
	if st := tr.Status(); !st.Has || !st.Stale || st.Report.State != osc7501.StateIdle || !st.At.Equal(at1) {
		t.Errorf("Status after an unreadable payload = %+v, want the idle report of %v, stale", st, at1)
	}
	if bad, ok := tr.LastInvalid(); !ok || !bad.Equal(at1.Add(time.Minute)) {
		t.Errorf("LastInvalid = %v, %t, want the clock's time", bad, ok)
	}

	at3 := at1.Add(2 * time.Minute)
	clock.set(at3)
	call("7501;state=working:app=sample-app")
	if st := tr.Status(); !st.Has || st.Stale || st.Report.State != osc7501.StateWorking || !st.At.Equal(at3) {
		t.Errorf("Status after a later report = %+v, want working at %v, not stale", st, at3)
	}
	if got := Counts(&tr); got != (Totals{Queries: 2, Reports: 2, Invalid: 1}) {
		t.Errorf("Counts = %+v", got)
	}
}

// With no tracker the query is still answered; with no clock the time is the
// system's; Counts of no tracker is nothing.
func TestHookWithoutATrackerOrAClock(t *testing.T) {
	reply, handled := Hook(nil, nil).Fn([]byte("7501;?"))
	if string(reply) != osc7501.QueryReply || !handled {
		t.Errorf("Hook(nil, nil) = %q, %t", reply, handled)
	}
	var tr osc7501.Tracker
	before := time.Now()
	Hook(&tr, nil).Fn([]byte("7501;state=idle"))
	if st := tr.Status(); !st.Has || st.At.Before(before) || st.At.After(time.Now()) {
		t.Errorf("Status with the system clock = %+v", st)
	}
	if got := Counts(nil); got != (Totals{}) {
		t.Errorf("Counts(nil) = %+v", got)
	}
}

// Through a session: the agent sends the query and then asks for the device
// attributes, and reads the library's reply before the reply to that request;
// the report it printed is what the bridge's view reports, at the clock's time.
func TestHookInASessionAnswersBeforeTheNextReplyAndTheViewReports(t *testing.T) {
	var tr osc7501.Tracker
	clock := newFakeClock(clockStart)
	s := startSession(t, "raw; print "+hx(oscQuery+daRequest+oscIdle)+"; echo; wait", withOSC(Hook(&tr, clock.now)))
	b := New(s, Config{Runtime: sampleRuntime, Version: sampleVersion, Tracker: &tr, Now: clock.now})

	var got string
	poll(t, func() string { return "the agent to read both replies; got " + got }, func() bool {
		got = string(received(s))
		return len(got) > len(osc7501.QueryReply) && strings.HasSuffix(got, "c")
	})
	rest, ok := strings.CutPrefix(got, osc7501.QueryReply)
	if !ok {
		t.Fatalf("the agent read %q, want the reply to the query first", got)
	}
	if !strings.HasPrefix(rest, "\x1b[?") || !strings.HasSuffix(rest, "c") || strings.Contains(rest, "7501") {
		t.Errorf("after the reply to the query the agent read %q, want the device attributes alone", rest)
	}

	st := report(t, b)
	if !st.Has || st.Stale || st.Report.State != osc7501.StateIdle || st.Report.App != "sample-app" || !st.At.Equal(clockStart) {
		t.Errorf("Report = %+v, want idle at the clock's %v", st, clockStart)
	}
	if got := Counts(&tr); got != (Totals{Queries: 1, Reports: 1}) {
		t.Errorf("Counts = %+v", got)
	}
}

// The stale flag, on the injected clock: a report, then an unreadable payload,
// then a report again, each printed when the test lets the agent go on.
func TestViewReportShowsTheStaleFlag(t *testing.T) {
	var tr osc7501.Tracker
	clock := newFakeClock(clockStart)
	script := "raw; print " + hx(oscIdle) + "; say ONE; echo 1; print " + hx(oscBad) + "; say TWO; echo 1; print " + hx(oscWorking) + "; say THREE; wait"
	s := startSession(t, script, withOSC(Hook(&tr, clock.now)))
	b := New(s, Config{Runtime: sampleRuntime, Version: sampleVersion, Tracker: &tr, Now: clock.now})

	waitScreen(t, s, "ONE")
	if st := report(t, b); !st.Has || st.Stale || st.Report.State != osc7501.StateIdle || !st.At.Equal(clockStart) {
		t.Errorf("Report after the first report = %+v", st)
	}

	clock.set(clockStart.Add(time.Hour))
	s.Input([]byte("n"))
	waitScreen(t, s, "TWO")
	if st := report(t, b); !st.Has || !st.Stale || st.Report.State != osc7501.StateIdle || !st.At.Equal(clockStart) {
		t.Errorf("Report after an unreadable payload = %+v, want the first report, stale", st)
	}

	clock.set(clockStart.Add(2 * time.Hour))
	s.Input([]byte("n"))
	waitScreen(t, s, "THREE")
	if st := report(t, b); !st.Has || st.Stale || st.Report.State != osc7501.StateWorking || !st.At.Equal(clockStart.Add(2*time.Hour)) {
		t.Errorf("Report after a later report = %+v, want working, not stale", st)
	}
	if got := Counts(&tr); got != (Totals{Reports: 2, Invalid: 1}) {
		t.Errorf("Counts = %+v", got)
	}
}

// Without the hook installed the session answers nothing to the query and the
// view reports none, whether the bridge has a tracker or not.
func TestWithoutTheHookTheViewReportsNone(t *testing.T) {
	s := startSession(t, "raw; print "+hx(oscQuery+daRequest+oscIdle)+"; say SENT; echo; wait")
	waitScreen(t, s, "SENT")
	var got string
	poll(t, func() string { return "the agent to read the device attributes; got " + got }, func() bool {
		got = string(received(s))
		return strings.HasSuffix(got, "c")
	})
	if strings.Contains(got, "7501") || !strings.HasPrefix(got, "\x1b[?") {
		t.Errorf("the agent read %q, want the device attributes alone", got)
	}

	var tr osc7501.Tracker
	for name, cfg := range map[string]Config{
		"a tracker no hook feeds": {Runtime: sampleRuntime, Version: sampleVersion, Tracker: &tr},
		"no tracker":              {Runtime: sampleRuntime, Version: sampleVersion},
	} {
		if st := report(t, New(s, cfg)); st.Has || st.Stale || st != (osc7501.Status{}) {
			t.Errorf("%s: Report = %+v, want none", name, st)
		}
	}
	if got := Counts(&tr); got != (Totals{}) {
		t.Errorf("Counts = %+v", got)
	}
}

// The hook is called where output is applied while the report and the totals
// are read elsewhere: under the race detector this fails if the tracker were
// ever read or written without its lock.
func TestHookAndReadersRunConcurrently(t *testing.T) {
	var tr osc7501.Tracker
	clock := newFakeClock(clockStart)
	h := Hook(&tr, clock.now)
	b := New(nil, Config{Tracker: &tr})
	const rounds = 2000
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			h.Fn([]byte("7501;?"))
			h.Fn([]byte("7501;state=idle:app=sample-app"))
			h.Fn([]byte("7501;state=no-such-state"))
		}
	}()
	go func() {
		defer wg.Done()
		var last Totals
		for i := 0; i < rounds; i++ {
			c := Counts(&tr)
			if c.Queries < last.Queries || c.Reports < last.Reports || c.Invalid < last.Invalid {
				t.Errorf("the totals fell: %+v after %+v", c, last)
				return
			}
			last = c
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			_ = b.status()
			clock.set(clockStart.Add(time.Duration(i) * time.Second))
		}
	}()
	wg.Wait()
	if got := Counts(&tr); got != (Totals{Queries: rounds, Reports: rounds, Invalid: rounds}) {
		t.Errorf("Counts = %+v", got)
	}
}
