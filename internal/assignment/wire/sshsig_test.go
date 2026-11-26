package wire

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
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
	for _, line := range []string{key, keyType + " " + key, "fixture\t" + key + " fixture key\n", key + " comment\r\n"} {
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

func TestCanonicalPrimeOrderEnrollment(t *testing.T) {
	priv := testKey()
	pub := priv.Public().(ed25519.PublicKey)
	seed := sample()
	seed.KeyID = keyID(pub)
	h := assignment.Header{Schema: 1, Authority: seed.Home.Authority, Incarnation: seed.Home.Incarnation,
		Store: seed.Home.Store, LedgerID: "ledger", KeyID: seed.KeyID}
	signer, err := NewSigner(h, priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(h, pub)
	if err != nil {
		t.Fatal(err)
	}
	message, err := (assignment.Event{Public: seed}).SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signer.Sign(h, message)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(h, message, sig); err != nil {
		t.Fatal(err)
	}
	blob, err := decodeSignature(sig)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.TrimPrefix(message, []byte(domain))
	if err := Verify(pub, body, armor(blob)); err != nil {
		t.Fatal(err)
	}
	zeros := strings.Repeat("00", 30)
	ones := strings.Repeat("ff", 30)
	for name, encoded := range map[string]string{
		"identity":                         "01" + zeros + "00",
		"order two":                        "ec" + ones + "7f",
		"order four positive":              "00" + zeros + "00",
		"order four negative":              "00" + zeros + "80",
		"order eight 1":                    "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05",
		"order eight 2":                    "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85",
		"order eight 3":                    "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a",
		"order eight 4":                    "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa",
		"negative zero identity":           "01" + zeros + "80",
		"unreduced identity":               "ee" + ones + "7f",
		"unreduced negative zero identity": "ee" + ones + "ff",
		"negative zero order two":          "ec" + ones + "ff",
		"unreduced y":                      "ed" + ones + "7f",
		"off curve":                        "02" + zeros + "00",
		// The standard base point plus the point of order two.
		"mixed order": "95" + strings.Repeat("99", 31),
	} {
		t.Run(name, func(t *testing.T) {
			key, err := hex.DecodeString(encoded)
			if err != nil || len(key) != ed25519.PublicKeySize {
				t.Fatal("invalid fixture", err)
			}
			line := "home " + keyType + " " + base64.StdEncoding.EncodeToString(keyBlob(key))
			if _, _, err := LoadPublicKey(line); !errors.Is(err, errKey) {
				t.Fatalf("loaded key: %v", err)
			}
			badHeader := h
			badHeader.KeyID = keyID(key)
			if _, err := NewVerifier(badHeader, key); !errors.Is(err, errKey) {
				t.Fatalf("pinned key: %v", err)
			}
			if _, err := NewSigner(badHeader, priv); !errors.Is(err, errEnrollment) {
				t.Fatalf("accepted header: %v", err)
			}
			badSeed := seed
			badSeed.KeyID = badHeader.KeyID
			badMessage, err := (assignment.Event{Public: badSeed}).SigningBytes()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := signer.Sign(h, badMessage); !errors.Is(err, errEnrollment) {
				t.Fatalf("signed seed: %v", err)
			}
			if err := verifier.Verify(h, badMessage, sig); !errors.Is(err, errEnrollment) {
				t.Fatalf("verified seed: %v", err)
			}
			if err := Verify(key, body, armor(blob)); !errors.Is(err, errKey) {
				t.Fatalf("verified with invalid key: %v", err)
			}
		})
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
	badSeed := p
	badSeed.ProjectID = ""
	unaddressable, err := (assignment.Event{Public: badSeed}).SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign(h, unaddressable); err == nil {
		t.Fatal("signed unaddressable seed")
	}
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
	rawWire, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !bytes.HasPrefix(rawWire, []byte("SSHSIG")) {
		t.Fatal("undecodable wire signature", err)
	}
	want, err := Sign(testKey(), body)
	if err != nil || armor(rawWire) != want {
		t.Fatal("signature or domain prefix differs", err)
	}
	if err := v.Verify(h, message, sig); err != nil {
		t.Fatal(err)
	}
	for _, alt := range []string{sig[:20] + "\n" + sig[20:], sig + "\r\n", sig + "=", want} {
		if v.Verify(h, message, alt) == nil {
			t.Fatal("verified alternate signature spelling")
		}
		p.Signature = alt
		if _, err := Marshal(p); err == nil {
			t.Fatal("marshaled alternate signature spelling")
		}
	}
	p.Signature = ""
	for _, bad := range [][]byte{body, append([]byte(domain), message...), bytes.Replace(message, []byte(`"home":{"authority":"home"`), []byte(`"home":{"authority":"else"`), 1),
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
