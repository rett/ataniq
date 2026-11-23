package credkind

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"strings"
	"testing"

	"ataniq/internal/approval/webauthntest"
)

// TestVerifyGood: a fresh assertion of the registered credential holds, and
// the result reports UV, the counter and the backup state.
func TestVerifyGood(t *testing.T) {
	a, c := paired(t)
	if c.Kind != NameUnknown || c.UserVerified != true || c.BackupEligible || c.BackupState {
		t.Fatalf("credential %+v", c)
	}
	res, err := Verify(c, assertion(t, a, chal(1)), expect(chal(1)))
	if err != nil {
		t.Fatal(err)
	}
	if res != (Result{UserVerified: true}) {
		t.Errorf("result %+v", res)
	}
	// Every kind verifies the same way.
	for _, k := range Kinds() {
		a := newAuth(t)
		c, err := k.Register(regInput(a.Create(chal(9))))
		if k.Name() != NameUnknown {
			wantErr(t, k.Name()+" with a zero AAGUID", err, ErrWrongKind)
			a.AAGUID = map[string][16]byte{NamePlatform: icloudAAGUID, NameVault: bitwardenAAGUID}[k.Name()]
			c, err = k.Register(regInput(a.Create(chal(9))))
		}
		if err != nil {
			t.Fatalf("%s: %v", k.Name(), err)
		}
		if _, err := k.Verify(c, assertion(t, a, chal(2)), expect(chal(2))); err != nil {
			t.Errorf("%s: %v", k.Name(), err)
		}
	}
}

