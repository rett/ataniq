//go:build linux

// Package session is one hosted terminal session of the lead holder (the
// lead-hosting design, sections 1.2 to 1.7). It joins three packages: the PTY
// and the agent's process (ptyhost), the emulator that mirrors the screen (the
// console's sessionpane wrapper, so that holder and viewer cannot drift) and
// the holder's event log (eventlog). It knows nothing of sockets or clients:
// the attach service drives it through the methods below. Linux only.
//
// # Output
//
// One goroutine reads the PTY in chunks of at most 64 KiB. For each chunk,
// under the output lock, it feeds the pane, advances the position (the count
// of PTY bytes fed since start) and calls the subscriber. The subscriber
// contract is: return at once and never block, never call the session (the
// output lock is held). The attach service copies the chunk into its own
// bounded buffers. With no subscriber the session keeps reading, answering the
// agent's queries and updating the emulator (design 1.4). [Session.Screen] runs
// a function under the same lock, so that a snapshot and its position agree.
//
// # Input
//
// Every byte to the agent passes through one input lock (design 1.2), from
// three sources: the writer ([Session.Input], [Session.Paste]), the
// emulator's replies to the agent's terminal queries, and an injection. The
// injection is not built here: [Session.WithInputLock] is the hook a later
// package takes the lock with for a fence, and [Session.Inject] is a stub that
// refuses and logs. The replies are queued (bounded to [ReplyQueueBytes], an
// overflow is dropped and counted) and written by a goroutine of their own, so
// the emulator never waits for the agent to read. A write to the agent that
// fails or times out breaks the input for good ([Session.InputBroken]): a
// prefix may have been delivered, so nothing more is written, from any source,
// and later input is refused with [ErrInputBroken].
//
// Writer input is filtered: Ctrl+Z is never forwarded, and while the terminal
// has ISIG set the bytes for VINTR, VQUIT and VSUSP are held back too. The
// terminal's settings are read at each writer input, so the filter follows the
// agent as it switches between raw and cooked mode; a change between the read
// and the write is not seen. In raw mode (ISIG off, as real agents run) the
// byte 0x03 is input like any other.
//
// # Lifetime
//
// Three facts are kept apart (design 1.4), each yes, no or unknown: the
// terminal stream ended, the TUI (the direct child) exited, the agent's
// process tree is gone. When the TUI exits the session waits for the stream
// (bounded) and for the tree (bounded), writes the final record, logs "exit"
// and closes [Session.Done]. Nothing is ever restarted.
//
// # What is never recorded
//
// The event log is given typed fields only (eventlog has no field for input or
// output). A held key is logged by kind and count, never the bytes around it.
// The final record holds the last screen as text, which is output: it is a
// file of the lead's state folder, mode 0600, and is not the event log.
package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/ptyhost"
	"ataniq/internal/terminalconsole/sessionpane"
)

// The holder's own size limits (design 1.3). They are tighter than the ones of
// ptyhost and of the pane, which accept smaller terminals.
const (
	MinCols = 20
	MaxCols = 500
	MinRows = 5
	MaxRows = 200
)

// Bounds and defaults.
const (
	// ReplyQueueBytes is the most bytes of the emulator's replies that wait
	// for the input lock. A reply that does not fit is dropped whole and
	// counted.
	ReplyQueueBytes = 64 << 10
	// DefaultStreamWait is how long, after the TUI exited, the session waits
	// for the terminal stream to end.
	DefaultStreamWait = 2 * time.Second
	// DefaultTreeWait is how long the session waits for the process tree to
	// be gone after the stream wait.
	DefaultTreeWait = 5 * time.Second
	// DefaultStopGrace is the time between SIGTERM and SIGKILL in Stop.
	DefaultStopGrace = 2 * time.Second

	// MaxExtraCounters is the most names AddCount keeps.
	MaxExtraCounters = 32
	// MaxCounterName is the longest name AddCount keeps.
	MaxCounterName = 48
)

