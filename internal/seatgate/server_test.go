package seatgate

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// leakCheck counts the goroutines now and, at the end of the test, waits for
// them to come back to that count. It is called first in a test, so the count
// is taken before anything the test starts.
func leakCheck(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			n := runtime.NumGoroutine()
			if n <= before {
				return
			}
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<16)
				buf = buf[:runtime.Stack(buf, true)]
				t.Errorf("goroutines: %d before, %d after\n%s", before, n, buf)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// env is a running server and the identities of both ends.
type env struct {
	t      *testing.T
	addr   string
	srv    Identity
	seat   Identity
	cancel context.CancelFunc
	done   chan error
}

// startServer starts a server on a loopback port. mut changes the
// configuration before it starts; it may be nil.
func startServer(t *testing.T, mut func(*ServerConfig), h Handler) *env {
	t.Helper()
	return startServerAt(t, "127.0.0.1:0", newID(t, "gate"), newID(t, "seat"), mut, h)
}

// startServerAt is startServer on a given address, with given identities, so
// that a server can be started again where one stopped.
func startServerAt(t *testing.T, addr string, srv, seat Identity, mut func(*ServerConfig), h Handler) *env {
	t.Helper()
	return startServerWrapped(t, addr, srv, seat, nil, mut, h)
}

// startServerWrapped is startServerAt with the listener given to wrap first,
// when wrap is not nil, so that a test can hold the server's end of a
// connection.
func startServerWrapped(t *testing.T, addr string, srv, seat Identity, wrap func(net.Listener) net.Listener, mut func(*ServerConfig), h Handler) *env {
	t.Helper()
	e := &env{t: t, srv: srv, seat: seat, done: make(chan error, 1)}
	var ln net.Listener
	var err error
	for i := 0; i < 100; i++ { // a port just closed may take a moment to be free
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	e.addr = ln.Addr().String()
	if wrap != nil {
		ln = wrap(ln)
	}
	cfg := ServerConfig{TLS: ServerTLS(e.srv, []string{e.seat.Fingerprint()}), Caps: []string{"owner.read", "seat.pair"}}
	if mut != nil {
		mut(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go func() { e.done <- Serve(ctx, ln, cfg, h) }()
	t.Cleanup(func() { e.stop() })
	return e
}

// stop shuts the server down and waits for Serve to return.
func (e *env) stop() error {
	e.cancel()
	select {
	case err := <-e.done:
		e.done <- err
		return err
	case <-time.After(10 * time.Second):
		e.t.Error("Serve did not return after its context was cancelled")
		return errors.New("timeout")
	}
}

// rawConn is a test client that speaks frames by hand.
type rawConn struct {
	t *testing.T
	c *tls.Conn
}

// rawDial connects as id, pinning the server's key, and completes the TLS
// handshake. It does not say hello.
func (e *env) rawDial(id Identity) (*rawConn, error) {
	e.t.Helper()
	d := net.Dialer{Timeout: 5 * time.Second}
	nc, err := d.Dial("tcp", e.addr)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(nc, ClientTLS(id, e.srv.Fingerprint(), "gate.example.invalid"))
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tc.Handshake(); err != nil {
		nc.Close()
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	r := &rawConn{e.t, tc}
	e.t.Cleanup(func() { tc.Close() })
	return r, nil
}

func (e *env) raw() *rawConn {
	e.t.Helper()
	r, err := e.rawDial(e.seat)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// helloed is a raw connection that has said hello.
func (e *env) helloed() *rawConn {
	e.t.Helper()
	r := e.raw()
	r.hello()
	return r
}

func (r *rawConn) send(payload string) {
	r.t.Helper()
	r.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := writeFrame(r.c, []byte(payload)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rawConn) sendBytes(b []byte) {
	r.t.Helper()
	r.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := r.c.Write(b); err != nil {
		r.t.Fatal(err)
	}
}

// recv reads one response, waiting up to d.
func (r *rawConn) recvWithin(d time.Duration) (response, error) {
	r.t.Helper()
	r.c.SetReadDeadline(time.Now().Add(d))
	p, err := readFrame(r.c)
	if err != nil {
		return response{}, err
	}
	return decodeResponse(p)
}

func (r *rawConn) recv() response {
	r.t.Helper()
	resp, err := r.recvWithin(5 * time.Second)
	if err != nil {
		r.t.Fatalf("no response: %v", err)
	}
	return resp
}

func (r *rawConn) call(id uint64, op, params string) response {
	r.t.Helper()
	if params == "" {
		r.send(fmt.Sprintf(`{"id":%d,"op":%q}`, id, op))
	} else {
		r.send(fmt.Sprintf(`{"id":%d,"op":%q,"params":%s}`, id, op, params))
	}
	return r.recv()
}

func (r *rawConn) hello() response {
	r.t.Helper()
	resp := r.call(1, OpHello, `{"proto":1,"caps":["owner.read"],"role":"seat"}`)
	if resp.Code != "" {
		r.t.Fatalf("hello refused: %s", resp.Code)
	}
	return resp
}

// expectClosed reads until the connection ends and fails if it does not within
// d. Responses that were already on the way are drained and returned.
func (r *rawConn) expectClosed(d time.Duration) []response {
	r.t.Helper()
	var got []response
	deadline := time.Now().Add(d)
	for {
		r.c.SetReadDeadline(deadline)
		p, err := readFrame(r.c)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				r.t.Fatalf("the connection was not closed within %v", d)
			}
			return got
		}
		if resp, err := decodeResponse(p); err == nil {
			got = append(got, resp)
		}
	}
}

// echo is a handler that answers {"op":..., "peer":...} and the params it
// was given.
func echo() Handler {
	return HandlerFunc(func(_ context.Context, p Peer, op string, params json.RawMessage) (json.RawMessage, error) {
		b, _ := json.Marshal(map[string]any{"op": op, "peer": p.Fingerprint, "params": params})
		return b, nil
	})
}

func TestServerHello(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, echo())
	r := e.raw()
	resp := r.hello()
	h, err := decodeHelloResult(resp.Result)
	if err != nil || h.Proto != 1 || strings.Join(h.Caps, ",") != "owner.read,seat.pair" {
		t.Fatalf("%s %v", resp.Result, err)
	}
	if resp := r.call(2, OpPing, ""); resp.Code != "" || resp.ID != 2 {
		t.Fatalf("ping: %+v", resp)
	}
}

func TestServerCapsOfNoneAnswerAnEmptyList(t *testing.T) {
	e := startServer(t, func(c *ServerConfig) { c.Caps = nil }, echo())
	resp := e.raw().hello()
	if string(resp.Result) != `{"proto":1,"caps":[]}` {
		t.Fatalf("%s", resp.Result)
	}
}

func TestServerRefusesAHelloOfAnotherMajorVersion(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, echo())
	r := e.raw()
	resp := r.call(1, OpHello, `{"proto":2,"caps":[],"role":"seat"}`)
	if resp.Code != CodeProtoUnsupported {
		t.Fatalf("%+v", resp)
	}
	r.expectClosed(3 * time.Second)
}

func TestServerRefusesABadHello(t *testing.T) {
	e := startServer(t, nil, echo())
	for name, params := range map[string]string{
		"wrong role":     `{"proto":1,"caps":[],"role":"gate"}`,
		"missing caps":   `{"proto":1,"role":"seat"}`,
		"unknown field":  `{"proto":1,"caps":[],"role":"seat","x":1}`,
		"capability bad": `{"proto":1,"caps":["Bad Cap"],"role":"seat"}`,
		"none":           ``,
	} {
		r := e.raw()
		resp := r.call(1, OpHello, params)
		if resp.Code != CodeBadHello {
			t.Errorf("%s: %+v", name, resp)
		}
		r.expectClosed(3 * time.Second)
	}
}

// A repeated key is a violation of the frame itself, so the connection is
// closed with no answer, hello or not.
func TestServerClosesOnARepeatedKeyInAHello(t *testing.T) {
	e := startServer(t, nil, echo())
	r := e.raw()
	r.send(`{"id":1,"op":"seat.hello","params":{"proto":1,"proto":1,"caps":[],"role":"seat"}}`)
	if got := r.expectClosed(3 * time.Second); len(got) != 0 {
		t.Fatalf("answered %+v", got)
	}
}

func TestServerServesNothingBeforeHello(t *testing.T) {
	leakCheck(t)
	called := make(chan string, 4)
	e := startServer(t, nil, HandlerFunc(func(_ context.Context, _ Peer, op string, _ json.RawMessage) (json.RawMessage, error) {
		called <- op
		return nil, nil
	}))
	for _, op := range []string{"owner.list", OpPing, "seat.challenge"} {
		r := e.raw()
		resp := r.call(1, op, "")
		if resp.Code != CodeHelloRequired {
			t.Errorf("%s before hello: %+v", op, resp)
		}
		r.expectClosed(3 * time.Second)
	}
	select {
	case op := <-called:
		t.Fatalf("the handler was called for %s before a hello", op)
	default:
	}
}

func TestServerRefusesASecondHello(t *testing.T) {
	e := startServer(t, nil, echo())
	r := e.helloed()
	resp := r.call(2, OpHello, `{"proto":1,"caps":[],"role":"seat"}`)
	if resp.Code != CodeHelloRepeated {
		t.Fatalf("%+v", resp)
	}
	if resp := r.call(3, OpPing, ""); resp.Code != "" {
		t.Fatalf("the connection did not stay: %+v", resp)
	}
}

func TestServerPassesThePeerOperationAndParams(t *testing.T) {
	e := startServer(t, nil, echo())
	r := e.helloed()
	resp := r.call(2, "owner.show", `{"id":"p-1"}`)
	var got struct {
		Op     string          `json:"op"`
		Peer   string          `json:"peer"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("%s %v", resp.Result, err)
	}
	if got.Op != "owner.show" || got.Peer != e.seat.Fingerprint() || string(got.Params) != `{"id":"p-1"}` {
		t.Fatalf("%+v", got)
	}
	resp = r.call(3, "owner.list", "")
	json.Unmarshal(resp.Result, &got)
	if string(got.Params) != "null" {
		t.Fatalf("params for a request with none: %s", got.Params)
	}
}

type testCoded struct{ code string }

func (e testCoded) Error() string { return "SECRET text of a coded error" }
func (e testCoded) Code() string  { return e.code }

type panicCode struct{}

func (panicCode) Error() string { return "x" }
func (panicCode) Code() string  { panic("SECRET in Code") }

func TestServerMapsHandlerErrorsToCodes(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, HandlerFunc(func(_ context.Context, _ Peer, op string, _ json.RawMessage) (json.RawMessage, error) {
		switch op {
		case "t.coded":
			return nil, testCoded{"no-such-card"}
		case "t.plain":
			return nil, errors.New("SECRET path /srv/example/key")
		case "t.wrapped":
			return nil, fmt.Errorf("SECRET context: %w", NewCodedError("card-expired"))
		case "t.badcode":
			return nil, testCoded{"Not A Code"}
		case "t.panicinside":
			return nil, panicCode{}
		case "t.panic":
			panic("SECRET panic text")
		case "t.notobject":
			return json.RawMessage(`[1]`), nil
		case "t.invalid":
			return json.RawMessage(`{`), nil
		case "t.huge":
			return json.RawMessage(`{"x":"` + strings.Repeat("a", MaxFrame) + `"}`), nil
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))
	r := e.helloed()
	for i, c := range []struct{ op, code string }{
		{"t.coded", "no-such-card"},
		{"t.plain", CodeFailed},
		{"t.wrapped", "card-expired"},
		{"t.badcode", CodeFailed},
		{"t.panicinside", CodeFailed},
		{"t.panic", CodeFailed},
		{"t.notobject", CodeFailed},
		{"t.invalid", CodeFailed},
		{"t.huge", CodeTooLarge},
		{"t.fine", ""},
	} {
		r.send(fmt.Sprintf(`{"id":%d,"op":%q}`, i+2, c.op))
		r.c.SetReadDeadline(time.Now().Add(5 * time.Second))
		p, err := readFrame(r.c)
		if err != nil {
			t.Fatalf("%s: %v", c.op, err)
		}
		if strings.Contains(string(p), "SECRET") || strings.Contains(string(p), "/srv") {
			t.Errorf("%s: text leaked: %s", c.op, p)
		}
		resp, err := decodeResponse(p)
		if err != nil || resp.Code != c.code || resp.ID != uint64(i+2) {
			t.Errorf("%s: %s %v, want code %q", c.op, p, err, c.code)
		}
	}
}

// Whatever a peer sends that breaks the framing or the strict form, the
// connection is closed, and the server goes on serving others.
func TestServerClosesOnAViolation(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, echo())
	big := make([]byte, 4)
	binary.BigEndian.PutUint32(big, MaxFrame+1)
	cases := map[string][]byte{
		"oversize length":  big,
		"huge length":      {0xff, 0xff, 0xff, 0xff},
		"zero length":      {0, 0, 0, 0},
		"invalid json":     append([]byte{0, 0, 0, 5}, "{oops"...),
		"not an object":    append([]byte{0, 0, 0, 2}, "[]"...),
		"duplicate keys":   framedBytes(`{"id":2,"id":3,"op":"owner.list"}`),
		"unknown field":    framedBytes(`{"id":2,"op":"owner.list","x":1}`),
		"trailing bytes":   framedBytes(`{"id":2,"op":"owner.list"}x`),
		"wrong op grammar": framedBytes(`{"id":2,"op":"Owner.List"}`),
		"op without dot":   framedBytes(`{"id":2,"op":"list"}`),
		"id zero":          framedBytes(`{"id":0,"op":"owner.list"}`),
		"id as string":     framedBytes(`{"id":"2","op":"owner.list"}`),
		"params array":     framedBytes(`{"id":2,"op":"owner.list","params":[]}`),
		"a response":       framedBytes(`{"id":2,"result":{}}`),
	}
	for name, b := range cases {
		r := e.helloed()
		r.sendBytes(b)
		got := r.expectClosed(3 * time.Second)
		if len(got) != 0 {
			t.Errorf("%s: answered %+v", name, got)
		}
	}
	// A truncated frame: half a frame and then the end of the stream.
	r := e.helloed()
	r.sendBytes(framedBytes(`{"id":2,"op":"owner.list"}`)[:10])
	r.c.CloseWrite()
	r.expectClosed(3 * time.Second)
	// And the server still serves.
	if resp := e.helloed().call(2, "owner.list", ""); resp.Code != "" {
		t.Fatalf("%+v", resp)
	}
}

func framedBytes(s string) []byte { return framed(s) }

func TestServerRefusesAnUnpinnedClient(t *testing.T) {
	leakCheck(t)
	called := make(chan struct{}, 1)
	e := startServer(t, nil, HandlerFunc(func(context.Context, Peer, string, json.RawMessage) (json.RawMessage, error) {
		called <- struct{}{}
		return nil, nil
	}))
	stranger := newID(t, "seat")
	r, err := e.rawDial(stranger)
	if err == nil {
		// TLS 1.3: the client's handshake ends before the server's verdict.
		r.send(`{"id":1,"op":"seat.hello","params":{"proto":1,"caps":[],"role":"seat"}}`)
		_, err = r.recvWithin(3 * time.Second)
	}
	if err == nil {
		t.Fatal("an unpinned client was served")
	}
	select {
	case <-called:
		t.Fatal("the handler was called")
	default:
	}
}

func TestServerIdReuse(t *testing.T) {
	leakCheck(t)
	release := make(chan struct{})
	e := startServer(t, nil, HandlerFunc(func(ctx context.Context, _ Peer, op string, _ json.RawMessage) (json.RawMessage, error) {
		if op == "t.block" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return nil, nil
	}))
	t.Run("while in flight the connection is closed", func(t *testing.T) {
		r := e.helloed()
		r.send(`{"id":7,"op":"t.block"}`)
		r.send(`{"id":7,"op":"t.block"}`)
		r.expectClosed(3 * time.Second)
	})
	t.Run("a ping may not take the id of a request in flight", func(t *testing.T) {
		r := e.helloed()
		r.send(`{"id":7,"op":"t.block"}`)
		r.send(`{"id":7,"op":"seat.ping"}`)
		r.expectClosed(3 * time.Second)
	})
	t.Run("after the answer the id is free again", func(t *testing.T) {
		r := e.helloed()
		for i := 0; i < 5; i++ {
			if resp := r.call(9, "t.quick", ""); resp.Code != "" || resp.ID != 9 {
				t.Fatalf("round %d: %+v", i, resp)
			}
		}
	})
	close(release)
}

func TestServerRateLimit(t *testing.T) {
	leakCheck(t)
	var mu sync.Mutex
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	called := 0
	e := startServer(t, func(c *ServerConfig) { c.RateMax, c.RatePer, c.Now = 5, time.Minute, clock }, HandlerFunc(
		func(context.Context, Peer, string, json.RawMessage) (json.RawMessage, error) {
			mu.Lock()
			called++
			mu.Unlock()
			return nil, nil
		}))
	r := e.helloed()
	limited := 0
	for i := 0; i < 20; i++ {
		resp := r.call(uint64(i+2), "owner.list", "")
		if resp.Code == CodeRateLimited {
			limited++
		} else if resp.Code != "" {
			t.Fatalf("%+v", resp)
		}
	}
	mu.Lock()
	if limited != 15 || called != 5 {
		t.Fatalf("%d limited and %d served, want 15 and 5", limited, called)
	}
	now = now.Add(61 * time.Second)
	mu.Unlock()
	if resp := r.call(100, "owner.list", ""); resp.Code != "" {
		t.Fatalf("after the window: %+v", resp)
	}
	// A flood on the wire, not one at a time: still bounded.
	for i := 0; i < 50; i++ {
		r.send(fmt.Sprintf(`{"id":%d,"op":"owner.list"}`, 200+i))
	}
	ok, lim := 0, 0
	for i := 0; i < 50; i++ {
		if resp := r.recv(); resp.Code == CodeRateLimited {
			lim++
		} else {
			ok++
		}
	}
	if ok != 4 || lim != 46 { // 1 already used in this window, so 4 more
		t.Fatalf("flood: %d served and %d limited, want 4 and 46", ok, lim)
	}
}

func TestServerDefaultRateIs60PerMinute(t *testing.T) {
	e := startServer(t, nil, echo())
	r := e.helloed()
	limited := 0
	for i := 0; i < 70; i++ {
		r.send(fmt.Sprintf(`{"id":%d,"op":"seat.ping"}`, i+2))
	}
	for i := 0; i < 70; i++ {
		if r.recv().Code == CodeRateLimited {
			limited++
		}
	}
	if limited != 10 {
		t.Fatalf("%d limited, want 10", limited)
	}
}

func TestServerInFlightLimit(t *testing.T) {
	leakCheck(t)
	release := make(chan struct{})
	e := startServer(t, func(c *ServerConfig) { c.MaxInFlight = 3 }, HandlerFunc(func(ctx context.Context, _ Peer, _ string, _ json.RawMessage) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, nil
	}))
	r := e.helloed()
	for i := 0; i < 5; i++ {
		r.send(fmt.Sprintf(`{"id":%d,"op":"t.block"}`, i+2))
	}
	for i := 0; i < 2; i++ {
		resp := r.recv()
		if resp.Code != CodeBusy {
			t.Fatalf("%+v", resp)
		}
	}
	close(release)
	for i := 0; i < 3; i++ {
		if resp := r.recv(); resp.Code != "" {
			t.Fatalf("%+v", resp)
		}
	}
}

func TestServerSlowlorisAndIdle(t *testing.T) {
	leakCheck(t)
	e := startServer(t, func(c *ServerConfig) {
		c.FrameTimeout = 200 * time.Millisecond
		c.IdleTimeout = 600 * time.Millisecond
		c.HelloTimeout = 300 * time.Millisecond
		c.HandshakeTimeout = 300 * time.Millisecond
	}, echo())
	t.Run("half a frame", func(t *testing.T) {
		r := e.helloed()
		r.sendBytes([]byte{0, 0, 0, 30, '{', '"', 'i'})
		start := time.Now()
		r.expectClosed(5 * time.Second)
		if time.Since(start) > 2*time.Second {
			t.Fatalf("closed after %v", time.Since(start))
		}
	})
	t.Run("half a header", func(t *testing.T) {
		r := e.helloed()
		r.sendBytes([]byte{0, 0})
		r.expectClosed(5 * time.Second)
	})
	t.Run("a trickle that never finishes", func(t *testing.T) {
		r := e.helloed()
		start := time.Now()
		r.sendBytes([]byte{0, 0, 0, 40})
		stop := make(chan struct{})
		go func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				case <-time.After(50 * time.Millisecond):
					r.c.SetWriteDeadline(time.Now().Add(time.Second))
					if _, err := r.c.Write([]byte{'x'}); err != nil {
						return
					}
				}
			}
		}()
		r.expectClosed(5 * time.Second)
		close(stop)
		if time.Since(start) > 2*time.Second {
			t.Fatalf("closed after %v", time.Since(start))
		}
	})
	t.Run("no hello", func(t *testing.T) {
		r := e.raw()
		start := time.Now()
		r.expectClosed(5 * time.Second)
		if time.Since(start) > 2*time.Second {
			t.Fatalf("closed after %v", time.Since(start))
		}
	})
	t.Run("silence after the hello", func(t *testing.T) {
		r := e.helloed()
		start := time.Now()
		r.expectClosed(5 * time.Second)
		if d := time.Since(start); d < 400*time.Millisecond || d > 3*time.Second {
			t.Fatalf("closed after %v, want about 600ms", d)
		}
	})
	t.Run("a connection kept alive by pings stays", func(t *testing.T) {
		r := e.helloed()
		for i := 0; i < 8; i++ {
			time.Sleep(200 * time.Millisecond)
			if resp := r.call(uint64(i+2), OpPing, ""); resp.Code != "" {
				t.Fatalf("%+v", resp)
			}
		}
	})
	t.Run("a connection that never handshakes", func(t *testing.T) {
		c, err := net.Dial("tcp", e.addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		start := time.Now()
		if _, err := c.Read(make([]byte, 1)); err == nil || time.Since(start) > 3*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
}

func TestServerRefusesTheNinthConnection(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, echo())
	var held []*rawConn
	for i := 0; i < 8; i++ {
		held = append(held, e.helloed())
	}
	// The ninth is closed as it arrives: the TLS handshake does not complete,
	// or no hello is answered.
	if r, err := e.rawDial(e.seat); err == nil {
		r.send(`{"id":1,"op":"seat.hello","params":{"proto":1,"caps":[],"role":"seat"}}`)
		if _, err := r.recvWithin(3 * time.Second); err == nil {
			t.Fatal("a ninth connection was served")
		}
	}
	// The first eight still work, and when one leaves a place is free.
	for i, r := range held {
		if resp := r.call(uint64(i+10), OpPing, ""); resp.Code != "" {
			t.Fatalf("%+v", resp)
		}
	}
	held[0].c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := e.rawDial(e.seat)
		if err == nil {
			r.send(`{"id":1,"op":"seat.hello","params":{"proto":1,"caps":[],"role":"seat"}}`)
			if resp, err := r.recvWithin(2 * time.Second); err == nil && resp.Code == "" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no place was freed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestServerRequestDeadline(t *testing.T) {
	leakCheck(t)
	release := make(chan struct{})
	e := startServer(t, func(c *ServerConfig) { c.RequestTimeout = 150 * time.Millisecond; c.MaxInFlight = 2 }, HandlerFunc(
		func(ctx context.Context, _ Peer, op string, _ json.RawMessage) (json.RawMessage, error) {
			switch op {
			case "t.honours":
				<-ctx.Done()
				return nil, ctx.Err()
			case "t.ignores":
				<-release
			}
			return nil, nil
		}))
	r := e.helloed()
	start := time.Now()
	if resp := r.call(2, "t.honours", ""); resp.Code != CodeDeadline || time.Since(start) > 3*time.Second {
		t.Fatalf("%+v after %v", resp, time.Since(start))
	}
	// A handler that ignores its context is answered at the deadline too,
	// and keeps its slot until it returns.
	if resp := r.call(3, "t.ignores", ""); resp.Code != CodeDeadline {
		t.Fatalf("%+v", resp)
	}
	if resp := r.call(4, "t.ignores", ""); resp.Code != CodeDeadline {
		t.Fatalf("%+v", resp)
	}
	if resp := r.call(5, "t.quick", ""); resp.Code != CodeBusy {
		t.Fatalf("both slots are held by handlers that did not return: %+v", resp)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := r.call(6, "t.quick", "")
		if resp.Code == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the slots were not freed: %+v", resp)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An id has one answer pending at a time, which is not one handler at a time:
// the answer deadline-exceeded frees the id while a handler that ignores its
// context still runs, and a request that reuses the id is served then (the
// wording of the package documentation).
func TestServerIdIsFreeAtTheDeadlineAnswerWhileItsHandlerStillRuns(t *testing.T) {
	leakCheck(t)
	release := make(chan struct{})
	var running atomic.Int32
	var overlap atomic.Bool
	e := startServer(t, func(c *ServerConfig) { c.RequestTimeout = 100 * time.Millisecond }, HandlerFunc(
		func(ctx context.Context, _ Peer, op string, _ json.RawMessage) (json.RawMessage, error) {
			if op == "t.ignores" {
				running.Add(1)
				defer running.Add(-1)
				<-release
				return nil, nil
			}
			overlap.Store(running.Load() > 0)
			return json.RawMessage(`{"ok":true}`), nil
		}))
	defer close(release)
	r := e.helloed()
	if resp := r.call(2, "t.ignores", ""); resp.Code != CodeDeadline {
		t.Fatalf("%+v", resp)
	}
	if resp := r.call(2, "t.quick", ""); resp.Code != "" || string(resp.Result) != `{"ok":true}` {
		t.Fatalf("the id was not free: %+v", resp)
	}
	if !overlap.Load() {
		t.Fatal("the first handler had returned: the test did not test the overlap")
	}
}

func TestServerConcurrentRequestsOnOneConnection(t *testing.T) {
	leakCheck(t)
	e := startServer(t, func(c *ServerConfig) { c.RateMax = 1000 }, HandlerFunc(
		func(ctx context.Context, _ Peer, _ string, params json.RawMessage) (json.RawMessage, error) {
			var p struct {
				N int `json:"n"`
			}
			if err := DecodeStrict(params, &p); err != nil {
				return nil, err
			}
			time.Sleep(time.Duration(10-p.N%10) * 3 * time.Millisecond) // later ones finish first
			return json.RawMessage(fmt.Sprintf(`{"n":%d}`, p.N)), nil
		}))
	r := e.helloed()
	const total = 200
	var wg sync.WaitGroup
	wg.Add(1)
	got := map[uint64]int{}
	go func() {
		defer wg.Done()
		for len(got) < total {
			resp := r.recv()
			var res struct {
				N int `json:"n"`
			}
			if resp.Code == CodeBusy {
				got[resp.ID] = -1
				continue
			}
			if err := json.Unmarshal(resp.Result, &res); err != nil {
				t.Errorf("%v", err)
				return
			}
			got[resp.ID] = res.N
		}
	}()
	for i := 0; i < total; i++ {
		r.send(fmt.Sprintf(`{"id":%d,"op":"t.n","params":{"n":%d}}`, i+2, i))
	}
	wg.Wait()
	ok := 0
	for id, n := range got {
		if n == -1 {
			continue
		}
		ok++
		if uint64(n+2) != id {
			t.Fatalf("id %d got the result of request %d", id, n)
		}
	}
	if ok == 0 {
		t.Fatal("nothing was served")
	}
}

func TestServerConcurrentConnections(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, echo())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.rawDial(e.seat)
			if err != nil {
				t.Error(err)
				return
			}
			defer r.c.Close()
			r.hello()
			for j := 0; j < 10; j++ {
				if resp := r.call(uint64(j+2), "owner.list", ""); resp.Code != "" {
					t.Errorf("%+v", resp)
				}
			}
		}()
	}
	wg.Wait()
}

func TestServerShutdown(t *testing.T) {
	t.Run("lets a request in flight finish", func(t *testing.T) {
		leakCheck(t)
		started, release := make(chan struct{}), make(chan struct{})
		e := startServer(t, nil, HandlerFunc(func(ctx context.Context, _ Peer, _ string, _ json.RawMessage) (json.RawMessage, error) {
			close(started)
			select {
			case <-release:
				return json.RawMessage(`{"done":true}`), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}))
		r := e.helloed()
		r.send(`{"id":2,"op":"t.slow"}`)
		within(t, "the handler to start", started, 5*time.Second)
		e.cancel()
		time.Sleep(100 * time.Millisecond)
		close(release)
		resp := r.recv()
		if resp.Code != "" || string(resp.Result) != `{"done":true}` {
			t.Fatalf("%+v", resp)
		}
		r.expectClosed(3 * time.Second)
		if err := e.stop(); err != nil {
			t.Fatal(err)
		}
		// Nobody can connect any more.
		if c, err := net.DialTimeout("tcp", e.addr, time.Second); err == nil {
			c.Close()
			t.Fatal("the listener is still open")
		}
	})
	t.Run("cuts a request that outlasts the grace", func(t *testing.T) {
		leakCheck(t)
		started := make(chan struct{})
		e := startServer(t, func(c *ServerConfig) { c.ShutdownGrace = 200 * time.Millisecond }, HandlerFunc(
			func(ctx context.Context, _ Peer, _ string, _ json.RawMessage) (json.RawMessage, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}))
		r := e.helloed()
		r.send(`{"id":2,"op":"t.slow"}`)
		within(t, "the handler to start", started, 5*time.Second)
		start := time.Now()
		e.cancel()
		r.expectClosed(5 * time.Second)
		if err := e.stop(); err != nil || time.Since(start) > 4*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
	t.Run("closes idle connections and returns nil", func(t *testing.T) {
		leakCheck(t)
		e := startServer(t, nil, echo())
		a, b := e.helloed(), e.raw()
		if err := e.stop(); err != nil {
			t.Fatal(err)
		}
		a.expectClosed(3 * time.Second)
		b.expectClosed(3 * time.Second)
	})
	t.Run("a handshake in progress does not hold it up", func(t *testing.T) {
		leakCheck(t)
		e := startServer(t, func(c *ServerConfig) { c.HandshakeTimeout = time.Minute }, echo())
		c, err := net.Dial("tcp", e.addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		time.Sleep(100 * time.Millisecond)
		start := time.Now()
		if err := e.stop(); err != nil || time.Since(start) > 3*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
}

func TestServeRefusesABadConfiguration(t *testing.T) {
	ln := func() net.Listener {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	id := newID(t, "x")
	if err := Serve(context.Background(), ln(), ServerConfig{}, echo()); err == nil {
		t.Error("no TLS configuration")
	}
	if err := Serve(context.Background(), ln(), ServerConfig{TLS: ServerTLS(id, nil)}, nil); err == nil {
		t.Error("no handler")
	}
	if err := Serve(context.Background(), ln(), ServerConfig{TLS: ServerTLS(id, nil), Caps: []string{"A"}}, echo()); err == nil {
		t.Error("a bad capability")
	}
}

type failingListener struct{ net.Listener }

func (failingListener) Accept() (net.Conn, error) { return nil, errors.New("boom") }

// scriptedListener fails each Accept with the next error of its script; a nil
// entry, and every Accept after the script, accepts on the listener it wraps.
type scriptedListener struct {
	net.Listener
	mu     sync.Mutex
	script []error
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	var err error
	if len(l.script) > 0 {
		err, l.script = l.script[0], l.script[1:]
	}
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return l.Listener.Accept()
}

// A listener that fails, as one out of descriptors does, is retried with a
// backoff that starts again after each success, and the server goes on
// serving; the first failure of each streak calls AcceptFailed once.
func TestServeRetriesAFailingListener(t *testing.T) {
	leakCheck(t)
	boom := errors.New("boom")
	var mu sync.Mutex
	var waits []time.Duration
	var failed atomic.Int32
	e := startServerWrapped(t, "127.0.0.1:0", newID(t, "gate"), newID(t, "seat"),
		func(ln net.Listener) net.Listener {
			return &scriptedListener{Listener: ln, script: []error{boom, boom, boom, nil, boom, boom}}
		},
		func(c *ServerConfig) {
			c.AcceptFailed = func() { failed.Add(1) }
			c.acceptWait = func(d time.Duration) time.Duration {
				mu.Lock()
				waits = append(waits, d)
				mu.Unlock()
				return d
			}
		}, echo())
	for i := uint64(1); i <= 2; i++ {
		r := e.helloed()
		if resp := r.call(i, "seat.ping", ""); resp.ID != i || resp.Code != "" {
			t.Fatalf("connection %d: %+v", i, resp)
		}
	}
	mu.Lock()
	got := fmt.Sprint(waits)
	mu.Unlock()
	if want := fmt.Sprint([]time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond}); got != want {
		t.Errorf("waits %s, want %s", got, want)
	}
	if n := failed.Load(); n != 2 {
		t.Errorf("AcceptFailed called %d times, want 2", n)
	}
	if err := e.stop(); err != nil {
		t.Errorf("Serve returned %v", err)
	}
}

// The backoff doubles to a ceiling of one second.
func TestServeAcceptBackoffStopsAtOneSecond(t *testing.T) {
	leakCheck(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	cfg := ServerConfig{TLS: ServerTLS(newID(t, "x"), nil)}
	cfg.acceptWait = func(d time.Duration) time.Duration {
		waits = append(waits, d) // the accept loop's goroutine only
		if len(waits) == 10 {
			cancel()
		}
		return 0
	}
	if err := Serve(ctx, failingListener{l}, cfg, echo()); err != nil {
		t.Fatalf("Serve returned %v", err)
	}
	want := []time.Duration{5, 10, 20, 40, 80, 160, 320, 640, 1000, 1000}
	for i := range want {
		want[i] *= time.Millisecond
	}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Errorf("waits %v, want %v", waits, want)
	}
}

// A cancel while Serve waits to accept again ends the wait at once.
func TestServeCancelDuringTheAcceptBackoffReturnsAtOnce(t *testing.T) {
	leakCheck(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	waiting := make(chan struct{}, 1)
	cfg := ServerConfig{TLS: ServerTLS(newID(t, "x"), nil)}
	cfg.acceptWait = func(time.Duration) time.Duration {
		select {
		case waiting <- struct{}{}:
		default:
		}
		return time.Hour
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, failingListener{l}, cfg, echo()) }()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not wait after a failed accept")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return when its context was cancelled during the backoff")
	}
}

// A mixed load of good and bad connections leaves no goroutine behind.
func TestServerLeaksNoGoroutines(t *testing.T) {
	leakCheck(t)
	e := startServer(t, func(c *ServerConfig) { c.FrameTimeout = 200 * time.Millisecond }, echo())
	for i := 0; i < 20; i++ {
		r := e.helloed()
		switch i % 4 {
		case 0:
			r.call(2, "owner.list", "")
		case 1:
			r.sendBytes([]byte{0, 0, 0, 0})
		case 2:
			r.sendBytes([]byte{0, 0, 0, 9, '{'})
		case 3:
			r.c.Close()
		}
		r.c.Close()
	}
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", e.addr)
		if err == nil {
			c.Close()
		}
	}
	if err := e.stop(); err != nil {
		t.Fatal(err)
	}
}

// A handler that ignores its context outlives its connection, and keeps a
// place until it returns: a peer that reconnects cannot start more handlers
// than the server's ceiling, MaxConns × MaxInFlight by default, however many
// connections it goes through. Each connection used to count its handlers
// afresh, so a flapping peer could pile them up without end.
func TestServerHandlersHaveAServerWideCeiling(t *testing.T) {
	leakCheck(t)
	release := make(chan struct{})
	started := make(chan struct{}, 100)
	connGone := make(chan struct{}, 100)
	var mu sync.Mutex
	running, most := 0, 0
	e := startServer(t, func(c *ServerConfig) { c.MaxConns, c.MaxInFlight = 3, 1 }, HandlerFunc(
		func(ctx context.Context, _ Peer, op string, _ json.RawMessage) (json.RawMessage, error) {
			if op != "t.ignores" {
				return nil, nil
			}
			mu.Lock()
			running++
			most = max(most, running)
			mu.Unlock()
			defer func() { mu.Lock(); running--; mu.Unlock() }()
			started <- struct{}{}
			<-ctx.Done() // the connection has gone
			connGone <- struct{}{}
			<-release // and the handler still has not returned
			return nil, nil
		}))
	var freeOnce sync.Once
	free := func() { freeOnce.Do(func() { close(release) }) }
	t.Cleanup(free) // a failing test frees its handlers too
	served, busy := 0, 0
	for i := 0; i < 6; i++ {
		r := e.helloed()
		// The loop takes frames in order: the ping is answered after the
		// request is refused or its handler started.
		r.send(`{"id":2,"op":"t.ignores"}`)
		r.send(`{"id":3,"op":"seat.ping"}`)
		switch resp := r.recv(); {
		case resp.ID == 2 && resp.Code == CodeBusy:
			busy++
			if resp := r.recv(); resp.ID != 3 || resp.Code != "" {
				t.Fatalf("connection %d: %+v", i, resp)
			}
			r.c.Close()
		case resp.ID == 3 && resp.Code == "":
			served++
			within(t, "the handler to start", started, 5*time.Second)
			r.c.Close()
			// The handler sees its connection end, so the connection's
			// place is (about to be) free; the handler goes on.
			within(t, "the connection to end", connGone, 5*time.Second)
		default:
			t.Fatalf("connection %d: %+v", i, resp)
		}
	}
	mu.Lock()
	m := most
	mu.Unlock()
	if m > 3 || served != 3 || busy != 3 {
		t.Fatalf("%d handlers ran at once, %d served and %d busy; want at most 3, 3 and 3", m, served, busy)
	}
	free()
	untilTrue(t, "the handlers to return", func() bool { mu.Lock(); defer mu.Unlock(); return running == 0 })
	// Their places are free again.
	r := e.helloed()
	if resp := r.call(2, "t.quick", ""); resp.Code != "" {
		t.Fatalf("after the handlers returned: %+v", resp)
	}
}

// A shutdown keeps its grace even when a connection's loop is stuck writing
// to a peer that has stopped reading. The loop writes the answers it serves
// itself (a ping, busy, rate-limited), and used to see the drain only once such
// a write ended, at WriteTimeout, so Serve could outlast the grace by that.
// A request that the connection's loop takes once the shutdown has begun is
// answered shutting-down and not started: the peer learns that nothing of it
// ran, where a cut connection would leave it unable to tell. A request already
// in flight is still given the grace.
func TestServerShutdownAnswersALateRequestWithoutRunningIt(t *testing.T) {
	leakCheck(t)
	var ran atomic.Int32
	inFlight, release, taken := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e := startServer(t, func(c *ServerConfig) {
		c.frameHook = func(op string, drain <-chan struct{}) {
			if op == "t.late" {
				close(taken)
				<-drain // the loop takes this frame only once the shutdown has begun
			}
		}
	}, HandlerFunc(func(ctx context.Context, _ Peer, op string, _ json.RawMessage) (json.RawMessage, error) {
		ran.Add(1)
		if op == "t.slow" {
			close(inFlight)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return json.RawMessage(`{"op":"` + op + `"}`), nil
	}))
	r := e.helloed()
	r.send(`{"id":2,"op":"t.slow"}`)
	within(t, "the handler to start", inFlight, 5*time.Second)
	r.send(`{"id":3,"op":"t.late"}`)
	within(t, "the loop to take the request", taken, 5*time.Second)
	e.cancel()
	resp := r.recv()
	if resp.ID != 3 || resp.Code != CodeShuttingDown {
		t.Fatalf("%+v", resp)
	}
	if n := ran.Load(); n != 1 {
		t.Fatalf("%d handlers ran, want only the one in flight", n)
	}
	close(release)
	resp = r.recv()
	if resp.ID != 2 || resp.Code != "" || string(resp.Result) != `{"op":"t.slow"}` {
		t.Fatalf("the request in flight: %+v", resp)
	}
	r.expectClosed(3 * time.Second)
	if err := e.stop(); err != nil {
		t.Fatal(err)
	}
}

func TestServerShutdownGraceHoldsAgainstAPeerThatStopsReading(t *testing.T) {
	leakCheck(t)
	big := json.RawMessage(`{"x":"` + strings.Repeat("a", 60000) + `"}`)
	e := startServer(t, func(c *ServerConfig) {
		bigRate(c)
		c.WriteTimeout = time.Minute
		c.ShutdownGrace = 200 * time.Millisecond
	}, HandlerFunc(func(context.Context, Peer, string, json.RawMessage) (json.RawMessage, error) {
		return big, nil
	}))
	r := e.helloed()
	// Big requests for big answers, each with a ping, and never a read: the
	// answers fill the buffers, a ping's answer then blocks the loop, the
	// server stops reading, and so at last a write of ours stalls.
	req := []byte(fmt.Sprintf(`{"id":%%d,"op":"t.big","params":%s}`, big))
	stalled := false
	for i := 0; i < 2000 && !stalled; i++ {
		for _, p := range [][]byte{fmt.Appendf(nil, string(req), 2*i+2), fmt.Appendf(nil, `{"id":%d,"op":"seat.ping"}`, 2*i+3)} {
			r.c.SetWriteDeadline(time.Now().Add(time.Second))
			if err := writeFrame(r.c, p); err != nil {
				var ne net.Error
				if !errors.As(err, &ne) || !ne.Timeout() {
					t.Fatal(err)
				}
				stalled = true
				break
			}
		}
	}
	if !stalled {
		t.Fatal("the server kept reading")
	}
	start := time.Now()
	e.cancel()
	if err := e.stop(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Serve returned %v after the shutdown began, with a grace of 200ms", d)
	}
}

// A shutdown that begins right as a handshake ends still gives the
// connection's request its grace. The watcher that closes a connection whose
// handshake a shutdown interrupts was told the handshake was over and not
// waited for, so if it ran only once the shutdown had begun as well it could
// pick the shutdown and close the connection, with its request in flight.
//
// A hook holds the watcher until the request is in flight and the shutdown has
// begun; the watcher then picks one of two ready cases at random, so the test
// makes 16 rounds. A server that waits for its watcher answers no request
// before the watcher is done, so the hook lets it go after 100 ms.
func TestServerShutdownRightAsAHandshakeEndsKeepsTheGrace(t *testing.T) {
	leakCheck(t)
	for round := 0; round < 16; round++ {
		gate, decided := make(chan struct{}), make(chan struct{})
		started, answer := make(chan struct{}), make(chan struct{})
		e := startServer(t, func(c *ServerConfig) {
			c.hsWatch = func() func() { <-gate; return func() { close(decided) } }
		}, HandlerFunc(func(ctx context.Context, _ Peer, _ string, _ json.RawMessage) (json.RawMessage, error) {
			close(started)
			select {
			case <-answer:
				return json.RawMessage(`{"done":true}`), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}))
		r := e.raw()
		r.send(`{"id":1,"op":"seat.hello","params":{"proto":1,"caps":[],"role":"seat"}}`)
		r.send(`{"id":2,"op":"t.slow"}`)
		select {
		case <-started:
			e.cancel()
			untilTrue(t, "the shutdown to begin", func() bool { // the drain is closed before the listener
				c, err := net.DialTimeout("tcp", e.addr, time.Second)
				if err == nil {
					c.Close()
				}
				return err != nil
			})
			close(gate)
			within(t, "the watcher", decided, 5*time.Second)
		case <-time.After(100 * time.Millisecond):
			close(gate)
			within(t, "the handler to start", started, 5*time.Second)
			e.cancel()
		}
		close(answer)
		got := map[uint64]response{}
		for len(got) < 2 {
			resp, err := r.recvWithin(5 * time.Second)
			if err != nil {
				t.Fatalf("round %d: the connection ended with its request in flight (%v); answers %+v", round, err, got)
			}
			got[resp.ID] = resp
		}
		if resp := got[2]; resp.Code != "" || string(resp.Result) != `{"done":true}` {
			t.Fatalf("round %d: %+v", round, resp)
		}
		if err := e.stop(); err != nil {
			t.Fatal(err)
		}
	}
}

// A request that reuses the id of one in flight closes the connection even
// when the rate is spent: it used to be answered rate-limited first, under the
// id of the request in flight, which would be answered too.
func TestServerIdReuseIsCaughtBeforeTheRateLimit(t *testing.T) {
	leakCheck(t)
	release := make(chan struct{})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := startServer(t, func(c *ServerConfig) {
		c.RateMax, c.Now = 1, func() time.Time { return now }
	}, HandlerFunc(func(ctx context.Context, _ Peer, _ string, _ json.RawMessage) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, nil
	}))
	defer close(release)
	r := e.helloed()
	r.send(`{"id":7,"op":"t.block"}`) // spends the rate
	r.send(`{"id":7,"op":"t.block"}`)
	if got := r.expectClosed(3 * time.Second); len(got) != 0 {
		t.Fatalf("answered before the close: %+v", got)
	}
}

// The id of a request is its own until its answer is written: a request that
// reuses it while the answer waits to be written closes the connection, as
// one that reuses it while the handler runs does. It used to be released
// before the answer was written, so the second request was taken and answered
// under the same id.
func TestServerIdIsHeldUntilItsAnswerIsWritten(t *testing.T) {
	leakCheck(t)
	ready, hold := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	e := startServer(t, func(c *ServerConfig) {
		c.answerHook = func(id uint64, written bool) {
			if id == 7 && !written {
				once.Do(func() { close(ready); <-hold })
			}
		}
	}, HandlerFunc(func(context.Context, Peer, string, json.RawMessage) (json.RawMessage, error) {
		calls.Add(1)
		return nil, nil
	}))
	var holdOnce sync.Once
	free := func() { holdOnce.Do(func() { close(hold) }) }
	t.Cleanup(free)
	r := e.helloed()
	r.send(`{"id":7,"op":"t.x"}`)
	within(t, "the answer to be ready", ready, 5*time.Second)
	r.send(`{"id":7,"op":"t.x"}`)
	got := r.expectClosed(3 * time.Second)
	free()
	if n := calls.Load(); n != 1 || len(got) != 0 {
		t.Fatalf("the handler ran %d times, answers before the close %+v", n, got)
	}
}

// A client may reuse an id once it has the answer, even while the server has
// not yet let go of the id after writing that answer.
func TestServerIdIsFreeOnceItsAnswerIsWritten(t *testing.T) {
	leakCheck(t)
	written, hold := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e := startServer(t, func(c *ServerConfig) {
		c.answerHook = func(id uint64, w bool) {
			if id == 7 && w {
				once.Do(func() { close(written); <-hold })
			}
		}
	}, echo())
	var holdOnce sync.Once
	free := func() { holdOnce.Do(func() { close(hold) }) }
	t.Cleanup(free)
	r := e.helloed()
	if resp := r.call(7, "t.x", ""); resp.Code != "" {
		t.Fatalf("%+v", resp)
	}
	within(t, "the answer to be written", written, 5*time.Second)
	r.send(`{"id":7,"op":"t.x"}`)
	free()
	if resp := r.recv(); resp.ID != 7 || resp.Code != "" {
		t.Fatalf("%+v", resp)
	}
	if resp := r.call(8, "seat.ping", ""); resp.ID != 8 || resp.Code != "" {
		t.Fatalf("%+v", resp)
	}
}

// stallListener hands each connection it accepts to the test as a stallConn.
type stallListener struct {
	net.Listener
	conns chan *stallConn
}

func (l stallListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	sc := &stallConn{Conn: c, entered: make(chan struct{}), release: make(chan error, 1), closed: make(chan struct{})}
	select {
	case l.conns <- sc:
	default:
	}
	return sc, nil
}

// stallConn is the server's end of a connection whose next write, once armed,
// is held as a peer that stops reading holds it: it waits until the test lets
// it go on, or fails it, or the connection is closed.
type stallConn struct {
	net.Conn
	mu        sync.Mutex
	armed     bool
	entered   chan struct{} // closed when the held write has started
	release   chan error    // nil lets the write go on; an error fails it
	closed    chan struct{}
	closeOnce sync.Once
}

func (c *stallConn) arm() {
	c.mu.Lock()
	c.armed = true
	c.mu.Unlock()
}

func (c *stallConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	armed := c.armed
	c.armed = false
	c.mu.Unlock()
	if armed {
		close(c.entered)
		select {
		case err := <-c.release:
			if err != nil {
				return 0, err
			}
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(b)
}

func (c *stallConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// A request that reuses an id while the answer under it is being written waits
// for that write. If the write fails, the connection is closed and the request
// is not served: the id was never free and the peer never had the answer. It
// used to be released after a failed write too, so the request was taken and
// could reach the handler on a connection that was going. If the write
// succeeds, the id is free (the peer may have the answer before the server's
// write returns) and the request is served, after the first was answered.
//
// The server's write is held where it meets the socket, and a hook says when
// the request that reuses the id has been seen while it is held. The clock of
// the rate limit counts the requests taken past the id check.
func TestServerIdReusedWhileItsAnswerIsWritten(t *testing.T) {
	for _, fail := range []bool{true, false} {
		name := "the write succeeds"
		if fail {
			name = "the write fails"
		}
		t.Run(name, func(t *testing.T) {
			leakCheck(t)
			conns := make(chan *stallConn, 1)
			seen := make(chan struct{})
			var seenOnce sync.Once
			var calls, taken atomic.Int32
			e := startServerWrapped(t, "127.0.0.1:0", newID(t, "gate"), newID(t, "seat"),
				func(ln net.Listener) net.Listener { return stallListener{ln, conns} },
				func(c *ServerConfig) {
					c.Now = func() time.Time { taken.Add(1); return time.Now() }
					c.reuseHook = func(id uint64) {
						if id == 7 {
							seenOnce.Do(func() { close(seen) })
						}
					}
				}, HandlerFunc(func(context.Context, Peer, string, json.RawMessage) (json.RawMessage, error) {
					calls.Add(1)
					return nil, nil
				}))
			r := e.helloed()
			sc := within(t, "the connection", conns, 5*time.Second)
			sc.arm()
			r.send(`{"id":7,"op":"t.x"}`)
			within(t, "the answer's write to start", sc.entered, 5*time.Second)
			r.send(`{"id":7,"op":"t.x"}`)
			within(t, "the reuse to be seen while the answer is written", seen, 5*time.Second)
			if fail {
				sc.release <- errors.New("the peer is gone")
				if got := r.expectClosed(3 * time.Second); len(got) != 0 {
					t.Fatalf("answered: %+v", got)
				}
				if err := e.stop(); err != nil {
					t.Fatal(err)
				}
				if n, k := calls.Load(), taken.Load(); n != 1 || k != 1 {
					t.Fatalf("after a failed write the reuse was taken: %d requests taken, %d handler calls; want 1 and 1", k, n)
				}
				return
			}
			sc.release <- nil
			for i := 0; i < 2; i++ {
				if resp := r.recv(); resp.ID != 7 || resp.Code != "" {
					t.Fatalf("answer %d: %+v", i, resp)
				}
			}
			if resp := r.call(8, "seat.ping", ""); resp.ID != 8 || resp.Code != "" {
				t.Fatalf("%+v", resp)
			}
			if n := calls.Load(); n != 2 {
				t.Fatalf("the handler ran %d times, want 2", n)
			}
		})
	}
}

// The default ceiling of handlers, MaxConns × MaxInFlight, stops at the
// largest int. It used to overflow: with huge settings it came out negative
// (or small), and every request for a handler was answered busy.
func TestServerDefaultHandlerCeilingSaturates(t *testing.T) {
	leakCheck(t)
	if got := (ServerConfig{}).withDefaults().MaxHandlers; got != 128 {
		t.Fatalf("the default is %d, want 128", got)
	}
	for _, c := range [][2]int{{math.MaxInt, 16}, {math.MaxInt/2 + 1, 2}, {math.MaxInt, math.MaxInt}, {3, math.MaxInt}} {
		if got := (ServerConfig{MaxConns: c[0], MaxInFlight: c[1]}).withDefaults().MaxHandlers; got != math.MaxInt {
			t.Errorf("MaxConns %d, MaxInFlight %d: MaxHandlers %d, want %d", c[0], c[1], got, math.MaxInt)
		}
	}
	e := startServer(t, func(c *ServerConfig) { c.MaxConns, c.MaxInFlight = math.MaxInt, 16 }, echo())
	r := e.helloed()
	if resp := r.call(2, "t.x", ""); resp.Code != "" {
		t.Fatalf("%+v", resp)
	}
}
