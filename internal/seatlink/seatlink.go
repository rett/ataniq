// Package seatlink is the seat app's port to the daemon over the seat gate:
// [Daemon] implements [seatapp.Daemon] by calling the gate's operations
// through a [seatgate.Client].
//
// Package seatapp may import nothing of the repository, so the link lives here,
// between the app and the transport. It opens no socket and no file: the
// command owns the client, its keys and its pins, and runs it.
//
// # Pairing
//
// [Daemon.Pair] sends seat.pair with [ownerwire.SeatPairParams], encoded by
// package ownerwire (which checks its form first), and reads the answer with
// [ownerwire.DecodeSeatPairResult], which holds it to exactly
// [ownerwire.PairWords] words of the fingerprint's grammar. The errors the app
// tells apart are mapped so:
//
//	the gate's code pairing-refused             seatapp.ErrCodeRefused
//	a code ownerwire does not take as a code    seatapp.ErrCodeRefused (nothing is sent)
//	seatgate.ErrDisconnected                    seatapp.ErrDaemonUnavailable
//	the context ended (cancelled or deadline)   seatapp.ErrDaemonUnavailable
//	anything else (any other code, a closed
//	client, an answer not of its form)          a plain error
//
// Only a refusal the daemon gave, and a code that cannot be one, say the code
// was refused, so the phone never tells the owner to scan again when the desk
// was not asked, and never says the desk is unreachable when the desk refused.
// Nothing is retried: a pairing call that was lost may have used one of the
// code's tries.
//
// The pairing code is a secret. No error of this package holds it, nor any
// byte of the request or of the answer; an error holds at most the gate's
// code, which is of the grammar [a-z0-9-]{1,40}.
package seatlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// CapPair is the capability of the seat.pair operation in a hello.
const CapPair = "pair"

// codePairingRefused is the code the gate answers a pairing the daemon did
// not accept with: the code unknown, expired, used or out of tries.
const codePairingRefused = "pairing-refused"

// The app's copy of the word count and the wire's must be the same number:
// either difference below is a negative constant, which does not convert to
// uint, if they are not.
const (
	_ = uint(ownerwire.PairWords - seatapp.WordCount)
	_ = uint(seatapp.WordCount - ownerwire.PairWords)
)

// Caps is the capability list the seat says in its hello
// ([seatgate.ClientConfig].Caps), in a new slice.
func Caps() []string { return []string{CapPair} }

// errBadAnswer is the error of an answer that is not a pairing result of its
// form. It holds nothing of the answer.
var errBadAnswer = errors.New("seatlink: the gate's answer to the pairing is not of its form")

// errFailed is the error of a call that failed for a reason the app does not
// tell apart. It holds nothing of the request; it wraps the transport's error,
// whose texts are fixed.
var errFailed = errors.New("seatlink: the pairing call failed")

// RefusedError is a refusal of the gate with a code the app does not tell
// apart. Code is of the grammar of [seatgate.ValidCode].
type RefusedError struct {
	Code string
}

func (e *RefusedError) Error() string { return "seatlink: the gate refused the pairing: " + e.Code }

// Daemon is the seat app's daemon port over the gate. It is safe for
// concurrent use, as its client is.
type Daemon struct {
	c *seatgate.Client
}

// New returns the port over c. The caller runs c ([seatgate.Client.Run]) and
// closes it; until c is connected, every call is [seatapp.ErrDaemonUnavailable].
func New(c *seatgate.Client) (*Daemon, error) {
	if c == nil {
		return nil, errors.New("seatlink: a client is required")
	}
	return &Daemon{c: c}, nil
}

// Pair relays a pairing to the daemon. See the package comment for the
// errors.
func (d *Daemon) Pair(ctx context.Context, r seatapp.PairRequest) (seatapp.PairResult, error) {
	if ctx.Err() != nil {
		return seatapp.PairResult{}, seatapp.ErrDaemonUnavailable
	}
	params, err := ownerwire.EncodeSeatPairParams(ownerwire.SeatPairParams{
		Code:         r.Code,
		CredentialID: r.CredentialID,
		PublicKey:    r.SPKI,
	})
	if err != nil {
		// The app has checked the credential id's length and that the key
		// is a P-256 key, both inside the wire's bounds, so what the wire
		// refuses here is the code: one the daemon could not accept. It is
		// not sent, and the error holds nothing of it.
		return seatapp.PairResult{}, seatapp.ErrCodeRefused
	}
	var raw json.RawMessage
	if err := d.c.Call(ctx, ownerwire.OpSeatPair, json.RawMessage(params), &raw); err != nil {
		return seatapp.PairResult{}, mapError(ctx, err)
	}
	res, err := ownerwire.DecodeSeatPairResult(raw)
	if err != nil || len(res.Words) != ownerwire.PairWords {
		return seatapp.PairResult{}, errBadAnswer
	}
	var out seatapp.PairResult
	copy(out.Words[:], res.Words)
	return out, nil
}

// mapError maps the error of a call to the errors the app tells apart.
func mapError(ctx context.Context, err error) error {
	var re *seatgate.RemoteError
	switch {
	case errors.As(err, &re):
		if re.Code == codePairingRefused {
			return seatapp.ErrCodeRefused
		}
		if !seatgate.ValidCode(re.Code) {
			return errFailed
		}
		return &RefusedError{Code: re.Code}
	case errors.Is(err, seatgate.ErrDisconnected),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		ctx.Err() != nil:
		return seatapp.ErrDaemonUnavailable
	}
	return fmt.Errorf("%w: %w", errFailed, err)
}
