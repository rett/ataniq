package credkind

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// maxClientData bounds the client data JSON; a browser's is a few hundred
// bytes.
const maxClientData = 4096

// clientDataMembers parses the client data strictly: valid UTF-8, one JSON
// object and nothing after it, no member named twice. It returns the members
// as raw JSON.
func clientDataMembers(raw []byte) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || len(raw) > maxClientData || !utf8.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	m := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := tok.(string)
		if !ok {
			return nil, false
		}
		if _, dup := m[key]; dup {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		m[key] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return m, true
}

// jsonString returns the member key when it is present and a JSON string.
func jsonString(m map[string]json.RawMessage, key string) (string, bool) {
	v, ok := m[key]
	if !ok || len(v) == 0 || v[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}

// checkClientData checks the client data of a ceremony of type typ against
// what the relying party expects, in the order of WebAuthn level 3, section
// 7.2, steps 10 to 14 (section 7.1 for a registration).
func checkClientData(raw []byte, typ string, e Expect) error {
	m, ok := clientDataMembers(raw)
	if !ok {
		return ErrClientData
	}
	t, ok := jsonString(m, "type")
	if !ok {
		return ErrClientData
	}
	if t != typ {
		return ErrType
	}
	ch, ok := jsonString(m, "challenge")
	if !ok {
		return ErrClientData
	}
	want := base64.RawURLEncoding.EncodeToString(e.Challenge)
	if subtle.ConstantTimeCompare([]byte(ch), []byte(want)) != 1 {
		return ErrChallenge
	}
	origin, ok := jsonString(m, "origin")
	if !ok {
		return ErrClientData
	}
	if subtle.ConstantTimeCompare([]byte(origin), []byte(e.Origin)) != 1 {
		return ErrOrigin
	}
	// crossOrigin is absent or the JSON false; topOrigin is there only when
	// the page was in another site's frame.
	if v, present := m["crossOrigin"]; present && string(v) != "false" {
		return ErrCrossOrigin
	}
	if _, present := m["topOrigin"]; present {
		return ErrCrossOrigin
	}
	return nil
}
