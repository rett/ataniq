package seatlink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// testCode is a pairing code of the wire's form; no error and no output may
// hold it.
const testCode = "K7M2Q9XAB3CDEFGH4JKM"

// newIdentity makes a seat gate identity in memory.
func newIdentity(t *testing.T, name string) seatgate.Identity {
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

// gate is a seat gate served by seatgate.Serve on the loopback address, with
// a fake handler.
type gate struct {
	id    seatgate.Identity
	addr  string
	calls atomic.Int64
	stop  func()
}

// serveGate serves h on 127.0.0.1 with a fresh identity, pinning the seat keys
// given. It stops when the test ends.
func serveGate(t *testing.T, pins []string, h seatgate.HandlerFunc) *gate {
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
			Caps:          []string{"pair", "read"},
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

// dial connects a seat client with identity seat, pinning the gate's key, and
// returns the port over it. It closes when the test ends.
func dial(t *testing.T, g *gate, seat seatgate.Identity) (*Daemon, *seatgate.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := seatgate.Dial(ctx, g.addr, seatgate.ClientConfig{TLS: seatgate.ClientTLS(seat, g.id.Fingerprint(), "gate"), Caps: Caps()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return d, c
}

// words are a fingerprint of the wire's form.
func words(n int) []string {
	all := []string{"amber", "basin", "cedar", "delta", "ember", "flint", "grove", "harbor", "inlet", "jade"}
	return append([]string(nil), all[:n]...)
}

// request is a pairing request of the form the app hands over.
func request() seatapp.PairRequest {
	return seatapp.PairRequest{
		Code:         testCode,
		CredentialID: bytes.Repeat([]byte{7}, 32),
		SPKI:         bytes.Repeat([]byte{9}, 91),
		// The link relays the registration as it is; the daemon checks it.
		AuthenticatorData: bytes.Repeat([]byte{5}, 164),
		RPID:              "seat.example.test",
		AAGUID:            [16]byte{1, 2, 3},
		BackupEligible:    true,
		UserVerified:      true,
	}
}

// answer is a handler that answers every call with result.
func answer(result any) seatgate.HandlerFunc {
	return func(context.Context, seatgate.Peer, string, json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(result)
	}
}

// refuse is a handler that refuses every call with code.
func refuse(code string) seatgate.HandlerFunc {
	return func(context.Context, seatgate.Peer, string, json.RawMessage) (json.RawMessage, error) {
		return nil, seatgate.NewCodedError(code)
	}
}

// noCode fails the test if err's text, or its %v and %+v forms, hold the
// pairing code.
func noCode(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if strings.Contains(s, testCode) || strings.Contains(strings.ToLower(s), strings.ToLower(testCode)) {
			t.Errorf("%s: the error holds the pairing code: %q", what, s)
		}
	}
}

// The request goes to the gate as seat.pair with the wire's params, decoded
// by the wire's own decoder, and the words come back in order.
func TestPairRelaysTheRequest(t *testing.T) {
	seat := newIdentity(t, "seat")
	want := request()
	var got ownerwire.SeatPairParams
	var gotOp string
	g := serveGate(t, []string{seat.Fingerprint()}, func(_ context.Context, p seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
		gotOp = op
		var err error
		if got, err = ownerwire.DecodeSeatPairParams(params); err != nil {
			return nil, seatgate.NewCodedError("bad-params")
		}
		return ownerwire.EncodeSeatPairResult(ownerwire.SeatPairResult{Words: words(ownerwire.PairWords)})
	})
	d, _ := dial(t, g, seat)
	res, err := d.Pair(context.Background(), want)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if gotOp != ownerwire.OpSeatPair {
		t.Errorf("op %q", gotOp)
	}
	if got.Code != want.Code || !bytes.Equal(got.CredentialID, want.CredentialID) || !bytes.Equal(got.PublicKey, want.SPKI) ||
		!bytes.Equal(got.AuthenticatorData, want.AuthenticatorData) || got.RPID != want.RPID {
		t.Errorf("the gate got another request")
	}
	for i, w := range words(ownerwire.PairWords) {
		if res.Words[i] != w {
			t.Errorf("word %d: %q, want %q", i, res.Words[i], w)
		}
	}
}

// The codes of the gate map to the errors the app tells apart: only
// pairing-refused is a refused code; every other code, the transport's
// included, is a plain error that is neither of the two, and never says the
// desk is unreachable.
func TestRemoteCodesMap(t *testing.T) {
	seat := newIdentity(t, "seat")
	for _, code := range []string{
		"pairing-refused",
		"credential-invalid", "keys-unavailable", "keys-full", "pairing-none", "pairing-expired",
		"unavailable", "door-unavailable", "journal-reopened", "bad-params", "unknown-op", "not-pinned",
		seatgate.CodeFailed, seatgate.CodeBusy, seatgate.CodeRateLimited, seatgate.CodeDeadline, seatgate.CodeShuttingDown,
	} {
		t.Run(code, func(t *testing.T) {
			g := serveGate(t, []string{seat.Fingerprint()}, refuse(code))
			d, _ := dial(t, g, seat)
			_, err := d.Pair(context.Background(), request())
			noCode(t, code, err)
			if code == "pairing-refused" {
				if !errors.Is(err, seatapp.ErrCodeRefused) {
					t.Errorf("err %v, want ErrCodeRefused", err)
				}
				return
			}
			if err == nil || errors.Is(err, seatapp.ErrCodeRefused) || errors.Is(err, seatapp.ErrDaemonUnavailable) {
				t.Errorf("err %v, want a plain error", err)
			}
			var re *RefusedError
			if !errors.As(err, &re) || re.Code != code {
				t.Errorf("err %v does not carry the code %q", err, code)
			}
		})
	}
}

// With no connection, a call is ErrDaemonUnavailable at once, and the
// pairing is not sent.
func TestNotConnectedIsUnavailable(t *testing.T) {
	seat := newIdentity(t, "seat")
	g := serveGate(t, []string{seat.Fingerprint()}, answer(ownerwire.SeatPairResult{Words: words(ownerwire.PairWords)}))
	c, err := seatgate.NewClient(g.addr, seatgate.ClientConfig{TLS: seatgate.ClientTLS(seat, g.id.Fingerprint(), "gate"), Caps: Caps()})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := New(c)
	_, err = d.Pair(context.Background(), request())
	if !errors.Is(err, seatapp.ErrDaemonUnavailable) {
		t.Errorf("err %v, want ErrDaemonUnavailable", err)
	}
	noCode(t, "not connected", err)
	if n := g.calls.Load(); n != 0 {
		t.Errorf("the gate's handler ran %d times", n)
	}
}

// A connection lost while the call waits is ErrDaemonUnavailable.
func TestLostConnectionIsUnavailable(t *testing.T) {
	seat := newIdentity(t, "seat")
	entered := make(chan struct{})
	var g *gate
	g = serveGate(t, []string{seat.Fingerprint()}, func(ctx context.Context, _ seatgate.Peer, _ string, _ json.RawMessage) (json.RawMessage, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	d, _ := dial(t, g, seat)
	go func() {
		<-entered
		g.stop()
	}()
	_, err := d.Pair(context.Background(), request())
	if !errors.Is(err, seatapp.ErrDaemonUnavailable) {
		t.Errorf("err %v, want ErrDaemonUnavailable", err)
	}
	noCode(t, "lost", err)
}

// A context that ends, before the call or while it waits, is
// ErrDaemonUnavailable; one that ended before is not sent.
func TestContextEndIsUnavailable(t *testing.T) {
	seat := newIdentity(t, "seat")
	block := make(chan struct{})
	defer close(block)
	g := serveGate(t, []string{seat.Fingerprint()}, func(ctx context.Context, _ seatgate.Peer, _ string, _ json.RawMessage) (json.RawMessage, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil, seatgate.NewCodedError("failed")
	})
	d, _ := dial(t, g, seat)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Pair(ctx, request())
	if !errors.Is(err, seatapp.ErrDaemonUnavailable) {
		t.Errorf("cancelled before: err %v", err)
	}
	if n := g.calls.Load(); n != 0 {
		t.Errorf("a call with an ended context was sent (%d)", n)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = d.Pair(ctx, request())
	if !errors.Is(err, seatapp.ErrDaemonUnavailable) {
		t.Errorf("deadline while waiting: err %v", err)
	}
	noCode(t, "deadline", err)
}

// A closed client is a plain error: the seat is going down, not the desk.
func TestClosedClientIsPlain(t *testing.T) {
	seat := newIdentity(t, "seat")
	g := serveGate(t, []string{seat.Fingerprint()}, answer(ownerwire.SeatPairResult{Words: words(ownerwire.PairWords)}))
	d, c := dial(t, g, seat)
	c.Close()
	_, err := d.Pair(context.Background(), request())
	if err == nil || errors.Is(err, seatapp.ErrDaemonUnavailable) || errors.Is(err, seatapp.ErrCodeRefused) {
		t.Errorf("err %v, want a plain error", err)
	}
	if !errors.Is(err, seatgate.ErrClosed) {
		t.Errorf("err %v does not wrap ErrClosed", err)
	}
}

// An answer that is not exactly PairWords words of the fingerprint's grammar,
// or that has a field the result does not name, is a plain error, never
// words.
func TestBadAnswersArePlain(t *testing.T) {
	seat := newIdentity(t, "seat")
	bad := map[string]any{
		"too few words":    map[string]any{"words": words(ownerwire.PairWords - 1)},
		"too many words":   map[string]any{"words": words(ownerwire.PairWords + 1)},
		"six words":        map[string]any{"words": words(6)},
		"no words":         map[string]any{},
		"a word too long":  map[string]any{"words": append(words(ownerwire.PairWords-1), "toolongword")},
		"a capital":        map[string]any{"words": append(words(ownerwire.PairWords-1), "Amber")},
		"an extra field":   map[string]any{"words": words(ownerwire.PairWords), "code": testCode},
		"words not a list": map[string]any{"words": "amber"},
	}
	for name, result := range bad {
		t.Run(name, func(t *testing.T) {
			g := serveGate(t, []string{seat.Fingerprint()}, answer(result))
			d, _ := dial(t, g, seat)
			res, err := d.Pair(context.Background(), request())
			if err == nil || errors.Is(err, seatapp.ErrDaemonUnavailable) || errors.Is(err, seatapp.ErrCodeRefused) {
				t.Errorf("err %v, want a plain error", err)
			}
			if res != (seatapp.PairResult{}) {
				t.Errorf("words came back: %q", res.Words)
			}
			noCode(t, name, err)
		})
	}
}

// A code the wire cannot take as a pairing code is refused here: the daemon
// could not accept it, so it is not sent.
func TestCodeOfAnotherFormIsRefusedUnsent(t *testing.T) {
	seat := newIdentity(t, "seat")
	g := serveGate(t, []string{seat.Fingerprint()}, answer(ownerwire.SeatPairResult{Words: words(ownerwire.PairWords)}))
	d, _ := dial(t, g, seat)
	for _, code := range []string{"K7M2-Q9XA", testCode + "A", strings.Replace(testCode, "K", "I", 1)} {
		r := request()
		r.Code = code
		_, err := d.Pair(context.Background(), r)
		if !errors.Is(err, seatapp.ErrCodeRefused) {
			t.Errorf("%d characters: err %v, want ErrCodeRefused", len(code), err)
		}
		if err != nil && strings.Contains(err.Error(), code) {
			t.Errorf("the error holds the code: %q", err)
		}
	}
	if n := g.calls.Load(); n != 0 {
		t.Errorf("a code of another form was sent (%d)", n)
	}
}

// A registration the wire does not take is not sent, and is not taken for a
// refused code: the phone is not told to scan again for it.
func TestARegistrationOfAnotherFormIsAPlainErrorUnsent(t *testing.T) {
	seat := newIdentity(t, "seat")
	g := serveGate(t, []string{seat.Fingerprint()}, answer(ownerwire.SeatPairResult{Words: words(ownerwire.PairWords)}))
	d, _ := dial(t, g, seat)
	for name, edit := range map[string]func(*seatapp.PairRequest){
		"an address as the relying party": func(r *seatapp.PairRequest) { r.RPID = "::1" },
		"data with no relying party":      func(r *seatapp.PairRequest) { r.RPID = "" },
		"data too long":                   func(r *seatapp.PairRequest) { r.AuthenticatorData = make([]byte, ownerwire.MaxRegistrationDataBytes+1) },
	} {
		r := request()
		edit(&r)
		_, err := d.Pair(context.Background(), r)
		if err == nil || errors.Is(err, seatapp.ErrCodeRefused) || errors.Is(err, seatapp.ErrDaemonUnavailable) {
			t.Errorf("%s: err %v, want a plain error", name, err)
		}
		noCode(t, name, err)
	}
	if n := g.calls.Load(); n != 0 {
		t.Errorf("a registration of another form was sent (%d)", n)
	}
	// A request with no registration at all is sent without one.
	r := request()
	r.AuthenticatorData, r.RPID = nil, ""
	if _, err := d.Pair(context.Background(), r); err != nil {
		t.Fatalf("a request without a registration: %v", err)
	}
}

// A wrong pin never connects, at either end: with a gate key the seat did not
// pin, or a seat key the gate did not pin, the client tries and tries again
// and is never connected, the gate's handler never runs, and a pairing is
// ErrDaemonUnavailable.
func TestWrongPinNeverConnects(t *testing.T) {
	seat := newIdentity(t, "seat")
	other := newIdentity(t, "other")
	cases := map[string]struct {
		gatePins   []string
		seatPinsOf func(g *gate) string
	}{
		"the seat pins another gate key": {[]string{seat.Fingerprint()}, func(*gate) string { return other.Fingerprint() }},
		"the gate pins another seat key": {[]string{other.Fingerprint()}, func(g *gate) string { return g.id.Fingerprint() }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := serveGate(t, tc.gatePins, answer(ownerwire.SeatPairResult{Words: words(ownerwire.PairWords)}))
			attempts := make(chan struct{}, 16)
			c, err := seatgate.NewClient(g.addr, seatgate.ClientConfig{
				TLS:  seatgate.ClientTLS(seat, tc.seatPinsOf(g), "gate"),
				Caps: Caps(),
				Sleep: func(ctx context.Context, _ time.Duration) error {
					select {
					case attempts <- struct{}{}:
					default:
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(10 * time.Millisecond):
						return nil
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			ran := make(chan struct{})
			go func() {
				defer close(ran)
				c.Run(ctx)
			}()
			defer func() {
				cancel()
				<-ran
			}()
			d, _ := New(c)
			for i := 0; i < 3; i++ {
				select {
				case <-attempts:
				case <-time.After(10 * time.Second):
					t.Fatal("the client made no attempt")
				}
				if _, ok := c.Connected(); ok {
					t.Fatal("connected with a wrong pin")
				}
				if _, err := d.Pair(context.Background(), request()); !errors.Is(err, seatapp.ErrDaemonUnavailable) {
					t.Errorf("err %v, want ErrDaemonUnavailable", err)
				}
			}
			if c.LastError() == nil {
				t.Error("no reason is given for the missing connection")
			}
			if n := g.calls.Load(); n != 0 {
				t.Errorf("the gate's handler ran %d times", n)
			}
		})
	}
}

func TestNewNeedsAClient(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Error("New took no client")
	}
}
