//go:build linux && ataniq_canary

package screenrec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"ataniq/internal/inject/screen"
)

// The names of the recording.
const (
	// DirName is the name of the folder in the lead's runtime folder that the
	// daemon gives the holder as --record-dir.
	DirName    = "record"
	ReadmeName = "README.txt"
	TitlesName = "titles.jsonl"
	OSCName    = "osc7501.jsonl"
	LimitName  = "LIMIT.txt"
)

// The bounds of a recording.
const (
	// MaxPairs is the most screen pairs one recording keeps. Reaching it
	// stops the pairs only: the titles and the payloads go on.
	MaxPairs = 400
	// MaxBytes is the most bytes one recording writes, README.txt included
	// and LIMIT.txt not.
	MaxBytes = 8 << 20
	// MaxPayload is the most bytes of one title or one OSC 7501 payload that
	// are kept.
	MaxPayload = 4 << 10
	// PairInterval is the least time between two snapshots of the pane, so
	// between two pairs.
	PairInterval = 500 * time.Millisecond
	// Settle is how long the pane must have had no output and no resize
	// before a snapshot is taken, so that a screen that keeps changing (an
	// animated spinner) does not fill the pairs. The last look, when the
	// session is over, does not wait for it.
	Settle = 300 * time.Millisecond
	// TornAfter is how long a due snapshot waits for the parser to reach
	// ground and for a mode 2026 frame to close. Past it the snapshot is
	// taken anyway and its pair is marked "torn": true.
	TornAfter = 2 * time.Second
	// PollInterval is how often Watch looks at the pane's position, size and
	// title.
	PollInterval = 100 * time.Millisecond
	// QueueLen is the most items waiting for the writer; a new one drops the
	// oldest.
	QueueLen = 32
)

// Readme is the text of README.txt.
const Readme = `Screen recording of one start of a scratch lead, written by ataniq-holder
(--record-dir, the lead's "record_screens": true).

raw, scrub before use; do not commit.

These files are what the agent drew and sent, unchanged. They may hold a login
URL, a login code, a session id, a home path, a host name or anything else the
agent showed. Scrub them by hand before any of it goes into a fixture, and
delete this folder once the fixtures are made.

NNNN.txt, NNNN.json  one distinct screen each, once it stood still for about
                     300 ms (and the last one), in the fixture format of
                     internal/inject/screen/testdata ("source": "recorded",
                     no description and no "want" yet); "torn": true marks
                     a screen taken mid-frame, not a fixture as it is
titles.jsonl         each window title as it changed, with "ms"
osc7501.jsonl        each OSC 7501 payload, with "ms"
LIMIT.txt            why the screens or the recording stopped early, and
                     "dropped N" when the queue dropped items

"ms" is milliseconds since the holder started recording. A title or payload
is cut at 4096 bytes ("cut": true) and stored as a JSON string.
`

// Config holds the settings of a Recorder. The zero value is the real thing.
type Config struct {
	// Now is the clock of "ms" and of the snapshot interval; nil is time.Now.
	Now func() time.Time
	// Logf takes the one line the recorder logs when it stops early; nil
	// discards it. The line names no path.
	Logf func(format string, args ...any)

	// MaxPairs, MaxBytes, QueueLen, PairInterval, PollInterval and Settle
	// replace the constants of the same names when above zero. They are for
	// tests.
	MaxPairs     int
	MaxBytes     int64
	QueueLen     int
	PairInterval time.Duration
	PollInterval time.Duration
	Settle       time.Duration
}

// kind is the kind of a queued item.
type kind int

const (
	kindPair kind = iota
	kindTitle
	kindOSC
)

// item is one queued record.
type item struct {
	kind kind
	ms   int64
	snap screen.Snapshot
	torn bool // the pair's snapshot was taken mid-sequence or in a frame
	// text is the title, data the payload, both already cut; size is their
	// length as seen.
	text string
	data []byte
	size int
}

// Recorder records the screens, titles and OSC 7501 payloads of one session.
// Its methods are safe for concurrent use.
type Recorder struct {
	cfg   Config
	begun time.Time

	mu        sync.Mutex
	queue     []item
	drops     uint64
	opened    bool // Open succeeded: the writer runs
	closing   bool // Close was called
	stopped   bool // the recording stopped early, or Open failed
	pairsDone bool // MaxPairs pairs were written: no more pairs are taken
	last      *screen.Snapshot
	lastTitle string
	wake      chan struct{}
	done      chan struct{} // closed when the writer returns

	// The writer's own state, touched by Open before the writer starts and
	// by the writer after.
	dir, titles, osc, limit int
	pairs                   int
	written                 int64
	reported                uint64
	pairsEnded              bool // the writer said that the pairs stopped
}

