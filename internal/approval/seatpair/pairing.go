package seatpair

import (
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"sync"
	"time"
)

// PendingLifetime is how long a presented credential waits for the owner at
// the desk. It is over at the presentation time plus this, exactly.
const PendingLifetime = 10 * time.Minute

// PendingIDBytes is the number of random bytes of a pending credential's id,
// and PendingIDLength the length of the id: those bytes in lower-case
// hexadecimal.
const (
	PendingIDBytes  = 8
	PendingIDLength = 2 * PendingIDBytes
)

// Phase is where a pairing stands.
type Phase int

const (
	// PhaseNone: no pairing is open. Either none was started, or the last one
	// ended: confirmed, rejected, replaced, or past its time.
	PhaseNone Phase = iota
	// PhaseOpen: a code was made and no credential has been presented.
	PhaseOpen
	// PhasePending: the code was right, and the credential waits at the desk.
	PhasePending
)

// String names the phase.
func (ph Phase) String() string {
	switch ph {
	case PhaseNone:
		return "none"
	case PhaseOpen:
		return "open"
	case PhasePending:
		return "pending"
	}
	return "unknown"
}

// Pairing is the state of at most one pairing. Its methods are safe for use
// by several goroutines, and each is one step: it either does what it says
// and moves the state, or refuses and leaves the state as it was, except
// that a wrong code counts a try.
type Pairing struct {
	mu    sync.Mutex
	rand  io.Reader
	clock func() time.Time

	started bool
	phase   Phase
	code    Code

	// What a right code brought, while the phase is PhasePending. Copies.
	credentialID []byte
	spki         []byte
	fingerprint  Fingerprint
	pendingID    string
	presentedAt  time.Time
	pendingEnds  time.Time
	// pendingLapsed is the latch of the pending credential's time: set the
	// first time it is seen at or past pendingEnds, and kept until the
	// pending credential is dropped.
	pendingLapsed bool
}

// NewPairing returns a Pairing with no pairing open. Start takes its random
// bytes from rand, and the time of the code from clock; a nil rand is the
// system's random source and a nil clock is time.Now. The other methods take
// the time from the caller.
func NewPairing(rand io.Reader, clock func() time.Time) *Pairing {
	if rand == nil {
		rand = crand.Reader
	}
	if clock == nil {
		clock = time.Now
	}
	return &Pairing{rand: rand, clock: clock}
}

// open reports whether the pairing is still going at now: a code that is
// not past its time and not used, or a pending credential not past its time.
// It is also where the latches of expiry are set: a code or a pending
// credential seen past its time is past it for every later reading, so a clock
// that steps back never makes one usable again (see Code for the rule about
// monotonic and wall readings).
func (p *Pairing) open(now time.Time) bool {
	switch p.phase {
	case PhaseOpen:
		return !p.code.expired(now) && !p.code.Burnt()
	case PhasePending:
		if p.pendingLapsed || !now.Before(p.pendingEnds) {
			p.pendingLapsed = true
		}
		return !p.pendingLapsed
	}
	return false
}

// Phase returns where the pairing stands at now.
func (p *Pairing) Phase(now time.Time) Phase {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.open(now) {
		return PhaseNone
	}
	return p.phase
}

// String says where the pairing stands, and nothing of its code or key.
func (p *Pairing) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return "seatpair.Pairing{" + p.phase.String() + "}"
}

// GoString is the same text for the %#v verb.
func (p *Pairing) GoString() string { return p.String() }

// Start opens a pairing and returns its code, which the caller shows once,
// as the QR at the desk, and keeps nowhere. While a pairing is open (a code
// not yet expired, burnt or used, or a credential pending and not past its
// time) Start refuses with ErrPairingOpen, unless replace is true, which
// ends that pairing: its code, if still unused, no longer works. A
// pairing that is over never needs replace. When the random source fails,
// Start returns ErrRandom and the pairing is as it was.
func (p *Pairing) Start(replace bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	if now.IsZero() {
		return "", ErrClock
	}
	if p.open(now) && !replace {
		return "", ErrPairingOpen
	}
	code, c := NewCode(p.rand, now)
	if code == "" {
		return "", ErrRandom
	}
	p.started = true
	p.phase = PhaseOpen
	p.code = c
	p.forget()
	return code, nil
}

// forget drops what a presentation brought.
func (p *Pairing) forget() {
	p.credentialID = nil
	p.spki = nil
	p.fingerprint = Fingerprint{}
	p.pendingID = ""
	p.presentedAt = time.Time{}
	p.pendingEnds = time.Time{}
	p.pendingLapsed = false
}

