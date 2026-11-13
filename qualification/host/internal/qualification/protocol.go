// Package qualification defines bounded workload observations, not qualification decisions.
package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	Version1       = 1
	MaxInputBytes  = 64 << 10
	MaxOutputBytes = 64 << 10
	MaxFacts       = 256
	MaxFakeFD      = 1<<20 - 1
	MaxEntries     = 128
	MaxLineBytes   = 1024
	maxDepth       = 8
)

type Case string

const (
	CaseInventory Case = "inventory"
	CaseFakeFile  Case = "fake-file"
	CaseFakeUnix  Case = "fake-unix"
	CaseFakeFD    Case = "fake-fd"
)

// Future matrix IDs are recognized, but never executed by this tranche.
func futureCase(c Case) bool {
	switch c {
	case "FS1", "FS2", "FS3", "FS4", "US1", "US2", "US3", "FD1", "FD2",
		"PR1", "PR2", "PR3", "NS1", "KR1", "N1", "N2", "N3", "R1", "R2", "R3", "R4", "L1", "L2":
		return true
	}
	return false
}

func validCase(c Case) bool {
	return c == CaseInventory || c == CaseFakeFile || c == CaseFakeUnix || c == CaseFakeFD || futureCase(c)
}

// All plain numeric fields are required and nonzero. Pointers distinguish absent
// identity components from legitimate zero values. IDs and nonces are lowercase hex.
type Request struct {
	Version       int      `json:"version"`
	RunID         string   `json:"run_id"`
	RequestID     string   `json:"request_id"`
	Case          Case     `json:"case"`
	Payload       *Payload `json:"payload,omitempty"`
	MaxDurationMS int      `json:"max_duration_ms"`
	MaxBytes      int      `json:"max_bytes"`
}

type Payload struct {
	File *NamePayload `json:"file,omitempty"`
	Unix *UnixPayload `json:"unix,omitempty"`
	FD   *FDPayload   `json:"fd,omitempty"`
}

type NamePayload struct {
	Name string `json:"name"`
}

type UnixPayload struct {
	Name   string  `json:"name"`
	Device *uint64 `json:"device"`
	Inode  *uint64 `json:"inode"`
	UID    *uint32 `json:"uid"`
	Nonce  string  `json:"nonce"`
}

type FDPayload struct {
	Name    string  `json:"name"`
	FD      *int    `json:"fd"`
	Device  *uint64 `json:"device"`
	Inode   *uint64 `json:"inode"`
	Regular *bool   `json:"regular"`
	SHA256  string  `json:"sha256"`
}

// State is known, unknown or missing. Value is mandatory only for known facts;
// a known false/zero/empty value must be represented explicitly, never inferred.
type Fact struct {
	Name  string  `json:"name"`
	State string  `json:"state"`
	Value *string `json:"value,omitempty"`
}

// Status is observed (operation completed, including a recorded syscall denial),
// inconclusive (missing/incomplete data or fixture), unsupported (future case),
// or error (invalid request/configuration/internal failure). None means pass.
type Observation struct {
	Version   int    `json:"version"`
	RunID     string `json:"run_id"`
	RequestID string `json:"request_id"`
	Case      Case   `json:"case"`
	Status    string `json:"status"`
	Facts     []Fact `json:"facts"`
	Reason    string `json:"reason,omitempty"`
	Errno     *int   `json:"errno,omitempty"`
}

