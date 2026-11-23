package seatlink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// testClock is a settable clock for the app.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// phoneRig is the decide rig with the seat app in front of the link, a
// clock before the test card's expiry, and a phone's browser logged in.
type phoneRig struct {
	*decideRig
	clock   *testClock
	browser *browser
	// page is the body of the last Card page open was answered with that
	// held the decide form: the page a tap is made on.
	page string
}

func newPhoneRig(t *testing.T) *phoneRig {
	t.Helper()
	return newPhoneRigAt(t, decideNow)
}

// newPhoneRigAt is newPhoneRig with the app's clock at start.
func newPhoneRigAt(t *testing.T, start time.Time) *phoneRig {
	t.Helper()
	r := &phoneRig{decideRig: newDecideRig(t), clock: &testClock{t: start}}
	h, err := seatapp.New(seatapp.Config{Origin: seatOrigin, RPID: seatRPID, Daemon: r.link, Now: r.clock.now, AllowNoFetchMetadataForTests: true})
	if err != nil {
		t.Fatal(err)
	}
	r.browser = newBrowser(h)
	if w := r.browser.loginWith(t, r.phone); w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	return r
}

// open shows the card on the Card page and lets a second pass.
func (r *phoneRig) open(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	w := r.browser.do("GET", "/cards/"+r.desk.reads.pending.ID, "")
	if pageForm(w.Body.String()) != "" {
		r.page = w.Body.String()
	}
	r.clock.advance(time.Second)
	return w
}

// pageForm is the id of the decide form of a Card page's body, "" for none.
func pageForm(page string) string {
	_, after, ok := strings.Cut(page, ` data-form="`)
	if !ok {
		return ""
	}
	form, _, _ := strings.Cut(after, `"`)
	return form
}

// tap posts a decision's tap, as the script of the last Card page open
// was answered with does: with that page's form.
func (r *phoneRig) tap(action string, signedOff []int, reason string) *httptest.ResponseRecorder {
	if signedOff == nil {
		signedOff = []int{}
	}
	body, _ := json.Marshal(map[string]any{"card": r.desk.reads.pending.ID, "form": pageForm(r.page), "action": action, "signed_off": signedOff, "reason": reason})
	return r.browser.do("POST", "/api/decide-challenge", string(body))
}

// confirm signs the challenge of a tap's answer with the phone, as the
// script does with navigator.credentials.get, and posts the assertion.
func (r *phoneRig) confirm(t *testing.T, tapped *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	var ch struct {
		ID        string   `json:"challenge_id"`
		Challenge string   `json:"challenge"`
		Allow     []string `json:"allow"`
	}
	if tapped.Code != 200 || json.Unmarshal(tapped.Body.Bytes(), &ch) != nil {
		t.Fatalf("tap: %d %s", tapped.Code, tapped.Body)
	}
	// The phone is asked for the session's one credential.
	if len(ch.Allow) != 1 || ch.Allow[0] != b64urlEnc(r.phone.CredentialID) {
		t.Errorf("allow %v", ch.Allow)
	}
	challenge, err := b64url(ch.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	as, err := r.phone.Get(challenge)
	if err != nil {
		t.Fatal(err)
	}
	return r.browser.do("POST", "/api/decide", decideJSON(ch.ID, as))
}

// tapPage posts a tap as the script of the Card page whose body is given
// does: with that page's form, and the Sec-Fetch-Site header a browser
// sends.
func (r *phoneRig) tapPage(page, action string, signedOff []int) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"card": r.desk.reads.pending.ID, "form": pageForm(page), "action": action, "signed_off": signedOff, "reason": ""})
	r.browser.fetchSite = true
	defer func() { r.browser.fetchSite = false }()
	return r.browser.do("POST", "/api/decide-challenge", string(body))
}

// replace makes the desk's card a replacement of the one shown: the same
// id, a binding of its own (another plan token), as a stand-in door that
// restarted and was asked again would.
func (r *phoneRig) replace(t *testing.T) {
	t.Helper()
	p := r.desk.reads.pending
	p.Binding.PlanToken = "h-00000001"
	digest, err := record.BindingDigest(p.Binding)
	if err != nil {
		t.Fatal(err)
	}
	p.BindingSHA256 = digest
	b, err := record.EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	r.desk.reads.mu.Lock()
	r.desk.reads.pending, r.desk.reads.bytes = p, b
	r.desk.reads.mu.Unlock()
}

