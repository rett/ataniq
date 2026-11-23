package seatapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// openCard shows the card to the session, as the Card page does, and lets
// a second pass, as before a tap.
func (r *rig) openCard(t *testing.T, s *http.Cookie) string {
	t.Helper()
	w := r.getWith("/cards/"+testCardID, s)
	if w.Code != 200 {
		t.Fatalf("card: %d", w.Code)
	}
	r.clock.Advance(time.Second)
	return w.Body.String()
}

// answer decodes a JSON answer.
func answer(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("answer %d %q: %v", w.Code, w.Body, err)
	}
	return m
}

// approveBody is the body of an approve of the test card with its one
// flagged path signed off.
const approveBody = `{"card":"` + testCardID + `","action":"approve","signed_off":[0],"reason":""}`

// denyBody is the body of a deny with a reason.
func denyBody(reason string) string {
	b, _ := json.Marshal(map[string]any{"card": testCardID, "action": "deny", "signed_off": []int{}, "reason": reason})
	return string(b)
}

// neverServed is a form id of the right form that no page was served with.
var neverServed = strings.Repeat("A", formLen)

// withForm is body with the id of the form of the last Card page the
// session was served, as the page's script sends it, or one never served
// when the session was served none. A body that is not one JSON object, or
// that names a form already, is left as it is.
func (r *rig) withForm(body string, s *http.Cookie) string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &m) != nil || m == nil {
		return body
	}
	if _, ok := m["form"]; ok {
		return body
	}
	form, ok := r.forms[s.Value]
	if !ok {
		form = neverServed
	}
	m["form"], _ = json.Marshal(form)
	b, _ := json.Marshal(m)
	return string(b)
}

// tap posts a decision's tap of the session's last Card page: POST
// /api/decide-challenge with body and that page's form (see withForm).
func (r *rig) tap(body string, s *http.Cookie) *httptest.ResponseRecorder {
	return r.postWith("/api/decide-challenge", r.withForm(body, s), s)
}

// challengeOf asks for a decision's challenge and returns its id.
func (r *rig) challengeOf(t *testing.T, s *http.Cookie, body string) string {
	t.Helper()
	w := r.tap(body, s)
	if w.Code != 200 {
		t.Fatalf("decide-challenge: %d %s", w.Code, w.Body)
	}
	id, _ := answer(t, w)["challenge_id"].(string)
	return id
}

// decideBodyOf is the body of POST /api/decide with an assertion of the
// credential given.
func decideBodyOf(challengeID string, credentialID []byte) string {
	b, _ := json.Marshal(map[string]string{
		"challenge_id":       challengeID,
		"credential_id":      b64.EncodeToString(credentialID),
		"client_data_json":   b64.EncodeToString([]byte(`{"type":"webauthn.get"}`)),
		"authenticator_data": b64.EncodeToString(bytes.Repeat([]byte{0xaa}, 37)),
		"signature":          b64.EncodeToString([]byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01}),
	})
	return string(b)
}

// wantFailure checks a failure's status, code and words.
func wantFailure(t *testing.T, what string, w *httptest.ResponseRecorder, status int, code, say string) {
	t.Helper()
	m := answer(t, w)
	if w.Code != status || m["error"] != code || m["say"] != say {
		t.Errorf("%s: %d %v, want %d %s %q", what, w.Code, m, status, code, say)
	}
}

// TestSeatDecideFormOnTheCardPage: the Card page of a pending card holds the
// decide form: one sign-off box per flagged path, approve and deny, the
// reason, and the screen that names the action, the card, its project, what
// it does, its target and its revision before the passkey is asked; and it
// alone allows publickey-credentials-get. The lead's text in it is shown
// through Text.
func TestSeatDecideFormOnTheCardPage(t *testing.T) {
	r := newRig(t)
	c := sampleCard()
	c.Flagged = append(c.Flagged, Flagged{Kind: "symlink", Reason: "a symbolic link", Path: NewLead(hostile)})
	r.daemon.card = c
	s := r.login(t)
	w := r.getWith("/cards/"+testCardID, s)
	checkHeadersGet(t, "the Card page", w, "(self)")
	body := w.Body.String()
	for _, want := range []string{
		`<section id="decide" aria-labelledby="decide-title" data-rp-id="seat.example" data-card="` + testCardID + `" data-form="`,
		`<input type="checkbox" id="signoff-0" class="signoff" value="0" data-offer="open">`,
		`<input type="checkbox" id="signoff-1" class="signoff" value="1" data-offer="open">`,
		`<button id="approve" type="button" data-offer="open" hidden disabled>Approve</button>`,
		`<textarea id="reason" rows="3" maxlength="500" data-offer="open" hidden></textarea>`,
		`<button id="deny" type="button" class="deny" data-offer="open" hidden>Deny</button>`,
		`<div id="confirm" hidden>`, `<button id="confirm-go" type="button" data-offer="open"></button>`,
		"<dt>Project</dt><dd>ataniq</dd>", "<dt>What</dt><dd>feature finish seat-glue</dd>", "<dt>Into</dt><dd>develop</dd>",
		"<dt>Revision</dt><dd class=\"mono\">binding abababababab</dd>", "<dt>Card</dt><dd>" + testCardID + "</dd>",
		`<a id="look" href="/cards/` + testCardID + `" hidden>Look again</a>`,
		`<p id="decide-status" role="status"></p>`, "<noscript>", `<script src="/static/decide.js"></script>`,
		"&lt;script&gt;alert(1)&lt;/script&gt;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "‮") {
		t.Error("the lead's text reached the form raw")
	}
	if strings.Count(body, "<form") != 0 || strings.Count(body, `id="decide"`) != 1 {
		t.Error("not one decide section, without a form element")
	}
	if f := formOf(body); !validFormID(f) || f == formOf(r.getWith("/cards/"+testCardID, s).Body.String()) {
		t.Errorf("the page's form id %q is not one of its own", f)
	}

	// Approve not offered: the host's line says why, and deny stays.
	r.daemon.offer = Offer{ApproveWhy: "the path list is cut on the card, and a page of the whole list was not read", Deny: true}
	body = r.getWith("/cards/"+testCardID, s).Body.String()
	if strings.Contains(body, `id="approve"`) || strings.Contains(body, `id="signoff-0"`) || !strings.Contains(body, `id="deny"`) ||
		!strings.Contains(body, "Approve is not offered: the path list is cut on the card, and a page of the whole list was not read.") {
		t.Error("approve not offered")
	}
	// Neither offered: no form, no deciding script, no passkey.
	r.daemon.offer = Offer{ApproveWhy: "this request is past its expiry", DenyWhy: "this request is past its expiry"}
	w = r.getWith("/cards/"+testCardID, s)
	checkHeaders(t, "a card offered nothing", w)
	body = w.Body.String()
	if strings.Contains(body, `id="decide"`) || strings.Contains(body, "decide.js") || !strings.Contains(body, "This card cannot be decided now: this request is past its expiry.") {
		t.Error("a card offered nothing")
	}
	// A card that ended, or whose decision is on its way, offers nothing,
	// and the port is not asked.
	r.daemon.offer = Offer{Approve: true, Deny: true}
	before := len(r.daemon.offers)
	r.daemon.card.State, r.daemon.card.Ended = "approved", true
	if body := r.getWith("/cards/"+testCardID, s).Body.String(); strings.Contains(body, `id="decide"`) {
		t.Error("an ended card has the form")
	}
	r.daemon.card.State, r.daemon.card.Ended, r.daemon.card.OnItsWay = "", false, true
	body = r.getWith("/cards/"+testCardID, s).Body.String()
	if strings.Contains(body, `id="decide"`) || !strings.Contains(body, "A decision of this card is on its way to the door.") {
		t.Error("a card whose decision is on its way")
	}
	if len(r.daemon.offers) != before {
		t.Error("the port was asked an offer for a card that is not pending")
	}
	// The list and the path pages have no form and no deciding script.
	for _, path := range []string{"/cards/", "/cards/" + testCardID + "/paths/0"} {
		w := r.getWith(path, s)
		checkHeaders(t, path, w)
		if strings.Contains(w.Body.String(), `id="decide"`) || strings.Contains(w.Body.String(), "decide.js") {
			t.Errorf("%s has the decide form", path)
		}
	}
}