// Errors. Test with errors.Is.
var (
	// ErrRoleLocked: another session holds the role lock (design 1.6).
	ErrRoleLocked = errors.New("session: the role lock is held by another session")
	// ErrClosed: the session is closed.
	ErrClosed = errors.New("session: closed")
	// ErrSubscribed: there is a subscriber already (at most one).
	ErrSubscribed = errors.New("session: there is a subscriber already")
	// ErrInjectionDisabled: injection is not built in this version.
	ErrInjectionDisabled = errors.New("session: injection is disabled")
	// ErrInputBroken: an earlier write to the agent failed part-way (see
	// Session.InputBroken); nothing more is written.
	ErrInputBroken = errors.New("session: the agent's input is broken by an earlier failed write")
	// ErrReservedCounter: the name given to AddCount is one of the session's
	// own counters (see ReservedCounters).
	ErrReservedCounter = errors.New("session: the counter name is reserved for the session's own counters")
	// ErrNotOver: the session is not over, so there is no final record yet.
	ErrNotOver = errors.New("session: not over yet")
)

// Config describes one session.
type Config struct {
	// Role and Epoch identify the holder; they are written to the event log
	// and to the final record. They must come from the holder's own
	// configuration and never from the terminal.
	Role, Epoch string

	// Argv, Env and Dir are passed to ptyhost as they are: Argv[0] is a
	// path, Env is exactly the agent's environment, Dir its working
	// directory.
	Argv []string
	Env  []string
	Dir  string

	// Cols and Rows are the initial size, clamped to MinCols..MaxCols by
	// MinRows..MaxRows.
	Cols, Rows int

	// Colours are the colours the pane reports to the agent's OSC 10, 11 and
	// 12 queries; nil entries are neutral. See Session.SetColours.
	Colours sessionpane.Colours

	// RoleLock is the path of the role's lock file. Start takes an exclusive
	// non-blocking flock on it (creating it with mode 0600) before the PTY is
	// opened, and holds it until Close.
	RoleLock string
	// StateDir is an existing folder of the lead's state where final.json
	// is written.
	StateDir string
	// Log is an open event log. The session never closes it.
	Log *eventlog.Log

	// StreamWait, TreeWait and StopGrace bound the waits of the lifetime
	// path and of Stop; zero means the Default constants.
	StreamWait time.Duration
	TreeWait   time.Duration
	StopGrace  time.Duration

	// PTY holds the injectable settings of ptyhost (WriteTimeout, ProcRoot,
	// CgroupRoot, NoCgroup, ProcScanLimit, CgroupWalkLimit). Its Argv, Env,
	// Dir, Cols and Rows are ignored: the fields above are used.
	PTY ptyhost.Config
}

// Session is one hosted terminal session. Its methods are safe for
// concurrent use.
type Session struct {
	cfg  Config
	host *ptyhost.Host
	pane *sessionpane.Pane
	log  *eventlog.Log

	started time.Time

	lockFile *os.File // the role lock; closing it releases the flock

	// omu is the output lock: it guards pos, sub and the order of
	// pane.Write, the subscriber's calls, Resize and Screen.
	omu     sync.Mutex
	pos     uint64
	sub     func(pos uint64, chunk []byte)
	subID   uint64
	nextSub uint64

	// imu is the input lock (design 1.2).
	imu       sync.Mutex
	lastInput atomic.Int64 // unix nanoseconds of the last writer input

	replies replyQueue

	readDone chan struct{} // closed when the output loop returned
	stream   atomic.Int32  // streamRunning, streamEnded or streamUnknown

	// The lifetime path.
	fmu      sync.Mutex // guards facts, final and finalErr
	facts    Facts
	final    FinalRecord
	finalErr error
	concl    bool          // facts are concluded (frozen)
	done     chan struct{} // closed when the lifetime path is over
	closing  chan struct{} // closed when Close starts
	stopOnce sync.Once

	// inputBroken is set under imu by the first failed write to the agent
	// and never cleared.
	inputBroken atomic.Bool

	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error

	cnt counters
}

// counters are the session's own counted events and caps.
type counters struct {
	replyDroppedChunks atomic.Uint64
	replyDroppedBytes  atomic.Uint64
	replyWriteErrors   atomic.Uint64
	heldSuspend        atomic.Uint64
	heldSignal         atomic.Uint64
	inputWriteErrors   atomic.Uint64
	inputRefused       atomic.Uint64
	termiosErrors      atomic.Uint64
	readErrors         atomic.Uint64
	logErrors          atomic.Uint64

	mu    sync.Mutex
	extra map[string]uint64
}

