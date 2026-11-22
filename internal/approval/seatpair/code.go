package seatpair

import (
	"crypto/sha256"
	"crypto/subtle"
	"io"
	"strings"
	"time"
)

const (
	// CodeLength is the number of characters of a pairing code.
	CodeLength = 20
	// CodeLifetime is how long a code is valid. A code is expired at its issue
	// time plus this, exactly, and not before, and once it has been seen
	// expired it stays so (see Code).
	CodeLifetime = 10 * time.Minute
	// MaxTries is the number of wrong tries a code allows. The last of them
	// burns the code.
	MaxTries = 5

	// alphabet is Crockford's base32 without the check symbols: the digits and
	// the letters but I, L, O and U. It has 32 characters, so the low five
	// bits of a random byte pick one without bias.
	alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// Code is the daemon's record of one pairing code. It holds the SHA-256 digest
// of the code, the time it expires, the wrong tries so far and whether it is
// used. It does not hold the code, and none of its texts show anything of it.
// The zero Code is a code nobody can present.
//
// Time. A Code judges the readings it is handed with Time.Before, Time.Add
// and Time.IsZero, and keeps them as they are. A reading from time.Now
// carries a monotonic part, and when both the issue and the reading carry one
// the lifetime is measured by it, so a wall clock that is set back (or
// forward) changes nothing: the code lives ten monotonic minutes. A reading
// without one (a fake clock's, or one that went through Round(0), UTC().Round
// or a parse) is judged by its wall time, and a wall clock set back to a time
// between the issue and the deadline then gives the code more wall time, to
// the deadline of that clock and no further; set back to before the issue it is
// refused ErrClock. Either way expiry is a latch: once a use of the code
// (Verify, or the pairing's Present, Phase or Start) has seen it expired, it is
// expired for good, as a used or a burnt code is, and no later reading, however
// early, makes it live again.
type Code struct {
	issued  bool
	digest  [sha256.Size]byte
	at      time.Time
	expires time.Time
	tries   int
	used    bool
	// lapsed is the latch of expiry: set the first time the code is seen at
	// or past its deadline, and never cleared.
	lapsed bool
}

// NewCode makes a code of CodeLength characters from rand, valid from now for
// CodeLifetime, for one use and MaxTries wrong tries. It returns the code text,
// which the caller shows once and keeps nowhere, and the record to keep.
//
// When rand gives fewer bytes than it needs, or now is the zero time, NewCode
// returns the empty text and the zero Code, which no text matches.
func NewCode(rand io.Reader, now time.Time) (code string, c Code) {
	if now.IsZero() {
		return "", Code{}
	}
	var raw [CodeLength]byte
	if _, err := io.ReadFull(rand, raw[:]); err != nil {
		return "", Code{}
	}
	var text [CodeLength]byte
	for i, b := range raw {
		text[i] = alphabet[b&31]
	}
	code = string(text[:])
	return code, Code{
		issued:  true,
		digest:  digest(code),
		at:      now,
		expires: now.Add(CodeLifetime),
	}
}

// digest is the SHA-256 of the code as the package compares it: in upper case,
// so a code typed in lower case is the same code.
func digest(code string) [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.ToUpper(code)))
}

// Expires is the time from which the code is expired.
func (c Code) Expires() time.Time { return c.expires }

// Tries is the number of wrong tries so far.
func (c Code) Tries() int { return c.tries }

// Used reports whether a presented code was taken as the right one.
func (c Code) Used() bool { return c.used }

// expired reports whether the code is past its time at now, and latches it:
// a code seen expired is expired for every later reading.
func (c *Code) expired(now time.Time) bool {
	if c.lapsed || !now.Before(c.expires) {
		c.lapsed = true
	}
	return c.lapsed
}

// Burnt reports whether the code has had MaxTries wrong tries.
func (c Code) Burnt() bool { return c.tries >= MaxTries }

// String is a text that says nothing of the code.
func (c Code) String() string {
	if !c.issued {
		return "seatpair.Code{none}"
	}
	return "seatpair.Code{expires " + c.expires.UTC().Format(time.RFC3339) + "}"
}

// GoString is the same text for the %#v verb.
func (c Code) GoString() string { return c.String() }

// Verify checks a presented code at the time now and, when it is right, marks
// the code used. It refuses, in this order: a time before the issue of the
// code (ErrClock), a used code (ErrUsed), a burnt one (ErrBurnt), an expired
// one (ErrExpired) and a wrong one (ErrWrongCode, which counts a try; the
// fifth of them burns the code). A refusal for the state of the code never
// shows whether the text presented was the right one, and counts no try. The
// time of the refusal ErrExpired is a latch: see Code.
func (c *Code) Verify(presented string, now time.Time) error {
	if err := c.check(presented, now); err != nil {
		return err
	}
	c.used = true
	return nil
}

// check is Verify without the mark: a right code stays unused, so the caller
// can refuse for another reason and let the phone try again.
func (c *Code) check(presented string, now time.Time) error {
	// The digests are compared before any branch on the state, and always of
	// one length, so what the time takes does not depend on the length of the
	// text presented or on how much of it is right.
	want := digest(presented)
	match := subtle.ConstantTimeCompare(want[:], c.digest[:]) == 1
	switch {
	case !c.issued:
		return ErrWrongCode
	case now.IsZero() || now.Before(c.at):
		return ErrClock
	case c.used:
		return ErrUsed
	case c.Burnt():
		return ErrBurnt
	case c.expired(now):
		return ErrExpired
	case !match:
		c.tries++
		return ErrWrongCode
	}
	return nil
}
