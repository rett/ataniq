package seatapp

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The tests of the Leads half of /static/watch.js. Like the others they read
// the script's text, as the repository has no JavaScript engine.

// TestSeatWatchScriptLeadsMatchesTheSeat: a Leads page is told by its mark,
// never by its address; it opens the stream with the cursor it was rendered
// with, acts on the Leads kind of event alone, reads the cursor as the seat
// writes it, and never polls.
func TestSeatWatchScriptLeadsMatchesTheSeat(t *testing.T) {
	js := watchScript(t)
	for _, want := range []string{
		`var onLeads = root.dataset.view === "leads";`,
		`var renderedChat = onLeads && validCursor(root.dataset.chat || "") ? root.dataset.chat : "0";`,
		`var conversation = onLeads && root.dataset.page === "conversation";`,
		`var followAfter = conversation && /^[0-9]{1,16}$/.test(root.dataset.follow || "") ? root.dataset.follow : "";`,
		`var streamKind = onLeads ? STREAM_KIND_CHAT : STREAM_KIND;`,
		"var MAX_CURSOR = " + strconv.FormatInt(MaxChatSeq, 10) + ";",
		"if (stopped || inflight || document.hidden || onLeads) { return; }",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	// The page's address decides nothing: its path is read only to link a
	// conversation's offer, and its following read, to the conversation's
	// page that holds its newest entry, and only when it is a
	// conversation's path; the address without its fragment, only to tell
	// whether that page is this one.
	if n := strings.Count(js, "location.pathname"); n != 3 || strings.Count(funcBody(t, js, "newestPage"), "location.pathname") != 2 ||
		!strings.Contains(funcBody(t, js, "newestPage"), "CHAT_PAGE.test(location.pathname)") ||
		!strings.Contains(js, "function here() { return location.pathname + location.search; }") {
		t.Errorf("watch.js reads its address other than for the offer's link (%d times)", n)
	}
	if n := strings.Count(js, "location.search"); n != 1 {
		t.Errorf("watch.js reads its query %d times", n)
	}
	if strings.Contains(js, "location.hash") || strings.Contains(js, "document.URL") {
		t.Error("watch.js reads its address")
	}
	// The cursor's form is the seat's: 0 to 2^53-1, decimal, no sign, no
	// leading zero.
	vc := funcBody(t, js, "validCursor")
	if !strings.Contains(vc, `/^(0|[1-9][0-9]{0,15})$/.test(s) && Number(s) <= MAX_CURSOR`) {
		t.Errorf("validCursor: %s", vc)
	}
	// chatHinted takes the data whole, a cursor and nothing else, puts none
	// of it on the page, and fails the stream for any other form.
	h := funcBody(t, js, "chatHinted")
	for _, want := range []string{`if (!validCursor(text) || text === "0") { return false; }`, "if (text !== renderedChat) { chatChanged(); }"} {
		if !strings.Contains(h, want) {
			t.Errorf("chatHinted lacks %q", want)
		}
	}
	for _, bad := range []string{"JSON.parse", "textContent", "say(", "createTextNode", "dataset", "location", "renderedChat ="} {
		if strings.Contains(h, bad) {
			t.Errorf("chatHinted holds %q", bad)
		}
	}
	// The rendered cursor never moves.
	if n := len(regexp.MustCompile(`\brenderedChat\s*=[^=]`).FindAllStringIndex(js, -1)); n != 1 {
		t.Errorf("the rendered cursor is set %d times", n)
	}
	// No poll of the chat: no timer of its own, and a failed stream turns to
	// no other request. A failure drops a following read waiting its turn,
	// and the change pending, so that a page that says it is not live moves
	// no more.
	fb := funcBody(t, js, "fallBack")
	if !strings.Contains(fb, "if (onLeads && !offerShown) { say(TEXT.notLive); }") ||
		!strings.Contains(fb, "if (onLeads) {\n      cancelReload();\n      pendingChange = false;\n    }") {
		t.Errorf("fallBack does not keep a Leads page to its Refresh link: %s", fb)
	}
	// A browser that cannot read the stream: a Leads page, which has no
	// poll, says it is not live and asks nothing, as a page made not live.
	if !strings.Contains(js, "if (!live || (onLeads && !streamable)) {\n    say(TEXT.notLive);\n    return;\n  }\n  next();") {
		t.Error("a Leads page that cannot stream does not say it is not live")
	}
	if strings.Contains(js, "setInterval") || strings.Count(js, "fetch(") != 1 {
		t.Error("watch.js has a request or timer of its own for the chat")
	}
}

// TestSeatWatchScriptLeadsOffersAndFollows: the directory and a conversation
// that does not follow offer a plain link in the polite status line, once,
// and move nothing; a conversation follows only when the seat marked it as
// showing the newest entries and its newest entry is in view, by the card
// list's reload rule; no hint moves the focus or scrolls.
func TestSeatWatchScriptLeadsOffersAndFollows(t *testing.T) {
	js := watchScript(t)
	for _, want := range []string{
		`leadsChanged: "Leads changed."`, `refresh: "Refresh"`, `newReplies: "New replies."`, `showNewest: "Show newest"`,
		`var LEADS_PATH = "/leads/";`, `var NEWEST = "#newest";`,
		`var CHAT_PAGE = /^\/leads\/daemon\/[a-z0-9][a-z0-9-]{0,31}$/;`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("watch.js lacks %q", want)
		}
	}
	// The key of a conversation's path is the seat's.
	if chatKey.String() != `^[a-z0-9][a-z0-9-]{0,31}$` {
		t.Errorf("the chat key's form moved: %s; CHAT_PAGE needs to follow", chatKey)
	}
	of := funcBody(t, js, "offer")
	for _, want := range []string{
		"if (offerShown) { return; }", "offerShown = true;",
		"say(TEXT.leadsChanged, [link(TEXT.refresh, LEADS_PATH)]);",
		"say(TEXT.newReplies, [link(TEXT.showNewest, newestTarget())]);",
	} {
		if !strings.Contains(of, want) {
			t.Errorf("offer lacks %q", want)
		}
	}
	for _, bad := range []string{"location.", "wantReload", "reloadNow", "addEventListener"} {
		if strings.Contains(of, bad) {
			t.Errorf("offer holds %q", bad)
		}
	}
	// "Show newest" goes to the page that holds the newest entry: never a
	// fragment alone on this page, which would only move the view on a page
	// that does not hold the new replies.
	if nt := funcBody(t, js, "newestTarget"); !strings.Contains(nt, "return page === here() ? page : page + NEWEST;") {
		t.Errorf("newestTarget: %s", nt)
	}
	// Nothing moves the focus or the view.
	for _, bad := range []string{".focus(", "scrollIntoView", "scrollTo(", "scrollBy(", "autofocus", "tabIndex"} {
		if strings.Contains(js, bad) {
			t.Errorf("watch.js holds %q", bad)
		}
	}
	fo := funcBody(t, js, "following")
	for _, want := range []string{`if (followAfter === "") { return false; }`, `document.getElementById("newest")`,
		"return box.bottom > 0 && box.top < window.innerHeight;"} {
		if !strings.Contains(fo, want) {
			t.Errorf("following lacks %q", want)
		}
	}
	if !strings.Contains(funcBody(t, js, "reloadNow"), "if (page !== here()) { location.replace(page + NEWEST); return; }") {
		t.Error("reloadNow does not read the conversation's following page")
	}
	// A conversation reads again by itself only while it follows and no
	// control holds the focus: looked at when the change comes and again
	// when the read is due, which otherwise shows the offer. A focused
	// control is any element but the body (and the document's element).
	if !strings.Contains(funcBody(t, js, "wantReload"), "if (onLeads && (!following() || focused())) { offer(); return; }") {
		t.Error("a conversation's following read does not look at the reader's place and focus when it is due")
	}
	if fc := funcBody(t, js, "focused"); !strings.Contains(fc, "var el = document.activeElement;") ||
		!strings.Contains(fc, "return !!el && el !== document.body && el !== document.documentElement;") {
		t.Errorf("focused: %s", fc)
	}
	// reloadNow runs only from wantReload's timer, which waits out the
	// page's age, a hidden page, typed text and an open dialog. A
	// conversation's age is forty seconds: its read is 2 calls of chat's
	// room, and one every 40 seconds keeps one conversation inside the
	// room's 5 in 80 seconds with a call to spare.
	m := regexp.MustCompile(`var FOLLOW_GAP = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("watch.js has no FOLLOW_GAP")
	}
	if gap, _ := strconv.Atoi(m[1]); gap < 40000 {
		t.Errorf("the following read's gap is %d ms", gap)
	}
	if !strings.Contains(funcBody(t, js, "wantReload"), "if (onLeads) { wait = Math.max(0, loadedAt + FOLLOW_GAP - Date.now()); }") {
		t.Error("a conversation's following read does not wait out FOLLOW_GAP")
	}
	if n := strings.Count(js, "reloadNow()"); n != 2 {
		t.Errorf("reloadNow appears %d times", n)
	}
	hd := funcBody(t, js, "holding")
	if !strings.Contains(hd, "typing()") || !strings.Contains(hd, `dialog[open]`) {
		t.Errorf("holding: %s", hd)
	}
	// The offer stays through the stream's rotations: quiet leaves it.
	if !strings.Contains(funcBody(t, js, "quiet"), "} else if (!offerShown) { say(\"\"); }") {
		t.Error("quiet clears a Leads page's offer")
	}
}

// TestSeatWatchScriptLeadsWordsAreConstants: the Leads texts are constants,
// and no word of deciding comes with them.
func TestSeatWatchScriptLeadsWordsAreConstants(t *testing.T) {
	js := watchScript(t)
	for _, m := range regexp.MustCompile(`(?s)say\(([^,;)]*)`).FindAllStringSubmatch(js, -1) {
		arg := strings.TrimSpace(m[1])
		if arg != `""` && !strings.HasPrefix(arg, "TEXT.") && arg != "text" {
			t.Errorf("say is given %q", arg)
		}
	}
	if mentionsDeciding(js) {
		t.Error("watch.js mentions deciding beyond its banner")
	}
}