// TestSeatDecideChallengeTakesTheCardAsShown: a decision's challenge needs a
// session and the session's reading of the card. The port is handed the
// binding the page showed, the action, the indexes, the session's
// credential, its reading (the pages of the whole path list read included)
// and the app's clock; the browser gets the challenge and the session's one
// credential as the allow-list.
func TestSeatDecideChallengeTakesTheCardAsShown(t *testing.T) {
	r := newRig(t)
	w := r.postJSON("/api/decide-challenge", approveBody)
	wantFailure(t, "no session", w, 401, "session_ended", sayNoSession)
	s := r.login(t)
	wantFailure(t, "a card not read", r.tap(approveBody, s), 409, "look_again", sayLookFirst)
	if n, _ := r.daemon.decideCounts(); n != 0 {
		t.Fatalf("the port was asked %d challenges", n)
	}
	opened := r.clock.Now()
	r.openCard(t, s)
	for _, n := range []string{"2", "0", "1"} {
		if w := r.getWith("/cards/"+testCardID+"/paths/"+n, s); w.Code != 200 {
			t.Fatalf("page %s: %d", n, w.Code)
		}
	}
	w = r.tap(approveBody, s)
	if w.Code != 200 {
		t.Fatalf("challenge: %d %s", w.Code, w.Body)
	}
	m := answer(t, w)
	if m["allow"].([]any)[0] != b64.EncodeToString(testCredentialID) || len(m["allow"].([]any)) != 1 ||
		m["challenge"] != b64.EncodeToString(bytes.Repeat([]byte{0x6b}, 32)) || m["action"] != "approve" ||
		m["card"] != testCardID || m["binding"] != "abababababab" || m["expires_in"] != float64(300) || m["signed_off"] != float64(1) {
		t.Errorf("answer %v", m)
	}
	q := r.daemon.decideChs[0]
	want := DecideChallengeRequest{ID: testCardID, BindingSHA256: strings.Repeat("ab", 32), Approve: true, SignedOff: []int{0},
		Credential: "0123456789abcdef", Reading: Reading{Opened: opened, PagesRead: []int{0, 1, 2}, Pages: 3, Review: strings.Repeat("ef", 32),
			Whole: AllWhole}, Now: r.clock.Now()}
	if !reflect.DeepEqual(q, want) {
		t.Errorf("the port got %+v\nwant %+v", q, want)
	}
	// The card page asked the port's offer with the session's reading.
	if len(r.daemon.offers) == 0 || !r.daemon.offers[0].Opened.Equal(opened) {
		t.Errorf("offers %+v", r.daemon.offers)
	}
	// A deny goes with its reason and no sign-off.
	r.challengeOf(t, s, denyBody("not this one"))
	if q := r.daemon.decideChs[1]; q.Approve || len(q.SignedOff) != 0 || q.Reason != "not this one" {
		t.Errorf("deny %+v", q)
	}
	// Another session of the same credential has not read the card.
	other := r.login(t)
	wantFailure(t, "another session", r.tap(approveBody, other), 409, "look_again", sayLookFirst)
}

// formOf returns the id of the decide form of a Card page's body.
func formOf(body string) string {
	_, after, ok := strings.Cut(body, ` data-form="`)
	if !ok {
		return ""
	}
	form, _, _ := strings.Cut(after, `"`)
	return form
}

// tapOf is the body of a tap of the page whose form is given: an approve
// with the one flagged path signed off.
func tapOf(form string) string {
	b, _ := json.Marshal(map[string]any{"card": testCardID, "form": form, "action": "approve", "signed_off": []int{0}, "reason": ""})
	return string(b)
}

// postFromPage sends a POST as a page's script does: with the Sec-Fetch-Site
// header that every browser sends.
func (r *rig) postFromPage(path, body string, s *http.Cookie) *httptest.ResponseRecorder {
	req := newRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(s)
	return r.recorder(req)
}

// TestSeatDecideTapOfAStalePage: two tabs of one session. The first shows
// the card; the card is replaced by one of the same id and another binding,
// and the second tab shows the replacement. A tap of the first tab is
// refused before the port is asked (so no challenge is drawn and the
// passkey is not asked), and the tap of the second is taken. A page's tap
// must name its form: one with none, or with a form the session was never
// served, is refused too, and so is a body that leaves the form out (which
// a test option, now gone, once let through without Sec-Fetch-Site). Two
// pages of the same binding are both good.
func TestSeatDecideTapOfAStalePage(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	first := formOf(r.openCard(t, s))
	r.daemon.card.BindingSHA256 = strings.Repeat("cd", 32)
	second := formOf(r.openCard(t, s))
	if first == "" || second == "" || first == second {
		t.Fatalf("the forms %q and %q", first, second)
	}
	wantFailure(t, "the first tab's tap", r.postFromPage("/api/decide-challenge", tapOf(first), s), 409, "card_changed", sayChanged)
	wantFailure(t, "a tap of no form", r.postFromPage("/api/decide-challenge", tapOf(""), s), 400, "bad_request", sayFailed)
	noForm, _ := json.Marshal(map[string]any{"card": testCardID, "action": "approve", "signed_off": []int{0}, "reason": ""})
	wantFailure(t, "a tap that leaves the form out", r.postFromPage("/api/decide-challenge", string(noForm), s), 400, "bad_request", sayFailed)
	wantFailure(t, "a deny that leaves the form out", r.postFromPage("/api/decide-challenge", denyBody("no"), s), 400, "bad_request", sayFailed)
	other := b64.EncodeToString(bytes.Repeat([]byte{0x42}, formBytes))
	wantFailure(t, "a form never served", r.postFromPage("/api/decide-challenge", tapOf(other), s), 409, "look_again", sayLookFirst)
	wantFailure(t, "a form of another shape", r.postFromPage("/api/decide-challenge", tapOf("x"), s), 400, "bad_request", sayFailed)
	if n, _ := r.daemon.decideCounts(); n != 0 {
		t.Fatalf("the port was asked %d challenges", n)
	}
	// Another session's form is not this session's.
	s2 := r.login(t)
	elsewhere := formOf(r.openCard(t, s2))
	wantFailure(t, "another session's form", r.postFromPage("/api/decide-challenge", tapOf(elsewhere), s), 409, "look_again", sayLookFirst)

	w := r.postFromPage("/api/decide-challenge", tapOf(second), s)
	if w.Code != 200 || answer(t, w)["binding"] != "cdcdcdcdcdcd" {
		t.Fatalf("the second tab's tap: %d %s", w.Code, w.Body)
	}
	if q := r.daemon.decideChs[0]; q.BindingSHA256 != strings.Repeat("cd", 32) {
		t.Errorf("the port was asked for the binding %s", q.BindingSHA256)
	}
	// A third tab of the same binding: the second tab's form still holds.
	third := formOf(r.openCard(t, s))
	for _, f := range []string{second, third} {
		if w := r.postFromPage("/api/decide-challenge", tapOf(f), s); w.Code != 200 {
			t.Errorf("a tab of the binding read last: %d %s", w.Code, w.Body)
		}
	}
}

