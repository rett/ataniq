//go:build linux && ataniq_canary

package injectbridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"ataniq/internal/inject"
	"ataniq/internal/inject/osc7501"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
)

// The deliverer is the control server's, by its method set.
var _ serve.Deliverer = (*Deliverer)(nil)

func wantWords(t *testing.T, what string, got wire.InjectResult, o wire.InjectOutcome, r wire.InjectReason, w wire.InjectWritten) {
	t.Helper()
	if err := got.Validate(); err != nil {
		t.Errorf("%s: the result %+v is not valid: %v", what, got, err)
	}
	if got.Outcome != o || got.Reason != r || got.Written != w {
		t.Errorf("%s: %q, %q, %q (%s), want %q, %q, %q", what, got.Outcome, got.Reason, got.Written, got.Detail, o, r, w)
	}
}

// listedBridge is a bridge with the first pair of the library's tested table.
func listedBridge(t *testing.T, s *session.Session) *Bridge {
	t.Helper()
	pair := testedPairs(t)[0]
	return New(s, Config{Runtime: pair.Runtime, Version: pair.Version, Tracker: new(osc7501.Tracker)})
}

// A text whose first line is not the header of the message id is refused as a
// text, and nothing is touched: the answer comes while the test holds the
// input lock, and the agent has read nothing.
func TestDeliverRefusesAHeaderThatDoesNotNameTheMessageID(t *testing.T) {
	s := startSession(t, readyScript)
	waitScreen(t, s, "READY")
	d := NewDeliverer(listedBridge(t, s), "")
	const body = "sample body 4f1c"
	cases := []struct{ name, id, text string }{
		{"another id", "msg-1", "[ataniq-msg id=msg-2 from=sample]\n" + body},
		{"an id with a suffix", "msg-1", "[ataniq-msg id=msg-10 from=sample]\n" + body},
		{"an id with a prefix", "msg-1", "[ataniq-msg id=xmsg-1 from=sample]\n" + body},
		{"no space after the id", "msg-1", "[ataniq-msg id=msg-1]\n" + body},
		{"no header", "msg-1", body},
		{"the header on the second line", "msg-1", "first line\n[ataniq-msg id=msg-1 from=sample]\n" + body},
		{"a space before the header", "msg-1", " [ataniq-msg id=msg-1 from=sample]\n" + body},
		{"another tag", "msg-1", "[other-msg id=msg-1 from=sample]\n" + body},
		{"an empty text", "msg-1", ""},
		{"an empty id", "", "[ataniq-msg id= from=sample]\n" + body},
		{"an id the wire refuses", "Msg 1", "[ataniq-msg id=Msg 1 from=sample]\n" + body},
		{"an id that is the rest of the header", "msg-1 from=sample]", "[ataniq-msg id=msg-1 from=sample] x\n" + body},
	}
	holdingTheInputLock(t, s, func() {
		for _, c := range cases {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			got := d.Deliver(ctx, c.id, c.text)
			if ctx.Err() != nil {
				t.Errorf("%s: the deliverer waited", c.name)
			}
			cancel()
			wantWords(t, c.name, got, wire.InjectOutcomeRefused, wire.InjectReasonText, wire.InjectWrittenNothing)
			if strings.Contains(got.Detail, body) {
				t.Errorf("%s: the detail repeats the text: %q", c.name, got.Detail)
			}
		}
		// With no bridge at all the answer is the same: the header is checked
		// before anything else.
		wantWords(t, "no bridge", NewDeliverer(nil, "").Deliver(context.Background(), "msg-1", body),
			wire.InjectOutcomeRefused, wire.InjectReasonText, wire.InjectWrittenNothing)
	})
	onlyProbeReceived(t, s)
}

// A pair that is not in the tested table is answered refused, untested,
// nothing, with the session untouched: the answer comes while the test holds
// the input lock, and the agent has read nothing.
func TestDeliverAnswersUntestedForAPairThatIsNotListed(t *testing.T) {
	s := startSession(t, readyScript)
	waitScreen(t, s, "READY")
	d := NewDeliverer(New(s, Config{Runtime: sampleRuntime, Version: sampleVersion}), "")
	holdingTheInputLock(t, s, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		got := d.Deliver(ctx, "msg-1", sampleText("msg-1"))
		wantWords(t, "unlisted pair", got, wire.InjectOutcomeRefused, wire.InjectReasonUntested, wire.InjectWrittenNothing)
		if !strings.Contains(got.Detail, sampleRuntime) || !strings.Contains(got.Detail, sampleVersion) {
			t.Errorf("detail %q does not name the pair", got.Detail)
		}
	})
	onlyProbeReceived(t, s)
}

// A context that has ended gives the mapped not-now result, with the session
// untouched: the answer comes while the test holds the input lock.
func TestDeliverWithAnEndedContextAnswersNotNow(t *testing.T) {
	s := startSession(t, readyScript)
	waitScreen(t, s, "READY")
	d := NewDeliverer(listedBridge(t, s), "")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	holdingTheInputLock(t, s, func() {
		for name, ctx := range map[string]context.Context{"cancelled": cancelled, "past its deadline": expired} {
			got := d.Deliver(ctx, "msg-1", sampleText("msg-1"))
			wantWords(t, name, got, wire.InjectOutcomeNotNow, wire.InjectReasonNotReady, wire.InjectWrittenNothing)
			if !strings.HasPrefix(got.Detail, "cancelled | ") {
				t.Errorf("%s: detail %q, want the fence's word for an ended context", name, got.Detail)
			}
		}
	})

	// A context that ends while the fence waits for the lock is the same
	// answer, and the function of the fence never ran.
	holdingTheInputLock(t, s, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		got := d.Deliver(ctx, "msg-1", sampleText("msg-1"))
		wantWords(t, "ended while waiting for the lock", got, wire.InjectOutcomeNotNow, wire.InjectReasonNotReady, wire.InjectWrittenNothing)
	})
	onlyProbeReceived(t, s)
}

