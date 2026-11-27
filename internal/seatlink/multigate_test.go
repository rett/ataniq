package seatlink

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// One already-connected pipe, presented to the gate's real TLS listener.
type gatePipe struct {
	net.Conn
	accepted bool
	closed   chan struct{}
	once     sync.Once
}

func (p *gatePipe) Accept() (net.Conn, error) {
	if !p.accepted {
		p.accepted = true
		return p.Conn, nil
	}
	<-p.closed
	return nil, net.ErrClosed
}
func (p *gatePipe) Addr() net.Addr { return p.LocalAddr() }
func (p *gatePipe) Close() error   { p.once.Do(func() { close(p.closed) }); return nil }

func pipeLink(t *testing.T, seat seatgate.Identity, h seatgate.HandlerFunc) (*Daemon, *seatgate.Client) {
	t.Helper()
	id := newIdentity(t, "gate")
	server, client := net.Pipe()
	ln := &gatePipe{Conn: server, closed: make(chan struct{})}
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- seatgate.Serve(ctx, ln, seatgate.ServerConfig{TLS: seatgate.ServerTLS(id, []string{seat.Fingerprint()}), Caps: []string{CapRead, CapGenesis}}, h)
	}()
	t.Cleanup(func() {
		stop()
		client.Close()
		server.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := seatgate.Dial(dialCtx, "gate", seatgate.ClientConfig{TLS: seatgate.ClientTLS(seat, id.Fingerprint(), ""), Caps: Caps(), Dial: func(context.Context, string) (net.Conn, error) { return client, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	d, err := New(c, WithGatePin(id.Fingerprint()))
	if err != nil {
		t.Fatal(err)
	}
	return d, c
}

func TestOfflineGateLeavesOtherInstanceReadable(t *testing.T) {
	seat := newIdentity(t, "seat")
	p, b := vector(t)
	f := &fakeReads{pending: p, bytes: b}
	first, c := pipeLink(t, seat, f.handle)
	second, _ := pipeLink(t, seat, f.handle)
	for _, link := range []*Daemon{first, second} {
		l, err := link.List(t.Context())
		if err != nil || len(l.Pending) != 1 || l.Pending[0].Project != p.Binding.Project || l.Pending[0].ID != p.ID {
			t.Fatalf("listing: %v %v", l, err)
		}
	}
	c.Close()
	if _, err := first.List(t.Context()); !errors.Is(err, seatapp.ErrDaemonUnavailable) {
		t.Fatal(err)
	}
	if _, err := first.Genesis(t.Context(), seatapp.GenesisCall{Verb: seatapp.GenesisVerbList}); !errors.Is(err, seatapp.ErrDaemonUnavailable) {
		t.Fatal(err)
	}
	if _, err := second.Show(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
}
