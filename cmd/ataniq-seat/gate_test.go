package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatgate"
)

// gateCode is a pairing code of the wire's form. No output of the command may
// hold it.
const gateCode = "K7M2Q9XAB3CDEFGH4JKM"

const (
	seatOrigin = "https://seat.example"
	seatRPID   = "seat.example"
)

// gateWords is a fingerprint of the wire's form.
var gateWords = []string{"amber", "basin", "cedar", "delta", "ember", "flint", "grove", "harbor"}

// fakeGate is a seat gate served by seatgate.Serve on 127.0.0.1 with a fake
// handler that records the pairing it was sent.
type fakeGate struct {
	id    seatgate.Identity
	addr  string
	calls atomic.Int64

	mu   sync.Mutex
	code string // the code of the last seat.pair
}

// startGate serves a gate that pins seatPin and answers seat.pair with
// refusal as its code, or with gateWords when refusal is empty. It stops when
// the test ends.
func startGate(t *testing.T, seatPin, refusal string) *fakeGate {
	t.Helper()
	certPEM, keyPEM, err := seatgate.GenerateIdentity(rand.Reader, time.Now(), "gate")
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGate{}
	if g.id, err = seatgate.ParseIdentity(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g.addr = ln.Addr().String()
	h := func(_ context.Context, _ seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
		g.calls.Add(1)
		if op != ownerwire.OpSeatPair {
			return nil, seatgate.NewCodedError("unknown-op")
		}
		p, err := ownerwire.DecodeSeatPairParams(params)
		if err != nil {
			return nil, seatgate.NewCodedError("bad-params")
		}
		g.mu.Lock()
		g.code = p.Code
		g.mu.Unlock()
		if refusal != "" {
			return nil, seatgate.NewCodedError(refusal)
		}
		return ownerwire.EncodeSeatPairResult(ownerwire.SeatPairResult{Words: gateWords})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		seatgate.Serve(ctx, ln, seatgate.ServerConfig{
			TLS:           seatgate.ServerTLS(g.id, []string{seatPin}),
			Caps:          []string{"pair", "read"},
			ShutdownGrace: 100 * time.Millisecond,
		}, seatgate.HandlerFunc(h))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return g
}

// seatKey runs gate-init into a new folder and returns the folder and the
// fingerprint it printed.
func seatKey(t *testing.T) (dir, fp string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "seat-key")
	var out, errout bytes.Buffer
	if code := dispatch(context.Background(), []string{"gate-init", "--identity", dir}, &out, &errout); code != 0 {
		t.Fatalf("gate-init: %d %q", code, errout.String())
	}
	return dir, strings.TrimSpace(out.String())
}

// pinFile writes a pin file holding content with mode perm.
func pinFile(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "gate-pin")
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f, perm); err != nil {
		t.Fatal(err)
	}
	return f
}

