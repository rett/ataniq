package desklink

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"ataniq/internal/seatgate"
)

func TestDeskLinkEnrollRefusals(t *testing.T) {
	var r Registry
	pin := identity(t).Fingerprint()
	viewer := Principal{ID: "desk", Role: "viewer"}
	for _, tc := range []struct {
		pin string
		p   Principal
	}{
		{"bad-pin", viewer},
		{pin, Principal{Role: "viewer"}},
		{pin, Principal{ID: "desk", Role: " "}},
	} {
		if err := r.Enroll(tc.pin, tc.p); err == nil {
			t.Fatal("accepted invalid enrollment")
		}
	}
	if err := r.Enroll(pin, viewer); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Principal{viewer, {ID: "other", Role: "writer"}} {
		if err := r.Enroll(pin, p); err == nil {
			t.Fatal("duplicate pin replaced the enrollment")
		}
	}
	if got := r.snapshot()[pin].principal; got != viewer {
		t.Fatalf("enrollment changed to %+v", got)
	}
}

func TestRevokeClosesEveryLinkForOnePrincipal(t *testing.T) {
	var r Registry
	server, first, second, other := identity(t), identity(t), identity(t), identity(t)
	for _, id := range []seatgate.Identity{first, second} {
		enroll(t, &r, id, Principal{ID: "one", Role: "viewer"})
	}
	enroll(t, &r, other, Principal{ID: "two", Role: "viewer"})
	type link struct {
		conn net.Conn
		done <-chan error
	}
	open := func(id seatgate.Identity) link {
		ready := make(chan bool, 1)
		raw, done := serve(t, &r, server, HandlerFunc(func(ctx context.Context, _ Peer, c net.Conn) error {
			ready <- true
			_, err := io.Copy(c, c)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}))
		c := connect(t, raw, id, server)
		receive(t, ready)
		return link{c, done}
	}
	links := []link{open(first), open(first), open(second)}
	survivor := open(other)
	if !r.Revoke("one") || r.Revoke("one") {
		t.Fatal("expected only the first revoke to remove pins")
	}
	for _, link := range links {
		if err := receive(t, link.done); !errors.Is(err, context.Canceled) {
			t.Fatalf("handler ended with %v", err)
		}
		if _, err := link.conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("revoked connection read: %v", err)
		}
	}
	for _, id := range []seatgate.Identity{first, second} {
		raw, done := serve(t, &r, server, HandlerFunc(func(context.Context, Peer, net.Conn) error {
			return nil
		}))
		c, err := Connect(t.Context(), raw, id, server.Fingerprint())
		if err == nil {
			c.Read(make([]byte, 1)) // take the server's TLS alert
		}
		if err := receive(t, done); err == nil {
			t.Fatal("a revoked key reconnected")
		}
	}
	if _, err := survivor.conn.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(survivor.conn, b); err != nil || b[0] != 'a' {
		t.Fatalf("other principal lost its link: %q, %v", b, err)
	}
	r.Revoke("two")
	receive(t, survivor.done)
}

type handshakeStarted struct {
	net.Conn
	once sync.Once
	read chan struct{}
}

func (c *handshakeStarted) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.read) })
	return c.Conn.Read(p)
}

func TestRevokeDuringHandshakeCannotInheritNewRole(t *testing.T) {
	var r Registry
	server, device := identity(t), identity(t)
	enroll(t, &r, device, Principal{ID: "old", Role: "viewer"})
	raw, client := net.Pipe()
	defer client.Close()
	c := &handshakeStarted{Conn: raw, read: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- ServeConn(t.Context(), c, server, &r, HandlerFunc(func(context.Context, Peer, net.Conn) error {
			return nil
		}))
	}()
	receive(t, c.read)
	r.Revoke("old")
	enroll(t, &r, device, Principal{ID: "new", Role: "writer"})
	connect(t, client, device, server)
	if err := receive(t, done); err == nil {
		t.Fatal("old handshake acquired the new enrollment")
	}
	raw, next := serve(t, &r, server, HandlerFunc(func(_ context.Context, p Peer, _ net.Conn) error {
		if p.Principal != (Principal{ID: "new", Role: "writer"}) {
			return errors.New("wrong enrollment on fresh handshake")
		}
		return nil
	}))
	connect(t, raw, device, server)
	if err := receive(t, next); err != nil {
		t.Fatal(err)
	}
}
