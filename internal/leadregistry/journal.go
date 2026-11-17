package leadregistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"ataniq/internal/leadrole"
)

const (
	// FileName is the name of the journal file inside the registry folder.
	FileName = "registry.jsonl"

	formatName    = "ataniq-lead-registry"
	formatVersion = 1
	timeLayout    = "2006-01-02T15:04:05.000000000Z"
	hashHexLen    = 2 * sha256.Size
	hashSuffixLen = len(`,"hash":"`) + hashHexLen + len(`"}`)
)

// Record kinds. Each is one journal line.
const (
	kindHeader        = "header"
	kindDefine        = "define"
	kindDesired       = "desired"
	kindLaunchBegin   = "launch-begin"
	kindLaunchOutcome = "launch-outcome"
	kindState         = "state"
	kindObserved      = "observed"
	kindOwnerDecision = "owner-decision"
)

// envelope is the content of one journal line, without its hash. The field
// order is the on-disk order.
type envelope struct {
	Seq     uint64          `json:"seq"`
	Time    string          `json:"time"`
	Kind    string          `json:"kind"`
	Role    *leadrole.Role  `json:"role,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

type headerPayload struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
}

type definePayload struct {
	Manager   Manager   `json:"manager"`
	Requested Requested `json:"requested"`
}

type desiredPayload struct {
	Desired Desired `json:"desired"`
}

type beginPayload struct {
	IntentID      string `json:"intent"`
	Epoch         uint64 `json:"epoch"`
	UnitName      string `json:"unit"`
	ProfileDigest string `json:"profile_digest"`
}

type outcomePayload struct {
	IntentID string       `json:"intent"`
	Epoch    uint64       `json:"epoch"`
	Outcome  Outcome      `json:"outcome"`
	Unit     UnitEvidence `json:"unit"`
}

type statePayload struct {
	Epoch    uint64   `json:"epoch"`
	State    State    `json:"state"`
	Evidence Evidence `json:"evidence"`
	Reason   Reason   `json:"reason"`
	Proof    string   `json:"proof,omitempty"`
}

type decisionPayload struct {
	Epoch    uint64 `json:"epoch"`
	State    State  `json:"state"`
	Reason   Reason `json:"reason"`
	Decision string `json:"decision"`
}

type observedPayload struct {
	Epoch    uint64   `json:"epoch"`
	Observed Observed `json:"observed"`
}

// payload is the typed payload of a journal record.
type payload interface{ validate() error }

func (h *headerPayload) validate() error {
	if h.Format != formatName {
		return fmt.Errorf("not a lead registry (format %q)", h.Format)
	}
	if h.Version != formatVersion {
		return fmt.Errorf("unsupported format version %d", h.Version)
	}
	return nil
}

func (d *definePayload) validate() error {
	if err := d.Manager.validate(); err != nil {
		return err
	}
	return d.Requested.validate()
}

func (d *desiredPayload) validate() error { return d.Desired.validate() }

func (o *outcomePayload) validate() error {
	if err := validIntentID(o.IntentID); err != nil {
		return err
	}
	if err := o.Outcome.validate(); err != nil {
		return err
	}
	if o.Outcome == OutcomePending {
		return invalid("outcome pending")
	}
	return o.Unit.validate()
}

func (o *observedPayload) validate() error {
	if err := o.Observed.validate(); err != nil {
		return err
	}
	if !o.Observed.any() {
		return invalid("nothing observed")
	}
	return nil
}

// record is a decoded and validated journal line.
type record struct {
	seq     uint64
	at      time.Time
	kind    string
	role    leadrole.Role
	payload payload // one of the *Payload types, by kind
}

// genesis is the chain value before the header line.
var genesis [sha256.Size]byte

func chain(prev [sha256.Size]byte, content []byte) [sha256.Size]byte {
	h := sha256.New()
	h.Write(prev[:])
	h.Write(content)
	var out [sha256.Size]byte
	h.Sum(out[:0])
	return out
}

func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// encodeLine builds the journal line (with its newline) for a record and
// returns it with the new chain value.
func encodeLine(prev [sha256.Size]byte, seq uint64, at time.Time, kind string, role leadrole.Role, body any) ([]byte, [sha256.Size]byte, error) {
	pb, err := marshalCompact(body)
	if err != nil {
		return nil, prev, fmt.Errorf("leadregistry: encode payload: %w", err)
	}
	env := envelope{Seq: seq, Time: at.UTC().Format(timeLayout), Kind: kind, Payload: pb}
	if !role.IsZero() {
		env.Role = &role
	}
	content, err := marshalCompact(env)
	if err != nil {
		return nil, prev, fmt.Errorf("leadregistry: encode line: %w", err)
	}
	sum := chain(prev, content)
	line := make([]byte, 0, len(content)+hashSuffixLen+1)
	line = append(line, content[:len(content)-1]...) // drop the closing brace
	line = append(line, `,"hash":"`...)
	line = hex.AppendEncode(line, sum[:])
	line = append(line, `"}`+"\n"...)
	if len(line)-1 > MaxLineBytes {
		return nil, prev, ErrTooLarge
	}
	return line, sum, nil
}

