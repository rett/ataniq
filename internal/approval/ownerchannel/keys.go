package ownerchannel

import (
	"time"

	"ataniq/internal/approval/ownerjournal"
)

// KeyStore is the list of the owner's deciding keys, the credentials of the
// phones he paired, as the service uses it: an interface so that a test can
// give it a fake. An opened *ownerjournal.KeyFile is one. The service owns
// the store from [Config].Keys on and closes it with itself.
//
// The list only grows: a credential is added, and revoked, and a revoked
// credential stays listed and is never made good again. Every error of a
// store other than the three of package ownerjournal that name a fact of the
// list ([ownerjournal.ErrKeysFull], [ownerjournal.ErrKeyExists] and
// [ownerjournal.ErrKeyUnknown]) means the list cannot be used, and the
// service answers it [ErrKeysUnavailable].
type KeyStore interface {
	// Keys returns every credential, in the order they were added.
	Keys() ([]ownerjournal.StoredKey, error)
	// AddKey puts a credential on the list.
	AddKey(n ownerjournal.NewKey) (ownerjournal.StoredKey, error)
	// RevokeKeys revokes the credentials with these display ids in one
	// step. One already revoked is left as it is; an id no credential has
	// refuses the whole call.
	RevokeKeys(ids []string, at time.Time) error
	// AppendRejected journals a pairing the owner rejected.
	AppendRejected(r ownerjournal.RejectedPairing) error
	// RaiseCounter records the sign counter a verified login or decision
	// of the credential with this display id reported: the list keeps the
	// highest seen and never lowers it, and a counter that is not higher
	// writes nothing. An id no credential has is
	// [ownerjournal.ErrKeyUnknown].
	RaiseCounter(id string, counter uint32, at time.Time) error
	Close() error
}

var _ KeyStore = (*ownerjournal.KeyFile)(nil)
