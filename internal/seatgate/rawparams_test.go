package seatgate

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// capture is a handler that keeps the params of each t.got it serves, as the
// gate's handler is handed them, and answers their length.
type capture struct{ got chan []byte }

func newCapture() *capture { return &capture{got: make(chan []byte, 16)} }

func (c *capture) handler() Handler {
	return HandlerFunc(func(_ context.Context, _ Peer, op string, params json.RawMessage) (json.RawMessage, error) {
		if op != "t.got" {
			return nil, NewCodedError("unknown-op")
		}
		c.got <- append([]byte(nil), params...)
		return json.RawMessage(`{"n":` + strconv.Itoa(len(params)) + `}`), nil
	})
}

// next returns the params of the next t.got the handler served.
func (c *capture) next(t *testing.T) []byte {
	t.Helper()
	select {
	case p := <-c.got:
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("the handler was not called")
		return nil
	}
}

// none checks that the handler served no t.got since the last next.
func (c *capture) none(t *testing.T, what string) {
	t.Helper()
	select {
	case p := <-c.got:
		t.Errorf("%s: the gate was handed %q", what, p)
	default:
	}
}

// htmlBytes are characters encoding/json escapes for HTML when it marshals,
// each into six bytes: <, >, &, U+2028 and U+2029.
const htmlBytes = "<>&  "

// rawParams is a params object {"x":"..."} of characters encoding/json would
// escape, padded with 'a' so that the request of id to t.got is exactly n
// bytes.
func rawParams(t *testing.T, id uint64, n int) json.RawMessage {
	t.Helper()
	head := `{"id":` + strconv.FormatUint(id, 10) + `,"op":"t.got","params":`
	empty := len(head + `{"x":""}` + `}`)
	body := strings.Repeat(htmlBytes, (n-empty)/len(htmlBytes)/2)
	if n-empty < len(body) {
		t.Fatalf("%d bytes cannot hold a request", n)
	}
	body += strings.Repeat("a", n-empty-len(body))
	return json.RawMessage(`{"x":"` + body + `"}`)
}

// TestClientSendsRawParamsAsGiven: params given as a json.RawMessage reach
// the gate's handler byte for byte, with <, >, & and U+2028 and U+2029 as they
// are (encoding/json would write each as six bytes) and with the white space
// inside the object kept. A request of such params of exactly the frame cap
// is sent (once escaped it would be far over the cap, and was refused
// ErrBadCall), and one byte more is refused ErrBadCall and not sent, and the
// connection stays.
func TestClientSendsRawParamsAsGiven(t *testing.T) {
	cp := newCapture()
	e := startServer(t, bigRate, cp.handler())
	// No pings, so that the ids of the calls are 2, 3 and 4.
	c := dial(t, e, func(cfg *ClientConfig) { cfg.PingInterval = time.Hour; cfg.DeadAfter = 2 * time.Hour })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// id 2: a request of exactly the cap.
	at := rawParams(t, 2, frameCap)
	if !bytes.Contains(at, []byte("<>&")) || len(at) < len(htmlBytes)*1000 {
		t.Fatalf("the params hold too few characters to escape: the test does not show what it means to")
	}
	var got struct {
		N int `json:"n"`
	}
	if err := c.Call(ctx, "t.got", at, &got); err != nil || got.N != len(at) {
		t.Fatalf("a request of the cap: %+v, %v", got, err)
	}
	if p := cp.next(t); !bytes.Equal(p, at) {
		t.Fatalf("the gate was handed %d bytes that differ from the %d given", len(p), len(at))
	}

	// id 3: one byte more is not sent, and the connection stays.
	over := rawParams(t, 3, frameCap+1)
	if err := c.Call(ctx, "t.got", over, nil); err != ErrBadCall {
		t.Fatalf("a request one byte past the cap: %v, want ErrBadCall", err)
	}
	cp.none(t, "one byte past the cap")
	if _, ok := c.Connected(); !ok {
		t.Fatal("the refused call cost the connection")
	}

	// id 4: small params with the characters and white space, as given.
	small := json.RawMessage(`{"path": "a&b/<c>` + " " + `", "n" : [1, 2]}`)
	if err := c.Call(ctx, "t.got", small, nil); err != nil {
		t.Fatal(err)
	}
	if p := cp.next(t); !bytes.Equal(p, small) {
		t.Errorf("the gate was handed %s, want %s", p, small)
	}
}

// TestClientRefusesRawParamsTheGateWouldNot: raw params are held to the
// strict form the gate reads a frame in before anything is sent. Params the
// gate would take as a violation of the wire (a key twice, nesting past the
// frame's depth, bytes around the object, two objects, invalid UTF-8) are
// ErrBadCall, the gate is handed nothing, and the connection stays; were they
// sent, the gate would drop the link. Params nested as deeply as the frame
// allows are sent.
func TestClientRefusesRawParamsTheGateWouldNot(t *testing.T) {
	cp := newCapture()
	e := startServer(t, bigRate, cp.handler())
	c := dial(t, e, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nest := func(n int) string { return strings.Repeat(`{"a":`, n-1) + `{}` + strings.Repeat(`}`, n-1) }
	for name, raw := range map[string]string{
		"a key twice":           `{"a":1,"a":2}`,
		"a key twice, escaped":  `{"a":1,"a":2}`,
		"a key twice, deeper":   `{"a":{"b":1,"b":2}}`,
		"nested past the frame": nest(MaxDepth),
		"space before":          ` {}`,
		"space after":           `{} `,
		"two objects":           `{}{}`,
		"not valid JSON":        `{"a":}`,
		"invalid UTF-8":         "{\"a\":\"\xff\"}",
		"null":                  `null`,
		"an array":              `[]`,
		"a string":              `"x"`,
		"empty":                 ``,
	} {
		if err := c.Call(ctx, "t.got", json.RawMessage(raw), nil); err != ErrBadCall {
			t.Errorf("%s: %v, want ErrBadCall", name, err)
		}
	}
	cp.none(t, "params the gate would refuse")
	if _, ok := c.Connected(); !ok {
		t.Fatal("a refused call cost the connection")
	}
	deepest := json.RawMessage(nest(MaxDepth - 1))
	if err := c.Call(ctx, "t.got", deepest, nil); err != nil {
		t.Fatalf("params as deep as the frame allows: %v", err)
	}
	if p := cp.next(t); !bytes.Equal(p, deepest) {
		t.Errorf("the gate was handed %s", p)
	}
}