func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}

// decodeLine verifies one journal line (without its newline) against the
// chain value before it and the sequence number it must carry, and returns the
// validated record and the new chain value.
func decodeLine(prev [sha256.Size]byte, wantSeq uint64, line []byte) (record, [sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if len(line) <= hashSuffixLen || !bytes.HasPrefix(line[len(line)-hashSuffixLen:], []byte(`,"hash":"`)) ||
		!bytes.HasSuffix(line, []byte(`"}`)) {
		return record{}, zero, fmt.Errorf("no hash field")
	}
	hexPart := line[len(line)-hashSuffixLen+len(`,"hash":"`) : len(line)-2]
	var got [sha256.Size]byte
	if n, err := hex.Decode(got[:], hexPart); err != nil || n != len(got) || !isLowerHex(hexPart) {
		return record{}, zero, fmt.Errorf("malformed hash")
	}
	content := append(append([]byte(nil), line[:len(line)-hashSuffixLen]...), '}')
	if want := chain(prev, content); want != got {
		return record{}, zero, fmt.Errorf("hash chain broken")
	}
	var env envelope
	if err := strictDecode(content, &env); err != nil {
		return record{}, zero, fmt.Errorf("bad JSON: %v", err)
	}
	if env.Seq != wantSeq {
		return record{}, zero, fmt.Errorf("sequence %d where %d was expected", env.Seq, wantSeq)
	}
	at, err := time.Parse(timeLayout, env.Time)
	if err != nil {
		return record{}, zero, fmt.Errorf("bad time %q", env.Time)
	}
	rec := record{seq: env.Seq, at: at.UTC(), kind: env.Kind}
	if env.Role != nil {
		rec.role = *env.Role
	}
	var p payload
	switch env.Kind {
	case kindHeader:
		p = &headerPayload{}
	case kindDefine:
		p = &definePayload{}
	case kindDesired:
		p = &desiredPayload{}
	case kindLaunchBegin:
		p = &beginPayload{}
	case kindLaunchOutcome:
		p = &outcomePayload{}
	case kindState:
		p = &statePayload{}
	case kindObserved:
		p = &observedPayload{}
	case kindOwnerDecision:
		p = &decisionPayload{}
	default:
		return record{}, zero, fmt.Errorf("unknown record kind %q", env.Kind)
	}
	if err := strictDecode(env.Payload, p); err != nil {
		return record{}, zero, fmt.Errorf("bad %s payload: %v", env.Kind, err)
	}
	if err := p.validate(); err != nil {
		return record{}, zero, fmt.Errorf("bad %s payload: %w", env.Kind, err)
	}
	if env.Kind != kindHeader && rec.role.IsZero() {
		return record{}, zero, fmt.Errorf("%s record without a role", env.Kind)
	}
	if env.Kind == kindHeader && !rec.role.IsZero() {
		return record{}, zero, fmt.Errorf("header names a role")
	}
	rec.payload = p
	return rec, got, nil
}

func isLowerHex(b []byte) bool {
	for _, c := range b {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validIntentID(id string) error {
	if len(id) != 32 || !isLowerHex([]byte(id)) {
		return invalid("intent id is not 32 lower-case hex characters")
	}
	return nil
}

func (b *beginPayload) validate() error {
	if err := validIntentID(b.IntentID); err != nil {
		return err
	}
	if len(b.ProfileDigest) != hashHexLen || !isLowerHex([]byte(b.ProfileDigest)) {
		return invalid("profile digest is not a hex SHA-256")
	}
	if b.Epoch == 0 {
		return invalid("launch epoch 0")
	}
	return nil
}

func (d *decisionPayload) validate() error {
	if d.State != StateStopped && d.State != StateEnded {
		return invalid("an owner decision resolves unknown to stopped or ended, not %q", string(d.State))
	}
	if err := d.Reason.validate(); err != nil {
		return err
	}
	if err := validText("decision", d.Decision, MaxProofBytes, false); err != nil {
		return err
	}
	if d.Decision == "" {
		return ErrDecisionRequired
	}
	return nil
}

func (s *statePayload) validate() error {
	if err := s.State.validate(); err != nil {
		return err
	}
	if s.State == StateStarting {
		return invalid("starting is set by a launch intent only")
	}
	if err := s.Evidence.validate(); err != nil {
		return err
	}
	if err := s.Reason.validate(); err != nil {
		return err
	}
	if err := validText("proof", s.Proof, MaxProofBytes, false); err != nil {
		return err
	}
	if (s.State == StateStopped || s.State == StateEnded) && s.Proof == "" {
		return ErrProofRequired
	}
	return nil
}
