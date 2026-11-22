package seatapp

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// vaultAAGUID is an AAGUID a password vault claims.
var vaultAAGUID = [16]byte{0xd5, 0x48, 0x82, 0x6e, 0x79, 0xb4, 0xdb, 0x40, 0xa3, 0xd8, 0x11, 0x11, 0x6f, 0x7e, 0x83, 0x49}

// The registration's authenticator data reaches the daemon port whole, with
// the relying party id it was checked against and what the app read in it:
// the AAGUID, the BE, BS and UV flags and the sign counter.
func TestPairKeepsTheRegistrationAndItsClaims(t *testing.T) {
	for _, c := range []struct {
		name   string
		flags  byte
		aaguid [16]byte
		count  uint32
		be, bs bool
	}{
		{"a synced vault passkey", uvUPAT | flagBE | flagBS, vaultAAGUID, 0, true, true},
		{"eligible, not backed up", uvUPAT | flagBE, vaultAAGUID, 0, true, false},
		{"a key with a counter and no AAGUID", uvUPAT, [16]byte{}, 0x01020304, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			a := newAuthenticator(t)
			body := a.register(t, r.getChallenge(t), func(x *registration) {
				x.Flags, x.AAGUID, x.SignCount = c.flags, c.aaguid, c.count
			})
			if w := r.postPair(body); w.Code != 200 {
				t.Fatalf("pair: %d %s", w.Code, w.Body)
			}
			calls := r.daemon.requests()
			if len(calls) != 1 {
				t.Fatalf("daemon calls: %d", len(calls))
			}
			got := calls[0]
			ad, _ := base64.RawURLEncoding.DecodeString(body["authenticator_data"])
			if !bytes.Equal(got.AuthenticatorData, ad) || got.RPID != testRPID {
				t.Errorf("the registration was not passed on: %d bytes for %q", len(got.AuthenticatorData), got.RPID)
			}
			if got.AAGUID != c.aaguid || got.BackupEligible != c.be || got.BackupState != c.bs || !got.UserVerified || got.SignCount != c.count {
				t.Errorf("the claims: %+v", got)
			}
		})
	}
}

// The app takes no registration larger than the daemon's parser reads.
func TestPairRefusesAnOversizedRegistration(t *testing.T) {
	r := newRig(t)
	a := newAuthenticator(t)
	// An extensions map of one text key and a byte string makes the data
	// longer than the daemon takes while every other part holds.
	pad := 4096 - (32 + 1 + 4 + 16 + 2 + len(a.credID) + len(a.cose()) + 6) + 1
	ext := append([]byte{0xa1, 0x61, 'x', 0x59, byte(pad >> 8), byte(pad)}, make([]byte, pad)...)
	body := a.register(t, r.getChallenge(t), func(x *registration) {
		x.Flags = uvUPAT | flagED
		x.COSE = append(a.cose(), ext...)
	})
	wantError(t, "a registration over 4096 bytes", r.postPair(body), 400, "authenticator_data_invalid")
	if n := len(r.daemon.requests()); n != 0 {
		t.Fatalf("%d calls reached the daemon", n)
	}
	// One byte less is taken.
	pad--
	ext = append([]byte{0xa1, 0x61, 'x', 0x59, byte(pad >> 8), byte(pad)}, make([]byte, pad)...)
	body = a.register(t, r.getChallenge(t), func(x *registration) {
		x.Flags = uvUPAT | flagED
		x.COSE = append(a.cose(), ext...)
	})
	if w := r.postPair(body); w.Code != 200 {
		t.Fatalf("a registration of 4096 bytes: %d %s", w.Code, w.Body)
	}
	if calls := r.daemon.requests(); len(calls) != 1 || len(calls[0].AuthenticatorData) != 4096 {
		t.Fatalf("%d calls", len(calls))
	}
}
