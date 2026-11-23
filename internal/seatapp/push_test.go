package seatapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePush is a notifier that is also a PushPort: it keeps what it was asked.
type fakePush struct {
	mu   sync.Mutex
	key  string
	subs int
	// err, when set, is what every call of the port returns.
	err error

	calls        []string
	subscribed   []PushSubscription
	unsubscribed []string
	notified     []int
}

func (f *fakePush) Notify(_ context.Context, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notified = append(f.notified, n)
	return nil
}

func (f *fakePush) Info(context.Context) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "info")
	if f.err != nil {
		return "", 0, f.err
	}
	k := f.key
	if k == "" {
		k = testVAPIDKey
	}
	return k, f.subs, nil
}

func (f *fakePush) Subscribe(_ context.Context, s PushSubscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "subscribe")
	f.subscribed = append(f.subscribed, s)
	return f.err
}

func (f *fakePush) Unsubscribe(_ context.Context, h string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "unsubscribe")
	f.unsubscribed = append(f.unsubscribed, h)
	return f.err
}

func (f *fakePush) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakePush) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// withPush gives the rig's app f as its notifier, which is a PushPort.
func withPush(f *fakePush) func(*Config) {
	return func(c *Config) { c.Notifier = f }
}

// testVAPIDKey is a public key of the right form: 0x04 and 64 more bytes.
var testVAPIDKey = b64.EncodeToString(append([]byte{0x04}, make([]byte, 64)...))

const testEndpoint = "https://push.example/send/abcDEF123_-"

func testP256DH() string {
	b := make([]byte, 65)
	b[0] = 0x04
	for i := 1; i < len(b); i++ {
		b[i] = byte(i)
	}
	return b64.EncodeToString(b)
}

func testAuth() string { return b64.EncodeToString([]byte("0123456789abcdef")) }

func subBody(endpoint, p256dh, auth string) string {
	b, _ := json.Marshal(map[string]string{"endpoint": endpoint, "p256dh": p256dh, "auth": auth})
	return string(b)
}

func goodSubBody() string { return subBody(testEndpoint, testP256DH(), testAuth()) }

func endpointHash(e string) string {
	sum := sha256.Sum256([]byte(e))
	return hex.EncodeToString(sum[:])
}

func goodUnsubBody() string { return `{"endpoint_sha256":"` + endpointHash(testEndpoint) + `"}` }

// pushRoutes are the routes of the phone's side of the notices.
var (
	pushGETs  = []string{"/manifest.webmanifest", "/sw.js", "/static/push.js", "/static/icon-192.png", "/static/icon-512.png", "/static/apple-touch-icon.png"}
	pushPOSTs = []string{"/api/push/info", "/api/push/subscribe", "/api/push/unsubscribe"}
)

// pushRig is a rig whose app has a push port, and a logged-in session.
func pushRig(t *testing.T) (*rig, *fakePush, *http.Cookie) {
	t.Helper()
	f := &fakePush{subs: 2}
	r := newRig(t, withPush(f))
	return r, f, r.login(t)
}

// pushAnswer decodes a JSON answer into a map and checks its content type.
func pushAnswer(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("answer %q: %v", w.Body.String(), err)
	}
	return m
}

// wantFail checks an answer {"ok":false,"code":CODE} with a status, exactly.
func wantFail(t *testing.T, what string, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Errorf("%s: status %d, want %d (%s)", what, w.Code, status, w.Body.String())
		return
	}
	m := pushAnswer(t, w)
	if !reflect.DeepEqual(m, map[string]any{"ok": false, "code": code}) {
		t.Errorf("%s: answer %v, want code %s", what, m, code)
	}
}

// TestPushNoPortNoRoutes: without a notifier, and with one that is no
// PushPort, every route of the notices answers 404 (before any other check,
// so a request of any kind finds the same nothing), and the card list shows
// nothing about notices.
func TestPushNoPortNoRoutes(t *testing.T) {
	for name, mod := range map[string]func(*Config){
		"no notifier":         nil,
		"a notifier, no port": func(c *Config) { c.Notifier = &fakeNotifier{} },
	} {
		var mods []func(*Config)
		if mod != nil {
			mods = append(mods, mod)
		}
		r := newRig(t, mods...)
		s := r.login(t)
		for _, p := range pushGETs {
			for _, c := range [][]*http.Cookie{nil, {s}} {
				w := r.getWith(p, c...)
				if w.Code != 404 || w.Body.String() != "not found\n" {
					t.Errorf("%s: GET %s: %d %q", name, p, w.Code, w.Body)
				}
			}
		}
		for _, p := range pushPOSTs {
			for _, b := range []string{"{}", goodSubBody(), goodUnsubBody()} {
				w := r.postWith(p, b, s)
				if w.Code != 404 || w.Body.String() != "not found\n" {
					t.Errorf("%s: POST %s: %d %q", name, p, w.Code, w.Body)
				}
			}
			// Not even the origin check is answered first.
			w := r.do("POST", p, strings.NewReader("{}"), map[string]string{"Origin": "https://evil.example", "Content-Type": "text/plain"})
			if w.Code != 404 {
				t.Errorf("%s: POST %s from another origin: %d", name, p, w.Code)
			}
		}
		page := r.getWith("/cards/", s).Body.String()
		for _, no := range []string{"notices", "Notices", "manifest", "apple-touch-icon", "push.js", "enable-notices"} {
			if strings.Contains(page, no) {
				t.Errorf("%s: the card list holds %q", name, no)
			}
		}
	}
}

// TestPushCardListShowsTheNoticesControls: with a port the card list, and
// no other page, carries the notices' section, the script, and the links of
// the manifest and the Home Screen icon.
func TestPushCardListShowsTheNoticesControls(t *testing.T) {
	r, _, s := pushRig(t)
	page := r.getWith("/cards/", s).Body.String()
	for _, want := range []string{
		`<link rel="manifest" href="/manifest.webmanifest">`,
		`<link rel="apple-touch-icon" sizes="180x180" href="/static/apple-touch-icon.png">`,
		`<button id="enable-notices" type="button" hidden>Enable notices</button>`,
		`<button id="stop-notices" type="button" class="quiet" hidden>Stop notices</button>`,
		`<p id="notices-status" role="status" aria-live="polite"></p>`,
		`<script src="/static/push.js"></script>`,
		"Notices work only from the Home Screen app on iPhone. Add this page to your Home Screen, open it from there, then tap Enable.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the card list lacks %q", want)
		}
	}
	// The links are in the head, the section is not.
	head, _, _ := strings.Cut(page, "</head>")
	if !strings.Contains(head, "rel=\"manifest\"") || strings.Contains(head, "enable-notices") {
		t.Errorf("head: %q", head)
	}
	for _, p := range []string{"/", "/pair/", "/login/", "/cards/" + testCardID, "/cards/" + testCardID + "/paths/1"} {
		body := r.getWith(p, s).Body.String()
		for _, no := range []string{"push.js", "manifest", "apple-touch-icon", "enable-notices"} {
			if strings.Contains(body, no) {
				t.Errorf("%s holds %q", p, no)
			}
		}
	}
}

