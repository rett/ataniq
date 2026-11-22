package credkind

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"testing"

	"ataniq/internal/approval/webauthntest"
)

// TestRegisterKinds: the AAGUID picks the kind, and the credential keeps the
// registration's id, key, AAGUID, flags and counter.
func TestRegisterKinds(t *testing.T) {
	for _, tc := range []struct {
		aaguid [16]byte
		kind   string
	}{
		{[16]byte{}, NameUnknown},
		{[16]byte{0xde, 0xad}, NameUnknown},
		{bitwardenAAGUID, NameVault},
		{icloudAAGUID, NamePlatform},
	} {
		a := newAuth(t)
		a.AAGUID = tc.aaguid
		a.Flags |= webauthntest.FlagBE | webauthntest.FlagBS
		a.SignCount = 3
		r := a.Create(chal(1))
		c, err := Register(regInput(r))
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		want := Credential{
			Kind: tc.kind, CredentialID: a.CredentialID, SPKI: a.SPKI(), AAGUID: tc.aaguid,
			BackupEligible: true, BackupState: true, UserVerified: true, SignCount: 3,
		}
		if c.Kind != want.Kind || !bytes.Equal(c.CredentialID, want.CredentialID) || !bytes.Equal(c.SPKI, want.SPKI) ||
			c.AAGUID != want.AAGUID || c.BackupEligible != want.BackupEligible || c.BackupState != want.BackupState ||
			c.UserVerified != want.UserVerified || c.SignCount != want.SignCount {
			t.Errorf("%s: got %+v", tc.kind, c)
		}
		// The credential does not share the input's bytes.
		r.CredentialID[0] ^= 1
		r.PublicKey[len(r.PublicKey)-1] ^= 1
		if !bytes.Equal(c.CredentialID, a.CredentialID) || !bytes.Equal(c.SPKI, a.SPKI()) {
			t.Error("the credential shares the input's bytes")
		}
		// Only the kind of the AAGUID registers it.
		for _, k := range Kinds() {
			_, err := k.Register(regInput(a.Create(chal(1))))
			if k.Name() == tc.kind {
				if err != nil {
					t.Errorf("%s by its kind: %v", tc.kind, err)
				}
				continue
			}
			wantErr(t, tc.kind+" by "+k.Name(), err, ErrWrongKind)
		}
	}
}

// regAuthData builds registration authenticator data from parts.
func regAuthData(rpID string, flags byte, aaguid [16]byte, credID, cose, ext []byte) []byte {
	b := authHeadOf(rpID, flags, 0, nil)
	b = append(b, aaguid[:]...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(credID)))
	b = append(b, credID...)
	b = append(b, cose...)
	return append(b, ext...)
}

