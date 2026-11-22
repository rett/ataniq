package seatgate

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestValidOp(t *testing.T) {
	for _, s := range []string{"seat.hello", "seat.ping", "owner.list", "owner.decide-signed", "a.b", "abc.x1-y2", "seat." + strings.Repeat("a", 40)} {
		if !ValidOp(s) {
			t.Errorf("%q refused", s)
		}
	}
	for name, s := range map[string]string{
		"empty": "", "no period": "seat", "no name": "seat.", "no namespace": ".hello",
		"upper namespace": "Seat.hello", "upper name": "seat.Hello", "digit namespace": "s1.hello",
		"underscore": "seat.he_llo", "second period": "seat.he.llo", "long name": "seat." + strings.Repeat("a", 41),
		"space": "seat. hello", "newline": "seat.hello\n", "multibyte": "seat.hé",
		"leading space": " seat.hello", "slash": "seat/hello", "long": strings.Repeat("a", 70) + ".b",
	} {
		if ValidOp(s) {
			t.Errorf("%s: %q accepted", name, s)
		}
	}
}

func TestValidCode(t *testing.T) {
	for _, s := range []string{"failed", "a", "rate-limited", "x1", strings.Repeat("a", 40)} {
		if !ValidCode(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, s := range []string{"", strings.Repeat("a", 41), "Failed", "bad code", "bad_code", "bad.code", "é", "a\n"} {
		if ValidCode(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestDecodeRequest(t *testing.T) {
	r, err := decodeRequest([]byte(`{"id":7,"op":"owner.list"}`))
	if err != nil || r.ID != 7 || r.Op != "owner.list" || r.Params != nil {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = decodeRequest([]byte(`{"op":"owner.show","params":{"id":"x","n":[1,{"a":2}]},"id":18446744073709551615}`))
	if err != nil || r.ID != 18446744073709551615 || string(r.Params) != `{"id":"x","n":[1,{"a":2}]}` {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestDecodeRequestRefusals(t *testing.T) {
	deep := func(n int) string { return strings.Repeat(`{"a":`, n) + `1` + strings.Repeat(`}`, n) }
	cases := map[string]string{
		"empty":                  ``,
		"not json":               `hello`,
		"truncated":              `{"id":1,"op":"a.b"`,
		"array":                  `[]`,
		"null":                   `null`,
		"string":                 `"x"`,
		"leading space":          ` {"id":1,"op":"a.b"}`,
		"trailing space":         `{"id":1,"op":"a.b"} `,
		"trailing newline":       "{\"id\":1,\"op\":\"a.b\"}\n",
		"trailing bytes":         `{"id":1,"op":"a.b"}x`,
		"two objects":            `{"id":1,"op":"a.b"}{"id":2,"op":"a.b"}`,
		"trailing comma":         `{"id":1,"op":"a.b",}`,
		"duplicate id":           `{"id":1,"id":2,"op":"a.b"}`,
		"duplicate op":           `{"id":1,"op":"a.b","op":"c.d"}`,
		"duplicate by escape":    `{"id":1,"id":2,"op":"a.b"}`,
		"duplicate in params":    `{"id":1,"op":"a.b","params":{"x":1,"x":2}}`,
		"duplicate deep":         `{"id":1,"op":"a.b","params":{"x":[{"y":1,"y":1}]}}`,
		"unknown field":          `{"id":1,"op":"a.b","extra":1}`,
		"case variant of id":     `{"ID":1,"op":"a.b"}`,
		"case variant of op":     `{"id":1,"Op":"a.b"}`,
		"case variant of params": `{"id":1,"op":"a.b","Params":{}}`,
		"missing id":             `{"op":"a.b"}`,
		"missing op":             `{"id":1}`,
		"id zero":                `{"id":0,"op":"a.b"}`,
		"id negative":            `{"id":-1,"op":"a.b"}`,
		"id float":               `{"id":1.5,"op":"a.b"}`,
		"id float form of int":   `{"id":1.0,"op":"a.b"}`,
		"id exponent":            `{"id":1e2,"op":"a.b"}`,
		"id string":              `{"id":"1","op":"a.b"}`,
		"id null":                `{"id":null,"op":"a.b"}`,
		"id too big":             `{"id":18446744073709551616,"op":"a.b"}`,
		"op number":              `{"id":1,"op":5}`,
		"op null":                `{"id":1,"op":null}`,
		"op empty":               `{"id":1,"op":""}`,
		"op no period":           `{"id":1,"op":"hello"}`,
		"op upper":               `{"id":1,"op":"Seat.hello"}`,
		"op long":                `{"id":1,"op":"seat.` + strings.Repeat("a", 41) + `"}`,
		"op with space":          `{"id":1,"op":"seat. hello"}`,
		"op with escape":         `{"id":1,"op":"seat.hello\n"}`,
		"params null":            `{"id":1,"op":"a.b","params":null}`,
		"params array":           `{"id":1,"op":"a.b","params":[]}`,
		"params string":          `{"id":1,"op":"a.b","params":"x"}`,
		"params number":          `{"id":1,"op":"a.b","params":1}`,
		"too deep":               `{"id":1,"op":"a.b","params":` + deep(MaxDepth) + `}`,
		"invalid utf-8":          "{\"id\":1,\"op\":\"a.b\",\"params\":{\"x\":\"\xff\"}}",
		"bom":                    "\xef\xbb\xbf{\"id\":1,\"op\":\"a.b\"}",
		"nul":                    "{\"id\":1,\"op\":\"a.b\"}\x00",
		"comment":                `{"id":1,/*x*/"op":"a.b"}`,
		"single quotes":          `{'id':1,'op':'a.b'}`,
	}
	for name, in := range cases {
		_, err := decodeRequest([]byte(in))
		if !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v, want a violation", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "extra") {
			t.Errorf("%s: the error text echoes the input: %v", name, err)
		}
	}
	// One level less than too deep is fine.
	if _, err := decodeRequest([]byte(`{"id":1,"op":"a.b","params":` + deep(MaxDepth-2) + `}`)); err != nil {
		t.Errorf("a params nested to the limit: %v", err)
	}
}

func TestDecodeResponse(t *testing.T) {
	r, err := decodeResponse([]byte(`{"id":3,"result":{"x":1}}`))
	if err != nil || r.ID != 3 || string(r.Result) != `{"x":1}` || r.Code != "" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = decodeResponse([]byte(`{"error":{"code":"no-such-card"},"id":4}`))
	if err != nil || r.ID != 4 || r.Code != "no-such-card" || r.Result != nil {
		t.Fatalf("%+v %v", r, err)
	}
	cases := map[string]string{
		"both":                `{"id":1,"result":{},"error":{"code":"x"}}`,
		"neither":             `{"id":1}`,
		"missing id":          `{"result":{}}`,
		"id zero":             `{"id":0,"result":{}}`,
		"result array":        `{"id":1,"result":[]}`,
		"result null":         `{"id":1,"result":null}`,
		"error string":        `{"id":1,"error":"failed"}`,
		"error text":          `{"id":1,"error":{"code":"x","text":"leak"}}`,
		"error message only":  `{"id":1,"error":{"message":"leak"}}`,
		"error empty":         `{"id":1,"error":{}}`,
		"code upper":          `{"id":1,"error":{"code":"Failed"}}`,
		"code with space":     `{"id":1,"error":{"code":"not found"}}`,
		"code long":           `{"id":1,"error":{"code":"` + strings.Repeat("a", 41) + `"}}`,
		"code empty":          `{"id":1,"error":{"code":""}}`,
		"code number":         `{"id":1,"error":{"code":5}}`,
		"duplicate code":      `{"id":1,"error":{"code":"a","code":"b"}}`,
		"unknown field":       `{"id":1,"result":{},"op":"a.b"}`,
		"a request as answer": `{"id":1,"op":"a.b"}`,
		"trailing":            `{"id":1,"result":{}}x`,
	}
	for name, in := range cases {
		if _, err := decodeResponse([]byte(in)); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v, want a violation", name, err)
		}
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	for _, r := range []request{
		{ID: 1, Op: "seat.ping"},
		{ID: 99, Op: "owner.show", Params: json.RawMessage(`{"id":"x"}`)},
	} {
		b, err := r.encode()
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeRequest(b)
		if err != nil || got.ID != r.ID || got.Op != r.Op || string(got.Params) != string(r.Params) {
			t.Fatalf("%s -> %+v %v", b, got, err)
		}
	}
	for _, r := range []response{
		{ID: 1},
		{ID: 2, Result: json.RawMessage(`{"a":[1,2]}`)},
		{ID: 3, Code: "failed"},
	} {
		b, err := r.encode()
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeResponse(b)
		if err != nil || got.ID != r.ID || got.Code != r.Code {
			t.Fatalf("%s -> %+v %v", b, got, err)
		}
	}
}

func TestEncodeRefusesWhatItCouldNotRead(t *testing.T) {
	for name, r := range map[string]request{
		"id 0":          {Op: "a.b"},
		"bad op":        {ID: 1, Op: "ab"},
		"array params":  {ID: 1, Op: "a.b", Params: json.RawMessage(`[]`)},
		"invalid json":  {ID: 1, Op: "a.b", Params: json.RawMessage(`{`)},
		"string params": {ID: 1, Op: "a.b", Params: json.RawMessage(`"x"`)},
	} {
		if _, err := r.encode(); err == nil {
			t.Errorf("request %s: encoded", name)
		}
	}
	for name, r := range map[string]response{
		"id 0":         {},
		"bad code":     {ID: 1, Code: "Bad Code"},
		"array result": {ID: 1, Result: json.RawMessage(`[]`)},
	} {
		if _, err := r.encode(); err == nil {
			t.Errorf("response %s: encoded", name)
		}
	}
}

// The params and the result are written byte for byte: a frame near the cap
// must not grow when it is written again, which an HTML-escaping encoder would
// make it do.
func TestEncodeKeepsTheRawBytes(t *testing.T) {
	raw := json.RawMessage(`{"a":"<b>&</b>"}`)
	b, err := request{ID: 1, Op: "a.b", Params: raw}.encode()
	if err != nil || !strings.Contains(string(b), `"params":{"a":"<b>&</b>"}`) {
		t.Fatalf("%s %v", b, err)
	}
	b, err = response{ID: 1, Result: raw}.encode()
	if err != nil || !strings.Contains(string(b), `"result":{"a":"<b>&</b>"}`) {
		t.Fatalf("%s %v", b, err)
	}
}

func TestDecodeHello(t *testing.T) {
	h, err := decodeHello(json.RawMessage(`{"proto":1,"caps":["owner.read","seat.pair"],"role":"seat"}`))
	if err != nil || h.Proto != 1 || !reflect.DeepEqual(h.Caps, []string{"owner.read", "seat.pair"}) {
		t.Fatalf("%+v %v", h, err)
	}
	if h, err = decodeHello(json.RawMessage(`{"proto":1,"caps":[],"role":"seat"}`)); err != nil || len(h.Caps) != 0 {
		t.Fatalf("empty caps: %+v %v", h, err)
	}
	if _, err := decodeHello(json.RawMessage(`{"proto":2,"caps":[],"role":"seat"}`)); err != errProto {
		t.Errorf("proto 2: %v", err)
	}
	if _, err := decodeHello(json.RawMessage(`{"proto":1000000,"caps":[],"role":"seat"}`)); err != errProto {
		t.Errorf("proto 1000000: %v", err)
	}
	bad := map[string]string{
		"none":             ``,
		"no proto":         `{"caps":[],"role":"seat"}`,
		"no caps":          `{"proto":1,"role":"seat"}`,
		"no role":          `{"proto":1,"caps":[]}`,
		"role other":       `{"proto":1,"caps":[],"role":"gate"}`,
		"role empty":       `{"proto":1,"caps":[],"role":""}`,
		"proto zero":       `{"proto":0,"caps":[],"role":"seat"}`,
		"proto negative":   `{"proto":-1,"caps":[],"role":"seat"}`,
		"proto string":     `{"proto":"1","caps":[],"role":"seat"}`,
		"proto float":      `{"proto":1.0,"caps":[],"role":"seat"}`,
		"caps null":        `{"proto":1,"caps":null,"role":"seat"}`,
		"caps object":      `{"proto":1,"caps":{},"role":"seat"}`,
		"caps number":      `{"proto":1,"caps":[1],"role":"seat"}`,
		"caps upper":       `{"proto":1,"caps":["Owner"],"role":"seat"}`,
		"caps empty name":  `{"proto":1,"caps":[""],"role":"seat"}`,
		"caps duplicate":   `{"proto":1,"caps":["a","a"],"role":"seat"}`,
		"caps too many":    `{"proto":1,"caps":[` + strings.TrimSuffix(strings.Repeat(`"c1",`, 33), ",") + `],"role":"seat"}`,
		"caps long":        `{"proto":1,"caps":["` + strings.Repeat("a", 41) + `"],"role":"seat"}`,
		"unknown field":    `{"proto":1,"caps":[],"role":"seat","x":1}`,
		"duplicate proto":  `{"proto":1,"proto":1,"caps":[],"role":"seat"}`,
		"case variant":     `{"Proto":1,"caps":[],"role":"seat"}`,
		"heartbeat fields": `{"proto":1,"caps":[],"role":"seat","ping":15}`,
		"array":            `[]`,
		"trailing":         `{"proto":1,"caps":[],"role":"seat"} `,
	}
	for name, in := range bad {
		if _, err := decodeHello(json.RawMessage(in)); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDecodeHelloResult(t *testing.T) {
	h, err := decodeHelloResult(json.RawMessage(`{"proto":1,"caps":["a"]}`))
	if err != nil || h.Proto != 1 || len(h.Caps) != 1 {
		t.Fatalf("%+v %v", h, err)
	}
	for name, in := range map[string]string{
		"proto 2":  `{"proto":2,"caps":[]}`,
		"no caps":  `{"proto":1}`,
		"null":     `{"proto":1,"caps":null}`,
		"role":     `{"proto":1,"caps":[],"role":"seat"}`,
		"dup caps": `{"proto":1,"caps":["a","a"]}`,
	} {
		if _, err := decodeHelloResult(json.RawMessage(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if got := string(hello{Proto: 1}.encode()); got != `{"proto":1,"caps":[]}` {
		t.Errorf("encode: %s", got)
	}
}

func TestDecodeStrict(t *testing.T) {
	type p struct {
		ID string `json:"id"`
		N  int    `json:"n"`
	}
	var got p
	if err := DecodeStrict(json.RawMessage(`{"id":"a","n":2}`), &got); err != nil || got != (p{"a", 2}) {
		t.Fatalf("%+v %v", got, err)
	}
	got = p{}
	if err := DecodeStrict(nil, &got); err != nil {
		t.Fatalf("absent params: %v", err)
	}
	for name, in := range map[string]string{
		"unknown field": `{"id":"a","z":1}`,
		"duplicate":     `{"id":"a","id":"b"}`,
		"wrong type":    `{"id":1}`,
		"trailing":      `{"id":"a"}{}`,
		"array":         `[]`,
		"null":          `null`,
		"float":         `{"n":1.5}`,
	} {
		var g p
		if err := DecodeStrict(json.RawMessage(in), &g); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
