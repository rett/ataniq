package seatnotice

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// sockPath returns a short socket path in a private folder.
func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "n.sock")
}

// serving is a server under test.
type serving struct {
	path string
	h    *FakeHandler
	stop func()
}

func startServer(t *testing.T, cfg ServeConfig, h *FakeHandler) *serving {
	t.Helper()
	path := sockPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("no Unix sockets: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, h, cfg) }()
	s := &serving{path: path, h: h}
	var once sync.Once
	s.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Serve ended with %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Serve did not end")
			}
		})
	}
	t.Cleanup(s.stop)
	return s
}

func (s *serving) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(s.path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func uid() uint32 { return uint32(os.Getuid()) }

func TestRoundTrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("peer credentials are Linux only")
	}
	h := &FakeHandler{}
	s := startServer(t, ServeConfig{AllowedUID: uid()}, h)
	c := s.client(t)
	ctx := context.Background()

	info, err := c.Info(ctx)
	if err != nil || info.VAPIDPublic != fakeKey || info.Subscriptions != 0 {
		t.Fatalf("info: %+v, %v", info, err)
	}
	if err := c.Subscribe(ctx, sub("a")); err != nil {
		t.Fatal(err)
	}
	if err := c.Subscribe(ctx, sub("a")); err != nil {
		t.Fatalf("a repeat of the same endpoint: %v", err)
	}
	if err := c.Subscribe(ctx, sub("b")); err != nil {
		t.Fatal(err)
	}
	if info, _ := c.Info(ctx); info.Subscriptions != 2 {
		t.Errorf("subscriptions: %+v", info)
	}
	for _, n := range []int{1, 0, 999} {
		if err := c.Notify(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if sent, err := c.NotifySent(ctx, 3); err != nil || sent != 2 {
		t.Errorf("sent %d, %v", sent, err)
	}
	if got := h.Notified(); len(got) != 4 || got[0] != 1 || got[1] != 0 || got[2] != 999 || got[3] != 3 {
		t.Errorf("notified %v", got)
	}
	if err := c.Unsubscribe(ctx, EndpointSHA256(sub("a").Endpoint)); err != nil {
		t.Fatal(err)
	}
	if got := h.Subscriptions(); len(got) != 1 || got[0] != sub("b") {
		t.Errorf("subscriptions kept: %+v", got)
	}
}

func TestRefusalCodesReachTheClient(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("peer credentials are Linux only")
	}
	h := &FakeHandler{}
	s := startServer(t, ServeConfig{AllowedUID: uid()}, h)
	c := s.client(t)
	ctx := context.Background()
	for i := 0; i < FakeMaxSubscriptions; i++ {
		if err := c.Subscribe(ctx, sub(string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	var ce *CodedError
	if err := c.Subscribe(ctx, sub("z")); !errors.As(err, &ce) || ce.Code != CodeTooMany {
		t.Errorf("fifth subscription: %v", err)
	}
	for in, want := range map[error]string{
		NewCodedError(CodeEndpointRefused): CodeEndpointRefused,
		errors.New("secret detail"):        CodeUnavailable,
		&CodedError{Code: "BAD CODE"}:      CodeUnavailable,
	} {
		h.mu.Lock()
		h.Err = in
		h.mu.Unlock()
		err := c.Notify(ctx, 1)
		if !errors.As(err, &ce) || ce.Code != want {
			t.Errorf("handler error %v: client saw %v, want code %s", in, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("a handler's error text reached the client: %v", err)
		}
	}
}

func TestPeerUIDRefusal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("peer credentials are Linux only")
	}
	h := &FakeHandler{}
	// The real uid, read from the kernel, allowed: served.
	ok := startServer(t, ServeConfig{AllowedUID: uid()}, h)
	if err := ok.client(t).Notify(context.Background(), 1); err != nil {
		t.Fatalf("the allowed uid was refused: %v", err)
	}
	// Any other uid allowed: this peer is refused, and its request never
	// reaches the handler.
	h2 := &FakeHandler{}
	no := startServer(t, ServeConfig{AllowedUID: uid() + 1}, h2)
	if err := no.client(t).Notify(context.Background(), 1); err == nil {
		t.Fatal("a peer with another uid was served")
	}
	if len(h2.Notified()) != 0 {
		t.Error("a refused peer's request reached the handler")
	}
	// A peer whose uid cannot be read is refused too.
	h3 := &FakeHandler{}
	broken := startServer(t, ServeConfig{AllowedUID: uid(), peerUID: func(net.Conn) (uint32, error) { return 0, errors.New("no") }}, h3)
	if err := broken.client(t).Notify(context.Background(), 1); err == nil || len(h3.Notified()) != 0 {
		t.Errorf("an unreadable peer was served: %v", err)
	}
	// And the injected reader is what decides: it reports the wrong id.
	h4 := &FakeHandler{}
	fake := startServer(t, ServeConfig{AllowedUID: 4242, peerUID: func(net.Conn) (uint32, error) { return 4243, nil }}, h4)
	if err := fake.client(t).Notify(context.Background(), 1); err == nil || len(h4.Notified()) != 0 {
		t.Errorf("a wrong uid was served: %v", err)
	}
}

func TestPeerUIDReadsTheKernel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("peer credentials are Linux only")
	}
	path := sockPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	got := make(chan uint32, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- 1<<32 - 1
			return
		}
		defer c.Close()
		u, err := peerUID(c)
		if err != nil {
			u = 1<<32 - 1
		}
		got <- u
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if u := <-got; u != uid() {
		t.Errorf("peer uid %d, want %d", u, uid())
	}
}

