//go:build linux

// Package ptyhost owns the pseudo-terminal of one hosted agent (the
// lead-hosting design, section 1.2): it opens the pair, starts the agent as a
// session leader with the slave as its controlling terminal, and keeps the
// master. The holder is the only process that holds the master; nothing here
// hands the descriptor out. With Config.NoControllingTerminal the child still
// leads a new session and still has the slave as descriptors 0, 1 and 2, but
// it is not given a controlling terminal: a sandbox probe that runs first
// inside the child's session takes the terminal itself. The option is off by
// default, and then the start is as described above.
//
// The package uses only golang.org/x/sys/unix and the standard library, and
// only on Linux. No shell is involved: the child is executed directly from
// Config.Argv with exactly Config.Env.
//
// # Streams and lifetime
//
// Three facts are kept apart (the lead-hosting design, section 1.4):
//
//   - the terminal stream ended: Read returns io.EOF (the kernel reports EIO
//     on the master once the last holder of the slave has closed it);
//   - the TUI, the direct child, exited: Wait returns its Exit;
//   - the agent's process tree is gone: TreeGone, with a proof string that
//     names the method.
//
// A grandchild that keeps the slave open keeps the stream open after the
// child exited, and a child can close the stream while it still runs.
//
// # Tree proof
//
// When the process runs in a control group of its own, the tree is gone when
// that group and every group below it hold only this process and no live
// process is left in the child's session (proof starts with "cgroup", and
// names both). A process in a child group is not listed in its parent's
// cgroup.procs, so the walk reads the cgroup.procs of every descendant group,
// breadth first and bounded (Config.CgroupWalkLimit); a walk that hits its
// bound, or cannot read a group, proves nothing and is counted. A group that
// cannot be opened any more is not taken as removed until its parent no
// longer lists the name; a name that appeared in the meantime is walked, and a
// parent that cannot be listed either (it may have been renamed with its
// children) proves nothing: the walk counts it, and a later TreeGone call
// tries again. "Of
// its own" is judged once, in Start, before the child is created: the group's
// cgroup.procs lists only this process and the group has no child group.
// Otherwise the fallback is that no process is left in the child's session
// (proof starts with "session"). That is weaker: a descendant that started
// its own session escapes it, and callers should label it so. Zombies do not
// count as present in the session scan (they run nothing, and a process that
// adopts orphans reaps them on its own schedule).
//
// The walk is not atomic, and the limit remains. A group, or an ancestor of
// one, may be renamed or removed after it was listed: the walk finds a renamed
// group again only while its parent can still be listed, and counts the rest as
// unreadable. A process that may write the cgroup.procs of a group in a
// delegated unit can also move during the walk, from a group not read yet into
// one already read, or out of the subtree (for example into a transient
// scope), and is then not seen by the walk. The session scan catches such a
// process only while it stays in the child's
// session. So the cgroup proof is strong only when the agent cannot migrate,
// for example with a read-only cgroup filesystem inside the sandbox.
//
// # What the proofs fit
//
// Both proofs fit a holder that starts the agent directly and alone in its
// control group: the child of Start is the agent (or the process that stays
// the leader of the agent's whole tree), the holder is the only other process
// in its group, and the agent's tree stays in the child's session or in that
// group. The sandbox layout of the lead-hosting design (section 1.1) is not
// that. A network helper runs in the holder's group, so Start does not judge
// the group "of its own" and the cgroup method is not chosen; and the probe
// gives the agent a new session, so the session proof, which is keyed on the
// direct child's pid, never sees the agent's session. The sandbox work must add
// what this package does not have: the caller naming the group to judge and
// the pids it expects to find there (the holder, the helper), or the session
// id to scan. Until then neither proof is evidence about a sandboxed tree, and
// the holder must not use them as such.
//
// # Signalling and pid reuse
//
// The kernel does not give a pid out again while some process still uses the
// number as its process group id or session id, but only while the number is
// in use. A session leader that was reaped, whose session then emptied
// unobserved, leaves the number free: the next process that gets it and calls
// setsid would look like a member of the old session, and a signal sent "to
// the session" would hit a stranger, outside the agent's tree.
//
// So the direct child is not reaped when it exits. Its exit facts are taken
// with waitid and WNOWAIT, and Wait returns at once, but the zombie stays: it
// holds the pid, the group id and the session id. It is reaped only when the
// session is proven empty (a scan that finds no live member while the zombie
// holds the id is authoritative, because no process can join a session whose
// leader is a zombie; the control group proof needs the same scan), or when the
// host is closed (Close, the final release; the host then gives up acting on
// the ids). Every signal sent by group or session id is sent under the mutex
// that guards the reap and only while the child is not reaped. Session members
// other than the leader are signalled through a pidfd after their session id
// was read again. Once the child is reaped without the session having been
// proven empty, TreeGone by the session method says "not gone" for good, and
// Stop signals nothing by those ids.
//
// The facts and the zombie are visible to the caller: the child shows as
// defunct in a process listing between its exit and the proof.
//
// # What the child inherits
//
// Start gives the child exactly the slave as descriptors 0, 1 and 2 and
// exactly Config.Env. Two other things cross fork and exec on their own:
//
//   - Descriptors of 3 or above that the holder opened without close-on-exec
//     (a socket handed over by the service manager, a descriptor inherited from
//     the holder's own parent). This package and the standard library open
//     their descriptors close-on-exec, so these are the ones the process was
//     started with. They are the holder's to close: it calls CloseOnExecFrom(3)
//     once at start, before the first Start.
//   - Signals the holder ignores. The Go runtime restores the default
//     disposition in the child for every signal it handles itself, but an
//     ignored signal (SIG_IGN) is kept across fork and exec; the runtime also
//     leaves SIGHUP and SIGINT ignored when the process started with them
//     ignored, and signal.Ignore does it for any signal. syscall.ForkExec has
//     no field to reset dispositions in the child, and doing it would need a
//     fork and exec of our own written with raw system calls, unsafe code in
//     the window between fork and exec, which this package does not contain.
//     So the holder must not ignore signals the agent should not ignore, and
//     asserts it at start with IgnoredSignals.
package ptyhost

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// ReadBufferSize is the size of the buffer the caller reads the master
	// with. Read never returns more than this in one call, even for a larger
	// buffer.
	ReadBufferSize = 64 << 10

	// Terminal size limits, inclusive. Start and Resize refuse a size outside
	// them; the caller clamps before it asks. This layer is looser than the
	// holder on purpose and keeps accepting a 2 by 1 terminal: the holder's own
	// minimum is 20 by 5 (the lead-hosting design, section 1.3), and the
	// holder's caller applies it before it calls Start or Resize.
	MinCols = 2
	MaxCols = 500
	MinRows = 1
	MaxRows = 200

	// DefaultWriteTimeout bounds one Write when Config.WriteTimeout is zero.
	DefaultWriteTimeout = 5 * time.Second

	// DefaultProcScanLimit bounds how many /proc entries one scan reads when
	// Config.ProcScanLimit is zero.
	DefaultProcScanLimit = 1 << 17

	maxStatBytes   = 4 << 10
	maxProcsBytes  = 1 << 20
	maxCgroupBytes = 64 << 10
)