// TestPushInfoAnswersKeyAndCount: the answer is the notifier's key and
// count and nothing else.
func TestPushInfoAnswersKeyAndCount(t *testing.T) {
	r, f, s := pushRig(t)
	w := r.postWith("/api/push/info", "{}", s)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	want := map[string]any{"ok": true, "vapid_public": testVAPIDKey, "subscriptions": float64(2)}
	if got := pushAnswer(t, w); !reflect.DeepEqual(got, want) {
		t.Errorf("answer %v, want %v", got, want)
	}
	if got := f.called(); !reflect.DeepEqual(got, []string{"info"}) {
		t.Errorf("calls %v", got)
	}
	// A key or count the browser could not use is not passed on.
	for name, mod := range map[string]func(*fakePush){
		"short key":     func(f *fakePush) { f.key = testVAPIDKey[:20] },
		"not a point":   func(f *fakePush) { f.key = b64.EncodeToString(append([]byte{0x05}, make([]byte, 64)...)) },
		"padded key":    func(f *fakePush) { f.key = testVAPIDKey + "=" },
		"negative":      func(f *fakePush) { f.subs = -1 },
		"too many subs": func(f *fakePush) { f.subs = 1000 },
	} {
		f.mu.Lock()
		f.key, f.subs = "", 2
		mod(f)
		f.mu.Unlock()
		r.clock.Advance(time.Minute)
		wantFail(t, name, r.postWith("/api/push/info", "{}", s), 502, "notifier_error")
	}
}

// TestPushSubscribeRelaysTheSubscription: a good subscription reaches the
// notifier whole, and the answer holds no part of it.
func TestPushSubscribeRelaysTheSubscription(t *testing.T) {
	r, f, s := pushRig(t)
	w := r.postWith("/api/push/subscribe", goodSubBody(), s)
	if w.Code != 200 || w.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("answer %d %q", w.Code, w.Body)
	}
	want := []PushSubscription{{Endpoint: testEndpoint, P256DH: testP256DH(), Auth: testAuth()}}
	if !reflect.DeepEqual(f.subscribed, want) {
		t.Errorf("relayed %v, want %v", f.subscribed, want)
	}
}

// TestPushUnsubscribeRelaysTheHash.
func TestPushUnsubscribeRelaysTheHash(t *testing.T) {
	r, f, s := pushRig(t)
	w := r.postWith("/api/push/unsubscribe", goodUnsubBody(), s)
	if w.Code != 200 || w.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("answer %d %q", w.Code, w.Body)
	}
	if !reflect.DeepEqual(f.unsubscribed, []string{endpointHash(testEndpoint)}) {
		t.Errorf("relayed %v", f.unsubscribed)
	}
}

// TestPushRefusesWithoutASession: no cookie, a made-up cookie, and a session
// past its idle time, each 401 and nothing asked of the notifier.
func TestPushRefusesWithoutASession(t *testing.T) {
	r, f, s := pushRig(t)
	bodies := map[string]string{"/api/push/info": "{}", "/api/push/subscribe": goodSubBody(), "/api/push/unsubscribe": goodUnsubBody()}
	for _, p := range pushPOSTs {
		wantFail(t, p+" without a cookie", r.postWith(p, bodies[p]), 401, "no_session")
		wantFail(t, p+" with a made-up cookie", r.postWith(p, bodies[p], &http.Cookie{Name: SessionCookie, Value: strings.Repeat("A", tokenLen)}), 401, "no_session")
		wantFail(t, p+" with a short cookie", r.postWith(p, bodies[p], &http.Cookie{Name: SessionCookie, Value: "x"}), 401, "no_session")
	}
	r.clock.Advance(SessionIdle + time.Second)
	for _, p := range pushPOSTs {
		wantFail(t, p+" past the idle time", r.postWith(p, bodies[p], s), 401, "no_session")
	}
	if got := f.called(); len(got) != 0 {
		t.Errorf("the notifier was asked %v", got)
	}
}

// TestPushRefusesWhatPostRefuses: the checks of every changing request apply
// to the three calls.
func TestPushRefusesWhatPostRefuses(t *testing.T) {
	r, f, s := pushRig(t)
	bodies := map[string]string{"/api/push/info": "{}", "/api/push/subscribe": goodSubBody(), "/api/push/unsubscribe": goodUnsubBody()}
	cookie := s.Name + "=" + s.Value
	for _, p := range pushPOSTs {
		base := func() map[string]string {
			return map[string]string{"Origin": testOrigin, "Content-Type": "application/json", "Cookie": cookie}
		}
		for name, c := range map[string]struct {
			mod    func(map[string]string)
			status int
		}{
			"no origin":            {func(h map[string]string) { h["Origin"] = "" }, 403},
			"another origin":       {func(h map[string]string) { h["Origin"] = "https://evil.example" }, 403},
			"another host":         {func(h map[string]string) { h["Host"] = "evil.example" }, 403},
			"no fetch metadata":    {func(h map[string]string) { h["Sec-Fetch-Site"] = "" }, 403},
			"cross-site":           {func(h map[string]string) { h["Sec-Fetch-Site"] = "cross-site" }, 403},
			"same-site":            {func(h map[string]string) { h["Sec-Fetch-Site"] = "same-site" }, 403},
			"form content type":    {func(h map[string]string) { h["Content-Type"] = "application/x-www-form-urlencoded" }, 415},
			"plain content type":   {func(h map[string]string) { h["Content-Type"] = "text/plain" }, 415},
			"no content type":      {func(h map[string]string) { h["Content-Type"] = "" }, 415},
			"a good request again": {func(h map[string]string) {}, 200},
		} {
			h := base()
			c.mod(h)
			w := r.do("POST", p, strings.NewReader(bodies[p]), h)
			if w.Code != c.status {
				t.Errorf("%s, %s: status %d, want %d", p, name, w.Code, c.status)
			}
			if c.status != 200 && strings.Contains(w.Body.String(), "ok") {
				t.Errorf("%s, %s: %q", p, name, w.Body)
			}
		}
		// A GET of an API route is not a route.
		if w := r.getWith(p, s); w.Code != 405 && w.Code != 404 {
			t.Errorf("GET %s: %d", p, w.Code)
		}
	}
	if got := f.called(); len(got) != 3 {
		t.Errorf("the notifier was asked %v, want the three good requests only", got)
	}
}

