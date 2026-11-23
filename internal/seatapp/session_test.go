package seatapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// checkCookieFlags fails the test when a cookie of the app is not a __Host-
// cookie with the path /, Secure, HttpOnly and SameSite=Strict, and no
// domain.
func checkCookieFlags(t *testing.T, what string, c *http.Cookie) {
	t.Helper()
	if !strings.HasPrefix(c.Name, "__Host-") || c.Path != "/" || c.Domain != "" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("%s: cookie %q: path %q, domain %q, secure %v, http-only %v, same-site %v", what, c.Name, c.Path, c.Domain, c.Secure, c.HttpOnly, c.SameSite)
	}
	if strings.Contains(c.Raw, "Expires=") {
		t.Errorf("%s: cookie %q has an Expires date", what, c.Name)
	}
}

// cookieOf returns the cookie of a name a response sets, or nil.
func cookieOf(w *httptest.ResponseRecorder, name string) *http.Cookie {
	var found *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			found = c
		}
	}
	return found
}

// postWith sends a well-formed same-origin POST with cookies, from a source
// address of its own (see testClientHeader), so that the per-source limits
// do not end a test that makes many logins.
func (r *rig) postWith(path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := newRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Content-Type", "application/json")
	r.sources++
	req.Header.Set(testClientHeader, fmt.Sprintf("198.51.%d.%d", r.sources/250, r.sources%250+1))
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

// getWith sends a GET with cookies.
func (r *rig) getWith(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := newRequest("GET", path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

// assertionBody is a login body of the form the app takes. The fake daemon
// does not verify it; seatlink's tests verify a real one.
func assertionBody() string {
	b, _ := json.Marshal(map[string]string{
		"credential_id":      b64.EncodeToString(testCredentialID),
		"client_data_json":   b64.EncodeToString([]byte(`{"type":"webauthn.get"}`)),
		"authenticator_data": b64.EncodeToString(bytes.Repeat([]byte{0xaa}, 37)),
		"signature":          b64.EncodeToString([]byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01}),
	})
	return string(b)
}

// startLogin asks for a login challenge with the cookies given and returns
// the login cookie the answer sets.
func (r *rig) startLogin(t *testing.T, cookies ...*http.Cookie) *http.Cookie {
	t.Helper()
	w := r.postWith("/api/login-challenge", "{}", cookies...)
	if w.Code != 200 {
		t.Fatalf("login challenge: %d %s", w.Code, w.Body)
	}
	c := cookieOf(w, LoginCookie)
	if c == nil || c.Value == "" {
		t.Fatalf("login challenge: no login cookie")
	}
	return c
}

// login logs in with the cookies given and returns the session cookie.
func (r *rig) login(t *testing.T, cookies ...*http.Cookie) *http.Cookie {
	t.Helper()
	lc := r.startLogin(t, cookies...)
	w := r.postWith("/api/login", assertionBody(), append([]*http.Cookie{lc}, cookies...)...)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	c := cookieOf(w, SessionCookie)
	if c == nil || c.Value == "" {
		t.Fatalf("login: no session cookie")
	}
	return c
}

// sessionOf reports whether a cookie value is a live session of the app,
// as a request carrying it would find.
func (r *rig) sessionOf(value string) (Session, bool) {
	req := newRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: value})
	return r.app.session(req)
}

