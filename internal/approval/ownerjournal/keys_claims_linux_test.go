package ownerjournal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// vaultAAGUID is an AAGUID a vault claims (the tests need any non-zero one).
var vaultAAGUID = [16]byte{0xd5, 0x48, 0x82, 0x6e, 0x79, 0xb4, 0xdb, 0x40, 0xa3, 0xd8, 0x11, 0x11, 0x6f, 0x7e, 0x83, 0x49}

// aClaimedKey is aKey(n) with the claims of a synced vault passkey that
// reported the counter c at the pairing.
func aClaimedKey(n int, c uint32) NewKey {
	k := aKey(n)
	k.Kind = "vault-passkey"
	k.AAGUID = vaultAAGUID
	k.BackupEligible, k.BackupState = true, true
	k.Counter = c
	return k
}

// writeKeyList writes a list of the records given, with the head that goes
// with it, and returns its path and bytes.
func writeKeyList(t *testing.T, recs ...keyRecord) (string, []byte) {
	t.Helper()
	path := newKeyPath(t)
	data := []byte(keysHeader + "\n")
	prev := sha256Of([]byte(keysHeader))
	for _, r := range recs {
		line, sum, err := encodeKeyRecord(prev, r)
		if err != nil {
			t.Fatal(err)
		}
		data, prev = append(data, line...), sum
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	head := fmt.Sprintf("%s %d %x\n", keysHeadHeader, len(recs), prev[:])
	if err := os.WriteFile(headPathOf(path), []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func TestTheClaimsOfAPairingAreKeptAndReadBack(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	got, err := k.AddKey(aClaimedKey(1, 7))
	if err != nil {
		t.Fatal(err)
	}
	plain := mustAdd(t, k, 2)
	check := func(when string, keys []StoredKey) {
		t.Helper()
		if len(keys) != 2 {
			t.Fatalf("%s: %+v", when, keys)
		}
		c := keys[0]
		if c.Kind != "vault-passkey" || c.AAGUID != vaultAAGUID || !c.BackupEligible || !c.BackupState || c.Counter != 7 {
			t.Fatalf("%s: the claimed key reads %+v", when, c)
		}
		p := keys[1]
		if p.Kind != "unverified" || p.AAGUID != ([16]byte{}) || p.BackupEligible || p.BackupState || p.Counter != 0 {
			t.Fatalf("%s: the key without claims reads %+v", when, p)
		}
	}
	if got.AAGUID != vaultAAGUID || got.Counter != 7 || plain.Counter != 0 {
		t.Fatalf("AddKey returned %+v and %+v", got, plain)
	}
	check("open", mustKeys(t, k))
	mustClose2(t, k)
	check("after a reopen", mustKeys(t, mustOpenKeys(t, path)))

	// The record holds the AAGUID in hex, and the credential without claims
	// is written as before, with none of the new members.
	lines := strings.Split(string(fileBytes(t, path)), "\n")
	if !strings.Contains(lines[1], `"aaguid":"d548826e79b4db40a3d811116f7e8349"`) ||
		!strings.Contains(lines[1], `"backup_eligible":true`) || !strings.Contains(lines[1], `"counter":7`) {
		t.Fatalf("the claimed record: %s", lines[1])
	}
	for _, member := range []string{"aaguid", "backup_eligible", "backup_state", "counter"} {
		if strings.Contains(lines[2], member) {
			t.Fatalf("the record without claims holds %s: %s", member, lines[2])
		}
	}
}

// A list written before the claims were kept has added records without them.
// It opens, reads as credentials of the kind "unverified" with no claims, and
// keeps its revocations: nothing is migrated.
func TestAListWithoutClaimsReadsAsUnverifiedAndKeepsItsRevocations(t *testing.T) {
	n1, n2 := aKey(1), aKey(2)
	id1, id2 := KeyDisplayID(n1.CredentialID), KeyDisplayID(n2.CredentialID)
	path, data := writeKeyList(t,
		keyRecord{Seq: 1, Type: keyAdded, At: "2026-11-20T14:00:00Z", ID: id1, Kind: "unverified", CredentialID: n1.CredentialID, PublicKey: n1.PublicKey, Words: keyWords},
		keyRecord{Seq: 2, Type: keyAdded, At: "2026-11-20T14:01:00Z", ID: id2, Kind: "unverified", CredentialID: n2.CredentialID, PublicKey: n2.PublicKey, Words: keyWords},
		keyRecord{Seq: 3, Type: keyRevoked, At: "2026-11-20T15:00:00Z", ID: id1},
	)
	k := mustOpenKeys(t, path)
	keys := mustKeys(t, k)
	if len(keys) != 2 || !keys[0].Revoked || keys[1].Revoked {
		t.Fatalf("the revocation was not kept: %+v", keys)
	}
	for _, s := range keys {
		if s.Kind != "unverified" || s.AAGUID != ([16]byte{}) || s.BackupEligible || s.BackupState || s.Counter != 0 {
			t.Fatalf("a key without claims reads %+v", s)
		}
	}
	if !bytes.Equal(fileBytes(t, path), data) {
		t.Fatal("opening the list changed it")
	}
	// A credential with claims is added to it beside the old ones.
	if _, err := k.AddKey(aClaimedKey(3, 0)); err != nil {
		t.Fatal(err)
	}
	if keys := mustKeys(t, k); len(keys) != 3 || !keys[0].Revoked || keys[2].Kind != "vault-passkey" {
		t.Fatalf("%+v", keys)
	}
}

func TestAKeyWithABackupStateAndNoEligibilityIsNotAdded(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	before := fileBytes(t, path)
	n := aClaimedKey(1, 0)
	n.BackupEligible = false
	if _, err := k.AddKey(n); err == nil {
		t.Fatal("added")
	}
	if !bytes.Equal(fileBytes(t, path), before) {
		t.Fatal("the file was changed")
	}
}

func TestTheCounterIsRaisedAndNeverLowered(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	s, err := k.AddKey(aClaimedKey(1, 5))
	if err != nil {
		t.Fatal(err)
	}
	counter := func(k *KeyFile) uint32 {
		t.Helper()
		return mustKeys(t, k)[0].Counter
	}
	at := base.Add(time.Hour)
	// Not higher: nothing is written.
	for _, c := range []uint32{0, 4, 5} {
		before := fileBytes(t, path)
		if err := k.RaiseCounter(s.ID, c, at); err != nil {
			t.Fatalf("%d: %v", c, err)
		}
		if !bytes.Equal(fileBytes(t, path), before) || counter(k) != 5 {
			t.Fatalf("%d: a counter that is not higher was written", c)
		}
	}
	if err := k.RaiseCounter(s.ID, 9, at); err != nil {
		t.Fatal(err)
	}
	if counter(k) != 9 {
		t.Fatalf("the counter is %d, want 9", counter(k))
	}
	if err := k.RaiseCounter(s.ID, 1<<32-1, at); err != nil {
		t.Fatal(err)
	}
	mustClose2(t, k)
	k = mustOpenKeys(t, path)
	if c := counter(k); c != 1<<32-1 {
		t.Fatalf("after a reopen the counter is %d", c)
	}
	if err := k.RaiseCounter("0000000000000000", 10, at); !errors.Is(err, ErrKeyUnknown) {
		t.Fatalf("an unknown id: %v", err)
	}
	for name, id := range map[string]string{"a bad id": "xyz", "capitals": strings.ToUpper(s.ID)} {
		if err := k.RaiseCounter(id, 10, at); err == nil || errors.Is(err, ErrKeyUnknown) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := k.RaiseCounter(s.ID, 10, time.Time{}); err == nil {
		t.Fatal("a raise with no time")
	}

	// A revoked credential's counter is not kept up, and nothing is written.
	t2, err := k.AddKey(aClaimedKey(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := k.RevokeKeys([]string{t2.ID}, at); err != nil {
		t.Fatal(err)
	}
	before := fileBytes(t, path)
	if err := k.RaiseCounter(t2.ID, 3, at); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fileBytes(t, path), before) || mustKeys(t, k)[1].Counter != 0 {
		t.Fatal("the counter of a revoked credential was written")
	}
}

// Counter records share the room of the rejected pairings, so a revocation
// always has room.
func TestCounterRecordsShareTheRoomOfRejectedPairings(t *testing.T) {
	n := aClaimedKey(1, 0)
	id := KeyDisplayID(n.CredentialID)
	recs := []keyRecord{{Seq: 1, Type: keyAdded, At: "2026-11-20T14:00:00Z", ID: id, Kind: n.Kind,
		CredentialID: n.CredentialID, PublicKey: n.PublicKey, Words: keyWords, AAGUID: formatAAGUID(n.AAGUID), BackupEligible: true}}
	for i := 0; i < maxSpare-1; i++ {
		recs = append(recs, keyRecord{Seq: int64(len(recs) + 1), Type: keyRejected, At: "2026-11-20T14:00:00Z", ID: id, Words: keyWords})
	}
	path, _ := writeKeyList(t, recs...)
	k := mustOpenKeys(t, path)
	if err := k.RaiseCounter(id, 1, base); err != nil {
		t.Fatalf("the last of the room: %v", err)
	}
	before := fileBytes(t, path)
	if err := k.RaiseCounter(id, 2, base); !errors.Is(err, ErrKeysFull) {
		t.Fatalf("a counter past the room: %v, want %v", err, ErrKeysFull)
	}
	if err := k.AppendRejected(RejectedPairing{ID: id, Words: keyWords, At: base}); !errors.Is(err, ErrKeysFull) {
		t.Fatalf("a rejected pairing past the room: %v, want %v", err, ErrKeysFull)
	}
	if !bytes.Equal(fileBytes(t, path), before) {
		t.Fatal("a refused record was written")
	}
	if err := k.RevokeKeys([]string{id}, base); err != nil {
		t.Fatalf("the revocation has no room: %v", err)
	}
	if keys := mustKeys(t, k); !keys[0].Revoked || keys[0].Counter != 1 {
		t.Fatalf("%+v", keys)
	}
}

func TestRecordsWithClaimsAgainstTheRulesAreDamage(t *testing.T) {
	n := aClaimedKey(1, 3)
	id := KeyDisplayID(n.CredentialID)
	add := keyRecord{Seq: 1, Type: keyAdded, At: "2026-11-20T14:00:00Z", ID: id, Kind: n.Kind, CredentialID: n.CredentialID,
		PublicKey: n.PublicKey, Words: keyWords, AAGUID: formatAAGUID(n.AAGUID), BackupEligible: true, BackupState: true, Counter: 3}
	raise := keyRecord{Seq: 2, Type: keyCounter, At: "2026-11-20T15:00:00Z", ID: id, Counter: 4}
	revoke := keyRecord{Seq: 2, Type: keyRevoked, At: "2026-11-20T15:00:00Z", ID: id}
	with := func(r keyRecord, edit func(*keyRecord)) keyRecord { edit(&r); return r }
	// The lawful list opens, with the counter raised.
	path, _ := writeKeyList(t, add, raise)
	k, err := OpenKeys(path)
	if err != nil {
		t.Fatalf("a lawful list is refused: %v", err)
	}
	if keys := mustKeys(t, k); keys[0].Counter != 4 {
		t.Fatalf("%+v", keys)
	}
	k.Close()

	for name, recs := range map[string][]keyRecord{
		"an AAGUID in capitals":           {with(add, func(r *keyRecord) { r.AAGUID = strings.ToUpper(r.AAGUID) })},
		"a short AAGUID":                  {with(add, func(r *keyRecord) { r.AAGUID = r.AAGUID[:30] })},
		"an AAGUID with dashes":           {with(add, func(r *keyRecord) { r.AAGUID = "d548826e-79b4-db40-a3d8-11116f7e8349" })},
		"an all-zero AAGUID written out":  {with(add, func(r *keyRecord) { r.AAGUID = strings.Repeat("0", 32) })},
		"a backup state without eligible": {with(add, func(r *keyRecord) { r.BackupEligible = false })},
		"a negative counter":              {with(add, func(r *keyRecord) { r.Counter = -1 })},
		"a counter over 32 bits":          {with(add, func(r *keyRecord) { r.Counter = 1 << 32 })},
		"a revocation with a claim":       {add, with(revoke, func(r *keyRecord) { r.BackupEligible = true })},
		"a revocation with a counter":     {add, with(revoke, func(r *keyRecord) { r.Counter = 9 })},
		"a rejection with an AAGUID":      {{Seq: 1, Type: keyRejected, At: "2026-11-20T14:00:00Z", ID: id, Words: keyWords, AAGUID: add.AAGUID}},
		"a counter that does not raise":   {add, with(raise, func(r *keyRecord) { r.Counter = 3 })},
		"a counter that lowers":           {add, with(raise, func(r *keyRecord) { r.Counter = 2 })},
		"a counter of 0":                  {with(add, func(r *keyRecord) { r.Counter = 0 }), with(raise, func(r *keyRecord) { r.Counter = 0 })},
		"a counter of an id not there":    {add, with(raise, func(r *keyRecord) { r.ID = "0000000000000000" })},
		"a counter of a revoked key":      {add, revoke, with(raise, func(r *keyRecord) { r.Seq = 3; r.Counter = 9 })},
		"a counter over 32 bits raised":   {add, with(raise, func(r *keyRecord) { r.Counter = 1 << 32 })},
		"a counter with a kind":           {add, with(raise, func(r *keyRecord) { r.Kind = "vault-passkey" })},
		"a counter with an AAGUID":        {add, with(raise, func(r *keyRecord) { r.AAGUID = add.AAGUID })},
		"a counter with a backup flag":    {add, with(raise, func(r *keyRecord) { r.BackupEligible = true })},
		"a counter with words":            {add, with(raise, func(r *keyRecord) { r.Words = keyWords })},
		"a counter with a credential":     {add, with(raise, func(r *keyRecord) { r.CredentialID = n.CredentialID })},
		"a counter before any credential": {with(raise, func(r *keyRecord) { r.Seq = 1 })},
	} {
		t.Run(name, func(t *testing.T) {
			path, data := writeKeyList(t, recs...)
			k, err := OpenKeys(path)
			if err == nil {
				k.Close()
				t.Fatal("opened")
			}
			if !errors.Is(err, ErrDamaged) {
				t.Fatalf("refused with %v, want %v", err, ErrDamaged)
			}
			if !bytes.Equal(fileBytes(t, path), data) {
				t.Fatal("the file was changed")
			}
		})
	}
}