// TestPushBadBodies: a body that is not one object of exactly the call's
// fields, with the values of the right form, is a bad request, and nothing
// is asked of the notifier or counted against the limit.
func TestPushBadBodies(t *testing.T) {
	r, f, s := pushRig(t)
	long := "https://push.example/" + strings.Repeat("a", maxPushEndpoint)
	atCap := "https://push.example/" + strings.Repeat("a", maxPushEndpoint-len("https://push.example/"))
	key65 := func(first byte) string { return b64.EncodeToString(append([]byte{first}, make([]byte, 64)...)) }
	cases := map[string]map[string]string{
		"/api/push/info": {
			"empty":          "",
			"cut":            "{",
			"an array":       "[]",
			"null":           "null",
			"a string":       `"x"`,
			"a number":       "1",
			"unknown member": `{"x":1}`,
			"two objects":    "{}{}",
			"trailing":       "{} x",
			"not JSON":       "x=1",
		},
		"/api/push/subscribe": {
			"empty object":       "{}",
			"null":               "null",
			"an array":           "[]",
			"no auth":            `{"endpoint":"` + testEndpoint + `","p256dh":"` + testP256DH() + `"}`,
			"extra member":       `{"endpoint":"` + testEndpoint + `","p256dh":"` + testP256DH() + `","auth":"` + testAuth() + `","x":1}`,
			"endpoint number":    `{"endpoint":5,"p256dh":"` + testP256DH() + `","auth":"` + testAuth() + `"}`,
			"auth null":          `{"endpoint":"` + testEndpoint + `","p256dh":"` + testP256DH() + `","auth":null}`,
			"trailing":           goodSubBody() + " {}",
			"http endpoint":      subBody("http://push.example/x", testP256DH(), testAuth()),
			"no scheme":          subBody("push.example/x", testP256DH(), testAuth()),
			"no host":            subBody("https:///x", testP256DH(), testAuth()),
			"user information":   subBody("https://u:p@push.example/x", testP256DH(), testAuth()),
			"a fragment":         subBody(testEndpoint+"#x", testP256DH(), testAuth()),
			"a space":            subBody("https://push.example/a b", testP256DH(), testAuth()),
			"a control byte":     subBody("https://push.example/a\u0001b", testP256DH(), testAuth()),
			"non-ASCII":          subBody("https://push.example/é", testP256DH(), testAuth()),
			"endpoint too long":  subBody(long, testP256DH(), testAuth()),
			"no endpoint":        subBody("", testP256DH(), testAuth()),
			"short p256dh":       subBody(testEndpoint, testP256DH()[:40], testAuth()),
			"long p256dh":        subBody(testEndpoint, testP256DH()+"AA", testAuth()),
			"padded p256dh":      subBody(testEndpoint, testP256DH()+"=", testAuth()),
			"p256dh not a point": subBody(testEndpoint, key65(0x05), testAuth()),
			"p256dh standard":    subBody(testEndpoint, "+"+testP256DH()[1:], testAuth()),
			"short auth":         subBody(testEndpoint, testP256DH(), testAuth()[:10]),
			"long auth":          subBody(testEndpoint, testP256DH(), testAuth()+"A"),
			"auth not base64":    subBody(testEndpoint, testP256DH(), "!!!!!!!!!!!!!!!!!!!!!!"),
			"auth spare bits":    subBody(testEndpoint, testP256DH(), "MDEyMzQ1Njc4OWFiY2RlZh"),
		},
		"/api/push/unsubscribe": {
			"empty object": "{}",
			"null":         "null",
			"no hash":      `{"endpoint_sha256":""}`,
			"short":        `{"endpoint_sha256":"` + endpointHash(testEndpoint)[:63] + `"}`,
			"long":         `{"endpoint_sha256":"` + endpointHash(testEndpoint) + `0"}`,
			"upper case":   `{"endpoint_sha256":"` + strings.ToUpper(endpointHash(testEndpoint)) + `"}`,
			"not hex":      `{"endpoint_sha256":"` + strings.Repeat("g", 64) + `"}`,
			"a number":     `{"endpoint_sha256":5}`,
			"extra member": `{"endpoint_sha256":"` + endpointHash(testEndpoint) + `","x":1}`,
			"the endpoint": `{"endpoint":"` + testEndpoint + `"}`,
		},
	}
	// The one at the cap is good.
	if !ValidPushSubscription(PushSubscription{Endpoint: atCap, P256DH: testP256DH(), Auth: testAuth()}) || len(atCap) != maxPushEndpoint {
		t.Fatalf("an endpoint of %d bytes is not good", len(atCap))
	}
	for p, set := range cases {
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			wantFail(t, p+": "+n, r.postWith(p, set[n], s), 400, "bad_request")
		}
	}
	if got := f.called(); len(got) != 0 {
		t.Errorf("the notifier was asked %v", got)
	}
	// None of those counted: the whole limit is still there.
	for i := 0; i < pushRequests; i++ {
		if w := r.postWith("/api/push/info", "{}", s); w.Code != 200 {
			t.Fatalf("call %d after the bad ones: %d", i, w.Code)
		}
	}
}

// TestPushOverCapBody: a body past MaxBody is 413, whether its length is
// declared (the guard) or not (the read).
func TestPushOverCapBody(t *testing.T) {
	r, f, s := pushRig(t)
	big := `{"endpoint":"` + strings.Repeat("a", MaxBody) + `"}`
	w := r.postWith("/api/push/subscribe", big, s)
	if w.Code != 413 {
		t.Errorf("declared: %d", w.Code)
	}
	req := newRequest("POST", "/api/push/subscribe", io.NopCloser(strings.NewReader(big)))
	req.ContentLength = -1
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s)
	w = httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	wantFail(t, "undeclared", w, 413, "too_large")
	if got := f.called(); len(got) != 0 {
		t.Errorf("the notifier was asked %v", got)
	}
}

