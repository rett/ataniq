//go:build linux

// Package serve is the holder's attach and control service: the two unix
// sockets of one lead holder and everything between the hosted session and
// its clients (the lead-hosting design, section 2). It drives one
// [session.Session] and speaks the [wire] protocol. Linux only.
//
// # Sockets
//
// attach.sock is for consoles, control.sock for the daemon, both in a runtime
// folder that must be a real directory of this user with mode 0700. Each is
// created with mode 0600 before it listens, and a stale socket of an earlier
// holder is removed (the caller holds the role lock). Every accepted
// connection must come from this user (SO_PEERCRED), else it is closed and the
// refusal is logged and counted.
//
// The control socket also checks the peer process: the pidfd that SO_PEERPIDFD
// gives names the peer, and its pid must be the pid written in the daemon
// lock file while that file is locked (a non-blocking exclusive flock on a
// fresh open of it fails with "would block"; the probe is made only when the
// peer's pid is the file's, so no other process can make the holder take the
// lock for a moment). That is the stated limit of the
// design: any process of the same user that writes its own pid into that file
// and holds a lock on it passes, and so does one that replaces the daemon.
// The check separates the daemon from a process that merely runs as the same
// user and has not set that up; it does not separate it from one that has.
//
// # Attach connections
//
// A connection's first frame is a Hello with a ticket that the daemon
// registered through the control socket. A ticket is single use: it is
// consumed by the first Hello that presents it, valid or not completed, and it
// expires. There is at most one writer and one viewer, and at most four
// connections in handshake. After the Welcome the client gets a snapshot of
// the emulator at a snapshot point, then Output frames from that position on.
//
// A snapshot point is the end of a PTY chunk (or the gap between two) where
// the emulator's parser is at ground. With mode 2026 set the service waits
// up to a hold time after the parser first became quiescent, then takes the
// real state as it is. After 1 s without a point the client gets a
// snapshot-waiting notice, once, and the wait goes on.
//
// # Fan-out
//
// The session calls one subscriber for each chunk, under its output lock. The
// subscriber copies the chunk into a bounded queue per client and returns;
// each client has one sender goroutine that writes the queue to its socket.
// The PTY loop never waits for a client. A queue holds at most [QueueBytes] of
// payload, plus one snapshot that does not count. When it would overflow, the
// queued output is dropped, the client gets a resync notice and one fresh
// snapshot; an overflow while a snapshot is unsent, or a second one within the
// window, disconnects the client with a too-slow notice. A client whose socket
// takes no bytes for the stall time is disconnected. Output that arrives while
// a resync snapshot is waiting for its point is not queued: the snapshot
// covers it.
//
// # Writer input and the order of locks
//
// Only the writer reaches the PTY. A writer frame (Input, Paste, Resize) is
// handled with the input mutex held, from the check that the client is the
// writer to the end of the session call, and every change of writer
// (release-writer) and the writer's colours and size at its handshake are
// done with that mutex held as well. So when release-writer has answered ok no
// byte of the released client has reached the PTY or will, and a client that
// was released during its handshake sets neither the colours nor the size. A
// release waits for a write in flight: one PTY write deadline, plus any
// section that holds the session's input lock (WithInputLock) at that time,
// because the writer's frame holds the input mutex while it waits for that lock.
//
// A write to the agent that failed part-way breaks the session's input for
// good ([session.Session.InputBroken]). A writer whose Input or Paste then
// comes to nothing for that reason gets one Notice with the reason
// "input-broken" for its connection (the one whose own write broke the input
// as well), which is counted ("input_broken_notices") and logged once; status
// reports the state in its counters as "input_broken".
//
// The locks are taken in this order, and a lock is never taken while one that
// comes later in the list is held: the input mutex (inMu), the session's
// input lock, the session's output lock, the hub lock (mu), the pane's own
// lock, the session's counter lock. The subscriber runs under the session's
// output lock and takes mu, never inMu. Nothing that takes the session's
// output lock or its input lock (Input, Paste, Resize, Screen, Counters,
// SubscribeScreen) is called with mu held.
//
// # Events and counters
//
// Events go to the event log through a bounded queue and a goroutine of their
// own, so a slow disk never holds the PTY loop. They carry names, pids, command
// lines, codes and counters: never a key or a byte of output. The service adds
// its counts to the session's ([session.Session.AddCount]), and status and the
// Exit frame add the pane's cap counters. A queue or a cap that is hit is
// counted.
package serve

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

