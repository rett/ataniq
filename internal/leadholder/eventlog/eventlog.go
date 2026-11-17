// Package eventlog is the holder's "always" event log of the lead-hosting
// design (section 1.7): JSON lines in a folder of the lead's state, mode 0600,
// size-capped and rotated.
//
// # What it records
//
// Start and exit, attach and detach, writer changes with the client, resizes,
// state changes, every injection attempt, every refusal and the cap counters.
// It never records keystrokes or output text. The API makes that hard to get
// wrong: [Log.Append] takes a kind, an event name and a [Fields] value, and
// [Fields] is a struct of small typed values. It has no field that takes input
// bytes or screen text, no []byte, no free map[string]any and no interface.
// Every string is capped, and a code-like string (reason, state, digest,
// counter name, message identity, sender, outcome, guard reading, command
// identity) is also restricted to a small character set. The injection record
// of design 1.7 has its own typed fields: MessageID, Sender, Outcome, Guard
// (a short list of codes) and Digest; CommandID names the control command that
// caused an event.
//
// Two checks in the tests keep it that way (design case H14). One freezes the
// exported fields of [Event] and [Fields], by name, type and JSON tag, against
// a list a reviewer has read: any new field fails there until the list is
// changed on purpose. The other rejects, by type and by name, a field that
// could carry bytes, a slice of bytes, an interface or a word like "text".
// Neither can tell what a caller puts into a string field, so the free-text
// fields are named here.
//
// # Free text
//
// Four fields take any text, capped and made valid UTF-8, but not restricted
// to a character set. ClientName and ClientCommand come from the peer's
// process (the client's name and its command line). Role and Epoch come from
// the holder's own configuration. The caller must take them only from those
// sources: never from the terminal, the session's output or anything typed.
// Every other string is a code. The package cannot check where a value came
// from; the frozen field set above is what stops a new text field.
//
// # Files and bounds
//
// The log is events.jsonl in a folder that must already exist, belong to the
// caller and have mode 0700. When the next event would take the file past
// [Options.MaxBytes] (default 4 MiB) the file is renamed to events.jsonl.1,
// replacing an older one, and a new file is started. So the file is never
// larger than the cap and the total kept is at most twice the cap. One event
// is never split across the two files, and an event over [MaxEventBytes]
// encoded is refused and counted, not written. Every cap that is hit is
// counted in [Stats].
//
// # Durability
//
// Each event is one write(2) in append mode, so it survives a crash of the
// holder process. There is deliberately no fsync per event: it would stall the
// holder on a slow disk and the log is evidence, not a journal. It does not
// survive a power loss or a kernel crash unless [Log.Sync] ran after it. The
// holder calls Sync (or Close, which syncs) for its final record.
//
// # Failures
//
// A write error is returned and counted, and the log keeps working when the
// disk recovers. If a failed write left part of a line in the file, the next
// event first writes a newline so the fragment cannot join the next record;
// [Read] skips such a fragment and reports it. The sequence number is taken
// before the write, so a lost event leaves a gap that shows in the file.
//
// The package has one writer per folder: two processes appending to the same
// folder are not coordinated (the holder's role lock gives the exclusivity).
package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// File names inside the log folder.
const (
	FileName    = "events.jsonl"
	RotatedName = "events.jsonl.1"
)

// Size limits.
const (
	// DefaultMaxBytes is the default size at which the file is rotated.
	DefaultMaxBytes = 4 << 20
	// MinMaxBytes and MaxMaxBytes bound Options.MaxBytes. The minimum is
	// above two maximum events so that any accepted event fits an empty file.
	MinMaxBytes = 32 << 10
	MaxMaxBytes = 16 << 20
	// MaxEventBytes is the largest encoded event, without its newline.
	MaxEventBytes = 16 << 10
)

