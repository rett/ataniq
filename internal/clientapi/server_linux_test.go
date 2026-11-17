package clientapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func sampleStatus() StatusResult {
	at := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	return StatusResult{
		Daemon: DaemonStatus{PID: 4242, StartedAt: at, RegistryRevision: 17, BootID: "boot-1",
			LastReconcileCompleted: true, LastReconcileAt: &at},
		Leads: []LeadStatus{{
			Role: "demo/lead", Kind: "lead", Project: "demo", Manager: "systemd-user",
			Epoch: 3, Desired: "running", State: "ready", ReasonCode: "proof", ReasonDetail: "ok",
			EvidenceKind: "unit", Since: at,
			Requested: RequestedProfile{Provider: "claude", Version: "1", Model: "m", Effort: "high", PermissionMode: "plan", AccessMode: "ro"},
			Observed: ObservedProfile{Provider: Observe("claude"), Version: Observed{}, Model: Observe(""),
				Effort: Observed{}, PermissionMode: Observed{}, AccessMode: Observed{}},
			LaunchIntent: &LaunchIntent{ID: "i-1", Epoch: 3, Unit: "ataniq-lead-demo.service", Outcome: "started"},
		}, {Role: "supervisor", Kind: "supervisor"}},
	}
}

func TestRoundTripHelloStatus(t *testing.T) {
	h := &stubHandler{version: "v-test", status: sampleStatus()}
	ts := startServer(t, h, Options{})
	ctx := context.Background()
	c, err := Dial(ctx, ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hello, err := c.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := HelloResult{
		Protocols:  []string{"ataniq.client/1"},
		Limits:     Limits{MaxRequestBytes: 64 << 10, MaxResponseBytes: 1 << 20, IdleTimeoutSeconds: 30, MaxConnections: 32},
		Operations: []string{"hello", "status"},
		Mutations:  false,
		Version:    "v-test",
	}
	if !jsonEqual(t, hello, want) {
		t.Fatalf("hello = %+v, want %+v", hello, want)
	}

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, st, sampleStatus()) {
		t.Fatalf("status = %+v", st)
	}
	if st.Leads[0].Observed.Model != (Observed{Observed: true}) || st.Leads[0].Observed.Version.Observed {
		t.Fatalf("observed round trip: %+v", st.Leads[0].Observed)
	}
	snap := ts.counters.Snapshot()
	if snap.Accepted != 1 || snap.Served[OpHello] != 1 || snap.Served[OpStatus] != 1 {
		t.Fatalf("counters = %+v", snap)
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	return bytes.Equal(x, y)
}

func TestHelloSaysReadOnlyOnTheWire(t *testing.T) {
	h := &stubHandler{version: "v"}
	ts := startServer(t, h, Options{})
	r := dialRaw(t, ts.path)
	line := r.ask(helloReq)
	var res map[string]json.RawMessage
	if err := json.Unmarshal(line.Result, &res); err != nil {
		t.Fatal(err)
	}
	if string(res["mutations"]) != "false" {
		t.Fatalf("mutations = %s", res["mutations"])
	}
	if string(res["operations"]) != `["hello","status"]` {
		t.Fatalf("operations = %s", res["operations"])
	}
}

func TestHelloOverridesWhatTheHandlerCannotOwn(t *testing.T) {
	h := &lyingHandler{}
	ts := startServer(t, h, Options{MaxConns: 5, IdleTimeout: 7 * time.Second})
	c, err := Dial(context.Background(), ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, err := c.Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Mutations || len(got.Operations) != 2 || got.Limits.MaxConnections != 5 || got.Limits.IdleTimeoutSeconds != 7 || got.Version != "x" {
		t.Fatalf("hello = %+v", got)
	}
}

type lyingHandler struct{ stubHandler }

func (h *lyingHandler) Hello(ctx context.Context) HelloResult {
	return HelloResult{Mutations: true, Operations: []string{"approve"}, Protocols: []string{"other/9"}, Version: "x",
		Limits: Limits{MaxConnections: 1000}}
}

func TestStatusEmptyLeadsIsAnArray(t *testing.T) {
	h := &stubHandler{}
	ts := startServer(t, h, Options{})
	r := dialRaw(t, ts.path)
	resp := r.ask(statusReq)
	if !resp.OK || !strings.Contains(string(resp.Result), `"leads":[]`) {
		t.Fatalf("resp = %+v %s", resp, resp.Result)
	}
}

func TestObservedWireForm(t *testing.T) {
	b, err := json.Marshal([]Observed{{}, Observe("v"), Observe("")})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `[{"observed":false},{"observed":true,"value":"v"},{"observed":true,"value":""}]` {
		t.Fatalf("got %s", b)
	}
	var o Observed
	if err := json.Unmarshal([]byte(`{"observed":false,"value":"x"}`), &o); err != nil || o != (Observed{}) {
		t.Fatalf("unobserved with value: %+v %v", o, err)
	}
}

func TestEveryErrorCode(t *testing.T) {
	h := &stubHandler{}
	ts := startServer(t, h, Options{})
	r := dialRaw(t, ts.path)

	// invalid-request
	for _, line := range []string{
		``, `   `, `not json`, `{`, `[]`, `null`, `"x"`, `5`, `true`, `{} extra`, `{}{}`,
		`{}`,
		`{"protocol":"ataniq.client/1","op":"hello"}`,
		`{"request_id":"a","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a"}`,
		`{"protocol":"ataniq.client/1","request_id":"","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"` + strings.Repeat("a", 65) + `","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a\u0001b","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"café","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":7,"op":"hello"}`,
		`{"protocol":7,"request_id":"a","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":["hello"]}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":null}`,
		"{\"protocol\":\"ataniq.client/1\",\"request_id\":\"a\",\"op\":\"hel\xfflo\"}",
		`{"Protocol":"ataniq.client/1","Request_ID":"a","Op":"hello"}`,
		// a repeated top-level key, whichever way it would resolve
		`{"protocol":"ataniq.client/1","request_id":"a","op":"approve","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","op":"approve"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op" : "hello", "op" : "status"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","\u006fp":"status"}`,
		`{"protocol":"ataniq.client/1","protocol":"ataniq.client/2","request_id":"a","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","request_id":"b","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","extra":1,"extra":2}`,
	} {
		resp := r.ask(line)
		wantError(t, resp, CodeInvalidRequest)
		if resp.Error.Message != msgInvalidRequest {
			t.Errorf("message for %q = %q", line, resp.Error.Message)
		}
	}
	if hello, status := h.calls(); hello+status != 0 {
		t.Fatalf("handler called %d+%d times for invalid requests", hello, status)
	}

	// unsupported-operation, with the id echoed and nothing of the op in the message
	for _, op := range []string{"approve", "Hello", "", "status ", "send", "subscribe"} {
		line, _ := json.Marshal(Request{Protocol: Protocol, RequestID: "op-id", Op: op})
		resp := r.ask(string(line))
		wantError(t, resp, CodeUnsupportedOperation)
		if resp.RequestID != "op-id" || resp.Error.Message != msgUnsupportedOperation {
			t.Errorf("resp = %+v", resp)
		}
	}

	// unsupported-protocol lists the supported ones
	resp := r.ask(`{"protocol":"ataniq.client/2","request_id":"p1","op":"hello"}`)
	wantError(t, resp, CodeUnsupportedProtocol)
	if resp.RequestID != "p1" || len(resp.Error.SupportedProtocols) != 1 || resp.Error.SupportedProtocols[0] != Protocol {
		t.Fatalf("resp = %+v", resp)
	}
	resp = r.ask(`{"protocol":"","request_id":"p2","op":"nonsense"}`)
	wantError(t, resp, CodeUnsupportedProtocol)

	// the connection still works, and unknown fields are ignored
	resp = r.ask(`{"extra":{"a":[1,2]},"protocol":"ataniq.client/1","request_id":"ok","op":"hello","more":1}`)
	if !resp.OK || resp.RequestID != "ok" {
		t.Fatalf("resp = %+v", resp)
	}
	snap := ts.counters.Snapshot()
	if snap.Invalid != 33 || snap.Unsupported != 8 {
		t.Fatalf("counters = %+v", snap)
	}
}

func TestMessagesNeverEchoPeerInput(t *testing.T) {
	h := &stubHandler{panicStatus: true}
	ts := startServer(t, h, Options{})
	r := dialRaw(t, ts.path)
	const marker = "PEERMARKER"
	for _, line := range []string{
		`{"protocol":"` + marker + `","request_id":"a","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"` + marker + `"}`,
		`{"protocol":"ataniq.client/1","request_id":"` + marker + marker + marker + marker + marker + marker + marker + marker + `","op":"hello"}`,
		marker,
	} {
		raw, err := r.askRaw(line)
		if err != nil {
			t.Fatal(err)
		}
		var resp Response
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil {
			t.Fatalf("no error for %q", line)
		}
		if strings.Contains(resp.Error.Message, marker) {
			t.Errorf("message echoes peer input: %q", resp.Error.Message)
		}
	}
}

func (r *rawConn) askRaw(line string) ([]byte, error) {
	r.t.Helper()
	r.send(line + "\n")
	return r.readLine()
}

func TestTooLargeRequest(t *testing.T) {
	h := &stubHandler{}
	ts := startServer(t, h, Options{})

	// A line of exactly the cap is served.
	r := dialRaw(t, ts.path)
	prefix := `{"protocol":"ataniq.client/1","request_id":"big","op":"hello","pad":"`
	suffix := `"}`
	line := prefix + strings.Repeat("x", MaxRequestBytes-len(prefix)-len(suffix)) + suffix
	if len(line) != MaxRequestBytes {
		t.Fatalf("line is %d bytes", len(line))
	}
	if resp := r.ask(line); !resp.OK || resp.RequestID != "big" {
		t.Fatalf("resp at the cap = %+v", resp)
	}

	// One byte over gets too-large and a closed connection, even while the
	// peer is still writing a much longer line.
	r = dialRaw(t, ts.path)
	over := prefix + strings.Repeat("x", MaxRequestBytes-len(prefix)-len(suffix)+1) + suffix
	r.send(over + "\n")
	resp := r.resp()
	wantError(t, resp, CodeTooLarge)
	if resp.Error.Message != msgTooLarge {
		t.Fatalf("message = %q", resp.Error.Message)
	}
	r.wantClosed()

	r = dialRaw(t, ts.path)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.c.Write([]byte(strings.Repeat("y", 300<<10) + "\n"))
	}()
	resp = r.resp()
	wantError(t, resp, CodeTooLarge)
	r.wantClosed()
	<-done

	// A huge line with no LF at all is cut too.
	r = dialRaw(t, ts.path)
	go r.c.Write([]byte(strings.Repeat("z", 200<<10)))
	wantError(t, r.resp(), CodeTooLarge)
	if got := ts.counters.Snapshot().TooLarge; got != 3 {
		t.Fatalf("TooLarge = %d", got)
	}
}

func TestOverCapResponse(t *testing.T) {
	st := sampleStatus()
	st.Leads[0].ReasonDetail = strings.Repeat("d", MaxResponseBytes)
	h := &stubHandler{status: st, version: "v"}
	ts := startServer(t, h, Options{})
	c, err := Dial(context.Background(), ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Status(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeInternal || e.Message != msgResponseTooLarge {
		t.Fatalf("err = %v", err)
	}
	// The connection is still good and a smaller answer goes through.
	if _, err := c.Hello(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ts.counters.Snapshot().Internal != 1 {
		t.Fatalf("counters = %+v", ts.counters.Snapshot())
	}
}

func TestHandlerFailureAndPanicBecomeInternal(t *testing.T) {
	h := &stubHandler{statusErr: errors.New("secret detail")}
	ts := startServer(t, h, Options{})
	r := dialRaw(t, ts.path)

	resp := r.ask(statusReq)
	wantError(t, resp, CodeInternal)
	if resp.RequestID != "r2" || strings.Contains(resp.Error.Message, "secret") {
		t.Fatalf("resp = %+v", resp)
	}

	h.mu.Lock()
	h.panicHello, h.panicStatus = true, true
	h.mu.Unlock()
	for _, q := range []string{helloReq, statusReq} {
		resp = r.ask(q)
		wantError(t, resp, CodeInternal)
		if strings.Contains(resp.Error.Message, "secret") {
			t.Fatalf("message = %q", resp.Error.Message)
		}
	}

	// The server and the connection survive.
	h.mu.Lock()
	h.panicHello, h.panicStatus, h.statusErr = false, false, nil
	h.mu.Unlock()
	if resp := r.ask(helloReq); !resp.OK {
		t.Fatalf("resp = %+v", resp)
	}
	r2 := dialRaw(t, ts.path)
	if resp := r2.ask(statusReq); !resp.OK {
		t.Fatalf("resp = %+v", resp)
	}
	if got := ts.counters.Snapshot().Internal; got != 3 {
		t.Fatalf("Internal = %d", got)
	}
}

func TestForeignUIDIsRefused(t *testing.T) {
	h := &stubHandler{version: "v"}
	var mu sync.Mutex
	asked := 0
	ts := startServer(t, h, Options{PeerUID: func(c net.Conn) (uint32, error) {
		mu.Lock()
		asked++
		mu.Unlock()
		return uint32(os.Geteuid()) + 1, nil
	}})

	// The refusal comes before any byte is read: the peer sends nothing.
	r := dialRaw(t, ts.path)
	resp := r.resp()
	wantError(t, resp, CodeUnauthorized)
	if resp.RequestID != "" || resp.Error.Message != msgUnauthorized {
		t.Fatalf("resp = %+v", resp)
	}
	r.wantClosed()

	// A peer that sends a request at once gets the typed error, not a reset.
	c, err := Dial(context.Background(), ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Hello(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeUnauthorized {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("after unauthorized: %v", err)
	}

	snap := ts.counters.Snapshot()
	if snap.RefusedUID != 2 || snap.Accepted != 2 {
		t.Fatalf("counters = %+v", snap)
	}
	if hello, status := h.calls(); hello+status != 0 {
		t.Fatalf("handler reached by a foreign UID")
	}
}

func TestPeerUIDErrorIsRefused(t *testing.T) {
	ts := startServer(t, &stubHandler{}, Options{PeerUID: func(c net.Conn) (uint32, error) {
		return 0, errors.New("no credentials")
	}})
	r := dialRaw(t, ts.path)
	wantError(t, r.resp(), CodeUnauthorized)
	r.wantClosed()
	if got := ts.counters.Snapshot().PeerErrors; got != 1 {
		t.Fatalf("PeerErrors = %d", got)
	}
}

func TestIdleTimeout(t *testing.T) {
	ts := startServer(t, &stubHandler{}, Options{IdleTimeout: 150 * time.Millisecond})
	r := dialRaw(t, ts.path)
	if resp := r.ask(helloReq); !resp.OK {
		t.Fatal("hello failed")
	}
	start := time.Now()
	r.wantClosed()
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("closed after %v", d)
	}
	waitFor(t, func() bool { return ts.counters.Snapshot().IdleTimeouts == 1 })

	// A line that trickles in slower than the timeout does not keep the
	// connection alive: the deadline is for the whole line.
	r = dialRaw(t, ts.path)
	r.send(`{"protocol"`)
	time.Sleep(100 * time.Millisecond)
	r.send(`:"ataniq.client/1"`)
	time.Sleep(100 * time.Millisecond)
	r.wantClosed()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConnectionCap(t *testing.T) {
	ts := startServer(t, &stubHandler{}, Options{MaxConns: 2})
	a := dialRaw(t, ts.path)
	b := dialRaw(t, ts.path)
	// Both are admitted once they have been answered.
	if !a.ask(helloReq).OK || !b.ask(helloReq).OK {
		t.Fatal("hello failed")
	}
	c := dialRaw(t, ts.path)
	resp := c.resp()
	wantError(t, resp, CodeBusy)
	if resp.Error.Message != msgBusy {
		t.Fatalf("message = %q", resp.Error.Message)
	}
	c.wantClosed()
	if got := ts.counters.Snapshot().OverCap; got != 1 {
		t.Fatalf("OverCap = %d", got)
	}

	// A refused peer that sent a request first still reads the response.
	d := dialRaw(t, ts.path)
	d.send(helloReq + "\n")
	wantError(t, d.resp(), CodeBusy)

	// Closing one frees a slot.
	a.c.Close()
	waitFor(t, func() bool {
		e := dialRaw(t, ts.path)
		e.send(helloReq + "\n")
		line, err := e.readLine()
		if err != nil {
			return false
		}
		var resp Response
		return json.Unmarshal(line, &resp) == nil && resp.OK
	})
}

func TestSeveralRequestsOnOneConnection(t *testing.T) {
	h := &stubHandler{version: "v", status: sampleStatus()}
	ts := startServer(t, h, Options{})
	c, err := Dial(context.Background(), ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 5; i++ {
		if _, err := c.Hello(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Status(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// An error response in the middle leaves the connection usable.
	r := dialRaw(t, ts.path)
	r.send(helloReq + "\n" + "garbage\n" + statusReq + "\n" + `{"protocol":"ataniq.client/1","request_id":"z","op":"x"}` + "\n")
	got := []Response{r.resp(), r.resp(), r.resp(), r.resp()}
	if !got[0].OK || got[0].RequestID != "r1" || got[1].OK || !got[2].OK || got[2].RequestID != "r2" || got[3].Error == nil || got[3].RequestID != "z" {
		t.Fatalf("got = %+v", got)
	}
	snap := ts.counters.Snapshot()
	if snap.Served[OpHello] != 6 || snap.Served[OpStatus] != 6 || snap.Accepted != 2 {
		t.Fatalf("counters = %+v", snap)
	}
}

func TestShutdownByContextWithOpenConnections(t *testing.T) {
	started := make(chan struct{})
	h := &stubHandler{onStatus: func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	}}
	ts := startServer(t, h, Options{})
	idle := dialRaw(t, ts.path)
	if !idle.ask(helloReq).OK {
		t.Fatal("hello failed")
	}
	mid := dialRaw(t, ts.path)
	mid.send(`{"protocol":"ataniq.client/1","request_id":"half"`) // an unfinished line
	busy := dialRaw(t, ts.path)
	busy.send(statusReq + "\n")
	<-started
	refused := dialRaw(t, ts.path) // drained after a refusal, if any
	_ = refused

	if err := ts.stop(t); err != nil {
		t.Fatalf("Serve = %v", err)
	}
	for _, r := range []*rawConn{idle, mid} {
		r.wantClosed()
	}
	// The connection whose handler was running ends too; whatever it was
	// owed, the stream ends.
	for {
		if _, err := busy.readLine(); err != nil {
			break
		}
	}
	if _, err := os.Lstat(ts.path); !os.IsNotExist(err) {
		t.Fatalf("socket file still there: %v", err)
	}
}

func TestServeStopsWhenListenerFails(t *testing.T) {
	l, _, err := Listen(socketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), l, &stubHandler{}, Options{}) }()
	time.Sleep(20 * time.Millisecond)
	l.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve returned nil for a listener closed under it")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestClientHonoursContext(t *testing.T) {
	block := make(chan struct{})
	h := &stubHandler{onStatus: func(ctx context.Context) { <-block }}
	ts := startServer(t, h, Options{})
	defer close(block)
	c, err := Dial(context.Background(), ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Status(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Hello(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("after a failed call: %v", err)
	}
}

func TestClientCloseInterruptsACallInFlight(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	h := &stubHandler{onStatus: func(ctx context.Context) {
		entered <- struct{}{}
		<-block
	}}
	ts := startServer(t, h, Options{})
	defer close(block)
	c, err := Dial(context.Background(), ts.path)
	if err != nil {
		t.Fatal(err)
	}
	// The call has no deadline and the handler never answers.
	callErr := make(chan error, 1)
	go func() {
		_, err := c.Status(context.Background())
		callErr <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler was not reached")
	}
	closeErr := make(chan error, 1)
	go func() { closeErr <- c.Close() }()
	select {
	case err := <-closeErr:
		if err != nil {
			t.Fatalf("Close = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close is blocked by the call in flight")
	}
	select {
	case err := <-callErr:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("the call returned %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not return after Close")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}
