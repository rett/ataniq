package desklink

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"ataniq/internal/seatgate"
)

const testHomeID = "test-home"

// A TLS server flight can contain several records in one Write. Read the
// flight off the synchronous pipe so the client can send a certificate alert
// midway through it, while some records are still in the client's buffer.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func identity(t *testing.T) seatgate.Identity {
	t.Helper()
	cert, key, err := seatgate.GenerateIdentity(rand.Reader, time.Now(), "device")
	if err != nil {
		t.Fatal(err)
	}
	id, err := seatgate.ParseIdentity(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func enroll(t *testing.T, r *Registry, id seatgate.Identity, p Principal) {
	t.Helper()
	if err := r.Enroll(id.Fingerprint(), p); err != nil {
		t.Fatal(err)
	}
}

// Both directions have a deadline so a regression fails the test even if one
// end is waiting for a TLS alert and the other is waiting for application data.
func serve(t *testing.T, r *Registry, server seatgate.Identity, h Handler) (net.Conn, <-chan error) {
	t.Helper()
	client, raw := net.Pipe()
	t.Cleanup(func() { client.Close() })
	deadline := time.Now().Add(5 * time.Second)
	client.SetDeadline(deadline)
	raw.SetDeadline(deadline)
	done := make(chan error, 1)
	go func() { done <- ServeConn(t.Context(), raw, server, testHomeID, r, h) }()
	return client, done
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the desk link")
		var zero T
		return zero
	}
}

func connect(t *testing.T, raw net.Conn, device, server seatgate.Identity) *Conn {
	t.Helper()
	conn, err := Connect(t.Context(), raw, device, server.Fingerprint(), testHomeID)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestDeskLinkAuthenticatedStream(t *testing.T) {
	var r Registry
	server, device := identity(t), identity(t)
	p := Principal{ID: "desk-a", Role: "viewer"}
	enroll(t, &r, device, p)
	peers := make(chan Peer, 1)
	handlerErr := errors.New("end of request")
	raw, done := serve(t, &r, server, HandlerFunc(func(_ context.Context, peer Peer, c net.Conn) error {
		peers <- peer
		buf := make([]byte, len("hello"))
		if _, err := io.ReadFull(c, buf); err != nil {
			return err
		}
		if _, err := c.Write(buf); err != nil {
			return err
		}
		return handlerErr
	}))
	c := connect(t, raw, device, server)
	if cs := c.ConnectionState(); cs.Version != tls.VersionTLS13 || cs.NegotiatedProtocol != "ataniq-desk/1" {
		t.Fatalf("wrong TLS version %x or ALPN %q", cs.Version, cs.NegotiatedProtocol)
	}
	if peer := receive(t, peers); peer.Fingerprint != device.Fingerprint() || peer.Principal != p {
		t.Fatalf("unexpected peer %s %+v", peer.Fingerprint, peer.Principal)
	}
	if _, err := io.WriteString(c, "hello"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("hello"))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("reply %q, error %v", buf, err)
	}
	if err := receive(t, done); err != handlerErr {
		t.Fatalf("server returned %v", err)
	}
	if _, err := c.Read(buf); err == nil {
		t.Fatal("connection still open after the handler returned")
	}
}

func TestUnpinnedAndWrongProtocolRefusedBeforeHandler(t *testing.T) {
	server, device, stranger := identity(t), identity(t), identity(t)
	var r Registry
	enroll(t, &r, device, Principal{ID: "desk", Role: "viewer"})
	for _, tc := range []struct {
		name   string
		id     seatgate.Identity
		protos []string
		noCert bool
	}{
		{"unpinned device", stranger, []string{ALPN}, false},
		{"gate protocol", device, []string{seatgate.ALPN}, false},
		{"absent ALPN", device, nil, false},
		{"missing certificate", device, []string{ALPN}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := make(chan bool, 1)
			raw, done := serve(t, &r, server, HandlerFunc(func(context.Context, Peer, net.Conn) error {
				called <- true
				return nil
			}))
			cfg := seatgate.ClientTLS(tc.id, server.Fingerprint(), "")
			cfg.NextProtos = tc.protos
			cfg.VerifyConnection = nil // the server must enforce the protocol itself
			if tc.noCert {
				cfg.Certificates = nil
			}
			c := tls.Client(raw, cfg)
			err := c.HandshakeContext(t.Context())
			if err == nil {
				// TLS 1.3 clients may learn of a server-side refusal on the first read.
				_, err = c.Read(make([]byte, 1))
			}
			var timeout net.Error
			if err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
				t.Fatalf("expected refusal before any application frame, got %v", err)
			}
			raw.Close()
			err = receive(t, done)
			if err == nil {
				t.Fatal("server accepted the link")
			}
			if tc.id.Fingerprint() == stranger.Fingerprint() && !strings.Contains(err.Error(), "key is not pinned") {
				t.Fatalf("expected TLS certificate pin refusal, got %v", err)
			}
			select {
			case <-called:
				t.Fatal("handed an unauthorized stream to the handler")
			default:
			}
		})
	}
}

func TestDeskLinkClientRefusesUnpinnedServerOrAbsentProtocol(t *testing.T) {
	server, device, stranger := identity(t), identity(t), identity(t)
	for _, tc := range []struct {
		name   string
		pin    string
		protos []string
	}{
		{"unpinned server", stranger.Fingerprint(), []string{ALPN}},
		{"absent ALPN", server.Fingerprint(), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, raw := net.Pipe()
			defer client.Close()
			defer raw.Close()
			cfg := seatgate.ServerTLS(server, []string{device.Fingerprint()})
			cfg.NextProtos, cfg.VerifyConnection = tc.protos, nil
			done := make(chan error, 1)
			go func() { done <- handshake(t.Context(), tls.Server(raw, cfg)) }()
			c, err := Connect(t.Context(), bufferedConn{client, bufio.NewReaderSize(client, 16384)}, device, tc.pin, testHomeID)
			if c != nil || err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("client refusal: conn %v, error %v", c != nil, err)
			}
			if err := receive(t, done); err == nil {
				t.Fatal("server handshake succeeded despite client refusal")
			}
		})
	}
}

func TestDeskLinkServerCancellation(t *testing.T) {
	var r Registry
	server, device := identity(t), identity(t)
	enroll(t, &r, device, Principal{ID: "desk", Role: "viewer"})
	client, raw := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan bool, 1)
	done := make(chan error, 1)
	go func() {
		done <- ServeConn(ctx, raw, server, testHomeID, &r, HandlerFunc(func(ctx context.Context, _ Peer, c net.Conn) error {
			ready <- true
			_, err := c.Read(make([]byte, 1))
			if err == nil {
				return errors.New("expected read to fail")
			}
			return ctx.Err()
		}))
	}()
	connect(t, client, device, server)
	receive(t, ready)
	cancel()
	if err := receive(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("handler returned %v", err)
	}
	if d := r.snapshot()[device.Fingerprint()]; len(d.live) != 0 {
		t.Fatal("closed connection still registered")
	}
}

func TestDeskLinkServerRequiresRegistryAndHandler(t *testing.T) {
	for _, tc := range []struct {
		r *Registry
		h Handler
	}{
		{nil, HandlerFunc(func(context.Context, Peer, net.Conn) error { return nil })},
		{new(Registry), nil},
	} {
		raw, client := net.Pipe()
		if err := ServeConn(t.Context(), raw, seatgate.Identity{}, testHomeID, tc.r, tc.h); err == nil {
			t.Fatal("server accepted incomplete configuration")
		}
		if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("expected closed transport, got %v", err)
		}
		client.Close()
	}
}
