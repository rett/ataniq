package link

import (
	"context"
	"errors"
	"net"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
)

// Poll dials the configured home until cancellation or a permanent refusal.
// Each connection can deliver one request. The witness has no home-facing listener.
func (s *Server) Poll(ctx context.Context, w Witness) error {
	home := s.home
	if w == nil || !s.polling.CompareAndSwap(false, true) {
		return cp.ErrRefused
	}
	defer s.polling.Store(false)
	if home.Network == "" {
		home.Network = "tcp"
	}
	if home.DialContext == nil {
		home.DialContext = (&net.Dialer{}).DialContext
	}
	t := transport{Remote: home, tls: clientTLS(home.ServerPin, s.id)}
	delay := 25 * time.Millisecond
	for ctx.Err() == nil {
		worked, err := s.pollOnce(ctx, t, w)
		if errors.Is(err, cp.ErrRefused) {
			return err
		}
		if worked && err == nil {
			delay = 25 * time.Millisecond
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelay(delay)):
		}
		delay = min(delay*2, time.Second)
	}
	return ctx.Err()
}
func (s *Server) pollOnce(parent context.Context, t transport, w Witness) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	conn, err := t.connect(ctx)
	if err != nil {
		return false, err
	}
	raw := conn.NetConn()
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err = raw.SetDeadline(deadline); err != nil {
		return false, err
	}
	poll, err := frame.New(frame.Poll).Marshal()
	if err != nil {
		return false, err
	}
	m, writeErr, readErr := sendFirst(conn, poll, true)
	if readErr != nil {
		return false, readErr
	}
	if writeErr != nil {
		return false, writeErr
	}
	if m.Kind == frame.Idle {
		return false, nil
	}
	return true, s.reply(ctx, conn, w, nil, m)
}
