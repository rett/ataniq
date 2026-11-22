package seatapp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOriginValidation(t *testing.T) {
	d := &fakeDaemon{}
	for _, tc := range []struct {
		name, origin, rpid string
		insecure           bool
		want               string // the origin the app compares with; empty means refused
	}{
		{"https host", "https://seat.example", "seat.example", false, "https://seat.example"},
		{"explicit default port", "https://seat.example:443", "seat.example", false, "https://seat.example"},
		{"rp id of a parent domain", "https://seat.example.org", "example.org", false, ""},
		{"rp id of a public suffix", "https://seat.example.co.uk", "co.uk", false, ""},
		{"rp id with a trailing dot", "https://seat.example", "seat.example.", false, ""},

		{"empty", "", "seat.example", false, ""},
		{"empty host", "https://", "seat.example", false, ""},
		{"empty host with port", "https://:443", "seat.example", false, ""},
		{"http", "http://seat.example", "seat.example", false, ""},
		{"http on 80", "http://seat.example:80", "seat.example", false, ""},
		{"other port", "https://seat.example:8443", "seat.example", false, ""},
		{"empty port", "https://seat.example:", "seat.example", false, ""},
		{"path", "https://seat.example/app", "seat.example", false, ""},
		{"trailing slash", "https://seat.example/", "seat.example", false, ""},
		{"query", "https://seat.example?x=1", "seat.example", false, ""},
		{"bare question mark", "https://seat.example?", "seat.example", false, ""},
		{"fragment", "https://seat.example#x", "seat.example", false, ""},
		{"user information", "https://owner@seat.example", "seat.example", false, ""},
		{"user and password", "https://owner:pw@seat.example", "seat.example", false, ""},
		{"no scheme", "seat.example", "seat.example", false, ""},
		{"other scheme", "ftp://seat.example", "seat.example", false, ""},
		{"upper case scheme", "HTTPS://seat.example", "seat.example", false, ""},
		{"upper case host", "https://Seat.Example", "seat.example", false, ""},
		{"address literal", "https://192.0.2.1", "192.0.2.1", false, ""},
		{"loopback literal", "https://127.0.0.1", "127.0.0.1", false, ""},
		{"v6 literal", "https://[::1]", "::1", false, ""},
		{"loopback http without the option", "http://127.0.0.1:8080", "127.0.0.1", false, ""},
		{"space", "https://seat .example", "seat.example", false, ""},
		{"rp id empty", "https://seat.example", "", false, ""},
		{"rp id of another host", "https://seat.example", "other.example", false, ""},
		{"rp id longer than the host", "https://seat.example", "a.seat.example", false, ""},
		{"rp id that only ends alike", "https://seat.example", "at.example", false, ""},

		{"test option: loopback http with port", "http://127.0.0.1:8080", "127.0.0.1", true, "http://127.0.0.1:8080"},
		{"test option: v6 loopback", "http://[::1]:8080", "::1", true, "http://[::1]:8080"},
		{"test option: localhost", "http://localhost:9", "localhost", true, "http://localhost:9"},
		{"test option: https loopback", "https://127.0.0.1:8443", "127.0.0.1", true, "https://127.0.0.1:8443"},
		{"test option: a public host is still refused", "http://seat.example", "seat.example", true, ""},
		{"test option: a public address is refused", "http://192.0.2.1:8080", "192.0.2.1", true, ""},
		{"test option: a path is still refused", "http://127.0.0.1:8080/x", "127.0.0.1", true, ""},
		{"test option: user information is still refused", "http://u@127.0.0.1:8080", "127.0.0.1", true, ""},
		{"test option: a wildcard is not loopback", "http://0.0.0.0:8080", "0.0.0.0", true, ""},
	} {
		h, err := New(Config{Origin: tc.origin, RPID: tc.rpid, Daemon: d, AllowInsecureLoopbackForTests: tc.insecure, AllowNoFetchMetadataForTests: true})
		if tc.want == "" {
			if err == nil {
				t.Errorf("%s: New accepted %q", tc.name, tc.origin)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		// The origin compared with is the canonical one: a POST with it
		// passes the origin check and one with the default port spelt out
		// does not.
		_, hostOnly, _ := strings.Cut(tc.want, "://")
		hdr := map[string]string{"Origin": tc.want, "Content-Type": "application/json", "Host": hostOnly}
		if got := doTo(h, "POST", "/api/pair-challenge", "{}", hdr).Code; got == 403 {
			t.Errorf("%s: the origin %q was refused", tc.name, tc.want)
		}
		hdr["Origin"] = tc.want + ":443"
		if got := doTo(h, "POST", "/api/pair-challenge", "{}", hdr).Code; got != 403 {
			t.Errorf("%s: a spelt-out default port passed the origin check: %d", tc.name, got)
		}
	}
	if _, err := New(Config{Origin: "https://a.example", RPID: "a.example"}); err == nil {
		t.Error("no daemon: New accepted it")
	}
}

func TestRoutes(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		path, ctype, contains string
	}{
		{"/healthz", "text/plain; charset=utf-8", "ok"},
		{"/", "text/html; charset=utf-8", "Authority seat"},
		{"/pair/", "text/html; charset=utf-8", "Pair this phone"},
		{"/static/words.js", "text/javascript; charset=utf-8", "SeatWords"},
		{"/static/pair.js", "text/javascript; charset=utf-8", ""},
		{"/static/app.css", "text/css; charset=utf-8", ""},
	} {
		w := r.do("GET", tc.path, nil, nil)
		if w.Code != 200 {
			t.Errorf("GET %s: status %d", tc.path, w.Code)
		}
		if got := w.Header().Get("Content-Type"); got != tc.ctype {
			t.Errorf("GET %s: content type %q, want %q", tc.path, got, tc.ctype)
		}
		if !strings.Contains(w.Body.String(), tc.contains) {
			t.Errorf("GET %s: body lacks %q", tc.path, tc.contains)
		}
		checkHeaders(t, "GET "+tc.path, w)
	}
	if body := r.do("GET", "/healthz", nil, nil).Body.String(); body != "ok\n" {
		t.Errorf("healthz body %q", body)
	}
}

func TestHeadersOnEveryResponse(t *testing.T) {
	r := newRig(t)
	ok := map[string]string{"Origin": testOrigin, "Content-Type": "application/json"}
	for _, tc := range []struct {
		what, method, path string
		body               string
		header             map[string]string
		status             int
	}{
		{"unknown path", "GET", "/nope", "", nil, 404},
		{"unknown subpath", "GET", "/pair/x", "", nil, 404},
		{"directory of assets", "GET", "/static/", "", nil, 404},
		{"wrong method on a page", "POST", "/pair/", "", ok, 405},
		{"wrong method on the redirect", "POST", "/pair", "", ok, 405},
		{"wrong method on the api", "GET", "/api/pair", "", nil, 405},
		{"delete", "DELETE", "/healthz", "", nil, 405},
		{"no origin", "POST", "/api/pair-challenge", "{}", map[string]string{"Content-Type": "application/json"}, 403},
		{"oversized", "POST", "/api/pair", strings.Repeat("a", MaxBody+1), ok, 413},
	} {
		w := r.do(tc.method, tc.path, strings.NewReader(tc.body), tc.header)
		if w.Code != tc.status {
			t.Errorf("%s: status %d, want %d", tc.what, w.Code, tc.status)
		}
		checkHeaders(t, tc.what, w)
	}
}

func TestPostChecks(t *testing.T) {
	r := newRig(t)
	const forbidden = "forbidden\n"
	for _, path := range []string{"/api/pair-challenge", "/api/pair"} {
		for _, tc := range []struct {
			what   string
			header map[string]string
			status int
			body   string
		}{
			{"no origin", map[string]string{"Content-Type": "application/json"}, 403, forbidden},
			{"other origin", map[string]string{"Origin": "https://evil.example", "Content-Type": "application/json"}, 403, forbidden},
			{"origin with a path", map[string]string{"Origin": testOrigin + "/", "Content-Type": "application/json"}, 403, forbidden},
			{"origin null", map[string]string{"Origin": "null", "Content-Type": "application/json"}, 403, forbidden},
			{"cross-site fetch", map[string]string{"Origin": testOrigin, "Sec-Fetch-Site": "cross-site", "Content-Type": "application/json"}, 403, forbidden},
			{"same-site fetch", map[string]string{"Origin": testOrigin, "Sec-Fetch-Site": "same-site", "Content-Type": "application/json"}, 403, forbidden},
			{"no content type", map[string]string{"Origin": testOrigin}, 415, "unsupported media type\n"},
			{"form content type", map[string]string{"Origin": testOrigin, "Content-Type": "application/x-www-form-urlencoded"}, 415, "unsupported media type\n"},
			{"text content type", map[string]string{"Origin": testOrigin, "Content-Type": "text/plain"}, 415, "unsupported media type\n"},
		} {
			w := r.do("POST", path, strings.NewReader("{}"), tc.header)
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Errorf("%s %s: %d %q, want %d %q", path, tc.what, w.Code, w.Body.String(), tc.status, tc.body)
			}
		}
	}
	// The same-origin fetch metadata and a charset parameter are fine.
	w := r.do("POST", "/api/pair-challenge", strings.NewReader("{}"), map[string]string{
		"Origin": testOrigin, "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json; charset=utf-8",
	})
	if w.Code == 403 || w.Code == 415 {
		t.Errorf("a well-formed POST was refused: %d", w.Code)
	}
}