// TestPushMapsTheNotifiersFailures: each code the notifier gives is a fixed
// answer, an unknown code is one more, and nothing of the error's text or of
// the request reaches the browser.
func TestPushMapsTheNotifiersFailures(t *testing.T) {
	r, f, s := pushRig(t)
	bodies := map[string]string{"/api/push/info": "{}", "/api/push/subscribe": goodSubBody(), "/api/push/unsubscribe": goodUnsubBody()}
	for _, c := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"bad subscription", &PushError{Code: "bad_subscription"}, 400, "bad_subscription"},
		{"too many", &PushError{Code: "too_many"}, 409, "too_many"},
		{"endpoint refused", &PushError{Code: "endpoint_refused"}, 422, "endpoint_refused"},
		{"unavailable", &PushError{Code: "unavailable"}, 503, "unavailable"},
		{"an unknown code", &PushError{Code: "weird_code"}, 502, "notifier_error"},
		{"a wrapped refusal", fmt.Errorf("call: %w", &PushError{Code: "too_many"}), 409, "too_many"},
		{"not reachable", errors.New("dial unix /run/notify/n.sock: " + testEndpoint), 503, "unavailable"},
		{"too slow", context.DeadlineExceeded, 503, "unavailable"},
	} {
		f.setErr(c.err)
		for _, p := range pushPOSTs {
			r.clock.Advance(time.Minute)
			w := r.postWith(p, bodies[p], s)
			wantFail(t, c.name+" "+p, w, c.status, c.code)
			for _, leak := range []string{"weird_code", "dial", "/run/notify", "push.example", testP256DH(), testAuth(), endpointHash(testEndpoint)} {
				if strings.Contains(w.Body.String(), leak) {
					t.Errorf("%s %s: the answer holds %q", c.name, p, leak)
				}
			}
		}
	}
}

// TestPushLimit: six calls a minute for a credential, the three routes
// together, over any of its sessions, and the minute passing lifts it.
func TestPushLimit(t *testing.T) {
	r, f, s := pushRig(t)
	s2 := r.login(t)
	bodies := map[string]string{"/api/push/info": "{}", "/api/push/subscribe": goodSubBody(), "/api/push/unsubscribe": goodUnsubBody()}
	order := []string{"/api/push/info", "/api/push/subscribe", "/api/push/unsubscribe", "/api/push/info", "/api/push/subscribe", "/api/push/unsubscribe"}
	for i, p := range order {
		c := s
		if i%2 == 1 {
			c = s2
		}
		if w := r.postWith(p, bodies[p], c); w.Code != 200 {
			t.Fatalf("call %d: %d %s", i, w.Code, w.Body)
		}
		r.clock.Advance(time.Second)
	}
	for _, p := range pushPOSTs {
		wantFail(t, "the seventh "+p, r.postWith(p, bodies[p], s), 429, "rate_limited")
		wantFail(t, "the seventh "+p+" of the other session", r.postWith(p, bodies[p], s2), 429, "rate_limited")
	}
	if n := len(f.called()); n != 6 {
		t.Errorf("the notifier was asked %d times, want 6", n)
	}
	r.clock.Advance(time.Minute)
	if w := r.postWith("/api/push/info", "{}", s); w.Code != 200 {
		t.Errorf("after the minute: %d", w.Code)
	}
}

// TestPushCallIsATapOfTheSession: a call counts as use of the session (the
// owner tapped), so it keeps the session from its idle end.
func TestPushCallIsATapOfTheSession(t *testing.T) {
	r, _, s := pushRig(t)
	for i := 0; i < 3; i++ {
		r.clock.Advance(SessionIdle - time.Minute)
		if w := r.postWith("/api/push/info", "{}", s); w.Code != 200 {
			t.Fatalf("call %d: %d", i, w.Code)
		}
	}
}

// TestPushNeverCalledWithAContextThatOutlivesTheRequest: the call to the
// notifier is bounded.
func TestPushCallIsBounded(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	f := &blockingPush{block: block, entered: make(chan struct{})}
	r := newRig(t, func(c *Config) { c.Notifier = f })
	s := r.login(t)
	if pushCall > 10*time.Second {
		t.Fatalf("pushCall is %v", pushCall)
	}
	req := newRequest("POST", "/api/push/info", strings.NewReader("{}"))
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		done <- w
	}()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the notifier was not called")
	}
	cancel()
	select {
	case w := <-done:
		wantFail(t, "a request that went away", w, 503, "unavailable")
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not end with its request")
	}
}

// blockingPush waits in Info until its context ends.
type blockingPush struct {
	fakePush
	block   chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (b *blockingPush) Info(ctx context.Context) (string, int, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-ctx.Done():
		return "", 0, ctx.Err()
	case <-b.block:
		return testVAPIDKey, 0, nil
	}
}

