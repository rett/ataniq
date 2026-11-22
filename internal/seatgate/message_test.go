package seatgate

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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
	type inner struct {
		Name string `json:"name"`
	}
	type Base struct {
		Kind string `json:"kind"`
	}
	type p struct {
		Base
		ID    string           `json:"id"`
		N     int              `json:"n"`
		In    *inner           `json:"in,omitempty"`
		List  []inner          `json:"list,omitempty"`
		ByKey map[string]inner `json:"by_key,omitempty"`
		Plain string           `json:",omitempty"` // no name in the tag: the field's
	}
	var got p
	if err := DecodeStrict(json.RawMessage(`{"id":"a","n":2}`), &got); err != nil || got.ID != "a" || got.N != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	got = p{}
	in := `{"id":"a","in":{"name":"x"},"list":[{"name":"y"}],"by_key":{"Any-Key":{"name":"z"}},"Plain":"q","kind":"k"}`
	if err := DecodeStrict(json.RawMessage(in), &got); err != nil || got.In.Name != "x" || got.List[0].Name != "y" ||
		got.ByKey["Any-Key"].Name != "z" || got.Plain != "q" || got.Kind != "k" {
		t.Fatalf("nested: %+v %v", got, err)
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
		// Keys are matched in their case, as the wire's own fields are; the
		// standard decoder would take each of these.
		"case variant":            `{"ID":"a"}`,
		"field alias":             `{"id":"a","ID":"b"}`,
		"case variant, nested":    `{"in":{"Name":"x"}}`,
		"case variant, in a list": `{"list":[{"NAME":"x"}]}`,
		"case variant, in a map":  `{"by_key":{"k":{"nAme":"x"}}}`,
		"case variant, untagged":  `{"plain":"q"}`,
		"case variant, promoted":  `{"KIND":"k"}`,
		"embedded struct by name": `{"Base":{"kind":"k"}}`,
	} {
		var g p
		if err := DecodeStrict(json.RawMessage(in), &g); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The types of the standard library's own tests of field selection
// (encoding/json decode_test.go and tagkey_test.go).
type (
	jsonS6  struct{ X int }
	jsonS8  struct{ jsonS9 }
	jsonS9  struct{ X, Y int }
	jsonS10 struct {
		jsonS11
		jsonS12
		jsonS13
	}
	jsonS11 struct{ jsonS6 }
	jsonS12 struct{ jsonS6 }
	jsonS13 struct{ jsonS8 }

	embed1 struct{ Q int }
	embed2 struct{ Q int }

	unexportedWithMethods struct{}

	badCodeTag struct {
		Z string `json:" !\"#&'()*+,."`
	}
)

func (unexportedWithMethods) F() {}

// embedMarshals is a type with a marshalling method, which json/v2 does not
// fill as a tagged embedded field of an unexported type, and encoding/json does.
type embedMarshals struct{ Q int }

func (embedMarshals) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// DecodeStrict takes a key only when the standard decoder would fill the field
// of exactly that name: its table of names follows the decoder's own choice
// of fields. It used to keep a name the decoder drops (two fields at one depth,
// or one type embedded by two paths), so the decoder, finding no field of that
// name, matched the key to another field in another case; it used to drop a
// tagged embedded struct of an unexported type, which the decoder fills; and it
// used to take a tag name the decoder does not.
func TestDecodeStrictChoosesTheDecodersFields(t *testing.T) {
	type IDA struct{ ID string }
	type IDB struct{ ID string }
	type conflict struct {
		IDA
		IDB
		Outer string `json:"id"`
	}
	type twoPaths struct {
		jsonS10
		Lower int `json:"x"`
	}
	type S6 struct {
		embed1 `json:"embed1"`
	}
	type S7 struct {
		embed1 `json:"embed1"`
		embed2
	}
	type S8 struct {
		embed1 `json:"embed1"`
		embed2 `json:"embed2"`
		Q      int
	}
	type S9 struct {
		unexportedWithMethods `json:"embed"`
	}
	// Each of these the decoder's two builds (with and without
	// GOEXPERIMENT=jsonv2) fill differently, so either may match a key to a
	// field in another case: they are refused whole.
	type symbolTag struct {
		A int `json:"a§b"` // json/v2: "a§b"; encoding/json: "A"
	}
	type quoteTag struct {
		A int `json:"a'b"` // json/v2: "a"; encoding/json: "A"
	}
	type embedTag struct {
		In embed1 `json:",embed"` // json/v2 promotes Q
	}
	type methodEmbed struct {
		embedMarshals `json:"m"` // json/v2 leaves it out, and matches "m" to M
		M             int
	}

	// Refused: each key is not the exact name of a field the decoder fills.
	for name, c := range map[string]struct {
		in  string
		dst any
	}{
		"a name two fields share, beside its other case":         {`{"id":"first","ID":"second"}`, new(conflict)},
		"a name two fields share":                                {`{"ID":"second"}`, new(conflict)},
		"a name of a type embedded twice, beside its other case": {`{"x":1,"X":2}`, new(twoPaths)},
		"a name of a type embedded twice":                        {`{"X":1,"Y":2}`, new(jsonS10)},
		"a case variant in a tagged embedded struct":             {`{"embed1":{"q":1}}`, new(S6)},
		"a tag the decoder does not take":                        {`{" !\"#&'()*+":"x"}`, new(badCodeTag)},
		"a symbol in a tag":                                      {`{"a§b":1}`, new(symbolTag)},
		"a symbol in a tag, Go name":                             {`{"A":1}`, new(symbolTag)},
		"a quote in a tag":                                       {`{"a":1}`, new(quoteTag)},
		"the embed option":                                       {`{"Q":1}`, new(embedTag)},
		"the embed option, field name":                           {`{"In":{"Q":1}}`, new(embedTag)},
		"an embedded type with methods":                          {`{"m":{"Q":1}}`, new(methodEmbed)},
	} {
		if err := DecodeStrict(json.RawMessage(c.in), c.dst); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v, %+v", name, err, c.dst)
		}
	}

	// Taken, as the standard decoder takes them.
	var c conflict
	if err := DecodeStrict(json.RawMessage(`{"id":"first"}`), &c); err != nil || c.Outer != "first" {
		t.Errorf("conflict: %+v %v", c, err)
	}
	var tp twoPaths
	if err := DecodeStrict(json.RawMessage(`{"x":1,"Y":2}`), &tp); err != nil || tp.Lower != 1 || tp.Y != 2 {
		t.Errorf("two paths: %+v %v", tp, err)
	}
	var s6 S6
	if err := DecodeStrict(json.RawMessage(`{"embed1":{"Q":1}}`), &s6); err != nil || s6.embed1.Q != 1 {
		t.Errorf("S6: %+v %v", s6, err)
	}
	var s7 S7
	if err := DecodeStrict(json.RawMessage(`{"embed1":{"Q":1},"Q":2}`), &s7); err != nil || s7 != (S7{embed1{1}, embed2{2}}) {
		t.Errorf("S7: %+v %v", s7, err)
	}
	var s8 S8
	if err := DecodeStrict(json.RawMessage(`{"embed1":{"Q":1},"embed2":{"Q":2},"Q":3}`), &s8); err != nil || s8 != (S8{embed1{1}, embed2{2}, 3}) {
		t.Errorf("S8: %+v %v", s8, err)
	}
	var s9 S9
	if err := DecodeStrict(json.RawMessage(`{"embed":{}}`), &s9); err != nil {
		t.Errorf("S9: %v", err)
	}
	var bad badCodeTag
	if err := DecodeStrict(json.RawMessage(`{"Z":"x"}`), &bad); err != nil || bad.Z != "x" {
		t.Errorf("bad tag: %+v %v", bad, err)
	}
}