// cdParts builds client data JSON from raw members, in order.
func cdParts(members ...string) []byte {
	return []byte("{" + strings.Join(members, ",") + "}")
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// TestVerifyClientDataFields: each member of the client data, changed and
// signed again, is refused by its own check; the signature is good, so the
// field check is what refuses it.
func TestVerifyClientDataFields(t *testing.T) {
	a, c := paired(t)
	ch := chal(3)
	typ := `"type":"webauthn.get"`
	chm := `"challenge":"` + b64(ch) + `"`
	org := `"origin":"` + testOrigin + `"`
	cases := []struct {
		name string
		cd   []byte
		want error
	}{
		{"type create", cdParts(`"type":"webauthn.create"`, chm, org), ErrType},
		{"type empty", cdParts(`"type":""`, chm, org), ErrType},
		{"type case", cdParts(`"type":"WebAuthn.get"`, chm, org), ErrType},
		{"type missing", cdParts(chm, org), ErrClientData},
		{"type not a string", cdParts(`"type":1`, chm, org), ErrClientData},
		{"type null", cdParts(`"type":null`, chm, org), ErrClientData},
		{"challenge other", cdParts(typ, `"challenge":"`+b64(chal(4))+`"`, org), ErrChallenge},
		{"challenge padded", cdParts(typ, `"challenge":"`+base64.URLEncoding.EncodeToString(ch)+`"`, org), ErrChallenge},
		{"challenge standard alphabet", cdParts(typ, `"challenge":"`+base64.RawStdEncoding.EncodeToString(append(ch[:30:30], 0xfb, 0xff))+`"`, org), ErrChallenge},
		{"challenge truncated", cdParts(typ, `"challenge":"`+b64(ch)[:42]+`"`, org), ErrChallenge},
		{"challenge longer", cdParts(typ, `"challenge":"`+b64(append(clone(ch), 0))+`"`, org), ErrChallenge},
		{"challenge empty", cdParts(typ, `"challenge":""`, org), ErrChallenge},
		{"challenge missing", cdParts(typ, org), ErrClientData},
		{"challenge as bytes", cdParts(typ, `"challenge":[1,2]`, org), ErrClientData},
		{"origin other host", cdParts(typ, chm, `"origin":"https://evil.example.test"`), ErrOrigin},
		{"origin http", cdParts(typ, chm, `"origin":"http://seat.example.test"`), ErrOrigin},
		{"origin port", cdParts(typ, chm, `"origin":"https://seat.example.test:8443"`), ErrOrigin},
		{"origin slash", cdParts(typ, chm, `"origin":"https://seat.example.test/"`), ErrOrigin},
		{"origin case", cdParts(typ, chm, `"origin":"https://SEAT.example.test"`), ErrOrigin},
		{"origin missing", cdParts(typ, chm), ErrClientData},
		{"cross origin true", cdParts(typ, chm, org, `"crossOrigin":true`), ErrCrossOrigin},
		{"cross origin string", cdParts(typ, chm, org, `"crossOrigin":"false"`), ErrCrossOrigin},
		{"cross origin null", cdParts(typ, chm, org, `"crossOrigin":null`), ErrCrossOrigin},
		{"cross origin zero", cdParts(typ, chm, org, `"crossOrigin":0`), ErrCrossOrigin},
		{"top origin", cdParts(typ, chm, org, `"crossOrigin":false`, `"topOrigin":"https://other.example.test"`), ErrCrossOrigin},
		{"top origin null", cdParts(typ, chm, org, `"topOrigin":null`), ErrCrossOrigin},
		{"a member twice", cdParts(typ, chm, org, `"type":"webauthn.get"`), ErrClientData},
		{"origin twice, second good", cdParts(typ, chm, `"origin":"https://evil.example.test"`, org), ErrClientData},
		{"challenge twice", cdParts(typ, `"challenge":"x"`, chm, org), ErrClientData},
		{"cross origin twice", cdParts(typ, chm, org, `"crossOrigin":true`, `"crossOrigin":false`), ErrClientData},
		{"data after the object", append(cdParts(typ, chm, org), `{}`...), ErrClientData},
		{"a second value", append(cdParts(typ, chm, org), ` 1`...), ErrClientData},
		{"not an object", []byte(`["webauthn.get"]`), ErrClientData},
		{"invalid UTF-8", cdParts(typ, chm, org, "\"x\":\"\xff\""), ErrClientData},
		{"cut short", cdParts(typ, chm, org)[:20], ErrClientData},
		{"empty", []byte{}, ErrClientData},
		{"too long", cdParts(typ, chm, org, `"x":"`+strings.Repeat("a", maxClientData)+`"`), ErrClientData},
	}
	for _, tc := range cases {
		in := signed(t, a, tc.cd, a.AssertionAuthData())
		_, err := Verify(c, in, expect(ch))
		wantErr(t, tc.name, err, tc.want)
	}
	// What a browser may add, and the JSON spacing, are accepted.
	for name, cd := range map[string][]byte{
		"members in another order": cdParts(org, chm, typ),
		"cross origin false":       cdParts(typ, chm, org, `"crossOrigin":false`),
		"spaces":                   []byte(" {\n\"type\" : \"webauthn.get\" ,\t" + chm + " , " + org + " , \"crossOrigin\" :  false } \n"),
		"another member":           cdParts(typ, chm, org, `"other_keys_can_be_added_here":"do not compare clientDataJSON against a template. See https://goo.gl/yabPex"`),
		"a nested member":          cdParts(typ, chm, org, `"tokenBinding":{"status":"present","id":"x"}`),
		"escaped type":             cdParts(`"type":"webauthn.get"`, chm, org),
	} {
		in := signed(t, a, cd, a.AssertionAuthData())
		if _, err := Verify(c, in, expect(ch)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// authHeadOf builds authenticator data for an assertion.
func authHeadOf(rpID string, flags byte, count uint32, ext []byte) []byte {
	sum := sha256.Sum256([]byte(rpID))
	b := append(sum[:], flags)
	b = binary.BigEndian.AppendUint32(b, count)
	return append(b, ext...)
}

// TestVerifyAuthDataFields: each field of the authenticator data, changed and
// signed again, is refused by its own check.
func TestVerifyAuthDataFields(t *testing.T) {
	a, c := paired(t)
	ch := chal(5)
	cd := webauthntest.ClientData(webauthntest.TypeGet, ch, testOrigin)
	const upuv = webauthntest.FlagUP | webauthntest.FlagUV
	other := sha256.Sum256([]byte(testRPID))
	other[31] ^= 1
	cases := []struct {
		name string
		ad   []byte
		want error
	}{
		{"rp id other", authHeadOf("other.example.test", upuv, 0, nil), ErrRPID},
		{"rp id parent", authHeadOf("example.test", upuv, 0, nil), ErrRPID},
		{"rp id hash one bit", append(other[:], upuv, 0, 0, 0, 0), ErrRPID},
		{"no UP", authHeadOf(testRPID, webauthntest.FlagUV, 0, nil), ErrUserPresence},
		{"no UV", authHeadOf(testRPID, webauthntest.FlagUP, 0, nil), ErrUserVerification},
		{"no UP and no UV", authHeadOf(testRPID, 0, 0, nil), ErrUserPresence},
		{"BS without BE", authHeadOf(testRPID, upuv|webauthntest.FlagBS, 0, nil), ErrBackup},
		{"BE where registered without", authHeadOf(testRPID, upuv|webauthntest.FlagBE, 0, nil), ErrBackup},
		{"AT in an assertion", authHeadOf(testRPID, upuv|webauthntest.FlagAT, 0, nil), ErrAuthData},
		{"ED without extensions", authHeadOf(testRPID, upuv|webauthntest.FlagED, 0, nil), ErrAuthData},
		{"extensions without ED", authHeadOf(testRPID, upuv, 0, []byte{0xa0}), ErrAuthData},
		{"a byte after the head", authHeadOf(testRPID, upuv, 0, []byte{0}), ErrAuthData},
		{"extensions not a map", authHeadOf(testRPID, upuv|webauthntest.FlagED, 0, []byte{0x80}), ErrAuthData},
		{"extensions with a byte after", authHeadOf(testRPID, upuv|webauthntest.FlagED, 0, []byte{0xa0, 0}), ErrAuthData},
		{"extensions indefinite", authHeadOf(testRPID, upuv|webauthntest.FlagED, 0, []byte{0xbf, 0xff}), ErrAuthData},
		{"extensions keys out of order", authHeadOf(testRPID, upuv|webauthntest.FlagED, 0, []byte{0xa2, 0x61, 'b', 0xf5, 0x61, 'a', 0xf5}), ErrAuthData},
		{"cut in the counter", authHeadOf(testRPID, upuv, 0, nil)[:36], ErrAuthData},
		{"cut in the rp id hash", authHeadOf(testRPID, upuv, 0, nil)[:20], ErrAuthData},
		{"empty", []byte{}, ErrAuthData},
		{"too long", authHeadOf(testRPID, upuv|webauthntest.FlagED, 0, append([]byte{0xa1, 0x61, 'x', 0x59, 0x04, 0x00}, make([]byte, 1024)...)), ErrAuthData},
	}
	for _, tc := range cases {
		_, err := Verify(c, signed(t, a, cd, tc.ad), expect(ch))
		wantErr(t, tc.name, err, tc.want)
	}
	// Strict extensions, the reserved flag bits and any counter are
	// accepted.
	for name, ad := range map[string][]byte{
		"extensions":    authHeadOf(testRPID, upuv|webauthntest.FlagED, 0, []byte{0xa2, 0x61, 'a', 0xf5, 0x62, 'a', 'a', 0x43, 1, 2, 3}),
		"empty ext map": authHeadOf(testRPID, upuv|webauthntest.FlagED, 0, []byte{0xa0}),
		"reserved bits": authHeadOf(testRPID, upuv|0x02|0x20, 0, nil),
		"high counter":  authHeadOf(testRPID, upuv, 0xffffffff, nil),
	} {
		if _, err := Verify(c, signed(t, a, cd, ad), expect(ch)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestVerifyEveryByte: changing any one bit of the client data, the
// authenticator data or the signature, without signing again, refuses the
// assertion.
func TestVerifyEveryByte(t *testing.T) {
	a, c := paired(t)
	a.Flags |= webauthntest.FlagED
	a.Extensions = []byte{0xa1, 0x63, 'e', 'x', 't', 0xf5}
	ch := chal(6)
	in := assertion(t, a, ch)
	if _, err := Verify(c, in, expect(ch)); err != nil {
		t.Fatal(err)
	}
	fields := map[string]*[]byte{
		"client data": &in.ClientDataJSON,
		"auth data":   &in.AuthenticatorData,
		"signature":   &in.Signature,
		"credential":  &in.CredentialID,
		"stored id":   &c.CredentialID,
		"stored SPKI": &c.SPKI,
		"challenge":   new([]byte),
	}
	*fields["challenge"] = clone(ch)
	for name, f := range fields {
		orig := clone(*f)
		for i := range orig {
			for _, bit := range []byte{0x01, 0x80} {
				*f = clone(orig)
				(*f)[i] ^= bit
				e := expect(ch)
				if name == "challenge" {
					e.Challenge = *f
				}
				cin := in
				if _, err := Verify(c, cin, e); err == nil {
					t.Errorf("%s: byte %d bit %#x changed and accepted", name, i, bit)
				}
			}
		}
		*f = orig
	}
}

// TestVerifySignature: the signature must be the credential key's, over
// authData || SHA-256(clientData), in strict DER.
func TestVerifySignature(t *testing.T) {
	a, c := paired(t)
	ch := chal(7)
	in := assertion(t, a, ch)
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(in.Signature, &sig); err != nil {
		t.Fatal(err)
	}
	n := elliptic.P256().Params().N
	der := func(r, s []byte) []byte {
		ri := append([]byte{0x02, byte(len(r))}, r...)
		si := append([]byte{0x02, byte(len(s))}, s...)
		body := append(ri, si...)
		return append([]byte{0x30, byte(len(body))}, body...)
	}
	minimal := func(x *big.Int) []byte {
		b := x.Bytes()
		if b[0]&0x80 != 0 {
			b = append([]byte{0}, b...)
		}
		return b
	}
	r, s := minimal(sig.R), minimal(sig.S)
	// The good signature, rebuilt by hand, holds: the builder is right.
	in.Signature = der(r, s)
	if _, err := Verify(c, in, expect(ch)); err != nil {
		t.Fatal(err)
	}
	other := newAuth(t)
	rawDigest := sha256.Sum256(append(clone(in.AuthenticatorData), in.ClientDataJSON...))
	rawSig, _ := ecdsa.SignASN1(rand.Reader, a.Key, rawDigest[:])
	cdOnly := sha256.Sum256(in.ClientDataJSON)
	cdSig, _ := ecdsa.SignASN1(rand.Reader, a.Key, cdOnly[:])
	otherSig, _ := other.Sign(in.AuthenticatorData, in.ClientDataJSON)
	cases := map[string][]byte{
		"another key":                otherSig,
		"over the raw client data":   rawSig,
		"over the client data alone": cdSig,
		"a leading zero on r":        der(append([]byte{0}, r...), s),
		"a leading zero on s":        der(r, append([]byte{0}, s...)),
		"r negative":                 der(append([]byte{0x80}, r...), s),
		"r zero":                     der([]byte{0}, s),
		"s is n":                     der(r, minimal(n)),
		"s is n plus s":              der(r, minimal(new(big.Int).Add(n, sig.S))),
		"r and s swapped":            der(s, r),
		"a byte after":               append(der(r, s), 0),
		"a sequence length too long": func() []byte { b := der(r, s); b[1]++; return append(b, 0) }(),
		"long form length":           append([]byte{0x30, 0x81, byte(len(der(r, s)) - 2)}, der(r, s)[2:]...),
		"set, not sequence":          func() []byte { b := der(r, s); b[0] = 0x31; return b }(),
		"bit string integers":        func() []byte { b := der(r, s); b[2] = 0x03; return b }(),
		"a third integer":            func() []byte { b := der(r, s); b = append(b, 0x02, 0x01, 0x01); b[1] += 3; return b }(),
		"raw r || s":                 append(sig.R.FillBytes(make([]byte, 32)), sig.S.FillBytes(make([]byte, 32))...),
		"empty":                      {},
	}
	for name, s := range cases {
		in.Signature = s
		_, err := Verify(c, in, expect(ch))
		wantErr(t, name, err, ErrSignature)
	}
}

// TestVerifyCredential: the assertion must name the stored credential, and
// the stored key must be a canonical P-256 SPKI.
func TestVerifyCredential(t *testing.T) {
	a, c := paired(t)
	ch := chal(8)
	in := assertion(t, a, ch)
	bad := in
	bad.CredentialID = append(clone(in.CredentialID), 0)
	_, err := Verify(c, bad, expect(ch))
	wantErr(t, "a longer id", err, ErrCredential)
	bad.CredentialID = nil
	_, err = Verify(c, bad, expect(ch))
	wantErr(t, "no id", err, ErrCredential)
	short := c
	short.CredentialID = c.CredentialID[:15]
	bad.CredentialID = short.CredentialID
	_, err = Verify(short, bad, expect(ch))
	wantErr(t, "a stored id of 15 bytes", err, ErrCredential)

	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	spki := func(k any) []byte { b, _ := x509.MarshalPKIXPublicKey(k); return b }
	for name, key := range map[string][]byte{
		"P-384":           spki(&p384.PublicKey),
		"RSA":             spki(&rsaKey.PublicKey),
		"a byte after":    append(clone(c.SPKI), 0),
		"cut":             c.SPKI[:len(c.SPKI)-1],
		"empty":           nil,
		"another P-256":   newAuth(t).SPKI(),
		"too long":        make([]byte, maxSPKI+1),
		"long form outer": append([]byte{0x30, 0x81, c.SPKI[1]}, c.SPKI[2:]...),
	} {
		k := c
		k.SPKI = key
		_, err := Verify(k, in, expect(ch))
		want := ErrKey
		if name == "another P-256" {
			want = ErrSignature
		}
		wantErr(t, name, err, want)
	}
}

// TestVerifyExpect: an incomplete expectation refuses every assertion.
func TestVerifyExpect(t *testing.T) {
	a, c := paired(t)
	in := assertion(t, a, chal(1))
	for name, e := range map[string]Expect{
		"no challenge":     {Origin: testOrigin, RPID: testRPID},
		"short challenge":  {Challenge: chal(1)[:15], Origin: testOrigin, RPID: testRPID},
		"long challenge":   {Challenge: make([]byte, 65), Origin: testOrigin, RPID: testRPID},
		"no origin":        {Challenge: chal(1), RPID: testRPID},
		"no relying party": {Challenge: chal(1), Origin: testOrigin},
	} {
		_, err := Verify(c, in, e)
		wantErr(t, name, err, ErrExpect)
	}
	// The challenge may be 16 bytes.
	in = assertion(t, a, chal(1)[:16])
	if _, err := Verify(c, in, expect(chal(1)[:16])); err != nil {
		t.Error(err)
	}
}

// TestVerifyCounter: a counter that does not grow is reported, not refused.
func TestVerifyCounter(t *testing.T) {
	cases := []struct {
		stored, got uint32
		suspect     bool
	}{
		{0, 0, false}, // a synced passkey
		{0, 1, false},
		{5, 6, false},
		{5, 5, true},
		{5, 4, true},
		{5, 0, true},
		{0xffffffff, 0xffffffff, true},
	}
	for _, tc := range cases {
		a, c := paired(t)
		c.SignCount = tc.stored
		a.SignCount = tc.got
		res, err := Verify(c, assertion(t, a, chal(2)), expect(chal(2)))
		if err != nil {
			t.Fatalf("%d -> %d: %v", tc.stored, tc.got, err)
		}
		if res.Counter != tc.got || res.CounterSuspect != tc.suspect {
			t.Errorf("%d -> %d: %+v", tc.stored, tc.got, res)
		}
	}
}

// TestVerifyBackup: BS only with BE, BE as registered, and BS reported.
func TestVerifyBackup(t *testing.T) {
	a := newAuth(t)
	a.Flags |= webauthntest.FlagBE
	c, err := Register(regInput(a.Create(chal(9))))
	if err != nil {
		t.Fatal(err)
	}
	if !c.BackupEligible || c.BackupState {
		t.Fatalf("%+v", c)
	}
	res, err := Verify(c, assertion(t, a, chal(1)), expect(chal(1)))
	if err != nil || res.BackupState {
		t.Fatalf("%+v %v", res, err)
	}
	a.Flags |= webauthntest.FlagBS
	res, err = Verify(c, assertion(t, a, chal(1)), expect(chal(1)))
	if err != nil || !res.BackupState {
		t.Fatalf("BS set later: %+v %v", res, err)
	}
	a.Flags &^= webauthntest.FlagBE | webauthntest.FlagBS
	_, err = Verify(c, assertion(t, a, chal(1)), expect(chal(1)))
	wantErr(t, "BE dropped", err, ErrBackup)
	a.Flags |= webauthntest.FlagBS
	_, err = Verify(c, assertion(t, a, chal(1)), expect(chal(1)))
	wantErr(t, "BS without BE", err, ErrBackup)
}

// TestVerifyKind: a credential is verified only by its own kind, and a
// credential of no kind by none.
func TestVerifyKind(t *testing.T) {
	a, c := paired(t)
	in := assertion(t, a, chal(1))
	for _, k := range Kinds() {
		_, err := k.Verify(c, in, expect(chal(1)))
		if k.Name() == c.Kind {
			if err != nil {
				t.Errorf("own kind: %v", err)
			}
			continue
		}
		wantErr(t, k.Name(), err, ErrKind)
	}
	c.Kind = "hardware-key"
	_, err := Verify(c, in, expect(chal(1)))
	wantErr(t, "no such kind", err, ErrKind)
	c.Kind = ""
	_, err = Verify(c, in, expect(chal(1)))
	wantErr(t, "empty kind", err, ErrKind)
}

// TestErrorsHoldNoInput: no error text holds the origin, the challenge or the
// relying party id it was given.
func TestErrorsHoldNoInput(t *testing.T) {
	a, c := paired(t)
	in := assertion(t, a, chal(1))
	e := Expect{Challenge: chal(2), Origin: "https://secret-origin.example.test", RPID: "secret-rp.example.test"}
	_, err := Verify(c, in, e)
	if err == nil {
		t.Fatal("accepted")
	}
	for _, s := range []string{"secret", b64(chal(1)), b64(chal(2))} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("%q holds %q", err, s)
		}
	}
}
