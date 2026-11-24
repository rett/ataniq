//go:build linux && ataniq_canary

package leadchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
	"unicode/utf8"
)

// MaxLineBytes is the longest line of a chat file, its newline included.
const MaxLineBytes = 8 << 10

// The kinds of line, and the version of the open line.
const (
	kindOpen    = "open"
	kindSwitch  = "switch"
	kindEntry   = "entry"
	fileVersion = 1
)

// timeLayout is the one form of a time in the file and on the wire: UTC to
// the second, ending in Z.
const timeLayout = "2006-01-02T15:04:05Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(timeLayout, s)
	if err != nil || t.Format(timeLayout) != s {
		return time.Time{}, false
	}
	return t, true
}

// sinceLayout is the form of the moment a turn-on found the outbox empty:
// UTC to the nanosecond, ending in Z.
const sinceLayout = time.RFC3339Nano

func formatSince(t time.Time) string { return t.UTC().Format(sinceLayout) }

func parseSince(s string) (time.Time, bool) {
	t, err := time.Parse(sinceLayout, s)
	if err != nil || formatSince(t) != s {
		return time.Time{}, false
	}
	return t, true
}

// openLine is the first line of every file: the version, and the
// allocation high-waters: the next sequence number to give at least, and the
// highest claim id given.
type openLine struct {
	Kind  string `json:"kind"`
	V     int    `json:"v"`
	Next  int64  `json:"next"`
	Claim int64  `json:"claim"`
}

// switchLine is a change of the lead's chat switch, with its generation. A
// turn-on also records the moment it found the outbox empty (since, to the
// nanosecond) and the outbox's identity ("device:inode").
type switchLine struct {
	Kind      string `json:"kind"`
	Chat      string `json:"chat"`
	Gen       int64  `json:"gen"`
	At        string `json:"at"`
	Epoch     int64  `json:"epoch"`
	Discarded int64  `json:"discarded"`
	Since     string `json:"since,omitempty"`
	Outbox    string `json:"outbox,omitempty"`
}

// entryLine is one entry of the conversation. claim is the claim id of the
// reply it was stored from.
type entryLine struct {
	Kind  string `json:"kind"`
	Seq   int64  `json:"seq"`
	Gen   int64  `json:"gen"`
	Epoch int64  `json:"epoch"`
	Who   string `json:"who"`
	At    string `json:"at"`
	Text  string `json:"text"`
	Claim int64  `json:"claim,omitempty"`
}

// members are the members each kind of line must have, and may have besides.
var members = map[string]struct{ want, allow []string }{
	kindOpen:   {want: []string{"kind", "v", "next", "claim"}},
	kindSwitch: {want: []string{"kind", "chat", "gen", "at", "epoch", "discarded"}, allow: []string{"since", "outbox"}},
	kindEntry:  {want: []string{"kind", "seq", "gen", "epoch", "who", "at", "text"}, allow: []string{"claim"}},
}

// encodeLine marshals one line with its newline, without escaping HTML
// characters, and refuses one over [MaxLineBytes].
func encodeLine(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("%w: a line does not encode", ErrInvalid)
	}
	if buf.Len() > MaxLineBytes {
		return nil, fmt.Errorf("%w: a line of %d bytes", ErrInvalid, buf.Len())
	}
	return buf.Bytes(), nil
}

// The reasons a line is refused. Each is fixed text: no reason holds a byte
// of the file, so an error that is logged shows nothing a damaged file holds.
var (
	errNotRecord  = errors.New("not one JSON object")
	errBadUTF8    = errors.New("not valid UTF-8")
	errTwice      = errors.New("a member appears twice")
	errNull       = errors.New("a member is null")
	errNoKind     = errors.New("no kind, or a kind that is not known")
	errLacks      = errors.New("a member the kind needs is missing")
	errForeign    = errors.New("a member the kind does not have")
	errMemberType = errors.New("a member has the wrong type")
	errDataAfter  = errors.New("data after the record")
)

// objectMembers reads raw as exactly one JSON object and returns its
// members: a member named twice, a null member and anything after the
// object are refused.
func objectMembers(raw []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, errNotRecord
	}
	set := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, errNotRecord
		}
		name, ok := tok.(string)
		if !ok {
			return nil, errNotRecord
		}
		if _, dup := set[name]; dup {
			return nil, errTwice
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, errNotRecord
		}
		if string(v) == "null" {
			return nil, errNull
		}
		set[name] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errNotRecord
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errDataAfter
	}
	return set, nil
}

// strict decodes raw into v: no unknown member, nothing after the object.
func strict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errMemberType
	}
	if _, err := dec.Token(); err != io.EOF {
		return errDataAfter
	}
	return nil
}

// decodeLine reads one line strictly: valid UTF-8, one object whose members
// are each named once and none null, its kind, then exactly the members of
// that kind, each of its type. It returns one of openLine, switchLine and
// entryLine. An error is one of the fixed reasons above.
func decodeLine(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errBadUTF8
	}
	set, err := objectMembers(raw)
	if err != nil {
		return nil, err
	}
	var kind string
	if err := json.Unmarshal(set["kind"], &kind); err != nil {
		return nil, errNoKind
	}
	m, ok := members[kind]
	if !ok {
		return nil, errNoKind
	}
	for _, name := range m.want {
		if _, ok := set[name]; !ok {
			return nil, errLacks
		}
	}
	for name := range set {
		if !slices.Contains(m.want, name) && !slices.Contains(m.allow, name) {
			return nil, errForeign
		}
	}
	switch kind {
	case kindOpen:
		var l openLine
		if err := strict(raw, &l); err != nil {
			return nil, err
		}
		return l, nil
	case kindSwitch:
		var l switchLine
		if err := strict(raw, &l); err != nil {
			return nil, err
		}
		return l, nil
	}
	var l entryLine
	if err := strict(raw, &l); err != nil {
		return nil, err
	}
	return l, nil
}
