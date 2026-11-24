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

// openLine is the first line of every file: the version and the floor of
// the sequence numbers, the next one to give at least.
type openLine struct {
	Kind string `json:"kind"`
	V    int    `json:"v"`
	Next int64  `json:"next"`
}

// switchLine is a change of the lead's chat switch, with its generation.
type switchLine struct {
	Kind      string `json:"kind"`
	Chat      string `json:"chat"`
	Gen       int64  `json:"gen"`
	At        string `json:"at"`
	Epoch     int64  `json:"epoch"`
	Discarded int64  `json:"discarded"`
}

// entryLine is one entry of the conversation.
type entryLine struct {
	Kind  string `json:"kind"`
	Seq   int64  `json:"seq"`
	Gen   int64  `json:"gen"`
	Epoch int64  `json:"epoch"`
	Who   string `json:"who"`
	At    string `json:"at"`
	Text  string `json:"text"`
	Src   string `json:"src,omitempty"`
}

// members are the members each kind of line must have, and may have besides.
var members = map[string]struct{ want, allow []string }{
	kindOpen:   {want: []string{"kind", "v", "next"}},
	kindSwitch: {want: []string{"kind", "chat", "gen", "at", "epoch", "discarded"}},
	kindEntry:  {want: []string{"kind", "seq", "gen", "epoch", "who", "at", "text"}, allow: []string{"src"}},
}

// encodeLine marshals one line with its newline, without escaping HTML
// characters, and refuses one over [MaxLineBytes].
func encodeLine(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if buf.Len() > MaxLineBytes {
		return nil, fmt.Errorf("%w: a line of %d bytes", ErrInvalid, buf.Len())
	}
	return buf.Bytes(), nil
}

// strict decodes raw into v: no unknown member, nothing after the object.
func strict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("not a record")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the record")
	}
	return nil
}

// decodeLine reads one line strictly: its kind, then exactly the members of
// that kind, then the line itself. It returns one of openLine, switchLine
// and entryLine.
func decodeLine(raw []byte) (any, error) {
	var set map[string]json.RawMessage
	if err := strict(raw, &set); err != nil {
		return nil, err
	}
	var kind string
	if err := json.Unmarshal(set["kind"], &kind); err != nil {
		return nil, errors.New("no kind")
	}
	m, ok := members[kind]
	if !ok {
		return nil, errors.New("unknown kind")
	}
	for _, name := range m.want {
		if _, ok := set[name]; !ok {
			return nil, fmt.Errorf("the %s line lacks %s", kind, name)
		}
	}
	for name := range set {
		if !slices.Contains(m.want, name) && !slices.Contains(m.allow, name) {
			return nil, fmt.Errorf("the %s line has %s", kind, name)
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

// validSrc reports 32 lower-case hexadecimal digits.
func validSrc(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
