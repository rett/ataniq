package webpush

import (
	"context"
	"crypto/ecdh"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

const testSubject = "mailto:ops@example.org"

// pushed is what the fake push service saw of one request.
type pushed struct {
	path    string
	header  http.Header
	payload []byte
	err     error
}

// fakeService is a push service over httptest TLS. It checks each request's
// VAPID token against key and decrypts its body with the user agent's key,
// then answers with respond.
type fakeService struct {
	srv     *httptest.Server
	ua      *ecdh.PrivateKey
	auth    []byte
	key     *VAPIDKey
	mu      sync.Mutex
	seen    []pushed
	respond func(w http.ResponseWriter, r *http.Request)
}

func newFakeService(t *testing.T, key *VAPIDKey, respond func(w http.ResponseWriter, r *http.Request)) *fakeService {
	t.Helper()
	ua, auth := newUA(t)
	f := &fakeService{ua: ua, auth: auth, key: key, respond: respond}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := pushed{path: r.URL.Path, header: r.Header.Clone()}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
		if err == nil {
			p.payload, p.err = decrypt(f.ua.Bytes(), f.auth, body)
		} else {
			p.err = err
		}
		f.mu.Lock()
		f.seen = append(f.seen, p)
		f.mu.Unlock()
		f.respond(w, r)
	}))
	t.Cleanup(func() {
		f.srv.Client().CloseIdleConnections()
		f.srv.Close()
	})
	return f
}

func (f *fakeService) requests() []pushed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pushed(nil), f.seen...)
}

func (f *fakeService) subscription(path string) Subscription {
	return Subscription{Endpoint: f.srv.URL + path, P256dh: f.ua.PublicKey().Bytes(), Auth: f.auth}
}

