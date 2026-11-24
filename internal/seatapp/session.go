package seatapp

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"sync"
	"time"
)

// The session's cookie and its limits.
const (
	// SessionCookie is the name of the session's cookie. The __Host- prefix
	// makes the browser keep it only when it is Secure, has the path / and
	// names no domain, so no other host, a sibling or a parent, can set or
	// overwrite it.
	SessionCookie = "__Host-seat"
	// LoginCookie is the name of the cookie that ties a login challenge to
	// the browser that asked for it, until the login ends.
	LoginCookie = "__Host-seat-login"
	// SessionIdle is how long a session lives without a request.
	SessionIdle = 30 * time.Minute
	// SessionMax is how long a session lives at most, whatever its use.
	SessionMax = 12 * time.Hour
	// maxSessions bounds the sessions kept; a new one past it drops the one
	// used least recently. Only a login with a paired passkey makes one.
	maxSessions = 16
	// tokenBytes is the size of a cookie's random value: 256 bits.
	tokenBytes = 32
)

// tokenLen is the length of a cookie's value: tokenBytes in unpadded
// base64url.
var tokenLen = base64.RawURLEncoding.EncodedLen(tokenBytes)

// tokenKey is what a cookie's value is kept under: its SHA-256. The value
// itself is kept nowhere, so the table's keys are no cookie, and a lookup
// takes no time that depends on how much of a guess was right.
type tokenKey [sha256.Size]byte

// keyOf returns the key of a cookie value, and false for a value that is
// not of the form the app makes (tokenLen characters of unpadded base64url).
// No value the app did not make is ever looked up.
func keyOf(value string) (tokenKey, bool) {
	if len(value) != tokenLen {
		return tokenKey{}, false
	}
	if b, ok := decodeB64(value); !ok || len(b) != tokenBytes {
		return tokenKey{}, false
	}
	return sha256.Sum256([]byte(value)), true
}

// newToken draws a fresh cookie value from r. The app draws every value it
// sets; it never takes one a browser brought.
func newToken(r io.Reader) (string, tokenKey, error) {
	raw := make([]byte, tokenBytes)
	if _, err := io.ReadFull(r, raw); err != nil {
		return "", tokenKey{}, err
	}
	v := b64.EncodeToString(raw)
	return v, sha256.Sum256([]byte(v)), nil
}

// Session is what a login gives the app: the credential that logged in. A
// later decision of the session uses exactly this credential.
type Session struct {
	// Credential is the credential's display id, 16 lower-case hex digits.
	Credential string
	// CredentialID is its raw id.
	CredentialID []byte
	// Kind is the kind word the daemon stores for it.
	Kind string
	// key is the session's key in the app's table, for the state the app
	// keeps with it (see decide.go).
	key  tokenKey
	chat *chatHistory
}

// session is one session as the app keeps it.
type session struct {
	Session
	created, used time.Time
	// cards are the session's readings of the cards it was shown, and
	// decision the one decision it has a challenge for (see decide.go).
	cards    map[string]*cardReading
	decision *pendingDecision
	// tick counts the uses of the readings, to drop the least used.
	tick uint64
	// gone is closed when the session is removed (logout, idle time, age,
	// eviction), so that a held watch poll ends with it (see watch.go).
	gone chan struct{}
	// kicks are the held watch polls' channels, the oldest first, at most
	// maxHeldPolls: a poll past the most closes the oldest one's to end it.
	kicks []chan struct{}
}

// sessions holds the sessions, server-side, in memory: a restart ends them
// all.
type sessions struct {
	now func() time.Time

	mu sync.Mutex
	m  map[tokenKey]*session
	// seq counts the decisions marked sent, so that a read can tell one
	// marked after it began (see decide.go).
	seq uint64
}

func newSessions(now func() time.Time) *sessions {
	return &sessions{now: now, m: map[tokenKey]*session{}}
}

// live reports whether s is still live at now.
func (s *session) live(now time.Time) bool {
	return now.Sub(s.used) < SessionIdle && now.Sub(s.created) < SessionMax
}

