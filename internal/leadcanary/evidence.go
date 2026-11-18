//go:build linux && ataniq_canary

package leadcanary

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/wire"
)

// EvidenceName is the file name of the evidence, in the state root.
const EvidenceName = "canary-evidence.jsonl"

// maxEvidenceLine is the longest line the evidence writes, newline included.
// The fields are bounded (the detail at 512 bytes), so the cap is never met by
// a valid entry; it is a backstop.
const maxEvidenceLine = 4096

// EvidencePath returns the path of the evidence file under the state root.
func (c Config) EvidencePath() string { return path.Join(c.StateRoot, EvidenceName) }

// Event names one kind of line of the evidence.
type Event string

// The events of the evidence.
const (
	// EventDaemonStart: the daemon is listening in canary mode.
	EventDaemonStart Event = "daemon-start"
	// EventDefine: the role was defined in the registry.
	EventDefine Event = "define"
	// EventStart: the role was started; the line carries the waiver.
	EventStart Event = "start"
	// EventTicket: a ticket was minted (never the ticket itself).
	EventTicket Event = "ticket"
	// EventInjectRefused: the message was refused before it reached the
	// holder (code says why).
	EventInjectRefused Event = "inject-refused"
	// EventInjectSent: the request frame is about to be written; the message
	// is spent from here.
	EventInjectSent Event = "inject-sent"
	// EventInjectResult: the result of the message, or the uncertain result
	// that stands in for no answer.
	EventInjectResult Event = "inject-result"
	// EventStop: the role was stopped.
	EventStop Event = "stop"
)

// ErrEntry is wrapped by every refusal of an [Entry]. Nothing is written for a
// refused entry.
var ErrEntry = errors.New("leadcanary: invalid evidence entry")

// Entry is what a caller gives [Evidence.Append]. The role, the agent kind, the
// time and, for the define and start lines, the qualification, login and waiver
// words are added by the writer. There is no field for the body of a message,
// and none may carry it.
type Entry struct {
	// Event is required.
	Event Event
	// Epoch is the epoch the line is about; 0 leaves it out. The start,
	// ticket, inject-sent and inject-result lines require it.
	Epoch uint64
	// MessageID is empty or a message id of the wire's grammar. The
	// inject-sent and inject-result lines require it.
	MessageID string
	// Outcome, Reason and Written are the words of a wire result, as
	// [ResultEntry] sets them; each is empty or a short token of a-z, 0-9 and
	// . _ -. The inject-result line requires Outcome and Written.
	Outcome, Reason, Written string
	// Detail is printable ASCII, at most 512 bytes. It must not hold a message
	// body.
	Detail string
	// Code is a short token (an [AdmitCode], for example). The inject-refused
	// line requires it.
	Code string
}

// evidenceLine is one line of the file, in the order of its keys.
type evidenceLine struct {
	At            string `json:"at"`
	Event         Event  `json:"event"`
	Role          string `json:"role"`
	Epoch         uint64 `json:"epoch,omitempty"`
	MessageID     string `json:"message_id,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Written       string `json:"written,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Waiver        string `json:"waiver,omitempty"`
	Agent         string `json:"agent"`
	Code          string `json:"code,omitempty"`
	Qualification string `json:"qualification,omitempty"`
	Login         string `json:"login,omitempty"`
}

// Evidence appends the events of a canary run to
// <state root>/canary-evidence.jsonl: mode 0600, opened once by its handle,
// O_APPEND, one JSON object and one LF per event, synced after each. It is safe
// for concurrent use.
type Evidence struct {
	mu   sync.Mutex
	f    *os.File
	role string
	kind string
	now  func() time.Time
	torn bool
}

