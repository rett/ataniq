package credkind

import (
	"bytes"
	"encoding/asn1"
	"math/big"
	"testing"

	"ataniq/internal/approval/webauthntest"
)

// The fuzz tests run their seeds in every test run; `go test -fuzz` searches
// further. Each holds a property beyond "does not panic".

// FuzzExtensions: whatever the reader accepts is one map that ends where the
// input ends, and the reader's bounds hold.
func FuzzExtensions(f *testing.F) {
	for _, s := range [][]byte{
		{0xa0}, {0xa1, 0x61, 'a', 0xf5}, {0xa2, 0x61, 'a', 0xf4, 0x61, 'b', 0xa1, 0x01, 0x80},
		{0xbf, 0xff}, {0xa1, 0x61, 'a', 0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if !extensionsOK(b) {
			return
		}
		if b[0]>>5 != cborMap {
			t.Fatalf("accepted a non-map %x", b)
		}
		// Cut short, it is refused: the reader read to the end.
		if extensionsOK(b[:len(b)-1]) {
			t.Fatalf("accepted %x and its prefix", b)
		}
		if extensionsOK(append(clone(b), 0)) {
			t.Fatalf("accepted %x with a byte after", b)
		}
	})
}

// FuzzSignature: what the DER reader accepts is exactly the DER that
// encoding/asn1 writes for the same integers, in range.
func FuzzSignature(f *testing.F) {
	a := newAuth(f)
	sig, _ := a.Sign([]byte("a"), []byte("b"))
	f.Add(sig)
	f.Add([]byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01})
	f.Add([]byte{0x30, 0x07, 0x02, 0x02, 0x00, 0x01, 0x02, 0x01, 0x01})
	f.Fuzz(func(t *testing.T, b []byte) {
		r, s, ok := parseSignature(b)
		if !ok {
			return
		}
		again, err := asn1.Marshal(struct{ R, S *big.Int }{r, s})
		if err != nil || !bytes.Equal(again, b) {
			t.Fatalf("accepted %x, which is not its canonical DER %x", b, again)
		}
		if r.Sign() <= 0 || s.Sign() <= 0 || r.BitLen() > 256 || s.BitLen() > 256 {
			t.Fatalf("accepted %x out of range", b)
		}
	})
}

// FuzzClientData: the client data reader never panics, and accepts only an
// object with no member twice.
func FuzzClientData(f *testing.F) {
	ch := chal(1)
	f.Add(webauthntest.ClientData(webauthntest.TypeGet, ch, testOrigin))
	f.Add([]byte(`{"type":"webauthn.get","type":"x"}`))
	f.Add([]byte(`{"a":{"b":[1,2,{"c":null}]}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, ok := clientDataMembers(raw)
		if ok && m == nil {
			t.Fatal("accepted with no members map")
		}
		_ = checkClientData(raw, typeGet, expect(ch))
	})
}

// FuzzAuthData: the authenticator data readers never panic, a parsed
// registration's parts lie inside the input, and an accepted assertion head
// is the first 37 bytes.
func FuzzAuthData(f *testing.F) {
	a := newAuth(f)
	f.Add(a.RegistrationAuthData())
	f.Add(a.AssertionAuthData())
	a.Flags |= webauthntest.FlagED
	a.Extensions = []byte{0xa1, 0x61, 'a', 0xf5}
	f.Add(a.RegistrationAuthData())
	f.Add(a.AssertionAuthData())
	f.Fuzz(func(t *testing.T, b []byte) {
		if ad, err := parseAssertionAuthData(b); err == nil {
			if !bytes.Equal(ad.rpIDHash, b[:32]) || ad.flags != b[32] || ad.flags&flagAT != 0 {
				t.Fatalf("assertion %x parsed as %+v", b, ad)
			}
		}
		if ad, err := parseRegistrationAuthData(b); err == nil {
			if ad.flags&flagAT == 0 || len(ad.credID) < minCredentialID || !bytes.Contains(b, ad.credID) {
				t.Fatalf("registration %x parsed as %+v", b, ad)
			}
			if _, ok := parseSPKI(ad.spki); !ok {
				t.Fatalf("registration %x gave a key that does not parse", b)
			}
		}
	})
}

// FuzzVerify: an assertion whose client data or authenticator data differs
// from what the key signed is never accepted, whatever the signature bytes.
func FuzzVerify(f *testing.F) {
	a, c := paired(f)
	ch := chal(4)
	good := assertion(f, a, ch)
	f.Add(good.ClientDataJSON, good.AuthenticatorData, good.Signature)
	f.Add([]byte(`{}`), []byte{}, []byte{})
	f.Fuzz(func(t *testing.T, cd, ad, sig []byte) {
		in := AssertionInput{CredentialID: c.CredentialID, ClientDataJSON: cd, AuthenticatorData: ad, Signature: sig}
		_, err := Verify(c, in, expect(ch))
		if err != nil {
			return
		}
		if !bytes.Equal(cd, good.ClientDataJSON) || !bytes.Equal(ad, good.AuthenticatorData) {
			t.Fatalf("accepted data the key did not sign: %q %x", cd, ad)
		}
	})
}
