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
// The fingerprint of a credential is six words, which the desk console and the
// phone both show so the owner can see they hold one key: the first six bytes
// of the SHA-256 of the length-prefixed credential id and key, each byte the
// index of a word in a fixed list of 256 (FingerprintOf, WordList). That is 48
// bits: it shows that the two sides agree on a key the owner has just seen
// made, within the minutes of a pairing; it is not a long-term name for a key.
//
// A pairing is Start (which refuses while one is open, unless asked to replace
// it), Present (a right code with a good credential makes the credential
// Pending, with its fingerprint, for at most 10 minutes, exactly; a second
// Present while one is pending is refused) and then Confirm or Reject. Confirm,
// the owner at the desk, is the only thing that makes a Credential, with the
// kind "unverified"; Reject drops the pending one. The code is spent by
// either: one code brings one credential at most.
//
// A pending credential has a pending id: 8 random bytes from the package's
// random source, as 16 lower-case hexadecimal digits, drawn when the
// credential becomes pending and given by PendingID. Confirm and Reject take it
// back and compare it, in constant time, to that of the credential pending at
// that moment; one that differs (the pairing was replaced, or ran out and
// another began) is refused with ErrPendingID and changes nothing. So the
// owner's word is about the credential whose six words he compared, never about
// one that took its place while he looked.
//
// Time. The package judges the readings it is handed with Time.Before and
// Time.Add and keeps them as they are, so a reading from time.Now, which
// carries a monotonic part, makes the ten minutes of a code and of a pending
// credential monotonic minutes: a wall clock set back or forward does not
// change them. A reading without one (a fake clock's) is judged by its wall
// time: set back to between the issue and the deadline, the clock gives the
// code or the credential more wall time, to its own deadline and no further;
// set back to before the issue (or the presentation) it is refused ErrClock.
// In both cases expiry is a latch: once Present, Phase, Pending, PendingID,
// Start or Confirm has seen a code or a pending credential past its time, it is
// past it for good and no later reading makes it live; a used or a burnt code
// is spent for good as well. A reading far in the future therefore ends a
// pairing for good, which fails safe.
//
// The package is what the daemon asks. It is not a wire, a daemon, an HTTP
// handler, a store or a clock: it opens no file and no socket, starts no
// program and keeps nothing between runs. The random source and the time are
// handed in, so a test drives both. A Pairing is only a value in memory; what
// the daemon keeps of a confirmed credential, and how it checks a later
// assertion, are outside this package. Nothing here decides whether the
// owner at the desk may confirm: only the caller knows who is at the desk.
package seatpair
