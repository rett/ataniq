//go:build linux && ataniq_canary

package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/wire"
)

// InjectTimeout is how long a delivery may take. The deliverer's context ends
// then, and the control server answers without waiting any longer.
const InjectTimeout = 15 * time.Second

// Deliverer delivers one message to the lead and says what became of it. It
// returns the wire's result, so this package knows nothing of how the message
// is written. The context ends at [InjectTimeout] or when the service closes,
// and a deliverer must stop writing when it does: until it returns, no other
// delivery starts (see [Server.inject]).
type Deliverer interface {
	Deliver(ctx context.Context, messageID, text string) wire.InjectResult
}

// InjectConfig is the injection's seam in Config. A nil Deliverer keeps the
// gate closed: the control server answers every inject request with disabled,
// as a release build does.
type InjectConfig struct{ Deliverer Deliverer }

// injectState is the injection's state in a Server.
type injectState struct {
	// running is true from the moment a delivery is accepted until its
	// deliverer has returned, which can be later than the answer.
	running atomic.Bool
	// timeout is the delivery's time when it is not zero (a test sets it; the
	// default is [InjectTimeout]).
	timeout atomic.Int64
}

// The results the handler makes up when it has no valid one from the
// deliverer. Each is a valid result and says what is known: a message may have
// been written, and how much of it is not.
var (
	resultUnencodable = injectUnknown("the result could not be encoded")
	resultPanicked    = injectUnknown("the delivery stopped unexpectedly")
	resultTimedOut    = injectUnknown("the delivery did not finish in time")
	resultClosed      = injectUnknown("the holder closed during the delivery")
)

func injectUnknown(detail string) wire.InjectResult {
	return wire.InjectResult{
		Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonDied,
		Written: wire.InjectWrittenUnknown, Detail: detail,
	}
}

