package seatapp

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// authedRoutes are the inventory's routes that need a session, with the path
// each is fetched at.
func authedRoutes() map[string]string {
	out := map[string]string{}
	for _, e := range inventory {
		if e.authed {
			p := strings.TrimSuffix(strings.TrimPrefix(e.pattern, "GET "), "{$}")
			if e.path != "" {
				p = e.path
			}
			out[e.pattern] = p
		}
	}
	return out
}

// TestSeatCardPagesNeedASession: every card page answers a request without a
// live session with a redirect to the login page, and asks the daemon
// nothing; with one, it answers the page, never stored by the browser.
func TestSeatCardPagesNeedASession(t *testing.T) {
	routes := authedRoutes()
	if len(routes) != 3 {
		t.Fatalf("%d card routes in the inventory", len(routes))
	}
	r := newRig(t)
	planted := &http.Cookie{Name: SessionCookie, Value: b64.EncodeToString(make([]byte, 32))}
	ended := r.login(t)
	r.postWith("/api/logout", "{}", ended)
	idle := r.login(t)
	r.clock.Advance(SessionIdle)
	live := r.login(t)
	for pattern, path := range routes {
		for name, cookies := range map[string][]*http.Cookie{
			"no cookie":       nil,
			"planted":         {planted},
			"logged out":      {ended},
			"idle":            {idle},
			"another cookie":  {{Name: "seat", Value: live.Value}},
			"two of the name": {live, live},
		} {
			w := r.getWith(path, cookies...)
			if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login/" {
				t.Errorf("%s, %s: %d, Location %q", pattern, name, w.Code, w.Header().Get("Location"))
			}
			checkHeaders(t, pattern+" "+name, w)
		}
	}
	if n := r.daemon.readCount(); n != 0 {
		t.Errorf("the daemon was asked %d reads without a session", n)
	}
	for pattern, path := range routes {
		w := r.getWith(path, live)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s with a session: %d, Cache-Control %q", pattern, w.Code, w.Header().Get("Cache-Control"))
		}
	}
	if n := r.daemon.readCount(); n != len(routes) {
		t.Errorf("the daemon was asked %d reads, want %d", n, len(routes))
	}
}

// TestSeatNoDecideRoute: no method on any address of a card, and no address
// a decision would use, does anything but answer 404 or 405, and none asks
// the daemon anything. The seat only shows cards; G9 adds the one decide
// form, on the Card page.
func TestSeatNoDecideRoute(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	before := r.daemon.count()
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{
			"/cards/", "/cards/" + testCardID, "/cards/" + testCardID + "/approve", "/cards/" + testCardID + "/deny",
			"/cards/" + testCardID + "/paths/0", "/api/decide", "/api/decide-challenge", "/api/approve", "/api/deny",
			"/api/challenge", "/api/sign-off",
		} {
			req := newRequest(method, path, strings.NewReader("{}"))
			req.Header.Set("Origin", testOrigin)
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(s)
			w := r.recorder(req)
			if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: %d", method, path, w.Code)
			}
		}
	}
	if n := r.daemon.count(); n != before {
		t.Errorf("the daemon was asked %d calls", n-before)
	}
	if len(deciders) != 0 {
		t.Errorf("deciders %v: no page of this app may decide yet", deciders)
	}
}

// hostile is text a lead could write to break a page: markup, a link, a
// script address, terminal controls, a direction override, a line feed and
// a quote.
const hostile = "<script>alert(1)</script><a href=\"https://evil.example/\">tap</a> javascript:alert(2) \x1b[2J\u202eevil\u200b\" onload=x '"

