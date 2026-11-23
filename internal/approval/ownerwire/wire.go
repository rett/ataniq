package ownerwire

import "ataniq/internal/approval/record"

// This file holds the Decode and Encode functions of every payload. Each
// pair shares one cap and one set of checks, so what an Encode function
// returns always decodes, and what a Decode function returns always
// encodes.

// payload is how one payload type travels: its name in a detail, its cap,
// the code for a broken form, and the checks of its content.
type payload[T any] struct {
	name    string
	limit   int
	invalid record.Code
	check   func(*T) error
}

func (p payload[T]) decode(data []byte) (T, error) {
	var v, zero T
	if err := decode(data, p.limit, p.invalid, p.name, &v); err != nil {
		return zero, err
	}
	if p.check != nil {
		if err := p.check(&v); err != nil {
			return zero, err
		}
	}
	return v, nil
}

func (p payload[T]) encode(v T) ([]byte, error) {
	if p.check != nil {
		if err := p.check(&v); err != nil {
			return nil, err
		}
	}
	return encode(&v, p.limit, p.invalid, p.name)
}

var (
	nothingForm        = payload[Nothing]{"nothing", MaxParamsBytes, record.CodeRecordInvalid, nil}
	listingForm        = payload[Listing]{"listing", MaxPageBytes, record.CodeRecordInvalid, checkListing}
	showParamsForm     = payload[ShowParams]{"show", MaxParamsBytes, record.CodeRecordInvalid, checkShowParams}
	showResultForm     = payload[ShowResult]{"shown", MaxPageBytes, record.CodeRecordInvalid, checkShowResult}
	pageParamsForm     = payload[PageParams]{"page", MaxParamsBytes, record.CodeRecordInvalid, checkPageParams}
	pathsPageForm      = payload[PathsPage]{"paths", MaxPageBytes, record.CodeRecordInvalid, checkPathsPage}
	patchPageForm      = payload[PatchPage]{"patch", MaxPageBytes, record.CodeRecordInvalid, checkPatchPage}
	deciderParamsForm  = payload[DeciderParams]{"decider", MaxParamsBytes, record.CodeRecordInvalid, checkDeciderParams}
	deciderResultForm  = payload[DeciderResult]{"decider", MaxResultBytes, record.CodeRecordInvalid, checkDeciderResult}
	decisionFrameForm  = payload[DecisionFrame]{"decision", MaxFrameBytes, record.CodeDecisionInvalid, checkDecisionFrame}
	decideResultForm   = payload[DecideResult]{"outcome", MaxResultBytes, record.CodeRecordInvalid, checkDecideResult}
	limitParamsForm    = payload[LimitParams]{"limit", MaxParamsBytes, record.CodeRecordInvalid, checkLimitParams}
	limitSetParamsForm = payload[LimitSetParams]{"limit", MaxParamsBytes, record.CodeRecordInvalid, checkLimitSetParams}
	limitViewForm      = payload[LimitView]{"limit", MaxResultBytes, record.CodeRecordInvalid, checkLimitView}

	pairStartParamsForm   = payload[PairStartParams]{"pair start", MaxParamsBytes, record.CodeRecordInvalid, checkPairStartParams}
	pairStartResultForm   = payload[PairStartResult]{"pair started", MaxResultBytes, record.CodeRecordInvalid, checkPairStartResult}
	pairStatusForm        = payload[PairStatus]{"pair status", MaxResultBytes, record.CodeRecordInvalid, checkPairStatus}
	pairPendingParamsForm = payload[PairPendingParams]{"pair pending", MaxParamsBytes, record.CodeRecordInvalid, checkPairPendingParams}
	seatPairParamsForm    = payload[SeatPairParams]{"seat pair", MaxSeatPairParamsBytes, record.CodeRecordInvalid, checkSeatPairParams}
	seatPairResultForm    = payload[SeatPairResult]{"seat paired", MaxResultBytes, record.CodeRecordInvalid, checkSeatPairResult}
	keyItemForm           = payload[KeyItem]{"key", MaxResultBytes, record.CodeRecordInvalid, checkKeyItem}
	keysViewForm          = payload[KeysView]{"keys", MaxResultBytes, record.CodeRecordInvalid, checkKeysView}
	keyRevokeParamsForm   = payload[KeyRevokeParams]{"key revoke", MaxParamsBytes, record.CodeRecordInvalid, checkKeyRevokeParams}
)

