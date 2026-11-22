package record

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"strconv"
)

// DecodeRequest reads a request from its bytes and checks it against the
// record rules and its form. A request over [MaxRequestBytes] is refused
// record-too-large; a feature finish that does not name exactly one valid
// feature feature-name; a verb outside the closed list
// verb-not-requestable; anything else request-invalid.
func DecodeRequest(data []byte) (Request, error) {
	var r Request
	if err := decode(data, MaxRequestBytes, CodeRequestInvalid, "request", &r); err != nil {
		return Request{}, err
	}
	if err := checkRequest(&r); err != nil {
		return Request{}, err
	}
	return r, nil
}

// DecodePending reads a pending record from its bytes and checks it against
// the record rules and its form. A record over [MaxPendingBytes], or whose
// note is over [MaxNoteBytes], is refused record-too-large; a feature finish
// that does not name exactly one valid feature feature-name; a verb outside
// the closed list verb-not-requestable; a binding or a plan text that does
// not match its digest binding-mismatch; anything else record-invalid. The
// verb and the feature name are checked by the rule a request is checked
// by, so they are refused with the same two codes in both records.
//
// The parts of the binding are checked against each other as well, and a
// binding whose parts disagree is record-invalid: the one effect of a
// feature finish moves a branch from the merge's first parent to the merge
// commit, which are also the two commits the summary compares; every effect
// of a prune deletes a branch. So a caller that holds a decoded record need
// not compare the effects with the merge and the summary again.
func DecodePending(data []byte) (Pending, error) {
	var p Pending
	if err := decode(data, MaxPendingBytes, CodeRecordInvalid, "pending", &p); err != nil {
		return Pending{}, err
	}
	if err := checkPending(&p); err != nil {
		return Pending{}, err
	}
	return p, nil
}

// DecodeDecision reads a decision from its bytes and checks it against the
// record rules and its form. A decision over [MaxDecisionBytes] is refused
// record-too-large; anything else, a reason over [MaxReasonBytes] included,
// decision-invalid. A decision carries exactly one of two blocks, as its
// confirmed says: the console block for [ConfirmedCode] and the seat block
// for [ConfirmedPasskey]; one with both, with neither, or with the other
// block is decision-invalid. Whether the decision fits a pending record (its
// digest, its sign-offs) is for the caller that holds both; see
// [SignOffsEqual].
func DecodeDecision(data []byte) (Decision, error) {
	var d Decision
	if err := decode(data, MaxDecisionBytes, CodeDecisionInvalid, "decision", &d); err != nil {
		return Decision{}, err
	}
	if err := checkDecision(&d); err != nil {
		return Decision{}, err
	}
	return d, nil
}

// EncodeRequest returns the canonical bytes of r. It makes the checks of
// [DecodeRequest], so what it returns always decodes.
func EncodeRequest(r Request) ([]byte, error) {
	if err := checkRequest(&r); err != nil {
		return nil, err
	}
	return encode(&r, MaxRequestBytes, CodeRequestInvalid, "request")
}

// EncodePending returns the canonical bytes of p. It makes the checks of
// [DecodePending], so what it returns always decodes: the caller fills
// Binding.PlanSHA256 with [PlanDigest] and BindingSHA256 with
// [BindingDigest] first.
func EncodePending(p Pending) ([]byte, error) {
	if err := checkPending(&p); err != nil {
		return nil, err
	}
	return encode(&p, MaxPendingBytes, CodeRecordInvalid, "pending")
}

// EncodeDecision returns the canonical bytes of d. It makes the checks of
// [DecodeDecision], so what it returns always decodes.
func EncodeDecision(d Decision) ([]byte, error) {
	if err := checkDecision(&d); err != nil {
		return nil, err
	}
	return encode(&d, MaxDecisionBytes, CodeDecisionInvalid, "decision")
}

// decode parses data under the record rules and fills dst, a pointer to a
// record type. limit is the cap of the kind, invalid the code for a record
// that is not well formed, and name the kind's name for the detail.
func decode(data []byte, limit int, invalid Code, name string, dst any) error {
	if len(data) > limit {
		return tooLarge(limit)
	}
	v, err := parse(data)
	if err != nil {
		return &Error{Code: invalid, Detail: err.Error()}
	}
	if err := fromValue(v, reflect.ValueOf(dst).Elem(), name); err != nil {
		return &Error{Code: invalid, Detail: err.Error()}
	}
	return nil
}

