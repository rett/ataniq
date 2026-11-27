package leadregistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"
	"unicode"
	"unicode/utf8"

	"ataniq/internal/leadrole"
)

const (
	// FileName is the name of the journal file inside the registry folder.
	FileName = "registry.jsonl"

	formatName         = "ataniq-lead-registry"
	formatVersion      = 1 // standalone and legacy journals
	boundFormatVersion = 2
	timeLayout         = "2006-01-02T15:04:05.000000000Z"
	hashHexLen         = 2 * sha256.Size
	hashSuffixLen      = len(`,"hash":"`) + hashHexLen + len(`"}`)
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
	kindRecovered     = "recovered"
	kindRecoveryAck   = "recovery-acknowledged"
)

// roleless reports whether records of the kind belong to no role.
func roleless(kind string) bool {
	return kind == kindHeader || kind == kindRecovered || kind == kindRecoveryAck
}

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
	Format      string `json:"format"`
	Version     int    `json:"version"`
	Authority   string `json:"authority,omitempty"`
	Incarnation string `json:"incarnation,omitempty"`
}

// recoveredPayload says what Open cut off the end of the journal.
type recoveredPayload struct {
	Lines  int   `json:"lines"`
	Bytes  int64 `json:"bytes"`
	Offset int64 `json:"offset"`
}

// ackPayload is the acknowledgement of the recovery records before it.
type ackPayload struct {
	Note string `json:"note"`
}

type definePayload struct {
	Manager   Manager   `json:"manager"`
	Requested Requested `json:"requested"`

	// The identity of the requested profile, taken from the stored bytes of
	// the record by decodeLine and never encoded: digest is the profile
	// digest of [requestedDigest], and foreign is true when the stored member
	// is not what this build writes for the decoded profile (a member it does
	// not know, another order or spelling of the members). See [reduce] and
	// [Registry.BeginLaunchWith].
	digest  string
	foreign bool
}

type desiredPayload struct {
	Desired Desired `json:"desired"`
}