// Caps on the typed fields, in bytes, applied before encoding.
const (
	MaxRole        = 128
	MaxEpoch       = 128
	MaxClientName  = 128
	MaxCommandLine = 512
	MaxReason      = 64
	MaxState       = 64
	MaxDigest      = 128
	MaxCounters    = 64
	MaxCounterName = 64
	// The injection record (design 1.7) and the command identity. They are
	// codes, not text.
	MaxMessageID  = 96
	MaxSender     = 96
	MaxOutcome    = 32
	MaxGuardCodes = 8  // entries in Fields.Guard
	MaxGuardCode  = 32 // bytes in one entry
	MaxCommandID  = 64
)

// Kind says what sort of statement an event is. The design records a provider
// report, an Ataniq observation and an owner decision as different kinds.
type Kind string

const (
	KindProviderReport    Kind = "provider-report"
	KindAtaniqObservation Kind = "ataniq-observation"
	KindOwnerDecision     Kind = "owner-decision"
	KindHolder            Kind = "holder"
)

// EventName is the event's name: one of the exported constants.
type EventName string

const (
	EventStart         EventName = "start"
	EventExit          EventName = "exit"
	EventAttach        EventName = "attach"
	EventDetach        EventName = "detach"
	EventWriterChange  EventName = "writer-change"
	EventResize        EventName = "resize"
	EventState         EventName = "state"
	EventInject        EventName = "inject"
	EventRefusal       EventName = "refusal"
	EventCounters      EventName = "counters"
	EventCheckMismatch EventName = "check-mismatch"
	EventResync        EventName = "resync"
	EventKeyHeld       EventName = "key-held"
)

var kinds = map[Kind]bool{
	KindProviderReport: true, KindAtaniqObservation: true,
	KindOwnerDecision: true, KindHolder: true,
}

var eventNames = map[EventName]bool{
	EventStart: true, EventExit: true, EventAttach: true, EventDetach: true,
	EventWriterChange: true, EventResize: true, EventState: true,
	EventInject: true, EventRefusal: true, EventCounters: true,
	EventCheckMismatch: true, EventResync: true, EventKeyHeld: true,
}

// Errors. Test with errors.Is.
var (
	// ErrUnsafe: the folder or file is not private to the caller, or a
	// symlink or a non-regular file is where the log file should be.
	ErrUnsafe = errors.New("eventlog: unsafe folder or file")
	// ErrInvalid: the kind or the event name is not an exported one.
	ErrInvalid = errors.New("eventlog: unknown kind or event name")
	// ErrTooLarge: the encoded event is over the cap (Append), or a file is
	// larger than Read will take (Read).
	ErrTooLarge = errors.New("eventlog: over the size cap")
	// ErrClosed: the log is closed.
	ErrClosed = errors.New("eventlog: closed")
	// ErrCorrupt: Read skipped lines that are not events.
	ErrCorrupt = errors.New("eventlog: lines skipped")
)

// Fields is everything an event can say besides its kind and name. All values
// are typed and bounded; zero values are left out of the line. There is
// deliberately no field that takes input bytes or screen text.
type Fields struct {
	Role          string            `json:"role,omitempty"`
	Epoch         string            `json:"epoch,omitempty"`
	ClientName    string            `json:"client,omitempty"`
	ClientPID     int               `json:"client_pid,omitempty"`
	ClientCommand string            `json:"client_cmd,omitempty"`
	Cols          int               `json:"cols,omitempty"`
	Rows          int               `json:"rows,omitempty"`
	Reason        string            `json:"reason,omitempty"` // reason code
	State         string            `json:"state,omitempty"`  // state name
	Counters      map[string]uint64 `json:"counters,omitempty"`
	Digest        string            `json:"digest,omitempty"` // digest of something, never the thing
	// The record of an injection, design 1.7: the identity of the message
	// in the lead's conversation, who sent it, what the guards read and
	// what came of it. Each is a code, never the message text; the digest
	// above stands for the text.
	MessageID string   `json:"message_id,omitempty"`
	Sender    string   `json:"sender,omitempty"`
	Outcome   string   `json:"outcome,omitempty"`
	Guard     []string `json:"guard,omitempty"` // guard readings, at most MaxGuardCodes codes
	// CommandID is the identity of the control command that caused the
	// event, a token of at most MaxCommandID bytes.
	CommandID  string `json:"command_id,omitempty"`
	ExitStatus *int   `json:"exit_status,omitempty"`
	// The three facts of design 1.4, each set only when known.
	StreamEnded *bool `json:"stream_ended,omitempty"`
	TUIExited   *bool `json:"tui_exited,omitempty"`
	TreeGone    *bool `json:"tree_gone,omitempty"`
}