func decideJSON(id string, as webauthntest.Assertion) string {
	var m map[string]string
	json.Unmarshal([]byte(loginJSON(as)), &m)
	m["challenge_id"] = id
	b, _ := json.Marshal(m)
	return string(b)
}

// said decodes an answer of the decision API.
func said(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("answer %d %q", w.Code, w.Body)
	}
	return m
}

// TestSeatDecideFromThePhone: the whole decision, from the Card page
// through the app, the link and the gate to a desk that recomputes the
// challenge and verifies with the daemon's verifier, with a software
// authenticator in the phone's place. An approve with both flagged paths
// signed off, and a deny of the flagged card with no box ticked. An approve
// with a sign-off missing is not offered and not sent.
func TestSeatDecideFromThePhone(t *testing.T) {
	r := newPhoneRig(t)
	w := r.open(t)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `id="signoff-1"`) || !strings.Contains(w.Body.String(), `id="approve"`) {
		t.Fatalf("card page: %d", w.Code)
	}
	if pp := w.Header().Get("Permissions-Policy"); !strings.Contains(pp, "publickey-credentials-get=(self),") {
		t.Errorf("Permissions-Policy %q", pp)
	}
	m := said(t, r.tap("approve", []int{0}, ""))
	if m["error"] != "not_allowed" || m["say"] != "This cannot be sent: 1 flagged path is not signed off." {
		t.Errorf("a sign-off missing: %v", m)
	}
	if c, _ := r.desk.counts(); c != 0 {
		t.Fatalf("the desk was asked %d challenges", c)
	}
	w = r.confirm(t, r.tap("approve", []int{1, 0}, ""))
	m = said(t, w)
	if w.Code != 200 || m["state"] != "approved" || m["held"] != false || m["say"] != "Sent. The desk answered:" {
		t.Errorf("approve: %d %v", w.Code, m)
	}
	p := r.desk.reads.pending
	f := r.desk.decided[0]
	if f.ID != p.ID || f.BindingSHA256 != p.BindingSHA256 || f.Decision != "approve" || len(f.SignedOff) != 2 ||
		f.SignedOff[0].Path != p.Binding.Summary.Flagged[0].Path || f.SignedOff[1].Path != p.Binding.Summary.Flagged[1].Path {
		t.Errorf("the desk got %+v", f)
	}

	r.open(t)
	w = r.confirm(t, r.tap("deny", nil, "not this one"))
	if m := said(t, w); w.Code != 200 || m["state"] != "denied" {
		t.Errorf("deny: %d %v", w.Code, m)
	}
	if f := r.desk.decided[1]; f.Decision != "deny" || len(f.SignedOff) != 0 || f.Reason != "not this one" {
		t.Errorf("the desk got %+v", f)
	}
}

// TestSeatDecideStaleTabOfOneSession: one session, two tabs. The first
// tab shows the card; the card is replaced (the same id, another binding)
// and the second tab shows the replacement, which the link now holds. The
// first tab's tap is refused "the card changed" before the desk is asked
// for a challenge, so the passkey is never asked for a card the tab did not
// show; the second tab's tap decides the replacement.
func TestSeatDecideStaleTabOfOneSession(t *testing.T) {
	r := newPhoneRig(t)
	first := r.open(t).Body.String()
	old := r.desk.reads.pending.BindingSHA256
	r.replace(t)
	second := r.open(t).Body.String()
	if !strings.Contains(first, "binding "+old[:12]) || !strings.Contains(second, "binding "+r.desk.reads.pending.BindingSHA256[:12]) {
		t.Fatal("the tabs do not show the two bindings")
	}
	w := r.tapPage(first, "approve", []int{0, 1})
	if m := said(t, w); w.Code != 409 || m["error"] != "card_changed" || m["challenge"] != nil {
		t.Errorf("the first tab's tap: %d %v", w.Code, m)
	}
	if c, _ := r.desk.counts(); c != 0 {
		t.Fatalf("the desk was asked %d challenges for the first tab", c)
	}
	w = r.confirm(t, r.tapPage(second, "approve", []int{0, 1}))
	if m := said(t, w); w.Code != 200 || m["state"] != "approved" {
		t.Errorf("the second tab's decision: %d %v", w.Code, m)
	}
	if f := r.desk.decided[0]; f.BindingSHA256 != r.desk.reads.pending.BindingSHA256 || f.BindingSHA256 == old {
		t.Errorf("the desk decided the binding %s", f.BindingSHA256)
	}
}

