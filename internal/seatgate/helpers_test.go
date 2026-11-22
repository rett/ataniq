package seatgate

import (
	"crypto/rand"
	"testing"
	"time"
)

// newID makes an identity for a test.
func newID(t testing.TB, name string) Identity {
	t.Helper()
	certPEM, keyPEM, err := GenerateIdentity(rand.Reader, time.Now(), name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ParseIdentity(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