// Names of the sockets in the runtime folder.
const (
	AttachSocket  = "attach.sock"
	ControlSocket = "control.sock"
)

// Limits and default timings (the lead-hosting design, sections 2.1 to 2.5).
const (
	// QueueBytes is the default bound of a client's output queue, in payload
	// bytes. One snapshot outside it may wait as well.
	QueueBytes = 1 << 20
	// MaxHandshakes is the most attach connections that wait for their
	// Hello at once; a further one is closed at once.
	MaxHandshakes = 4
	// MaxTickets is the most tickets that wait for their Hello.
	MaxTickets = 4

	DefaultTicketLifetime   = 30 * time.Second
	DefaultCheckInterval    = 5 * time.Second
	DefaultHandshakeTimeout = 5 * time.Second
	DefaultStall            = 30 * time.Second
	DefaultOverflowWindow   = 10 * time.Second
	DefaultSnapshotNotice   = time.Second
	DefaultSnapshotHold     = 150 * time.Millisecond
	DefaultFlushWait        = 2 * time.Second
	DefaultPollInterval     = 100 * time.Millisecond

	// maxControlConns bounds the control requests handled at once.
	maxControlConns = 8
	// snapshotPoll is how often a client that waits for a snapshot point is
	// looked at when no output arrives.
	snapshotPoll = 10 * time.Millisecond
	// eventQueue is the length of the event queue.
	eventQueue = 512
	// maxCmdline is the most bytes of /proc/PID/cmdline that are kept.
	maxCmdline = 512
)

// PeerCred is what the kernel says of the process at the other end of a unix
// socket connection.
type PeerCred struct {
	UID uint32
	PID int
}

// Config describes one service. Session, Dir, Lead, Epoch, Runtime, Log and
// DaemonLock are required; a zero timing or limit takes its default, and the
// functions at the end are for tests.
type Config struct {
	// Session is the hosted session. The service takes its one subscriber
	// slot. It never closes the session.
	Session *session.Session
	// Dir is the runtime folder: a directory (never a symlink) of this user
	// with mode 0700. The sockets are made in it.
	Dir string
	// Lead is the role name, Epoch the holder's epoch (never 0), Runtime and
	// RuntimeVersion what Welcome and status report.
	Lead           string
	Epoch          uint64
	Runtime        string
	RuntimeVersion string
	// Log is the event log. The service never closes it.
	Log *eventlog.Log
	// DaemonLock is the path of the daemon's lock file: the control socket
	// accepts the process whose pid it holds while it is locked.
	DaemonLock string

	// TicketLifetime is how long a registered ticket stays valid.
	TicketLifetime time.Duration
	// CheckInterval is the time between Check frames to a client.
	CheckInterval time.Duration
	// HandshakeTimeout is the time a connection has for its first frame, and
	// a control connection for its request and its response.
	HandshakeTimeout time.Duration
	// Stall is the time a client's socket may take no bytes before the client
	// is disconnected.
	Stall time.Duration
	// OverflowWindow is the time within which a second overflow disconnects a
	// client that was resynced.
	OverflowWindow time.Duration
	// SnapshotNotice is the time without a snapshot point after which a
	// client gets a snapshot-waiting notice.
	SnapshotNotice time.Duration
	// SnapshotHold is how long, with mode 2026 set, a snapshot waits after the
	// parser first became quiescent.
	SnapshotHold time.Duration
	// FlushWait is how long a client that is being disconnected gets to take
	// its last frames (a final notice, the Exit record) before its connection
	// is closed.
	FlushWait time.Duration
	// PollInterval is how often the lifetime facts are read while clients
	// are attached.
	PollInterval time.Duration
	// QueueBytes bounds a client's output queue; the default is [QueueBytes].
	QueueBytes int

	// PeerCred reads the peer's credentials; the default uses SO_PEERCRED.
	PeerCred func(*net.UnixConn) (PeerCred, error)
	// PidfdPID reads the pid that a pidfd names; the default reads
	// /proc/self/fdinfo/N.
	PidfdPID func(fd int) (int, error)
	// Now is the clock for ticket lifetimes, check times and the overflow
	// window; the default is time.Now.
	Now func() time.Time
}

