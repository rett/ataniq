package ownerjournal

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"ataniq/internal/approval/record"
)

// SurfaceSeat is the surface of a decision made on the owner's phone, through
// the seat app and the seat gate: the word the owner channel's service names
// that surface by. The activity entry of a decision confirmed by passkey says
// it.
const SurfaceSeat = "seat"

// ErrNoEvidence says that an entry keeps no evidence of a passkey assertion:
// it is a decision or a held entry confirmed by code.
var ErrNoEvidence = errors.New("ownerjournal: the entry keeps no evidence of a passkey assertion")

// The bounds of the evidence, the same as the seat gate's words hold an
// assertion to (package ownerwire; a test holds the two equal), with the
// credential id's of the list of deciding keys. Within them the evidence fits
// the two bodies it is kept in.
const (
	minEvidenceAuthData    = 37
	maxEvidenceAuthData    = 1 << 10
	maxEvidenceClientData  = 2 << 10
	maxEvidenceSignature   = 72
	evidenceNonceBytes     = 32
	evidenceInstanceLength = 64
	maxEvidenceHost        = 253
	originScheme           = "https://"
)

// SeatEvidence is what the journal keeps of the assertion that confirmed a
// decision made on the phone, beside the decision record: the four fields of
// the assertion, the nonce of the challenge it signed, the instance (the
// gate's own key fingerprint) and the seat origin the daemon checked the
// assertion against. The record keeps only the assertion's digest, because
// of its size cap. With the credential's public key from the list of
// deciding keys (a revoked key stays on it), an auditor recomputes the
// challenge from the record and this evidence, checks the signature again,
// and matches the record's assertion_sha256.
type SeatEvidence struct {
	// CredentialID, AuthenticatorData, ClientDataJSON and Signature are the
	// assertion's four fields, as the phone's browser gave them.
	CredentialID      []byte
	AuthenticatorData []byte
	ClientDataJSON    []byte
	Signature         []byte
	// Nonce is the daemon's nonce of the challenge, 32 bytes.
	Nonce []byte
	// Instance is the gate's own key fingerprint, 64 lower-case hexadecimal
	// digits.
	Instance string
	// Origin is the seat origin, https:// and a lower-case host name.
	Origin string
}

// check holds the evidence to its bounds.
func (v *SeatEvidence) check() error {
	between := func(b []byte, min, max int) bool { return len(b) >= min && len(b) <= max }
	switch {
	case !between(v.CredentialID, minCredentialIDLen, maxCredentialIDLen):
		return errors.New("ownerjournal: the evidence's credential id is not 16 to 1023 bytes")
	case !between(v.AuthenticatorData, minEvidenceAuthData, maxEvidenceAuthData):
		return errors.New("ownerjournal: the evidence's authenticator data is not 37 to 1024 bytes")
	case !between(v.ClientDataJSON, 1, maxEvidenceClientData):
		return errors.New("ownerjournal: the evidence's client data is not 1 to 2048 bytes")
	case !between(v.Signature, 1, maxEvidenceSignature):
		return errors.New("ownerjournal: the evidence's signature is not 1 to 72 bytes")
	case len(v.Nonce) != evidenceNonceBytes:
		return errors.New("ownerjournal: the evidence's nonce is not 32 bytes")
	case !validHex(v.Instance, evidenceInstanceLength):
		return errors.New("ownerjournal: the evidence's instance is not 64 lower-case hexadecimal digits")
	case !validOrigin(v.Origin):
		return errors.New("ownerjournal: the evidence's origin is not https:// and a lower-case host name")
	}
	return nil
}

// digest is the record's assertion_sha256 of the evidence's assertion.
func (v *SeatEvidence) digest() string {
	return record.AssertionDigest(v.CredentialID, v.AuthenticatorData, v.ClientDataJSON, v.Signature)
}

// validOrigin reports whether s is a seat origin in its canonical form:
// https:// and 1 to 253 lower-case letters, digits, dots and dashes, with no
// port, path or anything else.
func validOrigin(s string) bool {
	host, ok := strings.CutPrefix(s, originScheme)
	if !ok || len(host) < 1 || len(host) > maxEvidenceHost {
		return false
	}
	for i := 0; i < len(host); i++ {
		switch c := host[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-':
		default:
			return false
		}
	}
	return true
}

// The members the evidence adds to the bodies. The decision or held entry
// keeps what the assertion signed and its signature; the activity entry keeps
// who signed it and where from. Split so, each body stays within the 4096
// bytes the layout allows a body, for the largest evidence the bounds take.
type (
	// signedBody is the evidence in the body of a decision or a held entry.
	signedBody struct {
		ClientDataJSON string `json:"client_data_json"`
		Nonce          string `json:"nonce"`
		Signature      string `json:"signature"`
	}
	// signerBody is the evidence in the body of the activity entry.
	signerBody struct {
		AuthenticatorData string `json:"authenticator_data"`
		CredentialID      string `json:"credential_id"`
		Instance          string `json:"instance"`
		Origin            string `json:"origin"`
	}
)

// confirmation is what the journal takes from a decision's confirmation: the
// surface and the session of its activity entry, and the members the
// evidence adds to the decision's own body and to the activity's. A decision
// confirmed by code takes the console block's surface and session, as it
// always did, and adds nothing. One confirmed by passkey takes the surface
// seat and the seat block's session, has no last activity at the desk, and
// adds the evidence, which must be there and must be of the assertion the
// record names.
type confirmation struct {
	surface, session string
	own, activity    map[string]any
}

