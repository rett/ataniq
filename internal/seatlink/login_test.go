package seatlink

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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/credkind"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

const (
	seatOrigin = "https://seat.example"
	seatRPID   = "seat.example"
)

// TestSeatLinkLoginChallengeVector holds the login challenge to the
// contract's test vector (docs/design/seat-gate.md, "Test vectors"), which
// two programs that share no code computed.
func TestSeatLinkLoginChallengeVector(t *testing.T) {
	instance := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(0x40 + i)
	}
	want := "586b941d8d6d60ec7626a362dac9e97c777664ee647ecf1688121b7b1237fa8f"
	got := LoginChallengeOf(instance, nonce)
	if hex.EncodeToString(got[:]) != want {
		t.Errorf("login challenge %x, want %s", got, want)
	}
	// It is the wire's one function, which the daemon calls too.
	if w, err := ownerwire.LoginChallenge(instance, nonce); err != nil || hex.EncodeToString(w) != want {
		t.Errorf("ownerwire.LoginChallenge %x %v", w, err)
	}
	// A pin or a nonce of another form gives no challenge the daemon could
	// have drawn.
	if LoginChallengeOf("ABC", nonce) != ([32]byte{}) || LoginChallengeOf(instance, nonce[:31]) != ([32]byte{}) {
		t.Error("a challenge of a malformed pin or nonce")
	}
}

// serveGateCaps is serveGate with the capability list given.
func serveGateCaps(t *testing.T, pins, caps []string, h seatgate.HandlerFunc) *gate {
	t.Helper()
	g := &gate{id: newIdentity(t, "gate")}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g.addr = ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	counted := func(ctx context.Context, p seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
		g.calls.Add(1)
		return h(ctx, p, op, params)
	}
	go func() {
		defer close(done)
		seatgate.Serve(ctx, ln, seatgate.ServerConfig{
			TLS:           seatgate.ServerTLS(g.id, pins),
			Caps:          caps,
			ShutdownGrace: 100 * time.Millisecond,
		}, seatgate.HandlerFunc(counted))
	}()
	var once atomic.Bool
	g.stop = func() {
		if once.CompareAndSwap(false, true) {
			cancel()
			<-done
		}
	}
	t.Cleanup(g.stop)
	return g
}

