package link

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"sync"
	"syscall"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
	"ataniq/internal/seatgate"
)

// Witness rechecks the live grant for each request. RawSubmit verifies the
// exact body and both signatures under the witness's mutation contract.
type Witness interface {
	CheckHome(context.Context, cp.Slot, ed25519.PublicKey) error
	ReadHome(context.Context, cp.ReadRequest, ed25519.PublicKey) (cp.View, error)
	RawSubmit(context.Context, cp.Slot, ed25519.PublicKey, string, []byte, []byte, []byte) (cp.Receipt, error)
}

const requestTimeout = 30 * time.Second
const handshakeTimeout = 5 * time.Second

type Server struct {
	id           seatgate.Identity
	witnessKeyID string
	witnessPin   ed25519.PublicKey
	timeout      time.Duration
	connections  chan struct{}
	limits       limiter
}

func NewServer(id seatgate.Identity, witnessPin ed25519.PublicKey) (*Server, error) {
	wid, err := cp.KeyID(witnessPin)
	if err != nil || id.Fingerprint() == "" {
		return nil, cp.ErrRefused
	}
	return &Server{id: id, witnessKeyID: wid, witnessPin: slices.Clone(witnessPin), timeout: requestTimeout, connections: make(chan struct{}, 8)}, nil
}

// ServeOwner is a separate owner endpoint. Supply a private local listener
// and an independently provisioned client-certificate SPKI pin. Enrollment,
// release, retirement and owner reads exist only on this authenticated endpoint.
func (s *Server) ServeOwner(l net.Listener, a cp.Administration, ownerPin string) error {
	if a == nil || !seatgate.ValidFingerprint(ownerPin) {
		return cp.ErrRefused
	}
	switch addr := l.Addr().(type) {
	case *net.UnixAddr:
	case *net.TCPAddr:
		if !addr.IP.IsLoopback() {
			return cp.ErrRefused
		}
	default:
		return cp.ErrRefused
	}
	return serveListener(l, serverTLS(s.id, ownerPin), s.connections, s.timeout, func(ctx context.Context, conn *tls.Conn, m frame.Message) { _ = s.reply(ctx, conn, nil, a, m) })
}

func transientAccept(err error) bool {
	var n net.Error
	return !errors.Is(err, net.ErrClosed) && (errors.As(err, &n) && n.Timeout() ||
		errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.ENOMEM))
}