func (c *Config) withDefaults() {
	for _, d := range []struct {
		p *time.Duration
		v time.Duration
	}{
		{&c.TicketLifetime, DefaultTicketLifetime}, {&c.CheckInterval, DefaultCheckInterval},
		{&c.HandshakeTimeout, DefaultHandshakeTimeout}, {&c.Stall, DefaultStall},
		{&c.OverflowWindow, DefaultOverflowWindow}, {&c.SnapshotNotice, DefaultSnapshotNotice},
		{&c.SnapshotHold, DefaultSnapshotHold}, {&c.FlushWait, DefaultFlushWait},
		{&c.PollInterval, DefaultPollInterval},
	} {
		if *d.p <= 0 {
			*d.p = d.v
		}
	}
	if c.QueueBytes <= 0 {
		c.QueueBytes = QueueBytes
	}
	if c.PeerCred == nil {
		c.PeerCred = peerCred
	}
	if c.PidfdPID == nil {
		c.PidfdPID = pidfdPID
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Server is the attach and control service of one holder. Make it with New,
// run it with Serve and stop it with Close.
type Server struct {
	cfg  Config
	sess *session.Session
	log  *eventlog.Log
	pane *sessionpane.Pane
	uid  uint32

	dirfd     int
	attachLn  *net.UnixListener
	controlLn *net.UnixListener
	unsub     func()

	// inMu is held around a writer frame, from the role check to the end of
	// the session call, and around every change of writer. It is the first
	// lock of the order in the package doc.
	inMu sync.Mutex

	// mu is the hub lock. Lock order: inMu, the session's input lock, the
	// session's output lock, then mu, then the pane's own lock. Nothing that
	// takes the session's output lock (Screen, Resize, Counters,
	// SubscribeScreen) is called with mu held.
	mu          sync.Mutex
	clients     map[*client]struct{} // attached: after the Welcome
	writer      *client              // the claimed slots, attached or not yet
	viewer      *client
	tickets     []ticketRec
	handshaking int
	controlBusy int
	conns       map[net.Conn]struct{}
	closed      bool // teardown began: nothing new is accepted
	finishing   bool // the session is over

	// The emulator state the clients were told of.
	lastModes    sessionpane.ModeSet
	modesPayload []byte
	lastSize     wire.Size
	sizePayload  []byte
	state        stateKey
	statePayload []byte
	facts        wire.Facts
	exitStatus   *int
	nextFacts    time.Time
	snapHint     int
	// The digest of the screen at digestPos, when digestValid: it is cleared
	// when the size changes, as a resize does not move the position.
	digestHex   string
	digestPos   uint64
	digestValid bool

	stop         chan struct{}
	stopOnce     sync.Once
	teardownOnce sync.Once
	teardownErr  error
	served       atomic.Bool
	wg           sync.WaitGroup
	kick         chan struct{}
	gone         chan struct{} // a client left while the session is over

	evMu     sync.RWMutex
	evClosed bool
	evq      chan evItem
	evDone   chan struct{}
}

type evItem struct {
	name eventlog.EventName
	f    eventlog.Fields
}

// New checks the configuration and the runtime folder, makes the two sockets
// and takes the session's subscriber slot. It starts only the goroutine that
// writes events to the log; Serve starts the rest. On an error nothing is left
// open.
func New(cfg Config) (*Server, error) {
	cfg.withDefaults()
	switch {
	case cfg.Session == nil:
		return nil, errors.New("serve: Config.Session is required")
	case cfg.Log == nil:
		return nil, errors.New("serve: Config.Log is required")
	case cfg.Dir == "":
		return nil, errors.New("serve: Config.Dir is required")
	case cfg.DaemonLock == "":
		return nil, errors.New("serve: Config.DaemonLock is required")
	case cfg.Epoch == 0:
		return nil, errors.New("serve: Config.Epoch must not be 0")
	}
	// The welcome the service will send must encode: this checks the lead,
	// the runtime and the version before any socket exists.
	if _, err := wire.EncodeWelcome(wire.Welcome{
		Version: wire.ProtocolVersion, Role: wire.RoleViewer, Lead: cfg.Lead, Epoch: cfg.Epoch,
		Runtime: cfg.Runtime, RuntimeVersion: cfg.RuntimeVersion, Caps: wire.DefaultCaps(),
	}); err != nil {
		return nil, fmt.Errorf("serve: %w", err)
	}
	dirfd, err := openRuntimeDir(cfg.Dir)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg: cfg, sess: cfg.Session, log: cfg.Log, uid: uint32(os.Geteuid()),
		dirfd:   dirfd,
		clients: map[*client]struct{}{}, conns: map[net.Conn]struct{}{},
		stop: make(chan struct{}), kick: make(chan struct{}, 1), gone: make(chan struct{}, 1),
		evq: make(chan evItem, eventQueue), evDone: make(chan struct{}),
	}
	if s.attachLn, err = listenIn(dirfd, AttachSocket); err != nil {
		unix.Close(dirfd)
		return nil, err
	}
	if s.controlLn, err = listenIn(dirfd, ControlSocket); err != nil {
		s.attachLn.Close()
		_ = unix.Unlinkat(dirfd, AttachSocket, 0)
		unix.Close(dirfd)
		return nil, err
	}
	s.facts, s.exitStatus = wireFacts(cfg.Session.Facts())
	unsub, err := cfg.Session.SubscribeScreen(func(p *sessionpane.Pane, _ uint64) { s.pane = p }, s.onChunk)
	if err != nil {
		s.attachLn.Close()
		s.controlLn.Close()
		_ = unix.Unlinkat(dirfd, AttachSocket, 0)
		_ = unix.Unlinkat(dirfd, ControlSocket, 0)
		unix.Close(dirfd)
		return nil, fmt.Errorf("serve: %w", err)
	}
	s.unsub = unsub
	go s.logger()
	return s, nil
}

// Serve accepts connections until the session is over or Close is called. When
// the session is over, every client gets a State and an Exit frame and is
// closed, the sockets are removed and Serve returns nil. A call to Close makes
// it return too. Serve may be called once.
func (s *Server) Serve() error {
	if !s.served.CompareAndSwap(false, true) {
		return errors.New("serve: Serve was called already")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.wg.Add(3)
	s.mu.Unlock()
	go s.acceptLoop(s.attachLn, s.handleAttach)
	go s.acceptLoop(s.controlLn, s.handleControl)
	go s.timekeeper()
	select {
	case <-s.sess.Done():
		s.finishSession()
	case <-s.stop:
	}
	return s.teardown()
}

// Close stops the service: it closes the sockets and every connection, removes
// the socket files, takes the subscriber slot back and waits for every
// goroutine. It does not stop the session. It may be called more than once, and
// while Serve runs.
func (s *Server) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	return s.teardown()
}

func (s *Server) teardown() error {
	s.teardownOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		conns := make([]net.Conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()
		s.stopOnce.Do(func() { close(s.stop) })

		var errs []error
		for _, ln := range []*net.UnixListener{s.attachLn, s.controlLn} {
			if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
		for _, name := range []string{AttachSocket, ControlSocket} {
			if err := unix.Unlinkat(s.dirfd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				errs = append(errs, err)
			}
		}
		s.unsub() // outside mu: the subscriber takes mu
		for _, c := range conns {
			c.Close()
		}
		// Connections made between the snapshot of the set and the closed
		// flag were refused by track; the rest is closed above.
		s.wg.Wait()
		s.stopLogger()
		unix.Close(s.dirfd)
		s.teardownErr = errors.Join(errs...)
	})
	return s.teardownErr
}

// spawn runs fn as a goroutine that Close waits for, unless teardown began.
func (s *Server) spawn(fn func()) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		fn()
	}()
	return true
}

