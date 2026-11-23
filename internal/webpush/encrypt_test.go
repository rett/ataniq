package webpush

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// The values of RFC 8291 Appendix A, in base64url as the RFC gives them.
const (
	rfcPlaintext = "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24"
	rfcASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcMessage   = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := b64.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

// TestEncryptRFC8291Vector reproduces the message of RFC 8291 Appendix A
// byte for byte: the reader gives the RFC's salt and then its application
// server private key.
func TestEncryptRFC8291Vector(t *testing.T) {
	random := io.MultiReader(bytes.NewReader(mustB64(t, rfcSalt)), bytes.NewReader(mustB64(t, rfcASPrivate)))
	got, err := Encrypt(random, mustB64(t, rfcUAPublic), mustB64(t, rfcAuth), mustB64(t, rfcPlaintext))
	if err != nil {
		t.Fatal(err)
	}
	want := mustB64(t, rfcMessage)
	if !bytes.Equal(got, want) {
		t.Fatalf("message differs from RFC 8291 Appendix A:\n got  %s\n want %s", b64.EncodeToString(got), rfcMessage)
	}
	// The header names the RFC's application server key.
	if !bytes.Equal(got[saltLen+5:headerLen], mustB64(t, rfcASPublic)) {
		t.Fatal("key id is not the RFC's application server public key")
	}
	// And the user agent's side of the vector reads it back.
	plain, err := decrypt(mustB64(t, rfcUAPrivate), mustB64(t, rfcAuth), got)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "When I grow up, I want to be a watermelon" {
		t.Fatalf("decrypted %q", plain)
	}
}

// decrypt is the user agent's side of RFC 8291 and RFC 8188, for one record:
// the test's own reading of a message, written apart from Encrypt.
func decrypt(uaPrivate, auth, msg []byte) ([]byte, error) {
	if len(msg) < 21 {
		return nil, errors.New("short header")
	}
	salt := msg[:16]
	rs := binary.BigEndian.Uint32(msg[16:20])
	idlen := int(msg[20])
	if len(msg) < 21+idlen || rs < 18 {
		return nil, errors.New("bad header")
	}
	asPublicBytes := msg[21 : 21+idlen]
	record := msg[21+idlen:]
	if len(record) > int(rs) {
		return nil, errors.New("more than one record")
	}
	priv, err := ecdh.P256().NewPrivateKey(uaPrivate)
	if err != nil {
		return nil, err
	}
	asPublic, err := ecdh.P256().NewPublicKey(asPublicBytes)
	if err != nil {
		return nil, err
	}
	secret, err := priv.ECDH(asPublic)
	if err != nil {
		return nil, err
	}
	prkKey, _ := hkdf.Extract(sha256.New, secret, auth)
	info := append([]byte("WebPush: info\x00"), priv.PublicKey().Bytes()...)
	info = append(info, asPublicBytes...)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, record, nil)
	if err != nil {
		return nil, err
	}
	// Strip padding back to the delimiter, which must be 0x02 (last record).
	i := len(plain) - 1
	for i >= 0 && plain[i] == 0 {
		i--
	}
	if i < 0 || plain[i] != 0x02 {
		return nil, errors.New("no last-record delimiter")
	}
	return plain[:i], nil
}

// newUA makes a user agent's key pair and auth secret.
func newUA(t *testing.T) (*ecdh.PrivateKey, []byte) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return priv, auth
}

func TestEncryptRoundTrip(t *testing.T) {
	priv, auth := newUA(t)
	for _, payload := range [][]byte{nil, []byte(`{"n":3}`), bytes.Repeat([]byte{'x'}, MaxPayload)} {
		msg, err := Encrypt(rand.Reader, priv.PublicKey().Bytes(), auth, payload)
		if err != nil {
			t.Fatalf("payload of %d: %v", len(payload), err)
		}
		if len(msg) > RecordSize {
			t.Fatalf("payload of %d made a body of %d bytes, over %d", len(payload), len(msg), RecordSize)
		}
		if got := binary.BigEndian.Uint32(msg[16:20]); got != RecordSize {
			t.Fatalf("record size %d", got)
		}
		plain, err := decrypt(priv.Bytes(), auth, msg)
		if err != nil {
			t.Fatalf("payload of %d: %v", len(payload), err)
		}
		if !bytes.Equal(plain, payload) {
			t.Fatalf("payload of %d: round trip differs", len(payload))
		}
	}
	if MaxPayload != 3993 {
		t.Fatalf("MaxPayload is %d, RFC 8291 says 3993", MaxPayload)
	}
	// Two messages of the same payload differ: fresh salt and key each time.
	a, _ := Encrypt(rand.Reader, priv.PublicKey().Bytes(), auth, []byte("same"))
	b, _ := Encrypt(rand.Reader, priv.PublicKey().Bytes(), auth, []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("two messages are equal")
	}
	// A wrong auth secret does not read the message.
	other := bytes.Clone(auth)
	other[0] ^= 1
	if _, err := decrypt(priv.Bytes(), other, a); err == nil {
		t.Fatal("a wrong auth secret read the message")
	}
}

func TestEncryptRefusals(t *testing.T) {
	priv, auth := newUA(t)
	pub := priv.PublicKey().Bytes()
	if _, err := Encrypt(rand.Reader, pub, auth, make([]byte, MaxPayload+1)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("payload of 3994: %v", err)
	}
	bad := map[string][2][]byte{
		"short auth":           {pub, auth[:15]},
		"long auth":            {pub, append(bytes.Clone(auth), 0)},
		"compressed key":       {append([]byte{0x02}, pub[1:33]...), auth},
		"key not on the curve": {append([]byte{0x04}, make([]byte, 64)...), auth},
		"no key":               {nil, auth},
	}
	for name, c := range bad {
		if _, err := Encrypt(rand.Reader, c[0], c[1], []byte("x")); !errors.Is(err, ErrSubscriptionKeys) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A reader that runs dry, and one that gives only invalid scalars (all
	// ones is above the order of P-256).
	if _, err := Encrypt(bytes.NewReader(make([]byte, 20)), pub, auth, nil); !errors.Is(err, ErrRandom) {
		t.Fatalf("short reader: %v", err)
	}
	ones := bytes.Repeat([]byte{0xff}, saltLen+keyTries*scalarLen+64)
	if _, err := Encrypt(bytes.NewReader(ones), pub, auth, nil); !errors.Is(err, ErrRandom) {
		t.Fatalf("invalid scalars: %v", err)
	}
	// One invalid scalar, then a valid one: the second is used.
	r := io.MultiReader(bytes.NewReader(mustB64(t, rfcSalt)), bytes.NewReader(bytes.Repeat([]byte{0xff}, 32)), bytes.NewReader(mustB64(t, rfcASPrivate)))
	got, err := Encrypt(r, mustB64(t, rfcUAPublic), mustB64(t, rfcAuth), mustB64(t, rfcPlaintext))
	if err != nil || !bytes.Equal(got, mustB64(t, rfcMessage)) {
		t.Fatalf("retry after an invalid scalar: %v", err)
	}
}