// Event is one line of the log.
type Event struct {
	Time  time.Time `json:"time"`
	Seq   uint64    `json:"seq"`
	Kind  Kind      `json:"kind"`
	Event EventName `json:"event"`
	Fields
}

// Options configures Open. The zero value is valid.
type Options struct {
	// MaxBytes is the size at which events.jsonl is rotated; 0 means
	// DefaultMaxBytes. Values outside [MinMaxBytes, MaxMaxBytes] are refused.
	MaxBytes int64
	// Clock returns the time of an event; nil means time.Now. The time is
	// written in UTC.
	Clock func() time.Time
}

// Stats counts what the log did and every cap it hit.
type Stats struct {
	Written         uint64 // events written
	Rotations       uint64
	WriteErrors     uint64 // failed writes, rotations and reopens
	TooLarge        uint64 // events refused for size
	Invalid         uint64 // events refused for an unknown kind or name
	Truncated       uint64 // string fields cut to their cap
	Sanitized       uint64 // code-like strings with characters replaced
	CountersDropped uint64 // counter entries dropped over MaxCounters or for a name that became an earlier one
	GuardDropped    uint64 // guard readings dropped over MaxGuardCodes
}

type sink interface {
	io.Writer
	Sync() error
	Close() error
}

// Log is an open event log. It is safe for concurrent use.
type Log struct {
	mu       sync.Mutex
	dfd      int
	f        sink
	size     int64
	torn     bool // the file may end in a partial line
	seq      uint64
	closed   bool
	max      int64
	maxEvent int
	clock    func() time.Time
	stats    Stats
	buf      bytes.Buffer
}

// Open opens or creates events.jsonl in dir. The folder must exist, be owned
// by the caller and have mode 0700, and must not be a symlink; the file is
// created with mode 0600, must be a regular file of the caller, and must not
// be a symlink. Otherwise Open returns an error wrapping ErrUnsafe.
func Open(dir string, o Options) (*Log, error) {
	max := o.MaxBytes
	if max == 0 {
		max = DefaultMaxBytes
	}
	if max < MinMaxBytes || max > MaxMaxBytes {
		return nil, fmt.Errorf("eventlog: MaxBytes %d outside [%d, %d]", max, MinMaxBytes, MaxMaxBytes)
	}
	clock := o.Clock
	if clock == nil {
		clock = time.Now
	}
	dfd, err := openDir(dir)
	if err != nil {
		return nil, err
	}
	f, err := openFile(dfd, FileName, true)
	if err != nil {
		unix.Close(dfd)
		return nil, err
	}
	l := &Log{dfd: dfd, max: max, maxEvent: MaxEventBytes, clock: clock}
	if err := l.adopt(f); err != nil {
		f.Close()
		unix.Close(dfd)
		return nil, err
	}
	return l, nil
}

// adopt makes f the current file and reads its size and whether it ends in a
// partial line (a crashed writer left it).
func (l *Log) adopt(f *os.File) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return fmt.Errorf("eventlog: stat: %w", err)
	}
	l.f, l.size, l.torn = f, st.Size, false
	if st.Size > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], st.Size-1); err != nil || last[0] != '\n' {
			l.torn = true
		}
	}
	return nil
}

