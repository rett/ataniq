package link

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
	"ataniq/internal/seatgate"
)

func endpointFor(t *testing.T, home, witness seatgate.Identity, f *fixture) *Endpoint {
	t.Helper()
	cert := seatgate.ServerTLS(witness, nil).Certificates[0].Certificate[0]
	e, err := NewEndpoint(home, cert, f.pin, 1)
	must(t, err)
	return e
}

func TestWitnessDialsHome(t *testing.T) {
	for _, network := range []string{"pipe", "tcp"} {
		t.Run(network, func(t *testing.T) {
			f := newFixture(t, "witness")
			var l net.Listener
			var dial dialFunc
			if network == "tcp" {
				l = tcpListener(t)
				dial = (&net.Dialer{}).DialContext
			} else {
				p := &pipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
				l, dial = p, p.dial
			}
			homeID := identity(t, "home")
			e := endpointFor(t, homeID, f.id, f)
			done := make(chan error, 1)
			go func() { done <- e.Serve(l) }()
			defer func() {
				l.Close()
				if !errors.Is(<-done, net.ErrClosed) {
					t.Fatal("listener failed")
				}
			}()
			ctx, cancel := context.WithCancel(t.Context())
			stopped := make(chan error, 1)
			go func() {
				stopped <- f.s.Poll(ctx, Remote{Network: l.Addr().Network(), Address: l.Addr().String(), DialContext: dial, ServerPin: homeID.Fingerprint()}, memoryWitness{f.w})
			}()
			defer func() {
				cancel()
				if !errors.Is(<-stopped, context.Canceled) {
					t.Fatal("poller failed")
				}
			}()
			call, finish := context.WithTimeout(ctx, 3*time.Second)
			defer finish()
			state := f.enroll(t, f.w.Administration())
			c, err := e.Connect(call, f.slot, f.home, f.session)
			must(t, err)
			q := cp.Claim{Mutation: cp.Mutation{ID: cp.ID{2}, Expected: state}, NewSessionPublicKey: public(f.session)}
			first, err := c.Claim(call, q)
			must(t, err)
			again, err := c.Claim(call, q)
			must(t, err)
			if cp.ReceiptDigest(first) != cp.ReceiptDigest(again) {
				t.Fatal("redelivery changed receipt")
			}
		})
	}
}

func TestOneOutstandingMutationPerEnrollment(t *testing.T) {
	f := newFixture(t, "witness")
	q := endpointFor(t, identity(t, "home"), f.id, f).home.queue
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	submit := func(ctx context.Context, slot cp.Slot) <-chan error {
		ch := make(chan error, 1)
		go func() { _, err := q.exchange(ctx, frame.Claim, nil, slot); ch <- err }()
		return ch
	}
	first := submit(ctx, f.slot)
	var active pendingCall
	select {
	case active = <-q.calls:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer func() { active.result <- callResult{}; must(t, <-first) }()
	wait, stop := context.WithCancel(ctx)
	second := submit(wait, f.slot)
	other := submit(ctx, cp.Slot{Witness: "other"})
	select {
	case next := <-q.calls:
		if next.ctx != ctx {
			next.result <- callResult{}
			t.Fatal("second mutation handed off while first active")
		}
		next.result <- callResult{}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	must(t, <-other)
	stop()
	if err := <-second; !errors.Is(err, ErrNotSent) || !errors.Is(err, context.Canceled) {
		t.Fatalf("queued mutation: %v", err)
	}
}

func TestPollShutdownCancelsGrant(t *testing.T) {
	f := newFixture(t, "witness")
	e, r := f.listener(t)
	w := blockedGrant{entered: make(chan struct{})}
	stop := startPolling(t, f.s, r, w)
	result := make(chan error, 1)
	go func() { _, err := e.Connect(t.Context(), f.slot, f.home, f.session); result <- err }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("grant check did not start")
	}
	stop()
	if err := <-result; err == nil {
		t.Fatal("grant was not canceled")
	}
}