func TestNewClientNeedsAnAbsolutePath(t *testing.T) {
	for _, p := range []string{"", "n.sock", "./n.sock", "../x"} {
		if _, err := NewClient(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestClientWithoutNotifier(t *testing.T) {
	c, _ := NewClient(sockPath(t))
	start := time.Now()
	err := c.Notify(context.Background(), 1)
	if err == nil || time.Since(start) > time.Second {
		t.Errorf("a missing socket: %v after %v", err, time.Since(start))
	}
	if err != nil && strings.Contains(err.Error(), "/") {
		t.Errorf("the error holds the path: %v", err)
	}
}

func TestClientRefusesWhatItCannotSend(t *testing.T) {
	c, _ := NewClient(sockPath(t))
	if err := c.Notify(context.Background(), 1000); !errors.Is(err, ErrProtocol) {
		t.Errorf("n 1000: %v", err)
	}
	if err := c.Notify(context.Background(), -1); !errors.Is(err, ErrProtocol) {
		t.Errorf("n -1: %v", err)
	}
	if err := c.Subscribe(context.Background(), Subscription{}); !errors.Is(err, ErrProtocol) {
		t.Errorf("an empty subscription: %v", err)
	}
	if err := c.Unsubscribe(context.Background(), "x"); !errors.Is(err, ErrProtocol) {
		t.Errorf("a bad hash: %v", err)
	}
}

// rawServer answers each connection with what fn writes after reading the
// request's frame.
func rawServer(t *testing.T, fn func(c net.Conn)) string {
	t.Helper()
	path := sockPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				fn(c)
			}()
		}
	}()
	return path
}

func frame(s string) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(s))), s...)
}