func confirmationOf(rec record.Decision, seen Seen) (confirmation, error) {
	switch rec.Confirmed {
	case record.ConfirmedCode:
		if seen.Evidence != nil {
			return confirmation{}, errors.New("ownerjournal: evidence of a passkey assertion is given for a decision confirmed by code")
		}
		return confirmation{surface: rec.Console.Surface, session: rec.Console.Session}, nil
	case record.ConfirmedPasskey:
	default:
		return confirmation{}, errors.New("ownerjournal: the decision is confirmed in a way the journal does not know")
	}
	v := seen.Evidence
	switch {
	case v == nil:
		return confirmation{}, errors.New("ownerjournal: a decision confirmed by passkey needs the evidence of its assertion")
	case !seen.LastActivity.IsZero():
		return confirmation{}, errors.New("ownerjournal: a decision confirmed by passkey has no last activity at the desk")
	}
	if err := v.check(); err != nil {
		return confirmation{}, err
	}
	if v.digest() != rec.Seat.AssertionSHA256 {
		return confirmation{}, errors.New("ownerjournal: the evidence is not of the assertion the decision record names")
	}
	if KeyDisplayID(v.CredentialID) != rec.Seat.Credential {
		return confirmation{}, errors.New("ownerjournal: the evidence's credential is not the one the decision record names")
	}
	b64 := base64.StdEncoding.EncodeToString
	return confirmation{
		surface: SurfaceSeat,
		session: rec.Seat.Session,
		own: map[string]any{
			"client_data_json": b64(v.ClientDataJSON),
			"nonce":            hex.EncodeToString(v.Nonce),
			"signature":        b64(v.Signature),
		},
		activity: map[string]any{
			"authenticator_data": b64(v.AuthenticatorData),
			"credential_id":      b64(v.CredentialID),
			"instance":           v.Instance,
			"origin":             v.Origin,
		},
	}, nil
}

// with returns fields and more in one map, for a body.
func with(fields, more map[string]any) map[string]any {
	for k, v := range more {
		fields[k] = v
	}
	return fields
}

// EvidenceOf returns the evidence of the passkey assertion that confirmed the
// decision or held entry seq, read back from the entry and its activity
// entry. It answers [ErrNoEvidence] for an entry confirmed by code, and
// [ErrDamaged] when what it reads is not whole, or is not of the assertion,
// the credential and the gate session the entry's record names, or its
// activity entry does not say the surface seat with no desk activity. The
// nonce and the instance are in no record: what checks them is the
// challenge an auditor recomputes from the record and this evidence, and the
// signature he checks with the credential's key.
func (s *Store) EvidenceOf(seq int64) (SeatEvidence, error) {
	var out SeatEvidence
	err := s.read(func(q querier) error {
		entries, err := queryEntries(q, "SELECT "+entryColumns+" FROM entries WHERE seq = ? AND kind IN ('decision', 'held')", seq)
		if err != nil {
			return err
		}
		if len(entries) != 1 {
			return fmt.Errorf("ownerjournal: entry %d is not a decision or a held entry", seq)
		}
		e := entries[0]
		rec, _, err := readDecision(e.Record)
		if err != nil {
			return fmt.Errorf("%w: the record of entry %d cannot be read", ErrDamaged, e.Seq)
		}
		if rec.Confirmed != record.ConfirmedPasskey {
			return ErrNoEvidence
		}
		var own struct {
			Approve          bool   `json:"approve"`
			BindingSHA256    string `json:"binding_sha256"`
			PendingExpiresAt string `json:"pending_expires_at"`
			signedBody
		}
		if err := parseBody(e, &own); err != nil {
			return err
		}
		activities, err := related(q, e.Seq, KindActivity)
		if err != nil {
			return err
		}
		if len(activities) != 1 {
			return fmt.Errorf("%w: entry %d has no activity entry", ErrDamaged, e.Seq)
		}
		var act activityBody
		if err := parseBody(activities[0], &act); err != nil {
			return err
		}
		damaged := fmt.Errorf("%w: the evidence of entry %d is not whole", ErrDamaged, e.Seq)
		b64 := base64.StdEncoding.Strict().DecodeString
		var v SeatEvidence
		var errs [5]error
		v.ClientDataJSON, errs[0] = b64(own.ClientDataJSON)
		v.Signature, errs[1] = b64(own.Signature)
		v.Nonce, errs[2] = hex.DecodeString(own.Nonce)
		v.AuthenticatorData, errs[3] = b64(act.AuthenticatorData)
		v.CredentialID, errs[4] = b64(act.CredentialID)
		v.Instance, v.Origin = act.Instance, act.Origin
		if errors.Join(errs[:]...) != nil || !validHex(own.Nonce, 2*evidenceNonceBytes) || v.check() != nil {
			return damaged
		}
		if act.Surface != SurfaceSeat || act.Session != rec.Seat.Session || act.LastActivity != "" ||
			v.digest() != rec.Seat.AssertionSHA256 || KeyDisplayID(v.CredentialID) != rec.Seat.Credential {
			return damaged
		}
		out = v
		return nil
	})
	return out, err
}
