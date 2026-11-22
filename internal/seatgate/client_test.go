package seatgate

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastClient is the configuration of a client for tests: short timers.
func fastClient(e *env, mut func(*ClientConfig)) ClientConfig {
	cfg := ClientConfig{
		TLS:          ClientTLS(e.seat, e.srv.Fingerprint(), "gate.example.invalid"),
		Caps:         []string{"owner.read"},
		PingInterval: 100 * time.Millisecond,
		DeadAfter:    time.Second,
		BackoffMin:   10 * time.Millisecond,
		BackoffMax:   80 * time.Millisecond,
	}
	if mut != nil {
		mut(&cfg)
	}
	return cfg
}

// dial connects a client to the server of e and closes it at the end.
func dial(t *testing.T, e *env, mut func(*ClientConfig)) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, e.addr, fastClient(e, mut))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func untilTrue(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// bigRate keeps the server's rate limit out of a test that is not about it.
func bigRate(c *ServerConfig) { c.RateMax = 100000 }

func TestClientCallRoundTrip(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, echo())
	c := dial(t, e, nil)
	since, ok := c.Connected()
	if !ok || time.Since(since) > 5*time.Second {
		t.Fatalf("connected = %v %v", since, ok)
	}
	if got := strings.Join(c.Caps(), ","); got != "owner.read,seat.pair" {
		t.Fatalf("caps %q", got)
	}
	var out struct {
		Op     string          `json:"op"`
		Peer   string          `json:"peer"`
		Params json.RawMessage `json:"params"`
	}
	err := c.Call(context.Background(), "owner.show", map[string]string{"id": "p-1"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Op != "owner.show" || out.Peer != e.seat.Fingerprint() || string(out.Params) != `{"id":"p-1"}` {
		t.Fatalf("%+v", out)
	}
	if err := c.Call(context.Background(), "owner.list", nil, nil); err != nil {
		t.Fatal(err)
	}
	// A struct as params, a nil result.
	type p struct {
		N int `json:"n"`
	}
	if err := c.Call(context.Background(), "owner.limit", p{3}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClientRemoteErrors(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, HandlerFunc(func(_ context.Context, _ Peer, op string, _ json.RawMessage) (json.RawMessage, error) {
		switch op {
		case "t.coded":
			return nil, NewCodedError("card-expired")
		case "t.plain":
			return nil, errors.New("SECRET")
		case "t.panic":
			panic("SECRET")
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))
	c := dial(t, e, nil)
	for op, want := range map[string]string{"t.coded": "card-expired", "t.plain": CodeFailed, "t.panic": CodeFailed} {
		err := c.Call(context.Background(), op, nil, nil)
		var re *RemoteError
		if !errors.As(err, &re) || re.Code != want {
			t.Errorf("%s: %v, want code %q", op, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: the error text leaks: %v", op, err)
		}
	}
	// The connection survives them.
	if err := c.Call(context.Background(), "t.fine", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClientRefusesWhatItWouldNotSend(t *testing.T) {
	e := startServer(t, nil, echo())
	c := dial(t, e, nil)
	ctx := context.Background()
	if err := c.Call(ctx, OpHello, nil, nil); err != ErrReserved {
		t.Errorf("hello: %v", err)
	}
	if err := c.Call(ctx, OpPing, nil, nil); err != ErrReserved {
		t.Errorf("ping: %v", err)
	}
	for _, op := range []string{"", "list", "Owner.list", "owner.", "owner.a b"} {
		if err := c.Call(ctx, op, nil, nil); err != ErrBadCall {
			t.Errorf("op %q: %v", op, err)
		}
	}
	for name, params := range map[string]any{"number": 5, "array": []int{1}, "string": "x", "func": func() {}} {
		if err := c.Call(ctx, "owner.list", params, nil); err != ErrBadCall {
			t.Errorf("params %s: %v", name, err)
		}
	}
	huge := map[string]string{"x": strings.Repeat("a", MaxFrame)}
	if err := c.Call(ctx, "owner.list", huge, nil); err != ErrBadCall {
		t.Errorf("params over the frame cap: %v", err)
	}
	// None of that cost the connection.
	if err := c.Call(ctx, "owner.list", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClientResultIsReadStrictly(t *testing.T) {
	e := startServer(t, nil, echo())
	c := dial(t, e, nil)
	var narrow struct {
		Op string `json:"op"` // the echo also answers peer and params
	}
	err := c.Call(context.Background(), "owner.list", nil, &narrow)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("a result with fields the type does not name: %v", err)
	}
	if err := c.Call(context.Background(), "owner.list", nil, nil); err != nil {
		t.Fatalf("the connection should stay: %v", err)
	}
}

func TestClientConcurrentCalls(t *testing.T) {
	leakCheck(t)
	e := startServer(t, bigRate, HandlerFunc(func(_ context.Context, _ Peer, _ string, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			N int `json:"n"`
		}
		if err := DecodeStrict(params, &p); err != nil {
			return nil, err
		}
		time.Sleep(time.Duration(p.N%5) * time.Millisecond)
		return json.RawMessage(fmt.Sprintf(`{"n":%d}`, p.N)), nil
	}))
	c := dial(t, e, func(cfg *ClientConfig) { cfg.PingInterval = 20 * time.Millisecond })
	const workers, each = 12, 40
	var wg sync.WaitGroup
	var busy atomic.Int32
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				n := w*1000 + i
				var out struct {
					N int `json:"n"`
				}
				err := c.Call(context.Background(), "t.n", map[string]int{"n": n}, &out)
				var re *RemoteError
				switch {
				case errors.As(err, &re) && re.Code == CodeBusy:
					busy.Add(1) // 12 callers against 16 slots: rare, but allowed
				case err != nil:
					t.Errorf("call %d: %v", n, err)
				case out.N != n:
					t.Errorf("call %d got the result of %d", n, out.N)
				}
			}
		}()
	}
	wg.Wait()
	if busy.Load() > 0 {
		t.Logf("%d calls were answered busy", busy.Load())
	}
}

func TestClientNotConnectedAnswersAtOnce(t *testing.T) {
	leakCheck(t)
	id, seat := newID(t, "gate"), newID(t, "seat")
	c, err := NewClient("127.0.0.1:1", ClientConfig{TLS: ClientTLS(seat, id.Fingerprint(), "gate.example.invalid")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	if err := c.Call(context.Background(), "owner.list", nil, nil); err != ErrDisconnected {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("took %v", time.Since(start))
	}
	if _, ok := c.Connected(); ok {
		t.Fatal("connected to nothing")
	}
}

func TestDialFailures(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, echo())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Run("a server that is not the pinned one", func(t *testing.T) {
		cfg := fastClient(e, nil)
		cfg.TLS = ClientTLS(e.seat, newID(t, "gate").Fingerprint(), "gate.example.invalid")
		if c, err := Dial(ctx, e.addr, cfg); err == nil {
			c.Close()
			t.Fatal("dialled an unpinned server")
		}
	})
	t.Run("a client that is not pinned", func(t *testing.T) {
		cfg := fastClient(e, nil)
		cfg.TLS = ClientTLS(newID(t, "seat"), e.srv.Fingerprint(), "gate.example.invalid")
		if c, err := Dial(ctx, e.addr, cfg); err == nil {
			c.Close()
			t.Fatal("an unpinned client was let in")
		}
	})
	t.Run("nothing listens", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := ln.Addr().String()
		ln.Close()
		if c, err := Dial(ctx, addr, fastClient(e, nil)); err == nil {
			c.Close()
			t.Fatal("dialled nothing")
		}
	})
	t.Run("bad configuration", func(t *testing.T) {
		if _, err := Dial(ctx, e.addr, ClientConfig{}); err == nil {
			t.Error("no TLS configuration")
		}
		if _, err := Dial(ctx, "", fastClient(e, nil)); err == nil {
			t.Error("no address")
		}
		if _, err := Dial(ctx, e.addr, fastClient(e, func(c *ClientConfig) { c.Caps = []string{"A"} })); err == nil {
			t.Error("a bad capability")
		}
	})
	t.Run("a cancelled context", func(t *testing.T) {
		cctx, ccancel := context.WithCancel(ctx)
		ccancel()
		if c, err := Dial(cctx, e.addr, fastClient(e, nil)); err == nil {
			c.Close()
			t.Fatal("dialled with a cancelled context")
		}
	})
}

// A caller that ends the context of Dial once Dial has returned, as a
// deferred cancel does, keeps the connection Dial made. The context bounded
// the first attempt only; it used to be watched until a goroutine noticed the
// attempt was over, and closed the connection if it saw the context end first,
// which a loaded machine made happen to about one Dial in five.
func TestDialContextEndingAfterDialKeepsTheConnection(t *testing.T) {
	leakCheck(t)
	e := startServer(t, func(c *ServerConfig) { c.MaxConns = 1000; bigRate(c) }, echo())
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		// No redial: a lost connection stays lost, and shows.
		c, err := Dial(ctx, e.addr, fastClient(e, func(c *ClientConfig) { c.BackoffMin, c.BackoffMax = time.Hour, time.Hour }))
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		since, ok := c.Connected()
		err = c.Call(context.Background(), "owner.list", nil, nil)
		again, still := c.Connected()
		c.Close()
		if !ok || err != nil || !still || !again.Equal(since) {
			t.Fatalf("dial %d: connected %v, call %v, still connected %v (%v)", i, ok, err, still, c.LastError())
		}
	}
}

// A context that ends while the client waits for the answer to its hello ends
// the attempt then, not at HelloTimeout.
func TestDialContextEndsTheWaitForTheHello(t *testing.T) {
	leakCheck(t)
	srv, seat := fakeEnv(t)
	stop := make(chan struct{})
	addr := fakeGate(t, srv, seat, func(c *tls.Conn) {
		readHello(t, c)
		<-stop // never answers
	})
	t.Cleanup(func() { close(stop) }) // runs before the gate's own, which waits for it
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	cl, err := Dial(ctx, addr, clientFor(srv, seat, func(c *ClientConfig) { c.HelloTimeout = time.Minute }))
	if err == nil {
		cl.Close()
		t.Fatal("dialled a gate that never answered the hello")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the attempt ended after %v, want about 200ms", d)
	}
}

// fakeGate is a TLS server that plays the gate by hand, so that a test can make
// it say what a real one would not. behave is run for each connection, after
// the handshake.
func fakeGate(t *testing.T, srv, seat Identity, behave func(c *tls.Conn)) (addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, nc)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer nc.Close()
				tc := tls.Server(nc, ServerTLS(srv, []string{seat.Fingerprint()}))
				nc.SetDeadline(time.Now().Add(5 * time.Second))
				if tc.Handshake() != nil {
					return
				}
				nc.SetDeadline(time.Time{})
				behave(tc)
			}()
		}
	}()
	t.Cleanup(func() {
		close(stop)
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().String()
}

// readHello reads the hello a client sends and returns its id.
func readHello(t *testing.T, c *tls.Conn) uint64 {
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	p, err := readFrame(c)
	if err != nil {
		return 0
	}
	req, err := decodeRequest(p)
	if err != nil || req.Op != OpHello {
		return 0
	}
	c.SetReadDeadline(time.Time{})
	return req.ID
}

func sendFrame(c *tls.Conn, payload string) { writeFrame(c, []byte(payload)) }

func helloOK(c *tls.Conn, id uint64) {
	sendFrame(c, fmt.Sprintf(`{"id":%d,"result":{"proto":1,"caps":["x"]}}`, id))
}

func fakeEnv(t *testing.T) (srv, seat Identity) { return newID(t, "gate"), newID(t, "seat") }

func clientFor(srv, seat Identity, mut func(*ClientConfig)) ClientConfig {
	cfg := ClientConfig{
		TLS:          ClientTLS(seat, srv.Fingerprint(), "gate.example.invalid"),
		PingInterval: time.Hour,
		DeadAfter:    300 * time.Millisecond,
		BackoffMin:   10 * time.Millisecond,
		BackoffMax:   50 * time.Millisecond,
	}
	if mut != nil {
		mut(&cfg)
	}
	return cfg
}

func TestClientRefusesAGateThatAnswersTheHelloBadly(t *testing.T) {
	leakCheck(t)
	srv, seat := fakeEnv(t)
	answers := map[string]string{
		"proto-unsupported": `{"id":1,"error":{"code":"proto-unsupported"}}`,
		"another proto":     `{"id":1,"result":{"proto":2,"caps":[]}}`,
		"no caps":           `{"id":1,"result":{"proto":1}}`,
		"wrong id":          `{"id":7,"result":{"proto":1,"caps":[]}}`,
		"not json":          `nope`,
		"duplicate key":     `{"id":1,"id":1,"result":{"proto":1,"caps":[]}}`,
		"with role":         `{"id":1,"result":{"proto":1,"caps":[],"role":"seat"}}`,
	}
	for name, answer := range answers {
		addr := fakeGate(t, srv, seat, func(c *tls.Conn) {
			readHello(t, c)
			sendFrame(c, answer)
			time.Sleep(200 * time.Millisecond)
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cl, err := Dial(ctx, addr, clientFor(srv, seat, nil))
		cancel()
		if err == nil {
			cl.Close()
			t.Errorf("%s: dial succeeded", name)
			continue
		}
		if name == "proto-unsupported" {
			var re *RemoteError
			if !errors.As(err, &re) || re.Code != CodeProtoUnsupported {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
}

func TestClientSaysItsHello(t *testing.T) {
	srv, seat := fakeEnv(t)
	got := make(chan request, 1)
	addr := fakeGate(t, srv, seat, func(c *tls.Conn) {
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		p, _ := readFrame(c)
		req, _ := decodeRequest(p)
		got <- req
		helloOK(c, req.ID)
		time.Sleep(100 * time.Millisecond)
	})
	cl, err := Dial(context.Background(), addr, clientFor(srv, seat, func(c *ClientConfig) { c.Caps = []string{"a.b", "c"} }))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	req := <-got
	if req.Op != OpHello || req.ID != 1 || string(req.Params) != `{"proto":1,"caps":["a.b","c"],"role":"seat"}` {
		t.Fatalf("%+v %s", req, req.Params)
	}
}

func TestClientTakesSilenceForDeath(t *testing.T) {
	leakCheck(t)
	srv, seat := fakeEnv(t)
	var conns atomic.Int32
	addr := fakeGate(t, srv, seat, func(c *tls.Conn) {
		conns.Add(1)
		helloOK(c, readHello(t, c))
		// Then silence: read and say nothing until the client goes.
		for {
			c.SetReadDeadline(time.Now().Add(10 * time.Second))
			if _, err := readFrame(c); err != nil {
				return
			}
		}
	})
	cl, err := Dial(context.Background(), addr, clientFor(srv, seat, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if _, ok := cl.Connected(); !ok {
		t.Fatal("not connected")
	}
	start := time.Now()
	untilTrue(t, "the silent gate to be given up", func() bool { _, ok := cl.Connected(); return !ok })
	if d := time.Since(start); d < 200*time.Millisecond || d > 3*time.Second {
		t.Fatalf("given up after %v, want about 300ms", d)
	}
	// And it dials again.
	untilTrue(t, "a second connection", func() bool { return conns.Load() >= 2 })
}

func TestClientStaysConnectedOnAnIdleLinkByPinging(t *testing.T) {
	leakCheck(t)
	e := startServer(t, func(c *ServerConfig) { c.IdleTimeout = 300 * time.Millisecond; bigRate(c) }, echo())
	c := dial(t, e, func(cfg *ClientConfig) {
		cfg.PingInterval = 50 * time.Millisecond
		cfg.DeadAfter = 250 * time.Millisecond
	})
	since, _ := c.Connected()
	time.Sleep(1200 * time.Millisecond) // four idle timeouts
	if s2, ok := c.Connected(); !ok || !s2.Equal(since) {
		t.Fatalf("the connection was lost and remade: %v %v (was %v)", s2, ok, since)
	}
	if err := c.Call(context.Background(), "owner.list", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClientWithoutPingsIsClosedByTheServer(t *testing.T) {
	leakCheck(t)
	e := startServer(t, func(c *ServerConfig) { c.IdleTimeout = 200 * time.Millisecond }, echo())
	c := dial(t, e, func(cfg *ClientConfig) { cfg.PingInterval = time.Hour; cfg.DeadAfter = time.Hour })
	since, _ := c.Connected()
	untilTrue(t, "the server to close the silent link and the client to redial", func() bool {
		s, ok := c.Connected()
		return ok && !s.Equal(since)
	})
}

func TestClientReconnectsAfterTheServerRestarts(t *testing.T) {
	leakCheck(t)
	e := startServer(t, bigRate, echo())
	c := dial(t, e, nil)
	if err := c.Call(context.Background(), "owner.list", nil, nil); err != nil {
		t.Fatal(err)
	}
	first, _ := c.Connected()
	if err := e.stop(); err != nil {
		t.Fatal(err)
	}
	untilTrue(t, "the loss to be noticed", func() bool { _, ok := c.Connected(); return !ok })
	if err := c.Call(context.Background(), "owner.list", nil, nil); err != ErrDisconnected {
		t.Fatalf("while the server is down: %v", err)
	}
	if c.LastError() == nil {
		t.Error("no reason was kept for the loss")
	}
	e2 := startServerAt(t, e.addr, e.srv, e.seat, bigRate, echo())
	_ = e2
	untilTrue(t, "the reconnect", func() bool { _, ok := c.Connected(); return ok })
	if again, _ := c.Connected(); !again.After(first) {
		t.Errorf("since %v is not after %v", again, first)
	}
	if err := c.Call(context.Background(), "owner.list", nil, nil); err != nil {
		t.Fatalf("after the reconnect: %v", err)
	}
	if c.LastError() != nil {
		t.Errorf("a reason is kept while connected: %v", c.LastError())
	}
}

func TestClientRunKeepsTryingUntilTheGateAppears(t *testing.T) {
	leakCheck(t)
	srv, seat := fakeEnv(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	cl, err := NewClient(addr, clientFor(srv, seat, nil))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cl.Run(ctx) }()
	time.Sleep(150 * time.Millisecond)
	if _, ok := cl.Connected(); ok {
		t.Fatal("connected to nothing")
	}
	e := startServerAt(t, addr, srv, seat, bigRate, echo())
	_ = e
	untilTrue(t, "the first connection", func() bool { _, ok := cl.Connected(); return ok })
	if err := cl.Call(context.Background(), "owner.list", nil, nil); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if err := cl.Call(context.Background(), "owner.list", nil, nil); err == nil {
		t.Fatal("a call succeeded after Run ended")
	}
	if err := cl.Run(context.Background()); err == nil {
		t.Fatal("Run twice")
	}
}

func TestBackoffSchedule(t *testing.T) {
	s := time.Second
	// With no jitter (r = 0) the wait is half of the doubled step; with r near 1
	// it is the whole step.
	steps := []time.Duration{1 * s, 2 * s, 4 * s, 8 * s, 16 * s, 30 * s, 30 * s, 30 * s}
	for n, step := range steps {
		if got := backoff(s, 30*s, n, 0); got != step/2 {
			t.Errorf("n=%d r=0: %v, want %v", n, got, step/2)
		}
		if got := backoff(s, 30*s, n, 0.999999); got < step-step/1000 || got >= step {
			t.Errorf("n=%d r~1: %v, want just under %v", n, got, step)
		}
	}
	if got := backoff(s, 30*s, 1000, 0.5); got != 30*s*3/4 {
		t.Errorf("a huge n: %v", got)
	}
	for n := 0; n < 20; n++ {
		for _, r := range []float64{0, 0.1, 0.5, 0.9} {
			if d := backoff(s, 30*s, n, r); d < s/2 || d > 30*s {
				t.Errorf("n=%d r=%v: %v out of range", n, r, d)
			}
		}
	}
}

// The supervisor uses the schedule, with the clock and the random source of
// the configuration, and starts it again after a connection that was made.
func TestClientRunBacksOffWithAnInjectedClock(t *testing.T) {
	leakCheck(t)
	srv, seat := fakeEnv(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	var mu sync.Mutex
	var waits []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	cfg := clientFor(srv, seat, func(c *ClientConfig) {
		c.BackoffMin, c.BackoffMax = time.Second, 30*time.Second
		c.Rand = func() float64 { return 0 }
		c.Sleep = func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()
			waits = append(waits, d)
			if len(waits) >= 8 {
				cancel()
			}
			return ctx.Err()
		}
	})
	cl, err := NewClient(addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	want := []time.Duration{500, 1000, 2000, 4000, 8000, 15000, 15000, 15000}
	for i, w := range want {
		if i >= len(waits) || waits[i] != w*time.Millisecond {
			t.Fatalf("waits %v, want %v ms", waits, want)
		}
	}
}

func TestClientBackoffStartsOverAfterAConnection(t *testing.T) {
	leakCheck(t)
	e := startServer(t, bigRate, echo())
	var mu sync.Mutex
	var waits []time.Duration
	cfg := fastClient(e, func(c *ClientConfig) {
		c.Rand = func() float64 { return 0 }
		c.BackoffMin, c.BackoffMax = 100*time.Millisecond, 10*time.Second
		c.Sleep = func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			waits = append(waits, d)
			mu.Unlock()
			return sleep(ctx, 20*time.Millisecond)
		}
	})
	c, err := NewClient(e.addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	untilTrue(t, "connection", func() bool { _, ok := c.Connected(); return ok })
	e.stop()
	untilTrue(t, "failed attempts", func() bool { mu.Lock(); defer mu.Unlock(); return len(waits) >= 4 })
	startServerAt(t, e.addr, e.srv, e.seat, bigRate, echo())
	untilTrue(t, "reconnection", func() bool { _, ok := c.Connected(); return ok })
	mu.Lock()
	defer mu.Unlock()
	// The first wait after the loss is the first step; later ones double.
	if waits[0] != 50*time.Millisecond || waits[1] != 50*time.Millisecond || waits[2] != 100*time.Millisecond {
		t.Fatalf("waits %v", waits)
	}
}

func TestClientCallContext(t *testing.T) {
	leakCheck(t)
	release := make(chan struct{})
	var served atomic.Int32
	e := startServer(t, bigRate, HandlerFunc(func(ctx context.Context, _ Peer, op string, _ json.RawMessage) (json.RawMessage, error) {
		served.Add(1)
		if op == "t.block" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return json.RawMessage(`{"op":"` + op + `"}`), nil
	}))
	c := dial(t, e, nil)
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := c.Call(ctx, "t.block", nil, nil)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
	t.Run("cancel mid-call", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(50 * time.Millisecond); cancel() }()
		start := time.Now()
		err := c.Call(ctx, "t.block", nil, nil)
		if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
	t.Run("already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := c.Call(ctx, "t.quick", nil, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	})
	// The answers that came late are dropped, and the connection is whole.
	close(release)
	untilTrue(t, "the late answers", func() bool { return served.Load() >= 2 })
	var out struct {
		Op string `json:"op"`
	}
	if err := c.Call(context.Background(), "t.quick", nil, &out); err != nil || out.Op != "t.quick" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestClientCallEndsWhenTheServerGoes(t *testing.T) {
	leakCheck(t)
	started := make(chan struct{}, 1)
	e := startServer(t, func(c *ServerConfig) { c.ShutdownGrace = 50 * time.Millisecond }, HandlerFunc(
		func(ctx context.Context, _ Peer, _ string, _ json.RawMessage) (json.RawMessage, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}))
	c := dial(t, e, func(cfg *ClientConfig) { cfg.BackoffMin, cfg.BackoffMax = time.Hour, time.Hour })
	res := make(chan error, 1)
	go func() { res <- c.Call(context.Background(), "t.block", nil, nil) }()
	<-started
	e.cancel()
	select {
	case err := <-res:
		if err != ErrDisconnected {
			t.Fatalf("%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not end when the server went")
	}
}

func TestClientClose(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, HandlerFunc(func(ctx context.Context, _ Peer, _ string, _ json.RawMessage) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	c := dial(t, e, nil)
	res := make(chan error, 1)
	go func() { res <- c.Call(context.Background(), "t.block", nil, nil) }()
	time.Sleep(100 * time.Millisecond)
	c.Close()
	select {
	case err := <-res:
		if err != ErrClosed {
			t.Fatalf("%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not end on Close")
	}
	if err := c.Call(context.Background(), "t.quick", nil, nil); err != ErrClosed {
		t.Fatalf("after Close: %v", err)
	}
	if _, ok := c.Connected(); ok {
		t.Fatal("connected after Close")
	}
	c.Close() // twice is fine
}

func TestClientCloseCyclesLeakNothing(t *testing.T) {
	leakCheck(t)
	e := startServer(t, func(c *ServerConfig) { c.MaxConns = 100; bigRate(c) }, echo())
	for i := 0; i < 15; i++ {
		c, err := Dial(context.Background(), e.addr, fastClient(e, nil))
		if err != nil {
			t.Fatal(err)
		}
		c.Call(context.Background(), "owner.list", nil, nil)
		c.Close()
	}
}

func TestClientGivesUpOnAGateThatBreaksTheRules(t *testing.T) {
	leakCheck(t)
	srv, seat := fakeEnv(t)
	for name, frame := range map[string]string{
		"an answer to a request never made": `{"id":999,"result":{}}`,
		"a request":                         `{"id":2,"op":"seat.ping"}`,
		"duplicate key":                     `{"id":2,"id":2,"result":{}}`,
		"an error with text":                `{"id":2,"error":{"code":"x","text":"y"}}`,
		"not json":                          `nope`,
	} {
		var served atomic.Int32
		addr := fakeGate(t, srv, seat, func(c *tls.Conn) {
			helloOK(c, readHello(t, c))
			served.Add(1)
			time.Sleep(50 * time.Millisecond)
			sendFrame(c, frame)
			time.Sleep(time.Second)
		})
		cl, err := Dial(context.Background(), addr, clientFor(srv, seat, func(c *ClientConfig) { c.BackoffMin, c.BackoffMax = time.Hour, time.Hour }))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		for {
			if _, ok := cl.Connected(); !ok {
				break
			}
			if time.Since(start) > 3*time.Second {
				t.Errorf("%s: the connection was kept", name)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cl.Close()
	}
}

// A peer that does not read cannot hold a Call past its context, even while
// the call is writing.
func TestClientCallLeavesAStalledWriteAtItsContext(t *testing.T) {
	leakCheck(t)
	srv, seat := fakeEnv(t)
	stop := make(chan struct{})
	addr := fakeGate(t, srv, seat, func(c *tls.Conn) {
		helloOK(c, readHello(t, c))
		<-stop // never reads again
	})
	t.Cleanup(func() { close(stop) }) // runs before the gate's own, which waits for it
	cl, err := Dial(context.Background(), addr, clientFor(srv, seat, func(c *ClientConfig) {
		c.DeadAfter = time.Minute
		c.WriteTimeout = time.Minute
		c.BackoffMin, c.BackoffMax = time.Hour, time.Hour
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	params := map[string]string{"x": strings.Repeat("a", 60000)}
	for i := 0; i < 400; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		start := time.Now()
		err := cl.Call(ctx, "t.big", params, nil)
		cancel()
		if time.Since(start) > 2*time.Second {
			t.Fatalf("call %d held for %v (%v)", i, time.Since(start), err)
		}
		if err == ErrDisconnected {
			return // the buffers filled, the write was cut at the context and the link given up
		}
	}
	t.Fatal("the socket buffers never filled")
}
