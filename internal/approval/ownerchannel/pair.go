package ownerchannel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/presence"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/seatpair"
)

// SurfaceSeat is the surface name of the seat app's connection, the only
// surface [Service.PairPresent] takes a call from.
const SurfaceSeat = "seat"

// The errors of the pairing and key operations, found with errors.Is. Each has
// a fixed text that holds nothing a caller sent, no code, key or id; the
// daemon gives each a fixed code of its own. A refusal of the owner connection
// proper (not-decider, owner-away, record-invalid) is a [*record.Error].
var (
	// ErrKeysUnavailable says the list of keys cannot be used: there is no
	// store, or it is damaged, not private, or stopped after a failed write.
	// Nothing is added to a list that cannot be read, and an unreadable list
	// is never taken for an empty one.
	ErrKeysUnavailable = errors.New("ownerchannel: the list of deciding keys cannot be used")
	// ErrKeysFull says the list holds as many credentials as it may, revoked
	// ones included.
	ErrKeysFull = errors.New("ownerchannel: the list of deciding keys is full")
	// ErrKeyUnknown says no credential has the id named.
	ErrKeyUnknown = errors.New("ownerchannel: no credential has this id")
	// ErrKeyAmbiguous says the id named is the start of more than one
	// credential's id.
	ErrKeyAmbiguous = errors.New("ownerchannel: the id names more than one credential")
	// ErrPairingOpen says a pairing is open and the start was not asked to
	// replace it.
	ErrPairingOpen = errors.New("ownerchannel: a pairing is already open")
	// ErrNoPairing says no credential waits at the desk to be confirmed or
	// rejected.
	ErrNoPairing = errors.New("ownerchannel: no credential waits at the desk")
	// ErrPairingExpired says the credential waited for its ten minutes and
	// no longer; the pairing is over.
	ErrPairingExpired = errors.New("ownerchannel: the pending credential expired")
	// ErrPairingChanged says the credential the owner's word named (by its
	// pending id) is not the one pending now: the pairing was replaced, or
	// ran out and another took its place. Nothing was changed.
	ErrPairingChanged = errors.New("ownerchannel: the pending credential is not the one named")
	// ErrPairRefused says the seat's presentation was refused for the code
	// or for the state of the pairing: a wrong, used, burnt or expired code,
	// no pairing, or a credential already pending. It never says which.
	ErrPairRefused = errors.New("ownerchannel: the pairing code was refused")
	// ErrCredentialInvalid says the credential is not one that can be put
	// on the list: its id or its key is not of the form, or it is on the
	// list already.
	ErrCredentialInvalid = errors.New("ownerchannel: the credential cannot be used")
	// ErrNotSeat says the call did not come from a seat.
	ErrNotSeat = errors.New("ownerchannel: the call is not from a seat")
)

// pairing is the state of the one pairing the service holds, in memory. A
// restart abandons it: the new service has none.
type pairing struct {
	p *seatpair.Pairing
	// at is the time handed to the pairing's clock by the call that is
	// running, so that the code's time and the time the service reports are
	// one. It is the clock's own reading, monotonic part included (see
	// [Service.nowBoth]): the pairing's deadlines are measured by it. What the
	// service shows and journals is the wall reading of the same call.
	at time.Time
	// live is whether a pairing was started and not ended by a confirm, a
	// reject or a replacement; with the phase none it reads as expired.
	live bool
	// ends is when the code ends while the pairing waits, and when the
	// pending credential ends while one is pending; it is kept after.
	ends time.Time
	// id is the display id of the pending credential.
	id string
}

func (s *Service) initPairing() {
	s.pr.p = seatpair.NewPairing(s.cfg.PairRand, func() time.Time { return s.pr.at })
}

// keyStore returns the list, or [ErrKeysUnavailable] when the service has none.
func (s *Service) keyStore() (KeyStore, error) {
	if s.cfg.Keys == nil {
		return nil, ErrKeysUnavailable
	}
	return s.cfg.Keys, nil
}

// keysErr turns an error of the store into what the service answers. The
// facts of the list keep their own error; anything else means the list cannot
// be used, and the error is wrapped so that the log has it and the wire does
// not.
func keysErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ownerjournal.ErrKeysFull):
		return ErrKeysFull
	case errors.Is(err, ownerjournal.ErrKeyUnknown):
		return ErrKeyUnknown
	case errors.Is(err, ownerjournal.ErrKeyExists):
		return ErrCredentialInvalid
	}
	return fmt.Errorf("%w: %w", ErrKeysUnavailable, err)
}

