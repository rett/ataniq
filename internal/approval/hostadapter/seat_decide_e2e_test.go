//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/credkind"
	"ataniq/internal/approval/door"
	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/gatehost"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
	"ataniq/internal/seatlink"
)

// These tests run a decision from the phone through the whole chain on the
// loopback address, as the pairing tests do (seat_pair_e2e_test.go, whose
// world and helpers they use):
//
//	a request file in the drop folder, collected by the scratch collector
//	  -> the verification fakes and the real merge, review and pending record
//	  -> the stand-in door, which holds the card
//	webauthntest authenticator (the phone)
//	  -> HTTP -> seatapp (behind httptest) -> seatlink.Daemon
//	  -> seatgate.Client -> a TCP relay on 127.0.0.1 that only passes bytes
//	  -> TLS 1.3 with a pinned key at each end
//	  -> seatgate.Serve -> gatehost.NewDeciding
//	  -> the real ownerchannel.Service, on the real journal, the stand-in door
//	     and a key file in a private folder
//
// The relay carries the TLS of the seat and the gate end to end; it is there
// so that a test can cut the gate's connection after a decision was sent, or
// put the seat in front of another gate of the same daemon. Every refused case
// shows that the door holds no decision for the card, that the service does
// not show one on its way, and that the journal holds no decision but the
// ones the test made on purpose.

// The words the seat app's decision answers carry, as docs/design/seat-gate.md
// ("The phone's side") gives them and package seatapp writes them.
const (
	sayHeldWords    = "Held: the project's limit for today is reached. Nothing was applied."
	sayUnknownWords = "Decision sent. Outcome unknown."
	sayTooLongWords = "This took too long. Tap again."
	sayNotOffered   = "The desk does not take decisions from the phone now."
	heldMarkWords   = "Held: decided past the limit."
	decideFormMark  = `id="decide"`
)

// decideWorld is a pairing world (pairWorld) made for decisions: the door is
// behind a switch, the gate behind a relay, the gate pins a second seat key
// as during a rotation, and the service's fault hook is the test's.
type decideWorld struct {
	*pairWorld
	keyFile  *ownerjournal.KeyFile
	door     *swapDoor
	relay    *decRelay
	otherID  seatgate.Identity
	lose     atomic.Bool
	faultFor atomic.Pointer[func(string) error]
}

// newDecideWorld opens the owner channel on the scene change makes (nil adds
// feature.txt), with the stand-in door, a key file in a private folder and
// the pairing tests' presence window, and starts a deciding gate on
// 127.0.0.1 behind a relay. It starts no seat.
func newDecideWorld(t *testing.T, change func(l *lab)) *decideWorld {
	t.Helper()
	l := newLab(t)
	r := &rig{t: t, lab: l, scene: newScene(l, change), clock: doortest.NewClock(start), suite: newSuite(), proj: testProject(l)}
	dcfg := doortest.Sample(r.clock.Now)
	dcfg.Command = "qargi"
	r.stand = doortest.New(dcfg)
	r.state = privateDir(t)
	r.drop = privateDir(t)

	w := &decideWorld{door: &swapDoor{d: r.stand}}
	// Set before the service opens, and read through an atomic after.
	r.fault = func(point string) error {
		if f := w.faultFor.Load(); f != nil {
			return (*f)(point)
		}
		return nil
	}
	keys, err := ownerjournal.OpenKeys(filepath.Join(privateDir(t), "keys.log"))
	if err != nil {
		t.Fatalf("OpenKeys: %v", err)
	}
	cfg := r.config()
	cfg.Door = w.door
	cfg.Service.Keys = keys
	cfg.Service.PresenceWindow = pairPresenceWindow
	o, err := Open(context.Background(), cfg)
	if err != nil {
		_ = keys.Close()
		t.Fatalf("Open: %v", err)
	}
	r.o = o
	t.Cleanup(func() { _ = o.Close() })

	w.keyFile = keys
	w.pairWorld = &pairWorld{t: t, rig: r, svc: o.Service,
		gateID: newPairIdentity(t, "gate"), seatID: newPairIdentity(t, "seat")}
	w.otherID = newPairIdentity(t, "seat")
	w.gate = ownerchannel.Gate{Instance: w.gateID.Fingerprint(), Origin: pairOrigin, RPID: pairRPID}
	addr, calls := w.startGate(true)
	w.calls = calls
	w.relay = newDecRelay(t, addr)
	w.addr = w.relay.addr()
	return w
}

// startGate serves the owner channel on a new listener on 127.0.0.1 with the
// gate's identity, pinning both seat keys: a deciding handler, or, as a
// daemon without a seat origin makes it, one that serves pairing and reads
// only. It returns the address and the calls the handler served.
func (w *decideWorld) startGate(deciding bool) (string, *seenHandler) {
	w.t.Helper()
	pins := []string{w.seatID.Fingerprint(), w.otherID.Fingerprint()}
	pinned := func(fp string) bool { return slices.Contains(pins, fp) }
	var h *gatehost.Handler
	var err error
	if deciding {
		h, err = gatehost.NewDeciding(w.svc, w.svc, w.gate, pinned, nil)
	} else {
		h, err = gatehost.New(w.svc, pinned, nil)
	}
	if err != nil {
		w.t.Fatal(err)
	}
	calls := &seenHandler{h: h}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		w.t.Fatal(err)
	}
	addr := ln.Addr().String()
	if host, _, err := net.SplitHostPort(addr); err != nil || host != "127.0.0.1" {
		w.t.Fatalf("the gate listens on %q, not on the loopback address", addr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- seatgate.Serve(ctx, ln, seatgate.ServerConfig{
			TLS: seatgate.ServerTLS(w.gateID, pins), Caps: h.Caps(),
		}, &decHandler{w: w, next: calls})
	}()
	w.t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				w.t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			w.t.Error("Serve did not end")
		}
	})
	return addr, calls
}

// decHandler hands every call to the gate's handler, and when the test asked
// for it, cuts every connection of the relay once a seat.decide has been
// handled and before its answer is written: the decision is taken and its
// answer is lost.
type decHandler struct {
	w    *decideWorld
	next seatgate.Handler
}

func (h *decHandler) Handle(ctx context.Context, peer seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
	res, err := h.next.Handle(ctx, peer, op, params)
	if op == ownerwire.OpSeatDecide && h.w.lose.CompareAndSwap(true, false) {
		h.w.relay.cut()
	}
	return res, err
}

// onFault makes f the service's fault hook; nil clears it.
func (w *decideWorld) onFault(f func(point string) error) {
	if f == nil {
		w.faultFor.Store(nil)
		return
	}
	w.faultFor.Store(&f)
}

// decSeat is one seat: its own client of the gate, through the relay, with
// its own key, and the link and the app over it. The pairing world's helpers
// (post, get, login, register) run against its app.
type decSeat struct {
	*pairWorld
	c *seatgate.Client

	// forms holds the id of the decide form of the last Card page show
	// was answered with, for each session and card, as the page's tab
	// holds it: a tap names it.
	mu    sync.Mutex
	forms map[string]string
}

// formKey is the key of a session's Card page of a card in forms.
func formKey(sess *http.Cookie, card string) string { return sess.Value + " " + card }