// Start takes the role lock, creates the pane, starts the agent and logs
// "start". On an error nothing is left open. A second session for the same
// RoleLock fails with an error wrapping ErrRoleLocked.
func Start(cfg Config) (*Session, error) {
	if cfg.Log == nil {
		return nil, errors.New("session: Config.Log is required")
	}
	if cfg.RoleLock == "" {
		return nil, errors.New("session: Config.RoleLock is required")
	}
	if st, err := os.Stat(cfg.StateDir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("session: Config.StateDir %q is not a folder", cfg.StateDir)
	}
	if cfg.StreamWait <= 0 {
		cfg.StreamWait = DefaultStreamWait
	}
	if cfg.TreeWait <= 0 {
		cfg.TreeWait = DefaultTreeWait
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = DefaultStopGrace
	}
	cols, rows := clampSize(cfg.Cols, cfg.Rows)
	cfg.Cols, cfg.Rows = cols, rows

	lock, err := takeRoleLock(cfg.RoleLock)
	if err != nil {
		return nil, err
	}
	s := &Session{
		cfg:      cfg,
		log:      cfg.Log,
		lockFile: lock,
		started:  time.Now().UTC(),
		readDone: make(chan struct{}),
		done:     make(chan struct{}),
		closing:  make(chan struct{}),
	}
	s.replies.init()
	s.replies.owner = s
	s.pane = sessionpane.New(cols, rows, sessionpane.Options{
		Replies: s.replies.push,
		Colours: cfg.Colours,
	})

	pc := cfg.PTY
	pc.Argv, pc.Env, pc.Dir, pc.Cols, pc.Rows = cfg.Argv, cfg.Env, cfg.Dir, cols, rows
	host, err := ptyhost.Start(pc)
	if err != nil {
		s.pane.Close()
		lock.Close()
		return nil, fmt.Errorf("session: %w", err)
	}
	s.host = host

	s.event(eventlog.EventStart, eventlog.Fields{Role: cfg.Role, Epoch: cfg.Epoch, Cols: cols, Rows: rows})
	go s.readLoop()
	go s.replies.run()
	go s.lifecycle()
	return s, nil
}

func clampSize(cols, rows int) (int, int) {
	return min(max(cols, MinCols), MaxCols), min(max(rows, MinRows), MaxRows)
}

// takeRoleLock opens the lock file (created 0600; never a symlink) and takes
// an exclusive non-blocking flock on it. The descriptor is close-on-exec, so
// the agent never inherits the lock.
func takeRoleLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: open the role lock %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != os.Geteuid() {
		f.Close()
		return nil, fmt.Errorf("session: the role lock %s is not a regular file of this user", path)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s): the role already has a running session", ErrRoleLocked, path)
		}
		return nil, fmt.Errorf("session: lock the role lock %s: %w", path, err)
	}
	return f, nil
}

// event appends a holder event; a failure is counted, never fatal.
func (s *Session) event(name eventlog.EventName, f eventlog.Fields) {
	if err := s.log.Append(eventlog.KindHolder, name, f); err != nil {
		s.cnt.logErrors.Add(1)
	}
}

// Stream states.
const (
	streamRunning int32 = iota
	streamEnded
	streamUnknown
)

// readLoop is the one goroutine that reads the PTY.
func (s *Session) readLoop() {
	defer close(s.readDone)
	buf := make([]byte, ptyhost.ReadBufferSize)
	for {
		n, err := s.host.Read(buf)
		if n > 0 {
			s.deliver(buf[:n])
		}
		if err != nil {
			switch {
			case err == io.EOF:
				s.stream.Store(streamEnded)
			default:
				// A closed master (Close) or another read error: the
				// stream did not end in the way the kernel reports.
				if !errors.Is(err, os.ErrClosed) {
					s.cnt.readErrors.Add(1)
				}
				s.stream.Store(streamUnknown)
			}
			return
		}
	}
}

// deliver feeds one chunk under the output lock: the pane, then the
// subscriber, then the position.
func (s *Session) deliver(chunk []byte) {
	s.omu.Lock()
	defer s.omu.Unlock()
	s.pane.Write(chunk)
	if s.sub != nil {
		s.sub(s.pos, chunk)
	}
	s.pos += uint64(len(chunk))
}