// TestSeatCardWithHostileLeadText: lead text, and the daemon's own words,
// are shown only as plain text, with controls as visible escapes: no markup,
// no link, no script and no control character of theirs reaches the page.
func TestSeatCardWithHostileLeadText(t *testing.T) {
	r := newRig(t)
	c := sampleCard()
	c.Project = "<b>p</b>"
	c.Line = "line \x1b[31m red"
	c.State = "denied"
	c.Plan = []Lead{NewLead(hostile), NewLead(strings.Repeat("x", maxText+1000))}
	c.Note = []Lead{NewLead(hostile)}
	c.Refs[0].Ref = NewLead(hostile)
	c.Diff.Rows[0].Path = NewLead(hostile)
	c.Lists[0].Paths = []Lead{NewLead(hostile)}
	c.Flagged[0].Path, c.Flagged[0].Pattern = NewLead(hostile), NewLead(hostile)
	c.Differs.PathsAdded = []Lead{NewLead(hostile)}
	r.daemon.card = c
	r.daemon.listing.Pending[0].Project = hostile
	r.daemon.listing.Recent[0].Line = hostile
	r.daemon.page.Rows = []PathRow{{Path: NewLead(hostile)}}
	s := r.login(t)
	for _, path := range []string{"/cards/", "/cards/" + testCardID, "/cards/" + testCardID + "/paths/0"} {
		w := r.getWith(path, s)
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		body := w.Body.String()
		for _, bad := range []string{"<script>alert", "<a href=\"https://evil", "<b>", "\x1b", "\u202e", "\u200b", "\" onload"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: the page holds %q", path, bad)
			}
		}
		for _, want := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", `\u001b[2J\u202eevil\u200b`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the page lacks %q", path, want)
			}
		}
		for _, m := range regexp.MustCompile(`(?i)\s(href|src)="([^"]*)"`).FindAllStringSubmatch(body, -1) {
			if !strings.HasPrefix(m[2], "/") || strings.HasPrefix(m[2], "//") {
				t.Errorf("%s: an address that is not the app's: %q", path, m[2])
			}
		}
		// The rest of the inventory's rules hold too. The lead's text names
		// an address and a script address, as text only, which is all the
		// two rules that read the whole page can see; the addresses of the
		// page are checked above.
		for _, f := range inspectPage(body, []string{"a", "a#paths", "a#back", "a#prev", "a#next", "button#logout"}, false) {
			if f != "external address" && f != "javascript address" {
				t.Errorf("%s: %s", path, f)
			}
		}
	}
	body := r.getWith("/cards/"+testCardID, s).Body.String()
	if !strings.Contains(body, fmt.Sprintf("(%d more characters not shown)", 1000)) {
		t.Error("a long line is not cut with a count")
	}
	if !strings.Contains(body, `line \u001b[31m red`) || !strings.Contains(body, "&lt;b&gt;p&lt;/b&gt;") {
		t.Error("the daemon's words are not shown as plain text")
	}
}

// TestSeatTemplatesShowOnlyThroughText: in every template, an action that
// writes a value writes it through text or lead (both Text), or through a
// function that formats the app's own values, or is one of the few fields
// the app itself fills: no template writes a daemon's or a lead's string any
// other way.
func TestSeatTemplatesShowOnlyThroughText(t *testing.T) {
	control := map[string]bool{"if": true, "else": true, "end": true, "range": true, "with": true, "define": true, "template": true, "block": true}
	formatters := map[string]bool{"text": true, "lead": true, "when": true, "until": true, "num": true, "short": true, "cardHref": true, "pathsHref": true}
	// The fields the app fills itself: the page title and the problem's
	// words (fixed words of this package), the relying party (the config),
	// and addresses made by cardHref and pathsHref.
	appFields := map[string]bool{".Title": true, ".Message": true, ".RPID": true, ".RPName": true, ".Prev": true, ".Next": true, ".Back": true}
	action := regexp.MustCompile(`\{\{-?\s*(.*?)\s*-?\}\}`)
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil || len(files) < 7 {
		t.Fatalf("templates %v, %v", files, err)
	}
	for _, f := range files {
		b, _ := templateFS.ReadFile(f)
		for _, m := range action.FindAllStringSubmatch(string(b), -1) {
			a := m[1]
			first, _, _ := strings.Cut(a, " ")
			if control[first] || strings.HasPrefix(a, "/*") || formatters[first] || appFields[a] {
				continue
			}
			t.Errorf("%s: {{%s}} writes a value that is not through Text", f, a)
		}
	}
	// The rule sees what it looks for.
	for _, a := range []string{".Project", "printf \"%s\" .Line", "."} {
		first, _, _ := strings.Cut(a, " ")
		if control[first] || formatters[first] || appFields[a] {
			t.Errorf("the rule takes {{%s}}", a)
		}
	}
}

// TestSeatLeadPrintsThroughText: a Lead printed by fmt, as a template prints
// a value that is not a string, is Text's output, and its text is nowhere
// else.
func TestSeatLeadPrintsThroughText(t *testing.T) {
	l := NewLead("a\x1b[2J\u202eb")
	for _, verb := range []string{"%v", "%s", "%+v", "%#v", "%q", "%x"} {
		if got := fmt.Sprintf(verb, l); got != `a\u001b[2J\u202eb` {
			t.Errorf("%s: %q", verb, got)
		}
	}
	if got := fmt.Sprintf("%v", struct{ L Lead }{l}); strings.ContainsAny(got, "\x1b\u202e") {
		t.Errorf("in a struct: %q", got)
	}
	if !NewLead("").Empty() || NewLead("x").Empty() {
		t.Error("Empty")
	}
}

