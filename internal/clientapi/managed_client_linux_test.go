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

// managedPair serves h with a ManagedServer over net.Pipe, no socket, and
// returns a ManagedClient for the other end.
func managedPair(t *testing.T, h ManagedHandler) *ManagedClient {
	t.Helper()
	server := NewManagedServer(h, ManagedOptions{
		ResolvePrincipal: func(context.Context, net.Conn) (Principal, error) {
			return Principal{Issuer: "fixture", ClientID: "one"}, nil
		},
	})
	client, peer := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.ServeConn(context.Background(), peer) }()
	m := NewManagedClient(client)
	t.Cleanup(func() { m.Close(); <-done })
	return m
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestManagedClientHelloAndCall(t *testing.T) {
	stub := &managedStub{policies: map[Principal]TargetCapabilities{
		{Issuer: "fixture", ClientID: "one"}: {"a": {"conversation.read"}, "b": {}},
	}}
	m := managedPair(t, stub)
	hello, err := m.Hello(testCtx(t))
	if err != nil || len(hello.Protocols) != 1 || hello.Protocols[0] != ProtocolV2 ||
		len(hello.Capabilities) != 2 || hello.Limits.MaxRequestBytes != MaxRequestBytes {
		t.Fatalf("hello = %+v, %v", hello, err)
	}
	var out string
	if err := m.Call(testCtx(t), "a", "conversation.read", map[string]int{"page": 2}, &out); err != nil || out != "served" {
		t.Fatalf("call = %q, %v", out, err)
	}
	if stub.lastTarget != "a" || stub.lastOp != "conversation.read" || string(stub.lastParams) != `{"page":2}` {
		t.Fatalf("handler saw %q %q %s", stub.lastTarget, stub.lastOp, stub.lastParams)
	}
	// An operation the capabilities do not list is the server's refusal, an
	// error response that leaves the stream open.
	var e *Error
	if err := m.Call(testCtx(t), "b", "conversation.read", nil, nil); !errors.As(err, &e) || e.Code != CodeForbidden {
		t.Fatalf("unlisted operation = %v", err)
	}
	if err := m.Call(testCtx(t), "a", "conversation.read", nil, &out); err != nil {
		t.Fatalf("call after an error response = %v", err)
	}
}

func TestManagedClientRefusesBeforeSending(t *testing.T) {
	stub := &managedStub{policies: map[Principal]TargetCapabilities{{Issuer: "fixture", ClientID: "one"}: {"a": {"x.y"}}}}
	m := managedPair(t, stub)
	for name, tc := range map[string]struct {
		target string
		params any
		want   error
	}{
		"empty target":    {"", nil, ErrBadTarget},
		"invalid UTF-8":   {"a\xff", nil, ErrBadTarget},
		"params an array": {"a", []int{1}, ErrBadParams},
		"too large":       {"a", map[string]string{"k": strings.Repeat("x", MaxRequestBytes)}, ErrRequestTooLarge},
	} {
		if err := m.Call(testCtx(t), tc.target, "x.y", tc.params, nil); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if stub.calls != 0 || stub.projections != 0 {
		t.Fatalf("the server saw %d calls and %d projections", stub.calls, stub.projections)
	}
	if err := m.Call(testCtx(t), "a", "x.y", nil, nil); err != nil {
		t.Fatalf("client unusable after refusals: %v", err)
	}
}

// answerOnce serves one line with reply and then hangs up.
func answerOnce(t *testing.T, reply func(id string) []byte) *ManagedClient {
	t.Helper()
	client, peer := net.Pipe()
	go func() {
		defer peer.Close()
		line, err := bufio.NewReader(peer).ReadBytes('\n')
		if err != nil {
			return
		}
		var req Request
		json.Unmarshal(line, &req)
		peer.Write(reply(req.RequestID))
	}()
	m := NewManagedClient(client)
	t.Cleanup(func() { m.Close() })
	return m
}

func TestManagedClientChecksTheResponse(t *testing.T) {
	v2 := func(body string) func(string) []byte {
		return func(id string) []byte {
			return []byte(strings.ReplaceAll(`{"protocol":"ataniq.client/2","request_id":"ID",`+body+"}\n", "ID", id))
		}
	}
	v1err := `{"protocol":"ataniq.client/1","request_id":"ID","ok":false,"error":{"code":"%s","message":"m"}}` + "\n"
	for name, tc := range map[string]struct {
		reply func(string) []byte
		check func(error) bool
	}{
		"wrong request id": {func(string) []byte {
			return []byte(`{"protocol":"ataniq.client/2","request_id":"other","ok":true,"result":{}}` + "\n")
		}, func(err error) bool { return errors.Is(err, ErrBadResponse) }},
		"version 1 answer": {func(id string) []byte {
			return []byte(`{"protocol":"ataniq.client/1","request_id":"` + id + `","ok":true,"result":{}}` + "\n")
		}, func(err error) bool { return errors.Is(err, ErrBadResponse) }},
		"version 1 error other than unsupported-protocol": {func(id string) []byte {
			return []byte(strings.ReplaceAll(strings.Replace(v1err, "%s", "forbidden", 1), "ID", id))
		}, func(err error) bool { return errors.Is(err, ErrBadResponse) }},
		"version 1 unsupported-protocol": {func(id string) []byte {
			return []byte(strings.ReplaceAll(strings.Replace(v1err, "%s", "unsupported-protocol", 1), "ID", id))
		}, func(err error) bool {
			var e *Error
			return errors.As(err, &e) && e.Code == CodeUnsupportedProtocol
		}},
		"result too large": {func(string) []byte {
			return []byte(strings.Repeat("x", MaxResponseBytes+1) + "\n")
		}, func(err error) bool { return errors.Is(err, ErrResponseTooLarge) }},
		"error response": {v2(`"ok":false,"error":{"code":"busy","message":"m"}`),
			func(err error) bool { var e *Error; return errors.As(err, &e) && e.Code == CodeBusy }},
	} {
		m := answerOnce(t, tc.reply)
		_, err := m.Hello(testCtx(t))
		if !tc.check(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestManagedClientClosesAfterBadTransport(t *testing.T) {
	m := answerOnce(t, func(string) []byte { return []byte("not json\n") })
	if _, err := m.Hello(testCtx(t)); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("hello = %v", err)
	}
	if _, err := m.Hello(testCtx(t)); !errors.Is(err, ErrClosed) {
		t.Fatalf("hello after failure = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	client, _ := net.Pipe()
	silent := NewManagedClient(client)
	defer silent.Close()
	if _, err := silent.Hello(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("silent peer = %v", err)
	}
}