// track adds a connection to the set that teardown closes; it reports false,
// and the caller closes the connection, when teardown began.
func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) now() time.Time { return s.cfg.Now() }

// kickKeeper wakes the timekeeper.
func (s *Server) kickKeeper() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Server) acceptLoop(ln *net.UnixListener, handle func(*net.UnixConn)) {
	defer s.wg.Done()
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select { // a transient error (no descriptors): pause and go on
			case <-time.After(10 * time.Millisecond):
				continue
			case <-s.stop:
				return
			}
		}
		if !s.track(c) {
			c.Close()
			return
		}
		if !s.spawn(func() {
			defer s.untrack(c)
			defer c.Close()
			handle(c)
		}) {
			s.untrack(c)
			c.Close()
			return
		}
	}
}

// Counter names that the service adds to the session's counters.
const (
	cntRefusedUID       = "attach_refused_uid"
	cntRefusedFull      = "attach_refused_handshake_full"
	cntRefusedHandshake = "attach_refused_handshake"
	cntRefusedVersion   = "attach_refused_version"
	cntRefusedTicket    = "attach_refused_ticket"
	cntRefusedBusy      = "attach_refused_busy"
	cntControlPeer      = "control_refused_peer"
	cntControlEpoch     = "control_refused_epoch"
	cntControlOther     = "control_refused_other"
	cntBadFrames        = "bad_frames"
	cntViewerInput      = "viewer_input_refused"
	cntInputBroken      = "input_broken_notices"
	cntChecks           = "checks_sent"
	cntSnapshotsTaken   = "snapshots_taken"
	cntSnapshotErrors   = "snapshot_errors"
	cntSnapshotWaiting  = "snapshot_waiting_notices"
	cntResyncs          = "resyncs"
	cntTooSlow          = "too_slow_disconnects"
	cntStalled          = "stalled_disconnects"
	cntWriterChanges    = "writer_changes"
	cntEventsDropped    = "events_dropped"
	cntEventErrors      = "event_log_errors"
)