// usableKeys returns the list and its keys, or the error that says it cannot
// be used. Reading the list is the check that it can be.
func (s *Service) usableKeys() (KeyStore, []ownerjournal.StoredKey, error) {
	ks, err := s.keyStore()
	if err != nil {
		return nil, nil, err
	}
	keys, err := ks.Keys()
	if err != nil {
		return nil, nil, keysErr(err)
	}
	return ks, keys, nil
}

func keyItem(k ownerjournal.StoredKey) ownerwire.KeyItem {
	return ownerwire.KeyItem{ID: k.ID, Kind: k.Kind, PairedAt: record.FormatTime(k.PairedAt), Revoked: k.Revoked}
}

func keysView(keys []ownerjournal.StoredKey) ownerwire.KeysView {
	v := ownerwire.KeysView{Keys: make([]ownerwire.KeyItem, 0, len(keys))}
	for _, k := range keys {
		v.Keys = append(v.Keys, keyItem(k))
	}
	return v
}

// pairGate is what every write of the pairing operations needs of its source,
// the same as the other writes of the owner connection ([Service.LimitSet]):
// a well-formed surface and session, the decider's place and, when presence is
// asked, the owner's activity inside the presence window (owner-away). The
// action counts as activity when he was present. A write that only lowers
// (a revocation) asks no presence: it can never raise what a lost phone or a
// stranger may do, and "phone off" has to work whenever the owner is at the
// console.
func (s *Service) pairGate(src Source, now time.Time, needPresent bool) error {
	if !presence.ValidSurface(src.Surface) || !presence.ValidSession(src.Session) {
		return refuse(record.CodeRecordInvalid, "the surface or the session is not well formed")
	}
	if !src.Decider {
		return refuse(record.CodeNotDecider, "this connection is not the one that may decide")
	}
	if needPresent {
		if _, err := s.mayDecide(src, now, true); err != nil {
			return refuse(record.CodeOwnerAway, "no activity of the owner inside the presence window")
		}
	}
	return nil
}

// PairStart starts the pairing of a phone (owner.pair-start): it makes the
// one-time code, which is in the result and nowhere else (not in the journal,
// the notices, an error or any other answer), and which lives ten minutes, for
// one use and five wrong tries. While a pairing is open the start is refused
// with [ErrPairingOpen], unless the params say replace, which ends that pairing
// and its code. It needs the decider and the owner's presence, and the list of
// keys, which a pairing is of no use without, must be usable.
func (s *Service) PairStart(ctx context.Context, src Source, p ownerwire.PairStartParams) (ownerwire.PairStartResult, error) {
	_, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.PairStartResult{}, err
	}
	defer done()
	s.pmu.Lock()
	defer s.pmu.Unlock()
	mono, now := s.nowBoth()
	if err := s.pairGate(src, now, true); err != nil {
		return ownerwire.PairStartResult{}, err
	}
	if _, _, err := s.usableKeys(); err != nil {
		return ownerwire.PairStartResult{}, err
	}
	s.pr.at = mono
	code, err := s.pr.p.Start(p.Replace)
	switch {
	case errors.Is(err, seatpair.ErrPairingOpen):
		return ownerwire.PairStartResult{}, ErrPairingOpen
	case err != nil:
		return ownerwire.PairStartResult{}, fmt.Errorf("ownerchannel: the pairing could not start: %w", err)
	}
	s.pr.live, s.pr.id = true, ""
	s.pr.ends = now.Add(seatpair.CodeLifetime)
	return ownerwire.PairStartResult{Code: code, ExpiresAt: record.FormatTime(s.pr.ends)}, nil
}

