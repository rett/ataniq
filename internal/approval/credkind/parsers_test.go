package credkind

import (
	"bytes"
	"crypto/elliptic"
	"encoding/asn1"
	"math/big"
	"strings"
	"testing"
)

// TestExtensionsStrict: the extensions map is read as strict canonical CBOR.
func TestExtensionsStrict(t *testing.T) {
	nest := func(depth int) []byte {
		b := []byte{0xa1, 0x61, 'k'}
		for i := 0; i < depth; i++ {
			b = append(b, 0x81)
		}
		return append(b, 0x00)
	}
	many := func(n int) []byte {
		b := []byte{0xa1, 0x61, 'k', 0x98, byte(n)}
		return append(b, make([]byte, n)...)
	}
	good := map[string][]byte{
		"empty map":             {0xa0},
		"one boolean":           {0xa1, 0x61, 'a', 0xf5},
		"canonical order":       {0xa3, 0x61, 'a', 0xf4, 0x61, 'b', 0xf6, 0x62, 'a', 'a', 0xf7},
		"nested map":            {0xa1, 0x61, 'a', 0xa2, 0x01, 0x00, 0x20, 0x00},
		"bytes and text":        {0xa2, 0x61, 'a', 0x42, 1, 2, 0x61, 'b', 0x62, 0xc3, 0xa9},
		"one-byte length":       append([]byte{0xa1, 0x61, 'a', 0x58, 24}, make([]byte, 24)...),
		"depth at the bound":    nest(maxCBORDepth - 1),
		"items at the bound":    many(maxCBORItems - 2),
		"negative and unsigned": {0xa1, 0x61, 'a', 0x82, 0x38, 0x18, 0x18, 0x18},
	}
	for name, b := range good {
		if !extensionsOK(b) {
			t.Errorf("%s refused: %x", name, b)
		}
	}
	bad := map[string][]byte{
		"empty":                    {},
		"not a map":                {0x80},
		"an int key":               {0xa1, 0x01, 0xf5},
		"a bytes key":              {0xa1, 0x41, 'a', 0xf5},
		"keys out of order":        {0xa2, 0x61, 'b', 0xf5, 0x61, 'a', 0xf5},
		"longer key first":         {0xa2, 0x62, 'a', 'a', 0xf5, 0x61, 'b', 0xf5},
		"a key twice":              {0xa2, 0x61, 'a', 0xf5, 0x61, 'a', 0xf4},
		"nested keys out of order": {0xa1, 0x61, 'a', 0xa2, 0x02, 0x00, 0x01, 0x00},
		"nested key twice":         {0xa1, 0x61, 'a', 0xa2, 0x01, 0x00, 0x01, 0x00},
		"nested nint before uint":  {0xa1, 0x61, 'a', 0xa2, 0x20, 0x00, 0x01, 0x00},
		"indefinite map":           {0xbf, 0x61, 'a', 0xf5, 0xff},
		"indefinite bytes":         {0xa1, 0x61, 'a', 0x5f, 0x41, 0x00, 0xff},
		"reserved info":            {0xa1, 0x61, 'a', 0x1c},
		"long form small int":      {0xa1, 0x61, 'a', 0x18, 0x17},
		"long form length":         {0xa1, 0x78, 0x01, 'a', 0xf5},
		"two-byte form of 255":     {0xa1, 0x61, 'a', 0x19, 0x00, 0xff},
		"a tag":                    {0xa1, 0x61, 'a', 0xc1, 0x00},
		"a half float":             {0xa1, 0x61, 'a', 0xf9, 0x3c, 0x00},
		"a double":                 {0xa1, 0x61, 'a', 0xfb, 0, 0, 0, 0, 0, 0, 0, 0},
		"a simple value":           {0xa1, 0x61, 'a', 0xf0},
		"a two-byte simple":        {0xa1, 0x61, 'a', 0xf8, 0x20},
		"a break alone":            {0xa1, 0x61, 'a', 0xff},
		"invalid UTF-8 text":       {0xa1, 0x61, 'a', 0x61, 0xff},
		"invalid UTF-8 key":        {0xa1, 0x61, 0xff, 0xf5},
		"a byte after":             {0xa0, 0x00},
		"a second map":             {0xa0, 0xa0},
		"cut in a value":           {0xa1, 0x61, 'a', 0x42, 0x01},
		"cut in a head":            {0xa1, 0x61, 'a', 0x19, 0x01},
		"a map of 2 with 1":        {0xa2, 0x61, 'a', 0xf5},
		"a huge map":               {0xbb, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"a huge array":             {0xa1, 0x61, 'a', 0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"a huge string":            {0xa1, 0x61, 'a', 0x5b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"past the depth":           nest(maxCBORDepth),
		"past the items":           many(maxCBORItems - 1),
	}
	for name, b := range bad {
		if extensionsOK(b) {
			t.Errorf("%s accepted: %x", name, b)
		}
	}
}

// TestCanonicalLess: the order of CTAP2, major type first.
func TestCanonicalLess(t *testing.T) {
	order := [][]byte{{0x01}, {0x18, 0x18}, {0x20}, {0x38, 0x18}, {0x41, 0}, {0x60}, {0x61, 'a'}, {0x61, 'b'}, {0x62, 'a', 'a'}}
	for i := range order {
		for j := range order {
			if got := canonicalLess(order[i], order[j]); got != (i < j) {
				t.Errorf("%x < %x: %v", order[i], order[j], got)
			}
		}
	}
}

// TestSignatureDER: strict DER, minimal integers in [1, n-1], and what is
// accepted is exactly what encoding/asn1 writes for the same integers.
func TestSignatureDER(t *testing.T) {
	n := elliptic.P256().Params().N
	nMinus1 := new(big.Int).Sub(n, big.NewInt(1))
	enc := func(r, s *big.Int) []byte {
		b, err := asn1.Marshal(struct{ R, S *big.Int }{r, s})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for name, rs := range map[string][2]*big.Int{
		"ones":        {big.NewInt(1), big.NewInt(1)},
		"n-1":         {nMinus1, nMinus1},
		"top bit set": {big.NewInt(0x80), big.NewInt(0xff)},
		"mixed":       {big.NewInt(0x7f), nMinus1},
	} {
		sig := enc(rs[0], rs[1])
		r, s, ok := parseSignature(sig)
		if !ok || r.Cmp(rs[0]) != 0 || s.Cmp(rs[1]) != 0 {
			t.Errorf("%s refused: %x", name, sig)
		}
	}
	for name, sig := range map[string][]byte{
		"r zero":              enc(big.NewInt(0), big.NewInt(1)),
		"s zero":              enc(big.NewInt(1), big.NewInt(0)),
		"r is n":              enc(n, big.NewInt(1)),
		"s is n":              enc(big.NewInt(1), n),
		"r negative":          enc(big.NewInt(-1), big.NewInt(1)),
		"s negative":          enc(big.NewInt(1), big.NewInt(-5)),
		"padded r":            {0x30, 0x07, 0x02, 0x02, 0x00, 0x01, 0x02, 0x01, 0x01},
		"padded s":            {0x30, 0x07, 0x02, 0x01, 0x01, 0x02, 0x02, 0x00, 0x01},
		"padded top bit":      {0x30, 0x08, 0x02, 0x03, 0x00, 0x00, 0x80, 0x02, 0x01, 0x01},
		"empty integer":       {0x30, 0x05, 0x02, 0x00, 0x02, 0x01, 0x01},
		"length past the end": {0x30, 0x07, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01},
		"long sequence":       {0x30, 0x05, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01},
		"indefinite":          {0x30, 0x80, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01, 0x00, 0x00},
		"one integer":         {0x30, 0x03, 0x02, 0x01, 0x01},
		"integer too long":    append([]byte{0x30, 0x27, 0x02, 0x22, 0x01}, append(make([]byte, 33), 0x02, 0x01, 0x01)...),
		"a byte after":        append(enc(big.NewInt(1), big.NewInt(1)), 0),
		"empty":               nil,
	} {
		if _, _, ok := parseSignature(sig); ok {
			t.Errorf("%s accepted: %x", name, sig)
		}
	}
}

// TestClientDataMembers: one object, no member twice, valid UTF-8.
func TestClientDataMembers(t *testing.T) {
	for name, raw := range map[string]string{
		"plain":       `{"a":"b"}`,
		"spaces":      " \t\n{ \"a\" : 1 , \"b\" : [ ] } \n",
		"nested":      `{"a":{"a":1,"a":2}}`,
		"empty":       `{}`,
		"escaped key": `{"a":1,"b":2}`,
	} {
		if _, ok := clientDataMembers([]byte(raw)); !ok {
			t.Errorf("%s refused", name)
		}
	}
	for name, raw := range map[string]string{
		"duplicate":         `{"a":1,"a":1}`,
		"duplicate escaped": `{"a":1,"a":2}`,
		"array":             `[]`,
		"string":            `"x"`,
		"null":              `null`,
		"two objects":       `{}{}`,
		"trailing comma":    `{"a":1,}`,
		"unquoted key":      `{a:1}`,
		"single quotes":     `{'a':1}`,
		"cut":               `{"a":1`,
		"invalid UTF-8":     "{\"a\":\"\xc3\"}",
		"a NUL after":       "{}\x00",
		"a comment":         `{} //`,
		"too long":          `{"a":"` + strings.Repeat("x", maxClientData) + `"}`,
		"nothing":           ``,
	} {
		if _, ok := clientDataMembers([]byte(raw)); ok {
			t.Errorf("%s accepted", name)
		}
	}
	// A member's value is kept without the spaces around it.
	m, _ := clientDataMembers([]byte(`{"crossOrigin" :  false }`))
	if !bytes.Equal(m["crossOrigin"], []byte("false")) {
		t.Errorf("%q", m["crossOrigin"])
	}
}
