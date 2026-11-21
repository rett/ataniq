package intake

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/intake/verify"
)

// recordVersion is the version of both record types.
const recordVersion = 1

// VerificationRecord is what the owner keeps of one verification run: which
// tip, which plan, how it ended, and a hash and the length of its output. The
// output itself is lead-influenced text and is not in the record; see
// [Store.PutVerification].
type VerificationRecord struct {
	// Version is the record format, 1.
	Version int
	// Project is the project name the run was made for.
	Project string
	// TipRef is the lead ref whose tip was verified. It is raw lead text.
	TipRef string
	// Tip is the full commit id that was verified.
	Tip string
	// Merge is true for a record of merge mode: Tip is a host-built merge
	// and TipRef a pin under [PendingPrefix]. A lead-mode record has Merge
	// false and no Base, and is written as before.
	Merge bool
	// Base is, in merge mode, the target's tip the merge was built
	// against, a full object id. It is empty in lead mode.
	Base string
	// PlanDigest is [verify.PlanDigest] of Gates.
	PlanDigest string
	// Gates are the gates that were run, in order.
	Gates []verify.Gate
	// Passed, Exit, Stopped, OutputCut, Started and DurationMS are the
	// result of the run as [verify.Result] gave them.
	Passed bool
	Exit   int
	// Stopped is "", "timeout", "output-limit" or "error".
	Stopped string
	// OutputSHA256 is the hex SHA-256 of the whole output, and OutputBytes
	// its length.
	OutputSHA256 string
	OutputBytes  int64
	// OutputCut is true when output was dropped, so the hash is of what was
	// kept.
	OutputCut  bool
	Started    time.Time
	DurationMS int64
}

// IntakeRecord is what the owner keeps of one integration: the target branch
// moved from Base to Tip, with the digest of the review that was signed, the
// signed paths, and the passing verification record that allowed it.
type IntakeRecord struct {
	// Version is the record format, 1.
	Version int
	// Time is when the integration was recorded, in UTC.
	Time time.Time
	// Project is the project name.
	Project string
	// TargetRef is the upstream branch that moved, TipRef the lead branch.
	// Both are raw text.
	TargetRef, TipRef string
	// Base is the commit the target was at, Tip the commit it moved to.
	Base, Tip string
	// ReviewDigest is [Review.Digest] of the review that was signed.
	ReviewDigest string
	// Commits and Paths are the number of commits and of changed paths of
	// the review.
	Commits, Paths int
	// SignedPaths are the flagged paths the owner accepted, sorted, as raw
	// bytes.
	SignedPaths []string
	// Verification is the passing record that allowed the integration.
	Verification VerificationRecord
}

// rawText is a string that survives JSON: valid UTF-8 is written as a JSON
// string; bytes that are not valid UTF-8 (a path or a ref name of a lead may
// hold any byte) as {"b64": "..."} so that a record holds the exact bytes.
type rawText string

func (t rawText) MarshalJSON() ([]byte, error) {
	if utf8.ValidString(string(t)) {
		return json.Marshal(string(t))
	}
	return json.Marshal(struct {
		B64 string `json:"b64"`
	}{base64.StdEncoding.EncodeToString([]byte(t))})
}

func (t *rawText) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*t = rawText(s)
		return nil
	}
	var o struct {
		B64 *string `json:"b64"`
	}
	if err := strictDecode(data, &o); err != nil || o.B64 == nil {
		return errors.New("a text is a string or an object with b64")
	}
	b, err := base64.StdEncoding.Strict().DecodeString(*o.B64)
	if err != nil || utf8.Valid(b) {
		// Valid UTF-8 is always written as a string, so one form is
		// the only form of a text.
		return errors.New("b64 text is not base64 of bytes that are not UTF-8")
	}
	*t = rawText(b)
	return nil
}

// strictDecode decodes one JSON value from data into v and refuses unknown
// fields and anything after the value.
func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the JSON value")
	}
	return nil
}

type gateJSON struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

type verificationJSON struct {
	Version      int        `json:"version"`
	Project      string     `json:"project"`
	TipRef       rawText    `json:"tip_ref"`
	Tip          string     `json:"tip"`
	Merge        bool       `json:"merge,omitempty"`
	Base         string     `json:"base,omitempty"`
	PlanDigest   string     `json:"plan_digest"`
	Gates        []gateJSON `json:"gates"`
	Passed       bool       `json:"passed"`
	Exit         int        `json:"exit"`
	Stopped      string     `json:"stopped"`
	OutputSHA256 string     `json:"output_sha256"`
	OutputBytes  int64      `json:"output_bytes"`
	OutputCut    bool       `json:"output_cut"`
	Started      time.Time  `json:"started"`
	DurationMS   int64      `json:"duration_ms"`
}

// MarshalJSON writes the record with snake_case names.
func (v VerificationRecord) MarshalJSON() ([]byte, error) {
	j := verificationJSON{
		Version: v.Version, Project: v.Project, TipRef: rawText(v.TipRef), Tip: v.Tip,
		Merge: v.Merge, Base: v.Base, PlanDigest: v.PlanDigest, Passed: v.Passed, Exit: v.Exit, Stopped: v.Stopped,
		OutputSHA256: v.OutputSHA256, OutputBytes: v.OutputBytes, OutputCut: v.OutputCut,
		Started: v.Started, DurationMS: v.DurationMS,
		Gates: make([]gateJSON, 0, len(v.Gates)),
	}
	for _, g := range v.Gates {
		j.Gates = append(j.Gates, gateJSON{Name: g.Name, Argv: g.Argv})
	}
	return json.Marshal(j)
}