// TestSeatCardPartsShownWhole: the session's reading says which parts of
// the card the Card page showed whole. A path or a ref longer than the page
// shows (maxPath characters of its escaped text), a name of the header
// longer than maxName, or a line of the plan or of the note longer than
// maxText makes its part not whole, and the port's offer is asked with that
// reading; one of exactly the cap is whole. A page of the whole path list that cuts a path does
// not count as read, and says so.
func TestSeatCardPartsShownWhole(t *testing.T) {
	long := NewLead(strings.Repeat("x", maxPath+1))
	edge := NewLead(strings.Repeat("x", maxPath))
	all := AllWhole
	off := func(f func(*Whole)) Whole {
		w := AllWhole
		f(&w)
		return w
	}
	longName := strings.Repeat("x", maxName+1)
	longLine := NewLead(strings.Repeat("y", maxText+1))
	for name, tc := range map[string]struct {
		change func(*Card)
		want   Whole
	}{
		"nothing long":      {func(*Card) {}, all},
		"a path at the cap": {func(c *Card) { c.Diff.Rows[0].Path = edge; c.Flagged[0].Path = edge }, all},
		"names at the cap":  {func(c *Card) { c.Project, c.Target = longName[1:], longName[1:] }, all},
		"lines at the cap": {func(c *Card) {
			c.Plan[0], c.Note[0] = NewLead(strings.Repeat("y", maxText)), NewLead(strings.Repeat("y", maxText))
		}, all},
		"the project":         {func(c *Card) { c.Project = longName }, off(func(w *Whole) { w.Names = false })},
		"the target":          {func(c *Card) { c.Target = longName }, off(func(w *Whole) { w.Names = false })},
		"the feature":         {func(c *Card) { c.Feature = longName }, off(func(w *Whole) { w.Names = false })},
		"a plan line":         {func(c *Card) { c.Plan[1] = longLine }, off(func(w *Whole) { w.Plan = false })},
		"a note line":         {func(c *Card) { c.Note[1] = longLine }, off(func(w *Whole) { w.Note = false })},
		"a row":               {func(c *Card) { c.Diff.Rows[1].Path = long }, off(func(w *Whole) { w.Rows = false })},
		"a list's path":       {func(c *Card) { c.Lists[0].Paths[0] = long }, off(func(w *Whole) { w.Lists = false })},
		"a ref":               {func(c *Card) { c.Refs[0].Ref = long }, off(func(w *Whole) { w.Refs = false })},
		"a merged-into ref":   {func(c *Card) { c.Refs[0].MergedInto = &MergedInto{Ref: long, Tip: strings.Repeat("3", 40)} }, off(func(w *Whole) { w.Refs = false })},
		"a flagged path":      {func(c *Card) { c.Flagged[0].Path = long }, off(func(w *Whole) { w.Flagged = false })},
		"a flagged path rule": {func(c *Card) { c.Flagged[0].Pattern = long }, off(func(w *Whole) { w.Flagged = false })},
		"a differs path":      {func(c *Card) { c.Differs.PathsAdded = []Lead{long} }, off(func(w *Whole) { w.Differs = false })},
		"a differs ref":       {func(c *Card) { c.Differs.RefsRemoved = []Lead{long} }, off(func(w *Whole) { w.Differs = false })},
	} {
		r := newRig(t)
		c := sampleCard()
		tc.change(&c)
		r.daemon.card = c
		s := r.login(t)
		r.openCard(t, s)
		if got := r.daemon.offers[len(r.daemon.offers)-1].Whole; got != tc.want {
			t.Errorf("%s: the offer was asked with %+v, want %+v", name, got, tc.want)
		}
	}

	// The branch a prune's deleted ref was merged into is on the page.
	r := newRig(t)
	r.daemon.card.Refs[0].Deletes = true
	r.daemon.card.Refs[0].MergedInto = &MergedInto{Ref: NewLead("refs/heads/main"), Tip: strings.Repeat("4", 40)}
	s := r.login(t)
	if body := r.openCard(t, s); !strings.Contains(body, `<span class="meta">merged into</span> <span class="lead mono">refs/heads/main</span> <span class="meta">(444444444444)</span>`) {
		t.Error("the Card page does not say where the deleted branch was merged")
	}

	// A page of the whole path list with a path cut is not read.
	r.daemon.page = PathsPage{Pages: 2, Review: strings.Repeat("ef", 32), Rows: []PathRow{{Path: long, Status: "M"}}}
	w := r.getWith("/cards/"+testCardID+"/paths/0", s)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "A path on this page is too long to show whole on the phone, so this page does not count as read.") {
		t.Errorf("a page with a cut path: %d", w.Code)
	}
	r.daemon.page = PathsPage{Pages: 2, Review: strings.Repeat("ef", 32), Rows: []PathRow{{Path: edge, Status: "M"}}}
	if w := r.getWith("/cards/"+testCardID+"/paths/1", s); strings.Contains(w.Body.String(), "too long to show whole") {
		t.Error("a page with no path cut says one is")
	}
	r.challengeOf(t, s, approveBody)
	if rd := r.daemon.decideChs[0].Reading; len(rd.PagesRead) != 1 || rd.PagesRead[0] != 1 {
		t.Errorf("the pages read are %v, want [1]", rd.PagesRead)
	}
}

// showHooked is the fake port with a hook run once inside Show, after the
// card was read and before it is returned: what happens while a read is on
// its way back.
type showHooked struct {
	*fakeDaemon
	hook func()
}

func (d *showHooked) Show(ctx context.Context, id string) (Card, error) {
	c, err := d.fakeDaemon.Show(ctx, id)
	d.mu.Lock()
	h := d.hook
	d.hook = nil
	d.mu.Unlock()
	if h != nil {
		h()
	}
	return c, err
}

// TestSeatHeadIsNoRead: a HEAD of a card page is refused 405 and asks the
// port nothing, so it counts as no read of a page of the path list and does
// not settle a lost answer; a GET does both.
func TestSeatHeadIsNoRead(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	head := func(path string) *httptest.ResponseRecorder {
		req := newRequest("HEAD", path, nil)
		req.AddCookie(s)
		return r.recorder(req)
	}
	before := r.daemon.readCount()
	for _, path := range []string{"/cards/", "/cards/" + testCardID, "/cards/" + testCardID + "/paths/0", "/cards/" + testCardID + "/paths/1"} {
		if w := head(path); w.Code != 405 || w.Header().Get("Allow") != "GET" {
			t.Errorf("HEAD %s: %d, Allow %q", path, w.Code, w.Header().Get("Allow"))
		}
	}
	if n := r.daemon.readCount(); n != before {
		t.Errorf("the port was asked %d reads for HEADs", n-before)
	}
	r.challengeOf(t, s, approveBody)
	if rd := r.daemon.decideChs[0].Reading; len(rd.PagesRead) != 0 {
		t.Errorf("HEADs counted as reads of the pages %v", rd.PagesRead)
	}
	// A lost answer stands after a HEAD of the Card page.
	r.daemon.decideErr = ErrOutcomeUnknown
	id := r.challengeOf(t, s, approveBody)
	r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s)
	head("/cards/" + testCardID)
	wantFailure(t, "a tap after a HEAD", r.tap(approveBody, s), 409, "sent_unknown", saySentUnknown)
	r.daemon.decideErr = nil
	r.openCard(t, s)
	if w := r.tap(approveBody, s); w.Code != 200 {
		t.Errorf("a tap after a GET: %d %s", w.Code, w.Body)
	}
}

// TestSeatDecideStaleReadKeepsTheMark: a GET of the Card page whose read
// began before a decision of the card was sent, and whose answer was lost,
// shows the card as it was read, so it does not settle the lost answer: the
// page holds no form and says to look again, and a tap is still refused. A
// GET that begins after the send settles it.
func TestSeatDecideStaleReadKeepsTheMark(t *testing.T) {
	var hd *showHooked
	r := newRig(t, func(c *Config) {
		hd = &showHooked{fakeDaemon: c.Daemon.(*fakeDaemon)}
		c.Daemon = hd
	})
	s := r.login(t)
	r.openCard(t, s)
	id := r.challengeOf(t, s, approveBody)
	r.daemon.decideErr = ErrOutcomeUnknown
	hd.hook = func() {
		// The decision is sent and its answer lost while the GET's read is
		// on its way back.
		if w := r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s); w.Code != 502 {
			t.Errorf("the decision: %d %s", w.Code, w.Body)
		}
	}
	w := r.getWith("/cards/"+testCardID, s)
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, `id="decide"`) || !strings.Contains(body, "A decision of this card was sent and its outcome is not known. Look at it again.") {
		t.Errorf("the page of a read that began before the send: %d", w.Code)
	}
	r.clock.Advance(time.Second)
	wantFailure(t, "a tap after the stale read", r.tap(approveBody, s), 409, "sent_unknown", saySentUnknown)
	r.daemon.decideErr = nil
	r.openCard(t, s)
	if w := r.tap(approveBody, s); w.Code != 200 {
		t.Errorf("a tap after a read that began after the send: %d %s", w.Code, w.Body)
	}
}

// TestSeatDecideReadInFlight: a GET of the Card page that begins after a
// decision of the card was sent, and before its answer came, may show the
// card as it was before the decision was journaled: it holds no form and
// does not settle the decision. When the answer is then lost, the next tap
// is still refused, and only a read that begins after the loss settles it.
// On the base that read was served a form and cleared the mark, and the
// next tap drew a new challenge.
func TestSeatDecideReadInFlight(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	id := r.challengeOf(t, s, approveBody)
	var during *httptest.ResponseRecorder
	r.daemon.decideHook = func() { during = r.getWith("/cards/"+testCardID, s) }
	r.daemon.decideErr = ErrOutcomeUnknown
	wantFailure(t, "the lost answer", r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s), 502, "outcome_unknown", sayUnknown)
	r.daemon.decideHook = nil
	if body := during.Body.String(); during.Code != 200 || strings.Contains(body, `id="decide"`) ||
		!strings.Contains(body, "A decision of this card was sent and its outcome is not known. Look at it again.") {
		t.Errorf("the page read while the decision was on its way: %d", during.Code)
	}
	r.clock.Advance(time.Second)
	wantFailure(t, "a tap after the read in flight", r.tap(approveBody, s), 409, "sent_unknown", saySentUnknown)
	if c, _ := r.daemon.decideCounts(); c != 1 {
		t.Errorf("%d challenges drawn", c)
	}
	r.daemon.decideErr = nil
	r.openCard(t, s)
	if w := r.tap(approveBody, s); w.Code != 200 {
		t.Errorf("a tap after a read that began after the loss: %d %s", w.Code, w.Body)
	}
	// A certain answer settles the decision whatever was read meanwhile.
	id = r.challengeOf(t, s, approveBody)
	r.daemon.decideHook = func() { during = r.getWith("/cards/"+testCardID, s) }
	r.daemon.decideErr = ErrAssertionRefused
	r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s)
	r.daemon.decideHook = nil
	if strings.Contains(during.Body.String(), `id="decide"`) {
		t.Error("a read while a decision was on its way offered the form")
	}
	r.openCard(t, s)
	if w := r.tap(approveBody, s); w.Code != 200 {
		t.Errorf("a tap after a refusal: %d %s", w.Code, w.Body)
	}
}

