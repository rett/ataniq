package seatlink

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// The login operations of the gate and their capability, as the contract
// (docs/design/seat-gate.md, "Login") names them. The daemon's side of them is
// not built at this base; until it is, these names are this package's own.
const (
	// CapLogin is the capability of the two login operations.
	CapLogin = "login"
	// OpLoginChallenge asks the daemon for a login challenge.
	OpLoginChallenge = "seat.login-challenge"
	// OpLogin hands the daemon an assertion over a login challenge.
	OpLogin = "seat.login"
)

// LoginDomain is the domain string of a login challenge.
const LoginDomain = "ataniq.seat.login/1"

// The codes of the gate the login tells apart.
const (
	codeChallengeStale   = "challenge-stale"
	codeAssertionInvalid = "assertion-invalid"
	codeKeyUnknown       = "key-unknown"
	codeKeyRevoked       = "key-revoked"
	codeKindRefused      = "kind-refused"
	codeUnknownOp        = "unknown-op"
	codeTooManyRefusals  = "too-many-refusals"
)

// The bounds of the login's wire, the contract's.
const (
	challengeBytes   = 32
	nonceBytes       = 32
	maxAllow         = 16
	minAuthData      = 37
	maxAuthData      = 1024
	maxClientData    = 2048
	maxSignature     = 72
	challengeIDLen   = 16
	displayIDLen     = 16
	maxKindLen       = 32
	maxLoginParams   = 8 << 10
	maxLoginChResult = 64 << 10
)

// LoginChallengeOf computes the challenge of a login, as the contract gives
// it:
//
//	SHA-256( "ataniq.seat.login/1" || BE32(len(instance)) || instance || nonce )
//
// instance is the gate key's fingerprint, the pin the seat holds, as its 64
// lower-case hex digits. The contract places this function in package
// ownerwire, beside the decision's, for the daemon and the seat to share; it
// is not there at this base, so the seat has its own, held to the contract's
// test vector.
func LoginChallengeOf(instance string, nonce []byte) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(LoginDomain))
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(instance)))
	h.Write(n[:])
	h.Write([]byte(instance))
	h.Write(nonce)
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// assertionWire is a WebAuthn assertion on the gate's wire. Every byte string
// is standard base64 in the JSON.
type assertionWire struct {
	CredentialID      []byte `json:"credential_id"`
	AuthenticatorData []byte `json:"authenticator_data"`
	ClientDataJSON    []byte `json:"client_data_json"`
	Signature         []byte `json:"signature"`
}

// loginChallengeResult is the result of seat.login-challenge.
type loginChallengeResult struct {
	ChallengeID string   `json:"challenge_id"`
	Challenge   []byte   `json:"challenge"`
	Nonce       []byte   `json:"nonce"`
	ExpiresAt   string   `json:"expires_at"`
	Allow       [][]byte `json:"allow"`
}

// loginParams is the params of seat.login.
type loginParams struct {
	ChallengeID string        `json:"challenge_id"`
	Assertion   assertionWire `json:"assertion"`
}

