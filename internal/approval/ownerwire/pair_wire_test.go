package ownerwire

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

// The samples of the pairing and key forms. The code is a made-up one.
const sampleCode = "7ZB0KX4M9TQ2VH6NRC3E"

func sampleWords() []string {
	return []string{"acorn", "bridge", "cedar", "delta", "ember", "falcon"}
}

func samplePairStartResult() PairStartResult {
	return PairStartResult{Code: sampleCode, ExpiresAt: sampleDay}
}

func samplePairStatus() PairStatus {
	return PairStatus{Phase: PairPending, Words: sampleWords(), ExpiresAt: sampleDay}
}

func sampleSeatPairParams() SeatPairParams {
	return SeatPairParams{Code: sampleCode, CredentialID: bytes.Repeat([]byte{0x41}, 32), PublicKey: bytes.Repeat([]byte{0x30}, 91)}
}

func sampleKeyItem() KeyItem {
	return KeyItem{ID: "0123456789abcdef", Kind: "unverified", PairedAt: sampleDay}
}

func sampleKeysView() KeysView {
	return KeysView{Keys: []KeyItem{
		sampleKeyItem(),
		{ID: "fedcba9876543210", Kind: "unverified", PairedAt: "2026-11-21T08:00:00Z", Revoked: true},
	}}
}

