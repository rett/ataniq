package clientapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestValidOperationName(t *testing.T) {
	for name, ok := range map[string]bool{
		"canary.start":     true,
		"a.b":              true,
		"a-b.c-d":          true,
		"a1.2b":            true,
		"-.-":              true,
		"":                 false,
		".":                false,
		"hello":            false,
		"status":           false,
		"a.":               false,
		".b":               false,
		"a.b.c":            false,
		"a..b":             false,
		"A.b":              false,
		"a.B":              false,
		"a_b.c":            false,
		"a b.c":            false,
		"a.b\n":            false,
		"a.b ":             false,
		"ä.b":              false,
		"a/b.c":            false,
		"canary.start\x00": false,
	} {
		if got := validOperationName(name); got != ok {
			t.Errorf("validOperationName(%q) = %v, want %v", name, got, ok)
		}
	}
}

func TestValidExtensionCode(t *testing.T) {
	for code, ok := range map[ErrorCode]bool{
		"refused":                          true,
		"unavailable":                      true,
		"a":                                true,
		"a-1":                              true,
		CodeInvalidRequest:                 true,
		CodeInternal:                       true,
		CodeUnsupportedOperation:           true,
		ErrorCode(strings.Repeat("a", 64)): true,
		ErrorCode(strings.Repeat("a", 65)): false,
		"":                                 false,
		"1a":                               false,
		"-a":                               false,
		"Refused":                          false,
		"a_b":                              false,
		"a b":                              false,
		"a\n":                              false,
		"ä":                                false,
		CodeUnauthorized:                   false,
		CodeBusy:                           false,
		CodeTooLarge:                       false,
		CodeUnsupportedProtocol:            false,
	} {
		if got := validExtensionCode(code); got != ok {
			t.Errorf("validExtensionCode(%q) = %v, want %v", code, got, ok)
		}
	}
}

func TestValidExtensionMessage(t *testing.T) {
	for name, tc := range map[string]struct {
		msg string
		ok  bool
	}{
		"plain":        {"the role is not the configured one", true},
		"empty":        {"", true},
		"utf8":         {"café", true},
		"newline":      {"a\nb", false},
		"escape":       {"a\x1b[31m", false},
		"delete":       {"a\x7f", false},
		"c1 control":   {"a\u0085", false},
		"bad utf8":     {"a\xff", false},
		"at the cap":   {strings.Repeat("a", maxExtensionMessage), true},
		"over the cap": {strings.Repeat("a", maxExtensionMessage+1), false},
	} {
		if got := validExtensionMessage(tc.msg); got != tc.ok {
			t.Errorf("%s: validExtensionMessage = %v, want %v", name, got, tc.ok)
		}
	}
}

// fakeExtension is an Extension whose names and answers the test chooses.
type fakeExtension struct {
	ops      []string
	mutating bool
	handle   func(ctx context.Context, op string, params json.RawMessage) (any, *ErrorBody)
	panicOps bool
}

func (e *fakeExtension) Operations() []string {
	if e.panicOps {
		panic("operations panic")
	}
	return e.ops
}
func (e *fakeExtension) Mutating() bool { return e.mutating }
func (e *fakeExtension) Handle(ctx context.Context, op string, params json.RawMessage) (any, *ErrorBody) {
	if e.handle == nil {
		return map[string]string{"op": op}, nil
	}
	return e.handle(ctx, op, params)
}

func TestNewExtensionSet(t *testing.T) {
	ok := func(ops ...string) Extension { return &fakeExtension{ops: ops} }
	for name, tc := range map[string]struct {
		exts []Extension
		bad  bool
	}{
		"none":                       {nil, false},
		"empty":                      {[]Extension{}, false},
		"one":                        {[]Extension{ok("a.b", "a.c")}, false},
		"two":                        {[]Extension{ok("a.b"), ok("c.d")}, false},
		"no operations":              {[]Extension{ok()}, true},
		"no operations among others": {[]Extension{ok("a.b"), &fakeExtension{mutating: true}}, true},
		"nil extension":              {[]Extension{nil}, true},
		"bad name":                   {[]Extension{ok("a.b", "Ab.c")}, true},
		"no dot":                     {[]Extension{ok("canary")}, true},
		"two dots":                   {[]Extension{ok("a.b.c")}, true},
		"base hello":                 {[]Extension{ok("hello")}, true},
		"base status":                {[]Extension{ok("status")}, true},
		"repeated in one":            {[]Extension{ok("a.b", "a.b")}, true},
		"repeated across two":        {[]Extension{ok("a.b"), ok("c.d", "a.b")}, true},
		"panicking":                  {[]Extension{&fakeExtension{panicOps: true}}, true},
	} {
		set, err := newExtensionSet(tc.exts)
		if (err != nil) != tc.bad {
			t.Errorf("%s: err = %v, want error %v", name, err, tc.bad)
		}
		if err == nil && set == nil {
			t.Errorf("%s: no set", name)
		}
	}
}