// freeLoopback returns a loopback address with a port nothing listens on now.
func freeLoopback(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// watchLink sets the hook that hands over the command's gate client, for the
// length of the test.
func watchLink(t *testing.T) <-chan *seatgate.Client {
	t.Helper()
	ch := make(chan *seatgate.Client, 1)
	gateLinkHook = func(c *seatgate.Client) { ch <- c }
	t.Cleanup(func() { gateLinkHook = nil })
	return ch
}

// waitConnected waits until c has a connection.
func waitConnected(t *testing.T, c *seatgate.Client) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := c.Connected(); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the link did not come up: %v", c.LastError())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// post sends a POST of body to the app at addr as the phone's browser does.
func post(t *testing.T, addr, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "http://"+addr+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = seatRPID
	req.Header.Set("Origin", seatOrigin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// pairPhone pairs a software authenticator through the app at addr with
// gateCode: a challenge, then the registration made for it.
func pairPhone(t *testing.T, addr string) (int, map[string]any) {
	t.Helper()
	status, m := post(t, addr, "/api/pair-challenge", map[string]any{})
	ch, _ := m["challenge"].(string)
	if status != http.StatusOK || ch == "" {
		t.Fatalf("challenge: %d %v", status, m)
	}
	raw, err := base64.RawURLEncoding.DecodeString(ch)
	if err != nil {
		t.Fatal(err)
	}
	a, err := webauthntest.New(seatRPID, seatOrigin)
	if err != nil {
		t.Fatal(err)
	}
	reg := a.Create(raw)
	enc := base64.RawURLEncoding.EncodeToString
	return post(t, addr, "/api/pair", map[string]string{
		"code":               gateCode,
		"challenge":          ch,
		"credential_id":      enc(reg.CredentialID),
		"client_data_json":   enc(reg.ClientDataJSON),
		"authenticator_data": enc(reg.AuthenticatorData),
		"public_key":         enc(reg.PublicKey),
	})
}

// noCodeIn fails the test if out holds the pairing code, in any case.
func noCodeIn(t *testing.T, what, out string) {
	t.Helper()
	if strings.Contains(strings.ToLower(out), strings.ToLower(gateCode)) {
		t.Errorf("%s holds the pairing code: %q", what, out)
	}
}

// With the three flags, a pairing goes over the gate: the gate gets the code,
// the phone gets the words, and the code appears in nothing the command
// prints, from its start to its end.
func TestPairOverTheGate(t *testing.T) {
	dir, fp := seatKey(t)
	g := startGate(t, fp, "")
	links := watchLink(t)
	addr, stderr, stop := serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t),
		"--gate", g.addr, "--gate-pin-file", pinFile(t, g.id.Fingerprint()+"\n", 0o600), "--identity", dir})
	waitConnected(t, <-links)

	status, m := pairPhone(t, addr.String())
	if status != http.StatusOK {
		t.Fatalf("pair: %d %v", status, m)
	}
	got, _ := json.Marshal(m["words"])
	want, _ := json.Marshal(gateWords)
	if string(got) != string(want) {
		t.Errorf("words %s, want %s", got, want)
	}
	g.mu.Lock()
	sent := g.code
	g.mu.Unlock()
	if sent != gateCode {
		t.Errorf("the gate got another code")
	}
	if code := stop(); code != 0 {
		t.Errorf("shutdown code %d", code)
	}
	noCodeIn(t, "stderr", stderr.String())
	if !strings.Contains(stderr.String(), "linking to the seat gate at "+g.addr) {
		t.Errorf("stderr does not say the link: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), fp) {
		t.Errorf("stderr holds the seat's fingerprint: %q", stderr.String())
	}
}

// The daemon's refusal reaches the phone as code_refused, and the code is
// in nothing the command prints.
func TestRefusedCodeOverTheGate(t *testing.T) {
	dir, fp := seatKey(t)
	g := startGate(t, fp, "pairing-refused")
	links := watchLink(t)
	addr, stderr, stop := serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t),
		"--gate", g.addr, "--gate-pin-file", pinFile(t, g.id.Fingerprint(), 0o600), "--identity", dir})
	waitConnected(t, <-links)
	status, m := pairPhone(t, addr.String())
	if status != http.StatusForbidden || m["error"] != "code_refused" {
		t.Errorf("pair: %d %v", status, m)
	}
	stop()
	noCodeIn(t, "stderr", stderr.String())
}

