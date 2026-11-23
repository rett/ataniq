package ownerwire

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"

	"ataniq/internal/approval/record"
)

// This file holds the words of the five operations only the seat gate serves:
// a login and a decision from the owner's phone, each a WebAuthn assertion
// over a challenge the daemon draws, and the long wait that tells the seat
// when the pending cards changed. See docs/design/seat-gate.md, "The
// challenge" and "Login".

// The bounds of the seat gate's forms.
const (
	// MaxSeatChallengeParamsBytes caps the params of seat.challenge: a
	// decision frame and a credential's display id.
	MaxSeatChallengeParamsBytes = MaxFrameBytes + 1<<10
	// MaxSeatDecideParamsBytes caps the params of seat.decide: a decision
	// frame, a challenge id and an assertion.
	MaxSeatDecideParamsBytes = MaxFrameBytes + 8<<10
	// MaxSeatLoginParamsBytes caps the params of seat.login: a challenge id
	// and an assertion.
	MaxSeatLoginParamsBytes = 8 << 10
	// MaxSeatLoginChallengeBytes caps the result of seat.login-challenge,
	// which carries up to [MaxLoginAllow] raw credential ids beside the
	// challenge: room for the longest of each, in base64.
	MaxSeatLoginChallengeBytes = 32 << 10

	// ChallengeIDLength is the length of a challenge id: 16 lower-case
	// hexadecimal digits, 8 bytes the daemon draws at random.
	ChallengeIDLength = 16
	// ChallengeBytes is the length of a challenge, a SHA-256.
	ChallengeBytes = sha256.Size
	// NonceBytes is the length of a challenge's nonce, which the daemon
	// draws at random.
	NonceBytes = 32
	// InstanceLength is the length of an instance: the gate's own key
	// fingerprint, 64 lower-case hexadecimal digits.
	InstanceLength = 64

	// MinAuthenticatorDataBytes and MaxAuthenticatorDataBytes bound the
	// authenticator data of an assertion: its head at least (the relying
	// party hash, the flags and the counter), and at most 1 KiB.
	MinAuthenticatorDataBytes = 37
	MaxAuthenticatorDataBytes = 1 << 10
	// MaxClientDataBytes caps the client data of an assertion.
	MaxClientDataBytes = 2 << 10
	// MaxSignatureBytes caps the signature of an assertion: the longest DER
	// form of an ECDSA signature on P-256.
	MaxSignatureBytes = 72
	// MaxLoginAllow is the most credentials a login challenge allows.
	MaxLoginAllow = 16

	// MaxSeatWaitResultBytes caps the result of seat.wait. The longest
	// result, [MaxSeatWaitCards] cards of the longest id and kind, fits
	// with room to spare.
	MaxSeatWaitResultBytes = 24 << 10
	// MaxSeatWaitCards is the most cards one seat.wait result lists. The
	// version and the count cover the whole pending set, and "more" says
	// that the list is not all of it.
	MaxSeatWaitCards = 128
	// MaxSeatWaitKindBytes is the longest kind of a card in a seat.wait
	// result.
	MaxSeatWaitKindBytes = 16
	// SeatWaitVersionLength is the length of a pending-set version: 32
	// lower-case hexadecimal digits, 16 bytes.
	SeatWaitVersionLength = 32
	// CardKindApproval is the kind of a card that asks for the owner's
	// decision on an approval record. Every pending card is of it today.
	CardKindApproval = "approval"
)

// The words the seat gate answers its operations with when it refuses
// one, beside the codes of the transport, of the gate's handler and of the
// service. Each is of the grammar of a gate code, [a-z0-9-]{1,40}, and is
// never a text for a person. They are the gate's words, not refusals of a
// record: no decoder of this package answers with them.
const (
	// CodeChallengeStale answers a call whose challenge id names no
	// outstanding challenge of its kind and gate session (unknown, used,
	// expired, of another session, dropped by a restart), or whose frame is
	// not the one the challenge was drawn for.
	CodeChallengeStale = "challenge-stale"
	// CodeChallengesFull answers a seat.challenge while every room for a
	// decision challenge is taken and none can be replaced.
	CodeChallengesFull = "challenges-full"
	// CodeAssertionInvalid answers an assertion that names another
	// credential than the challenge's or does not verify, for any reason.
	CodeAssertionInvalid = "assertion-invalid"
	// CodeKeyRevoked answers a credential that is on the list of deciding
	// keys and revoked.
	CodeKeyRevoked = "key-revoked"
	// CodeKindRefused answers a credential whose stored kind the verifier
	// does not know, or that the project does not accept.
	CodeKindRefused = "kind-refused"
	// CodeWaitFull answers a seat.wait while the gate already holds as many
	// waits for that seat as it allows. It is a word of the gate's own
	// bound, not a refusal of what the seat sent.
	CodeWaitFull = "wait-full"
)