// TestSeatLoginMakesASession: a login challenge goes to the browser with its
// allow-list and a login cookie; the login hands the daemon the assertion and
// the id of the challenge kept with that cookie; the session's cookie is a
// __Host- cookie with a fresh 256-bit value, and the login cookie is removed.
func TestSeatLoginMakesASession(t *testing.T) {
	r := newRig(t)
	w := r.postWith("/api/login-challenge", "{}")
	if w.Code != 200 {
		t.Fatalf("challenge: %d %s", w.Code, w.Body)
	}
	var ch struct {
		Challenge string   `json:"challenge"`
		Allow     []string `json:"allow"`
	}
	if err := decodeBody(w.Body, &ch); err != nil {
		t.Fatalf("challenge body: %v", err)
	}
	if ch.Challenge != b64.EncodeToString(bytes.Repeat([]byte{0x5a}, 32)) || len(ch.Allow) != 1 || ch.Allow[0] != b64.EncodeToString(testCredentialID) {
		t.Errorf("challenge answer %+v", ch)
	}
	lc := cookieOf(w, LoginCookie)
	if lc == nil {
		t.Fatal("no login cookie")
	}
	checkCookieFlags(t, "login cookie", lc)
	if lc.MaxAge != 300 || len(lc.Value) != 43 {
		t.Errorf("login cookie: max-age %d, value of %d characters", lc.MaxAge, len(lc.Value))
	}
	if strings.Contains(w.Body.String(), "0000000000000001") {
		t.Error("the challenge's id went to the browser")
	}

	w = r.postWith("/api/login", assertionBody(), lc)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"next":"`+afterLogin+`"}` {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	if got := r.daemon.logins; len(got) != 1 || got[0].ChallengeID != "0000000000000001" ||
		!bytes.Equal(got[0].CredentialID, testCredentialID) || !bytes.Equal(got[0].AuthenticatorData, bytes.Repeat([]byte{0xaa}, 37)) ||
		string(got[0].ClientDataJSON) != `{"type":"webauthn.get"}` || len(got[0].Signature) != 8 {
		t.Errorf("the daemon was asked %+v", got)
	}
	sc := cookieOf(w, SessionCookie)
	if sc == nil {
		t.Fatal("no session cookie")
	}
	checkCookieFlags(t, "session cookie", sc)
	if sc.MaxAge != 0 || len(sc.Value) != 43 || sc.Value == lc.Value {
		t.Errorf("session cookie: max-age %d, value %q", sc.MaxAge, sc.Value)
	}
	raw := strings.Join(w.Header().Values("Set-Cookie"), "\n")
	for _, want := range []string{SessionCookie + "=" + sc.Value + "; Path=/; HttpOnly; Secure; SameSite=Strict", LoginCookie + "=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Strict"} {
		if !strings.Contains(raw, want) {
			t.Errorf("Set-Cookie lacks %q:\n%s", want, raw)
		}
	}
	s, ok := r.sessionOf(sc.Value)
	if !ok || s.Credential != "0123456789abcdef" || !bytes.Equal(s.CredentialID, testCredentialID) || s.Kind != "platform-passkey" {
		t.Errorf("session %+v %v", s, ok)
	}
	// The value is kept only as its digest.
	r.app.sess.mu.Lock()
	for k := range r.app.sess.m {
		if bytes.Contains(k[:], []byte(sc.Value)) {
			t.Error("the table keeps the cookie's value")
		}
	}
	r.app.sess.mu.Unlock()
}

// TestSeatSessionFixation: a session value the browser brings is never the
// session's. A value an attacker planted before the login is not taken, and a
// session the browser held is ended by a new login, which gets a new value.
func TestSeatSessionFixation(t *testing.T) {
	r := newRig(t)
	planted := &http.Cookie{Name: SessionCookie, Value: b64.EncodeToString(bytes.Repeat([]byte{7}, 32))}
	sc := r.login(t, planted)
	if sc.Value == planted.Value {
		t.Fatal("the login took the planted value")
	}
	if _, ok := r.sessionOf(planted.Value); ok {
		t.Error("the planted value is a session")
	}
	if _, ok := r.sessionOf(sc.Value); !ok {
		t.Error("the new value is not a session")
	}

	// A second login from the same browser ends the first session.
	again := r.login(t, sc)
	if again.Value == sc.Value {
		t.Fatal("a second login kept the session's value")
	}
	if _, ok := r.sessionOf(sc.Value); ok {
		t.Error("the session from before the second login is still live")
	}
	if _, ok := r.sessionOf(again.Value); !ok {
		t.Error("the second login made no session")
	}
	// A value that is not of the app's form is not even looked up.
	for _, v := range []string{"", "x", planted.Value + "A", strings.Repeat("A", 42) + "=", strings.Repeat("+", 43)} {
		if _, ok := keyOf(v); ok {
			t.Errorf("keyOf(%q) took it", v)
		}
	}
}

// TestSeatLoginChallengeIsTheBrowsers: the challenge a login answers is the
// one the app kept with the browser's own login cookie, taken once; a login
// without that cookie, with one already used, or after the challenge ran out
// asks the daemon nothing.
func TestSeatLoginChallengeIsTheBrowsers(t *testing.T) {
	r := newRig(t)
	a := r.startLogin(t)
	b := r.startLogin(t)
	// Browser B answers with its own cookie: its own challenge is used.
	if w := r.postWith("/api/login", assertionBody(), b); w.Code != 200 {
		t.Fatalf("login of b: %d %s", w.Code, w.Body)
	}
	if got := r.daemon.logins; len(got) != 1 || got[0].ChallengeID != "0000000000000002" {
		t.Errorf("b's login named %+v", got)
	}
	// No cookie, the used cookie again, or a cookie the app never made.
	for name, cookies := range map[string][]*http.Cookie{
		"none":    nil,
		"used":    {b},
		"unknown": {{Name: LoginCookie, Value: b64.EncodeToString(bytes.Repeat([]byte{9}, 32))}},
		"twice":   {a, a},
	} {
		w := r.postWith("/api/login", assertionBody(), cookies...)
		wantErrorNoCookies(t, name, w, 400, "challenge_invalid")
	}
	if n := len(r.daemon.logins); n != 1 {
		t.Errorf("the daemon was asked %d logins", n)
	}
	// "twice" burnt nothing: a request with two login cookies is none.
	if w := r.postWith("/api/login", assertionBody(), a); w.Code != 200 {
		t.Errorf("a's login after the refusals: %d %s", w.Code, w.Body)
	}

	// The challenge runs out after 5 minutes, or sooner when the daemon's
	// expiry is sooner.
	c := r.startLogin(t)
	r.clock.Advance(loginTTL)
	wantErrorNoCookies(t, "expired", r.postWith("/api/login", assertionBody(), c), 400, "challenge_invalid")
	r.daemon.loginCh.Expires = r.clock.Now().Add(time.Minute)
	c = r.startLogin(t)
	r.clock.Advance(time.Minute)
	wantErrorNoCookies(t, "the daemon's expiry", r.postWith("/api/login", assertionBody(), c), 400, "challenge_invalid")
	if n := len(r.daemon.logins); n != 2 {
		t.Errorf("the daemon was asked %d logins", n)
	}
}

// wantErrorNoCookies is wantError for a login answer, which may remove the
// login cookie and sets no other.
func wantErrorNoCookies(t *testing.T, what string, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status || w.Body.String() != `{"error":"`+code+`"}`+"\n" {
		t.Errorf("%s: %d %q, want %d %s", what, w.Code, w.Body.String(), status, code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name != LoginCookie || c.Value != "" || c.MaxAge >= 0 {
			t.Errorf("%s: sets the cookie %q=%q", what, c.Name, c.Value)
		}
	}
}

// TestSeatSessionLimits: a session ends 30 minutes after its last use and 12
// hours after it began, whatever its use.
func TestSeatSessionLimits(t *testing.T) {
	r := newRig(t)
	sc := r.login(t)
	r.clock.Advance(SessionIdle - time.Second)
	if _, ok := r.sessionOf(sc.Value); !ok {
		t.Fatal("the session ended before its idle time")
	}
	r.clock.Advance(SessionIdle - time.Second)
	if _, ok := r.sessionOf(sc.Value); !ok {
		t.Fatal("a use did not keep the session")
	}
	r.clock.Advance(SessionIdle)
	if _, ok := r.sessionOf(sc.Value); ok {
		t.Fatal("the session outlived its idle time")
	}

	sc = r.login(t)
	start := r.clock.Now()
	for r.clock.Now().Sub(start) < SessionMax-20*time.Minute {
		r.clock.Advance(20 * time.Minute)
		if _, ok := r.sessionOf(sc.Value); !ok {
			t.Fatalf("the session ended after %v of use", r.clock.Now().Sub(start))
		}
	}
	r.clock.Advance(SessionMax - r.clock.Now().Sub(start))
	if _, ok := r.sessionOf(sc.Value); ok {
		t.Fatal("the session outlived its 12 hours")
	}
	if r.app.sess.count() != 0 {
		t.Errorf("%d sessions are kept", r.app.sess.count())
	}
}

// TestSeatLogout: the session ends at the seat and the cookie is removed.
func TestSeatLogout(t *testing.T) {
	r := newRig(t)
	sc := r.login(t)
	w := r.postWith("/api/logout", "{}", sc)
	if w.Code != 200 {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	c := cookieOf(w, SessionCookie)
	if c == nil || c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("logout cookie %+v", c)
	}
	if _, ok := r.sessionOf(sc.Value); ok {
		t.Error("the session is live after the logout")
	}
}

// TestSeatSessionsAreBounded: past the bound, the session used least
// recently goes.
func TestSeatSessionsAreBounded(t *testing.T) {
	r := newRig(t)
	// Ten seconds apart, so that the bound of the login challenges is not
	// met.
	first := r.login(t)
	r.clock.Advance(10 * time.Second)
	var last *http.Cookie
	for i := 0; i < maxSessions; i++ {
		last = r.login(t)
		r.clock.Advance(10 * time.Second)
	}
	if r.app.sess.count() != maxSessions {
		t.Errorf("%d sessions kept", r.app.sess.count())
	}
	if _, ok := r.sessionOf(first.Value); ok {
		t.Error("the oldest session was kept")
	}
	if _, ok := r.sessionOf(last.Value); !ok {
		t.Error("the newest session was dropped")
	}
}

// TestSeatLoginErrors: each answer of the daemon has its code, a login that
// fails makes no session, and an empty allow-list keeps no challenge.
func TestSeatLoginErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrLoginRefused, 403, "login_refused"},
		{ErrChallengeStale, 400, "challenge_invalid"},
		{ErrNotOffered, 503, "login_unavailable"},
		{ErrDaemonUnavailable, 503, "daemon_unavailable"},
		{ErrBusy, 503, "desk_busy"},
		{fmt.Errorf("wrapped: %w", ErrLoginRefused), 403, "login_refused"},
		{errors.New("some text that must not be shown"), 502, "daemon_error"},
	} {
		r := newRig(t)
		r.daemon.loginErr = tc.err
		lc := r.startLogin(t)
		w := r.postWith("/api/login", assertionBody(), lc)
		wantErrorNoCookies(t, "login: "+tc.code, w, tc.status, tc.code)

		r = newRig(t)
		r.daemon.loginChErr = tc.err
		w = r.postWith("/api/login-challenge", "{}")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), `"`+tc.code+`"`) || cookieOf(w, LoginCookie) != nil {
			t.Errorf("challenge: %v: %d %s", tc.err, w.Code, w.Body)
		}
	}
	// No paired credential: the phone is told so and nothing is kept.
	r := newRig(t)
	r.daemon.loginCh.Allow = nil
	w := r.postWith("/api/login-challenge", "{}")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "not_paired") || cookieOf(w, LoginCookie) != nil {
		t.Errorf("no keys: %d %s", w.Code, w.Body)
	}
	// An answer of the daemon that is not of its form is no session.
	for name, mod := range map[string]func(*fakeDaemon){
		"display id":     func(d *fakeDaemon) { d.loginRes.Credential = "0123456789ABCDEF" },
		"short id":       func(d *fakeDaemon) { d.loginRes.CredentialID = []byte{1} },
		"no kind":        func(d *fakeDaemon) { d.loginRes.Kind = "" },
		"short ch":       func(d *fakeDaemon) { d.loginCh.Challenge = d.loginCh.Challenge[:31] },
		"short allow id": func(d *fakeDaemon) { d.loginCh.Allow = [][]byte{{1, 2, 3}} },
	} {
		r := newRig(t)
		mod(r.daemon)
		w := r.postWith("/api/login-challenge", "{}")
		if lc := cookieOf(w, LoginCookie); lc != nil {
			w = r.postWith("/api/login", assertionBody(), lc)
		}
		if w.Code != 502 || cookieOf(w, SessionCookie) != nil {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

// TestSeatLoginBody: an assertion outside the wire's bounds is refused before
// the daemon is asked, and the challenge is burnt all the same.
func TestSeatLoginBody(t *testing.T) {
	good := map[string][]byte{
		"credential_id":      testCredentialID,
		"client_data_json":   []byte(`{}`),
		"authenticator_data": bytes.Repeat([]byte{1}, 37),
		"signature":          []byte{1},
	}
	for name, change := range map[string][2]any{
		"short id":         {"credential_id", make([]byte, 15)},
		"long id":          {"credential_id", make([]byte, 1024)},
		"short auth data":  {"authenticator_data", make([]byte, 36)},
		"long auth data":   {"authenticator_data", make([]byte, 1025)},
		"long client data": {"client_data_json", make([]byte, 2049)},
		"long signature":   {"signature", make([]byte, 73)},
		"empty signature":  {"signature", []byte{}},
	} {
		r := newRig(t)
		lc := r.startLogin(t)
		fields := map[string]string{}
		for k, v := range good {
			fields[k] = b64.EncodeToString(v)
		}
		fields[change[0].(string)] = b64.EncodeToString(change[1].([]byte))
		body, _ := json.Marshal(fields)
		w := r.postWith("/api/login", string(body), lc)
		if w.Code != 400 && w.Code != 413 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
		if len(r.daemon.logins) != 0 {
			t.Errorf("%s: the daemon was asked", name)
		}
		if w := r.postWith("/api/login", assertionBody(), lc); w.Code != 400 {
			t.Errorf("%s: the challenge was not burnt: %d", name, w.Code)
		}
	}
	// Unknown fields and padding are refused as well.
	r := newRig(t)
	lc := r.startLogin(t)
	if w := r.postWith("/api/login", `{"credential_id":"x","extra":1}`, lc); w.Code != 400 {
		t.Errorf("unknown field: %d", w.Code)
	}
}

// TestSeatLoginChallengeLimits: each source asks at most 10 login challenges
// in 15 minutes, and all sources together 8 a minute, so that logins never
// take more than 16 of the gate's 60 requests a minute. A refused request
// asks the daemon nothing.
func TestSeatLoginChallengeLimits(t *testing.T) {
	r := newRig(t, func(c *Config) { c.ClientAddrHeader = "X-Client" })
	ask := func(addr string) int {
		req := newRequest("POST", "/api/login-challenge", strings.NewReader("{}"))
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Client", addr)
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		return w.Code
	}
	for i := 0; i < loginAllRequests; i++ {
		if c := ask(fmt.Sprintf("192.0.2.%d", i+1)); c != 200 {
			t.Fatalf("request %d: %d", i, c)
		}
	}
	if c := ask("192.0.2.200"); c != 429 {
		t.Errorf("past the bound of all sources: %d", c)
	}
	if r.daemon.loginChs != loginAllRequests {
		t.Errorf("the daemon was asked %d times", r.daemon.loginChs)
	}
	// One source, a minute apart so that the bound of all is not met.
	r = newRig(t, func(c *Config) { c.ClientAddrHeader = "X-Client" })
	for i := 0; i < loginSourceRequests; i++ {
		if c := ask("192.0.2.1"); c != 200 {
			t.Fatalf("request %d: %d", i, c)
		}
		r.clock.Advance(time.Minute)
	}
	if c := ask("192.0.2.1"); c != 429 {
		t.Errorf("past the bound of one source: %d", c)
	}
	if c := ask("192.0.2.2"); c != 200 {
		t.Errorf("another source: %d", c)
	}
}

// TestSeatLoginsAreBounded: past the bound, the oldest login challenge goes,
// as the daemon's own oldest goes.
func TestSeatLoginsAreBounded(t *testing.T) {
	r := newRig(t)
	var cookies []*http.Cookie
	for i := 0; i <= maxLogins; i++ {
		cookies = append(cookies, r.startLogin(t))
		r.clock.Advance(10 * time.Second)
	}
	if w := r.postWith("/api/login", assertionBody(), cookies[0]); w.Code != 400 {
		t.Errorf("the oldest login challenge: %d", w.Code)
	}
	if w := r.postWith("/api/login", assertionBody(), cookies[maxLogins]); w.Code != 200 {
		t.Errorf("the newest login challenge: %d %s", w.Code, w.Body)
	}
}

// TestSeatLoginPage: the page asks for a passkey of the allow-list with user
// verification, and its script takes text only as text.
func TestSeatLoginPage(t *testing.T) {
	r := newRig(t)
	w := r.do("GET", "/login/", nil, nil)
	if w.Code != 200 {
		t.Fatalf("GET /login/: %d", w.Code)
	}
	checkHeadersGet(t, "login page", w, "(self)")
	page := w.Body.String()
	for _, want := range []string{`data-rp-id="seat.example"`, `<script src="/static/login.js"></script>`, `id="start"`, `role="status"`, "<noscript>"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	js := r.do("GET", "/static/login.js", nil, nil).Body.String()
	for _, want := range []string{
		"/api/login-challenge", "/api/login", "allowCredentials", `userVerification: "required"`, "timeout: 120000",
		`credentials: "same-origin"`, `referrerPolicy: "strict-origin"`, `referrer: ""`, "rpId: root.dataset.rpId",
		"credential_id", "client_data_json", "authenticator_data", "signature",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	for _, bad := range []string{"innerHTML", "outerHTML", "document.write", "eval(", "insertAdjacentHTML", "http://", "https://", "localStorage", "document.cookie", "no-referrer"} {
		if strings.Contains(js, bad) {
			t.Errorf("the script holds %q", bad)
		}
	}
	if strings.Count(js, "fetch(") != 1 {
		t.Errorf("the script holds %d fetch calls", strings.Count(js, "fetch("))
	}
}

// TestSeatLoginScriptKnowsTheCodes: every code of the login's API that the
// phone must tell apart has its own words in the script; any other code is
// the script's "failed".
func TestSeatLoginScriptKnowsTheCodes(t *testing.T) {
	js := newRig(t).do("GET", "/static/login.js", nil, nil).Body.String()
	codes := append([]apiError{errChallenge, errRateLimited, errDaemonUnavailable}, loginErrorCodes...)
	seen := map[string]bool{}
	for _, e := range append(codes, errorCodes...) {
		seen[e.code] = true
	}
	if len(seen) != len(codes)+len(errorCodes)-3 {
		t.Errorf("a login code is also a code of the pairing")
	}
	for _, e := range codes {
		if !strings.Contains(js, "    "+e.code+": [") {
			t.Errorf("the script has no words for %s", e.code)
		}
	}
}