// TestSeatDecideReadAcrossTheLoss: a GET of the Card page whose read began
// while a decision of the card was on its way, and came back after the
// decision's answer was lost, may show the card as it was before: it holds
// no form and does not settle the lost answer.
func TestSeatDecideReadAcrossTheLoss(t *testing.T) {
	var hd *showHooked
	r := newRig(t, func(c *Config) {
		hd = &showHooked{fakeDaemon: c.Daemon.(*fakeDaemon)}
		c.Daemon = hd
	})
	s := r.login(t)
	r.openCard(t, s)
	id := r.challengeOf(t, s, approveBody)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan *httptest.ResponseRecorder)
	r.daemon.decideHook = func() {
		hd.mu.Lock()
		hd.hook = func() { close(entered); <-release }
		hd.mu.Unlock()
		go func() { done <- r.getWith("/cards/"+testCardID, s) }()
		<-entered
	}
	r.daemon.decideErr = ErrOutcomeUnknown
	wantFailure(t, "the lost answer", r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s), 502, "outcome_unknown", sayUnknown)
	r.daemon.decideHook = nil
	close(release)
	w := <-done
	if body := w.Body.String(); w.Code != 200 || strings.Contains(body, `id="decide"`) ||
		!strings.Contains(body, "A decision of this card was sent and its outcome is not known. Look at it again.") {
		t.Errorf("the page of a read that began while the decision was on its way: %d", w.Code)
	}
	r.clock.Advance(time.Second)
	wantFailure(t, "a tap after that read", r.tap(approveBody, s), 409, "sent_unknown", saySentUnknown)
	r.openCard(t, s)
	r.daemon.decideErr = nil
	if w := r.tap(approveBody, s); w.Code != 200 {
		t.Errorf("a tap after a read that began after the loss: %d %s", w.Code, w.Body)
	}
}

// brokenWriter is a response writer whose connection is gone: what is
// written is kept in body, and Write fails, or, with flushFails, Write
// takes it and the flush fails.
type brokenWriter struct {
	header     http.Header
	body       bytes.Buffer
	flushFails bool
}

func (w *brokenWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *brokenWriter) WriteHeader(int) {}

func (w *brokenWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	if w.flushFails {
		return len(b), nil
	}
	return 0, errors.New("write: broken pipe")
}

// FlushError is the flush http.ResponseController asks for.
func (w *brokenWriter) FlushError() error {
	if w.flushFails {
		return errors.New("flush: broken pipe")
	}
	return nil
}

// getBroken GETs path with the session s into a broken writer.
func (r *rig) getBroken(path string, s *http.Cookie, flushFails bool) *brokenWriter {
	req := newRequest("GET", path, nil)
	req.AddCookie(s)
	w := &brokenWriter{flushFails: flushFails}
	r.h.ServeHTTP(w, req)
	return w
}

// TestSeatPagesNotWrittenCountForNothing: a Card page or a page of the
// path list that was not written to the phone (the write or its flush
// failed: the phone went away) counts for nothing. It does not settle a
// lost answer, its form is not taken, and a path page is not credited as
// read. On the base each was noted before the page was written.
func TestSeatPagesNotWrittenCountForNothing(t *testing.T) {
	for _, flushFails := range []bool{false, true} {
		r := newRig(t)
		s := r.login(t)
		r.openCard(t, s)
		id := r.challengeOf(t, s, approveBody)
		r.daemon.decideErr = ErrOutcomeUnknown
		r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s)
		r.daemon.decideErr = nil
		r.getBroken("/cards/"+testCardID, s, flushFails)
		r.clock.Advance(time.Second)
		wantFailure(t, "a tap after a Card page not written", r.tap(approveBody, s), 409, "sent_unknown", saySentUnknown)

		// A Card page that offers the form but was not written: its form
		// is not taken.
		r.openCard(t, s)
		w := r.getBroken("/cards/"+testCardID, s, flushFails)
		if flushFails {
			form := formOf(w.body.String())
			if form == "" {
				t.Fatal("the page not flushed holds no form")
			}
			wantFailure(t, "the form of a page not written", r.postWith("/api/decide-challenge", r.withForm(tapOf(form), s), s), 409, "look_again", sayLookFirst)
		}

		// A page of the path list not written is no read.
		r.getBroken("/cards/"+testCardID+"/paths/0", s, flushFails)
		r.challengeOf(t, s, approveBody)
		if rd := r.daemon.decideChs[len(r.daemon.decideChs)-1].Reading; len(rd.PagesRead) != 0 {
			t.Errorf("flush fails %v: pages not written counted as read: %v", flushFails, rd.PagesRead)
		}
		r.getWith("/cards/"+testCardID+"/paths/0", s)
		r.challengeOf(t, s, approveBody)
		if rd := r.daemon.decideChs[len(r.daemon.decideChs)-1].Reading; len(rd.PagesRead) != 1 {
			t.Errorf("flush fails %v: a page written is not counted: %v", flushFails, rd.PagesRead)
		}
	}
}

// hookedWriter is a recorder that runs hook once, as the page is written:
// what happens while the page is on its way to the phone.
type hookedWriter struct {
	*httptest.ResponseRecorder
	hook func()
}

func (w *hookedWriter) Write(b []byte) (int, error) {
	if h := w.hook; h != nil {
		w.hook = nil
		h()
	}
	return w.ResponseRecorder.Write(b)
}

// TestSeatDecideSentWhilePageIsWritten: a decision of the card sent, and
// its answer lost, while a Card page that offers the form is being written
// makes that read stale: once written it does not settle the lost answer,
// and its form is not taken.
func TestSeatDecideSentWhilePageIsWritten(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	w := &hookedWriter{ResponseRecorder: httptest.NewRecorder(), hook: func() {
		id := r.challengeOf(t, s, approveBody)
		r.daemon.decideErr = ErrOutcomeUnknown
		r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s)
	}}
	req := newRequest("GET", "/cards/"+testCardID, nil)
	req.AddCookie(s)
	r.h.ServeHTTP(w, req)
	form := formOf(w.Body.String())
	if form == "" {
		t.Fatal("the page holds no form: the test does not show what it means to")
	}
	r.daemon.decideErr = nil
	r.clock.Advance(time.Second)
	wantFailure(t, "a tap after the page", r.tap(approveBody, s), 409, "sent_unknown", saySentUnknown)
	wantFailure(t, "a tap of its form", r.postWith("/api/decide-challenge", r.withForm(tapOf(form), s), s), 409, "look_again", sayLookFirst)
}

// TestSeatPathPagesKeepTheirReview: the pages of a path list count with the
// review each was read for; a page of another review starts the count
// again, so pages read before the card was replaced do not count for the
// replacement.
func TestSeatPathPagesKeepTheirReview(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	for _, n := range []string{"0", "1"} {
		r.getWith("/cards/"+testCardID+"/paths/"+n, s)
	}
	r.daemon.page.Review = strings.Repeat("aa", 32)
	r.getWith("/cards/"+testCardID+"/paths/2", s)
	r.openCard(t, s)
	r.challengeOf(t, s, approveBody)
	if rd := r.daemon.decideChs[0].Reading; rd.Review != strings.Repeat("aa", 32) || len(rd.PagesRead) != 1 || rd.PagesRead[0] != 2 {
		t.Errorf("the reading is of the review %s, pages %v", rd.Review, rd.PagesRead)
	}
	// A page without a review is no page.
	r.daemon.page.Review = ""
	if w := r.getWith("/cards/"+testCardID+"/paths/0", s); w.Code != 502 {
		t.Errorf("a page without its review: %d", w.Code)
	}
}