// DecodeNothing reads the params of an operation that takes none (owner.list,
// owner.beat, owner.looked, owner.pair-status, owner.keys, owner.keys-off) or the result of one that
// answers nothing (owner.beat, owner.looked, owner.pair-reject). It accepts an empty JSON object and refuses
// everything else record-invalid, or record-too-large over [MaxParamsBytes].
func DecodeNothing(data []byte) error {
	_, err := nothingForm.decode(data)
	return err
}

// EncodeNothing returns the empty JSON object that [DecodeNothing] accepts.
func EncodeNothing() []byte {
	return []byte("{}")
}

// DecodeListing reads the result of owner.list. A listing over
// [MaxPageBytes] is refused record-too-large; anything else that is wrong
// record-invalid.
func DecodeListing(data []byte) (Listing, error) { return listingForm.decode(data) }

// EncodeListing writes the result of owner.list. It refuses a listing that
// would be over [MaxPageBytes] record-too-large and cuts nothing itself:
// the caller drops recent decisions, oldest first, and encodes again.
func EncodeListing(l Listing) ([]byte, error) { return listingForm.encode(l) }

// DecodeShowParams reads the params of owner.show. Params over
// [MaxParamsBytes] are refused record-too-large; anything else that is
// wrong record-invalid.
func DecodeShowParams(data []byte) (ShowParams, error) { return showParamsForm.decode(data) }

// EncodeShowParams writes the params of owner.show.
func EncodeShowParams(p ShowParams) ([]byte, error) { return showParamsForm.encode(p) }

// DecodeShowResult reads the result of owner.show. It checks that the
// digest of each record it carries is the digest of that record's bytes,
// and refuses binding-mismatch when it is not. It does not read the
// records themselves: the caller hands the bytes to [record.DecodePending].
// A result over [MaxPageBytes], or a record over [record.MaxPendingBytes],
// is refused record-too-large; anything else that is wrong record-invalid.
func DecodeShowResult(data []byte) (ShowResult, error) { return showResultForm.decode(data) }

// EncodeShowResult writes the result of owner.show, with each record's
// bytes as they are.
func EncodeShowResult(r ShowResult) ([]byte, error) { return showResultForm.encode(r) }

// DecodePageParams reads the params of owner.paths and of owner.patch.
// Params over [MaxParamsBytes] are refused record-too-large; anything else
// that is wrong record-invalid.
func DecodePageParams(data []byte) (PageParams, error) { return pageParamsForm.decode(data) }

// EncodePageParams writes the params of owner.paths and of owner.patch.
func EncodePageParams(p PageParams) ([]byte, error) { return pageParamsForm.encode(p) }

// DecodePathsPage reads the result of owner.paths. A page over
// [MaxPageBytes] is refused record-too-large; anything else that is wrong
// record-invalid.
func DecodePathsPage(data []byte) (PathsPage, error) { return pathsPageForm.decode(data) }

// EncodePathsPage writes the result of owner.paths. It refuses a page that
// would be over [MaxPageBytes]; [SplitPaths] makes pages that fit.
func EncodePathsPage(p PathsPage) ([]byte, error) { return pathsPageForm.encode(p) }

// DecodePatchPage reads the result of owner.patch. A page over
// [MaxPageBytes] is refused record-too-large; anything else that is wrong
// record-invalid, which includes a page the console's pager would end the
// reading on: an empty piece that continues, a line in more than
// [MaxLinePieces] pieces, a page without a piece that is not the one page
// of an empty patch, and a last page whose last line continues.
func DecodePatchPage(data []byte) (PatchPage, error) { return patchPageForm.decode(data) }

// EncodePatchPage writes the result of owner.patch. It refuses a page that
// would be over [MaxPageBytes]; [SplitPatch] makes pages that fit.
func EncodePatchPage(p PatchPage) ([]byte, error) { return patchPageForm.encode(p) }

