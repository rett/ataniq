// Package seatpair is the pure logic of pairing a phone with the daemon: the
// one-time pairing code and the state machine of one pairing.
//
// A pairing goes: the owner at the desk starts it and gets a code; the phone
// presents the code with the credential it just made; the owner at the desk
// confirms. The package decides each step from the values it is handed and
// from nothing else.
//
// The code is 20 characters of a Crockford-style base32 alphabet without the
// letters I, L, O and U (100 bits). It is valid for 10 minutes, for one use,
// and for five wrong tries: the fifth wrong try burns it. An expired, used or
// burnt code is refused with an error of its own (ErrExpired, ErrUsed,
// ErrBurnt), a wrong one with ErrWrongCode. The code is compared in constant
// time, as the digests of both sides, which are of one length whatever the
// length of the text presented. It never appears in an error text or in the
// text of any value of the package.
//
// The credential's key is read in the form the browser's getPublicKey()
// gives, a DER SubjectPublicKeyInfo, and only an ES256 key is accepted: ECDSA
// on P-256, a point on the curve, the canonical encoding and no byte after it
// (ParseKey). A credential id is 16 to 1024 bytes (CheckCredentialID). A bad
// credential is refused only after a right code, and leaves the code unused.
//
// The package is what the daemon asks. It is not a wire, a daemon, an HTTP
// handler, a store or a clock: it opens no file and no socket, starts no
// program and keeps nothing between runs. The random source and the time are
// handed in, so a test drives both. A Pairing is only a value in memory; what
// the daemon keeps of a confirmed credential, and how it checks a later
// assertion, are outside this package. Nothing here decides whether the
// owner at the desk may confirm: only the caller knows who is at the desk.
package seatpair
