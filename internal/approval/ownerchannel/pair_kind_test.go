package ownerchannel

import (
	"context"
	"errors"
	"testing"

	"ataniq/internal/approval/credkind"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/webauthntest"
)

const (
	kindRPID   = "seat.example.test"
	kindOrigin = "https://seat.example.test"
)

// The AAGUIDs of credkind's table that the tests claim.
var (
	vaultAAGUID    = [16]byte{0xd5, 0x48, 0x82, 0x6e, 0x79, 0xb4, 0xdb, 0x40, 0xa3, 0xd8, 0x11, 0x11, 0x6f, 0x7e, 0x83, 0x49}
	platformAAGUID = [16]byte{0xfb, 0xfc, 0x30, 0x07, 0x15, 0x4e, 0x4e, 0xcc, 0x8c, 0x0b, 0x6e, 0x02, 0x05, 0x57, 0xd7, 0xbd}
)

// authenticator is a software authenticator for the seat's relying party.
func authenticator(t *testing.T) *webauthntest.Authenticator {
	t.Helper()
	a, err := webauthntest.New(kindRPID, kindOrigin)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// registered is the seat.pair of a credential with its registration.
func registered(code string, a *webauthntest.Authenticator) ownerwire.SeatPairParams {
	return ownerwire.SeatPairParams{
		Code: code, CredentialID: a.CredentialID, PublicKey: a.SPKI(),
		AuthenticatorData: a.RegistrationAuthData(), RPID: kindRPID,
	}
}

func TestTheKindIsRecordedAtPairing(t *testing.T) {
	for _, c := range []struct {
		name   string
		aaguid [16]byte
		flags  byte
		count  uint32
		kind   string
		wantID string
		be, bs bool
	}{
		{"a synced vault passkey", vaultAAGUID, webauthntest.FlagBE | webauthntest.FlagBS, 0, credkind.NameVault, "d548826e-79b4-db40-a3d8-11116f7e8349", true, true},
		{"a platform passkey not backed up yet", platformAAGUID, webauthntest.FlagBE, 0, credkind.NamePlatform, "fbfc3007-154e-4ecc-8c0b-6e020557d7bd", true, false},
		{"a key with a counter and no AAGUID", [16]byte{}, 0, 41, credkind.NameUnknown, "", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, keys := pairEnv(t)
			a := authenticator(t)
			a.AAGUID, a.SignCount = c.aaguid, c.count
			a.Flags |= c.flags
			code := e.mustStart()
			if _, err := e.svc.PairPresent(context.Background(), seatSrc, registered(code, a)); err != nil {
				t.Fatalf("PairPresent: %v", err)
			}
			st := e.status()
			if st.Phase != ownerwire.PairPending || st.Kind != c.kind || st.AAGUID != c.wantID ||
				st.BackupEligible != c.be || st.BackupState != c.bs {
				t.Fatalf("the pending status: %+v", st)
			}
			item, err := e.confirm()
			if err != nil {
				t.Fatal(err)
			}
			if item.Kind != c.kind || item.AAGUID != c.wantID || item.BackupEligible != c.be || item.BackupState != c.bs {
				t.Fatalf("the key: %+v", item)
			}
			if _, err := ownerwire.EncodeKeyItem(item); err != nil {
				t.Fatal(err)
			}
			stored, _ := keys.Keys()
			if len(stored) != 1 {
				t.Fatalf("%+v", stored)
			}
			s := stored[0]
			if s.Kind != c.kind || s.AAGUID != c.aaguid || s.BackupEligible != c.be || s.BackupState != c.bs || s.Counter != c.count {
				t.Fatalf("the stored key: %+v", s)
			}
			// The list shows the key as the confirm did.
			if view, err := e.svc.Keys(context.Background()); err != nil || len(view.Keys) != 1 || view.Keys[0] != item {
				t.Fatalf("%+v %v", view, err)
			}
			// What is stored is what the verifier is handed: a key of a kind it
			// knows, with the backup eligibility of the pairing.
			if _, ok := credkind.ByName(s.Kind); !ok {
				t.Fatalf("the stored kind %q is not a kind of credkind", s.Kind)
			}
		})
	}
}

// A seat that relays no registration pairs a credential of the kind
// "unverified", as before, and the status shows no claim.
func TestAPairingWithoutTheRegistrationIsUnverified(t *testing.T) {
	e, keys := pairEnv(t)
	e.pairedUp(newPhone(t, 1))
	if st := e.status(); st.Kind != "" || st.AAGUID != "" || st.BackupEligible || st.BackupState {
		t.Fatalf("a claim with no registration: %+v", st)
	}
	item, err := e.confirm()
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != "unverified" || item.AAGUID != "" || item.BackupEligible {
		t.Fatalf("%+v", item)
	}
	if stored, _ := keys.Keys(); stored[0].Kind != "unverified" || stored[0].Counter != 0 {
		t.Fatalf("%+v", stored)
	}
}