// TestSeatReadErrors: each error of a read is a page with fixed words and
// its status; the error's text is never shown.
func TestSeatReadErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		words  string
	}{
		{ErrNoCard, 404, "The desk has no card of this name."},
		{ErrCardChanged, 409, "The card changed while it was read."},
		{ErrTooLarge, 413, "This is too large for this phone. Read it at the desk."},
		{ErrNotOffered, 503, "The desk does not show its cards on the phone now."},
		{ErrBusy, 503, "The desk is busy. Try again in a minute."},
		{ErrDaemonUnavailable, 503, "The desk cannot be reached."},
		{fmt.Errorf("wrapped: %w", ErrNoCard), 404, "The desk has no card of this name."},
		{errors.New("a text <i>of the daemon</i>"), 502, "This could not be read."},
	} {
		r := newRig(t)
		r.daemon.readErr = tc.err
		s := r.login(t)
		for path := range map[string]bool{"/cards/": true, "/cards/" + testCardID: true, "/cards/" + testCardID + "/paths/0": true} {
			w := r.getWith(path, s)
			body := w.Body.String()
			if w.Code != tc.status || !strings.Contains(body, tc.words) || strings.Contains(body, "of the daemon") {
				t.Errorf("%v, %s: %d", tc.err, path, w.Code)
			}
			checkHeaders(t, path, w)
			for _, f := range inspectPage(body, []string{"a#back"}, false) {
				t.Errorf("%v, %s: %s", tc.err, path, f)
			}
		}
	}
}

// TestSeatCardAddresses: an id or a page number that is not of its form is
// not asked of the daemon, and a card the daemon answers for another id is
// not shown.
func TestSeatCardAddresses(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	for _, path := range []string{
		"/cards/x", "/cards/AP-1", "/cards/ap-", "/cards/ap--1", "/cards/ap-" + strings.Repeat("a", 49), "/cards/ap-a_b",
		"/cards/" + testCardID + "/paths/x", "/cards/" + testCardID + "/paths/-1", "/cards/" + testCardID + "/paths/01",
		"/cards/" + testCardID + "/paths/65536", "/cards/" + testCardID + "/paths/+1", "/cards/x/paths/0",
	} {
		if w := r.getWith(path, s); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if n := r.daemon.readCount(); n != 0 {
		t.Errorf("the daemon was asked %d reads", n)
	}
	r.daemon.card.ID = "ap-20261122-0002"
	if w := r.getWith("/cards/"+testCardID, s); w.Code != 502 || strings.Contains(w.Body.String(), "0002") {
		t.Errorf("another card: %d", w.Code)
	}
	// A page that is not the one asked, or of no page count, is not shown.
	r.daemon.page.Pages = 1
	if w := r.getWith("/cards/"+testCardID+"/paths/1", s); w.Code != 502 {
		t.Errorf("a page past the last: %d", w.Code)
	}
}

// TestSeatPathsPage: the pages of a path list link to each other and back to
// the card, with the app's own addresses.
func TestSeatPathsPage(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	body := r.getWith("/cards/"+testCardID+"/paths/1", s).Body.String()
	for _, want := range []string{
		`<a id="prev" href="/cards/` + testCardID + `/paths/0">`, `<a id="next" href="/cards/` + testCardID + `/paths/2">`,
		`<a id="back" href="/cards/` + testCardID + `">`, "page 2 of 3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page 1 lacks %q", want)
		}
	}
	first := r.getWith("/cards/"+testCardID+"/paths/0", s).Body.String()
	last := r.getWith("/cards/"+testCardID+"/paths/2", s).Body.String()
	if strings.Contains(first, `id="prev"`) || strings.Contains(last, `id="next"`) {
		t.Error("a link past the ends")
	}
}

