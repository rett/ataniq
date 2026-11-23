package seatapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testOrigin = "https://seat.example"
	testRPID   = "seat.example"
)

// testClientHeader is the header the rig's app reads a client's address
// from. A request without it is counted by its peer, as before.
const testClientHeader = "X-Test-Client"

// testCredentialID is the raw id of the paired credential of the fakes.
var testCredentialID = bytes.Repeat([]byte{0x11}, 32)

// fakeDaemon is the in-memory daemon port.
type fakeDaemon struct {
	mu    sync.Mutex
	calls []PairRequest
	words [WordCount]string
	err   error

	// The login calls: what LoginChallenge and Login answer, and what
	// Login was asked.
	loginCh     LoginChallenge
	loginChErr  error
	loginRes    LoginResult
	loginErr    error
	logins      []LoginRequest
	loginChs    int
	challengeNo int

	// The reads: what they answer, the error they all answer, how many
	// were asked and the ids asked for.
	listing Listing
	card    Card
	page    PathsPage
	readErr error
	reads   int
	shown   []string
}

func (d *fakeDaemon) LoginChallenge(context.Context) (LoginChallenge, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.loginChs++
	if d.loginChErr != nil {
		return LoginChallenge{}, d.loginChErr
	}
	ch := d.loginCh
	if ch.ID == "" {
		// A new id each time, as the daemon draws one.
		d.challengeNo++
		ch.ID = fmt.Sprintf("%016x", d.challengeNo)
	}
	return ch, nil
}

func (d *fakeDaemon) Login(_ context.Context, r LoginRequest) (LoginResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logins = append(d.logins, r)
	if d.loginErr != nil {
		return LoginResult{}, d.loginErr
	}
	return d.loginRes, nil
}

// calls counts the calls of the daemon port of every kind.
func (d *fakeDaemon) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls) + d.loginChs + len(d.logins) + d.reads
}

func (d *fakeDaemon) List(context.Context) (Listing, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reads++
	return d.listing, d.readErr
}

func (d *fakeDaemon) Show(_ context.Context, id string) (Card, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reads++
	d.shown = append(d.shown, id)
	if d.readErr != nil {
		return Card{}, d.readErr
	}
	c := d.card
	if c.ID == "" {
		c.ID = id
	}
	return c, nil
}

func (d *fakeDaemon) Pages(_ context.Context, id string, n int) (PathsPage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reads++
	d.shown = append(d.shown, id)
	if d.readErr != nil {
		return PathsPage{}, d.readErr
	}
	p := d.page
	p.N = n
	return p, nil
}

// readCount is the number of reads the daemon was asked.
func (d *fakeDaemon) readCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reads
}

// testCardID is the id of the fakes' card.
const testCardID = "ap-20261122-0001"

// sampleCard is a card with every section, of plain text.
func sampleCard() Card {
	at := time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC)
	return Card{
		ID: testCardID, Project: "ataniq", Role: "lead", Verb: "feature finish", Feature: "seat-glue",
		Target: "develop", Class: "ref-move", Asked: at, Expires: at.Add(4 * time.Hour),
		BindingSHA256: strings.Repeat("ab", 32),
		Plan:          []Lead{NewLead("Merge the seat glue."), NewLead("Then run the suite.")},
		Refs:          []Ref{{Kind: "update", Ref: NewLead("refs/heads/develop"), Old: strings.Repeat("1", 40), New: strings.Repeat("2", 40)}},
		Diff: &Diff{Files: 2, Added: 10, Removed: 3, Truncated: true, Rows: []PathRow{
			{Path: NewLead("internal/seatapp/cards.go"), Status: "A", Added: 10},
			{Path: NewLead("docs/seat.md"), Status: "M", Removed: 3},
		}},
		Lists:    []List{{Label: "binary files", Paths: []Lead{NewLead("logo.png")}}, {Label: "symlinks"}},
		Flagged:  []Flagged{{Kind: "exec-config", Reason: "changes how the build runs", Path: NewLead("Makefile"), Pattern: NewLead("Makefile")}},
		Evidence: []Evidence{{Kind: "suite", RecordID: "sr-20261122-0001"}},
		Note:     []Lead{NewLead("Two lines"), NewLead("of a note.")},
		Differs:  &Differs{EarlierID: "ap-20261121-0003", ReadAt: at.Add(-time.Hour), Lines: []string{"The feature's tip moved: 111111111111 to 222222222222."}},
	}
}

func (d *fakeDaemon) Offer(string, string, Reading, time.Time) Offer { return Offer{} }

func (d *fakeDaemon) DecideChallenge(context.Context, DecideChallengeRequest) (DecideChallenge, error) {
	return DecideChallenge{}, ErrDaemonUnavailable
}

func (d *fakeDaemon) Decide(context.Context, DecideRequest) (DecideResult, error) {
	return DecideResult{}, ErrDaemonUnavailable
}

func (d *fakeDaemon) Pair(_ context.Context, r PairRequest) (PairResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, r)
	if d.err != nil {
		return PairResult{}, d.err
	}
	return PairResult{Words: d.words}, nil
}

func (d *fakeDaemon) requests() []PairRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]PairRequest(nil), d.calls...)
}

