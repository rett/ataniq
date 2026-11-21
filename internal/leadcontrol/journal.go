//go:build linux && ataniq_canary

package leadcontrol

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"ataniq/internal/leadrole"
)

// The kinds of line.
const (
	kindAdmit       = "admit"
	kindFinish      = "finish"
	kindInterrupted = "interrupted"
	kindTombstone   = "tombstone"
)

// line is every kind of line; the kind says which fields it carries and the
// rest must be empty.
type line struct {
	Kind    string          `json:"kind"`
	At      string          `json:"at,omitempty"`
	Client  string          `json:"client,omitempty"`
	ID      string          `json:"id,omitempty"`
	Op      string          `json:"op,omitempty"`
	Role    string          `json:"role,omitempty"`
	Digest  string          `json:"digest,omitempty"`
	Status  Status          `json:"status,omitempty"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Key     string          `json:"key,omitempty"`
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// marshalCompact marshals a value without escaping HTML characters.
func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// encodeLine returns the line with its newline, or [ErrTooLarge].
func encodeLine(l line) ([]byte, error) {
	data, err := marshalCompact(l)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(data)+1 > MaxLineBytes {
		return nil, ErrTooLarge
	}
	return append(data, '\n'), nil
}

func corrupt(n int, format string, args ...any) error {
	return fmt.Errorf("%w: line %d: %s", ErrCorrupt, n, fmt.Sprintf(format, args...))
}

// decodeLine reads one line strictly: no unknown member, nothing after the
// object, and only the members of its kind.
func decodeLine(raw []byte) (line, error) {
	var l line
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return line{}, errors.New("not a record")
	}
	if _, err := dec.Token(); err != io.EOF {
		return line{}, errors.New("data after the record")
	}
	if l.Result != nil && string(l.Result) == "null" {
		return line{}, errors.New("a null result")
	}
	present := func(set ...string) []string { return set }
	var want []string  // the members that must be set
	var allow []string // the members that may be set besides
	switch l.Kind {
	case kindAdmit:
		want = present("at", "client", "id", "op", "role", "digest")
	case kindFinish:
		want, allow = present("at", "client", "id", "status"), present("code", "message", "result")
	case kindInterrupted:
		want = present("at", "client", "id", "status")
	case kindTombstone:
		want = present("key")
	default:
		return line{}, errors.New("unknown kind")
	}
	set := map[string]bool{
		"at": l.At != "", "client": l.Client != "", "id": l.ID != "", "op": l.Op != "", "role": l.Role != "",
		"digest": l.Digest != "", "status": l.Status != "", "code": l.Code != "", "message": l.Message != "",
		"result": len(l.Result) > 0, "key": l.Key != "",
	}
	ok := map[string]bool{}
	for _, m := range want {
		if !set[m] {
			return line{}, fmt.Errorf("the %s line lacks %s", l.Kind, m)
		}
		ok[m] = true
	}
	for _, m := range allow {
		ok[m] = true
	}
	for m, isSet := range set {
		if isSet && !ok[m] {
			return line{}, fmt.Errorf("the %s line has %s", l.Kind, m)
		}
	}
	return l, nil
}

// parseHash reads 64 lower-case hex digits.
func parseHash(s string) ([sha256.Size]byte, bool) {
	var out [sha256.Size]byte
	if len(s) != 2*sha256.Size || s != string(bytes.ToLower([]byte(s))) {
		return out, false
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, false
	}
	copy(out[:], b)
	return out, true
}

// replayLine applies one line of the file to the state, or says why the file is
// corrupt.
func (t *Table) replayLine(n int, raw []byte) error {
	l, err := decodeLine(raw)
	if err != nil {
		return corrupt(n, "%v", err)
	}
	if l.Kind == kindTombstone {
		h, ok := parseHash(l.Key)
		if !ok {
			return corrupt(n, "the tombstone key is not 64 hex digits")
		}
		if _, dup := t.tombs[h]; dup {
			return corrupt(n, "a tombstone is written twice")
		}
		t.addTombstone(h)
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, l.At)
	if err != nil {
		return corrupt(n, "the time is not RFC 3339")
	}
	at = at.UTC()
	if !validClient(l.Client) || !validID(l.ID) {
		return corrupt(n, "the client or the command id is not well formed")
	}
	k := key{l.Client, l.ID}
	switch l.Kind {
	case kindAdmit:
		role, err := leadrole.Parse(l.Role)
		if err != nil || !validOp(l.Op) {
			return corrupt(n, "the role or the operation is not valid")
		}
		digest, ok := parseHash(l.Digest)
		if !ok {
			return corrupt(n, "the digest is not 64 hex digits")
		}
		if _, dup := t.receipts[k]; dup {
			return corrupt(n, "the key is admitted twice")
		}
		if _, dead := t.tombs[k.hash()]; dead {
			return corrupt(n, "the key is admitted after its tombstone")
		}
		if _, dup := t.open[l.ID]; dup {
			return corrupt(n, "the command id of an open command is admitted again")
		}
		t.add(&entry{
			key: k, role: role, digest: digest,
			rec: Receipt{CommandID: l.ID, Op: l.Op, Role: role.String(), Status: StatusInProgress, AdmittedAt: at},
		})
	case kindFinish, kindInterrupted:
		e, ok := t.receipts[k]
		if !ok || e.rec.Status.final() {
			return corrupt(n, "the %s line is for a command that is not open", l.Kind)
		}
		if l.Kind == kindFinish {
			if !l.Status.final() || !validToken(l.Code) || !validMessage(l.Message) {
				return corrupt(n, "the finish line has a status, code or message that is not valid")
			}
			e.rec.Status, e.rec.Code, e.rec.Message = l.Status, l.Code, l.Message
			if len(l.Result) > 0 {
				e.rec.Result = append(json.RawMessage(nil), l.Result...)
			}
		} else {
			if l.Status != StatusUncertain && l.Status != StatusUnavailable {
				return corrupt(n, "the interrupted line has a status that is not uncertain or unavailable")
			}
			e.rec.Status, e.rec.Code, e.rec.Message = l.Status, CodeInterrupted, MessageInterrupted
			e.interrupted = true
		}
		e.rec.FinishedAt = &at
		delete(t.open, l.ID)
	}
	return nil
}

// start replays the file, cuts an unfinished last line, ends every open command
// with an interrupted line, and compacts the file when it holds too much.
func (t *Table) start(dirFD int, path string) error {
	rd := bufio.NewReaderSize(t.f, MaxLineBytes+1)
	var offset int64
	n := 0
	for {
		raw, err := rd.ReadSlice('\n')
		if err == nil {
			n++
			if err := t.replayLine(n, raw[:len(raw)-1]); err != nil {
				return err
			}
			offset += int64(len(raw))
			continue
		}
		if err == bufio.ErrBufferFull {
			return corrupt(n+1, "the line is longer than %d bytes", MaxLineBytes)
		}
		if err != io.EOF {
			return fmt.Errorf("leadcontrol: cannot read %s: %w", path, err)
		}
		if len(raw) > 0 {
			// An append that never returned: no caller acted on it.
			if err := t.f.Truncate(offset); err != nil {
				return fmt.Errorf("leadcontrol: cannot cut the unfinished line: %w", err)
			}
			if err := t.f.Sync(); err != nil {
				return fmt.Errorf("leadcontrol: cannot sync the cut: %w", err)
			}
		}
		break
	}
	if err := t.endOpenCommands(); err != nil {
		return err
	}
	changed := false
	for len(t.receipts) > t.maxReceipts && t.evictOne() {
		changed = true
	}
	if changed || t.trimmed {
		// The file holds more than the table keeps: write only what it keeps.
		return t.compact(dirFD, path)
	}
	return nil
}

// endOpenCommands gives every command that has no final line an interrupted
// line, in admission order, in one write.
func (t *Table) endOpenCommands() error {
	var buf bytes.Buffer
	at := t.now().UTC()
	type ending struct {
		e  *entry
		st Status
	}
	var ended []ending
	for _, e := range t.order {
		if e.rec.Status.final() {
			continue
		}
		st := StatusUncertain
		if e.rec.Op == OpTicket {
			st = StatusUnavailable
		}
		data, err := encodeLine(line{Kind: kindInterrupted, At: formatTime(at), Client: e.key.client, ID: e.key.id, Status: st})
		if err != nil {
			return err
		}
		buf.Write(data)
		ended = append(ended, ending{e, st})
	}
	if len(ended) == 0 {
		return nil
	}
	if n, err := t.f.Write(buf.Bytes()); err != nil || n != buf.Len() {
		if err == nil {
			err = errors.New("short write")
		}
		return fmt.Errorf("leadcontrol: cannot write the interrupted lines: %w", err)
	}
	if err := t.f.Sync(); err != nil {
		return fmt.Errorf("leadcontrol: cannot sync the interrupted lines: %w", err)
	}
	for _, en := range ended {
		finished := at
		en.e.rec.Status, en.e.rec.Code, en.e.rec.Message = en.st, CodeInterrupted, MessageInterrupted
		en.e.rec.FinishedAt = &finished
		en.e.interrupted = true
		delete(t.open, en.e.key.id)
	}
	return nil
}

// snapshot returns the lines that rebuild the state: the tombstones, then every
// held receipt in admission order with its final line.
func (t *Table) snapshot() ([]byte, error) {
	var buf bytes.Buffer
	for _, h := range t.tombOrder {
		data, err := encodeLine(line{Kind: kindTombstone, Key: hex.EncodeToString(h[:])})
		if err != nil {
			return nil, err
		}
		buf.Write(data)
	}
	for _, e := range t.order {
		data, err := encodeLine(line{
			Kind: kindAdmit, At: formatTime(e.rec.AdmittedAt), Client: e.key.client, ID: e.key.id,
			Op: e.rec.Op, Role: e.rec.Role, Digest: hex.EncodeToString(e.digest[:]),
		})
		if err != nil {
			return nil, err
		}
		buf.Write(data)
		if !e.rec.Status.final() {
			continue
		}
		l := line{Kind: kindFinish, At: formatTime(*e.rec.FinishedAt), Client: e.key.client, ID: e.key.id, Status: e.rec.Status}
		if e.interrupted {
			l.Kind = kindInterrupted
		} else {
			l.Code, l.Message, l.Result = e.rec.Code, e.rec.Message, e.rec.Result
		}
		if data, err = encodeLine(l); err != nil {
			return nil, err
		}
		buf.Write(data)
	}
	return buf.Bytes(), nil
}