func TestClientRefusesBadAnswers(t *testing.T) {
	for name, reply := range map[string][]byte{
		"not json":      frame(`nope`),
		"wrong op":      frame(`{"ok":true,"sent":1}`),
		"extra field":   frame(`{"ok":true,"x":1}`),
		"empty frame":   {0, 0, 0, 0},
		"huge frame":    binary.BigEndian.AppendUint32(nil, MaxFrame+1),
		"truncated":     append(binary.BigEndian.AppendUint32(nil, 50), 'x'),
		"nothing":       nil,
		"bad code":      frame(`{"ok":false,"code":"No"}`),
		"two frames ok": append(frame(`{"ok":true,"subscriptions":1,"vapid_public":"`+fakeKey+`"}`), frame(`junk`)...),
	} {
		path := rawServer(t, func(c net.Conn) {
			readFrame(c)
			c.Write(reply)
		})
		c, _ := NewClient(path)
		_, err := c.Info(context.Background())
		if name == "two frames ok" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestClientDeadline(t *testing.T) {
	// A notifier that reads the request and never answers.
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	path := rawServer(t, func(c net.Conn) {
		readFrame(c)
		<-hang
	})
	c, _ := NewClient(path)
	c.Deadline = 150 * time.Millisecond
	start := time.Now()
	err := c.Notify(context.Background(), 2)
	if err == nil {
		t.Fatal("a silent notifier answered")
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > 2*time.Second {
		t.Errorf("the call took %v, want about the 150ms deadline", d)
	}

	// The default is two seconds.
	if DefaultDeadline != 2*time.Second {
		t.Errorf("DefaultDeadline is %v", DefaultDeadline)
	}

	// A context that ends sooner ends the call sooner, and says so.
	c.Deadline = 0
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start = time.Now()
	err = c.Notify(ctx, 2)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Errorf("context deadline: %v after %v", err, time.Since(start))
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel2)
	start = time.Now()
	if err := c.Notify(ctx2, 2); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Errorf("cancel: %v after %v", err, time.Since(start))
	}
}

func TestServerDeadlines(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("peer credentials are Linux only")
	}
	// A handler that never answers is cut at the deadline, and its context
	// ends.
	block := make(chan struct{})
	defer close(block)
	h := &FakeHandler{Block: block}
	s := startServer(t, ServeConfig{AllowedUID: uid(), Deadline: 200 * time.Millisecond}, h)
	c := s.client(t)
	start := time.Now()
	if err := c.Notify(context.Background(), 1); err == nil {
		t.Fatal("a blocked handler answered")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("the server held the call %v", d)
	}
	// A peer that sends half a frame and stops is cut off at the deadline.
	conn, err := net.Dial("unix", s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte{0, 0, 0, 20, '{'})
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	start = time.Now()
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("the server did not close a stalled peer: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("a stalled peer held the server %v", d)
	}
}

func TestServerRefusesBadRequestsWithoutAnswer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("peer credentials are Linux only")
	}
	h := &FakeHandler{}
	s := startServer(t, ServeConfig{AllowedUID: uid()}, h)
	for _, req := range [][]byte{
		frame(`{"op":"stop"}`),
		frame(`{"op":"notify","n":1000}`),
		frame(`{"op":"notify","n":1,"extra":1}`),
		{0, 0, 0, 0},
		binary.BigEndian.AppendUint32(nil, MaxFrame+1),
		append(binary.BigEndian.AppendUint32(nil, MaxFrame), make([]byte, 10)...),
	} {
		conn, err := net.Dial("unix", s.path)
		if err != nil {
			t.Fatal(err)
		}
		conn.Write(req)
		if uc, ok := conn.(*net.UnixConn); ok && len(req) > 4 && binary.BigEndian.Uint32(req) == MaxFrame {
			uc.CloseWrite()
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		b, err := io.ReadAll(conn)
		conn.Close()
		if err != nil || len(b) != 0 {
			t.Errorf("request %q: got %q, %v", req, b, err)
		}
	}
	if len(h.Notified()) != 0 {
		t.Errorf("a refused request reached the handler: %v", h.Notified())
	}
}

func TestServeStopsAndLeaksNothing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("peer credentials are Linux only")
	}
	before := runtime.NumGoroutine()
	h := &FakeHandler{}
	s := startServer(t, ServeConfig{AllowedUID: uid()}, h)
	c := s.client(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Notify(context.Background(), 1)
		}()
	}
	wg.Wait()
	s.stop()
	if err := c.Notify(context.Background(), 1); err == nil {
		t.Error("a stopped server answered")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("goroutines: %d before, %d after", before, n)
	}
}
