//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/credkind"
	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/gatehost"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/seatpair"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
	"ataniq/internal/seatlink"
)

// These tests run the pairing of a phone through the whole chain on the
// loopback address, with nothing faked but the phone's authenticator and the
// planner's door:
//
//	webauthntest authenticator (the phone)
//	  -> HTTP -> seatapp (the seat app, behind httptest)
//	  -> seatlink.Daemon -> seatgate.Client
//	  -> TLS 1.3 with a pinned key at each end, on 127.0.0.1
//	  -> seatgate.Serve -> gatehost.Handler
//	  -> the real ownerchannel.Service, on the real journal, on the stand-in
//	     door and on a key file in a private folder
//
// The desk's side is the service's own calls (PairStart, PairStatus,
// PairConfirm, PairReject) with the console's source. Nothing here skips the
// TLS pins: a test that cannot connect fails.

const (
	// pairRPID and pairOrigin are the seat's relying party id and origin as
	// the seat app, the gate and the phone all hold them: an https origin
	// with no port, the form the gate's values take. The app listens on plain
	// HTTP under httptest, and a request names this host as a proxy's does.
	pairRPID   = "127.0.0.1"
	pairOrigin = "https://127.0.0.1"
	// pairPresenceWindow is the owner's presence window of these tests, short
	// enough that a pending credential outlives it.
	pairPresenceWindow = 2 * time.Minute
	// pairCallTime bounds one call, so that a regression fails in seconds.
	pairCallTime = 15 * time.Second
	// bitwardenAAGUID is an AAGUID of the credkind table that claims the kind
	// "vault-passkey".
	bitwardenAAGUID = "d548826e-79b4-db40-a3d8-11116f7e8349"
)

// pairDesk is the console of the owner, the one that may decide.
var pairDesk = ownerchannel.Source{Surface: "console", Session: "0123456789abcdef", Decider: true, PID: 41822, TTY: "/dev/pts/4", Client: "console-a 0.0.1"}

// pairCtx is a context that ends after pairCallTime.
func pairCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pairCallTime)
	t.Cleanup(cancel)
	return ctx
}

// seenHandler is the gate's handler of the test: it hands every call to the
// real one and notes the operation, so that a test shows that what it did
// went over the gate and that a refused connection reached nothing.
type seenHandler struct {
	h   seatgate.Handler
	mu  sync.Mutex
	ops []string
}

func (s *seenHandler) Handle(ctx context.Context, peer seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
	s.mu.Lock()
	s.ops = append(s.ops, op)
	s.mu.Unlock()
	return s.h.Handle(ctx, peer, op, params)
}

func (s *seenHandler) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ops)
}

func (s *seenHandler) count(op string) int {
	n := 0
	for _, o := range s.seen() {
		if o == op {
			n++
		}
	}
	return n
}

// pairWorld is one test's world: the owner channel with its service, the gate
// listening on the loopback address with fresh identities, and, once
// startSeat has run, the seat's client, link and app.
type pairWorld struct {
	t    *testing.T
	rig  *rig
	svc  *ownerchannel.Service
	gate ownerchannel.Gate

	gateID, seatID seatgate.Identity
	addr           string
	calls          *seenHandler

	link *seatlink.Daemon
	web  *httptest.Server
}