func TestPairStartParamsAreOneBool(t *testing.T) {
	p, err := DecodePairStartParams([]byte(`{"replace":true}`))
	if err != nil || !p.Replace {
		t.Fatalf("decoded %+v, %v", p, err)
	}
	for name, in := range map[string]string{
		"no field":       `{}`,
		"a number":       `{"replace":1}`,
		"a word":         `{"replace":"yes"}`,
		"another field":  `{"replace":false,"code":"x"}`,
		"a null":         `{"replace":null}`,
		"a capital":      `{"Replace":true}`,
		"a list instead": `[true]`,
	} {
		_, err := DecodePairStartParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

func TestPairStartResultCode(t *testing.T) {
	for name, code := range map[string]string{
		"empty":          "",
		"too short":      sampleCode[:19],
		"too long":       sampleCode + "0",
		"an I":           "7ZB0KX4M9TQ2VH6NRC3I",
		"an L":           "7ZB0KX4M9TQ2VH6NRC3l",
		"an O":           "7ZB0KX4M9TQ2VH6NRC3O",
		"a U":            "7ZB0KX4M9TQ2VH6NRC3u",
		"a dash":         "7ZB0KX4M9TQ2VH6NRC-E",
		"a space":        "7ZB0KX4M9TQ2VH6NRC E",
		"a non-ASCII":    "7ZB0KX4M9TQ2VH6NRCé",
		"a control byte": "7ZB0KX4M9TQ2VH6NRC\x01",
	} {
		r := PairStartResult{Code: code, ExpiresAt: sampleDay}
		_, err := EncodePairStartResult(r)
		refused(t, err, record.CodeRecordInvalid, "encode "+name)
		in := `{"code":` + fmt.Sprintf("%q", code) + `,"expires_at":"` + sampleDay + `"}`
		_, err = DecodePairStartResult([]byte(in))
		if err == nil {
			t.Errorf("decode %s: accepted", name)
		}
	}
	// Either case is a code, the code is read in upper case by the daemon.
	lower := PairStartResult{Code: strings.ToLower(sampleCode), ExpiresAt: sampleDay}
	if _, err := EncodePairStartResult(lower); err != nil {
		t.Errorf("a code in lower case is refused: %v", err)
	}
	_, err := EncodePairStartResult(PairStartResult{Code: sampleCode, ExpiresAt: "tomorrow"})
	refused(t, err, record.CodeRecordInvalid, "a time that is not a record's")
}

// The code is in the result of owner.pair-start and in the params of
// seat.pair, and a value of either prints without it.
func TestAPairingCodeDoesNotPrint(t *testing.T) {
	r := samplePairStartResult()
	p := sampleSeatPairParams()
	for name, v := range map[string]any{"result": r, "result pointer": &r, "params": p, "params pointer": &p} {
		for _, verb := range []string{"%v", "%+v", "%s", "%#v", "%q"} {
			if out := fmt.Sprintf(verb, v); strings.Contains(out, sampleCode) || strings.Contains(out, "QUFB") {
				t.Errorf("%s with %s prints the secret: %s", name, verb, out)
			}
		}
	}
}

func TestPairStatusPhases(t *testing.T) {
	ok := []PairStatus{
		{Phase: PairNone},
		{Phase: PairWaiting, ExpiresAt: sampleDay},
		{Phase: PairPending, Words: sampleWords(), ExpiresAt: sampleDay},
		{Phase: PairExpired, ExpiresAt: sampleDay},
	}
	for _, s := range ok {
		b, err := EncodePairStatus(s)
		if err != nil {
			t.Errorf("%s: %v", s.Phase, err)
			continue
		}
		if got, err := DecodePairStatus(b); err != nil || got.Phase != s.Phase || len(got.Words) != len(s.Words) {
			t.Errorf("%s: decoded %+v, %v", s.Phase, got, err)
		}
	}
	bad := map[string]PairStatus{
		"no phase":                {},
		"another phase":           {Phase: "open", ExpiresAt: sampleDay},
		"none with a time":        {Phase: PairNone, ExpiresAt: sampleDay},
		"none with words":         {Phase: PairNone, Words: sampleWords()},
		"waiting with words":      {Phase: PairWaiting, Words: sampleWords(), ExpiresAt: sampleDay},
		"waiting with no time":    {Phase: PairWaiting},
		"expired with words":      {Phase: PairExpired, Words: sampleWords(), ExpiresAt: sampleDay},
		"pending with no words":   {Phase: PairPending, ExpiresAt: sampleDay},
		"pending with five words": {Phase: PairPending, Words: sampleWords()[:5], ExpiresAt: sampleDay},
		"pending with seven":      {Phase: PairPending, Words: append(sampleWords(), "garden"), ExpiresAt: sampleDay},
		"pending with no time":    {Phase: PairPending, Words: sampleWords()},
		"an empty list of words":  {Phase: PairWaiting, Words: []string{}, ExpiresAt: sampleDay},
		"a capital in a word":     {Phase: PairPending, Words: []string{"Acorn", "bridge", "cedar", "delta", "ember", "falcon"}, ExpiresAt: sampleDay},
		"a short word":            {Phase: PairPending, Words: []string{"ab", "bridge", "cedar", "delta", "ember", "falcon"}, ExpiresAt: sampleDay},
		"a long word":             {Phase: PairPending, Words: []string{"acornss", "bridge", "cedar", "delta", "ember", "falcon"}, ExpiresAt: sampleDay},
		"a digit in a word":       {Phase: PairPending, Words: []string{"acor1", "bridge", "cedar", "delta", "ember", "falcon"}, ExpiresAt: sampleDay},
	}
	for name, s := range bad {
		_, err := EncodePairStatus(s)
		refused(t, err, record.CodeRecordInvalid, "encode "+name)
	}
	// The decoder holds the same rules, also for a spelling the encoder
	// does not write.
	for name, in := range map[string]string{
		"an empty list for words": `{"phase":"waiting","words":[],"expires_at":"` + sampleDay + `"}`,
		"a code":                  `{"phase":"waiting","code":"` + sampleCode + `","expires_at":"` + sampleDay + `"}`,
	} {
		_, err := DecodePairStatus([]byte(in))
		refused(t, err, record.CodeRecordInvalid, "decode "+name)
	}
}

func TestSeatPairParamsContent(t *testing.T) {
	change := func(edit func(*SeatPairParams)) SeatPairParams {
		p := sampleSeatPairParams()
		edit(&p)
		return p
	}
	for name, p := range map[string]SeatPairParams{
		"no code":              change(func(p *SeatPairParams) { p.Code = "" }),
		"a short code":         change(func(p *SeatPairParams) { p.Code = p.Code[:10] }),
		"an I in the code":     change(func(p *SeatPairParams) { p.Code = "I" + p.Code[1:] }),
		"a credential id of 0": change(func(p *SeatPairParams) { p.CredentialID = nil }),
		"a short id":           change(func(p *SeatPairParams) { p.CredentialID = p.CredentialID[:15] }),
		"a long id":            change(func(p *SeatPairParams) { p.CredentialID = make([]byte, MaxCredentialIDBytes+1) }),
		"no key":               change(func(p *SeatPairParams) { p.PublicKey = nil }),
		"a long key":           change(func(p *SeatPairParams) { p.PublicKey = make([]byte, MaxPublicKeyBytes+1) }),
	} {
		_, err := EncodeSeatPairParams(p)
		refused(t, err, record.CodeRecordInvalid, name)
	}
	// The longest of each fits the params' cap.
	full := change(func(p *SeatPairParams) {
		p.CredentialID = make([]byte, MaxCredentialIDBytes)
		p.PublicKey = make([]byte, MaxPublicKeyBytes)
	})
	if _, err := EncodeSeatPairParams(full); err != nil {
		t.Errorf("the longest id and key are refused: %v", err)
	}
	for name, in := range map[string]string{
		"bytes not in base64":   `{"code":"` + sampleCode + `","credential_id":"!!","public_key":"AAAA"}`,
		"a url alphabet":        `{"code":"` + sampleCode + `","credential_id":"` + strings.Repeat("-_", 12) + `","public_key":"AAAA"}`,
		"a list of bytes":       `{"code":"` + sampleCode + `","credential_id":[1,2],"public_key":"AAAA"}`,
		"a missing credential":  `{"code":"` + sampleCode + `","public_key":"AAAA"}`,
		"a missing key":         `{"code":"` + sampleCode + `","credential_id":"` + strings.Repeat("AAAA", 6) + `"}`,
		"a field of the daemon": `{"code":"` + sampleCode + `","credential_id":"` + strings.Repeat("AAAA", 6) + `","public_key":"AAAA","kind":"unverified"}`,
	} {
		_, err := DecodeSeatPairParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

func TestSeatPairResultIsSixWords(t *testing.T) {
	for name, w := range map[string][]string{
		"none": nil, "five": sampleWords()[:5], "seven": append(sampleWords(), "garden"),
		"a word with a capital": {"acorn", "bridge", "cedar", "delta", "ember", "Falcon"},
	} {
		_, err := EncodeSeatPairResult(SeatPairResult{Words: w})
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

func TestKeyContent(t *testing.T) {
	change := func(edit func(*KeysView)) KeysView {
		v := sampleKeysView()
		edit(&v)
		return v
	}
	for name, v := range map[string]KeysView{
		"a short id":            change(func(v *KeysView) { v.Keys[0].ID = "0123" }),
		"a capital in an id":    change(func(v *KeysView) { v.Keys[0].ID = "0123456789ABCDEF" }),
		"a non-hex id":          change(func(v *KeysView) { v.Keys[0].ID = "0123456789abcdeg" }),
		"no kind":               change(func(v *KeysView) { v.Keys[0].Kind = "" }),
		"a kind with a capital": change(func(v *KeysView) { v.Keys[0].Kind = "Unverified" }),
		"a kind with a space":   change(func(v *KeysView) { v.Keys[0].Kind = "hardware token" }),
		"a long kind":           change(func(v *KeysView) { v.Keys[0].Kind = strings.Repeat("a", 33) }),
		"a time with an offset": change(func(v *KeysView) { v.Keys[0].PairedAt = "2026-11-20T14:06:40+00:00" }),
		"no time":               change(func(v *KeysView) { v.Keys[0].PairedAt = "" }),
		"an id twice":           change(func(v *KeysView) { v.Keys[1].ID = v.Keys[0].ID }),
		"too many keys": change(func(v *KeysView) {
			v.Keys = nil
			for i := 0; i <= MaxKeys; i++ {
				v.Keys = append(v.Keys, KeyItem{ID: fmt.Sprintf("%016x", i), Kind: "unverified", PairedAt: sampleDay})
			}
		}),
	} {
		_, err := EncodeKeysView(v)
		refused(t, err, record.CodeRecordInvalid, name)
	}
	full := change(func(v *KeysView) {
		v.Keys = nil
		for i := 0; i < MaxKeys; i++ {
			v.Keys = append(v.Keys, KeyItem{ID: fmt.Sprintf("%016x", i), Kind: "unverified", PairedAt: sampleDay, Revoked: true})
		}
	})
	if b, err := EncodeKeysView(full); err != nil {
		t.Errorf("a list of %d keys is refused: %v", MaxKeys, err)
	} else if len(b) > MaxResultBytes {
		t.Errorf("a full list is %d bytes, over the cap of %d", len(b), MaxResultBytes)
	}
	// An empty list is a list.
	if b, err := EncodeKeysView(KeysView{}); err != nil || string(b) != `{"keys":null}` && string(b) != `{"keys":[]}` {
		t.Errorf("an empty list encodes to %s, %v", b, err)
	}
	if _, err := DecodeKeysView([]byte(`{"keys":[]}`)); err != nil {
		t.Errorf("an empty list is refused: %v", err)
	}
	for name, in := range map[string]string{
		"a field not named": `{"keys":[],"count":0}`,
		"no list":           `{}`,
		"a key with the credential id": `{"keys":[{"id":"0123456789abcdef","kind":"unverified","paired_at":"` + sampleDay +
			`","revoked":false,"credential_id":"AAAA"}]}`,
		"a key with no revoked mark": `{"keys":[{"id":"0123456789abcdef","kind":"unverified","paired_at":"` + sampleDay + `"}]}`,
	} {
		_, err := DecodeKeysView([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

func TestKeyRevokeParamsTakeAPrefix(t *testing.T) {
	for _, id := range []string{"0123", "01234", "0123456789abcde", "0123456789abcdef"} {
		if _, err := EncodeKeyRevokeParams(KeyRevokeParams{ID: id}); err != nil {
			t.Errorf("%q: %v", id, err)
		}
	}
	for name, id := range map[string]string{
		"empty": "", "three digits": "012", "too long": "0123456789abcdef0", "a capital": "0123456789abcdeF",
		"not hex": "0123xyz", "a space": "0123 4567", "a dash": "0123-4567",
	} {
		_, err := EncodeKeyRevokeParams(KeyRevokeParams{ID: id})
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

// No refusal of the new forms repeats what the payload brought, the pairing
// code least of all.
func TestPairRefusalsHoldNoByteOfThePayload(t *testing.T) {
	const marker = "zq-marker-zq"
	for name, in := range map[string]func() error{
		"a code": func() error {
			_, err := DecodeSeatPairParams([]byte(`{"code":"` + marker + `","credential_id":"AAAA","public_key":"AAAA"}`))
			return err
		},
		"a result with a wrong code": func() error {
			_, err := DecodePairStartResult([]byte(`{"code":"` + marker + `","expires_at":"` + sampleDay + `"}`))
			return err
		},
		"an unknown key": func() error {
			_, err := DecodePairStatus([]byte(`{"` + marker + `":1}`))
			return err
		},
		"a wrong id": func() error {
			_, err := DecodeKeyRevokeParams([]byte(`{"id":"` + marker + `"}`))
			return err
		},
		"a wrong phase": func() error {
			_, err := DecodePairStatus([]byte(`{"phase":"` + marker + `"}`))
			return err
		},
	} {
		err := in()
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "zq") {
			t.Errorf("%s: the refusal repeats the payload: %v", name, err)
		}
	}
	if _, err := DecodeSeatPairParams([]byte(`{"code":"` + sampleCode + `","credential_id":"AAAA","public_key":"AAAA"}`)); err == nil || strings.Contains(err.Error(), sampleCode) {
		t.Errorf("a short credential id: %v", err)
	}
}
