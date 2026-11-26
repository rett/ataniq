package link

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
	"ataniq/internal/seatgate"
)

func TestWitnessCertificateRequired(t *testing.T) {
	f := newFixture(t, "witness")
	e, r := f.listener(t)
	if e.listenerTLS.ClientAuth != tls.RequireAndVerifyClientCert || e.listenerTLS.ClientCAs == nil {
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
			admin := f.endpoint(serve(t, func(l net.Listener) error { return f.s.ServeOwner(l, f.w.Administration(), ownerID.Fingerprint()) }))
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

func TestRejectedWitnessStopsPolling(t *testing.T) {
	f := newFixture(t, "witness")
	_, r := f.listener(t)
	other, err := NewServer(identity(t, "untrusted"), f.pin)
	must(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := other.Poll(ctx, r, memoryWitness{f.w}); !errors.Is(err, cp.ErrRefused) {
		t.Fatalf("poll: %v", err)
	}
}