// DecodeDeciderParams reads the params of owner.decider. Params over
// [MaxParamsBytes] are refused record-too-large; anything else that is
// wrong, such as a session that is not 16 lower-case hexadecimal digits,
// record-invalid.
func DecodeDeciderParams(data []byte) (DeciderParams, error) { return deciderParamsForm.decode(data) }

// EncodeDeciderParams writes the params of owner.decider.
func EncodeDeciderParams(p DeciderParams) ([]byte, error) { return deciderParamsForm.encode(p) }

// DecodeDeciderResult reads the result of owner.decider. A result over
// [MaxResultBytes] is refused record-too-large; anything else that is
// wrong record-invalid.
func DecodeDeciderResult(data []byte) (DeciderResult, error) { return deciderResultForm.decode(data) }

// EncodeDeciderResult writes the result of owner.decider.
func EncodeDeciderResult(r DeciderResult) ([]byte, error) { return deciderResultForm.encode(r) }

// DecodeDecisionFrame reads the params of owner.decide. A frame over
// [MaxFrameBytes] is refused record-too-large. Everything else that is
// wrong is refused decision-invalid: a field the frame does not name, which
// is every field the daemon fills; a decision that is not approve or deny;
// an approve with a reason; a deny with sign-offs; a reason over
// [record.MaxReasonBytes]; a sign-off of no known kind, or one that is
// there twice.
//
// Whether the frame fits the pending record (its digest, its sign-offs
// against the flagged list) is for the daemon, which holds the record.
func DecodeDecisionFrame(data []byte) (DecisionFrame, error) { return decisionFrameForm.decode(data) }

// EncodeDecisionFrame writes the params of owner.decide. It makes the
// checks of [DecodeDecisionFrame].
func EncodeDecisionFrame(f DecisionFrame) ([]byte, error) { return decisionFrameForm.encode(f) }

// DecodeDecideResult reads the result of owner.decide. A result over
// [MaxResultBytes] is refused record-too-large; anything else that is
// wrong record-invalid.
func DecodeDecideResult(data []byte) (DecideResult, error) { return decideResultForm.decode(data) }

// EncodeDecideResult writes the result of owner.decide.
func EncodeDecideResult(r DecideResult) ([]byte, error) { return decideResultForm.encode(r) }

// DecodeLimitParams reads the params of owner.limit. Params over
// [MaxParamsBytes] are refused record-too-large; anything else that is
// wrong record-invalid.
func DecodeLimitParams(data []byte) (LimitParams, error) { return limitParamsForm.decode(data) }

// EncodeLimitParams writes the params of owner.limit.
func EncodeLimitParams(p LimitParams) ([]byte, error) { return limitParamsForm.encode(p) }

// DecodeLimitSetParams reads the params of owner.limit-set. Params over
// [MaxParamsBytes] are refused record-too-large; anything else that is
// wrong record-invalid. Whether the change is allowed is the limit's own
// rule, and the daemon answers that with limit-refused.
func DecodeLimitSetParams(data []byte) (LimitSetParams, error) {
	return limitSetParamsForm.decode(data)
}

// EncodeLimitSetParams writes the params of owner.limit-set.
func EncodeLimitSetParams(p LimitSetParams) ([]byte, error) { return limitSetParamsForm.encode(p) }

// DecodeLimitView reads the result of owner.limit and of owner.limit-set. A
// view over [MaxResultBytes] is refused record-too-large; anything else
// that is wrong record-invalid.
func DecodeLimitView(data []byte) (LimitView, error) { return limitViewForm.decode(data) }

// EncodeLimitView writes the result of owner.limit and of owner.limit-set.
func EncodeLimitView(v LimitView) ([]byte, error) { return limitViewForm.encode(v) }

// DecodePairStartParams reads the params of owner.pair-start. Params over
// [MaxParamsBytes] are refused record-too-large; anything else that is wrong
// record-invalid.
func DecodePairStartParams(data []byte) (PairStartParams, error) {
	return pairStartParamsForm.decode(data)
}

// EncodePairStartParams writes the params of owner.pair-start.
func EncodePairStartParams(p PairStartParams) ([]byte, error) { return pairStartParamsForm.encode(p) }

