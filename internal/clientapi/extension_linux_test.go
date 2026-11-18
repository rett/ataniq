package clientapi

import (
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

// recordingExtension is a fakeExtension that records what reaches Handle.
type recordingExtension struct {
	fakeExtension
	mu     sync.Mutex
	ops    []string
	params []json.RawMessage
	ctxs   []context.Context
}

func newRecordingExtension(mutating bool, ops ...string) *recordingExtension {
	e := &recordingExtension{fakeExtension: fakeExtension{ops: ops, mutating: mutating}}
	e.fakeExtension.handle = func(ctx context.Context, op string, params json.RawMessage) (any, *ErrorBody) {
		e.mu.Lock()
		e.ops = append(e.ops, op)
		e.params = append(e.params, append(json.RawMessage(nil), params...))
		e.ctxs = append(e.ctxs, ctx)
		e.mu.Unlock()
		return map[string]any{"op": op, "params": params}, nil
	}
	return e
}

func (e *recordingExtension) calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ops...)
}

func helloOf(t *testing.T, resp Response) HelloResult {
	t.Helper()
	if !resp.OK {
		t.Fatalf("hello failed: %+v", resp.Error)
	}
	var h HelloResult
	if err := json.Unmarshal(resp.Result, &h); err != nil {
		t.Fatal(err)
	}
	return h
}

func extReq(id, op, params string) string {
	line := `{"protocol":"ataniq.client/1","request_id":"` + id + `","op":"` + op + `"`
	if params != "" {
		line += `,"params":` + params
	}
	return line + "}"
}

func TestServerWithoutExtensionsAnswersByteForByte(t *testing.T) {
	for name, extra := range map[string][]Extension{
		"nil":        nil,
		"empty":      {},
		"no ops":     {&fakeExtension{}},
		"only a nil": nil,
	} {
		h := &stubHandler{version: "v-test", status: StatusResult{}}
		ts := startServer(t, h, Options{Extra: extra})
		r := dialRaw(t, ts.path)
		r.send(helloReq + "\n")
		line, err := r.readLine()
		if err != nil {
			t.Fatal(err)
		}
		want := `{"protocol":"ataniq.client/1","request_id":"r1","ok":true,"result":{"protocols":["ataniq.client/1"],` +
			`"limits":{"max_request_bytes":65536,"max_response_bytes":1048576,"idle_timeout_seconds":30,"max_connections":32},` +
			`"operations":["hello","status"],"mutations":false,"version":"v-test"}}` + "\n"
		if string(line) != want {
			t.Fatalf("%s: hello line\n%s\nwant\n%s", name, line, want)
		}
		// An extension's name is unsupported when none is registered.
		wantError(t, r.ask(extReq("r2", "x.run", `{}`)), CodeUnsupportedOperation)
		snap := ts.counters.Snapshot()
		if len(snap.Served) != 2 || snap.Served[OpHello] != 1 {
			t.Fatalf("%s: served = %v", name, snap.Served)
		}
	}
}

func TestHelloListsExtensionOperationsInOrder(t *testing.T) {
	a := newRecordingExtension(false, "x.one", "x.two")
	b := newRecordingExtension(false, "y.three")
	ts := startServer(t, &stubHandler{version: "v"}, Options{Extra: []Extension{a, b}})
	r := dialRaw(t, ts.path)
	h := helloOf(t, r.ask(helloReq))
	if got := strings.Join(h.Operations, " "); got != "hello status x.one x.two y.three" {
		t.Fatalf("operations = %q", got)
	}
	if h.Mutations {
		t.Fatal("mutations true with no mutating extension")
	}
	if h.Version != "v" || len(h.Protocols) != 1 {
		t.Fatalf("hello = %+v", h)
	}
}

func TestHelloMutationsFollowsTheRegisteredExtensions(t *testing.T) {
	for name, tc := range map[string]struct {
		flags []bool
		want  bool
	}{
		"one quiet":           {[]bool{false}, false},
		"one mutating":        {[]bool{true}, true},
		"mutating then quiet": {[]bool{true, false}, true},
		"quiet then mutating": {[]bool{false, true}, true},
		"both quiet":          {[]bool{false, false}, false},
	} {
		var exts []Extension
		for i, m := range tc.flags {
			exts = append(exts, newRecordingExtension(m, "x"+string(rune('a'+i))+".run"))
		}
		ts := startServer(t, &stubHandler{}, Options{Extra: exts})
		h := helloOf(t, dialRaw(t, ts.path).ask(helloReq))
		if h.Mutations != tc.want {
			t.Errorf("%s: mutations = %v, want %v", name, h.Mutations, tc.want)
		}
		ts.stop(t)
	}
}