func (l *Log) closeFiles() error {
	var err error
	if l.f != nil {
		err = l.f.Close()
		l.f = nil
	}
	if l.dfd >= 0 {
		if e := unix.Close(l.dfd); err == nil {
			err = e
		}
		l.dfd = -1
	}
	return err
}

// Append records one event. It takes the time from the clock and the next
// sequence number, bounds the fields, and writes one line. It never blocks on
// anything but the file write. A write error is returned and counted; the log
// stays usable.
func (l *Log) Append(kind Kind, name EventName, f Fields) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	if !kinds[kind] || !eventNames[name] {
		l.stats.Invalid++
		return ErrInvalid
	}
	l.seq++
	ev := Event{Time: l.clock().UTC(), Seq: l.seq, Kind: kind, Event: name, Fields: l.bound(f)}
	l.buf.Reset()
	enc := json.NewEncoder(&l.buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&ev); err != nil {
		l.stats.Invalid++
		return fmt.Errorf("eventlog: encode: %w", err)
	}
	if n := l.buf.Len() - 1; n > l.maxEvent {
		l.stats.TooLarge++
		return fmt.Errorf("%w: event of %d bytes, cap %d", ErrTooLarge, n, l.maxEvent)
	}
	if l.f == nil { // a failed rotation left no file
		f, err := openFile(l.dfd, FileName, true)
		if err == nil {
			err = l.adopt(f)
			if err != nil {
				f.Close()
			}
		}
		if err != nil {
			l.stats.WriteErrors++
			return err
		}
	}
	if l.size > 0 && l.size+int64(l.buf.Len())+l.tornLen() > l.max {
		if err := l.rotate(); err != nil {
			l.stats.WriteErrors++
			return err
		}
	}
	out := l.buf.Bytes()
	if l.torn {
		out = append([]byte{'\n'}, out...)
	}
	n, err := l.f.Write(out)
	l.size += int64(n)
	if err != nil {
		if n > 0 {
			l.torn = true
		}
		l.stats.WriteErrors++
		return fmt.Errorf("eventlog: write: %w", err)
	}
	l.torn = false
	l.stats.Written++
	return nil
}

func (l *Log) tornLen() int64 {
	if l.torn {
		return 1
	}
	return 0
}

// rotate renames the file to the rotated name and starts a new one. On
// failure after the rename the log has no file and the next Append retries.
func (l *Log) rotate() error {
	if err := l.f.Close(); err != nil {
		l.f = nil
		return fmt.Errorf("eventlog: rotate: %w", err)
	}
	l.f = nil
	if err := unix.Renameat(l.dfd, FileName, l.dfd, RotatedName); err != nil {
		// Reopen the unrotated file so the log keeps working.
		if f, e := openFile(l.dfd, FileName, false); e == nil {
			l.f = f
		}
		return fmt.Errorf("eventlog: rotate: %w", err)
	}
	f, err := openFile(l.dfd, FileName, true)
	if err != nil {
		return fmt.Errorf("eventlog: rotate: %w", err)
	}
	l.f, l.size, l.torn = f, 0, false
	l.stats.Rotations++
	return nil
}

// Sync flushes the file to stable storage. Use it for the final record.
func (l *Log) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	if l.f == nil {
		return nil
	}
	return l.f.Sync()
}

// Stats returns a copy of the counters.
func (l *Log) Stats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stats
}

// Close syncs and closes the log. Later calls return ErrClosed.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	l.closed = true
	var err error
	if l.f != nil {
		err = l.f.Sync()
	}
	if e := l.closeFiles(); err == nil {
		err = e
	}
	return err
}

