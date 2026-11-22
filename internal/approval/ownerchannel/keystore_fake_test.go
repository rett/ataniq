package ownerchannel

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"time"

	"ataniq/internal/approval/ownerjournal"
)

// fakeKeys is a list of deciding keys in memory, with the rules of the real
// one that the service depends on, and a way to make it fail. It outlives a
// service, so a test that restarts the service keeps its list.
type fakeKeys struct {
	mu       sync.Mutex
	keys     []ownerjournal.StoredKey
	rejected []ownerjournal.RejectedPairing
	closed   int
	// failRead and failAdd, failRevoke and failReject are the errors the
	// calls answer with while set.
	failRead, failAdd, failRevoke, failReject error
	// writes counts the calls that changed the list.
	writes int
}

func newFakeKeys() *fakeKeys { return &fakeKeys{} }

func (f *fakeKeys) Keys() ([]ownerjournal.StoredKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRead != nil {
		return nil, f.failRead
	}
	out := make([]ownerjournal.StoredKey, len(f.keys))
	for i, k := range f.keys {
		k.CredentialID = append([]byte(nil), k.CredentialID...)
		k.PublicKey = append([]byte(nil), k.PublicKey...)
		out[i] = k
	}
	return out, nil
}

func (f *fakeKeys) AddKey(n ownerjournal.NewKey) (ownerjournal.StoredKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAdd != nil {
		return ownerjournal.StoredKey{}, f.failAdd
	}
	id := ownerjournal.KeyDisplayID(n.CredentialID)
	for _, k := range f.keys {
		if k.ID == id {
			return ownerjournal.StoredKey{}, ownerjournal.ErrKeyExists
		}
	}
	if len(f.keys) >= ownerjournal.MaxKeys {
		return ownerjournal.StoredKey{}, ownerjournal.ErrKeysFull
	}
	k := ownerjournal.StoredKey{
		ID: id, CredentialID: bytes.Clone(n.CredentialID), PublicKey: bytes.Clone(n.PublicKey),
		Kind: n.Kind, AAGUID: n.AAGUID, BackupEligible: n.BackupEligible, BackupState: n.BackupState, Counter: n.Counter,
		PairedAt: n.PairedAt.Truncate(time.Second), Words: n.Words,
	}
	f.keys = append(f.keys, k)
	f.writes++
	return k, nil
}

func (f *fakeKeys) RevokeKeys(ids []string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRevoke != nil {
		return f.failRevoke
	}
	index := make([]int, 0, len(ids))
	for _, id := range ids {
		found := -1
		for i := range f.keys {
			if f.keys[i].ID == id {
				found = i
			}
		}
		if found < 0 {
			return ownerjournal.ErrKeyUnknown
		}
		index = append(index, found)
	}
	for _, i := range index {
		if !f.keys[i].Revoked {
			f.keys[i].Revoked, f.keys[i].RevokedAt = true, at
			f.writes++
		}
	}
	return nil
}

func (f *fakeKeys) AppendRejected(r ownerjournal.RejectedPairing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failReject != nil {
		return f.failReject
	}
	f.rejected = append(f.rejected, r)
	f.writes++
	return nil
}

func (f *fakeKeys) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

// seed puts a credential with a display id of the test's own on the list.
func (f *fakeKeys) seed(id string, revoked bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, ownerjournal.StoredKey{
		ID: id, CredentialID: []byte(strings.Repeat("c", 16)), PublicKey: []byte("k"), Kind: "unverified",
		PairedAt: time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC), Words: "acorn bridge cedar delta ember falcon garden harbor", Revoked: revoked,
	})
}

var errStoreDown = errors.New("the store is down at /private/path/keys.log")