// Assertion is a WebAuthn assertion as the phone's browser gave it and the
// seat relays it: the four fields of an AuthenticatorAssertionResponse, as
// raw bytes (standard base64 in the JSON). The user handle is not carried.
// Whether the bytes verify is for the daemon's verifier; the decoders check
// their lengths only.
type Assertion struct {
	// CredentialID is the id of the credential that signed,
	// [MinCredentialIDBytes] to [MaxCredentialIDBytes].
	CredentialID []byte `json:"credential_id"`
	// AuthenticatorData is the authenticator data,
	// [MinAuthenticatorDataBytes] to [MaxAuthenticatorDataBytes].
	AuthenticatorData []byte `json:"authenticator_data"`
	// ClientDataJSON is the client data, 1 to [MaxClientDataBytes].
	ClientDataJSON []byte `json:"client_data_json"`
	// Signature is the DER signature, 1 to [MaxSignatureBytes].
	Signature []byte `json:"signature"`
}

// Digest returns the digest a passkey decision record keeps of the
// assertion, its seat block's assertion_sha256: [record.AssertionDigest] of
// the four fields.
func (a Assertion) Digest() string {
	return record.AssertionDigest(a.CredentialID, a.AuthenticatorData, a.ClientDataJSON, a.Signature)
}

// SeatChallengeParams is the params of seat.challenge: the decision the
// owner chose on the phone, and the credential of the seat's session.
type SeatChallengeParams struct {
	// Frame is the decision, as the seat built it from its own copy of the
	// card.
	Frame DecisionFrame `json:"frame"`
	// Credential is the display id of the session's credential,
	// [KeyIDLength] lower-case hexadecimal digits.
	Credential string `json:"credential"`
}

// SeatChallengeResult is the result of seat.challenge: the challenge the
// phone is to sign. The seat recomputes the challenge from the card it
// showed, the gate's pin and the nonce, and asks nothing of the phone when
// they differ.
type SeatChallengeResult struct {
	// ChallengeID names the challenge, [ChallengeIDLength] lower-case
	// hexadecimal digits.
	ChallengeID string `json:"challenge_id"`
	// Challenge is [DecisionChallenge] of the frame, [ChallengeBytes].
	Challenge []byte `json:"challenge"`
	// Nonce is the daemon's nonce in it, [NonceBytes].
	Nonce []byte `json:"nonce"`
	// ExpiresAt is when the challenge ends unused, a record's time.
	ExpiresAt string `json:"expires_at"`
}

// SeatLoginChallengeResult is the result of seat.login-challenge (whose
// params are none): a login challenge and the credentials that may answer it.
type SeatLoginChallengeResult struct {
	// ChallengeID, Challenge, Nonce and ExpiresAt are as in
	// [SeatChallengeResult]; Challenge is [LoginChallenge] of the nonce.
	ChallengeID string `json:"challenge_id"`
	Challenge   []byte `json:"challenge"`
	Nonce       []byte `json:"nonce"`
	ExpiresAt   string `json:"expires_at"`
	// Allow are the raw ids of the credentials that may log in (on the list
	// of deciding keys, not revoked, of a kind the verifier knows), newest
	// first, at most [MaxLoginAllow], none twice. The seat hands them to the
	// browser as allowCredentials. It may be empty.
	Allow [][]byte `json:"allow"`
}

// SeatDecideParams is the params of seat.decide: the decision, the
// challenge it was drawn for, and the assertion over that challenge.
//
// It is read in two parts, so that the daemon burns the challenge before it
// judges anything of the content. [DecodeSeatDecideParams] reads the
// envelope: the members and their JSON types, the challenge id's grammar,
// each byte string's base64 and length, and the whole within its cap. The
// frame's content is checked afterwards with [CheckDecisionFrame].
type SeatDecideParams struct {
	// ChallengeID names the challenge, as [SeatChallengeResult] gave it.
	ChallengeID string `json:"challenge_id"`
	// Frame is the decision, the same the challenge was drawn for.
	Frame DecisionFrame `json:"frame"`
	// Assertion is the phone's assertion over the challenge.
	Assertion Assertion `json:"assertion"`
}

