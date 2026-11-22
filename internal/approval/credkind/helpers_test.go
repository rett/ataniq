package credkind

import (
	"bytes"
	"errors"
	"testing"

	"ataniq/internal/approval/webauthntest"
)

const (
	testRPID   = "seat.example.test"
	testOrigin = "https://seat.example.test"
)

// bitwardenAAGUID is the vault AAGUID of the table that the tests use.
var bitwardenAAGUID = mustAAGUID("d548826e-79b4-db40-a3d8-11116f7e8349")

// icloudAAGUID is a platform AAGUID of the table.
var icloudAAGUID = mustAAGUID("fbfc3007-154e-4ecc-8c0b-6e020557d7bd")

func mustAAGUID(s string) [16]byte {
	id, ok := parseAAGUID(s)
	if !ok {
		panic(s)
	}
	return id
}

func chal(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func expect(ch []byte) Expect {
	return Expect{Challenge: ch, Origin: testOrigin, RPID: testRPID}
}

func newAuth(t testing.TB) *webauthntest.Authenticator {
	t.Helper()
	a, err := webauthntest.New(testRPID, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func regInput(r webauthntest.Registration) RegistrationInput {
	return RegistrationInput{
		RPID:              testRPID,
		CredentialID:      r.CredentialID,
		SPKI:              r.PublicKey,
		AuthenticatorData: r.AuthenticatorData,
	}
}

// paired registers a fresh authenticator and returns it with its credential.
func paired(t testing.TB) (*webauthntest.Authenticator, Credential) {
	t.Helper()
	a := newAuth(t)
	c, err := Register(regInput(a.Create(chal(9))))
	if err != nil {
		t.Fatal(err)
	}
	return a, c
}

func assertion(t testing.TB, a *webauthntest.Authenticator, ch []byte) AssertionInput {
	t.Helper()
	as, err := a.Get(ch)
	if err != nil {
		t.Fatal(err)
	}
	return AssertionInput{
		CredentialID:      as.CredentialID,
		ClientDataJSON:    as.ClientDataJSON,
		AuthenticatorData: as.AuthenticatorData,
		Signature:         as.Signature,
	}
}

// signed returns an assertion of clientData and authData, signed by a.
func signed(t testing.TB, a *webauthntest.Authenticator, cd, ad []byte) AssertionInput {
	t.Helper()
	sig, err := a.Sign(ad, cd)
	if err != nil {
		t.Fatal(err)
	}
	return AssertionInput{CredentialID: a.CredentialID, ClientDataJSON: cd, AuthenticatorData: ad, Signature: sig}
}

func wantErr(t *testing.T, name string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Errorf("%s: got %v, want %v", name, got, want)
	}
}

func clone(b []byte) []byte { return append([]byte(nil), b...) }
