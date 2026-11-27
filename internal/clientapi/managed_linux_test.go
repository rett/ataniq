package clientapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type managedStub struct {
	policies           map[Principal]TargetCapabilities
	projections, calls int
	lastPrincipal      Principal
	lastTarget, lastOp string
	lastParams         json.RawMessage
}

func (m *managedStub) Capabilities(ctx context.Context) (TargetCapabilities, error) {
	m.projections++
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		panic("missing principal")
	}
	policy := m.policies[p]
	snapshot := make(TargetCapabilities, len(policy))
	for target, ops := range policy {
		snapshot[target] = slices.Clone(ops)
	}
	return snapshot, nil
}

func (m *managedStub) HandleManaged(ctx context.Context, target, op string, params json.RawMessage) (any, *ErrorBody) {
	m.calls++
	m.lastPrincipal, _ = PrincipalFromContext(ctx)
	m.lastTarget, m.lastOp, m.lastParams = target, op, params
	return "served", nil
}

func askManaged(t *testing.T, s *server, conn net.Conn, line, protocol string) Response {
	t.Helper()
	out := s.respond(context.Background(), conn, []byte(line))
	var r Response
	if err := json.Unmarshal(out, &r); err != nil || r.Protocol != protocol || r.RequestID != "r" {
		t.Fatalf("response = %s, error = %v", out, err)
	}
	return r
}

const managedHello = `{"protocol":"ataniq.client/2","request_id":"r","op":"hello"}`
const managedRead = `{"protocol":"ataniq.client/2","request_id":"r","op":"conversation.read","target":"a","params":{"page":2}}`

func TestProtocolV2PrincipalAndTargetCapabilities(t *testing.T) {
	first := Principal{Issuer: "home", ClientID: "one"}
	second := Principal{Issuer: "home", ClientID: "two"}
	caps := TargetCapabilities{"a": {"conversation.read"}, "b": {"writer.release"}}
	m := &managedStub{policies: map[Principal]TargetCapabilities{first: caps}}
	peer, other := net.Pipe()
	defer peer.Close()
	defer other.Close()
	s := NewManagedServer(m, ManagedOptions{ResolvePrincipal: func(ctx context.Context, conn net.Conn) (Principal, error) {
		if conn == peer {
			return first, nil
		}
		return second, nil
	}}).s
	hello := func(c net.Conn) ManagedHelloResult {
		t.Helper()
		r := askManaged(t, s, c, managedHello, ProtocolV2)
		var h ManagedHelloResult
		if !r.OK || json.Unmarshal(r.Result, &h) != nil {
			t.Fatalf("hello = %+v", r)
		}
		return h
	}
	got := hello(peer)
	if !reflect.DeepEqual(got.Capabilities, caps) || !reflect.DeepEqual(got.Protocols, []string{ProtocolV2}) ||
		got.Limits.MaxRequestBytes != MaxRequestBytes || got.Limits.MaxResponseBytes != MaxResponseBytes ||
		got.Limits.MaxConnections != DefaultMaxConns || got.Limits.IdleTimeoutSeconds != 30 {
		t.Fatalf("hello = %+v", got)
	}
	if got := hello(other).Capabilities; got == nil || len(got) != 0 {
		t.Fatalf("second principal sees %v", got)
	}
	wantError(t, askManaged(t, s, other, managedRead, ProtocolV2), CodeForbidden)
	wantError(t, askManaged(t, s, peer, strings.Replace(managedRead, `"a"`, `"b"`, 1), ProtocolV2), CodeForbidden)
	r := askManaged(t, s, peer, managedRead, ProtocolV2)
	if !r.OK || string(r.Result) != `"served"` || m.calls != 1 || m.lastPrincipal != first ||
		m.lastTarget != "a" || m.lastOp != "conversation.read" || string(m.lastParams) != `{"page":2}` {
		t.Fatalf("result = %+v, handler = %+v", r, m)
	}
	delete(caps, "a")
	wantError(t, askManaged(t, s, peer, managedRead, ProtocolV2), CodeForbidden)
	if m.calls != 1 {
		t.Fatalf("revoked target reached handler: %d calls", m.calls)
	}
}