type beginPayload struct {
	IntentID      string `json:"intent"`
	Epoch         uint64 `json:"epoch"`
	UnitName      string `json:"unit"`
	ProfileDigest string `json:"profile_digest"`
	// ParentEpoch and Identity are set for a coordinator and only then.
	ParentEpoch uint64 `json:"parent_epoch,omitempty"`
	Identity    string `json:"identity,omitempty"`
	// BootID is the boot identity the caller gave, if any.
	BootID string `json:"boot_id,omitempty"`
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

// roleChecker is implemented by a payload that must be checked against the
// role of its record as well.
type roleChecker interface {
	checkRole(role leadrole.Role) error
}

func checkRole(p payload, role leadrole.Role) error {
	if c, ok := p.(roleChecker); ok {
		return c.checkRole(role)
	}
	return nil
}

func (h *headerPayload) validate() error {
	if h.Format != formatName {
		return fmt.Errorf("not a lead registry (format %q)", h.Format)
	}
	switch h.Version {
	case formatVersion:
		if h.Authority != "" || h.Incarnation != "" {
			return fmt.Errorf("instance fields in a legacy header")
		}
	case boundFormatVersion:
		if len(h.Authority) != hashHexLen || !isLowerHex([]byte(h.Authority)) ||
			len(h.Incarnation) != 32 || !isLowerHex([]byte(h.Incarnation)) {
			return fmt.Errorf("invalid instance binding")
		}
	default:
		return fmt.Errorf("unsupported format version %d", h.Version)
	}
	return nil
}

func (d *definePayload) validate() error { return d.Manager.validate() }

// checkRole checks the profile for the kind of role.
func (d *definePayload) checkRole(role leadrole.Role) error {
	return d.Requested.validateFor(role)
}

func (p *recoveredPayload) validate() error {
	if p.Lines != 1 {
		return invalid("recovered record cuts %d lines, not 1", p.Lines)
	}
	if p.Bytes < 1 || p.Bytes > MaxLineBytes+1 {
		return invalid("recovered record cuts %d bytes", p.Bytes)
	}
	if p.Offset < 0 {
		return invalid("recovered record cuts at offset %d", p.Offset)
	}
	return nil
}

func (p *ackPayload) validate() error {
	return validText("recovery note", p.Note, MaxProofBytes, true)
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
	return sealLine(prev, content)
}

// sealLine appends the chain hash to a complete JSON object. Migration uses
// the stored object bytes so profiles and unknown fields keep their encoding.
func sealLine(prev [sha256.Size]byte, content []byte) ([]byte, [sha256.Size]byte, error) {
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

// decodeRecord decodes one JSON object of a journal line: the envelope or a
// payload. A member the type does not know is ignored, so a line that a newer
// build wrote with an optional member this build lacks replays as the record
// this build knows; the line's own bytes stay what the hash chain covers.
// Data after the object is refused.
func decodeRecord(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}

// requestedDigest returns the profile digest of the requested member of a
// define record as it is stored, and whether that member is foreign: not the
// bytes this build writes for the profile r it decoded to.
//
// The digest is the hex SHA-256 of the stored member, compacted and with the
// characters < > & and U+2028, U+2029 escaped as json.Marshal escapes them.
// For a member this build wrote that is exactly [Requested.Digest] of r: the
// journal's encoder writes what json.Marshal writes but for that escaping. For
// a member a newer build wrote with a member this build does not know, it is
// the digest that build computed of its own profile, so its launch-begin line
// still matches. The identity of a profile is its stored bytes, not what this
// build makes of them.
func requestedDigest(raw json.RawMessage, r Requested) (digest string, foreign bool, err error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", false, err
	}
	own, err := marshalCompact(r)
	if err != nil {
		return "", false, err
	}
	foreign = !bytes.Equal(compact.Bytes(), own)
	var escaped bytes.Buffer
	json.HTMLEscape(&escaped, compact.Bytes())
	sum := sha256.Sum256(escaped.Bytes())
	return hex.EncodeToString(sum[:]), foreign, nil
}

// repeatsMember reports whether an object anywhere in the JSON value data has
// two members whose names encoding/json would match to the same field: the
// same name, or two names that are equal under its case folding (simple
// Unicode case folding, as bytes.EqualFold; "Requested" and "requested" are
// one member to it, see [foldName]). The
// names are compared as decoded, escapes resolved. data has already been
// decoded once, so an error here is not expected.
//
// No build writes a repeated member, and a line that has one is ambiguous:
// the typed decoding of a define record merges the occurrences of an object
// member (the members of each that it knows) while json.RawMessage keeps only
// the last, so the restriction an earlier occurrence carries (a member this
// build does not know) would leave both the digest and the foreign check. A
// define record with one replays, and its profile is not launched
// (ErrLaunchProfile) until the role is defined again. It is not damage
// (ErrCorrupt): the line is chained and every value in it is one this build
// knows, the refusal of a launch is what this build does with any profile it
// cannot read whole, and a define again lifts it, where a journal that does
// not open stops every role of the daemon.
func repeatsMember(data []byte) (bool, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	// One set of the folded names per open object, and nil for an array.
	var open []map[string]bool
	key := false // the next token of the innermost object is a member name
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if key {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				open = open[:len(open)-1]
				key = len(open) > 0 && open[len(open)-1] != nil
				continue
			}
			name, ok := tok.(string)
			if !ok {
				return false, fmt.Errorf("a member name that is not a string")
			}
			folded := foldName(name)
			if open[len(open)-1][folded] {
				return true, nil
			}
			open[len(open)-1][folded] = true
			key = false
			continue
		}
		switch tok {
		case json.Delim('{'):
			open = append(open, map[string]bool{})
			key = true
			continue
		case json.Delim('['):
			open = append(open, nil)
		case json.Delim(']'):
			open = open[:len(open)-1]
		}
		// After a value (or an array's end) inside an object, a name comes
		// next; inside an array, another value.
		key = len(open) > 0 && open[len(open)-1] != nil
	}
}

