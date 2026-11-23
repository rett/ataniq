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

// within receives from ch, failing the test if nothing comes within d, so
// that a regression fails in seconds rather than at the package timeout.
func within[T any](t *testing.T, what string, ch <-chan T, d time.Duration) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("timed out after %v waiting for %s", d, what)
		var zero T
		return zero
	}
}