func TestHelloStillOverridesWhatTheHandlerCannotOwnWithExtensions(t *testing.T) {
	ext := newRecordingExtension(true, "x.run")
	ts := startServer(t, &lyingHandler{}, Options{Extra: []Extension{ext}})
	h := helloOf(t, dialRaw(t, ts.path).ask(helloReq))
	if got := strings.Join(h.Operations, " "); got != "hello status x.run" || !h.Mutations {
		t.Fatalf("hello = %+v", h)
	}
}

func TestExtensionRequestReachesHandle(t *testing.T) {
	ext := newRecordingExtension(false, "x.run", "x.other")
	h := &stubHandler{}
	ts := startServer(t, h, Options{Extra: []Extension{ext}})
	r := dialRaw(t, ts.path)

	resp := r.ask(extReq("a1", "x.run", `{"role":"r","n":[1,2]}`))
	if !resp.OK || resp.RequestID != "a1" {
		t.Fatalf("resp = %+v", resp)
	}
	var got struct {
		Op     string          `json:"op"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil || got.Op != "x.run" || string(got.Params) != `{"role":"r","n":[1,2]}` {
		t.Fatalf("result = %s (%v)", resp.Result, err)
	}

	// No params: Handle gets nil.
	if resp := r.ask(extReq("a2", "x.other", "")); !resp.OK {
		t.Fatalf("resp = %+v", resp)
	}
	if hello, status := h.calls(); hello+status != 0 {
		t.Fatalf("the base handler was reached: %d %d", hello, status)
	}
	if ops := ext.calls(); strings.Join(ops, " ") != "x.run x.other" {
		t.Fatalf("ops = %v", ops)
	}
	ext.mu.Lock()
	second := ext.params[1]
	ext.mu.Unlock()
	if second != nil && len(second) != 0 {
		t.Fatalf("params without a member = %q", second)
	}
	snap := ts.counters.Snapshot()
	if snap.Served["x.run"] != 1 || snap.Served["x.other"] != 1 || snap.Served[OpHello] != 0 {
		t.Fatalf("served = %v", snap.Served)
	}
}

func TestExtensionServedCountersAreListedFromTheStart(t *testing.T) {
	ts := startServer(t, &stubHandler{}, Options{Extra: []Extension{newRecordingExtension(false, "x.run")}})
	waitFor(t, func() bool { _, ok := ts.counters.Snapshot().Served["x.run"]; return ok })
	if n := ts.counters.Snapshot().Served["x.run"]; n != 0 {
		t.Fatalf("served = %d", n)
	}
}

func TestExtensionParamsThatAreNotAnObjectAreInvalid(t *testing.T) {
	ext := newRecordingExtension(false, "x.run")
	ts := startServer(t, &stubHandler{}, Options{Extra: []Extension{ext}})
	r := dialRaw(t, ts.path)
	for _, p := range []string{`null`, `[]`, `"{}"`, `5`, `true`, `{"a":1,"a":2}`} {
		resp := r.ask(extReq("p", "x.run", p))
		wantError(t, resp, CodeInvalidRequest)
		if resp.Error.Message != msgInvalidRequest || resp.RequestID != "p" {
			t.Fatalf("params %s: resp = %+v", p, resp)
		}
	}
	// The connection stays open and Handle was never reached.
	if resp := r.ask(extReq("ok", "x.run", `{}`)); !resp.OK {
		t.Fatalf("resp = %+v", resp)
	}
	if ops := ext.calls(); len(ops) != 1 {
		t.Fatalf("Handle was reached %d times", len(ops))
	}
	if n := ts.counters.Snapshot().Invalid; n != 6 {
		t.Fatalf("invalid = %d", n)
	}
}

func TestHelloAndStatusIgnoreParams(t *testing.T) {
	ext := newRecordingExtension(false, "x.run")
	h := &stubHandler{version: "v"}
	ts := startServer(t, h, Options{Extra: []Extension{ext}})
	r := dialRaw(t, ts.path)
	for _, p := range []string{`5`, `[1]`, `null`, `{"a":1}`, `{"a":1,"a":2}`} {
		if resp := r.ask(extReq("h", "hello", p)); !resp.OK {
			t.Fatalf("hello with params %s: %+v", p, resp)
		}
		if resp := r.ask(extReq("s", "status", p)); !resp.OK {
			t.Fatalf("status with params %s: %+v", p, resp)
		}
	}
	if len(ext.calls()) != 0 {
		t.Fatal("an extension was reached")
	}
}

func TestUnregisteredExtensionOperationIsUnsupported(t *testing.T) {
	ext := newRecordingExtension(false, "x.run")
	ts := startServer(t, &stubHandler{}, Options{Extra: []Extension{ext}})
	r := dialRaw(t, ts.path)
	for _, op := range []string{"x.other", "X.run", "x.run ", "x", "x.run.more", "approve"} {
		resp := r.ask(extReq("u", op, `{}`))
		wantError(t, resp, CodeUnsupportedOperation)
		if resp.Error.Message != msgUnsupportedOperation {
			t.Fatalf("%q: %+v", op, resp.Error)
		}
	}
	if len(ext.calls()) != 0 {
		t.Fatal("an extension was reached")
	}
}

func TestExtensionRequestPassesThePeerCheck(t *testing.T) {
	ext := newRecordingExtension(true, "x.run")
	ts := startServer(t, &stubHandler{}, Options{
		Extra:   []Extension{ext},
		PeerUID: func(net.Conn) (uint32, error) { return uint32(os.Geteuid()) + 1, nil },
	})
	// A peer that sends at once still gets the typed refusal and no handler call.
	c, err := Dial(context.Background(), ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call(context.Background(), "x.run", map[string]string{"a": "b"}, nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeUnauthorized {
		t.Fatalf("err = %v", err)
	}
	if len(ext.calls()) != 0 {
		t.Fatal("an extension was reached by a foreign UID")
	}

	// A peer whose UID cannot be read is refused too.
	ext2 := newRecordingExtension(true, "x.run")
	ts2 := startServer(t, &stubHandler{}, Options{
		Extra:   []Extension{ext2},
		PeerUID: func(net.Conn) (uint32, error) { return 0, errors.New("no credentials") },
	})
	c2, err := Dial(context.Background(), ts2.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	err = c2.Call(context.Background(), "x.run", nil, nil)
	if !errors.As(err, &e) || e.Code != CodeUnauthorized {
		t.Fatalf("err = %v", err)
	}
	if len(ext2.calls()) != 0 {
		t.Fatal("an extension was reached by an unreadable UID")
	}
}

func TestExtensionRequestOverTheCapIsTooLarge(t *testing.T) {
	ext := newRecordingExtension(false, "x.run")
	ts := startServer(t, &stubHandler{}, Options{Extra: []Extension{ext}})
	r := dialRaw(t, ts.path)
	// A line one byte over the cap, params included.
	head := `{"protocol":"ataniq.client/1","request_id":"b","op":"x.run","params":{"k":"`
	tail := `"}}`
	pad := MaxRequestBytes + 1 - len(head) - len(tail)
	r.send(head + strings.Repeat("a", pad) + tail + "\n")
	resp := r.resp()
	wantError(t, resp, CodeTooLarge)
	r.wantClosed()
	if len(ext.calls()) != 0 || ts.counters.Snapshot().TooLarge != 1 {
		t.Fatalf("calls=%v counters=%+v", ext.calls(), ts.counters.Snapshot())
	}

	// A line exactly at the cap is served.
	r = dialRaw(t, ts.path)
	pad = MaxRequestBytes - len(head) - len(tail)
	r.send(head + strings.Repeat("a", pad) + tail + "\n")
	if resp := r.resp(); !resp.OK {
		t.Fatalf("at the cap: %+v", resp)
	}
}

func TestExtensionRequestIsSubjectToTheIdleTimeoutAndTheConnectionCap(t *testing.T) {
	ext := newRecordingExtension(false, "x.run")
	ts := startServer(t, &stubHandler{}, Options{
		Extra:       []Extension{ext},
		IdleTimeout: 100 * time.Millisecond,
		MaxConns:    1,
	})
	r := dialRaw(t, ts.path)
	if resp := r.ask(extReq("a", "x.run", `{}`)); !resp.OK {
		t.Fatalf("resp = %+v", resp)
	}
	// A second connection is over the cap.
	r2 := dialRaw(t, ts.path)
	wantError(t, r2.resp(), CodeBusy)
	// The first idles out, with no response.
	r.wantClosed()
	waitFor(t, func() bool { return ts.counters.Snapshot().IdleTimeouts == 1 })
}

func TestExtensionHandleRunsWithTheServersContext(t *testing.T) {
	entered := make(chan struct{})
	released := make(chan context.Context, 1)
	ext := &fakeExtension{ops: []string{"x.wait"}}
	ext.handle = func(ctx context.Context, op string, params json.RawMessage) (any, *ErrorBody) {
		close(entered)
		<-ctx.Done()
		released <- ctx
		return nil, nil
	}
	ts := startServer(t, &stubHandler{}, Options{Extra: []Extension{ext}})
	r := dialRaw(t, ts.path)
	r.send(extReq("w", "x.wait", "") + "\n")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Handle not reached")
	}
	// Serve waits for Handle, which returns when the server's context ends.
	if err := ts.stop(t); err != nil {
		t.Fatalf("Serve = %v", err)
	}
	select {
	case ctx := <-released:
		if ctx.Err() == nil {
			t.Fatal("Handle returned before its context ended")
		}
	default:
		t.Fatal("Handle did not return")
	}
}

func TestExtensionPanicAnswersInternal(t *testing.T) {
	ext := &fakeExtension{ops: []string{"x.boom", "x.ok"}}
	ext.handle = func(ctx context.Context, op string, params json.RawMessage) (any, *ErrorBody) {
		if op == "x.boom" {
			panic("secret text of the panic")
		}
		return map[string]int{"n": 1}, nil
	}
	ts := startServer(t, &stubHandler{}, Options{Extra: []Extension{ext}})
	r := dialRaw(t, ts.path)
	r.send(extReq("p", "x.boom", `{"a":"echo-me"}`) + "\n")
	line, err := r.readLine()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), "secret") || strings.Contains(string(line), "echo-me") {
		t.Fatalf("the response echoes text: %s", line)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatal(err)
	}
	wantError(t, resp, CodeInternal)
	if resp.Error.Message != msgInternal || resp.RequestID != "p" {
		t.Fatalf("resp = %+v", resp)
	}
	// The connection and the server go on.
	if resp := r.ask(extReq("q", "x.ok", "")); !resp.OK {
		t.Fatalf("after the panic: %+v", resp)
	}
	snap := ts.counters.Snapshot()
	if snap.Internal != 1 || snap.Served["x.boom"] != 0 || snap.Served["x.ok"] != 1 {
		t.Fatalf("counters = %+v", snap)
	}
}