// TestSeatDecideTooLargeIsCertain: a decision whose words the port finds
// too large for the gate is not sent: the phone says so, and the card is
// not left marked sent.
func TestSeatDecideTooLargeIsCertain(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	r.daemon.decideChErr = ErrTooLarge
	wantFailure(t, "a challenge too large", r.tap(approveBody, s), 413, "too_large", sayTooLarge)
	r.daemon.decideChErr = nil
	id := r.challengeOf(t, s, approveBody)
	r.daemon.decideErr = ErrTooLarge
	wantFailure(t, "a decision too large", r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s), 413, "too_large", sayTooLarge)
	r.daemon.decideErr = nil
	if w := r.tap(approveBody, s); w.Code != 200 {
		t.Errorf("a tap after a decision too large: %d %s", w.Code, w.Body)
	}
}

// TestSeatDecideBodies: a body that is not a decision of the form the page
// sends is refused, and the port is not asked.
func TestSeatDecideBodies(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	many := make([]string, maxSignOffs+1)
	for i := range many {
		many[i] = strconv.Itoa(i)
	}
	for name, body := range map[string]string{
		"another action":         `{"card":"` + testCardID + `","action":"later","signed_off":[],"reason":""}`,
		"a deny with a sign-off": `{"card":"` + testCardID + `","action":"deny","signed_off":[0],"reason":""}`,
		"an approve's reason":    `{"card":"` + testCardID + `","action":"approve","signed_off":[0],"reason":"x"}`,
		"an index twice":         `{"card":"` + testCardID + `","action":"approve","signed_off":[0,0],"reason":""}`,
		"an index past":          `{"card":"` + testCardID + `","action":"approve","signed_off":[1],"reason":""}`,
		"a negative index":       `{"card":"` + testCardID + `","action":"approve","signed_off":[-1],"reason":""}`,
		"too many indexes":       `{"card":"` + testCardID + `","action":"approve","signed_off":[` + strings.Join(many, ",") + `],"reason":""}`,
		"a bad card id":          `{"card":"AP-1","action":"approve","signed_off":[0],"reason":""}`,
		"a binding":              `{"card":"` + testCardID + `","action":"approve","signed_off":[0],"reason":"","binding_sha256":"00"}`,
		"a path":                 `{"card":"` + testCardID + `","action":"approve","signed_off":["Makefile"],"reason":""}`,
		"not JSON":               `card=` + testCardID,
		"two objects":            approveBody + approveBody,
	} {
		wantFailure(t, name, r.tap(body, s), 400, "bad_request", sayFailed)
	}
	if n, _ := r.daemon.decideCounts(); n != 0 {
		t.Errorf("the port was asked %d challenges", n)
	}
	// The decide route takes the same care.
	r.challengeOf(t, s, approveBody)
	wantFailure(t, "a short credential", r.postWith("/api/decide", decideBodyOf(r.daemon.decideChs[0].ID, []byte{1}), s), 409, "challenge_expired", sayTooLong)
	if _, n := r.daemon.decideCounts(); n != 0 {
		t.Errorf("the port was asked %d decisions", n)
	}
}

// TestSeatDecideBodySizes: both bodies of a decision fit the app's cap on a
// request, MaxBody (8192 bytes): the largest decide-challenge the page sends
// (its form's id and 300 sign-off indexes, or a deny's reason of 500 bytes
// that are all escaped) and the largest decide (an assertion at the wire's bounds:
// credential id 1023 bytes, authenticator data 1024, client data 2048,
// signature 72, all base64url). A body of exactly MaxBody bytes is read; one
// byte more is refused 413 and the port is not asked.
func TestSeatDecideBodySizes(t *testing.T) {
	idx := make([]int, maxSignOffs)
	for i := range idx {
		idx[i] = i
	}
	form := strings.Repeat("A", formLen)
	big, _ := json.Marshal(map[string]any{"card": "ap-" + strings.Repeat("a", 48), "form": form, "action": "approve", "signed_off": idx, "reason": ""})
	longDeny, _ := json.Marshal(map[string]any{"card": "ap-" + strings.Repeat("a", 48), "form": form, "action": "deny", "signed_off": []int{}, "reason": strings.Repeat(`"`, 500)})
	assertion, _ := json.Marshal(map[string]string{
		"challenge_id":       strings.Repeat("a", 16),
		"credential_id":      b64.EncodeToString(make([]byte, maxCredentialID)),
		"client_data_json":   b64.EncodeToString(make([]byte, maxClientData)),
		"authenticator_data": b64.EncodeToString(make([]byte, maxAuthData)),
		"signature":          b64.EncodeToString(make([]byte, maxSignature)),
	})
	t.Logf("largest bodies: decide-challenge %d (approve) and %d (deny) bytes, decide %d bytes, cap %d", len(big), len(longDeny), len(assertion), MaxBody)
	for name, n := range map[string]int{"approve": len(big), "deny": len(longDeny), "decide": len(assertion)} {
		if n > 6<<10 {
			t.Errorf("the largest %s body is %d bytes, over 6 KiB", name, n)
		}
	}
	pad := func(body string, n int) string { return body + strings.Repeat(" ", n-len(body)) }

	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	for _, chunked := range []bool{false, true} {
		send := func(path, body string) *httptest.ResponseRecorder {
			req := newRequest("POST", path, strings.NewReader(body))
			req.Header.Set("Origin", testOrigin)
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(s)
			if chunked {
				req.ContentLength = -1
			}
			return r.recorder(req)
		}
		before, _ := r.daemon.decideCounts()
		if w := send("/api/decide-challenge", pad(r.withForm(approveBody, s), MaxBody)); w.Code != 200 {
			t.Errorf("chunked %v: a challenge body of MaxBody bytes: %d %s", chunked, w.Code, w.Body)
		}
		if n, _ := r.daemon.decideCounts(); n != before+1 {
			t.Errorf("chunked %v: the port was not asked", chunked)
		}
		w := send("/api/decide-challenge", pad(r.withForm(approveBody, s), MaxBody+1))
		if w.Code != 413 {
			t.Errorf("chunked %v: a challenge body of MaxBody+1 bytes: %d", chunked, w.Code)
		}
		if n, _ := r.daemon.decideCounts(); n != before+1 {
			t.Errorf("chunked %v: the port was asked for a body past the cap", chunked)
		}
		id := r.challengeOf(t, s, approveBody)
		_, decided := r.daemon.decideCounts()
		w = send("/api/decide", pad(decideBodyOf(id, testCredentialID), MaxBody+1))
		if w.Code != 413 {
			t.Errorf("chunked %v: a decide body of MaxBody+1 bytes: %d", chunked, w.Code)
		}
		id = r.challengeOf(t, s, approveBody)
		if w := send("/api/decide", pad(decideBodyOf(id, testCredentialID), MaxBody)); w.Code != 200 {
			t.Errorf("chunked %v: a decide body of MaxBody bytes: %d %s", chunked, w.Code, w.Body)
		}
		if _, n := r.daemon.decideCounts(); n != decided+1 {
			t.Errorf("chunked %v: %d decisions sent, want %d", chunked, n, decided+1)
		}
		r.openCard(t, s) // the decision settled the card; read it again
	}
}

// TestSeatDecideSendsTheKeptFrame: the decision sent is the frame the port
// froze for the challenge, kept by the app, with the browser's assertion
// and the session's credential; the answer is the daemon's state and line
// through Text.
func TestSeatDecideSendsTheKeptFrame(t *testing.T) {
	r := newRig(t)
	r.daemon.decideRes = DecideResult{State: "approved", Line: "approved \x1b[2J <b>on the phone</b>"}
	s := r.login(t)
	r.openCard(t, s)
	id := r.challengeOf(t, s, approveBody)
	w := r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s)
	if w.Code != 200 {
		t.Fatalf("decide: %d %s", w.Code, w.Body)
	}
	m := answer(t, w)
	if m["state"] != "approved" || m["line"] != Text("approved \x1b[2J <b>on the phone</b>", maxLine) || m["held"] != false || m["say"] != sayDone {
		t.Errorf("answer %v", m)
	}
	got := r.daemon.decides[0]
	want := DecisionFrame{ID: testCardID, BindingSHA256: strings.Repeat("ab", 32), Approve: true, SignedOff: []SignOff{{Kind: "exec-config", Path: "Makefile"}}}
	if got.ChallengeID != id || !reflect.DeepEqual(got.Frame, want) || !bytes.Equal(got.CredentialID, testCredentialID) ||
		!bytes.Equal(got.AuthenticatorData, bytes.Repeat([]byte{0xaa}, 37)) {
		t.Errorf("the port got %+v", got)
	}
	// The card was decided: it is not decided again until it is read again.
	wantFailure(t, "after the decision", r.tap(approveBody, s), 409, "cannot_decide", sayCannot)
}

