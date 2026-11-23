package seatlink

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// The login operations of the gate and their capability, as the contract
// (docs/design/seat-gate.md, "Login") names them. The names are package
// ownerwire's; the capability is the gate's word for them.
const (
	// CapLogin is the capability of the two login operations.
	CapLogin = "login"
	// OpLoginChallenge asks the daemon for a login challenge:
	// ownerwire.OpSeatLoginChallenge.
	OpLoginChallenge = ownerwire.OpSeatLoginChallenge
	// OpLogin hands the daemon an assertion over a login challenge:
	// ownerwire.OpSeatLogin.
	OpLogin = ownerwire.OpSeatLogin
)

// The codes of the gate the login tells apart that are not words of package
// ownerwire.
const (
	codeKeyUnknown      = "key-unknown"
	codeUnknownOp       = "unknown-op"
	codeTooManyRefusals = "too-many-refusals"
)

// LoginChallengeOf is the challenge of a login, ownerwire.LoginChallenge of
// the gate key's fingerprint (the pin the seat holds, 64 lower-case hex
// digits) and the daemon's nonce, as an array. The daemon and the link share
// that one function; this one is kept for the callers that want the array.
// An instance or a nonce of another form gives the zero array, which no
// daemon's challenge is.
func LoginChallengeOf(instance string, nonce []byte) [sha256.Size]byte {
	var out [sha256.Size]byte
	if ch, err := ownerwire.LoginChallenge(instance, nonce); err == nil {
		copy(out[:], ch)
	}
	return out
}

// errNoPin is the error of a login on a link made without the gate's pin: the
// challenge cannot be recomputed, so none is asked.
var errNoPin = errors.New("seatlink: the link has no gate pin, so it cannot check a login challenge")

// errChallengeDiffers is the error of a login challenge that is not the one
// the gate's key and the daemon's nonce give. Nothing is asked of the phone.
var errChallengeDiffers = errors.New("seatlink: the gate's login challenge is not the one its key and nonce give")

// errLoginAnswer is the error of an answer to a login call that is not of its
// form. It holds nothing of the answer.
var errLoginAnswer = errors.New("seatlink: the gate's answer to the login is not of its form")

// loginCodes are the codes of the gate a login call maps to the app's errors.
var loginCodes = map[string]error{
	ownerwire.CodeChallengeStale:   seatapp.ErrChallengeStale,
	ownerwire.CodeAssertionInvalid: seatapp.ErrLoginRefused,
	codeKeyUnknown:                 seatapp.ErrLoginRefused,
	ownerwire.CodeKeyRevoked:       seatapp.ErrLoginRefused,
	ownerwire.CodeKindRefused:      seatapp.ErrLoginRefused,
	codeUnknownOp:                  seatapp.ErrNotOffered,
	seatgate.CodeRateLimited:       seatapp.ErrBusy,
	seatgate.CodeBusy:              seatapp.ErrBusy,
	codeTooManyRefusals:            seatapp.ErrBusy,
}

// offers reports whether the gate's hello listed the capability.
func (d *Daemon) offers(capability string) bool {
	for _, c := range d.c.Caps() {
		if c == capability {
			return true
		}
	}
	return false
}

// LoginChallenge asks the daemon for a login challenge (seat.login-challenge)
// and checks it: the challenge must be the one the gate's pin and the
// daemon's nonce give, or nothing is asked of the phone. It returns
// [seatapp.ErrNotOffered] when the gate does not list "login", and
// [seatapp.ErrBusy], before anything is sent, when anyone's room has no
// space for the challenge and its login together: the room of the login is
// kept with the challenge.
func (d *Daemon) LoginChallenge(ctx context.Context) (seatapp.LoginChallenge, error) {
	if ctx.Err() != nil {
		return seatapp.LoginChallenge{}, seatapp.ErrDaemonUnavailable
	}
	if d.pin == "" {
		return seatapp.LoginChallenge{}, errNoPin
	}
	if !d.offers(CapLogin) {
		if _, ok := d.c.Connected(); !ok {
			return seatapp.LoginChallenge{}, seatapp.ErrDaemonUnavailable
		}
		return seatapp.LoginChallenge{}, seatapp.ErrNotOffered
	}
	// The room of the login's seat.login is kept with the challenge's, for
	// this challenge alone: once the phone has answered, the login is never
	// refused for room.
	token, ok := d.budget.reserve(kindPublic)
	if !ok {
		return seatapp.LoginChallenge{}, seatapp.ErrBusy
	}
	out, err := d.loginChallenge(ctx)
	if err != nil {
		d.budget.release(token)
		return seatapp.LoginChallenge{}, err
	}
	d.budget.bind(token, out.ID, out.Expires)
	return out, nil
}

