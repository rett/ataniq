package link

import (
	"context"
	"crypto/ed25519"
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
	e, err := NewEndpoint(home, cert, f.pin, 1, f.slot, loopbackTrust())
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
				f.s.home = Remote{Network: l.Addr().Network(), Address: l.Addr().String(), DialContext: dial, ServerPin: homeID.Fingerprint()}
				stopped <- f.s.Poll(ctx, memoryWitness{f.w})
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
			if err := f.s.Poll(call, memoryWitness{f.w}); !errors.Is(err, cp.ErrRefused) {
				t.Fatal("second poll loop", err)
			}
			foreign := f.slot
			foreign.Store += "-other"
			if _, err := e.Connect(call, foreign, f.home, f.session); !errors.Is(err, cp.ErrRefused) {
				t.Fatal("foreign enrollment", err)
			}
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
	first := make(chan error, 1)
	go func() { _, err := q.exchange(ctx, frame.Claim, nil, false); first <- err }()
	active := <-q.calls
	if _, err := q.exchange(ctx, frame.Advance, nil, false); !errors.Is(err, ErrNotSent) {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() { _, err := q.exchange(ctx, frame.Read, nil, true); read <- err }()
	select {
	case call := <-q.recoveryCalls:
		call.result <- callResult{}
	case <-ctx.Done():
		t.Fatal("recovery read was not admitted")
	}
	must(t, <-read)
	active.result <- callResult{}
	must(t, <-first)
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

func TestPermanentPollConfigurationError(t *testing.T) {
	f := newFixture(t, "witness")
	for _, home := range []Remote{
		{Network: "tcp", Address: "no-port", ServerPin: f.id.Fingerprint()},
		{Network: "udp", Address: "localhost:1", ServerPin: f.id.Fingerprint()},
	} {
		_, err := NewServer(f.id, f.pin, home, f.slot)
		var config *ConfigError
		if !errors.As(err, &config) {
			t.Fatalf("invalid configuration: %v", err)
		}
	}
}

type slowCurrentRead struct {
	memoryWitness
	entered chan struct{}
}

func (w slowCurrentRead) ReadHome(ctx context.Context, q cp.ReadRequest, key ed25519.PublicKey) (cp.View, error) {
	if q.ReceiptID == (cp.ID{}) {
		close(w.entered)
		<-ctx.Done()
		return cp.View{}, ctx.Err()
	}
	return w.Memory.ReadHome(ctx, q, key)
}
func TestRecoveryBehindSlowOrdinaryRead(t *testing.T) {
	f := newFixture(t, "witness")
	e, r := f.listener(t)
	w := slowCurrentRead{memoryWitness{f.w}, make(chan struct{})}
	startPolling(t, f.s, r, w)
	f.enroll(t, f.w.Administration())
	c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
	must(t, err)
	go c.Read(t.Context(), cp.ReadRequest{Slot: f.slot, Challenge: cp.ID{1}})
	<-w.entered
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	_, err = c.Settle(ctx, 1, f.slot, cp.ID{2})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatalf("settlement behind blocked ordinary read: %v", err)
	}
}

func TestNextPollServesRecoveryFirst(t *testing.T) {
	q := requestQueue{calls: make(chan pendingCall, 1), recoveryCalls: make(chan pendingCall, 1)}
	q.calls <- pendingCall{kind: frame.Connect}
	q.recoveryCalls <- pendingCall{kind: frame.Settle}
	for _, kind := range []frame.Kind{frame.Settle, frame.Connect} {
		call, ready := q.nextCall()
		if !ready || call.kind != kind {
			t.Fatalf("next poll got %v, want %v", call.kind, kind)
		}
	}
}
