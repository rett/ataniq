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
	if _, err := parseHomeMutation("claim", malformed); !errors.Is(err, ErrRefused) {
		t.Fatalf("invalid session pin parsed: %v", err)
	}
}
