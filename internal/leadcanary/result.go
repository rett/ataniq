//go:build linux && ataniq_canary

package leadcanary

import "ataniq/internal/leadholder/wire"

// invalidResultDetail is the detail of the result that stands in for one the
// wire does not carry.
const invalidResultDetail = "the result is not one the wire carries"

// NoAnswerResult returns the result that stands in for an inject that was
// written to the holder and got no usable answer (no answer in time, a closed
// connection, a frame or decode failure): uncertain, died, unknown, with the
// detail "no answer from the holder". The message is spent, so this is a
// result and not an error; the evidence writes it as an inject-result line.
func NoAnswerResult() wire.InjectResult {
	return wire.InjectResult{
		Outcome: wire.InjectOutcomeUncertain,
		Reason:  wire.InjectReasonDied,
		Written: wire.InjectWrittenUnknown,
		Detail:  "no answer from the holder",
	}
}

// trusted returns r when it is a result the wire carries, and otherwise an
// uncertain, died, unknown result that says so: a result that contradicts the
// wire's own rules is not believed.
func trusted(r wire.InjectResult) wire.InjectResult {
	if r.Validate() == nil {
		return r
	}
	return wire.InjectResult{
		Outcome: wire.InjectOutcomeUncertain,
		Reason:  wire.InjectReasonDied,
		Written: wire.InjectWrittenUnknown,
		Detail:  invalidResultDetail,
	}
}

// ResultEntry turns a wire result into the inject-result evidence entry for the
// message and the epoch: outcome, reason, written and detail, as the wire has
// them. A result that is not valid by [wire.InjectResult.Validate] is entered
// as the uncertain, died, unknown result that stands in for it, with a detail
// that says so; nothing the holder sent in it is written.
func ResultEntry(messageID string, epoch uint64, r wire.InjectResult) Entry {
	r = trusted(r)
	return Entry{
		Event: EventInjectResult, Epoch: epoch, MessageID: messageID,
		Outcome: string(r.Outcome), Reason: string(r.Reason), Written: string(r.Written),
		Detail: r.Detail,
	}
}

// Judge says what a result means for the epoch, as section 2 of the seams
// note rules.
//
// spent is true for every result, "not-now" included: once the request frame is
// written the message is spent for the epoch, whatever the answer was (even
// not-now or refused, with nothing written), and a second message in the epoch
// is refused. This is the plan's "one message per epoch": no queue and no retry,
// and a not-now result must not be sent again in the same epoch. Only a refusal
// before the frame is written leaves the message unspent, and that is not a
// result.
//
// uncertain is true when the outcome is uncertain, and for a result that is not
// valid by [wire.InjectResult.Validate] (which is not believed). An uncertain
// result needs an operator's decision.
func Judge(r wire.InjectResult) (spent, uncertain bool) {
	r = trusted(r)
	return true, r.Outcome == wire.InjectOutcomeUncertain
}

// ReleasesMessage says whether a lead of scratch mode takes a result as a
// message that was not delivered, so that the epoch's message is not spent.
// The canary does not use it: its rule, [Judge], spends the message on every
// result.
//
// It is true only for a result that is valid by [wire.InjectResult.Validate],
// whose outcome is "not-now" or "refused" and whose written is "nothing": the
// holder says it wrote nothing to the lead's input. It is false for everything
// else, so a message that may have reached the lead never looks unspent: a
// submitted result, any uncertain one (the "died" result with written
// "nothing" included, since the lead's end during the send leaves open what
// reached it) and a result the wire would refuse.
func ReleasesMessage(r wire.InjectResult) bool {
	if r.Validate() != nil || r.Written != wire.InjectWrittenNothing {
		return false
	}
	return r.Outcome == wire.InjectOutcomeNotNow || r.Outcome == wire.InjectOutcomeRefused
}
