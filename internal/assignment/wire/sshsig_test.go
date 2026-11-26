package wire

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"ataniq/internal/assignment"
)

func testKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
}

func TestSignatureProfile(t *testing.T) {
	priv := testKey()
	pub := priv.Public().(ed25519.PublicKey)
	body := []byte(`{"schema":1}`)
	sig, err := Sign(priv, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(pub, body, sig); err != nil {
		t.Fatal(err)
	}
	if err := Verify(pub, body, strings.ReplaceAll(sig, "\n", "\r\n")); err != nil {
		t.Fatal(err)
	}
	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	if Verify(other.Public().(ed25519.PublicKey), body, sig) == nil ||
		Verify(pub, append(body, 0), sig) == nil || Verify(pub[:1], body, sig) == nil {
		t.Fatal("verified a different key or message")
	}
	for _, bad := range []string{sig + "x", " " + sig, sig + "\n", sig + sig, strings.Replace(sig, "\n", "\n?", 1)} {
		if Verify(pub, body, bad) == nil {
			t.Fatal("accepted bad armor")
		}
	}
	raw, err := unarmor(sig)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise every field (magic, version, key, namespace, reserved,
	// algorithm, nested algorithm, lengths and signature) at every offset.
	for i := range raw {
		raw[i] ^= 1
		if Verify(pub, body, armor(raw)) == nil {
			t.Fatalf("accepted mutation at %d", i)
		}
		raw[i] ^= 1
		if Verify(pub, body, armor(raw[:i])) == nil {
			t.Fatalf("accepted cut at %d", i)
		}
	}
	if Verify(pub, body, armor(append(raw, 0))) == nil {
		t.Fatal("accepted trailing byte")
	}
	priv[len(priv)-1] ^= 1
	for _, bad := range []ed25519.PrivateKey{nil, priv[:32], priv} {
		if _, err := Sign(bad, body); err == nil {
			t.Fatal("accepted malformed key")
		}
	}
}

func TestPublicKeyLine(t *testing.T) {
	pub := testKey().Public().(ed25519.PublicKey)
	encoded := base64.StdEncoding.EncodeToString(keyBlob(pub))
	key := keyType + " " + encoded
	for _, line := range []string{key, "fixture\t" + key + " fixture key\n", key + " comment\r\n"} {
		got, id, err := LoadPublicKey(line)
		if err != nil || !got.Equal(pub) || id != keyID(pub) {
			t.Fatal(id, err)
		}
	}
	for _, bad := range []string{"", keyType, "# " + key, "fixture cert-authority " + key, key + "\n" + key,
		key + "\r\n\r\n", "fixture ssh-rsa " + encoded, keyType + " !!!!!", keyType + " " + base64.StdEncoding.EncodeToString(pub),
		keyType + " " + base64.StdEncoding.EncodeToString(append(keyBlob(pub), 0))} {
		if _, _, err := LoadPublicKey(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestLedgerAdapters(t *testing.T) {
	priv := testKey()
	pub := priv.Public().(ed25519.PublicKey)
	h := assignment.Header{Schema: 1, Authority: "home", Store: "store", Incarnation: "instance", LedgerID: "ledger", KeyID: keyID(pub)}
	s, err := NewSigner(h, priv)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(h, pub)
	if err != nil {
		t.Fatal(err)
	}
	priv[0] ^= 1
	pub[0] ^= 1
	p := sample()
	p.Home = assignment.Instance{Authority: h.Authority, Store: h.Store, Incarnation: h.Incarnation}
	p.KeyID = h.KeyID
	message, err := (assignment.Event{Public: p}).SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := s.Sign(h, message)
	if err != nil {
		t.Fatal(err)
	}
	body, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Sign(testKey(), body)
	if err != nil || sig != want {
		t.Fatal("signature or domain prefix differs", err)
	}
	if err := v.Verify(h, message, sig); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{body, append([]byte(domain), message...), bytes.Replace(message, []byte("home\""), []byte("else\""), 1),
		bytes.Replace(message, []byte(p.KeyID), []byte("new"), 1), bytes.Replace(message, []byte(":"), []byte(": "), 1)} {
		if _, err := s.Sign(h, bad); err == nil || v.Verify(h, bad, sig) == nil {
			t.Fatal("accepted unbound message")
		}
	}
	h.LedgerID += "2"
	if _, err := s.Sign(h, message); err == nil || v.Verify(h, message, sig) == nil {
		t.Fatal("accepted a different header")
	}
	if _, err := NewVerifier(h, pub); err == nil {
		t.Fatal("enrolled mismatched key")
	}
	if _, err := NewSigner(h, priv); err == nil {
		t.Fatal("enrolled malformed private key")
	}
}

func FuzzSSHSig(f *testing.F) {
	priv := testKey()
	pub := priv.Public().(ed25519.PublicKey)
	body := []byte(`{"schema":1}`)
	sig, err := Sign(priv, body)
	if err != nil {
		f.Fatal(err)
	}
	raw, err := unarmor(sig)
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{[]byte(sig), raw, []byte(beginArmor), nil, append(bytes.Clone(raw), 0)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 4096 {
			return
		}
		// Mutate binary seeds as well as armor so corrupt length fields reach
		// the SSH parser independently of the outer base64 format.
		for _, candidate := range []string{string(b), armor(b)} {
			if Verify(pub, body, candidate) == nil {
				got, err := unarmor(candidate)
				if err != nil || !bytes.Equal(got, raw) {
					t.Fatal("accepted a different signature")
				}
			}
		}
	})
}
