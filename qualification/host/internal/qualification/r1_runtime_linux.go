//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type r1DiskControl struct {
	dir      *os.File
	path     string
	initial  syscall.Stat_t
	clock    r1Clock
	deadline time.Time
}

func r1Object(s syscall.Stat_t) R1ObjectIdentity { return R1ObjectIdentity{s.Dev, s.Ino} }
func r1OpenControl(path string, c r1Clock, deadline time.Time) (r1Control, error) {
	dir, err := openAbsolute(path, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(dir.Fd()), &st); err != nil {
		dir.Close()
		return nil, err
	}
	return &r1DiskControl{dir, path, st, c, deadline}, nil
}
func (s *r1DiskControl) Directory() R1ObjectIdentity { return r1Object(s.initial) }
func (s *r1DiskControl) Close() error                { return s.dir.Close() }
func (s *r1DiskControl) Check() error {
	f, err := openAbsolute(s.path, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return err
	}
	var current, pinned syscall.Stat_t
	e1 := syscall.Fstat(int(f.Fd()), &current)
	e2 := syscall.Fstat(int(s.dir.Fd()), &pinned)
	e3 := f.Close()
	if e1 != nil {
		return e1
	}
	if e2 != nil {
		return e2
	}
	if e3 != nil {
		return e3
	}
	for _, st := range []syscall.Stat_t{current, pinned} {
		if st.Dev != s.initial.Dev || st.Ino != s.initial.Ino || st.Mode != s.initial.Mode || st.Uid != s.initial.Uid || st.Gid != s.initial.Gid || st.Nlink != s.initial.Nlink {
			return errors.New("R1 directory replaced or changed")
		}
	}
	return nil
}
func r1Regular(s syscall.Stat_t) bool {
	return s.Mode&syscall.S_IFMT == syscall.S_IFREG && s.Nlink == 1 && s.Size >= 0 && s.Size <= R1MaxFrame
}
func r1SameStat(a, b syscall.Stat_t) bool {
	// Atime may change on read; identity, ownership, mode, links, size, mtime
	// and ctime must not. Directory publication may change directory times.
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
func (s *r1DiskControl) Read(name string, deadline time.Time) (result r1File, retErr error) {
	if s.deadline.Before(deadline) {
		deadline = s.deadline
	}
	if name != R1ContextName && name != R1ReleaseName && name != R1FinishName {
		return result, errors.New("nonfixed R1 name")
	}
	if r1Expired(s.clock, deadline) {
		return result, errR1Deadline
	}
	if err := s.Check(); err != nil {
		return result, err
	}
	before, err := fd1LeafStat(int(s.dir.Fd()), name)
	if err != nil {
		if os.IsNotExist(err) {
			return result, errR1Absent
		}
		return result, err
	}
	if !r1Regular(before) {
		return result, errors.New("R1 nonregular control")
	}
	fd, err := syscall.Openat(int(s.dir.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return result, err
	}
	defer func() {
		if err := syscall.Close(fd); err != nil {
			result, retErr = r1File{}, err
		}
	}()
	var opened, after syscall.Stat_t
	if err := syscall.Fstat(fd, &opened); err != nil {
		return result, err
	}
	if !r1Regular(opened) || !r1SameStat(before, opened) {
		return result, errors.New("R1 open drift")
	}
	b, err := r1Read(s.clock, deadline, r1RawRead(fd), R1MaxFrame)
	if err != nil {
		return result, err
	}
	if err := syscall.Fstat(fd, &after); err != nil {
		return result, err
	}
	visible, err := fd1LeafStat(int(s.dir.Fd()), name)
	if err != nil {
		return result, err
	}
	if !r1SameStat(opened, after) || !r1SameStat(after, visible) || int64(len(b)) != after.Size {
		return result, errors.New("R1 read drift")
	}
	if err := s.Check(); err != nil {
		return result, err
	}
	if r1Expired(s.clock, deadline) {
		return result, errR1Deadline
	}
	// Fixed comparable subject, excluding mutable access time.
	token := fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d:%d:%d:%d:%d", after.Dev, after.Ino, after.Mode, after.Uid, after.Gid, after.Nlink, after.Size, after.Mtim.Sec, after.Mtim.Nsec, after.Ctim.Sec, after.Ctim.Nsec)
	return r1File{b, token}, nil
}

type r1Mapping struct {
	data []byte
	page int
}

func r1Allocate() (r1Memory, error) {
	b, err := syscall.Mmap(-1, 0, R1MemoryBytes, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		return nil, err
	}
	return &r1Mapping{b, os.Getpagesize()}, nil
}
func (m *r1Mapping) Size() int     { return len(m.data) }
func (m *r1Mapping) PageSize() int { return m.page }
func (m *r1Mapping) TouchPage(offset int) error {
	if offset < 0 || offset >= len(m.data) || offset%m.page != 0 {
		return errors.New("invalid R1 page")
	}
	m.data[offset] = 0xa5
	return nil
}
func (m *r1Mapping) Release() error {
	err := syscall.Munmap(m.data)
	if err == nil {
		m.data = nil
	}
	return err
}

func r1RawRead(fd int) func([]byte) (int, error) {
	return func(b []byte) (int, error) {
		n, err := syscall.Read(fd, b)
		if n < 0 {
			n = 0
		}
		if n == 0 && err == nil {
			err = io.EOF
		}
		return n, err
	}
}
func r1RawWrite(fd int) func([]byte) (int, error) {
	return func(b []byte) (int, error) {
		n, err := syscall.Write(fd, b)
		if n < 0 {
			n = 0
		}
		return n, err
	}
}

// The actor needs only its own/owned-child local identity. It does not inspect
// cgroups, bind host PIDs, or claim kernel-group membership.
func r1LocalIdentity(pid int) (R1ActorIdentity, error) {
	var id R1ActorIdentity
	if pid < 1 {
		return id, errors.New("invalid R1 pid")
	}
	base := "/proc/" + strconv.Itoa(pid)
	readStart := func() (uint64, error) {
		b, err := kernelRead(base+"/stat", 8192)
		if err != nil {
			return 0, err
		}
		end := strings.LastIndex(string(b), ") ")
		if end < 0 {
			return 0, errors.New("R1 stat")
		}
		f := strings.Fields(string(b)[end+2:])
		if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
			return 0, errors.New("R1 not living")
		}
		return strconv.ParseUint(f[19], 10, 64)
	}
	start, err := readStart()
	if err != nil {
		return id, err
	}
	exe, err := os.Stat(base + "/exe")
	if err != nil {
		return id, err
	}
	ns, err := os.Stat(base + "/ns/pid")
	if err != nil {
		return id, err
	}
	a, b := identity(exe), identity(ns)
	id = R1ActorIdentity{pid, start, R1ObjectIdentity{a.Device, a.Inode}, R1ObjectIdentity{b.Device, b.Inode}}
	end, err := readStart()
	if err != nil || start != end || !id.valid() {
		return R1ActorIdentity{}, errors.New("R1 identity unavailable or drift")
	}
	return id, nil
}

const r1ChildArg = "--private-r1-child"
const r1FinishToken = "ataniq-r1-finish-v1\n"
const r1AbortToken = "ataniq-r1-abort-v1\n"

var errR1Metadata = errors.New("R1 metadata unsupported")

type r1Handshake struct {
	Version  int              `json:"version"`
	Status   string           `json:"status"`
	Identity *R1ActorIdentity `json:"identity,omitempty"`
}

func r1DecodeHandshake(b []byte) (r1Handshake, error) {
	var h r1Handshake
	if err := r1Decode(b, &h); err != nil {
		return h, err
	}
	if h.Version != 1 {
		return h, errors.New("R1 handshake version")
	}
	if h.Status == "unsupported" && h.Identity == nil {
		return h, errR1Metadata
	}
	if h.Status != "observed" || h.Identity == nil || !h.Identity.valid() {
		return h, errors.New("R1 handshake identity")
	}
	return h, nil
}

type r1ExecChild struct {
	clock            r1Clock
	localIdentity    func(int) (R1ActorIdentity, error)
	pid              int
	process          *os.Process
	hello, token     *os.File
	helloFD, tokenFD int
	id               *R1ActorIdentity
	result           R1ChildResult
}

func r1Spawn(c r1Clock) (r1Child, error) {
	return r1SpawnWith(c, func(cmd *exec.Cmd) error { return cmd.Start() })
}

// Tests can substitute only the owned test-binary startup. Production always
// self-execs the fixed private mode, with no command/environment input.
func r1SpawnWith(c r1Clock, start func(*exec.Cmd) error) (r1Child, error) {
	// os.Pipe creates CLOEXEC endpoints. Only these two child endpoints are
	// assigned by os/exec. There is no new host inherited-descriptor profile.
	helloRead, helloWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	tokenRead, tokenWrite, err := os.Pipe()
	if err != nil {
		helloRead.Close()
		helloWrite.Close()
		return nil, err
	}
	cmd := &exec.Cmd{Path: "/proc/self/exe", Args: []string{"probe", r1ChildArg}, Env: []string{}, ExtraFiles: []*os.File{helloWrite, tokenRead}}
	err = start(cmd)
	e1, e2 := helloWrite.Close(), tokenRead.Close()
	if err != nil {
		helloRead.Close()
		tokenWrite.Close()
		return nil, err
	}
	child := &r1ExecChild{clock: c, localIdentity: r1LocalIdentity, pid: cmd.Process.Pid, process: cmd.Process, hello: helloRead, token: tokenWrite, helloFD: int(helloRead.Fd()), tokenFD: int(tokenWrite.Fd()), result: R1ChildResult{Termination: "unknown", Reap: "unknown", Token: "none"}}
	if e1 != nil || e2 != nil {
		return child, errors.New("R1 unused pipe close")
	}
	// Cache raw descriptors before SetNonblock. Calling File.Fd again can
	// restore blocking mode on a Go pollable pipe, even without File.Read.
	if err := syscall.SetNonblock(child.helloFD, true); err != nil {
		return child, err
	}
	if err := syscall.SetNonblock(child.tokenFD, true); err != nil {
		return child, err
	}
	return child, nil
}
func (c *r1ExecChild) Ready(deadline time.Time) (R1ActorIdentity, error) {
	b, err := r1Read(c.clock, deadline, r1RawRead(c.helloFD), R1MaxFrame)
	e := c.hello.Close()
	c.hello = nil
	if err != nil {
		return R1ActorIdentity{}, err
	}
	if e != nil {
		return R1ActorIdentity{}, e
	}
	h, err := r1DecodeHandshake(b)
	if err != nil {
		return R1ActorIdentity{}, err
	}
	actual, err := c.localIdentity(c.pid)
	if err != nil {
		// The handshake alone cannot corroborate a public identity. The exact
		// spawned pid/process and pipes remain owned for bounded abort/reap.
		return R1ActorIdentity{}, errR1Metadata
	}
	if actual != *h.Identity {
		c.id = &actual
		return actual, errors.New("R1 child identity mismatch")
	}
	c.id = &actual
	if r1Expired(c.clock, deadline) {
		return actual, errR1Deadline
	}
	return actual, c.Alive()
}
func (c *r1ExecChild) poll() (bool, error) {
	if c.result.Reap == "reaped" {
		return true, nil
	}
	var st syscall.WaitStatus
	n, err := syscall.Wait4(c.pid, &st, syscall.WNOHANG, nil)
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if n != c.pid {
		return false, errors.New("R1 wait identity")
	}
	code := st.ExitStatus()
	c.result.Identity, c.result.Termination, c.result.Reap, c.result.ExitCode = c.id, "exited", "reaped", &code
	if st.Signaled() {
		signal := int(st.Signal())
		c.result.Termination, c.result.ExitCode, c.result.Signal = "signaled", nil, &signal
	} else if !st.Exited() {
		return true, errors.New("R1 nonterminal wait status")
	}
	if err := c.process.Release(); err != nil {
		return true, err
	}
	return true, nil
}
func (c *r1ExecChild) Alive() error {
	ended, err := c.poll()
	if err != nil {
		return err
	}
	if ended {
		return errors.New("R1 child ended early")
	}
	return nil
}
func (c *r1ExecChild) End(token string, deadline time.Time) (result R1ChildResult, retErr error) {
	defer func() {
		if c.hello != nil {
			if err := c.hello.Close(); err != nil && retErr == nil {
				retErr = err
			}
			c.hello = nil
		}
		if c.token != nil {
			if err := c.token.Close(); err != nil && retErr == nil {
				retErr = err
			}
			c.token = nil
		}
	}()
	if token != "finish" && token != "abort" {
		return c.result, errors.New("R1 child token")
	}
	if c.result.Reap == "reaped" {
		return c.result, errors.New("R1 child already ended")
	}
	// Save a validated identity for cleanup even if readiness could not finish.
	if c.id == nil {
		if id, err := c.localIdentity(c.pid); err == nil {
			c.id = &id
		}
	}
	c.result.Identity = c.id
	text := r1FinishToken
	if token == "abort" {
		text = r1AbortToken
	}
	var writeErr error
	if c.token == nil {
		writeErr = errors.New("R1 token pipe closed")
	} else {
		writeErr = r1Write(c.clock, deadline, r1RawWrite(c.tokenFD), []byte(text))
		// A failed/partial write does not establish delivery of a token.
		if writeErr == nil {
			c.result.Token = token
		}
	}
	for {
		if r1Expired(c.clock, deadline) {
			return c.result, errR1Deadline
		}
		ended, err := c.poll()
		if err != nil {
			return c.result, err
		}
		if ended {
			return c.result, writeErr
		}
		if err := r1Pause(c.clock, deadline); err != nil {
			return c.result, err
		}
	}
}

// runR1Child never reads host control files or allocates pressure memory. EOF
// means parent-lost, and retains the independent lifetime rather than exiting.
func runR1Child(c r1Clock, read func([]byte) (int, error), write func([]byte) (int, error), self func() (R1ActorIdentity, error), closeHello func() error) int {
	deadline := c.Now().Add(R1ChildLifetime)
	id, err := self()
	h := r1Handshake{Version: 1, Status: "observed", Identity: &id}
	if err != nil || !id.valid() {
		h.Status, h.Identity = "unsupported", nil
	}
	b, err := json.Marshal(h)
	if err != nil {
		return 23
	}
	if err := r1Write(c, r1Deadline(c, deadline, R1ReadyWait), write, append(b, '\n')); err != nil {
		_ = closeHello()
		return 23
	}
	if err := closeHello(); err != nil {
		return 23
	}
	if h.Status == "unsupported" {
		return 24
	}
	return r1ChildTokens(c, deadline, read)
}

func r1ChildTokens(c r1Clock, deadline time.Time, read func([]byte) (int, error)) int {
	var frame [64]byte
	off := 0
	lost := false
	for calls := 0; calls < 8192; calls++ {
		if r1Expired(c, deadline) {
			if lost {
				return 22
			}
			return 21
		}
		if lost {
			if r1Pause(c, deadline) != nil {
				return 22
			}
			continue
		}
		n, err := read(frame[off:])
		if n < 0 || n > len(frame)-off {
			return 23
		}
		off += n
		if r1Expired(c, deadline) {
			return 21
		}
		if bytes.Contains(frame[:off], []byte{'\n'}) {
			switch string(frame[:off]) {
			case r1FinishToken:
				return 0
			case r1AbortToken:
				return 20
			default:
				return 23
			}
		}
		if off == len(frame) {
			return 23
		}
		if err == io.EOF {
			lost = true
			continue
		}
		if err != nil && err != syscall.EINTR && err != syscall.EAGAIN && err != syscall.EWOULDBLOCK {
			lost = true
			continue
		}
		if n == 0 || err != nil {
			if r1Pause(c, deadline) != nil {
				return 21
			}
		}
	}
	// Even pathological fast partial reads cannot turn parent loss into an
	// immediate normal exit. Exhaustion retains the independent deadline.
	for !r1Expired(c, deadline) {
		_ = r1Pause(c, deadline)
	}
	return 23
}

// R1Memory is an explicit opt-in entrypoint. HostConfig still rejects R1.
// There are no public control-root, size, lifetime or command selectors.
func R1Memory(in, out *os.File) error {
	c := r1WallClock{}
	start := c.Now()
	inputFD, outputFD := int(in.Fd()), int(out.Fd())
	if err := syscall.SetNonblock(inputFD, true); err != nil {
		return err
	}
	if err := syscall.SetNonblock(outputFD, true); err != nil {
		return err
	}
	return runR1(r1Deps{clock: c, read: r1RawRead(inputFD), write: r1RawWrite(outputFD),
		open: func() (r1Control, error) { return r1OpenControl(R1ControlTarget, c, start.Add(R1MainLifetime)) },
		self: func() (R1ActorIdentity, error) {
			id, err := r1LocalIdentity(os.Getpid())
			if err != nil {
				return id, errR1Metadata
			}
			return id, nil
		},
		spawn: func() (r1Child, error) { return r1Spawn(c) }, allocate: r1Allocate, overall: start.Add(R1MainLifetime)})
}

// R1PrivateChild is only the fixed self-exec endpoint, using child slots 3/4.
func R1PrivateChild() int {
	if len(os.Environ()) != 0 {
		return 23
	}
	hello, token := os.NewFile(3, "r1-private-identity"), os.NewFile(4, "r1-private-token")
	defer token.Close()
	// Reject accidental direct invocation or wrong inherited descriptor types.
	for n := 0; n < 2; n++ {
		fd := n + 3
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFIFO {
			hello.Close()
			return 23
		}
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
		access := syscall.O_WRONLY
		if n == 1 {
			access = syscall.O_RDONLY
		}
		if errno != 0 || int(flags)&syscall.O_ACCMODE != access {
			hello.Close()
			return 23
		}
		if syscall.SetNonblock(fd, true) != nil {
			hello.Close()
			return 23
		}
	}
	return runR1Child(r1WallClock{}, r1RawRead(4), r1RawWrite(3), func() (R1ActorIdentity, error) { return r1LocalIdentity(os.Getpid()) }, hello.Close)
}