func TestExtensionResultThatCannotBeMarshalledOrIsTooBigAnswersInternal(t *testing.T) {
	ext := &fakeExtension{ops: []string{"x.chan", "x.big", "x.edge"}}
	ext.handle = func(ctx context.Context, op string, params json.RawMessage) (any, *ErrorBody) {
		switch op {
		case "x.chan":
			return make(chan int), nil
		case "x.big":
			return strings.Repeat("a", MaxResponseBytes), nil
		}
		// Fills the response line to its cap exactly.
		const fixed = len(`{"protocol":"ataniq.client/1","request_id":"e","ok":true,"result":""}` + "\n")
		return strings.Repeat("a", MaxResponseBytes-fixed), nil
	}
	ts := startServer(t, &stubHandler{}, Options{Extra: []Extension{ext}})
	r := dialRaw(t, ts.path)

	resp := r.ask(extReq("c", "x.chan", ""))
	wantError(t, resp, CodeInternal)
	if resp.Error.Message != msgInternal {
		t.Fatalf("resp = %+v", resp.Error)
	}
	resp = r.ask(extReq("b", "x.big", ""))
	wantError(t, resp, CodeInternal)
	if resp.Error.Message != msgResponseTooLarge {
		t.Fatalf("resp = %+v", resp.Error)
	}
	// A response at the cap is sent.
	r.send(extReq("e", "x.edge", "") + "\n")
	line, err := r.readLine()
	if err != nil || len(line) != MaxResponseBytes {
		t.Fatalf("edge: %d bytes, %v", len(line), err)
	}
	if n := ts.counters.Snapshot().Internal; n != 2 {
		t.Fatalf("internal = %d", n)
	}
}