// TestSeatDecideCutFlaggedPathsOnThePhone: on the Card page whose two
// flagged paths are too long to be shown whole (and read the same there),
// Approve and the sign-off boxes are not offered, the page says why (the
// first part it cut: the two paths are changed paths too, in the rows
// above), and a tap of Approve anyway is refused before the desk is asked;
// Deny stays.
func TestSeatDecideCutFlaggedPathsOnThePhone(t *testing.T) {
	r := newPhoneRig(t)
	longFlagged(t, r.decideRig)
	page := r.open(t).Body.String()
	if strings.Contains(page, `id="approve"`) || strings.Contains(page, `id="signoff-0"`) || !strings.Contains(page, `id="deny"`) ||
		!strings.Contains(page, "Approve is not offered: "+reasonCutRows+".") {
		t.Fatal("the Card page offers Approve for flagged paths it cut")
	}
	w := r.tapPage(page, "approve", []int{0, 1})
	if m := said(t, w); w.Code != 409 || m["error"] != "not_allowed" || m["say"] != "This cannot be sent: "+reasonCutRows+"." {
		t.Errorf("an approve of cut flagged paths: %d %v", w.Code, m)
	}
	if c, _ := r.desk.counts(); c != 0 {
		t.Errorf("the desk was asked %d challenges", c)
	}
}

// TestSeatDecidePagesOfAReplacedReview: the pages of a card's whole path
// list read straight away, before its Card page, count for the review they
// were read for. The card is replaced by one of the same id and page count
// and another review; its Card page does not offer Approve, and a tap of
// Approve is refused before the desk is asked. Reading the new review's
// pages offers it.
func TestSeatDecidePagesOfAReplacedReview(t *testing.T) {
	r := newPhoneRig(t)
	truncated(t, r.decideRig)
	id := r.desk.reads.pending.ID
	for _, n := range []string{"0", "1"} {
		if w := r.browser.do("GET", "/cards/"+id+"/paths/"+n, ""); w.Code != 200 {
			t.Fatalf("page %s: %d %s", n, w.Code, w.Body)
		}
	}
	p := r.desk.reads.pending
	p.Binding.Summary.ReviewDigest = strings.Repeat("e", 64)
	digest, err := record.BindingDigest(p.Binding)
	if err != nil {
		t.Fatal(err)
	}
	p.BindingSHA256 = digest
	b, err := record.EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	r.desk.reads.mu.Lock()
	r.desk.reads.pending, r.desk.reads.bytes = p, b
	r.desk.reads.mu.Unlock()
	page := r.open(t).Body.String()
	if strings.Contains(page, `id="approve"`) {
		t.Error("the replacement is offered Approve on the pages of the old review")
	}
	if w := r.tapPage(page, "approve", []int{0, 1}); w.Code != 409 || said(t, w)["error"] != "not_allowed" {
		t.Errorf("a tap of Approve: %d %s", w.Code, w.Body)
	}
	if c, _ := r.desk.counts(); c != 0 {
		t.Errorf("the desk was asked %d challenges", c)
	}
	for _, n := range []string{"0", "1"} {
		r.browser.do("GET", "/cards/"+id+"/paths/"+n, "")
	}
	if page := r.open(t).Body.String(); !strings.Contains(page, `id="approve"`) {
		t.Error("the replacement's own pages read, Approve is not offered")
	}
}