// newSeat connects a seat with the key given and the gate's real pin, and
// puts a link and an app over it. Its client reconnects within a fraction of
// a second when the connection is lost.
func (w *decideWorld) newSeat(id seatgate.Identity) *decSeat {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pairCallTime)
	defer cancel()
	c, err := seatgate.Dial(ctx, w.relay.addr(), seatgate.ClientConfig{
		TLS: seatgate.ClientTLS(id, w.gateID.Fingerprint(), "gate"), Caps: seatlink.Caps(),
		DialTimeout: 5 * time.Second, HelloTimeout: 5 * time.Second,
		BackoffMin: 20 * time.Millisecond, BackoffMax: 100 * time.Millisecond,
	})
	if err != nil {
		w.t.Fatalf("the seat cannot reach the gate with the right pins: %v", err)
	}
	w.t.Cleanup(func() { _ = c.Close() })
	link, err := seatlink.New(c, seatlink.WithGatePin(w.gateID.Fingerprint()))
	if err != nil {
		w.t.Fatal(err)
	}
	app, err := seatapp.New(seatapp.Config{
		Origin: pairOrigin, RPID: pairRPID, Daemon: link, Now: w.rig.clock.Now,
		AllowInsecureLoopbackForTests: true, AllowNoFetchMetadataForTests: true,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	web := httptest.NewServer(app)
	w.t.Cleanup(web.Close)
	pw := *w.pairWorld
	pw.link, pw.web = link, web
	return &decSeat{pairWorld: &pw, c: c, forms: map[string]string{}}
}

// since is when the seat's present connection was made, or the zero time.
func (s *decSeat) since() time.Time {
	at, _ := s.c.Connected()
	return at
}

// waitReconnected waits until the seat has a connection made after before,
// whose gate lists "decide" or not as asked.
func (s *decSeat) waitReconnected(before time.Time, decide bool) {
	s.t.Helper()
	deadline := time.Now().Add(pairCallTime)
	for time.Now().Before(deadline) {
		if at, ok := s.c.Connected(); ok && at.After(before) && slices.Contains(s.c.Caps(), seatlink.CapDecide) == decide {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.t.Fatalf("the seat did not connect again (caps %q, last error %v)", s.c.Caps(), s.c.LastError())
}

// pair pairs the phone through the seat and confirms it at the desk.
func (w *decideWorld) pair(s *decSeat, phone *webauthntest.Authenticator) ownerwire.KeyItem {
	w.t.Helper()
	st := s.pairPending(phone)
	item, err := w.svc.PairConfirm(pairCtx(w.t), pairDesk, ownerwire.PairPendingParams{PendingID: st.PendingID})
	if err != nil {
		w.t.Fatalf("PairConfirm: %v", err)
	}
	return item
}

// logIn logs the phone in through the seat and returns the session's cookie.
func (s *decSeat) logIn(phone *webauthntest.Authenticator) *http.Cookie {
	s.t.Helper()
	ch, res := s.login(phone)
	if ch.status != http.StatusOK || res.status != http.StatusOK {
		s.t.Fatalf("the login: %d %q, %d %q", ch.status, ch.body, res.status, res.body)
	}
	c := res.cookie(seatapp.SessionCookie)
	if c == nil || c.MaxAge < 0 {
		s.t.Fatalf("the login made no session: %q", res.body)
	}
	return c
}

// feature makes a feature branch from the scene's root with the files given
// and returns its tip.
func (w *decideWorld) feature(name string, files ...string) string {
	w.t.Helper()
	l := w.rig.lab
	l.load(w.rig.scene.root)
	for i := 0; i+1 < len(files); i += 2 {
		l.put(files[i], files[i+1])
	}
	tip := l.commit("feature "+name+"\n", w.rig.scene.root)
	l.setRef(featureRefOf(name), tip)
	return tip
}

// ask drops a request file for the feature at its tip into the drop folder,
// has the collector take it, and returns the pending card it became.
func (w *decideWorld) ask(feature, tip string) string {
	w.t.Helper()
	id, b := w.rig.request(feature, func(rq *record.Request) { rq.FeatureSHA = tip })
	w.rig.dropFile(id, b)
	if n, err := w.rig.o.Collector.Scan(context.Background()); err != nil || n != 1 {
		w.t.Fatalf("Scan took %d requests: %v", n, err)
	}
	st, ok := w.svc.Status(id)
	if !ok || st.State != ownerchannel.RequestPending || st.PendingID == "" {
		w.t.Fatalf("the request is %+v, want a pending card", st)
	}
	return st.PendingID
}

// card asks for the scene's feature x and returns the card.
func (w *decideWorld) card() string { return w.ask("x", w.rig.scene.feature) }

// pendingOf reads the pending record of a card from the door.
func (w *decideWorld) pendingOf(id string) record.Pending {
	w.t.Helper()
	shown, err := w.door.cur().Show(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	p, err := record.DecodePending(shown.Pending.Bytes)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

// doorDecision returns the decision the door took for a card, and its state.
func (w *decideWorld) doorDecision(id string) (*record.Decision, record.State) {
	w.t.Helper()
	shown, err := w.door.cur().Show(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	if len(shown.Decision.Bytes) == 0 {
		return nil, shown.Entry.State
	}
	d, err := record.DecodeDecision(shown.Decision.Bytes)
	if err != nil {
		w.t.Fatal(err)
	}
	return &d, shown.Entry.State
}

// undecided checks that nothing was decided for the card: the door holds no
// decision and the card is pending, and the service shows no decision on its
// way, no hold and no outcome.
func (w *decideWorld) undecided(id, why string) {
	w.t.Helper()
	if d, st := w.doorDecision(id); d != nil || st != record.StatePending {
		w.t.Errorf("%s: the door holds %+v for the card, in the state %s", why, d, st)
	}
	sh, err := w.svc.Show(pairCtx(w.t), id)
	if err != nil {
		w.t.Fatalf("%s: Show: %v", why, err)
	}
	if sh.OnItsWay || sh.Held || sh.Outcome != nil {
		w.t.Errorf("%s: the service shows a decision of the card: on its way %v, held %v, outcome %+v", why, sh.OnItsWay, sh.Held, sh.Outcome)
	}
}

// journal closes the owner channel, which holds the journal, and opens the
// journal of the state folder. The test calls it last.
func (w *decideWorld) journal() *ownerjournal.Store {
	w.t.Helper()
	if err := w.rig.o.Close(); err != nil {
		w.t.Fatalf("Close: %v", err)
	}
	jr, err := ownerjournal.Open(filepath.Join(w.rig.state, OwnerDirName, JournalName))
	if err != nil {
		w.t.Fatalf("the journal: %v", err)
	}
	w.t.Cleanup(func() { _ = jr.Close() })
	return jr
}

// decisions reads the journal and returns its decision and held entries, and
// the count of every kind. The test calls it last.
func (w *decideWorld) decisions() ([]ownerjournal.Entry, map[ownerjournal.Kind]int) {
	w.t.Helper()
	es, err := w.journal().Entries(0, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []ownerjournal.Entry
	for _, e := range es {
		if e.Kind == ownerjournal.KindDecision || e.Kind == ownerjournal.KindHeld {
			out = append(out, e)
		}
	}
	return out, kinds(es)
}

// onlyDecided checks, last, that the journal holds a decision or a held
// entry for exactly the challenges given, in order, and nothing for any
// other: every refused attempt left none.
func (w *decideWorld) onlyDecided(challenges ...string) map[ownerjournal.Kind]int {
	w.t.Helper()
	es, k := w.decisions()
	var got []string
	for _, e := range es {
		d, err := record.DecodeDecision(e.Record)
		if err != nil {
			w.t.Fatal(err)
		}
		got = append(got, d.Seat.ChallengeID)
	}
	if !slices.Equal(got, challenges) {
		w.t.Errorf("the journal holds decisions of the challenges %q, want %q", got, challenges)
	}
	return k
}

// tap is the owner's tap on Approve or Deny on the Card page: POST
// /api/decide-challenge with the card, the id of the form of the last Card
// page of the card show was answered with for the session, the action, the
// indexes of the boxes ticked and the reason, as the page's script sends
// them.
func (s *decSeat) tap(sess *http.Cookie, card, action string, signedOff []int, reason string) reply {
	s.t.Helper()
	if signedOff == nil {
		signedOff = []int{}
	}
	s.mu.Lock()
	form, ok := s.forms[formKey(sess, card)]
	s.mu.Unlock()
	if !ok {
		s.t.Fatalf("a tap on %s before its Card page with the form was shown to the session", card)
	}
	return s.post("/api/decide-challenge", map[string]any{
		"card": card, "form": form, "action": action, "signed_off": signedOff, "reason": reason,
	}, sess)
}

// sign is the phone signing the challenge of a tap's answer.
func (s *decSeat) sign(phone *webauthntest.Authenticator, tap reply) webauthntest.Assertion {
	s.t.Helper()
	raw, err := b64u.DecodeString(tap.str(s.t, "challenge"))
	if err != nil {
		s.t.Fatal(err)
	}
	as, err := phone.Get(raw)
	if err != nil {
		s.t.Fatal(err)
	}
	return as
}

// send is the confirm: POST /api/decide with the challenge's id and the
// assertion.
func (s *decSeat) send(sess *http.Cookie, challengeID string, as webauthntest.Assertion) reply {
	s.t.Helper()
	return s.post("/api/decide", map[string]string{
		"challenge_id":       challengeID,
		"credential_id":      b64u.EncodeToString(as.CredentialID),
		"client_data_json":   b64u.EncodeToString(as.ClientDataJSON),
		"authenticator_data": b64u.EncodeToString(as.AuthenticatorData),
		"signature":          b64u.EncodeToString(as.Signature),
	}, sess)
}

// show opens the Card page, which the seat must answer, and lets a second
// pass, so that the card model does not take the tap for one made as the page
// opened.
func (s *decSeat) show(sess *http.Cookie, card string) string {
	s.t.Helper()
	page := s.get("/cards/"+card, sess)
	if page.status != http.StatusOK {
		s.t.Fatalf("GET /cards/%s: %d %q", card, page.status, page.body)
	}
	if _, after, ok := strings.Cut(string(page.body), ` data-form="`); ok {
		form, _, _ := strings.Cut(after, `"`)
		s.mu.Lock()
		s.forms[formKey(sess, card)] = form
		s.mu.Unlock()
	}
	s.rig.clock.Advance(time.Second)
	return string(page.body)
}

// tapOK taps and requires a challenge.
func (s *decSeat) tapOK(sess *http.Cookie, card, action string, signedOff []int, reason string) reply {
	s.t.Helper()
	t := s.tap(sess, card, action, signedOff, reason)
	if t.status != http.StatusOK {
		s.t.Fatalf("POST /api/decide-challenge for %s %s: %d %q", action, card, t.status, t.body)
	}
	return t
}

// decide is the whole decision on the phone: the Card page, the tap, the
// phone's assertion and the confirm. It returns the tap's answer, the
// assertion and the confirm's answer.
func (s *decSeat) decide(sess *http.Cookie, phone *webauthntest.Authenticator, card, action string, signedOff []int, reason string) (reply, webauthntest.Assertion, reply) {
	s.t.Helper()
	s.show(sess, card)
	t := s.tapOK(sess, card, action, signedOff, reason)
	as := s.sign(phone, t)
	return t, as, s.send(sess, t.str(s.t, "challenge_id"), as)
}

// wantDone checks a confirm's answer: 200, the state given, not held, and
// the host's line.
func wantDone(t *testing.T, res reply, state record.State) {
	t.Helper()
	if res.status != http.StatusOK {
		t.Fatalf("POST /api/decide: %d %q, want 200", res.status, res.body)
	}
	var held bool
	if err := json.Unmarshal(res.fields(t)["held"], &held); err != nil || held {
		t.Errorf("the answer is held (%v): %q", err, res.body)
	}
	if got := res.str(t, "state"); got != string(state) {
		t.Errorf("the answer's state is %q, want %q: %q", got, state, res.body)
	}
	if res.str(t, "line") == "" || !strings.HasPrefix(res.str(t, "say"), "Sent.") {
		t.Errorf("the answer has no line from the host: %q", res.body)
	}
}

// wantRefusedReply checks an answer of the app: the status and the code.
func wantRefusedReply(t *testing.T, res reply, status int, code, why string) {
	t.Helper()
	if res.status != status || res.code(t) != code {
		t.Errorf("%s: the app answers %d %q, want %d %s", why, res.status, res.body, status, code)
	}
}

// appFrame is the frame of a card as the app keeps it: an approve with every
// flagged path signed off, or a deny with the reason.
func appFrame(p record.Pending, approve bool, reason string) seatapp.DecisionFrame {
	f := frame(p, approve)
	out := seatapp.DecisionFrame{ID: f.ID, BindingSHA256: f.BindingSHA256, Approve: approve, Reason: reason}
	for _, so := range f.SignedOff {
		out.SignedOff = append(out.SignedOff, seatapp.SignOff{Kind: so.Kind, Path: string(so.Path)})
	}
	return out
}

// linkDecide hands the daemon a decision through the seat's link, with no app
// between: what a seat that does not keep the app's rules could send.
func (s *decSeat) linkDecide(challengeID string, f seatapp.DecisionFrame, as webauthntest.Assertion) (seatapp.DecideResult, error) {
	s.t.Helper()
	return s.link.Decide(pairCtx(s.t), seatapp.DecideRequest{
		ChallengeID: challengeID, Frame: f, CredentialID: as.CredentialID,
		AuthenticatorData: as.AuthenticatorData, ClientDataJSON: as.ClientDataJSON, Signature: as.Signature,
	})
}

// rawSeat is a second connection of a seat's key to the gate, through the
// relay, that sends the gate's words as it likes: a seat taken over.
func (w *decideWorld) rawSeat(id seatgate.Identity) *seatgate.Client {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pairCallTime)
	defer cancel()
	c, err := seatgate.Dial(ctx, w.relay.addr(), seatgate.ClientConfig{
		TLS: seatgate.ClientTLS(id, w.gateID.Fingerprint(), "gate"), Caps: seatlink.Caps(),
		DialTimeout: 5 * time.Second, HelloTimeout: 5 * time.Second,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = c.Close() })
	return c
}

// rawChallenge asks seat.challenge for a frame and a credential.
func rawChallenge(t *testing.T, c *seatgate.Client, f ownerwire.DecisionFrame, credential string) ownerwire.SeatChallengeResult {
	t.Helper()
	params, err := ownerwire.EncodeSeatChallengeParams(ownerwire.SeatChallengeParams{Frame: f, Credential: credential})
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err := c.Call(pairCtx(t), ownerwire.OpSeatChallenge, json.RawMessage(params), &raw); err != nil {
		t.Fatalf("seat.challenge: %v", err)
	}
	res, err := ownerwire.DecodeSeatChallengeResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// rawDecide sends seat.decide and returns the gate's refusal code, or "" and
// the result.
func rawDecide(t *testing.T, c *seatgate.Client, challengeID string, f ownerwire.DecisionFrame, as webauthntest.Assertion) (string, ownerwire.DecideResult) {
	t.Helper()
	params, err := ownerwire.EncodeSeatDecideParams(ownerwire.SeatDecideParams{
		ChallengeID: challengeID, Frame: f,
		Assertion: ownerwire.Assertion{CredentialID: as.CredentialID, AuthenticatorData: as.AuthenticatorData,
			ClientDataJSON: as.ClientDataJSON, Signature: as.Signature},
	})
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	err = c.Call(pairCtx(t), ownerwire.OpSeatDecide, json.RawMessage(params), &raw)
	var re *seatgate.RemoteError
	switch {
	case errors.As(err, &re):
		return re.Code, ownerwire.DecideResult{}
	case err != nil:
		t.Fatalf("seat.decide: %v", err)
	}
	res, err := ownerwire.DecodeDecideResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	return "", res
}

// decideSetup is the start of most tests: a world on the scene change makes, one
// seat, the phone paired and confirmed at the desk, a card asked for by a
// request file, and the phone logged in.
func decideSetup(t *testing.T, change func(l *lab)) (w *decideWorld, s *decSeat, phone *webauthntest.Authenticator, item ownerwire.KeyItem, card string, sess *http.Cookie) {
	t.Helper()
	w = newDecideWorld(t, change)
	s = w.newSeat(w.seatID)
	phone = newPhone(t)
	item = w.pair(s, phone)
	card = w.card()
	sess = s.logIn(phone)
	return w, s, phone, item, card, sess
}

// The decision, whole: a request file becomes a card, the phone that paired
// at the desk logs in, lists the card and shows it, signs off its flagged
// path, taps Approve, and its passkey signs the daemon's challenge; the
// stand-in door takes the approve, the ref moves, and the outcome reaches the
// phone. The journal keeps the decision with the evidence of the assertion,
// from which an audit, with the key list alone, recomputes the challenge and
// checks the signature again.
func TestSeatDecideE2E(t *testing.T) {
	w := newDecideWorld(t, execFeature)
	s := w.newSeat(w.seatID)
	phone := newPhone(t)
	item := w.pair(s, phone)
	card := w.card()
	p := w.pendingOf(card)
	if p.Binding.Summary == nil || len(p.Binding.Summary.Flagged) != 1 || p.Binding.Summary.Flagged[0].Path != "Makefile" {
		t.Fatalf("the card's flagged paths are %+v, want the Makefile", p.Binding.Summary)
	}
	sess := s.logIn(phone)

	// The list, then the card, with its decide form and the box of its
	// flagged path.
	list := s.get("/cards/", sess)
	if list.status != http.StatusOK || !strings.Contains(string(list.body), card) {
		t.Fatalf("the list does not show the card: %d %q", list.status, list.body)
	}
	page := s.show(sess, card)
	for _, want := range []string{decideFormMark, `id="approve"`, `id="deny"`, `id="signoff-0"`, "Makefile", "STAND-IN"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Card page has no %s", want)
		}
	}

	tap := s.tapOK(sess, card, "approve", []int{0}, "")
	var allow []string
	if err := json.Unmarshal(tap.fields(t)["allow"], &allow); err != nil || !slices.Equal(allow, []string{b64u.EncodeToString(phone.CredentialID)}) {
		t.Errorf("the decision allows %q (%v), want the session's credential alone", allow, err)
	}
	if tap.str(t, "card") != card || tap.str(t, "binding") != p.BindingSHA256[:12] || tap.str(t, "action") != "approve" {
		t.Errorf("the tap's answer names %q", tap.body)
	}
	challengeID := tap.str(t, "challenge_id")
	as := s.sign(phone, tap)
	if before := w.calls.count(ownerwire.OpSeatDecide); before != 0 {
		t.Fatalf("seat.decide was served %d times before the confirm", before)
	}
	res := s.send(sess, challengeID, as)
	wantDone(t, res, record.StateApplied)

	// The door took the approve, confirmed by the passkey, and the ref
	// moved to the merge the card showed.
	d, st := w.doorDecision(card)
	if d == nil || st != record.StateApplied {
		t.Fatalf("the door holds %+v in the state %s, want the approve applied", d, st)
	}
	wantSeat := record.SeatBlock{
		AssertionSHA256: record.AssertionDigest(as.CredentialID, as.AuthenticatorData, as.ClientDataJSON, as.Signature),
		ChallengeID:     challengeID, Credential: item.ID, Session: w.seatID.Fingerprint()[:16],
	}
	if d.Decision != record.DecisionApprove || d.Confirmed != record.ConfirmedPasskey || d.Seat != wantSeat ||
		d.Console != (record.Console{}) || len(d.SignedOff) != 1 || d.SignedOff[0].Path != "Makefile" {
		t.Errorf("the door's decision is %+v, want an approve by passkey with the seat block %+v", d, wantSeat)
	}
	if got := w.rig.lab.ref(targetBranch); got != p.Binding.Merge.Commit {
		t.Errorf("the target is at %s, want the card's merge %s", got, p.Binding.Merge.Commit)
	}

	// The outcome reaches the seat: the card, read again, says applied and
	// offers nothing.
	again := s.get("/cards/"+card, sess)
	if again.status != http.StatusOK || !strings.Contains(string(again.body), string(record.StateApplied)) || strings.Contains(string(again.body), decideFormMark) {
		t.Errorf("the card read again: %d, applied shown %v, form shown %v", again.status,
			strings.Contains(string(again.body), string(record.StateApplied)), strings.Contains(string(again.body), decideFormMark))
	}
	if w.calls.count(ownerwire.OpSeatChallenge) != 1 || w.calls.count(ownerwire.OpSeatDecide) != 1 {
		t.Errorf("the gate served %q, want one challenge and one decide", w.calls.seen())
	}

	// The audit: the key list (a revoked key stays on it) and the journal
	// alone.
	stored, err := w.keyFile.Keys()
	if err != nil {
		t.Fatal(err)
	}
	jr := w.journal()
	es, err := jr.Entries(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if k := kinds(es); k[ownerjournal.KindDecision] != 1 || k[ownerjournal.KindHeld] != 0 {
		t.Fatalf("the journal holds %v, want one decision", k)
	}
	for _, e := range es {
		if e.Kind == ownerjournal.KindDecision {
			auditSeatDecision(t, jr, e, stored)
		}
	}
}

// auditSeatDecision checks a decision of the journal as an auditor would, from the
// journal and the key list only: the evidence is the record's assertion, the
// challenge recomputed from the record and the evidence's nonce and instance
// is the one the client data names, and the signature verifies with the
// credential's key from the list, by the verifier and again by hand.
func auditSeatDecision(t *testing.T, jr *ownerjournal.Store, e ownerjournal.Entry, keys []ownerjournal.StoredKey) {
	t.Helper()
	rec, err := record.DecodeDecision(e.Record)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := jr.EvidenceOf(e.Seq)
	if err != nil {
		t.Fatalf("EvidenceOf: %v", err)
	}
	if got := record.AssertionDigest(ev.CredentialID, ev.AuthenticatorData, ev.ClientDataJSON, ev.Signature); got != rec.Seat.AssertionSHA256 {
		t.Errorf("the evidence's assertion digest is %s, the record's %s", got, rec.Seat.AssertionSHA256)
	}
	if ev.Origin != pairOrigin {
		t.Errorf("the evidence's origin is %q", ev.Origin)
	}
	var key *ownerjournal.StoredKey
	for i := range keys {
		if keys[i].ID == rec.Seat.Credential {
			key = &keys[i]
		}
	}
	if key == nil || ownerjournal.KeyDisplayID(ev.CredentialID) != rec.Seat.Credential {
		t.Fatalf("the credential %s of the record is not on the key list", rec.Seat.Credential)
	}
	f := ownerwire.DecisionFrame{ID: rec.ID, BindingSHA256: rec.BindingSHA256, Decision: rec.Decision,
		SignedOff: append([]record.SignOff{}, rec.SignedOff...), Reason: rec.Reason}
	ch, err := ownerwire.DecisionChallenge(ev.Instance, f, ev.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	var cd struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(ev.ClientDataJSON, &cd); err != nil || cd.Challenge != b64u.EncodeToString(ch) || cd.Type != "webauthn.get" || cd.Origin != ev.Origin {
		t.Errorf("the client data %q does not name the recomputed challenge (%v)", ev.ClientDataJSON, err)
	}
	cred := credkind.Credential{Kind: key.Kind, CredentialID: key.CredentialID, SPKI: key.PublicKey, AAGUID: key.AAGUID,
		BackupEligible: key.BackupEligible, BackupState: key.BackupState && key.BackupEligible}
	if _, err := credkind.Verify(cred, credkind.AssertionInput{CredentialID: ev.CredentialID, ClientDataJSON: ev.ClientDataJSON,
		AuthenticatorData: ev.AuthenticatorData, Signature: ev.Signature}, credkind.Expect{Challenge: ch, Origin: ev.Origin, RPID: pairRPID}); err != nil {
		t.Errorf("the verifier refuses the journal's evidence: %v", err)
	}
	pub, err := x509.ParsePKIXPublicKey(key.PublicKey)
	ec, ok := pub.(*ecdsa.PublicKey)
	if err != nil || !ok || !ecdsa.VerifyASN1(ec, webauthntest.SignedDigest(ev.AuthenticatorData, ev.ClientDataJSON), ev.Signature) {
		t.Errorf("the signature does not verify with the listed key (%v)", err)
	}
}

// A deny on a card with a flagged path needs no sign-off, and a fresh
// assertion as an approve does: the door takes the deny with the reason and
// no sign-off. An approve without the box ticked is not even asked of the
// daemon.
func TestSeatDecideE2EDenyFlagged(t *testing.T) {
	w, s, phone, _, card, sess := decideSetup(t, execFeature)
	s.show(sess, card)
	none := s.tap(sess, card, "approve", nil, "")
	wantRefusedReply(t, none, http.StatusConflict, "not_allowed", "an approve with no sign-off")
	if n := w.calls.count(ownerwire.OpSeatChallenge); n != 0 {
		t.Errorf("an approve with no sign-off was sent: %q", w.calls.seen())
	}

	tap, _, res := s.decide(sess, phone, card, "deny", nil, "not this one")
	if tap.str(t, "action") != "deny" {
		t.Errorf("the tap's answer is %q", tap.body)
	}
	wantDone(t, res, record.StateDenied)
	d, st := w.doorDecision(card)
	if d == nil || st != record.StateDenied || d.Decision != record.DecisionDeny || len(d.SignedOff) != 0 ||
		d.Reason != "not this one" || d.Confirmed != record.ConfirmedPasskey {
		t.Errorf("the door holds %+v in the state %s, want the deny by passkey with the reason", d, st)
	}
	if got := w.rig.lab.ref(targetBranch); got != w.rig.scene.target {
		t.Errorf("a deny moved the target to %s", got)
	}
	if k := w.onlyDecided(tap.str(t, "challenge_id")); k[ownerjournal.KindUse] != 0 {
		t.Errorf("a deny used the card: %v", k)
	}
}

// Past the project's limit for the day an approve from the phone is held: the
// card stays pending, nothing reaches the door, no use is counted, and the
// phone says held and shows the held mark on the card and in the list.
func TestSeatDecideE2EHeld(t *testing.T) {
	w := newDecideWorld(t, nil)
	s := w.newSeat(w.seatID)
	phone := newPhone(t)
	w.pair(s, phone)
	if err := w.svc.Beat(pairDesk); err != nil {
		t.Fatal(err)
	}
	if v, err := w.svc.LimitSet(pairCtx(t), pairDesk, ownerwire.LimitSetParams{Project: "project-a", To: 1}); err != nil || v.Limit != 1 {
		t.Fatalf("LimitSet: %+v, %v", v, err)
	}
	first := w.card()
	second := w.ask("y", w.feature("y", "y.txt", "y\n"))
	sess := s.logIn(phone)

	t1, _, res := s.decide(sess, phone, first, "approve", nil, "")
	wantDone(t, res, record.StateApplied)

	t2, _, held := s.decide(sess, phone, second, "approve", nil, "")
	if held.status != http.StatusOK {
		t.Fatalf("the approve past the limit: %d %q", held.status, held.body)
	}
	var isHeld bool
	if err := json.Unmarshal(held.fields(t)["held"], &isHeld); err != nil || !isHeld ||
		held.str(t, "state") != string(record.StatePending) || !strings.Contains(held.str(t, "say"), sayHeldWords) {
		t.Errorf("the approve past the limit answers %q, want held and pending with the held words", held.body)
	}
	w.undecidedButHeld(second)
	page := s.get("/cards/"+second, sess)
	if page.status != http.StatusOK || !strings.Contains(string(page.body), heldMarkWords) {
		t.Errorf("the card does not show the held mark: %d", page.status)
	}
	list := s.get("/cards/", sess)
	if list.status != http.StatusOK || !strings.Contains(string(list.body), heldMarkWords) {
		t.Errorf("the list does not show the held mark: %d", list.status)
	}
	// The day's uses are the journal's use entries: the first approve's
	// alone.
	k := w.onlyDecided(t1.str(t, "challenge_id"), t2.str(t, "challenge_id"))
	if k[ownerjournal.KindDecision] != 1 || k[ownerjournal.KindHeld] != 1 || k[ownerjournal.KindUse] != 1 {
		t.Errorf("the journal holds %v, want one decision, one held and one use", k)
	}
}

// undecidedButHeld checks that the door holds no decision for a held card and
// the service shows it held.
func (w *decideWorld) undecidedButHeld(id string) {
	w.t.Helper()
	if d, st := w.doorDecision(id); d != nil || st != record.StatePending {
		w.t.Errorf("the door holds %+v for the held card, in the state %s", d, st)
	}
	sh, err := w.svc.Show(pairCtx(w.t), id)
	if err != nil || !sh.Held || sh.OnItsWay {
		w.t.Errorf("the service shows the held card held %v, on its way %v (%v)", sh.Held, sh.OnItsWay, err)
	}
}

// A challenge is used once. The confirm sent again is refused by the seat,
// which burnt its copy; the same challenge, frame and assertion sent to the
// daemon again by the seat's link are refused challenge-stale. A challenge is
// burnt before the assertion is judged: after a refused assertion, a good one
// over the same challenge is refused as well, and nothing is decided.
func TestSeatDecideE2EReplay(t *testing.T) {
	w, s, phone, _, card, sess := decideSetup(t, nil)
	second := w.ask("y", w.feature("y", "y.txt", "y\n"))
	tap, as, res := s.decide(sess, phone, card, "approve", nil, "")
	wantDone(t, res, record.StateApplied)
	challengeID := tap.str(t, "challenge_id")

	again := s.send(sess, challengeID, as)
	wantRefusedReply(t, again, http.StatusConflict, "challenge_expired", "the confirm sent again")
	if n := w.calls.count(ownerwire.OpSeatDecide); n != 1 {
		t.Errorf("the seat sent the decision again: seat.decide served %d times", n)
	}
	if _, err := s.linkDecide(challengeID, appFrame(w.pendingOf(card), true, ""), as); !errors.Is(err, seatapp.ErrChallengeStale) {
		t.Errorf("the used challenge sent again to the daemon: %v, want %v", err, seatapp.ErrChallengeStale)
	}

	// The burn comes first: a refused assertion uses the challenge up.
	s.show(sess, second)
	t2 := s.tapOK(sess, second, "approve", nil, "")
	phone.Flags &^= webauthntest.FlagUV
	bad := s.sign(phone, t2)
	phone.Flags |= webauthntest.FlagUV
	refused := s.send(sess, t2.str(t, "challenge_id"), bad)
	wantRefusedReply(t, refused, http.StatusForbidden, "assertion_refused", "an assertion without user verification")
	good := s.sign(phone, t2)
	if _, err := s.linkDecide(t2.str(t, "challenge_id"), appFrame(w.pendingOf(second), true, ""), good); !errors.Is(err, seatapp.ErrChallengeStale) {
		t.Errorf("a good assertion over a challenge a refusal burnt: %v, want %v", err, seatapp.ErrChallengeStale)
	}
	w.undecided(second, "after the refusals")
	w.onlyDecided(challengeID)
}

// An assertion counts only for the very decision it signed. The phone signs
// the challenge of another card, and the seat sends it for this one; a seat
// taken over gets the nonce of a challenge and has the phone sign the
// challenge of another decision, sign-off set or reason over it, then sends
// it with the frame the challenge was drawn for (the signature does not
// verify) or with the frame it signed (the frame is not the challenge's).
// Each is refused, and nothing is decided; the right assertion then decides.
func TestSeatDecideE2EAssertionOverAnother(t *testing.T) {
	w, s, phone, item, card, sess := decideSetup(t, execFeature)
	other := w.ask("y", w.feature("y", "y.txt", "y\n", "Makefile", "y:\n\ttrue\n"))

	// Through the app: two sessions of the phone, one on each card. The
	// phone signs the other card's challenge.
	sess2 := s.logIn(phone)
	s.show(sess, card)
	mine := s.tapOK(sess, card, "approve", []int{0}, "")
	s.show(sess2, other)
	theirs := s.tapOK(sess2, other, "approve", []int{0}, "")
	res := s.send(sess, mine.str(t, "challenge_id"), s.sign(phone, theirs))
	wantRefusedReply(t, res, http.StatusForbidden, "assertion_refused", "the assertion over another card's challenge")
	w.undecided(card, "the assertion over another card's challenge")
	w.undecided(other, "the assertion over another card's challenge")

	// Through the gate, with the seat's own key.
	raw := w.rawSeat(w.seatID)
	p, q := w.pendingOf(card), w.pendingOf(other)
	approve := frame(p, true)
	deny := frame(p, false)
	deny.Reason = "not this one"
	otherSet := frame(p, true)
	otherSet.SignedOff = []record.SignOff{{Kind: record.FlagOddPath, Path: "Makefile"}}
	otherReason := deny
	otherReason.Reason = "another reason"
	cases := []struct {
		name          string
		drawn, signed ownerwire.DecisionFrame
	}{
		{"another card", approve, frame(q, true)},
		{"another decision", approve, deny},
		{"another sign-off set", approve, otherSet},
		{"another reason", deny, otherReason},
	}
	for _, c := range cases {
		for _, sendSigned := range []bool{false, true} {
			ch := rawChallenge(t, raw, c.drawn, item.ID)
			alt, err := ownerwire.DecisionChallenge(w.gateID.Fingerprint(), c.signed, ch.Nonce)
			if err != nil {
				t.Fatal(err)
			}
			as, err := phone.Get(alt)
			if err != nil {
				t.Fatal(err)
			}
			sent, want := c.drawn, ownerwire.CodeAssertionInvalid
			if sendSigned {
				sent, want = c.signed, ownerwire.CodeChallengeStale
			}
			if code, res := rawDecide(t, raw, ch.ChallengeID, sent, as); code != want {
				t.Errorf("%s, sent with the frame %s: the gate answers %q %+v, want %s", c.name, map[bool]string{false: "drawn", true: "signed"}[sendSigned], code, res, want)
			}
		}
	}
	w.undecided(card, "the assertions over other decisions")
	w.undecided(other, "the assertions over other decisions")

	// The assertion over the frame the challenge was drawn for decides.
	ch := rawChallenge(t, raw, approve, item.ID)
	as, err := phone.Get(ch.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	if code, res := rawDecide(t, raw, ch.ChallengeID, approve, as); code != "" || res.State != record.StateApplied {
		t.Fatalf("the right assertion: %q %+v", code, res)
	}
	w.onlyDecided(ch.ChallengeID)
}

// A card whose binding changed between the Card page and the decision is not
// decided. The stand-in door forgets its cards, as a restart of the planner's
// stand-in would, and the card's id comes back as another card: a challenge
// drawn before the change is refused binding-mismatch at the decision (and
// journaled as a refusal), a tap is refused binding-mismatch by the daemon,
// and once the seat has shown the new card the seat link refuses a tap from a
// session that saw the old one without asking the daemon. Read again, the
// new card is decided.
func TestSeatDecideE2EStaleBinding(t *testing.T) {
	w, s, phone, _, card, sess := decideSetup(t, execFeature)
	old := w.pendingOf(card)
	s.show(sess, card)
	tap := s.tapOK(sess, card, "approve", []int{0}, "")

	// The change: a fresh stand-in door, and a request that becomes a card
	// of the same id with another binding.
	cfg := doortest.Sample(w.rig.clock.Now)
	cfg.Command = "qargi"
	w.door.swap(doortest.New(cfg))
	again := w.ask("y", w.feature("y", "y.txt", "y\n", "Makefile", "y:\n\ttrue\n"))
	now := w.pendingOf(card)
	if again != card || now.BindingSHA256 == old.BindingSHA256 {
		t.Fatalf("the new card is %s with the binding %s, want %s with another binding than %s", again, now.BindingSHA256, card, old.BindingSHA256)
	}

	// The challenge drawn before the change.
	res := s.send(sess, tap.str(t, "challenge_id"), s.sign(phone, tap))
	wantRefusedReply(t, res, http.StatusConflict, "card_changed", "the decision drawn before the change")
	w.undecided(card, "the decision drawn before the change")

	// A tap after it: the seat link still has the old card, the daemon
	// refuses.
	asked := w.calls.count(ownerwire.OpSeatChallenge)
	stale := s.tap(sess, card, "approve", []int{0}, "")
	wantRefusedReply(t, stale, http.StatusConflict, "card_changed", "a tap on the old card")
	if n := w.calls.count(ownerwire.OpSeatChallenge); n != asked+1 {
		t.Errorf("seat.challenge was served %d times, want %d", n, asked+1)
	}

	// Another session shows the new card; the seat link now refuses the old
	// one's tap itself.
	sess2 := s.logIn(phone)
	s.show(sess2, card)
	asked = w.calls.count(ownerwire.OpSeatChallenge)
	stale = s.tap(sess, card, "approve", []int{0}, "")
	wantRefusedReply(t, stale, http.StatusConflict, "card_changed", "a tap on the old card after the new one was shown")
	if n := w.calls.count(ownerwire.OpSeatChallenge); n != asked {
		t.Errorf("a tap on the old card was sent: seat.challenge served %d times, want %d", n, asked)
	}
	w.undecided(card, "the taps on the old card")

	// Read again, the new card is decided.
	t2, _, done := s.decide(sess, phone, card, "approve", []int{0}, "")
	wantDone(t, done, record.StateApplied)
	if d, _ := w.doorDecision(card); d == nil || d.BindingSHA256 != now.BindingSHA256 {
		t.Errorf("the door holds %+v, want the new card's approve", d)
	}
	k := w.onlyDecided(t2.str(t, "challenge_id"))
	if k[ownerjournal.KindRefused] != 1 {
		t.Errorf("the journal holds %v, want the refusal of the decision drawn before the change", k)
	}
}

// A key revoked at the desk decides nothing once the revoke returned: a
// revoke between the challenge and the confirm, and one that lands while the
// decision is in the daemon, after the assertion verified and before the
// append. The phone is told the passkey is no longer paired and its session
// ends. A decision appended before the revoke stands.
func TestSeatDecideE2ERevoked(t *testing.T) {
	revoke := func(w *decideWorld, item ownerwire.KeyItem) {
		if _, err := w.svc.KeyRevoke(context.Background(), pairDesk, ownerwire.KeyRevokeParams{ID: item.ID}); err != nil {
			w.t.Errorf("KeyRevoke: %v", err)
		}
	}
	refused := func(t *testing.T, w *decideWorld, s *decSeat, sess *http.Cookie, card string, res reply) {
		t.Helper()
		wantRefusedReply(t, res, http.StatusForbidden, "key_revoked", "a revoked key")
		if c := res.cookie(seatapp.SessionCookie); c == nil || c.MaxAge >= 0 {
			t.Errorf("the session was not ended")
		}
		if page := s.get("/cards/", sess); page.status != http.StatusSeeOther {
			t.Errorf("the ended session still reads the cards: %d", page.status)
		}
		w.undecided(card, "a revoked key")
		if keys := w.keys(); len(keys) != 1 || !keys[0].Revoked {
			t.Errorf("the list holds %+v", keys)
		}
		w.onlyDecided()
	}

	t.Run("before the confirm", func(t *testing.T) {
		w, s, phone, item, card, sess := decideSetup(t, nil)
		s.show(sess, card)
		tap := s.tapOK(sess, card, "approve", nil, "")
		revoke(w, item)
		refused(t, w, s, sess, card, s.send(sess, tap.str(t, "challenge_id"), s.sign(phone, tap)))
	})

	t.Run("after the assertion verified", func(t *testing.T) {
		w, s, phone, item, card, sess := decideSetup(t, nil)
		var fired atomic.Bool
		w.onFault(func(point string) error {
			if point == "seat-verified" && fired.CompareAndSwap(false, true) {
				revoke(w, item)
			}
			return nil
		})
		s.show(sess, card)
		tap := s.tapOK(sess, card, "approve", nil, "")
		res := s.send(sess, tap.str(t, "challenge_id"), s.sign(phone, tap))
		if !fired.Load() {
			t.Fatal("the decision never reached the point after the verification")
		}
		refused(t, w, s, sess, card, res)
	})

	t.Run("after the append", func(t *testing.T) {
		w, s, phone, item, card, sess := decideSetup(t, nil)
		var fired atomic.Bool
		w.onFault(func(point string) error {
			if point == "decision-journaled" && fired.CompareAndSwap(false, true) {
				revoke(w, item)
			}
			return nil
		})
		tap, _, res := s.decide(sess, phone, card, "approve", nil, "")
		if !fired.Load() {
			t.Fatal("the decision was never journaled")
		}
		wantDone(t, res, record.StateApplied)
		if d, st := w.doorDecision(card); d == nil || st != record.StateApplied {
			t.Errorf("the door holds %+v in the state %s, want the approve applied", d, st)
		}
		if keys := w.keys(); len(keys) != 1 || !keys[0].Revoked {
			t.Errorf("the list holds %+v", keys)
		}
		w.onlyDecided(tap.str(t, "challenge_id"))
	})
}

// A challenge belongs to the gate session of the seat that asked for it. With
// two seat keys pinned, as during a rotation, the other seat cannot use it,
// and its try does not burn it: the seat that asked then decides with it.
func TestSeatDecideE2EOtherGateSession(t *testing.T) {
	w, s, phone, _, card, sess := decideSetup(t, nil)
	other := w.newSeat(w.otherID)
	s.show(sess, card)
	tap := s.tapOK(sess, card, "approve", nil, "")
	as := s.sign(phone, tap)
	if _, err := other.linkDecide(tap.str(t, "challenge_id"), appFrame(w.pendingOf(card), true, ""), as); !errors.Is(err, seatapp.ErrChallengeStale) {
		t.Errorf("the other seat's decide: %v, want %v", err, seatapp.ErrChallengeStale)
	}
	w.undecided(card, "the other gate session")
	res := s.send(sess, tap.str(t, "challenge_id"), as)
	wantDone(t, res, record.StateApplied)
	if d, _ := w.doorDecision(card); d == nil || d.Seat.Session != w.seatID.Fingerprint()[:16] {
		t.Errorf("the door's decision is %+v, want the asking seat's session", d)
	}
	w.onlyDecided(tap.str(t, "challenge_id"))
}

// An assertion without user verification, and one whose client data names
// another origin, are refused; nothing is decided, the session stays, and a
// good assertion on a new tap then decides.
func TestSeatDecideE2EAssertionRefused(t *testing.T) {
	cases := map[string]func(a *webauthntest.Authenticator){
		"user verification off": func(a *webauthntest.Authenticator) { a.Flags &^= webauthntest.FlagUV },
		"wrong origin":          func(a *webauthntest.Authenticator) { a.Origin = "https://seat.example.invalid" },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			w, s, phone, _, card, sess := decideSetup(t, nil)
			flags, origin := phone.Flags, phone.Origin
			spoil(phone)
			_, _, res := s.decide(sess, phone, card, "approve", nil, "")
			wantRefusedReply(t, res, http.StatusForbidden, "assertion_refused", name)
			w.undecided(card, name)
			phone.Flags, phone.Origin = flags, origin
			tap, _, done := s.decide(sess, phone, card, "approve", nil, "")
			wantDone(t, done, record.StateApplied)
			w.onlyDecided(tap.str(t, "challenge_id"))
		})
	}
}

// A challenge lives five minutes. After them the seat says to tap again and
// sends nothing; the daemon refuses the expired challenge when a seat sends
// it anyway; a new tap decides.
func TestSeatDecideE2EExpiredChallenge(t *testing.T) {
	w, s, phone, _, card, sess := decideSetup(t, nil)
	s.show(sess, card)
	tap := s.tapOK(sess, card, "approve", nil, "")
	if n, err := strconv.Atoi(string(tap.fields(t)["expires_in"])); err != nil || n != int(ownerchannel.ChallengeLife/time.Second) {
		t.Errorf("the challenge expires in %s seconds (%v)", tap.fields(t)["expires_in"], err)
	}
	w.rig.clock.Advance(ownerchannel.ChallengeLife + time.Second)
	as := s.sign(phone, tap)
	res := s.send(sess, tap.str(t, "challenge_id"), as)
	wantRefusedReply(t, res, http.StatusConflict, "challenge_expired", "an expired challenge")
	if res.str(t, "say") != sayTooLongWords {
		t.Errorf("the phone says %q", res.str(t, "say"))
	}
	if n := w.calls.count(ownerwire.OpSeatDecide); n != 0 {
		t.Errorf("the seat sent an expired decision: seat.decide served %d times", n)
	}
	if _, err := s.linkDecide(tap.str(t, "challenge_id"), appFrame(w.pendingOf(card), true, ""), as); !errors.Is(err, seatapp.ErrChallengeStale) {
		t.Errorf("the expired challenge sent to the daemon: %v, want %v", err, seatapp.ErrChallengeStale)
	}
	w.undecided(card, "an expired challenge")
	t2, _, done := s.decide(sess, phone, card, "approve", nil, "")
	wantDone(t, done, record.StateApplied)
	w.onlyDecided(t2.str(t, "challenge_id"))
}

// The gate's connection is cut after the decision was sent and taken, before
// its answer comes: the phone says the outcome is unknown, the seat never
// sends the decision again, by itself or on a tap, and the card read again,
// once the seat is back, shows what the daemon did.
func TestSeatDecideE2ELostAnswer(t *testing.T) {
	w, s, phone, _, card, sess := decideSetup(t, nil)
	s.show(sess, card)
	tap := s.tapOK(sess, card, "approve", nil, "")
	as := s.sign(phone, tap)
	before := s.since()
	w.lose.Store(true)
	res := s.send(sess, tap.str(t, "challenge_id"), as)
	if res.status != http.StatusBadGateway || res.code(t) != "outcome_unknown" || res.str(t, "say") != sayUnknownWords {
		t.Fatalf("the lost answer: %d %q, want 502 outcome_unknown", res.status, res.body)
	}
	if w.lose.Load() {
		t.Fatal("the connection was not cut")
	}
	s.waitReconnected(before, true)

	// Nothing is sent again: neither a tap nor the confirm reaches the gate.
	asked, sent := w.calls.count(ownerwire.OpSeatChallenge), w.calls.count(ownerwire.OpSeatDecide)
	retap := s.tap(sess, card, "approve", nil, "")
	wantRefusedReply(t, retap, http.StatusConflict, "sent_unknown", "a tap after the lost answer")
	resend := s.send(sess, tap.str(t, "challenge_id"), as)
	wantRefusedReply(t, resend, http.StatusConflict, "challenge_expired", "the confirm sent again after the lost answer")
	if w.calls.count(ownerwire.OpSeatChallenge) != asked || w.calls.count(ownerwire.OpSeatDecide) != sent || sent != 1 {
		t.Errorf("the seat sent something again: %q", w.calls.seen())
	}

	// The card read again shows the true state: the decision was taken.
	if d, st := w.doorDecision(card); d == nil || st != record.StateApplied {
		t.Fatalf("the door holds %+v in the state %s", d, st)
	}
	page := s.get("/cards/"+card, sess)
	if page.status != http.StatusOK || !strings.Contains(string(page.body), string(record.StateApplied)) || strings.Contains(string(page.body), decideFormMark) {
		t.Errorf("the card read again: %d, applied shown %v, form shown %v", page.status,
			strings.Contains(string(page.body), string(record.StateApplied)), strings.Contains(string(page.body), decideFormMark))
	}
	after := s.tap(sess, card, "approve", nil, "")
	wantRefusedReply(t, after, http.StatusConflict, "cannot_decide", "a tap on the decided card")
	if w.calls.count(ownerwire.OpSeatChallenge) != asked {
		t.Errorf("a tap on the decided card was sent: %q", w.calls.seen())
	}
	w.onlyDecided(tap.str(t, "challenge_id"))
}

// A daemon without a seat origin serves pairing and reads only. The seat,
// put in front of such a gate of the same daemon, still reads the card, but
// a tap says the desk takes no decisions from the phone and asks the daemon
// nothing; the gate refuses the decision operations unknown-op, and no login
// is offered.
func TestSeatDecideE2EWithoutDecideCapability(t *testing.T) {
	w, s, phone, item, card, sess := decideSetup(t, nil)
	s.show(sess, card)

	addr, calls := w.startGate(false)
	before := s.since()
	w.relay.retarget(addr)
	w.relay.cut()
	s.waitReconnected(before, false)

	page := s.get("/cards/"+card, sess)
	if page.status != http.StatusOK {
		t.Fatalf("the card on a gate without decisions: %d", page.status)
	}
	s.rig.clock.Advance(time.Second)
	res := s.tap(sess, card, "approve", nil, "")
	wantRefusedReply(t, res, http.StatusServiceUnavailable, "decide_unavailable", "a tap on a gate without decisions")
	if res.str(t, "say") != sayNotOffered {
		t.Errorf("the phone says %q", res.str(t, "say"))
	}
	if _, err := s.link.DecideChallenge(pairCtx(t), seatapp.DecideChallengeRequest{ID: card, BindingSHA256: w.pendingOf(card).BindingSHA256,
		Approve: true, Credential: item.ID, Now: w.rig.clock.Now()}); !errors.Is(err, seatapp.ErrNotOffered) {
		t.Errorf("the link's challenge on a gate without decisions: %v, want %v", err, seatapp.ErrNotOffered)
	}
	if n := calls.count(ownerwire.OpSeatChallenge) + calls.count(ownerwire.OpSeatDecide); n != 0 {
		t.Errorf("the seat asked a gate without decisions: %q", calls.seen())
	}
	if ch, _ := s.login(phone); ch.status == http.StatusOK || ch.cookie(seatapp.LoginCookie) != nil {
		t.Errorf("a gate without decisions offers a login: %d %q", ch.status, ch.body)
	}

	// A seat that sends the operations anyway is refused by the gate.
	raw := w.rawSeat(w.seatID)
	err := raw.Call(pairCtx(t), ownerwire.OpSeatChallenge, json.RawMessage(`{}`), nil)
	var re *seatgate.RemoteError
	if !errors.As(err, &re) || re.Code != gatehost.CodeUnknownOp {
		t.Errorf("seat.challenge on a gate without decisions: %v, want %s", err, gatehost.CodeUnknownOp)
	}
	err = raw.Call(pairCtx(t), ownerwire.OpSeatDecide, json.RawMessage(`{}`), nil)
	if !errors.As(err, &re) || re.Code != gatehost.CodeUnknownOp {
		t.Errorf("seat.decide on a gate without decisions: %v, want %s", err, gatehost.CodeUnknownOp)
	}
	w.undecided(card, "a gate without decisions")
	w.onlyDecided()
}

// swapDoor is the planner's door behind a switch: the stand-in until a test
// puts another in its place, as a restart of the stand-in would.
type swapDoor struct {
	mu sync.Mutex
	d  door.Door
}

func (s *swapDoor) cur() door.Door {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d
}

func (s *swapDoor) swap(d door.Door) {
	s.mu.Lock()
	s.d = d
	s.mu.Unlock()
}

func (s *swapDoor) Admit(ctx context.Context, r door.Record) (door.Admission, error) {
	return s.cur().Admit(ctx, r)
}
func (s *swapDoor) Plan(ctx context.Context, in door.PlanInput) (door.Record, error) {
	return s.cur().Plan(ctx, in)
}
func (s *swapDoor) Decide(ctx context.Context, r door.Record) (door.Decided, error) {
	return s.cur().Decide(ctx, r)
}
func (s *swapDoor) Use(ctx context.Context, in door.UseInput) (door.Used, error) {
	return s.cur().Use(ctx, in)
}
func (s *swapDoor) Settle(ctx context.Context, in door.SettleInput) (door.Settled, error) {
	return s.cur().Settle(ctx, in)
}
func (s *swapDoor) List(ctx context.Context) ([]door.Entry, error) { return s.cur().List(ctx) }
func (s *swapDoor) Show(ctx context.Context, id string) (door.Shown, error) {
	return s.cur().Show(ctx, id)
}

// decRelay passes the bytes of each TCP connection it takes on 127.0.0.1 to
// the gate's address and back, and nothing else: the TLS of the seat and the
// gate runs through it. cut closes every connection it carries; a seat then
// connects again through it.
type decRelay struct {
	ln     net.Listener
	mu     sync.Mutex
	target string
	conns  map[net.Conn]bool
	wg     sync.WaitGroup
}

func newDecRelay(t *testing.T, target string) *decRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &decRelay{ln: ln, target: target, conns: map[net.Conn]bool{}}
	r.wg.Add(1)
	go r.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		r.cut()
		r.wg.Wait()
	})
	return r
}

func (r *decRelay) addr() string { return r.ln.Addr().String() }

func (r *decRelay) retarget(addr string) {
	r.mu.Lock()
	r.target = addr
	r.mu.Unlock()
}

func (r *decRelay) serve() {
	defer r.wg.Done()
	for {
		in, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.mu.Lock()
		target := r.target
		r.mu.Unlock()
		out, err := net.DialTimeout("tcp", target, 5*time.Second)
		if err != nil {
			_ = in.Close()
			continue
		}
		r.mu.Lock()
		r.conns[in], r.conns[out] = true, true
		r.mu.Unlock()
		r.wg.Add(2)
		go r.pipe(in, out)
		go r.pipe(out, in)
	}
}

func (r *decRelay) pipe(dst, src net.Conn) {
	defer r.wg.Done()
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

// cut closes every connection the relay carries.
func (r *decRelay) cut() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for c := range r.conns {
		_ = c.Close()
	}
	r.conns = map[net.Conn]bool{}
}