// TestSeatDecideOnce: one challenge is sent at most once. A second tap with
// the same challenge, as a double tap makes, reaches nobody; a new challenge
// replaces the old one, which is then refused; an assertion of another
// credential than the session's is not sent; a challenge that ran out is
// not sent.
func TestSeatDecideOnce(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	id := r.challengeOf(t, s, approveBody)
	r.daemon.decideErr = ErrChallengeStale // a refusal, so the card stays as it was
	r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s)
	wantFailure(t, "a double tap", r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s), 409, "challenge_expired", sayTooLong)
	if _, n := r.daemon.decideCounts(); n != 1 {
		t.Errorf("%d decisions sent", n)
	}
	r.daemon.decideErr = nil

	first := r.challengeOf(t, s, approveBody)
	second := r.challengeOf(t, s, denyBody(""))
	if first == second {
		t.Fatal("one challenge twice")
	}
	wantFailure(t, "a replaced challenge", r.postWith("/api/decide", decideBodyOf(first, testCredentialID), s), 409, "challenge_expired", sayTooLong)
	wantFailure(t, "after a refused one", r.postWith("/api/decide", decideBodyOf(second, testCredentialID), s), 409, "challenge_expired", sayTooLong)

	id = r.challengeOf(t, s, approveBody)
	wantFailure(t, "another credential", r.postWith("/api/decide", decideBodyOf(id, bytes.Repeat([]byte{0x22}, 32)), s), 403, "assertion_refused", sayAssertion)

	id = r.challengeOf(t, s, approveBody)
	r.clock.Advance(decideTTL)
	wantFailure(t, "a challenge that ran out", r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s), 409, "challenge_expired", sayTooLong)
	// The daemon's own expiry, when sooner, is the challenge's.
	r.daemon.decideCh = func(c *DecideChallenge) { c.Expires = r.clock.Now().Add(time.Minute) }
	r.openCard(t, s)
	id = r.challengeOf(t, s, approveBody)
	r.clock.Advance(time.Minute)
	wantFailure(t, "past the daemon's expiry", r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s), 409, "challenge_expired", sayTooLong)
	if _, n := r.daemon.decideCounts(); n != 1 {
		t.Errorf("%d decisions sent", n)
	}
	// No session, no decision.
	wantFailure(t, "no session", r.postJSON("/api/decide", decideBodyOf(id, testCredentialID)), 401, "session_ended", sayNoSession)
}

// TestSeatDecideDropsWhatItWillNotSend: the app tells the port of each
// challenge whose decision it will never send, so that the room the port
// keeps for it goes at once: one a new tap of the session replaced, one
// burnt by a tap that named another challenge, by another credential's
// assertion or by its expiry, and one whose answer the app did not take.
// A challenge whose decision was sent is not dropped.
func TestSeatDecideDropsWhatItWillNotSend(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	var want []string
	first := r.challengeOf(t, s, approveBody)
	second := r.challengeOf(t, s, denyBody(""))
	want = append(want, first)
	r.postWith("/api/decide", decideBodyOf(first, testCredentialID), s)
	want = append(want, second)
	id := r.challengeOf(t, s, approveBody)
	r.postWith("/api/decide", decideBodyOf(id, bytes.Repeat([]byte{0x22}, 32)), s)
	want = append(want, id)
	id = r.challengeOf(t, s, approveBody)
	r.clock.Advance(decideTTL)
	r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s)
	want = append(want, id)
	r.openCard(t, s)
	// An answer of the port the app does not take.
	r.daemon.decideCh = func(c *DecideChallenge) { c.Frame.Approve = false }
	w := r.tap(approveBody, s)
	wantFailure(t, "a challenge of another frame", w, 502, "failed", sayFailed)
	r.daemon.decideCh = nil
	want = append(want, fmt.Sprintf("%016x", 1<<40+r.daemon.challengeNo))
	if got := r.daemon.droppedIDs(); !reflect.DeepEqual(got, want) {
		t.Errorf("dropped %v, want %v", got, want)
	}
	// A decision sent is not dropped, whatever its answer.
	id = r.challengeOf(t, s, approveBody)
	if w := r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s); w.Code != 200 {
		t.Fatalf("the decision: %d %s", w.Code, w.Body)
	}
	if got := r.daemon.droppedIDs(); !reflect.DeepEqual(got, want) {
		t.Errorf("a decision sent was dropped: %v", got)
	}
}

// TestSeatDecideCancelLeavesTheCard: a challenge the phone never answered (a
// cancelled or failed prompt) sends nothing and changes nothing: the card
// still offers the form, and the next tap asks a new challenge.
func TestSeatDecideCancelLeavesTheCard(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	r.challengeOf(t, s, approveBody)
	if body := r.openCard(t, s); !strings.Contains(body, `id="decide"`) {
		t.Error("the form is gone after a cancel")
	}
	id := r.challengeOf(t, s, approveBody)
	if w := r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s); w.Code != 200 {
		t.Errorf("the next tap: %d %s", w.Code, w.Body)
	}
	if c, d := r.daemon.decideCounts(); c != 2 || d != 1 {
		t.Errorf("%d challenges and %d decisions", c, d)
	}
}

// TestSeatDecideLostAnswer: when the answer to a decision is lost the
// phone says so, and the card is not decided again, by a tap or by the
// script, until it is read again: a read that fails changes nothing, a read
// that shows the decision on its way offers nothing, and a read that shows
// it pending offers the form again. A decision in flight blocks a second
// one too. Only a refusal settles a decision at once.
func TestSeatDecideLostAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		e    decideError
	}{
		{"outcome unknown", ErrOutcomeUnknown, errDecideUnknown},
		{"an error the app does not tell apart", errors.New("seatlink: the gate refused seat.decide: shutting-down <i>"), errDecideFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			s := r.login(t)
			r.openCard(t, s)
			id := r.challengeOf(t, s, approveBody)
			var during *httptest.ResponseRecorder
			r.daemon.decideHook = func() { during = r.tap(approveBody, s) }
			r.daemon.decideErr = tc.err
			w := r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s)
			wantFailure(t, "the lost answer", w, 502, tc.e.code, tc.e.say)
			if strings.Contains(w.Body.String(), "shutting") || strings.Contains(w.Body.String(), "<i>") {
				t.Error("the error's text is shown")
			}
			wantFailure(t, "a tap while it was in flight", during, 409, "sent_unknown", saySentUnknown)
			r.daemon.decideHook = nil
			wantFailure(t, "a tap after", r.tap(approveBody, s), 409, "sent_unknown", saySentUnknown)

			r.daemon.readErr = ErrDaemonUnavailable
			if w := r.getWith("/cards/"+testCardID, s); w.Code != 503 {
				t.Errorf("look again, the desk down: %d", w.Code)
			}
			wantFailure(t, "still unknown", r.tap(approveBody, s), 409, "sent_unknown", saySentUnknown)

			r.daemon.readErr = nil
			r.daemon.card.OnItsWay = true
			if body := r.openCard(t, s); strings.Contains(body, `id="decide"`) || !strings.Contains(body, "on its way to the door") {
				t.Error("on its way: the form is offered")
			}
			wantFailure(t, "on its way", r.tap(approveBody, s), 409, "cannot_decide", sayCannot)

			r.daemon.card.OnItsWay = false
			if body := r.openCard(t, s); !strings.Contains(body, `id="decide"`) {
				t.Error("pending again: no form")
			}
			r.daemon.decideErr = nil
			id = r.challengeOf(t, s, approveBody)
			if w := r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s); w.Code != 200 {
				t.Errorf("decided after looking again: %d %s", w.Code, w.Body)
			}
			if c, d := r.daemon.decideCounts(); c != 2 || d != 2 {
				t.Errorf("%d challenges and %d decisions", c, d)
			}
		})
	}
	// A refusal says nothing was decided: the next tap may come at once.
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	id := r.challengeOf(t, s, approveBody)
	r.daemon.decideErr = ErrAssertionRefused
	wantFailure(t, "refused", r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s), 403, "assertion_refused", sayAssertion)
	r.challengeOf(t, s, approveBody)
}

