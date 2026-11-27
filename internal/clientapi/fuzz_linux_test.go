package clientapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// recordingHandler records every operation that reaches it.
type recordingHandler struct {
	mu  sync.Mutex
	ops []string
}

func (h *recordingHandler) Hello(ctx context.Context) HelloResult {
	h.mu.Lock()
	h.ops = append(h.ops, OpHello)
	h.mu.Unlock()
	return HelloResult{Version: "fuzz"}
}

func (h *recordingHandler) Status(ctx context.Context) (StatusResult, error) {
	h.mu.Lock()
	h.ops = append(h.ops, OpStatus)
	h.mu.Unlock()
	return StatusResult{}, nil
}

var fixedMessages = map[string]bool{
	msgInvalidRequest: true, msgTooLarge: true, msgUnsupportedProtocol: true,
	msgUnsupportedOperation: true, msgUnauthorized: true, msgBusy: true,
	msgInternal: true, msgResponseTooLarge: true,
}

var knownCodes = map[ErrorCode]bool{
	CodeInvalidRequest: true, CodeTooLarge: true, CodeUnsupportedProtocol: true,
	CodeUnsupportedOperation: true, CodeUnauthorized: true, CodeBusy: true, CodeInternal: true,
}

func addRequestSeeds(f *testing.F) {
	seeds := []string{
		helloReq, statusReq,
		``, `{}`, `[]`, `null`, `"hello"`, `0`, `{`, `}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"approve"}`,
		`{"protocol":"ataniq.client/2","request_id":"a","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","op":"status"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"status","op":"send"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"approve","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op" : "hello", "op" : "status"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"hello","\u006fp":"status"}`,
		`{"protocol":"ataniq.client/1","protocol":"ataniq.client/1","request_id":"a","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":null}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":{"op":"hello"}}`,
		`{"protocol":"ataniq.client/1","request_id":"\u0000","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"\ud800","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"hello"}`,
		`{"protocol":"ataniq.client/1","request_id":"a","op":"hello"} {}`,
		"{\"protocol\":\"ataniq.client/1\",\"request_id\":\"a\",\"op\":\"hello\"}\r",
		"\xff\xfe{\"op\":\"hello\"}",
		strings.Repeat("[", 5000),
		`{"protocol":"ataniq.client/1","request_id":"` + strings.Repeat("a", 64) + `","op":"status"}`,
		`{"protocol":"ataniq.client/1","request_id":"` + strings.Repeat("a", 65) + `","op":"status"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
}

// FuzzDecodeRequest drives the request decoder and the whole request path
// with arbitrary bytes. It asserts that nothing panics, that only the two
// operations are ever dispatched, and that every response is one valid line.
func FuzzDecodeRequest(f *testing.F) {
	addRequestSeeds(f)
	f.Fuzz(func(t *testing.T, line []byte) {
		id, op, fail := decodeRequest(line)
		if fail == nil {
			if op != OpHello && op != OpStatus {
				t.Fatalf("decoder passed op %q", op)
			}
			if !validRequestID(id) || !utf8.Valid(line) {
				t.Fatalf("decoder passed id %q", id)
			}
		} else {
			if op != "" || (id != "" && !validRequestID(id)) {
				t.Fatalf("failed decode returned op %q id %q", op, id)
			}
			if !knownCodes[fail.Code] || !fixedMessages[fail.Message] {
				t.Fatalf("error body %+v", fail)
			}
			if (fail.Code == CodeUnsupportedProtocol) != (len(fail.SupportedProtocols) > 0) {
				t.Fatalf("supported_protocols with %s", fail.Code)
			}
		}

		h := &recordingHandler{}
		s := newServer(h, Options{})
		out := s.respond(context.Background(), nil, line)

		if len(h.ops) > 1 {
			t.Fatalf("one request reached the handler %d times", len(h.ops))
		}
		for _, o := range h.ops {
			if o != OpHello && o != OpStatus {
				t.Fatalf("dispatched %q", o)
			}
		}
		if (fail == nil) != (len(h.ops) == 1) {
			t.Fatalf("decode failure %v but %d dispatches", fail, len(h.ops))
		}
		if len(out) == 0 || out[len(out)-1] != '\n' || bytes.Count(out, []byte{'\n'}) != 1 || len(out) > MaxResponseBytes {
			t.Fatalf("response is not one bounded line: %q", out)
		}
		var resp Response
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("response %q: %v", out, err)
		}
		if resp.Protocol != Protocol || (resp.RequestID != "" && !validRequestID(resp.RequestID)) {
			t.Fatalf("response %q", out)
		}
		if resp.OK != (fail == nil) || resp.OK == (resp.Error != nil) {
			t.Fatalf("response %q for failure %v", out, fail)
		}
		if resp.Error != nil && (!knownCodes[resp.Error.Code] || !fixedMessages[resp.Error.Message]) {
			t.Fatalf("response %q", out)
		}
	})
}

// FuzzReadLine drives the request line reader: no panic, no line over the cap,
// and a line it returns holds no LF.
func FuzzReadLine(f *testing.F) {
	for _, s := range []string{"", "\n", "a\nb\n", "abc", strings.Repeat("x", 5000) + "\n", "\n\n"} {
		f.Add([]byte(s), 16)
	}
	f.Fuzz(func(t *testing.T, data []byte, max int) {
		if max < 0 || max > 1<<16 {
			t.Skip()
		}
		br := newTestReader(data)
		var buf []byte
		for i := 0; i < len(data)+2; i++ {
			line, tooLarge, err := readLine(br, buf[:0], max)
			if err != nil || tooLarge {
				return
			}
			if len(line) > max || bytes.IndexByte(line, '\n') >= 0 {
				t.Fatalf("line %q over %d or holding LF", line, max)
			}
			buf = line
		}
	})
}
