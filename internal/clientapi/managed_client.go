package clientapi

import (
	"bufio"
	"context"
	"errors"
	"net"
	"unicode/utf8"
)

// ErrBadTarget is returned by [ManagedClient.Call], without sending anything,
// for a target that is empty or not valid UTF-8.
var ErrBadTarget = errors.New("clientapi: target is empty or not valid UTF-8")

// ManagedClient is one connection to a [ManagedServer], over a stream the
// caller opened and authenticated. It speaks only [ProtocolV2]: it never
// sends a version 1 request, and it does not retry one when the peer does not
// speak version 2. It has the size caps, timeouts and error mapping of
// [Client]: the same request and response limits, a context deadline that
// bounds a call, an error response as [*Error], and the closing of the stream
// after a failure of the transport or of a response's form.
//
// It is safe for concurrent use; calls take turns on the stream.
type ManagedClient struct {
	c *Client
}

// NewManagedClient takes ownership of conn: Close closes it.
func NewManagedClient(conn net.Conn) *ManagedClient {
	return &ManagedClient{c: &Client{conn: conn, br: bufio.NewReaderSize(conn, 4096), protocol: ProtocolV2}}
}

// Close closes the stream, also under a call in flight, as [Client.Close].
func (m *ManagedClient) Close() error { return m.c.Close() }

// Hello asks for the protocols, limits and capabilities of the principal the
// stream's server resolved. A server that does not speak version 2 answers
// "unsupported-protocol", which is returned as the [*Error] it is.
func (m *ManagedClient) Hello(ctx context.Context) (ManagedHelloResult, error) {
	var r ManagedHelloResult
	err := m.c.call(ctx, "", OpHello, nil, &r)
	return r, err
}

// Call asks for operation op of target, a target ID the hello result lists
// with that operation. The target is sent as given and must be non-empty valid
// UTF-8; otherwise [ErrBadTarget] is returned and nothing is sent. params,
// result and the errors are those of [Client.Call].
func (m *ManagedClient) Call(ctx context.Context, target, op string, params, result any) error {
	if target == "" || !utf8.ValidString(target) {
		return ErrBadTarget
	}
	raw, err := paramsObject(params)
	if err != nil {
		return err
	}
	return m.c.call(ctx, target, op, raw, result)
}
