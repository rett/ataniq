package link

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	jitter "math/rand/v2"
	"net"
	"slices"
	"sync/atomic"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
	"ataniq/internal/seatgate"
)

// Remote pins the authority's transport key and its independent witness
// signing key and epoch. DialContext defaults to net.Dialer.DialContext.
type Remote struct {
	Network, Address string
	ServerPin        string
	WitnessPin       ed25519.PublicKey
	Epoch            uint64
	DialContext      func(context.Context, string, string) (net.Conn, error)
}

type transport struct {
	Remote
	queue    *requestQueue
	recovery bool
	tls      *tls.Config
	wid      string
}

func (e Remote) transport(owner seatgate.Identity) (transport, error) {
	wid, err := cp.KeyID(e.WitnessPin)
	if err != nil || e.Epoch == 0 || !seatgate.ValidFingerprint(e.ServerPin) || e.Address == "" {
		return transport{}, cp.ErrRefused
	}
	if e.Network == "" {
		e.Network = "tcp"
	}
	if e.DialContext == nil {
		e.DialContext = (&net.Dialer{}).DialContext
	}
	e.WitnessPin = slices.Clone(e.WitnessPin)
	return transport{Remote: e, tls: clientTLS(e.ServerPin, owner), wid: wid}, nil
}

type client struct {
	transport
	slot cp.Slot
	keys *signingCapability
}

type signingCapability struct {
	home, session         ed25519.PublicKey
	signHome, signSession func([]byte) ([]byte, error)
}

func newSigningCapability(home, session ed25519.PrivateKey) *signingCapability {
	home, session = slices.Clone(home), slices.Clone(session)
	return &signingCapability{
		home: home.Public().(ed25519.PublicKey), session: session.Public().(ed25519.PublicKey),
		signHome:    func(b []byte) ([]byte, error) { return cp.Sign(cp.HomeNamespace, home, b) },
		signSession: func(b []byte) ([]byte, error) { return cp.Sign(cp.SessionNamespace, session, b) },
	}
}
func (signingCapability) String() string                 { return "[redacted checkpoint keys]" }
func (k signingCapability) GoString() string             { return k.String() }
func (k signingCapability) Format(out fmt.State, _ rune) { io.WriteString(out, k.String()) }

func (c client) MarshalText() ([]byte, error)    { return []byte(c.String()), nil }
func (client) String() string                    { return "[redacted checkpoint client]" }
func (c client) GoString() string                { return c.String() }
func (c client) Format(out fmt.State, verb rune) { io.WriteString(out, c.String()) }

func validPrivate(k ed25519.PrivateKey) bool {
	return len(k) == ed25519.PrivateKeySize && k.Equal(ed25519.NewKeyFromSeed(k.Seed()))
}

// Connect verifies the home grant with a signed grant check. A grant
// can already exist when the owner has yet to enroll the slot.
func (e Endpoint) Connect(ctx context.Context, slot cp.Slot, home, session ed25519.PrivateKey) (cp.Client, error) {
	t := e.home
	if t.queue == nil {
		return nil, cp.ErrRefused
	}
	if slot != e.slot {
		return nil, &cp.Reason{Kind: cp.Unauthorized}
	}
	if !validSlot(slot) || !validPrivate(home) || !validPrivate(session) {
		return nil, &cp.Reason{Kind: cp.InvalidRequest}
	}
	h, s := home.Public().(ed25519.PublicKey), session.Public().(ed25519.PublicKey)
	if bytes.Equal(h, s) || bytes.Equal(h, t.WitnessPin) || bytes.Equal(s, t.WitnessPin) {
		return nil, &cp.Reason{Kind: cp.KeyRoleConflict}
	}
	c := &client{t, slot, newSigningCapability(home, session)}
	q := cp.ReadRequest{Slot: slot}
	rand.Read(q.Challenge[:])
	m, err := c.signedRead(frame.Connect, q)
	if err != nil {
		return nil, err
	}
	if _, err := t.exchange(ctx, m); err != nil {
		return nil, err
	}
	return c, nil
}

type owner struct{ transport }

// Administration dials the separately served owner endpoint with its pinned
// client certificate. An owner read requires neither a home nor a session key.
func (e Remote) Administration(id seatgate.Identity) (cp.Administration, error) {
	if e.Network != "unix" || id.Fingerprint() == "" {
		return nil, cp.ErrRefused
	}
	t, err := e.transport(id)
	if err != nil {
		return nil, err
	}
	return &owner{t}, nil
}

// ErrNotSent means no bytes of the mutation were delivered to the stream.
var ErrNotSent = fmt.Errorf("checkpoint: mutation not sent: %w", cp.ErrUnavailable)