func TestExtensionFailIsSentWithItsCode(t *testing.T) {
	var body *ErrorBody
	var mu sync.Mutex
	ext := &fakeExtension{ops: []string{"x.run"}}
	ext.handle = func(ctx context.Context, op string, params json.RawMessage) (any, *ErrorBody) {
		mu.Lock()
		defer mu.Unlock()
		return "ignored", body
	}
	ts := startServer(t, &stubHandler{}, Options{Extra: []Extension{ext}})
	r := dialRaw(t, ts.path)
	set := func(b *ErrorBody) { mu.Lock(); body = b; mu.Unlock() }

	set(&ErrorBody{Code: "refused", Message: "the role is not the configured one", SupportedProtocols: []string{"other/1"}})
	resp := r.ask(extReq("f", "x.run", ""))
	wantError(t, resp, "refused")
	if resp.Error.Message != "the role is not the configured one" || len(resp.Error.SupportedProtocols) != 0 || resp.RequestID != "f" {
		t.Fatalf("resp = %+v", resp)
	}
	if len(resp.Result) != 0 {
		t.Fatalf("a failing response carries a result: %s", resp.Result)
	}

	// A code of the protocol's own that does not end a connection may be used.
	set(&ErrorBody{Code: CodeInvalidRequest, Message: "a field is missing"})
	wantError(t, r.ask(extReq("g", "x.run", "")), CodeInvalidRequest)

	// Malformed bodies are "internal".
	for name, b := range map[string]*ErrorBody{
		"empty code":      {Code: "", Message: "m"},
		"upper case":      {Code: "Refused", Message: "m"},
		"spaces":          {Code: "a b", Message: "m"},
		"control":         {Code: "a\nb", Message: "m"},
		"long":            {Code: ErrorCode(strings.Repeat("a", 65)), Message: "m"},
		"unauthorized":    {Code: CodeUnauthorized, Message: "m"},
		"busy":            {Code: CodeBusy, Message: "m"},
		"too-large":       {Code: CodeTooLarge, Message: "m"},
		"unsupported-pro": {Code: CodeUnsupportedProtocol, Message: "m"},
		"message newline": {Code: "refused", Message: "a\nb"},
		"message escape":  {Code: "refused", Message: "\x1b[2J"},
		"message long":    {Code: "refused", Message: strings.Repeat("a", maxExtensionMessage+1)},
	} {
		set(b)
		resp := r.ask(extReq("m", "x.run", ""))
		wantError(t, resp, CodeInternal)
		if resp.Error.Message != msgInternal {
			t.Errorf("%s: %+v", name, resp.Error)
		}
	}
	if n := ts.counters.Snapshot().Internal; n != 12 {
		t.Fatalf("internal = %d", n)
	}
}