// clock is a settable clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// counterReader is a deterministic Random: the SHA-256 of a running count, so
// that no two reads are equal.
type counterReader struct {
	n   uint64
	buf []byte
}

func (r *counterReader) Read(p []byte) (int, error) {
	for len(r.buf) < len(p) {
		r.n++
		sum := sha256.Sum256(binary.BigEndian.AppendUint64(nil, r.n))
		r.buf = append(r.buf, sum[:]...)
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// rig is an app under test with its fakes.
type rig struct {
	h       http.Handler
	app     *app
	sources int // the source addresses postWith used
	daemon  *fakeDaemon
	clock   *clock
}

func newRig(t *testing.T, mod ...func(*Config)) *rig {
	t.Helper()
	r := &rig{
		daemon: &fakeDaemon{
			words:    [WordCount]string{"amber", "birch", "cedar", "delta", "ember", "fjord", "garnet", "heron"},
			loginCh:  LoginChallenge{Challenge: bytes.Repeat([]byte{0x5a}, 32), Allow: [][]byte{testCredentialID}},
			loginRes: LoginResult{Credential: "0123456789abcdef", CredentialID: testCredentialID, Kind: "platform-passkey"},
			listing: Listing{
				Pending: []Item{{ID: testCardID, Project: "ataniq", Verb: "feature finish", Feature: "seat-glue",
					Asked: time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC), Expires: time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC), Held: true}},
				Recent: []Recent{{ID: "ap-20261121-0003", Project: "ataniq", Verb: "prune", State: "denied", Line: "denied at the desk",
					At: time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)}},
				NotListed: 2,
			},
			card: sampleCard(),
			page: PathsPage{Pages: 3, Rows: []PathRow{{Path: NewLead("a/b.go"), Status: "M", Added: 1}}},
		},
		clock: &clock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)},
	}
	// The test requests carry no Sec-Fetch-Site, as a browser's always do; the
	// tests of that rule turn the option off.
	cfg := Config{Origin: testOrigin, RPID: testRPID, Daemon: r.daemon, Now: r.clock.Now, Random: &counterReader{},
		AllowNoFetchMetadataForTests: true, ClientAddrHeader: testClientHeader}
	for _, m := range mod {
		m(&cfg)
	}
	a, h, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.h, r.app = h, a
	return r
}

// newRequest is httptest.NewRequest with the Host of the test origin, which
// is what the proxy passes on. A "Host" entry in a header map of do or doTo
// replaces it.
func newRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = testRPID
	return req
}

// do sends a request to the handler.
func (r *rig) do(method, path string, body io.Reader, header map[string]string) *httptest.ResponseRecorder {
	req := newRequest(method, path, body)
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	return w
}

// doTo sends a request with a string body to a handler.
func doTo(h http.Handler, method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := newRequest(method, path, strings.NewReader(body))
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// postJSON sends a well-formed same-origin POST.
func (r *rig) postJSON(path, body string) *httptest.ResponseRecorder {
	return r.do("POST", path, strings.NewReader(body), map[string]string{
		"Origin":       testOrigin,
		"Content-Type": "application/json",
	})
}

// wantHeaders are the headers every response carries, spelt out so that a
// change to the app's values fails here.
var wantHeaders = map[string]string{
	"Content-Security-Policy":      "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; require-trusted-types-for 'script'",
	"Strict-Transport-Security":    "max-age=63072000",
	"X-Content-Type-Options":       "nosniff",
	"Referrer-Policy":              "no-referrer",
	"X-Frame-Options":              "DENY",
	"Cache-Control":                "no-store",
	"Cross-Origin-Opener-Policy":   "same-origin",
	"Cross-Origin-Resource-Policy": "same-origin",
}

// checkHeaders fails the test when a response lacks a header, allows a
// passkey assertion or sets a cookie.
func checkHeaders(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	checkHeadersGet(t, what, w, "()")
}

// checkHeadersGet is checkHeaders for a response whose allow-list of
// publickey-credentials-get is get.
func checkHeadersGet(t *testing.T, what string, w *httptest.ResponseRecorder, get string) {
	t.Helper()
	for k, v := range wantHeaders {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s: header %s = %q, want %q", what, k, got, v)
		}
	}
	pp := w.Header().Get("Permissions-Policy")
	for _, want := range []string{"publickey-credentials-create=(self)", "camera=()", "microphone=()", "geolocation=()"} {
		if !strings.Contains(pp, want) {
			t.Errorf("%s: Permissions-Policy %q lacks %s", what, pp, want)
		}
	}
	// A passkey assertion is allowed to the app's own origin on the pages
	// that ask for one, and to nobody anywhere else.
	if strings.Count(pp, "publickey-credentials-get=") != 1 || !strings.Contains(pp, "publickey-credentials-get="+get+",") {
		t.Errorf("%s: Permissions-Policy %q, want publickey-credentials-get=%s", what, pp, get)
	}
	if c := w.Header().Values("Set-Cookie"); len(c) != 0 {
		t.Errorf("%s: sets a cookie: %v", what, c)
	}
}

func stringReader(s string) io.Reader { return strings.NewReader(s) }