func TestNewExtensionSetKeepsTheOrderAndReadsMutations(t *testing.T) {
	a := &fakeExtension{ops: []string{"x.one", "x.two"}}
	b := &fakeExtension{ops: []string{"y.three"}, mutating: true}
	set, err := newExtensionSet([]Extension{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(set.operations(), " "); got != "hello status x.one x.two y.three" {
		t.Fatalf("operations = %q", got)
	}
	if !set.mutating() {
		t.Fatal("not mutating with a mutating extension")
	}
	// The list is read once: the extension's slice changing later changes
	// nothing.
	a.ops[0] = "z.changed"
	if !set.has("x.one") || set.has("z.changed") {
		t.Fatal("the set follows the extension's slice")
	}
	// A nil set is the no-extension case.
	var none *extensionSet
	if none.has("a.b") || none.mutating() || strings.Join(none.operations(), " ") != "hello status" {
		t.Fatal("a nil set is not empty")
	}
}

func TestDecodeRequestExtParams(t *testing.T) {
	set, err := newExtensionSet([]Extension{&fakeExtension{ops: []string{"x.run"}}})
	if err != nil {
		t.Fatal(err)
	}
	head := `{"protocol":"ataniq.client/1","request_id":"a","op":`
	for name, tc := range map[string]struct {
		line   string
		op     string
		params string
		code   ErrorCode
	}{
		"absent":              {head + `"x.run"}`, "x.run", "", ""},
		"empty object":        {head + `"x.run","params":{}}`, "x.run", `{}`, ""},
		"object":              {head + `"x.run","params": {"a": [1, {"b": null}]} }`, "x.run", `{"a": [1, {"b": null}]}`, ""},
		"nested repeated key": {head + `"x.run","params":{"a":{"k":1,"k":2}}}`, "x.run", `{"a":{"k":1,"k":2}}`, ""},
		"null":                {head + `"x.run","params":null}`, "", "", CodeInvalidRequest},
		"array":               {head + `"x.run","params":[]}`, "", "", CodeInvalidRequest},
		"string":              {head + `"x.run","params":"{}"}`, "", "", CodeInvalidRequest},
		"number":              {head + `"x.run","params":5}`, "", "", CodeInvalidRequest},
		"repeated key":        {head + `"x.run","params":{"a":1,"a":2}}`, "", "", CodeInvalidRequest},
		"repeated escaped":    {head + `"x.run","params":{"a":1,"a":2}}`, "", "", CodeInvalidRequest},
		"params twice":        {head + `"x.run","params":{},"params":{}}`, "", "", CodeInvalidRequest},
		"hello ignores":       {head + `"hello","params":5}`, "hello", "", ""},
		"status ignores":      {head + `"status","params":[1,1]}`, "status", "", ""},
		"hello repeated":      {head + `"hello","params":{"a":1,"a":2}}`, "hello", "", ""},
		"unregistered":        {head + `"x.other","params":{}}`, "", "", CodeUnsupportedOperation},
		"base like":           {head + `"approve"}`, "", "", CodeUnsupportedOperation},
		"case differs":        {head + `"X.run"}`, "", "", CodeUnsupportedOperation},
	} {
		id, op, params, fail := decodeRequestExt([]byte(tc.line), set)
		if tc.code != "" {
			if fail == nil || fail.Code != tc.code || op != "" || params != nil {
				t.Errorf("%s: op=%q params=%q fail=%+v", name, op, params, fail)
			}
			continue
		}
		if fail != nil || id != "a" || op != tc.op || string(params) != tc.params {
			t.Errorf("%s: id=%q op=%q params=%q fail=%+v", name, id, op, params, fail)
		}
	}
	// Without a set the extension's name is unsupported, as before.
	_, _, _, fail := decodeRequestExt([]byte(head+`"x.run"}`), nil)
	if fail == nil || fail.Code != CodeUnsupportedOperation {
		t.Fatalf("without a set: %+v", fail)
	}
	// The base decoder answers as it did.
	_, op, fail := decodeRequest([]byte(head + `"hello","params":5}`))
	if fail != nil || op != OpHello {
		t.Fatalf("decodeRequest: op=%q fail=%+v", op, fail)
	}
}

func TestRequestLineIsTodaysWithoutParams(t *testing.T) {
	b, err := json.Marshal(Request{Protocol: Protocol, RequestID: "c-1", Op: OpHello})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"protocol":"ataniq.client/1","request_id":"c-1","op":"hello"}` {
		t.Fatalf("line = %s", got)
	}
	b, err = json.Marshal(Request{Protocol: Protocol, RequestID: "c-2", Op: "x.run", Params: json.RawMessage(`{"a":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"protocol":"ataniq.client/1","request_id":"c-2","op":"x.run","params":{"a":1}}` {
		t.Fatalf("line = %s", got)
	}
}

// pipeClient returns a Client on one end of an in-memory pipe, and a function
// that serves the other end: it reads one request line, passes it to answer and
// writes back the line answer returns (nothing when it returns ""). It returns
// the request line it read.
func pipeClient(t *testing.T) (c *Client, serve func(answer func(line string) string) string) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	c = &Client{conn: a, br: bufio.NewReaderSize(a, 4096)}
	br := bufio.NewReader(b)
	return c, func(answer func(line string) string) string {
		b.SetDeadline(time.Now().Add(5 * time.Second))
		line, err := br.ReadString('\n')
		if err != nil {
			t.Errorf("fake server read: %v", err)
			return ""
		}
		if out := answer(line); out != "" {
			if _, err := b.Write([]byte(out)); err != nil {
				t.Errorf("fake server write: %v", err)
			}
		}
		return line
	}
}

func TestClientRequestLines(t *testing.T) {
	type result struct{ Op string }
	for name, tc := range map[string]struct {
		call func(c *Client) error
		line string
	}{
		"hello": {func(c *Client) error { _, err := c.Hello(context.Background()); return err },
			`{"protocol":"ataniq.client/1","request_id":"c-1","op":"hello"}` + "\n"},
		"status": {func(c *Client) error { _, err := c.Status(context.Background()); return err },
			`{"protocol":"ataniq.client/1","request_id":"c-1","op":"status"}` + "\n"},
		"call without params": {func(c *Client) error { return c.Call(context.Background(), "x.run", nil, &result{}) },
			`{"protocol":"ataniq.client/1","request_id":"c-1","op":"x.run"}` + "\n"},
		"call with a typed nil": {func(c *Client) error {
			var p *struct{}
			return c.Call(context.Background(), "x.run", p, &result{})
		}, `{"protocol":"ataniq.client/1","request_id":"c-1","op":"x.run"}` + "\n"},
		"call with a struct": {func(c *Client) error {
			return c.Call(context.Background(), "x.run", struct {
				Role string `json:"role"`
			}{"r"}, &result{})
		}, `{"protocol":"ataniq.client/1","request_id":"c-1","op":"x.run","params":{"role":"r"}}` + "\n"},
		"call with a map": {func(c *Client) error {
			return c.Call(context.Background(), "x.run", map[string]any{"b": 1, "a": "x"}, &result{})
		}, `{"protocol":"ataniq.client/1","request_id":"c-1","op":"x.run","params":{"a":"x","b":1}}` + "\n"},
		"call with raw params": {func(c *Client) error {
			return c.Call(context.Background(), "x.run", json.RawMessage(`{"a":1}`), &result{})
		}, `{"protocol":"ataniq.client/1","request_id":"c-1","op":"x.run","params":{"a":1}}` + "\n"},
	} {
		t.Run(name, func(t *testing.T) {
			c, serve := pipeClient(t)
			errc := make(chan error, 1)
			go func() { errc <- tc.call(c) }()
			got := serve(func(line string) string {
				switch {
				case strings.Contains(line, `"op":"hello"`):
					return `{"protocol":"ataniq.client/1","request_id":"c-1","ok":true,"result":{}}` + "\n"
				case strings.Contains(line, `"op":"status"`):
					return `{"protocol":"ataniq.client/1","request_id":"c-1","ok":true,"result":{"leads":[]}}` + "\n"
				}
				return `{"protocol":"ataniq.client/1","request_id":"c-1","ok":true,"result":{"Op":"x.run"}}` + "\n"
			})
			if err := <-errc; err != nil {
				t.Fatalf("call: %v", err)
			}
			if got != tc.line {
				t.Fatalf("line = %q\nwant   %q", got, tc.line)
			}
		})
	}
}

func TestClientCallRefusesParamsThatAreNotAnObjectWithoutSending(t *testing.T) {
	c, _ := pipeClient(t) // nothing reads the pipe: a send would block until the deadline
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for name, params := range map[string]any{
		"array":   []int{1},
		"string":  "{}",
		"number":  5,
		"bool":    true,
		"channel": make(chan int),
	} {
		err := c.Call(ctx, "x.run", params, nil)
		if !errors.Is(err, ErrBadParams) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// An object that makes the line too long is refused too, and the client
	// stays usable.
	err := c.Call(ctx, "x.run", map[string]string{"k": strings.Repeat("a", MaxRequestBytes)}, nil)
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("too large: err = %v", err)
	}
	if c.closed.Load() {
		t.Fatal("the client closed itself")
	}
	if c.seq != 0 {
		t.Fatalf("a refused call used request id %d", c.seq)
	}
}

func TestClientCallLineCapIsTheServers(t *testing.T) {
	// The largest object that fits the cap is sent; one byte more is not.
	base := len(`{"protocol":"ataniq.client/1","request_id":"c-1","op":"x.run","params":{"k":""}}`)
	fill := MaxRequestBytes - base
	c, serve := pipeClient(t)
	errc := make(chan error, 1)
	go func() {
		errc <- c.Call(context.Background(), "x.run", map[string]string{"k": strings.Repeat("a", fill)}, nil)
	}()
	line := serve(func(string) string {
		return `{"protocol":"ataniq.client/1","request_id":"c-1","ok":true,"result":{}}` + "\n"
	})
	if err := <-errc; err != nil {
		t.Fatalf("at the cap: %v", err)
	}
	if len(line)-1 != MaxRequestBytes {
		t.Fatalf("line is %d bytes, want %d", len(line)-1, MaxRequestBytes)
	}
	err := c.Call(context.Background(), "x.run", map[string]string{"k": strings.Repeat("a", fill+1)}, nil)
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
}

func TestClientCallErrorResponseIsTheTypedError(t *testing.T) {
	c, serve := pipeClient(t)
	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "x.run", nil, nil) }()
	serve(func(string) string {
		return `{"protocol":"ataniq.client/1","request_id":"c-1","ok":false,"error":{"code":"refused","message":"no"}}` + "\n"
	})
	var e *Error
	if err := <-errc; !errors.As(err, &e) || e.Code != "refused" || e.Message != "no" {
		t.Fatalf("err = %v", e)
	}
	if c.closed.Load() {
		t.Fatal("an error response closed the client")
	}
}

func TestClientCallResultDoesNotFit(t *testing.T) {
	c, serve := pipeClient(t)
	errc := make(chan error, 1)
	var out struct{ N int }
	go func() { errc <- c.Call(context.Background(), "x.run", nil, &out) }()
	serve(func(string) string {
		return `{"protocol":"ataniq.client/1","request_id":"c-1","ok":true,"result":{"N":"text"}}` + "\n"
	})
	if err := <-errc; !errors.Is(err, ErrBadResponse) {
		t.Fatalf("err = %v", err)
	}
	if !c.closed.Load() {
		t.Fatal("a bad response left the client open")
	}
}

// FuzzDecodeRequestExt drives the decoder with an extension registered: an
// operation is dispatched only when it is a base one or registered, and params
// come back only as one object.
func FuzzDecodeRequestExt(f *testing.F) {
	for _, s := range []string{
		`{"protocol":"ataniq.client/1","request_id":"a","op":"x.run"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"x.run","params":{}}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"x.run","params":{"a":1,"a":2}}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"x.run","params":[]}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","params":5}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"x.run","params":{"\ud800":1}}`,
	} {
		f.Add([]byte(s))
	}
	set, err := newExtensionSet([]Extension{&fakeExtension{ops: []string{"x.run"}}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		id, op, params, fail := decodeRequestExt(line, set)
		if fail != nil {
			if op != "" || params != nil || (id != "" && !validRequestID(id)) {
				t.Fatalf("failed decode returned op %q params %q id %q", op, params, id)
			}
			return
		}
		if op != OpHello && op != OpStatus && op != "x.run" {
			t.Fatalf("dispatched %q", op)
		}
		if !validRequestID(id) {
			t.Fatalf("id %q", id)
		}
		if params != nil && (op != "x.run" || params[0] != '{' || !json.Valid(params) || repeatsKey(params)) {
			t.Fatalf("params %q with op %q", params, op)
		}
	})
}
