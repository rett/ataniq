package ownerjournal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const keyWords = "acorn bridge cedar delta ember falcon"

// newKeyPath returns the path of a list that does not exist yet, in a new
// folder that is the user's own.
func newKeyPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, KeysFileName)
}

func mustOpenKeys(t *testing.T, path string) *KeyFile {
	t.Helper()
	k, err := OpenKeys(path)
	if err != nil {
		t.Fatalf("OpenKeys: %v", err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

// aKey makes a credential of the number n: its id and key are made from n.
func aKey(n int) NewKey {
	return NewKey{
		CredentialID: bytes.Repeat([]byte{byte(n), byte(n >> 8), 0x41}, 12),
		PublicKey:    bytes.Repeat([]byte{byte(n), 0x30}, 45),
		Kind:         "unverified",
		PairedAt:     base.Add(time.Duration(n) * time.Minute),
		Words:        keyWords,
	}
}

func mustAdd(t *testing.T, k *KeyFile, n int) StoredKey {
	t.Helper()
	s, err := k.AddKey(aKey(n))
	if err != nil {
		t.Fatalf("AddKey %d: %v", n, err)
	}
	return s
}

func mustKeys(t *testing.T, k *KeyFile) []StoredKey {
	t.Helper()
	keys, err := k.Keys()
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	return keys
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestANewListIsAWholeFileWithNoKeys(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	if keys := mustKeys(t, k); len(keys) != 0 {
		t.Fatalf("a new list has %d keys", len(keys))
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("the file: %v, %v", info, err)
	}
	if got := string(fileBytes(t, path)); got != keysHeader+"\n" {
		t.Fatalf("the file is %q", got)
	}
	if _, err := os.Lstat(path + keysNewSuffix); err == nil {
		t.Fatalf("the file under the new name is left")
	}
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
	k = mustOpenKeys(t, path)
	if keys := mustKeys(t, k); len(keys) != 0 {
		t.Fatalf("a list opened again has %d keys", len(keys))
	}
}

func TestAKeyAddedIsThereAfterARestart(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	first := mustAdd(t, k, 1)
	second := mustAdd(t, k, 2)
	if first.ID != KeyDisplayID(aKey(1).CredentialID) || len(first.ID) != DisplayIDLength || first.Revoked {
		t.Fatalf("the first key: %+v", first)
	}
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
	k = mustOpenKeys(t, path)
	keys := mustKeys(t, k)
	if len(keys) != 2 {
		t.Fatalf("%d keys after a restart, want 2", len(keys))
	}
	for i, want := range []StoredKey{first, second} {
		got := keys[i]
		a := aKey(i + 1)
		if got.ID != want.ID || !bytes.Equal(got.CredentialID, a.CredentialID) || !bytes.Equal(got.PublicKey, a.PublicKey) ||
			got.Kind != "unverified" || !got.PairedAt.Equal(a.PairedAt) || got.Words != keyWords || got.Revoked || !got.RevokedAt.IsZero() {
			t.Errorf("key %d is %+v", i, got)
		}
	}
	// What Keys returns is a copy.
	keys[0].CredentialID[0] ^= 0xff
	keys[0].PublicKey[0] ^= 0xff
	if again := mustKeys(t, k); !bytes.Equal(again[0].CredentialID, aKey(1).CredentialID) || !bytes.Equal(again[0].PublicKey, aKey(1).PublicKey) {
		t.Fatalf("a caller changed the list through what Keys returned")
	}
}

func TestAKeyThatCannotBeAddedWritesNothing(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	mustAdd(t, k, 1)
	before := fileBytes(t, path)
	bad := map[string]func(*NewKey){
		"a short id":        func(n *NewKey) { n.CredentialID = n.CredentialID[:15] },
		"a long id":         func(n *NewKey) { n.CredentialID = make([]byte, 1025) },
		"no key":            func(n *NewKey) { n.PublicKey = nil },
		"a long key":        func(n *NewKey) { n.PublicKey = make([]byte, 513) },
		"no kind":           func(n *NewKey) { n.Kind = "" },
		"a kind in capital": func(n *NewKey) { n.Kind = "Unverified" },
		"a kind with space": func(n *NewKey) { n.Kind = "hardware token" },
		"five words":        func(n *NewKey) { n.Words = "acorn bridge cedar delta ember" },
		"a capital word":    func(n *NewKey) { n.Words = "Acorn bridge cedar delta ember falcon" },
		"two spaces":        func(n *NewKey) { n.Words = "acorn  bridge cedar delta ember falcon" },
		"no time":           func(n *NewKey) { n.PairedAt = time.Time{} },
	}
	for name, edit := range bad {
		n := aKey(2)
		edit(&n)
		if _, err := k.AddKey(n); err == nil {
			t.Errorf("%s: added", name)
		}
	}
	if _, err := k.AddKey(aKey(1)); !errors.Is(err, ErrKeyExists) {
		t.Errorf("the same credential twice: %v, want %v", err, ErrKeyExists)
	}
	if after := fileBytes(t, path); !bytes.Equal(before, after) {
		t.Fatalf("a refused add changed the file")
	}
	if n := len(mustKeys(t, k)); n != 1 {
		t.Fatalf("%d keys, want 1", n)
	}
}

func TestTheListHoldsMaxKeysAndNoMore(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	for i := 0; i < MaxKeys; i++ {
		mustAdd(t, k, i)
	}
	// Revoking does not make room: revoked credentials stay listed.
	var ids []string
	for _, s := range mustKeys(t, k) {
		ids = append(ids, s.ID)
	}
	if err := k.RevokeKeys(ids, base); err != nil {
		t.Fatal(err)
	}
	before := fileBytes(t, path)
	if _, err := k.AddKey(aKey(MaxKeys)); !errors.Is(err, ErrKeysFull) {
		t.Fatalf("a key past the cap: %v, want %v", err, ErrKeysFull)
	}
	if !bytes.Equal(before, fileBytes(t, path)) {
		t.Fatal("a refused add changed the file")
	}
	// The full list is read back.
	mustClose2(t, k)
	k = mustOpenKeys(t, path)
	keys := mustKeys(t, k)
	if len(keys) != MaxKeys {
		t.Fatalf("%d keys, want %d", len(keys), MaxKeys)
	}
	for _, s := range keys {
		if !s.Revoked {
			t.Fatalf("a key is not revoked after a restart: %s", s.ID)
		}
	}
}

func mustClose2(t *testing.T, k *KeyFile) {
	t.Helper()
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRevokingStaysAfterARestartAndIsNeverUndone(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	a, b, c := mustAdd(t, k, 1), mustAdd(t, k, 2), mustAdd(t, k, 3)
	at := base.Add(time.Hour)
	if err := k.RevokeKeys([]string{b.ID}, at); err != nil {
		t.Fatal(err)
	}
	size := len(fileBytes(t, path))
	// Revoking it again, or an empty list, writes nothing.
	for name, ids := range map[string][]string{"again": {b.ID}, "twice in a call": {b.ID, b.ID}, "no ids": nil} {
		if err := k.RevokeKeys(ids, at.Add(time.Minute)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := len(fileBytes(t, path)); got != size {
		t.Fatalf("a revoke that changes nothing wrote %d bytes", got-size)
	}
	// An id that is not on the list refuses the whole call.
	if err := k.RevokeKeys([]string{a.ID, "0000000000000000"}, at); !errors.Is(err, ErrKeyUnknown) {
		t.Fatalf("an unknown id: %v, want %v", err, ErrKeyUnknown)
	}
	if got := len(fileBytes(t, path)); got != size {
		t.Fatal("a refused revoke wrote")
	}
	// A time that is not one is refused.
	if err := k.RevokeKeys([]string{a.ID}, time.Time{}); err == nil {
		t.Fatal("a revoke with no time")
	}
	// All of them in one write, the revoked one left out.
	if err := k.RevokeKeys([]string{a.ID, b.ID, c.ID}, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	mustClose2(t, k)
	k = mustOpenKeys(t, path)
	keys := mustKeys(t, k)
	if len(keys) != 3 {
		t.Fatalf("%d keys", len(keys))
	}
	want := []time.Time{at.Add(2 * time.Hour), at, at.Add(2 * time.Hour)}
	for i, s := range keys {
		if !s.Revoked || !s.RevokedAt.Equal(want[i]) {
			t.Errorf("key %d: revoked %v at %v, want at %v", i, s.Revoked, s.RevokedAt, want[i])
		}
	}
	// The file has one revoke for each credential: no record is written twice.
	if n := strings.Count(string(fileBytes(t, path)), `"type":"revoked"`); n != 3 {
		t.Fatalf("%d revoke records, want 3", n)
	}
}

// A rejected pairing is journaled with its display id and its words, and
// nothing of the key.
func TestARejectedPairingHoldsNoKey(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	n := aKey(7)
	id := KeyDisplayID(n.CredentialID)
	if err := k.AppendRejected(RejectedPairing{ID: id, Words: keyWords, At: base}); err != nil {
		t.Fatal(err)
	}
	file := string(fileBytes(t, path))
	for name, secret := range map[string]string{
		"the credential id": base64.StdEncoding.EncodeToString(n.CredentialID),
		"the key":           base64.StdEncoding.EncodeToString(n.PublicKey),
	} {
		if strings.Contains(file, secret) {
			t.Errorf("the file holds %s", name)
		}
	}
	if !strings.Contains(file, id) || !strings.Contains(file, keyWords) || !strings.Contains(file, `"type":"rejected"`) {
		t.Fatalf("the record is not as expected:\n%s", file)
	}
	if len(mustKeys(t, k)) != 0 {
		t.Fatal("a rejected pairing is on the list")
	}
	mustClose2(t, k)
	k = mustOpenKeys(t, path)
	if len(mustKeys(t, k)) != 0 {
		t.Fatal("a rejected pairing is on the list after a restart")
	}
	for name, r := range map[string]RejectedPairing{
		"no id":      {Words: keyWords, At: base},
		"a bad id":   {ID: "xyz", Words: keyWords, At: base},
		"no words":   {ID: id, At: base},
		"no time":    {ID: id, Words: keyWords},
		"five words": {ID: id, Words: "acorn bridge cedar delta ember", At: base},
	} {
		if err := k.AppendRejected(r); err == nil {
			t.Errorf("%s: journaled", name)
		}
	}
}

func TestTheRoomForRejectedPairingsEndsBeforeTheRoomForKeys(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	id := KeyDisplayID(aKey(1).CredentialID)
	var err error
	i := 0
	for ; i < MaxKeyRecords && err == nil; i++ {
		err = k.AppendRejected(RejectedPairing{ID: id, Words: keyWords, At: base})
	}
	if !errors.Is(err, ErrKeysFull) || i != maxRejected+1 {
		t.Fatalf("the %dth rejected pairing: %v, want %v at %d", i, err, ErrKeysFull, maxRejected+1)
	}
	// Every credential still fits, and so does its revocation.
	for n := 0; n < MaxKeys; n++ {
		mustAdd(t, k, n)
	}
	var ids []string
	for _, s := range mustKeys(t, k) {
		ids = append(ids, s.ID)
	}
	if err := k.RevokeKeys(ids, base); err != nil {
		t.Fatalf("phone off with a full file: %v", err)
	}
	mustClose2(t, k)
	k = mustOpenKeys(t, path)
	for _, s := range mustKeys(t, k) {
		if !s.Revoked {
			t.Fatal("a key is not revoked")
		}
	}
}

// A list that does not hold is refused, and left exactly as it was: it is
// never taken for an empty one, and never repaired.
func TestADamagedListIsRefusedAndLeftAlone(t *testing.T) {
	good := func(t *testing.T) (string, [][]byte) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		a, b := mustAdd(t, k, 1), mustAdd(t, k, 2)
		if err := k.RevokeKeys([]string{a.ID}, base.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := k.AppendRejected(RejectedPairing{ID: b.ID, Words: keyWords, At: base}); err != nil {
			t.Fatal(err)
		}
		mustClose2(t, k)
		raw := fileBytes(t, path)
		return path, bytes.SplitAfter(raw[:len(raw)-1], []byte("\n"))
	}
	join := func(lines [][]byte) []byte {
		var out []byte
		for _, l := range lines {
			out = append(out, l...)
		}
		return out
	}
	// lines[0] is the header, 1 and 2 add, 3 revokes, 4 is the rejection.
	flip := func(l []byte, at int) []byte {
		c := append([]byte(nil), l...)
		c[at] ^= 0x01
		return c
	}
	cases := map[string]func(l [][]byte) []byte{
		"a changed digit in a digest":    func(l [][]byte) []byte { l[1] = flip(l[1], 3); return join(l) },
		"a changed byte in a record":     func(l [][]byte) []byte { l[2] = flip(l[2], 90); return join(l) },
		"a changed byte in the header":   func(l [][]byte) []byte { l[0] = flip(l[0], 2); return join(l) },
		"a missing record in the middle": func(l [][]byte) []byte { return join(append(append([][]byte{}, l[:2]...), l[3:]...)) },
		"a missing first record":         func(l [][]byte) []byte { return join(append([][]byte{l[0]}, l[2:]...)) },
		"two records swapped":            func(l [][]byte) []byte { l[1], l[2] = l[2], l[1]; return join(l) },
		"a record twice":                 func(l [][]byte) []byte { return join(append(append([][]byte{}, l[:3]...), l[2:]...)) },
		"a record cut short":             func(l [][]byte) []byte { return join(l)[:len(join(l))-20] },
		"no line feed at the end":        func(l [][]byte) []byte { b := join(l); return b[:len(b)-1] },
		"a half-written record at the end": func(l [][]byte) []byte {
			return append(join(l), l[2][:len(l[2])/2]...)
		},
		"a blank line": func(l [][]byte) []byte {
			return join(append(append([][]byte{}, l[:2]...), append([][]byte{[]byte("\n")}, l[2:]...)...))
		},
		"a record with no digest":   func(l [][]byte) []byte { _, rec, _ := bytes.Cut(l[1], []byte(" ")); l[1] = rec; return join(l) },
		"the header and no newline": func(l [][]byte) []byte { return bytes.TrimSuffix(l[0], []byte("\n")) },
		"another file's header":     func(l [][]byte) []byte { l[0] = []byte("ataniq-ownerkeys 2\n"); return join(l) },
		"binary garbage":            func(l [][]byte) []byte { return bytes.Repeat([]byte{0, 0xff, '\n'}, 40) },
		"a sqlite file":             func(l [][]byte) []byte { return append([]byte("SQLite format 3\x00"), make([]byte, 200)...) },
		"an empty file":             func(l [][]byte) []byte { return nil },
		"an enormous file":          func(l [][]byte) []byte { return bytes.Repeat([]byte("x"), maxKeysFileBytes+1) },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			path, lines := good(t)
			data := edit(lines)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			k, err := OpenKeys(path)
			if err == nil {
				k.Close()
				t.Fatalf("a damaged file was opened")
			}
			if !errors.Is(err, ErrDamaged) {
				t.Fatalf("refused with %v, want %v", err, ErrDamaged)
			}
			if after := fileBytes(t, path); !bytes.Equal(after, data) {
				t.Fatalf("the refused file was changed")
			}
			// It is not locked after a refusal.
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
		})
	}
}

// Records that are whole and chained but break the rules of the list: whoever
// can write the file could write them, so the rules are held a second time on
// reading.
func TestARecordAgainstTheRulesOfTheListIsDamage(t *testing.T) {
	write := func(t *testing.T, recs ...keyRecord) (string, []byte) {
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
		// The head that goes with the list, so the rules are what is tested.
		head := fmt.Sprintf("%s %d %s\n", keysHeadHeader, len(recs), hex.EncodeToString(prev[:]))
		if err := os.WriteFile(headPathOf(path), []byte(head), 0o600); err != nil {
			t.Fatal(err)
		}
		return path, data
	}
	n := aKey(1)
	id := KeyDisplayID(n.CredentialID)
	add := keyRecord{Seq: 1, Type: keyAdded, At: "2026-11-20T14:00:00Z", ID: id, Kind: "unverified", CredentialID: n.CredentialID, PublicKey: n.PublicKey, Words: keyWords}
	with := func(r keyRecord, edit func(*keyRecord)) keyRecord { edit(&r); return r }
	revoke := keyRecord{Seq: 2, Type: keyRevoked, At: "2026-11-20T15:00:00Z", ID: id}
	// The one lawful list opens.
	path, _ := write(t, add, revoke)
	k, err := OpenKeys(path)
	if err != nil {
		t.Fatalf("a lawful list is refused: %v", err)
	}
	if keys := mustKeys(t, k); len(keys) != 1 || !keys[0].Revoked {
		t.Fatalf("keys %+v", keys)
	}
	k.Close()

	for name, recs := range map[string][]keyRecord{
		"a number that skips":                       {with(add, func(r *keyRecord) { r.Seq = 2 })},
		"a number that repeats":                     {add, with(revoke, func(r *keyRecord) { r.Seq = 1 })},
		"a type not known":                          {with(add, func(r *keyRecord) { r.Type = "restored" })},
		"a time that is not a record's":             {with(add, func(r *keyRecord) { r.At = "2026-11-20 14:00:00" })},
		"a display id that is not the credential's": {with(add, func(r *keyRecord) { r.ID = "0000000000000000" })},
		"a display id in capitals":                  {with(add, func(r *keyRecord) { r.ID = strings.ToUpper(r.ID) })},
		"a credential twice":                        {add, with(add, func(r *keyRecord) { r.Seq = 2 })},
		"a revoke of an id not there":               {add, with(revoke, func(r *keyRecord) { r.ID = "0000000000000000" })},
		"a revoke of a revoked key":                 {add, revoke, with(revoke, func(r *keyRecord) { r.Seq = 3 })},
		"a revoke with a key":                       {add, with(revoke, func(r *keyRecord) { r.PublicKey = n.PublicKey })},
		"a revoke with words":                       {add, with(revoke, func(r *keyRecord) { r.Words = keyWords })},
		"a revoke before any add":                   {with(revoke, func(r *keyRecord) { r.Seq = 1 })},
		"an add with no key":                        {with(add, func(r *keyRecord) { r.PublicKey = nil })},
		"an add with a short id": {with(add, func(r *keyRecord) {
			r.CredentialID = r.CredentialID[:15]
			r.ID = KeyDisplayID(r.CredentialID)
		})},
		"an add with a bad kind":    {with(add, func(r *keyRecord) { r.Kind = "Hardware" })},
		"an add with no words":      {with(add, func(r *keyRecord) { r.Words = "" })},
		"a rejection with a key":    {{Seq: 1, Type: keyRejected, At: "2026-11-20T14:00:00Z", ID: id, Words: keyWords, PublicKey: n.PublicKey}},
		"a rejection with a kind":   {{Seq: 1, Type: keyRejected, At: "2026-11-20T14:00:00Z", ID: id, Words: keyWords, Kind: "unverified"}},
		"a rejection with no words": {{Seq: 1, Type: keyRejected, At: "2026-11-20T14:00:00Z", ID: id}},
	} {
		t.Run(name, func(t *testing.T) {
			path, data := write(t, recs...)
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

func TestMoreCredentialsThanTheListHoldsIsDamage(t *testing.T) {
	path := newKeyPath(t)
	data := []byte(keysHeader + "\n")
	prev := sha256Of([]byte(keysHeader))
	for i := 0; i <= MaxKeys; i++ {
		n := aKey(i)
		r := keyRecord{Seq: int64(i + 1), Type: keyAdded, At: "2026-11-20T14:00:00Z", ID: KeyDisplayID(n.CredentialID),
			Kind: "unverified", CredentialID: n.CredentialID, PublicKey: n.PublicKey, Words: keyWords}
		line, sum, err := encodeKeyRecord(prev, r)
		if err != nil {
			t.Fatal(err)
		}
		data, prev = append(data, line...), sum
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeys(path); !errors.Is(err, ErrDamaged) {
		t.Fatalf("%v, want %v", err, ErrDamaged)
	}
}

func TestAnUntrustedFileOrFolderIsRefused(t *testing.T) {
	prepare := func(t *testing.T) string {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		mustClose2(t, k)
		return path
	}
	t.Run("a file the group can read", func(t *testing.T) {
		path := prepare(t)
		os.Chmod(path, 0o640)
		if _, err := OpenKeys(path); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a file that is executable", func(t *testing.T) {
		path := prepare(t)
		os.Chmod(path, 0o700)
		if _, err := OpenKeys(path); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a file with a second name", func(t *testing.T) {
		path := prepare(t)
		if err := os.Link(path, path+".again"); err != nil {
			t.Skip(err)
		}
		if _, err := OpenKeys(path); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a symbolic link", func(t *testing.T) {
		path := prepare(t)
		link := filepath.Join(filepath.Dir(path), "link.log")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenKeys(link); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a folder the group can reach", func(t *testing.T) {
		path := prepare(t)
		os.Chmod(filepath.Dir(path), 0o750)
		defer os.Chmod(filepath.Dir(path), 0o700)
		if _, err := OpenKeys(path); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a folder that is a symbolic link", func(t *testing.T) {
		path := prepare(t)
		link := filepath.Join(t.TempDir(), "dir")
		if err := os.Symlink(filepath.Dir(path), link); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenKeys(filepath.Join(link, KeysFileName)); err == nil {
			t.Fatal("opened through a link to the folder")
		}
	})
	t.Run("a file that is a folder", func(t *testing.T) {
		path := newKeyPath(t)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenKeys(path); err == nil {
			t.Fatal("opened")
		}
	})
	t.Run("a folder that is not there", func(t *testing.T) {
		if _, err := OpenKeys(filepath.Join(t.TempDir(), "none", KeysFileName)); err == nil {
			t.Fatal("opened")
		}
	})
	for _, p := range []string{"keys.log", "/a/../b/keys.log", "/a//keys.log", "/a/./keys.log", ""} {
		if _, err := OpenKeys(p); err == nil {
			t.Errorf("the path %q was taken", p)
		}
	}
}

func TestTheListIsHeldByOneOpenAtATime(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	if _, err := OpenKeys(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second open: %v, want %v", err, ErrLocked)
	}
	mustClose2(t, k)
	mustClose2(t, k)
	if _, err := k.Keys(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Keys on a closed list: %v", err)
	}
	if _, err := k.AddKey(aKey(1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("AddKey on a closed list: %v", err)
	}
	if err := k.RevokeKeys(nil, base); !errors.Is(err, ErrClosed) {
		t.Fatalf("RevokeKeys on a closed list: %v", err)
	}
	if err := k.AppendRejected(RejectedPairing{ID: KeyDisplayID(aKey(1).CredentialID), Words: keyWords, At: base}); !errors.Is(err, ErrClosed) {
		t.Fatalf("AppendRejected on a closed list: %v", err)
	}
	mustOpenKeys(t, path)
}

// A new file appears under its name only whole. A leftover of an earlier
// crash under the new name is thrown away; a list that is there is never made
// again over.
func TestAnEarlierCrashWhileMakingTheListLeavesNothingBehind(t *testing.T) {
	path := newKeyPath(t)
	if err := os.WriteFile(path+keysNewSuffix, []byte("half a header"), 0o600); err != nil {
		t.Fatal(err)
	}
	k := mustOpenKeys(t, path)
	if string(fileBytes(t, path)) != keysHeader+"\n" {
		t.Fatalf("the list is %q", fileBytes(t, path))
	}
	mustAdd(t, k, 1)
	mustClose2(t, k)
	// A leftover beside a list that is there changes nothing.
	if err := os.WriteFile(path+keysNewSuffix, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	k = mustOpenKeys(t, path)
	if len(mustKeys(t, k)) != 1 {
		t.Fatal("the key is gone")
	}
}

// When the file is replaced, or changed under the list, nothing is read from
// or written to what is there now.
func TestAListThatIsReplacedOrChangedUnderItRefusesEverything(t *testing.T) {
	t.Run("replaced by another file", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		other := path + ".other"
		if err := os.WriteFile(other, []byte(keysHeader+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(other, path); err != nil {
			t.Fatal(err)
		}
		if _, err := k.Keys(); err == nil {
			t.Fatal("Keys read a file that was replaced")
		}
		if _, err := k.AddKey(aKey(2)); err == nil {
			t.Fatal("AddKey wrote to a file that was replaced")
		}
		if err := k.RevokeKeys(nil, base); err == nil {
			t.Fatal("RevokeKeys went on a file that was replaced")
		}
		if got := string(fileBytes(t, path)); got != keysHeader+"\n" {
			t.Fatalf("the file that took its place was written: %q", got)
		}
	})
	t.Run("made readable by the group", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		os.Chmod(path, 0o644)
		if _, err := k.Keys(); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("written to by another", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString("junk\n")
		f.Close()
		before := fileBytes(t, path)
		if _, err := k.AddKey(aKey(2)); !errors.Is(err, ErrUncertain) {
			t.Fatalf("AddKey: %v, want %v", err, ErrUncertain)
		}
		if !bytes.Equal(before, fileBytes(t, path)) {
			t.Fatal("a file that was written to was written to again")
		}
		// And the list refuses everything until it is opened again, where the
		// junk is damage.
		if _, err := k.Keys(); !errors.Is(err, ErrUncertain) {
			t.Fatalf("Keys: %v, want %v", err, ErrUncertain)
		}
		mustClose2(t, k)
		if _, err := OpenKeys(path); !errors.Is(err, ErrDamaged) {
			t.Fatalf("open: %v, want %v", err, ErrDamaged)
		}
	})
}

// A write that fails leaves it open whether the record is in the file: the
// list refuses everything after it, and the file says what is true when it is
// opened again.
func TestAFailedWriteStopsTheList(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	mustAdd(t, k, 1)
	k.file.Close() // the next write fails
	if _, err := k.AddKey(aKey(2)); !errors.Is(err, ErrUncertain) {
		t.Fatalf("AddKey: %v, want %v", err, ErrUncertain)
	}
	if _, err := k.Keys(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("Keys after a failed write: %v, want %v", err, ErrUncertain)
	}
	if err := k.RevokeKeys(nil, base); !errors.Is(err, ErrUncertain) {
		t.Fatalf("RevokeKeys after a failed write: %v", err)
	}
	k.Close()
	k = mustOpenKeys(t, path)
	if keys := mustKeys(t, k); len(keys) != 1 {
		t.Fatalf("%d keys, want 1", len(keys))
	}
}

func TestSeveralGoroutinesAddAndRead(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 4; i++ {
				if _, err := k.AddKey(aKey(g*10 + i)); err != nil {
					t.Error(err)
				}
				if _, err := k.Keys(); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if n := len(mustKeys(t, k)); n != 32 {
		t.Fatalf("%d keys, want 32", n)
	}
	mustClose2(t, k)
	k = mustOpenKeys(t, path)
	if n := len(mustKeys(t, k)); n != 32 {
		t.Fatalf("%d keys after a restart, want 32", n)
	}
}

func TestTheDisplayIDIsTheFirstEightBytesOfTheDigest(t *testing.T) {
	// SHA-256 of "abc" is ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad.
	if got := KeyDisplayID([]byte("abc")); got != "ba7816bf8f01cfea" {
		t.Fatalf("%s", got)
	}
}

func TestTheFileNameIsFixed(t *testing.T) {
	if KeysFileName != "keys.log" || fmt.Sprint(MaxKeys) != "64" {
		t.Fatalf("%s %d", KeysFileName, MaxKeys)
	}
}

func sha256Of(b []byte) [32]byte { return sha256.Sum256(b) }

// Records lost from the end of the file are not found by the digests of the
// chain, which would leave a shorter list that holds; they are found by the
// head, the record count and digest of the last record that keys.head keeps.
// This test once pinned the opposite (the shorter list opened), which let a
// lost revocation make a revoked key good again after a restart; it now holds
// that the list is refused as damaged, with the cause named, and is left as it
// is. TestACutThatTheHeadFollowsIsNotFound pins what remains.
func TestRecordsLostFromTheEndAreDamage(t *testing.T) {
	for name, cut := range map[string]func(lines [][]byte) []byte{
		"the last credential": func(l [][]byte) []byte { return bytes.Join(l[:2], nil) },
		"the last revocation": func(l [][]byte) []byte { return bytes.Join(l[:3], nil) },
		"every record":        func(l [][]byte) []byte { return l[0] },
	} {
		t.Run(name, func(t *testing.T) {
			path := newKeyPath(t)
			k := mustOpenKeys(t, path)
			a := mustAdd(t, k, 1)
			mustAdd(t, k, 2)
			if err := k.RevokeKeys([]string{a.ID}, base.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			mustClose2(t, k)
			lines := bytes.SplitAfter(fileBytes(t, path), []byte("\n"))
			cutBytes := cut(lines)
			if err := os.WriteFile(path, cutBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := OpenKeys(path)
			if !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), "suffix lost") {
				t.Fatalf("a list with records lost from its end: %v, want %v (suffix lost)", err, ErrDamaged)
			}
			if !bytes.Equal(fileBytes(t, path), cutBytes) {
				t.Fatal("the refused file was changed")
			}
		})
	}
}

// What the head cannot hold: someone who can write the folder can cut the list
// and write the head to match, and the list that is left holds. The package
// doc names this limit.
func TestACutThatTheHeadFollowsIsNotFound(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	a := mustAdd(t, k, 1)
	mustClose2(t, k)
	headOf1 := fileBytes(t, headPathOf(path))
	k = mustOpenKeys(t, path)
	mustAdd(t, k, 2)
	mustClose2(t, k)
	lines := bytes.SplitAfter(fileBytes(t, path), []byte("\n"))
	if err := os.WriteFile(path, bytes.Join(lines[:2], nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(headPathOf(path), headOf1, 0o600); err != nil {
		t.Fatal(err)
	}
	k = mustOpenKeys(t, path)
	if keys := mustKeys(t, k); len(keys) != 1 || keys[0].ID != a.ID {
		t.Fatalf("%+v", keys)
	}
}

// headPathOf is where the head of the list at path is.
func headPathOf(path string) string {
	return filepath.Join(filepath.Dir(path), KeysHeadFileName)
}

// Same-length damage while the list is open: a byte of a record changed in
// place. Every call that reads or writes finds it, marks the list damaged, and
// refuses everything after.
func TestSameLengthDamageWhileOpenIsFound(t *testing.T) {
	flipInPlace := func(t *testing.T, path string, line int) {
		t.Helper()
		data := fileBytes(t, path)
		lines := bytes.SplitAfter(data, []byte("\n"))
		off := 0
		for i := 0; i < line; i++ {
			off += len(lines[i])
		}
		// A digit of the digest of that line: the length does not change.
		if data[off] == '0' {
			data[off] = '1'
		} else {
			data[off] = '0'
		}
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteAt(data, 0); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("found at Keys", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		mustAdd(t, k, 2)
		flipInPlace(t, path, 1)
		if _, err := k.Keys(); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Keys: %v, want %v", err, ErrDamaged)
		}
		// Refused for good, whatever is done to the file after.
		if _, err := k.AddKey(aKey(3)); !errors.Is(err, ErrDamaged) {
			t.Fatalf("AddKey: %v, want %v", err, ErrDamaged)
		}
		if err := k.RevokeKeys(nil, base); !errors.Is(err, ErrDamaged) {
			t.Fatalf("RevokeKeys: %v, want %v", err, ErrDamaged)
		}
		if err := k.AppendRejected(RejectedPairing{ID: KeyDisplayID(aKey(1).CredentialID), Words: keyWords, At: base}); !errors.Is(err, ErrDamaged) {
			t.Fatalf("AppendRejected: %v, want %v", err, ErrDamaged)
		}
		flipInPlace(t, path, 1) // put it back
		if _, err := k.Keys(); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Keys after the file was put right: %v, want %v", err, ErrDamaged)
		}
	})
	t.Run("found before an append", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		flipInPlace(t, path, 1)
		before := fileBytes(t, path)
		if _, err := k.AddKey(aKey(2)); !errors.Is(err, ErrDamaged) {
			t.Fatalf("AddKey: %v, want %v", err, ErrDamaged)
		}
		if !bytes.Equal(before, fileBytes(t, path)) {
			t.Fatal("a record was appended to a damaged chain")
		}
		if _, err := k.Keys(); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Keys: %v, want %v", err, ErrDamaged)
		}
	})
	t.Run("a revocation is not made on a damaged chain either", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		a := mustAdd(t, k, 1)
		flipInPlace(t, path, 1)
		if err := k.RevokeKeys([]string{a.ID}, base); !errors.Is(err, ErrDamaged) {
			t.Fatalf("RevokeKeys: %v, want %v", err, ErrDamaged)
		}
	})
	t.Run("the head changed under the list", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		other := newKeyPath(t)
		k2 := mustOpenKeys(t, other)
		mustAdd(t, k2, 2)
		mustClose2(t, k2)
		if err := os.WriteFile(headPathOf(path), fileBytes(t, headPathOf(other)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := k.AddKey(aKey(3)); !errors.Is(err, ErrDamaged) {
			t.Fatalf("AddKey: %v, want %v", err, ErrDamaged)
		}
		if _, err := k.Keys(); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Keys: %v, want %v", err, ErrDamaged)
		}
	})
	t.Run("a list that nobody touched is not refused", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		for i := 1; i <= 5; i++ {
			mustAdd(t, k, i)
			mustKeys(t, k)
		}
		if len(mustKeys(t, k)) != 5 {
			t.Fatal("the keys are not all there")
		}
	})
}

// The head keeps the count and digest of the last record, written after the
// record is on the disk. A crash between the two leaves the file one record
// ahead of the head, which chains from it: that is accepted and the head is
// made right.
func TestACrashBetweenTheAppendAndTheHeadIsRepaired(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	a := mustAdd(t, k, 1)
	oldHead := fileBytes(t, headPathOf(path))
	b := mustAdd(t, k, 2)
	mustClose2(t, k)
	newHead := fileBytes(t, headPathOf(path))
	if bytes.Equal(oldHead, newHead) {
		t.Fatal("the head did not move with the record")
	}
	// The crash: the record is in the file, the head is the one before.
	if err := os.WriteFile(headPathOf(path), oldHead, 0o600); err != nil {
		t.Fatal(err)
	}
	k = mustOpenKeys(t, path)
	if keys := mustKeys(t, k); len(keys) != 2 || keys[0].ID != a.ID || keys[1].ID != b.ID {
		t.Fatalf("%+v", keys)
	}
	if got := fileBytes(t, headPathOf(path)); !bytes.Equal(got, newHead) {
		t.Fatalf("the head was not repaired: %q, want %q", got, newHead)
	}
	mustClose2(t, k)
	// And a revocation of several credentials, written in one step, is the same
	// crash with more than one record ahead.
	k = mustOpenKeys(t, path)
	before := fileBytes(t, headPathOf(path))
	if err := k.RevokeKeys([]string{a.ID, b.ID}, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	mustClose2(t, k)
	if err := os.WriteFile(headPathOf(path), before, 0o600); err != nil {
		t.Fatal(err)
	}
	k = mustOpenKeys(t, path)
	for _, key := range mustKeys(t, k) {
		if !key.Revoked {
			t.Fatalf("a revocation that was in the file is lost: %+v", key)
		}
	}
}

// A file that is ahead of the head by a record that does not chain from it is
// not a crash between two writes, and neither is a head of another chain.
func TestAFileAheadOfTheHeadThatDoesNotChainIsDamage(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	mustAdd(t, k, 1)
	mustAdd(t, k, 2)
	mustClose2(t, k)
	other := newKeyPath(t)
	k2 := mustOpenKeys(t, other)
	mustAdd(t, k2, 7)
	mustClose2(t, k2)
	// A head with a count of one, but of another chain.
	if err := os.WriteFile(headPathOf(path), fileBytes(t, headPathOf(other)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeys(path); !errors.Is(err, ErrDamaged) {
		t.Fatalf("%v, want %v", err, ErrDamaged)
	}
}

// The head is the list's own: a list with records and no head is damaged, a
// list with none gets one, and a head that is not of the form is damage.
func TestTheHeadFile(t *testing.T) {
	t.Run("a new list makes its head", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		info, err := os.Lstat(headPathOf(path))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("the head: %v, %v", info, err)
		}
		mustClose2(t, k)
		if _, err := os.Lstat(headPathOf(path) + ".new"); err == nil {
			t.Fatal("the temporary name of the head is left")
		}
	})
	t.Run("the head has the mode of the list after every write", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		a := mustAdd(t, k, 2)
		if err := k.RevokeKeys([]string{a.ID}, base); err != nil {
			t.Fatal(err)
		}
		if err := k.AppendRejected(RejectedPairing{ID: a.ID, Words: keyWords, At: base}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(headPathOf(path))
		if err != nil || info.Mode().Perm() != 0o600 || info.Mode().Type() != 0 {
			t.Fatalf("the head: %v, %v", info, err)
		}
		if _, err := os.Lstat(headPathOf(path) + ".new"); err == nil {
			t.Fatal("the temporary name of the head is left")
		}
	})
	t.Run("an existing list with records and no head is damaged", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		mustClose2(t, k)
		if err := os.Remove(headPathOf(path)); err != nil {
			t.Fatal(err)
		}
		before := fileBytes(t, path)
		if _, err := OpenKeys(path); !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), "head") {
			t.Fatalf("%v, want %v naming the head", err, ErrDamaged)
		}
		if !bytes.Equal(before, fileBytes(t, path)) {
			t.Fatal("the refused list was changed")
		}
		if _, err := os.Lstat(headPathOf(path)); err == nil {
			t.Fatal("a head was made for a list that has records")
		}
	})
	t.Run("a list with no records and no head gets one", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustClose2(t, k)
		if err := os.Remove(headPathOf(path)); err != nil {
			t.Fatal(err)
		}
		k = mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		mustClose2(t, k)
		if _, err := OpenKeys(path); err != nil {
			t.Fatal(err)
		}
	})
	for name, content := range map[string]string{
		"empty":            "",
		"no line feed":     "ataniq-ownerkeys-head 1 0 " + strings.Repeat("0", 64),
		"another version":  "ataniq-ownerkeys-head 2 0 " + strings.Repeat("0", 64) + "\n",
		"a negative count": "ataniq-ownerkeys-head 1 -1 " + strings.Repeat("0", 64) + "\n",
		"a capital digest": "ataniq-ownerkeys-head 1 0 " + strings.Repeat("A", 64) + "\n",
		"a short digest":   "ataniq-ownerkeys-head 1 0 " + strings.Repeat("0", 63) + "\n",
		"garbage":          strings.Repeat("\x00", 90),
		"two lines":        "ataniq-ownerkeys-head 1 0 " + strings.Repeat("0", 64) + "\nx\n",
		"too long":         strings.Repeat("x", 4096),
	} {
		t.Run("a head that is "+name, func(t *testing.T) {
			path := newKeyPath(t)
			k := mustOpenKeys(t, path)
			mustClose2(t, k)
			if err := os.WriteFile(headPathOf(path), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenKeys(path); !errors.Is(err, ErrDamaged) {
				t.Fatalf("%v, want %v", err, ErrDamaged)
			}
		})
	}
	t.Run("a head with a count the list does not reach", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustClose2(t, k)
		head := "ataniq-ownerkeys-head 1 5 " + strings.Repeat("0", 64) + "\n"
		if err := os.WriteFile(headPathOf(path), []byte(head), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenKeys(path); !errors.Is(err, ErrDamaged) {
			t.Fatalf("%v, want %v", err, ErrDamaged)
		}
	})
}

// The head is held to the list's own trust checks.
func TestTheHeadFileIsHeldToTheTrustChecksOfTheList(t *testing.T) {
	prepare := func(t *testing.T) string {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		mustClose2(t, k)
		return path
	}
	for name, mode := range map[string]os.FileMode{"readable by the group": 0o640, "executable": 0o700, "readable by all": 0o644, "not readable": 0o400} {
		t.Run(name, func(t *testing.T) {
			path := prepare(t)
			if err := os.Chmod(headPathOf(path), mode); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenKeys(path); !errors.Is(err, ErrNotPrivate) {
				t.Fatalf("%v, want %v", err, ErrNotPrivate)
			}
		})
	}
	t.Run("a second name", func(t *testing.T) {
		path := prepare(t)
		if err := os.Link(headPathOf(path), headPathOf(path)+".again"); err != nil {
			t.Skip(err)
		}
		if _, err := OpenKeys(path); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("%v, want %v", err, ErrNotPrivate)
		}
	})
	t.Run("a symbolic link", func(t *testing.T) {
		path := prepare(t)
		real := headPathOf(path) + ".real"
		if err := os.Rename(headPathOf(path), real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, headPathOf(path)); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenKeys(path); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("%v, want %v", err, ErrNotPrivate)
		}
	})
	t.Run("a folder in its place", func(t *testing.T) {
		path := prepare(t)
		os.Remove(headPathOf(path))
		if err := os.Mkdir(headPathOf(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenKeys(path); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("%v, want %v", err, ErrNotPrivate)
		}
	})
	t.Run("changed while the list is open", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		if err := os.Chmod(headPathOf(path), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := k.AddKey(aKey(2)); err == nil {
			t.Fatal("a record was appended with a head the group can read")
		}
		if _, err := k.Keys(); err == nil {
			t.Fatal("Keys read a list whose head the group can read")
		}
	})
}

// A write whose head cannot be written stops the list as a failed write does,
// and the record that is in the file is taken as the crash it is on the next open.
func TestAHeadThatCannotBeWrittenStopsTheList(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	mustAdd(t, k, 1)
	// A folder in the way of the temporary name makes the head unwritable.
	if err := os.Mkdir(headPathOf(path)+".new", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := k.AddKey(aKey(2)); !errors.Is(err, ErrUncertain) {
		t.Fatalf("AddKey: %v, want %v", err, ErrUncertain)
	}
	if _, err := k.Keys(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("Keys: %v, want %v", err, ErrUncertain)
	}
	mustClose2(t, k)
	os.Remove(headPathOf(path) + ".new")
	k = mustOpenKeys(t, path)
	if keys := mustKeys(t, k); len(keys) != 2 {
		t.Fatalf("%d keys: the record that reached the file is not there", len(keys))
	}
}

// Close called twice returns what the first call returned.
func TestCloseTwiceReturnsWhatTheFirstReturned(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	first := k.Close()
	if second := k.Close(); second != first {
		t.Fatalf("the second Close returned %v, the first %v", second, first)
	}
	// A list whose descriptor is closed under it: the first Close reports it,
	// the second says the same.
	k = mustOpenKeys(t, newKeyPath(t))
	k.file.Close()
	first = k.Close()
	if first == nil {
		t.Fatal("the first Close of a list with a closed file returned nil")
	}
	if second := k.Close(); second == nil || second.Error() != first.Error() {
		t.Fatalf("the second Close returned %v, the first %v", second, first)
	}
}

// A leftover temporary name of the head that is a second name of another file
// is refused before anything is written to it: the other file keeps its bytes.
func TestTheTemporaryHeadIsCheckedBeforeItIsTruncated(t *testing.T) {
	const keep = "bytes of another file that must stay\n"
	plant := func(t *testing.T, path string) string {
		t.Helper()
		other := filepath.Join(t.TempDir(), "other")
		if err := os.WriteFile(other, []byte(keep), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(other, headPathOf(path)+".new"); err != nil {
			t.Skip(err)
		}
		return other
	}
	intact := func(t *testing.T, other string) {
		t.Helper()
		got, err := os.ReadFile(other)
		if err != nil || string(got) != keep {
			t.Fatalf("the other file is %q, %v; want its bytes unchanged", got, err)
		}
	}
	t.Run("when a head is made at open", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustClose2(t, k)
		if err := os.Remove(headPathOf(path)); err != nil {
			t.Fatal(err)
		}
		other := plant(t, path)
		if _, err := OpenKeys(path); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("OpenKeys: %v, want %v", err, ErrNotPrivate)
		}
		intact(t, other)
	})
	t.Run("when a record is added", func(t *testing.T) {
		path := newKeyPath(t)
		k := mustOpenKeys(t, path)
		mustAdd(t, k, 1)
		other := plant(t, path)
		if _, err := k.AddKey(aKey(2)); !errors.Is(err, ErrNotPrivate) {
			t.Fatalf("AddKey: %v, want %v", err, ErrNotPrivate)
		}
		intact(t, other)
	})
}

// A leftover temporary name of the head that is a plain file of the user with
// one name, longer than the line, is taken over: the head ends up the line alone.
func TestALeftoverTemporaryHeadIsTakenOver(t *testing.T) {
	path := newKeyPath(t)
	k := mustOpenKeys(t, path)
	mustAdd(t, k, 1)
	mustClose2(t, k)
	junk := bytes.Repeat([]byte("left by a crash "), 40)
	if err := os.WriteFile(headPathOf(path)+".new", junk, 0o600); err != nil {
		t.Fatal(err)
	}
	k = mustOpenKeys(t, path)
	mustAdd(t, k, 2)
	mustClose2(t, k)
	if _, err := os.Lstat(headPathOf(path) + ".new"); err == nil {
		t.Fatal("the temporary name of the head is left")
	}
	k = mustOpenKeys(t, path)
	if keys := mustKeys(t, k); len(keys) != 2 {
		t.Fatalf("%d keys, want 2", len(keys))
	}
}
