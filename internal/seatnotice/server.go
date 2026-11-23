package seatnotice

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// DefaultDeadline is how long a call of the client, and a turn of the server
// with a connection, may last: from the dial (or the accept) to the end of
// the response. It is the deadline of info, subscribe and unsubscribe, and
// bounds the reading of every request, notify's included.
const DefaultDeadline = 2 * time.Second

// DefaultNotifyDeadline is the deadline of the notify operation alone, on
// both sides: a notify sends one push to each of up to four subscriptions,
// and one push may take 5 seconds (the notifier's own bound is 12 seconds in
// all, so that its answer is in before this ends). The seat's own bound on
// the call must be longer than this, to leave the client's deadline the one
// that ends it.
const DefaultNotifyDeadline = 15 * time.Second

// maxServing bounds the connections the server holds at once; a connection
// past it is closed unread.
const maxServing = 16

// Handler is the notifier's side of the protocol: what the server calls with
// each decoded request. A returned [*CodedError] is answered as its code; any
// other error is answered [CodeUnavailable]. Its context ends when the
// connection's deadline does.
type Handler interface {
	Info(ctx context.Context) (Info, error)
	// Notify is told n, 0 to [MaxN], and returns how many pushes it sent.
	Notify(ctx context.Context, n int) (sent int, err error)
	Subscribe(ctx context.Context, s Subscription) error
	Unsubscribe(ctx context.Context, endpointSHA256 string) error
}

// ServeConfig is what Serve needs.
type ServeConfig struct {
	// AllowedUID is the one user id whose connections are served: the
	// peer's, as the kernel reports it (SO_PEERCRED). It has no default.
	AllowedUID uint32
	// Deadline is the server's turn with a connection, [DefaultDeadline]
	// when zero.
	Deadline time.Duration
	// NotifyDeadline is the server's turn with a notify, measured from the
	// accept, [DefaultNotifyDeadline] when zero. The request is still read
	// within Deadline.
	NotifyDeadline time.Duration
	// peerUID reads the peer's user id; the system's when nil. For tests of
	// the refusal of a wrong id.
	peerUID func(net.Conn) (uint32, error)
}

// Serve accepts connections on ln until ctx ends, and serves each with h: it
// refuses the peers whose user id is not cfg.AllowedUID (closing the
// connection unread, so nothing of the request is looked at), reads one
// request, calls h, writes one response and closes. It closes ln when ctx
// ends, waits for the connections in flight, and returns nil then; any other
// end of the accept loop is its error. On a system that cannot read a peer's
// user id every connection is refused.
func Serve(ctx context.Context, ln net.Listener, h Handler, cfg ServeConfig) error {
	if cfg.Deadline <= 0 {
		cfg.Deadline = DefaultDeadline
	}
	if cfg.NotifyDeadline <= 0 {
		cfg.NotifyDeadline = DefaultNotifyDeadline
	}
	peer := cfg.peerUID
	if peer == nil {
		peer = peerUID
	}
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, maxServing)
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer c.Close()
			serveConn(ctx, c, h, cfg, peer)
		}()
	}
}

func serveConn(ctx context.Context, c net.Conn, h Handler, cfg ServeConfig, peer func(net.Conn) (uint32, error)) {
	uid, err := peer(c)
	if err != nil || uid != cfg.AllowedUID {
		return
	}
	start := time.Now()
	if c.SetDeadline(start.Add(cfg.Deadline)) != nil {
		return
	}
	payload, err := readFrame(c)
	if err != nil {
		return
	}
	req, err := decodeRequest(payload)
	if err != nil {
		return
	}
	// The turn of the operation: the request was read within Deadline, and
	// a notify then has NotifyDeadline from the accept in all, for its
	// pushes and for writing the answer.
	dl := start.Add(cfg.Deadline)
	if req.Op == OpNotify {
		dl = start.Add(max(cfg.Deadline, cfg.NotifyDeadline))
		if c.SetDeadline(dl) != nil {
			return
		}
	}
	cctx, cancel := context.WithDeadline(ctx, dl)
	defer cancel()
	var (
		info Info
		sent int
	)
	switch req.Op {
	case OpInfo:
		info, err = h.Info(cctx)
	case OpNotify:
		sent, err = h.Notify(cctx, req.N)
	case OpSubscribe:
		err = h.Subscribe(cctx, req.Sub)
	case OpUnsubscribe:
		err = h.Unsubscribe(cctx, req.EndpointSHA256)
	}
	code := ""
	if err != nil {
		var ce *CodedError
		if errors.As(err, &ce) && ValidCode(ce.Code) {
			code = ce.Code
		} else {
			code = CodeUnavailable
		}
	}
	out, err := encodeResponse(req.Op, code, info, sent)
	if err != nil {
		// A handler's answer out of bounds is the notifier's fault.
		out, _ = encodeResponse(req.Op, CodeUnavailable, Info{}, 0)
	}
	writeFrame(c, out)
}