// With a pin that is not the gate's key, the link never comes up and the
// gate's handler never runs: the phone is told the desk is unreachable, not
// that its code was refused.
func TestWrongGatePinNeverConnects(t *testing.T) {
	dir, fp := seatKey(t)
	g := startGate(t, fp, "")
	_, other := seatKey(t)
	links := watchLink(t)
	addr, stderr, stop := serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t),
		"--gate", g.addr, "--gate-pin-file", pinFile(t, other, 0o600), "--identity", dir})
	c := <-links
	deadline := time.Now().Add(10 * time.Second)
	for c.LastError() == nil {
		if time.Now().After(deadline) {
			t.Fatal("no attempt failed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := c.Connected(); ok {
		t.Fatal("connected with a wrong pin")
	}
	status, m := pairPhone(t, addr.String())
	if status != http.StatusServiceUnavailable || m["error"] != "daemon_unavailable" {
		t.Errorf("pair: %d %v", status, m)
	}
	if n := g.calls.Load(); n != 0 {
		t.Errorf("the gate's handler ran %d times", n)
	}
	stop()
	noCodeIn(t, "stderr", stderr.String())
}

// Without the three flags the command keeps the unwired port: no link is
// made and pairing is daemon_unavailable.
func TestNoGateFlagsKeepTheUnwiredPort(t *testing.T) {
	links := watchLink(t)
	addr, stderr, stop := serve(t, []string{"--origin", seatOrigin, "--listen", freeLoopback(t)})
	status, m := pairPhone(t, addr.String())
	if status != http.StatusServiceUnavailable || m["error"] != "daemon_unavailable" {
		t.Errorf("pair: %d %v", status, m)
	}
	stop()
	select {
	case <-links:
		t.Error("a gate link was made without the flags")
	default:
	}
	if strings.Contains(stderr.String(), "gate") {
		t.Errorf("stderr speaks of a gate: %q", stderr.String())
	}
	noCodeIn(t, "stderr", stderr.String())
}

// The gate flags go together, the address must be host:port, and the pin file
// must be a private regular file holding exactly one fingerprint. Each is
// refused before anything listens.
func TestParseGateFlags(t *testing.T) {
	dir, fp := seatKey(t)
	good := pinFile(t, fp+"\n", 0o600)
	base := []string{"--origin", seatOrigin, "--listen", "127.0.0.1:8080"}
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }

	if o, _, ok := parse(with("--gate", "192.0.2.1:4443", "--gate-pin-file", good, "--identity", dir), io.Discard); !ok || o.gatePin != fp {
		t.Errorf("good flags refused, or pin %q", o.gatePin)
	}
	if _, _, ok := parse(with("--gate", "gate.example:4443", "--gate-pin-file", good, "--identity", dir), io.Discard); !ok {
		t.Error("a host name was refused")
	}

	link := filepath.Join(t.TempDir(), "pin-link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]string{
		"gate alone":                {"--gate", "192.0.2.1:4443"},
		"pin file alone":            {"--gate-pin-file", good},
		"identity alone":            {"--identity", dir},
		"no identity":               {"--gate", "192.0.2.1:4443", "--gate-pin-file", good},
		"no pin file":               {"--gate", "192.0.2.1:4443", "--identity", dir},
		"no gate":                   {"--gate-pin-file", good, "--identity", dir},
		"gate without port":         {"--gate", "192.0.2.1", "--gate-pin-file", good, "--identity", dir},
		"gate port zero":            {"--gate", "192.0.2.1:0", "--gate-pin-file", good, "--identity", dir},
		"gate port too big":         {"--gate", "192.0.2.1:65536", "--gate-pin-file", good, "--identity", dir},
		"gate without host":         {"--gate", ":4443", "--gate-pin-file", good, "--identity", dir},
		"gate wildcard":             {"--gate", "0.0.0.0:4443", "--gate-pin-file", good, "--identity", dir},
		"missing pin file":          {"--gate", "192.0.2.1:4443", "--gate-pin-file", "/nonexistent/pin", "--identity", dir},
		"pin file is a link":        {"--gate", "192.0.2.1:4443", "--gate-pin-file", link, "--identity", dir},
		"pin file is a folder":      {"--gate", "192.0.2.1:4443", "--gate-pin-file", dir, "--identity", dir},
		"pin file group-writable":   {"--gate", "192.0.2.1:4443", "--gate-pin-file", pinFile(t, fp, 0o620), "--identity", dir},
		"pin file other-writable":   {"--gate", "192.0.2.1:4443", "--gate-pin-file", pinFile(t, fp, 0o602), "--identity", dir},
		"two fingerprints":          {"--gate", "192.0.2.1:4443", "--gate-pin-file", pinFile(t, fp+"\n"+fp+"\n", 0o600), "--identity", dir},
		"upper-case fingerprint":    {"--gate", "192.0.2.1:4443", "--gate-pin-file", pinFile(t, strings.ToUpper(fp), 0o600), "--identity", dir},
		"short fingerprint":         {"--gate", "192.0.2.1:4443", "--gate-pin-file", pinFile(t, fp[:63], 0o600), "--identity", dir},
		"empty pin file":            {"--gate", "192.0.2.1:4443", "--gate-pin-file", pinFile(t, "", 0o600), "--identity", dir},
		"fingerprint with a prefix": {"--gate", "192.0.2.1:4443", "--gate-pin-file", pinFile(t, "sha256:"+fp, 0o600), "--identity", dir},
		"large pin file":            {"--gate", "192.0.2.1:4443", "--gate-pin-file", pinFile(t, fp+strings.Repeat(" ", maxPinFile), 0o600), "--identity", dir},
	}
	for name, extra := range bad {
		var out bytes.Buffer
		if _, code, ok := parse(with(extra...), &out); ok || code != 2 {
			t.Errorf("%s: accepted (code %d)", name, code)
		}
		if !strings.Contains(out.String(), "Usage: ataniq-seat") {
			t.Errorf("%s: no usage in %q", name, out.String())
		}
		if strings.Contains(out.String(), fp) {
			t.Errorf("%s: the diagnostic holds the file's content: %q", name, out.String())
		}
	}
}

// An identity folder that does not load is refused before anything listens
// or dials.
func TestRunRefusesABadIdentityBeforeListening(t *testing.T) {
	_, fp := seatKey(t)
	empty := t.TempDir()
	links := watchLink(t)
	var out bytes.Buffer
	ready := false
	code := run(context.Background(), []string{"--origin", seatOrigin, "--listen", freeLoopback(t),
		"--gate", "127.0.0.1:9", "--gate-pin-file", pinFile(t, fp, 0o600), "--identity", empty}, &out, func(net.Addr) { ready = true })
	if code != 2 || ready {
		t.Errorf("code %d, ready %v: %q", code, ready, out.String())
	}
	if !strings.Contains(out.String(), "--identity") || strings.Contains(out.String(), empty) {
		t.Errorf("stderr %q", out.String())
	}
	select {
	case <-links:
		t.Error("a link was made")
	default:
	}
}