// Config describes the child and the injectable paths.
type Config struct {
	// Argv is the child's command line. Argv[0] must contain a slash (an
	// absolute path, or one relative to Dir): no PATH search is made, and the
	// caller's own environment plays no part.
	Argv []string
	// Env is exactly the child's environment, nothing inherited. A nil Env is
	// an empty environment.
	Env []string
	// Dir is the child's working directory; empty keeps the caller's.
	Dir string
	// Cols and Rows are the initial terminal size, within MinCols..MaxCols
	// by MinRows..MaxRows.
	Cols, Rows int

	// NoControllingTerminal starts the child in a new session (Setsid) but
	// without making the slave its controlling terminal (no Setctty). The
	// child still has the slave as descriptors 0, 1 and 2, leads its session
	// and has no controlling terminal until it takes one itself (a sandbox
	// probe does, with TIOCSCTTY on descriptor 0). The zero value keeps the
	// start unchanged: a new session with the slave as its controlling
	// terminal.
	NoControllingTerminal bool

	// WriteTimeout bounds one Write (see Host.Write); zero means
	// DefaultWriteTimeout.
	WriteTimeout time.Duration

	// ProcRoot is the proc filesystem root, default "/proc". It is read for
	// the control group of this process, for the session scan and for the
	// session kill; tests give a fake tree.
	ProcRoot string
	// CgroupRoot is the cgroup v2 mount, default "/sys/fs/cgroup".
	CgroupRoot string
	// NoCgroup skips the control group method, so the tree proof is always
	// the session fallback.
	NoCgroup bool
	// ProcScanLimit bounds the entries one /proc scan reads; zero means
	// DefaultProcScanLimit. A scan that hits it proves nothing, and counts.
	ProcScanLimit int
	// CgroupWalkLimit bounds how many control groups one walk of the
	// holder's group and the groups below it reads; zero means
	// DefaultCgroupWalkLimit. A walk that hits it proves nothing, and counts.
	CgroupWalkLimit int
}