// foldName folds a member name as encoding/json does to match it with a
// field (its foldName): an ASCII letter to upper case, every other rune to the
// smallest rune of its unicode.SimpleFold orbit, so that two names fold to one
// exactly when bytes.EqualFold holds for them.
func foldName(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); {
		c := name[i]
		if c < utf8.RuneSelf {
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			out = append(out, c)
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(name[i:])
		for {
			next := unicode.SimpleFold(r)
			if next <= r {
				r = next
				break
			}
			r = next
		}
		out = utf8.AppendRune(out, r)
		i += n
	}
	return string(out)
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
	if err := decodeRecord(content, &env); err != nil {
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
	case kindRecovered:
		p = &recoveredPayload{}
	case kindRecoveryAck:
		p = &ackPayload{}
	default:
		return record{}, zero, fmt.Errorf("unknown record kind %q", env.Kind)
	}
	if err := decodeRecord(env.Payload, p); err != nil {
		return record{}, zero, fmt.Errorf("bad %s payload: %v", env.Kind, err)
	}
	if env.Kind == kindHeader {
		if repeated, err := repeatsMember(line); repeated || err != nil {
			return record{}, zero, fmt.Errorf("ambiguous header")
		}
	}
	if d, ok := p.(*definePayload); ok {
		var stored struct {
			Requested json.RawMessage `json:"requested"`
		}
		if err := json.Unmarshal(env.Payload, &stored); err != nil || len(stored.Requested) == 0 {
			return record{}, zero, fmt.Errorf("bad %s payload: no requested profile", env.Kind)
		}
		digest, foreign, err := requestedDigest(stored.Requested, d.Requested)
		if err != nil {
			return record{}, zero, fmt.Errorf("bad %s payload: %v", env.Kind, err)
		}
		// A member repeated anywhere in the record, the requested member
		// itself and the envelope's payload member included, makes the stored
		// profile ambiguous: the typed decoding merges the occurrences and
		// the raw one keeps the last, so what was checked would not be what
		// is launched, and of two payload members of the envelope only the
		// last is decoded at all. The whole validated line is checked, not
		// the payload that was selected from it, and with its final hash
		// member, so an earlier "hash" member (or a case alias of it) in the
		// content is a repeat too; the chain value stays over the content.
		// Such a profile is not launched (see [repeatsMember]).
		repeated, err := repeatsMember(line)
		if err != nil {
			return record{}, zero, fmt.Errorf("bad %s payload: %v", env.Kind, err)
		}
		d.digest, d.foreign = digest, foreign || repeated
	}
	if err := p.validate(); err != nil {
		return record{}, zero, fmt.Errorf("bad %s payload: %w", env.Kind, err)
	}
	if !roleless(env.Kind) && rec.role.IsZero() {
		return record{}, zero, fmt.Errorf("%s record without a role", env.Kind)
	}
	if roleless(env.Kind) && !rec.role.IsZero() {
		return record{}, zero, fmt.Errorf("%s record names a role", env.Kind)
	}
	if err := checkRole(p, rec.role); err != nil {
		return record{}, zero, fmt.Errorf("bad %s payload: %w", env.Kind, err)
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
	return validText("boot identity", b.BootID, MaxBootIdentityBytes, false)
}

// checkRole checks that a coordinator's intent carries its parent's epoch and
// its identity, and that every other role's carries neither.
func (b *beginPayload) checkRole(role leadrole.Role) error {
	if role.Kind() != leadrole.KindCoordinator {
		if b.ParentEpoch != 0 || b.Identity != "" {
			return invalid("a parent epoch or an identity in the launch of a role that is not a coordinator")
		}
		return nil
	}
	if b.ParentEpoch == 0 {
		return invalid("coordinator launch without a parent epoch")
	}
	if want := role.Identity(b.ParentEpoch, b.Epoch); b.Identity != want {
		return invalid("coordinator launch identity %q is not %q", b.Identity, want)
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