// dialPinned is dial with the gate's pin given to the link.
func dialPinned(t *testing.T, g *gate, seat seatgate.Identity) *Daemon {
	t.Helper()
	_, c := dial(t, g, seat)
	d, err := New(c, WithGatePin(g.id.Fingerprint()))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// fakeDesk is the daemon's side of the gate's login, as the contract gives
// it, with the verifier the daemon is to use (credkind) and an in-memory key
// list. It decides nothing.
type fakeDesk struct {
	mu         sync.Mutex
	instance   string // the gate key's fingerprint
	keys       []credkind.Credential
	revoked    map[string]bool
	challenges map[string]deskChallenge
	// tamper, when set, changes the challenge the gate answers.
	tamper bool
	// logins counts the calls of seat.login that reached the desk.
	logins int
}

type deskChallenge struct {
	challenge [32]byte
	expires   time.Time
}

func newFakeDesk(instance string) *fakeDesk {
	return &fakeDesk{instance: instance, revoked: map[string]bool{}, challenges: map[string]deskChallenge{}}
}

// pair registers the authenticator's credential, as a confirmed pairing does.
func (f *fakeDesk) pair(t *testing.T, a *webauthntest.Authenticator) {
	t.Helper()
	reg := a.Create(bytes.Repeat([]byte{1}, 32))
	c, err := credkind.Register(credkind.RegistrationInput{
		RPID: seatRPID, CredentialID: reg.CredentialID, SPKI: reg.PublicKey, AuthenticatorData: reg.AuthenticatorData,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.keys = append(f.keys, c)
	f.mu.Unlock()
}

func (f *fakeDesk) handle(_ context.Context, _ seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch op {
	case OpLoginChallenge:
		var none struct{}
		if seatgate.DecodeStrict(params, &none) != nil {
			return nil, seatgate.NewCodedError("bad-params")
		}
		nonce := make([]byte, 32)
		rand.Read(nonce)
		idb := make([]byte, 8)
		rand.Read(idb)
		id := hex.EncodeToString(idb)
		ch := LoginChallengeOf(f.instance, nonce)
		f.challenges[id] = deskChallenge{challenge: ch, expires: time.Now().Add(5 * time.Minute)}
		if f.tamper {
			ch[0] ^= 1
		}
		var allow [][]byte
		for i := len(f.keys) - 1; i >= 0; i-- {
			if !f.revoked[string(f.keys[i].CredentialID)] {
				allow = append(allow, f.keys[i].CredentialID)
			}
		}
		// The daemon writes its answers with the wire's encoders, as the
		// gate's handler does.
		return ownerwire.EncodeSeatLoginChallengeResult(ownerwire.SeatLoginChallengeResult{
			ChallengeID: id, Challenge: ch[:], Nonce: nonce,
			ExpiresAt: record.FormatTime(time.Now().Add(5 * time.Minute)), Allow: allow,
		})
	case OpLogin:
		f.logins++
		var p ownerwire.SeatLoginParams
		if seatgate.DecodeStrict(params, &p) != nil {
			return nil, seatgate.NewCodedError("bad-params")
		}
		entry, ok := f.challenges[p.ChallengeID]
		delete(f.challenges, p.ChallengeID)
		if !ok || time.Now().After(entry.expires) {
			return nil, seatgate.NewCodedError(ownerwire.CodeChallengeStale)
		}
		var key *credkind.Credential
		for i := range f.keys {
			if bytes.Equal(f.keys[i].CredentialID, p.Assertion.CredentialID) {
				key = &f.keys[i]
			}
		}
		if key == nil {
			return nil, seatgate.NewCodedError(codeKeyUnknown)
		}
		if f.revoked[string(key.CredentialID)] {
			return nil, seatgate.NewCodedError(ownerwire.CodeKeyRevoked)
		}
		_, err := credkind.Verify(*key, credkind.AssertionInput{
			CredentialID: p.Assertion.CredentialID, ClientDataJSON: p.Assertion.ClientDataJSON,
			AuthenticatorData: p.Assertion.AuthenticatorData, Signature: p.Assertion.Signature,
		}, credkind.Expect{Challenge: entry.challenge[:], Origin: seatOrigin, RPID: seatRPID})
		if err != nil {
			return nil, seatgate.NewCodedError(ownerwire.CodeAssertionInvalid)
		}
		return ownerwire.EncodeSeatLoginResult(ownerwire.SeatLoginResult{Credential: DisplayID(key.CredentialID), CredentialID: key.CredentialID, Kind: key.Kind})
	}
	return nil, seatgate.NewCodedError(codeUnknownOp)
}

// seatApp is the seat app over a link, as ataniq-seat wires it, with the
// test options for requests that are not a browser's.
func seatApp(t *testing.T, d seatapp.Daemon) http.Handler {
	t.Helper()
	h, err := seatapp.New(seatapp.Config{Origin: seatOrigin, RPID: seatRPID, Daemon: d, AllowNoFetchMetadataForTests: true})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// browser is a phone's browser in front of the app: it keeps the app's
// cookies.
type browser struct {
	h       http.Handler
	cookies map[string]*http.Cookie
}

func newBrowser(h http.Handler) *browser { return &browser{h: h, cookies: map[string]*http.Cookie{}} }

func (b *browser) do(method, path, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Host = seatRPID
	if method == "POST" {
		req.Header.Set("Origin", seatOrigin)
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	b.h.ServeHTTP(w, req)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = &http.Cookie{Name: c.Name, Value: c.Value}
		}
	}
	return w
}

// loginWith runs the login page's steps with an authenticator: it asks for a
// login challenge, makes the assertion over it, and posts it. It returns the
// answer to the login, or to the challenge when that failed.
func (b *browser) loginWith(t *testing.T, a *webauthntest.Authenticator) *httptest.ResponseRecorder {
	t.Helper()
	w := b.do("POST", "/api/login-challenge", "{}")
	if w.Code != 200 {
		return w
	}
	var ch struct {
		Challenge string   `json:"challenge"`
		Allow     []string `json:"allow"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ch); err != nil {
		t.Fatalf("challenge body %s", w.Body)
	}
	challenge, err := b64url(ch.Challenge)
	if err != nil {
		t.Fatalf("challenge %q", ch.Challenge)
	}
	as, err := a.Get(challenge)
	if err != nil {
		t.Fatal(err)
	}
	return b.do("POST", "/api/login", loginJSON(as))
}

func loginJSON(as webauthntest.Assertion) string {
	body, _ := json.Marshal(map[string]string{
		"credential_id":      b64urlEnc(as.CredentialID),
		"client_data_json":   b64urlEnc(as.ClientDataJSON),
		"authenticator_data": b64urlEnc(as.AuthenticatorData),
		"signature":          b64urlEnc(as.Signature),
	})
	return string(body)
}

// TestSeatLinkLoginWithASoftwareAuthenticator: the whole login, from the
// seat app through the link and the gate to a desk that verifies with the
// daemon's verifier, with a software authenticator in the phone's place.
func TestSeatLinkLoginWithASoftwareAuthenticator(t *testing.T) {
	seat := newIdentity(t, "seat")
	var desk *fakeDesk
	g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"pair", "read", "login"},
		func(ctx context.Context, p seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
			return desk.handle(ctx, p, op, params)
		})
	desk = newFakeDesk(g.id.Fingerprint())
	phone, err := webauthntest.New(seatRPID, seatOrigin)
	if err != nil {
		t.Fatal(err)
	}
	desk.pair(t, phone)
	link := dialPinned(t, g, seat)
	// Each browser below has an app of its own, so that the app's bound of
	// login challenges of all sources, 8 a minute, is not met.
	fresh := func() *browser { return newBrowser(seatApp(t, link)) }

	b := fresh()
	w := b.do("POST", "/api/login-challenge", "{}")
	if w.Code != 200 || !strings.Contains(w.Body.String(), b64urlEnc(phone.CredentialID)) {
		t.Fatalf("challenge: %d %s", w.Code, w.Body)
	}
	b = fresh()
	if w := b.loginWith(t, phone); w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	if b.cookies[seatapp.SessionCookie] == nil {
		t.Fatal("no session after the login")
	}

	// Each of these is refused by the desk's verifier, and makes no session.
	for name, mod := range map[string]func(*webauthntest.Authenticator){
		"another origin":        func(a *webauthntest.Authenticator) { a.Origin = "https://evil.example" },
		"another relying party": func(a *webauthntest.Authenticator) { a.RPID = "evil.example" },
		"no user verification":  func(a *webauthntest.Authenticator) { a.Flags = webauthntest.FlagUP },
		"another key": func(a *webauthntest.Authenticator) {
			other, _ := webauthntest.New(seatRPID, seatOrigin)
			a.Key = other.Key
		},
	} {
		a := *phone
		mod(&a)
		b := fresh()
		w := b.loginWith(t, &a)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "login_refused") || b.cookies[seatapp.SessionCookie] != nil {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}

	// A credential that is not paired, and one that is revoked.
	stranger, _ := webauthntest.New(seatRPID, seatOrigin)
	if w := fresh().loginWith(t, stranger); w.Code != 403 {
		t.Errorf("a stranger's passkey: %d %s", w.Code, w.Body)
	}

	// An assertion made for one challenge does not log in with another.
	b = fresh()
	w = b.do("POST", "/api/login-challenge", "{}")
	old, _ := phone.Get(bytes.Repeat([]byte{3}, 32))
	if w := b.do("POST", "/api/login", loginJSON(old)); w.Code != 403 {
		t.Errorf("an assertion over another challenge: %d %s", w.Code, w.Body)
	}

	desk.mu.Lock()
	desk.revoked[string(phone.CredentialID)] = true
	desk.mu.Unlock()
	if w := fresh().loginWith(t, phone); w.Code != 409 || !strings.Contains(w.Body.String(), "not_paired") {
		t.Errorf("revoked, and no key left to allow: %d %s", w.Code, w.Body)
	}
	other, _ := webauthntest.New(seatRPID, seatOrigin)
	desk.pair(t, other)
	if w := fresh().loginWith(t, phone); w.Code != 403 {
		t.Errorf("a revoked passkey: %d %s", w.Code, w.Body)
	}
}

// TestSeatLinkLoginChallengeIsRecomputed: a login challenge that is not the
// one the gate's pin and the daemon's nonce give is never handed to the
// phone, and a link without the pin asks for none.
func TestSeatLinkLoginChallengeIsRecomputed(t *testing.T) {
	seat := newIdentity(t, "seat")
	var desk *fakeDesk
	g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"pair", "read", "login"},
		func(ctx context.Context, p seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
			return desk.handle(ctx, p, op, params)
		})
	desk = newFakeDesk(g.id.Fingerprint())
	phone, _ := webauthntest.New(seatRPID, seatOrigin)
	desk.pair(t, phone)
	d := dialPinned(t, g, seat)
	if _, err := d.LoginChallenge(context.Background()); err != nil {
		t.Fatalf("an honest challenge: %v", err)
	}
	desk.mu.Lock()
	desk.tamper = true
	desk.mu.Unlock()
	_, err := d.LoginChallenge(context.Background())
	if !errors.Is(err, errChallengeDiffers) {
		t.Errorf("a changed challenge: %v", err)
	}
	w := newBrowser(seatApp(t, d)).do("POST", "/api/login-challenge", "{}")
	if w.Code != 502 || strings.Contains(w.Body.String(), "challenge\"") || len(w.Result().Cookies()) != 0 {
		t.Errorf("the app over a changed challenge: %d %s", w.Code, w.Body)
	}

	// A gate pinned under another key computes another challenge.
	desk.mu.Lock()
	desk.tamper, desk.instance = false, strings.Repeat("0", 64)
	desk.mu.Unlock()
	if _, err := d.LoginChallenge(context.Background()); !errors.Is(err, errChallengeDiffers) {
		t.Errorf("a challenge for another gate: %v", err)
	}

	// Without the pin nothing is asked.
	_, c := dial(t, g, seat)
	bare, _ := New(c)
	before := g.calls.Load()
	if _, err := bare.LoginChallenge(context.Background()); !errors.Is(err, errNoPin) {
		t.Errorf("no pin: %v", err)
	}
	if g.calls.Load() != before {
		t.Error("a link without the pin asked the gate")
	}
	if _, err := New(c, WithGatePin("not a pin")); err == nil {
		t.Error("New took a pin that is not a fingerprint")
	}
}

// TestSeatLinkLoginNeedsTheCapability: a gate that does not list "login" is
// asked nothing, and the phone is told the desk does not take logins.
func TestSeatLinkLoginNeedsTheCapability(t *testing.T) {
	seat := newIdentity(t, "seat")
	g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"pair", "read"}, refuse("failed"))
	d := dialPinned(t, g, seat)
	if _, err := d.LoginChallenge(context.Background()); !errors.Is(err, seatapp.ErrNotOffered) {
		t.Errorf("challenge: %v", err)
	}
	if _, err := d.Login(context.Background(), seatapp.LoginRequest{ChallengeID: "0123456789abcdef"}); !errors.Is(err, seatapp.ErrNotOffered) {
		t.Errorf("login: %v", err)
	}
	if n := g.calls.Load(); n != 0 {
		t.Errorf("the gate was asked %d times", n)
	}
	w := newBrowser(seatApp(t, d)).do("POST", "/api/login-challenge", "{}")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "login_unavailable") {
		t.Errorf("the app: %d %s", w.Code, w.Body)
	}
}

// TestSeatLinkLoginCodes: the gate's codes map to the app's errors, and the
// login's answer must name the credential the assertion named.
func TestSeatLinkLoginCodes(t *testing.T) {
	seat := newIdentity(t, "seat")
	req := seatapp.LoginRequest{
		ChallengeID: "0123456789abcdef", CredentialID: bytes.Repeat([]byte{7}, 32),
		AuthenticatorData: bytes.Repeat([]byte{1}, 37), ClientDataJSON: []byte("{}"), Signature: []byte{1},
	}
	for code, want := range map[string]error{
		"challenge-stale":   seatapp.ErrChallengeStale,
		"assertion-invalid": seatapp.ErrLoginRefused,
		"key-unknown":       seatapp.ErrLoginRefused,
		"key-revoked":       seatapp.ErrLoginRefused,
		"kind-refused":      seatapp.ErrLoginRefused,
		"unknown-op":        seatapp.ErrNotOffered,
		"rate-limited":      seatapp.ErrBusy,
		"busy":              seatapp.ErrBusy,
		"too-many-refusals": seatapp.ErrBusy,
		"unavailable":       nil,
		"failed":            nil,
	} {
		g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"login"}, refuse(code))
		d := dialPinned(t, g, seat)
		_, err := d.Login(context.Background(), req)
		if want != nil {
			if !errors.Is(err, want) {
				t.Errorf("%s: %v, want %v", code, err, want)
			}
			continue
		}
		var re *RefusedError
		if !errors.As(err, &re) || re.Code != code || re.Op != OpLogin || !strings.Contains(err.Error(), OpLogin) {
			t.Errorf("%s: %v", code, err)
		}
	}
	for name, res := range map[string]ownerwire.SeatLoginResult{
		"another credential": {Credential: DisplayID(bytes.Repeat([]byte{8}, 32)), CredentialID: bytes.Repeat([]byte{8}, 32), Kind: "passkey"},
		"another display id": {Credential: "0123456789abcdef", CredentialID: req.CredentialID, Kind: "passkey"},
		"a bad kind":         {Credential: DisplayID(req.CredentialID), CredentialID: req.CredentialID, Kind: "Pass key"},
	} {
		g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"login"}, answer(res))
		d := dialPinned(t, g, seat)
		if _, err := d.Login(context.Background(), req); !errors.Is(err, errLoginAnswer) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An answer that names the assertion's credential is taken.
	good := ownerwire.SeatLoginResult{Credential: DisplayID(req.CredentialID), CredentialID: req.CredentialID, Kind: "vault-passkey"}
	g := serveGateCaps(t, []string{seat.Fingerprint()}, []string{"login"}, answer(good))
	if res, err := dialPinned(t, g, seat).Login(context.Background(), req); err != nil || res.Credential != good.Credential || res.Kind != "vault-passkey" {
		t.Errorf("good: %+v %v", res, err)
	}
	// A request outside the wire's bounds is not sent.
	bad := req
	bad.Signature = make([]byte, 73)
	g = serveGateCaps(t, []string{seat.Fingerprint()}, []string{"login"}, answer(good))
	if _, err := dialPinned(t, g, seat).Login(context.Background(), bad); !errors.Is(err, seatapp.ErrLoginRefused) || g.calls.Load() != 0 {
		t.Errorf("out of bounds: %v, %d calls", err, g.calls.Load())
	}
}

// TestSeatLinkLoginChallengeAnswer: a login challenge's answer that is not
// of its form is refused.
func TestSeatLinkLoginChallengeAnswer(t *testing.T) {
	seat := newIdentity(t, "seat")
	nonce := bytes.Repeat([]byte{4}, 32)
	for name, mod := range map[string]func(*ownerwire.SeatLoginChallengeResult){
		"short nonce":    func(r *ownerwire.SeatLoginChallengeResult) { r.Nonce = r.Nonce[:31] },
		"bad id":         func(r *ownerwire.SeatLoginChallengeResult) { r.ChallengeID = "0123456789ABCDEF" },
		"bad time":       func(r *ownerwire.SeatLoginChallengeResult) { r.ExpiresAt = "tomorrow" },
		"too many keys":  func(r *ownerwire.SeatLoginChallengeResult) { r.Allow = make([][]byte, 17) },
		"short allow id": func(r *ownerwire.SeatLoginChallengeResult) { r.Allow = [][]byte{{1}} },
		"an id twice": func(r *ownerwire.SeatLoginChallengeResult) {
			r.Allow = [][]byte{bytes.Repeat([]byte{7}, 16), bytes.Repeat([]byte{7}, 16)}
		},
		"no allow list": func(r *ownerwire.SeatLoginChallengeResult) { r.Allow = nil },
	} {
		var g *gate
		g = serveGateCaps(t, []string{seat.Fingerprint()}, []string{"login"}, func(context.Context, seatgate.Peer, string, json.RawMessage) (json.RawMessage, error) {
			ch := LoginChallengeOf(g.id.Fingerprint(), nonce)
			r := ownerwire.SeatLoginChallengeResult{ChallengeID: "0123456789abcdef", Challenge: ch[:], Nonce: nonce,
				ExpiresAt: record.FormatTime(time.Now()), Allow: [][]byte{bytes.Repeat([]byte{7}, 16)}}
			for i := range r.Allow {
				r.Allow[i] = bytes.Repeat([]byte{7}, 16)
			}
			mod(&r)
			return json.Marshal(r)
		})
		if _, err := dialPinned(t, g, seat).LoginChallenge(context.Background()); !errors.Is(err, errLoginAnswer) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func b64urlEnc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func b64url(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
