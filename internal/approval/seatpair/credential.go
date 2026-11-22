package seatpair

import "time"

// KindUnverified is the kind of every credential this package makes: the
// pairing proves that the owner at the desk saw the same six words on the
// phone, and not what kind of authenticator holds the key (a syncing passkey
// or a hardware token). A later step may set another kind.
const KindUnverified = "unverified"

// Credential is a paired phone's deciding key, as the daemon would put it on
// its list: only Pairing.Confirm makes one.
type Credential struct {
	// ID is the credential id the phone made.
	ID []byte
	// SPKI is the credential's public key, a DER SubjectPublicKeyInfo of an
	// ES256 key, in the canonical encoding.
	SPKI []byte
	// Kind is KindUnverified.
	Kind string
	// PairedAt is the time the owner confirmed at the desk.
	PairedAt time.Time
}

// Fingerprint is the six words of the credential.
func (c Credential) Fingerprint() Fingerprint { return FingerprintOf(c.ID, c.SPKI) }