func TestManagedRefusedWithoutPrincipal(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Principal
		err  error
	}{
		{"missing resolver", Principal{}, nil},
		{"empty principal", Principal{}, nil},
		{"no issuer", Principal{ClientID: "one"}, nil},
		{"no client", Principal{Issuer: "home"}, nil},
		{"failed proof", Principal{Issuer: "home", ClientID: "one"}, errors.New("private reason")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &managedStub{}
			opts := ManagedOptions{}
			if tc.name != "missing resolver" {
				opts.ResolvePrincipal = func(context.Context, net.Conn) (Principal, error) { return tc.p, tc.err }
			}
			s := NewManagedServer(m, opts).s
			for _, req := range []string{managedHello, managedRead} {
				wantError(t, askManaged(t, s, nil, req, ProtocolV2), CodeForbidden)
				req = strings.TrimSuffix(req, "}") + `,"principal":{"issuer":"home","client_id":"one"}}`
				r := askManaged(t, s, nil, req, ProtocolV2)
				wantError(t, r, CodeForbidden)
				if r.Error.Message != msgForbidden || r.Result != nil {
					t.Fatalf("response = %+v", r)
				}
			}
			if m.projections != 0 || m.calls != 0 {
				t.Fatalf("managed handler reached: %+v", m)
			}
		})
	}
}

func TestManagedTargetIdentityOnTheWire(t *testing.T) {
	p := Principal{Issuer: "home", ClientID: "one"}
	caps := TargetCapabilities{
		"�": {"conversation.read"}, "🌱": {"conversation.read"}, "empty": nil,
	}
	m := &managedStub{policies: map[Principal]TargetCapabilities{p: caps}}
	s := NewManagedServer(m, ManagedOptions{ResolvePrincipal: func(context.Context, net.Conn) (Principal, error) {
		return p, nil
	}}).s
	r := askManaged(t, s, nil, managedHello, ProtocolV2)
	var h ManagedHelloResult
	if !r.OK || json.Unmarshal(r.Result, &h) != nil || h.Capabilities["empty"] == nil {
		t.Fatalf("expected an empty operation list: %+v", r)
	}
	for _, id := range []string{`"\ud800"`, `"\udc00"`} {
		req := strings.Replace(managedRead, `"a"`, id, 1)
		wantError(t, askManaged(t, s, nil, req, ProtocolV2), CodeInvalidRequest)
	}
	if m.calls != 0 {
		t.Fatal("ill-formed target reached the handler")
	}
	for id, target := range map[string]string{`"\ufffd"`: "�", `"\ud83c\udf31"`: "🌱"} {
		req := strings.Replace(managedRead, `"a"`, id, 1)
		if r := askManaged(t, s, nil, req, ProtocolV2); !r.OK || m.lastTarget != target {
			t.Fatalf("target %s: result %+v, handler target %q", id, r, m.lastTarget)
		}
	}
}

func TestManagedHelloRequiresAddressableCapabilities(t *testing.T) {
	p := Principal{Issuer: "home", ClientID: "one"}
	for _, caps := range []TargetCapabilities{
		{"": {"conversation.read"}}, {"\xff": {"conversation.read"}}, {"a": {"status"}},
	} {
		m := &managedStub{policies: map[Principal]TargetCapabilities{p: caps}}
		s := NewManagedServer(m, ManagedOptions{ResolvePrincipal: func(context.Context, net.Conn) (Principal, error) {
			return p, nil
		}}).s
		wantError(t, askManaged(t, s, nil, managedHello, ProtocolV2), CodeInternal)
	}
}

