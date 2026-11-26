package link

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"ataniq/internal/assignment"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
	"ataniq/internal/seatgate"
)

func key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func public(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	w             *cp.Memory
	pin           ed25519.PublicKey
	s             *Server
	id            seatgate.Identity
	home, session ed25519.PrivateKey
	slot          cp.Slot
}

func newFixture(t *testing.T, name string) *fixture {
	t.Helper()
	k := key(t)
	w, err := cp.NewMemory(name, 1, k)
	must(t, err)
	id := identity(t, "authority")
	slot := cp.Slot{Witness: name, Authority: name, Store: name}
	s, err := NewServer(id, public(k), Remote{Address: "home", ServerPin: id.Fingerprint()}, slot)
	must(t, err)
	f := &fixture{w, public(k), s, id, key(t), key(t), slot}
	must(t, w.Grant(f.slot, public(f.home), true))
	return f
}
func (f *fixture) endpoint(e Remote) Remote {
	e.ServerPin = f.id.Fingerprint()
	e.WitnessPin = f.pin
	e.Epoch = 1
	return e
}
func (f *fixture) listener(t *testing.T) (*Endpoint, Remote) {
	id := identity(t, "home")
	e := endpointFor(t, id, f.id, f)
	r := f.endpoint(serve(t, e.Serve))
	r.ServerPin = id.Fingerprint()
	return e, r
}
func (f *fixture) serve(t *testing.T) *Endpoint {
	e, r := f.listener(t)
	startPolling(t, f.s, r, memoryWitness{f.w})
	return e
}
func (f *fixture) enroll(t *testing.T, a cp.Administration) cp.State {
	t.Helper()
	hID, err := cp.KeyID(public(f.home))
	must(t, err)
	h := assignment.Header{Schema: 1, Authority: f.slot.Authority, Store: f.slot.Store, Incarnation: f.slot.Store, LedgerID: f.slot.Store, KeyID: hID}
	raw, err := h.Bytes()
	must(t, err)
	q := cp.Enrollment{ID: cp.ID{1}, Slot: f.slot, HeaderBytes: raw, HomePublicKey: public(f.home)}
	q.ProofOfPossession, err = cp.ProveEnrollment(f.home, q)
	must(t, err)
	r, err := a.Enroll(t.Context(), q)
	must(t, err)
	ack, err := cp.VerifyReceipt(f.pin, r)
	must(t, err)
	return ack.State
}

func TestPinAndDowngrade(t *testing.T) {
	f := newFixture(t, "witness")
	_, e := f.listener(t)
	t.Run("wrong pinned server", func(t *testing.T) {
		bad := e
		bad.ServerPin = identity(t, "other").Fingerprint()
		calls := 0
		bad.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) { calls++; return e.DialContext(ctx, n, a) }
		tr, err := bad.transport(f.id)
		must(t, err)
		_, err = tr.connect(t.Context())
		if !errors.Is(err, cp.ErrRefused) || calls != 1 {
			t.Fatalf("pin mismatch: calls %d, err %v", calls, err)
		}
	})
	for _, tc := range []struct {
		name    string
		version uint16
		alpn    string
	}{
		{"TLS 1.2", tls.VersionTLS12, ALPN}, {"seat ALPN", tls.VersionTLS13, seatgate.ALPN}, {"missing ALPN", tls.VersionTLS13, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := e.DialContext(t.Context(), e.Network, e.Address)
			must(t, err)
			defer raw.Close()
			raw.SetDeadline(time.Now().Add(2 * time.Second))
			cfg := clientTLS(e.ServerPin, f.id)
			cfg.MinVersion = tc.version
			cfg.MaxVersion = tc.version
			cfg.VerifyConnection = nil // model a peer willing to downgrade
			cfg.NextProtos = nil
			if tc.alpn != "" {
				cfg.NextProtos = []string{tc.alpn}
			}
			c := tls.Client(raw, cfg)
			if err := c.Handshake(); err == nil {
				// Even a refused application call would prove that the
				// downgraded handshake reached checkpoint dispatch.
				if frame.New(frame.Release, nil).WriteToStream(c) == nil {
					if _, err := frame.ReadMessage(c); err == nil {
						t.Fatal("downgrade reached the application")
					}
				}
			}
		})
	}
}
func TestMalformedAndSlowFramesClose(t *testing.T) {
	f := newFixture(t, "witness")
	f.s.timeout = 75 * time.Millisecond
	e := f.endpoint(testOwner(t, f.s, f.w.Administration(), f.id.Fingerprint()))
	tr, err := e.transport(f.id)
	must(t, err)
	for _, tc := range []struct {
		name  string
		input []byte
	}{
		{"oversize", binary.BigEndian.AppendUint32(nil, cp.MaxFrame+1)},
		{"unknown version", []byte{0, 0, 0, 2, 2, byte(frame.Granted)}},
		{"partial prefix", []byte{0}},
		{"partial payload", []byte{0, 0, 0, 16, 1}},
		{"response as request", []byte{0, 0, 0, 2, 1, byte(frame.Granted)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := tr.connect(t.Context())
			must(t, err)
			defer c.NetConn().Close()
			c.SetDeadline(time.Now().Add(2 * time.Second))
			_, err = c.Write(tc.input)
			must(t, err)
			_, err = c.Read(make([]byte, 1))
			var timeout net.Error
			if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
				t.Fatalf("peer did not close: %v", err)
			}
		})
	}
}
func TestConnectionCap(t *testing.T) {
	f := newFixture(t, "witness")
	f.s.ownerCapacity = 1
	e := f.endpoint(testOwner(t, f.s, f.w.Administration(), f.id.Fingerprint()))
	tr, err := e.transport(f.id)
	must(t, err)
	// Occupy the one permitted connection through its handshake.
	busy, err := tr.connect(t.Context())
	must(t, err)
	defer busy.NetConn().Close()
	_, err = busy.Write([]byte{0})
	must(t, err)
	c, err := tr.connect(t.Context())
	must(t, err)
	defer c.NetConn().Close()
	c.SetDeadline(time.Now().Add(time.Second))
	_, err = c.Read(make([]byte, 1))
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("excess connection stayed open: %v", err)
	}
}