// inject answers an inject request (the request is for this epoch: dispatch
// has checked). The gate is open only when a deliverer was given; without one
// the answer is the release build's, disabled.
//
// Before the deliverer is called the answers are refusals, and a refusal means
// nothing was written: a request with no message id or no text is bad-request,
// and so is one whose text does not start "[ataniq-msg id=", the message id and
// a space (the text is not logged, so the refusal says nothing of it). A
// request that arrives while a delivery runs, or while the holder is closing,
// is busy. Once the deliverer is called the answer is ok with a result and is
// never a refusal. The inject events (one when it starts, one with the answer,
// and a third when a deliverer that the answer did not wait for returns) carry
// the message id, the command id and the SHA-256 of the text in hex, never the
// text.
//
// A deliverer that panics, or does not return within the delivery's time, is
// answered uncertain, died, unknown. One that outlasts the time may still write,
// so the gate stays shut for the rest of its run: a request that comes while it
// runs is busy, and the next delivery can start only after it has returned, and
// what it returned is logged as a late result. A deliverer that never returns
// keeps injection busy until the holder ends.
func (s *Server) inject(req wire.Request) wire.Response {
	d := s.cfg.Deliverer
	if d == nil {
		return s.injectDisabled()
	}
	if wire.CheckMessageID(req.MessageID) != nil || wire.CheckInjectText(req.Text) != nil {
		return refusal(wire.CodeBadRequest, "an inject request needs a message id and a text")
	}
	if !strings.HasPrefix(req.Text, "[ataniq-msg id="+req.MessageID+" ") {
		// The header is on the first line, and the prefix has no newline.
		return refusal(wire.CodeBadRequest, "the text's header does not name the message id")
	}
	s.mu.Lock()
	closing := s.closed || s.finishing
	s.mu.Unlock()
	if closing {
		return refusal(wire.CodeBusy, "the holder is closing")
	}
	if !s.inj.running.CompareAndSwap(false, true) {
		return refusal(wire.CodeBusy, "a delivery is running")
	}
	sum := sha256.Sum256([]byte(req.Text))
	ev := eventlog.Fields{MessageID: req.MessageID, CommandID: req.CommandID, Digest: hex.EncodeToString(sum[:])}
	started := ev
	started.Outcome = "started"
	s.emit(eventlog.EventInject, started)

	timeout := time.Duration(s.inj.timeout.Load())
	if timeout <= 0 {
		timeout = InjectTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	done := make(chan wire.InjectResult, 1)
	var settled atomic.Bool
	// answered is closed when this handler returns, which is after its answer
	// event: a deliverer that has a late result to log waits for it.
	answered := make(chan struct{})
	defer close(answered)
	go s.deliver(ctx, cancel, d, req.MessageID, req.Text, ev, &settled, answered, done)

	// giveUp is the answer when the handler stops waiting: the result made up
	// for it, or, when the deliverer has just returned, its own (it has then
	// settled the delivery, and its result is on its way).
	giveUp := func(made wire.InjectResult) wire.InjectResult {
		if settled.CompareAndSwap(false, true) {
			return made
		}
		return <-done
	}
	var res wire.InjectResult
	select {
	case res = <-done:
	case <-ctx.Done():
		// The deliverer saw the same end. A result that came with it is the
		// answer; none is a delivery that outlasted its time.
		select {
		case res = <-done:
		default:
			res = giveUp(resultTimedOut)
		}
	case <-s.stop:
		cancel()
		select {
		case res = <-done:
		default:
			res = giveUp(resultClosed)
		}
	}
	res = encodable(res)

	after := ev
	after.Outcome, after.Reason = string(res.Outcome), string(res.Reason)
	after.State = "written-" + string(res.Written)
	s.emit(eventlog.EventInject, after)
	return wire.Response{OK: true, Inject: &res}
}

// deliver calls the deliverer and sends what it returned, or the result for a
// panic, on done (which has room). The delivery is over, and the next may
// start, when the deliverer has returned: the flag is cleared before the result
// is sent, so a client that has its answer is never refused busy for this
// delivery. A panic is recovered and its value dropped: it can hold the text.
//
// Whoever comes first, the handler giving up or the deliverer returning,
// settles the delivery. A deliverer that returns after the handler has given
// up (the answer was uncertain) has what it reported logged, as a third inject
// event with the identity and the digest of the others ([lateEvent]). It is
// the third in the log too: the deliverer waits for answered, which the handler
// closes when it has logged its answer. The handler does not block between
// giving up and that, so the wait is short.
func (s *Server) deliver(ctx context.Context, cancel context.CancelFunc, d Deliverer, messageID, text string, ev eventlog.Fields, settled *atomic.Bool, answered <-chan struct{}, done chan<- wire.InjectResult) {
	res := resultPanicked
	defer cancel() // after the result is sent, so the handler does not see an end before it
	defer func() {
		if !settled.CompareAndSwap(false, true) {
			// Before the flag is cleared, so the event comes before that of
			// the next delivery, and after the handler's answer event, so
			// it is the last of this one.
			<-answered
			s.emit(eventlog.EventInject, lateEvent(ev, res))
		}
		s.inj.running.Store(false)
		done <- res
	}()
	defer func() { _ = recover() }()
	res = d.Deliver(ctx, messageID, text)
}

// lateEvent is the event for the result that a deliverer returned after the
// handler had answered: ev (the message, command and digest) with the
// outcome "late-" and the result's, its reason, and its written count as the
// state. A result that the wire does not carry is "late-invalid", with
// nothing else of it: its words are not trusted in the log.
func lateEvent(ev eventlog.Fields, res wire.InjectResult) eventlog.Fields {
	if encodable(res) != res {
		ev.Outcome = "late-invalid"
		return ev
	}
	ev.Outcome, ev.Reason = "late-"+string(res.Outcome), string(res.Reason)
	ev.State = "written-" + string(res.Written)
	return ev
}

// encodable returns res when the wire carries it, else the result for one that
// cannot be encoded.
func encodable(res wire.InjectResult) wire.InjectResult {
	if res.Validate() != nil {
		return resultUnencodable
	}
	if _, err := wire.EncodeResponse(wire.Response{OK: true, Inject: &res}); err != nil {
		return resultUnencodable
	}
	return res
}
