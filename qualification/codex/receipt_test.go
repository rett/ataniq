package codex

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
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
			l := &requestLedger{arrival: make(chan struct{}, 1)}
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
			client := &http.Client{Transport: tr}
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
