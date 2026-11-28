package link

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
	"ataniq/internal/seatgate"
)

func TestWitnessCertificateRequired(t *testing.T) {
	f := newFixture(t, "witness")
	e, r := f.listener(t)
	if e.listenerTLS.ClientAuth != tls.RequireAnyClientCert || e.listenerTLS.ClientCAs != nil {
		t.Error("home does not verify the registered witness certificate")
	}
	for _, id := range []seatgate.Identity{{}, identity(t, "untrusted")} {
		tr, err := r.transport(id)
		must(t, err)
		c, err := tr.connect(t.Context())
		if err != nil {
			if !errors.Is(err, cp.ErrRefused) {
				t.Fatal(err)
			}
			continue
		}
		c.SetReadDeadline(time.Now().Add(time.Second))
		_, err = c.Read(make([]byte, 1))
		c.NetConn().Close()
		if !handshakeRefusal(err) {
			t.Fatalf("unauthorized poller: %v", err)
		}
	}
}

func TestAdmissionIsolation(t *testing.T) {
	for _, network := range []string{"pipe", "tcp"} {
		t.Run(network, func(t *testing.T) {
			f := newFixture(t, "witness")
			homeID := identity(t, "home")
			e := endpointFor(t, homeID, f.id, f)
			var l net.Listener
			var dial dialFunc
			if network == "tcp" {
				l = tcpListener(t)
				dial = (&net.Dialer{}).DialContext
			} else {
				p := &pipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
				l, dial = p, p.dial
			}
			done := make(chan error, 1)
			go func() { done <- e.Serve(l) }()
			defer func() {
				l.Close()
				if !errors.Is(<-done, net.ErrClosed) {
					t.Fatal("home did not shut down")
				}
			}()
			for range 2 {
				c, err := dial(t.Context(), l.Addr().Network(), l.Addr().String())
				must(t, err)
				defer c.Close()
			}
			ownerID := identity(t, "owner")
			admin := f.endpoint(testOwner(t, f.s, f.w, f.w.Administration(), ownerID.Fingerprint()))
			a, err := admin.Administration(ownerID)
			must(t, err)
			f.enroll(t, a)
			c, err := dial(t.Context(), l.Addr().Network(), l.Addr().String())
			must(t, err)
			defer c.Close()
			c.SetReadDeadline(time.Now().Add(time.Second))
			_, err = c.Read(make([]byte, 1))
			var timeout net.Error
			if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
				t.Fatalf("per-source limit: %v", err)
			}
		})
	}
}

func TestFrameHeaderDeadline(t *testing.T) {
	f := newFixture(t, "witness")
	_, r := f.listener(t)
	tr, err := r.transport(f.id)
	must(t, err)
	c, err := tr.connect(t.Context())
	must(t, err)
	defer c.NetConn().Close()
	c.SetDeadline(time.Now().Add(4 * time.Second))
	_, err = c.Read(make([]byte, 1))
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("silent authenticated connection: %v", err)
	}
	c, err = tr.connect(t.Context())
	must(t, err)
	defer c.NetConn().Close()
	b, err := frame.New(frame.Poll).Marshal()
	must(t, err)
	_, err = c.Write(b[:4])
	must(t, err)
	time.Sleep(2200 * time.Millisecond)
	_, err = c.Write(b[4:])
	must(t, err)
	reply, err := frame.ReadMessage(c)
	must(t, err)
	if reply.Kind != frame.Idle {
		t.Fatal("missing idle reply")
	}
}

func TestDatedWitnessRetriesPeerAlert(t *testing.T) {
	for _, network := range []string{"pipe", "tcp"} {
		for _, years := range []int{-20, 3} {
			t.Run(fmt.Sprintf("%s/%d", network, years), func(t *testing.T) {
				f := newFixture(t, "witness")
				cert, key, err := seatgate.GenerateIdentity(rand.Reader, time.Now().AddDate(years, 0, 0), "witness")
				must(t, err)
				f.id, err = seatgate.ParseIdentity(cert, key)
				must(t, err)
				f.s.id = f.id
				home := identity(t, "home")
				e := endpointFor(t, home, f.id, f)
				var attempts atomic.Int32
				var first, second atomic.Int64
				e.listenerTLS.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
					switch attempts.Add(1) {
					case 2:
						second.Store(time.Now().UnixNano())
					case 1:
						first.Store(time.Now().UnixNano())
						return nil, errors.New("temporary refusal")
					}
					return nil, nil
				}
				r := serveNetwork(t, network, e.Serve)
				r.ServerPin = home.Fingerprint()
				startPolling(t, f.s, r, memoryWitness{f.w})
				ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
				defer cancel()
				_, err = e.Connect(ctx, f.slot, f.home, f.session)
				must(t, err)
				if time.Duration(second.Load()-first.Load()) < 500*time.Millisecond {
					t.Fatal("peer alert missed long backoff")
				}
				if attempts.Load() < 2 {
					t.Fatal("missing retry")
				}
			})
		}
	}
}