func (f *fakeService) sender(t *testing.T, timeout time.Duration) *Sender {
	t.Helper()
	s, err := NewSender(Config{
		Key: f.key, Subject: testSubject, Client: f.srv.Client(),
		Hosts: AllowHosts("127.0.0.1"), TestEndpoints: true,
		Timeout: timeout, TokenLife: 12 * time.Hour,
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func status(code int, header ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.WriteHeader(code)
	}
}

func TestSendCreated(t *testing.T) {
	key, _ := GenerateVAPIDKey()
	f := newFakeService(t, key, status(http.StatusCreated))
	s := f.sender(t, 5*time.Second)
	err := s.Send(context.Background(), f.subscription("/push/abc"), []byte(`{"n":3}`),
		Options{TTL: 90*time.Second + 500*time.Millisecond, Urgency: UrgencyHigh, Topic: "cards"})
	if err != nil {
		t.Fatal(err)
	}
	seen := f.requests()
	if len(seen) != 1 {
		t.Fatalf("%d requests", len(seen))
	}
	p := seen[0]
	if p.err != nil || string(p.payload) != `{"n":3}` || p.path != "/push/abc" {
		t.Fatalf("payload %q path %q: %v", p.payload, p.path, p.err)
	}
	for k, want := range map[string]string{
		"Content-Encoding": "aes128gcm", "Content-Type": "application/octet-stream",
		"Ttl": "90", "Urgency": "high", "Topic": "cards",
	} {
		if got := p.header.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	rest, ok := strings.CutPrefix(p.header.Get("Authorization"), "vapid t=")
	tok, k, ok2 := strings.Cut(rest, ", k=")
	if !ok || !ok2 || k != key.PublicKeyString() {
		t.Fatalf("Authorization %q", p.header.Get("Authorization"))
	}
	c := verifyToken(t, tok, key.PublicKey())
	if c["aud"] != f.srv.URL || c["sub"] != testSubject || c["exp"] != float64(testNow.Add(12*time.Hour).Unix()) {
		t.Fatalf("claims %v, server %s", c, f.srv.URL)
	}
	if p.header.Get("Cookie") != "" {
		t.Error("a cookie was sent")
	}

	// No Urgency or Topic header when they are not set; TTL is always sent.
	if err := s.Send(context.Background(), f.subscription("/push/abc"), []byte(`{"n":1}`), Options{}); err != nil {
		t.Fatal(err)
	}
	p = f.requests()[1]
	if _, ok := p.header["Urgency"]; ok {
		t.Error("Urgency sent")
	}
	if _, ok := p.header["Topic"]; ok {
		t.Error("Topic sent")
	}
	if p.header.Get("TTL") != "0" {
		t.Errorf("TTL %q", p.header.Get("TTL"))
	}
}

func TestSendStatuses(t *testing.T) {
	key, _ := GenerateVAPIDKey()
	retryDate := testNow.Add(5 * time.Minute).Format(http.TimeFormat)
	cases := []struct {
		name    string
		respond func(http.ResponseWriter, *http.Request)
		kind    error
		code    int
		retry   time.Duration
	}{
		{"404", status(404), ErrGone, 404, 0},
		{"410", status(410), ErrGone, 410, 0},
		{"413", status(413, "Retry-After", "60"), ErrEntityTooLarge, 413, 0},
		{"429 with seconds", status(429, "Retry-After", "120"), ErrRateLimited, 429, 2 * time.Minute},
		{"429 with a date", status(429, "Retry-After", retryDate), ErrRateLimited, 429, 5 * time.Minute},
		{"429 without", status(429), ErrRateLimited, 429, 0},
		{"500 without", status(500), ErrServer, 500, 0},
		{"503 with seconds", status(503, "Retry-After", "30"), ErrServer, 503, 30 * time.Second},
		{"599", status(599, "Retry-After", "1"), ErrServer, 599, time.Second},
		{"200", status(200), ErrUnexpectedStatus, 200, 0},
		{"202", status(202), ErrUnexpectedStatus, 202, 0},
		{"400 ignores Retry-After", status(400, "Retry-After", "10"), ErrUnexpectedStatus, 400, 0},
		{"401", status(401), ErrUnexpectedStatus, 401, 0},
		{"403", status(403), ErrUnexpectedStatus, 403, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeService(t, key, c.respond)
			err := f.sender(t, 5*time.Second).Send(context.Background(), f.subscription("/p/token-xyz"), []byte(`{"n":1}`), Options{TTL: time.Minute})
			var se *StatusError
			if !errors.As(err, &se) || !errors.Is(err, c.kind) || se.Status != c.code || se.RetryAfter != c.retry {
				t.Fatalf("got %v (%#v)", err, se)
			}
			for _, other := range []error{ErrGone, ErrEntityTooLarge, ErrRateLimited, ErrServer, ErrUnexpectedStatus} {
				if other != c.kind && errors.Is(err, other) {
					t.Fatalf("%v is also %v", err, other)
				}
			}
			if strings.Contains(err.Error(), "token-xyz") || strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("error names the endpoint: %v", err)
			}
		})
	}
}

func TestSendFollowsNoRedirect(t *testing.T) {
	key, _ := GenerateVAPIDKey()
	f := newFakeService(t, key, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			w.WriteHeader(http.StatusCreated)
			return
		}
		code, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/push/"))
		http.Redirect(w, r, "/elsewhere", code)
	})
	for _, code := range []int{301, 302, 303, 307, 308} {
		err := f.sender(t, 5*time.Second).Send(context.Background(), f.subscription(fmt.Sprintf("/push/%d", code)), []byte("x"), Options{})
		var se *StatusError
		if !errors.As(err, &se) || se.Status != code || !errors.Is(err, ErrUnexpectedStatus) {
			t.Fatalf("redirect %d: %v", code, err)
		}
	}
	seen := f.requests()
	if len(seen) != 5 {
		t.Fatalf("%d requests", len(seen))
	}
	for _, p := range seen {
		if p.path == "/elsewhere" {
			t.Fatal("a redirect was followed")
		}
	}
	// The caller's client is left as it was.
	if f.srv.Client().CheckRedirect != nil {
		t.Fatal("the caller's client was changed")
	}
}

func TestSendBoundsResponseBody(t *testing.T) {
	key, _ := GenerateVAPIDKey()
	release := make(chan struct{})
	f := newFakeService(t, key, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		// Far more than 4 KiB, then hold the body open: a sender that read
		// to the end would wait for its timeout.
		_, _ = w.Write([]byte(strings.Repeat("x", 64<<10)))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	start := time.Now()
	err := f.sender(t, 20*time.Second).Send(context.Background(), f.subscription("/push"), []byte("x"), Options{})
	elapsed := time.Since(start)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("Send took %v: it read past 4 KiB", elapsed)
	}
}

func TestSendBoundsRequestTime(t *testing.T) {
	key, _ := GenerateVAPIDKey()
	release := make(chan struct{})
	f := newFakeService(t, key, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	start := time.Now()
	err := f.sender(t, 300*time.Millisecond).Send(context.Background(), f.subscription("/push/secret-token"), []byte("x"), Options{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the timeout did not bound the request")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error names the endpoint: %v", err)
	}
	// The caller's context bounds it too.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.sender(t, 5*time.Second).Send(ctx, f.subscription("/push"), []byte("x"), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: %v", err)
	}
}

// roundTrip is a transport that records the request and answers 201, so the
// refusals can be tested without a server: a refused endpoint never reaches
// it.
type roundTrip struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (rt *roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.reqs = append(rt.reqs, r)
	rt.mu.Unlock()
	if r.Body != nil {
		_ = r.Body.Close()
	}
	return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
}

