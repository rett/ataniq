package clientapi

import (
	"context"
	"net"
	"slices"
	"time"
)

// ManagedOptions configure a [ManagedServer]. The connection and time limits
// have the same defaults as [Options].
type ManagedOptions struct {
	MaxConns     int
	IdleTimeout  time.Duration
	WriteTimeout time.Duration
	Counters     *Counters
	// ResolvePrincipal must verify current enrollment from the authenticated
	// connection and respect cancellation. A missing or incomplete proof
	// refuses dispatch and delivery.
	ResolvePrincipal func(context.Context, net.Conn) (Principal, error)
}

// ManagedServer serves version 2 on authenticated desk streams. Share one
// instance across streams to enforce the advertised connection limit.
type ManagedServer struct {
	s     *server
	slots chan struct{}
}

// NewManagedServer takes only the handler for principal-scoped data. The
// scratch inventory and its extensions belong to the Unix listener.
func NewManagedServer(h ManagedHandler, opts ManagedOptions) *ManagedServer {
	s := newServer(nil, Options{
		MaxConns: opts.MaxConns, IdleTimeout: opts.IdleTimeout,
		WriteTimeout: opts.WriteTimeout, Counters: opts.Counters,
	})
	s.protocol, s.managed, s.resolvePrincipal = ProtocolV2, h, opts.ResolvePrincipal
	return &ManagedServer{s: s, slots: make(chan struct{}, s.maxConns)}
}

// ServeConn takes ownership of an already authenticated connection. It closes
// the stream on return or cancellation. The caller performs desk authentication
// and ties ctx to the lifetime of the admitted session.
func (m *ManagedServer) ServeConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	m.s.counters.accepted.Add(1)
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	default:
		m.s.counters.overCap.Add(1)
		conn.SetWriteDeadline(time.Now().Add(minDuration(m.s.write, time.Second)))
		conn.Write(encodeError(ProtocolV2, "", &ErrorBody{Code: CodeBusy, Message: msgBusy}))
		return
	}
	m.s.serveConn(ctx, conn)
}

func (s *server) respondManaged(ctx context.Context, conn net.Conn, req Request) ([]byte, *ErrorBody) {
	forbidden := &ErrorBody{Code: CodeForbidden, Message: msgForbidden}
	if ctx.Err() != nil || s.resolvePrincipal == nil || s.managed == nil {
		return nil, forbidden
	}
	p, err := s.resolvePrincipal(ctx, conn)
	if err != nil || p.Issuer == "" || p.ClientID == "" {
		return nil, forbidden
	}
	handlerCtx := context.WithValue(ctx, principalKey{}, p)
	caps, err := s.managed.Capabilities(handlerCtx)
	if err != nil {
		return nil, s.internal(msgInternal)
	}
	if ctx.Err() != nil {
		return nil, forbidden
	}
	var result any
	var fail *ErrorBody
	var view TargetCapabilities
	if req.Op == OpHello {
		var ok bool
		view, ok = caps.forHello()
		if !ok {
			return nil, s.internal(msgInternal)
		}
		result = ManagedHelloResult{Protocols: []string{ProtocolV2}, Limits: s.limits(), Capabilities: view}
	} else {
		if !slices.Contains(caps[req.Target], req.Op) {
			return nil, forbidden
		}
		result, fail = s.handlerResult(s.managed.HandleManaged(handlerCtx, req.Target, req.Op, req.Params))
	}

	// The handler and even its JSON marshaler may run across a policy
	// change. Recheck before releasing their bytes or a handler error.
	out, fail := s.encodeResult(req, result, fail)
	current, err := s.resolvePrincipal(ctx, conn)
	if err != nil || current != p || ctx.Err() != nil {
		return nil, forbidden
	}
	currentCaps, err := s.managed.Capabilities(handlerCtx)
	if err != nil {
		return nil, s.internal(msgInternal)
	}
	if ctx.Err() != nil {
		return nil, forbidden
	}
	if req.Op == OpHello {
		if !currentCaps.permitsView(view) {
			return nil, forbidden
		}
	} else if !slices.Contains(currentCaps[req.Target], req.Op) {
		return nil, forbidden
	}
	return out, fail
}