// PairStatus says where the pairing stands (owner.pair-status): none; waiting
// for the phone; a credential pending at the desk, with the six words of its
// fingerprint and its pending id, which a confirm or a reject must give back;
// or expired, which is what a pairing that ran out or whose code
// was burnt reads as, until the next start. It never holds the code.
func (s *Service) PairStatus(ctx context.Context) (ownerwire.PairStatus, error) {
	_, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.PairStatus{}, err
	}
	defer done()
	s.pmu.Lock()
	defer s.pmu.Unlock()
	mono, _ := s.nowBoth()
	switch s.pr.p.Phase(mono) {
	case seatpair.PhaseOpen:
		return ownerwire.PairStatus{Phase: ownerwire.PairWaiting, ExpiresAt: record.FormatTime(s.pr.ends)}, nil
	case seatpair.PhasePending:
		fp, ok := s.pr.p.Pending(mono)
		id, _ := s.pr.p.PendingID(mono)
		if ok && id != "" {
			return ownerwire.PairStatus{Phase: ownerwire.PairPending, Words: append([]string(nil), fp[:]...), PendingID: id, ExpiresAt: record.FormatTime(s.pr.ends)}, nil
		}
	}
	if s.pr.live {
		return ownerwire.PairStatus{Phase: ownerwire.PairExpired, ExpiresAt: record.FormatTime(s.pr.ends)}, nil
	}
	return ownerwire.PairStatus{Phase: ownerwire.PairNone}, nil
}

// PairPresent is the seat app's call (seat.pair): the pairing code and the
// credential the phone made. A right code with a good credential makes the
// credential pending at the desk for ten minutes, and the answer is its
// fingerprint, six words. It makes no credential: only [Service.PairConfirm]
// does. A refusal of the code or of the pairing's state is [ErrPairRefused] and
// never says which, a credential that is not an ES256 key or has an id of
// another length is [ErrCredentialInvalid] and leaves the code unused, and the
// fifth wrong code burns it. A credential that is on the list of keys already,
// revoked or not, is [ErrCredentialInvalid] as well, but only after the code was
// right, and it ends the pairing (the code is spent). The source must be a seat ([SurfaceSeat]); the
// daemon passes only one it knows to be.
func (s *Service) PairPresent(ctx context.Context, src Source, p ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error) {
	_, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.SeatPairResult{}, err
	}
	defer done()
	if src.Surface != SurfaceSeat {
		return ownerwire.SeatPairResult{}, ErrNotSeat
	}
	if !presence.ValidSession(src.Session) {
		return ownerwire.SeatPairResult{}, refuse(record.CodeRecordInvalid, "the session is not well formed")
	}
	if _, err := ownerwire.EncodeSeatPairParams(p); err != nil {
		return ownerwire.SeatPairResult{}, refuse(record.CodeRecordInvalid, "the parameters are not valid for this operation")
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	mono, now := s.nowBoth()
	// A list that cannot take the credential refuses before the code is
	// spent on it.
	_, listed, err := s.usableKeys()
	if err != nil {
		return ownerwire.SeatPairResult{}, err
	}
	fp, err := s.pr.p.Present(p.Code, p.CredentialID, p.PublicKey, mono)
	switch {
	case err == nil:
	case errors.Is(err, seatpair.ErrCredentialID), errors.Is(err, seatpair.ErrKeyFormat),
		errors.Is(err, seatpair.ErrKeyAlgorithm), errors.Is(err, seatpair.ErrKeyPoint):
		return ownerwire.SeatPairResult{}, ErrCredentialInvalid
	case errors.Is(err, seatpair.ErrWrongCode), errors.Is(err, seatpair.ErrUsed), errors.Is(err, seatpair.ErrBurnt),
		errors.Is(err, seatpair.ErrExpired), errors.Is(err, seatpair.ErrNoPairing), errors.Is(err, seatpair.ErrPending):
		if errors.Is(err, seatpair.ErrWrongCode) && s.pr.live && s.pr.p.Phase(mono) == seatpair.PhaseNone {
			// The try was the one that burnt the code. The pairing reads as
			// expired from this moment, and the desk is told, once: nothing of
			// the code or of the try is in the line.
			s.pr.ends = now
			s.notice("a pairing code was burnt by wrong tries; start the pairing again")
		}
		return ownerwire.SeatPairResult{}, ErrPairRefused
	default:
		return ownerwire.SeatPairResult{}, fmt.Errorf("ownerchannel: the pairing could not take the credential: %w", err)
	}
	id := ownerjournal.KeyDisplayID(p.CredentialID)
	// A credential that is on the list already, revoked or not, could only
	// fail at the confirm, after the pairing was spent and with the wrong
	// advice. It is refused here, after the code was checked (so a wrong code
	// still counted its try), and the pairing is ended: the code is used.
	for _, k := range listed {
		if k.ID == id {
			if pid, ok := s.pr.p.PendingID(mono); ok {
				_ = s.pr.p.Reject(pid)
			}
			s.pr.live, s.pr.id = false, ""
			return ownerwire.SeatPairResult{}, ErrCredentialInvalid
		}
	}
	s.pr.id = id
	s.pr.ends = now.Add(seatpair.PendingLifetime)
	return ownerwire.SeatPairResult{Words: append([]string(nil), fp[:]...)}, nil
}