func TestServeRefusesBadExtensionsBeforeItAccepts(t *testing.T) {
	for name, exts := range map[string][]Extension{
		"nil extension":       {nil},
		"bad name":            {newRecordingExtension(false, "Canary.start")},
		"no dot":              {newRecordingExtension(false, "canary")},
		"two dots":            {newRecordingExtension(false, "a.b.c")},
		"empty part":          {newRecordingExtension(false, "a.")},
		"base hello":          {newRecordingExtension(false, "hello")},
		"base status":         {newRecordingExtension(false, "status")},
		"repeated in one":     {newRecordingExtension(false, "a.b", "a.b")},
		"repeated across two": {newRecordingExtension(false, "a.b"), newRecordingExtension(true, "c.d", "a.b")},
		"panicking":           {&fakeExtension{panicOps: true}},
	} {
		t.Run(name, func(t *testing.T) {
			l, path, err := Listen(socketDir(t))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- Serve(context.Background(), l, &stubHandler{}, Options{Extra: exts}) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("Serve returned nil")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Serve did not refuse")
			}
			// Nothing was accepted, and the listener was closed.
			if c, err := net.Dial("unix", path); err == nil {
				c.Close()
				t.Fatal("the socket still accepts")
			}
			for _, e := range exts {
				if re, ok := e.(*recordingExtension); ok && len(re.calls()) != 0 {
					t.Fatal("Handle was called")
				}
			}
		})
	}
}

