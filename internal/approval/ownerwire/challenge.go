package ownerwire

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strconv"

	"ataniq/internal/approval/record"
)

// The domain strings that open a challenge's hashed bytes. They differ, so
// an assertion made for a login can never be taken as one for a decision,
// and the reverse.
const (
	// DecisionDomain opens the bytes of a decision challenge.
	DecisionDomain = "ataniq.seat.decision/1"
	// LoginDomain opens the bytes of a login challenge.
	LoginDomain = "ataniq.seat.login/1"
)

// DecisionChallenge returns the challenge the phone signs for a decision:
//
//	SHA-256( DecisionDomain
//	         || F(instance) || F(id) || F(binding_sha256) || F(decision)
//	         || F(signed_off_sha256) || F(reason_sha256)
//	         || nonce )
//	F(x) = BE32(len(x)) || x
//
// instance is the gate's own key fingerprint ([InstanceLength] lower-case
// hexadecimal digits), the id, the binding digest and the decision are the
// frame's, signed_off_sha256 is [SignedOffDigest] of its sign-offs and
// reason_sha256 is [ReasonDigest] of its reason, each as its ASCII, and nonce
// is the [NonceBytes] raw bytes, last. The daemon computes it from the frame
// it holds the challenge for; the seat computes it again from the card it
// showed, and both must get the same bytes.
//
// It refuses a frame whose content is wrong decision-invalid, as
// [CheckDecisionFrame] does, and an instance or a nonce of another form
// record-invalid.
func DecisionChallenge(instance string, f DecisionFrame, nonce []byte) ([]byte, error) {
	if err := CheckDecisionFrame(f); err != nil {
		return nil, err
	}
	if err := checkInstanceNonce(instance, nonce); err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte(DecisionDomain))
	for _, field := range []string{instance, f.ID, f.BindingSHA256, f.Decision, SignedOffDigest(f.SignedOff), ReasonDigest(f.Reason)} {
		framed(h, []byte(field))
	}
	h.Write(nonce)
	return h.Sum(nil), nil
}

// LoginChallenge returns the challenge the phone signs for a login:
//
//	SHA-256( LoginDomain || F(instance) || nonce )
//
// with instance and nonce as in [DecisionChallenge]. It refuses an instance or
// a nonce of another form record-invalid.
func LoginChallenge(instance string, nonce []byte) ([]byte, error) {
	if err := checkInstanceNonce(instance, nonce); err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte(LoginDomain))
	framed(h, []byte(instance))
	h.Write(nonce)
	return h.Sum(nil), nil
}

// SignedOffDigest returns the lower-case hexadecimal SHA-256 of
//
//	BE32(n) || F(kind_1) || F(path_1) || ... || F(kind_n) || F(path_n)
//
// over the sign-offs in their order, n being their number (0 for none, as for
// every deny). A path is taken as its raw bytes, the string a [record.Text]
// holds, not its JSON form, so a path that the record writes as b64 hashes as
// its bytes.
func SignedOffDigest(signed []record.SignOff) string {
	h := sha256.New()
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(signed)))
	h.Write(n[:])
	for _, s := range signed {
		framed(h, []byte(s.Kind))
		framed(h, []byte(s.Path))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ReasonDigest returns the lower-case hexadecimal SHA-256 of a reason's bytes
// as the record holds them, with no normalization: the digest of the empty
// string for none, as for every approve.
func ReasonDigest(reason string) string {
	sum := sha256.Sum256([]byte(reason))
	return hex.EncodeToString(sum[:])
}

// framed writes x with its length in front, four bytes big-endian.
func framed(h hash.Hash, x []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(x)))
	h.Write(n[:])
	h.Write(x)
}

func checkInstanceNonce(instance string, nonce []byte) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.need(record.IsLowerHex(instance, InstanceLength), "the instance is not "+strconv.Itoa(InstanceLength)+" lower-case hexadecimal digits")
	c.need(len(nonce) == NonceBytes, "the nonce is not "+strconv.Itoa(NonceBytes)+" bytes")
	return c.result()
}