// loginResult is the result of seat.login.
type loginResult struct {
	Credential   string `json:"credential"`
	CredentialID []byte `json:"credential_id"`
	Kind         string `json:"kind"`
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
	codeChallengeStale:       seatapp.ErrChallengeStale,
	codeAssertionInvalid:     seatapp.ErrLoginRefused,
	codeKeyUnknown:           seatapp.ErrLoginRefused,
	codeKeyRevoked:           seatapp.ErrLoginRefused,
	codeKindRefused:          seatapp.ErrLoginRefused,
	codeUnknownOp:            seatapp.ErrNotOffered,
	seatgate.CodeRateLimited: seatapp.ErrBusy,
	seatgate.CodeBusy:        seatapp.ErrBusy,
	codeTooManyRefusals:      seatapp.ErrBusy,
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
// [seatapp.ErrNotOffered] when the gate does not list "login".
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
	var raw json.RawMessage
	if err := d.c.Call(ctx, OpLoginChallenge, nil, &raw); err != nil {
		return seatapp.LoginChallenge{}, mapCall(ctx, OpLoginChallenge, err, loginCodes)
	}
	if len(raw) > maxLoginChResult {
		return seatapp.LoginChallenge{}, errLoginAnswer
	}
	var res loginChallengeResult
	if seatgate.DecodeStrict(raw, &res) != nil || !validChallengeID(res.ChallengeID) ||
		len(res.Challenge) != challengeBytes || len(res.Nonce) != nonceBytes || len(res.Allow) > maxAllow {
		return seatapp.LoginChallenge{}, errLoginAnswer
	}
	expires, err := record.ParseTime(res.ExpiresAt)
	if err != nil {
		return seatapp.LoginChallenge{}, errLoginAnswer
	}
	for _, id := range res.Allow {
		if len(id) < ownerwire.MinCredentialIDBytes || len(id) > ownerwire.MaxCredentialIDBytes {
			return seatapp.LoginChallenge{}, errLoginAnswer
		}
	}
	want := LoginChallengeOf(d.pin, res.Nonce)
	if subtle.ConstantTimeCompare(want[:], res.Challenge) != 1 {
		return seatapp.LoginChallenge{}, errChallengeDiffers
	}
	out := seatapp.LoginChallenge{ID: res.ChallengeID, Challenge: want[:], Expires: expires}
	for _, id := range res.Allow {
		out.Allow = append(out.Allow, append([]byte(nil), id...))
	}
	return out, nil
}

// Login hands the daemon a browser's assertion over a login challenge
// (seat.login) and returns the credential that logged in. The answer must
// name the credential the assertion named, by its raw id and its display id.
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
	a := assertionWire{
		CredentialID: r.CredentialID, AuthenticatorData: r.AuthenticatorData,
		ClientDataJSON: r.ClientDataJSON, Signature: r.Signature,
	}
	if !validChallengeID(r.ChallengeID) || !validAssertion(a) {
		return seatapp.LoginResult{}, seatapp.ErrLoginRefused
	}
	params, err := json.Marshal(loginParams{ChallengeID: r.ChallengeID, Assertion: a})
	if err != nil || len(params) > maxLoginParams {
		return seatapp.LoginResult{}, seatapp.ErrLoginRefused
	}
	var raw json.RawMessage
	if err := d.c.Call(ctx, OpLogin, json.RawMessage(params), &raw); err != nil {
		return seatapp.LoginResult{}, mapCall(ctx, OpLogin, err, loginCodes)
	}
	var res loginResult
	if seatgate.DecodeStrict(raw, &res) != nil || !validKind(res.Kind) ||
		subtle.ConstantTimeCompare(res.CredentialID, r.CredentialID) != 1 || res.Credential != DisplayID(res.CredentialID) {
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

// validAssertion reports whether an assertion is within the wire's bounds.
func validAssertion(a assertionWire) bool {
	return len(a.CredentialID) >= ownerwire.MinCredentialIDBytes && len(a.CredentialID) <= ownerwire.MaxCredentialIDBytes &&
		len(a.AuthenticatorData) >= minAuthData && len(a.AuthenticatorData) <= maxAuthData &&
		len(a.ClientDataJSON) >= 1 && len(a.ClientDataJSON) <= maxClientData &&
		len(a.Signature) >= 1 && len(a.Signature) <= maxSignature
}

// validChallengeID reports whether s is a challenge id: 16 lower-case hex
// digits.
func validChallengeID(s string) bool { return lowerHex(s, challengeIDLen) }

// lowerHex reports whether s is n lower-case hex digits.
func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// validKind reports whether s is a kind word of the key list's grammar: 1 to
// 32 lower-case letters, digits and dashes, starting with a letter.
func validKind(s string) bool {
	if s == "" || len(s) > maxKindLen || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= 'a' && s[i] <= 'z' || s[i] >= '0' && s[i] <= '9' || s[i] == '-') {
			return false
		}
	}
	return true
}
