// Package appcontract contains private application data contracts. These
// values do not establish authentication, authorization, or durable state.
package appcontract

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
)

var errInvalid = errors.New("invalid contract value")

// Instance identifies the store incarnation to which a value belongs.
type Instance struct {
	Authority              string
	StoreID, IncarnationID [16]byte
}

func validID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

// frame is private: callers can only construct the closed contracts below.
func frame(domain string, fields ...[]byte) []byte {
	b := append([]byte("ATANIQ-"+domain), 0, 1)
	for _, f := range fields {
		b = binary.BigEndian.AppendUint32(b, uint32(len(f)))
		b = append(b, f...)
	}
	return b
}

func (i Instance) fields() [][]byte {
	return [][]byte{[]byte(i.Authority), i.StoreID[:], i.IncarnationID[:]}
}

func number(n uint64) []byte { return binary.BigEndian.AppendUint64(nil, n) }

// ProjectionKeys holds domain-separated keys; it grants no access to data.
type ProjectionKeys struct{ status, resource, event, cause [32]byte }

func derive(master [32]byte, label string) [32]byte {
	h := hmac.New(sha256.New, master[:])
	h.Write(frame("KDF", []byte(label)))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func NewProjectionKeys(master [32]byte) ProjectionKeys {
	return ProjectionKeys{derive(master, "status-v1"), derive(master, "resource-v1"), derive(master, "event-id-v1"), derive(master, "cause-event-id-v1")}
}

type EventKind string

const (
	InputAdmitted   EventKind = "input_admitted"
	InputCancelled  EventKind = "input_cancelled"
	ItemDisposition EventKind = "item_disposition"
	StateRefresh    EventKind = "state_refresh"
)

func mac(key [32]byte, data []byte) string {
	h := hmac.New(sha256.New, key[:])
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// EventID identifies a projected queue event independently of its viewer.
func (k ProjectionKeys) EventID(i Instance, project, conversation string, sequence uint64, kind EventKind) (string, error) {
	if !validID(i.Authority) || !validID(project) || !validID(conversation) || sequence == 0 || sequence > math.MaxInt64 {
		return "", errInvalid
	}
	switch kind {
	case InputAdmitted, InputCancelled, ItemDisposition, StateRefresh:
	default:
		return "", errInvalid
	}
	f := append(i.fields(), []byte(project), []byte(conversation), number(sequence), []byte(kind))
	return mac(k.event, frame("EVENT-ID", f...)), nil
}

// CauseEventID identifies an owner-visible loss event.
func (k ProjectionKeys) CauseEventID(i Instance, sequence uint64) (string, error) {
	if !validID(i.Authority) || sequence == 0 || sequence > math.MaxInt64 {
		return "", errInvalid
	}
	f := append(i.fields(), number(sequence))
	return mac(k.cause, frame("CAUSE-EVENT-ID", f...)), nil
}

// CredentialVerifier computes the stored verifier for random credentials.
// Validity, credential authentication and secret generation are separate.
func CredentialVerifier(i Instance, client, credential string, generation uint64, salt, secret [32]byte) ([32]byte, error) {
	if !validID(i.Authority) || !validID(client) || !validID(credential) || generation == 0 || generation > math.MaxInt64 {
		return [32]byte{}, errInvalid
	}
	f := append(i.fields(), []byte(client), []byte(credential), number(generation), salt[:], secret[:])
	return sha256.Sum256(frame("CREDENTIAL", f...)), nil
}