func TestTCPReservedWitnessHandshake(t *testing.T) {
	f := newFixture(t, "witness")
	id := identity(t, "home")
	e := endpointFor(t, id, f.id, f)
	r := serveNetwork(t, "tcp", e.Serve)
	r.ServerPin = id.Fingerprint()
	var silent []net.Conn
	for _, ip := range []string{"127.0.0.2", "127.0.0.3"} {
		d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}
		for range 2 {
			c, err := d.DialContext(t.Context(), "tcp4", r.Address)
			must(t, err)
			defer c.Close()
			silent = append(silent, c)
		}
	}
	startPolling(t, f.s, r, memoryWitness{f.w})
	ctx, cancel := context.WithTimeout(t.Context(), 450*time.Millisecond)
	defer cancel()
	_, err := e.Connect(ctx, f.slot, f.home, f.session)
	must(t, err)
	silent[0].SetReadDeadline(time.Now().Add(time.Second))
	_, err = silent[0].Read(make([]byte, 1))
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("silent peer exceeded first-byte window", err)
	}
}

type sourceConn struct {
	net.Conn
	source net.IP
}

func (c sourceConn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: c.source} }

func TestConfiguredSourcesHaveReservedSlots(t *testing.T) {
	admit := homeAdmission(loopbackTrust())
	for _, ip := range []string{"127.0.0.2", "127.0.0.2", "127.0.0.3", "127.0.0.3"} {
		release := admit(sourceConn{source: net.ParseIP(ip)})
		if release == nil {
			t.Fatal("missing ordinary capacity")
		}
		defer release()
	}
	if admit(sourceConn{source: net.ParseIP("127.0.0.4")}) != nil {
		t.Fatal("ordinary limit exceeded")
	}
	release := admit(sourceConn{source: net.ParseIP("127.0.0.1")})
	if release == nil {
		t.Fatal("reserved capacity unavailable")
	}
	release()
}

type tcpPeer struct {
	net.Conn
	addr *net.TCPAddr
}

func (c tcpPeer) RemoteAddr() net.Addr { return c.addr }
func TestTrustSourceNormalization(t *testing.T) {
	f := newFixture(t, "witness")
	id := identity(t, "home")
	cert := seatgate.ServerTLS(f.id, nil).Certificates[0].Certificate[0]
	for _, cidr := range []string{"0.0.0.0/0", "::/0", "::ffff:0:0/96", "::ffff:192.0.2.0/95"} {
		_, err := NewEndpoint(id, cert, f.pin, 1, f.slot, TrustSources{netip.MustParsePrefix(cidr)})
		var config *ConfigError
		if !errors.As(err, &config) {
			t.Errorf("accepted unbounded or ambiguous source %s", cidr)
		}
	}
	for _, tc := range []struct{ cidr, peer string }{
		{"::ffff:192.0.2.123/120", "192.0.2.7"},
		{"fe80::/64", "fe80::2%eth0"},
	} {
		e, err := NewEndpoint(id, cert, f.pin, 1, f.slot, TrustSources{netip.MustParsePrefix(tc.cidr)})
		must(t, err)
		admit := homeAdmission(e.trust)
		for _, ip := range []string{"192.0.3.1", "192.0.3.1", "192.0.3.2", "192.0.3.2"} {
			if admit(sourceConn{source: net.ParseIP(ip)}) == nil {
				t.Fatal("ordinary setup")
			}
		}
		addr := net.TCPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr(tc.peer), 1))
		if admit(tcpPeer{addr: addr}) == nil {
			t.Errorf("%s missed reserved %s", tc.peer, tc.cidr)
		}
	}
}
