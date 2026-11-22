package seatgate

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

// The fuzz tests feed the decoders any bytes. Their seeds run as ordinary
// tests. What must hold for any input: no panic; a refusal is a violation, or
// for a reader the end of input; no frame is read that is empty or beyond
// the cap; and what decodes can be encoded to bytes that decode to the same
// message.

func seedFrames(f *testing.F) {
	for _, p := range []string{
		`{"id":1,"op":"seat.hello","params":{"proto":1,"caps":["a"],"role":"seat"}}`,
		`{"id":2,"op":"seat.ping"}`,
		`{"id":3,"result":{"x":[1,2,{"y":null}]}}`,
		`{"id":4,"error":{"code":"failed"}}`,
		`{"id":1,"id":2,"op":"a.b"}`,
		`{"id":1,"op":"a.b","params":{"x":{"x":{"x":{"x":{"x":{"x":{"x":{}}}}}}}}}`,
		`{"id":1,"op":"a.b","extra":1}`,
		`{"id":1.5,"op":"a.b"}`,
		`{"id":1,"op":"a.b"}x`,
		``, `{}`, `[]`, `null`, "\xff", `{"id":"\ud800","op":"a.b"}`,
	} {
		f.Add([]byte(p))
	}
}

func isViolation(err error) bool { return errors.Is(err, ErrProtocol) }

func FuzzDecodeRequest(f *testing.F) {
	seedFrames(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := decodeRequest(data)
		if err != nil {
			if !isViolation(err) {
				t.Fatalf("refusal is not a violation: %v", err)
			}
			return
		}
		enc, err := r.encode()
		if err != nil {
			t.Fatalf("a request that decoded does not encode: %v", err)
		}
		again, err := decodeRequest(enc)
		if err != nil || again.ID != r.ID || again.Op != r.Op || !bytes.Equal(compact(again.Params), compact(r.Params)) {
			t.Fatalf("round trip: %+v then %+v (%v)", r, again, err)
		}
	})
}

func FuzzDecodeResponse(f *testing.F) {
	seedFrames(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := decodeResponse(data)
		if err != nil {
			if !isViolation(err) {
				t.Fatalf("refusal is not a violation: %v", err)
			}
			return
		}
		enc, err := r.encode()
		if err != nil {
			t.Fatalf("a response that decoded does not encode: %v", err)
		}
		again, err := decodeResponse(enc)
		if err != nil || again.ID != r.ID || again.Code != r.Code || !bytes.Equal(compact(again.Result), compact(r.Result)) {
			t.Fatalf("round trip: %+v then %+v (%v)", r, again, err)
		}
	})
}

func FuzzDecodeHello(f *testing.F) {
	for _, p := range []string{
		`{"proto":1,"caps":["a.b","c"],"role":"seat"}`, `{"proto":2,"caps":[],"role":"seat"}`,
		`{"proto":1,"caps":null,"role":"seat"}`, `{}`, ``, `{"proto":1,"caps":["a","a"],"role":"seat"}`,
	} {
		f.Add([]byte(p))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		h, err := decodeHello(data)
		if err != nil {
			if !isViolation(err) {
				t.Fatalf("refusal is not a violation: %v", err)
			}
			return
		}
		if h.Proto != ProtoMajor || h.Role != "seat" || !checkCaps(h.Caps) {
			t.Fatalf("a hello that decoded is not valid: %+v", h)
		}
	})
}

// FuzzReadFrame feeds the byte stream reader: whatever comes in, a frame that
// is returned is of 1 to MaxFrame bytes, and a frame that decodes as a request
// or a response was one under the rules above.
func FuzzReadFrame(f *testing.F) {
	for _, p := range []string{`{"id":1,"op":"a.b"}`, `{"id":1,"result":{}}`, ``, `{`} {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(len(p)))
		f.Add(append(b[:], p...))
	}
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{0, 1, 0, 1, 'x'})
	f.Add([]byte{0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		for i := 0; i < 8; i++ {
			p, err := readFrame(r)
			if err != nil {
				if err != io.EOF && !isViolation(err) {
					t.Fatalf("refusal is neither the end nor a violation: %v", err)
				}
				return
			}
			if len(p) == 0 || len(p) > MaxFrame {
				t.Fatalf("a frame of %d bytes was returned", len(p))
			}
			decodeRequest(p)
			decodeResponse(p)
		}
	})
}

func compact(b []byte) []byte {
	var out bytes.Buffer
	if len(b) == 0 {
		return nil
	}
	if err := jsonCompact(&out, b); err != nil {
		return b
	}
	return out.Bytes()
}

func jsonCompact(dst *bytes.Buffer, src []byte) error { return json.Compact(dst, src) }
