package seatgate

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// frameCap is the frame cap the gate needs, written out here rather than
// taken from MaxFrame, so that a change of the constant is a change of this
// test as well: the largest result of the owner channel's service
// (ownerwire.MaxPageBytes, 256 KiB, which this package does not import; a test
// of the daemon's gate holds the two together) and 1 KiB for the response
// around it.
const frameCap = 256<<10 + 1<<10

func TestMaxFrameHoldsTheLargestPageAndItsEnvelope(t *testing.T) {
	if MaxFrame != frameCap {
		t.Fatalf("MaxFrame is %d, want %d (256 KiB and 1 KiB)", MaxFrame, frameCap)
	}
}

// The frame reader and writer take a payload of exactly the cap and refuse
// one byte more, the reader before it reads any payload.
func TestFrameReaderAndWriterAtTheCap(t *testing.T) {
	at := bytes.Repeat([]byte("x"), frameCap)
	got, err := readFrame(bytes.NewReader(framed(string(at))))
	if err != nil || len(got) != frameCap {
		t.Fatalf("a frame of the cap: %d bytes, %v", len(got), err)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], frameCap+1)
	if _, err := readFrame(bytes.NewReader(hdr[:])); !errors.Is(err, ErrProtocol) {
		t.Fatalf("a length one past the cap: %v, want a violation", err)
	}
	var b bytes.Buffer
	if err := writeFrame(&b, at); err != nil {
		t.Fatalf("writing a frame of the cap: %v", err)
	}
	if err := writeFrame(&b, append(at, 'x')); !errors.Is(err, ErrProtocol) {
		t.Fatalf("writing one byte past the cap: %v, want a violation", err)
	}
}

// padRequest is a request of id and op whose params {"x":"..."} make the
// frame exactly n bytes.
func padRequest(t *testing.T, id uint64, op string, n int) string {
	t.Helper()
	empty := `{"id":` + strconv.FormatUint(id, 10) + `,"op":"` + op + `","params":{"x":""}}`
	if n < len(empty) {
		t.Fatalf("%d bytes cannot hold a request", n)
	}
	return `{"id":` + strconv.FormatUint(id, 10) + `,"op":"` + op + `","params":{"x":"` + strings.Repeat("a", n-len(empty)) + `"}}`
}

// sizedResult is a handler that answers t.size with a result {"x":"..."} of
// the length its params ask for, and t.len with the length of its params.
func sizedResult() Handler {
	return HandlerFunc(func(_ context.Context, _ Peer, op string, params json.RawMessage) (json.RawMessage, error) {
		switch op {
		case "t.size":
			var p struct {
				Pad int `json:"pad"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, NewCodedError("bad-params")
			}
			return json.RawMessage(`{"x":"` + strings.Repeat("a", p.Pad) + `"}`), nil
		case "t.len":
			return json.RawMessage(`{"n":` + strconv.Itoa(len(params)) + `}`), nil
		}
		return nil, NewCodedError("unknown-op")
	})
}

// resultPad is the pad that makes the response of id to t.size exactly n
// bytes.
func resultPad(id uint64, n int) int {
	return n - len(`{"id":`+strconv.FormatUint(id, 10)+`,"result":{"x":""}}`)
}

// The server reads a request of exactly the cap and serves it, and closes a
// connection that announces one byte more; it answers a result whose response
// is exactly the cap and refuses one byte more too-large.
func TestServerAtTheFrameCap(t *testing.T) {
	leakCheck(t)
	e := startServer(t, nil, sizedResult())
	r := e.helloed()
	r.send(padRequest(t, 2, "t.len", frameCap))
	r.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	p, err := readFrame(r.c)
	if err != nil {
		t.Fatalf("a request of the cap: %v", err)
	}
	if resp, err := decodeResponse(p); err != nil || resp.Code != "" || resp.ID != 2 {
		t.Fatalf("a request of the cap: %s %v", p, err)
	}

	for _, c := range []struct {
		id   uint64
		size int
		code string
	}{{3, frameCap, ""}, {4, frameCap + 1, CodeTooLarge}} {
		r.send(`{"id":` + strconv.FormatUint(c.id, 10) + `,"op":"t.size","params":{"pad":` + strconv.Itoa(resultPad(c.id, c.size)) + `}}`)
		r.c.SetReadDeadline(time.Now().Add(10 * time.Second))
		p, err := readFrame(r.c)
		if err != nil {
			t.Fatalf("a response of %d bytes: %v", c.size, err)
		}
		resp, err := decodeResponse(p)
		if err != nil || resp.Code != c.code || resp.ID != c.id {
			t.Fatalf("a response of %d bytes: code %q id %d %v, want code %q", c.size, resp.Code, resp.ID, err, c.code)
		}
		if c.code == "" && len(p) != frameCap {
			t.Fatalf("the response is %d bytes, want %d", len(p), frameCap)
		}
	}

	// One byte past the cap, announced: the connection is closed unanswered.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], frameCap+1)
	r.sendBytes(hdr[:])
	if got := r.expectClosed(3 * time.Second); len(got) != 0 {
		t.Fatalf("answered %+v", got)
	}
}

// The client sends a request of exactly the cap and refuses to send one byte
// more, and it reads a response of exactly the cap.
func TestClientAtTheFrameCap(t *testing.T) {
	e := startServer(t, bigRate, sizedResult())
	// No pings, so that the ids of the calls are 2, 3 and 4.
	c := dial(t, e, func(cfg *ClientConfig) { cfg.PingInterval = time.Hour; cfg.DeadAfter = 2 * time.Hour })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// id 2: params that make the request exactly the cap.
	empty := len(`{"id":2,"op":"t.len","params":{"x":""}}`)
	params := map[string]string{"x": strings.Repeat("a", frameCap-empty)}
	var got struct {
		N int `json:"n"`
	}
	if err := c.Call(ctx, "t.len", params, &got); err != nil || got.N != frameCap-len(`{"id":2,"op":"t.len","params":}`) {
		t.Fatalf("a request of the cap: %+v, %v", got, err)
	}
	// id 3: one byte more is not sent, and the connection stays.
	params["x"] += "a"
	if err := c.Call(ctx, "t.len", params, nil); err != ErrBadCall {
		t.Fatalf("a request one byte past the cap: %v, want ErrBadCall", err)
	}
	if _, ok := c.Connected(); !ok {
		t.Fatal("the refused call cost the connection")
	}
	// id 4: a response of exactly the cap is read.
	var out struct {
		X string `json:"x"`
	}
	if err := c.Call(ctx, "t.size", map[string]int{"pad": resultPad(4, frameCap)}, &out); err != nil || len(out.X) != resultPad(4, frameCap) {
		t.Fatalf("a response of the cap: %d bytes of x, %v", len(out.X), err)
	}
}
