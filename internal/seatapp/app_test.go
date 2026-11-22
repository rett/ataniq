package seatapp

import (
	"net/http"
	"strings"
	"testing"
)

func TestNewRefusesBadConfig(t *testing.T) {
	d := &fakeDaemon{}
	for name, cfg := range map[string]Config{
		"no origin":        {RPID: "a", Daemon: d},
		"path in origin":   {Origin: "https://a.example/x", RPID: "a", Daemon: d},
		"slash in origin":  {Origin: "https://a.example/", RPID: "a", Daemon: d},
		"query in origin":  {Origin: "https://a.example?x", RPID: "a", Daemon: d},
		"user in origin":   {Origin: "https://u@a.example", RPID: "a", Daemon: d},
		"scheme in origin": {Origin: "ftp://a.example", RPID: "a", Daemon: d},
		"no rpid":          {Origin: "https://a.example", Daemon: d},
		"no daemon":        {Origin: "https://a.example", RPID: "a"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
	if _, err := New(Config{Origin: "http://127.0.0.1:8080", RPID: "127.0.0.1", Daemon: d}); err != nil {
		t.Errorf("a loopback origin with a port: %v", err)
	}
}

func TestRoutes(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		path, ctype, contains string
	}{
		{"/healthz", "text/plain; charset=utf-8", "ok"},
		{"/", "text/html; charset=utf-8", "Authority seat"},
		{"/pair", "text/html; charset=utf-8", "Pair this phone"},
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
		{"wrong method on a page", "POST", "/pair", "", ok, 405},
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