// TestSeatDecideReadOnlyGateHasNoForm: on a gate whose hello lists no
// "decide", and on a link without the gate's pin, the Card page holds no
// decide form, loads no deciding script and does not allow
// publickey-credentials-get; it says the desk takes no decisions from the
// phone. A tap anyway is refused before anything is sent: the page served
// the session no form, so the tap names one it was never served, and is
// told to look again. The link itself refuses the challenge of a gate
// without "decide" as not offered.
func TestSeatDecideReadOnlyGateHasNoForm(t *testing.T) {
	for name, mk := range map[string]func(t *testing.T) (*decideRig, seatapp.Daemon){
		"no decide": func(t *testing.T) (*decideRig, seatapp.Daemon) {
			r := newDecideRig(t, "pair", "read", "login")
			return r, r.link
		},
		// A link without the gate's pin asks no login, so the session is
		// made through a pinned link to the same desk.
		"no pin": func(t *testing.T) (*decideRig, seatapp.Daemon) {
			r := newDecideRig(t)
			pinned := dialPinned(t, r.gate, r.seat)
			r.link.pin = ""
			return r, loginVia{Daemon: r.link, login: pinned}
		},
	} {
		r, port := mk(t)
		clock := &testClock{t: decideNow}
		h, err := seatapp.New(seatapp.Config{Origin: seatOrigin, RPID: seatRPID, Daemon: port, Now: clock.now, AllowNoFetchMetadataForTests: true})
		if err != nil {
			t.Fatal(err)
		}
		b := newBrowser(h)
		if w := b.loginWith(t, r.phone); w.Code != 200 {
			t.Fatalf("%s: login: %d %s", name, w.Code, w.Body)
		}
		w := b.do("GET", "/cards/"+r.desk.reads.pending.ID, "")
		body := w.Body.String()
		if w.Code != 200 || strings.Contains(body, `id="decide"`) || strings.Contains(body, "decide.js") ||
			!strings.Contains(body, "This card cannot be decided now: "+reasonNotOffered+".") {
			t.Errorf("%s: the Card page: %d", name, w.Code)
		}
		if pp := w.Header().Get("Permissions-Policy"); !strings.Contains(pp, "publickey-credentials-get=(),") {
			t.Errorf("%s: Permissions-Policy %q", name, pp)
		}
		clock.advance(time.Second)
		// The page has no form, so a tap anyway names one it was never served.
		tap, _ := json.Marshal(map[string]any{"card": r.desk.reads.pending.ID, "form": strings.Repeat("A", 22), "action": "approve", "signed_off": []int{0, 1}, "reason": ""})
		if w := b.do("POST", "/api/decide-challenge", string(tap)); w.Code != 409 || said(t, w)["error"] != "look_again" {
			t.Errorf("%s: a tap anyway: %d %s", name, w.Code, w.Body)
		}
		if name == "no decide" {
			if _, err := r.link.DecideChallenge(context.Background(), r.approve()); !errors.Is(err, seatapp.ErrNotOffered) {
				t.Errorf("%s: the link's challenge: %v, want %v", name, err, seatapp.ErrNotOffered)
			}
		}
		if c, _ := r.desk.counts(); c != 0 {
			t.Errorf("%s: the desk was asked %d challenges", name, c)
		}
	}
}

// loginVia is a port whose logins are another port's.
type loginVia struct {
	*Daemon
	login *Daemon
}

func (l loginVia) LoginChallenge(ctx context.Context) (seatapp.LoginChallenge, error) {
	return l.login.LoginChallenge(ctx)
}

func (l loginVia) Login(ctx context.Context, q seatapp.LoginRequest) (seatapp.LoginResult, error) {
	return l.login.Login(ctx, q)
}

// TestSeatDecideTamperedChallenge: a challenge the gate answers that is not
// the card's never reaches the browser, so the phone's passkey is never
// asked; nothing is decided.
func TestSeatDecideTamperedChallenge(t *testing.T) {
	r := newPhoneRig(t)
	r.open(t)
	r.desk.tamper = func(f ownerwire.DecisionFrame, res *ownerwire.SeatChallengeResult) {
		f.Decision, f.SignedOff = record.DecisionDeny, []record.SignOff{}
		res.Challenge, _ = ownerwire.DecisionChallenge(r.gate.id.Fingerprint(), f, res.Nonce)
	}
	w := r.tap("approve", []int{0, 1}, "")
	m := said(t, w)
	if w.Code != 502 || m["error"] != "challenge_differs" || m["challenge"] != nil || m["challenge_id"] != nil || m["allow"] != nil {
		t.Errorf("a tampered challenge: %d %v", w.Code, m)
	}
	// With nothing kept, a decide sends nothing.
	if w := r.browser.do("POST", "/api/decide", decideJSON("0123456789abcdef", webauthntest.Assertion{CredentialID: r.phone.CredentialID,
		AuthenticatorData: make([]byte, 37), ClientDataJSON: []byte("{}"), Signature: []byte{1}})); w.Code != 409 {
		t.Errorf("decide after: %d %s", w.Code, w.Body)
	}
	if _, d := r.desk.counts(); d != 0 {
		t.Errorf("%d decisions reached the desk", d)
	}
}