// TestSeatDecideHeld: an approve past the project's limit for the day comes
// back held: the host's line, the held words under it, and the card still
// pending, so a later approve may come with a new tap and a new assertion.
func TestSeatDecideHeld(t *testing.T) {
	r := newRig(t)
	r.daemon.decideRes = DecideResult{State: "pending", Line: "held: the limit of ataniq for today is reached", Held: true}
	s := r.login(t)
	r.openCard(t, s)
	id := r.challengeOf(t, s, approveBody)
	w := r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s)
	m := answer(t, w)
	if w.Code != 200 || m["held"] != true || m["state"] != "pending" || m["line"] != "held: the limit of ataniq for today is reached" ||
		m["say"] != "Held: the project's limit for today is reached. Nothing was applied. Raise the limit at the desk, then approve again here or at the desk." {
		t.Errorf("held: %d %v", w.Code, m)
	}
	r.challengeOf(t, s, approveBody)
	r.daemon.card.Held = true
	if body := r.openCard(t, s); !strings.Contains(body, "Held: decided past the limit.") || !strings.Contains(body, `id="decide"`) {
		t.Error("the held card: no mark or no form")
	}
}

// TestSeatDecideWords: each error of the port is fixed words of the app,
// with its code and status; no error's text is shown. A passkey that is no
// longer paired ends the session.
func TestSeatDecideWords(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
		say    string
	}{
		{ErrChallengeStale, 409, "challenge_stale", "This took too long, or the card changed. Look at the card and tap again."},
		{ErrCardChanged, 409, "card_changed", "The card changed. Look at it again."},
		{ErrCardExpired, 409, "card_expired", "The card's time ran out."},
		{ErrCannotDecide, 409, "cannot_decide", "The card cannot be decided now. Look at it again."},
		{ErrAssertionRefused, 403, "assertion_refused", "The phone's answer was not accepted. If this repeats, check the seat at the desk."},
		{ErrKindRefused, 403, "kind_refused", "This project does not take decisions from this kind of passkey."},
		{ErrBusy, 503, "desk_busy", "The desk is busy. Try again in a few minutes."},
		{ErrNotOffered, 503, "decide_unavailable", "The desk does not take decisions from the phone now."},
		{ErrDaemonUnavailable, 503, "daemon_unavailable", "The desk cannot be reached. Nothing was sent."},
		{ErrDecisionsInhibited, 503, "decisions_inhibited", "Decisions are paused on this host. Nothing was sent."},
		{ErrChallengeDiffers, 502, "challenge_differs", sayDiffers},
		{&NotAllowedError{Reason: "1 flagged path is not signed off"}, 409, "not_allowed", "This cannot be sent: 1 flagged path is not signed off."},
		{errors.New("a text <i>of the daemon</i>"), 502, "failed", "Something went wrong. Look at the card again."},
		{ErrKeyRevoked, 403, "key_revoked", "This phone's passkey is no longer paired. Pair it again at the desk."},
	} {
		for _, route := range []string{"/api/decide-challenge", "/api/decide"} {
			if route == "/api/decide" && errors.Is(tc.err, ErrChallengeDiffers) {
				continue
			}
			r := newRig(t)
			s := r.login(t)
			r.openCard(t, s)
			var w *httptest.ResponseRecorder
			if route == "/api/decide-challenge" {
				r.daemon.decideChErr = tc.err
				w = r.tap(approveBody, s)
			} else {
				id := r.challengeOf(t, s, approveBody)
				r.daemon.decideErr = tc.err
				w = r.postWith(route, decideBodyOf(id, testCredentialID), s)
			}
			wantFailure(t, route+" "+tc.code, w, tc.status, tc.code, tc.say)
			if strings.Contains(w.Body.String(), "of the daemon") {
				t.Errorf("%s %s: the error's text is shown", route, tc.code)
			}
			_, live := r.sessionOf(s.Value)
			gone := cookieOf(w, SessionCookie)
			if tc.code == "key_revoked" {
				if live || gone == nil || gone.MaxAge >= 0 {
					t.Errorf("%s: the session goes on after key_revoked", route)
				}
			} else if !live || gone != nil {
				t.Errorf("%s %s: the session ended", route, tc.code)
			}
		}
	}
}

// TestSeatDecideExpiredCard: a card past its expiry is not renewed by
// asking a new challenge: the app refuses it and asks the port nothing.
func TestSeatDecideExpiredCard(t *testing.T) {
	r := newRig(t)
	r.daemon.card.Expires = r.clock.Now().Add(10 * time.Minute)
	s := r.login(t)
	r.openCard(t, s)
	r.clock.Advance(10 * time.Minute)
	wantFailure(t, "expired", r.tap(approveBody, s), 409, "card_expired", sayExpired)
	wantFailure(t, "an expired deny", r.tap(denyBody(""), s), 409, "card_expired", sayExpired)
	if n, _ := r.daemon.decideCounts(); n != 0 {
		t.Errorf("the port was asked %d challenges", n)
	}
}

// TestSeatDecideRate: a credential's sessions ask at most 10 decision
// challenges a minute, so a phone cannot spend the gate's rate.
func TestSeatDecideRate(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	for i := 0; i < decideRequests; i++ {
		r.challengeOf(t, s, approveBody)
	}
	wantFailure(t, "past the bound", r.tap(approveBody, s), 429, "rate_limited", sayRate)
	if n, _ := r.daemon.decideCounts(); n != decideRequests {
		t.Errorf("the port was asked %d challenges", n)
	}
	r.clock.Advance(decideWindow)
	r.openCard(t, s)
	r.challengeOf(t, s, approveBody)
}

