package seatnotice

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"time"
)

// Notifier is the seat's view of the notifier: the four calls of the
// protocol. [*Client] implements it, over a Unix socket.
type Notifier interface {
	Info(ctx context.Context) (Info, error)
	Notify(ctx context.Context, n int) error
	Subscribe(ctx context.Context, s Subscription) error
	Unsubscribe(ctx context.Context, endpointSHA256 string) error
}

// Client calls the notifier on a Unix socket: each call dials, sends one
// request, reads one response and closes, so a Client holds no connection and
// is safe for use by many goroutines.
type Client struct {
	path string
	// Deadline is how long one call may last, [DefaultDeadline] when zero.
	// A context that ends sooner ends the call sooner.
	Deadline time.Duration
}

var _ Notifier = (*Client)(nil)

// NewClient returns a client of the socket at path, which must be absolute.
// It dials nothing.
func NewClient(path string) (*Client, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("seatnotice: the socket path must be absolute")
	}
	return &Client{path: path}, nil
}

// call sends req and returns the checked response. A refusal is a
// [*CodedError].
func (c *Client) call(ctx context.Context, req request) (response, error) {
	payload, err := req.encode()
	if err != nil {
		return response{}, err
	}
	d := c.Deadline
	if d <= 0 {
		d = DefaultDeadline
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.path)
	if err != nil {
		return response{}, errors.New("seatnotice: the notifier is not reachable")
	}
	defer conn.Close()
	dl, _ := ctx.Deadline()
	if err := conn.SetDeadline(dl); err != nil {
		return response{}, errors.New("seatnotice: the notifier is not reachable")
	}
	// A cancelled context ends a read or write in progress.
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if err := writeFrame(conn, payload); err != nil {
		return response{}, callError(ctx, err)
	}
	data, err := readFrame(conn)
	if err != nil {
		return response{}, callError(ctx, err)
	}
	resp, err := decodeResponse(req.Op, data)
	if err != nil {
		return response{}, err
	}
	if !resp.OK {
		return response{}, NewCodedError(resp.Code)
	}
	return resp, nil
}

// callError says what ended a call, in a fixed text: the context's error
// when it ended, a violation as it is, and otherwise a lost connection.
func callError(ctx context.Context, err error) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if errors.Is(err, ErrProtocol) {
		return err
	}
	return errors.New("seatnotice: the notifier did not answer")
}

// Info asks for the VAPID public key and the number of subscriptions.
func (c *Client) Info(ctx context.Context) (Info, error) {
	r, err := c.call(ctx, request{Op: OpInfo})
	if err != nil {
		return Info{}, err
	}
	return r.Info, nil
}

// Notify tells the notifier that n cards wait (0 to [MaxN]) and nothing else.
func (c *Client) Notify(ctx context.Context, n int) error {
	_, err := c.NotifySent(ctx, n)
	return err
}

// NotifySent is Notify, and returns how many pushes the notifier sent.
func (c *Client) NotifySent(ctx context.Context, n int) (int, error) {
	r, err := c.call(ctx, request{Op: OpNotify, N: n})
	if err != nil {
		return 0, err
	}
	return r.Sent, nil
}

// Subscribe hands the notifier a browser's push subscription.
func (c *Client) Subscribe(ctx context.Context, s Subscription) error {
	_, err := c.call(ctx, request{Op: OpSubscribe, Sub: s})
	return err
}

// Unsubscribe removes the subscription whose endpoint hashes to
// endpointSHA256 (see [EndpointSHA256]).
func (c *Client) Unsubscribe(ctx context.Context, endpointSHA256 string) error {
	_, err := c.call(ctx, request{Op: OpUnsubscribe, EndpointSHA256: endpointSHA256})
	return err
}