// TestSeatDecideDoubleTap: the same assertion posted twice reaches the desk
// once.
func TestSeatDecideDoubleTap(t *testing.T) {
	r := newPhoneRig(t)
	r.open(t)
	tapped := r.tap("approve", []int{0, 1}, "")
	var ch struct {
		ID        string `json:"challenge_id"`
		Challenge string `json:"challenge"`
	}
	json.Unmarshal(tapped.Body.Bytes(), &ch)
	c, _ := b64url(ch.Challenge)
	as, _ := r.phone.Get(c)
	if w := r.browser.do("POST", "/api/decide", decideJSON(ch.ID, as)); w.Code != 200 {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	if w := r.browser.do("POST", "/api/decide", decideJSON(ch.ID, as)); w.Code != 409 || said(t, w)["error"] != "challenge_expired" {
		t.Errorf("second: %d %s", w.Code, w.Body)
	}
	if _, d := r.desk.counts(); d != 1 {
		t.Errorf("%d decisions reached the desk", d)
	}
}

// TestSeatDecideLostAnswerOnThePhone: an answer that leaves the outcome open
// says so and is never followed by a second decision until the card is
// read again: on its way, then pending, and only then a new tap.
func TestSeatDecideLostAnswerOnThePhone(t *testing.T) {
	r := newPhoneRig(t)
	r.open(t)
	r.desk.outcome = func(context.Context, ownerwire.DecisionFrame) (json.RawMessage, error) {
		return nil, seatgate.NewCodedError("door-unavailable")
	}
	w := r.confirm(t, r.tap("approve", []int{0, 1}, ""))
	if m := said(t, w); w.Code != 502 || m["error"] != "outcome_unknown" || m["say"] != "Decision sent. Outcome unknown." {
		t.Errorf("lost: %d %v", w.Code, m)
	}
	if m := said(t, r.tap("approve", []int{0, 1}, "")); m["error"] != "sent_unknown" {
		t.Errorf("a tap after: %v", m)
	}
	r.desk.reads.show, _ = ownerwire.EncodeShowResult(ownerwire.ShowResult{Record: ownerwire.NewRecord(r.desk.reads.bytes), OnItsWay: true})
	if body := r.open(t).Body.String(); strings.Contains(body, `id="decide"`) || !strings.Contains(body, "on its way to the door") {
		t.Error("on its way: the form is offered")
	}
	r.desk.reads.show = nil
	r.desk.outcome = nil
	if body := r.open(t).Body.String(); !strings.Contains(body, `id="decide"`) {
		t.Error("pending again: no form")
	}
	if w := r.confirm(t, r.tap("approve", []int{0, 1}, "")); w.Code != 200 {
		t.Errorf("after looking again: %d %s", w.Code, w.Body)
	}
	if _, d := r.desk.counts(); d != 2 {
		t.Errorf("%d decisions reached the desk", d)
	}
}

// TestSeatDecideHeldOnThePhone: a held approve shows the host's line and the
// held words.
func TestSeatDecideHeldOnThePhone(t *testing.T) {
	r := newPhoneRig(t)
	r.open(t)
	r.desk.outcome = func(context.Context, ownerwire.DecisionFrame) (json.RawMessage, error) {
		return ownerwire.EncodeDecideResult(ownerwire.DecideResult{State: record.StatePending, Line: "held: the limit for today is reached", Held: true})
	}
	w := r.confirm(t, r.tap("approve", []int{0, 1}, ""))
	m := said(t, w)
	if w.Code != 200 || m["held"] != true || m["line"] != "held: the limit for today is reached" ||
		m["say"] != "Held: the project's limit for today is reached. Nothing was applied. Raise the limit at the desk, then approve again here or at the desk." {
		t.Errorf("held: %d %v", w.Code, m)
	}
}

// TestSeatDecideWordsOnThePhone: each code the gate answers a decision with
// is the contract's fixed words on the phone, through the app and the link;
// a code neither knows is the generic words, with nothing of the peer's.
func TestSeatDecideWordsOnThePhone(t *testing.T) {
	words := map[string]string{
		"challenge-stale":   "This took too long, or the card changed. Look at the card and tap again.",
		"binding-mismatch":  "The card changed. Look at it again.",
		"review-changed":    "The card changed. Look at it again.",
		"approval-expired":  "The card's time ran out.",
		"approval-consumed": "The card cannot be decided now. Look at it again.",
		"approval-void":     "The card cannot be decided now. Look at it again.",
		"previous-epoch":    "The card cannot be decided now. Look at it again.",
		"decision-invalid":  "The card cannot be decided now. Look at it again.",
		"assertion-invalid": "The phone's answer was not accepted. If this repeats, check the seat at the desk.",
		"key-revoked":       "This phone's passkey is no longer paired. Pair it again at the desk.",
		"key-unknown":       "This phone's passkey is no longer paired. Pair it again at the desk.",
		"kind-refused":      "This project does not take decisions from this kind of passkey.",
		"challenges-full":   "The desk is busy. Try again in a few minutes.",
		"busy":              "The desk is busy. Try again in a few minutes.",
		"rate-limited":      "The desk is busy. Try again in a few minutes.",
		"too-many-refusals": "The desk is busy. Try again in a few minutes.",
		"unavailable":       "Decision sent. Outcome unknown.",
		"deadline-exceeded": "Decision sent. Outcome unknown.",
		"door-unavailable":  "Decision sent. Outcome unknown.",
		"journal-reopened":  "Decision sent. Outcome unknown.",
		"failed":            "Decision sent. Outcome unknown.",
		"shutting-down":     "Something went wrong. Look at the card again.",
		"x-never-heard":     "Something went wrong. Look at the card again.",
	}
	for code, want := range words {
		t.Run(code, func(t *testing.T) {
			r := newPhoneRig(t)
			r.open(t)
			r.desk.outcome = func(context.Context, ownerwire.DecisionFrame) (json.RawMessage, error) {
				return nil, seatgate.NewCodedError(code)
			}
			w := r.confirm(t, r.tap("approve", []int{0, 1}, ""))
			if m := said(t, w); m["say"] != want || strings.Contains(w.Body.String(), `"`+code+`"`) {
				t.Errorf("%d %v, want %q", w.Code, m, want)
			}
			ended := r.browser.cookies[seatapp.SessionCookie] == nil
			if ended != (code == "key-revoked" || code == "key-unknown") {
				t.Errorf("the session ended: %v", ended)
			}
		})
	}
	// The challenge's refusals have the same words.
	r := newPhoneRig(t)
	r.open(t)
	r.desk.mu.Lock()
	r.desk.expired = true
	r.desk.mu.Unlock()
	if m := said(t, r.tap("approve", []int{0, 1}, "")); m["say"] != "The card's time ran out." {
		t.Errorf("an expired card at the desk: %v", m)
	}
}

// TestSeatDecideExpiredOnThePhone: a card past its expiry is offered
// nothing, and a tap asks the desk for no challenge.
func TestSeatDecideExpiredOnThePhone(t *testing.T) {
	expiry := time.Date(2026, 11, 21, 14, 2, 4, 0, time.UTC)
	r := newPhoneRigAt(t, expiry.Add(-5*time.Minute))
	r.open(t)
	r.clock.advance(5 * time.Minute)
	if m := said(t, r.tap("deny", nil, "")); m["error"] != "card_expired" {
		t.Errorf("tap: %v", m)
	}
	if c, _ := r.desk.counts(); c != 0 {
		t.Errorf("the desk was asked %d challenges", c)
	}
	w := r.browser.do("GET", "/cards/"+r.desk.reads.pending.ID, "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `id="decide"`) || !strings.Contains(w.Body.String(), "past its expiry") {
		t.Errorf("an expired card: %d, or it has the form", w.Code)
	}
}
