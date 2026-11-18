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
		"upper-case keys": func(r Request) []byte {
			return []byte(`{"PROTOCOL":"` + Protocol + `","Request_ID":"` + r.RequestID + `","OK":true,"RESULT":{"protocols":["ataniq.client/1"],"mutations":false,"version":"v"}}` + "\n")
		},
		"mixed-case key beside the right ones": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":true,"Result":{"version":"v"}}` + "\n")
		},
		"upper-case error keys": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":false,"error":{"Code":"busy","Message":"m"}}` + "\n")
		},
		"empty id on unsupported-operation": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"","ok":false,"error":{"code":"unsupported-operation","message":"m"}}` + "\n")
		},
		"empty id on internal": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"","ok":false,"error":{"code":"internal","message":"m"}}` + "\n")
		},
		"empty id on unsupported-protocol": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"","ok":false,"error":{"code":"unsupported-protocol","message":"m"}}` + "\n")
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

func TestClientAcceptsEmptyIDOnlyOnRefusalCodes(t *testing.T) {
	for _, code := range []ErrorCode{CodeUnauthorized, CodeBusy, CodeTooLarge, CodeInvalidRequest} {
		t.Run(string(code), func(t *testing.T) {
			path := fakeServer(t, func(r Request) []byte {
				return []byte(`{"protocol":"` + Protocol + `","request_id":"","ok":false,"error":{"code":"` + string(code) + `","message":"m"}}` + "\n")
			})
			c, err := Dial(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, err = c.Hello(context.Background())
			var e *Error
			if !errors.As(err, &e) || e.Code != code {
				t.Fatalf("err = %v", err)
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

// hostile is text a faulty or impostor server could send to reach a terminal.
const hostile = "x\x1b[31m\x1b]0;PWNED\a"

func TestClientErrorTextsCarryNothingTheServerSent(t *testing.T) {
	long := strings.Repeat("A", 500000)
	cases := map[string]func(Request) []byte{
		"hostile code": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":false,"error":{"code":"x\u001b[31m\u001b]0;PWNED\u0007` + long + `","message":"` + `m\u001b[31m` + `"}}` + "\n")
		},
		"hostile protocol": func(r Request) []byte {
			return []byte(`{"protocol":"x\u001b]0;PWNED\u0007` + long + `","request_id":"` + r.RequestID + `","ok":true,"result":{}}` + "\n")
		},
		"hostile syntax": func(r Request) []byte { return []byte("{\x1b]0;PWNED\x07\n") },
		"hostile type": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":"x\u001b]0;PWNED\u0007"}` + "\n")
		},
		"hostile result": func(r Request) []byte {
			return []byte(`{"protocol":"` + Protocol + `","request_id":"` + r.RequestID + `","ok":true,"result":{"protocols":"x\u001b]0;PWNED\u0007"}}` + "\n")
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
			if err == nil {
				t.Fatal("no error")
			}
			text := err.Error()
			if len(text) > 200 {
				t.Errorf("error text is %d bytes", len(text))
			}
			for i := 0; i < len(text); i++ {
				if text[i] < 0x20 || text[i] > 0x7e {
					t.Fatalf("error text has the byte %#x: %q", text[i], text)
				}
			}
			// The code is the one server text that is printed, escaped and cut.
			if name != "hostile code" && (strings.Contains(text, "PWNED") || strings.Contains(text, "AAAA")) {
				t.Errorf("error text echoes the server: %q", text)
			}
			if name == "hostile code" && !strings.Contains(text, `\x1b[31m`) {
				t.Errorf("code is not printed escaped: %q", text)
			}
		})
	}
	t.Run("the fields stay available", func(t *testing.T) {
		e := &Error{Code: CodeBusy, Message: hostile}
		if e.Message != hostile || !strings.Contains(e.Error(), "busy") {
			t.Fatalf("Error() = %q", e.Error())
		}
	})
}
