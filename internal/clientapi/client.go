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
	"strings"
	"sync"
	"sync/atomic"
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

// maxErrorCodeText is the most of a server's code that [Error.Error] prints.
const maxErrorCodeText = 64

// Error is the typed error for an error response. Its fields are what the
// server sent, and a faulty or impostor server can send anything: a caller
// that shows Message must make it safe for its output first (a terminal,
// above all, must not receive it raw).
type Error struct {
	// Code is the server's code, as received.
	Code ErrorCode
	// Message is the server's text, as received and unchecked.
	Message string
	// SupportedProtocols is set with [CodeUnsupportedProtocol].
	SupportedProtocols []string
}

// Error returns the server's code, cut to a short length and quoted with
// every control and non-ASCII character escaped, so that nothing the server
// sent can reach a terminal or a log through it. It leaves out Message, which
// stays available as a field.
func (e *Error) Error() string {
	code := string(e.Code)
	if len(code) > maxErrorCodeText {
		code = code[:maxErrorCodeText] + "..."
	}
	return "clientapi: server answered " + strconv.QuoteToASCII(code)
}

// Client is one connection to the daemon. It is safe for concurrent use; calls
// take turns on the connection. Close may be called at any time, also while a
// call is in flight.
type Client struct {
	mu   sync.Mutex // held by a call for its whole exchange
	conn net.Conn
	br   *bufio.Reader
	seq  uint64

	// closed is set when the connection is closed, by Close or by a failure;
	// closeOnce closes the connection exactly once, without c.mu.
	closed    atomic.Bool
	closeOnce sync.Once
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

// Close closes the connection. It does not wait for a call in flight: it
// closes the connection under that call, which then returns [ErrClosed]. The
// first Close returns the error of closing the connection; later ones return
// nil.
func (c *Client) Close() error { return c.closeConn() }

// closeConn closes the connection once, without taking c.mu, and returns the
// error of the first close to its first caller.
func (c *Client) closeConn() error {
	var err error
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		err = c.conn.Close()
	})
	return err
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
	if c.closed.Load() {
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
		// A Close from another goroutine ended the call.
		wasClosed := c.closed.Load()
		c.closeConn()
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if wasClosed {
			return ErrClosed
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
	// The errors below carry fixed texts: what the server sent, including the
	// text of a decoder error that quotes it, stays out of them.
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fail(fmt.Errorf("%w: not a response object", ErrBadResponse))
	}
	if !exactKeys(raw, "protocol", "request_id", "ok", "result", "error") {
		return fail(fmt.Errorf("%w: a key is not spelled exactly", ErrBadResponse))
	}
	if resp.Protocol != Protocol {
		return fail(fmt.Errorf("%w: protocol does not match", ErrBadResponse))
	}
	if resp.OK {
		if resp.RequestID != id {
			return fail(fmt.Errorf("%w: request id does not match", ErrBadResponse))
		}
		if resp.Error != nil || len(resp.Result) == 0 {
			return fail(fmt.Errorf("%w: result missing", ErrBadResponse))
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fail(fmt.Errorf("%w: result does not fit the operation", ErrBadResponse))
		}
		return nil
	}
	if resp.Error == nil {
		return fail(fmt.Errorf("%w: error missing", ErrBadResponse))
	}
	if !exactKeys(rawField(raw, "error"), "code", "message", "supported_protocols") {
		return fail(fmt.Errorf("%w: a key is not spelled exactly", ErrBadResponse))
	}
	// A refusal of the connection ("unauthorized", "busy"), of an over-long
	// line ("too-large") or of an unreadable request ("invalid-request")
	// carries no request id when the server had none to echo. Every other
	// error response must carry the request's.
	emptyOK := resp.RequestID == "" && (resp.Error.Code == CodeUnauthorized || resp.Error.Code == CodeBusy ||
		resp.Error.Code == CodeTooLarge || resp.Error.Code == CodeInvalidRequest)
	if resp.RequestID != id && !emptyOK {
		return fail(fmt.Errorf("%w: request id does not match", ErrBadResponse))
	}
	e := &Error{Code: resp.Error.Code, Message: resp.Error.Message, SupportedProtocols: resp.Error.SupportedProtocols}
	switch e.Code {
	case CodeUnauthorized, CodeBusy, CodeTooLarge:
		// The server closes the connection after these.
		c.closeConn()
	}
	return e
}

// exactKeys reports whether no key of the JSON object in raw is a different
// spelling of one of known: encoding/json matches keys without regard to case,
// and the protocol's keys are exact. A key that is not near any of known is
// an unknown key and is ignored, as is anything raw is not an object.
func exactKeys(raw []byte, known ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return true
	}
	for key := range fields {
		for _, k := range known {
			if key != k && strings.EqualFold(key, k) {
				return false
			}
		}
	}
	return true
}

// rawField returns the value of the exact key name in the JSON object raw, or
// nil.
func rawField(raw []byte, name string) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	return fields[name]
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
