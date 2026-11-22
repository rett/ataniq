package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	good := [][]string{
		{"--origin", "https://seat.example", "--socket", "/run/seat/http.sock"},
		{"--origin", "https://seat.example", "--listen", "127.0.0.1:8080"},
		{"--origin", "https://seat.example", "--listen", "[::1]:8080", "--rpid", "example"},
		{"--origin", "https://seat.example", "--listen", "127.9.9.9:1"},
	}
	for _, args := range good {
		var out bytes.Buffer
		if _, code, ok := parse(args, &out); !ok || code != 0 {
			t.Errorf("%v refused: %q", args, out.String())
		}
	}
	bad := map[string][]string{
		"no listener":         {"--origin", "https://seat.example"},
		"both listeners":      {"--origin", "https://seat.example", "--socket", "/x", "--listen", "127.0.0.1:1"},
		"no origin":           {"--socket", "/x"},
		"wildcard":            {"--origin", "https://seat.example", "--listen", "0.0.0.0:8080"},
		"all interfaces":      {"--origin", "https://seat.example", "--listen", ":8080"},
		"a public address":    {"--origin", "https://seat.example", "--listen", "192.0.2.1:8080"},
		"a name":              {"--origin", "https://seat.example", "--listen", "localhost:8080"},
		"v6 wildcard":         {"--origin", "https://seat.example", "--listen", "[::]:8080"},
		"no port":             {"--origin", "https://seat.example", "--listen", "127.0.0.1"},
		"port zero":           {"--origin", "https://seat.example", "--listen", "127.0.0.1:0"},
		"bad mode":            {"--origin", "https://seat.example", "--socket", "/x", "--socket-mode", "rw"},
		"mode too wide":       {"--origin", "https://seat.example", "--socket", "/x", "--socket-mode", "1777"},
		"stray argument":      {"--origin", "https://seat.example", "--socket", "/x", "more"},
		"unknown flag":        {"--origin", "https://seat.example", "--socket", "/x", "--token", "x"},
		"origin without host": {"--origin", "seat", "--socket", "/x"},
	}
	for name, args := range bad {
		var out bytes.Buffer
		if _, code, ok := parse(args, &out); ok || code != 2 {
			t.Errorf("%s: accepted (code %d)", name, code)
		}
		if !strings.Contains(out.String(), "Usage: ataniq-seat") {
			t.Errorf("%s: no usage in %q", name, out.String())
		}
	}
	o, _, _ := parse([]string{"--origin", "https://seat.example:8443", "--socket", "/x"}, io.Discard)
	if o.rpid != "seat.example" {
		t.Errorf("rpid defaults to %q", o.rpid)
	}
}

func TestRunRefusesNonLoopbackBeforeListening(t *testing.T) {
	var out bytes.Buffer
	ready := false
	code := run(context.Background(), []string{"--origin", "https://seat.example", "--listen", "0.0.0.0:0"}, &out, func(net.Addr) { ready = true })
	if code != 2 || ready {
		t.Errorf("code %d, ready %v", code, ready)
	}
}

// serve runs the command with args until the test ends and returns its address.
func serve(t *testing.T, args []string) (addr net.Addr, stderr *bytes.Buffer, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stderr = &bytes.Buffer{}
	got := make(chan net.Addr, 1)
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, stderr, func(a net.Addr) { got <- a }) }()
	select {
	case addr = <-got:
	case c := <-done:
		cancel()
		t.Fatalf("run ended with %d: %s", c, stderr.String())
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("no listener")
	}
	stop = func() int {
		cancel()
		select {
		case c := <-done:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("no shutdown")
			return -1
		}
	}
	return addr, stderr, stop
}

func TestServeOnUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "seat")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "http.sock")
	_, stderr, stop := serve(t, []string{"--origin", "https://seat.example", "--socket", sock, "--socket-mode", "0600"})
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v", fi.Mode().Perm())
	}
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := c.Get("http://seat/healthz")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "ok\n" || resp.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("healthz: %q %v", b, resp.Header)
	}
	// No request detail is printed.
	if code := stop(); code != 0 {
		t.Errorf("shutdown code %d", code)
	}
	if _, err := os.Stat(sock); err == nil {
		t.Error("the socket file is left")
	}
	if strings.Contains(stderr.String(), "healthz") {
		t.Errorf("a request is logged: %q", stderr.String())
	}
}

func TestServeOnLoopback(t *testing.T) {
	// Port 0 is refused by the flags, so a free port is found first.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free := l.Addr().String()
	l.Close()
	addr, _, stop := serve(t, []string{"--origin", "https://seat.example", "--listen", free})
	resp, err := http.Get("http://" + addr.String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
	if code := stop(); code != 0 {
		t.Errorf("shutdown code %d", code)
	}
}

func TestStaleSocketIsReplacedAndOthersRefused(t *testing.T) {
	dir, err := os.MkdirTemp("", "seat")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	// A socket file nothing answers on: made, then its listener closed
	// without unlinking.
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	_, _, stop := serve(t, []string{"--origin", "https://seat.example", "--socket", sock})
	// A second instance on the same path is refused while the first runs.
	var out bytes.Buffer
	if code := run(context.Background(), []string{"--origin", "https://seat.example", "--socket", sock}, &out, nil); code != 1 {
		t.Errorf("second instance: code %d, %q", code, out.String())
	}
	stop()
	// A regular file at the path is never removed.
	f := filepath.Join(dir, "file")
	os.WriteFile(f, []byte("x"), 0o600)
	if code := run(context.Background(), []string{"--origin", "https://seat.example", "--socket", f}, &out, nil); code != 1 {
		t.Errorf("regular file: code %d", code)
	}
	if _, err := os.Stat(f); err != nil {
		t.Error("the file was removed")
	}
}
