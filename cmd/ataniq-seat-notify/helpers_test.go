package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/seatnotice"
	"ataniq/internal/webpush"
)

const testSubject = "mailto:owner@example.org"

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// pushed is what the fake push service saw of one request.
type pushed struct {
	path    string
	header  http.Header
	payload []byte
	err     error
}

// ua is a user agent: the key pair and auth secret a subscription is made of.
type ua struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newUA(t *testing.T) *ua {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return &ua{priv: priv, auth: auth}
}

// push is the fake service: an httptest TLS server that decrypts what it is
// sent with the user agents' keys and answers by path, 201 unless say has an
// entry.
type push struct {
	srv *httptest.Server
	key *webpush.VAPIDKey

	mu   sync.Mutex
	uas  map[string]*ua
	seen []pushed
	say  map[string]int
	hold map[string]chan struct{}
}

func newPush(t *testing.T) *push {
	t.Helper()
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	p := &push{key: key, uas: map[string]*ua{}, say: map[string]int{}, hold: map[string]chan struct{}{}}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
		rec := pushed{path: r.URL.Path, header: r.Header.Clone(), err: err}
		p.mu.Lock()
		u := p.uas[r.URL.Path]
		code := p.say[r.URL.Path]
		hold := p.hold[r.URL.Path]
		p.mu.Unlock()
		if err == nil && u != nil {
			rec.payload, rec.err = decrypt(u.priv.Bytes(), u.auth, body)
		}
		p.mu.Lock()
		p.seen = append(p.seen, rec)
		p.mu.Unlock()
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		if code == 0 {
			code = http.StatusCreated
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(func() {
		p.srv.Client().CloseIdleConnections()
		p.srv.Close()
	})
	return p
}

// host is the fake service's host, which the tests' policy allows.
func (p *push) host() string {
	u, _ := url.Parse(p.srv.URL)
	return u.Hostname()
}

// subscription makes a subscription for path with a new user agent.
func (p *push) subscription(t *testing.T, path string) seatnotice.Subscription {
	t.Helper()
	u := newUA(t)
	p.mu.Lock()
	p.uas[path] = u
	p.mu.Unlock()
	return seatnotice.Subscription{Endpoint: p.srv.URL + path, P256DH: b64(u.priv.PublicKey().Bytes()), Auth: b64(u.auth)}
}

func (p *push) respond(path string, code int) {
	p.mu.Lock()
	p.say[path] = code
	p.mu.Unlock()
}

func (p *push) requests() []pushed {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pushed(nil), p.seen...)
}

// testHook points the command's sender at the fake service.
func (p *push) testHook(cfg *webpush.Config) {
	cfg.Client = p.srv.Client()
	cfg.TestEndpoints = true
	cfg.Hosts = webpush.AllowHosts(p.host())
}

// allow is the notifier's endpoint policy for the tests: the fake service's
// host, on any port (the fake listens on none but its own).
func (p *push) allow(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "https" && u.Hostname() == p.host()
}

// install points the command at the fake service and at lookup for the seat
// user, for the length of the test.
func (p *push) install(t *testing.T, lookup func(string) (uint32, error)) {
	t.Helper()
	oldLookup, oldHook, oldAllow := lookupUID, senderHook, allowEndpoints
	lookupUID, senderHook = lookup, p.testHook
	allowEndpoints = func(webpush.HostPolicy) func(string) bool { return p.allow }
	t.Cleanup(func() { lookupUID, senderHook, allowEndpoints = oldLookup, oldHook, oldAllow })
}

// sender makes the real sender, pointed at the fake service.
func (p *push) sender(t *testing.T) *webpush.Sender {
	t.Helper()
	cfg := webpush.Config{Key: p.key, Subject: testSubject, Timeout: 5 * time.Second}
	p.testHook(&cfg)
	s, err := webpush.NewSender(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// logs collects the notifier's log lines.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logs) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "")
}

// clock is a clock the tests move.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)} }

// ownDir makes a folder of this user that group and other cannot write.
func ownDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "sn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(dir, 0o700)
		os.RemoveAll(dir)
	})
	return dir
}

// testStore opens an empty store in a new folder.
func testStore(t *testing.T, allow func(string) bool) (*store, string) {
	t.Helper()
	dir := ownDir(t)
	path := filepath.Join(dir, "subscriptions.json")
	st, err := openStore(path, allow)
	if err != nil {
		t.Fatal(err)
	}
	return st, path
}

// testNotifier makes a notifier on the fake service with a fresh store, a
// log and a clock.
func testNotifier(t *testing.T, p *push) (*notifier, *store, *logs, *clock) {
	t.Helper()
	st, _ := testStore(t, p.allow)
	lg := &logs{}
	ck := newClock()
	n := newNotifier(p.key, st, p.sender(t), p.allow, time.Hour, newLimiter(maxPushesPerHour, time.Hour, ck.now), lg.logf)
	return n, st, lg, ck
}

// decrypt is the user agent's side of RFC 8291 and RFC 8188 for one record,
// the test's own reading of a message.
func decrypt(uaPrivate, auth, msg []byte) ([]byte, error) {
	if len(msg) < 21 {
		return nil, errors.New("short header")
	}
	salt := msg[:16]
	rs := binary.BigEndian.Uint32(msg[16:20])
	idlen := int(msg[20])
	if len(msg) < 21+idlen || rs < 18 {
		return nil, errors.New("bad header")
	}
	asPublicBytes := msg[21 : 21+idlen]
	record := msg[21+idlen:]
	if len(record) > int(rs) {
		return nil, errors.New("more than one record")
	}
	priv, err := ecdh.P256().NewPrivateKey(uaPrivate)
	if err != nil {
		return nil, err
	}
	asPublic, err := ecdh.P256().NewPublicKey(asPublicBytes)
	if err != nil {
		return nil, err
	}
	secret, err := priv.ECDH(asPublic)
	if err != nil {
		return nil, err
	}
	prkKey, _ := hkdf.Extract(sha256.New, secret, auth)
	info := append([]byte("WebPush: info\x00"), priv.PublicKey().Bytes()...)
	info = append(info, asPublicBytes...)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, record, nil)
	if err != nil {
		return nil, err
	}
	i := len(plain) - 1
	for i >= 0 && plain[i] == 0 {
		i--
	}
	if i < 0 || plain[i] != 0x02 {
		return nil, errors.New("no last-record delimiter")
	}
	return plain[:i], nil
}