// PairConfirm is the owner at the desk saying the six words are the same on
// the phone (owner.pair-confirm): the pending credential goes on the list of
// deciding keys with the kind "unverified", and the answer is its row. The
// params name the credential the owner compared by its pending id (the one
// [Service.PairStatus] showed with the words), which is checked, under the
// pairing's lock and in constant time, against the credential pending now: one
// that is not it is refused with [ErrPairingChanged] and nothing changes. It
// needs the decider and the owner's presence. It is refused with [ErrNoPairing]
// when none waits and with [ErrPairingExpired] when its ten minutes are over. A list
// that cannot be used, or that is full, is found before the pairing is touched,
// so the credential stays pending; a list that fails while the credential is
// put on it ends the pairing, and the owner pairs again.
func (s *Service) PairConfirm(ctx context.Context, src Source, p ownerwire.PairPendingParams) (ownerwire.KeyItem, error) {
	_, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.KeyItem{}, err
	}
	defer done()
	s.pmu.Lock()
	defer s.pmu.Unlock()
	mono, now := s.nowBoth()
	if err := s.pairGate(src, now, true); err != nil {
		return ownerwire.KeyItem{}, err
	}
	if _, err := ownerwire.EncodePairPendingParams(p); err != nil {
		return ownerwire.KeyItem{}, refuse(record.CodeRecordInvalid, "the parameters are not valid for this operation")
	}
	ks, keys, err := s.usableKeys()
	if err != nil {
		return ownerwire.KeyItem{}, err
	}
	if len(keys) >= ownerjournal.MaxKeys {
		return ownerwire.KeyItem{}, ErrKeysFull
	}
	cred, err := s.pr.p.Confirm(mono, p.PendingID)
	switch {
	case err == nil:
	case errors.Is(err, seatpair.ErrNotPending):
		return ownerwire.KeyItem{}, ErrNoPairing
	case errors.Is(err, seatpair.ErrPendingID):
		return ownerwire.KeyItem{}, ErrPairingChanged
	case errors.Is(err, seatpair.ErrPendingExpired):
		return ownerwire.KeyItem{}, ErrPairingExpired
	default:
		return ownerwire.KeyItem{}, fmt.Errorf("ownerchannel: the pairing could not be confirmed: %w", err)
	}
	s.pr.live, s.pr.id = false, ""
	stored, err := ks.AddKey(ownerjournal.NewKey{
		CredentialID: cred.ID, PublicKey: cred.SPKI, Kind: cred.Kind, PairedAt: cred.PairedAt.Round(0),
		Words: cred.Fingerprint().String(),
	})
	if err != nil {
		s.notice("a pairing was confirmed and its credential could not be put on the list of keys; pair the phone again")
		return ownerwire.KeyItem{}, keysErr(err)
	}
	return keyItem(stored), nil
}

// PairReject drops the pending credential and ends the pairing
// (owner.pair-reject), and journals it with the credential's display id and
// fingerprint words and no key. It needs the decider and the owner's presence.
// The params name the credential by its pending id, as for
// [Service.PairConfirm]: another credential's id is refused with
// [ErrPairingChanged] and nothing changes. It is refused with [ErrNoPairing]
// when none waits. The list is checked before the pairing is touched, so a
// list that cannot be used leaves the credential pending. Once that check has
// passed the credential is dropped and the pairing ended, before the journal is
// written, because a drop only lowers what anyone may do; when the journal then
// fails (an error of the store, or the cap on rejected pairings) the call
// answers that error although the rejection took effect, and the credential is
// not pending any more, which the status shows.
func (s *Service) PairReject(ctx context.Context, src Source, p ownerwire.PairPendingParams) error {
	_, done, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	s.pmu.Lock()
	defer s.pmu.Unlock()
	mono, now := s.nowBoth()
	if err := s.pairGate(src, now, true); err != nil {
		return err
	}
	if _, err := ownerwire.EncodePairPendingParams(p); err != nil {
		return refuse(record.CodeRecordInvalid, "the parameters are not valid for this operation")
	}
	ks, _, err := s.usableKeys()
	if err != nil {
		return err
	}
	fp, ok := s.pr.p.Pending(mono)
	id := s.pr.id
	rejectErr := func() error {
		switch err := s.pr.p.Reject(p.PendingID); {
		case err == nil:
			return nil
		case errors.Is(err, seatpair.ErrPendingID):
			return ErrPairingChanged
		default:
			return ErrNoPairing
		}
	}
	if !ok {
		// The pending credential's time may be over; it is still dropped
		// (seatpair's Reject needs no time), but it has no words to journal.
		if err := rejectErr(); err != nil {
			return err
		}
		s.pr.live, s.pr.id = false, ""
		return nil
	}
	if err := rejectErr(); err != nil {
		return err
	}
	s.pr.live, s.pr.id = false, ""
	if err := ks.AppendRejected(ownerjournal.RejectedPairing{ID: id, Words: strings.Join(fp[:], " "), At: now}); err != nil {
		s.notice("a rejected pairing could not be journaled")
		return keysErr(err)
	}
	return nil
}

