package seatapp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// LoginChallenge is the daemon's answer to a login challenge, as the port
// checked it: the port recomputed Challenge from the gate's key and the
// daemon's nonce before it returned it.
type LoginChallenge struct {
	// ID is the daemon's id of the challenge. The app keeps it server-side,
	// with the browser that asked; the browser never sees it.
	ID string
	// Challenge is the 32 bytes the passkey signs.
	Challenge []byte
	// Allow are the raw ids of the credentials that may log in, which the
	// browser hands to the passkey as its allow-list.
	Allow [][]byte
	// Expires is when the daemon drops the challenge.
	Expires time.Time
}

// LoginRequest is a browser's answer to a login challenge: the four fields of
// a WebAuthn assertion and the id of the challenge it answers, which the app
// took from its own store and not from the browser.
type LoginRequest struct {
	ChallengeID       string
	CredentialID      []byte
	AuthenticatorData []byte
	ClientDataJSON    []byte
	Signature         []byte
}

// LoginResult is the daemon's answer to a login that verified: the
// credential that logged in.
type LoginResult struct {
	// Credential is the credential's display id, 16 lower-case hex digits.
	Credential string
	// CredentialID is its raw id.
	CredentialID []byte
	// Kind is the kind word the daemon stores for it.
	Kind string
}

// The errors of the login calls of a Daemon that the app tells apart.
var (
	// ErrNotOffered says that the gate does not offer the operation: it
	// does not list its capability.
	ErrNotOffered = errors.New("seatapp: the desk does not offer this")
	// ErrLoginRefused says that the daemon did not accept the login: the
	// credential is not on the list, is revoked, is of a kind it does not
	// take, or the assertion did not verify.
	ErrLoginRefused = errors.New("seatapp: the login was not accepted")
	// ErrChallengeStale says that the daemon has no such challenge any
	// more: it expired, was used, or was dropped.
	ErrChallengeStale = errors.New("seatapp: the challenge is no longer outstanding")
	// ErrBusy says that the gate did not take the call for its load: the
	// rate of the seat's connection is spent, or too much is in flight; or
	// that the port did not send it, its room at the gate being spent.
	ErrBusy = errors.New("seatapp: the desk is busy")
)

// The limits of the login.
const (
	// loginTTL is the longest a login challenge is kept; the daemon's own
	// expiry is used when it is sooner.
	loginTTL = 5 * time.Minute
	// maxLogins is the most login challenges kept at once. The oldest goes
	// when a new one comes, as the daemon's store of them does: a login
	// challenge is never refused for room.
	maxLogins = 16
	// loginSourceRequests and loginSourceWindow bound the login challenges
	// one source may ask for.
	loginSourceRequests = 10
	loginSourceWindow   = 15 * time.Minute
	// loginAllRequests and loginAllWindow bound the login challenges of all
	// sources together, so that strangers cannot fill the store of them. They
	// do not bound the gate's calls: a challenge may be answered at any time
	// in its life, so the logins of a minute can be more. The port counts
	// every call it sends the gate, and keeps logins and pairings to their
	// own room there (package seatlink: 10 calls in 80 seconds, 5 logins),
	// apart from the owner's reads and decisions; a login past it is
	// answered desk_busy.
	loginAllRequests = 8
	loginAllWindow   = time.Minute
	// The bounds of an assertion, those of the gate's wire.
	minAuthData   = 37
	maxAuthData   = 1024
	maxClientData = 2048
	maxSignature  = 72
	// maxAllow is the most credentials a login challenge may allow.
	maxAllow = 16
	// displayIDLen is the length of a credential's display id.
	displayIDLen = 16
)

// The API's codes of the login.
var (
	errLoginRefused    = apiError{http.StatusForbidden, "login_refused"}
	errLoginNotOffered = apiError{http.StatusServiceUnavailable, "login_unavailable"}
	errNotPaired       = apiError{http.StatusConflict, "not_paired"}
	errDeskBusy        = apiError{http.StatusServiceUnavailable, "desk_busy"}
	loginErrorCodes    = []apiError{errLoginRefused, errLoginNotOffered, errNotPaired, errDeskBusy}
)

// loginAttempt is one login challenge kept for the browser that asked.
type loginAttempt struct {
	challengeID string
	created     time.Time
	expires     time.Time
}

// logins holds the login flow's state: the outstanding challenges, each
// under the key of the login cookie of the browser that asked, and the
// limits.
type logins struct {
	cfg Config

	mu sync.Mutex
	m  map[tokenKey]loginAttempt

	perSource *limiter
	all       *limiter
}

func newLogins(cfg Config) *logins {
	return &logins{
		cfg:       cfg,
		m:         map[tokenKey]loginAttempt{},
		perSource: newLimiter(loginSourceRequests, loginSourceWindow),
		all:       newLimiter(loginAllRequests, loginAllWindow),
	}
}

// keep stores a login challenge under k. Expired ones are dropped first; with
// the store still full, the oldest goes.
func (l *logins) keep(k tokenKey, at loginAttempt) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.cfg.Now()
	for key, old := range l.m {
		if !now.Before(old.expires) {
			delete(l.m, key)
		}
	}
	for len(l.m) >= maxLogins {
		var oldest tokenKey
		var created time.Time
		first := true
		for key, old := range l.m {
			if first || old.created.Before(created) {
				oldest, created, first = key, old.created, false
			}
		}
		delete(l.m, oldest)
	}
	l.m[k] = at
}

