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
		{"--origin", "https://seat.example", "--socket", "/run/seat/http.sock", "--client-addr-header", "X-Real-IP"},
		{"--origin", "https://seat.example", "--listen", "127.0.0.1:8080"},
		{"--origin", "https://seat.example", "--listen", "[::1]:8080", "--rpid", "seat.example"},
		{"--origin", "https://seat.example", "--listen", "127.9.9.9:1"},
	}
	for _, args := range good {
		var out bytes.Buffer
		if _, code, ok := parse(args, &out); !ok || code != 0 {
			t.Errorf("%v refused: %q", args, out.String())
		}
	}
	bad := map[string][]string{
		"no listener":                            {"--origin", "https://seat.example"},
		"both listeners":                         {"--origin", "https://seat.example", "--socket", "/x", "--listen", "127.0.0.1:1"},
		"no origin":                              {"--socket", "/x"},
		"wildcard":                               {"--origin", "https://seat.example", "--listen", "0.0.0.0:8080"},
		"all interfaces":                         {"--origin", "https://seat.example", "--listen", ":8080"},
		"a public address":                       {"--origin", "https://seat.example", "--listen", "192.0.2.1:8080"},
		"a name":                                 {"--origin", "https://seat.example", "--listen", "localhost:8080"},
		"v6 wildcard":                            {"--origin", "https://seat.example", "--listen", "[::]:8080"},
		"no port":                                {"--origin", "https://seat.example", "--listen", "127.0.0.1"},
		"port zero":                              {"--origin", "https://seat.example", "--listen", "127.0.0.1:0"},
		"socket without a client address header": {"--origin", "https://seat.example", "--socket", "/x"},
		"bad mode":                               {"--origin", "https://seat.example", "--socket", "/x", "--client-addr-header", "X-Real-IP", "--socket-mode", "rw"},
		"mode too wide":                          {"--origin", "https://seat.example", "--socket", "/x", "--client-addr-header", "X-Real-IP", "--socket-mode", "1777"},
		"stray argument":                         {"--origin", "https://seat.example", "--socket", "/x", "--client-addr-header", "X-Real-IP", "more"},
		"unknown flag":                           {"--origin", "https://seat.example", "--socket", "/x", "--client-addr-header", "X-Real-IP", "--token", "x"},
		"origin without host":                    {"--origin", "seat", "--socket", "/x", "--client-addr-header", "X-Real-IP"},
		"both origins":                           {"--origin", "https://seat.example", "--origin-file", "/x", "--socket", "/x"},
		"missing origin file":                    {"--origin-file", "/nonexistent/origin", "--socket", "/x"},
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
	o, _, _ := parse([]string{"--origin", "https://seat.example:8443", "--socket", "/x", "--client-addr-header", "X-Real-IP"}, io.Discard)
	if o.rpid != "seat.example" {
		t.Errorf("rpid defaults to %q", o.rpid)
	}
}

func TestOriginFileAndNoDefaultOrigin(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "origin")
	os.WriteFile(f, []byte("https://seat.example\n"), 0o600)
	o, _, ok := parse([]string{"--origin-file", f, "--socket", "/x", "--client-addr-header", "X-Real-IP"}, io.Discard)
	if !ok || o.origin != "https://seat.example" || o.rpid != "seat.example" {
		t.Errorf("origin file: %+v %v", o, ok)
	}
	big := filepath.Join(dir, "big")
	os.WriteFile(big, bytes.Repeat([]byte("a"), maxOriginFile+1), 0o600)
	if _, _, ok := parse([]string{"--origin-file", big, "--socket", "/x", "--client-addr-header", "X-Real-IP"}, io.Discard); ok {
		t.Error("a large origin file was accepted")
	}
	if _, _, ok := parse([]string{"--origin-file", dir, "--socket", "/x", "--client-addr-header", "X-Real-IP"}, io.Discard); ok {
		t.Error("a directory was accepted as the origin file")
	}
	// The origin has no default, and there is no flag to loosen its checks.
	if _, _, ok := parse([]string{"--socket", "/x", "--client-addr-header", "X-Real-IP"}, io.Discard); ok {
		t.Error("no origin was accepted")
	}
	if _, _, ok := parse([]string{"--origin", "https://seat.example", "--socket", "/x", "--insecure"}, io.Discard); ok {
		t.Error("an insecure flag exists")
	}
	// A bad origin is refused by the app before anything listens.
	for _, origin := range []string{"http://seat.example", "https://seat.example:8443", "https://seat.example/x", "https://127.0.0.1"} {
		var out bytes.Buffer
		ready := false
		code := run(context.Background(), []string{"--origin", origin, "--listen", "127.0.0.1:9"}, &out, func(net.Addr) { ready = true })
		if code != 2 || ready {
			t.Errorf("%s: code %d, ready %v", origin, code, ready)
		}
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
	_, stderr, stop := serve(t, []string{"--origin", "https://seat.example", "--socket", sock, "--client-addr-header", "X-Real-IP", "--socket-mode", "0600"})
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
	_, _, stop := serve(t, []string{"--origin", "https://seat.example", "--socket", sock, "--client-addr-header", "X-Real-IP"})
	// A second instance on the same path is refused while the first runs.
	var out bytes.Buffer
	if code := run(context.Background(), []string{"--origin", "https://seat.example", "--socket", sock, "--client-addr-header", "X-Real-IP"}, &out, nil); code != 1 {
		t.Errorf("second instance: code %d, %q", code, out.String())
	}
	stop()
	// A regular file at the path is never removed.
	f := filepath.Join(dir, "file")
	os.WriteFile(f, []byte("x"), 0o600)
	if code := run(context.Background(), []string{"--origin", "https://seat.example", "--socket", f, "--client-addr-header", "X-Real-IP"}, &out, nil); code != 1 {
		t.Errorf("regular file: code %d", code)
	}
	if _, err := os.Stat(f); err != nil {
		t.Error("the file was removed")
	}
}

