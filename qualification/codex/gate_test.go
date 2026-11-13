package codex

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeListener keeps the mock HTTP server and its client separate without
// requiring network privileges. It does not test OS network containment.
type pipeListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return pipeAddr("mock") }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

// requestGate is a fixture for exploring admission, not a production fence.
// Authority and the actual upstream admission are serialized by mu. An admitted
// bounded request can complete after authority is revoked.
type requestGate struct {
	mu       sync.Mutex
	online   bool
	epoch    uint64
	changed  chan struct{}
	upstream string
	client   *http.Client
}

func (g *requestGate) set(online bool, epoch uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.online, g.epoch = online, epoch
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *requestGate) send(ctx context.Context, epoch uint64, body string) (*http.Response, error) {
	for {
		g.mu.Lock()
		if epoch != g.epoch {
			g.mu.Unlock()
			return nil, errors.New("stale epoch")
		}
		if g.online {
			// Starting the HTTP request while holding the lock serializes the
			// authority decision with admission into this fixture's transport.
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.upstream, strings.NewReader(body))
			if err != nil {
				g.mu.Unlock()
				return nil, err
			}
			// The transport performs the effect asynchronously, so hold the lock
			// through RoundTrip until the upstream has returned its headers.
			resp, err := g.client.Do(req)
			g.mu.Unlock()
			return resp, err
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func TestGateFixture(t *testing.T) {
	arrivals := make(chan string, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	listener := &pipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 128))
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		arrivals <- string(b)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		if string(b) == "inflight" {
			<-release
		}
		_, _ = w.Write([]byte("complete"))
	})}
	go upstream.Serve(listener)
	defer upstream.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		select {
		case listener.connections <- server:
			return client, nil
		case <-ctx.Done():
			client.Close()
			server.Close()
			return nil, ctx.Err()
		case <-listener.done:
			client.Close()
			server.Close()
			return nil, net.ErrClosed
		}
	}}
	defer transport.CloseIdleConnections()
	g := &requestGate{online: true, epoch: 1, changed: make(chan struct{}), upstream: "http://mock/responses", client: &http.Client{Timeout: 3 * time.Second, Transport: transport}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := g.send(ctx, 1, "inflight")
	if err != nil {
		t.Fatal(err)
	}
	if got := <-arrivals; got != "inflight" {
		t.Fatal(got)
	}
	g.set(false, 1)
	type result struct {
		resp *http.Response
		err  error
	}
	resultCh := make(chan result, 1)
	go func() { r, err := g.send(ctx, 1, "continuation"); resultCh <- result{r, err} }()
	select {
	case got := <-arrivals:
		t.Fatalf("offline request reached upstream: %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(b) != "complete" {
		t.Fatalf("inflight: %q %v", b, err)
	}
	g.set(true, 2)
	if got := <-resultCh; got.err == nil {
		got.resp.Body.Close()
		t.Fatal("old epoch admitted")
	}
	select {
	case got := <-arrivals:
		t.Fatalf("stale request reached upstream: %s", got)
	default:
	}
	resp, err = g.send(ctx, 2, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := <-arrivals; got != "fresh" {
		t.Fatal(got)
	}
	g.set(false, 2)
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := g.send(canceled, 2, "canceled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	g.set(true, 2)
	if _, err := g.send(canceled, 2, "canceled-online"); !errors.Is(err, context.Canceled) {
		t.Fatalf("online cancel: %v", err)
	}
	select {
	case got := <-arrivals:
		t.Fatalf("canceled request reached upstream: %s", got)
	default:
	}
	g.upstream = ":invalid"
	if _, err := g.send(ctx, 2, "invalid"); err == nil {
		t.Fatal("invalid upstream accepted")
	}
	select {
	case got := <-arrivals:
		t.Fatalf("invalid request reached upstream: %s", got)
	default:
	}
}