// count adds one to a counter of the service. The names above are none of the
// session's reserved ones (a test checks it), so AddCount's error cannot occur.
func (s *Server) count(name string) { _ = s.sess.AddCount(name, 1) }

// allCounters is every counter of the session and the service, and the pane's
// cap counters. It takes the session's output lock: call it without mu.
func (s *Server) allCounters() map[string]uint64 {
	out := s.sess.Counters()
	if s.pane != nil {
		for _, v := range s.pane.Counters().List() {
			out[v.Name] = v.Value
		}
	}
	return out
}

// emit queues an event for the log. It never blocks: a full queue drops the
// event and counts it.
func (s *Server) emit(name eventlog.EventName, f eventlog.Fields) {
	s.evMu.RLock()
	defer s.evMu.RUnlock()
	if s.evClosed {
		s.count(cntEventsDropped)
		return
	}
	select {
	case s.evq <- evItem{name: name, f: f}:
	default:
		s.count(cntEventsDropped)
	}
}

// logger writes the queued events to the event log.
func (s *Server) logger() {
	defer close(s.evDone)
	for it := range s.evq {
		s.write(it)
	}
}

func (s *Server) write(it evItem) {
	if err := s.log.Append(eventlog.KindHolder, it.name, it.f); err != nil {
		s.count(cntEventErrors)
	}
}

// stopLogger closes the event queue and waits for the queued events to be
// written. It is called after every goroutine that emits has returned.
func (s *Server) stopLogger() {
	s.evMu.Lock()
	s.evClosed = true
	close(s.evq)
	s.evMu.Unlock()
	<-s.evDone
}