type countedConn struct {
	net.Conn
	written atomic.Uint64
}

func (c *countedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.written.Add(uint64(n))
	return n, err
}
func retryDelay(limit time.Duration) time.Duration {
	return limit/2 + jitter.N(limit/2+1)
}

// TLS exposes peer alerts on stream transports as remote net.OpErrors.
func peerAlert(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "remote error"
}

// TLS over streams wraps its private alert type in a remote net.OpError.
// Match the closed set by its standard-library AlertError representation.
func handshakeRefusal(err error) bool {
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "remote error" || op.Err == nil {
		return false
	}
	for _, a := range []tls.AlertError{40, 42, 43, 44, 45, 46, 48, 49, 70, 71, 116, 120} {
		if op.Err.Error() == a.Error() {
			return true
		}
	}
	return false
}
func replyReadError(err error, consumed int64, fallback error) error {
	if consumed == 0 && handshakeRefusal(err) {
		return cp.ErrRefused
	}
	return fallback
}

// Retry connection establishment before sending any request. Each call has
// a total time bound, including every handshake and backoff.
func (t transport) connect(ctx context.Context) (*tls.Conn, error) {
	delay := 25 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return nil, errors.Join(cp.ErrUnavailable, ctx.Err())
		}
		attempt, done := context.WithTimeout(ctx, handshakeTimeout)
		raw, err := t.DialContext(attempt, t.Network, t.Address)
		if err == nil {
			// Bound handshake I/O, including the TLS alert sent on a pin
			// mismatch. Preserve the verifier's refusal if that write stalls.
			deadline, _ := attempt.Deadline()
			if err := raw.SetDeadline(deadline); err != nil {
				raw.Close()
				done()
				return nil, cp.ErrUnavailable
			}
			conn := tls.Client(&countedConn{Conn: raw}, t.tls)
			err = conn.HandshakeContext(ctx)
			if err == nil {
				done()
				return conn, nil
			}
			raw.Close()
		}
		done()
		var header tls.RecordHeaderError
		if handshakeRefusal(err) || errors.As(err, &header) {
			return nil, cp.ErrRefused
		}
		if peerAlert(err) {
			return nil, errors.Join(cp.ErrUnavailable, err)
		}
		if errors.Is(err, cp.ErrRefused) {
			return nil, err
		}
		// A protocol or authentication refusal ends the attempt. Back off
		// only for transport failures (including a peer lost mid-handshake).
		var netErr net.Error
		if !errors.As(err, &netErr) && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, cp.ErrUnavailable
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(cp.ErrUnavailable, ctx.Err())
		case <-time.After(retryDelay(delay)):
		}
		delay = min(2*delay, time.Second)
	}
}

type answer struct {
	receipt cp.Receipt
	view    cp.View
}

// TLS 1.3 can deliver the peer's client-certificate rejection after the
// dialer's handshake returns. Read while writing the first record so an
// unbuffered stream can carry that alert even if the peer never reads data.
func sendFirst(conn *tls.Conn, data []byte, dialed bool) (reply frame.Message, writeErr, readErr error) {
	write := func() error {
		n, err := conn.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			conn.NetConn().Close()
		}
		return err
	}
	if !dialed {
		if err := write(); err != nil {
			return frame.Message{}, err, nil
		}
		reply, err := frame.ReadMessage(conn)
		return reply, nil, err
	}
	written := make(chan error, 1)
	go func() { written <- write() }()
	first := io.LimitedReader{R: conn, N: cp.MaxFrame + 4}
	reply, err := frame.ReadMessage(&first)
	if err != nil {
		conn.NetConn().Close()
		err = replyReadError(err, cp.MaxFrame+4-first.N, err)
	}
	return reply, <-written, err
}

