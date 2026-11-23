package seatnotice

import (
	"errors"
	"testing"
)

// The fuzz tests feed the decoders any bytes. Their seeds run as ordinary
// tests. What must hold for any input: no panic; a refusal is a violation;
// and what decodes encodes to bytes that decode to the same message.

func FuzzDecodeRequest(f *testing.F) {
	for _, p := range []string{
		`{"op":"info"}`, `{"op":"notify","n":5}`, `{"op":"notify","n":1000}`,
		`{"op":"subscribe","sub":` + subJSON(sub("f")) + `}`,
		`{"op":"unsubscribe","endpoint_sha256":"` + EndpointSHA256("https://push.example/x") + `"}`,
		`{"op":"info","op":"info"}`, `{"op":"notify","N":1}`, ``, `{}`, `[]`, `null`, "\xff", `{"op":"\ud800"}`,
		`{"op":"info","a":{"b":{"c":{"d":{}}}}}`,
	} {
		f.Add([]byte(p))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := decodeRequest(data)
		if err != nil {
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("refusal is not a violation: %v", err)
			}
			return
		}
		enc, err := r.encode()
		if err != nil {
			t.Fatalf("a request that decoded does not encode: %v", err)
		}
		if len(enc) > MaxFrame {
			t.Fatalf("encoded request is %d bytes", len(enc))
		}
		again, err := decodeRequest(enc)
		if err != nil || again != r {
			t.Fatalf("round trip: %+v then %+v (%v)", r, again, err)
		}
	})
}

func FuzzDecodeResponse(f *testing.F) {
	for _, p := range []string{
		`{"ok":true,"vapid_public":"` + fakeKey + `","subscriptions":1}`, `{"ok":true,"sent":2}`, `{"ok":true}`,
		`{"ok":false,"code":"too_many"}`, `{"ok":false}`, `{"ok":true,"ok":false}`, ``, `{}`, "\xff",
	} {
		f.Add([]byte(p))
	}
	ops := []string{OpInfo, OpNotify, OpSubscribe, OpUnsubscribe}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, op := range ops {
			r, err := decodeResponse(op, data)
			if err != nil {
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("refusal is not a violation: %v", err)
				}
				continue
			}
			code := r.Code
			enc, err := encodeResponse(op, code, r.Info, r.Sent)
			if err != nil {
				t.Fatalf("a response that decoded does not encode: %v", err)
			}
			again, err := decodeResponse(op, enc)
			if err != nil || again != r {
				t.Fatalf("round trip: %+v then %+v (%v)", r, again, err)
			}
		}
	})
}
