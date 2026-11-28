package seatlink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatgate"
)

// postForm is a page's form posted by the browser: same origin, the form's
// content type, and the browser's cookies.
func (b *browser) postForm(path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Host = seatRPID
	req.Header.Set("Origin", seatOrigin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	b.h.ServeHTTP(w, req)
	return w
}

// The whole interview on loopback: a phone logs in, starts a project from the
// card list, answers its rounds on the question pages, reads the summary,
// changes an answer and finishes, through the seat app, the link, the gate
// and a real thread store under catalog v1. Each post is answered with a
// redirect, and an answer and the page after it are one call of the gate.
func TestSeatLinkGenesisOverTheSeatApp(t *testing.T) {
	seat := newIdentity(t, "seat")
	store := newStoreDesk(t)
	var desk *fakeDesk
	g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"pair", "read", "login", "genesis"},
		func(ctx context.Context, peer seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
			switch op {
			case OpLoginChallenge, OpLogin:
				return desk.handle(ctx, peer, op, params)
			case "owner.list":
				return ownerwire.EncodeListing(ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}})
			}
			return store.handle(ctx, peer, op, params)
		})
	desk = newFakeDesk(g.id.Fingerprint())
	phone, err := webauthntest.New(seatRPID, seatOrigin)
	if err != nil {
		t.Fatal(err)
	}
	desk.pair(t, phone)
	d := dialPinned(t, g, seat)
	br := newBrowser(seatApp(t, d))
	if w := br.loginWith(t, phone); w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	cards := br.do("GET", "/cards/", "")
	if cards.Code != 200 || !strings.Contains(cards.Body.String(), `<form id="start-project" action="/projects/new" method="post">`) {
		t.Fatalf("the card list: %d\n%s", cards.Code, cards.Body)
	}
	w := br.postForm("/projects/new", nil)
	loc := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || !regexp.MustCompile(`^/projects/[0-9a-f]{16}$`).MatchString(loc) {
		t.Fatalf("the start: %d to %q", w.Code, loc)
	}
	thread := strings.TrimPrefix(loc, "/projects/")
	// The rounds of the web app path, as the forms send them.
	rounds := []url.Values{
		{"n0": {"kind"}, "a0": {"software"}},
		{"n0": {"software.sub_kind"}, "a0": {"web_app"}, "n1": {"goal"}, "a1": {"own_use"}, "n2": {"audience"}, "a2": {"me"}},
		{"n0": {"software.stack"}, "a0": {"~other"}, "t0": {"Go and a <small> database"}, "n1": {"software.data"}, "a1": {"~host"}, "n2": {"software.auth"}, "a2": {"passkey"}},
		{"n0": {"software.deploy_kind"}, "a0": {"local"}, "n1": {"hosts"}, "a1": {"this_computer"}, "n2": {"budget_band"}, "a2": {"none"}},
		{"n0": {"deliverables"}, "m0": {"first_version"}, "n1": {"quality_bar"}, "a1": {"solid"}, "n2": {"schedule"}, "a2": {"~skip"}},
		{"n0": {"review_points"}, "m0": {"plan", "demo"}},
	}
	for i, form := range rounds {
		page := br.do("GET", "/projects/"+thread, "")
		body := page.Body.String()
		if page.Code != 200 || !strings.Contains(body, "Round "+string(rune('1'+i))+" of 6.") {
			t.Fatalf("round %d's page: %d\n%s", i+1, page.Code, body)
		}
		// The page's form holds each node of the round, and its round.
		for k := 0; k < 3; k++ {
			if n := form.Get("n" + string(rune('0'+k))); n != "" && !strings.Contains(body, `value="`+n+`"`) {
				t.Fatalf("round %d's page lacks the node %s", i+1, n)
			}
		}
		form.Set("round", string(rune('1'+i)))
		before := g.calls.Load()
		w := br.postForm("/projects/"+thread+"/answer", form)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/projects/"+thread {
			t.Fatalf("round %d's answer: %d to %q", i+1, w.Code, w.Header().Get("Location"))
		}
		// The page the post redirects to is answered from the link's
		// memory: the answer was the one call.
		next := br.do("GET", "/projects/"+thread, "")
		if next.Code != 200 || g.calls.Load()-before != 1 {
			t.Fatalf("round %d: %d calls of the gate for the answer and its page", i+1, g.calls.Load()-before)
		}
	}
	sum := br.do("GET", "/projects/"+thread, "").Body.String()
	for _, want := range []string{"What you chose", `<q class="owners">Go and a &lt;small&gt; database</q>`, "(your own words)",
		`href="/projects/` + thread + `/change/kind"`, `<form id="finish"`} {
		if !strings.Contains(sum, want) {
			t.Fatalf("the summary lacks %q:\n%s", want, sum)
		}
	}
	// A change of the goal: the change page shows the catalog's question.
	cp := br.do("GET", "/projects/"+thread+"/change/goal", "").Body.String()
	if !strings.Contains(cp, `value="share"`) || !strings.Contains(cp, `<input type="hidden" id="node" name="node" value="goal">`) {
		t.Fatalf("the change page:\n%s", cp)
	}
	w = br.postForm("/projects/"+thread+"/change", url.Values{"node": {"goal"}, "a0": {"share"}})
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/projects/"+thread+"?") {
		t.Fatalf("the change: %d to %q", w.Code, w.Header().Get("Location"))
	}
	// Secret-shaped words are refused, and never shown back.
	w = br.postForm("/projects/"+thread+"/change", url.Values{"node": {"software.stack"}, "a0": {"~other"}, "t0": {"-----BEGIN OPENSSH PRIVATE KEY-----"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/projects/"+thread+"/change/software.stack?note=secret" {
		t.Fatalf("a secret: %d to %q", w.Code, w.Header().Get("Location"))
	}
	if page := br.do("GET", w.Header().Get("Location"), "").Body.String(); strings.Contains(page, "OPENSSH") || !strings.Contains(page, "password or key") {
		t.Fatalf("the page after a secret:\n%s", page)
	}
	// The summary links the draft plan; the plan page is one call of the
	// gate, and a reload of it none.
	sum = br.do("GET", "/projects/"+thread, "").Body.String()
	if !strings.Contains(sum, `<a id="see-plan" href="/projects/`+thread+`/plan">See the draft plan</a>`) {
		t.Fatalf("the summary after the change lacks the plan's link:\n%s", sum)
	}
	before := g.calls.Load()
	draft := br.do("GET", "/projects/"+thread+"/plan", "")
	if draft.Code != 200 || g.calls.Load()-before != 1 {
		t.Fatalf("the draft plan page: %d, %d calls of the gate\n%s", draft.Code, g.calls.Load()-before, draft.Body)
	}
	tag := regexp.MustCompile(`Plan <span class="mono">([0-9a-f]{12})</span>`)
	dm := tag.FindStringSubmatch(draft.Body.String())
	for _, want := range []string{"<h1>Draft plan</h1>", `A full web app</strong>, <span class="mono">` + thread[:6] + `</span>`,
		"This is a draft; finishing the interview keeps it as revision 1.", "What a charter would cover",
		`<q class="owners">Go and a &lt;small&gt; database</q>`} {
		if !strings.Contains(draft.Body.String(), want) {
			t.Fatalf("the draft plan page lacks %q:\n%s", want, draft.Body)
		}
	}
	if dm == nil {
		t.Fatalf("the draft plan page has no digest tag:\n%s", draft.Body)
	}
	before = g.calls.Load()
	if again := br.do("GET", "/projects/"+thread+"/plan", ""); again.Code != 200 || g.calls.Load() != before {
		t.Fatalf("the draft plan page reloaded: %d, %d calls", again.Code, g.calls.Load()-before)
	}
	// Finish: the finished page and its digest; the card list lists nothing
	// in progress.
	if w = br.postForm("/projects/"+thread+"/finish", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("the finish: %d", w.Code)
	}
	fin := br.do("GET", "/projects/"+thread, "").Body.String()
	if !strings.Contains(fin, "Nothing has started, and no agent was given the brief yet.") || strings.Contains(fin, "<form") ||
		!strings.Contains(fin, `<a id="see-plan" href="/projects/`+thread+`/plan">See the plan</a>`) {
		t.Fatalf("the finished page:\n%s", fin)
	}
	// The plan kept at finish: revision 1, with the draft's digest.
	kept := br.do("GET", "/projects/"+thread+"/plan", "")
	km := tag.FindStringSubmatch(kept.Body.String())
	if kept.Code != 200 || !strings.Contains(kept.Body.String(), "<h1>Plan, revision 1</h1>") || strings.Contains(kept.Body.String(), "This is a draft") ||
		km == nil || km[1] != dm[1] {
		t.Fatalf("the kept plan's page (draft tag %v):\n%s", dm, kept.Body)
	}
	if body := br.do("GET", "/cards/", "").Body.String(); !strings.Contains(body, "No project is in progress.") {
		t.Fatalf("the card list after the finish:\n%s", body)
	}
	// No page holds a decide control.
	for _, page := range []string{sum, cp, fin, draft.Body.String(), kept.Body.String()} {
		if strings.Contains(page, "decide.js") || strings.Contains(page, `id="approve"`) {
			t.Fatal("a project page holds a decide control")
		}
	}
}

// An owner who taps every 5 seconds is never told the desk is busy. The
// interview on the pages, through the seat app, the link, the gate and a real
// thread store, with the link's clocks moved by hand: the six rounds of the
// web app path answered one by one, then two changes of an answer, each tap
// followed at once by the page its post redirects to, and the card list
// opened among them. Each tap is one call of the gate, whatever the page
// after it asks, and the room of the interview is not spent: on the phone
// trial every tap cost two, and the interview ran out of room at its fifth
// round.
func TestSeatLinkGenesisPhonePaceNeverBusy(t *testing.T) {
	seat := newIdentity(t, "seat")
	store := newStoreDesk(t)
	var desk *fakeDesk
	g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"pair", "read", "login", "genesis"},
		func(ctx context.Context, peer seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
			switch op {
			case OpLoginChallenge, OpLogin:
				return desk.handle(ctx, peer, op, params)
			case "owner.list":
				return ownerwire.EncodeListing(ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}})
			}
			return store.handle(ctx, peer, op, params)
		})
	desk = newFakeDesk(g.id.Fingerprint())
	phone, err := webauthntest.New(seatRPID, seatOrigin)
	if err != nil {
		t.Fatal(err)
	}
	desk.pair(t, phone)
	d := dialPinned(t, g, seat)
	clock := &testClock{t: decideNow}
	d.gnow = clock.now
	d.budget.now = clock.now
	br := newBrowser(seatApp(t, d))
	if w := br.loginWith(t, phone); w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	const pace = 5 * time.Second
	// tap posts a form, follows its redirect as the browser does, and checks
	// that the tap was one call of the gate and neither answer was busy.
	taps := 0
	tap := func(what, path string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		clock.advance(pace)
		taps++
		before := g.calls.Load()
		w := br.postForm(path, form)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d to %q", what, w.Code, w.Header().Get("Location"))
		}
		loc := w.Header().Get("Location")
		page := br.do("GET", loc, "")
		if page.Code != 200 || strings.Contains(page.Body.String(), "busy") || strings.Contains(loc, "note=busy") {
			t.Fatalf("%s: the page after it (%q) is %d, busy or not shown:\n%s", what, loc, page.Code, page.Body)
		}
		if n := g.calls.Load() - before; n != 1 {
			t.Fatalf("%s: %d calls of the gate for the tap and the page after it, want 1", what, n)
		}
		return w
	}
	w := tap("the start", "/projects/new", nil)
	thread := strings.TrimPrefix(w.Header().Get("Location"), "/projects/")
	rounds := []url.Values{
		{"n0": {"kind"}, "a0": {"software"}},
		{"n0": {"software.sub_kind"}, "a0": {"web_app"}, "n1": {"goal"}, "a1": {"own_use"}, "n2": {"audience"}, "a2": {"me"}},
		{"n0": {"software.stack"}, "a0": {"~other"}, "t0": {"Go"}, "n1": {"software.data"}, "a1": {"~host"}, "n2": {"software.auth"}, "a2": {"passkey"}},
		{"n0": {"software.deploy_kind"}, "a0": {"local"}, "n1": {"hosts"}, "a1": {"this_computer"}, "n2": {"budget_band"}, "a2": {"none"}},
		{"n0": {"deliverables"}, "m0": {"first_version"}, "n1": {"quality_bar"}, "a1": {"solid"}, "n2": {"schedule"}, "a2": {"~skip"}},
		{"n0": {"review_points"}, "m0": {"plan", "demo"}},
	}
	for i, form := range rounds {
		form.Set("round", strconv.Itoa(i+1))
		tap("round "+strconv.Itoa(i+1), "/projects/"+thread+"/answer", form)
		if i == 3 {
			// The card list is opened on the way: it lists the project in
			// progress, and asks the gate once.
			clock.advance(pace)
			if body := br.do("GET", "/cards/", "").Body.String(); !strings.Contains(body, thread) {
				t.Fatalf("the card list lacks the project:\n%s", body)
			}
		}
	}
	// Two changes of an answer, on the summary.
	tap("the first change", "/projects/"+thread+"/change", url.Values{"node": {"goal"}, "a0": {"share"}})
	tap("the second change", "/projects/"+thread+"/change", url.Values{"node": {"goal"}, "a0": {"own_use"}})
	if taps != 9 {
		t.Fatalf("%d taps, want 9", taps)
	}
	// Nine taps and one list: ten calls of the interview in 50 seconds, in a
	// room of genesisRoom.
	if n := len(d.budget.sent[kindGenesis]); n != 10 || n > genesisRoom {
		t.Errorf("%d calls of the interview in the window, want 10 (room %d)", n, genesisRoom)
	}
}