// After a write starts, a mutation may have committed even on a write error.
// Only a recognized witness refusal or a matching signed receipt resolves it.
func (t transport) exchange(ctx context.Context, m frame.Message) (answer, error) {
	if m.Kind.Mutation() {
		rand.Read(m.Nonce[:])
	}
	data, err := m.Marshal()
	if err != nil {
		return answer{}, &cp.Reason{Kind: cp.InvalidRequest}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	mutation := m.Kind.Mutation()
	if t.queue != nil {
		return t.queue.exchange(ctx, m.Kind, data, t.recovery)
	}
	conn, err := t.connect(ctx)
	if err != nil {
		if mutation && errors.Is(err, cp.ErrUnavailable) {
			return answer{}, errors.Join(ErrNotSent, ctx.Err())
		}
		return answer{}, err
	}
	defer conn.NetConn().Close()
	return t.exchangeOn(ctx, conn, m.Kind, data, true)
}

func (t transport) exchangeOn(ctx context.Context, conn *tls.Conn, kind frame.Kind, data []byte, dialed bool) (answer, error) {
	mutation := kind.Mutation()
	raw := conn.NetConn()
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if raw.SetDeadline(deadline) != nil || ctx.Err() != nil {
		if mutation {
			return answer{}, errors.Join(ErrNotSent, ctx.Err())
		}
		return answer{}, errors.Join(cp.ErrUnavailable, ctx.Err())
	}

	result := cp.ErrUnavailable
	if mutation {
		result = cp.ErrUnknownOutcome
	}
	counter := raw.(*countedConn)
	before := counter.written.Load()
	reply, writeErr, readErr := sendFirst(conn, data, dialed)
	if !mutation && errors.Is(readErr, cp.ErrRefused) {
		return answer{}, cp.ErrRefused
	}
	if writeErr != nil && mutation && counter.written.Load() == before {
		return answer{}, errors.Join(ErrNotSent, ctx.Err())
	}
	if writeErr != nil || readErr != nil {
		return answer{}, errors.Join(result, ctx.Err())
	}
	if reply.Kind == frame.Failure {
		if !cp.VerifyDisposition(t.WitnessPin, t.Epoch, sha256.Sum256(data), string(reply.Fields[0]), reply.Fields[1]) {
			return answer{}, result
		}
		err := decodeError(reply.Fields[0])
		if err == frame.ErrProtocol || err == cp.ErrUnknownOutcome {
			return answer{}, result
		}
		return answer{}, err
	}
	if kind == frame.Connect {
		if reply.Kind != frame.Granted || !cp.VerifyDisposition(t.WitnessPin, t.Epoch, sha256.Sum256(data), "granted", reply.Fields[0]) {
			return answer{}, result
		}
		return answer{}, nil
	}
	if mutation {
		if reply.Kind != frame.Receipt {
			return answer{}, result
		}
		return answer{receipt: receiptOf(reply)}, nil
	}
	var v cp.View
	if reply.Kind != frame.View || !exactJSON(reply.Fields[0], &v.Current) {
		return answer{}, result
	}
	fresh, err := frame.ReadMessage(conn)
	if err != nil || fresh.Kind != frame.Fresh {
		return answer{}, result
	}
	v.Fresh = receiptOf(fresh)
	prior, err := frame.ReadMessage(conn)
	if err != nil || prior.Kind != frame.Prior {
		return answer{}, result
	}
	if len(prior.Fields[0]) != 0 || len(prior.Fields[1]) != 0 {
		r := receiptOf(prior)
		v.Prior = &r
	}
	return answer{view: v}, nil
}

func receiptOf(m frame.Message) cp.Receipt {
	return cp.Receipt{Body: m.Fields[0], Signature: m.Fields[1]}
}

func (c *client) Read(ctx context.Context, q cp.ReadRequest) (cp.View, error) {
	if q.Slot != c.slot {
		return cp.View{}, &cp.Reason{Kind: cp.Unauthorized}
	}
	m, err := c.signedRead(frame.Read, q)
	if err != nil {
		return cp.View{}, err
	}
	return c.read(ctx, q, m)
}

func (c *client) signedRead(kind frame.Kind, q cp.ReadRequest) (frame.Message, error) {
	var epoch uint64
	if kind == frame.Settle {
		epoch = c.Epoch
	}
	b, err := readBody(q, c.wid, operation[kind], epoch)
	if err != nil {
		return frame.Message{}, err
	}
	sig, err := c.keys.signHome(b)
	if err != nil {
		return frame.Message{}, err
	}
	return frame.New(kind, b, c.keys.home, sig), nil
}
func (c *owner) Read(ctx context.Context, q cp.ReadRequest) (cp.View, error) {
	b, err := readBody(q, c.wid, "read", 0)
	if err != nil {
		return cp.View{}, err
	}
	return c.read(ctx, q, frame.New(frame.OwnerRead, b))
}
func (t transport) read(ctx context.Context, q cp.ReadRequest, m frame.Message) (cp.View, error) {
	t.recovery = q.ReceiptID != (cp.ID{})
	a, err := t.exchange(ctx, m)
	if err != nil {
		return cp.View{}, err
	}
	err = cp.VerifyView(t.WitnessPin, q, a.view)
	if err != nil && !errors.Is(err, cp.ErrUnavailable) {
		return cp.View{}, cp.ErrUnavailable
	}
	if a.view.Current.Epoch != t.Epoch {
		return cp.View{}, cp.ErrStale
	}
	if t.queue != nil && a.view.Prior != nil {
		ack, verifyErr := cp.VerifyReceipt(t.WitnessPin, *a.view.Prior)
		if verifyErr == nil && ack.State.Epoch == t.Epoch {
			t.queue.resolve(ack.ID, ack.RequestDigest)
		}
	}
	return a.view, err
}

func (c *client) Claim(ctx context.Context, q cp.Claim) (cp.Receipt, error) {
	if !bytes.Equal(q.NewSessionPublicKey, c.keys.session) {
		return cp.Receipt{}, &cp.Reason{Kind: cp.InvalidRequest}
	}
	return c.submit(ctx, frame.Claim, q.Expected, q)
}
func (c *client) Advance(ctx context.Context, q cp.Advance) (cp.Receipt, error) {
	return c.submit(ctx, frame.Advance, q.Expected, q)
}
func (c *client) submit(ctx context.Context, k frame.Kind, s cp.State, q cp.Intent) (cp.Receipt, error) {
	if s.Slot != c.slot {
		return cp.Receipt{}, &cp.Reason{Kind: cp.Unauthorized}
	}
	b, err := q.Body(c.wid)
	if err != nil {
		return cp.Receipt{}, err
	}
	hs, err := c.keys.signHome(b)
	if err != nil {
		return cp.Receipt{}, err
	}
	ss, err := c.keys.signSession(b)
	if err != nil {
		return cp.Receipt{}, err
	}
	return c.rawSubmit(ctx, k, c.slot, c.keys.home, b, hs, ss)
}
func (t transport) rawSubmit(ctx context.Context, k frame.Kind, slot cp.Slot, home ed25519.PublicKey, b, hs, ss []byte) (cp.Receipt, error) {

	route, err := json.Marshal(slot)
	if err != nil {
		return cp.Receipt{}, err
	}
	intent, err := cp.ParseIntent(operation[k], t.wid, b)
	if err != nil {
		return cp.Receipt{}, err
	}
	var id cp.ID
	switch q := intent.(type) {
	case cp.Claim:
		id = q.ID
	case cp.Advance:
		id = q.ID
	default:
		return cp.Receipt{}, cp.ErrRefused
	}
	retry, err := t.queue.begin(mutationRef{id, cp.RequestDigest(b)})
	if err != nil {
		return cp.Receipt{}, err
	}
	r, err := t.mutate(ctx, frame.New(k, route, home, b, hs, ss), b)
	return r, t.queue.finish(retry, err)
}

// RawSubmit is the checkpointtest harness hook for qualifying a remote
// witness. It forwards an existing claim or advance and its detached proofs
// verbatim through the same exchange and receipt checks as the home client.
func (e Endpoint) RawSubmit(ctx context.Context, slot cp.Slot, home ed25519.PublicKey, op string, body, homeSig, sessionSig []byte) (cp.Receipt, error) {
	t := e.home
	if t.queue == nil {
		return cp.Receipt{}, cp.ErrRefused
	}
	if slot != e.slot {
		return cp.Receipt{}, &cp.Reason{Kind: cp.Unauthorized}
	}
	switch op {
	case "claim":
		return t.rawSubmit(ctx, frame.Claim, slot, home, body, homeSig, sessionSig)
	case "advance":
		return t.rawSubmit(ctx, frame.Advance, slot, home, body, homeSig, sessionSig)
	default:
		return cp.Receipt{}, &cp.Reason{Kind: cp.InvalidRequest}
	}
}
func (c *owner) Enroll(ctx context.Context, q cp.Enrollment) (cp.Receipt, error) {
	b, err := q.Body(c.wid)
	if err != nil {
		return cp.Receipt{}, err
	}
	return c.mutate(ctx, frame.New(frame.Enroll, b, q.ProofOfPossession), b)
}
func (c *owner) Release(ctx context.Context, q cp.Mutation) (cp.Receipt, error) {
	return c.end(ctx, frame.Release, q)
}
func (c *owner) Retire(ctx context.Context, q cp.Retirement) (cp.Receipt, error) {
	return c.end(ctx, frame.Retire, q)
}
func (c *owner) end(ctx context.Context, k frame.Kind, q cp.Intent) (cp.Receipt, error) {
	b, err := q.Body(c.wid)
	if err != nil {
		return cp.Receipt{}, err
	}
	return c.mutate(ctx, frame.New(k, b), b)
}
func (t transport) mutate(ctx context.Context, m frame.Message, b []byte) (cp.Receipt, error) {
	a, err := t.exchange(ctx, m)
	if err != nil {
		return cp.Receipt{}, err
	}
	if err := cp.MatchReceipt(t.WitnessPin, t.Epoch, operation[m.Kind], b, a.receipt); err != nil {
		return cp.Receipt{}, err
	}
	return a.receipt, nil
}