func TestExtensionServedThroughTheClient(t *testing.T) {
	type params struct {
		Role string `json:"role"`
	}
	type result struct {
		Role  string `json:"role"`
		State string `json:"state"`
	}
	ext := &fakeExtension{ops: []string{"x.define", "x.fail"}, mutating: true}
	ext.handle = func(ctx context.Context, op string, raw json.RawMessage) (any, *ErrorBody) {
		var p params
		if len(raw) == 0 {
			// No params member: the zero value.
		} else if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &ErrorBody{Code: CodeInvalidRequest, Message: "the parameters are not valid"}
		}
		if op == "x.fail" {
			return nil, &ErrorBody{Code: "refused", Message: "a fixed text"}
		}
		return result{Role: p.Role, State: "defined"}, nil
	}
	ts := startServer(t, &stubHandler{version: "v"}, Options{Extra: []Extension{ext}})
	ctx := context.Background()
	c, err := Dial(ctx, ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	h, err := c.Hello(ctx)
	if err != nil || !h.Mutations || strings.Join(h.Operations, " ") != "hello status x.define x.fail" {
		t.Fatalf("hello = %+v, %v", h, err)
	}
	var out result
	if err := c.Call(ctx, "x.define", params{Role: "r"}, &out); err != nil || out != (result{Role: "r", State: "defined"}) {
		t.Fatalf("call = %+v, %v", out, err)
	}
	// A nil result discards the answer.
	if err := c.Call(ctx, "x.define", params{Role: "r"}, nil); err != nil {
		t.Fatal(err)
	}
	var e *Error
	if err := c.Call(ctx, "x.fail", params{}, nil); !errors.As(err, &e) || e.Code != "refused" || e.Message != "a fixed text" {
		t.Fatalf("err = %v", err)
	}
	if err := c.Call(ctx, "x.define", nil, nil); err != nil {
		t.Fatalf("no params: %v", err)
	}
	// An operation nobody serves is the typed error, and the client goes on.
	if err := c.Call(ctx, "x.none", nil, nil); !errors.As(err, &e) || e.Code != CodeUnsupportedOperation {
		t.Fatalf("err = %v", err)
	}
	// The base operations still work on the same connection.
	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	snap := ts.counters.Snapshot()
	if snap.Served["x.define"] != 3 || snap.Served["x.fail"] != 0 || snap.Served[OpStatus] != 1 || snap.Unsupported != 1 {
		t.Fatalf("counters = %+v", snap)
	}
}

func TestClientCallOnBaseOperations(t *testing.T) {
	ts := startServer(t, &stubHandler{version: "v", status: sampleStatus()}, Options{})
	ctx := context.Background()
	c, err := Dial(ctx, ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var h HelloResult
	if err := c.Call(ctx, OpHello, nil, &h); err != nil || h.Version != "v" {
		t.Fatalf("hello = %+v, %v", h, err)
	}
	var st StatusResult
	if err := c.Call(ctx, OpStatus, map[string]int{"ignored": 1}, &st); err != nil || len(st.Leads) != 2 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	var e *Error
	if err := c.Call(ctx, "x.run", nil, nil); !errors.As(err, &e) || e.Code != CodeUnsupportedOperation {
		t.Fatalf("err = %v", err)
	}
}

func TestExtensionRequestsFromSeveralConnectionsAtOnce(t *testing.T) {
	ext := &fakeExtension{ops: []string{"x.run"}}
	ts := startServer(t, &stubHandler{}, Options{Extra: []Extension{ext}})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := Dial(context.Background(), ts.path)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			for j := 0; j < 20; j++ {
				var out map[string]string
				if err := c.Call(context.Background(), "x.run", map[string]int{"j": j}, &out); err != nil || out["op"] != "x.run" {
					t.Errorf("call: %v %v", out, err)
					return
				}
				if _, err := c.Hello(context.Background()); err != nil {
					t.Errorf("hello: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if n := ts.counters.Snapshot().Served["x.run"]; n != 160 {
		t.Fatalf("served = %d", n)
	}
}