func newPairIdentity(t *testing.T, name string) seatgate.Identity {
	t.Helper()
	certPEM, keyPEM, err := seatgate.GenerateIdentity(rand.Reader, time.Now(), name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := seatgate.ParseIdentity(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// newPairWorld opens the owner channel on a state folder with the stand-in
// door and a key file of a temporary folder, and starts the gate on
// 127.0.0.1 behind it. It starts no seat.
func newPairWorld(t *testing.T) *pairWorld {
	t.Helper()
	// The owner channel, as newRig makes it, but with the key file and the
	// presence window of these tests, which newRig has no place for.
	l := newLab(t)
	r := &rig{t: t, lab: l, scene: newScene(l, nil), clock: doortest.NewClock(start), suite: newSuite(), proj: testProject(l)}
	dcfg := doortest.Sample(r.clock.Now)
	dcfg.Command = "qargi"
	r.stand = doortest.New(dcfg)
	r.state = privateDir(t)
	r.drop = privateDir(t)

	keys, err := ownerjournal.OpenKeys(filepath.Join(privateDir(t), "keys.log"))
	if err != nil {
		t.Fatalf("OpenKeys: %v", err)
	}
	cfg := r.config()
	cfg.Service.Keys = keys
	cfg.Service.PresenceWindow = pairPresenceWindow
	o, err := Open(context.Background(), cfg)
	if err != nil {
		_ = keys.Close()
		t.Fatalf("Open: %v", err)
	}
	r.o = o
	t.Cleanup(func() { _ = o.Close() })

	w := &pairWorld{t: t, rig: r, svc: o.Service,
		gateID: newPairIdentity(t, "gate"), seatID: newPairIdentity(t, "seat")}
	w.gate = ownerchannel.Gate{Instance: w.gateID.Fingerprint(), Origin: pairOrigin, RPID: pairRPID}

	pinned := func(fp string) bool { return fp == w.seatID.Fingerprint() }
	h, err := gatehost.NewDeciding(o.Service, o.Service, w.gate, pinned, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.calls = &seenHandler{h: h}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	w.addr = ln.Addr().String()
	if host, _, err := net.SplitHostPort(w.addr); err != nil || host != "127.0.0.1" {
		t.Fatalf("the gate listens on %q, not on the loopback address", w.addr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- seatgate.Serve(ctx, ln, seatgate.ServerConfig{
			TLS: seatgate.ServerTLS(w.gateID, []string{w.seatID.Fingerprint()}), Caps: h.Caps(),
		}, w.calls)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not end")
		}
	})
	return w
}

// dial connects a client to the gate with the identity given and the gate key
// fingerprint it pins, over real TLS.
func (w *pairWorld) dial(id seatgate.Identity, pin string) (*seatgate.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pairCallTime)
	defer cancel()
	return seatgate.Dial(ctx, w.addr, seatgate.ClientConfig{
		TLS: seatgate.ClientTLS(id, pin, "gate"), Caps: seatlink.Caps(),
		DialTimeout: 5 * time.Second, HelloTimeout: 5 * time.Second,
	})
}

// startSeat connects the seat's client with its own identity and the gate's
// real pin, and puts the link and the app over it, listening under httptest.
func (w *pairWorld) startSeat() {
	w.t.Helper()
	c, err := w.dial(w.seatID, w.gateID.Fingerprint())
	if err != nil {
		w.t.Fatalf("the seat cannot reach the gate with the right pins: %v", err)
	}
	w.t.Cleanup(func() { _ = c.Close() })
	for _, want := range []string{seatlink.CapPair, seatlink.CapRead, seatlink.CapLogin} {
		if !slices.Contains(c.Caps(), want) {
			w.t.Fatalf("the gate's hello lists %q, not %q", c.Caps(), want)
		}
	}
	w.link, err = seatlink.New(c, seatlink.WithGatePin(w.gateID.Fingerprint()))
	if err != nil {
		w.t.Fatal(err)
	}
	app, err := seatapp.New(seatapp.Config{
		Origin: pairOrigin, RPID: pairRPID, Daemon: w.link,
		// The app reads the time the service reads: the daemon dates the
		// login challenge by its clock, and the app drops one that the
		// time it holds says is over.
		Now:                           w.rig.clock.Now,
		AllowInsecureLoopbackForTests: true, AllowNoFetchMetadataForTests: true,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.web = httptest.NewServer(app)
	w.t.Cleanup(w.web.Close)
}

// reply is an answer of the app.
type reply struct {
	status  int
	body    []byte
	cookies []*http.Cookie
}

// cookie returns the cookie of a name the reply sets, or nil.
func (r reply) cookie(name string) *http.Cookie {
	for _, c := range r.cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// fields decodes the reply's body, a JSON object of strings and lists.
func (r reply) fields(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("the body is not a JSON object: %v: %q", err, r.body)
	}
	return m
}

// str decodes the string member of a reply.
func (r reply) str(t *testing.T, key string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(r.fields(t)[key], &s); err != nil {
		t.Fatalf("member %q of %q is not a string: %v", key, r.body, err)
	}
	return s
}

// code is the code of an error reply of the app.
func (r reply) code(t *testing.T) string { return r.str(t, "error") }

// post sends a POST the way the proxy in front of the app does: the app's
// host name as the Host, its origin as the Origin, JSON, and the cookies
// given.
func (w *pairWorld) post(path string, body any, cookies ...*http.Cookie) reply {
	w.t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		w.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, w.web.URL+path, bytes.NewReader(b))
	if err != nil {
		w.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", pairOrigin)
	return w.send(req, cookies)
}

// get sends a GET with the cookies given.
func (w *pairWorld) get(path string, cookies ...*http.Cookie) reply {
	w.t.Helper()
	req, err := http.NewRequest(http.MethodGet, w.web.URL+path, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.send(req, cookies)
}

func (w *pairWorld) send(req *http.Request, cookies []*http.Cookie) reply {
	w.t.Helper()
	req.Host = pairRPID
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	client := &http.Client{
		Timeout:       pairCallTime,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		w.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		w.t.Fatal(err)
	}
	return reply{status: resp.StatusCode, body: b, cookies: resp.Cookies()}
}

var b64u = base64.RawURLEncoding

// newPhone makes the passkey of a phone: a vault passkey that is synced (the
// BE and BS flags), for the seat's relying party and origin.
func newPhone(t *testing.T) *webauthntest.Authenticator {
	t.Helper()
	a, err := webauthntest.New(pairRPID, pairOrigin)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(bitwardenAAGUID, "-", ""))
	if err != nil || len(raw) != len(a.AAGUID) {
		t.Fatalf("bad AAGUID %q", bitwardenAAGUID)
	}
	copy(a.AAGUID[:], raw)
	a.Flags = webauthntest.FlagUP | webauthntest.FlagUV | webauthntest.FlagBE | webauthntest.FlagBS
	return a
}

// startPairing is the desk starting a pairing, with the owner present: it
// returns the one-time code.
func (w *pairWorld) startPairing() string {
	w.t.Helper()
	if err := w.svc.Beat(pairDesk); err != nil {
		w.t.Fatalf("Beat: %v", err)
	}
	res, err := w.svc.PairStart(pairCtx(w.t), pairDesk, ownerwire.PairStartParams{})
	if err != nil {
		w.t.Fatalf("PairStart: %v", err)
	}
	return res.Code
}

// register is the phone's side of a pairing through the app: it asks for the
// challenge, makes the passkey over it and posts the registration with the
// code. It returns the app's answer to the last post.
func (w *pairWorld) register(a *webauthntest.Authenticator, code string) reply {
	w.t.Helper()
	ch := w.post("/api/pair-challenge", struct{}{})
	if ch.status != http.StatusOK {
		w.t.Fatalf("POST /api/pair-challenge: %d %q", ch.status, ch.body)
	}
	challenge := ch.str(w.t, "challenge")
	raw, err := b64u.DecodeString(challenge)
	if err != nil {
		w.t.Fatal(err)
	}
	reg := a.Create(raw)
	return w.post("/api/pair", map[string]string{
		"code":               code,
		"challenge":          challenge,
		"credential_id":      b64u.EncodeToString(reg.CredentialID),
		"client_data_json":   b64u.EncodeToString(reg.ClientDataJSON),
		"authenticator_data": b64u.EncodeToString(reg.AuthenticatorData),
		"public_key":         b64u.EncodeToString(reg.PublicKey),
	})
}

// pairPending makes a credential pending at the desk: the whole phone side of
// a pairing, up to the desk's word. It checks that the words the phone shows
// are the ones the desk's status shows, and that they are the fingerprint of
// the credential the phone made, and returns the status.
func (w *pairWorld) pairPending(a *webauthntest.Authenticator) ownerwire.PairStatus {
	w.t.Helper()
	code := w.startPairing()
	if st, err := w.svc.PairStatus(pairCtx(w.t)); err != nil || st.Phase != ownerwire.PairWaiting {
		w.t.Fatalf("after the start, the pairing is %+v (%v), want waiting", st, err)
	}
	res := w.register(a, code)
	if res.status != http.StatusOK {
		w.t.Fatalf("POST /api/pair: %d %q", res.status, res.body)
	}
	var phone []string
	if err := json.Unmarshal(res.fields(w.t)["words"], &phone); err != nil {
		w.t.Fatalf("the words of %q: %v", res.body, err)
	}
	st, err := w.svc.PairStatus(pairCtx(w.t))
	if err != nil {
		w.t.Fatalf("PairStatus: %v", err)
	}
	if st.Phase != ownerwire.PairPending || st.PendingID == "" {
		w.t.Fatalf("after the phone paired, the pairing is %+v, want pending", st)
	}
	if len(phone) != ownerwire.PairWords || !slices.Equal(phone, st.Words) {
		w.t.Fatalf("the phone shows %q, the desk %q", phone, st.Words)
	}
	// The words are the fingerprint of the credential, computed here
	// independently of the daemon.
	want := seatpair.FingerprintOf(a.CredentialID, a.SPKI())
	if !slices.Equal(st.Words, want[:]) {
		w.t.Fatalf("the desk shows %q, the credential's fingerprint is %q", st.Words, want)
	}
	return st
}

// keys is the list of deciding keys, from the service.
func (w *pairWorld) keys() []ownerwire.KeyItem {
	w.t.Helper()
	v, err := w.svc.Keys(pairCtx(w.t))
	if err != nil {
		w.t.Fatalf("Keys: %v", err)
	}
	return v.Keys
}

// login is the phone logging in through the app with the passkey: the
// challenge, the assertion over it and the login. It returns the answers.
func (w *pairWorld) login(a *webauthntest.Authenticator) (challenge, result reply) {
	w.t.Helper()
	challenge = w.post("/api/login-challenge", struct{}{})
	if challenge.status != http.StatusOK {
		return challenge, reply{}
	}
	raw, err := b64u.DecodeString(challenge.str(w.t, "challenge"))
	if err != nil {
		w.t.Fatal(err)
	}
	as, err := a.Get(raw)
	if err != nil {
		w.t.Fatal(err)
	}
	cookie := challenge.cookie(seatapp.LoginCookie)
	if cookie == nil {
		w.t.Fatal("the login challenge set no login cookie")
	}
	return challenge, w.post("/api/login", map[string]string{
		"credential_id":      b64u.EncodeToString(as.CredentialID),
		"client_data_json":   b64u.EncodeToString(as.ClientDataJSON),
		"authenticator_data": b64u.EncodeToString(as.AuthenticatorData),
		"signature":          b64u.EncodeToString(as.Signature),
	}, cookie)
}

// loggedIn says whether a login answer made a session that the app takes for
// one: the page of the cards, which reads the desk over the gate, opens with
// it, and does not without it.
func (w *pairWorld) loggedIn(res reply) bool {
	w.t.Helper()
	c := res.cookie(seatapp.SessionCookie)
	if res.status != http.StatusOK || c == nil || c.MaxAge < 0 {
		return false
	}
	if page := w.get("/cards/"); page.status != http.StatusSeeOther {
		w.t.Fatalf("the cards open without a session: %d", page.status)
	}
	return w.get("/cards/", c).status == http.StatusOK
}

// linkLogin is a login by the link, with no app between: the daemon is asked
// for a challenge, which the link checks against the gate's key, the
// authenticator signs it and the daemon is handed the assertion. It returns
// the daemon's answer to the login.
func (w *pairWorld) linkLogin(a *webauthntest.Authenticator) (seatapp.LoginResult, error) {
	w.t.Helper()
	ctx := pairCtx(w.t)
	ch, err := w.link.LoginChallenge(ctx)
	if err != nil {
		w.t.Fatalf("LoginChallenge: %v", err)
	}
	as, err := a.Get(ch.Challenge)
	if err != nil {
		w.t.Fatal(err)
	}
	return w.link.Login(ctx, seatapp.LoginRequest{
		ChallengeID: ch.ID, CredentialID: as.CredentialID,
		AuthenticatorData: as.AuthenticatorData, ClientDataJSON: as.ClientDataJSON, Signature: as.Signature,
	})
}

// refuseLogin checks that a credential that is not on the list cannot log in
// by any road: through the app, which asks the gate for the challenge and
// gets an allow list that is empty, and by the link, which hands the daemon the credential's own
// assertion over a challenge it drew.
func (w *pairWorld) refuseLogin(a *webauthntest.Authenticator, why string) {
	w.t.Helper()
	ch, res := w.login(a)
	// The daemon's allow list is empty, so the app says the phone is not
	// paired and goes no further: no login cookie, no session.
	if ch.status != http.StatusConflict || ch.code(w.t) != "not_paired" || res.status != 0 || ch.cookie(seatapp.SessionCookie) != nil {
		w.t.Errorf("%s: the app answers the login challenge with %d %q, want 409 not_paired and no session", why, ch.status, ch.body)
	}
	if _, err := w.linkLogin(a); !errors.Is(err, seatapp.ErrLoginRefused) {
		w.t.Errorf("%s: the daemon's answer to the login is %v, want %v", why, err, seatapp.ErrLoginRefused)
	}
}

// The pairing, whole: the desk starts it, the phone makes a passkey and pairs
// over the app and the gate, the eight words the phone shows are the ones the
// desk shows, the desk's word puts the key on the list with the kind it
// claimed, and then the phone logs in with it, through the gate, and a
// passkey that was never confirmed at the desk does not.
func TestSeatPairE2E(t *testing.T) {
	w := newPairWorld(t)
	w.startSeat()
	phone, stranger := newPhone(t), newPhone(t)

	if keys := w.keys(); len(keys) != 0 {
		t.Fatalf("the list holds %+v before any pairing", keys)
	}
	st := w.pairPending(phone)
	if st.Kind != credkind.NameVault || st.AAGUID != bitwardenAAGUID || !st.BackupEligible || !st.BackupState {
		t.Errorf("the desk shows the claims %q %q %v %v, want the phone's own", st.Kind, st.AAGUID, st.BackupEligible, st.BackupState)
	}
	// Pending is not paired: nothing is on the list and nothing logs in.
	if keys := w.keys(); len(keys) != 0 {
		t.Fatalf("a pending credential is on the list: %+v", keys)
	}
	w.refuseLogin(phone, "pending")

	// The desk's word. The owner is present: the beat that started the
	// pairing is inside the window.
	item, err := w.svc.PairConfirm(pairCtx(t), pairDesk, ownerwire.PairPendingParams{PendingID: st.PendingID})
	if err != nil {
		t.Fatalf("PairConfirm: %v", err)
	}
	if item.ID != ownerjournal.KeyDisplayID(phone.CredentialID) || item.Kind != credkind.NameVault ||
		item.AAGUID != bitwardenAAGUID || !item.BackupEligible || item.Revoked {
		t.Errorf("the key put on the list is %+v", item)
	}
	if keys := w.keys(); len(keys) != 1 || keys[0] != item {
		t.Fatalf("the list holds %+v, want the confirmed key %+v", keys, item)
	}
	if after, err := w.svc.PairStatus(pairCtx(t)); err != nil || after.Phase != ownerwire.PairNone {
		t.Errorf("after the confirm the pairing is %+v (%v), want none", after, err)
	}

	// The login of the confirmed credential, through the app and the gate.
	ch, res := w.login(phone)
	if ch.status != http.StatusOK {
		t.Fatalf("POST /api/login-challenge: %d %q", ch.status, ch.body)
	}
	var allow []string
	if err := json.Unmarshal(ch.fields(t)["allow"], &allow); err != nil || !slices.Equal(allow, []string{b64u.EncodeToString(phone.CredentialID)}) {
		t.Errorf("the allow list is %q (%v), want the confirmed credential alone", allow, err)
	}
	if !w.loggedIn(res) {
		t.Fatalf("the confirmed credential cannot log in: %d %q", res.status, res.body)
	}
	// The daemon names the credential that logged in, with the kind of the
	// list.
	got, err := w.linkLogin(phone)
	if err != nil {
		t.Fatalf("the link's login: %v", err)
	}
	if got.Credential != item.ID || got.Kind != credkind.NameVault || !bytes.Equal(got.CredentialID, phone.CredentialID) {
		t.Errorf("the daemon answers the login with %+v, want the confirmed key %+v", got, item)
	}

	// A passkey the desk never confirmed gets a challenge, signs it and is
	// refused: nothing is a session, and the phone is not logged in.
	_, bad := w.login(stranger)
	if bad.status != http.StatusForbidden || bad.code(t) != "login_refused" || bad.cookie(seatapp.SessionCookie) != nil {
		t.Errorf("a credential that was never paired: %d %q, want 403 login_refused and no session", bad.status, bad.body)
	}
	if _, err := w.linkLogin(stranger); !errors.Is(err, seatapp.ErrLoginRefused) {
		t.Errorf("the daemon's answer to a credential that was never paired is %v", err)
	}
	if keys := w.keys(); len(keys) != 1 {
		t.Errorf("the list holds %+v after the refusals, want the one key", keys)
	}

	// Everything above went over the gate.
	for _, op := range []string{ownerwire.OpSeatPair, ownerwire.OpSeatLoginChallenge, ownerwire.OpSeatLogin, ownerwire.OpList} {
		if w.calls.count(op) == 0 {
			t.Errorf("the gate never served %s; it served %q", op, w.calls.seen())
		}
	}
}

// A seat whose pin of the gate is wrong, and one whose own key the gate does
// not pin, cannot even say hello: the TLS handshake ends it, nothing reaches
// the handler, and the pairing's code is not spent. The seat that holds the
// right pins then pairs.
func TestSeatPairE2EWrongPin(t *testing.T) {
	w := newPairWorld(t)
	code := w.startPairing()

	other := newPairIdentity(t, "gate")
	if _, err := w.dial(w.seatID, other.Fingerprint()); err == nil {
		t.Error("a seat that pins another key than the gate's connected")
	}
	stranger := newPairIdentity(t, "seat")
	if _, err := w.dial(stranger, w.gateID.Fingerprint()); err == nil {
		t.Error("a seat whose key the gate does not pin connected")
	}
	if n := len(w.calls.seen()); n != 0 {
		t.Errorf("the handler served %q to a connection that failed its pins", w.calls.seen())
	}
	if st, err := w.svc.PairStatus(pairCtx(t)); err != nil || st.Phase != ownerwire.PairWaiting {
		t.Fatalf("the pairing is %+v (%v), want waiting: the code is not spent", st, err)
	}

	w.startSeat()
	phone := newPhone(t)
	if res := w.register(phone, code); res.status != http.StatusOK {
		t.Fatalf("the seat with the right pins cannot pair: %d %q", res.status, res.body)
	}
	if st, err := w.svc.PairStatus(pairCtx(t)); err != nil || st.Phase != ownerwire.PairPending {
		t.Errorf("the pairing is %+v (%v), want pending", st, err)
	}
	if w.calls.count(ownerwire.OpSeatPair) != 1 {
		t.Errorf("the gate served %q, want one seat.pair", w.calls.seen())
	}
}

// A pairing that the desk did not confirm leaves no key: a pending
// credential, a rejected one and one whose ten minutes ran out cannot log in,
// by the app or by the daemon, and the desk cannot confirm what is not
// pending.
func TestSeatPairE2EUnconfirmedCannotLogIn(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		w := newPairWorld(t)
		w.startSeat()
		phone := newPhone(t)
		w.pairPending(phone)
		w.refuseLogin(phone, "pending")
		if keys := w.keys(); len(keys) != 0 {
			t.Errorf("the list holds %+v", keys)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		w := newPairWorld(t)
		w.startSeat()
		phone := newPhone(t)
		st := w.pairPending(phone)
		if err := w.svc.PairReject(pairCtx(t), pairDesk, ownerwire.PairPendingParams{PendingID: st.PendingID}); err != nil {
			t.Fatalf("PairReject: %v", err)
		}
		if after, err := w.svc.PairStatus(pairCtx(t)); err != nil || after.Phase != ownerwire.PairNone {
			t.Errorf("after the reject the pairing is %+v (%v), want none", after, err)
		}
		if _, err := w.svc.PairConfirm(pairCtx(t), pairDesk, ownerwire.PairPendingParams{PendingID: st.PendingID}); !errors.Is(err, ownerchannel.ErrNoPairing) {
			t.Errorf("a confirm of the rejected credential: %v, want %v", err, ownerchannel.ErrNoPairing)
		}
		w.refuseLogin(phone, "rejected")
		if keys := w.keys(); len(keys) != 0 {
			t.Errorf("the list holds %+v", keys)
		}
	})

	t.Run("expired", func(t *testing.T) {
		w := newPairWorld(t)
		w.startSeat()
		phone := newPhone(t)
		st := w.pairPending(phone)
		// The pending credential waits ten minutes, and not a second more.
		w.rig.clock.Advance(seatpair.PendingLifetime + time.Second)
		if after, err := w.svc.PairStatus(pairCtx(t)); err != nil || after.Phase != ownerwire.PairExpired {
			t.Errorf("after its ten minutes the pairing is %+v (%v), want expired", after, err)
		}
		// The owner is at the desk again; only the pairing is over.
		if err := w.svc.Beat(pairDesk); err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.PairConfirm(pairCtx(t), pairDesk, ownerwire.PairPendingParams{PendingID: st.PendingID}); !errors.Is(err, ownerchannel.ErrPairingExpired) {
			t.Errorf("a confirm after the ten minutes: %v, want %v", err, ownerchannel.ErrPairingExpired)
		}
		w.refuseLogin(phone, "expired")
		if keys := w.keys(); len(keys) != 0 {
			t.Errorf("the list holds %+v", keys)
		}
	})
}

// The owner's word counts only while he is present: a confirm after the
// presence window ran out is refused owner-away, the credential stays
// pending and cannot log in, and once he is back the same word puts it on
// the list.
func TestSeatPairE2EConfirmAfterPresenceLapse(t *testing.T) {
	w := newPairWorld(t)
	w.startSeat()
	phone := newPhone(t)
	st := w.pairPending(phone)

	// The window is two minutes, the pending credential lives ten.
	w.rig.clock.Advance(pairPresenceWindow + time.Minute)
	_, err := w.svc.PairConfirm(pairCtx(t), pairDesk, ownerwire.PairPendingParams{PendingID: st.PendingID})
	if c, ok := record.CodeOf(err); !ok || c != record.CodeOwnerAway {
		t.Fatalf("a confirm after the presence window: %v, want the refusal %s", err, record.CodeOwnerAway)
	}
	if keys := w.keys(); len(keys) != 0 {
		t.Fatalf("a refused confirm put %+v on the list", keys)
	}
	if after, err := w.svc.PairStatus(pairCtx(t)); err != nil || after.Phase != ownerwire.PairPending || after.PendingID != st.PendingID {
		t.Fatalf("after the refusal the pairing is %+v (%v), want the same one pending", after, err)
	}
	w.refuseLogin(phone, "unconfirmed")

	if err := w.svc.Beat(pairDesk); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.PairConfirm(pairCtx(t), pairDesk, ownerwire.PairPendingParams{PendingID: st.PendingID}); err != nil {
		t.Fatalf("PairConfirm with the owner back: %v", err)
	}
	_, res := w.login(phone)
	if !w.loggedIn(res) {
		t.Errorf("the credential confirmed with the owner present cannot log in: %d %q", res.status, res.body)
	}
}
