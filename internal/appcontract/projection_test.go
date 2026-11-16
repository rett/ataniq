package appcontract

import (
	"encoding/hex"
	"math"
	"testing"
)

func TestProjectionVectors(t *testing.T) {
	var i Instance
	i.Authority = "a"
	var master, salt, secret [32]byte
	for n := 0; n < 16; n++ {
		i.StoreID[n] = byte(n)
		i.IncarnationID[n] = byte(n + 16)
	}
	for n := 0; n < 32; n++ {
		master[n] = byte(n + 32)
		secret[n] = byte(n + 96)
		salt[n] = byte(n + 128)
	}
	k := NewProjectionKeys(master)
	for _, tc := range []struct {
		got  [32]byte
		want string
	}{
		{k.status, "c19caaff307f31f982187ebff5ab784bc0df8eaa73c7752d1ca500c2e13fdad5"},
		{k.resource, "c7a7120909528747f12884d6ce4f777aef76604cc42104d609480c0edee4fe27"},
		{k.event, "ddf14f73712389cfda0a89d13103a5a48f171b51ee6cd430ba4c327b6f329bc3"},
		{k.cause, "ad1171fdd125811519c3db674a528ed490cb78ddfe894e9a2ec77aff68b4da13"},
	} {
		if hex.EncodeToString(tc.got[:]) != tc.want {
			t.Errorf("key: %x, want %s", tc.got, tc.want)
		}
	}
	event, err := k.EventID(i, "p", "q", 7, StateRefresh)
	if err != nil || event != "0f96dc370f2b9da36e72e1b0b5c41f07bf63efddd39c7ae7c2fe61355cb38b83" {
		t.Fatalf("event: %s, %v", event, err)
	}
	cause, err := k.CauseEventID(i, 7)
	if err != nil || cause != "a55106ccab5c5dc10d01153ce8fec8dba5c3441040b884cf34024dfa88d67705" {
		t.Fatalf("cause: %s, %v", cause, err)
	}
	v, err := CredentialVerifier(i, "c", "k", 1, salt, secret)
	if err != nil || hex.EncodeToString(v[:]) != "e0729361b4d009184d010f6ac765d92e83609fcf23d15ad89b6cf2e819c10987" {
		t.Fatalf("verifier: %x, %v", v, err)
	}
	for _, n := range []uint64{0, math.MaxInt64 + 1} {
		if _, err := k.EventID(i, "p", "q", n, StateRefresh); err == nil {
			t.Errorf("accepted event sequence %d", n)
		}
		if _, err := k.CauseEventID(i, n); err == nil {
			t.Errorf("accepted cause sequence %d", n)
		}
		if _, err := CredentialVerifier(i, "c", "k", n, salt, secret); err == nil {
			t.Errorf("accepted generation %d", n)
		}
	}
	if _, err := k.EventID(i, "p", "q", 1, "unknown"); err == nil {
		t.Error("accepted unknown event kind")
	}
	if _, err := k.EventID(i, "p/invalid", "q", 1, StateRefresh); err == nil {
		t.Error("accepted invalid ID")
	}
}
