package osc7501

import (
	"sync"
	"testing"
	"time"
)

func TestTracker(t *testing.T) {
	var tr Tracker
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

	if _, _, ok := tr.Last(); ok {
		t.Fatal("a new tracker has a last report")
	}
	if _, ok := tr.LastInvalid(); ok {
		t.Fatal("a new tracker has an invalid time")
	}

	if got := tr.Observe([]byte("?"), at(1)); got != EventQuery {
		t.Fatalf("query event = %v", got)
	}
	if _, _, ok := tr.Last(); ok {
		t.Fatal("a query is not a report")
	}

	if got := tr.Observe([]byte("state=working:app=codex"), at(2)); got != EventReport {
		t.Fatalf("report event = %v", got)
	}
	r, when, ok := tr.Last()
	if !ok || r.State != StateWorking || r.App != "codex" || !when.Equal(at(2)) {
		t.Fatalf("Last = %+v %v %v", r, when, ok)
	}

	// An unreadable payload does not replace the last report, but its time is
	// kept so the caller can see that something unreadable came after it.
	if got := tr.Observe([]byte("state=sleeping"), at(3)); got != EventInvalid {
		t.Fatalf("invalid event = %v", got)
	}
	r2, when2, ok := tr.Last()
	if !ok || r2 != r || !when2.Equal(at(2)) {
		t.Fatalf("Last after invalid = %+v %v %v", r2, when2, ok)
	}
	inv, ok := tr.LastInvalid()
	if !ok || !inv.Equal(at(3)) || !inv.After(when2) {
		t.Fatalf("LastInvalid = %v %v", inv, ok)
	}

	// clear is a report like any other and is kept.
	tr.Observe([]byte("state=clear"), at(4))
	r3, when3, ok := tr.Last()
	if !ok || r3.State != StateClear || !when3.Equal(at(4)) {
		t.Fatalf("Last after clear = %+v %v %v", r3, when3, ok)
	}
	if inv, _ := tr.LastInvalid(); !inv.Equal(at(3)) {
		t.Fatalf("a valid report moved LastInvalid to %v", inv)
	}

	tr.Observe([]byte("7501;?"), at(5))
	tr.Observe(nil, at(6))
	if c := (Counts{Queries: 2, Reports: 2, Invalid: 2}); tr.Counts() != c {
		t.Fatalf("Counts = %+v, want %+v", tr.Counts(), c)
	}
}

func TestTrackerStatus(t *testing.T) {
	var tr Tracker
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	if st := tr.Status(); st != (Status{}) {
		t.Fatalf("a new tracker has Status %+v", st)
	}

	// Unreadable payloads and no valid report ever: stale, nothing to show.
	tr.Observe([]byte("state=sleeping"), t0)
	if st := tr.Status(); st.Has || !st.Stale || st.Report != (Report{}) {
		t.Fatalf("Status after only an invalid payload = %+v", st)
	}

	// Equal times must not hide the order: the idle report, then an
	// unreadable one at the same instant.
	tr.Observe([]byte("state=idle:app=codex"), t0.Add(time.Second))
	if st := tr.Status(); !st.Has || st.Stale || st.Report.State != StateIdle || !st.At.Equal(t0.Add(time.Second)) {
		t.Fatalf("Status after a valid report = %+v", st)
	}
	tr.Observe([]byte("state=sleeping"), t0.Add(time.Second))
	st := tr.Status()
	if !st.Has || !st.Stale || st.Report.State != StateIdle || st.Report.App != "codex" || !st.At.Equal(t0.Add(time.Second)) {
		t.Fatalf("Status after an invalid payload at the same instant = %+v", st)
	}

	// A query and repeated reads do not change Stale.
	tr.Observe([]byte("?"), t0.Add(2*time.Second))
	tr.Status()
	if !tr.Status().Stale {
		t.Fatal("a query or a Status call cleared Stale")
	}

	// A following valid report clears it.
	tr.Observe([]byte("state=working:app=codex"), t0.Add(time.Second))
	if st := tr.Status(); !st.Has || st.Stale || st.Report.State != StateWorking {
		t.Fatalf("Status after a following valid report = %+v", st)
	}
}

func TestEventString(t *testing.T) {
	for e, want := range map[Event]string{EventQuery: "query", EventReport: "report", EventInvalid: "invalid", Event(99): "invalid"} {
		if e.String() != want {
			t.Errorf("Event(%d) = %q, want %q", int(e), e.String(), want)
		}
	}
}

func TestTrackerConcurrent(t *testing.T) {
	var tr Tracker
	now := time.Now()
	var wg sync.WaitGroup
	payloads := []string{"?", "state=idle:app=codex", "bogus", "state=clear"}
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				tr.Observe([]byte(payloads[i%len(payloads)]), now)
				tr.Last()
				tr.LastInvalid()
				tr.Counts()
			}
		}()
	}
	wg.Wait()
	c := tr.Counts()
	if c.Queries != 400 || c.Reports != 800 || c.Invalid != 400 {
		t.Fatalf("Counts = %+v", c)
	}
}