// bound returns f with every string capped, code-like strings restricted and
// the counters bounded. It counts each cap that is hit.
func (l *Log) bound(f Fields) Fields {
	f.Role = l.text(f.Role, MaxRole)
	f.Epoch = l.text(f.Epoch, MaxEpoch)
	f.ClientName = l.text(f.ClientName, MaxClientName)
	f.ClientCommand = l.text(f.ClientCommand, MaxCommandLine)
	f.Reason = l.code(f.Reason, MaxReason)
	f.State = l.code(f.State, MaxState)
	f.Digest = l.code(f.Digest, MaxDigest)
	f.MessageID = l.code(f.MessageID, MaxMessageID)
	f.Sender = l.code(f.Sender, MaxSender)
	f.Outcome = l.code(f.Outcome, MaxOutcome)
	f.CommandID = l.code(f.CommandID, MaxCommandID)
	if len(f.Guard) > 0 {
		n := min(len(f.Guard), MaxGuardCodes)
		g := make([]string, n)
		for i := range g {
			g[i] = l.code(f.Guard[i], MaxGuardCode)
		}
		l.stats.GuardDropped += uint64(len(f.Guard) - n)
		f.Guard = g
	}
	if f.ClientPID < 0 {
		f.ClientPID = 0
	}
	if f.Cols < 0 {
		f.Cols = 0
	}
	if f.Rows < 0 {
		f.Rows = 0
	}
	if len(f.Counters) > 0 {
		names := make([]string, 0, len(f.Counters))
		for k := range f.Counters {
			names = append(names, k)
		}
		sort.Strings(names)
		out := make(map[string]uint64, min(len(names), MaxCounters))
		for _, k := range names {
			if len(out) >= MaxCounters {
				l.stats.CountersDropped++
				continue
			}
			ck := l.code(k, MaxCounterName)
			if _, dup := out[ck]; dup {
				// Two names became one key. Summing could wrap and hide
				// both: keep the first in sorted order and count the other.
				l.stats.CountersDropped++
				continue
			}
			out[ck] = f.Counters[k]
		}
		f.Counters = out
	}
	// Copy the pointed-to values so the caller cannot change them later.
	if f.ExitStatus != nil {
		v := *f.ExitStatus
		f.ExitStatus = &v
	}
	f.StreamEnded = copyBool(f.StreamEnded)
	f.TUIExited = copyBool(f.TUIExited)
	f.TreeGone = copyBool(f.TreeGone)
	return f
}

func copyBool(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// text makes s valid UTF-8 and cuts it to max bytes on a character boundary.
func (l *Log) text(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
		l.stats.Truncated++
	}
	return s
}

// code restricts s to [A-Za-z0-9._:/+=-] (other bytes become '_') and cuts it
// to max bytes.
func (l *Log) code(s string, max int) string {
	if len(s) > max {
		s = s[:max]
		l.stats.Truncated++
	}
	changed := false
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == ':', c == '/', c == '+', c == '=', c == '-':
		default:
			b[i] = '_'
			changed = true
		}
	}
	if changed {
		l.stats.Sanitized++
	}
	return string(b)
}

// openDir opens the log folder and checks it: not a symlink, owned by the
// caller, mode exactly 0700.
func openDir(dir string) (int, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ELOOP || err == unix.ENOTDIR {
			return -1, fmt.Errorf("%w: folder is a symlink or not a folder", ErrUnsafe)
		}
		return -1, fmt.Errorf("eventlog: open folder: %w", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("eventlog: stat folder: %w", err)
	}
	if int(st.Uid) != os.Geteuid() {
		unix.Close(fd)
		return -1, fmt.Errorf("%w: folder belongs to another user", ErrUnsafe)
	}
	if m := st.Mode & 07777; m != 0o700 {
		unix.Close(fd)
		return -1, fmt.Errorf("%w: folder mode is %04o, want 0700", ErrUnsafe, m)
	}
	return fd, nil
}

