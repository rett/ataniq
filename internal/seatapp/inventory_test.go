package seatapp

import (
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The page inventory. Every route of the app is listed here with what it
// serves, and the test fetches each one. An approve control exists in exactly
// one place, the Card page reached from the Cards list; no other page, notice,
// link or message may hold or open a decide control. No such page exists yet,
// so no route is in deciders. When the Card page is added, it is listed there
// by its pattern, and only it.
var inventory = []struct {
	pattern string
	kind    string // "page", "asset", "text", "redirect" or "api"
	// controls are the form controls the page may hold, by element and id.
	controls []string
}{
	{"GET /healthz", "text", nil},
	{"GET /{$}", "page", nil},
	{"GET /pair/{$}", "page", []string{"button#start"}},
	{"GET /pair", "redirect", nil},
	{"GET /static/pair.js", "asset", nil},
	{"GET /static/app.css", "asset", nil},
	{"POST /api/pair-challenge", "api", nil},
	{"POST /api/pair", "api", nil},
}

// deciders are the routes allowed to hold a decide control. None yet.
var deciders = map[string]bool{}

func TestInventoryListsEveryRoute(t *testing.T) {
	var have, want []string
	for _, rt := range (&app{pair: &pairing{}}).routes() {
		have = append(have, rt.pattern)
	}
	for _, e := range inventory {
		want = append(want, e.pattern)
	}
	sort.Strings(have)
	sort.Strings(want)
	if strings.Join(have, "\n") != strings.Join(want, "\n") {
		t.Errorf("the routes and the inventory differ:\nroutes:    %q\ninventory: %q", have, want)
	}
	for p := range deciders {
		found := false
		for _, e := range inventory {
			found = found || e.pattern == p
		}
		if !found {
			t.Errorf("decider %q is not a route", p)
		}
	}
}

// decideWords are the words that make a control, an id, a name, a class, an
// address or a data attribute look like deciding something.
var decideWords = regexp.MustCompile(`(?i)approv|den(y|ies|ied)|decid|decision|reject|accept|allow|grant|authori[sz]|sign-?off|confirm|later|put-?off|passkey-?assert|assertion`)

// externalRe finds an address with a scheme or without one that names a host.
var externalRe = regexp.MustCompile(`(?i)https?:|[("'=\s]//[a-z0-9]`)

var (
	tagRe  = regexp.MustCompile(`(?is)<(/?)([a-z][a-z0-9-]*)([^>]*)>`)
	attrRe = regexp.MustCompile(`(?is)([a-z_:][-a-z0-9_:.]*)\s*(?:=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?`)
)

// inspectPage returns what is wrong with an HTML page: an inline script or
// style, an event handler, an external address, a form or a control that the
// inventory does not list for it, and a control that looks like deciding when
// the page may not hold one.
func inspectPage(html string, allowed []string, mayDecide bool) []string {
	var found []string
	add := func(parts ...string) { found = append(found, strings.Join(parts, " ")) }
	if strings.Contains(strings.ToLower(html), "javascript:") {
		add("javascript address")
	}
	if externalRe.MatchString(html) {
		add("external address")
	}
	for _, m := range tagRe.FindAllStringSubmatch(html, -1) {
		closing, name, rest := m[1], strings.ToLower(m[2]), m[3]
		if closing != "" {
			continue
		}
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllStringSubmatch(rest, -1) {
			v := a[2] + a[3] + a[4]
			attrs[strings.ToLower(a[1])] = v
			key := strings.ToLower(a[1])
			if strings.HasPrefix(key, "on") {
				add("event handler", key)
			}
			if key == "style" {
				add("style attribute")
			}
			if (key == "href" || key == "src" || key == "action" || key == "formaction") && v != "" &&
				!(strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//")) && !strings.HasPrefix(v, "#") {
				add("address that is not of this app", key, v)
			}
			if !mayDecide && (key == "id" || key == "name" || key == "class" || key == "action" || key == "formaction" ||
				key == "href" || key == "value" || key == "for" || strings.HasPrefix(key, "data-") || strings.HasPrefix(key, "aria-")) &&
				key != "data-rp-name" && decideWords.MatchString(v) {
				add("attribute that looks like deciding", key, v)
			}
		}
		switch name {
		case "style":
			add("style element")
		case "script":
			if !strings.HasPrefix(attrs["src"], "/static/") {
				add("inline script")
			}
		case "form", "input", "button", "select", "textarea", "a", "iframe", "object", "embed", "img":
			id := name
			if attrs["id"] != "" {
				id += "#" + attrs["id"]
			}
			ok := false
			for _, a := range allowed {
				ok = ok || a == id
			}
			if !ok {
				add("element the inventory does not list", id)
			}
		}
	}
	return found
}

func TestPageInventory(t *testing.T) {
	r := newRig(t)
	for _, e := range inventory {
		if !strings.HasPrefix(e.pattern, "GET ") {
			continue
		}
		path := strings.TrimSuffix(strings.TrimPrefix(e.pattern, "GET "), "{$}")
		w := r.do("GET", path, nil, nil)
		if e.kind == "redirect" {
			if w.Code != 308 || w.Header().Get("Location") != path+"/" {
				t.Errorf("%s: %d, Location %q", e.pattern, w.Code, w.Header().Get("Location"))
			}
			checkHeaders(t, e.pattern, w)
			continue
		}
		if w.Code != 200 {
			t.Errorf("%s: status %d", e.pattern, w.Code)
			continue
		}
		checkHeaders(t, e.pattern, w)
		body := w.Body.String()
		switch e.kind {
		case "page":
			if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Errorf("%s: content type %q", e.pattern, ct)
			}
			for _, f := range inspectPage(body, e.controls, deciders[e.pattern]) {
				t.Errorf("%s: %s", e.pattern, f)
			}
			if strings.Contains(strings.ToLower(body), "<form") {
				t.Errorf("%s: a form, and none exists yet", e.pattern)
			}
		case "asset":
			if externalRe.MatchString(body) {
				t.Errorf("%s: an external address", e.pattern)
			}
			if strings.HasSuffix(path, ".js") && regexp.MustCompile(`(?i)approv|decid|decision|den(y|ied)`).MatchString(body) {
				t.Errorf("%s: the script mentions deciding", e.pattern)
			}
		case "text":
			if body != "ok\n" {
				t.Errorf("%s: %q", e.pattern, body)
			}
		}
	}
}

