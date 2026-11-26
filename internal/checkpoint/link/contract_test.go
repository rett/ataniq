package link

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
	"ataniq/internal/checkpoint/link/linktest"
	"ataniq/internal/seatgate"
)

// The contract fixture uses the in-process, finite memory backend.
type memoryWitness struct{ *cp.Memory }

func (w memoryWitness) CheckHome(ctx context.Context, slot cp.Slot, key ed25519.PublicKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return w.Memory.CheckHome(slot, key)
}

func identity(t *testing.T, name string) seatgate.Identity {
	t.Helper()
	cert, key, err := seatgate.GenerateIdentity(rand.Reader, time.Now(), name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := seatgate.ParseIdentity(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type dialFunc func(context.Context, string, string) (net.Conn, error)

// Restricted test runners may forbid socket(2). Exercise the complete TLS
// handshake and frame exchange there too, using a synchronous loopback stream.
type pipeListener struct {
	incoming chan net.Conn
	closed   chan struct{}
	once     sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.incoming:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	c, s := net.Pipe()
	select {
	case l.incoming <- s:
		return c, nil
	case <-ctx.Done():
		c.Close()
		s.Close()
		return nil, ctx.Err()
	case <-l.closed:
		c.Close()
		s.Close()
		return nil, net.ErrClosed
	}
}

func loopback(t *testing.T) (net.Listener, dialFunc) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err == nil {
		return l, (&net.Dialer{}).DialContext
	}
	if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
		t.Fatal(err)
	}
	p := &pipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
	t.Log("socket bind denied; exercising TLS over net.Pipe")
	return p, p.dial
}

func serve(t *testing.T, run func(net.Listener) error) Endpoint {
	t.Helper()
	l, dial := loopback(t)
	done := make(chan error, 1)
	go func() { done <- run(l) }()
	t.Cleanup(func() {
		l.Close()
		if err := <-done; !errors.Is(err, net.ErrClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	return Endpoint{Network: l.Addr().Network(), Address: l.Addr().String(), DialContext: dial}
}

// Both legs authenticate TLS. The proxy operates on complete plaintext
// frames and is the only component that drops successful acknowledgements.
func proxied(t *testing.T, e Endpoint, id, owner seatgate.Identity, p *linktest.Proxy) Endpoint {
	t.Helper()
	upstream, err := e.transport(owner)
	if err != nil {
		t.Fatal(err)
	}
	cfg := serverTLS(id, owner.Fingerprint())
	frontend := serve(t, func(l net.Listener) error {
		var wg sync.WaitGroup
		defer wg.Wait()
		for {
			raw, err := l.Accept()
			if err != nil {
				return err
			}
			wg.Go(func() {
				defer raw.Close()
				raw.SetDeadline(time.Now().Add(requestTimeout))
				ctx, done := context.WithTimeout(t.Context(), requestTimeout)
				defer done()
				client := tls.Server(raw, cfg)
				if client.HandshakeContext(ctx) != nil {
					return
				}
				back, err := upstream.connect(ctx)
				if err != nil {
					return
				}
				defer back.NetConn().Close()
				back.SetDeadline(time.Now().Add(requestTimeout))
				_ = p.Forward(client, back)
			})
		}
	})
	frontend.ServerPin = id.Fingerprint()
	frontend.WitnessPin = e.WitnessPin
	frontend.Epoch = e.Epoch
	return frontend
}

func TestWitnessContract(t *testing.T) {
	serverID, ownerID, proxyID := identity(t, "witness"), identity(t, "owner"), identity(t, "proxy")
	checkpointtest.Run(t, func(t *testing.T, k ed25519.PrivateKey) checkpointtest.Harness {
		pin := k.Public().(ed25519.PublicKey)
		w, err := cp.NewMemory("witness", 1, k)
		if err != nil {
			t.Fatal(err)
		}
		s, err := NewServer(serverID, pin)
		if err != nil {
			t.Fatal(err)
		}
		home := serve(t, func(l net.Listener) error { return s.Serve(l, memoryWitness{w}) })
		admin := serve(t, func(l net.Listener) error { return s.ServeOwner(l, w.Administration(), ownerID.Fingerprint()) })
		var proxy linktest.Proxy
		for _, e := range []*Endpoint{&home, &admin} {
			e.ServerPin = serverID.Fingerprint()
			e.WitnessPin = pin
			e.Epoch = 1
		}
		home = proxied(t, home, proxyID, seatgate.Identity{}, &proxy)
		admin = proxied(t, admin, proxyID, ownerID, &proxy)
		a, err := admin.Administration(ownerID)
		if err != nil {
			t.Fatal(err)
		}
		// The contract callback also saves the original receipt in the test
		// goroutine's fixture. A subsequent recovery read must synchronize
		// with that write even when kernel TCP carries the reply.
		var delivery sync.Mutex
		dial := home.DialContext
		home.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			delivery.Lock()
			delivery.Unlock()
			return dial(ctx, network, addr)
		}
		return checkpointtest.Harness{
			Admin: a, Grant: w.Grant, Revoke: w.Revoke, Inhibit: w.Inhibit, SetReceiptLimit: w.SetReceiptLimit,
			LoseNextReply: proxy.DropNextReply,
			TransformNextReply: func(change func(cp.Receipt) cp.Receipt) {
				w.TransformNextReply(func(r cp.Receipt) cp.Receipt {
					delivery.Lock()
					defer delivery.Unlock()
					return change(r)
				})
			},
			Connect: func(slot cp.Slot, h, s ed25519.PrivateKey, p ed25519.PublicKey) (cp.Client, error) {
				e := home
				e.WitnessPin = p
				return e.Connect(t.Context(), slot, h, s)
			},
			RawSubmit: home.RawSubmit,
		}
	})
}