// Exit is how the direct child ended.
type Exit struct {
	Pid int
	// How is "exit" (Code is the status), "signal" (Signal is set) or
	// "unknown" (the child could not be waited for; Err says why).
	How      string
	Code     int // exit status; -1 unless How is "exit"
	Signal   syscall.Signal
	CoreDump bool
	Err      error
}

// String describes the exit in one line.
func (e Exit) String() string {
	switch e.How {
	case "exit":
		return fmt.Sprintf("exited with status %d", e.Code)
	case "signal":
		s := fmt.Sprintf("killed by signal %d (%s)", int(e.Signal), e.Signal)
		if e.CoreDump {
			s += ", core dumped"
		}
		return s
	}
	return fmt.Sprintf("unknown (%v)", e.Err)
}

// Counters are the caps and failures the host counted instead of hiding.
type Counters struct {
	// ScanTruncated counts /proc scans that hit the entry limit.
	ScanTruncated uint64
	// ScanErrors counts /proc entries that could not be read or parsed (an
	// entry that vanished during the scan is not an error).
	ScanErrors uint64
	// CgroupTruncated counts walks of the control group tree that hit the
	// group or process limit.
	CgroupTruncated uint64
	// CgroupErrors counts control groups that could not be read during a
	// walk (one that vanished is not an error).
	CgroupErrors uint64
	// PidfdFailures counts pidfd_open and pidfd_send_signal calls that failed
	// for a reason other than the process being gone (ESRCH): a process that
	// should have been signalled may not have been.
	PidfdFailures uint64
}

// Host is one running child on a pseudo-terminal.
type Host struct {
	cfg      Config
	master   *os.File
	pid      int
	writeTmo time.Duration
	procRoot string
	scanMax  int
	walkMax  int

	wmu sync.Mutex // serialises Write (deadline and write together)

	// cgroupProcs is the cgroup.procs path judged "of its own" at Start, or
	// empty; cgroupNote says why the group was not used.
	cgroupProcs string
	cgroupNote  string

	// mu guards reaped and every signal sent by the child's group or session
	// id, and the judging of the session and the control group: while a
	// holder of mu sees reaped false, the zombie reserves the pid.
	mu     sync.Mutex
	reaped bool
	exit   Exit
	// done is closed once the child has exited and exit is set; the zombie may
	// still be held. release is closed (once, by letGo) when the zombie may be
	// reaped.
	done        chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	killFor     time.Duration // how long Stop keeps killing after the grace

	sessionEmpty atomic.Bool // latched: the session had no live member

	scanTruncated atomic.Uint64
	scanErrors    atomic.Uint64

	cgroupTruncated atomic.Uint64
	cgroupErrors    atomic.Uint64
	pidfdFailures   atomic.Uint64

	closeOnce sync.Once
}