// DecodePairStartResult reads the result of owner.pair-start, which holds the
// pairing code. A result over [MaxResultBytes] is refused record-too-large;
// anything else that is wrong, such as a code of another form, record-invalid.
func DecodePairStartResult(data []byte) (PairStartResult, error) {
	return pairStartResultForm.decode(data)
}

// EncodePairStartResult writes the result of owner.pair-start.
func EncodePairStartResult(r PairStartResult) ([]byte, error) {
	return pairStartResultForm.encode(r)
}

// DecodePairStatus reads the result of owner.pair-status. A result over
// [MaxResultBytes] is refused record-too-large; anything else that is wrong,
// such as words for a pairing that is not pending, or none for one that is,
// record-invalid.
func DecodePairStatus(data []byte) (PairStatus, error) { return pairStatusForm.decode(data) }

// EncodePairStatus writes the result of owner.pair-status.
func EncodePairStatus(s PairStatus) ([]byte, error) { return pairStatusForm.encode(s) }

// DecodePairPendingParams reads the params of owner.pair-confirm and of
// owner.pair-reject. The pending id is required: a call without it, with
// another field, or with an id that is not 16 lower-case hexadecimal digits
// is refused record-invalid, and params over [MaxParamsBytes]
// record-too-large.
func DecodePairPendingParams(data []byte) (PairPendingParams, error) {
	return pairPendingParamsForm.decode(data)
}

// EncodePairPendingParams writes the params of owner.pair-confirm and of
// owner.pair-reject.
func EncodePairPendingParams(p PairPendingParams) ([]byte, error) {
	return pairPendingParamsForm.encode(p)
}

// DecodeSeatPairParams reads the params of seat.pair. Params over
// [MaxParamsBytes] are refused record-too-large; anything else that is wrong,
// such as a code of another form or a credential id of another length,
// record-invalid. Whether the key is a key is for the daemon.
func DecodeSeatPairParams(data []byte) (SeatPairParams, error) {
	return seatPairParamsForm.decode(data)
}

// EncodeSeatPairParams writes the params of seat.pair.
func EncodeSeatPairParams(p SeatPairParams) ([]byte, error) { return seatPairParamsForm.encode(p) }

// DecodeSeatPairResult reads the result of seat.pair. A result over
// [MaxResultBytes] is refused record-too-large; anything else that is wrong
// record-invalid.
func DecodeSeatPairResult(data []byte) (SeatPairResult, error) {
	return seatPairResultForm.decode(data)
}

// EncodeSeatPairResult writes the result of seat.pair.
func EncodeSeatPairResult(r SeatPairResult) ([]byte, error) { return seatPairResultForm.encode(r) }

// DecodeKeyItem reads the result of owner.pair-confirm. A result over
// [MaxResultBytes] is refused record-too-large; anything else that is wrong
// record-invalid.
func DecodeKeyItem(data []byte) (KeyItem, error) { return keyItemForm.decode(data) }

// EncodeKeyItem writes the result of owner.pair-confirm.
func EncodeKeyItem(k KeyItem) ([]byte, error) { return keyItemForm.encode(k) }

// DecodeKeysView reads the result of owner.keys, owner.key-revoke and
// owner.keys-off. A result over [MaxResultBytes] is refused record-too-large;
// anything else that is wrong, such as more than [MaxKeys] keys or an id
// twice, record-invalid.
func DecodeKeysView(data []byte) (KeysView, error) { return keysViewForm.decode(data) }

// EncodeKeysView writes the result of owner.keys, owner.key-revoke and
// owner.keys-off.
func EncodeKeysView(v KeysView) ([]byte, error) { return keysViewForm.encode(v) }

// DecodeKeyRevokeParams reads the params of owner.key-revoke. Params over
// [MaxParamsBytes] are refused record-too-large; anything else that is wrong
// record-invalid. Whether the id names exactly one credential is for the
// daemon.
func DecodeKeyRevokeParams(data []byte) (KeyRevokeParams, error) {
	return keyRevokeParamsForm.decode(data)
}

// EncodeKeyRevokeParams writes the params of owner.key-revoke.
func EncodeKeyRevokeParams(p KeyRevokeParams) ([]byte, error) { return keyRevokeParamsForm.encode(p) }