// UnmarshalJSON reads a record strictly: an unknown field, a missing field of
// the format, a wrong version or a malformed value is an error.
func (v *VerificationRecord) UnmarshalJSON(data []byte) error {
	var j verificationJSON
	if err := strictDecode(data, &j); err != nil {
		return err
	}
	rec := VerificationRecord{
		Version: j.Version, Project: j.Project, TipRef: string(j.TipRef), Tip: j.Tip,
		Merge: j.Merge, Base: j.Base, PlanDigest: j.PlanDigest, Passed: j.Passed, Exit: j.Exit, Stopped: j.Stopped,
		OutputSHA256: j.OutputSHA256, OutputBytes: j.OutputBytes, OutputCut: j.OutputCut,
		Started: j.Started, DurationMS: j.DurationMS,
	}
	for _, g := range j.Gates {
		rec.Gates = append(rec.Gates, verify.Gate{Name: g.Name, Argv: g.Argv})
	}
	if err := rec.check(); err != nil {
		return err
	}
	*v = rec
	return nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// check reports why a record is not well formed. It is used when a record is
// read and before one is written.
func (v VerificationRecord) check() error {
	switch {
	case v.Version != recordVersion:
		return fmt.Errorf("verification record version %d is not %d", v.Version, recordVersion)
	case v.Project == "" || len(v.Project) > 63:
		return errors.New("verification record has no valid project")
	case !gitenv.IsObjectID(v.Tip):
		return errors.New("verification record tip is not a full object id")
	case v.Merge && (!gitenv.IsObjectID(v.Base) || len(v.Base) != len(v.Tip)):
		return errors.New("verification record of a merge has no base that is a full object id of the format of the tip")
	case !v.Merge && v.Base != "":
		return errors.New("verification record of a lead branch has a base")
	case !isHex64(v.PlanDigest):
		return errors.New("verification record plan digest is not a SHA-256")
	case len(v.Gates) == 0:
		return errors.New("verification record has no gates")
	case !isHex64(v.OutputSHA256):
		return errors.New("verification record output hash is not a SHA-256")
	case v.OutputBytes < 0 || v.DurationMS < 0:
		return errors.New("verification record has a negative size or duration")
	}
	switch v.Stopped {
	case "", verify.StoppedTimeout, verify.StoppedOutputLimit, verify.StoppedError:
	default:
		return errors.New("verification record has an unknown stop reason")
	}
	return nil
}

// passing reports whether the record is a clean pass: the run passed, exited
// 0 by itself, was not stopped, and its output was not cut.
func (v VerificationRecord) passing() bool {
	return v.Passed && v.Exit == 0 && v.Stopped == "" && !v.OutputCut
}

type intakeJSON struct {
	Version      int                `json:"version"`
	Time         time.Time          `json:"time"`
	Project      string             `json:"project"`
	TargetRef    rawText            `json:"target_ref"`
	TipRef       rawText            `json:"tip_ref"`
	Base         string             `json:"base"`
	Tip          string             `json:"tip"`
	ReviewDigest string             `json:"review_digest"`
	Commits      int                `json:"commits"`
	Paths        int                `json:"paths"`
	SignedPaths  []rawText          `json:"signed_paths"`
	Verification VerificationRecord `json:"verification"`
}

// MarshalJSON writes the record with snake_case names.
func (r IntakeRecord) MarshalJSON() ([]byte, error) {
	j := intakeJSON{
		Version: r.Version, Time: r.Time, Project: r.Project,
		TargetRef: rawText(r.TargetRef), TipRef: rawText(r.TipRef),
		Base: r.Base, Tip: r.Tip, ReviewDigest: r.ReviewDigest,
		Commits: r.Commits, Paths: r.Paths, Verification: r.Verification,
		SignedPaths: make([]rawText, 0, len(r.SignedPaths)),
	}
	for _, p := range r.SignedPaths {
		j.SignedPaths = append(j.SignedPaths, rawText(p))
	}
	return json.Marshal(j)
}

// UnmarshalJSON reads a record strictly, as [VerificationRecord] does.
func (r *IntakeRecord) UnmarshalJSON(data []byte) error {
	var j intakeJSON
	if err := strictDecode(data, &j); err != nil {
		return err
	}
	rec := IntakeRecord{
		Version: j.Version, Time: j.Time, Project: j.Project,
		TargetRef: string(j.TargetRef), TipRef: string(j.TipRef),
		Base: j.Base, Tip: j.Tip, ReviewDigest: j.ReviewDigest,
		Commits: j.Commits, Paths: j.Paths, Verification: j.Verification,
	}
	for _, p := range j.SignedPaths {
		rec.SignedPaths = append(rec.SignedPaths, string(p))
	}
	if err := rec.check(); err != nil {
		return err
	}
	*r = rec
	return nil
}

// check reports why a record is not well formed.
func (r IntakeRecord) check() error {
	switch {
	case r.Version != recordVersion:
		return fmt.Errorf("intake record version %d is not %d", r.Version, recordVersion)
	case r.Project == "" || len(r.Project) > 63:
		return errors.New("intake record has no valid project")
	case !gitenv.IsObjectID(r.Base) || !gitenv.IsObjectID(r.Tip):
		return errors.New("intake record base or tip is not a full object id")
	case !isHex64(r.ReviewDigest):
		return errors.New("intake record review digest is not a SHA-256")
	case r.Commits < 0 || r.Paths < 0:
		return errors.New("intake record has a negative count")
	}
	return r.Verification.check()
}