// TestSocketNeedsAClientAddressHeader: a client of a Unix socket has no
// address, so without a header the proxy sets every client would share one
// rate-limit bucket, and one forged-Origin client could use it up for all.
func TestSocketNeedsAClientAddressHeader(t *testing.T) {
	var out bytes.Buffer
	if _, code, ok := parse([]string{"--origin", "https://seat.example", "--socket", "/x"}, &out); ok || code != 2 {
		t.Errorf("accepted (code %d)", code)
	}
	if !strings.Contains(out.String(), "--client-addr-header") {
		t.Errorf("the diagnostic does not name the flag: %q", out.String())
	}
	ready := false
	var out2 bytes.Buffer
	code := run(context.Background(), []string{"--origin", "https://seat.example", "--socket", filepath.Join(t.TempDir(), "s")}, &out2, func(net.Addr) { ready = true })
	if code != 2 || ready {
		t.Errorf("run: code %d, ready %v", code, ready)
	}
	// A loopback listener needs none: its peers have addresses.
	if _, _, ok := parse([]string{"--origin", "https://seat.example", "--listen", "127.0.0.1:8080"}, io.Discard); !ok {
		t.Error("--listen was refused without the header")
	}
	if _, _, ok := parse([]string{"--origin", "https://seat.example", "--socket", "/x", "--client-addr-header", "X-Real-IP"}, io.Discard); !ok {
		t.Error("--socket was refused with the header")
	}
}

func TestParseNotifySocket(t *testing.T) {
	base := []string{"--origin", "https://seat.example", "--listen", "127.0.0.1:8080"}
	gate := []string{"--gate", "gate.example:7443", "--gate-pin-file", pinFile(t, strings.Repeat("ab", 32)+"\n", 0o600), "--identity", "/x"}
	with := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	var errout bytes.Buffer
	o, _, ok := parse(with(base, gate, []string{"--notify-socket", "/run/seat-notify/n.sock"}), &errout)
	if !ok || o.notifySocket != "/run/seat-notify/n.sock" {
		t.Fatalf("a good socket refused: %q", errout.String())
	}
	if o, _, ok := parse(with(base, gate), io.Discard); !ok || o.notifySocket != "" {
		t.Error("without the flag the socket is not empty")
	}
	for name, args := range map[string][]string{
		"relative":     with(base, gate, []string{"--notify-socket", "n.sock"}),
		"dot relative": with(base, gate, []string{"--notify-socket", "./n.sock"}),
		"no gate":      with(base, []string{"--notify-socket", "/run/seat-notify/n.sock"}),
	} {
		var out bytes.Buffer
		if _, code, ok := parse(args, &out); ok || code != 2 {
			t.Errorf("%s: accepted (code %d)", name, code)
		}
		if !strings.Contains(out.String(), "Usage: ataniq-seat") || !strings.Contains(out.String(), "--notify-socket") {
			t.Errorf("%s: no usage in %q", name, out.String())
		}
	}
}