// SeatLoginParams is the params of seat.login: a login challenge's id and
// the assertion over it.
type SeatLoginParams struct {
	// ChallengeID names the login challenge.
	ChallengeID string `json:"challenge_id"`
	// Assertion is the phone's assertion over it.
	Assertion Assertion `json:"assertion"`
}

// SeatLoginResult is the result of seat.login: the credential that logged
// in. The daemon keeps no session; the seat makes one from this answer.
type SeatLoginResult struct {
	// Credential is the credential's display id, [KeyIDLength] lower-case
	// hexadecimal digits: the first 8 bytes of the SHA-256 of CredentialID.
	Credential string `json:"credential"`
	// CredentialID is the raw credential id, which the seat puts in the
	// allowCredentials of each decision of the session.
	CredentialID []byte `json:"credential_id"`
	// Kind is the credential's stored kind, a word.
	Kind string `json:"kind"`
}

// SeatWaitParams is the params of seat.wait: the version of the pending set
// the seat last saw.
type SeatWaitParams struct {
	// Since is a version that [PendingVersion] gave, [SeatWaitVersionLength]
	// lower-case hexadecimal digits, or empty when the seat has none. The
	// gate answers at once when the pending set's version is not Since, and
	// otherwise holds the answer until it changes or the hold ends. The
	// field is always written.
	Since string `json:"since"`
}

// SeatWaitCard is one pending card in a seat.wait result: what the seat
// needs to tell one card from another, and nothing of its text.
type SeatWaitCard struct {
	// ID is the pending record's id.
	ID string `json:"id"`
	// Kind is the card's kind, a word of at most [MaxSeatWaitKindBytes]
	// bytes; today only [CardKindApproval].
	Kind string `json:"kind"`
	// BindingSHA256 is the binding digest a decision on the card names.
	BindingSHA256 string `json:"binding_sha256"`
	// Held is the mark "decided past the limit", as in [PendingItem].
	Held bool `json:"held"`
}

// SeatWaitResult is the result of seat.wait: where the pending set stands.
type SeatWaitResult struct {
	// Version is [PendingVersion] of the whole pending set,
	// [SeatWaitVersionLength] lower-case hexadecimal digits.
	Version string `json:"version"`
	// Waiting is how many cards are pending, the whole set.
	Waiting int `json:"waiting"`
	// Cards lists the pending cards, at most [MaxSeatWaitCards], none twice.
	Cards []SeatWaitCard `json:"cards"`
	// More is true when Waiting is more than the cards listed, and false
	// when it is exactly their number.
	More bool `json:"more"`
}

var (
	seatChallengeParamsForm = payload[SeatChallengeParams]{"seat challenge", MaxSeatChallengeParamsBytes, record.CodeRecordInvalid, checkSeatChallengeParams}
	seatChallengeResultForm = payload[SeatChallengeResult]{"seat challenged", MaxResultBytes, record.CodeRecordInvalid, checkSeatChallengeResult}
	seatLoginChallengeForm  = payload[SeatLoginChallengeResult]{"seat login challenge", MaxSeatLoginChallengeBytes, record.CodeRecordInvalid, checkSeatLoginChallengeResult}
	seatDecideParamsForm    = payload[SeatDecideParams]{"seat decide", MaxSeatDecideParamsBytes, record.CodeRecordInvalid, checkSeatDecideEnvelope}
	seatLoginParamsForm     = payload[SeatLoginParams]{"seat login", MaxSeatLoginParamsBytes, record.CodeRecordInvalid, checkSeatLoginParams}
	seatLoginResultForm     = payload[SeatLoginResult]{"seat logged in", MaxResultBytes, record.CodeRecordInvalid, checkSeatLoginResult}
	seatWaitParamsForm      = payload[SeatWaitParams]{"seat wait", MaxParamsBytes, record.CodeRecordInvalid, checkSeatWaitParams}
	seatWaitResultForm      = payload[SeatWaitResult]{"seat waited", MaxSeatWaitResultBytes, record.CodeRecordInvalid, checkSeatWaitResult}
)