// Present is what the phone says, relayed: the pairing code, the id of the
// credential it made and that credential's public key. It refuses, each with
// its own error: no pairing ever started (ErrNoPairing), a time that is zero
// or before the code's (ErrClock), a credential already pending (ErrPending;
// the code is not looked at and no try is counted), and then the refusals of
// the code (ErrUsed, ErrBurnt, ErrExpired, ErrWrongCode). For a right code it
// then checks the credential: the id's length (ErrCredentialID) and the key
// (ErrKeyFormat, ErrKeyAlgorithm, ErrKeyPoint; see ParseKey), and a refusal
// there leaves the code unused; so does a random source that cannot give the
// pending id (ErrRandom).
//
// On success the code is used, the credential is pending until the time now
// plus PendingLifetime, with a pending id of its own (PendingID), and Present
// returns its fingerprint, which the desk console shows to the owner. Present
// keeps copies of what it is handed. No credential is made until Confirm.
func (p *Pairing) Present(code string, credentialID, spki []byte, now time.Time) (Fingerprint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return Fingerprint{}, ErrNoPairing
	}
	if now.IsZero() {
		return Fingerprint{}, ErrClock
	}
	if p.phase == PhasePending && p.open(now) {
		return Fingerprint{}, ErrPending
	}
	if p.phase == PhasePending {
		// The wait is over: the pairing ends, its code is used.
		p.phase = PhaseNone
		p.forget()
	}
	if err := p.code.check(code, now); err != nil {
		return Fingerprint{}, err
	}
	// The code is right; a credential that is not an ES256 key is refused
	// without using the code, so the phone may try again with a good one.
	if err := CheckCredentialID(credentialID); err != nil {
		return Fingerprint{}, err
	}
	if _, err := ParseKey(spki); err != nil {
		return Fingerprint{}, err
	}
	var raw [PendingIDBytes]byte
	if _, err := io.ReadFull(p.rand, raw[:]); err != nil {
		return Fingerprint{}, ErrRandom
	}
	p.code.used = true
	p.phase = PhasePending
	p.pendingID = hex.EncodeToString(raw[:])
	p.credentialID = append([]byte(nil), credentialID...)
	p.spki = append([]byte(nil), spki...)
	p.fingerprint = FingerprintOf(p.credentialID, p.spki)
	p.presentedAt = now
	p.pendingEnds = now.Add(PendingLifetime)
	return p.fingerprint, nil
}

// Pending returns the fingerprint of the credential that waits at the desk at
// now, and true; or false when none waits (none presented, or its time is over).
func (p *Pairing) Pending(now time.Time) (Fingerprint, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phase != PhasePending || !p.open(now) {
		return Fingerprint{}, false
	}
	return p.fingerprint, true
}

// PendingID returns the pending id of the credential that waits at the desk at
// now, and true; or false when none waits. The id is 8 random bytes, drawn when
// the credential became pending, as 16 lower-case hexadecimal digits: another
// credential, even the same one presented again, has another. The desk shows it
// with the six words, and Confirm and Reject take it back, so that the owner's
// word is about the credential he compared.
func (p *Pairing) PendingID(now time.Time) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phase != PhasePending || !p.open(now) {
		return "", false
	}
	return p.pendingID, true
}

// sameID compares two pending ids as the digests of both, which are of one
// length, in constant time.
func sameID(a, b string) bool {
	da, db := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(da[:], db[:]) == 1
}

// Confirm is the owner at the desk saying that the six words are the same on
// the phone, for the credential with the pending id given. It is the only thing
// that makes a Credential. It refuses with ErrNotPending when no credential
// was presented or the pairing ended, with ErrPendingID when the id is not the
// pending credential's (the pairing was replaced, or ran out and another
// began; nothing changes), with ErrClock for a time that is zero or before the
// presentation (the credential stays pending), and with ErrPendingExpired at
// the presentation time plus PendingLifetime and after it, or at any time once
// the credential has been seen past its time (the latch of Code's comment),
// which also ends the pairing. On success the
// pairing is over, and the Credential carries the id and key as presented,
// the kind KindUnverified and now as PairedAt.
func (p *Pairing) Confirm(now time.Time, pendingID string) (Credential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phase != PhasePending {
		return Credential{}, ErrNotPending
	}
	if !sameID(pendingID, p.pendingID) {
		return Credential{}, ErrPendingID
	}
	if !p.pendingLapsed && (now.IsZero() || now.Before(p.presentedAt)) {
		return Credential{}, ErrClock
	}
	if p.pendingLapsed || !now.Before(p.pendingEnds) {
		p.phase = PhaseNone
		p.forget()
		return Credential{}, ErrPendingExpired
	}
	c := Credential{
		ID:       p.credentialID,
		SPKI:     p.spki,
		Kind:     KindUnverified,
		PairedAt: now,
	}
	p.phase = PhaseNone
	p.credentialID, p.spki = nil, nil
	p.forget()
	return c, nil
}

// Reject is the owner at the desk saying the words differ, or that the
// pairing is not wanted. It drops the pending credential and ends the pairing;
// the code stays used, so the same code makes no second try. It refuses with
// ErrNotPending when no credential waits, and with ErrPendingID, changing
// nothing, when the pending id given is not the waiting credential's. It
// needs no time: a credential whose time is over may be rejected as well.
func (p *Pairing) Reject(pendingID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phase != PhasePending {
		return ErrNotPending
	}
	if !sameID(pendingID, p.pendingID) {
		return ErrPendingID
	}
	p.phase = PhaseNone
	p.forget()
	return nil
}
