package durable

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link"
	"ataniq/internal/seatgate"
)

var _ link.Witness = (*Witness)(nil)

// Run both TLS endpoints on a private in-process stream, including on
// builders that lack permission to open sockets.
type listener struct {
	incoming chan net.Conn
	closed   chan struct{}
	once     sync.Once
}

func (l *listener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (l *listener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.incoming:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *listener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	a, b := net.Pipe()
	select {
	case l.incoming <- b:
		return a, nil
	case <-ctx.Done():
		a.Close()
		b.Close()
		return nil, ctx.Err()
	}
}
func tlsIdentity(t *testing.T, label string) seatgate.Identity {
	cert, key, err := seatgate.GenerateIdentity(rand.Reader, time.Now(), label)
	must(t, err)
	id, err := seatgate.ParseIdentity(cert, key)
	must(t, err)
	return id
}

func TestReopenedWitnessOverLink(t *testing.T) {
	c := config(t, testKey("link witness"))
	c.ReceiptLimit = 3
	w := create(t, c)
	q := fixtureEnrollment(t, w)
	enrolled, err := w.Administration().Enroll(t.Context(), q)
	must(t, err)
	must(t, w.Close())
	w = reopen(t, c)
	must(t, w.Grant(q.Slot, q.HomePublicKey, true))
	w.Inhibit(false)
	homeID, witnessID := tlsIdentity(t, "home"), tlsIdentity(t, "witness")
	cert := seatgate.ServerTLS(witnessID, nil).Certificates[0].Certificate[0]
	ep, err := link.NewEndpoint(homeID, cert, c.Pin, c.Identity.Epoch, q.Slot, link.TrustSources{netip.MustParsePrefix("127.0.0.1/32")})
	must(t, err)
	l := &listener{incoming: make(chan net.Conn), closed: make(chan struct{})}
	serving := make(chan error, 1)
	go func() { serving <- ep.Serve(l) }()
	defer func() {
		must(t, l.Close())
		if err := <-serving; !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	}()
	remote := link.Remote{Address: "loopback", ServerPin: homeID.Fingerprint(), DialContext: l.dial}
	server, err := link.NewServer(witnessID, c.Pin, remote, q.Slot)
	must(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	polling := make(chan error, 1)
	go func() { polling <- server.Poll(ctx, w) }()
	defer func() {
		cancel()
		if err := <-polling; !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}()
	session := testKey("link session")
	client, err := ep.Connect(ctx, q.Slot, testKey("fixture home"), session)
	must(t, err)
	view, err := client.Read(ctx, cp.ReadRequest{Slot: q.Slot, Challenge: cp.ID{2}, ReceiptID: q.ID})
	must(t, err)
	if view.Prior == nil || cp.ReceiptDigest(*view.Prior) != cp.ReceiptDigest(enrolled) {
		t.Fatal("link lost original receipt")
	}
	claim := cp.Claim{Mutation: cp.Mutation{ID: cp.ID{3}, Expected: view.Current}, NewSessionPublicKey: public(session)}
	r, err := client.Claim(ctx, claim)
	must(t, err)
	prior, err := client.Settle(ctx, c.Identity.Epoch, q.Slot, claim.ID)
	must(t, err)
	if cp.ReceiptDigest(prior) != cp.ReceiptDigest(r) {
		t.Fatal("changed settlement receipt")
	}
	_, err = client.Settle(ctx, c.Identity.Epoch, q.Slot, cp.ID{4})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal("signed fence disposition", err)
	}
	_, err = client.Settle(ctx, c.Identity.Epoch, q.Slot, cp.ID{5})
	if !errors.Is(err, cp.ErrCapacity) || errors.Is(err, cp.ErrUnavailable) {
		t.Fatalf("link capacity disposition: %v", err)
	}
}
