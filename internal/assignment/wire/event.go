package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"ataniq/internal/assignment"
)

const Namespace = "ataniq.assignment.event.v1"
const domain = Namespace + "\x00"

var errProjection = errors.New("assignment wire: invalid schema-1 projection")

// Digests in the public projection use lowercase hexadecimal SHA-256.
func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// Marshal returns the canonical public object without framing. Leave Signature
// empty for a signing body. The signed envelope carries the unpadded base64url SSHSIG blob.
func Marshal(p assignment.PublicEnvelope) ([]byte, error) {
	if p.Schema != 1 || p.Algorithm != "Ed25519" || p.EvidenceDigests == nil {
		return nil, errProjection
	}
	switch p.Kind {
	case assignment.Seed, assignment.Freeze, assignment.Inventory, assignment.BeliefAccepted,
		assignment.Reserve, assignment.ReceiptReconciled, assignment.FenceReceipt, assignment.Fenced,
		assignment.Commit, assignment.ConfirmReceipt, assignment.Open, assignment.HoldStarted,
		assignment.HoldReviewed, assignment.AbortPrepared, assignment.Aborted, assignment.AbortedClosed,
		assignment.RollbackFreeze:
	default:
		return nil, errProjection
	}
	switch p.Phase {
	case assignment.PhaseReceiptDisputed, assignment.PhaseFrozen, assignment.PhaseInventoried,
		assignment.PhaseBeliefVerified, assignment.PhaseReserved, assignment.PhasePartiallyFenced,
		assignment.PhaseFenced, assignment.PhaseCommittedClosed, assignment.PhaseConfirmed,
		assignment.PhaseAborting, assignment.PhaseAborted, assignment.PhaseAbortedClosed:
	default:
		return nil, errProjection
	}
	for _, s := range [...]string{p.CommitRevision, p.Generation, p.PreviousGeneration, p.PreviousRevision, p.Revision} {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != s {
			return nil, errProjection
		}
	}
	if !validDigest(p.EventID) || (p.InventoryDigest != "" && !validDigest(p.InventoryDigest)) ||
		(p.PreviousPublicDigest != "" && !validDigest(p.PreviousPublicDigest)) {
		return nil, errProjection
	}
	if len(p.EvidenceDigests) > recordLimit/64 {
		return nil, assignment.ErrLimit
	}
	for _, s := range p.EvidenceDigests {
		if !validDigest(s) {
			return nil, errProjection
		}
	}
	e := assignment.Event{Public: p, Signature: p.Signature}
	if p.Signature != "" {
		return e.SignedPublicBytes()
	}
	message, err := e.SigningBytes()
	if err != nil {
		return nil, err
	}
	body, ok := bytes.CutPrefix(message, []byte(domain))
	if !ok {
		return nil, errProjection
	}
	return body, nil
}

// Unmarshal reads one bare public object, without a journal LF or trailing
// data. Member order and intra-object spacing do not affect its signing bytes.
// Authentication and state transition checks belong to the caller.
func Unmarshal(b []byte) (assignment.PublicEnvelope, error) {
	var p assignment.PublicEnvelope
	if len(b) > recordLimit {
		return p, assignment.ErrLimit
	}
	if len(b) == 0 || b[0] != '{' || b[len(b)-1] != '}' || bytes.ContainsAny(b, "\r\n") {
		return p, errProjection
	}
	canonical, err := Canonicalize(b)
	if err != nil {
		return p, err
	}
	return unmarshalCanonical(canonical)
}

func unmarshalCanonical(canonical []byte) (assignment.PublicEnvelope, error) {
	var p assignment.PublicEnvelope
	if len(canonical) > recordLimit {
		return p, assignment.ErrLimit
	}
	if err := json.Unmarshal(canonical, &p); err != nil {
		return assignment.PublicEnvelope{}, err
	}
	// Byte equality with the typed representation checks exact field names,
	// required members and types (including null) at every depth.
	typed, err := Marshal(p)
	if err != nil {
		return assignment.PublicEnvelope{}, err
	}
	if !bytes.Equal(typed, canonical) {
		return assignment.PublicEnvelope{}, errProjection
	}
	return p, nil
}