// pngOf fetches an icon and decodes it.
func pngOf(t *testing.T, r *rig, path string) (image.Config, image.Image, []byte) {
	t.Helper()
	w := r.do("GET", path, nil, nil)
	if w.Code != 200 {
		t.Fatalf("%s: status %d", path, w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("%s: content type %q", path, ct)
	}
	body := w.Body.Bytes()
	cfg, format, err := image.DecodeConfig(strings.NewReader(string(body)))
	if err != nil || format != "png" {
		t.Fatalf("%s: not a PNG: %v %q", path, err, format)
	}
	img, err := png.Decode(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return cfg, img, body
}

// TestPushIconsAreValidPNGsOfTheirSize: the two icons of the manifest and the
// Home Screen icon are small, opaque, solid PNGs of the sizes they state.
func TestPushIconsAreValidPNGsOfTheirSize(t *testing.T) {
	r, _, _ := pushRig(t)
	for path, size := range map[string]int{"/static/icon-192.png": 192, "/static/icon-512.png": 512, "/static/apple-touch-icon.png": 180} {
		cfg, img, body := pngOf(t, r, path)
		if cfg.Width != size || cfg.Height != size {
			t.Errorf("%s: %dx%d, want %dx%d", path, cfg.Width, cfg.Height, size, size)
		}
		if len(body) > 2048 {
			t.Errorf("%s: %d bytes", path, len(body))
		}
		// Opaque and of one colour: the Home Screen fills a transparent
		// icon with black.
		first := img.At(0, 0)
		for _, pt := range [][2]int{{0, 0}, {size - 1, 0}, {0, size - 1}, {size - 1, size - 1}, {size / 2, size / 2}} {
			c := img.At(pt[0], pt[1])
			if c != first {
				t.Errorf("%s: pixel %v is %v, not %v", path, pt, c, first)
			}
			if _, _, _, a := c.RGBA(); a != 0xffff {
				t.Errorf("%s: pixel %v is not opaque", path, pt)
			}
		}
	}
}

// TestPushManifest: the manifest is JSON of the members the Home Screen app
// needs, its colours are the style sheet's, and its icons are routes that
// exist, with the sizes it says.
func TestPushManifest(t *testing.T) {
	r, _, _ := pushRig(t)
	w := r.do("GET", "/manifest.webmanifest", nil, nil)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	checkHeaders(t, "manifest", w)
	if ct := w.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("content type %q", ct)
	}
	var m struct {
		Name            string `json:"name"`
		ShortName       string `json:"short_name"`
		StartURL        string `json:"start_url"`
		Scope           string `json:"scope"`
		Display         string `json:"display"`
		BackgroundColor string `json:"background_color"`
		ThemeColor      string `json:"theme_color"`
		Icons           []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
			Type  string `json:"type"`
		} `json:"icons"`
	}
	dec := json.NewDecoder(strings.NewReader(w.Body.String()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Name != "Authority seat" || m.ShortName != "Seat" || m.StartURL != "/cards/" || m.Scope != "/" || m.Display != "standalone" {
		t.Errorf("manifest %+v", m)
	}
	// The colours are the style sheet's dark ground (the theme the Home
	// Screen app opens in).
	css := r.do("GET", "/static/app.css", nil, nil).Body.String()
	for _, c := range []string{m.BackgroundColor, m.ThemeColor} {
		if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(c) || !strings.Contains(css, "--bg: "+c+";") {
			t.Errorf("colour %q is not a ground of app.css", c)
		}
	}
	if len(m.Icons) != 2 {
		t.Fatalf("icons %v", m.Icons)
	}
	sizes := map[string]bool{}
	for _, ic := range m.Icons {
		cfg, _, _ := pngOf(t, r, ic.Src)
		if want := fmt.Sprintf("%dx%d", cfg.Width, cfg.Height); ic.Sizes != want || ic.Type != "image/png" {
			t.Errorf("icon %+v: the file is %s", ic, want)
		}
		sizes[ic.Sizes] = true
	}
	if !sizes["192x192"] || !sizes["512x512"] {
		t.Errorf("icon sizes %v", sizes)
	}
}

// TestPushServiceWorkerHeaders: the script is JavaScript, "no-cache" so the
// browser checks it for a change, with its scope the whole origin, and every
// other header of the app.
func TestPushServiceWorkerHeaders(t *testing.T) {
	r, _, _ := pushRig(t)
	w := r.do("GET", "/sw.js", nil, nil)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/javascript" {
		t.Errorf("content type %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("cache control %q", got)
	}
	if got := w.Header().Get("Service-Worker-Allowed"); got != "/" {
		t.Errorf("Service-Worker-Allowed %q", got)
	}
	checkHeadersWith(t, "sw.js", w, "()", map[string]string{"Cache-Control": "no-cache"})
	// Only the service worker has the header or the cache rule.
	for _, p := range []string{"/static/push.js", "/static/watch.js", "/manifest.webmanifest", "/static/app.css", "/healthz"} {
		w := r.do("GET", p, nil, nil)
		if w.Header().Get("Service-Worker-Allowed") != "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %v", p, w.Header())
		}
	}
}

// TestSeatCSPOpensManifestAndWorkerOnly: the policy has the two directives of
// the notices, both 'self', and its other directives are as they were: closed
// by default, no inline, Trusted Types required, and a trusted-types
// directive that names the one policy push.js makes, seat-sw, and nothing
// else, with no allow-duplicates.
func TestSeatCSPOpensManifestAndWorkerOnly(t *testing.T) {
	r := newRig(t)
	csp := r.do("GET", "/healthz", nil, nil).Header().Get("Content-Security-Policy")
	got := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		name, val, _ := strings.Cut(strings.TrimSpace(d), " ")
		got[name] = val
	}
	want := map[string]string{
		"default-src": "'none'", "script-src": "'self'", "style-src": "'self'", "connect-src": "'self'", "img-src": "'self'",
		"manifest-src": "'self'", "worker-src": "'self'", "base-uri": "'none'", "form-action": "'self'",
		"frame-ancestors": "'none'", "require-trusted-types-for": "'script'", "trusted-types": "seat-sw",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CSP directives %v, want %v", got, want)
	}
	if strings.Contains(csp, "unsafe") || strings.Contains(csp, "allow-duplicates") || strings.Contains(csp, "'none'; trusted") || strings.Contains(csp, "data:") || strings.Contains(csp, "blob:") {
		t.Errorf("CSP %q", csp)
	}
}

// swText is the service worker's script.
func swText(t *testing.T) string {
	t.Helper()
	w := newRig(t, withPush(&fakePush{})).do("GET", "/sw.js", nil, nil)
	if w.Code != 200 {
		t.Fatalf("sw.js: %d", w.Code)
	}
	return w.Body.String()
}

