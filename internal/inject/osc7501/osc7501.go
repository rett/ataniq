package osc7501

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// QueryReply is the reply to the support query: ESC ] 7501 ; ? ESC \. An agent
// that sent the query takes any reply whose data starts with "?" as the answer.
const QueryReply = "\x1b]7501;?\x1b\\"

// Limits applied by Parse. They are fixed, not configuration.
const (
	// MaxPayload is the longest payload Parse reads, in bytes.
	MaxPayload = 4096
	// MaxText is the most bytes of a decoded msg or title that are kept.
	MaxText = 1024
	// MaxToken is the longest app or kind value, in bytes.
	MaxToken = 64
	// MaxFields is the most fields a payload may hold.
	MaxFields = 32
)

// prefix may start a payload: some emulators pass the OSC data with its
// command number, others without.
const prefix = "7501;"

// State is the state an agent reports.
type State string

// The six states of a report.
const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateDone    State = "done"
	StateBlocked State = "blocked"
	StateError   State = "error"
	StateClear   State = "clear"
)

// Valid reports whether s is one of the six states.
func (s State) Valid() bool {
	switch s {
	case StateIdle, StateWorking, StateDone, StateBlocked, StateError, StateClear:
		return true
	}
	return false
}

// Report is one parsed status report. The zero Report is "no report"; Parse
// returns it with every error.
type Report struct {
	State State
	// App names the agent, for example codex or claude-code. Empty when the
	// report has none (state=clear has none).
	App string
	// Kind says why the agent is blocked, for example permission. Empty when
	// absent.
	Kind string
	// Msg and Title are the decoded msg and title fields: valid UTF-8, with
	// control characters replaced and at most MaxText bytes. They are text the
	// agent chose; show them as text, never act on them.
	Msg   string
	Title string
	// Unknown counts the keys the parser does not know. They are ignored.
	Unknown int
}

// Errors returned by Parse. They are wrapped with detail; test with errors.Is.
var (
	ErrEmpty     = errors.New("osc7501: empty payload")
	ErrTooLong   = errors.New("osc7501: payload too long")
	ErrField     = errors.New("osc7501: malformed field")
	ErrDuplicate = errors.New("osc7501: duplicate key")
	ErrState     = errors.New("osc7501: missing or unknown state")
	ErrToken     = errors.New("osc7501: app or kind is not a short token")
	ErrText      = errors.New("osc7501: msg or title cannot be read")
)

func trimPrefix(payload []byte) []byte {
	return bytes.TrimPrefix(payload, []byte(prefix))
}

// IsQuery reports whether payload is the support query: the payload, after an
// optional leading "7501;", starts with "?".
func IsQuery(payload []byte) bool {
	p := trimPrefix(payload)
	return len(p) > 0 && p[0] == '?'
}

// Reply returns the bytes to write to the agent for a query payload, and false
// for anything else. The result is a new slice.
//
// Ordering rule: the agent sends the query and then DA1 (ESC [ c) as a
// sentinel, and it reports only when the reply reaches it before the DA1
// reply. The holder therefore calls Reply from the emulator's OSC 7501
// handler, while the emulator is parsing the query, and writes the bytes to
// the agent at once, so they are queued ahead of the reply the emulator makes
// to the DA1 that follows. A reply written later, from another goroutine
// after the emulator's write returns, can lose that race.
func Reply(payload []byte) ([]byte, bool) {
	if !IsQuery(payload) {
		return nil, false
	}
	return []byte(QueryReply), true
}

// Parse reads one report. The payload is the OSC data after "7501;"; a leading
// "7501;" is tolerated. The payload is at most MaxPayload bytes and holds at
// most MaxFields fields separated by ":", each key=value, with no key twice.
// state is required and must be one of the six states. msg and title are
// standard base64 of valid UTF-8; the decoded text has control characters (and
// the line separators and bidirectional overrides that can disguise text)
// replaced by U+FFFD and is cut to MaxText bytes at a character boundary. app
// and kind are kept only as tokens of letters, digits, "-" and "_" up to
// MaxToken bytes. Other keys are ignored and counted in Report.Unknown.
//
// Any error returns the zero Report: a report that cannot be read is not
// guessed. A query is not a report; Parse returns ErrField for it.
func Parse(payload []byte) (Report, error) {
	p := trimPrefix(payload)
	if len(p) == 0 {
		return Report{}, ErrEmpty
	}
	if len(p) > MaxPayload {
		return Report{}, fmt.Errorf("%w: %d bytes, at most %d", ErrTooLong, len(p), MaxPayload)
	}
	var (
		r     Report
		seen  = make(map[string]struct{}, 6)
		state bool
		rest  = string(p)
	)
	for n := 0; ; n++ {
		if n >= MaxFields {
			return Report{}, fmt.Errorf("%w: more than %d fields", ErrField, MaxFields)
		}
		field, tail, more := strings.Cut(rest, ":")
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" {
			return Report{}, fmt.Errorf("%w: field %d is not key=value", ErrField, n+1)
		}
		if !printableASCII(key) {
			return Report{}, fmt.Errorf("%w: field %d has an unreadable key", ErrField, n+1)
		}
		if _, dup := seen[key]; dup {
			return Report{}, fmt.Errorf("%w: %q", ErrDuplicate, key)
		}
		seen[key] = struct{}{}
		switch key {
		case "state":
			r.State = State(value)
			if !r.State.Valid() {
				return Report{}, ErrState
			}
			state = true
		case "app", "kind":
			if !token(value) {
				return Report{}, fmt.Errorf("%w: %s", ErrToken, key)
			}
			if key == "app" {
				r.App = value
			} else {
				r.Kind = value
			}
		case "msg", "title":
			text, err := decodeText(value)
			if err != nil {
				return Report{}, fmt.Errorf("%w: %s: %v", ErrText, key, err)
			}
			if key == "msg" {
				r.Msg = text
			} else {
				r.Title = text
			}
		default:
			r.Unknown++
		}
		if !more {
			break
		}
		rest = tail
	}
	if !state {
		return Report{}, ErrState
	}
	return r, nil
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func token(s string) bool {
	if s == "" || len(s) > MaxToken {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func decodeText(v string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", errors.New("bad base64")
	}
	if !utf8.Valid(raw) {
		return "", errors.New("not valid UTF-8")
	}
	var b strings.Builder
	for _, r := range string(raw) {
		if unsafeRune(r) {
			r = utf8.RuneError
		}
		if b.Len()+utf8.RuneLen(r) > MaxText {
			break
		}
		b.WriteRune(r)
	}
	return b.String(), nil
}

// unsafeRune reports a rune that must not reach a screen from agent text:
// control characters, the line and paragraph separators and the
// bidirectional embedding, override and isolate controls.
func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || r == 0x2028 || r == 0x2029 ||
		(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}