// openFile opens name in the folder for appending, never through a symlink
// and never blocking on a special file. With create it makes the file with
// mode 0600 when it is absent. The result is a regular file of the caller
// with mode 0600.
func openFile(dfd int, name string, create bool) (*os.File, error) {
	const base = unix.O_RDWR | unix.O_APPEND | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	var fd int
	var err error
	created := false
	for try := 0; try < 3; try++ {
		if create {
			fd, err = unix.Openat(dfd, name, base|unix.O_CREAT|unix.O_EXCL, 0o600)
			if err == nil {
				created = true
				break
			}
			if err != unix.EEXIST {
				break
			}
		}
		fd, err = unix.Openat(dfd, name, base, 0)
		if err == nil || err != unix.ENOENT || !create {
			break
		}
	}
	if err != nil {
		if err == unix.ELOOP {
			return nil, fmt.Errorf("%w: %s is a symlink", ErrUnsafe, name)
		}
		return nil, fmt.Errorf("eventlog: open %s: %w", name, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("eventlog: stat %s: %w", name, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrUnsafe, name)
	}
	if int(st.Uid) != os.Geteuid() {
		unix.Close(fd)
		return nil, fmt.Errorf("%w: %s belongs to another user", ErrUnsafe, name)
	}
	if created && st.Mode&07777 != 0o600 { // an odd umask
		if err := unix.Fchmod(fd, 0o600); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("eventlog: chmod %s: %w", name, err)
		}
		st.Mode = st.Mode&^07777 | 0o600
	}
	if st.Mode&07777 != 0o600 {
		unix.Close(fd)
		return nil, fmt.Errorf("%w: %s has mode %04o, want 0600", ErrUnsafe, name, st.Mode&07777)
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("eventlog: %s: %w", name, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// MaxReadBytes is the most Read takes from one file.
const MaxReadBytes = MaxMaxBytes

// Read returns the events of events.jsonl.1 and then events.jsonl, in file
// order, for tests and the status command. The folder must pass the same
// checks as for Open and the files must be private regular files of the
// caller; a missing file is empty. Lines that are not events (a torn line, an
// over-long line) are skipped and reported in the error, which wraps
// ErrCorrupt; a file larger than MaxReadBytes is read up to that size and the
// error wraps ErrTooLarge. The events are valid whenever they are returned,
// even with an error.
func Read(dir string) ([]Event, error) {
	dfd, err := openDir(dir)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dfd)
	var events []Event
	var skipped int
	var tooLarge bool
	for _, name := range []string{RotatedName, FileName} {
		f, err := openFile(dfd, name, false)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return events, err
		}
		sk, big, err := readFile(f, &events)
		f.Close()
		skipped += sk
		tooLarge = tooLarge || big
		if err != nil {
			return events, err
		}
	}
	var errs []error
	if skipped > 0 {
		errs = append(errs, fmt.Errorf("%w: %d", ErrCorrupt, skipped))
	}
	if tooLarge {
		errs = append(errs, fmt.Errorf("%w: a file is over %d bytes", ErrTooLarge, MaxReadBytes))
	}
	return events, errors.Join(errs...)
}

func readFile(f *os.File, events *[]Event) (skipped int, tooLarge bool, err error) {
	var st os.FileInfo
	if st, err = f.Stat(); err != nil {
		return 0, false, err
	}
	tooLarge = st.Size() > MaxReadBytes
	r := bufio.NewReaderSize(io.LimitReader(f, MaxReadBytes), 2*MaxEventBytes)
	for {
		line, rerr := r.ReadSlice('\n')
		if rerr == bufio.ErrBufferFull {
			skipped++
			for rerr == bufio.ErrBufferFull {
				_, rerr = r.ReadSlice('\n')
			}
			if rerr != nil {
				break
			}
			continue
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		switch {
		case len(line) == 0:
			// blank (the newline that closes a torn fragment)
		case len(line) > MaxEventBytes:
			skipped++
		default:
			var ev Event
			if json.Unmarshal(line, &ev) != nil || ev.Seq == 0 || ev.Kind == "" || ev.Event == "" {
				skipped++
			} else {
				*events = append(*events, ev)
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				err = rerr
			}
			break
		}
	}
	return skipped, tooLarge, err
}
