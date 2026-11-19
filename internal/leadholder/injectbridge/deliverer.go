//go:build linux && ataniq_canary

package injectbridge

import (
	"context"
	"strings"
	"time"

	"ataniq/internal/inject"
	"ataniq/internal/leadholder/wire"
)

// DeliverTimeout is how long after the call a delivery may last: the fence's
// deadline. It bounds the whole attempt, the wait for the input lock
// included, because Deliver ends its context at that deadline. It is shorter
// than the control server's own bound on a delivery, so the result is given
// before the server stops waiting.
const DeliverTimeout = 10 * time.Second

// headerStart is how the first line of a message starts, before its id.
const headerStart = "[ataniq-msg id="

// Deliverer delivers messages to the session of one Bridge. Its Deliver is the
// method the control server asks of a deliverer. It keeps no state between
// calls; the session's input lock serialises deliveries.
type Deliverer struct {
	b    *Bridge
	mode string
}

// NewDeliverer returns the deliverer of b. expectedMode is the permission mode
// the agent's footer must show for a delivery to be made, on a runtime whose
// screen shows one; it goes to the fence as it is.
func NewDeliverer(b *Bridge, expectedMode string) *Deliverer {
	return &Deliverer{b: b, mode: expectedMode}
}

// Deliver runs the library's fence once for one message and returns its
// result in the wire's words (see WireResult). It never retries, and it
// writes nothing itself.
//
// The first line of text is the message's header. It must start with
// "[ataniq-msg id=", the message id and a space, and the id must be one the
// wire accepts; otherwise the result is that of a refused text (refused, text,
// nothing) and neither the library nor the session is touched.
//
// The whole call, the wait for the input lock included, ends within
// DeliverTimeout, or at ctx's deadline when that is earlier; nothing is
// written later. When ctx has ended before the fence writes,
// the result is not-now, not-ready, nothing; an error of the session
// (session.ErrClosed, session.ErrInputBroken, a failed write) is uncertain,
// died, with what was written before it.
func (d *Deliverer) Deliver(ctx context.Context, messageID, text string) wire.InjectResult {
	if ctx == nil {
		ctx = context.Background()
	}
	header, _, _ := strings.Cut(text, "\n")
	if wire.CheckMessageID(messageID) != nil || !strings.HasPrefix(header, headerStart+messageID+" ") {
		return WireResult(inject.Result{
			Written: inject.WrittenNothing, Outcome: inject.OutcomeRefusedText,
			Detail: "the first line of the text is not the header of the message id",
		})
	}
	// A nil *Bridge in the interface would not be nil to the library.
	var s inject.Session
	if b := d.bridge(); b != nil {
		s = b
	}
	opts := d.options(ctx)
	// The wait for the input lock is bounded by ctx alone: end ctx at the
	// fence's deadline too, so that the result comes before the server's bound.
	ctx, cancel := context.WithDeadline(ctx, opts.Deadline)
	defer cancel()
	return WireResult(inject.Deliver(ctx, s, inject.Message{Text: text, Header: header}, opts))
}

// now returns the configured clock, or time.Now.
func (b *Bridge) now() func() time.Time {
	if b == nil || b.cfg.Now == nil {
		return time.Now
	}
	return b.cfg.Now
}

func (d *Deliverer) bridge() *Bridge {
	if d == nil {
		return nil
	}
	return d.b
}

// options are the fence's settings for a delivery that starts now: the
// deadline DeliverTimeout from now on the bridge's clock, or ctx's deadline
// when that is earlier, the expected mode and the clock.
func (d *Deliverer) options(ctx context.Context) inject.Options {
	now := d.bridge().now()
	deadline := now().Add(DeliverTimeout)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	opts := inject.Options{Deadline: deadline, Now: now}
	if d != nil {
		opts.ExpectedMode = d.mode
	}
	return opts
}