// An interface that holds a pointer is decoded through the pointer, by the
// standard decoder, so DecodeStrict holds the keys under it to the pointed-to
// type. It used to look at the field's type alone, an interface, and take any
// key, so a case variant filled a field there.
func TestDecodeStrictFollowsAPointerInAnInterface(t *testing.T) {
	type inner struct {
		ID string `json:"id"`
	}
	type dst struct {
		P any            `json:"p"`
		L []any          `json:"l"`
		A [1]any         `json:"a"`
		V any            `json:"v"`
		M map[string]any `json:"m"`
	}
	fresh := func() *dst {
		return &dst{P: &inner{}, L: []any{&inner{}}, A: [1]any{&inner{}}, V: inner{}, M: map[string]any{"k": &inner{}}}
	}
	for name, in := range map[string]string{
		"a case variant":             `{"p":{"ID":"x"}}`,
		"a field alias":              `{"p":{"id":"a","ID":"b"}}`,
		"a case variant in a list":   `{"l":[{"ID":"x"}]}`,
		"a case variant in an array": `{"a":[{"ID":"x"}]}`,
	} {
		d := fresh()
		if err := DecodeStrict(json.RawMessage(in), d); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v, %+v", name, err, d.P)
		}
	}
	d := fresh()
	p, l, a := d.P.(*inner), d.L[0].(*inner), d.A[0].(*inner)
	// A struct held by value, and a map's values, are replaced by the decoder
	// with what it makes of the object, so any keys go there.
	in := `{"p":{"id":"x"},"l":[{"id":"y"}],"a":[{"id":"z"}],"v":{"ID":1},"m":{"k":{"ID":2}}}`
	if err := DecodeStrict(json.RawMessage(in), d); err != nil || p.ID != "x" || l.ID != "y" || a.ID != "z" {
		t.Fatalf("%v: %+v %+v %+v", err, p, l, a)
	}
}

// recursiveLink is a pointer to itself: a value of it is nil or points to
// another one, without end.
type recursiveLink *recursiveLink

