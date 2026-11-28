// Package desklink authenticates a device connection to the desk using the
// seatgate TLS identities and key pins. The caller supplies the transport.
// Its handler owns application framing and authorization for the proven peer.
package desklink

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"maps"
	"net"
	"slices"
	"time"

	"ataniq/internal/seatgate"
)

// ALPN identifies the desk link in both directions of the TLS handshake.
const ALPN = "ataniq-desk/1"

// Handler serves one authenticated connection until it returns. Revocation or
// cancellation of ServeConn's context cancels ctx and closes the transport.
// A handler may be serving several connections concurrently.
type Handler interface {
	ServeDesk(context.Context, Peer, net.Conn) error
}

// HandlerFunc adapts a connection-serving function to Handler.
type HandlerFunc func(context.Context, Peer, net.Conn) error

func (f HandlerFunc) ServeDesk(ctx context.Context, peer Peer, conn net.Conn) error {
	return f(ctx, peer, conn)
}

// deskTLS specializes a fresh seatgate config's protocol check. Certificate
// shape, usage and key pins are checked by seatgate's VerifyPeerCertificate.
func deskTLS(cfg *tls.Config) *tls.Config {
	cfg.NextProtos = []string{ALPN}
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if cs.Version != tls.VersionTLS13 || cs.NegotiatedProtocol != ALPN {
			return errors.New("desklink: TLS 1.3 and " + ALPN + " are required")
		}
		return nil
	}
	return cfg
}

func handshake(ctx context.Context, c *tls.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.HandshakeContext(ctx)
}

// Connect authenticates the server's pinned key and presents the device key
// over raw. The caller owns the resulting TLS stream. On error Connect closes
// raw. As in TLS 1.3 generally, the first read may report rejection of the
// client certificate after the client-side handshake has completed.
func Connect(ctx context.Context, raw net.Conn, device seatgate.Identity, pinnedServer string) (*tls.Conn, error) {
	c := tls.Client(raw, deskTLS(seatgate.ClientTLS(device, pinnedServer, "")))
	if err := handshake(ctx, c); err != nil {
		raw.Close()
		return nil, err
	}
	return c, nil
}

// ServeConn owns raw and closes it on return. It authenticates the device and
// registers the session for revocation before giving h any application bytes.
// It returns the handshake, admission or handler error to the local caller.
func ServeConn(ctx context.Context, raw net.Conn, server seatgate.Identity, r *Registry, h Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &session{raw: raw, cancel: cancel}
	var enrolledDevice *device
	defer func() {
		raw.Close()
		if enrolledDevice != nil {
			r.detach(enrolledDevice, s)
		}
	}()
	if r == nil || h == nil {
		return errors.New("desklink: a registry and handler are required")
	}
	enrolled := r.snapshot()
	cfg := deskTLS(seatgate.ServerTLS(server, slices.Collect(maps.Keys(enrolled))))
	c := tls.Server(raw, cfg)
	if err := handshake(ctx, c); err != nil {
		return err
	}
	// ServerTLS requires exactly one verified client certificate.
	sum := sha256.Sum256(c.ConnectionState().PeerCertificates[0].RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(sum[:])
	d := enrolled[pin]
	if !r.attach(pin, d, s) {
		return errors.New("desklink: device enrollment changed during handshake")
	}
	enrolledDevice = d
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.ServeDesk(ctx, Peer{Fingerprint: pin, Principal: d.principal}, c)
}