// A registration that does not hold is refused before the code is looked at:
// the code is not spent and no try is counted.
func TestARegistrationThatDoesNotHoldIsRefusedAndTheCodeKept(t *testing.T) {
	e, keys := pairEnv(t)
	code := e.mustStart()
	a := authenticator(t)
	other := authenticator(t)
	for name, edit := range map[string]func(*ownerwire.SeatPairParams){
		"another relying party": func(p *ownerwire.SeatPairParams) { p.RPID = "other.example.test" },
		"no user verification": func(p *ownerwire.SeatPairParams) {
			b := authenticator(t)
			b.CredentialID, b.Key = a.CredentialID, a.Key
			b.Flags = webauthntest.FlagUP
			p.AuthenticatorData = b.RegistrationAuthData()
		},
		"another credential's data": func(p *ownerwire.SeatPairParams) { p.AuthenticatorData = other.RegistrationAuthData() },
		"another key":               func(p *ownerwire.SeatPairParams) { p.PublicKey = other.SPKI() },
		"another credential id": func(p *ownerwire.SeatPairParams) {
			p.CredentialID = append([]byte(nil), a.CredentialID...)
			p.CredentialID[0] ^= 0xff
		},
		"an assertion's data": func(p *ownerwire.SeatPairParams) {
			p.AuthenticatorData = append(a.AssertionAuthData(), make([]byte, 20)...)
		},
		"data cut short": func(p *ownerwire.SeatPairParams) {
			p.AuthenticatorData = p.AuthenticatorData[:len(p.AuthenticatorData)-1]
		},
		"a backup state, not BE": func(p *ownerwire.SeatPairParams) { p.AuthenticatorData[32] |= webauthntest.FlagBS },
		"a byte after the key":   func(p *ownerwire.SeatPairParams) { p.AuthenticatorData = append(p.AuthenticatorData, 0) },
		"a wrong code, bad data, too": func(p *ownerwire.SeatPairParams) {
			p.Code = "ZZZZZZZZZZZZZZZZZZZZ"
			p.RPID = "other.example.test"
		},
	} {
		p := registered(code, a)
		edit(&p)
		if _, err := e.svc.PairPresent(context.Background(), seatSrc, p); !errors.Is(err, ErrCredentialInvalid) {
			t.Fatalf("%s: %v, want %v", name, err, ErrCredentialInvalid)
		}
		if st := e.status(); st.Phase != ownerwire.PairWaiting {
			t.Fatalf("%s: the pairing is %+v", name, st)
		}
	}
	// More refusals than the code has tries, and the code still pairs.
	if _, err := e.svc.PairPresent(context.Background(), seatSrc, registered(code, a)); err != nil {
		t.Fatalf("the right registration after the refused ones: %v", err)
	}
	if st := e.status(); st.Kind != credkind.NameUnknown {
		t.Fatalf("%+v", st)
	}
	if stored, _ := keys.Keys(); len(stored) != 0 {
		t.Fatalf("%+v", stored)
	}
}

// The claim belongs to the credential it came with: a pairing that ends or is
// replaced takes it away, and a later credential presented without a
// registration shows none.
func TestTheClaimEndsWithItsPairing(t *testing.T) {
	e, keys := pairEnv(t)
	a := authenticator(t)
	a.AAGUID, a.Flags = vaultAAGUID, a.Flags|webauthntest.FlagBE|webauthntest.FlagBS
	if _, err := e.svc.PairPresent(context.Background(), seatSrc, registered(e.mustStart(), a)); err != nil {
		t.Fatal(err)
	}
	if err := e.reject(); err != nil {
		t.Fatal(err)
	}
	e.pairedUp(newPhone(t, 2))
	if st := e.status(); st.Kind != "" || st.BackupEligible {
		t.Fatalf("after a rejection: %+v", st)
	}

	// Replaced while pending.
	b := authenticator(t)
	b.AAGUID = vaultAAGUID
	r, err := e.pairStart(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PairPresent(context.Background(), seatSrc, registered(r.Code, b)); err != nil {
		t.Fatal(err)
	}
	r, err = e.pairStart(true)
	if err != nil {
		t.Fatal(err)
	}
	ph := newPhone(t, 3)
	if _, err := e.present(r.Code, ph); err != nil {
		t.Fatal(err)
	}
	if st := e.status(); st.Kind != "" {
		t.Fatalf("after a replacement: %+v", st)
	}
	item, err := e.confirm()
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != "unverified" || item.ID != ph.displayID() {
		t.Fatalf("%+v", item)
	}
	if stored, _ := keys.Keys(); len(stored) != 1 || stored[0].AAGUID != ([16]byte{}) {
		t.Fatalf("%+v", stored)
	}
}

// A key on the list without claims, as one paired before them, lists as it
// did.
func TestAKeyWithoutClaimsListsAsBefore(t *testing.T) {
	e, keys := pairEnv(t)
	keys.seed("0123456789abcdef", true)
	view, err := e.svc.Keys(context.Background())
	if err != nil || len(view.Keys) != 1 {
		t.Fatalf("%+v %v", view, err)
	}
	want := ownerwire.KeyItem{ID: "0123456789abcdef", Kind: "unverified", PairedAt: "2026-11-19T08:00:00Z", Revoked: true}
	if view.Keys[0] != want {
		t.Fatalf("%+v, want %+v", view.Keys[0], want)
	}
}