// Screen runs fn under the output lock with the pane and the position (the
// count of PTY bytes fed to it), so that a snapshot and its position agree and
// no chunk is delivered between them. fn must return promptly and must not
// call the session.
func (s *Session) Screen(fn func(p *sessionpane.Pane, pos uint64)) {
	s.omu.Lock()
	defer s.omu.Unlock()
	fn(s.pane, s.pos)
}

// Subscribe sets the subscriber: fn is called for each chunk, under the output
// lock, with the position before the chunk. The chunk is only valid during the
// call. fn must return at once, never block and never call the session. At
// most one subscriber: a second Subscribe fails with ErrSubscribed until the
// first is unsubscribed. The returned function unsubscribes (it may be called
// more than once, and never from fn); when it has returned fn is not running
// and will not be called again.
func (s *Session) Subscribe(fn func(pos uint64, chunk []byte)) (unsubscribe func(), err error) {
	return s.SubscribeScreen(nil, fn)
}

// SubscribeScreen runs snap like Screen and, in the same hold of the output
// lock, sets the subscriber, so that the first chunk fn sees starts at the
// position snap saw: the attach service takes a snapshot and follows the stream
// without a gap. snap may be nil. If the subscriber cannot be set, snap does
// not run.
func (s *Session) SubscribeScreen(snap func(p *sessionpane.Pane, pos uint64), fn func(pos uint64, chunk []byte)) (unsubscribe func(), err error) {
	if fn == nil {
		return nil, errors.New("session: nil subscriber")
	}
	s.omu.Lock()
	defer s.omu.Unlock()
	if s.sub != nil {
		return nil, ErrSubscribed
	}
	if snap != nil {
		snap(s.pane, s.pos)
	}
	s.nextSub++
	id := s.nextSub
	s.sub, s.subID = fn, id
	return func() {
		s.omu.Lock()
		defer s.omu.Unlock()
		if s.subID == id {
			s.sub = nil
		}
	}, nil
}

// SetColours changes the colours the pane reports to OSC 10, 11 and 12
// queries (the writer's colours, design 1.2). The answer goes to the agent
// only. OSC 52 is dropped by the pane and counted.
func (s *Session) SetColours(c sessionpane.Colours) { s.pane.SetColours(c) }

// Resize clamps the size to the holder's limits and resizes the PTY and the
// pane together under the output lock, so that a snapshot never sees them
// disagree. It returns the applied size, which on an error is the size before.
// A resize that changes the size is logged. After the output stream ended the
// pane keeps its last screen and takes no resize, so the call changes nothing
// (neither the PTY nor the pane, no event) and returns the current size.
func (s *Session) Resize(cols, rows int) (appliedCols, appliedRows int, err error) {
	cols, rows = clampSize(cols, rows)
	s.omu.Lock()
	curC, curR := s.pane.Size()
	if ended, _, _ := s.pane.Ended(); ended {
		s.omu.Unlock()
		return curC, curR, nil
	}
	if cols == curC && rows == curR {
		s.omu.Unlock()
		return cols, rows, nil
	}
	if err := s.host.Resize(cols, rows); err != nil {
		s.omu.Unlock()
		return curC, curR, fmt.Errorf("session: %w", err)
	}
	s.pane.Resize(cols, rows)
	s.omu.Unlock()
	s.event(eventlog.EventResize, eventlog.Fields{Cols: cols, Rows: rows})
	return cols, rows, nil
}

// Size returns the pane's size.
func (s *Session) Size() (cols, rows int) { return s.pane.Size() }

// Pid returns the direct child's process id, for display and records.
func (s *Session) Pid() int { return s.host.Pid() }

// Done is closed when the session is over: the TUI exited, the waits are done
// and the final record is written (or its writing failed).
func (s *Session) Done() <-chan struct{} { return s.done }

// Stop asks ptyhost to stop the agent: SIGTERM, Config.StopGrace, then
// SIGKILL. It returns when the tree is gone or ptyhost gave up; the lifetime
// path then finishes on its own (wait for Done). It may be called more than
// once.
func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		s.event(eventlog.EventState, eventlog.Fields{State: "stopping"})
	})
	s.host.Stop(s.cfg.StopGrace)
}