func bounded(s string, lo, hi int, valid func(rune) bool) bool {
	return utf8.ValidString(s) && len(s) >= lo && len(s) <= hi && strings.IndexFunc(s, func(r rune) bool { return !valid(r) }) < 0
}
func isHex(r rune) bool { return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' }
func isName(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}
func validHex(s string) bool { return bounded(s, 8, 32, isHex) }
func validName(s string) bool {
	return bounded(s, 1, 64, isName) && s[0] != '.' && !strings.Contains(s, "..")
}
func validText(s string, max int) bool {
	return bounded(s, 0, max, func(r rune) bool { return !unicode.IsControl(r) && r != unicode.ReplacementChar })
}

func (r Request) payloadName() (string, error) {
	p := r.Payload
	if r.Case == CaseInventory || futureCase(r.Case) {
		if p != nil {
			return "", errors.New("case takes no payload")
		}
		return "", nil
	}
	if p == nil {
		return "", errors.New("payload required")
	}
	n := 0
	for _, present := range []bool{p.File != nil, p.Unix != nil, p.FD != nil} {
		if present {
			n++
		}
	}
	if n != 1 {
		return "", errors.New("exactly one payload required")
	}
	switch {
	case r.Case == CaseFakeFile && p.File != nil:
		return p.File.Name, nil
	case r.Case == CaseFakeUnix && p.Unix != nil:
		u := p.Unix
		if u.Device == nil || u.Inode == nil || u.UID == nil || !validHex(u.Nonce) {
			return "", errors.New("unix identity and nonce required")
		}
		return u.Name, nil
	case r.Case == CaseFakeFD && p.FD != nil:
		f := p.FD
		if f.FD == nil || *f.FD < 3 || *f.FD > MaxFakeFD || f.Device == nil || f.Inode == nil || f.Regular == nil || !*f.Regular || !bounded(f.SHA256, 64, 64, isHex) {
			return "", errors.New("bounded regular fake-fd identity and digest required")
		}
		return f.Name, nil
	}
	return "", errors.New("payload does not match case")
}

func (r Request) Validate() error {
	if r.Version != Version1 || !validHex(r.RunID) || !validHex(r.RequestID) || !validCase(r.Case) {
		return errors.New("bad version, ids or case")
	}
	if r.MaxDurationMS < 1 || r.MaxDurationMS > 2000 || r.MaxBytes < 1 || r.MaxBytes > 4096 {
		return errors.New("bounds out of range")
	}
	name, err := r.payloadName()
	if err != nil {
		return err
	}
	if r.Case != CaseInventory && !futureCase(r.Case) && !validName(name) {
		return errors.New("bad flat name")
	}
	return nil
}

func (o Observation) Validate() error {
	// Invalid wire input cannot supply trustworthy IDs. The CLI uses this reserved
	// uncorrelated error envelope; it never copies identifiers from rejected input.
	uncorrelated := o.Status == "error" && o.RunID == "" && o.RequestID == "" && o.Case == ""
	if o.Version != Version1 || !uncorrelated && (!validHex(o.RunID) || !validHex(o.RequestID) || !validCase(o.Case)) {
		return errors.New("bad observation identity")
	}
	if o.Status != "observed" && o.Status != "inconclusive" && o.Status != "unsupported" && o.Status != "error" {
		return errors.New("bad status")
	}
	if futureCase(o.Case) && o.Status != "unsupported" && o.Status != "error" {
		return errors.New("future case is unsupported")
	}
	if o.Facts == nil || len(o.Facts) > MaxFacts || !validText(o.Reason, 256) || o.Status != "observed" && o.Reason == "" || o.Errno != nil && (*o.Errno < 1 || *o.Errno > 4095) {
		return errors.New("bad facts, reason or errno")
	}
	seen := map[string]bool{}
	for _, f := range o.Facts {
		if !bounded(f.Name, 1, 64, isName) || seen[f.Name] {
			return errors.New("bad or duplicate fact name")
		}
		seen[f.Name] = true
		if f.State == "known" {
			if f.Value == nil || !validText(*f.Value, 1024) {
				return errors.New("known fact requires bounded value")
			}
		} else if (f.State != "unknown" && f.State != "missing") || f.Value != nil {
			return errors.New("bad fact presence")
		}
		if o.Status == "observed" && f.State != "known" {
			return errors.New("incomplete facts require inconclusive status")
		}
	}
	if uncorrelated && (len(o.Facts) != 0 || o.Errno != nil) {
		return errors.New("bad uncorrelated error")
	}
	return nil
}

// Canonical ASCII keys prevent encoding/json's ASCII case and Unicode SimpleFold
// aliases from ever reaching struct matching (including escaped aliases).
func walk(d *json.Decoder, depth int) error {
	if depth > maxDepth {
		return errors.New("nesting too deep")
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		if tok == nil {
			return errors.New("null is not a field value")
		}
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || !bounded(key, 1, 64, func(r rune) bool { return r >= 'a' && r <= 'z' || r == '_' || r >= '0' && r <= '9' }) {
				return errors.New("noncanonical key")
			}
			if seen[key] {
				return errors.New("duplicate key")
			}
			seen[key] = true
		}
		if err := walk(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func decode(r io.Reader, limit int, target any) error {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return errors.New("wire read failed")
	}
	if len(data) > limit {
		return errors.New("frame too large")
	}
	if !utf8.Valid(data) {
		return errors.New("invalid utf8")
	}
	t := bytes.TrimSpace(data)
	if len(t) == 0 || t[0] != '{' {
		return errors.New("frame must be an object")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := walk(d, 1); err != nil {
		return errors.New("invalid JSON structure")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid fields")
	}
	return nil
}

func DecodeRequest(r io.Reader) (Request, error) {
	var req Request
	if err := decode(r, MaxInputBytes, &req); err != nil {
		return Request{}, err
	}
	if err := req.Validate(); err != nil {
		return Request{}, err
	}
	return req, nil
}

func DecodeObservation(r io.Reader) (Observation, error) {
	var o Observation
	if err := decode(r, MaxOutputBytes, &o); err != nil {
		return Observation{}, err
	}
	if err := o.Validate(); err != nil {
		return Observation{}, err
	}
	return o, nil
}

func EncodeObservation(w io.Writer, o Observation) error {
	if err := o.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > MaxOutputBytes {
		return errors.New("observation too large")
	}
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		return io.ErrShortWrite
	}
	return err
}
