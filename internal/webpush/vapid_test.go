package webpush

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// verifyToken checks a JWT the way a push service does, apart from the
// package's code: the header, the ES256 signature with pub (an uncompressed
// point), and returns the claims.
func verifyToken(t *testing.T, token string, pub []byte) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	var header map[string]any
	if err := json.Unmarshal(mustB64(t, parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "ES256" || header["typ"] != "JWT" || len(header) != 2 {
		t.Fatalf("header %v", header)
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub)
	if err != nil {
		t.Fatal(err)
	}
	sig := mustB64(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature of %d bytes", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
	var c map[string]any
	if err := json.Unmarshal(mustB64(t, parts[1]), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTokenClaimsAndSignature(t *testing.T) {
	k, err := GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	tok, err := k.Token("https://push.example.org", "mailto:ops@example.org", now, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c := verifyToken(t, tok, k.PublicKey())
	if c["aud"] != "https://push.example.org" || c["sub"] != "mailto:ops@example.org" ||
		c["exp"] != float64(now.Add(12*time.Hour).Unix()) || len(c) != 3 {
		t.Fatalf("claims %v", c)
	}
	// A token signed by another key does not verify with this one: the
	// check above is a real check.
	other, _ := GenerateVAPIDKey()
	tok2, _ := other.Token("https://push.example.org", "mailto:ops@example.org", now, time.Hour)
	parts := strings.Split(tok2, ".")
	key, _ := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), k.PublicKey())
	sig := mustB64(t, parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("another key's token verified")
	}

	header, err := k.AuthorizationHeader("https://push.example.org", "https://example.org/contact", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(header, "vapid t=")
	tokPart, keyPart, ok2 := strings.Cut(rest, ", k=")
	if !ok || !ok2 || keyPart != k.PublicKeyString() {
		t.Fatalf("header %q", header)
	}
	if c := verifyToken(t, tokPart, mustB64(t, keyPart)); c["sub"] != "https://example.org/contact" {
		t.Fatalf("claims %v", c)
	}
}

func TestTokenRefusals(t *testing.T) {
	k, _ := GenerateVAPIDKey()
	now := time.Now()
	cases := []struct {
		name, aud, sub string
		life           time.Duration
	}{
		{"life over 12 h", "https://p.example", "mailto:a@example.org", MaxTokenLife + time.Second},
		{"zero life", "https://p.example", "mailto:a@example.org", 0},
		{"negative life", "https://p.example", "mailto:a@example.org", -time.Hour},
		{"http audience", "http://p.example", "mailto:a@example.org", time.Hour},
		{"audience with path", "https://p.example/x", "mailto:a@example.org", time.Hour},
		{"empty audience", "https://", "mailto:a@example.org", time.Hour},
		{"empty subject", "https://p.example", "", time.Hour},
		{"other scheme", "https://p.example", "tel:123", time.Hour},
		{"bare mailto", "https://p.example", "mailto:", time.Hour},
		{"space in subject", "https://p.example", "mailto:a b@example.org", time.Hour},
		{"long subject", "https://p.example", "mailto:" + strings.Repeat("a", 250), time.Hour},
	}
	for _, c := range cases {
		if _, err := k.Token(c.aud, c.sub, now, c.life); !errors.Is(err, ErrToken) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, err := k.Token("https://p.example", "mailto:a@example.org", now, MaxTokenLife); err != nil {
		t.Errorf("12 h exactly: %v", err)
	}
}

func TestParseVAPIDKey(t *testing.T) {
	k, _ := GenerateVAPIDKey()
	data, err := k.MarshalPEM()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseVAPIDKey(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.PublicKey(), k.PublicKey()) || len(k.PublicKey()) != 65 || k.PublicKey()[0] != 4 {
		t.Fatal("key changed through PEM")
	}
	// PublicKey is a copy.
	p := k.PublicKey()
	p[1] ^= 1
	if bytes.Equal(p, k.PublicKey()) {
		t.Fatal("PublicKey is not a copy")
	}

	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der384, _ := x509.MarshalPKCS8PrivateKey(p384)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	derRSA, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	block, _ := pem.Decode(data)
	sec1, _ := x509.MarshalECPrivateKey(k.priv)
	bad := map[string][]byte{
		"empty":           nil,
		"P-384":           pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der384}),
		"RSA":             pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: derRSA}),
		"SEC 1 block":     pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1}),
		"two blocks":      append(bytes.Clone(data), data...),
		"text after":      append(bytes.Clone(data), "x"...),
		"headers":         pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Headers: map[string]string{"A": "b"}, Bytes: block.Bytes}),
		"garbage in DER":  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("nope")}),
		"no PEM at all":   []byte("hello"),
		"public key only": pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: block.Bytes}),
	}
	for name, d := range bad {
		if _, err := ParseVAPIDKey(d); !errors.Is(err, ErrKey) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseVAPIDKey(append(bytes.Clone(data), "\n\n"...)); err != nil {
		t.Errorf("trailing white space: %v", err)
	}
}