// get returns the session of a cookie value, and marks it used. A session
// past its idle time or its age is removed and is not returned.
func (ss *sessions) get(value string) (Session, bool) {
	k, ok := keyOf(value)
	if !ok {
		return Session{}, false
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.m[k]
	if !ok {
		return Session{}, false
	}
	now := ss.now()
	if !s.live(now) {
		ss.drop(k)
		return Session{}, false
	}
	s.used = now
	out := s.Session
	out.key = k
	return out, true
}

// look returns the session of a cookie value like get, and does not mark it
// used: a request that only looks never extends the session's idle time. A
// session past its idle time or its age is removed and is not returned. The
// watch poll is its only caller (the hint stream uses lookUntil). gone is
// closed when the session ends.
func (ss *sessions) look(value string) (s Session, gone <-chan struct{}, ok bool) {
	s, gone, _, ok = ss.lookUntil(value)
	return s, gone, ok
}

// lookUntil is look, which also returns when the session ends by itself if
// nothing marks it used: the earlier of its idle time and its age. The hint
// stream arms a timer at it, so that it ends with the session in silence.
func (ss *sessions) lookUntil(value string) (s Session, gone <-chan struct{}, until time.Time, ok bool) {
	k, ok := keyOf(value)
	if !ok {
		return Session{}, nil, time.Time{}, false
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	cur, ok := ss.m[k]
	if !ok {
		return Session{}, nil, time.Time{}, false
	}
	if !cur.live(ss.now()) {
		ss.drop(k)
		return Session{}, nil, time.Time{}, false
	}
	out := cur.Session
	out.key = k
	return out, cur.gone, cur.until(), true
}

// liveUntil reports whether the session of k is live, without marking it
// used, and when it ends by itself (see lookUntil). A session past its idle
// time or its age is removed.
func (ss *sessions) liveUntil(k tokenKey) (until time.Time, ok bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	cur, ok := ss.m[k]
	if !ok {
		return time.Time{}, false
	}
	if !cur.live(ss.now()) {
		ss.drop(k)
		return time.Time{}, false
	}
	return cur.until(), true
}

// until is when s ends by itself if nothing marks it used.
func (s *session) until() time.Time {
	idle, age := s.used.Add(SessionIdle), s.created.Add(SessionMax)
	if age.Before(idle) {
		return age
	}
	return idle
}

// holdWatch makes the caller one of the session's held watch polls, at most
// maxHeldPolls of them (one for each tab of the owner's). With that many held
// already, the oldest is ended: its kick channel is closed. It returns the
// caller's own kick channel, closed when a newer poll takes its place, and
// release, which the caller runs when its poll ends. It reports false when
// the session is gone.
func (ss *sessions) holdWatch(k tokenKey) (kick <-chan struct{}, release func(), ok bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.m[k]
	if !ok {
		return nil, nil, false
	}
	for len(s.kicks) >= maxHeldPolls {
		close(s.kicks[0])
		s.kicks = s.kicks[1:]
	}
	mine := make(chan struct{})
	s.kicks = append(s.kicks, mine)
	return mine, func() {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		cur, ok := ss.m[k]
		if !ok {
			return
		}
		for i, c := range cur.kicks {
			if c == mine {
				cur.kicks = append(cur.kicks[:i:i], cur.kicks[i+1:]...)
				return
			}
		}
	}, true
}

// drop removes the session of k and tells its held poll. The caller holds
// the lock.
func (ss *sessions) drop(k tokenKey) {
	if s, ok := ss.m[k]; ok {
		close(s.gone)
		delete(ss.m, k)
	}
}

// add keeps a new session under k. Sessions no longer live are dropped first;
// with the table still full, the one used least recently goes.
func (ss *sessions) add(k tokenKey, s Session) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := ss.now()
	for key, old := range ss.m {
		if !old.live(now) {
			ss.drop(key)
		}
	}
	for len(ss.m) >= maxSessions {
		var oldest tokenKey
		var at time.Time
		first := true
		for key, old := range ss.m {
			if first || old.used.Before(at) {
				oldest, at, first = key, old.used, false
			}
		}
		ss.drop(oldest)
	}
	s.CredentialID = append([]byte(nil), s.CredentialID...)
	s.chat = &chatHistory{}
	ss.m[k] = &session{Session: s, created: now, used: now, gone: make(chan struct{})}
}

// end removes the session of a cookie value, if there is one.
func (ss *sessions) end(value string) {
	k, ok := keyOf(value)
	if !ok {
		return
	}
	ss.mu.Lock()
	ss.drop(k)
	ss.mu.Unlock()
}

// count is the number of sessions kept, live or not yet swept.
func (ss *sessions) count() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return len(ss.m)
}

// cookieValue returns the value of the one cookie of a name the request
// carries. A request with none, or with more than one, has none: two cookies
// of one name are not the app's doing.
func cookieValue(r *http.Request, name string) string {
	cs := r.CookiesNamed(name)
	if len(cs) != 1 {
		return ""
	}
	return cs[0].Value
}

// setCookie sets a cookie of the app: the path /, Secure, HttpOnly and
// SameSite=Strict, so it is the host's alone, travels only over TLS, is out of
// reach of a script, and is not sent with a request that another site starts.
// maxAge 0 makes a cookie that ends with the browser's session; a negative
// one removes the cookie.
func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearCookie removes a cookie of the app.
func clearCookie(w http.ResponseWriter, name string) {
	setCookie(w, name, "", -1)
}

// session returns the session of a request, if it has one that is live.
func (a *app) session(r *http.Request) (Session, bool) {
	return a.sess.get(cookieValue(r, SessionCookie))
}

// loginPath is the login page.
const loginPath = "/login/"

// authed wraps the handler of a page that needs a session. A request without
// one is sent to the login page, and nothing of the page, and no call to the
// daemon, is made for it. A HEAD is refused 405 before anything else: a page
// a session reads is a GET whose body the browser shows, and a HEAD, which
// shows nothing, must not count as a read of a card or of a page of its
// paths, nor settle a lost answer.
func (a *app) authed(h func(http.ResponseWriter, *http.Request, Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeText(w, http.StatusMethodNotAllowed, "method not allowed\n")
			return
		}
		s, ok := a.session(r)
		if !ok {
			w.Header().Set("Location", loginPath)
			writeText(w, http.StatusSeeOther, "log in first\n")
			return
		}
		h(w, r, s)
	}
}
