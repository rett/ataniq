package seatapp

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"math/big"
	"testing"
)

// coseOf builds a COSE_Key from its parts; a test changes the bytes it needs
// to.
func coseOf(x, y []byte) []byte {
	b := []byte{0xa5, 0x01, 0x02, 0x03, 0x26, 0x20, 0x01, 0x21, 0x58, 0x20}
	b = append(b, x...)
	b = append(b, 0x22, 0x58, 0x20)
	return append(b, y...)
}

func xyOf(a *authenticator) (x, y []byte) {
	pt, _ := a.key.PublicKey.ECDH()
	raw := pt.Bytes()
	return raw[1:33], raw[33:65]
}

// offCurve returns 32-byte x and y that are no point of P-256.
func offCurve() (x, y []byte) {
	x = bytes.Repeat([]byte{1}, 32)
	y = bytes.Repeat([]byte{2}, 32)
	return
}

func TestCOSEReadsTheCanonicalKey(t *testing.T) {
	a := newAuthenticator(t)
	spki, n, e := coseES256(a.cose())
	if e != nil || n != len(a.cose()) || !bytes.Equal(spki, spkiOf(t, &a.key.PublicKey)) {
		t.Fatalf("coseES256: %x %d %v", spki, n, e)
	}
	// Bytes after the key are not its own: n says where it ended.
	_, n, e = coseES256(append(a.cose(), 0xa0))
	if e != nil || n != len(a.cose()) {
		t.Errorf("with a byte after: %d %v", n, e)
	}
}

func TestCOSEMalformed(t *testing.T) {
	a := newAuthenticator(t)
	x, y := xyOf(a)
	good := a.cose()
	with := func(f func(b []byte) []byte) []byte { return f(append([]byte{}, good...)) }
	cases := []struct {
		name string
		in   []byte
		want apiError
	}{
		{"empty", nil, errAuthData},
		{"cut short", good[:len(good)-1], errAuthData},
		{"cut after the head", good[:1], errAuthData},
		{"not a map", with(func(b []byte) []byte { b[0] = 0x85; return b }), errAuthData},
		{"indefinite map", with(func(b []byte) []byte { b[0] = 0xbf; return append(b, 0xff) }), errAuthData},
		{"four entries", with(func(b []byte) []byte { b[0] = 0xa4; return b[:len(b)-35] }), errAuthData},
		{"six entries", with(func(b []byte) []byte { b[0] = 0xa6; return append(b, 0x04, 0x01) }), errAuthData},
		{"map of 5 in two bytes", with(func(b []byte) []byte { return append([]byte{0xb8, 0x05}, b[1:]...) }), errAuthData},
		{"kty as a text key", with(func(b []byte) []byte { b[1] = 0x61; return b }), errAuthData},
		{"keys out of order", with(func(b []byte) []byte { b[1], b[3] = 0x03, 0x01; return b }), errAuthData},
		{"duplicate key", with(func(b []byte) []byte { b[3] = 0x01; return b }), errAuthData},
		{"x before crv", coseOrder(x), errAuthData},
		{"extra key instead of y", with(func(b []byte) []byte { b[len(b)-35] = 0x23; return b }), errAuthData},
		{"kty as a text value", with(func(b []byte) []byte { b[2] = 0x61; return b }), errAuthData},
		{"alg as a byte string", with(func(b []byte) []byte { b[4] = 0x41; return b }), errAuthData},
		{"alg not in its shortest form", with(func(b []byte) []byte { return append(append(append([]byte{}, b[:4]...), 0x38, 0x06), b[5:]...) }), errAuthData},
		{"x as a text string", with(func(b []byte) []byte { b[8] = 0x78; return b }), errAuthData},
		{"x of 31 bytes", with(func(b []byte) []byte { b[9] = 0x1f; return b }), errAuthData},
		{"x of 33 bytes", with(func(b []byte) []byte { b[9] = 0x21; return b }), errAuthData},
		{"x with an indefinite length", with(func(b []byte) []byte { b[8] = 0x5f; return b }), errAuthData},
		{"x with a long length form", with(func(b []byte) []byte {
			return append(append(append([]byte{}, b[:8]...), 0x59, 0x00, 0x20), b[10:]...)
		}), errAuthData},
		{"y of 31 bytes", with(func(b []byte) []byte { b[len(b)-34] = 0x1f; return b }), errAuthData},
		{"y a number", with(func(b []byte) []byte { return append(b[:len(b)-34], 0x01) }), errAuthData},

		{"kty OKP", with(func(b []byte) []byte { b[2] = 0x01; return b }), errKey},
		{"alg ES384", with(func(b []byte) []byte {
			b[4] = 0x38
			return append(append(append([]byte{}, b[:4]...), 0x38, 0x22), b[5:]...)
		}), errKey},
		{"alg RS256", with(func(b []byte) []byte {
			b[4] = 0x39
			return append(append(append([]byte{}, b[:4]...), 0x39, 0x01, 0x00), b[5:]...)
		}), errKey},
		{"alg EdDSA", with(func(b []byte) []byte { b[4] = 0x27; return b }), errKey},
		{"crv P-384", with(func(b []byte) []byte { b[6] = 0x02; return b }), errKey},
		{"crv X25519", with(func(b []byte) []byte { b[6] = 0x04; return b }), errKey},
		{"x and y off the curve", coseOf(offCurve()), errKey},
		{"the point at (0, 0)", coseOf(make([]byte, 32), make([]byte, 32)), errKey},
		{"x not below the prime", coseOf(bytes.Repeat([]byte{0xff}, 32), y), errKey},
		{"x of one key, y of another", coseOf(x, otherY(t, a)), errKey},
	}
	for _, tc := range cases {
		spki, n, e := coseES256(tc.in)
		if e == nil || *e != tc.want {
			t.Errorf("%s: spki %x n %d err %v, want %v", tc.name, spki, n, e, tc.want)
		}
	}
}

