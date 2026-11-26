package checkpoint

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"ataniq/internal/assignment"
	"ataniq/internal/assignment/wire"
)

// Advance repeats at most 16 variable identifiers: audience (1), slot (3),
// ledger header (4), expected head (4), successor (4). JSON marshaling may
// escape each byte as six ASCII bytes before canonicalization. Reserve 2 KiB
// for member names, punctuation, fingerprints, digests and full-width counters.
const maxFrame = 16*6*assignment.MaxReferenceBytes + 2048

// MaxFrame is the byte ceiling shared by signed records and link frames.
const MaxFrame = maxFrame
const maxDepth = 8

func textOK(s string) bool {
	return s != "" && len(s) <= assignment.MaxReferenceBytes && utf8.ValidString(s)
}
func (s Slot) valid() bool { return textOK(s.Witness) && textOK(s.Authority) && textOK(s.Store) }
func digestOK(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

// Bound nesting before passing network or fixture bytes to a recursive decoder.
// Canonicalize handles JSON syntax, duplicate members and Unicode validation.
func canonical(raw []byte) ([]byte, error) {
	if len(raw) > maxFrame {
		return nil, &Reason{Kind: InvalidRequest}
	}
	depth := 0
	quoted, escaped := false, false
	for _, c := range raw {
		if quoted {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > maxDepth {
				return nil, &Reason{Kind: InvalidRequest}
			}
		case '}', ']':
			depth--
		}
	}
	b, err := wire.Canonicalize(raw)
	if err != nil {
		return nil, &Reason{Kind: InvalidRequest}
	}
	return b, nil
}
func encode(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, &Reason{Kind: InvalidRequest}
	}
	return canonical(raw)
}

// Re-encoding the typed record also enforces exact case, required members,
// decimal-string counters and one spelling for each signed record.
func decode(raw []byte, value any) error {
	b, err := canonical(raw)
	if err != nil || !bytes.Equal(raw, b) || json.Unmarshal(b, value) != nil {
		return &Reason{Kind: InvalidRequest}
	}
	rebuilt, err := encode(value)
	if err != nil || !bytes.Equal(raw, rebuilt) {
		return &Reason{Kind: InvalidRequest}
	}
	return nil
}

type headRecord struct {
	Authority   string `json:"authority"`
	Store       string `json:"store"`
	Incarnation string `json:"incarnation"`
	LedgerID    string `json:"ledger_id"`
	Digest      string `json:"digest"`
	Sequence    uint64 `json:"sequence,string"`
}

func head(h assignment.Checkpoint) headRecord { return headRecord(h) }

type stateRecord struct {
	Slot              Slot              `json:"slot"`
	Header            assignment.Header `json:"header"`
	Head              headRecord        `json:"head"`
	Epoch             uint64            `json:"epoch,string"`
	JournalSequence   uint64            `json:"journal_sequence,string"`
	Revision          uint64            `json:"revision,string"`
	Fence             uint64            `json:"fence,string"`
	HomeKeyID         string            `json:"home_key_id"`
	SessionKeyID      string            `json:"session_key_id"`
	Status            string            `json:"status"`
	LastReceiptDigest string            `json:"last_receipt_digest"`
}

func recordState(s State) stateRecord {
	return stateRecord{s.Slot, s.Header, head(s.Head), s.Epoch, s.JournalSequence, s.Revision, s.Fence, s.HomeKeyID, s.SessionKeyID, s.Status, s.LastReceiptDigest}
}
func (s stateRecord) state() State {
	return State{s.Slot, s.Header, assignment.Checkpoint(s.Head), s.Epoch, s.Revision, s.Fence, s.JournalSequence, s.HomeKeyID, s.SessionKeyID, s.Status, s.LastReceiptDigest}
}
func validState(s State) bool {
	h, p := s.Header, s.Head
	ledger, err := assignment.NewLedger(h)
	if err != nil || p.Sequence == 0 && p != ledger.Checkpoint() {
		return false
	}
	return s.Slot.valid() && s.Slot.Authority == h.Authority && s.Slot.Store == h.Store &&
		h.Authority == p.Authority && h.Store == p.Store && h.Incarnation == p.Incarnation && h.LedgerID == p.LedgerID &&
		digestOK(p.Digest) && s.HomeKeyID == h.KeyID && s.Epoch != 0 && s.Revision != 0 && s.JournalSequence != 0 &&
		(s.Status == Active || s.Status == Retired && s.SessionKeyID == "") &&
		(s.SessionKeyID == "" || textOK(s.SessionKeyID) && s.Fence > 0) && digestOK(s.LastReceiptDigest)
}