// After a session error the result is the mapped died one: a session that is
// over, a session whose input an earlier failed write broke, and no session.
func TestDeliverAfterASessionErrorAnswersDied(t *testing.T) {
	t.Run("the session is over", func(t *testing.T) {
		s := startSession(t, readyScript)
		waitScreen(t, s, "READY")
		d := NewDeliverer(listedBridge(t, s), "")
		s.Close()
		got := d.Deliver(testCtx(t), "msg-1", sampleText("msg-1"))
		wantWords(t, "closed", got, wire.InjectOutcomeUncertain, wire.InjectReasonDied, wire.InjectWrittenNothing)
		if !strings.Contains(got.Detail, "closed") {
			t.Errorf("detail %q does not hold the session's error", got.Detail)
		}
	})
	t.Run("the input is broken", func(t *testing.T) {
		pair := testedPairs(t)[0]
		s, b := brokenSession(t, Config{Runtime: pair.Runtime, Version: pair.Version})
		d := NewDeliverer(b, "")
		for i := 0; i < 2; i++ { // every later delivery, not only the next
			got := d.Deliver(testCtx(t), "msg-1", sampleText("msg-1"))
			wantWords(t, "broken", got, wire.InjectOutcomeUncertain, wire.InjectReasonDied, wire.InjectWrittenNothing)
			if !strings.Contains(got.Detail, "broken") {
				t.Errorf("detail %q does not hold the session's error", got.Detail)
			}
		}
		if !s.InputBroken() {
			t.Error("the input is no longer broken")
		}
	})
	t.Run("no session", func(t *testing.T) {
		pair := testedPairs(t)[0]
		var none *Deliverer
		for name, d := range map[string]*Deliverer{
			"no bridge":       NewDeliverer(nil, ""),
			"no session":      NewDeliverer(New(nil, Config{Runtime: pair.Runtime, Version: pair.Version}), ""),
			"a nil deliverer": none,
		} {
			got := d.Deliver(context.Background(), "msg-1", sampleText("msg-1"))
			wantWords(t, name, got, wire.InjectOutcomeUncertain, wire.InjectReasonDied, wire.InjectWrittenNothing)
		}
	})
}

// A whole delivery through the deliverer on a screen no recogniser knows: one
// attempt, the mapped not-now result with the fence's readings in the detail,
// and nothing written.
func TestDeliverOnAnUnknownScreenAnswersNotNowAndWritesNothing(t *testing.T) {
	s := startSession(t, readyScript)
	waitScreen(t, s, "READY")
	d := NewDeliverer(listedBridge(t, s), "sample-mode")
	start := time.Now()
	got := d.Deliver(testCtx(t), "msg-1", sampleText("msg-1"))
	wantWords(t, "unknown screen", got, wire.InjectOutcomeNotNow, wire.InjectReasonNotReady, wire.InjectWrittenNothing)
	if !strings.Contains(got.Detail, " | not idle, written nothing") {
		t.Errorf("detail %q does not hold the fence's readings", got.Detail)
	}
	// One attempt: the fence's first settle is at most a second, and a
	// deliverer that tried again until its deadline would take ten.
	if took := time.Since(start); took > DeliverTimeout/2 {
		t.Errorf("the delivery took %v", took)
	}
	onlyProbeReceived(t, s)
}

// The fence's settings: the deadline is ten seconds from the call on the
// bridge's clock, or the context's deadline when that is earlier; the mode and
// the clock are handed on.
func TestDeliverOptions(t *testing.T) {
	if DeliverTimeout != 10*time.Second {
		t.Fatalf("DeliverTimeout = %v", DeliverTimeout)
	}
	if DeliverTimeout >= serve.InjectTimeout {
		t.Errorf("DeliverTimeout %v is not below the control server's %v", DeliverTimeout, serve.InjectTimeout)
	}
	clock := newFakeClock(clockStart)
	d := NewDeliverer(New(nil, Config{Now: clock.now}), "sample-mode")

	opts := d.options(context.Background())
	if !opts.Deadline.Equal(clockStart.Add(10*time.Second)) || opts.ExpectedMode != "sample-mode" {
		t.Errorf("options = deadline %v, mode %q", opts.Deadline, opts.ExpectedMode)
	}
	if opts.Now == nil || !opts.Now().Equal(clockStart) {
		t.Error("the clock was not handed on")
	}
	if opts.Timing != (inject.Timing{}) {
		t.Errorf("the timing is not left to the library's defaults: %+v", opts.Timing)
	}

	early := clockStart.Add(3 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), early)
	defer cancel()
	if got := d.options(ctx).Deadline; !got.Equal(early) {
		t.Errorf("deadline %v with a context that ends at %v", got, early)
	}
	late := clockStart.Add(time.Minute)
	ctx2, cancel2 := context.WithDeadline(context.Background(), late)
	defer cancel2()
	if got := d.options(ctx2).Deadline; !got.Equal(clockStart.Add(10 * time.Second)) {
		t.Errorf("deadline %v with a context that ends later, at %v", got, late)
	}

	// With no clock configured it is the system's.
	before := time.Now()
	got := NewDeliverer(New(nil, Config{}), "").options(context.Background())
	if got.Deadline.Before(before.Add(DeliverTimeout)) || got.Deadline.After(time.Now().Add(DeliverTimeout)) {
		t.Errorf("deadline %v on the system clock", got.Deadline)
	}
}