func (rt *roundTrip) count() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.reqs)
}

func TestSendRefusesEndpoints(t *testing.T) {
	key, _ := GenerateVAPIDKey()
	ua, auth := newUA(t)
	rt := &roundTrip{}
	s, err := NewSender(Config{Key: key, Subject: testSubject, Client: &http.Client{Transport: rt}, Hosts: AllowHosts("push.example.org", "127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	refused := map[string]string{
		"http":                "http://push.example.org/x",
		"no scheme":           "push.example.org/x",
		"opaque":              "https:push.example.org/x",
		"credentials":         "https://user:pass@push.example.org/x",
		"user only":           "https://user@push.example.org/x",
		"fragment":            "https://push.example.org/x#frag",
		"empty fragment":      "https://push.example.org/x#",
		"IPv4 literal":        "https://127.0.0.1/x",
		"IPv6 literal":        "https://[::1]/x",
		"IPv6 with zone":      "https://[fe80::1%25eth0]/x",
		"mapped IPv4":         "https://[::ffff:127.0.0.1]/x",
		"other port":          "https://push.example.org:8443/x",
		"empty port":          "https://push.example.org:/x",
		"host not in policy":  "https://other.example.org/x",
		"subdomain of policy": "https://a.push.example.org/x",
		"no host":             "https:///x",
		"empty":               "",
		"too long":            "https://push.example.org/" + strings.Repeat("a", maxEndpoint),
		"control character":   "https://push.example.org/\x01",
		"ftp":                 "ftp://push.example.org/x",
	}
	for name, ep := range refused {
		err := s.Send(context.Background(), Subscription{Endpoint: ep, P256dh: ua.PublicKey().Bytes(), Auth: auth}, []byte("x"), Options{})
		if !errors.Is(err, ErrEndpoint) {
			t.Errorf("%s: %v", name, err)
		}
		if ep != "" && strings.Contains(err.Error(), ep) {
			t.Errorf("%s: error names the endpoint: %v", name, err)
		}
	}
	if rt.count() != 0 {
		t.Fatalf("%d refused requests were sent", rt.count())
	}

	// Accepted, with the origin as the token's audience: the default port
	// and the case of the host do not appear in it.
	for _, ep := range []string{"https://push.example.org/x", "https://PUSH.Example.org:443/x?q=1"} {
		if err := s.Send(context.Background(), Subscription{Endpoint: ep, P256dh: ua.PublicKey().Bytes(), Auth: auth}, []byte("x"), Options{}); err != nil {
			t.Fatalf("%s: %v", ep, err)
		}
		r := rt.reqs[len(rt.reqs)-1]
		rest, _ := strings.CutPrefix(r.Header.Get("Authorization"), "vapid t=")
		tok, _, _ := strings.Cut(rest, ", k=")
		if c := verifyToken(t, tok, key.PublicKey()); c["aud"] != "https://push.example.org" {
			t.Fatalf("%s: aud %v", ep, c["aud"])
		}
	}

	// With TestEndpoints the policy still decides.
	ts, _ := NewSender(Config{Key: key, Subject: testSubject, Client: &http.Client{Transport: rt}, Hosts: AllowHosts("127.0.0.1"), TestEndpoints: true})
	before := rt.count()
	if err := ts.Send(context.Background(), Subscription{Endpoint: "https://127.0.0.2:8443/x", P256dh: ua.PublicKey().Bytes(), Auth: auth}, []byte("x"), Options{}); !errors.Is(err, ErrEndpoint) {
		t.Fatalf("test sender, host not in policy: %v", err)
	}
	if err := ts.Send(context.Background(), Subscription{Endpoint: "http://127.0.0.1:8443/x", P256dh: ua.PublicKey().Bytes(), Auth: auth}, []byte("x"), Options{}); !errors.Is(err, ErrEndpoint) {
		t.Fatalf("test sender, http: %v", err)
	}
	if err := ts.Send(context.Background(), Subscription{Endpoint: "https://127.0.0.1:8443/x", P256dh: ua.PublicKey().Bytes(), Auth: auth}, []byte("x"), Options{}); err != nil {
		t.Fatalf("test sender, allowed: %v", err)
	}
	if rt.count() != before+1 {
		t.Fatal("test sender sent a refused request")
	}
}

func TestSendRefusesOptionsAndPayload(t *testing.T) {
	key, _ := GenerateVAPIDKey()
	ua, auth := newUA(t)
	rt := &roundTrip{}
	s, _ := NewSender(Config{Key: key, Subject: testSubject, Client: &http.Client{Transport: rt}, Hosts: AllowHosts("push.example.org")})
	sub := Subscription{Endpoint: "https://push.example.org/x", P256dh: ua.PublicKey().Bytes(), Auth: auth}
	bad := map[string]Options{
		"negative TTL":     {TTL: -time.Second},
		"TTL over 28 days": {TTL: MaxTTL + time.Second},
		"urgency":          {Urgency: "urgent"},
		"topic too long":   {Topic: strings.Repeat("a", 33)},
		"topic with space": {Topic: "a b"},
		"topic with dot":   {Topic: "a.b"},
	}
	for name, o := range bad {
		if err := s.Send(context.Background(), sub, []byte("x"), o); !errors.Is(err, ErrOptions) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.Send(context.Background(), sub, make([]byte, MaxPayload+1), Options{}); !errors.Is(err, ErrPayloadTooLarge) {
		t.Errorf("payload of 3994: %v", err)
	}
	if err := s.Send(context.Background(), Subscription{Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: auth[:8]}, []byte("x"), Options{}); !errors.Is(err, ErrSubscriptionKeys) {
		t.Errorf("short auth: %v", err)
	}
	if rt.count() != 0 {
		t.Fatalf("%d refused requests were sent", rt.count())
	}
	for _, o := range []Options{{TTL: MaxTTL, Topic: strings.Repeat("A", 32), Urgency: UrgencyVeryLow}, {Urgency: UrgencyLow}, {Urgency: UrgencyNormal}} {
		if err := s.Send(context.Background(), sub, make([]byte, MaxPayload), o); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

func TestNewSenderRefusals(t *testing.T) {
	key, _ := GenerateVAPIDKey()
	good := Config{Key: key, Subject: testSubject, Client: &http.Client{}, Hosts: AllowHosts("push.example.org")}
	if _, err := NewSender(good); err != nil {
		t.Fatal(err)
	}
	mutate := map[string]func(*Config){
		"no key":           func(c *Config) { c.Key = nil },
		"no client":        func(c *Config) { c.Client = nil },
		"no policy":        func(c *Config) { c.Hosts = nil },
		"no subject":       func(c *Config) { c.Subject = "" },
		"bad subject":      func(c *Config) { c.Subject = "ops@example.org" },
		"token over 12 h":  func(c *Config) { c.TokenLife = MaxTokenLife + time.Second },
		"negative life":    func(c *Config) { c.TokenLife = -time.Second },
		"negative timeout": func(c *Config) { c.Timeout = -time.Second },
	}
	for name, m := range mutate {
		c := good
		m(&c)
		if _, err := NewSender(c); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAllowHosts(t *testing.T) {
	p := AllowHosts("Push.Example.org", "", "web.push.example.net")
	for host, want := range map[string]bool{
		"push.example.org": true, "PUSH.EXAMPLE.ORG": true, "web.push.example.net": true,
		"": false, "example.org": false, "a.push.example.org": false, "push.example.org.": false,
	} {
		if p(host) != want {
			t.Errorf("%q: %v", host, !want)
		}
	}
	if AllowHosts()("push.example.org") {
		t.Error("an empty list accepted a host")
	}
}

func TestRetryAfter(t *testing.T) {
	for v, want := range map[string]time.Duration{
		"":                         0,
		"0":                        0,
		"1":                        time.Second,
		"120":                      2 * time.Minute,
		"-5":                       0,
		"+5":                       0,
		"1.5":                      0,
		"abc":                      0,
		"86400":                    MaxRetryAfter,
		"999999999999999999999999": MaxRetryAfter,
		testNow.Add(time.Hour).Format(http.TimeFormat):      time.Hour,
		testNow.Add(-time.Hour).Format(http.TimeFormat):     0,
		testNow.Add(48 * time.Hour).Format(http.TimeFormat): MaxRetryAfter,
	} {
		if got := retryAfter(v, testNow); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
}

func TestDecodeKey(t *testing.T) {
	for _, s := range []string{"BTBZMqHH6r4Tts7J_aSIgg", "BTBZMqHH6r4Tts7J_aSIgg=="} {
		b, err := DecodeKey(s)
		if err != nil || len(b) != 16 {
			t.Errorf("%q: %d bytes, %v", s, len(b), err)
		}
	}
	if _, err := DecodeKey("not base64!"); err == nil {
		t.Error("bad text decoded")
	}
}

func TestStatusErrorText(t *testing.T) {
	e := &StatusError{Status: 429, RetryAfter: time.Minute}
	if got := e.Error(); got != fmt.Sprintf("%s (status 429), retry after 1m0s", ErrRateLimited) {
		t.Fatalf("%q", got)
	}
}
