package seatpair

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// spkiOfScalar is the DER SubjectPublicKeyInfo of the P-256 key whose private
// scalar is the 32 bytes b, so a test has a key that is always the same.
func spkiOfScalar(b []byte) []byte {
	priv, err := ecdh.P256().NewPrivateKey(b)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKIXPublicKey(priv.PublicKey())
	if err != nil {
		panic(err)
	}
	return der
}

// scalarOf is a valid P-256 scalar made of one byte, repeated.
func scalarOf(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func marshal(t *testing.T, pub any) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestParseKeyAcceptsES256(t *testing.T) {
	for i := 0; i < 5; i++ {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseKey(marshal(t, &priv.PublicKey))
		if err != nil {
			t.Fatalf("ParseKey of a P-256 key: %v", err)
		}
		if !got.Equal(&priv.PublicKey) {
			t.Error("ParseKey returned another key")
		}
	}
	got, err := ParseKey(spkiOfScalar(scalarOf(1)))
	if err != nil || got.Curve != elliptic.P256() {
		t.Errorf("fixed key: %v, %v", got, err)
	}
}

func TestParseKeyRefusesOtherAlgorithms(t *testing.T) {
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p521, _ := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	x25519, _ := ecdh.X25519().GenerateKey(rand.Reader)
	for name, der := range map[string][]byte{
		"P-384":   marshal(t, &p384.PublicKey),
		"P-521":   marshal(t, &p521.PublicKey),
		"Ed25519": marshal(t, edPub),
		"RSA":     marshal(t, &rsaKey.PublicKey),
		"X25519":  marshal(t, x25519.PublicKey()),
	} {
		if _, err := ParseKey(der); err != ErrKeyAlgorithm {
			t.Errorf("%s: ParseKey = %v, want ErrKeyAlgorithm", name, err)
		}
	}
}

func TestParseKeyRefusesGarbage(t *testing.T) {
	good := spkiOfScalar(scalarOf(2))
	random := make([]byte, 91)
	_, _ = rand.Read(random)
	for name, der := range map[string][]byte{
		"nil":            nil,
		"empty":          {},
		"one byte":       {0x30},
		"zeros":          make([]byte, 91),
		"random":         random,
		"text":           []byte("-----BEGIN PUBLIC KEY-----"),
		"truncated":      good[:len(good)-1],
		"half":           good[:len(good)/2],
		"empty sequence": {0x30, 0x00},
		"too long":       bytes.Repeat([]byte{0x30}, maxKeyLen+1),
	} {
		if _, err := ParseKey(der); err != ErrKeyFormat {
			t.Errorf("%s: ParseKey = %v, want ErrKeyFormat", name, err)
		}
	}
}

func TestParseKeyRefusesTrailingBytes(t *testing.T) {
	good := spkiOfScalar(scalarOf(3))
	for name, extra := range map[string][]byte{
		"a zero":         {0},
		"a newline":      {'\n'},
		"zeros":          make([]byte, 16),
		"a second key":   good,
		"an empty seq.":  {0x30, 0x00},
		"a long garbage": bytes.Repeat([]byte{0xFF}, 300),
	} {
		der := append(append([]byte(nil), good...), extra...)
		if _, err := ParseKey(der); err != ErrKeyFormat {
			t.Errorf("trailing %s: ParseKey = %v, want ErrKeyFormat", name, err)
		}
	}
}

func TestParseKeyRefusesAPointOffTheCurve(t *testing.T) {
	good := spkiOfScalar(scalarOf(4))
	flipY := append([]byte(nil), good...)
	flipY[len(flipY)-1] ^= 0x01
	flipX := append([]byte(nil), good...)
	flipX[len(flipX)-33] ^= 0x80
	infinity := append([]byte(nil), good[:len(good)-65]...)
	infinity = append(infinity, make([]byte, 65)...)
	for name, der := range map[string][]byte{
		"y changed":   flipY,
		"x changed":   flipX,
		"all zero":    infinity,
		"compressed":  compressed(t, good),
		"hybrid form": withPrefix(good, 0x06),
	} {
		_, err := ParseKey(der)
		if err != ErrKeyPoint {
			t.Errorf("%s: ParseKey = %v, want ErrKeyPoint", name, err)
		}
	}
}

// withPrefix is der with the first byte of its point (0x04) replaced.
func withPrefix(der []byte, b byte) []byte {
	out := append([]byte(nil), der...)
	out[len(out)-65] = b
	return out
}

// compressed is a SubjectPublicKeyInfo for the same key with the point in the
// 33-byte compressed form, which Go does not parse and a browser does not give.
func compressed(t *testing.T, der []byte) []byte {
	t.Helper()
	point := der[len(der)-65:]
	c := append([]byte{2 + point[64]&1}, point[1:33]...)
	var info struct {
		Algorithm struct {
			Algorithm  asn1.ObjectIdentifier
			Parameters asn1.ObjectIdentifier
		}
		PublicKey asn1.BitString
	}
	info.Algorithm.Algorithm = oidECPublicKey
	info.Algorithm.Parameters = oidP256
	info.PublicKey = asn1.BitString{Bytes: c, BitLength: 8 * len(c)}
	out, err := asn1.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParseKeyRefusesNamedCurveWithoutParameters(t *testing.T) {
	good := spkiOfScalar(scalarOf(5))
	var info struct {
		Algorithm struct {
			Algorithm asn1.ObjectIdentifier
		}
		PublicKey asn1.BitString
	}
	info.Algorithm.Algorithm = oidECPublicKey
	info.PublicKey = asn1.BitString{Bytes: good[len(good)-65:], BitLength: 65 * 8}
	der, err := asn1.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseKey(der); err != ErrKeyAlgorithm {
		t.Errorf("EC key with no curve = %v, want ErrKeyAlgorithm", err)
	}
}

func TestParseKeyRefusesNonCanonicalEncodings(t *testing.T) {
	good := spkiOfScalar(scalarOf(6))
	// The outer length in the long form, which DER does not allow for 89.
	long := append([]byte{0x30, 0x81, good[1]}, good[2:]...)
	// The bit string with an unused-bits count other than zero.
	bits := append([]byte(nil), good...)
	bits[len(bits)-66] = 1
	for name, der := range map[string][]byte{
		"long length form": long,
		"unused bits":      bits,
	} {
		if _, err := ParseKey(der); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCheckCredentialID(t *testing.T) {
	for _, c := range []struct {
		n  int
		ok bool
	}{
		{0, false}, {1, false}, {15, false}, {16, true}, {32, true},
		{1023, true}, {1024, false}, {1025, false}, {1 << 20, false},
	} {
		err := CheckCredentialID(make([]byte, c.n))
		if c.ok && err != nil || !c.ok && err != ErrCredentialID {
			t.Errorf("id of %d bytes: %v", c.n, err)
		}
	}
	if err := CheckCredentialID(nil); err != ErrCredentialID {
		t.Errorf("nil id: %v", err)
	}
}

func TestKeyErrorsAreDistinctAndPlain(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range []Error{ErrCredentialID, ErrKeyFormat, ErrKeyAlgorithm, ErrKeyPoint} {
		if seen[e.Error()] {
			t.Errorf("text %q twice", e)
		}
		seen[e.Error()] = true
		if !errors.Is(fmt.Errorf("w: %w", e), e) {
			t.Errorf("errors.Is fails for %v", e)
		}
		if strings.ContainsAny(e.Error(), "\n") {
			t.Errorf("%q has a newline", e)
		}
	}
}

// TestPresentChecksTheCredential: a right code with a bad credential is
// refused for the credential, the code stays unused and no try is counted,
// and a wrong code with a bad credential is a wrong code.
func TestPresentChecksTheCredential(t *testing.T) {
	p, clk := newTestPairing()
	code, _ := p.Start(false)
	ed, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, c := range []struct {
		name string
		id   []byte
		spki []byte
		want error
	}{
		{"short id", idA[:15], spkiA, ErrCredentialID},
		{"long id", make([]byte, 1025), spkiA, ErrCredentialID},
		{"garbage key", idA, []byte("not a key"), ErrKeyFormat},
		{"no key", idA, nil, ErrKeyFormat},
		{"trailing byte", idA, append(append([]byte(nil), spkiA...), 0), ErrKeyFormat},
		{"ed25519", idA, marshal(t, ed), ErrKeyAlgorithm},
	} {
		if err := present(p, code, c.id, c.spki, clk.now); err != c.want {
			t.Errorf("%s: Present = %v, want %v", c.name, err, c.want)
		}
		if p.Phase(clk.now) != PhaseOpen || p.code.Used() || p.code.Tries() != 0 {
			t.Fatalf("%s: the pairing moved: %v", c.name, p)
		}
	}
	if err := present(p, wrongCode(code), idA, []byte("garbage"), clk.now); err != ErrWrongCode {
		t.Errorf("wrong code and garbage key = %v, want ErrWrongCode", err)
	}
	if err := present(p, code, idA, spkiA, clk.now); err != nil {
		t.Errorf("right code with a good credential after the refusals = %v", err)
	}
}
