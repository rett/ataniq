package link

import (
	"context"
	"errors"
	"net"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
)

// ConfigError identifies a local configuration that cannot start a poll loop.
type ConfigError struct{ Problem string }

func (e *ConfigError) Error() string { return "checkpoint link: " + e.Problem }
func (*ConfigError) Unwrap() error   { return cp.ErrRefused }

// Poll dials the configured home until cancellation. Peer alerts are retryable.
// Each connection can deliver one request. The witness has no home-facing listener.
func (s *Server) Poll(ctx context.Context, w Witness) error {
	home := s.home
	if w == nil {
		return &ConfigError{"missing witness"}
	}
	if !s.polling.CompareAndSwap(false, true) {
		return &ConfigError{"poll loop already running"}
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
		capDelay := time.Second
		if errors.Is(err, errRequestFailed) {
			capDelay = 5 * time.Second
		}
		if errors.Is(err, cp.ErrRefused) {
			delay = max(delay, time.Second)
			capDelay = 30 * time.Second
		}
		if worked && err == nil {
			delay = 25 * time.Millisecond
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(max(25*time.Millisecond, s.pollDelay(delay))):
		}
		delay = min(delay*2, capDelay)
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