// OpenEvidence opens the evidence file under the state root of the config,
// creating it with mode 0600 when it is not there. The file is opened without
// following a symbolic link at its last element and judged by its handle: it
// must be a regular file of the caller with mode 0600. The state root must
// exist. A nil now means time.Now. The role and the agent kind of the config go
// on every line.
func OpenEvidence(cfg Config, now func() time.Time) (*Evidence, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	p := path.Join(cfg.StateRoot, EvidenceName)
	const flags = unix.O_WRONLY | unix.O_APPEND | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	created := true
	fd, err := unix.Open(p, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
	if errors.Is(err, unix.EEXIST) {
		created = false
		fd, err = unix.Open(p, flags, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("leadcanary: cannot open the evidence file: %w", err)
	}
	f := os.NewFile(uintptr(fd), p)
	fail := func(format string, args ...any) (*Evidence, error) {
		_ = f.Close()
		return nil, fmt.Errorf(format, args...)
	}
	if created {
		// The umask may have cleared bits of the mode; the file is 0600 exactly.
		if err := unix.Fchmod(fd, 0o600); err != nil {
			return fail("leadcanary: cannot set the mode of the evidence file: %w", err)
		}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fail("leadcanary: cannot examine the evidence file: %w", err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return fail("leadcanary: the evidence file is not a regular file")
	case int(st.Uid) != unix.Geteuid():
		return fail("leadcanary: the evidence file is not owned by the caller")
	case st.Mode&07777 != 0o600:
		return fail("leadcanary: the evidence file has mode %04o, want 0600", st.Mode&07777)
	case st.Nlink != 1:
		// A second link could make this append to a file that is also kept
		// somewhere else.
		return fail("leadcanary: the evidence file has more than one link")
	}
	return &Evidence{f: f, role: cfg.Role.String(), kind: string(cfg.Agent.Kind), now: now}, nil
}

// Close closes the file. Appending after it is an error.
func (e *Evidence) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.f == nil {
		return nil
	}
	err := e.f.Close()
	e.f = nil
	return err
}

// Append writes one line for the entry and syncs it. The define and start lines
// carry "qualification" and "login" ([QualificationText], [LoginText]), and the
// start line also carries "waiver" (canaryroot.WaiverText); every line carries
// the role and the agent kind. An entry that does not pass its checks is
// refused with an error that wraps [ErrEntry] and nothing is written. A failed
// write is returned, and a part of a line that it left is ended by a newline
// before the next line.
func (e *Evidence) Append(en Entry) error {
	if err := en.check(); err != nil {
		return err
	}
	// The lock is held from before the time is read until the line is written,
	// so that the times in the file never go back.
	e.mu.Lock()
	defer e.mu.Unlock()
	l := evidenceLine{
		At: e.now().UTC().Format(time.RFC3339Nano), Event: en.Event, Role: e.role, Epoch: en.Epoch,
		MessageID: en.MessageID, Outcome: en.Outcome, Reason: en.Reason, Written: en.Written,
		Detail: en.Detail, Agent: e.kind, Code: en.Code,
	}
	switch en.Event {
	case EventDefine:
		l.Qualification, l.Login = QualificationText, LoginText
	case EventStart:
		l.Qualification, l.Login = QualificationText, LoginText
		l.Waiver = canaryroot.WaiverText
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(l); err != nil { // appends the LF
		return fmt.Errorf("%w: %v", ErrEntry, err)
	}
	if buf.Len() > maxEvidenceLine {
		return fmt.Errorf("%w: the line is longer than %d bytes", ErrEntry, maxEvidenceLine)
	}
	if e.f == nil {
		return errors.New("leadcanary: the evidence file is closed")
	}
	out := buf.Bytes()
	if e.torn {
		out = append([]byte{'\n'}, out...)
	}
	n, err := e.f.Write(out)
	if err != nil || n != len(out) {
		e.torn = true
		if err == nil {
			err = errors.New("short write")
		}
		return fmt.Errorf("leadcanary: cannot write the evidence: %w", err)
	}
	e.torn = false
	if err := e.f.Sync(); err != nil {
		return fmt.Errorf("leadcanary: cannot sync the evidence: %w", err)
	}
	return nil
}

// check applies the rules of an entry.
func (en Entry) check() error {
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrEntry, fmt.Sprintf(format, args...))
	}
	switch en.Event {
	case EventDaemonStart, EventDefine, EventStop, EventStart, EventTicket,
		EventInjectRefused, EventInjectSent, EventInjectResult:
	default:
		return refuse("unknown event")
	}
	if en.MessageID != "" && wire.CheckMessageID(en.MessageID) != nil {
		return refuse("the message id is not well formed")
	}
	for _, f := range []struct{ name, val string }{
		{"outcome", en.Outcome}, {"reason", en.Reason}, {"written", en.Written}, {"code", en.Code},
	} {
		if !tokenOrEmpty(f.val) {
			return refuse("%s is not a short token", f.name)
		}
	}
	if wire.CheckInjectDetail(en.Detail) != nil {
		return refuse("the detail is not printable ASCII of at most %d bytes", wire.MaxInjectDetail)
	}
	needEpoch := en.Event == EventStart || en.Event == EventTicket ||
		en.Event == EventInjectSent || en.Event == EventInjectResult
	switch {
	case needEpoch && en.Epoch == 0:
		return refuse("the %s line needs an epoch", en.Event)
	case (en.Event == EventInjectSent || en.Event == EventInjectResult) && en.MessageID == "":
		return refuse("the %s line needs a message id", en.Event)
	case en.Event == EventInjectResult && (en.Outcome == "" || en.Written == ""):
		return refuse("the inject-result line needs an outcome and written")
	case en.Event == EventInjectRefused && en.Code == "":
		return refuse("the inject-refused line needs a code")
	}
	return nil
}

// tokenOrEmpty reports an empty string or 1 to 64 characters of a-z, 0-9 and
// . _ -.
func tokenOrEmpty(s string) bool {
	if len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
