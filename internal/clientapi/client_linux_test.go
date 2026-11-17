package clientapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// fakeServer answers one connection with what reply returns for the request.
func fakeServer(t *testing.T, reply func(req Request) []byte) string {
	t.Helper()
	dir, err := filepath.Abs(socketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	l, path, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadBytes('\n')
					if err != nil {
						return
					}
					var req Request
					if json.Unmarshal(line, &req) != nil {
						return
					}
					c.Write(reply(req))
				}
			}()
		}
	}()
	return path
}

func okReply(id, proto string) []byte {
	return []byte(`{"protocol":"` + proto + `","request_id":"` + id + `","ok":true,"result":{"protocols":["ataniq.client/1"],"mutations":false,"version":"v"}}` + "\n")
}

func TestClientChecksProtocolAndRequestID(t *testing.T) {
	cases := map[string]func(Request) []byte{
		"wrong protocol": func(r Request) []byte { return okReply(r.RequestID, "ataniq.client/2") },
		"wrong id":       func(r Request) []byte { return okReply("other", Protocol) },
		"empty id on ok": func(r Request) []byte { return okReply("", Protocol) },
		"not json":       func(r Request) []byte { return []byte("hello\n") },
		"not an object":  func(r Request) []byte { return []byte("[]\n") },
		"ok without result": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":true}` + "\n")
		},
		"error without body": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":false}` + "\n")
		},
		"error with other id": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"zzz","ok":false,"error":{"code":"busy","message":"m"}}` + "\n")
		},
		"result of the wrong type": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":true,"result":{"protocols":7}}` + "\n")
		},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := Dial(context.Background(), fakeServer(t, reply))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, err = c.Hello(context.Background())
			if !errors.Is(err, ErrBadResponse) {
				t.Fatalf("err = %v, want ErrBadResponse", err)
			}
			if _, err := c.Hello(context.Background()); !errors.Is(err, ErrClosed) {
				t.Fatalf("after a bad response: %v", err)
			}
		})
	}
}

func TestClientAcceptsEmptyIDOnErrorResponses(t *testing.T) {
	path := fakeServer(t, func(r Request) []byte {
		return []byte(`{"protocol":"` + Protocol + `","request_id":"","ok":false,"error":{"code":"invalid-request","message":"m"}}` + "\n")
	})
	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Hello(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v", err)
	}
	// An ordinary error response keeps the connection.
	if _, err := c.Hello(context.Background()); !errors.As(err, &e) {
		t.Fatalf("second call: %v", err)
	}
}

func TestClientRefusesAResponseOverTheCap(t *testing.T) {
	for name, size := range map[string]int{"over": MaxResponseBytes, "far over": 3 * MaxResponseBytes} {
		t.Run(name, func(t *testing.T) {
			path := fakeServer(t, func(r Request) []byte {
				// The line with its LF is one byte over the cap, or more.
				pad := strings.Repeat("x", size-len(`{"protocol":"","request_id":"","ok":true,"result":{"a":""}}`)-len(Protocol)-len(r.RequestID)+1)
				return []byte(`{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":true,"result":{"a":"` + pad + `"}}` + "\n")
			})
			c, err := Dial(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err := c.Hello(context.Background()); !errors.Is(err, ErrResponseTooLarge) {
				t.Fatalf("err = %v", err)
			}
			if _, err := c.Status(context.Background()); !errors.Is(err, ErrClosed) {
				t.Fatalf("after: %v", err)
			}
		})
	}
}

func TestClientReadsAResponseAtTheCap(t *testing.T) {
	path := fakeServer(t, func(r Request) []byte {
		head := `{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":true,"result":{"version":"`
		tail := `"}}` + "\n"
		return []byte(head + strings.Repeat("v", MaxResponseBytes-len(head)-len(tail)) + tail)
	})
	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, err := c.Hello(context.Background())
	if err != nil || len(got.Version) < MaxResponseBytes-200 {
		t.Fatalf("err = %v, version %d bytes", err, len(got.Version))
	}
}

func TestClientDialAndClose(t *testing.T) {
	if _, err := Dial(context.Background(), filepath.Join(t.TempDir(), "none.sock")); err == nil {
		t.Fatal("Dial to nothing succeeded")
	}
	ts := startServer(t, &stubHandler{}, Options{})
	c, err := Dial(context.Background(), ts.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Hello(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dial(ctx, ts.path); err == nil {
		t.Fatal("Dial with an ended context succeeded")
	}
}

func TestClientReturnsTypedErrorForUnsupportedProtocol(t *testing.T) {
	path := fakeServer(t, func(r Request) []byte {
		return []byte(`{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":false,"error":{"code":"unsupported-protocol","message":"m","supported_protocols":["ataniq.client/1","ataniq.client/2"]}}` + "\n")
	})
	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Status(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeUnsupportedProtocol || len(e.SupportedProtocols) != 2 {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(e.Error(), "unsupported-protocol") {
		t.Fatalf("Error() = %q", e.Error())
	}
}
