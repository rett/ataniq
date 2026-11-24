package seatapp

import (
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The page inventory. Every route of the app is listed here with what it
// serves, and the test fetches each one. An approve control exists in exactly
// one place, the Card page reached from the Cards list; no other page, notice,
// link or message may hold or open a decide control. So deciders holds the
// Card page alone, and decideAssets the one script that decides, which only
// a decider may load.
var inventory = []struct {
	pattern string
	kind    string // "page", "asset", "text", "redirect" or "api"
	// controls are the form controls the page may hold, by element and id;
	// "a" is a link without an id.
	controls []string
	// path is the path fetched for a pattern with wildcards; the pattern's
	// own when empty.
	path string
	// authed says the route needs a session: the test fetches it logged in.
	authed bool
}{
	{"GET /healthz", "text", nil, "", false},
	{"GET /{$}", "page", []string{"a#login"}, "", false},
	{"GET /pair/{$}", "page", []string{"button#start", "a#login-link"}, "", false},
	{"GET /pair", "redirect", nil, "", false},
	{"GET /static/words.js", "asset", nil, "", false},
	{"GET /static/pair.js", "asset", nil, "", false},
	{"GET /static/app.css", "asset", nil, "", false},
	{"POST /api/pair-challenge", "api", nil, "", false},
	{"POST /api/pair", "api", nil, "", false},
	{"GET /login/{$}", "page", []string{"button#start"}, "", false},
	{"GET /login", "redirect", nil, "", false},
	{"GET /static/login.js", "asset", nil, "", false},
	{"POST /api/login-challenge", "api", nil, "", false},
	{"POST /api/login", "api", nil, "", false},
	{"POST /api/logout", "api", nil, "", false},
	{pattern: "GET /cards/{$}", kind: "page", controls: withNav("a", "button#logout", "button#enable-notices", "button#stop-notices",
		"form#start-project", "button#start-project-go"), authed: true},
	{pattern: "GET /cards", kind: "redirect"},
	{pattern: "GET /cards/{id}", kind: "page", controls: withNav("a#paths", "a#back", "button#logout",
		"input#signoff-0", "button#approve", "textarea#reason", "button#deny", "button#confirm-go", "button#confirm-cancel", "a#look"),
		path: "/cards/" + testCardID, authed: true},
	{pattern: "GET /cards/{id}/paths/{n}", kind: "page", controls: withNav("a#prev", "a#next", "a#back", "button#logout"), path: "/cards/" + testCardID + "/paths/1", authed: true},
	{pattern: "GET /static/session.js", kind: "asset"},
	{pattern: "GET /static/decide.js", kind: "asset"},
	{pattern: "POST /api/decide-challenge", kind: "api"},
	{pattern: "POST /api/decide", kind: "api"},
	{pattern: "GET /static/watch.js", kind: "asset"},
	{pattern: "POST /api/watch", kind: "api"},
	{pattern: "GET /manifest.webmanifest", kind: "asset"},
	{pattern: "GET /sw.js", kind: "asset"},
	{pattern: "GET /static/push.js", kind: "asset"},
	{pattern: "GET /static/icon-192.png", kind: "asset"},
	{pattern: "GET /static/icon-512.png", kind: "asset"},
	{pattern: "GET /static/apple-touch-icon.png", kind: "asset"},
	{pattern: "POST /api/push/info", kind: "api"},
	{pattern: "POST /api/push/subscribe", kind: "api"},
	{pattern: "POST /api/push/unsubscribe", kind: "api"},
	// The new-project pages: plain forms that post to the app, and no
	// decide control.
	{pattern: "GET /projects/{$}", kind: "page", controls: withNav("a", "a#back", "button#logout", "form#start-project", "button#start-project-go"), authed: true},
	{pattern: "GET /projects", kind: "redirect"},
	{pattern: "POST /projects/new", kind: "api"},
	{pattern: "GET /projects/{thread}", kind: "page", controls: questionControls("form#answer", "input#round", "a#project-list", "a#discard-link"),
		path: "/projects/" + testThread, authed: true},
	{pattern: "GET /projects/{thread}/change/{node}", kind: "page", controls: questionControls("form#change", "input#node", "a#keep"),
		path: "/projects/" + testThread + "/change/kind", authed: true},
	{pattern: "GET /projects/{thread}/discard", kind: "page", controls: withNav("form#discard", "button#discard-go", "a#keep", "button#logout"),
		path: "/projects/" + testThread + "/discard", authed: true},
	// The plan page: links only, no form, no button, no script: its two
	// links and the main navigation's, and no logout control.
	{pattern: "GET /projects/{thread}/plan", kind: "page", controls: withNav("a#project-page", "a#project-list"),
		path: "/projects/" + testThread + "/plan", authed: true},
	{pattern: "POST /projects/{thread}/answer", kind: "api"},
	{pattern: "POST /projects/{thread}/change", kind: "api"},
	{pattern: "POST /projects/{thread}/finish", kind: "api"},
	{pattern: "POST /projects/{thread}/discard", kind: "api"},
}

// navControls are the links every page behind a session holds before its
// content: the skip link and the main navigation's, all plain links. The
// "Projects" link is there only while new projects are offered.
var navControls = []string{"a#skip", "a#nav-cards", "a#nav-projects"}

// withNav is the controls given and the navigation's.
func withNav(controls ...string) []string {
	return append(append([]string{}, navControls...), controls...)
}

// questionControls are the controls of a page with the form of a round, or
// of one question, beside the ones given: for each of up to three questions
// its node, its options (or a scale's five steps), "Other" with its text,
// "You decide for me" and "Skip", and the one submit button.
func questionControls(more ...string) []string {
	out := withNav(append([]string{"button#send", "button#logout"}, more...)...)
	for i := 0; i < MaxGenesisQuestions; i++ {
		q := "q" + strconv.Itoa(i) + "-"
		out = append(out, "input#n"+strconv.Itoa(i), "input#"+q+"other", "input#"+q+"text", "input#"+q+"host", "input#"+q+"skip")
		for j := 0; j <= MaxGenesisOptions; j++ {
			out = append(out, "input#"+q+"o"+strconv.Itoa(j))
		}
	}
	return out
}

// formRoutes are the pages that may hold a form, each a plain post to one of
// the app's project routes, and no other page holds one. The forms post an
// answer, a change, a finish, a discard or a start: never a decision.
var formRoutes = map[string]bool{
	"GET /cards/{$}": true, "GET /projects/{$}": true, "GET /projects/{thread}": true,
	"GET /projects/{thread}/change/{node}": true, "GET /projects/{thread}/discard": true,
}

// formRe finds a form's start tag, and projectAction the address of a
// project's post.
var (
	formRe        = regexp.MustCompile(`(?is)<form\b([^>]*)>`)
	projectAction = regexp.MustCompile(`^/projects/[0-9a-f]{16}/(answer|change|finish|discard)$`)
)

// checkForms fails for a form that is not a plain post to a project route of
// the app.
func checkForms(t testing.TB, pattern, html string) {
	t.Helper()
	for _, m := range formRe.FindAllStringSubmatch(html, -1) {
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllStringSubmatch(m[1], -1) {
			attrs[strings.ToLower(a[1])] = a[2] + a[3] + a[4]
		}
		action := attrs["action"]
		ok := attrs["method"] == "post" && attrs["enctype"] == "" && attrs["target"] == "" &&
			(action == "/projects/new" || projectAction.MatchString(action))
		if !ok {
			t.Errorf("%s: a form that is not a plain post to a project route: %s", pattern, m[0])
		}
	}
}

// passkeyRoutes are the pages whose Permissions-Policy allows
// publickey-credentials-get to the app's own origin. Every other response
// forbids it.
var passkeyRoutes = map[string]bool{"GET /login/{$}": true, "GET /cards/{id}": true}

// cookieRoutes are the routes that may set a cookie, with the cookies each
// may set. No page sets one. The decision's routes only remove the session's
// cookie, when the daemon says its passkey is no longer paired.
var cookieRoutes = map[string][]string{
	"POST /api/login-challenge":  {LoginCookie},
	"POST /api/login":            {LoginCookie, SessionCookie},
	"POST /api/logout":           {SessionCookie},
	"POST /api/decide-challenge": {SessionCookie},
	"POST /api/decide":           {SessionCookie},
}

// pushPages are the pages that may load the notices' script and link the web
// app manifest: the card list, and no other. The service worker, the
// manifest and the icons are listed routes of their own.
var pushPages = map[string]bool{"GET /cards/{$}": true}

// cacheOverride are the responses whose Cache-Control is not "no-store": a
// browser checks the service worker's script for a change with each start.
var cacheOverride = map[string]map[string]string{"GET /sw.js": {"Cache-Control": "no-cache"}}

// deciders are the routes allowed to hold a decide control: the Card page,
// and no other.
var deciders = map[string]bool{"GET /cards/{id}": true}

// decideAssets are the scripts that decide; a page that is not a decider
// may not load one.
var decideAssets = map[string]bool{"/static/decide.js": true}

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
			if !mayDecide && key == "data-offer" {
				add("an offer control on a page that may not decide", v)
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
			if decideAssets[attrs["src"]] && !mayDecide {
				add("a deciding script on a page that may not decide", attrs["src"])
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

// decidingRe finds the words of deciding in the text of a script.
var decidingRe = regexp.MustCompile(`(?i)approv|decid|decision|den(y|ied)`)

// watchBanner is the one sentence of a script that is not a decide script
// which may hold such a word: the banner the live refresh script shows when
// the card of a page changed or was settled elsewhere. It is a constant of
// the script, and the rule below takes out that exact sentence and nothing
// else, so the script holds no other word of the kind. The script reaches the
// decision form's controls only by the neutral attribute data-offer that the
// Card page puts on them, never by the name of one.
const watchBanner = "This card changed or was decided elsewhere. Look again."

// mentionsDeciding reports whether the text of a script that is not a decide
// script holds a word of deciding, but for the one banner sentence.
func mentionsDeciding(js string) bool {
	return decidingRe.MatchString(strings.ReplaceAll(js, watchBanner, ""))
}

func TestPageInventory(t *testing.T) {
	// The notifier is a PushPort, so the notices' routes and controls exist;
	// the daemon runs the interview, so the project pages exist.
	r := newRig(t, withPush(&fakePush{}), withGenesis(&fakeGenesis{}))
	session := r.login(t)
	for _, e := range inventory {
		if !strings.HasPrefix(e.pattern, "GET ") {
			continue
		}
		path := strings.TrimSuffix(strings.TrimPrefix(e.pattern, "GET "), "{$}")
		if e.path != "" {
			path = e.path
		}
		var w *httptest.ResponseRecorder
		if e.authed {
			w = r.getWith(path, session)
		} else {
			w = r.do("GET", path, nil, nil)
		}
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
		switch {
		case passkeyRoutes[e.pattern]:
			checkHeadersGet(t, e.pattern, w, "(self)")
		case cacheOverride[e.pattern] != nil:
			checkHeadersWith(t, e.pattern, w, "()", cacheOverride[e.pattern])
		default:
			checkHeaders(t, e.pattern, w)
		}
		body := w.Body.String()
		switch e.kind {
		case "page":
			if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Errorf("%s: content type %q", e.pattern, ct)
			}
			for _, f := range inspectPage(body, e.controls, deciders[e.pattern]) {
				t.Errorf("%s: %s", e.pattern, f)
			}
			// A page behind a session has the main navigation; a public
			// page has none, and no skip link.
			if e.authed {
				for _, f := range checkNav(body) {
					t.Errorf("%s: %s", e.pattern, f)
				}
			} else if hasNav(body) {
				t.Errorf("%s: a public page with the main navigation", e.pattern)
			}
			// A decider is listed because it holds the form: the
			// inventory's page shows it.
			if deciders[e.pattern] && !strings.Contains(body, `<script src="/static/decide.js"></script>`) {
				t.Errorf("%s: a decider without its form", e.pattern)
			}
			// The notices' script and the manifest are the card list's alone.
			if pushPages[e.pattern] {
				for _, p := range []string{"<script src=\"/static/push.js\"></script>", "<link rel=\"manifest\" href=\"/manifest.webmanifest\">", "<link rel=\"apple-touch-icon\""} {
					if !strings.Contains(body, p) {
						t.Errorf("%s: lacks %q", e.pattern, p)
					}
				}
			} else {
				for _, p := range []string{"/static/push.js", "/manifest.webmanifest", "apple-touch-icon", "notices"} {
					if strings.Contains(body, p) {
						t.Errorf("%s: holds %q, which only the card list may", e.pattern, p)
					}
				}
			}
			if strings.Contains(strings.ToLower(body), "<form") && !formRoutes[e.pattern] {
				t.Errorf("%s: a form, and only the project pages and the card list's start may hold one", e.pattern)
			}
			// A form is posted with the page's referrer policy, and only
			// same-origin (of the policies that leak nothing to another
			// origin) keeps the Origin header the app checks: no-referrer
			// would make every post "Origin: null" and a 403.
			if strings.Contains(strings.ToLower(body), "<form") {
				if got := w.Header().Values("Referrer-Policy"); len(got) != 1 || got[0] != "same-origin" {
					t.Errorf("%s: a page with a form has Referrer-Policy %q, want same-origin", e.pattern, got)
				}
			}
			checkForms(t, e.pattern, body)
		case "asset":
			if externalRe.MatchString(body) {
				t.Errorf("%s: an external address", e.pattern)
			}
			if strings.HasSuffix(path, ".js") && !decideAssets[path] && mentionsDeciding(body) {
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
		"inline script":       `<script>x()</script>`,
		"inline script src":   `<script src="https://cdn.example/x.js"></script>`,
		"style attribute":     `<p style="color:red">x</p>`,
		"style element":       `<style>p{}</style>`,
		"event handler":       `<p onclick="x()">x</p>`,
		"javascript link":     `<a href="javascript:x()">x</a>`,
		"external image":      `<img src="https://evil.example/p.png">`,
		"protocol-relative":   `<link rel="stylesheet" href="//evil.example/a.css">`,
		"a form":              `<form action="/x" method="post"></form>`,
		"unlisted button":     `<button id="other" type="button">x</button>`,
		"approve form":        `<form action="/cards/1/approve" method="post"></form>`,
		"decide id":           `<div id="decide"></div>`,
		"deny class":          `<p class="deny-now">x</p>`,
		"data attribute":      `<p data-action="approve">x</p>`,
		"aria label":          `<p aria-label="Approve this card">x</p>`,
		"later link":          `<a href="/cards/1/later">x</a>`,
		"relative address":    `<a href="cards">x</a>`,
		"an iframe":           `<iframe src="/x"></iframe>`,
		"hidden approve btn":  `<button id="start" name="approve" type="button">x</button>`,
		"the deciding script": `<script src="/static/decide.js"></script>`,
		"a sign-off box":      `<input type="checkbox" id="signoff-0">`,
		"an offer control":    `<p data-offer="open">x</p>`,
	} {
		if f := inspectPage(page, []string{"button#start"}, false); len(f) == 0 {
			t.Errorf("%s: no finding", name)
		}
	}
	// A page that may decide is not held to the words, only to the rest.
	if f := inspectPage(`<button id="start" name="approve" type="button">x</button><script src="/static/decide.js"></script>`, []string{"button#start"}, true); len(f) != 0 {
		t.Errorf("a decider: %v", f)
	}
}

// TestDecideControlOnlyOnTheCardPage: the Card page is the one decider, and
// its form fails the rule a page that may not decide is held to, so a decide
// control on any other route would fail the inventory.
func TestDecideControlOnlyOnTheCardPage(t *testing.T) {
	if len(deciders) != 1 || !deciders["GET /cards/{id}"] {
		t.Fatalf("deciders %v: the Card page and no other", deciders)
	}
	r := newRig(t)
	s := r.login(t)
	card := r.getWith("/cards/"+testCardID, s).Body.String()
	var controls []string
	for _, e := range inventory {
		if e.pattern == "GET /cards/{id}" {
			controls = e.controls
		}
	}
	if f := inspectPage(card, controls, true); len(f) != 0 {
		t.Errorf("the Card page as a decider: %v", f)
	}
	joined := strings.Join(inspectPage(card, controls, false), "; ")
	for _, want := range []string{"a deciding script", "attribute that looks like deciding id approve", "attribute that looks like deciding id signoff-0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the Card page held as a page that may not decide: no %q in %s", want, joined)
		}
	}
}

// TestSeatCookiesOnlyFromTheLoginAPI checks that no route sets a cookie,
// whatever it is sent, but the three of the login, and those only the
// cookies listed for them, each with the flags of the app's cookies.
func TestSeatCookiesOnlyFromTheLoginAPI(t *testing.T) {
	r := newRig(t)
	for _, e := range inventory {
		method, path, _ := strings.Cut(e.pattern, " ")
		path = strings.TrimSuffix(path, "{$}")
		req := newRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", "a=b")
		w := httptest.NewRecorder()
		r.h.ServeHTTP(w, req)
		allowed := cookieRoutes[e.pattern]
		for _, c := range w.Result().Cookies() {
			ok := false
			for _, name := range allowed {
				ok = ok || c.Name == name
			}
			if !ok {
				t.Errorf("%s sets the cookie %q", e.pattern, c.Name)
			}
			checkCookieFlags(t, e.pattern, c)
		}
	}
	for p := range cookieRoutes {
		found := false
		for _, e := range inventory {
			found = found || e.pattern == p
		}
		if !found {
			t.Errorf("cookie route %q is not a route", p)
		}
	}
}

// formSpy is a testing.TB that notes whether a check failed.
type formSpy struct {
	testing.TB
	failed bool
}

func (f *formSpy) Helper()               {}
func (f *formSpy) Errorf(string, ...any) { f.failed = true }

// The project pages in every state the inventory does not fetch (the summary,
// the finished brief, the project list with a note) hold only their listed
// controls, no decide control, and only plain posts to the project routes;
// and the forms rule sees what it looks for.
func TestProjectPagesInEveryState(t *testing.T) {
	g := &fakeGenesis{}
	r := newRig(t, withGenesis(g))
	s := r.login(t)
	sum := sampleSummary()
	g.set(GenesisVerbRound, &sum, nil)
	body := r.getWith("/projects/"+testThread+"?reset=3&note=changed", s).Body.String()
	for _, f := range inspectPage(body, withNav("a", "form#finish", "button#finish-go", "a#project-list", "a#discard-link", "a#see-plan", "button#logout"), false) {
		t.Errorf("the summary: %s", f)
	}
	checkForms(t, "the summary", body)
	fin := sampleSummary()
	fin.State, fin.Digest = GenesisStateFinished, strings.Repeat("ab", 32)
	g.set(GenesisVerbRound, &fin, nil)
	body = r.getWith("/projects/"+testThread+"?note=done", s).Body.String()
	for _, f := range inspectPage(body, withNav("a#project-list", "a#see-plan", "button#logout"), false) {
		t.Errorf("the finished page: %s", f)
	}
	body = r.getWith("/projects/?note=open", s).Body.String()
	for _, f := range inspectPage(body, withNav("a", "a#back", "button#logout", "form#start-project", "button#start-project-go"), false) {
		t.Errorf("the project list: %s", f)
	}
	// The rule sees a form that is not a plain post to a project route.
	for _, bad := range []string{`<form action="/api/decide" method="post">`, `<form action="/projects/new">`,
		`<form action="/projects/` + testThread + `/approve" method="post">`, `<form action="/projects/new" method="post" target="x">`,
		`<form action="/projects/new" method="post" enctype="multipart/form-data">`} {
		spy := &formSpy{TB: t}
		checkForms(spy, "x", bad)
		if !spy.failed {
			t.Errorf("no finding for %s", bad)
		}
	}
}