// Start opens a pseudo-terminal pair, sets its size and starts cfg.Argv as a
// session leader with the slave as controlling terminal and as stdin, stdout
// and stderr. The slave is closed in the caller; the master is close-on-exec
// and never inherited.
func Start(cfg Config) (*Host, error) {
	if len(cfg.Argv) == 0 || !strings.Contains(cfg.Argv[0], "/") {
		return nil, errors.New("ptyhost: Argv[0] must be a path (no PATH search is made)")
	}
	if err := checkSize(cfg.Cols, cfg.Rows); err != nil {
		return nil, err
	}
	h := &Host{
		cfg:      cfg,
		writeTmo: cfg.WriteTimeout,
		procRoot: cfg.ProcRoot,
		scanMax:  cfg.ProcScanLimit,
		walkMax:  cfg.CgroupWalkLimit,
		done:     make(chan struct{}),
		release:  make(chan struct{}),
		killFor:  killWait,
	}
	if h.writeTmo <= 0 {
		h.writeTmo = DefaultWriteTimeout
	}
	if h.procRoot == "" {
		h.procRoot = "/proc"
	}
	if h.scanMax <= 0 {
		h.scanMax = DefaultProcScanLimit
	}
	if h.walkMax <= 0 {
		h.walkMax = DefaultCgroupWalkLimit
	}
	if cfg.NoCgroup {
		h.cgroupNote = "control group method disabled"
	} else {
		root := cfg.CgroupRoot
		if root == "" {
			root = "/sys/fs/cgroup"
		}
		h.cgroupProcs, h.cgroupNote = privateCgroup(h.procRoot, root, os.Getpid(), h.walkMax)
	}

	masterFd, slaveFd, err := openPair()
	if err != nil {
		return nil, err
	}
	if err := setWinsize(masterFd, cfg.Cols, cfg.Rows); err != nil {
		_ = unix.Close(masterFd)
		_ = unix.Close(slaveFd)
		return nil, fmt.Errorf("ptyhost: set size: %w", err)
	}
	env := cfg.Env
	if env == nil {
		env = []string{}
	}
	// The slave is the child's descriptors 0, 1 and 2, and Setctty makes
	// descriptor 0 its controlling terminal after Setsid, unless
	// NoControllingTerminal is set (Setsid alone: the child leads a new
	// session with no controlling terminal). Every other descriptor of ours
	// is close-on-exec (the master and the slave were opened so); descriptors
	// a caller opened without it are the caller's.
	sys := &syscall.SysProcAttr{Setsid: true, Setctty: !cfg.NoControllingTerminal, Ctty: 0}
	pid, err := syscall.ForkExec(cfg.Argv[0], cfg.Argv, &syscall.ProcAttr{
		Dir:   cfg.Dir,
		Env:   env,
		Files: []uintptr{uintptr(slaveFd), uintptr(slaveFd), uintptr(slaveFd)},
		Sys:   sys,
	})
	_ = unix.Close(slaveFd)
	if err != nil {
		_ = unix.Close(masterFd)
		return nil, fmt.Errorf("ptyhost: start %q: %w", cfg.Argv[0], err)
	}
	h.pid = pid
	// The master is non-blocking so that the runtime poller serves it: a
	// Close unblocks a Read and a write deadline works.
	h.master = os.NewFile(uintptr(masterFd), "ptmx")
	go h.reap()
	return h, nil
}

func checkSize(cols, rows int) error {
	if cols < MinCols || cols > MaxCols || rows < MinRows || rows > MaxRows {
		return fmt.Errorf("ptyhost: size %dx%d outside %d..%d by %d..%d", cols, rows, MinCols, MaxCols, MinRows, MaxRows)
	}
	return nil
}

// openPair opens /dev/ptmx, unlocks the slave and opens it. The slave name
// comes from TIOCGPTN. Neither descriptor is a controlling terminal of the
// caller (O_NOCTTY) and both are close-on-exec; the master is non-blocking.
func openPair() (master, slave int, err error) {
	master, err = unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, -1, fmt.Errorf("ptyhost: open /dev/ptmx: %w", err)
	}
	fail := func(what string, e error) (int, int, error) {
		_ = unix.Close(master)
		return -1, -1, fmt.Errorf("ptyhost: %s: %w", what, e)
	}
	if err := unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0); err != nil {
		return fail("unlock pty", err)
	}
	n, err := unix.IoctlGetInt(master, unix.TIOCGPTN)
	if err != nil {
		return fail("pty number", err)
	}
	slave, err = unix.Open(fmt.Sprintf("/dev/pts/%d", n), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail("open pty slave", err)
	}
	return master, slave, nil
}

func setWinsize(fd, cols, rows int) error {
	return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
}