// loginChallenge sends seat.login-challenge and checks the answer.
func (d *Daemon) loginChallenge(ctx context.Context) (seatapp.LoginChallenge, error) {
	var raw json.RawMessage
	if err := d.c.Call(ctx, OpLoginChallenge, nil, &raw); err != nil {
		return seatapp.LoginChallenge{}, mapCall(ctx, OpLoginChallenge, err, loginCodes)
	}
	// The gate's strict reading, then the wire's own decoder: its cap, the
	// challenge id, the lengths of the challenge, the nonce and each
	// allowed id, at most ownerwire.MaxLoginAllow of them, none twice.
	var probe ownerwire.SeatLoginChallengeResult
	if seatgate.DecodeStrict(raw, &probe) != nil {
		return seatapp.LoginChallenge{}, errLoginAnswer
	}
	res, err := ownerwire.DecodeSeatLoginChallengeResult(raw)
	if err != nil {
		return seatapp.LoginChallenge{}, errLoginAnswer
	}
	expires, err := record.ParseTime(res.ExpiresAt)
	if err != nil {
		return seatapp.LoginChallenge{}, errLoginAnswer
	}
	want, err := ownerwire.LoginChallenge(d.pin, res.Nonce)
	if err != nil || subtle.ConstantTimeCompare(want, res.Challenge) != 1 {
		return seatapp.LoginChallenge{}, errChallengeDiffers
	}
	out := seatapp.LoginChallenge{ID: res.ChallengeID, Challenge: want, Expires: expires}
	for _, id := range res.Allow {
		out.Allow = append(out.Allow, append([]byte(nil), id...))
	}
	return out, nil
}

// Login hands the daemon a browser's assertion over a login challenge
// (seat.login) and returns the credential that logged in. The answer must
// name the credential the assertion named, by its raw id and its display id.
// The login of a challenge the link drew, in its life, has the room its
// challenge kept, so it is never refused for room after the passkey.
func (d *Daemon) Login(ctx context.Context, r seatapp.LoginRequest) (seatapp.LoginResult, error) {
	if ctx.Err() != nil {
		return seatapp.LoginResult{}, seatapp.ErrDaemonUnavailable
	}
	if !d.offers(CapLogin) {
		if _, ok := d.c.Connected(); !ok {
			return seatapp.LoginResult{}, seatapp.ErrDaemonUnavailable
		}
		return seatapp.LoginResult{}, seatapp.ErrNotOffered
	}
	// The wire's encoder holds the request to its form (the challenge id,
	// the assertion's lengths, the cap): one outside it is not sent.
	params, err := ownerwire.EncodeSeatLoginParams(ownerwire.SeatLoginParams{
		ChallengeID: r.ChallengeID,
		Assertion: ownerwire.Assertion{
			CredentialID: r.CredentialID, AuthenticatorData: r.AuthenticatorData,
			ClientDataJSON: r.ClientDataJSON, Signature: r.Signature,
		},
	})
	if err != nil {
		return seatapp.LoginResult{}, seatapp.ErrLoginRefused
	}
	// The login is counted in the room its challenge kept, and is never
	// refused for room; one of a challenge whose room is not kept (its life
	// ended, or the link did not draw it) is sent only with room.
	if !d.budget.answer(kindPublic, r.ChallengeID) {
		return seatapp.LoginResult{}, seatapp.ErrBusy
	}
	var raw json.RawMessage
	if err := d.c.Call(ctx, OpLogin, json.RawMessage(params), &raw); err != nil {
		return seatapp.LoginResult{}, mapCall(ctx, OpLogin, err, loginCodes)
	}
	// The wire's decoder checks the kind word and that the display id is
	// the raw id's; the answer must name the credential that signed.
	var probe ownerwire.SeatLoginResult
	if seatgate.DecodeStrict(raw, &probe) != nil {
		return seatapp.LoginResult{}, errLoginAnswer
	}
	res, err := ownerwire.DecodeSeatLoginResult(raw)
	if err != nil || subtle.ConstantTimeCompare(res.CredentialID, r.CredentialID) != 1 || res.Credential != DisplayID(res.CredentialID) {
		return seatapp.LoginResult{}, errLoginAnswer
	}
	return seatapp.LoginResult{Credential: res.Credential, CredentialID: append([]byte(nil), res.CredentialID...), Kind: res.Kind}, nil
}

// DisplayID is the display id of a credential, as the daemon's key list
// gives it: the first 8 bytes of the SHA-256 of its raw id, in lower-case
// hex.
func DisplayID(credentialID []byte) string {
	sum := sha256.Sum256(credentialID)
	return hex.EncodeToString(sum[:8])
}
