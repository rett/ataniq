//go:build linux && ataniq_canary

package injectbridge

import (
	"context"
	"testing"
	"time"

	"ataniq/internal/inject/osc7501"
	"ataniq/internal/leadholder/wire"
)

// The fence's deadline bounds the wait for the input lock too: with the lock
// held past it, Deliver answers not-now in time and writes nothing. The
// bridge's clock is behind the system clock, so the deadline is 0.5 s ahead
// while the context's own deadline is far.
func TestDeliverWaitForTheLockEndsAtTheFencesDeadline(t *testing.T) {
	s := startSession(t, readyScript)
	waitScreen(t, s, "READY")
	pair := testedPairs(t)[0]
	behind := func() time.Time { return time.Now().Add(-(DeliverTimeout - 500*time.Millisecond)) }
	d := NewDeliverer(New(s, Config{Runtime: pair.Runtime, Version: pair.Version, Tracker: new(osc7501.Tracker), Now: behind}), "")

	holdingTheInputLock(t, s, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		start := time.Now()
		got := d.Deliver(ctx, "msg-1", sampleText("msg-1"))
		took := time.Since(start)
		wantWords(t, "lock held past the deadline", got, wire.InjectOutcomeNotNow, wire.InjectReasonNotReady, wire.InjectWrittenNothing)
		if took < 300*time.Millisecond || took > 3*time.Second {
			t.Errorf("Deliver took %v, want about 0.5s", took)
		}
	})
	onlyProbeReceived(t, s)
}