// codeOnly drops the lines that are only a comment, which hold no code.
func codeOnly(js string) string {
	var out []string
	for _, l := range strings.Split(js, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "//") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// TestSeatServiceWorkerHoldsNothingItShouldNot: no request handler, no
// store, no other script, no buttons, no data, and no address but the card
// list's.
func TestSeatServiceWorkerHoldsNothingItShouldNot(t *testing.T) {
	js := swText(t)
	code := codeOnly(js)
	for _, bad := range []string{
		"fetch", "caches", "cache", "importScripts", "actions", "action", "skipWaiting", "claim", "localStorage", "sessionStorage",
		"indexedDB", "XMLHttpRequest", "WebSocket", "sendBeacon", "eval(", "Function(", "innerHTML", "postMessage", "BroadcastChannel",
		"sync", "url", "href", "icon", "badge", "image", "vibrate", "silent", "requireInteraction", "timestamp", "dir", "lang",
	} {
		if strings.Contains(strings.ToLower(js), strings.ToLower(bad)) {
			t.Errorf("sw.js holds %q", bad)
		}
	}
	if strings.Contains(code, "https:") || strings.Contains(code, "http:") || externalRe.MatchString(js) {
		t.Error("sw.js holds an external address")
	}
	// The listeners are the two, and no other.
	var events []string
	for _, m := range regexp.MustCompile(`addEventListener\(\s*"([^"]*)"`).FindAllStringSubmatch(code, -1) {
		events = append(events, m[1])
	}
	if !reflect.DeepEqual(events, []string{"push", "notificationclick"}) {
		t.Errorf("sw.js listens to %v", events)
	}
	if regexp.MustCompile(`\bonfetch\b|\bself\.on[a-z]+\s*=`).MatchString(code) {
		t.Error("sw.js sets a handler by property")
	}
	// Every string of the script is one of these: the fixed texts, the card
	// list's path, the tag, the two events, and the words of its checks.
	allowed := map[string]bool{
		"use strict": true, "Cards are waiting": true, " waiting": true, "/cards/": true, "waiting": true,
		"object": true, "n": true, "number": true, "push": true, "notificationclick": true, "window": true,
	}
	for _, m := range regexp.MustCompile(`"([^"\\]*)"`).FindAllStringSubmatch(code, -1) {
		if !allowed[m[1]] {
			t.Errorf("sw.js holds the string %q", m[1])
		}
	}
	// A script that is not a decide script names no word of deciding.
	if mentionsDeciding(js) {
		t.Error("sw.js mentions deciding")
	}
}

// TestSeatServiceWorkerReadsOnlyN: the one member of the payload it reads is
// n, which must be the payload's only member and an integer 0 to 999, and any
// other payload shows the plain text.
func TestSeatServiceWorkerReadsOnlyN(t *testing.T) {
	js := codeOnly(swText(t))
	for _, m := range regexp.MustCompile(`\bbody\.([A-Za-z_]+)`).FindAllStringSubmatch(js, -1) {
		if m[1] != "n" {
			t.Errorf("sw.js reads body.%s", m[1])
		}
	}
	// The payload comes from event.data.json() and from nothing else of the
	// event, and it is parsed inside a try.
	for _, m := range regexp.MustCompile(`\bevent\.([A-Za-z_.]+)`).FindAllStringSubmatch(js, -1) {
		switch m[1] {
		case "data.json", "waitUntil", "notification.close":
		default:
			t.Errorf("sw.js reads event.%s", m[1])
		}
	}
	for _, want := range []string{
		"try {", "event.data.json()", "typeof body === \"object\"", "Array.isArray(body)", "names.length === 1", "names[0] === \"n\"",
		"Number.isInteger(n)", "n >= 0", "n <= MAX_N", "var MAX_N = 999;", "catch (e) {\n    return PLAIN;",
		"var PLAIN = \"Cards are waiting\";", "var SUFFIX = \" waiting\";", "String(n) + SUFFIX",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("sw.js lacks %q", want)
		}
	}
	// The one notification: a fixed tag, no renotify, and the options are
	// exactly that.
	if !strings.Contains(js, "showNotification(titleOf(event), { tag: TAG, renotify: false })") || !strings.Contains(js, `var TAG = "waiting";`) {
		t.Error("sw.js shows something other than the fixed notification")
	}
	if n := strings.Count(js, "showNotification("); n != 1 {
		t.Errorf("sw.js shows a notification in %d places", n)
	}
}

// TestSeatServiceWorkerTapOpensOnlyTheCardList: a tap closes the
// notification and focuses a window of the origin and takes it to /cards/, or
// opens /cards/; nothing of the notification is used.
func TestSeatServiceWorkerTapOpensOnlyTheCardList(t *testing.T) {
	js := codeOnly(swText(t))
	_, click, ok := strings.Cut(js, `addEventListener("notificationclick"`)
	if !ok {
		t.Fatal("no notificationclick handler")
	}
	for _, want := range []string{"event.notification.close();", "event.waitUntil(openCards());"} {
		if !strings.Contains(click, want) {
			t.Errorf("the click handler lacks %q", want)
		}
	}
	// The handler reads nothing of the notification but close.
	if strings.Contains(click, "event.notification.data") || strings.Contains(click, "event.notification.tag") ||
		strings.Contains(click, "event.action") || strings.Contains(click, "notification.title") {
		t.Error("the click handler reads the notification")
	}
	if !strings.Contains(js, `var CARDS_PATH = "/cards/";`) {
		t.Error("the card list's path is not the one constant")
	}
	// Every window call takes CARDS_PATH.
	for _, call := range []string{"openWindow(", "navigate("} {
		if n := strings.Count(js, call); n == 0 {
			t.Errorf("sw.js never calls %s", call)
		}
		for _, m := range regexp.MustCompile(regexp.QuoteMeta(call)+`([^)]*)\)`).FindAllStringSubmatch(js, -1) {
			if m[1] != "CARDS_PATH" {
				t.Errorf("sw.js calls %s%s)", call, m[1])
			}
		}
	}
	for _, want := range []string{`matchAll({ type: "window", includeUncontrolled: true })`, ".focus()"} {
		if !strings.Contains(js, want) {
			t.Errorf("sw.js lacks %q", want)
		}
	}
	// A tap works before the worker controls any page: the windows asked for
	// include the uncontrolled ones, the worker does not claim the page or
	// handle requests to control it, and a window is navigated before it is
	// focused (a worker cannot navigate an uncontrolled window, and the
	// refusal opens the list in a window of its own, which focus() must not
	// have used the tap's permission for first).
	if n := strings.Count(js, "matchAll("); n != 1 {
		t.Errorf("sw.js asks for windows in %d places", n)
	}
	if strings.Contains(js, "clients.claim") || strings.Contains(js, `"fetch"`) {
		t.Error("sw.js claims clients or handles fetches")
	}
	nav, foc := strings.Index(js, ".navigate(CARDS_PATH)"), strings.Index(js, ".focus()")
	if nav < 0 || foc < 0 || nav > foc {
		t.Errorf("sw.js focuses before it navigates (navigate at %d, focus at %d)", nav, foc)
	}
	if !strings.Contains(js, "}).catch(function () {\n    return self.clients.openWindow(CARDS_PATH);") {
		t.Error("a window that cannot be navigated does not fall back to opening the list")
	}
	if strings.Contains(click, "event.data") {
		t.Error("the click handler reads event data")
	}
}

// pushScript is the page script of the notices.
func pushScript(t *testing.T) string {
	t.Helper()
	w := newRig(t, withPush(&fakePush{})).do("GET", "/static/push.js", nil, nil)
	if w.Code != 200 {
		t.Fatalf("push.js: %d", w.Code)
	}
	return w.Body.String()
}

// clickHandlerRanges returns the spans of the bodies of the functions given to
// addEventListener("click", ...) in js, whole-line comments taken out first.
func clickHandlerRanges(js string) [][2]int {
	var out [][2]int
	for from := 0; ; {
		i := strings.Index(js[from:], `addEventListener("click"`)
		if i < 0 {
			return out
		}
		i += from
		from = i + 1
		open := strings.Index(js[i:], "{")
		if open < 0 {
			continue
		}
		open += i
		depth := 0
		for j := open; j < len(js); j++ {
			switch js[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					out = append(out, [2]int{open, j})
					j = len(js)
				}
			}
		}
	}
}

