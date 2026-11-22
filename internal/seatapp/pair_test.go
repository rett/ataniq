package seatapp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// getChallenge asks for a challenge and returns it.
func (r *rig) getChallenge(t *testing.T) string {
	t.Helper()
	w := r.postJSON("/api/pair-challenge", "{}")
	if w.Code != 200 {
		t.Fatalf("challenge: %d %s", w.Code, w.Body)
	}
	var out struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Challenge == "" {
		t.Fatalf("challenge body %q", w.Body)
	}
	return out.Challenge
}

func (r *rig) postPair(body map[string]string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	return r.postJSON("/api/pair", string(b))
}

// wantError checks a response is the fixed answer for code.
func wantError(t *testing.T, what string, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status || w.Body.String() != `{"error":"`+code+`"}`+"\n" {
		t.Errorf("%s: %d %q, want %d %s", what, w.Code, w.Body.String(), status, code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s: content type %q", what, ct)
	}
	checkHeaders(t, what, w)
}

func TestChallenge(t *testing.T) {
	r := newRig(t)
	c := r.getChallenge(t)
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || len(raw) != 32 {
		t.Fatalf("challenge %q: %d bytes, %v", c, len(raw), err)
	}
	if c2 := r.getChallenge(t); c2 == c {
		t.Error("two challenges are equal")
	}
	// A challenge answer is JSON, never cached.
	w := r.postJSON("/api/pair-challenge", "{}")
	if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", w.Header())
	}
}

func TestChallengeRandomFailure(t *testing.T) {
	r := newRig(t, func(c *Config) { c.Random = bytes.NewReader([]byte("short")) })
	wantError(t, "short random", r.postJSON("/api/pair-challenge", "{}"), 500, "internal")
}

func TestChallengeCapAndExpiry(t *testing.T) {
	// Each request comes from its own source so that the per-source limit is
	// not the one that answers.
	r := newRig(t, func(c *Config) { c.ClientAddrHeader = "X-Client" })
	from := func(i int) *httptest.ResponseRecorder {
		return r.do("POST", "/api/pair-challenge", strings.NewReader("{}"), map[string]string{
			"Origin": testOrigin, "Content-Type": "application/json", "X-Client": fmt.Sprintf("192.0.2.%d", i),
		})
	}
	for i := 1; i <= maxChallenges; i++ {
		if w := from(i); w.Code != 200 {
			t.Fatalf("challenge %d: %d", i, w.Code)
		}
	}
	wantError(t, "the 33rd", from(100), 429, "rate_limited")
	// Expired ones make room.
	r.clock.Advance(challengeTTL + time.Second)
	if w := from(101); w.Code != 200 {
		t.Errorf("after expiry: %d", w.Code)
	}
}

