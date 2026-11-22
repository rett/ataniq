package seatapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The limits of the pairing flow.
const (
	challengeSize     = 32
	challengeTTL      = 5 * time.Minute
	maxChallenges     = 32
	pairAttempts      = 5
	pairWindow        = 15 * time.Minute
	challengeRequests = 20
	daemonTimeout     = 15 * time.Second
	maxCodeLen        = 64
	maxWordLen        = 32
	minCredentialID   = 16
	maxCredentialID   = 1024
)

// apiError is a failure of the API: a status and a fixed code. A response
// holds the code and nothing the request sent.
type apiError struct {
	status int
	code   string
}

// The set of codes the API answers with.
var (
	errBadRequest        = apiError{http.StatusBadRequest, "bad_request"}
	errTooLarge          = apiError{http.StatusRequestEntityTooLarge, "too_large"}
	errChallenge         = apiError{http.StatusBadRequest, "challenge_invalid"}
	errClientData        = apiError{http.StatusBadRequest, "client_data_invalid"}
	errAuthData          = apiError{http.StatusBadRequest, "authenticator_data_invalid"}
	errUserVerification  = apiError{http.StatusBadRequest, "user_verification_required"}
	errCredential        = apiError{http.StatusBadRequest, "credential_invalid"}
	errKey               = apiError{http.StatusBadRequest, "key_invalid"}
	errCodeRefused       = apiError{http.StatusForbidden, "code_refused"}
	errRateLimited       = apiError{http.StatusTooManyRequests, "rate_limited"}
	errDaemonUnavailable = apiError{http.StatusServiceUnavailable, "daemon_unavailable"}
	errDaemon            = apiError{http.StatusBadGateway, "daemon_error"}
	errInternal          = apiError{http.StatusInternalServerError, "internal"}
	errorCodes           = []apiError{errBadRequest, errTooLarge, errChallenge, errClientData, errAuthData, errUserVerification, errCredential, errKey, errCodeRefused, errRateLimited, errDaemonUnavailable, errDaemon, errInternal}
)

// writeJSON writes v as the response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeText(w, http.StatusInternalServerError, "internal error\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, e apiError) {
	writeJSON(w, e.status, map[string]string{"error": e.code})
}

// pairing holds the pairing flow's state: the outstanding challenges and the
// per-source limits.
type pairing struct {
	cfg     Config
	rpIDSum [sha256.Size]byte

	mu         sync.Mutex
	challenges map[string]time.Time // the challenge, base64url, to its expiry

	pairLimit      *limiter
	challengeLimit *limiter
}

func newPairing(cfg Config) *pairing {
	return &pairing{
		cfg:            cfg,
		rpIDSum:        sha256.Sum256([]byte(cfg.RPID)),
		challenges:     map[string]time.Time{},
		pairLimit:      newLimiter(pairAttempts, pairWindow),
		challengeLimit: newLimiter(challengeRequests, pairWindow),
	}
}

var b64 = base64.RawURLEncoding.Strict()

// decodeB64 decodes unpadded base64url, strictly: no padding, no line breaks,
// no spare bits.
func decodeB64(s string) ([]byte, bool) {
	if s == "" || strings.ContainsAny(s, "\r\n") {
		return nil, false
	}
	b, err := b64.DecodeString(s)
	return b, err == nil
}

// challenge answers POST /api/pair-challenge with a new challenge, kept for
// challengeTTL, single use, at most maxChallenges outstanding.
func (p *pairing) challenge(w http.ResponseWriter, r *http.Request) {
	now := p.cfg.Now()
	if !p.challengeLimit.allow(source(r, p.cfg.ClientAddrHeader), now) {
		writeError(w, errRateLimited)
		return
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		writeError(w, bodyError(err))
		return
	}
	raw := make([]byte, challengeSize)
	if _, err := io.ReadFull(p.cfg.Random, raw); err != nil {
		writeError(w, errInternal)
		return
	}
	enc := b64.EncodeToString(raw)
	p.mu.Lock()
	for k, exp := range p.challenges {
		if !now.Before(exp) {
			delete(p.challenges, k)
		}
	}
	if len(p.challenges) >= maxChallenges {
		p.mu.Unlock()
		writeError(w, errRateLimited)
		return
	}
	p.challenges[enc] = now.Add(challengeTTL)
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"challenge": enc})
}

// take burns a challenge: it is removed whether or not it is still good, and
// the result says whether it was known and unexpired.
func (p *pairing) take(enc string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	exp, ok := p.challenges[enc]
	delete(p.challenges, enc)
	return ok && now.Before(exp)
}

// pairBody is the body of POST /api/pair.
type pairBody struct {
	Code              string `json:"code"`
	Challenge         string `json:"challenge"`
	CredentialID      string `json:"credential_id"`
	ClientDataJSON    string `json:"client_data_json"`
	AuthenticatorData string `json:"authenticator_data"`
	PublicKey         string `json:"public_key"`
}

// bodyError maps a failure to read a request body.
func bodyError(err error) apiError {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return errTooLarge
	}
	return errBadRequest
}

// decodeBody decodes the one JSON object of a body into v, with no unknown
// field and nothing after it.
func decodeBody(body io.Reader, v any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the object")
	}
	return nil
}

