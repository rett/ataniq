package clientapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// ErrClosed is returned by a [Client] that was closed, or that dropped its
// connection after a failure it cannot recover from.
var ErrClosed = errors.New("clientapi: client closed")

// ErrResponseTooLarge is returned when the server sends a line over
// [MaxResponseBytes]. The client closes the connection.
var ErrResponseTooLarge = errors.New("clientapi: response line over the response limit")

// ErrBadResponse is wrapped by the errors for a response that is not valid, or
// whose protocol or request id does not match the request. The client closes
// the connection.
var ErrBadResponse = errors.New("clientapi: bad response")

// Error is the typed error for an error response.
type Error struct {
	// Code is the server's code.
	Code ErrorCode
	// Message is the server's fixed text.
	Message string
	// SupportedProtocols is set with [CodeUnsupportedProtocol].
	SupportedProtocols []string
}

// Error returns the code and the server's text.
func (e *Error) Error() string {
	return "clientapi: server answered " + string(e.Code) + ": " + e.Message
}

// Client is one connection to the daemon. It is safe for concurrent use; calls
// take turns on the connection.
type Client struct {
	mu     sync.Mutex
	conn   net.Conn
	br     *bufio.Reader
	seq    uint64
	closed bool
}

// Dial connects to the socket at path.
func Dial(ctx context.Context, path string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, br: bufio.NewReaderSize(conn, 4096)}, nil
}

// Close closes the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// Hello asks for the server's protocols, limits and operations.
func (c *Client) Hello(ctx context.Context) (HelloResult, error) {
	var r HelloResult
	err := c.call(ctx, OpHello, &r)
	return r, err
}

// Status asks for the daemon block and the leads.
func (c *Client) Status(ctx context.Context) (StatusResult, error) {
	var r StatusResult
	err := c.call(ctx, OpStatus, &r)
	return r, err
}

// call sends one request and reads its response into result. The context's
// deadline and cancellation apply to the whole exchange. A failure of the
// transport or of the response's form closes the client, as the stream can no
// longer be trusted; an error response does not.
func (c *Client) call(ctx context.Context, op string, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		c.conn.SetDeadline(dl)
	} else {
		c.conn.SetDeadline(time.Time{})
	}
	stop := context.AfterFunc(ctx, func() { c.conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	fail := func(err error) error {
		c.closed = true
		c.conn.Close()
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		// The connection's deadline is the context's and can fire a moment
		// before the context reports it.
		var ne net.Error
		if dl, ok := ctx.Deadline(); ok && errors.As(err, &ne) && ne.Timeout() && !time.Now().Before(dl) {
			return context.DeadlineExceeded
		}
		return err
	}

	c.seq++
	id := "c-" + strconv.FormatUint(c.seq, 10)
	line, err := json.Marshal(Request{Protocol: Protocol, RequestID: id, Op: op})
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return fail(err)
	}
	raw, err := readResponseLine(c.br)
	if err != nil {
		return fail(err)
	}
	if !utf8.Valid(raw) {
		return fail(fmt.Errorf("%w: not UTF-8", ErrBadResponse))
	}
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fail(fmt.Errorf("%w: %v", ErrBadResponse, err))
	}
	if resp.Protocol != Protocol {
		return fail(fmt.Errorf("%w: protocol %q", ErrBadResponse, resp.Protocol))
	}
	if resp.OK {
		if resp.RequestID != id {
			return fail(fmt.Errorf("%w: request id does not match", ErrBadResponse))
		}
		if resp.Error != nil || len(resp.Result) == 0 {
			return fail(fmt.Errorf("%w: result missing", ErrBadResponse))
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fail(fmt.Errorf("%w: %v", ErrBadResponse, err))
		}
		return nil
	}
	if resp.Error == nil {
		return fail(fmt.Errorf("%w: error missing", ErrBadResponse))
	}
	// A refusal of the connection ("unauthorized", "busy") or of an unreadable
	// request carries no request id; any other error response must match.
	if resp.RequestID != id && resp.RequestID != "" {
		return fail(fmt.Errorf("%w: request id does not match", ErrBadResponse))
	}
	e := &Error{Code: resp.Error.Code, Message: resp.Error.Message, SupportedProtocols: resp.Error.SupportedProtocols}
	switch e.Code {
	case CodeUnauthorized, CodeBusy, CodeTooLarge:
		// The server closes the connection after these.
		c.closed = true
		c.conn.Close()
	}
	return e
}

// readResponseLine reads one line of at most [MaxResponseBytes] bytes
// including its LF, and returns it without the LF.
func readResponseLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf)+len(chunk) > MaxResponseBytes {
			return nil, ErrResponseTooLarge
		}
		switch {
		case err == nil:
			return bytes.TrimSuffix(append(buf, chunk...), []byte{'\n'}), nil
		case errors.Is(err, bufio.ErrBufferFull):
			buf = append(buf, chunk...)
		default:
			return nil, err
		}
	}
}