// A pointer whose chain of types or of values comes back on itself is
// decoded from null, and anything else is refused, at once. DecodeStrict used
// to follow such a type for ever, before the decoder was reached, so a
// handler that decoded into one hung on any params (holding its place among
// the handlers); and the decoder itself recurses without end on one that is
// not null, until the stack overflows.
func TestDecodeStrictEndsOnACycleOfPointers(t *testing.T) {
	type link struct {
		Next recursiveLink
	}
	type held struct {
		P any `json:"p"`
	}
	decode := func(in string, dst any) error {
		res := make(chan error, 1)
		go func() { res <- DecodeStrict(json.RawMessage(in), dst) }()
		return within(t, "DecodeStrict to return on "+in, res, 5*time.Second)
	}
	var l link
	if err := decode(`{"Next":null}`, &l); err != nil || l.Next != nil {
		t.Fatalf("null: %v %v", err, l.Next)
	}
	for _, in := range []string{`{"Next":{}}`, `{"Next":1}`, `{"Next":[]}`} {
		if err := decode(in, new(link)); !errors.Is(err, ErrProtocol) {
			t.Fatalf("%s: %v", in, err)
		}
	}
	self := func() *held { // an interface that holds a pointer to itself
		h := new(held)
		h.P = &h.P
		return h
	}
	pair := func() *held { // two interfaces that hold pointers to each other
		h := new(held)
		var other any = &h.P
		h.P = &other
		return h
	}
	for name, mk := range map[string]func() *held{"self": self, "pair": pair} {
		if err := decode(`{"p":{"x":1}}`, mk()); !errors.Is(err, ErrProtocol) {
			t.Fatalf("%s: %v", name, err)
		}
		if h := mk(); decode(`{"p":null}`, h) != nil || h.P != nil {
			t.Fatalf("%s, null: %#v", name, h.P)
		}
	}
}

// A struct's fields are the same on every object of its type, so DecodeStrict
// works them out once per type and not once per object: a frame of many small
// objects costs a few allocations an object (the check of each object, about
// five), not the fifty that the selection of the fields cost each time.
// Allocations are counted, not time, so that a loaded machine cannot fail it.
func TestDecodeStrictWorksOutAStructsFieldsOnce(t *testing.T) {
	type item struct {
		A, B, C, D, E, F string
		G, H, I, J       int
		K                struct{ X, Y int }
	}
	type params struct {
		Items []item `json:"items"`
	}
	const n = 2000
	raw := json.RawMessage(`{"items":[` + strings.Repeat(`{},`, n-1) + `{}]}`)
	allocs := testing.AllocsPerRun(5, func() {
		var p params
		if err := DecodeStrict(raw, &p); err != nil || len(p.Items) != n {
			t.Fatal(err, len(p.Items))
		}
	})
	if per := allocs / n; per > 10 {
		t.Fatalf("DecodeStrict makes %.1f allocations an object", per)
	}
}

// The cache of a type's fields changes nothing about what is refused: with it
// warm, by the same type decoded before, a key of another case and a repeated
// key are still refused, and a refused type is still refused.
func TestDecodeStrictStaysStrictWithAWarmCache(t *testing.T) {
	type inner struct {
		Name string `json:"name"`
	}
	type params struct {
		ID    string  `json:"id"`
		Inner inner   `json:"inner"`
		List  []inner `json:"list"`
	}
	good := `{"id":"a","inner":{"name":"b"},"list":[{"name":"c"},{"name":"d"}]}`
	for round := 0; round < 3; round++ { // the first round warms the cache
		var p params
		if err := DecodeStrict(json.RawMessage(good), &p); err != nil || p.ID != "a" || p.Inner.Name != "b" || len(p.List) != 2 {
			t.Fatalf("round %d: %v %+v", round, err, p)
		}
		for name, in := range map[string]string{
			"upper case":          `{"ID":"a"}`,
			"title case":          `{"Id":"a"}`,
			"upper case, nested":  `{"inner":{"NAME":"b"}}`,
			"upper case, in list": `{"list":[{"name":"c"},{"Name":"d"}]}`,
			"repeated":            `{"id":"a","id":"b"}`,
			"repeated, nested":    `{"inner":{"name":"b","name":"c"}}`,
			"repeated, in list":   `{"list":[{"name":"c"},{"name":"d","name":"e"}]}`,
			"both cases":          `{"id":"a","ID":"b"}`,
			"unknown":             `{"other":1}`,
		} {
			var p params
			if err := DecodeStrict(json.RawMessage(in), &p); !errors.Is(err, ErrProtocol) {
				t.Errorf("round %d, %s: %s: %v", round, name, in, err)
			}
		}
		// A type the decoder reads in two builds differently stays refused.
		var bad struct {
			X int `json:"x"`
			Y int `json:",embed"`
		}
		if err := DecodeStrict(json.RawMessage(`{"embed":1}`), &bad); !errors.Is(err, ErrProtocol) {
			t.Errorf("round %d, embed option: %v", round, err)
		}
	}
	// Decodes of one type from many goroutines, the cache cold: the race
	// detector reads this.
	type fresh struct {
		A int `json:"a"`
		B struct {
			C int `json:"c"`
		} `json:"b"`
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var f fresh
			if err := DecodeStrict(json.RawMessage(`{"a":1,"b":{"c":2}}`), &f); err != nil || f.B.C != 2 {
				t.Error(err)
			}
			if err := DecodeStrict(json.RawMessage(`{"a":1,"B":{"c":2}}`), &f); !errors.Is(err, ErrProtocol) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