func TestReadAuthenticationAndOwnerIsolation(t *testing.T) {
	f := newFixture(t, "witness")
	e := f.serve(t)
	f.enroll(t, f.w.Administration())
	c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
	must(t, err)
	home := c.(*client)
	q := cp.ReadRequest{Slot: f.slot, Challenge: cp.ID{3}}
	m, err := home.signedRead(frame.Read, q)
	must(t, err)
	q.ReceiptID = cp.ID{1}
	m.Fields[0], err = readBody(q, home.wid, "read")
	must(t, err)
	_, err = home.exchange(t.Context(), m)
	if !errors.Is(err, cp.ErrRefused) {
		t.Fatalf("changed receipt ID: %v", err)
	}
	for _, k := range []frame.Kind{frame.OwnerRead, frame.Enroll, frame.Release, frame.Retire} {
		req := frame.New(k, []byte("{}"))
		if k == frame.Enroll {
			req.Fields = append(req.Fields, nil)
		}
		_, err = home.exchange(t.Context(), req)
		if !errors.Is(err, cp.ErrRefused) {
			t.Fatalf("owner operation %v on home endpoint: %v", k, err)
		}
	}
	ownerID := identity(t, "owner")
	oe := f.endpoint(testOwner(t, f.s, f.w.Administration(), ownerID.Fingerprint()))
	for _, id := range []seatgate.Identity{{}, identity(t, "untrusted")} {
		tr, err := oe.transport(id)
		must(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		_, err = tr.exchange(ctx, frame.New(frame.OwnerRead, m.Fields[0]))
		cancel()
		if !errors.Is(err, cp.ErrRefused) {
			t.Fatalf("owner read without owner certificate: %v", err)
		}
	}
}

func TestRatesAcrossConnections(t *testing.T) {
	f := newFixture(t, "witness")
	e := f.serve(t)
	state := f.enroll(t, f.w.Administration())
	c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
	must(t, err)
	q := cp.ReadRequest{Slot: f.slot}
	for i := 1; i < 60; i++ {
		q.Challenge[0] = byte(i)
		_, err = c.Read(t.Context(), q)
		must(t, err)
	}
	q.Challenge[0]++
	if _, err := c.Read(t.Context(), q); !errors.Is(err, cp.ErrUnavailable) {
		t.Fatalf("read limit: %v", err)
	}
	_, err = c.Claim(t.Context(), cp.Claim{Mutation: cp.Mutation{ID: cp.ID{2}, Expected: state}, NewSessionPublicKey: public(f.session)})
	if !errors.Is(err, cp.ErrUnavailable) {
		t.Fatalf("mutation exceeded shared allowance: %v", err)
	}
	_, err = e.Connect(t.Context(), f.slot, f.home, key(t))
	if !errors.Is(err, cp.ErrUnavailable) {
		t.Fatalf("grant check exceeded shared allowance: %v", err)
	}
	q.ReceiptID = cp.ID{1}
	v, err := c.Read(t.Context(), q)
	must(t, err)
	if v.Prior == nil {
		t.Fatal("missing recovery receipt")
	}
	for range 59 {
		_, err = c.Read(t.Context(), q)
		must(t, err)
	}
	if _, err = c.Read(t.Context(), q); !errors.Is(err, cp.ErrUnavailable) {
		t.Fatalf("recovery allowance: %v", err)
	}
}

type unavailableReader struct{ cp.Administration }

func (a unavailableReader) Read(ctx context.Context, q cp.ReadRequest) (cp.View, error) {
	v, _ := a.Administration.Read(ctx, q)
	return v, cp.ErrUnavailable
}

func TestReadFailureWithReadyEvidence(t *testing.T) {
	f := newFixture(t, "witness")
	f.enroll(t, f.w.Administration())
	id := identity(t, "owner")
	e := f.endpoint(testOwner(t, f.s, unavailableReader{f.w.Administration()}, id.Fingerprint()))
	a, err := e.Administration(id)
	must(t, err)
	q := cp.ReadRequest{Slot: f.slot, Challenge: cp.ID{1}, ReceiptID: cp.ID{1}}
	v, err := a.Read(t.Context(), q)
	if !errors.Is(err, cp.ErrUnavailable) || len(v.Fresh.Body) != 0 {
		t.Fatalf("backend failure returned an admissible view: %v", err)
	}
	f.w.Inhibit(true)
	q.Challenge = cp.ID{2}
	v, err = a.Read(t.Context(), q)
	if !errors.Is(err, cp.ErrUnavailable) || !errors.Is(cp.VerifyView(f.pin, q, v), cp.ErrUnavailable) || v.Prior == nil {
		t.Fatalf("missing signed inhibited evidence: %v", err)
	}
}

type waitingWitness struct {
	memoryWitness
	received, release chan struct{}
}

func (w waitingWitness) RawSubmit(ctx context.Context, slot cp.Slot, h ed25519.PublicKey, op string, b, hs, ss []byte) (cp.Receipt, error) {
	r, err := w.Memory.RawSubmit(ctx, slot, h, op, b, hs, ss)
	close(w.received)
	select {
	case <-w.release:
	case <-ctx.Done():
	}
	return r, err
}

func TestCancelAfterCommit(t *testing.T) {
	f := newFixture(t, "witness")
	w := waitingWitness{memoryWitness{f.w}, make(chan struct{}), make(chan struct{})}
	e, r := f.listener(t)
	startPolling(t, f.s, r, w)
	s := f.enroll(t, f.w.Administration())
	c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
	must(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	q := cp.Claim{Mutation: cp.Mutation{ID: cp.ID{2}, Expected: s}, NewSessionPublicKey: public(f.session)}
	done := make(chan error, 1)
	go func() { _, err := c.Claim(ctx, q); done <- err }()
	select {
	case <-w.received:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach witness")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, cp.ErrUnknownOutcome) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel after send: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not interrupt response read")
	}
	close(w.release)
	v, err := c.Read(t.Context(), cp.ReadRequest{Slot: f.slot, Challenge: cp.ID{3}, ReceiptID: q.ID})
	must(t, err)
	if v.Prior == nil || v.Current.Revision != 2 {
		t.Fatal("could not recover cancelled claim")
	}
}

func TestReconnectAfterRestart(t *testing.T) {
	f := newFixture(t, "witness")
	e, r := f.listener(t)
	stop := startPolling(t, f.s, r, memoryWitness{f.w})
	f.enroll(t, f.w.Administration())
	c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
	must(t, err)
	stop()
	read := make(chan error, 1)
	go func() {
		_, err := c.Read(t.Context(), cp.ReadRequest{Slot: f.slot, Challenge: cp.ID{3}, ReceiptID: cp.ID{1}})
		read <- err
	}()
	restarted, err := NewServer(f.id, f.pin, r, f.slot)
	must(t, err)
	startPolling(t, restarted, r, memoryWitness{f.w})
	select {
	case err := <-read:
		must(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("read did not survive witness restart")
	}
}

func TestEscapedIdentifiersFit(t *testing.T) {
	f := newFixture(t, strings.Repeat("<", assignment.MaxReferenceBytes))
	e := f.serve(t)
	ownerID := identity(t, "owner")
	oe := f.endpoint(testOwner(t, f.s, f.w.Administration(), ownerID.Fingerprint()))
	a, err := oe.Administration(ownerID)
	must(t, err)
	s := f.enroll(t, a)
	c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
	must(t, err)
	r, err := c.Claim(t.Context(), cp.Claim{Mutation: cp.Mutation{ID: cp.ID{2}, Expected: s}, NewSessionPublicKey: public(f.session)})
	must(t, err)
	ack, err := cp.VerifyReceipt(f.pin, r)
	must(t, err)
	next := ack.State.Head
	next.Sequence++
	next.Digest = strings.Repeat("b", 64)
	r, err = c.Advance(t.Context(), cp.Advance{Mutation: cp.Mutation{ID: cp.ID{3}, Expected: ack.State}, Next: next})
	must(t, err)
	v, err := c.Read(t.Context(), cp.ReadRequest{Slot: f.slot, Challenge: cp.ID{4}, ReceiptID: cp.ID{3}})
	must(t, err)
	if v.Prior == nil || !bytes.Equal(v.Prior.Body, r.Body) {
		t.Fatal("large reply lost its historical receipt")
	}
}