// take burns the login challenge of a login cookie's value: it is removed
// whether or not it is still good, and its id is returned when it was known
// and unexpired.
func (l *logins) take(value string) (string, bool) {
	k, ok := keyOf(value)
	if !ok {
		return "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	at, ok := l.m[k]
	delete(l.m, k)
	if !ok || !l.cfg.Now().Before(at.expires) {
		return "", false
	}
	return at.challengeID, true
}

// loginChallenge answers POST /api/login-challenge: it asks the daemon for a
// login challenge, keeps its id with a new login cookie of this browser, and
// answers the challenge and the allow-list.
func (a *app) loginChallenge(w http.ResponseWriter, r *http.Request) {
	l := a.login
	now := a.cfg.Now()
	// Each source is limited, and so are all of them together, so that
	// strangers who reach the login page cannot use up the gate's rate.
	if !l.perSource.allow(source(r, a.cfg.ClientAddrHeader), now) || !l.all.allow("all", now) {
		writeError(w, errRateLimited)
		return
	}
	var in struct{}
	if err := decodeBody(r.Body, &in); err != nil {
		writeError(w, bodyError(err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), daemonTimeout)
	defer cancel()
	ch, err := a.cfg.Daemon.LoginChallenge(ctx)
	if err != nil {
		writeError(w, loginError(err))
		return
	}
	if len(ch.Challenge) != challengeSize || len(ch.Allow) > maxAllow || ch.ID == "" {
		writeError(w, errDaemon)
		return
	}
	if len(ch.Allow) == 0 {
		writeError(w, errNotPaired)
		return
	}
	allow := make([]string, len(ch.Allow))
	for i, id := range ch.Allow {
		if len(id) < minCredentialID || len(id) > maxCredentialID {
			writeError(w, errDaemon)
			return
		}
		allow[i] = b64.EncodeToString(id)
	}
	value, k, err := newToken(a.cfg.Random)
	if err != nil {
		writeError(w, errInternal)
		return
	}
	expires := now.Add(loginTTL)
	if !ch.Expires.IsZero() && ch.Expires.Before(expires) {
		expires = ch.Expires
	}
	l.keep(k, loginAttempt{challengeID: ch.ID, created: now, expires: expires})
	setCookie(w, LoginCookie, value, int(loginTTL/time.Second))
	writeJSON(w, http.StatusOK, map[string]any{"challenge": b64.EncodeToString(ch.Challenge), "allow": allow})
}

// loginBody is the body of POST /api/login: a WebAuthn assertion.
type loginBody struct {
	CredentialID      string `json:"credential_id"`
	ClientDataJSON    string `json:"client_data_json"`
	AuthenticatorData string `json:"authenticator_data"`
	Signature         string `json:"signature"`
}

// loginPost answers POST /api/login: it burns the browser's login challenge,
// hands the assertion to the daemon, and on its answer starts a session with
// a value it draws itself.
func (a *app) loginPost(w http.ResponseWriter, r *http.Request) {
	// The challenge is the one kept with this browser's login cookie, and
	// it is burnt before anything the browser sent is looked at.
	challengeID, ok := a.login.take(cookieValue(r, LoginCookie))
	clearCookie(w, LoginCookie)
	if !ok {
		writeError(w, errChallenge)
		return
	}
	var in loginBody
	if err := decodeBody(r.Body, &in); err != nil {
		writeError(w, bodyError(err))
		return
	}
	credID, ok1 := decodeB64(in.CredentialID)
	clientData, ok2 := decodeB64(in.ClientDataJSON)
	authData, ok3 := decodeB64(in.AuthenticatorData)
	sig, ok4 := decodeB64(in.Signature)
	if !ok1 || !ok2 || !ok3 || !ok4 ||
		len(credID) < minCredentialID || len(credID) > maxCredentialID ||
		len(authData) < minAuthData || len(authData) > maxAuthData ||
		len(clientData) > maxClientData || len(sig) > maxSignature {
		writeError(w, errBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), daemonTimeout)
	defer cancel()
	res, err := a.cfg.Daemon.Login(ctx, LoginRequest{
		ChallengeID: challengeID, CredentialID: credID,
		AuthenticatorData: authData, ClientDataJSON: clientData, Signature: sig,
	})
	if err != nil {
		writeError(w, loginError(err))
		return
	}
	if !validDisplayID(res.Credential) || len(res.CredentialID) < minCredentialID || len(res.CredentialID) > maxCredentialID || res.Kind == "" {
		writeError(w, errDaemon)
		return
	}
	value, k, err := newToken(a.cfg.Random)
	if err != nil {
		writeError(w, errInternal)
		return
	}
	// A session the browser brought ends here: the new one has a value the
	// app drew, never one the browser chose or held before the login.
	a.sess.end(cookieValue(r, SessionCookie))
	a.sess.add(k, Session{Credential: res.Credential, CredentialID: res.CredentialID, Kind: res.Kind})
	setCookie(w, SessionCookie, value, 0)
	writeJSON(w, http.StatusOK, map[string]string{"next": afterLogin})
}

// logout answers POST /api/logout: the session ends, server-side and in the
// browser.
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if err := decodeBody(r.Body, &in); err != nil {
		writeError(w, bodyError(err))
		return
	}
	a.sess.end(cookieValue(r, SessionCookie))
	clearCookie(w, SessionCookie)
	writeJSON(w, http.StatusOK, map[string]string{"next": loginPath})
}

// loginError maps an error of the login calls to the API's code.
func loginError(err error) apiError {
	switch {
	case errors.Is(err, ErrLoginRefused):
		return errLoginRefused
	case errors.Is(err, ErrChallengeStale):
		return errChallenge
	case errors.Is(err, ErrNotOffered):
		return errLoginNotOffered
	case errors.Is(err, ErrBusy):
		return errDeskBusy
	case errors.Is(err, ErrDaemonUnavailable):
		return errDaemonUnavailable
	}
	return errDaemon
}

// validDisplayID reports whether s is a display id: 16 lower-case hex digits.
func validDisplayID(s string) bool {
	if len(s) != displayIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}