// DecodeSeatChallengeParams reads the params of seat.challenge, whole: they
// burn nothing. Params over [MaxSeatChallengeParamsBytes] are refused
// record-too-large; a frame whose content is wrong decision-invalid, as
// [DecodeDecisionFrame] refuses it; anything else that is wrong, such as a
// credential that is not a display id, record-invalid.
func DecodeSeatChallengeParams(data []byte) (SeatChallengeParams, error) {
	return seatChallengeParamsForm.decode(data)
}

// EncodeSeatChallengeParams writes the params of seat.challenge. It makes the
// checks of [DecodeSeatChallengeParams].
func EncodeSeatChallengeParams(p SeatChallengeParams) ([]byte, error) {
	return seatChallengeParamsForm.encode(p)
}

// DecodeSeatChallengeResult reads the result of seat.challenge. A result over
// [MaxResultBytes] is refused record-too-large; anything else that is wrong,
// such as a challenge or a nonce of another length, record-invalid.
func DecodeSeatChallengeResult(data []byte) (SeatChallengeResult, error) {
	return seatChallengeResultForm.decode(data)
}

// EncodeSeatChallengeResult writes the result of seat.challenge.
func EncodeSeatChallengeResult(r SeatChallengeResult) ([]byte, error) {
	return seatChallengeResultForm.encode(r)
}

// DecodeSeatLoginChallengeResult reads the result of seat.login-challenge. A
// result over [MaxSeatLoginChallengeBytes] is refused record-too-large;
// anything else that is wrong, such as more than [MaxLoginAllow] credentials
// allowed or one allowed twice, record-invalid.
func DecodeSeatLoginChallengeResult(data []byte) (SeatLoginChallengeResult, error) {
	return seatLoginChallengeForm.decode(data)
}

// EncodeSeatLoginChallengeResult writes the result of seat.login-challenge.
func EncodeSeatLoginChallengeResult(r SeatLoginChallengeResult) ([]byte, error) {
	return seatLoginChallengeForm.encode(r)
}

// DecodeSeatDecideParams reads the envelope of the params of seat.decide: the
// members and their JSON types (the frame's among them), the challenge id's
// grammar, the assertion's lengths, and the whole within
// [MaxSeatDecideParamsBytes]. It does not check the frame's content: the
// caller takes the challenge first and then calls [CheckDecisionFrame]. Params
// over the cap are refused record-too-large; anything else that is wrong
// record-invalid.
func DecodeSeatDecideParams(data []byte) (SeatDecideParams, error) {
	return seatDecideParamsForm.decode(data)
}

// EncodeSeatDecideParams writes the params of seat.decide. It checks the
// frame's content as well as the envelope, so it writes only params that
// pass both parts of the reading: a frame whose content is wrong is refused
// decision-invalid.
func EncodeSeatDecideParams(p SeatDecideParams) ([]byte, error) {
	if err := CheckDecisionFrame(p.Frame); err != nil {
		return nil, err
	}
	return seatDecideParamsForm.encode(p)
}

// DecodeSeatLoginParams reads the params of seat.login: the challenge id's
// grammar and the assertion's lengths. Params over [MaxSeatLoginParamsBytes]
// are refused record-too-large; anything else that is wrong record-invalid.
func DecodeSeatLoginParams(data []byte) (SeatLoginParams, error) {
	return seatLoginParamsForm.decode(data)
}

// EncodeSeatLoginParams writes the params of seat.login.
func EncodeSeatLoginParams(p SeatLoginParams) ([]byte, error) {
	return seatLoginParamsForm.encode(p)
}

// DecodeSeatLoginResult reads the result of seat.login. A result over
// [MaxResultBytes] is refused record-too-large; anything else that is wrong,
// such as a display id that is not that of the credential id, record-invalid.
func DecodeSeatLoginResult(data []byte) (SeatLoginResult, error) {
	return seatLoginResultForm.decode(data)
}

// EncodeSeatLoginResult writes the result of seat.login.
func EncodeSeatLoginResult(r SeatLoginResult) ([]byte, error) {
	return seatLoginResultForm.encode(r)
}

// DecodeSeatWaitParams reads the params of seat.wait: a version that is empty
// or [SeatWaitVersionLength] lower-case hexadecimal digits. Params over
// [MaxParamsBytes] are refused record-too-large; anything else that is wrong
// record-invalid.
func DecodeSeatWaitParams(data []byte) (SeatWaitParams, error) {
	return seatWaitParamsForm.decode(data)
}

// EncodeSeatWaitParams writes the params of seat.wait.
func EncodeSeatWaitParams(p SeatWaitParams) ([]byte, error) {
	return seatWaitParamsForm.encode(p)
}