type recordHeader struct {
	Schema    int    `json:"schema"`
	Operation string `json:"operation"`
	Audience  string `json:"audience"`
	KeyID     string `json:"key_id"`
}
type enrollRecord struct {
	recordHeader
	ID            string `json:"id"`
	Slot          Slot   `json:"slot"`
	HeaderBytes   string `json:"header_bytes"`
	HomePublicKey string `json:"home_public_key"`
}

func enrollmentBody(e Enrollment, keyID string) ([]byte, error) {
	if !e.Slot.valid() || e.ID == (ID{}) || len(e.HeaderBytes) > maxFrame || len(e.HomePublicKey) != ed25519.PublicKeySize {
		return nil, &Reason{Kind: InvalidRequest}
	}
	return encode(enrollRecord{recordHeader{1, "enroll", e.Slot.Witness, keyID}, idString(e.ID), e.Slot, base64.RawURLEncoding.EncodeToString(e.HeaderBytes), base64.RawURLEncoding.EncodeToString(e.HomePublicKey)})
}

// ProveEnrollment signs the exact header bytes and slot for the owner to
// submit alongside the independently configured pin.
func ProveEnrollment(key ed25519.PrivateKey, e Enrollment) ([]byte, error) {
	if !validPrivate(key) || !bytes.Equal(e.HomePublicKey, key.Public().(ed25519.PublicKey)) {
		return nil, &Reason{Kind: InvalidRequest}
	}
	id, err := KeyID(e.HomePublicKey)
	if err != nil {
		return nil, err
	}
	b, err := enrollmentBody(e, id)
	if err != nil {
		return nil, err
	}
	return homeProfile.sign(key, b)
}

type mutationRecord struct {
	recordHeader
	ID                string      `json:"id"`
	Expected          stateRecord `json:"expected"`
	SessionPublicKey  string      `json:"session_public_key,omitempty"`
	Next              *headRecord `json:"next,omitempty"`
	DispositionDigest string      `json:"disposition_digest,omitempty"`
}

func mutationBody(op, keyID string, m Mutation, session []byte, next *assignment.Checkpoint, disposition string) ([]byte, error) {
	if m.ID == (ID{}) || !validState(m.Expected) {
		return nil, &Reason{Kind: InvalidRequest}
	}
	r := mutationRecord{recordHeader: recordHeader{1, op, m.Expected.Slot.Witness, keyID}, ID: idString(m.ID), Expected: recordState(m.Expected)}
	switch op {
	case "claim":
		if len(session) != ed25519.PublicKeySize {
			return nil, &Reason{Kind: InvalidRequest}
		}
		r.SessionPublicKey = base64.RawURLEncoding.EncodeToString(session)
	case "advance":
		if next == nil || !textOK(next.Authority) || !textOK(next.Incarnation) || !textOK(next.Store) || !textOK(next.LedgerID) || !digestOK(next.Digest) {
			return nil, &Reason{Kind: InvalidRequest}
		}
		h := head(*next)
		r.Next = &h
	case "retire":
		if !digestOK(disposition) {
			return nil, &Reason{Kind: InvalidRequest}
		}
		r.DispositionDigest = disposition
	case "release":
	default:
		return nil, &Reason{Kind: InvalidRequest}
	}
	return encode(r)
}

