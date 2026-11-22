package seatpair

import (
	crand "crypto/rand"
	"io"
	"sync"
	"time"
)

// PendingLifetime is how long a presented credential waits for the owner at
// the desk. It is over at the presentation time plus this, exactly.
const PendingLifetime = 10 * time.Minute

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
	presentedAt  time.Time
	pendingEnds  time.Time
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
func (p *Pairing) open(now time.Time) bool {
	switch p.phase {
	case PhaseOpen:
		return now.Before(p.code.expires) && !p.code.Burnt()
	case PhasePending:
		return now.Before(p.pendingEnds)
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
	p.presentedAt = time.Time{}
	p.pendingEnds = time.Time{}
}

// Present is what the phone says, relayed: the pairing code, the id of the
// credential it made and that credential's public key. It refuses, each with
// its own error: no pairing ever started (ErrNoPairing), a time that is zero
// or before the code's (ErrClock), a credential already pending (ErrPending;
// the code is not looked at and no try is counted), and then the refusals of
// the code (ErrUsed, ErrBurnt, ErrExpired, ErrWrongCode). On success the code
// is used and the credential is pending until the time now plus
// PendingLifetime. Present keeps copies of what it is handed.
func (p *Pairing) Present(code string, credentialID, spki []byte, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return ErrNoPairing
	}
	if now.IsZero() {
		return ErrClock
	}
	if p.phase == PhasePending && p.open(now) {
		return ErrPending
	}
	if p.phase == PhasePending {
		// The wait is over: the pairing ends, its code is used.
		p.phase = PhaseNone
		p.forget()
	}
	if err := p.code.check(code, now); err != nil {
		return err
	}
	p.code.used = true
	p.phase = PhasePending
	p.credentialID = append([]byte(nil), credentialID...)
	p.spki = append([]byte(nil), spki...)
	p.presentedAt = now
	p.pendingEnds = now.Add(PendingLifetime)
	return nil
}