// TestRegisterFields: each field of a registration, changed, is refused by
// its own check.
func TestRegisterFields(t *testing.T) {
	a := newAuth(t)
	const flags = webauthntest.FlagUP | webauthntest.FlagUV | webauthntest.FlagAT
	id := a.CredentialID
	cose := a.COSEKey()
	good := regAuthData(testRPID, flags, [16]byte{}, id, cose, nil)
	if _, err := Register(RegistrationInput{RPID: testRPID, CredentialID: id, SPKI: a.SPKI(), AuthenticatorData: good}); err != nil {
		t.Fatal(err)
	}
	withCOSE := func(c []byte) []byte { return regAuthData(testRPID, flags, [16]byte{}, id, c, nil) }
	patched := func(i int, v byte) []byte { c := clone(cose); c[i] = v; return c }
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384SPKI, _ := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	offCurve := clone(cose)
	offCurve[76] ^= 1
	cases := []struct {
		name string
		in   RegistrationInput
		want error
	}{
		{"rp id other", RegistrationInput{testRPID, id, a.SPKI(), regAuthData("other.example.test", flags, [16]byte{}, id, cose, nil)}, ErrRPID},
		{"rp id empty", RegistrationInput{"", id, a.SPKI(), good}, ErrExpect},
		{"no UP", RegistrationInput{testRPID, id, a.SPKI(), regAuthData(testRPID, flags&^webauthntest.FlagUP, [16]byte{}, id, cose, nil)}, ErrUserPresence},
		{"no UV", RegistrationInput{testRPID, id, a.SPKI(), regAuthData(testRPID, flags&^webauthntest.FlagUV, [16]byte{}, id, cose, nil)}, ErrUserVerification},
		{"BS without BE", RegistrationInput{testRPID, id, a.SPKI(), regAuthData(testRPID, flags|webauthntest.FlagBS, [16]byte{}, id, cose, nil)}, ErrBackup},
		{"no AT", RegistrationInput{testRPID, id, a.SPKI(), regAuthData(testRPID, flags&^webauthntest.FlagAT, [16]byte{}, id, cose, nil)}, ErrAuthData},
		{"another id reported", RegistrationInput{testRPID, append(clone(id[:31]), id[31]^1), a.SPKI(), good}, ErrCredential},
		{"an id of 15 bytes", RegistrationInput{testRPID, id[:15], a.SPKI(), regAuthData(testRPID, flags, [16]byte{}, id[:15], cose, nil)}, ErrCredential},
		{"an id of 1024 bytes", RegistrationInput{testRPID, make([]byte, 1024), a.SPKI(), regAuthData(testRPID, flags, [16]byte{}, make([]byte, 1024), cose, nil)}, ErrCredential},
		{"an id of 15 in the data", RegistrationInput{testRPID, id, a.SPKI(), regAuthData(testRPID, flags, [16]byte{}, id[:15], cose, nil)}, ErrAuthData},
		{"another key reported", RegistrationInput{testRPID, id, newAuth(t).SPKI(), good}, ErrKey},
		{"a P-384 key reported", RegistrationInput{testRPID, id, p384SPKI, good}, ErrKey},
		{"the key with a byte after", RegistrationInput{testRPID, id, append(a.SPKI(), 0), good}, ErrKey},
		{"no key reported", RegistrationInput{testRPID, id, nil, good}, ErrKey},
		{"kty OKP", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(patched(2, 0x01))}, ErrKey},
		{"alg EdDSA", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(patched(4, 0x27))}, ErrKey},
		{"crv P-384", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(patched(6, 0x02))}, ErrKey},
		{"point off the curve", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(offCurve)}, ErrKey},
		{"a map of 6", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(append(patched(0, 0xa6), 0x23, 0x00))}, ErrAuthData},
		{"a map of 4", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(patched(0, 0xa4))}, ErrAuthData},
		{"keys out of order", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(append([]byte{0xa5, 0x03, 0x26, 0x01, 0x02}, cose[5:]...))}, ErrAuthData},
		{"kty in long form", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(append([]byte{0xa5, 0x18, 0x01}, cose[2:]...))}, ErrAuthData},
		{"x of 31 bytes", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(patched(9, 0x1f))}, ErrAuthData},
		{"x as text", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(patched(8, 0x78))}, ErrAuthData},
		{"cut in y", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(cose[:70])}, ErrAuthData},
		{"a byte after the key", RegistrationInput{testRPID, id, a.SPKI(), withCOSE(append(clone(cose), 0))}, ErrAuthData},
		{"ED without extensions", RegistrationInput{testRPID, id, a.SPKI(), regAuthData(testRPID, flags|webauthntest.FlagED, [16]byte{}, id, cose, nil)}, ErrAuthData},
		{"extensions not strict", RegistrationInput{testRPID, id, a.SPKI(), regAuthData(testRPID, flags|webauthntest.FlagED, [16]byte{}, id, cose, []byte{0xa1, 0x01, 0xf5})}, ErrAuthData},
		{"cut in the AAGUID", RegistrationInput{testRPID, id, a.SPKI(), good[:45]}, ErrAuthData},
		{"cut in the id", RegistrationInput{testRPID, id, a.SPKI(), good[:60]}, ErrAuthData},
		{"empty", RegistrationInput{testRPID, id, a.SPKI(), nil}, ErrAuthData},
		{"too long", RegistrationInput{testRPID, id, a.SPKI(), regAuthData(testRPID, flags|webauthntest.FlagED, [16]byte{}, id, cose, append([]byte{0xa1, 0x61, 'x', 0x59, 0x10, 0x00}, make([]byte, 4096)...))}, ErrAuthData},
	}
	for _, tc := range cases {
		_, err := Register(tc.in)
		wantErr(t, tc.name, err, tc.want)
		for _, k := range Kinds() {
			if _, err := k.Register(tc.in); err == nil {
				t.Errorf("%s: kind %s registered it", tc.name, k.Name())
			}
		}
	}
	// Strict extensions after the key are accepted.
	ext := regAuthData(testRPID, flags|webauthntest.FlagED, [16]byte{}, id, cose, []byte{0xa1, 0x6b, 'c', 'r', 'e', 'd', 'P', 'r', 'o', 't', 'e', 'c', 't', 0x02})
	if _, err := Register(RegistrationInput{testRPID, id, a.SPKI(), ext}); err != nil {
		t.Errorf("extensions: %v", err)
	}
}