// DecodeSeatWaitResult reads the result of seat.wait. A result over
// [MaxSeatWaitResultBytes] is refused record-too-large; anything else that is
// wrong, such as more than [MaxSeatWaitCards] cards, a card twice, or a count
// that does not fit the cards and "more", record-invalid.
func DecodeSeatWaitResult(data []byte) (SeatWaitResult, error) {
	return seatWaitResultForm.decode(data)
}

// EncodeSeatWaitResult writes the result of seat.wait.
func EncodeSeatWaitResult(r SeatWaitResult) ([]byte, error) {
	return seatWaitResultForm.encode(r)
}

// pendingVersionDomain starts the bytes [PendingVersion] hashes, so that no
// other digest of this package can be taken for a version.
const pendingVersionDomain = "ataniq.seat-wait.pending/1"

// PendingVersion returns the version of a pending set: the first 16 bytes, in
// lower-case hexadecimal, of the SHA-256 of a canonical encoding of the set.
// It is a digest, not a counter, so a daemon that restarts with the same
// pending set answers the same version, and a seat that held it is not woken
// for nothing.
//
// The encoding is the domain string, then the number of cards, then each card
// in the order of its kind, id, binding digest and held mark, and every field
// is a 4-byte big-endian length and its bytes (the held mark is one byte, 0 or
// 1), so that no two sets can write the same bytes. The order of items does
// not matter and the argument is not changed. An empty set has a version too.
//
// The daemon gives it the whole pending set, not the at most [MaxSeatWaitCards]
// cards of a result, so that a change past the list still changes the version.
// It checks nothing: what it is given is hashed as it is.
func PendingVersion(items []SeatWaitCard) string {
	sorted := append([]SeatWaitCard(nil), items...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		switch {
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		case a.ID != b.ID:
			return a.ID < b.ID
		case a.BindingSHA256 != b.BindingSHA256:
			return a.BindingSHA256 < b.BindingSHA256
		}
		return !a.Held && b.Held
	})
	h := sha256.New()
	field := func(s string) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	field(pendingVersionDomain)
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(sorted)))
	h.Write(count[:])
	for _, card := range sorted {
		field(card.Kind)
		field(card.ID)
		field(card.BindingSHA256)
		held := byte(0)
		if card.Held {
			held = 1
		}
		h.Write([]byte{held})
	}
	return hex.EncodeToString(h.Sum(nil)[:SeatWaitVersionLength/2])
}

// CheckDecisionFrame checks the content of a decision frame, as
// [DecodeDecisionFrame] does after it read the form: a pending id, a binding
// digest, approve or deny, no reason for an approve and no sign-off for a
// deny, a reason within [record.MaxReasonBytes], sign-offs of known kinds and
// none twice. A frame that is wrong is refused decision-invalid. It is the
// second part of reading the params of seat.decide.
func CheckDecisionFrame(f DecisionFrame) error {
	return checkDecisionFrame(&f)
}

// challengeID checks a challenge id's grammar.
func (c *checker) challengeID(s, where string) {
	c.need(record.IsLowerHex(s, ChallengeIDLength), where+" is not "+strconv.Itoa(ChallengeIDLength)+" lower-case hexadecimal digits")
}

// bytesBetween checks the length of a byte string.
func (c *checker) bytesBetween(b []byte, min, max int, where string) {
	c.need(len(b) >= min && len(b) <= max, where+" is not "+strconv.Itoa(min)+" to "+strconv.Itoa(max)+" bytes")
}

// credentialID checks a raw credential id's length, the bounds of a pairing.
func (c *checker) credentialID(b []byte, where string) {
	c.bytesBetween(b, MinCredentialIDBytes, MaxCredentialIDBytes, where)
}

func (c *checker) assertion(a *Assertion, where string) {
	c.credentialID(a.CredentialID, where+".credential_id")
	c.bytesBetween(a.AuthenticatorData, MinAuthenticatorDataBytes, MaxAuthenticatorDataBytes, where+".authenticator_data")
	c.bytesBetween(a.ClientDataJSON, 1, MaxClientDataBytes, where+".client_data_json")
	c.bytesBetween(a.Signature, 1, MaxSignatureBytes, where+".signature")
}

