//go:build linux && ataniq_canary

package injectbridge

import (
	"testing"
	"time"

	"ataniq/internal/inject"
	"ataniq/internal/inject/osc7501"
	"ataniq/internal/leadholder/wire"
)

// Pending is asked of the session at each call: it turns true inside one
// Frozen call when the agent prints on its own.
func TestPendingTurnsTrueInsideOneFrozenCall(t *testing.T) {
	s := startSession(t, "raw; mode paste; say READY; sleep 300ms; say LATE; wait")
	waitScreen(t, s, "READY")
	b := New(s, Config{Runtime: sampleRuntime, Version: sampleVersion})
	inFrozen(t, b, func(v inject.View, _ func([]byte) error) {
		if v.Pending() {
			t.Fatal("Pending is true before the agent printed")
		}
		poll(t, func() string { return "Pending to turn true" }, v.Pending)
	})
}

// Ended is asked of the session at each call: it turns true inside one Frozen
// call when the agent exits.
func TestEndedIsTrueAfterTheAgentExits(t *testing.T) {
	s := startSession(t, "raw; say READY; sleep 300ms; exit 0")
	waitScreen(t, s, "READY")
	inFrozen(t, New(s, Config{Runtime: sampleRuntime, Version: sampleVersion}), func(v inject.View, _ func([]byte) error) {
		if v.Ended() {
			t.Fatal("Ended is true before the agent exited")
		}
		poll(t, func() string { return "Ended to turn true" }, v.Ended)
	})
}

// One attempt: against an agent that floods, the first settle never drains and
// takes the fence's first-settle bound (1 s); a second attempt would take twice
// that, so the time bound sits just under 2 s, as far above one attempt as it
// can be while a second attempt still exceeds it.
func TestDeliverMakesOneAttempt(t *testing.T) {
	s := startSession(t, "raw; flood 4000 0 1")
	pair := testedPairs(t)[0]
	d := NewDeliverer(New(s, Config{Runtime: pair.Runtime, Version: pair.Version, Tracker: new(osc7501.Tracker)}), "")
	start := time.Now()
	got := d.Deliver(testCtx(t), "msg-1", sampleText("msg-1"))
	took := time.Since(start)
	wantWords(t, "flooding agent", got, wire.InjectOutcomeNotNow, wire.InjectReasonNotReady, wire.InjectWrittenNothing)
	if took > 1950*time.Millisecond {
		t.Errorf("the delivery took %v, more than one attempt", took)
	}
}