// coseOrder builds a map with the five entries in an order that is not
// canonical.
func coseOrder(x []byte) []byte {
	b := []byte{0xa5, 0x21, 0x58, 0x20}
	b = append(b, x...)
	b = append(b, 0x01, 0x02, 0x03, 0x26, 0x20, 0x01)
	return b
}

// otherY is the y of another valid key.
func otherY(t *testing.T, not *authenticator) []byte {
	t.Helper()
	for {
		b := newAuthenticator(t)
		if _, y := xyOf(b); !bytes.Equal(y, mustY(not)) {
			return y
		}
	}
}

func mustY(a *authenticator) []byte { _, y := xyOf(a); return y }

func TestCOSEOnCurveCheckAgreesWithTheCurve(t *testing.T) {
	// A point found by search to be off the curve is refused; the key made by
	// the standard library is accepted. Guards against a parser that builds an
	// SPKI from any 64 bytes.
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pt, _ := k.PublicKey.ECDH()
	raw := pt.Bytes()
	if _, _, e := coseES256(coseOf(raw[1:33], raw[33:65])); e != nil {
		t.Fatalf("a good point: %v", e)
	}
	y := new(big.Int).SetBytes(raw[33:65])
	y.Add(y, big.NewInt(1))
	if _, _, e := coseES256(coseOf(raw[1:33], y.FillBytes(make([]byte, 32)))); e == nil || *e != errKey {
		t.Errorf("a point one off: %v", e)
	}
}

func TestExtensionsOK(t *testing.T) {
	deep := bytes.Repeat([]byte{0x81}, maxCBORDepth+2) // arrays nested too deep
	for _, tc := range []struct {
		name string
		in   []byte
		ok   bool
	}{
		{"empty map", []byte{0xa0}, true},
		{"hmac-secret true", append([]byte{0xa1, 0x6b}, append([]byte("hmac-secret"), 0xf5)...), true},
		{"map with an array", []byte{0xa1, 0x61, 'a', 0x82, 0x01, 0x02}, true},
		{"nothing", nil, false},
		{"not a map", []byte{0x80}, false},
		{"indefinite map", []byte{0xbf, 0xff}, false},
		{"indefinite text", []byte{0xa1, 0x7f, 0x61, 'a', 0xff, 0x01}, false},
		{"truncated", []byte{0xa1, 0x61, 'a'}, false},
		{"bytes after the map", []byte{0xa0, 0x00}, false},
		{"too deep", append([]byte{0xa1, 0x61, 'a'}, append(deep, 0x00)...), false},
		{"a huge count", []byte{0xbb, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, false},
		{"a count beyond the data", []byte{0xa5, 0x01, 0x02}, false},
		{"not shortest", []byte{0xb8, 0x00}, false},
	} {
		if got := extensionsOK(tc.in); got != tc.ok {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.ok)
		}
	}
}