func TestChallengePerSourceLimit(t *testing.T) {
	r := newRig(t)
	for i := 0; i < challengeRequests; i++ {
		if w := r.postJSON("/api/pair-challenge", "{}"); w.Code != 200 {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	wantError(t, "over the limit", r.postJSON("/api/pair-challenge", "{}"), 429, "rate_limited")
}

func TestPairHappyPath(t *testing.T) {
	r := newRig(t)
	a := newAuthenticator(t)
	ch := r.getChallenge(t)
	body := a.register(t, ch)
	w := r.postPair(body)
	if w.Code != 200 {
		t.Fatalf("pair: %d %s", w.Code, w.Body)
	}
	var out struct {
		Words []string `json:"words"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || strings.Join(out.Words, " ") != "amber birch cedar delta ember fjord garnet heron" {
		t.Errorf("words %q (%v)", w.Body, err)
	}
	checkHeaders(t, "pair", w)
	calls := r.daemon.requests()
	if len(calls) != 1 {
		t.Fatalf("daemon calls: %d", len(calls))
	}
	spki := spkiOf(t, &a.key.PublicKey)
	if calls[0].Code != "K7M2-Q9XA" || !bytes.Equal(calls[0].CredentialID, a.credID) || !bytes.Equal(calls[0].SPKI, spki) {
		t.Errorf("daemon request %+v", calls[0])
	}
	// The challenge is single use.
	wantError(t, "replay", r.postPair(body), 400, "challenge_invalid")
	if len(r.daemon.requests()) != 1 {
		t.Error("a replay reached the daemon")
	}
}

func TestPairAcceptsWhatBrowsersAdd(t *testing.T) {
	r := newRig(t)
	a := newAuthenticator(t)
	body := a.register(t, r.getChallenge(t), func(x *registration) {
		x.CrossOrigin = false
		x.Extra = map[string]any{"other_keys_can_be_added_here": "do not compare clientDataJSON against a template. See https://goo.gl/yabPex"}
		x.Flags = uvUPAT | 0x08 | 0x10 // backup eligible, backed up
	})
	if w := r.postPair(body); w.Code != 200 {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

func TestPairRefusals(t *testing.T) {
	a := newAuthenticator(t)
	other := newAuthenticator(t)
	short := func(n int) []byte { return bytes.Repeat([]byte{7}, n) }
	cases := []struct {
		name   string
		mod    func(*registration)
		status int
		code   string
	}{
		{"wrong type", func(x *registration) { x.Type = "webauthn.get" }, 400, "client_data_invalid"},
		{"no type", func(x *registration) { x.Type = "" }, 400, "client_data_invalid"},
		{"other challenge in client data", func(x *registration) { x.CDChallenge = base64.RawURLEncoding.EncodeToString(short(32)) }, 400, "client_data_invalid"},
		{"wrong origin in client data", func(x *registration) { x.Origin = "https://evil.example" }, 400, "client_data_invalid"},
		{"origin with a port", func(x *registration) { x.Origin = testOrigin + ":443" }, 400, "client_data_invalid"},
		{"cross origin", func(x *registration) { x.CrossOrigin = true }, 400, "client_data_invalid"},
		{"cross origin as a string", func(x *registration) { x.CrossOrigin = "false" }, 400, "client_data_invalid"},
		{"top origin", func(x *registration) { x.Extra = map[string]any{"topOrigin": testOrigin} }, 400, "client_data_invalid"},
		{"client data not JSON", func(x *registration) { x.ClientData = []byte("not json") }, 400, "client_data_invalid"},
		{"client data empty object", func(x *registration) { x.ClientData = []byte("{}") }, 400, "client_data_invalid"},
		{"client data null", func(x *registration) { x.ClientData = []byte("null") }, 400, "client_data_invalid"},
		{"client data an array", func(x *registration) { x.ClientData = []byte("[]") }, 400, "client_data_invalid"},
		{"client data with trailing data", func(x *registration) {
			x.ClientData = []byte(`{"type":"webauthn.create","challenge":"` + x.Challenge + `","origin":"` + testOrigin + `"} {}`)
		}, 400, "client_data_invalid"},
		{"client data with a numeric type", func(x *registration) {
			x.ClientData = []byte(`{"type":1,"challenge":"` + x.Challenge + `","origin":"` + testOrigin + `"}`)
		}, 400, "client_data_invalid"},
		{"client data with a null origin", func(x *registration) {
			x.ClientData = []byte(`{"type":"webauthn.create","challenge":"` + x.Challenge + `","origin":null}`)
		}, 400, "client_data_invalid"},
		{"client data with a differently cased key", func(x *registration) {
			x.ClientData = []byte(`{"Type":"webauthn.create","challenge":"` + x.Challenge + `","origin":"` + testOrigin + `"}`)
		}, 400, "client_data_invalid"},

		{"missing user verification", func(x *registration) { x.Flags = flagUP | flagAT }, 400, "user_verification_required"},
		{"missing user presence", func(x *registration) { x.Flags = flagUV | flagAT }, 400, "authenticator_data_invalid"},
		{"missing attested data", func(x *registration) { x.Flags = flagUP | flagUV }, 400, "authenticator_data_invalid"},
		{"no flags", func(x *registration) { x.Flags = 0 }, 400, "authenticator_data_invalid"},
		{"wrong rp id hash", func(x *registration) { x.RPID = "other.example" }, 400, "authenticator_data_invalid"},
		{"authenticator data too short", func(x *registration) { x.AuthData = short(36) }, 400, "authenticator_data_invalid"},
		{"authenticator data without credential data", func(x *registration) { x.AuthData = short(37) }, 400, "authenticator_data_invalid"},
		{"credential id longer than the data", func(x *registration) {
			x.AuthCredID = a.credID
			x.COSE = nil
		}, 400, "authenticator_data_invalid"},
		{"credential id in the data differs", func(x *registration) { x.AuthCredID = other.credID }, 400, "credential_invalid"},
		{"posted credential id differs", func(x *registration) { x.PostedCredID = other.credID }, 400, "credential_invalid"},
		{"credential id one byte short", func(x *registration) { x.PostedCredID = short(15); x.AuthCredID = short(15) }, 400, "credential_invalid"},
		{"credential id too long", func(x *registration) { x.PostedCredID = short(1025); x.AuthCredID = short(1025) }, 400, "credential_invalid"},
		{"credential id of 1024 bytes", func(x *registration) { x.PostedCredID = short(1024); x.AuthCredID = short(1024) }, 400, "credential_invalid"},
		{"backed up but not backup eligible", func(x *registration) { x.Flags = uvUPAT | 0x10 }, 400, "authenticator_data_invalid"},

		{"garbage key", func(x *registration) { x.SPKI = []byte("not a key") }, 400, "key_invalid"},
		{"key with trailing data", func(x *registration) { x.SPKI = append(append([]byte{}, x.SPKI...), 0) }, 400, "key_invalid"},
		{"truncated key", func(x *registration) { x.SPKI = x.SPKI[:len(x.SPKI)-1] }, 400, "key_invalid"},

		{"empty code", func(x *registration) { x.Code = "" }, 400, "bad_request"},
		{"code with a space", func(x *registration) { x.Code = "K7M2 Q9XA" }, 400, "bad_request"},
		{"code with a control character", func(x *registration) { x.Code = "K7M2\nQ9XA" }, 400, "bad_request"},
		{"code beyond ASCII", func(x *registration) { x.Code = "K7M2-Q9XÄ" }, 400, "bad_request"},
		{"long code", func(x *registration) { x.Code = strings.Repeat("A", 65) }, 400, "bad_request"},
		{"padded base64", func(x *registration) {
			x.Raw = map[string]string{"public_key": base64.URLEncoding.EncodeToString(x.SPKI) + "=="}
		}, 400, "bad_request"},
		{"standard alphabet base64", func(x *registration) { x.Raw = map[string]string{"client_data_json": "a+b/"} }, 400, "bad_request"},
		{"base64 with a line break", func(x *registration) { x.Raw = map[string]string{"authenticator_data": "AAAA\nAAAA"} }, 400, "bad_request"},
		{"empty field", func(x *registration) { x.Raw = map[string]string{"credential_id": ""} }, 400, "bad_request"},
		{"missing challenge", func(x *registration) { x.Raw = map[string]string{"challenge": ""} }, 400, "bad_request"},
	}
	for name, spki := range otherKeys(t) {
		spki := spki
		cases = append(cases, struct {
			name   string
			mod    func(*registration)
			status int
			code   string
		}{name + " key", func(x *registration) { x.SPKI = spki }, 400, "key_invalid"})
	}
	for _, tc := range cases {
		r := newRig(t)
		w := r.postPair(a.register(t, r.getChallenge(t), tc.mod))
		wantError(t, tc.name, w, tc.status, tc.code)
		if n := len(r.daemon.requests()); n != 0 {
			t.Errorf("%s: the daemon was called", tc.name)
		}
	}
}

func TestPairChallengeStates(t *testing.T) {
	a := newAuthenticator(t)
	t.Run("unknown", func(t *testing.T) {
		r := newRig(t)
		unknown := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
		wantError(t, "unknown", r.postPair(a.register(t, unknown)), 400, "challenge_invalid")
	})
	t.Run("expired", func(t *testing.T) {
		r := newRig(t)
		ch := r.getChallenge(t)
		r.clock.Advance(challengeTTL)
		wantError(t, "expired", r.postPair(a.register(t, ch)), 400, "challenge_invalid")
	})
	t.Run("just in time", func(t *testing.T) {
		r := newRig(t)
		ch := r.getChallenge(t)
		r.clock.Advance(challengeTTL - time.Second)
		if w := r.postPair(a.register(t, ch)); w.Code != 200 {
			t.Errorf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("burned by a failed try", func(t *testing.T) {
		r := newRig(t)
		ch := r.getChallenge(t)
		wantError(t, "bad", r.postPair(a.register(t, ch, func(x *registration) { x.Type = "webauthn.get" })), 400, "client_data_invalid")
		wantError(t, "retry", r.postPair(a.register(t, ch)), 400, "challenge_invalid")
	})
	t.Run("burned by a refused code", func(t *testing.T) {
		r := newRig(t)
		r.daemon.err = ErrCodeRefused
		ch := r.getChallenge(t)
		wantError(t, "refused", r.postPair(a.register(t, ch)), 403, "code_refused")
		r.daemon.err = nil
		wantError(t, "retry", r.postPair(a.register(t, ch)), 400, "challenge_invalid")
	})
}

// TestPairExpiryIsSampledWhenTheChallengeIsTaken: a request that starts inside
// the challenge's five minutes and is still being read when they end must not
// consume it.
func TestPairExpiryIsSampledWhenTheChallengeIsTaken(t *testing.T) {
	r := newRig(t)
	a := newAuthenticator(t)
	ch := r.getChallenge(t)
	r.clock.Advance(challengeTTL - time.Second)
	b, _ := json.Marshal(a.register(t, ch))
	body := &slowBody{r: bytes.NewReader(b), after: func() { r.clock.Advance(2 * time.Second) }}
	w := r.do("POST", "/api/pair", body, map[string]string{"Origin": testOrigin, "Content-Type": "application/json"})
	wantError(t, "read across the expiry", w, 400, "challenge_invalid")
	if len(r.daemon.requests()) != 0 {
		t.Error("the daemon was called")
	}
}

// slowBody is a request body whose reading takes time: after runs when the
// end is reached.
type slowBody struct {
	r     *bytes.Reader
	after func()
}

func (s *slowBody) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil {
		s.after()
	}
	return n, err
}

func TestPairDaemonErrors(t *testing.T) {
	a := newAuthenticator(t)
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"code refused", ErrCodeRefused, 403, "code_refused"},
		{"code refused, wrapped", fmt.Errorf("daemon said no: %w", ErrCodeRefused), 403, "code_refused"},
		{"unavailable", ErrDaemonUnavailable, 503, "daemon_unavailable"},
		{"unavailable, wrapped", fmt.Errorf("dial /run/private/path: %w", ErrDaemonUnavailable), 503, "daemon_unavailable"},
		{"anything else", errors.New("secret internal detail K7M2-Q9XA /home/someone"), 502, "daemon_error"},
	} {
		r := newRig(t)
		r.daemon.err = tc.err
		w := r.postPair(a.register(t, r.getChallenge(t)))
		wantError(t, tc.name, w, tc.status, tc.code)
		if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "K7M2") {
			t.Errorf("%s: the error text leaked: %q", tc.name, w.Body)
		}
	}
}

func TestPairBodies(t *testing.T) {
	r := newRig(t, func(c *Config) { c.ClientAddrHeader = "X-Client" })
	ch := r.getChallenge(t)
	n := 0
	post := func(body string) *httptest.ResponseRecorder {
		n++ // each try from its own source, so that the limit is not what answers
		return r.do("POST", "/api/pair", strings.NewReader(body), map[string]string{
			"Origin": testOrigin, "Content-Type": "application/json", "X-Client": fmt.Sprintf("192.0.2.%d", n),
		})
	}
	good := `{"code":"K7M2-Q9XA","challenge":"` + ch + `","credential_id":"AAAA","client_data_json":"AAAA","authenticator_data":"AAAA","public_key":"AAAA"}`
	for _, tc := range []struct {
		name, body string
		code       string
	}{
		{"empty body", ``, "bad_request"},
		{"garbage", `}{ not json`, "bad_request"},
		{"an array", `[]`, "bad_request"},
		{"null", `null`, "bad_request"},
		{"a string", `"x"`, "bad_request"},
		{"empty object", `{}`, "bad_request"},
		{"unknown field", strings.TrimSuffix(good, "}") + `,"extra":1}`, "bad_request"},
		{"a number for a string", `{"code":1}`, "bad_request"},
		{"trailing data", good + ` {}`, "bad_request"},
		{"two objects", good + good, "bad_request"},
		{"truncated", good[:len(good)-5], "bad_request"},
	} {
		wantError(t, tc.name, post(tc.body), 400, tc.code)
	}
	// None of the garbage burned the challenge it did not name.
	if got := post(good); got.Code != 400 || !strings.Contains(got.Body.String(), "credential_invalid") {
		t.Errorf("the good-looking body: %d %s", got.Code, got.Body)
	}
}

func TestPairOversizedBody(t *testing.T) {
	r := newRig(t)
	// Declared too long.
	w := r.postJSON("/api/pair", strings.Repeat(" ", MaxBody+1))
	if w.Code != 413 {
		t.Errorf("declared: %d", w.Code)
	}
	// Not declared (chunked): the read stops at the limit.
	for _, path := range []string{"/api/pair", "/api/pair-challenge"} {
		req := newRequest("POST", path, stringReader(strings.Repeat(" ", MaxBody+1)))
		req.ContentLength = -1
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		wantError(t, "undeclared "+path, w, 413, "too_large")
	}
	// Exactly the limit is read (and is garbage).
	req := newRequest("POST", "/api/pair", stringReader(`{"code":"`+strings.Repeat("a", MaxBody-20)+`"}`))
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	wantError(t, "at the limit", w, 400, "bad_request")
}

func TestPairWrongMethodAndType(t *testing.T) {
	r := newRig(t)
	for _, m := range []string{"GET", "PUT", "DELETE", "PATCH"} {
		if w := r.do(m, "/api/pair", nil, map[string]string{"Origin": testOrigin}); w.Code != 405 {
			t.Errorf("%s /api/pair: %d", m, w.Code)
		}
		if w := r.do(m, "/api/pair-challenge", nil, map[string]string{"Origin": testOrigin}); w.Code != 405 {
			t.Errorf("%s /api/pair-challenge: %d", m, w.Code)
		}
	}
	w := r.do("POST", "/api/pair", strings.NewReader("a=b"), map[string]string{"Origin": testOrigin, "Content-Type": "application/x-www-form-urlencoded"})
	if w.Code != 415 {
		t.Errorf("form post: %d", w.Code)
	}
}

func TestPairWrongOriginHeader(t *testing.T) {
	r := newRig(t)
	a := newAuthenticator(t)
	body, _ := json.Marshal(a.register(t, r.getChallenge(t)))
	for _, origin := range []string{"", "https://evil.example", "http://seat.example", testOrigin + ":443", "null"} {
		hdr := map[string]string{"Content-Type": "application/json"}
		if origin != "" {
			hdr["Origin"] = origin
		}
		w := r.do("POST", "/api/pair", bytes.NewReader(body), hdr)
		if w.Code != 403 || w.Body.String() != "forbidden\n" {
			t.Errorf("origin %q: %d %q", origin, w.Code, w.Body)
		}
	}
	if len(r.daemon.requests()) != 0 {
		t.Error("the daemon was called")
	}
	// The refused tries did not burn the challenge.
	if w := r.postJSON("/api/pair", string(body)); w.Code != 200 {
		t.Errorf("after the refusals: %d %s", w.Code, w.Body)
	}
}

func TestPairRateLimit(t *testing.T) {
	a := newAuthenticator(t)
	garbage := func(r *rig, hdr map[string]string) int {
		h := map[string]string{"Origin": testOrigin, "Content-Type": "application/json"}
		for k, v := range hdr {
			h[k] = v
		}
		return r.do("POST", "/api/pair", strings.NewReader("{"), h).Code
	}
	t.Run("sixth attempt", func(t *testing.T) {
		r := newRig(t)
		for i := 0; i < pairAttempts; i++ {
			if c := garbage(r, nil); c != 400 {
				t.Fatalf("attempt %d: %d", i+1, c)
			}
		}
		// A valid pairing is refused too.
		ch := r.getChallenge(t)
		wantError(t, "sixth", r.postPair(a.register(t, ch)), 429, "rate_limited")
		if len(r.daemon.requests()) != 0 {
			t.Error("the daemon was called")
		}
		r.clock.Advance(pairWindow - time.Minute)
		wantError(t, "still inside", r.postPair(a.register(t, ch)), 429, "rate_limited")
		r.clock.Advance(2 * time.Minute)
		ch = r.getChallenge(t)
		if w := r.postPair(a.register(t, ch)); w.Code != 200 {
			t.Errorf("after the window: %d %s", w.Code, w.Body)
		}
	})
	t.Run("another peer", func(t *testing.T) {
		r := newRig(t)
		for i := 0; i < pairAttempts+1; i++ {
			garbage(r, nil)
		}
		req := newRequest("POST", "/api/pair", strings.NewReader("{"))
		req.RemoteAddr = "198.51.100.7:4000"
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Errorf("another peer: %d", w.Code)
		}
	})
	t.Run("a forwarding header nobody configured is ignored", func(t *testing.T) {
		r := newRig(t)
		for i := 0; i < pairAttempts; i++ {
			garbage(r, map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.113.%d", i)})
		}
		if c := garbage(r, map[string]string{"X-Forwarded-For": "203.0.113.99"}); c != 429 {
			t.Errorf("a spoofed header made a new source: %d", c)
		}
	})
	t.Run("the configured header names the source", func(t *testing.T) {
		r := newRig(t, func(c *Config) { c.ClientAddrHeader = "X-Real-IP" })
		for i := 0; i < pairAttempts; i++ {
			garbage(r, map[string]string{"X-Real-IP": "203.0.113.1"})
		}
		if c := garbage(r, map[string]string{"X-Real-IP": "203.0.113.1"}); c != 429 {
			t.Errorf("sixth from one client: %d", c)
		}
		if c := garbage(r, map[string]string{"X-Real-IP": "203.0.113.2"}); c != 400 {
			t.Errorf("another client: %d", c)
		}
		// A header that does not parse falls back to the peer, whose bucket
		// is shared by everything that arrives without one.
		for i := 0; i < pairAttempts; i++ {
			garbage(r, map[string]string{"X-Real-IP": "not an address"})
		}
		if c := garbage(r, nil); c != 429 {
			t.Errorf("fallback bucket: %d", c)
		}
	})
	t.Run("an IPv6 client counts by its /64", func(t *testing.T) {
		r := newRig(t, func(c *Config) { c.ClientAddrHeader = "X-Real-IP" })
		for i := 0; i < pairAttempts; i++ {
			garbage(r, map[string]string{"X-Real-IP": fmt.Sprintf("2001:db8:1:2::%x", i+1)})
		}
		if c := garbage(r, map[string]string{"X-Real-IP": "2001:db8:1:2:ffff::1"}); c != 429 {
			t.Errorf("same /64: %d", c)
		}
	})
}

func TestLimiterBoundsItsTable(t *testing.T) {
	l := newLimiter(1, time.Hour)
	now := time.Unix(1e9, 0)
	for i := 0; i < maxSources; i++ {
		if !l.allow(fmt.Sprint(i), now) {
			t.Fatalf("source %d refused", i)
		}
	}
	if l.allow("new", now) {
		t.Error("a new source was allowed with the table full")
	}
	if l.allow("1", now) {
		t.Error("a known source went over its limit")
	}
	if !l.allow("new", now.Add(2*time.Hour)) {
		t.Error("expired entries were not dropped")
	}
}

func TestErrorCodesAreASmallFixedSet(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range errorCodes {
		if seen[e.code] || e.code == "" || strings.ToLower(e.code) != e.code {
			t.Errorf("code %q", e.code)
		}
		seen[e.code] = true
	}
	if len(errorCodes) > 16 {
		t.Errorf("%d codes", len(errorCodes))
	}
}