// TestSeatReadsAreBounded: the reads of one credential's sessions are
// bounded, 20 a minute, so that one phone cannot spend the gate's rate; a
// read past the bound asks the daemon nothing.
func TestSeatReadsAreBounded(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	other := r.login(t) // another session of the same credential
	for i := 0; i < readRequests; i++ {
		c := s
		if i%2 == 1 {
			c = other
		}
		if w := r.getWith("/cards/", c); w.Code != 200 {
			t.Fatalf("read %d: %d", i, w.Code)
		}
	}
	w := r.getWith("/cards/"+testCardID, s)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "Wait a minute") {
		t.Errorf("past the bound: %d", w.Code)
	}
	if n := r.daemon.readCount(); n != readRequests {
		t.Errorf("the daemon was asked %d reads", n)
	}
	r.clock.Advance(readWindow)
	if w := r.getWith("/cards/", s); w.Code != 200 {
		t.Errorf("a minute later: %d", w.Code)
	}
}

// TestSeatCardPagesShow: the card list and the card page show what a phone
// needs: the STAND-IN banner, that the seat only shows cards, the held mark,
// the lead's note under its label, and the link to the whole path list only
// when the card's list is cut; and they are well formed for a phone.
func TestSeatCardPagesShow(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	list := r.getWith("/cards/", s).Body.String()
	card := r.getWith("/cards/"+testCardID, s).Body.String()
	for name, body := range map[string]string{"list": list, "card": card} {
		for _, want := range []string{
			"<strong>STAND-IN</strong>", "Nothing here is a real approval.", "Approve or deny at the desk.",
			`<button id="logout" type="button" class="quiet" hidden>Log out</button>`, `<script src="/static/session.js"></script>`,
			`<html lang="en">`, `<meta name="viewport" content="width=device-width, initial-scale=1">`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
		if strings.Count(body, "<h1") != 1 || strings.Count(body, "<main") != 1 {
			t.Errorf("%s: not one h1 and one main", name)
		}
	}
	for _, want := range []string{`<a href="/cards/` + testCardID + `">`, "Held: decided past the limit.", "Ends in 3 h.", "2 more waiting cards", "denied at the desk"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	for _, want := range []string{"Lead-stated, not checked.", "Two lines\nof a note.", "Read the whole path list", "Flagged paths", "Each needs its own sign-off at the desk.", "From ap-20261121-0003"} {
		if !strings.Contains(card, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	r.daemon.card.Diff.Truncated = false
	if strings.Contains(r.getWith("/cards/"+testCardID, s).Body.String(), `id="paths"`) {
		t.Error("the path list link on a card whose list is whole")
	}
	r.daemon.listing = Listing{}
	empty := r.getWith("/cards/", s).Body.String()
	if !strings.Contains(empty, "No card is waiting.") || !strings.Contains(empty, "Nothing recent.") {
		t.Error("the empty list says nothing")
	}
}

// TestSeatLoginGoesToTheCards: a login sends the phone to the card list.
func TestSeatLoginGoesToTheCards(t *testing.T) {
	r := newRig(t)
	lc := r.startLogin(t)
	w := r.postWith("/api/login", assertionBody(), lc)
	if strings.TrimSpace(w.Body.String()) != `{"next":"/cards/"}` {
		t.Errorf("login answer %s", w.Body)
	}
}

// TestSeatSessionScript: the logout script posts to the app's own address
// with the session's cookie and sets no text but by the browser.
func TestSeatSessionScript(t *testing.T) {
	js := newRig(t).do("GET", "/static/session.js", nil, nil).Body.String()
	for _, want := range []string{`"/api/logout"`, `credentials: "same-origin"`, `referrerPolicy: "strict-origin"`, `location.assign("/login/")`} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	for _, bad := range []string{"innerHTML", "document.write", "eval(", "http://", "https://", "document.cookie", "no-referrer"} {
		if strings.Contains(js, bad) {
			t.Errorf("the script holds %q", bad)
		}
	}
}

// TestSeatTimes: how long until a card ends reads in words.
func TestSeatTimes(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		-time.Second: "ended", 0: "ended", 30 * time.Second: "in less than a minute", 5 * time.Minute: "in 5 min",
		2 * time.Hour: "in 2 h", 2*time.Hour + 7*time.Minute: "in 2 h 7 min",
	} {
		if got := until(now, now.Add(d)); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
	if when(time.Time{}) != "unknown" || when(now) != "2026-11-22 12:00 UTC" {
		t.Error("when")
	}
	if short("") != "none" || short(strings.Repeat("a", 40)) != strings.Repeat("a", 12) {
		t.Error("short")
	}
}

// recorder serves a request and returns the answer.
func (r *rig) recorder(req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}
