package seatapp

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The tests of /static/watch.js read its text: the repository has no
// JavaScript engine. They check what the text must never hold, what it must
// hold, and that it handles every state the seat's poll answers, which they
// read from watchpoll.go so that the two stay in step.

func watchScript(t *testing.T) string {
	t.Helper()
	w := newRig(t).do("GET", "/static/watch.js", nil, nil)
	if w.Code != 200 {
		t.Fatalf("watch.js: status %d", w.Code)
	}
	return w.Body.String()
}

// TestSeatWatchScriptIsSafe: no markup is built from text, nothing is
// evaluated, no inline script is made, every text goes through textContent,
// and the one fetch is the app's own request: same origin, no redirect, no
// cache, JSON.
func TestSeatWatchScriptIsSafe(t *testing.T) {
	js := watchScript(t)
	for _, bad := range []string{
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "Function(", "new Function",
		"setAttribute(\"on", "createContextualFragment", "DOMParser", "srcdoc", "javascript:", "importScripts",
		"XMLHttpRequest", "WebSocket", "EventSource", "sendBeacon", "localStorage", "sessionStorage", "document.cookie",
		"setInterval", "createElement(\"script\")", "innerText",
	} {
		if strings.Contains(js, bad) {
			t.Errorf("watch.js holds %q", bad)
		}
	}
	// A string handed to a timer is code.
	if regexp.MustCompile(`setTimeout\(\s*["']`).MatchString(js) {
		t.Error("watch.js hands a string to a timer")
	}
	for _, want := range []string{
		"textContent", `"use strict"`,
		`credentials: "same-origin"`, `redirect: "error"`, `cache: "no-store"`, `referrerPolicy: "strict-origin"`,
		`method: "POST"`, `"Content-Type": "application/json"`, "JSON.stringify", "AbortController", "signal: ctl.signal",
		"visibilitychange", "document.hidden", "location.reload()",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	if externalRe.MatchString(js) {
		t.Error("watch.js holds an external address")
	}
}

// TestSeatWatchScriptReferencesOnlyItsOwnAddresses: the one address it posts
// to is /api/watch; the two it links to are the login page and the card list.
func TestSeatWatchScriptReferencesOnlyItsOwnAddresses(t *testing.T) {
	js := watchScript(t)
	api := regexp.MustCompile(`/api/[A-Za-z0-9_-]*`).FindAllString(js, -1)
	if len(api) == 0 {
		t.Fatal("watch.js names no /api address")
	}
	for _, a := range api {
		if a != "/api/watch" {
			t.Errorf("watch.js names %q", a)
		}
	}
	// Every quoted path of the script is one of the three.
	for _, m := range regexp.MustCompile(`"(/[^"]*)"`).FindAllStringSubmatch(js, -1) {
		switch m[1] {
		case "/api/watch", "/login/", "/cards/":
		default:
			t.Errorf("watch.js names the address %q", m[1])
		}
	}
	if n := strings.Count(js, "fetch("); n != 1 {
		t.Errorf("watch.js calls fetch %d times", n)
	}
	if strings.Contains(js, "location.assign") || strings.Contains(js, "location.href") || strings.Contains(js, "location.replace") {
		t.Error("watch.js moves the page by a way other than a reload")
	}
}

// TestSeatWatchScriptWords: the status line's words are the constants the
// design names, and the script never takes a text from the seat's answer.
func TestSeatWatchScriptWords(t *testing.T) {
	js := watchScript(t)
	for _, want := range []string{
		`"Not live"`, `"Not live: the desk is out of reach"`, `"Session ended. Log in again."`,
		`"` + watchBanner + `"`, `"Reload"`, `"Back to the cards"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %s", want)
		}
	}
	// The only members of an answer the script reads are the three of
	// watchAnswer that it acts on; no text of the answer is shown.
	for _, m := range regexp.MustCompile(`\bdata\.([A-Za-z_]+)`).FindAllStringSubmatch(js, -1) {
		switch m[1] {
		case "state", "version", "card":
		default:
			t.Errorf("watch.js reads data.%s of an answer", m[1])
		}
	}
	for _, m := range regexp.MustCompile(`(?s)(?:textContent|createTextNode)\s*=?\s*\(?\s*([^;)]*)`).FindAllStringSubmatch(js, -1) {
		if strings.Contains(m[1], "data.") || strings.Contains(m[1], "resp") {
			t.Errorf("watch.js sets a text from the answer: %q", m[1])
		}
	}
	// A script that is not a decide script holds no word of deciding but the
	// banner, and names no control of the decision form.
	if mentionsDeciding(js) {
		t.Error("watch.js mentions deciding beyond its banner")
	}
	for _, id := range []string{"approve", "deny", "reason", "signoff", "confirm", "#decide", `"decide"`} {
		if strings.Contains(strings.ReplaceAll(js, watchBanner, ""), id) {
			t.Errorf("watch.js names %q", id)
		}
	}
}

// constsOf returns the string constants of watchpoll.go whose names start
// with prefix, as value by name.
func constsOf(t *testing.T, prefix string) map[string]string {
	t.Helper()
	src, err := os.ReadFile("watchpoll.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	re := regexp.MustCompile(`(?m)^\s*(` + prefix + `[A-Za-z]+)\s*=\s*"([a-z]+)"\s*$`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = m[2]
	}
	return out
}

// TestSeatWatchScriptHandlesEveryAnswer: every state and every card state
// watchpoll.go can answer has a case in the script, the request carries the
// keys the handler decodes, and the script reads the keys the handler writes.
func TestSeatWatchScriptHandlesEveryAnswer(t *testing.T) {
	js := watchScript(t)
	states, cards := constsOf(t, "watch"), constsOf(t, "card")
	// The constants of the limits are numbers or durations, not strings, so
	// they are not in the maps; the states are five and the card states four.
	if len(states) != 5 || len(cards) != 4 {
		t.Fatalf("watchpoll.go: states %v, card states %v", states, cards)
	}
	var names []string
	for _, v := range states {
		names = append(names, v)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "changed,ended,offline,replaced,same" {
		t.Errorf("the answer states are %v: this test and watch.js need to follow", names)
	}
	for _, v := range states {
		if !strings.Contains(js, `case "`+v+`":`) {
			t.Errorf("watch.js has no case for the answer state %q", v)
		}
	}
	for _, v := range cards {
		if !strings.Contains(js, `case "`+v+`":`) && !strings.Contains(js, `cs === "`+v+`"`) {
			t.Errorf("watch.js does not handle the card state %q", v)
		}
	}
	// A replaced poll is waited out for a hold's length and does not fall
	// through to the next poll's gap, so that two tabs never end each
	// other's polls in turn.
	if !regexp.MustCompile(`case "replaced":[^}]*?schedule\(REPLACED_WAIT\);\s*return;`).MatchString(js) {
		t.Error(`watch.js does not wait out REPLACED_WAIT and return in its case "replaced"`)
	}
	// The keys of the request and the answer, from the structs' tags.
	src, err := os.ReadFile("watchpoll.go")
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string]bool{}
	for _, m := range regexp.MustCompile("`json:\"([a-z]+)\"`").FindAllStringSubmatch(string(src), -1) {
		tags[m[1]] = true
	}
	for _, k := range []string{"version", "card", "binding", "state", "waiting"} {
		if !tags[k] {
			t.Fatalf("watchpoll.go has no json key %q: this test needs to follow", k)
		}
	}
	for _, want := range []string{"body = { version: version }", "body.card = card", "body.binding = binding", "data.state", "data.version", "data.card"} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	// The statuses of the transport, and the waits: 429 backs off a minute,
	// an error five seconds doubling to a minute, offline thirty seconds,
	// a replaced poll a hold's length,
	// a request is cut at thirty seconds, and a reload waits ten.
	for _, want := range []string{
		"resp.status === 429", "LIMITED_WAIT = 60000", "BACKOFF_FIRST = 5000", "BACKOFF_LAST = 60000", "backoff * 2",
		"OFFLINE_WAIT = 30000", "REPLACED_WAIT = 25000", "REQUEST_TIMEOUT = 30000", "RELOAD_GAP = 10000",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
}

// TestSeatWatchScriptLiveAndKeepsQuiet: a page that is not live gets the
// quiet line and no poll; an ended session stops the polling; the card page
// has no reload but the owner's button.
func TestSeatWatchScriptLiveAndKeepsQuiet(t *testing.T) {
	js := watchScript(t)
	for _, want := range []string{`root.dataset.live === "yes"`, "say(TEXT.notLive);", "stopped = true;", "data.state"} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	// The one data-live value the template writes for a live page is "yes".
	if !strings.Contains(partsTemplate(t), `{{if .Live}}yes{{else}}no{{end}}`) {
		t.Error("the watch partial no longer writes yes for a live page")
	}
	// location.reload appears in the list's timer and in the banner's
	// button, and nowhere else.
	if n := strings.Count(js, "location.reload()"); n != 2 {
		t.Errorf("location.reload() appears %d times", n)
	}
	// The list reloads only through wantReload, which is called for a list
	// page's changed answer alone (a page marked still is no list).
	if n := strings.Count(js, "wantReload()"); n != 2 { // its definition and its one call
		t.Errorf("wantReload appears %d times", n)
	}
	if !regexp.MustCompile(`(?s)\} else \{\s*quiet\(\);\s*if \(data\.state === "changed" && !still\) \{ wantReload\(\); \}`).MatchString(js) {
		t.Error("watch.js reloads other than from the list page's changed answer")
	}
	// The reload waits for the page's age, and for a field without typed text.
	for _, want := range []string{"loadedAt + RELOAD_GAP", "typing()", "reloadTimer !== null"} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
}

func partsTemplate(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("templates/parts.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSeatWatchPagesCarryTheStatusLine: both pages hold the one status
// element, empty, polite, before the script that fills it.
func TestSeatWatchPagesCarryTheStatusLine(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	const line = `<p id="watch-status" role="status" aria-live="polite"></p>`
	for _, path := range []string{"/cards/", "/cards/" + testCardID} {
		body := r.getWith(path, s).Body.String()
		if n := strings.Count(body, line); n != 1 {
			t.Errorf("%s: %d status lines", path, n)
		}
		if strings.Index(body, line) > strings.Index(body, `<script src="/static/watch.js"></script>`) {
			t.Errorf("%s: the status line is after the script", path)
		}
		if n := strings.Count(body, `id="watch-status"`); n != 1 {
			t.Errorf("%s: id watch-status %d times", path, n)
		}
	}
}

// TestSeatDecisionControlsCarryTheOfferMark: every control of the decision
// form carries data-offer, which is how watch.js turns them off without
// naming them, and nothing else on the Card page does, nor does the list.
func TestSeatDecisionControlsCarryTheOfferMark(t *testing.T) {
	r := newRig(t)
	c := sampleCard()
	c.Flagged = append(c.Flagged, Flagged{Kind: "symlink", Reason: "a symbolic link", Path: NewLead("link")})
	r.daemon.card = c
	s := r.login(t)
	body := r.getWith("/cards/"+testCardID, s).Body.String()
	offered := map[string]bool{}
	for _, m := range tagRe.FindAllStringSubmatch(body, -1) {
		if m[1] != "" {
			continue
		}
		attrs := map[string]string{}
		has := map[string]bool{}
		for _, a := range attrRe.FindAllStringSubmatch(m[3], -1) {
			attrs[strings.ToLower(a[1])] = a[2] + a[3] + a[4]
			has[strings.ToLower(a[1])] = true
		}
		if has["data-offer"] {
			offered[attrs["id"]] = true
			if attrs["data-offer"] != "open" {
				t.Errorf("%s: data-offer is %q", attrs["id"], attrs["data-offer"])
			}
		}
	}
	want := []string{"approve", "deny", "reason", "signoff-0", "signoff-1", "confirm-go"}
	for _, id := range want {
		if !offered[id] {
			t.Errorf("the control %s lacks data-offer", id)
		}
		delete(offered, id)
	}
	for id := range offered {
		t.Errorf("%q carries data-offer", id)
	}
	for _, path := range []string{"/cards/", "/cards/" + testCardID + "/paths/1"} {
		if strings.Contains(r.getWith(path, s).Body.String(), "data-offer") {
			t.Errorf("%s carries data-offer", path)
		}
	}
	// The Card page's own script keeps the controls off once the live script
	// has marked the document, and the live script sets the mark.
	dec := r.do("GET", "/static/decide.js", nil, nil).Body.String()
	if !strings.Contains(dec, `document.documentElement.dataset.stale === "yes"`) {
		t.Error("decide.js does not read the stale mark")
	}
	if !strings.Contains(watchScript(t), `document.documentElement.dataset.stale = "yes"`) ||
		!strings.Contains(watchScript(t), `querySelectorAll("[data-offer]")`) || !strings.Contains(watchScript(t), "el.disabled = true") {
		t.Error("watch.js does not turn the offered controls off and set the stale mark")
	}
}

// TestSeatMentionsDecidingRule: the rule for a script that is not a decide
// script takes out the banner sentence and nothing else.
func TestSeatMentionsDecidingRule(t *testing.T) {
	if mentionsDeciding("var a = \"" + watchBanner + "\";") {
		t.Error("the banner sentence is flagged")
	}
	for _, js := range []string{
		`var b = document.getElementById("approve");`,
		`x("Denied");`, `x("deny")`, `var d = "decision";`,
		"var a = \"" + watchBanner + "\"; var b = \"approve\";",
		`"This card was decided elsewhere."`,
	} {
		if !mentionsDeciding(js) {
			t.Errorf("not flagged: %s", js)
		}
	}
}
