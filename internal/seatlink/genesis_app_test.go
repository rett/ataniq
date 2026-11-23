package seatlink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

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
	// Finish: the finished page and its digest; the card list lists nothing
	// in progress.
	if w = br.postForm("/projects/"+thread+"/finish", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("the finish: %d", w.Code)
	}
	fin := br.do("GET", "/projects/"+thread, "").Body.String()
	if !strings.Contains(fin, "Nothing has started, and no agent was given the brief yet.") || strings.Contains(fin, "<form") {
		t.Fatalf("the finished page:\n%s", fin)
	}
	if body := br.do("GET", "/cards/", "").Body.String(); !strings.Contains(body, "No project is in progress.") {
		t.Fatalf("the card list after the finish:\n%s", body)
	}
	// No page holds a decide control.
	for _, page := range []string{sum, cp, fin} {
		if strings.Contains(page, "decide.js") || strings.Contains(page, `id="approve"`) {
			t.Fatal("a project page holds a decide control")
		}
	}
}