func TestBodyLimit(t *testing.T) {
	r := newRig(t)
	h := map[string]string{"Origin": testOrigin, "Content-Type": "application/json"}
	// Declared too long: refused before any read.
	w := r.do("POST", "/api/pair", strings.NewReader(strings.Repeat("a", MaxBody+1)), h)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("declared oversized body: %d", w.Code)
	}
}

func TestServerLimits(t *testing.T) {
	s := Server(http.NotFoundHandler())
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.IdleTimeout <= 0 || s.MaxHeaderBytes <= 0 {
		t.Errorf("a limit is not set: %+v", s)
	}
}

// TestPostNeedsTheHostAndFetchMetadata: on every POST the Host is the
// origin's own host (a proxy that passes another one is a misconfiguration the
// app refuses to serve), and the browser's Sec-Fetch-Site is there and says
// same-origin; every browser that can make a passkey sends it, so its absence
// is a client that is not one.
func TestPostNeedsTheHostAndFetchMetadata(t *testing.T) {
	r := newRig(t, func(c *Config) { c.AllowNoFetchMetadataForTests = false })
	post := func(path, host, site string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		req.Host = host
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("Content-Type", "application/json")
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		return w.Code
	}
	for _, path := range []string{"/api/pair-challenge", "/api/pair"} {
		if c := post(path, "seat.example", "same-origin"); c == 403 {
			t.Errorf("%s: the right host and site were refused", path)
		}
		for _, tc := range []struct{ what, host, site string }{
			{"no fetch metadata", "seat.example", ""},
			{"a different host", "other.example", "same-origin"},
			{"a parent domain as host", "example", "same-origin"},
			{"the host with a port", "seat.example:443", "same-origin"},
			{"an empty host", "", "same-origin"},
			{"cross-site", "seat.example", "cross-site"},
			{"none (typed address)", "seat.example", "none"},
		} {
			if c := post(path, tc.host, tc.site); c != 403 {
				t.Errorf("%s %s: %d, want 403", path, tc.what, c)
			}
		}
	}
	// The same-origin Host is case-folded by the browser; the app compares it
	// as sent, so a differently cased one is refused too.
	if c := post("/api/pair-challenge", "Seat.Example", "same-origin"); c != 403 {
		t.Errorf("upper case host: %d", c)
	}
}

// TestNoFetchMetadataOptionIsOnlyForTests: the option exists in Config for
// tests, and the default (a zero Config value) requires the header.
func TestNoFetchMetadataOptionIsOnlyForTests(t *testing.T) {
	h, err := New(Config{Origin: testOrigin, RPID: testRPID, Daemon: &fakeDaemon{}})
	if err != nil {
		t.Fatal(err)
	}
	w := doTo(h, "POST", "/api/pair-challenge", "{}", map[string]string{"Origin": testOrigin, "Content-Type": "application/json"})
	if w.Code != 403 {
		t.Errorf("a POST with no Sec-Fetch-Site: %d", w.Code)
	}
}
