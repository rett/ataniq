package desklink

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"

	"ataniq/internal/seatgate"
)

func TestCF01(t *testing.T) {
	server, device := identity(t), identity(t)
	p := Principal{ID: "desk", Role: "viewer"}
	open := func(t *testing.T, r *Registry, clientHome string) (*Conn, Peer) {
		t.Helper()
		peers := make(chan Peer, 1)
		raw, done := serve(t, r, server, HandlerFunc(func(_ context.Context, peer Peer, _ net.Conn) error {
			peers <- peer
			return nil
		}))
		c, err := Connect(t.Context(), raw, device, server.Fingerprint(), clientHome)
		if err != nil {
			t.Fatal(err)
		}
		if err := receive(t, done); err != nil {
			t.Fatal(err)
		}
		return c, receive(t, peers)
	}

	t.Run("SameKeyNewExporter", func(t *testing.T) {
		var r Registry
		enroll(t, &r, device, p)
		first, firstPeer := open(t, &r, testHomeID)
		second, secondPeer := open(t, &r, testHomeID)
		for _, c := range []*Conn{first, second} {
			// Independent encoding of the two length-prefixed strings.
			state := c.ConnectionState()
			want, err := state.ExportKeyingMaterial("EXPORTER-ataniq-desk-v1", []byte("\x00\x00\x00\x09test-home\x00\x00\x00\x0dataniq-desk/1"), 32)
			if err != nil {
				t.Fatal(err)
			}
			if !c.ChannelBinding.Matches(want) {
				t.Fatal("client binding differs from the expected TLS exporter")
			}
		}
		if !firstPeer.ChannelBinding.Matches(first.ChannelBinding[:]) || !secondPeer.ChannelBinding.Matches(second.ChannelBinding[:]) {
			t.Fatal("client and server derived different bindings on the same connection")
		}
		if secondPeer.ChannelBinding.Matches(first.ChannelBinding[:]) {
			t.Fatal("second connection accepted the first connection's binding")
		}
	})

	t.Run("DifferentHome", func(t *testing.T) {
		var r Registry
		enroll(t, &r, device, p)
		c, peer := open(t, &r, "other-home")
		if peer.ChannelBinding.Matches(c.ChannelBinding[:]) {
			t.Fatal("binding matched with a different home ID on the same connection")
		}
	})

	t.Run("RevokedDuringHandshake", func(t *testing.T) {
		var r Registry
		enroll(t, &r, device, p)
		enrollment := r.snapshot()[device.Fingerprint()]
		called := false
		raw, done := serve(t, &r, server, HandlerFunc(func(context.Context, Peer, net.Conn) error {
			called = true
			return nil
		}))
		cfg := deskTLS(seatgate.ClientTLS(device, server.Fingerprint(), ""))
		// The server has received ClientHello and requested the certificate,
		// but cannot finish the handshake until this callback returns it.
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			if !r.Revoke(p.ID) {
				return nil, errors.New("expected to revoke the enrolled device")
			}
			return &cfg.Certificates[0], nil
		}
		if err := handshake(t.Context(), tls.Client(raw, cfg)); err != nil {
			t.Fatal(err)
		}
		if err := receive(t, done); err == nil || err.Error() != "desklink: device enrollment changed during handshake" {
			t.Fatalf("expected admission refusal after revocation, got %v", err)
		}
		if called || len(enrollment.live) != 0 {
			t.Fatal("revoked handshake yielded an attached peer")
		}
	})
}
