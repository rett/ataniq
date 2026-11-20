//go:build linux && ataniq_canary

package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
)

// A deliverer that returns after the handler has decided to stop waiting and
// before the handler has settled the delivery settles it itself: its result is
// the answer, and nothing is logged late. The hook holds the handler in that
// window, which is a few instructions wide, until the deliverer has returned.
//
// Two more properties of the same code are held by reasoning and by no test.
// The deliverer settles with one compare-and-swap, not a load and a store
// (when the two sides run those instructions at once, one of them must lose);
// the hook lets the deliverer finish before the handler tries, so it cannot
// tell the two apart. And the late event is queued before the gate opens
// again, so it comes before the next delivery's started event; a test of that
// would need to hold the deliverer between the two statements.
func TestInjectDelivererSettlesInTheGiveUpWindow(t *testing.T) {
	release := make(chan struct{})
	d := &fakeDeliverer{fn: func(ctx context.Context, _, _ string) wire.InjectResult {
		<-ctx.Done()
		<-release // still running when the handler looks for a result
		return submitted()
	}}
	e := canaryEnv(t, d)
	e.srv.inj.timeout.Store(int64(50 * time.Millisecond))
	hooked := 0
	e.srv.inj.beforeGiveUp = func() {
		hooked++
		close(release)
		e.waitGateOpen() // the deliverer has returned and settled
	}
	// Called here, not through the socket, so the hook runs in the test's
	// goroutine.
	r := e.srv.inject(injectReq("inj-window"))
	if hooked != 1 {
		t.Fatalf("the handler gave up %d times, want once", hooked)
	}
	if want := submitted(); !r.OK || r.Inject == nil || *r.Inject != want {
		t.Fatalf("answer %+v, want ok with the deliverer's %+v", r, want)
	}
	// The deliverer was done before the handler answered, so a late event
	// would be in the log before the answer event.
	evs := e.injectEvents(2)
	if len(evs) != 2 || evs[0].Outcome != "started" || evs[1].Outcome != "submitted" || evs[1].State != "written-whole" {
		t.Fatalf("events %+v, want started and submitted", evs)
	}
	for _, ev := range evs {
		if ev.CommandID != "inj-window" || strings.HasPrefix(ev.Outcome, "late-") {
			t.Errorf("event %+v", ev)
		}
	}
	e.noTextInLog()
}