// Keys returns the list of deciding keys (owner.keys): every credential, in
// the order they were paired, revoked ones included and marked.
func (s *Service) Keys(ctx context.Context) (ownerwire.KeysView, error) {
	_, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.KeysView{}, err
	}
	defer done()
	s.pmu.Lock()
	defer s.pmu.Unlock()
	_, keys, err := s.usableKeys()
	if err != nil {
		return ownerwire.KeysView{}, err
	}
	return keysView(keys), nil
}

// KeyRevoke revokes one credential (owner.key-revoke) and returns the list.
// The id is a display id or the start of one, and must name exactly one
// credential: [ErrKeyUnknown] when none, [ErrKeyAmbiguous] when more than one.
// A credential already revoked is left as it is. It needs the decider, and not
// the owner's presence: it only lowers.
func (s *Service) KeyRevoke(ctx context.Context, src Source, p ownerwire.KeyRevokeParams) (ownerwire.KeysView, error) {
	_, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.KeysView{}, err
	}
	defer done()
	s.pmu.Lock()
	defer s.pmu.Unlock()
	now := s.now()
	if err := s.pairGate(src, now, false); err != nil {
		return ownerwire.KeysView{}, err
	}
	if _, err := ownerwire.EncodeKeyRevokeParams(p); err != nil {
		return ownerwire.KeysView{}, refuse(record.CodeRecordInvalid, "the parameters are not valid for this operation")
	}
	ks, keys, err := s.usableKeys()
	if err != nil {
		return ownerwire.KeysView{}, err
	}
	match := ""
	for _, k := range keys {
		if strings.HasPrefix(k.ID, p.ID) {
			if match != "" {
				return ownerwire.KeysView{}, ErrKeyAmbiguous
			}
			match = k.ID
		}
	}
	if match == "" {
		return ownerwire.KeysView{}, ErrKeyUnknown
	}
	if err := ks.RevokeKeys([]string{match}, now); err != nil {
		return ownerwire.KeysView{}, keysErr(err)
	}
	return s.reread(ks)
}

// KeysOff is "phone off" (owner.keys-off): every credential is revoked, in one
// step, and the list is returned. It needs the decider, and not the owner's
// presence.
func (s *Service) KeysOff(ctx context.Context, src Source) (ownerwire.KeysView, error) {
	_, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.KeysView{}, err
	}
	defer done()
	s.pmu.Lock()
	defer s.pmu.Unlock()
	now := s.now()
	if err := s.pairGate(src, now, false); err != nil {
		return ownerwire.KeysView{}, err
	}
	ks, keys, err := s.usableKeys()
	if err != nil {
		return ownerwire.KeysView{}, err
	}
	var ids []string
	for _, k := range keys {
		if !k.Revoked {
			ids = append(ids, k.ID)
		}
	}
	if len(ids) > 0 {
		if err := ks.RevokeKeys(ids, now); err != nil {
			return ownerwire.KeysView{}, keysErr(err)
		}
	}
	return s.reread(ks)
}

// reread returns the list as the store has it now.
func (s *Service) reread(ks KeyStore) (ownerwire.KeysView, error) {
	keys, err := ks.Keys()
	if err != nil {
		return ownerwire.KeysView{}, keysErr(err)
	}
	return keysView(keys), nil
}