// TestSeatDecideScript: the decide script asks the passkey in one place,
// with user verification required and the session's one credential
// allowed, posts only to the app's own addresses, sends nothing by itself
// (no timer), keeps nothing to send later, and sets no text but with
// textContent.
func TestSeatDecideScript(t *testing.T) {
	js := newRig(t).do("GET", "/static/decide.js", nil, nil).Body.String()
	for _, want := range []string{
		`userVerification: "required"`, `allowCredentials: [{ type: "public-key", id: p.allow }]`,
		`"/api/decide-challenge"`, `"/api/decide"`, `credentials: "same-origin"`, `referrerPolicy: "strict-origin"`,
		`pending = null;`, "textContent", "Decision sent. Outcome unknown.", "Cancelled. Nothing was sent.",
		"This took too long. Tap again.", "The seat cannot be reached. Nothing was sent.",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	if n := strings.Count(js, "navigator.credentials.get("); n != 1 {
		t.Errorf("%d places ask for an assertion", n)
	}
	if n := strings.Count(js, `post("/api/decide",`); n != 1 {
		t.Errorf("%d places send a decision", n)
	}
	// A cancel sends nothing: the cancel button only goes back, and in
	// confirm every way the passkey sheet ends without an assertion goes
	// back and returns before the one place that sends the decision.
	_, onCancel, _ := strings.Cut(js, `cancelBtn.addEventListener("click", function () {`)
	onCancel, _, _ = strings.Cut(onCancel, "});")
	if !strings.Contains(onCancel, "back(SAY.cancelled);") || strings.Contains(onCancel, "post(") || strings.Contains(onCancel, "credentials") {
		t.Errorf("the cancel button does more than go back: %q", onCancel)
	}
	_, sheet, _ := strings.Cut(js, "cred = await navigator.credentials.get(")
	sheet, _, _ = strings.Cut(sheet, `post("/api/decide",`)
	for _, want := range []string{
		`if (e && e.name === "NotSupportedError") { back(SAY.unsupported); return; }`,
		`if (e && e.name === "SecurityError") { back(SAY.site_refused); return; }`,
		"back(SAY.cancelled); // NotAllowedError: cancelled, timed out, or no consent\n      return;",
		"if (!cred || !cred.response) { back(SAY.cancelled); return; }",
	} {
		if !strings.Contains(sheet, want) {
			t.Errorf("the passkey sheet's end before the decision is sent lacks %q", want)
		}
	}
	for _, bad := range []string{
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "Function(", "http://", "https://",
		"document.cookie", "setTimeout", "setInterval", "localStorage", "sessionStorage", "indexedDB", "serviceWorker", "sendBeacon",
		".submit(", "no-referrer",
	} {
		if strings.Contains(js, bad) {
			t.Errorf("the script holds %q", bad)
		}
	}
}

// TestSeatDecideChecksThePortsAnswer: a challenge whose frame is not the
// decision asked (another card, binding, action, sign-offs or reason), or
// that is not of its form, is not handed to the browser.
func TestSeatDecideChecksThePortsAnswer(t *testing.T) {
	for name, change := range map[string]func(*DecideChallenge){
		"another card":      func(c *DecideChallenge) { c.Frame.ID = "ap-20261122-0002" },
		"another binding":   func(c *DecideChallenge) { c.Frame.BindingSHA256 = strings.Repeat("cd", 32) },
		"another action":    func(c *DecideChallenge) { c.Frame.Approve = !c.Frame.Approve },
		"a sign-off less":   func(c *DecideChallenge) { c.Frame.SignedOff = nil },
		"a short challenge": func(c *DecideChallenge) { c.Challenge = c.Challenge[:31] },
		"a bad id":          func(c *DecideChallenge) { c.ID = "XYZ" },
		"already expired":   func(c *DecideChallenge) { c.Expires = time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC) },
	} {
		r := newRig(t)
		r.daemon.decideCh = change
		s := r.login(t)
		r.openCard(t, s)
		w := r.tap(approveBody, s)
		if w.Code == 200 || strings.Contains(w.Body.String(), "challenge\"") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	r := newRig(t)
	r.daemon.decideCh = func(c *DecideChallenge) { c.Frame.Reason = "another" }
	s := r.login(t)
	r.openCard(t, s)
	if w := r.tap(denyBody("mine"), s); w.Code == 200 {
		t.Errorf("another reason: %d %s", w.Code, w.Body)
	}
}

// TestSeatReadingsKeepTheSentMark: the table of a session's readings never
// drops one marked sent, whatever else is read: a card whose decision is in
// flight, or whose answer was lost, keeps its mark through 16 or more reads
// of other cards, and a lost answer after them still marks it again, so a
// read that began before the loss does not offer the form, and one that
// begins after it does. With every reading marked the table grows by the
// marks and drops none.
func TestSeatReadingsKeepTheSentMark(t *testing.T) {
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	ss := newSessions(func() time.Time { return now })
	var k tokenKey
	k[0] = 1
	ss.add(k, Session{})
	card := func(id string) Card { return Card{ID: id, BindingSHA256: "b" + id, State: "pending"} }
	read := func(id string) {
		t.Helper()
		if !ss.opened(k, card(id), now, ss.begin(), pageForm{}) {
			t.Fatalf("the read of %s was not noted", id)
		}
	}
	sentOf := func(id string) (sent, kept bool) {
		ss.with(k, func(s *session) {
			if r, ok := s.cards[id]; ok {
				sent, kept = r.sent, true
			}
		})
		return
	}
	size := func() (n int) {
		ss.with(k, func(s *session) { n = len(s.cards) })
		return
	}

	read("x")
	if !ss.markSent(k, "x") {
		t.Fatal("the decision of x was not marked sent")
	}
	// The other cards are read in another tab of the session, while x's
	// decision is in flight.
	for i := 0; i < 3*maxReadings; i++ {
		read(fmt.Sprintf("other-%d", i))
	}
	if sent, kept := sentOf("x"); !kept || !sent {
		t.Fatalf("after %d other reads x's reading: kept %v, sent %v", 3*maxReadings, kept, sent)
	}
	if n := size(); n != maxReadings {
		t.Errorf("%d readings, want the cap of %d", n, maxReadings)
	}
	// Its answer is lost: it is marked again, so a read that began before
	// the loss does not clear it, and one that begins after does.
	before := ss.begin()
	ss.lost(k, "x")
	if _, fresh := ss.peek(k, card("x"), now, before); fresh {
		t.Error("a read that began before the loss was taken as fresh")
	}
	if ss.opened(k, card("x"), now, before, pageForm{id: "f"}) {
		t.Error("a read that began before the loss offered the form")
	}
	if _, fresh := ss.peek(k, card("x"), now, ss.begin()); !fresh {
		t.Error("a read that began after the loss was not fresh")
	}
	if !ss.opened(k, card("x"), now, ss.begin(), pageForm{id: "f"}) {
		t.Error("a read that began after the loss did not offer the form")
	}
	if sent, _ := sentOf("x"); sent {
		t.Error("x is still marked sent after a read began after the loss")
	}

	// Every reading marked: the table grows past the cap by the marks.
	ss2 := newSessions(func() time.Time { return now })
	ss2.add(k, Session{})
	for i := 0; i < maxReadings+3; i++ {
		id := fmt.Sprintf("m-%d", i)
		if !ss2.opened(k, card(id), now, ss2.begin(), pageForm{}) || !ss2.markSent(k, id) {
			t.Fatalf("card %s was not read and marked", id)
		}
	}
	n := 0
	ss2.with(k, func(s *session) {
		n = len(s.cards)
		for id, r := range s.cards {
			if !r.sent {
				t.Errorf("%s lost its mark", id)
			}
		}
	})
	if n != maxReadings+3 {
		t.Errorf("%d readings, want %d marked ones all kept", n, maxReadings+3)
	}
}

// TestSeatDecideInhibitedSettles: a port that answers ErrDecisionsInhibited
// sent nothing, so the phone shows the paused words and the card is as it
// was: once decisions are taken again, a tap gets a challenge, without a
// read of the card in between, and the decide script lets the owner tap
// again after that answer.
func TestSeatDecideInhibitedSettles(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.openCard(t, s)
	r.daemon.decideChErr = ErrDecisionsInhibited
	wantFailure(t, "a paused challenge", r.tap(approveBody, s), 503, "decisions_inhibited", sayInhibited)
	r.daemon.decideChErr = nil
	id := r.challengeOf(t, s, approveBody)
	r.daemon.decideErr = ErrDecisionsInhibited
	wantFailure(t, "a paused decision", r.postWith("/api/decide", decideBodyOf(id, testCredentialID), s), 503, "decisions_inhibited", sayInhibited)
	r.daemon.decideErr = nil
	r.challengeOf(t, s, approveBody)

	js := r.do("GET", "/static/decide.js", nil, nil).Body.String()
	if !strings.Contains(js, `decisions_inhibited: true`) {
		t.Error("the script does not let the owner tap again after decisions_inhibited")
	}
}

// TestSeatCardWhileDecisionsAreInhibited: a port whose Offer says Inhibited
// gets a Card page that shows the notice and no decide form, no deciding
// script and no passkey, while the card stays readable (its facts, its
// paths page); a card that is not pending is not offered to the port at
// all, and once the port offers again, the form is back.
func TestSeatCardWhileDecisionsAreInhibited(t *testing.T) {
	r := newRig(t)
	s := r.login(t)
	r.daemon.offer = Offer{Approve: true, Deny: true, Inhibited: true}
	w := r.getWith("/cards/"+testCardID, s)
	checkHeaders(t, "a card while decisions are paused", w)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Decisions are paused on this host.") {
		t.Fatalf("no notice: %d\n%s", w.Code, body)
	}
	for _, not := range []string{`id="decide"`, "decide.js", `id="approve"`, `id="deny"`, `id="signoff-0"`, "<form", "data-form="} {
		if strings.Contains(body, not) {
			t.Errorf("the paused page holds %q", not)
		}
	}
	for _, want := range []string{"<h1>ataniq · feature finish seat-glue</h1>", "Merge the seat glue.", "refs/heads/develop", `id="back"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the card is not readable: no %q", want)
		}
	}
	if pp := w.Header().Get("Permissions-Policy"); strings.Contains(pp, "publickey-credentials-get=(self)") {
		t.Errorf("the page may ask for a passkey: %q", pp)
	}
	// The reads go on.
	for _, path := range []string{"/cards/", "/cards/" + testCardID + "/paths/0"} {
		if w := r.getWith(path, s); w.Code != 200 || strings.Contains(w.Body.String(), "Decisions are paused") {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	// A tap that a stale page makes is still refused by the port's own
	// answer, which is the enforcement.
	r.daemon.offer = Offer{Approve: true, Deny: true}
	if body := r.getWith("/cards/"+testCardID, s).Body.String(); !strings.Contains(body, `id="decide"`) || strings.Contains(body, "Decisions are paused") {
		t.Error("the form is not back once the port offers again")
	}
	// A card that is not pending asks the port nothing and shows no notice.
	r.daemon.offer = Offer{Inhibited: true}
	before := len(r.daemon.offers)
	r.daemon.card.State, r.daemon.card.Ended = "approved", true
	if body := r.getWith("/cards/"+testCardID, s).Body.String(); strings.Contains(body, "Decisions are paused") || len(r.daemon.offers) != before {
		t.Error("an ended card is offered to the port or shows the notice")
	}
}