// ctl runs f with the master's descriptor kept valid against Close.
func (h *Host) ctl(f func(fd int) error) error {
	rc, err := h.master.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = f(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

// Pid returns the direct child's process id. It is for display and records:
// do not signal it, because it may be reaped and reused; use Stop.
func (h *Host) Pid() int { return h.pid }

// Read reads terminal output from the master, at most ReadBufferSize bytes
// at a time. EIO, which the kernel reports once the slave side has closed
// and the output was drained, is the normal end of the terminal stream and
// is returned as io.EOF. After Close it returns an error wrapping
// os.ErrClosed.
func (h *Host) Read(p []byte) (int, error) {
	if len(p) > ReadBufferSize {
		p = p[:ReadBufferSize]
	}
	n, err := h.master.Read(p)
	if err != nil && errors.Is(err, syscall.EIO) {
		err = io.EOF
	}
	return n, err
}

// Write writes all of p to the terminal input, or returns an error. It does
// not block forever when the child does not read: the master is
// non-blocking and one Write has a deadline (Config.WriteTimeout, default
// DefaultWriteTimeout) for the whole of p. On a timeout the error wraps
// os.ErrDeadlineExceeded and n is how many bytes were accepted before it; a
// caller must treat the input as broken at that point (a prefix was
// delivered). Writes are serialised, but the caller still holds its input
// lock around Write so that the bytes of two sources do not interleave.
func (h *Host) Write(p []byte) (int, error) {
	return h.write(p, time.Time{}, false)
}

// WriteBy is Write with a second bound: its deadline is the earlier of the
// host's write timeout from now (Config.WriteTimeout) and deadline. A
// deadline that has passed when the write starts, including the zero time,
// returns the timeout error (it wraps os.ErrDeadlineExceeded) with n 0,
// writing nothing; it is not read as "no deadline". The host never retries or
// completes a write that failed at the bound: n bytes, a prefix of p, were
// accepted, and the caller must treat the input as broken as it does after a
// timed-out Write. The time spent waiting for another writer (writes are
// serialised) counts against deadline.
func (h *Host) WriteBy(p []byte, deadline time.Time) (int, error) {
	return h.write(p, deadline, true)
}

// write is the one write: p to the master with a deadline of the host's write
// timeout from now, or by when hasBy is set and by is earlier.
func (h *Host) write(p []byte, by time.Time, hasBy bool) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	h.wmu.Lock()
	defer h.wmu.Unlock()
	now := time.Now()
	end := now.Add(h.writeTmo)
	if hasBy && by.Before(end) {
		end = by
	}
	// A bound that has passed is set as a past time that is not the zero
	// time: the zero time would mean no deadline at all.
	passed := !end.After(now)
	if passed {
		end = time.Unix(1, 0)
	}
	if err := h.master.SetWriteDeadline(end); err != nil {
		return 0, fmt.Errorf("ptyhost: write: %w", err)
	}
	if passed {
		return 0, fmt.Errorf("ptyhost: write: 0 of %d bytes accepted: %w", len(p), os.ErrDeadlineExceeded)
	}
	n, err := h.master.Write(p)
	if err != nil {
		return n, fmt.Errorf("ptyhost: write: %d of %d bytes accepted: %w", n, len(p), err)
	}
	return n, nil
}

// Queued returns how many bytes are queued for reading on the master, asked
// of the kernel (FIONREAD) without reading them: a Read afterwards returns
// them. It is the output of the child that the caller has not read yet; bytes
// the child wrote that the kernel has not yet handed to the terminal's line
// discipline are not counted. After Close it returns an error.
func (h *Host) Queued() (int, error) {
	var n int
	err := h.ctl(func(fd int) error {
		v, err := unix.IoctlGetInt(fd, unix.TIOCINQ) // FIONREAD under its other name
		n = v
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("ptyhost: queued: %w", err)
	}
	return n, nil
}

// Resize sets the terminal size; the child gets SIGWINCH. A size outside
// MinCols..MaxCols by MinRows..MaxRows is refused.
func (h *Host) Resize(cols, rows int) error {
	if err := checkSize(cols, rows); err != nil {
		return err
	}
	if err := h.ctl(func(fd int) error { return setWinsize(fd, cols, rows) }); err != nil {
		return fmt.Errorf("ptyhost: resize: %w", err)
	}
	return nil
}

// Close closes the master. The kernel then hangs up the terminal (SIGHUP to
// the session leader's foreground group, and EOF-like errors for any reader
// of the slave). Close is also the final release of the direct child's
// zombie (see the package comment): it tries the tree proof one last time
// and lets the zombie be reaped whether the tree is gone or not, and the
// host stops acting on the child's group and session ids. It is safe to call
// more than once. After the child has exited and Close has returned, the
// host holds no goroutine and no descriptor.
func (h *Host) Close() error {
	var err error
	h.closeOnce.Do(func() {
		err = h.master.Close()
		// The last chance to prove the tree gone while the zombie still
		// reserves its ids; then the zombie is let go, proven or not.
		if h.hasExited() {
			h.TreeGone()
		}
		h.letGo()
	})
	return err
}

// Counters returns the counted caps and failures.
func (h *Host) Counters() Counters {
	return Counters{
		ScanTruncated:   h.scanTruncated.Load(),
		ScanErrors:      h.scanErrors.Load(),
		CgroupTruncated: h.cgroupTruncated.Load(),
		CgroupErrors:    h.cgroupErrors.Load(),
		PidfdFailures:   h.pidfdFailures.Load(),
	}
}