// drawn checks what a challenge's answer carries: its id, the challenge, the
// nonce and the expiry.
func (c *checker) drawn(id string, challenge, nonce []byte, expiresAt, where string) {
	c.challengeID(id, where+".challenge_id")
	c.need(len(challenge) == ChallengeBytes, where+".challenge is not "+strconv.Itoa(ChallengeBytes)+" bytes")
	c.need(len(nonce) == NonceBytes, where+".nonce is not "+strconv.Itoa(NonceBytes)+" bytes")
	c.time(expiresAt, where+".expires_at")
}

func checkSeatChallengeParams(p *SeatChallengeParams) error {
	if err := checkDecisionFrame(&p.Frame); err != nil {
		return err
	}
	c := &checker{invalid: record.CodeRecordInvalid}
	c.need(record.IsLowerHex(p.Credential, KeyIDLength), "seat.credential is not "+strconv.Itoa(KeyIDLength)+" lower-case hexadecimal digits")
	return c.result()
}

func checkSeatChallengeResult(r *SeatChallengeResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.drawn(r.ChallengeID, r.Challenge, r.Nonce, r.ExpiresAt, "seat")
	return c.result()
}

func checkSeatLoginChallengeResult(r *SeatLoginChallengeResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.drawn(r.ChallengeID, r.Challenge, r.Nonce, r.ExpiresAt, "seat")
	c.need(len(r.Allow) <= MaxLoginAllow, "seat.allow has more than "+strconv.Itoa(MaxLoginAllow)+" credentials")
	seen := make(map[string]struct{}, len(r.Allow))
	for i, id := range r.Allow {
		at := "seat.allow[" + strconv.Itoa(i) + "]"
		c.credentialID(id, at)
		_, dup := seen[string(id)]
		c.need(!dup, at+" repeats an earlier credential")
		seen[string(id)] = struct{}{}
	}
	return c.result()
}

// checkSeatDecideEnvelope is the first part of reading seat.decide: all but
// the frame's content.
func checkSeatDecideEnvelope(p *SeatDecideParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.challengeID(p.ChallengeID, "seat.challenge_id")
	c.assertion(&p.Assertion, "seat.assertion")
	return c.result()
}

func checkSeatLoginParams(p *SeatLoginParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.challengeID(p.ChallengeID, "seat.challenge_id")
	c.assertion(&p.Assertion, "seat.assertion")
	return c.result()
}

func checkSeatLoginResult(r *SeatLoginResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.credentialID(r.CredentialID, "seat.credential_id")
	c.need(r.Credential == displayID(r.CredentialID), "seat.credential is not the display id of seat.credential_id")
	c.word(r.Kind, "seat.kind")
	return c.result()
}

func checkSeatWaitParams(p *SeatWaitParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.need(p.Since == "" || record.IsLowerHex(p.Since, SeatWaitVersionLength), "seat.since is neither empty nor "+strconv.Itoa(SeatWaitVersionLength)+" lower-case hexadecimal digits")
	return c.result()
}

func checkSeatWaitResult(r *SeatWaitResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.need(record.IsLowerHex(r.Version, SeatWaitVersionLength), "seat.version is not "+strconv.Itoa(SeatWaitVersionLength)+" lower-case hexadecimal digits")
	c.need(len(r.Cards) <= MaxSeatWaitCards, "seat.cards has more than "+strconv.Itoa(MaxSeatWaitCards)+" cards")
	c.need(r.Waiting >= len(r.Cards), "seat.waiting is less than the cards listed")
	c.need(r.More == (r.Waiting > len(r.Cards)), "seat.more does not say whether seat.waiting is more than the cards listed")
	seen := make(map[string]struct{}, len(r.Cards))
	for i := range r.Cards {
		card, at := &r.Cards[i], "seat.cards["+strconv.Itoa(i)+"]"
		c.pendingID(card.ID, at+".id")
		_, dup := seen[card.ID]
		c.need(!dup, at+" repeats an earlier id")
		seen[card.ID] = struct{}{}
		c.word(card.Kind, at+".kind")
		c.need(len(card.Kind) <= MaxSeatWaitKindBytes, at+".kind is longer than "+strconv.Itoa(MaxSeatWaitKindBytes)+" bytes")
		c.sha256(card.BindingSHA256, at+".binding_sha256")
	}
	return c.result()
}

// displayID is a credential's display id: the first 8 bytes of the SHA-256
// of its id, in lower-case hexadecimal.
func displayID(credentialID []byte) string {
	sum := sha256.Sum256(credentialID)
	return hex.EncodeToString(sum[:KeyIDLength/2])
}
