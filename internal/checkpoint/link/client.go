package link

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
	"ataniq/internal/seatgate"
)

// Endpoint pins the authority's transport key and its independent witness
// signing key and epoch. DialContext defaults to net.Dialer.DialContext.
type Endpoint struct {
	Network, Address string
	ServerPin        string
	WitnessPin       ed25519.PublicKey
	Epoch            uint64
	DialContext      func(context.Context, string, string) (net.Conn, error)
}

type transport struct {
	Endpoint
	tls *tls.Config
	wid string
}

func (e Endpoint) transport(owner seatgate.Identity) (transport, error) {
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
	return transport{e, clientTLS(e.ServerPin, owner), wid}, nil
}

type client struct {
	transport
	slot          cp.Slot
	home, session ed25519.PrivateKey
}

func validPrivate(k ed25519.PrivateKey) bool {
	return len(k) == ed25519.PrivateKeySize && k.Equal(ed25519.NewKeyFromSeed(k.Seed()))
}

// Connect verifies the home grant with a signed grant check. A grant
// can already exist when the owner has yet to enroll the slot.
func (e Endpoint) Connect(ctx context.Context, slot cp.Slot, home, session ed25519.PrivateKey) (cp.Client, error) {
	t, err := e.transport(seatgate.Identity{})
	if err != nil {
		return nil, err
	}
	if !validSlot(slot) || !validPrivate(home) || !validPrivate(session) {
		return nil, &cp.Reason{Kind: cp.InvalidRequest}
	}
	h, s := home.Public().(ed25519.PublicKey), session.Public().(ed25519.PublicKey)
	if bytes.Equal(h, s) || bytes.Equal(h, t.WitnessPin) || bytes.Equal(s, t.WitnessPin) {
		return nil, &cp.Reason{Kind: cp.KeyRoleConflict}
	}
	c := &client{t, slot, slices.Clone(home), slices.Clone(session)}
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
func (e Endpoint) Administration(id seatgate.Identity) (cp.Administration, error) {
	if id.Fingerprint() == "" {
		return nil, cp.ErrRefused
	}
	t, err := e.transport(id)
	if err != nil {
		return nil, err
	}
	return &owner{t}, nil
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
			conn := tls.Client(raw, t.tls)
			err = conn.HandshakeContext(ctx)
			if err == nil {
				done()
				return conn, nil
			}
			raw.Close()
		}
		done()
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
		case <-time.After(delay):
		}
		delay = min(2*delay, time.Second)
	}
}

type answer struct {
	receipt cp.Receipt
	view    cp.View
}

// After a write starts, a mutation may have committed even on a write error.
// Only a recognized witness refusal or a matching signed receipt resolves it.
func (t transport) exchange(ctx context.Context, m frame.Message) (answer, error) {
	data, err := m.Marshal()
	if err != nil {
		return answer{}, &cp.Reason{Kind: cp.InvalidRequest}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	conn, err := t.connect(ctx)
	if err != nil {
		return answer{}, err
	}
	raw := conn.NetConn()
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if raw.SetDeadline(deadline) != nil || ctx.Err() != nil {
		return answer{}, errors.Join(cp.ErrUnavailable, ctx.Err())
	}
	mutation := m.Kind != frame.Connect && m.Kind != frame.Read && m.Kind != frame.OwnerRead
	result := cp.ErrUnavailable
	if mutation {
		result = cp.ErrUnknownOutcome
	}
	n, err := conn.Write(data)
	if err != nil || n != len(data) {
		return answer{}, errors.Join(result, ctx.Err())
	}
	reply, err := frame.ReadMessage(conn)
	if err != nil {
		return answer{}, errors.Join(result, ctx.Err())
	}
	if reply.Kind == frame.Failure {
		err := decodeError(reply.Fields[0])
		if err == frame.ErrProtocol || err == cp.ErrUnknownOutcome {
			return answer{}, result
		}
		return answer{}, err
	}
	if m.Kind == frame.Connect {
		if reply.Kind != frame.Granted {
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
	b, err := readBody(q, c.wid, operation[kind])
	if err != nil {
		return frame.Message{}, err
	}
	sig, err := cp.Sign(cp.HomeNamespace, c.home, b)
	if err != nil {
		return frame.Message{}, err
	}
	return frame.New(kind, b, c.home.Public().(ed25519.PublicKey), sig), nil
}
func (c *owner) Read(ctx context.Context, q cp.ReadRequest) (cp.View, error) {
	b, err := readBody(q, c.wid, "read")
	if err != nil {
		return cp.View{}, err
	}
	return c.read(ctx, q, frame.New(frame.OwnerRead, b))
}
func (t transport) read(ctx context.Context, q cp.ReadRequest, m frame.Message) (cp.View, error) {
	a, err := t.exchange(ctx, m)
	if err != nil {
		return cp.View{}, err
	}
	err = cp.VerifyView(t.WitnessPin, q, a.view)
	if err != nil && !errors.Is(err, cp.ErrUnavailable) {
		return cp.View{}, err
	}
	if a.view.Current.Epoch != t.Epoch {
		return cp.View{}, cp.ErrStale
	}
	return a.view, err
}

func (c *client) Claim(ctx context.Context, q cp.Claim) (cp.Receipt, error) {
	if !bytes.Equal(q.NewSessionPublicKey, c.session.Public().(ed25519.PublicKey)) {
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
	hs, err := cp.Sign(cp.HomeNamespace, c.home, b)
	if err != nil {
		return cp.Receipt{}, err
	}
	ss, err := cp.Sign(cp.SessionNamespace, c.session, b)
	if err != nil {
		return cp.Receipt{}, err
	}
	return c.rawSubmit(ctx, k, c.slot, c.home.Public().(ed25519.PublicKey), b, hs, ss)
}
func (t transport) rawSubmit(ctx context.Context, k frame.Kind, slot cp.Slot, home ed25519.PublicKey, b, hs, ss []byte) (cp.Receipt, error) {
	route, err := json.Marshal(slot)
	if err != nil {
		return cp.Receipt{}, err
	}
	return t.mutate(ctx, frame.New(k, route, home, b, hs, ss), b)
}

// RawSubmit is the checkpointtest harness hook for qualifying a remote
// witness. It forwards an existing claim or advance and its detached proofs
// verbatim through the same exchange and receipt checks as the home client.
func (e Endpoint) RawSubmit(ctx context.Context, slot cp.Slot, home ed25519.PublicKey, op string, body, homeSig, sessionSig []byte) (cp.Receipt, error) {
	t, err := e.transport(seatgate.Identity{})
	if err != nil {
		return cp.Receipt{}, err
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