// TestPairBindsTheKeyToTheCredential is the refusal of a registration whose
// posted public key is not the key in the attested credential data: the
// daemon would otherwise hold (credential A, key B).
func TestPairBindsTheKeyToTheCredential(t *testing.T) {
	a, b := newAuthenticator(t), newAuthenticator(t)
	spkiA, spkiB := spkiOf(t, &a.key.PublicKey), spkiOf(t, &b.key.PublicKey)
	for name, mod := range map[string]func(*registration){
		"credential A's data with B's key": func(x *registration) { x.SPKI = spkiB },
		"credential A's id and B's data":   func(x *registration) { x.COSE = b.cose() },
	} {
		r := newRig(t)
		w := r.postPair(a.register(t, r.getChallenge(t), mod))
		wantError(t, name, w, 400, "key_invalid")
		if len(r.daemon.requests()) != 0 {
			t.Errorf("%s: the daemon was called", name)
		}
	}
	// (B's data and B's key under A's id is consistent, and nothing in a
	// registration without attestation can say the id is not B's: the daemon
	// binds the id to the key, and the owner compares the words.)
	// The same key in both is accepted, and the daemon gets that key.
	r := newRig(t)
	if w := r.postPair(a.register(t, r.getChallenge(t))); w.Code != 200 {
		t.Fatalf("matching key: %d %s", w.Code, w.Body)
	}
	if c := r.daemon.requests(); len(c) != 1 || !bytes.Equal(c[0].SPKI, spkiA) {
		t.Errorf("daemon request %+v", c)
	}
}

// TestPairRefusesMalformedCOSEInTheRegistration shows each COSE rule through
// the whole handler, and the trailing-data rules of the authenticator data.
func TestPairRefusesMalformedCOSEInTheRegistration(t *testing.T) {
	a := newAuthenticator(t)
	good := a.cose()
	ext := []byte{0xa1, 0x6b, 'h', 'm', 'a', 'c', '-', 's', 'e', 'c', 'r', 'e', 't', 0xf5}
	for _, tc := range []struct {
		name string
		mod  func(*registration)
		code string
		ok   bool
	}{
		{"no key at all", func(x *registration) { x.COSE = nil }, "authenticator_data_invalid", false},
		{"indefinite map", func(x *registration) { x.COSE = append([]byte{0xbf}, append(good[1:], 0xff)...) }, "authenticator_data_invalid", false},
		{"six entries", func(x *registration) { x.COSE = append(append([]byte{0xa6}, good[1:]...), 0x04, 0x01) }, "authenticator_data_invalid", false},
		{"wrong alg", func(x *registration) { x.COSE = append(append(append([]byte{}, good[:4]...), 0x27), good[5:]...) }, "key_invalid", false},
		{"off the curve", func(x *registration) { x.COSE = coseOf(offCurve()) }, "key_invalid", false},
		{"trailing byte, no extension flag", func(x *registration) { x.COSE = append(append([]byte{}, good...), 0xa0) }, "authenticator_data_invalid", false},
		{"extension flag, nothing after", func(x *registration) { x.Flags = uvUPAT | flagED }, "authenticator_data_invalid", false},
		{"extension flag, garbage after", func(x *registration) { x.Flags = uvUPAT | flagED; x.COSE = append(append([]byte{}, good...), 0xff) }, "authenticator_data_invalid", false},
		{"extension flag, not a map", func(x *registration) { x.Flags = uvUPAT | flagED; x.COSE = append(append([]byte{}, good...), 0x80) }, "authenticator_data_invalid", false},
		{"extension flag, map and more", func(x *registration) {
			x.Flags = uvUPAT | flagED
			x.COSE = append(append(append([]byte{}, good...), ext...), 0x00)
		}, "authenticator_data_invalid", false},
		{"extension flag and a map", func(x *registration) {
			x.Flags = uvUPAT | flagED
			x.COSE = append(append([]byte{}, good...), ext...)
		}, "", true},
	} {
		r := newRig(t)
		w := r.postPair(a.register(t, r.getChallenge(t), tc.mod))
		if tc.ok {
			if w.Code != 200 {
				t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
			}
			continue
		}
		wantError(t, tc.name, w, 400, tc.code)
		if len(r.daemon.requests()) != 0 {
			t.Errorf("%s: the daemon was called", tc.name)
		}
	}
}