// New returns a recorder that queues and writes nothing until Open. Its "ms"
// counts from now.
func New(cfg Config) *Recorder {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.MaxPairs <= 0 {
		cfg.MaxPairs = MaxPairs
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = MaxBytes
	}
	if cfg.QueueLen <= 0 {
		cfg.QueueLen = QueueLen
	}
	if cfg.PairInterval <= 0 {
		cfg.PairInterval = PairInterval
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = PollInterval
	}
	if cfg.Settle <= 0 {
		cfg.Settle = Settle
	}
	return &Recorder{
		cfg: cfg, begun: cfg.Now(),
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		dir: -1, titles: -1, osc: -1, limit: -1,
	}
}

// Error is a failure of the recorder: the step and the system's errno, and
// never a path.
type Error struct {
	Step string
	Err  error
}

func (e *Error) Error() string { return e.Step + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// sysErr keeps only the errno of err, so that no path reaches a log line.
func sysErr(step string, err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return &Error{Step: step, Err: errno}
	}
	return &Error{Step: step, Err: errors.New("failed")}
}

// limitError is a bound that was reached.
type limitError struct{ reason string }

func (e *limitError) Error() string { return e.reason }

// elapsed is "ms": the milliseconds since New.
func (r *Recorder) elapsed() int64 {
	ms := r.cfg.Now().Sub(r.begun).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

// pushLocked queues it, with r.mu held, dropping the oldest item when the
// queue is full, and wakes the writer.
func (r *Recorder) pushLocked(it item) {
	if len(r.queue) >= r.cfg.QueueLen {
		n := copy(r.queue, r.queue[1:])
		r.queue[n] = item{}
		r.queue = r.queue[:n]
		r.drops++
	}
	r.queue = append(r.queue, it)
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// accepting says new items are taken, with r.mu held.
func (r *Recorder) accepting() bool { return !r.closing && !r.stopped }

// takesPairs says a new pair would be taken: the recorder takes items and
// MaxPairs was not reached.
func (r *Recorder) takesPairs() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accepting() && !r.pairsDone
}

// Screen queues a pair for s when it differs from the last screen queued and
// MaxPairs was not reached.
func (r *Recorder) Screen(s screen.Snapshot) { r.screen(s, false) }

// screen is Screen for a snapshot that may be torn: taken while the parser was
// in the middle of a sequence or a mode 2026 frame was open. Its pair says so.
func (r *Recorder) screen(s screen.Snapshot, torn bool) {
	ms := r.elapsed()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepting() || r.pairsDone || (r.last != nil && sameScreen(*r.last, s)) {
		return
	}
	c := s
	c.Faint = append([]screen.Span(nil), s.Faint...)
	r.last = &c
	r.pushLocked(item{kind: kindPair, ms: ms, snap: c, torn: torn})
}

func sameScreen(a, b screen.Snapshot) bool {
	if a.Text != b.Text || a.Cols != b.Cols || a.Rows != b.Rows || a.CursorX != b.CursorX ||
		a.CursorY != b.CursorY || a.CursorHidden != b.CursorHidden || a.AltScreen != b.AltScreen ||
		a.Title != b.Title || len(a.Faint) != len(b.Faint) {
		return false
	}
	for i := range a.Faint {
		if a.Faint[i] != b.Faint[i] {
			return false
		}
	}
	return true
}

// Title queues a line for t when it differs from the last title queued (the
// first is compared with the empty title).
func (r *Recorder) Title(t string) {
	ms := r.elapsed()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepting() || t == r.lastTitle {
		return
	}
	r.lastTitle = t
	r.pushLocked(item{kind: kindTitle, ms: ms, text: cutText(t), size: len(t)})
}

// OSC7501 queues a line for one payload of the status protocol, the OSC data
// after "7501;". It copies at most MaxPayload bytes and returns at once: the
// session calls it where it applies output.
func (r *Recorder) OSC7501(data []byte) {
	ms := r.elapsed()
	keep := append([]byte(nil), data[:min(len(data), MaxPayload)]...)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.accepting() {
		return
	}
	r.pushLocked(item{kind: kindOSC, ms: ms, data: keep, size: len(data)})
}

// cutText cuts s to at most MaxPayload bytes at a character boundary.
func cutText(s string) string {
	if len(s) <= MaxPayload {
		return s
	}
	i := MaxPayload
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// Open makes the folder dir (its parent must exist; dir must not), mode 0700,
// writes README.txt and the two line files, and starts the writer. dir is an
// absolute clean path; the parent is opened without following a link at its
// last element, and the folder and the files are made by handle, exclusively
// and without following a link. On an error the recorder records nothing more,
// and what Open made is left as it is.
func (r *Recorder) Open(dir string) error {
	r.mu.Lock()
	if r.opened || !r.accepting() {
		r.mu.Unlock()
		return &Error{Step: "open", Err: errors.New("the recorder was opened, closed or stopped before")}
	}
	r.mu.Unlock()
	if err := r.open(dir); err != nil {
		r.closeFiles()
		r.mu.Lock()
		r.stopped, r.queue = true, nil
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	r.opened = true
	r.mu.Unlock()
	go r.run()
	return nil
}

func (r *Recorder) open(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == "/" {
		return &Error{Step: "open", Err: errors.New("the folder is not an absolute clean path")}
	}
	parent, name := filepath.Dir(dir), filepath.Base(dir)
	pfd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return sysErr("open the parent folder", err)
	}
	defer unix.Close(pfd)
	if err := unix.Mkdirat(pfd, name, 0o700); err != nil {
		return sysErr("make the folder", err)
	}
	dfd, err := unix.Openat(pfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return sysErr("open the folder", err)
	}
	r.dir = dfd
	var st unix.Stat_t
	if err := unix.Fstat(dfd, &st); err != nil {
		return sysErr("look at the folder", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != unix.Geteuid() {
		return &Error{Step: "look at the folder", Err: errors.New("it is not a folder of this user")}
	}
	if err := unix.Fchmod(dfd, 0o700); err != nil {
		return sysErr("set the mode of the folder", err)
	}
	if err := r.writeFile(ReadmeName, []byte(Readme)); err != nil {
		return err
	}
	r.written = int64(len(Readme))
	if r.titles, err = r.create(TitlesName); err != nil {
		return err
	}
	if r.osc, err = r.create(OSCName); err != nil {
		return err
	}
	return nil
}

// create makes the file name in the folder, exclusively, mode 0600, for
// appending.
func (r *Recorder) create(name string) (int, error) {
	fd, err := unix.Openat(r.dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_APPEND, 0o600)
	if err != nil {
		return -1, sysErr("create "+name, err)
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		unix.Close(fd)
		return -1, sysErr("set the mode of "+name, err)
	}
	return fd, nil
}

// writeFile makes the file name with data.
func (r *Recorder) writeFile(name string, data []byte) error {
	fd, err := r.create(name)
	if err != nil {
		return err
	}
	err = writeAll(fd, data)
	if cerr := unix.Close(fd); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return sysErr("write "+name, err)
	}
	return nil
}

// writeAll writes all of b to fd.
func writeAll(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := unix.Write(fd, b)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func (r *Recorder) closeFiles() {
	for _, fd := range []*int{&r.titles, &r.osc, &r.limit, &r.dir} {
		if *fd >= 0 {
			unix.Close(*fd)
			*fd = -1
		}
	}
}

// run is the writer: it writes what is queued, in order, until Close and an
// empty queue, or until the recording stops early.
func (r *Recorder) run() {
	defer close(r.done)
	defer r.closeFiles()
	for {
		r.mu.Lock()
		items, drops, closing := r.queue, r.drops, r.closing
		r.queue = nil
		r.mu.Unlock()
		for _, it := range items {
			if err := r.write(it); err != nil {
				r.halt(err)
				return
			}
		}
		if drops > r.reported {
			if err := r.appendLimit("dropped " + strconv.FormatUint(drops, 10) + "\n"); err != nil {
				r.halt(err)
				return
			}
			r.reported = drops
		}
		if len(items) == 0 {
			if closing {
				return
			}
			<-r.wake
		}
	}
}

// halt stops the recording for good: a bound is written to LIMIT.txt, and the
// one log line says why.
func (r *Recorder) halt(err error) {
	r.mu.Lock()
	r.stopped, r.queue = true, nil
	drops := r.drops
	r.mu.Unlock()
	var lim *limitError
	if !errors.As(err, &lim) {
		r.cfg.Logf("recording screens stopped: %v", err)
		return
	}
	line := "recording stopped: " + lim.reason + "\n"
	if drops > r.reported {
		line += "dropped " + strconv.FormatUint(drops, 10) + "\n"
	}
	if werr := r.appendLimit(line); werr != nil {
		r.cfg.Logf("recording screens stopped: %s; and %v", lim.reason, werr)
		return
	}
	r.cfg.Logf("recording screens stopped: %s", lim.reason)
}

// appendLimit appends to LIMIT.txt, which it makes the first time.
func (r *Recorder) appendLimit(s string) error {
	if r.limit < 0 {
		fd, err := r.create(LimitName)
		if err != nil {
			return err
		}
		r.limit = fd
	}
	if err := writeAll(r.limit, []byte(s)); err != nil {
		return sysErr("write "+LimitName, err)
	}
	return nil
}

// endPairs stops the pairs for good, once: the pair that would pass MaxPairs
// and every later one is not written, one line goes to LIMIT.txt and one to
// the log, and the titles and the payloads go on.
func (r *Recorder) endPairs() error {
	if r.pairsEnded {
		return nil
	}
	r.pairsEnded = true
	r.mu.Lock()
	r.pairsDone = true
	r.mu.Unlock()
	why := fmt.Sprintf("%d screens were written, the most a recording keeps", r.pairs)
	if err := r.appendLimit("screens stopped: " + why + "; titles and payloads go on\n"); err != nil {
		return err
	}
	r.cfg.Logf("recording screens: no more screens (%s); titles and payloads go on", why)
	return nil
}

// fits checks the byte bound for n more bytes.
func (r *Recorder) fits(n int) error {
	if r.written+int64(n) > r.cfg.MaxBytes {
		return &limitError{fmt.Sprintf("the next record would pass %d bytes in all", r.cfg.MaxBytes)}
	}
	return nil
}

// write writes one item.
func (r *Recorder) write(it item) error {
	switch it.kind {
	case kindPair:
		if r.pairs >= r.cfg.MaxPairs {
			return r.endPairs()
		}
		txt := []byte(it.snap.Text + "\n")
		js, err := pairJSON(it.snap, it.torn)
		if err != nil {
			return &Error{Step: "encode a screen", Err: err}
		}
		if err := r.fits(len(txt) + len(js)); err != nil {
			return err
		}
		name := fmt.Sprintf("%04d", r.pairs+1)
		if err := r.writeFile(name+".txt", txt); err != nil {
			return err
		}
		if err := r.writeFile(name+".json", js); err != nil {
			return err
		}
		r.pairs++
		r.written += int64(len(txt) + len(js))
		return nil
	case kindTitle:
		return r.appendLine(r.titles, TitlesName, titleLine{MS: it.ms, Bytes: it.size, Cut: len(it.text) < it.size, Title: it.text})
	default:
		return r.appendLine(r.osc, OSCName, oscLine{
			MS: it.ms, Bytes: it.size, Cut: len(it.data) < it.size, UTF8: utf8.Valid(it.data), Payload: string(it.data),
		})
	}
}

// titleLine is one line of titles.jsonl.
type titleLine struct {
	MS    int64  `json:"ms"`
	Bytes int    `json:"bytes"`
	Cut   bool   `json:"cut"`
	Title string `json:"title"`
}

// oscLine is one line of osc7501.jsonl.
type oscLine struct {
	MS      int64  `json:"ms"`
	Bytes   int    `json:"bytes"`
	Cut     bool   `json:"cut"`
	UTF8    bool   `json:"utf8"`
	Payload string `json:"payload"`
}

// appendLine writes v as one JSON line to the file fd.
func (r *Recorder) appendLine(fd int, name string, v any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return &Error{Step: "encode a line of " + name, Err: err}
	}
	if err := r.fits(b.Len()); err != nil {
		return err
	}
	if err := writeAll(fd, b.Bytes()); err != nil {
		return sysErr("write "+name, err)
	}
	r.written += int64(b.Len())
	return nil
}

// pairFile is NNNN.json: the fixture format of internal/inject/screen/testdata
// without "want", and "torn": true for a snapshot taken mid-sequence or in a
// mode 2026 frame (absent otherwise; it is not a field of a fixture).
type pairFile struct {
	Source       string     `json:"source"`
	Description  string     `json:"description"`
	Cols         int        `json:"cols"`
	Rows         int        `json:"rows"`
	CursorX      int        `json:"cursor_x"`
	CursorY      int        `json:"cursor_y"`
	CursorHidden bool       `json:"cursor_hidden"`
	AltScreen    bool       `json:"alt_screen"`
	Title        string     `json:"title"`
	Faint        []spanFile `json:"faint"`
	Torn         bool       `json:"torn,omitempty"`
}

type spanFile struct {
	Row  int `json:"row"`
	From int `json:"from"`
	To   int `json:"to"`
}

// SourceRecorded is the "source" of every pair.
const SourceRecorded = "recorded"

func pairJSON(s screen.Snapshot, torn bool) ([]byte, error) {
	f := pairFile{
		Source: SourceRecorded, Cols: s.Cols, Rows: s.Rows, CursorX: s.CursorX, CursorY: s.CursorY,
		CursorHidden: s.CursorHidden, AltScreen: s.AltScreen, Title: s.Title, Torn: torn,
		Faint: make([]spanFile, 0, len(s.Faint)),
	}
	for _, sp := range s.Faint {
		f.Faint = append(f.Faint, spanFile{Row: sp.Row, From: sp.From, To: sp.To})
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Close stops taking items, lets the writer write what is queued, and waits
// for it. It may be called more than once, and before Open: then nothing is
// written.
func (r *Recorder) Close() {
	r.mu.Lock()
	r.closing = true
	opened := r.opened
	if !opened {
		r.queue = nil
	}
	r.mu.Unlock()
	if !opened {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
	<-r.done
}

// Stopped says the recording stopped early: a bound, a failed write or a
// failed Open.
func (r *Recorder) Stopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}
