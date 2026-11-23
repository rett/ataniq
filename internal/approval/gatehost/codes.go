package gatehost

import (
	"context"
	"errors"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/seatgate"
)

// The codes the handler answers with. Each is of the grammar of
// [seatgate.ValidCode] and is never a text for a person.
const (
	// CodeUnknownOp answers an operation the handler does not serve.
	CodeUnknownOp = "unknown-op"
	// CodeBadParams answers params that the gate's strict reading or the
	// operation's decoder refuses.
	CodeBadParams = "bad-params"
	// CodeNotPinned answers a peer whose key the pin check does not know.
	CodeNotPinned = "not-pinned"
	// CodeUnavailable answers a call the service cannot take: it is closed,
	// stopped, or the call was cancelled.
	CodeUnavailable = "unavailable"
	// CodeDeadline answers a call whose deadline passed in the service.
	CodeDeadline = seatgate.CodeDeadline
	// CodeFailed answers anything else; the cause is in the log.
	CodeFailed = seatgate.CodeFailed
	// CodeDoorUnavailable answers a call the door could not serve.
	CodeDoorUnavailable = "door-unavailable"
	// CodeJournalReopened answers a call after which the journal was
	// reopened; the seat looks again.
	CodeJournalReopened = "journal-reopened"
)

// The codes of the pairing and key errors of the service, the same words the
// console knows them by.
const (
	CodeKeysUnavailable   = "keys-unavailable"
	CodeKeysFull          = "keys-full"
	CodeKeyUnknown        = "key-unknown"
	CodeKeyAmbiguous      = "key-ambiguous"
	CodePairingOpen       = "pairing-open"
	CodePairingNone       = "pairing-none"
	CodePairingExpired    = "pairing-expired"
	CodePairingChanged    = "pairing-changed"
	CodePairingRefused    = "pairing-refused"
	CodeCredentialInvalid = "credential-invalid"
)

// sentinels maps each error of the service that has a code of its own to the
// code, in the order they are looked for.
var sentinels = []struct {
	err  error
	code string
}{
	{ownerchannel.ErrClosed, CodeUnavailable},
	{ownerchannel.ErrStopped, CodeUnavailable},
	{ownerchannel.ErrKeysUnavailable, CodeKeysUnavailable},
	{ownerchannel.ErrKeysFull, CodeKeysFull},
	{ownerchannel.ErrKeyUnknown, CodeKeyUnknown},
	{ownerchannel.ErrKeyAmbiguous, CodeKeyAmbiguous},
	{ownerchannel.ErrPairingOpen, CodePairingOpen},
	{ownerchannel.ErrNoPairing, CodePairingNone},
	{ownerchannel.ErrPairingExpired, CodePairingExpired},
	{ownerchannel.ErrPairingChanged, CodePairingChanged},
	{ownerchannel.ErrPairRefused, CodePairingRefused},
	{ownerchannel.ErrCredentialInvalid, CodeCredentialInvalid},
	{ownerchannel.ErrDoorUnavailable, CodeDoorUnavailable},
	{ownerchannel.ErrJournalReopened, CodeJournalReopened},
	{ownerchannel.ErrSeatParams, CodeBadParams},
	{ownerchannel.ErrChallengeStale, ownerwire.CodeChallengeStale},
	{ownerchannel.ErrChallengesFull, ownerwire.CodeChallengesFull},
	{ownerchannel.ErrAssertionInvalid, ownerwire.CodeAssertionInvalid},
	{ownerchannel.ErrKeyRevoked, ownerwire.CodeKeyRevoked},
	{ownerchannel.ErrKindRefused, ownerwire.CodeKindRefused},
}

// passedRefusals are the refusal codes of the service that a seat is told as
// they are: the ones the pairing call, the reads, the login and the decision
// can meet. Any other refusal is answered failed.
var passedRefusals = map[record.Code]bool{
	record.CodeBindingMismatch:  true,
	record.CodeApprovalVoid:     true,
	record.CodeApprovalExpired:  true,
	record.CodeApprovalConsumed: true,
	record.CodeDecisionInvalid:  true,
	record.CodePreviousEpoch:    true,
	record.CodeReviewChanged:    true,
	record.CodeRecordInvalid:    true,
	record.CodeRecordTooLarge:   true,
}

// codeOf is the code to tell a seat of an error of the service. A refusal is
// looked at first, as the daemon's owner connection does, so that a refusal
// the door answered keeps its code; [ownerchannel.ErrNotSeat], which this
// handler never causes, is a failure like any error with no code.
func codeOf(err error) string {
	var re *record.Error
	switch {
	case errors.As(err, &re):
		if passedRefusals[re.Code] {
			return string(re.Code)
		}
		return CodeFailed
	case errors.Is(err, context.Canceled):
		return CodeUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return CodeDeadline
	}
	for _, s := range sentinels {
		if errors.Is(err, s.err) {
			return s.code
		}
	}
	return CodeFailed
}
