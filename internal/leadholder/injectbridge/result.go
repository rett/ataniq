//go:build linux && ataniq_canary

package injectbridge

import (
	"strings"

	"ataniq/internal/inject"
	"ataniq/internal/leadholder/wire"
)

// contradictory starts the detail of a result whose outcome and written do
// not go together.
const contradictory = "contradictory result: "

// WireResult maps the library's result to the result the control wire
// carries. It is the only mapping:
//
//	outcome            written           wire outcome  reason         written
//	not idle           nothing           not-now       not-ready      nothing
//	blocked            nothing           not-now       blocked        nothing
//	untested version   nothing           refused       untested       nothing
//	refused text       nothing           refused       text           nothing
//	not submitted      paste             uncertain     not-submitted  partial
//	submitted          paste and Enter   submitted     (none)         whole
//	died               nothing           uncertain     died           nothing
//	died               paste             uncertain     died           partial
//	died               paste and Enter   uncertain     died           whole
//	died               unknown           uncertain     died           unknown
//	any other pair                       uncertain     died           unknown
//
// A pair outside the table is a result the library never makes; it is
// answered as the least that is known (something may have been written), and
// its detail starts "contradictory result: ".
//
// The detail is the result's Readings alone when they already contain the
// result's Detail (they begin with the outcome and what was written and hold
// the Detail after them), so that the Detail is not spent twice; otherwise it
// is the Detail, " | " and the Readings. Either is made printable ASCII and
// cut to the wire's length by wire.SanitizeDetail. Nothing branches on it. Every result returned passes wire.InjectResult.Validate.
func WireResult(r inject.Result) wire.InjectResult {
	out, ok := wireWords(r.Outcome, r.Written)
	detail := r.Readings()
	if !strings.Contains(detail, r.Detail) {
		detail = r.Detail + " | " + detail
	}
	if !ok {
		detail = contradictory + detail
	}
	out.Detail = wire.SanitizeDetail(detail)
	return out
}

// wireWords is the table of WireResult. It reports false for a pair that is
// not in it, with the words of the last row.
func wireWords(o inject.Outcome, w inject.Written) (wire.InjectResult, bool) {
	words := func(o wire.InjectOutcome, r wire.InjectReason, w wire.InjectWritten) (wire.InjectResult, bool) {
		return wire.InjectResult{Outcome: o, Reason: r, Written: w}, true
	}
	nothing := w == inject.WrittenNothing
	switch {
	case o == inject.OutcomeNotIdle && nothing:
		return words(wire.InjectOutcomeNotNow, wire.InjectReasonNotReady, wire.InjectWrittenNothing)
	case o == inject.OutcomeBlocked && nothing:
		return words(wire.InjectOutcomeNotNow, wire.InjectReasonBlocked, wire.InjectWrittenNothing)
	case o == inject.OutcomeUntestedVersion && nothing:
		return words(wire.InjectOutcomeRefused, wire.InjectReasonUntested, wire.InjectWrittenNothing)
	case o == inject.OutcomeRefusedText && nothing:
		return words(wire.InjectOutcomeRefused, wire.InjectReasonText, wire.InjectWrittenNothing)
	case o == inject.OutcomeNotSubmitted && w == inject.WrittenPaste:
		return words(wire.InjectOutcomeUncertain, wire.InjectReasonNotSubmitted, wire.InjectWrittenPartial)
	case o == inject.OutcomeSubmitted && w == inject.WrittenPasteAndEnter:
		return words(wire.InjectOutcomeSubmitted, wire.InjectReasonNone, wire.InjectWrittenWhole)
	case o == inject.OutcomeDied:
		switch w {
		case inject.WrittenNothing:
			return words(wire.InjectOutcomeUncertain, wire.InjectReasonDied, wire.InjectWrittenNothing)
		case inject.WrittenPaste:
			return words(wire.InjectOutcomeUncertain, wire.InjectReasonDied, wire.InjectWrittenPartial)
		case inject.WrittenPasteAndEnter:
			return words(wire.InjectOutcomeUncertain, wire.InjectReasonDied, wire.InjectWrittenWhole)
		case inject.WrittenUnknown:
			return words(wire.InjectOutcomeUncertain, wire.InjectReasonDied, wire.InjectWrittenUnknown)
		}
	}
	return wire.InjectResult{
		Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonDied, Written: wire.InjectWrittenUnknown,
	}, false
}
