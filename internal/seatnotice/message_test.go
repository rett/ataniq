package seatnotice

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestDecodeRequestAccepts(t *testing.T) {
	s := sub("abc")
	for _, tc := range []struct {
		in   string
		want request
	}{
		{`{"op":"info"}`, request{Op: OpInfo}},
		{`{"op":"notify","n":0}`, request{Op: OpNotify, N: 0}},
		{`{"op":"notify","n":7}`, request{Op: OpNotify, N: 7}},
		{`{"n":999,"op":"notify"}`, request{Op: OpNotify, N: 999}},
		{`{"op":"subscribe","sub":` + subJSON(s) + `}`, request{Op: OpSubscribe, Sub: s}},
		{`{"op":"unsubscribe","endpoint_sha256":"` + strings.Repeat("ab", 32) + `"}`, request{Op: OpUnsubscribe, EndpointSHA256: strings.Repeat("ab", 32)}},
	} {
		got, err := decodeRequest([]byte(tc.in))
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
}

func TestDecodeRequestRefuses(t *testing.T) {
	s := sub("abc")
	hash := strings.Repeat("ab", 32)
	bad := []string{
		``, `{}`, `[]`, `null`, `"info"`, `{"op":"info"} `, ` {"op":"info"}`, `{"op":"info"}{}`, "\xff",
		`{"op":"stop"}`, `{"op":"INFO"}`, `{"OP":"info"}`, `{"op":"info","op":"info"}`,
		`{"op":"info","n":1}`, `{"op":"info","extra":1}`, `{"op":1}`, `{"op":null}`,
		`{"op":"notify"}`, `{"op":"notify","n":-1}`, `{"op":"notify","n":1000}`, `{"op":"notify","n":1.0}`,
		`{"op":"notify","n":1e2}`, `{"op":"notify","n":"7"}`, `{"op":"notify","n":007}`, `{"op":"notify","n":+7}`,
		`{"op":"notify","n":null}`, `{"op":"notify","n":true}`, `{"op":"notify","n":7,"N":7}`, `{"op":"notify","N":7}`,
		`{"op":"notify","n":7,"sub":` + subJSON(s) + `}`,
		`{"op":"subscribe"}`, `{"op":"subscribe","sub":null}`, `{"op":"subscribe","sub":[]}`, `{"op":"subscribe","sub":{}}`,
		`{"op":"subscribe","sub":` + subJSON(s)[:len(subJSON(s))-1] + `,"x":1}}`,
		`{"op":"subscribe","sub":{"endpoint":"` + s.Endpoint + `","p256dh":"` + s.P256DH + `"}}`,
		`{"op":"subscribe","sub":{"endpoint":"` + s.Endpoint + `","p256dh":"` + s.P256DH + `","auth":"` + s.Auth + `","Auth":"x"}}`,
		`{"op":"subscribe","sub":{"endpoint":"` + s.Endpoint + `","endpoint":"` + s.Endpoint + `","p256dh":"` + s.P256DH + `","auth":"` + s.Auth + `"}}`,
		`{"op":"unsubscribe"}`, `{"op":"unsubscribe","endpoint_sha256":"` + hash[:63] + `"}`,
		`{"op":"unsubscribe","endpoint_sha256":"` + strings.ToUpper(hash) + `"}`,
		`{"op":"unsubscribe","endpoint_sha256":"` + hash + `","n":1}`,
		`{"op":"info","a":{"b":{"c":{"d":{"e":1}}}}}`,
	}
	for _, in := range bad {
		_, err := decodeRequest([]byte(in))
		if err == nil {
			t.Errorf("accepted %q", in)
		} else if !errors.Is(err, ErrProtocol) {
			t.Errorf("%q: refusal is not a violation: %v", in, err)
		}
	}
}

func TestValidateSubscriptionBounds(t *testing.T) {
	ok := sub("x")
	if err := ValidateSubscription(ok); err != nil {
		t.Fatal(err)
	}
	longOK := ok
	longOK.Endpoint = "https://push.example/" + strings.Repeat("a", MaxEndpoint-len("https://push.example/"))
	if len(longOK.Endpoint) != MaxEndpoint {
		t.Fatalf("fixture is %d bytes", len(longOK.Endpoint))
	}
	if err := ValidateSubscription(longOK); err != nil {
		t.Errorf("an endpoint of exactly %d bytes refused: %v", MaxEndpoint, err)
	}
	cases := map[string]func(s *Subscription){
		"endpoint too long": func(s *Subscription) { s.Endpoint += "a"; s.Endpoint = longOK.Endpoint + "a" },
		"empty endpoint":    func(s *Subscription) { s.Endpoint = "" },
		"http":              func(s *Subscription) { s.Endpoint = "http://push.example/x" },
		"no host":           func(s *Subscription) { s.Endpoint = "https:///x" },
		"user info":         func(s *Subscription) { s.Endpoint = "https://u:p@push.example/x" },
		"fragment":          func(s *Subscription) { s.Endpoint = "https://push.example/x#f" },
		"space":             func(s *Subscription) { s.Endpoint = "https://push.example/a b" },
		"control":           func(s *Subscription) { s.Endpoint = "https://push.example/a\x01" },
		"non ascii":         func(s *Subscription) { s.Endpoint = "https://push.example/é" },
		"upper scheme":      func(s *Subscription) { s.Endpoint = "HTTPS://push.example/x" },
		"p256dh short":      func(s *Subscription) { s.P256DH = b64(point(1)[:64]) },
		"p256dh long":       func(s *Subscription) { s.P256DH = b64(append(point(1), 0)) },
		"p256dh compressed": func(s *Subscription) { p := point(1); p[0] = 0x02; s.P256DH = b64(p) },
		"p256dh padded":     func(s *Subscription) { s.P256DH += "=" },
		"p256dh std alpha": func(s *Subscription) {
			s.P256DH = strings.NewReplacer("-", "+", "_", "/").Replace(b64(bytes.Repeat([]byte{0x04, 0xfb, 0xff}, 22)[:65]))
		},
		"p256dh non canon": func(s *Subscription) { s.P256DH = s.P256DH[:len(s.P256DH)-1] + "B" },
		"auth short":       func(s *Subscription) { s.Auth = b64([]byte("0123456789abcde")) },
		"auth long":        func(s *Subscription) { s.Auth = b64([]byte("0123456789abcdefg")) },
		"auth empty":       func(s *Subscription) { s.Auth = "" },
		"auth garbage":     func(s *Subscription) { s.Auth = "!!!!!!!!!!!!!!!!!!!!!!" },
	}
	for name, mod := range cases {
		s := ok
		mod(&s)
		if name == "p256dh non canon" && s.P256DH == ok.P256DH {
			continue
		}
		if err := ValidateSubscription(s); err == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
}

func TestDecodeResponse(t *testing.T) {
	key := fakeKey
	good := []struct {
		op, in string
		want   response
	}{
		{OpInfo, `{"ok":true,"vapid_public":"` + key + `","subscriptions":2}`, response{OK: true, Info: Info{VAPIDPublic: key, Subscriptions: 2}}},
		{OpNotify, `{"ok":true,"sent":3}`, response{OK: true, Sent: 3}},
		{OpSubscribe, `{"ok":true}`, response{OK: true}},
		{OpUnsubscribe, `{"ok":true}`, response{OK: true}},
		{OpSubscribe, `{"ok":false,"code":"too_many"}`, response{Code: "too_many"}},
		{OpInfo, `{"code":"unavailable","ok":false}`, response{Code: "unavailable"}},
	}
	for _, tc := range good {
		got, err := decodeResponse(tc.op, []byte(tc.in))
		if err != nil || got != tc.want {
			t.Errorf("%s %s: got %+v, %v", tc.op, tc.in, got, err)
		}
	}
	bad := []struct{ op, in string }{
		{OpInfo, `{"ok":true}`},
		{OpInfo, `{"ok":true,"vapid_public":"abc","subscriptions":1}`},
		{OpInfo, `{"ok":true,"vapid_public":"` + key + `","subscriptions":1000}`},
		{OpInfo, `{"ok":true,"vapid_public":"` + key + `","subscriptions":1,"sent":1}`},
		{OpNotify, `{"ok":true,"sent":-1}`},
		{OpNotify, `{"ok":true,"sent":1.5}`},
		{OpNotify, `{"ok":true}`},
		{OpSubscribe, `{"ok":true,"sent":1}`},
		{OpSubscribe, `{"ok":false}`},
		{OpSubscribe, `{"ok":false,"code":"Too-Many"}`},
		{OpSubscribe, `{"ok":false,"code":""}`},
		{OpSubscribe, `{"ok":false,"code":"` + strings.Repeat("a", 41) + `"}`},
		{OpSubscribe, `{"ok":false,"code":"x","sent":1}`},
		{OpSubscribe, `{"ok":"true"}`},
		{OpSubscribe, `{"ok":1}`},
		{OpSubscribe, `{"OK":true}`},
		{OpSubscribe, `{"ok":true,"ok":true}`},
		{"stop", `{"ok":true}`},
	}
	for _, tc := range bad {
		if _, err := decodeResponse(tc.op, []byte(tc.in)); err == nil || !errors.Is(err, ErrProtocol) {
			t.Errorf("%s %s: err %v", tc.op, tc.in, err)
		}
	}
}

func TestValidCode(t *testing.T) {
	for _, c := range []string{"a", "bad_subscription", "x9", strings.Repeat("a", 40)} {
		if !ValidCode(c) {
			t.Errorf("%q refused", c)
		}
	}
	for _, c := range []string{"", "A", "a-b", "a b", "é", strings.Repeat("a", 41), "a\n"} {
		if ValidCode(c) {
			t.Errorf("%q accepted", c)
		}
	}
	if NewCodedError("Bad Code").Code != CodeUnavailable {
		t.Error("a code out of the grammar was kept")
	}
}

func TestEncodeThenDecode(t *testing.T) {
	for _, r := range []request{
		{Op: OpInfo}, {Op: OpNotify, N: 0}, {Op: OpNotify, N: 999}, {Op: OpSubscribe, Sub: sub("q")},
		{Op: OpUnsubscribe, EndpointSHA256: EndpointSHA256("https://push.example/x")},
	} {
		b, err := r.encode()
		if err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
		got, err := decodeRequest(b)
		if err != nil || got != r {
			t.Errorf("%s: got %+v, %v", b, got, err)
		}
	}
	for _, r := range []request{{Op: OpNotify, N: -1}, {Op: OpNotify, N: 1000}, {Op: OpSubscribe}, {Op: OpUnsubscribe, EndpointSHA256: "x"}, {Op: "stop"}} {
		if _, err := r.encode(); err == nil {
			t.Errorf("%+v encoded", r)
		}
	}
}

func TestFrameBounds(t *testing.T) {
	hdr := func(n uint32) []byte { return binary.BigEndian.AppendUint32(nil, n) }
	if _, err := readFrame(bytes.NewReader(hdr(0))); !errors.Is(err, ErrProtocol) {
		t.Errorf("empty frame: %v", err)
	}
	if _, err := readFrame(bytes.NewReader(hdr(MaxFrame + 1))); !errors.Is(err, ErrProtocol) {
		t.Errorf("large frame: %v", err)
	}
	if _, err := readFrame(bytes.NewReader(append(hdr(10), 'x'))); !errors.Is(err, ErrProtocol) {
		t.Errorf("truncated frame: %v", err)
	}
	if _, err := readFrame(bytes.NewReader(nil)); !errors.Is(err, ErrProtocol) {
		t.Errorf("no frame: %v", err)
	}
	full := append(hdr(MaxFrame), bytes.Repeat([]byte{'x'}, MaxFrame)...)
	if p, err := readFrame(bytes.NewReader(full)); err != nil || len(p) != MaxFrame {
		t.Errorf("a frame of exactly %d: %d, %v", MaxFrame, len(p), err)
	}
	var buf bytes.Buffer
	if err := writeFrame(&buf, nil); err == nil {
		t.Error("an empty frame was written")
	}
	if err := writeFrame(&buf, make([]byte, MaxFrame+1)); err == nil {
		t.Error("a large frame was written")
	}
}