// outsideClickHandlers returns the offsets of word in js that are not inside
// the body of a function given to addEventListener("click", ...).
func outsideClickHandlers(js, word string) []int {
	ranges := clickHandlerRanges(js)
	var bad []int
	for from := 0; ; {
		i := strings.Index(js[from:], word)
		if i < 0 {
			return bad
		}
		i += from
		from = i + len(word)
		in := false
		for _, rg := range ranges {
			in = in || (i > rg[0] && i < rg[1])
		}
		if !in {
			bad = append(bad, i)
		}
	}
}

// TestPushCheckSeesAPermissionOutsideAClick: the check that the permission is
// asked only in a click's handler fails for a script that asks it anywhere
// else.
func TestPushCheckSeesAPermissionOutsideAClick(t *testing.T) {
	good := `a.addEventListener("click", async function () { var p = await Notification.requestPermission(); if (p) { x(); } });`
	if got := outsideClickHandlers(good, "requestPermission"); len(got) != 0 {
		t.Errorf("a click handler: %v", got)
	}
	for name, js := range map[string]string{
		"top level":     `Notification.requestPermission();`,
		"a function":    `function ask() { return Notification.requestPermission(); } a.addEventListener("click", ask);`,
		"another event": `a.addEventListener("load", function () { Notification.requestPermission(); });`,
		"after the handler": `a.addEventListener("click", function () { x(); });
Notification.requestPermission();`,
		"a timer in no click": `setTimeout(function () { Notification.requestPermission(); }, 1);`,
	} {
		if got := outsideClickHandlers(js, "requestPermission"); len(got) == 0 {
			t.Errorf("%s: not seen", name)
		}
	}
}

// TestSeatPushScriptIsSafe: no markup is made from text, nothing is
// evaluated, every text goes through textContent, the one fetch is the app's
// own request, and the script holds no word of deciding.
func TestSeatPushScriptIsSafe(t *testing.T) {
	js := pushScript(t)
	code := codeOnly(js)
	for _, bad := range []string{
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "Function(", "new Function",
		"setAttribute(", "createContextualFragment", "DOMParser", "srcdoc", "javascript:", "importScripts",
		"XMLHttpRequest", "WebSocket", "EventSource", "sendBeacon", "localStorage", "sessionStorage", "document.cookie",
		"setInterval", "createElement(", "innerText", "location.", "window.open", "postMessage", "showNotification",
		".src =", ".href =", ".action =",
	} {
		if strings.Contains(code, bad) {
			t.Errorf("push.js holds %q", bad)
		}
	}
	if regexp.MustCompile(`setTimeout\(\s*["']`).MatchString(code) {
		t.Error("push.js hands a string to a timer")
	}
	for _, want := range []string{
		"textContent", `"use strict"`,
		`credentials: "same-origin"`, `redirect: "error"`, `cache: "no-store"`, `referrerPolicy: "strict-origin"`,
		`method: "POST"`, `"Content-Type": "application/json"`, "JSON.stringify", "AbortController", "signal: ctl.signal",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("push.js lacks %q", want)
		}
	}
	if externalRe.MatchString(js) {
		t.Error("push.js holds an external address")
	}
	if mentionsDeciding(js) {
		t.Error("push.js mentions deciding")
	}
	// textContent is the one way it puts text on the page, in one function.
	if n := strings.Count(code, ".textContent ="); n != 1 || !strings.Contains(code, "function say(text) { statusEl.textContent = text; }") {
		t.Errorf("push.js sets textContent %d times", n)
	}
}

// TestSeatPushScriptReferencesOnlyItsOwnAddresses: it posts only to the three
// routes, registers only the service worker, and every address it names is a
// route of the app.
func TestSeatPushScriptReferencesOnlyItsOwnAddresses(t *testing.T) {
	js := codeOnly(pushScript(t))
	routes := map[string]bool{}
	for _, rt := range (&app{pair: &pairing{}}).routes() {
		_, p, _ := strings.Cut(rt.pattern, " ")
		routes[p] = true
	}
	for _, m := range regexp.MustCompile(`"(/[^"]*)"`).FindAllStringSubmatch(js, -1) {
		switch m[1] {
		case "/":
		case "/sw.js", "/api/push/info", "/api/push/subscribe", "/api/push/unsubscribe":
			if !routes[m[1]] {
				t.Errorf("push.js names %q, which is no route", m[1])
			}
		default:
			t.Errorf("push.js names the address %q", m[1])
		}
	}
	if n := strings.Count(js, "fetch("); n != 1 {
		t.Errorf("push.js calls fetch %d times", n)
	}
	for _, path := range []string{"INFO_PATH", "SUBSCRIBE_PATH", "UNSUBSCRIBE_PATH"} {
		if !strings.Contains(js, "call("+path) {
			t.Errorf("push.js never calls %s", path)
		}
	}
	// call(path, ...) is given one of the three constants and nothing built.
	for _, m := range regexp.MustCompile(`\bcall\(([^,)]*)`).FindAllStringSubmatch(js, -1) {
		switch m[1] {
		case "INFO_PATH", "SUBSCRIBE_PATH", "UNSUBSCRIBE_PATH", "path":
		default:
			t.Errorf("push.js calls call(%s", m[1])
		}
	}
}

// TestSeatPushScriptAsksPermissionOnlyInAClick: the permission is requested
// in the handler of the Enable button's click, once, before anything is
// subscribed, and nowhere else.
func TestSeatPushScriptAsksPermissionOnlyInAClick(t *testing.T) {
	js := codeOnly(pushScript(t))
	if n := strings.Count(js, "requestPermission"); n != 1 {
		t.Fatalf("push.js names requestPermission %d times", n)
	}
	if bad := outsideClickHandlers(js, "requestPermission"); len(bad) != 0 {
		t.Errorf("requestPermission outside a click handler, at %v", bad)
	}
	// Both buttons have a click handler; nothing else is a way in.
	if n := strings.Count(js, `addEventListener("click"`); n != 2 {
		t.Errorf("push.js has %d click handlers", n)
	}
	if strings.Contains(js, `addEventListener("load"`) || strings.Contains(js, "DOMContentLoaded") || strings.Contains(js, "visibilitychange") {
		t.Error("push.js acts on an event that is no tap")
	}
	// Subscribing and registering are in the Enable handler too, after the
	// permission is known to be granted.
	perm := strings.Index(js, "Notification.requestPermission()")
	granted := strings.Index(js, `permission !== "granted"`)
	for _, call := range []string{".register(", ".subscribe("} {
		i := strings.Index(js, call)
		if i < 0 || i < perm || i < granted {
			t.Errorf("push.js: %s is not after the permission", call)
		}
		if bad := outsideClickHandlers(js, call); len(bad) != 0 {
			t.Errorf("push.js: %s outside a click handler", call)
		}
	}
	// The status of the permission is read, never changed, on load.
	for _, m := range regexp.MustCompile(`Notification\.([A-Za-z]+)`).FindAllStringSubmatch(js, -1) {
		if m[1] != "requestPermission" {
			t.Errorf("push.js uses Notification.%s", m[1])
		}
	}
}

