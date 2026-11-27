package clientapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"testing"
)

// pipeRequests exchanges real frames on one stream, including any buffered
// state between hello and the next request.
func pipeRequests(t *testing.T, serve func(net.Conn)) func(string) Response {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer serverEnd.Close()
		serve(serverEnd)
	}()
	t.Cleanup(func() { clientEnd.Close(); <-done })
	r := &rawConn{t: t, c: clientEnd, br: bufio.NewReader(clientEnd)}
	return func(request string) Response {
		t.Helper()
		r.send(request + "\n")
		line, err := r.readLine()
		if err != nil {
			t.Fatal(err)
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
}

func TestCF01(t *testing.T) {
	owner := Principal{Issuer: "home", ClientID: "owner"}
	reader := Principal{Issuer: "home", ClientID: "reader"}
	policies := map[Principal]TargetCapabilities{
		owner:  {"private": {"conversation.read"}},
		reader: {"a": {"conversation.read"}},
	}
	resolve := func(context.Context, net.Conn) (Principal, error) { return reader, nil }
	managed := &managedStub{policies: policies}
	desk := NewManagedServer(managed, ManagedOptions{ResolvePrincipal: resolve})
	deskStream := func(c net.Conn) { desk.ServeConn(context.Background(), c) }

	t.Run("V1AfterHello", func(t *testing.T) {
		ask := pipeRequests(t, deskStream)
		if r := ask(managedHello); !r.OK || r.Protocol != ProtocolV2 {
			t.Fatalf("managed hello = %+v", r)
		}
		for _, req := range []string{statusReq, helloReq, strings.Replace(managedRead, ProtocolV2, Protocol, 1)} {
			r := ask(req)
			wantError(t, r, CodeUnsupportedProtocol)
			if r.Protocol != ProtocolV2 || !reflect.DeepEqual(r.Error.SupportedProtocols, []string{ProtocolV2}) {
				t.Fatalf("downgrade response = %+v, error = %+v", r, r.Error)
			}
		}
		if r := ask(managedRead); !r.OK {
			t.Fatalf("authorized read after refusal = %+v", r)
		}
		wantError(t, pipeRequests(t, deskStream)(statusReq), CodeUnsupportedProtocol)
	})

	t.Run("ScratchInventory", func(t *testing.T) {
		h := &stubHandler{status: StatusResult{Daemon: DaemonStatus{RegistryRevision: 1},
			Leads: []LeadStatus{{Role: "scratch/lead"}}}}
		scratch := newServer(h, Options{})
		var err error
		x := newRecordingExtension(false, "scratch.list")
		scratch.ext, err = newExtensionSet([]Extension{x})
		if err != nil {
			t.Fatal(err)
		}
		ask := pipeRequests(t, func(c net.Conn) { scratch.serveConn(context.Background(), c) })
		r := ask(helloReq)
		var hello HelloResult
		dec := json.NewDecoder(bytes.NewReader(r.Result))
		dec.DisallowUnknownFields()
		if !r.OK || dec.Decode(&hello) != nil ||
			!reflect.DeepEqual(hello.Operations, []string{OpHello, OpStatus, "scratch.list"}) ||
			!reflect.DeepEqual(hello.Protocols, []string{Protocol}) {
			t.Fatalf("scratch hello = %s", r.Result)
		}
		r = ask(statusReq)
		var status StatusResult
		dec = json.NewDecoder(bytes.NewReader(r.Result))
		dec.DisallowUnknownFields()
		if !r.OK || dec.Decode(&status) != nil || !reflect.DeepEqual(status, h.status) {
			t.Fatalf("scratch status = %s", r.Result)
		}
		r = ask(managedHello)
		wantError(t, r, CodeUnsupportedProtocol)
		if r.Protocol != Protocol || !reflect.DeepEqual(r.Error.SupportedProtocols, []string{Protocol}) {
			t.Fatalf("scratch protocol refusal = %+v", r)
		}
		wantError(t, ask(managedRead), CodeUnsupportedProtocol)
		wantError(t, ask(strings.Replace(managedRead, ProtocolV2, Protocol, 1)), CodeUnsupportedOperation)
		if r := ask(strings.Replace(helloReq, OpHello, "scratch.list", 1)); !r.OK || len(x.calls()) != 1 {
			t.Fatalf("scratch extension = %+v", r)
		}
	})

	t.Run("ForgedPrincipal", func(t *testing.T) {
		ask := pipeRequests(t, deskStream)
		foreign := strings.Replace(managedRead, `"a"`, `"private"`, 1)
		claim := `"principal":{"issuer":"home","client_id":"owner"}`
		before := managed.calls
		for _, req := range []string{managedHello, managedRead, foreign} {
			wantError(t, ask(strings.TrimSuffix(req, "}")+","+claim+"}"), CodeForbidden)
		}
		wantError(t, ask(strings.Replace(foreign, `"page":2`, claim, 1)), CodeForbidden)
		if managed.calls != before {
			t.Fatal("forged principal reached the operation handler")
		}
	})
}

// The result changes the grant while the server encodes the reply, after the
// operation was admitted but before any bytes have been handed to the stream.
type revokeOnMarshal func()

func (revoke revokeOnMarshal) MarshalJSON() ([]byte, error) {
	revoke()
	return []byte(`"private response"`), nil
}

type revokingRead struct {
	*managedStub
	revoke func()
}

func (m revokingRead) HandleManaged(context.Context, string, string, json.RawMessage) (any, *ErrorBody) {
	return revokeOnMarshal(m.revoke), nil
}

func TestManagedDeliveryRechecks(t *testing.T) {
	p := Principal{Issuer: "home", ClientID: "one"}
	q := Principal{Issuer: "home", ClientID: "two"}
	for _, change := range []string{"target revoked", "operation removed", "principal replaced"} {
		t.Run(change, func(t *testing.T) {
			current := p
			caps := TargetCapabilities{"a": {"conversation.read"}}
			stub := &managedStub{policies: map[Principal]TargetCapabilities{p: caps, q: caps}}
			called := false
			h := revokingRead{stub, func() {
				called = true
				switch change {
				case "target revoked":
					delete(caps, "a")
				case "operation removed":
					caps["a"][0] = "different.op"
				case "principal replaced":
					current = q
				}
			}}
			m := NewManagedServer(h, ManagedOptions{ResolvePrincipal: func(context.Context, net.Conn) (Principal, error) {
				return current, nil
			}})
			ask := pipeRequests(t, func(c net.Conn) { m.ServeConn(context.Background(), c) })
			r := ask(managedRead)
			wantError(t, r, CodeForbidden)
			if !called || r.Result != nil || r.Error.Message != msgForbidden {
				t.Fatalf("result after %s = %+v", change, r)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		revoke func(TargetCapabilities)
	}{
		{"hello projection revoked", func(caps TargetCapabilities) { caps["a"][0] = "different.op" }},
		{"hello inventory target revoked", func(caps TargetCapabilities) { delete(caps, "inventory") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := TargetCapabilities{"a": {"conversation.read"}, "inventory": {}}
			stub := &managedStub{policies: map[Principal]TargetCapabilities{p: caps}}
			m := NewManagedServer(stub, ManagedOptions{ResolvePrincipal: func(context.Context, net.Conn) (Principal, error) {
				if stub.projections == 1 {
					tc.revoke(caps)
				}
				return p, nil
			}})
			r := askManaged(t, m.s, nil, managedHello, ProtocolV2)
			wantError(t, r, CodeForbidden)
			if r.Result != nil {
				t.Fatalf("stale hello = %s", r.Result)
			}
		})
	}
}

func TestManagedRefusalCounters(t *testing.T) {
	for _, tc := range []struct {
		name, request          string
		code                   ErrorCode
		forbidden, unsupported uint64
	}{
		{"parse", strings.TrimSuffix(managedHello, "}") + `,"principal":{}}`, CodeForbidden, 1, 0},
		{"dispatch", strings.Replace(managedRead, `"a"`, `"absent"`, 1), CodeForbidden, 1, 0},
		{"delivery", managedRead, CodeForbidden, 1, 0},
		{"unsupported protocol", strings.Replace(managedHello, ProtocolV2, Protocol, 1), CodeUnsupportedProtocol, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Principal{Issuer: "home", ClientID: "reader"}
			caps := TargetCapabilities{"a": {"conversation.read"}}
			stub := &managedStub{policies: map[Principal]TargetCapabilities{p: caps}}
			h := revokingRead{stub, func() { delete(caps, "a") }}
			var counters Counters
			m := NewManagedServer(h, ManagedOptions{
				Counters:         &counters,
				ResolvePrincipal: func(context.Context, net.Conn) (Principal, error) { return p, nil },
			})
			wantError(t, askManaged(t, m.s, nil, tc.request, ProtocolV2), tc.code)
			snap := counters.Snapshot()
			if snap.Forbidden != tc.forbidden || snap.Unsupported != tc.unsupported {
				t.Fatalf("forbidden = %d, unsupported = %d, want %d and %d",
					snap.Forbidden, snap.Unsupported, tc.forbidden, tc.unsupported)
			}
		})
	}
}

func TestManagedStreamLimits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManagedServer(&managedStub{}, ManagedOptions{
		MaxConns: 1,
		ResolvePrincipal: func(context.Context, net.Conn) (Principal, error) {
			return Principal{Issuer: "home", ClientID: "one"}, nil
		},
	})
	stopped := make(chan struct{})
	first := pipeRequests(t, func(c net.Conn) {
		defer close(stopped)
		m.ServeConn(ctx, c)
	})
	if r := first(managedHello); !r.OK {
		t.Fatalf("first stream hello = %+v", r)
	}
	serverEnd, clientEnd := net.Pipe()
	defer clientEnd.Close()
	go m.ServeConn(context.Background(), serverEnd)
	r := &rawConn{t: t, c: clientEnd, br: bufio.NewReader(clientEnd)}
	line, err := r.readLine()
	var refusal Response
	if err != nil || json.Unmarshal(line, &refusal) != nil || refusal.Protocol != ProtocolV2 {
		t.Fatalf("over capacity = %q, error = %v", line, err)
	}
	wantError(t, refusal, CodeBusy)
	r.wantClosed()
	cancel()
	<-stopped
	next := pipeRequests(t, func(c net.Conn) { m.ServeConn(context.Background(), c) })
	tooLarge := next(strings.Repeat("x", MaxRequestBytes+1))
	wantError(t, tooLarge, CodeTooLarge)
	if tooLarge.Protocol != ProtocolV2 {
		t.Fatalf("oversized frame answered in %s", tooLarge.Protocol)
	}
}
