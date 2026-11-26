package checkpoint

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func TestParseClaimValidatesSessionPin(t *testing.T) {
	body := vectors(t)["claim"].Body
	identity := make([]byte, 32)
	identity[0] = 1
	malformed := bytes.Replace(body, []byte(base64.RawURLEncoding.EncodeToString(public(fixtureKey("session")))), []byte(base64.RawURLEncoding.EncodeToString(identity)), 1)
	if _, err := ParseIntent("claim", "", malformed); !errors.Is(err, ErrRefused) {
		t.Fatalf("invalid session pin parsed: %v", err)
	}
}

func TestClaimSessionSeparation(t *testing.T) {
	f := newSetup(t)
	home, witness := public(f.home), public(f.witness)
	for _, pin := range [][]byte{home, witness} {
		if err := checkSessionRole(pin, home, witness); !errors.Is(err, ErrRefused) {
			t.Fatalf("accepted overlapping roles: %v", err)
		}
	}
	requireOK(t, checkSessionRole(public(f.session), home, witness))
}