func serveListener(l net.Listener, cfg *tls.Config, connections chan struct{}, timeout time.Duration, handle func(context.Context, *tls.Conn, frame.Message)) error {
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	delay := 5 * time.Millisecond
	for {
		c, err := l.Accept()
		if err != nil {
			if !transientAccept(err) {
				return err
			}
			time.Sleep(delay)
			delay = min(delay*2, time.Second)
			continue
		}
		delay = 5 * time.Millisecond
		select {
		case connections <- struct{}{}:
		default:
			c.Close()
			continue
		}
		workers.Go(func() {
			defer func() { <-connections }()
			defer c.Close()
			stop := context.AfterFunc(ctx, func() { c.Close() })
			defer stop()
			conn := tls.Server(&countedConn{Conn: c}, cfg)
			if c.SetDeadline(time.Now().Add(handshakeTimeout)) != nil || conn.HandshakeContext(ctx) != nil {
				return
			}
			reqCtx, done := context.WithTimeout(ctx, timeout)
			defer done()
			deadline, _ := reqCtx.Deadline()
			if c.SetDeadline(deadline) != nil {
				return
			}
			m, err := frame.ReadMessage(conn)
			if err != nil {
				return
			}
			handle(reqCtx, conn, m)
		})
	}
}
func (s *Server) reply(ctx context.Context, conn *tls.Conn, w Witness, a cp.Administration, m frame.Message) error {
	replies, err := s.dispatch(ctx, w, a, m)
	if errors.Is(err, frame.ErrProtocol) {
		return err
	}
	if err != nil {
		replies = []frame.Message{encodeError(err)}
	}
	for _, reply := range replies {
		if err := reply.WriteToStream(conn); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) dispatch(ctx context.Context, w Witness, a cp.Administration, m frame.Message) ([]frame.Message, error) {
	f := m.Fields
	var receipt cp.Receipt
	var err error
	switch m.Kind {
	case frame.Connect, frame.Read, frame.OwnerRead:
		if m.Kind == frame.OwnerRead && a == nil || m.Kind != frame.OwnerRead && w == nil {
			return nil, cp.ErrRefused
		}
		q, err := parseRead(f[0], s.witnessKeyID, operation[m.Kind])
		if err != nil {
			return nil, err
		}
		peer := "owner"
		if w != nil {
			if err := authenticate(ctx, w, q.Slot, f[1], f[0], f[2]); err != nil {
				return nil, err
			}
			peer = string(f[1])
		}
		pool := ordinary
		if m.Kind != frame.Connect && q.ReceiptID != (cp.ID{}) {
			pool = recoveryReads
		}
		if !s.limits.allow(peer, pool) {
			return nil, cp.ErrUnavailable
		}
		if m.Kind == frame.Connect {
			return []frame.Message{frame.New(frame.Granted)}, nil
		}
		var v cp.View
		if w != nil {
			v, err = w.ReadHome(ctx, q, f[1])
		} else {
			v, err = a.Read(ctx, q)
		}
		// A signed inhibited view carries recoverable evidence. The client
		// independently derives ErrUnavailable from its signed guard field.
		if err != nil {
			if !errors.Is(err, cp.ErrUnavailable) || !errors.Is(cp.VerifyView(s.witnessPin, q, v), cp.ErrUnavailable) {
				return nil, err
			}
		}
		current, err := json.Marshal(v.Current)
		if err != nil {
			return nil, err
		}
		var prior cp.Receipt
		if v.Prior != nil {
			prior = *v.Prior
		}
		return []frame.Message{frame.New(frame.View, current), frame.New(frame.Fresh, v.Fresh.Body, v.Fresh.Signature), frame.New(frame.Prior, prior.Body, prior.Signature)}, nil
	case frame.Claim, frame.Advance:
		if w == nil {
			return nil, cp.ErrRefused
		}
		var slot cp.Slot
		if !exactJSON(f[0], &slot) || !validSlot(slot) {
			return nil, frame.ErrProtocol
		}
		if err := authenticate(ctx, w, slot, f[1], f[2], f[3]); err != nil {
			return nil, err
		}
		if !s.limits.allow(string(f[1]), ordinary) {
			return nil, cp.ErrUnavailable
		}
		receipt, err = w.RawSubmit(ctx, slot, f[1], operation[m.Kind], f[2], f[3], f[4])
	case frame.Enroll, frame.Release, frame.Retire:
		if a == nil {
			return nil, cp.ErrRefused
		}
		if !s.limits.allow("owner", ordinary) {
			return nil, cp.ErrUnavailable
		}
		intent, parseErr := cp.ParseIntent(operation[m.Kind], s.witnessKeyID, f[0])
		if parseErr != nil {
			return nil, parseErr
		}
		switch q := intent.(type) {
		case cp.Enrollment:
			q.ProofOfPossession = f[1]
			receipt, err = a.Enroll(ctx, q)
		case cp.Mutation:
			receipt, err = a.Release(ctx, q)
		case cp.Retirement:
			receipt, err = a.Retire(ctx, q)
		default:
			return nil, frame.ErrProtocol
		}
	default:
		return nil, frame.ErrProtocol
	}
	if err != nil {
		return nil, err
	}
	return []frame.Message{frame.New(frame.Receipt, receipt.Body, receipt.Signature)}, nil
}

func exactJSON(b []byte, out any) bool {
	if !decodeJSON(b, out) {
		return false
	}
	encoded, err := json.Marshal(out)
	return err == nil && bytes.Equal(b, encoded)
}

func authenticate(ctx context.Context, w Witness, slot cp.Slot, key, body, sig []byte) error {
	if err := w.CheckHome(ctx, slot, key); err != nil {
		return err
	}
	if cp.VerifySignature(cp.HomeNamespace, key, body, sig) != nil {
		return &cp.Reason{Kind: cp.Unauthorized}
	}
	return nil
}
