package clientapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// stubHandler is a Handler whose answers and failures the test chooses.
type stubHandler struct {
	mu          sync.Mutex
	helloCalls  int
	statusCalls int
	version     string
	status      StatusResult
	statusErr   error
	panicHello  bool
	panicStatus bool
	onStatus    func(ctx context.Context)
}

func (h *stubHandler) Hello(ctx context.Context) HelloResult {
	h.mu.Lock()
	h.helloCalls++
	p := h.panicHello
	v := h.version
	h.mu.Unlock()
	if p {
		panic("hello panic with a secret text")
	}
	return HelloResult{Version: v}
}

func (h *stubHandler) Status(ctx context.Context) (StatusResult, error) {
	h.mu.Lock()
	h.statusCalls++
	p, st, err, hook := h.panicStatus, h.status, h.statusErr, h.onStatus
	h.mu.Unlock()
	if p {
		panic("status panic")
	}
	if hook != nil {
		hook(ctx)
	}
	return st, err
}

func (h *stubHandler) calls() (hello, status int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.helloCalls, h.statusCalls
}

// socketDir returns a folder path for a socket that is short enough for a Unix
// socket address. The folder is not created.
func socketDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	if len(SocketPath(dir)) > maxSocketPath {
		base, err := os.MkdirTemp("", "ca")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(base) })
		dir = filepath.Join(base, "run")
	}
	return dir
}

type testServer struct {
	path     string
	counters *Counters
	cancel   context.CancelFunc
	done     chan error
}

// startServer listens in a fresh folder and serves h until the test ends.
func startServer(t *testing.T, h Handler, opts Options) *testServer {
	t.Helper()
	l, path, err := Listen(socketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if opts.Counters == nil {
		opts.Counters = &Counters{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	ts := &testServer{path: path, counters: opts.Counters, cancel: cancel, done: make(chan error, 1)}
	go func() { ts.done <- Serve(ctx, l, h, opts) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ts.done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after its context ended")
		}
	})
	return ts
}

// stop ends the server and waits for Serve to return.
func (ts *testServer) stop(t *testing.T) error {
	t.Helper()
	ts.cancel()
	select {
	case err := <-ts.done:
		ts.done <- err
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context ended")
	}
	return nil
}

// rawConn is a connection that speaks the wire by hand.
type rawConn struct {
	t  *testing.T
	c  net.Conn
	br *bufio.Reader
}

func dialRaw(t *testing.T, path string) *rawConn {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &rawConn{t: t, c: c, br: bufio.NewReader(c)}
}

func (r *rawConn) send(s string) {
	r.t.Helper()
	r.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := r.c.Write([]byte(s)); err != nil {
		r.t.Fatalf("write: %v", err)
	}
}

// readLine reads one response line.
func (r *rawConn) readLine() ([]byte, error) {
	r.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := r.br.ReadBytes('\n')
	return line, err
}

// resp reads and decodes one response.
func (r *rawConn) resp() Response {
	r.t.Helper()
	line, err := r.readLine()
	if err != nil {
		r.t.Fatalf("read response: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		r.t.Fatalf("response %q: %v", line, err)
	}
	if resp.Protocol != Protocol {
		r.t.Fatalf("response protocol %q", resp.Protocol)
	}
	return resp
}

// ask sends one line and reads its response.
func (r *rawConn) ask(line string) Response {
	r.t.Helper()
	r.send(line + "\n")
	return r.resp()
}

// wantClosed asserts that the server closed the connection without sending
// anything more.
func (r *rawConn) wantClosed() {
	r.t.Helper()
	line, err := r.readLine()
	if err == nil {
		r.t.Fatalf("expected the connection closed, read %q", line)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		r.t.Fatal("connection still open")
	}
}

func wantError(t *testing.T, resp Response, code ErrorCode) {
	t.Helper()
	if resp.OK || resp.Error == nil || resp.Error.Code != code {
		t.Fatalf("response = %+v (error %+v), want error %s", resp, resp.Error, code)
	}
}

const helloReq = `{"protocol":"ataniq.client/1","request_id":"r1","op":"hello"}`
const statusReq = `{"protocol":"ataniq.client/1","request_id":"r2","op":"status"}`

func newTestReader(data []byte) *bufio.Reader {
	return bufio.NewReaderSize(bytes.NewReader(data), 16)
}