func TestProtocolV2SeparateFromLegacy(t *testing.T) {
	m := &managedStub{}
	h := &stubHandler{version: "v"}
	resolutions := 0
	desk := NewManagedServer(m, ManagedOptions{ResolvePrincipal: func(context.Context, net.Conn) (Principal, error) {
		resolutions++
		return Principal{Issuer: "home", ClientID: "one"}, nil
	}}).s
	s := newServer(h, Options{})
	x := newRecordingExtension(true, "conversation.read")
	var err error
	s.ext, err = newExtensionSet([]Extension{x})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"protocol":"ataniq.client/1","request_id":"r1","ok":true,"result":{"protocols":["ataniq.client/1"],"limits":{"max_request_bytes":65536,"max_response_bytes":1048576,"idle_timeout_seconds":30,"max_connections":32},"operations":["hello","status","conversation.read"],"mutations":true,"version":"v"}}` + "\n"
	if got := string(s.respond(context.Background(), nil, []byte(helloReq))); got != want {
		t.Fatalf("/1 hello = %s", got)
	}
	line := strings.Replace(managedRead, ProtocolV2, Protocol, 1)
	r := askManaged(t, s, nil, line, Protocol)
	if !r.OK || len(x.calls()) != 1 {
		t.Fatalf("/1 extension = %+v", r)
	}
	wantError(t, askManaged(t, s, nil, strings.Replace(line, "conversation.read", "writer.release", 1), Protocol), CodeUnsupportedOperation)
	if m.calls != 0 || m.projections != 0 || resolutions != 0 {
		t.Fatal("/1 reached managed code")
	}
	wantError(t, askManaged(t, desk, nil, managedRead, ProtocolV2), CodeForbidden)
	wantError(t, askManaged(t, desk, nil, strings.Replace(managedHello, "hello", "status", 1), ProtocolV2), CodeUnsupportedOperation)
	if hello, status := h.calls(); hello != 1 || status != 0 || len(x.calls()) != 1 {
		t.Fatal("/2 reached a legacy handler")
	}
	r = askManaged(t, s, nil, managedHello, Protocol)
	wantError(t, r, CodeUnsupportedProtocol)
	if !reflect.DeepEqual(r.Error.SupportedProtocols, []string{Protocol}) {
		t.Fatalf("protocols = %v", r.Error.SupportedProtocols)
	}
}

func TestProtocolV2RejectsInvalidRequests(t *testing.T) {
	m := &managedStub{}
	s := NewManagedServer(m, ManagedOptions{}).s
	for _, replacement := range []string{`"Target":"a"`, `"target":""`, `"target":7`, `"target":"a","target":"b"`} {
		req := strings.Replace(managedRead, `"target":"a"`, replacement, 1)
		var r Response
		if err := json.Unmarshal(s.respond(context.Background(), nil, []byte(req)), &r); err != nil {
			t.Fatal(err)
		}
		wantError(t, r, CodeInvalidRequest)
	}
	for _, bad := range []string{"null", "7", `{"n":1,"n":2}`} {
		req := strings.Replace(managedRead, `{"page":2}`, bad, 1)
		wantError(t, askManaged(t, s, nil, req, ProtocolV2), CodeInvalidRequest)
	}
	req := strings.Replace(managedRead, `"request_id":"r"`, `"request_id":""`, 1)
	var r Response
	if err := json.Unmarshal(s.respond(context.Background(), nil, []byte(req)), &r); err != nil {
		t.Fatal(err)
	}
	wantError(t, r, CodeInvalidRequest)
	if r.Protocol != ProtocolV2 || r.RequestID != "" {
		t.Fatalf("invalid ID response = %+v", r)
	}
	r = askManaged(t, s, nil, strings.Replace(managedHello, ProtocolV2, "other", 1), ProtocolV2)
	wantError(t, r, CodeUnsupportedProtocol)
	if !reflect.DeepEqual(r.Error.SupportedProtocols, []string{ProtocolV2}) {
		t.Fatalf("protocols = %v", r.Error.SupportedProtocols)
	}
	if m.calls != 0 || m.projections != 0 {
		t.Fatal("invalid request reached managed handler")
	}
}

func TestPrincipalRecheckedOnOneConnection(t *testing.T) {
	p := Principal{Issuer: "home", ClientID: "one"}
	m := &managedStub{policies: map[Principal]TargetCapabilities{p: {"a": {"conversation.read"}}}}
	serverEnd, clientEnd := net.Pipe()
	n := 0
	s := NewManagedServer(m, ManagedOptions{ResolvePrincipal: func(ctx context.Context, c net.Conn) (Principal, error) {
		n++
		if c != serverEnd || n > 2 {
			return Principal{}, errors.New("no current proof")
		}
		return p, nil
	}}).s
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer serverEnd.Close()
		s.serveConn(context.Background(), serverEnd)
	}()
	defer func() { clientEnd.Close(); <-done }()
	r := &rawConn{t: t, c: clientEnd, br: bufio.NewReader(clientEnd)}
	for i, req := range []string{managedHello, helloReq, managedRead} {
		r.send(req + "\n")
		line, err := r.readLine()
		var resp Response
		if err != nil || json.Unmarshal(line, &resp) != nil {
			t.Fatalf("response = %q, error = %v", line, err)
		}
		switch i {
		case 0:
			if !resp.OK || resp.Protocol != ProtocolV2 {
				t.Fatal(string(line))
			}
		case 1:
			wantError(t, resp, CodeUnsupportedProtocol)
			if resp.Protocol != ProtocolV2 {
				t.Fatal(string(line))
			}
		case 2:
			wantError(t, resp, CodeForbidden)
			if resp.Protocol != ProtocolV2 {
				t.Fatal(string(line))
			}
		}
	}
	if m.calls != 0 || m.projections != 2 || n != 3 {
		t.Fatalf("handler = %+v, resolutions = %d", m, n)
	}
}

type failingManaged struct{ failure string }

func (m failingManaged) Capabilities(context.Context) (TargetCapabilities, error) {
	switch m.failure {
	case "policy error":
		return nil, errors.New("private policy error")
	case "policy panic":
		panic("private policy panic")
	case "large hello":
		return TargetCapabilities{strings.Repeat("a", MaxResponseBytes): {"conversation.read"}}, nil
	}
	return TargetCapabilities{"a": {"conversation.read"}}, nil
}

func (m failingManaged) HandleManaged(context.Context, string, string, json.RawMessage) (any, *ErrorBody) {
	switch m.failure {
	case "handler panic":
		panic("private handler panic")
	case "invalid result":
		return make(chan int), nil
	case "large result":
		return strings.Repeat("a", MaxResponseBytes), nil
	case "malformed error":
		return nil, &ErrorBody{Code: CodeBusy, Message: "invalid"}
	default:
		return nil, &ErrorBody{Code: "stale", Message: "the target changed", SupportedProtocols: []string{"private"}}
	}
}

func TestProtocolV2HandlerFailures(t *testing.T) {
	for _, failure := range []string{"verifier panic", "policy error", "policy panic", "large hello", "handler panic", "invalid result", "large result", "malformed error", "handler refusal"} {
		t.Run(failure, func(t *testing.T) {
			s := NewManagedServer(failingManaged{failure}, ManagedOptions{ResolvePrincipal: func(context.Context, net.Conn) (Principal, error) {
				if failure == "verifier panic" {
					panic("private verifier panic")
				}
				return Principal{Issuer: "home", ClientID: "one"}, nil
			}}).s
			req := managedRead
			if failure == "large hello" {
				req = managedHello
			}
			r := askManaged(t, s, nil, req, ProtocolV2)
			if failure == "handler refusal" {
				wantError(t, r, "stale")
				if r.Error.Message != "the target changed" || len(r.Error.SupportedProtocols) != 0 {
					t.Fatalf("error = %+v", r.Error)
				}
				return
			}
			wantError(t, r, CodeInternal)
			if (r.Error.Message != msgInternal && r.Error.Message != msgResponseTooLarge) || s.counters.Snapshot().Internal != 1 {
				t.Fatalf("error = %+v", r.Error)
			}
		})
	}
}
