package consoleclient

import (
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
)

// This file holds the operations of [Owner] that pair a phone and keep the
// list of deciding keys. They return the types the wire package defines, and
// decide nothing: the daemon is the one that knows who may pair, and the rules
// are its own. Every call is sent once, on the connection as it is: nothing is
// sent again after a failure, so a start whose answer did not come back is
// started again by the owner (with replace, if the first one made a pairing),
// and the code that answer carried is lost with it.

// PairError is a refusal of the daemon that is one of the pairing and key
// operations' own. Its text is fixed here: nothing the daemon sent is in it.
// The refusals the owner connection shares with the rest (not-decider,
// owner-away, record-invalid) are a [*record.Error], as for a decision.
type PairError struct {
	// Code is the daemon's code: one of the Pair* constants below.
	Code string
}

// The codes of a [PairError].
const (
	PairKeysUnavailable   = "keys-unavailable"
	PairKeysFull          = "keys-full"
	PairKeyUnknown        = "key-unknown"
	PairKeyAmbiguous      = "key-ambiguous"
	PairPairingOpen       = "pairing-open"
	PairPairingNone       = "pairing-none"
	PairPairingExpired    = "pairing-expired"
	PairPairingChanged    = "pairing-changed"
	PairPairingRefused    = "pairing-refused"
	PairCredentialInvalid = "credential-invalid"
)

// pairTexts are the words of each code, in the console's voice.
var pairTexts = map[string]string{
	PairKeysUnavailable:   "the list of deciding keys cannot be used; see the daemon's log",
	PairKeysFull:          "the list of deciding keys is full",
	PairKeyUnknown:        "no credential has that id",
	PairKeyAmbiguous:      "that id starts more than one credential; give more of it",
	PairPairingOpen:       "a pairing is already open; start again with replace to end it",
	PairPairingNone:       "no credential is waiting to be confirmed or rejected",
	PairPairingExpired:    "the credential waited too long; the pairing is over",
	PairPairingChanged:    "the pending credential is not the one you compared; look at the words again",
	PairPairingRefused:    "the pairing was refused",
	PairCredentialInvalid: "the credential cannot be used",
}

// Error returns the fixed text of the code.
func (e *PairError) Error() string { return pairTexts[e.Code] }

func init() {
	for code := range pairTexts {
		pairCodes[clientapi.ErrorCode(code)] = code
	}
}

// pairCodes are the daemon's codes of a [PairError].
var pairCodes = map[clientapi.ErrorCode]string{}

// PairStart starts the pairing of a phone and returns its one-time code, which
// the console shows once (as the QR at the desk) and keeps nowhere; a value of
// the result prints without it. With replace, a pairing that is open is ended
// and replaced; without it, a start while one is open is refused with a
// [PairError] of the code [PairPairingOpen].
func (o *Owner) PairStart(replace bool) (ownerwire.PairStartResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	params, err := ownerwire.EncodePairStartParams(ownerwire.PairStartParams{Replace: replace})
	if err != nil {
		return ownerwire.PairStartResult{}, err
	}
	raw, err := o.ask(ownerwire.OpPairStart, jsonRaw(params))
	if err != nil {
		return ownerwire.PairStartResult{}, err
	}
	res, err := ownerwire.DecodePairStartResult(raw)
	if err != nil {
		return ownerwire.PairStartResult{}, o.badAnswer()
	}
	return res, nil
}

// PairStatus reads where the pairing stands: none, waiting for the phone, a
// credential pending at the desk with the eight words of its fingerprint and its
// pending id (the id to give back to [Owner.PairConfirm] and [Owner.PairReject]),
// or expired. It never holds the code.
func (o *Owner) PairStatus() (ownerwire.PairStatus, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	raw, err := o.ask(ownerwire.OpPairStatus, nil)
	if err != nil {
		return ownerwire.PairStatus{}, err
	}
	st, err := ownerwire.DecodePairStatus(raw)
	if err != nil {
		return ownerwire.PairStatus{}, o.badAnswer()
	}
	return st, nil
}

// PairConfirm is the owner saying the eight words are the same on the phone: the
// pending credential goes on the list of deciding keys, and the answer is its
// row. The id is the pending id the status showed with the words he compared;
// when it is not the credential pending now (the pairing was replaced, or ran
// out and another began) the daemon changes nothing and refuses with a
// [PairError] of the code [PairPairingChanged]. An id the wire package refuses
// is refused here and nothing is sent.
func (o *Owner) PairConfirm(pendingID string) (ownerwire.KeyItem, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	params, err := ownerwire.EncodePairPendingParams(ownerwire.PairPendingParams{PendingID: pendingID})
	if err != nil {
		return ownerwire.KeyItem{}, err
	}
	raw, err := o.ask(ownerwire.OpPairConfirm, jsonRaw(params))
	if err != nil {
		return ownerwire.KeyItem{}, err
	}
	k, err := ownerwire.DecodeKeyItem(raw)
	if err != nil {
		return ownerwire.KeyItem{}, o.badAnswer()
	}
	return k, nil
}

// PairReject drops the pending credential and ends the pairing. The id is the
// pending id of the credential the owner looked at, as for [Owner.PairConfirm]:
// another credential's id is refused by the daemon with [PairPairingChanged],
// and an id the wire package refuses is not sent.
func (o *Owner) PairReject(pendingID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	params, err := ownerwire.EncodePairPendingParams(ownerwire.PairPendingParams{PendingID: pendingID})
	if err != nil {
		return err
	}
	raw, err := o.ask(ownerwire.OpPairReject, jsonRaw(params))
	if err != nil {
		return err
	}
	if ownerwire.DecodeNothing(raw) != nil {
		return o.badAnswer()
	}
	return nil
}

// Keys reads the list of deciding keys, revoked ones included and marked, in
// the order they were paired.
func (o *Owner) Keys() (ownerwire.KeysView, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	raw, err := o.ask(ownerwire.OpKeys, nil)
	if err != nil {
		return ownerwire.KeysView{}, err
	}
	v, err := ownerwire.DecodeKeysView(raw)
	if err != nil {
		return ownerwire.KeysView{}, o.badAnswer()
	}
	return v, nil
}

// KeyRevoke revokes one credential and returns the list. The id is a display
// id or the start of one, of at least four digits, that names exactly one
// credential; an id the wire package refuses is refused here and nothing is
// sent.
func (o *Owner) KeyRevoke(id string) (ownerwire.KeysView, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	params, err := ownerwire.EncodeKeyRevokeParams(ownerwire.KeyRevokeParams{ID: id})
	if err != nil {
		return ownerwire.KeysView{}, err
	}
	raw, err := o.ask(ownerwire.OpKeyRevoke, jsonRaw(params))
	if err != nil {
		return ownerwire.KeysView{}, err
	}
	v, err := ownerwire.DecodeKeysView(raw)
	if err != nil {
		return ownerwire.KeysView{}, o.badAnswer()
	}
	return v, nil
}

// KeysOff is "phone off": every credential is revoked, and the list is
// returned.
func (o *Owner) KeysOff() (ownerwire.KeysView, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	raw, err := o.ask(ownerwire.OpKeysOff, nil)
	if err != nil {
		return ownerwire.KeysView{}, err
	}
	v, err := ownerwire.DecodeKeysView(raw)
	if err != nil {
		return ownerwire.KeysView{}, o.badAnswer()
	}
	return v, nil
}
