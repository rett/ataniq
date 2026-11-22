package webauthntest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

const (
	testRPID   = "seat.example.test"
	testOrigin = "https://seat.example.test"
)

func newAuth(t *testing.T) *Authenticator {
	t.Helper()
	a, err := New(testRPID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func challenge(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

// TestNewDefaults: a fresh key and id per authenticator, UP and UV, a zero
// AAGUID and a zero counter.
func TestNewDefaults(t *testing.T) {
	a, b := newAuth(t), newAuth(t)
	if a.Flags != FlagUP|FlagUV {
		t.Errorf("flags %#x", a.Flags)
	}
	if a.AAGUID != ([16]byte{}) || a.SignCount != 0 || a.CounterStep != 0 || a.Extensions != nil {
		t.Error("not the defaults")
	}
	if len(a.CredentialID) != CredentialIDSize {
		t.Errorf("credential id of %d bytes", len(a.CredentialID))
	}
	if bytes.Equal(a.CredentialID, b.CredentialID) || a.Key.Equal(b.Key) {
		t.Error("two authenticators share a credential id or a key")
	}
}

// TestClientData: the JSON a browser makes, in its order, unescaped.
func TestClientData(t *testing.T) {
	ch := []byte{0xfb, 0xff, 0x01}
	got := string(ClientData(TypeGet, ch, "https://a.test/<&>"))
	want := `{"type":"webauthn.get","challenge":"-_8B","origin":"https://a.test/<&>","crossOrigin":false}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ClientData(`a"b`, nil, `\`)), `a"b`) {
		t.Error("a quote is not escaped")
	}
}

// TestCreate: the registration's authenticator data is laid out as WebAuthn
// says, with AT added to the flags and the chosen AAGUID, and its COSE_Key and
// SPKI are the same key.
func TestCreate(t *testing.T) {
	a := newAuth(t)
	a.AAGUID = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	a.Flags = FlagUP | FlagUV | FlagBE | FlagBS
	a.SignCount = 7
	r := a.Create(challenge(1))
	ad := r.AuthenticatorData
	sum := sha256.Sum256([]byte(testRPID))
	if !bytes.Equal(ad[:32], sum[:]) {
		t.Error("rpIdHash")
	}
	if ad[32] != FlagUP|FlagUV|FlagBE|FlagBS|FlagAT {
		t.Errorf("flags %#x", ad[32])
	}
	if binary.BigEndian.Uint32(ad[33:37]) != 7 {
		t.Error("counter")
	}
	if !bytes.Equal(ad[37:53], a.AAGUID[:]) {
		t.Error("aaguid")
	}
	n := int(binary.BigEndian.Uint16(ad[53:55]))
	if n != CredentialIDSize || !bytes.Equal(ad[55:55+n], a.CredentialID) {
		t.Error("credential id")
	}
	cose := ad[55+n:]
	if len(cose) != 77 || !bytes.Equal(cose, a.COSEKey()) {
		t.Errorf("COSE_Key of %d bytes", len(cose))
	}
	pub, err := x509.ParsePKIXPublicKey(r.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ek := pub.(*ecdsa.PublicKey)
	pt, _ := ek.ECDH()
	raw := pt.Bytes()
	if !bytes.Equal(cose[10:42], raw[1:33]) || !bytes.Equal(cose[45:77], raw[33:65]) {
		t.Error("the COSE_Key and the SPKI are not the same point")
	}
	var cd map[string]any
	if err := json.Unmarshal(r.ClientDataJSON, &cd); err != nil {
		t.Fatal(err)
	}
	if cd["type"] != TypeCreate || cd["origin"] != testOrigin ||
		cd["challenge"] != base64.RawURLEncoding.EncodeToString(challenge(1)) {
		t.Errorf("client data %s", r.ClientDataJSON)
	}
	if !bytes.Equal(r.CredentialID, a.CredentialID) {
		t.Error("credential id of the response")
	}
	r.CredentialID[0] ^= 1
	if bytes.Equal(r.CredentialID, a.CredentialID) {
		t.Error("the response shares the authenticator's id")
	}
}

// TestGet: the assertion's flags are exactly Flags, the counter steps, the
// extensions follow the head, and the signature verifies over
// authData || SHA-256(clientData) with the SPKI's key.
func TestGet(t *testing.T) {
	a := newAuth(t)
	a.SignCount, a.CounterStep = 10, 3
	a.Flags = FlagUP | FlagED
	a.Extensions = []byte{0xa0}
	as, err := a.Get(challenge(2))
	if err != nil {
		t.Fatal(err)
	}
	ad := as.AuthenticatorData
	if len(ad) != 38 || ad[32] != FlagUP|FlagED || ad[37] != 0xa0 {
		t.Errorf("authenticator data %x", ad)
	}
	if binary.BigEndian.Uint32(ad[33:37]) != 13 || a.SignCount != 13 {
		t.Error("the counter did not step")
	}
	pub, _ := x509.ParsePKIXPublicKey(a.SPKI())
	cdHash := sha256.Sum256(as.ClientDataJSON)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	if !ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), digest[:], as.Signature) {
		t.Error("the signature does not verify")
	}
	if !bytes.Equal(digest[:], SignedDigest(ad, as.ClientDataJSON)) {
		t.Error("SignedDigest")
	}
	var cd map[string]any
	json.Unmarshal(as.ClientDataJSON, &cd)
	if cd["type"] != TypeGet {
		t.Error("type")
	}
	if (&Authenticator{}).Key != nil {
		t.Fatal("zero value has a key")
	}
	if _, err := (&Authenticator{}).Sign(nil, nil); err == nil {
		t.Error("signed without a key")
	}
}

// TestStandardLibraryOnly: the package and its tests import the standard
// library only.
func TestStandardLibraryOnly(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			files++
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				first, _, _ := strings.Cut(path, "/")
				if strings.Contains(first, ".") || first == "ataniq" || first == "C" {
					t.Errorf("%s imports %s, which is not the standard library", name, path)
				}
			}
		}
	}
	if files < 3 {
		t.Errorf("only %d files read", files)
	}
}
