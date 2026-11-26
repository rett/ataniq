package link

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"

	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
	"ataniq/internal/checkpoint/link/internal/frame"
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

func serve(t *testing.T, run func(net.Listener) error) Remote {
	t.Helper()
	return serveNetwork(t, "auto", run)
}

func serveNetwork(t *testing.T, network string, run func(net.Listener) error) Remote {
	t.Helper()
	var l net.Listener
	var dial dialFunc
	switch network {
	case "unix":
		dir, err := os.MkdirTemp("", "owner-")
		must(t, err)
		t.Cleanup(func() { os.RemoveAll(dir) })
		l, err = net.Listen("unix", filepath.Join(dir, "sock"))
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Log("Unix bind denied; exercising authenticated framing over net.Pipe")
			p := &pipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
			l, dial = p, p.dial
		} else {
			must(t, err)
			dial = (&net.Dialer{}).DialContext
		}
	case "tcp":
		l = tcpListener(t)
		dial = (&net.Dialer{}).DialContext
	case "pipe":
		p := &pipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
		l, dial = p, p.dial
	default:
		l, dial = loopback(t)
	}
	done := make(chan error, 1)
	go func() { done <- run(l) }()
	t.Cleanup(func() {
		l.Close()
		if err := <-done; !errors.Is(err, net.ErrClosed) {
			t.Errorf("serve: %v", err)
		}
	})
	if network != "unix" {
		network = l.Addr().Network()
	}
	return Remote{Network: network, Address: l.Addr().String(), DialContext: dial}
}

// Both legs authenticate TLS. The proxy operates on complete plaintext
// frames and is the only component that drops successful acknowledgements.
func proxied(t *testing.T, e Remote, id, owner seatgate.Identity, p *linktest.Proxy, poll bool) Remote {
	t.Helper()
	upstream, err := e.transport(owner)
	if err != nil {
		t.Fatal(err)
	}
	cfg := serverTLS(id, owner.Fingerprint())
	network := "unix"
	if poll {
		network = "auto"
	}
	frontend := serveNetwork(t, network, func(l net.Listener) error {
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
				if poll {
					m, err := frame.ReadMessage(client)
					if err != nil || m.Kind != frame.Poll || m.WriteToStream(back) != nil {
						return
					}
					_ = p.Forward(back, client)
				} else {
					_ = p.Forward(client, back)
				}
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
		w, err := cp.NewMemory("witness", 1, k)
		if err != nil {
			t.Fatal(err)
		}
		return linkHarness(t, public(k), w, serverID, ownerID, proxyID)
	})
}

