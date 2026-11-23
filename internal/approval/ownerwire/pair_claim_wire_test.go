package ownerwire

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

const sampleAAGUID = "d548826e-79b4-db40-a3d8-11116f7e8349"

// sampleRegisteredParams is a seat.pair with the registration's
// authenticator data and the relying party id.
func sampleRegisteredParams() SeatPairParams {
	p := sampleSeatPairParams()
	p.AuthenticatorData = bytes.Repeat([]byte{0x49}, 164)
	p.RPID = "seat.example.test"
	return p
}

func sampleClaimedStatus() PairStatus {
	st := samplePairStatus()
	st.Kind, st.AAGUID, st.BackupEligible, st.BackupState = "vault-passkey", sampleAAGUID, true, true
	return st
}

func TestSeatPairParamsCarryTheRegistration(t *testing.T) {
	p := sampleRegisteredParams()
	b, err := EncodeSeatPairParams(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"rp_id":"seat.example.test"`) || !strings.Contains(string(b), `"authenticator_data":"`) {
		t.Fatalf("encoded %s", b)
	}
	got, err := DecodeSeatPairParams(b)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("decoded %v, %v", got, err)
	}
	// The params of a seat that relays no registration still read, with
	// neither member.
	plain, err := EncodeSeatPairParams(sampleSeatPairParams())
	if err != nil || strings.Contains(string(plain), "rp_id") || strings.Contains(string(plain), "authenticator_data") {
		t.Fatalf("%s %v", plain, err)
	}
	// The longest of everything fits the params' cap.
	full := sampleRegisteredParams()
	full.CredentialID = make([]byte, MaxCredentialIDBytes)
	full.PublicKey = make([]byte, MaxPublicKeyBytes)
	full.AuthenticatorData = make([]byte, MaxRegistrationDataBytes)
	full.RPID = strings.Repeat("a", MaxRPIDBytes)
	if _, err := EncodeSeatPairParams(full); err != nil {
		t.Errorf("the longest params are refused: %v", err)
	}

	change := func(edit func(*SeatPairParams)) SeatPairParams {
		p := sampleRegisteredParams()
		edit(&p)
		return p
	}
	for name, p := range map[string]SeatPairParams{
		"data without a relying party": change(func(p *SeatPairParams) { p.RPID = "" }),
		"a relying party without data": change(func(p *SeatPairParams) { p.AuthenticatorData = nil }),
		"data too short":               change(func(p *SeatPairParams) { p.AuthenticatorData = p.AuthenticatorData[:MinRegistrationDataBytes-1] }),
		"data too long":                change(func(p *SeatPairParams) { p.AuthenticatorData = make([]byte, MaxRegistrationDataBytes+1) }),
		"a relying party in capitals":  change(func(p *SeatPairParams) { p.RPID = "Seat.example.test" }),
		"a relying party with a port":  change(func(p *SeatPairParams) { p.RPID = "seat.example.test:443" }),
		"a relying party as an origin": change(func(p *SeatPairParams) { p.RPID = "https://seat.example.test" }),
		"a long relying party":         change(func(p *SeatPairParams) { p.RPID = strings.Repeat("a", MaxRPIDBytes+1) }),
	} {
		_, err := EncodeSeatPairParams(p)
		refused(t, err, record.CodeRecordInvalid, name)
	}
	for name, in := range map[string]string{
		"an empty relying party": `{"code":"` + sampleCode + `","credential_id":"` + strings.Repeat("AAAA", 6) + `","public_key":"AAAA","rp_id":""}`,
		"empty data":             `{"code":"` + sampleCode + `","credential_id":"` + strings.Repeat("AAAA", 6) + `","public_key":"AAAA","authenticator_data":""}`,
	} {
		_, err := DecodeSeatPairParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

func TestAPendingStatusCarriesTheClaim(t *testing.T) {
	st := sampleClaimedStatus()
	b, err := EncodePairStatus(st)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePairStatus(b)
	if err != nil || !reflect.DeepEqual(got, st) {
		t.Fatalf("decoded %+v, %v", got, err)
	}
	// A kind alone, with no AAGUID and no backup flag, is a claim too.
	alone := samplePairStatus()
	alone.Kind = "passkey"
	if _, err := EncodePairStatus(alone); err != nil {
		t.Fatalf("a kind alone: %v", err)
	}
	// The status of a pairing that relayed no claim is as it was.
	if b, err := EncodePairStatus(samplePairStatus()); err != nil || strings.Contains(string(b), "kind") {
		t.Fatalf("%s %v", b, err)
	}

	change := func(edit func(*PairStatus)) PairStatus {
		st := sampleClaimedStatus()
		edit(&st)
		return st
	}
	for name, st := range map[string]PairStatus{
		"a claim while waiting": change(func(s *PairStatus) {
			s.Phase, s.Words, s.PendingID = PairWaiting, nil, ""
		}),
		"a backup flag while expired":  {Phase: PairExpired, ExpiresAt: sampleDay, BackupEligible: true},
		"a kind for no pairing":        {Phase: PairNone, Kind: "passkey"},
		"an AAGUID with no kind":       change(func(s *PairStatus) { s.Kind = "" }),
		"a backup flag with no kind":   change(func(s *PairStatus) { s.Kind, s.AAGUID = "", "" }),
		"a backup state, not eligible": change(func(s *PairStatus) { s.BackupEligible = false }),
		"a kind with a capital":        change(func(s *PairStatus) { s.Kind = "Vault-passkey" }),
		"an AAGUID in capitals":        change(func(s *PairStatus) { s.AAGUID = strings.ToUpper(s.AAGUID) }),
		"an AAGUID with no dashes":     change(func(s *PairStatus) { s.AAGUID = strings.ReplaceAll(s.AAGUID, "-", "") }),
		"an AAGUID that is all zero":   change(func(s *PairStatus) { s.AAGUID = "00000000-0000-0000-0000-000000000000" }),
		"an AAGUID cut short":          change(func(s *PairStatus) { s.AAGUID = s.AAGUID[:35] }),
		"an AAGUID with a letter":      change(func(s *PairStatus) { s.AAGUID = "g" + s.AAGUID[1:] }),
	} {
		_, err := EncodePairStatus(st)
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

func TestAKeyCarriesTheClaim(t *testing.T) {
	k := sampleKeyItem()
	k.Kind, k.AAGUID, k.BackupEligible = "vault-passkey", sampleAAGUID, true
	b, err := EncodeKeyItem(k)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeKeyItem(b); err != nil || got != k {
		t.Fatalf("decoded %+v, %v", got, err)
	}
	for name, edit := range map[string]func(*KeyItem){
		"a backup state, not eligible": func(k *KeyItem) { k.BackupEligible, k.BackupState = false, true },
		"an AAGUID in capitals":        func(k *KeyItem) { k.AAGUID = strings.ToUpper(sampleAAGUID) },
		"an AAGUID that is all zero":   func(k *KeyItem) { k.AAGUID = "00000000-0000-0000-0000-000000000000" },
	} {
		bad := k
		edit(&bad)
		_, err := EncodeKeyItem(bad)
		refused(t, err, record.CodeRecordInvalid, name)
		_, err = EncodeKeysView(KeysView{Keys: []KeyItem{bad}})
		refused(t, err, record.CodeRecordInvalid, "a list with "+name)
	}
	// A key of the list written before the claims reads as it did.
	if _, err := DecodeKeyItem([]byte(`{"id":"0123456789abcdef","kind":"unverified","paired_at":"` + sampleDay + `","revoked":false}`)); err != nil {
		t.Fatalf("a key without claims: %v", err)
	}
}
