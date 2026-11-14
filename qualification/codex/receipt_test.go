package codex

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUpstreamReceiptAccounting(t *testing.T) {
	var catalog []any
	if err := json.Unmarshal(knownCatalog, &catalog); err != nil {
		t.Fatal(err)
	}
	valid, err := json.Marshal(map[string]any{"input": []any{map[string]any{"type": "additional_tools", "tools": catalog}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		bodies    []string
		wantError string
	}{
		{"valid", []string{string(valid)}, ""},
		{"extra", []string{string(valid), string(valid)}, "unexpected upstream request 2"},
		{"invalid extra catalog", []string{string(valid), `{"input":[]}`}, "request 2: unknown catalog"},
		{"malformed", []string{`{`}, "request 1: invalid request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &requestLedger{}
			pl := &pipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
			server := &http.Server{Handler: l.handler(1, func(w http.ResponseWriter, _ int) { w.WriteHeader(http.StatusOK) })}
			go server.Serve(pl)
			defer server.Close()
			tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				c, s := net.Pipe()
				select {
				case pl.connections <- s:
					return c, nil
				case <-ctx.Done():
					c.Close()
					s.Close()
					return nil, ctx.Err()
				}
			}}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
			for _, body := range tc.bodies {
				resp, err := client.Post("http://mock/", "application/json", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			_, err := l.check(1)
			if tc.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("got %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestTerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"turn":{"status":"completed"}}`, true},
		{`{"turn":{"status":"failed"}}`, false},
		{`{"turn":{"status":"interrupted"}}`, false},
		{`{"turn":{}}`, false},
		{`{`, false},
	} {
		if got := validateTerminal(json.RawMessage(tc.body)) == nil; got != tc.valid {
			t.Errorf("%s: valid=%v", tc.body, got)
		}
	}
}

// pipeHTTP runs the same handlers as the fixture without opening a port.
func pipeHTTP(t *testing.T, h http.Handler) (*http.Server, *http.Client) {
	t.Helper()
	pl := &pipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
	s := &http.Server{Handler: h}
	go s.Serve(pl)
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		c, peer := net.Pipe()
		select {
		case pl.connections <- peer:
			return c, nil
		case <-ctx.Done():
			c.Close()
			peer.Close()
			return nil, ctx.Err()
		case <-pl.done:
			c.Close()
			peer.Close()
			return nil, net.ErrClosed
		}
	}}
	t.Cleanup(func() { tr.CloseIdleConnections(); s.Close() })
	return s, &http.Client{Transport: tr, Timeout: 2 * time.Second}
}

func TestOfflineReceiptSnapshot(t *testing.T) {
	var catalog []any
	if err := json.Unmarshal(knownCatalog, &catalog); err != nil {
		t.Fatal(err)
	}
	valid, _ := json.Marshal(map[string]any{"input": []any{map[string]any{"type": "additional_tools", "tools": catalog}}})
	for _, tc := range []struct {
		name      string
		count     int
		wantError bool
	}{{"held", 1, false}, {"early continuation", 2, true}} {
		t.Run(tc.name, func(t *testing.T) {
			l := &requestLedger{}
			_, client := pipeHTTP(t, l.handler(2, func(w http.ResponseWriter, _ int) {}))
			for i := 0; i < tc.count; i++ {
				resp, err := client.Post("http://mock/", "application/json", strings.NewReader(string(valid)))
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			if err := observeOffline(l, time.Millisecond); (err != nil) != tc.wantError {
				t.Fatalf("offline observation: %v", err)
			}
		})
	}
}

func TestFrontendRejections(t *testing.T) {
	oversized := `{}` + strings.Repeat(" ", 4<<20)
	if !json.Valid([]byte(oversized)) || !json.Valid([]byte(oversized[:4<<20])) {
		t.Fatal("oversized fixture and its first 4 MiB must be valid JSON")
	}
	for _, body := range []string{"{", strings.Repeat("x", 4<<20), oversized} {
		front, backend := &frontLedger{}, &requestLedger{}
		_, upstream := pipeHTTP(t, backend.handler(1, func(w http.ResponseWriter, _ int) {}))
		gate := &requestGate{online: true, epoch: 1, changed: make(chan struct{}), upstream: "http://mock/", client: upstream}
		_, client := pipeHTTP(t, front.handler(gate, make(chan int, 1), make(chan string, 1)))
		resp, err := client.Post("http://mock/v1/responses", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 || front.check(1) == nil {
			t.Fatalf("status=%d front=%v", resp.StatusCode, front.check(1))
		}
		if _, err := backend.check(0); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShutdownWaitsForHandlerAccounting(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	front := &frontLedger{}
	gate := &requestGate{online: true, epoch: 1, changed: make(chan struct{}), upstream: "http://mock/"}
	_, upstream := pipeHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release }))
	gate.client = upstream
	server, client := pipeHTTP(t, front.handler(gate, make(chan int, 1), make(chan string, 1)))
	finished := make(chan error, 1)
	go func() {
		resp, err := client.Post("http://mock/v1/responses", "application/json", strings.NewReader(`{}`))
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		finished <- err
	}()
	<-started
	if err := front.check(1); err == nil {
		t.Fatal("pending handler was invisible")
	}
	drained := make(chan error, 1)
	go func() { drained <- shutdownServer(server) }()
	select {
	case err := <-drained:
		t.Fatalf("shutdown returned before handler: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if err := front.check(1); err != nil {
		t.Fatalf("handler not fully accounted after drain: %v", err)
	}
}