// Recreate the transport services around the memory witness directory.
func linkHarness(t *testing.T, pin ed25519.PublicKey, w *cp.Memory, serverID, ownerID, proxyID seatgate.Identity) checkpointtest.Harness {
	homeID := identity(t, "home")
	placeholder := Remote{Address: "localhost:1", ServerPin: homeID.Fingerprint()}
	s, err := NewServer(serverID, pin, placeholder, cp.Slot{Witness: "witness", Authority: "owner", Store: "owner"})
	must(t, err)
	advanceClock := func(s *Server) {
		s.pollDelay = func(time.Duration) time.Duration { return 25 * time.Millisecond }
		now := time.Now()
		s.limits.now = func() time.Time { now = now.Add(2 * time.Second); return now }
	}
	advanceClock(s)
	admin := testOwner(t, s, w, w.Administration(), ownerID.Fingerprint())
	admin.ServerPin, admin.WitnessPin, admin.Epoch = serverID.Fingerprint(), pin, 1
	var proxy linktest.Proxy
	admin = proxied(t, admin, proxyID, ownerID, &proxy, false)
	endpoints := map[cp.Slot]*Endpoint{}
	var pollers []context.CancelFunc
	endpoint := func(slot cp.Slot) *Endpoint {
		if endpoints[slot] == nil {
			e, err := NewEndpoint(homeID, seatgate.ServerTLS(serverID, nil).Certificates[0].Certificate[0], pin, 1, slot, loopbackTrust())
			must(t, err)
			addr := serve(t, e.Serve)
			addr.ServerPin, addr.WitnessPin, addr.Epoch = homeID.Fingerprint(), pin, 1
			addr = proxied(t, addr, proxyID, serverID, &proxy, true)
			poller, err := NewServer(serverID, pin, addr, slot)
			must(t, err)
			advanceClock(poller)
			pollers = append(pollers, startPolling(t, poller, addr, memoryWitness{w}))
			endpoints[slot] = e
		}
		return endpoints[slot]
	}
	a, err := admin.Administration(ownerID)
	if err != nil {
		t.Fatal(err)
	}
	// The contract callback also saves the original receipt in the test
	// goroutine's fixture. A subsequent recovery read must synchronize
	// with that write even when kernel TCP carries the reply.
	var delivery sync.Mutex
	return checkpointtest.Harness{
		Restart: func() checkpointtest.Harness {
			for _, stop := range pollers {
				stop()
			}
			return linkHarness(t, pin, w, serverID, ownerID, proxyID)
		},
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
			e := endpoint(slot)
			if !ed25519.PublicKey(p).Equal(e.home.WitnessPin) {
				return nil, &cp.Reason{Kind: cp.Unauthorized}
			}
			c, err := e.Connect(t.Context(), slot, h, s)
			if err != nil {
				return nil, err
			}
			return synchronizedClient{c, &delivery}, nil
		},
		RawSubmit: func(ctx context.Context, slot cp.Slot, key ed25519.PublicKey, op string, b, hs, ss []byte) (cp.Receipt, error) {
			return endpoint(slot).RawSubmit(ctx, slot, key, op, b, hs, ss)
		},
	}
}

type synchronizedClient struct {
	cp.Client
	delivery *sync.Mutex
}

func (c synchronizedClient) Read(ctx context.Context, q cp.ReadRequest) (cp.View, error) {
	v, err := c.Client.Read(ctx, q)
	c.delivery.Lock()
	c.delivery.Unlock()
	return v, err
}
func startPolling(t *testing.T, s *Server, r Remote, w Witness) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	s.home = r
	go func() {
		defer close(done)
		if err := s.Poll(ctx, w); !errors.Is(err, context.Canceled) {
			t.Errorf("poll: %v", err)
		}
	}()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return stop
}

func loopbackTrust() TrustSources { return TrustSources{netip.MustParsePrefix("127.0.0.1/32")} }

func testOwner(t *testing.T, s *Server, signer WitnessEvidence, a cp.Administration, pin string) Remote {
	return serveNetwork(t, "unix", func(l net.Listener) error {
		if _, pipe := l.(*pipeListener); pipe {
			return s.serveOwner(l, a, signer, pin, func(net.Conn) func() { return func() {} })
		}
		return s.ServeOwner(l, a, signer, pin, uint32(os.Getuid()))
	})
}
func TestOwnerPeerUID(t *testing.T) {
	f := newFixture(t, "witness")
	owner := identity(t, "owner")
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "owner.sock"))
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("Unix bind denied: %v", err)
	}
	must(t, err)
	done := make(chan error, 1)
	go func() {
		done <- f.s.ServeOwner(l, f.w.Administration(), f.w, owner.Fingerprint(), uint32(os.Getuid())^1)
	}()
	defer func() {
		l.Close()
		if !errors.Is(<-done, net.ErrClosed) {
			t.Fatal("owner listener failed")
		}
	}()
	raw, err := net.Dial("unix", l.Addr().String())
	must(t, err)
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(time.Second))
	if err = tls.Client(raw, clientTLS(f.id.Fingerprint(), owner)).Handshake(); err == nil {
		t.Fatal("different UID completed owner handshake")
	}
}