// TestInspectPageSeesWhatItLooksFor shows the inventory check on pages that
// break each rule, and on one that breaks none.
func TestInspectPageSeesWhatItLooksFor(t *testing.T) {
	clean := `<!doctype html><html lang="en"><head><link rel="stylesheet" href="/static/app.css"></head><body><main><button id="start" type="button">Go</button><script src="/static/pair.js"></script></main></body></html>`
	if f := inspectPage(clean, []string{"button#start"}, false); len(f) != 0 {
		t.Errorf("a clean page: %v", f)
	}
	for name, page := range map[string]string{
		"inline script":      `<script>x()</script>`,
		"inline script src":  `<script src="https://cdn.example/x.js"></script>`,
		"style attribute":    `<p style="color:red">x</p>`,
		"style element":      `<style>p{}</style>`,
		"event handler":      `<p onclick="x()">x</p>`,
		"javascript link":    `<a href="javascript:x()">x</a>`,
		"external image":     `<img src="https://evil.example/p.png">`,
		"protocol-relative":  `<link rel="stylesheet" href="//evil.example/a.css">`,
		"a form":             `<form action="/x" method="post"></form>`,
		"unlisted button":    `<button id="other" type="button">x</button>`,
		"approve form":       `<form action="/cards/1/approve" method="post"></form>`,
		"decide id":          `<div id="decide"></div>`,
		"deny class":         `<p class="deny-now">x</p>`,
		"data attribute":     `<p data-action="approve">x</p>`,
		"aria label":         `<p aria-label="Approve this card">x</p>`,
		"later link":         `<a href="/cards/1/later">x</a>`,
		"relative address":   `<a href="cards">x</a>`,
		"an iframe":          `<iframe src="/x"></iframe>`,
		"hidden approve btn": `<button id="start" name="approve" type="button">x</button>`,
	} {
		if f := inspectPage(page, []string{"button#start"}, false); len(f) == 0 {
			t.Errorf("%s: no finding", name)
		}
	}
	// A page that may decide is not held to the words, only to the rest.
	if f := inspectPage(`<button id="start" name="approve" type="button">x</button>`, []string{"button#start"}, true); len(f) != 0 {
		t.Errorf("a decider: %v", f)
	}
}

// TestNoCookiesAnywhere checks that no route sets a cookie, whatever it is
// sent: the app has no session yet.
func TestNoCookiesAnywhere(t *testing.T) {
	r := newRig(t)
	for _, e := range inventory {
		method, path, _ := strings.Cut(e.pattern, " ")
		path = strings.TrimSuffix(path, "{$}")
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", "a=b")
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		if c := w.Header().Values("Set-Cookie"); len(c) != 0 {
			t.Errorf("%s sets a cookie: %v", e.pattern, c)
		}
	}
}
