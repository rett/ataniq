package wire

import (
	"unicode"
	"unicode/utf8"
)

// Caps of the inject operation.
const (
	// MaxInjectText is the longest text of an inject request, in bytes.
	MaxInjectText = 4096
	// MaxMessageID is the longest message id of an inject request.
	MaxMessageID = 64
	// MaxInjectDetail is the longest detail of an [InjectResult], in bytes.
	MaxInjectDetail = 512
)

// CheckMessageID checks the message id of an inject request: 1 to
// [MaxMessageID] bytes, the first a lower-case letter or a digit and each
// other one a lower-case letter, a digit or one of the signs . _ -.
func CheckMessageID(s string) error {
	if len(s) < 1 || len(s) > MaxMessageID {
		return invalidf("message_id is %d bytes, want 1 to %d", len(s), MaxMessageID)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || (c != '.' && c != '_' && c != '-')) {
			return invalidf("message_id does not match [a-z0-9][a-z0-9._-]{0,%d}", MaxMessageID-1)
		}
	}
	return nil
}

// CheckInjectText checks the text of an inject request: 1 to
// [MaxInjectText] bytes of valid UTF-8, each character a newline or one that
// [unicode.IsPrint] accepts (so no other control character, no tab, no format
// character and no line or paragraph separator). The error never repeats the
// text.
func CheckInjectText(s string) error {
	if len(s) < 1 || len(s) > MaxInjectText {
		return invalidf("text is %d bytes, want 1 to %d", len(s), MaxInjectText)
	}
	if !utf8.ValidString(s) {
		return invalidf("text is not valid UTF-8")
	}
	for _, r := range s {
		if r != '\n' && !unicode.IsPrint(r) {
			return invalidf("text has a character that is neither a newline nor printable (U+%04X)", r)
		}
	}
	return nil
}

// InjectOutcome is what happened to an inject request that reached the
// deliverer, in the words of the wire.
type InjectOutcome string

// The outcomes. A refusal of the request itself is a [Response] with a code
// and never an [InjectResult].
const (
	// InjectOutcomeNotNow: the lead is not in a state that takes a message,
	// and nothing was written. Asking again later may work.
	InjectOutcomeNotNow InjectOutcome = "not-now"
	// InjectOutcomeRefused: the message cannot be delivered as it is or to
	// this lead, and nothing was written.
	InjectOutcomeRefused InjectOutcome = "refused"
	// InjectOutcomeSubmitted: the whole message was written and sent.
	InjectOutcomeSubmitted InjectOutcome = "submitted"
	// InjectOutcomeUncertain: what reached the lead is not known for sure.
	// Only a person can settle it.
	InjectOutcomeUncertain InjectOutcome = "uncertain"
)

// InjectReason says why an outcome other than submitted is what it is.
type InjectReason string

// The reasons. [InjectReasonNone] goes with [InjectOutcomeSubmitted] only.
const (
	InjectReasonNone         InjectReason = ""
	InjectReasonNotReady     InjectReason = "not-ready"
	InjectReasonBlocked      InjectReason = "blocked"
	InjectReasonUntested     InjectReason = "untested"
	InjectReasonText         InjectReason = "text"
	InjectReasonNotSubmitted InjectReason = "not-submitted"
	InjectReasonDied         InjectReason = "died"
)

// InjectWritten says how much of the message reached the lead's input.
type InjectWritten string

// The amounts.
const (
	InjectWrittenNothing InjectWritten = "nothing"
	InjectWrittenPartial InjectWritten = "partial"
	InjectWrittenWhole   InjectWritten = "whole"
	InjectWrittenUnknown InjectWritten = "unknown"
)

// InjectResult is the answer of a delivery, carried in an ok [Response]. Each
// of its three words is from a closed set, and only these combinations are
// valid (anything else is refused when it is encoded and when it is decoded):
//
//	outcome    reason         written
//	not-now    not-ready      nothing
//	not-now    blocked        nothing
//	refused    untested       nothing
//	refused    text           nothing
//	uncertain  not-submitted  partial
//	submitted  (none)         whole
//	uncertain  died           nothing, partial, whole or unknown
//
// Detail is for a person reading a log: printable ASCII (0x20 to 0x7e), at
// most [MaxInjectDetail] bytes, and nothing branches on it.
type InjectResult struct {
	Outcome InjectOutcome `json:"outcome" wire:"required"`
	Reason  InjectReason  `json:"reason,omitempty"`
	Written InjectWritten `json:"written" wire:"required"`
	Detail  string        `json:"detail,omitempty"`
}

// validInject lists the valid combinations of an [InjectResult].
var validInject = []struct {
	outcome InjectOutcome
	reason  InjectReason
	written InjectWritten
}{
	{InjectOutcomeNotNow, InjectReasonNotReady, InjectWrittenNothing},
	{InjectOutcomeNotNow, InjectReasonBlocked, InjectWrittenNothing},
	{InjectOutcomeRefused, InjectReasonUntested, InjectWrittenNothing},
	{InjectOutcomeRefused, InjectReasonText, InjectWrittenNothing},
	{InjectOutcomeUncertain, InjectReasonNotSubmitted, InjectWrittenPartial},
	{InjectOutcomeSubmitted, InjectReasonNone, InjectWrittenWhole},
	{InjectOutcomeUncertain, InjectReasonDied, InjectWrittenNothing},
	{InjectOutcomeUncertain, InjectReasonDied, InjectWrittenPartial},
	{InjectOutcomeUncertain, InjectReasonDied, InjectWrittenWhole},
	{InjectOutcomeUncertain, InjectReasonDied, InjectWrittenUnknown},
}

// CheckInjectDetail checks the detail of an [InjectResult]: at most
// [MaxInjectDetail] bytes, each printable ASCII (0x20 to 0x7e).
func CheckInjectDetail(s string) error {
	if len(s) > MaxInjectDetail {
		return invalidf("detail is %d bytes, want at most %d", len(s), MaxInjectDetail)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return invalidf("detail has a byte outside printable ASCII at offset %d", i)
		}
	}
	return nil
}

// Validate reports whether the result is one that the wire carries: each word
// is in its set, the three together are one of the valid combinations, and
// the detail fits. The error wraps [ErrInvalid].
func (r InjectResult) Validate() error {
	switch r.Outcome {
	case InjectOutcomeNotNow, InjectOutcomeRefused, InjectOutcomeSubmitted, InjectOutcomeUncertain:
	default:
		return invalidf("inject outcome is %q", string(r.Outcome))
	}
	switch r.Reason {
	case InjectReasonNone, InjectReasonNotReady, InjectReasonBlocked, InjectReasonUntested,
		InjectReasonText, InjectReasonNotSubmitted, InjectReasonDied:
	default:
		return invalidf("inject reason is %q", string(r.Reason))
	}
	switch r.Written {
	case InjectWrittenNothing, InjectWrittenPartial, InjectWrittenWhole, InjectWrittenUnknown:
	default:
		return invalidf("inject written is %q", string(r.Written))
	}
	ok := false
	for _, v := range validInject {
		if v.outcome == r.Outcome && v.reason == r.Reason && v.written == r.Written {
			ok = true
			break
		}
	}
	if !ok {
		return invalidf("inject result %q, %q, %q is not a valid combination", string(r.Outcome), string(r.Reason), string(r.Written))
	}
	return CheckInjectDetail(r.Detail)
}