// Close stops the agent if it still runs, waits for the lifetime path (so the
// final record exists), and releases everything: the pane, the PTY, the
// goroutines and the role lock. It may be called more than once. It does not
// close the event log.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.Stop()
		close(s.closing)
		<-s.done
		s.closed.Store(true)
		s.closeErr = s.host.Close()
		<-s.readDone
		s.pane.Close()
		s.replies.stop()
		if err := s.lockFile.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

// reservedCounters are the names of the session's own counters, the ones
// Counters reports without AddCount plus the two the session keeps in the same
// map: "extra_counters_dropped" and "final_screen_truncated".
var reservedCounters = map[string]bool{
	"pty_scan_truncated":     true,
	"pty_scan_errors":        true,
	"pty_cgroup_truncated":   true,
	"pty_cgroup_errors":      true,
	"pty_pidfd_failures":     true,
	"reply_dropped_chunks":   true,
	"reply_dropped_bytes":    true,
	"reply_write_errors":     true,
	"held_keys_suspend":      true,
	"held_keys_signal":       true,
	"input_write_errors":     true,
	"input_refused":          true,
	"input_broken":           true,
	"termios_errors":         true,
	"read_errors":            true,
	"log_errors":             true,
	"clipboard_blocked":      true,
	"output_bytes":           true,
	"extra_counters_dropped": true,
	"final_screen_truncated": true,
}

// ReservedCounters returns, sorted, the counter names that AddCount refuses:
// the session's own counters, which are evidence and must not be changed by
// the service.
func ReservedCounters() []string {
	out := make([]string, 0, len(reservedCounters))
	for n := range reservedCounters {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// AddCount adds delta to a named counter that is written to the final record
// and to the exit event, for the service to count its own caps. A name that is
// one of the session's own counters (ReservedCounters) is refused with
// ErrReservedCounter and nothing is counted. At most MaxExtraCounters names of
// at most MaxCounterName bytes are kept; a name over either, or an empty one,
// is not kept and is counted in "extra_counters_dropped" instead (the error is
// nil).
func (s *Session) AddCount(name string, delta uint64) error {
	if reservedCounters[name] {
		return fmt.Errorf("%w: %q", ErrReservedCounter, name)
	}
	s.addCount(name, delta)
	return nil
}

// addCount is AddCount without the reserved-name check, for the session's own
// counters kept in the same map.
func (s *Session) addCount(name string, delta uint64) {
	c := &s.cnt
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.extra == nil {
		c.extra = map[string]uint64{}
	}
	if _, ok := c.extra[name]; !ok && (len(c.extra) >= MaxExtraCounters || len(name) > MaxCounterName || name == "") {
		c.extra["extra_counters_dropped"]++
		return
	}
	c.extra[name] += delta
}

// Counters returns every counter: ptyhost's, the session's own and the
// service's (AddCount), by name.
func (s *Session) Counters() map[string]uint64 {
	out := map[string]uint64{}
	pc := s.host.Counters()
	out["pty_scan_truncated"] = pc.ScanTruncated
	out["pty_scan_errors"] = pc.ScanErrors
	out["pty_cgroup_truncated"] = pc.CgroupTruncated
	out["pty_cgroup_errors"] = pc.CgroupErrors
	out["pty_pidfd_failures"] = pc.PidfdFailures
	c := &s.cnt
	out["reply_dropped_chunks"] = c.replyDroppedChunks.Load()
	out["reply_dropped_bytes"] = c.replyDroppedBytes.Load()
	out["reply_write_errors"] = c.replyWriteErrors.Load()
	out["held_keys_suspend"] = c.heldSuspend.Load()
	out["held_keys_signal"] = c.heldSignal.Load()
	out["input_write_errors"] = c.inputWriteErrors.Load()
	out["input_refused"] = c.inputRefused.Load()
	out["input_broken"] = 0
	if s.inputBroken.Load() {
		out["input_broken"] = 1
	}
	out["termios_errors"] = c.termiosErrors.Load()
	out["read_errors"] = c.readErrors.Load()
	out["log_errors"] = c.logErrors.Load()
	out["clipboard_blocked"] = uint64(s.pane.ClipboardBlocked())
	s.omu.Lock()
	out["output_bytes"] = s.pos
	s.omu.Unlock()
	c.mu.Lock()
	for k, v := range c.extra {
		out[k] += v
	}
	c.mu.Unlock()
	return out
}