// AdvanceBody is the saved intent signed by both the home and its session.
// Its Expected field binds the predecessor receipt, complete head and fence.
func AdvanceBody(a Advance) ([]byte, error) {
	return mutationBody("advance", a.Expected.HomeKeyID, a.Mutation, nil, &a.Next, "")
}
func hash(domain string, parts ...[]byte) string {
	h := sha256.New()
	h.Write([]byte(domain + "\x00"))
	for _, part := range parts {
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func RequestDigest(body []byte) string { return hash("ataniq.checkpoint.request.v1", body) }
func ReceiptDigest(r Receipt) string {
	return hash("ataniq.checkpoint.receipt.v1", r.Body, []byte{0}, r.Signature)
}

type ackRecord struct {
	recordHeader
	ID            string      `json:"id"`
	RequestDigest string      `json:"request_digest"`
	Result        stateRecord `json:"result"`
}
type readRecord struct {
	recordHeader
	JournalSequence uint64      `json:"journal_sequence,string,omitempty"`
	PreviousDigest  string      `json:"previous_digest,omitempty"`
	Challenge       string      `json:"challenge"`
	Guard           string      `json:"guard"`
	Current         stateRecord `json:"current"`
	ReceiptID       string      `json:"receipt_id"`
	PriorDigest     string      `json:"prior_digest"`
}

func expectedRead(keyID string, q ReadRequest, v View, guard string) (readRecord, error) {
	if q.Challenge == (ID{}) || q.Slot != v.Current.Slot || !validState(v.Current) {
		return readRecord{}, &Reason{Kind: InvalidRequest}
	}
	r := readRecord{recordHeader: recordHeader{1, "read", q.Slot.Authority, keyID}, Challenge: idString(q.Challenge), Guard: guard, Current: recordState(v.Current)}
	if q.ReceiptID != (ID{}) {
		r.ReceiptID = idString(q.ReceiptID)
		if v.Prior != nil {
			r.PriorDigest = ReceiptDigest(*v.Prior)
		}
	} else if v.Prior != nil {
		return readRecord{}, &Reason{Kind: InvalidRequest}
	}
	return r, nil
}

// Acknowledgement binds one immutable result to its request and predecessor.
// The signed result carries the predecessor receipt digest. The current state's
// LastReceiptDigest is derived from the completed receipt to avoid self-reference.
type Acknowledgement struct {
	Operation                        string
	ID                               ID
	RequestDigest, PredecessorDigest string
	State                            State
}

func VerifyReceipt(pin ed25519.PublicKey, r Receipt) (Acknowledgement, error) {
	keyID, err := KeyID(pin)
	if err != nil {
		return Acknowledgement{}, err
	}
	return verifyReceipt(pin, keyID, r)
}
func verifyReceipt(pin ed25519.PublicKey, keyID string, r Receipt) (Acknowledgement, error) {
	var a ackRecord
	if decode(r.Body, &a) != nil || !witnessProfile.verify(pin, r.Body, r.Signature) || a.Schema != 1 || a.KeyID != keyID ||
		a.Audience != a.Result.Slot.Authority || !digestOK(a.ID) || !digestOK(a.RequestDigest) {
		return Acknowledgement{}, &Reason{Kind: InvalidRequest}
	}
	switch a.Operation {
	case "enroll", "claim", "advance", "release", "retire":
	default:
		return Acknowledgement{}, &Reason{Kind: InvalidRequest}
	}
	var id ID
	if _, err := hex.Decode(id[:], []byte(a.ID)); err != nil {
		return Acknowledgement{}, &Reason{Kind: InvalidRequest}
	}
	prior := a.Result.LastReceiptDigest
	if id == (ID{}) || (a.Operation == "enroll" && (prior != "" || a.Result.Revision != 1)) ||
		(a.Operation != "enroll" && !digestOK(prior)) {
		return Acknowledgement{}, &Reason{Kind: InvalidRequest}
	}
	state := a.Result.state()
	state.LastReceiptDigest = ReceiptDigest(r)
	if !validState(state) {
		return Acknowledgement{}, &Reason{Kind: InvalidRequest}
	}
	return Acknowledgement{a.Operation, id, a.RequestDigest, prior, state}, nil
}

// VerifyView authenticates the challenge, state and optional historical
// receipt. An authenticated inhibited read returns ErrUnavailable.
func VerifyView(pin ed25519.PublicKey, q ReadRequest, v View) error {
	keyID, err := KeyID(pin)
	if err != nil {
		return err
	}
	return verifyView(pin, keyID, q, v)
}
func verifyView(pin ed25519.PublicKey, keyID string, q ReadRequest, v View) error {
	if len(v.Fresh.Body) > maxFrame || !witnessProfile.verify(pin, v.Fresh.Body, v.Fresh.Signature) {
		return &Reason{Kind: InvalidRequest}
	}
	if v.Prior != nil {
		a, err := verifyReceipt(pin, keyID, *v.Prior)
		if err != nil || a.ID != q.ReceiptID || a.State.Slot != q.Slot || a.State.JournalSequence > v.Current.JournalSequence {
			return &Reason{Kind: InvalidRequest}
		}
	}
	var signed readRecord
	if decode(v.Fresh.Body, &signed) != nil || (signed.Guard != "ready" && signed.Guard != "inhibited") {
		return &Reason{Kind: InvalidRequest}
	}
	expected, err := expectedRead(keyID, q, v, signed.Guard)
	if err != nil {
		return err
	}
	// Memory witnesses and older reads omit the durable directory position.
	if signed.JournalSequence != 0 || signed.PreviousDigest != "" {
		if signed.JournalSequence < v.Current.JournalSequence || !digestOK(signed.PreviousDigest) {
			return &Reason{Kind: InvalidRequest}
		}
		expected.JournalSequence, expected.PreviousDigest = signed.JournalSequence, signed.PreviousDigest
	}
	if signed != expected {
		return &Reason{Kind: InvalidRequest}
	}
	if signed.Guard == "inhibited" {
		return ErrUnavailable
	}
	return nil
}

func idString(id ID) string { return hex.EncodeToString(id[:]) }