// TestSeatPushScriptStandaloneAndSupport: it shows the controls only for the
// Home Screen app on a browser that has what it needs.
func TestSeatPushScriptStandaloneAndSupport(t *testing.T) {
	js := codeOnly(pushScript(t))
	for _, want := range []string{
		`"serviceWorker" in navigator`, `"PushManager" in window`, `"Notification" in window`,
		"navigator.standalone === true", `matchMedia("(display-mode: standalone)").matches`,
		"if (!supported) { say(TEXT.unsupported); return; }", "if (!standalone) { say(TEXT.notHome); return; }",
		"userVisibleOnly: true", "applicationServerKey: key", `getKey("p256dh")`, `getKey("auth")`,
		`crypto.subtle.digest("SHA-256"`, "endpoint_sha256:", "sub.unsubscribe()", `register(swScriptURL(), { scope: "/" })`,
		"KEY_LENGTH = 65",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("push.js lacks %q", want)
		}
	}
	// The notifier forgets the subscription before the browser does, so that
	// a failure leaves a subscription that can be stopped again.
	if a, b := strings.Index(js, "call(UNSUBSCRIBE_PATH"), strings.LastIndex(js, "await sub.unsubscribe();"); a < 0 || b < 0 || a > b {
		t.Error("push.js stops the browser's subscription before the notifier's")
	}
}

// TestSeatPushScriptWordsAreConstants: every text of the status line is one of
// the constants, and none comes from an answer.
func TestSeatPushScriptWordsAreConstants(t *testing.T) {
	js := codeOnly(pushScript(t))
	for _, want := range []string{
		`"This browser cannot show notices."`, `"Open this page from the Home Screen app to turn on notices."`,
		`"Notices are on."`, `"Notices are off."`, `"Notices need your permission. Change it in the phone's settings."`,
		`"Could not turn on notices. Try again later."`, `"Could not stop notices. Try again later."`,
		`"No room for another phone. Stop notices on another phone first."`, `"Too many tries. Wait a minute."`,
		`"Session ended. Log in again."`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("push.js lacks %s", want)
		}
	}
	for _, m := range regexp.MustCompile(`\bsay\(([^;]*?)\);`).FindAllStringSubmatch(js, -1) {
		arg := m[1]
		if !(strings.HasPrefix(arg, "TEXT.") || arg == "subscribed ? TEXT.on : TEXT.off" || arg == "fallback") {
			t.Errorf("push.js says %q", arg)
		}
	}
	// What it reads of an answer: ok, a code (to pick a constant), and the
	// key.
	for _, m := range regexp.MustCompile(`\b(?:data|err|info)\.([A-Za-z_]+)`).FindAllStringSubmatch(js, -1) {
		switch m[1] {
		case "ok", "code", "vapid_public":
		default:
			t.Errorf("push.js reads .%s of an answer", m[1])
		}
	}
	for _, m := range regexp.MustCompile(`(?s)(?:textContent|createTextNode)\s*=?\s*\(?\s*([^;)]*)`).FindAllStringSubmatch(js, -1) {
		for _, bad := range []string{"data.", "resp", "err.", "info.", "e.message"} {
			if strings.Contains(m[1], bad) {
				t.Errorf("push.js sets a text from %q", m[1])
			}
		}
	}
}

// TestSeatPushScriptRegistersThroughAPolicy: the register call is a script
// sink under the page's Trusted Types rule (the Service Workers standard
// takes a TrustedScriptURL there), so the address goes through a policy
// that answers for one address, the worker's, and for no other.
func TestSeatPushScriptRegistersThroughAPolicy(t *testing.T) {
	js := codeOnly(pushScript(t))
	if n := strings.Count(js, ".register("); n != 1 {
		t.Fatalf("push.js registers %d times", n)
	}
	for _, want := range []string{
		`var SW_PATH = "/sw.js";`, "window.trustedTypes", `createPolicy("seat-sw"`,
		"createScriptURL: function (url) {", "if (url !== SW_PATH) { throw new TypeError(", "return swPolicy.createScriptURL(SW_PATH);",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("push.js lacks %q", want)
		}
	}
	if n := strings.Count(js, "createPolicy("); n != 1 {
		t.Errorf("push.js makes %d policies", n)
	}
	// The register call is given the policy's answer, not a string.
	if !strings.Contains(js, "register(swScriptURL(), ") {
		t.Error("push.js registers with something other than the policy's answer")
	}
}

// TestSeatCSPNamesEveryTrustedTypesPolicy: every policy any script of the app
// creates is one the CSP's trusted-types directive names, and the directive
// names no policy that no script creates, so a policy the page makes is not
// refused and none can be made that the app does not know.
func TestSeatCSPNamesEveryTrustedTypesPolicy(t *testing.T) {
	r := newRig(t, withPush(&fakePush{}))
	csp := r.do("GET", "/healthz", nil, nil).Header().Get("Content-Security-Policy")
	var named []string
	for _, d := range strings.Split(csp, ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(d), "trusted-types "); ok {
			named = strings.Fields(v)
		}
	}
	scripts, err := fs.Glob(assetFS, "assets/*.js")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("scripts %v: %v", scripts, err)
	}
	created := map[string]bool{}
	for _, p := range scripts {
		b, err := fs.ReadFile(assetFS, p)
		if err != nil {
			t.Fatal(err)
		}
		code := codeOnly(string(b))
		if strings.Contains(code, "createPolicy") {
			for _, m := range regexp.MustCompile(`createPolicy\(\s*([^,)]*)`).FindAllStringSubmatch(code, -1) {
				name := strings.Trim(strings.TrimSpace(m[1]), `"'`)
				created[name] = true
			}
			if n := len(regexp.MustCompile(`createPolicy\(`).FindAllString(code, -1)); n != 1 {
				t.Errorf("%s makes %d policies", p, n)
			}
		}
	}
	if len(created) == 0 {
		t.Fatal("no script makes a policy: the directive is for nothing")
	}
	want := make([]string, 0, len(created))
	for n := range created {
		want = append(want, n)
	}
	sort.Strings(want)
	sort.Strings(named)
	if !reflect.DeepEqual(named, want) {
		t.Errorf("the CSP names the policies %v, the scripts make %v", named, want)
	}
}