// encode writes src, a pointer to a record type, in the canonical form and
// holds the cap.
func encode(src any, limit int, invalid Code, name string) ([]byte, error) {
	out, err := appendValue(nil, reflect.ValueOf(src).Elem(), name)
	if err != nil {
		return nil, &Error{Code: invalid, Detail: err.Error()}
	}
	if len(out) > limit {
		return nil, tooLarge(limit)
	}
	return out, nil
}

func tooLarge(limit int) *Error {
	return &Error{Code: CodeRecordTooLarge, Detail: "the record is larger than " + strconv.Itoa(limit) + " bytes"}
}

// BindingDigest returns the digest a decision is bound to: the SHA-256, in
// lower-case hexadecimal, of the canonical form of b. It does not check b
// against the form; it fails only when a plain string of b cannot be
// written (a control character, or bytes that are not UTF-8).
func BindingDigest(b Binding) (string, error) {
	out, err := appendValue(nil, reflect.ValueOf(&b).Elem(), "binding")
	if err != nil {
		return "", &Error{Code: CodeRecordInvalid, Detail: err.Error()}
	}
	return Digest(out), nil
}

// PlanDigest returns the digest of a plan text: the SHA-256 of its raw
// bytes, not of its JSON form.
func PlanDigest(planText string) string {
	return Digest([]byte(planText))
}

// Digest returns the SHA-256 of data in lower-case hexadecimal. The digest
// of a record is taken over its bytes as they travelled.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// AssertionDigest returns the assertion_sha256 of a seat block: the SHA-256,
// in lower-case hexadecimal, of the four fields of a WebAuthn assertion, each
// with its length in front as four bytes, big-endian:
//
//	F(credential_id) || F(authenticator_data) || F(client_data_json) || F(signature)
//	F(x) = BE32(len(x)) || x
//
// So no two assertions give the same bytes. It checks nothing of the fields.
func AssertionDigest(credentialID, authenticatorData, clientDataJSON, signature []byte) string {
	h := sha256.New()
	var n [4]byte
	for _, part := range [][]byte{credentialID, authenticatorData, clientDataJSON, signature} {
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ValidFeatureName reports whether s can be a feature name: 1 to 48 bytes
// of lower-case ASCII letters, digits and dashes. A name that passes may
// stand in the host's own lines; it can still be chosen to mislead, so a
// surface never lets it stand for anything but a name.
func ValidFeatureName(s string) bool {
	return len(s) >= 1 && len(s) <= 48 && all(s, isNameByte)
}

// ValidRequestID reports whether s is a request id: "rq-" and then 1 to 48
// bytes of lower-case ASCII letters, digits and dashes.
func ValidRequestID(s string) bool {
	return len(s) > 3 && s[:3] == "rq-" && ValidFeatureName(s[3:])
}

// ValidPendingID reports whether s is a pending id: "ap-" and then 1 to 48
// bytes of lower-case ASCII letters, digits and dashes.
func ValidPendingID(s string) bool {
	return len(s) > 3 && s[:3] == "ap-" && ValidFeatureName(s[3:])
}

// ValidObjectID reports whether s is a full git object id: 40 or 64
// lower-case hexadecimal digits.
func ValidObjectID(s string) bool {
	return (len(s) == 40 || len(s) == 64) && all(s, isHexByte)
}

// SignOffsEqual reports whether signed is exactly the sign-off of flagged:
// the same (kind, path) pairs, in flagged's order, with no pair twice. A
// surface builds a decision's sign-offs in that order.
func SignOffsEqual(flagged []Flag, signed []SignOff) bool {
	if len(flagged) != len(signed) {
		return false
	}
	seen := make(map[SignOff]struct{}, len(flagged))
	for i, f := range flagged {
		pair := SignOff{Kind: f.Kind, Path: f.Path}
		if _, dup := seen[pair]; dup || signed[i] != pair {
			return false
		}
		seen[pair] = struct{}{}
	}
	return true
}

func all(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

func isNameByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}