// pair answers POST /api/pair: it checks what the browser made and, when it
// holds, hands the code, the credential id and the key to the daemon.
func (p *pairing) pair(w http.ResponseWriter, r *http.Request) {
	now := p.cfg.Now()
	if !p.pairLimit.allow(source(r, p.cfg.ClientAddrHeader), now) {
		writeError(w, errRateLimited)
		return
	}
	var in pairBody
	if err := decodeBody(r.Body, &in); err != nil {
		writeError(w, bodyError(err))
		return
	}
	if in.Challenge == "" {
		writeError(w, errBadRequest)
		return
	}
	// The challenge is burned before anything else is looked at.
	if !p.take(in.Challenge, now) {
		writeError(w, errChallenge)
		return
	}
	req, e := p.verify(in)
	if e != nil {
		writeError(w, *e)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), daemonTimeout)
	defer cancel()
	res, err := p.cfg.Daemon.Pair(ctx, req)
	switch {
	case errors.Is(err, ErrCodeRefused):
		writeError(w, errCodeRefused)
	case errors.Is(err, ErrDaemonUnavailable):
		writeError(w, errDaemonUnavailable)
	case err != nil:
		writeError(w, errDaemon)
	default:
		// The words come from the daemon, which is not the app's own text.
		var words [6]string
		for i, word := range res.Words {
			words[i] = oneLine(word, maxWordLen)
		}
		writeJSON(w, http.StatusOK, map[string]any{"words": words})
	}
}

// verify checks a pairing body against the challenge it named and the app's
// origin and relying party, and returns the daemon request.
func (p *pairing) verify(in pairBody) (PairRequest, *apiError) {
	fail := func(e apiError) (PairRequest, *apiError) { return PairRequest{}, &e }
	if !validCode(in.Code) {
		return fail(errBadRequest)
	}
	credID, ok1 := decodeB64(in.CredentialID)
	clientData, ok2 := decodeB64(in.ClientDataJSON)
	authData, ok3 := decodeB64(in.AuthenticatorData)
	spki, ok4 := decodeB64(in.PublicKey)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return fail(errBadRequest)
	}
	if len(credID) < minCredentialID || len(credID) > maxCredentialID {
		return fail(errCredential)
	}
	if !p.clientDataOK(clientData, in.Challenge) {
		return fail(errClientData)
	}
	keySPKI, e := p.authDataCheck(authData, credID)
	if e != nil {
		return fail(*e)
	}
	// The key the authenticator made is the key in its attested credential
	// data. The browser's own SPKI accessor must give that key, byte for byte:
	// with attestation "none" nothing is signed, so this is what binds the
	// credential id to the key.
	if !bytes.Equal(keySPKI, spki) {
		return fail(errKey)
	}
	return PairRequest{Code: in.Code, CredentialID: credID, SPKI: spki}, nil
}

// validCode accepts what a one-time code can look like: 1 to maxCodeLen
// printable ASCII characters other than space. The daemon decides whether it
// is the code.
func validCode(c string) bool {
	if c == "" || len(c) > maxCodeLen {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] <= ' ' || c[i] >= 0x7f {
			return false
		}
	}
	return true
}

// clientDataOK parses the client data strictly (one JSON object, nothing after
// it, the members of the check present with their exact types) and checks it
// is a registration for this challenge from this origin, not made in a frame
// of another site.
func (p *pairing) clientDataOK(raw []byte, challenge string) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var m map[string]json.RawMessage
	if err := dec.Decode(&m); err != nil || m == nil {
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		return false
	}
	str := func(key string) (string, bool) {
		v, ok := m[key]
		if !ok {
			return "", false
		}
		var s string
		return s, json.Unmarshal(v, &s) == nil && string(v) != "null"
	}
	typ, ok := str("type")
	if !ok || typ != "webauthn.create" {
		return false
	}
	ch, ok := str("challenge")
	if !ok || subtle.ConstantTimeCompare([]byte(ch), []byte(challenge)) != 1 {
		return false
	}
	origin, ok := str("origin")
	if !ok || origin != p.cfg.Origin {
		return false
	}
	if v, present := m["crossOrigin"]; present && string(bytes.TrimSpace(v)) != "false" {
		return false
	}
	// A top origin is there only when the page was in another site's frame.
	if _, present := m["topOrigin"]; present {
		return false
	}
	return true
}

// Flags of the authenticator data.
const (
	flagUP = 0x01
	flagUV = 0x04
	flagAT = 0x40
	flagED = 0x80
)

// authDataCheck parses the authenticator data of a registration and checks the
// relying party hash, the flags and that the attested credential's id is the
// posted one. The data is rpIdHash (32), flags (1), counter (4), then the
// attested credential data: aaguid (16), id length (2), id, and the COSE_Key
// of the credential's public key; after it, only the extensions map the ED
// flag announces. It returns the canonical DER SubjectPublicKeyInfo of the
// COSE_Key.
func (p *pairing) authDataCheck(ad, credID []byte) ([]byte, *apiError) {
	const head = 32 + 1 + 4 + 16 + 2
	if len(ad) < head {
		return nil, &errAuthData
	}
	if subtle.ConstantTimeCompare(ad[:32], p.rpIDSum[:]) != 1 {
		return nil, &errAuthData
	}
	flags := ad[32]
	if flags&flagUP == 0 || flags&flagAT == 0 {
		return nil, &errAuthData
	}
	if flags&flagUV == 0 {
		return nil, &errUserVerification
	}
	n := int(binary.BigEndian.Uint16(ad[head-2 : head]))
	if n < minCredentialID || n > maxCredentialID || len(ad) < head+n+1 {
		return nil, &errAuthData
	}
	if subtle.ConstantTimeCompare(ad[head:head+n], credID) != 1 {
		return nil, &errCredential
	}
	spki, used, e := coseES256(ad[head+n:])
	if e != nil {
		return nil, e
	}
	rest := ad[head+n+used:]
	switch {
	case flags&flagED != 0 && !extensionsOK(rest):
		return nil, &errAuthData
	case flags&flagED == 0 && len(rest) != 0:
		return nil, &errAuthData
	}
	return spki, nil
}
