// Package hookport serves the holder's per-intent observation socket on Linux.
// Admission checks the connecting process's uid, cgroup and liveness before
// reading. It always refuses a process whose main thread has exited, even
// when other threads remain alive. The port refuses peers with more than
// 4096 threads to bound the task walk per connection. Any admitted peer
// can forge events or pass on its connection.
//
// The holder's seccomp filter denies process_vm_readv and process_vm_writev.
// It cannot filter the pathname /proc/<pid>/mem. The holder can still read
// or write memory through that file where ptrace permissions, including the
// host's Yama policy, allow it. The filter also leaves perf_event_open,
// kcmp and signals to same-uid processes subject to host access controls.
package hookport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	procLimit = 4096
	// The Groups line in status can contain 65,536 decimal gids of up to
	// ten digits plus a separator each. Leave room for the other fields.
	statusLimit = 1 << 20
	// Bound the work one peer can demand on the serial observation port.
	maxPeerThreads = 4096
)

var errPeer = errors.New("hookport: peer refused")
var errLeaderState = fmt.Errorf("%w: leader state", errPeer)

// PeerReader supplies kernel evidence. PIDFD transfers ownership to the caller.
// Proc records are complete or return an error on overflow.
type PeerReader interface {
	Credentials(net.Conn) (*unix.Ucred, error)
	PIDFD(net.Conn) (int, error)
	FDInfo(fd int) ([]byte, error)
	Stat(pid int) ([]byte, error)
	Status(pid int) ([]byte, error)
	Threads(pid int) ([]string, error)
	Cgroup(pid int, tid string) ([]byte, error)
	Alive(fd int) (bool, error)
	CloseFD(fd int) error
}

type KernelPeers struct{}

func socketOption[T any](c net.Conn, read func(int) (T, error)) (v T, err error) {
	u, ok := c.(*net.UnixConn)
	if !ok {
		return v, errPeer
	}
	rc, err := u.SyscallConn()
	if err != nil {
		return v, err
	}
	var opErr error
	if err = rc.Control(func(fd uintptr) { v, opErr = read(int(fd)) }); err != nil {
		return v, err
	}
	return v, opErr
}

func (KernelPeers) Credentials(c net.Conn) (*unix.Ucred, error) {
	return socketOption(c, func(fd int) (*unix.Ucred, error) { return unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED) })
}

func (KernelPeers) PIDFD(c net.Conn) (int, error) {
	return socketOption(c, func(fd int) (int, error) { return unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PEERPIDFD) })
}

func readProc(name string, limit int) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if len(b) > limit {
		return nil, errPeer
	}
	return b, err
}

func (KernelPeers) FDInfo(fd int) ([]byte, error) {
	return readProc("/proc/self/fdinfo/"+strconv.Itoa(fd), procLimit)
}

func (KernelPeers) Stat(pid int) ([]byte, error) {
	return readProc("/proc/"+strconv.Itoa(pid)+"/stat", procLimit)
}

func (KernelPeers) Status(pid int) ([]byte, error) {
	return readProc("/proc/"+strconv.Itoa(pid)+"/status", statusLimit)
}

func (KernelPeers) Threads(pid int) ([]string, error) {
	dir, err := os.Open("/proc/" + strconv.Itoa(pid) + "/task")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	// Threads can be created since the status read. One extra name
	// lets admit detect overflow without reading the rest of the list.
	return dir.Readdirnames(maxPeerThreads + 1)
}

func (KernelPeers) Cgroup(pid int, tid string) ([]byte, error) {
	return readProc("/proc/"+strconv.Itoa(pid)+"/task/"+tid+"/cgroup", procLimit)
}

func (KernelPeers) Alive(fd int) (bool, error) {
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(poll, 0)
	return n == 0 && err == nil, err
}

func (KernelPeers) CloseFD(fd int) error { return unix.Close(fd) }

func validGroup(s string) bool {
	return len(s) <= procLimit && strings.HasPrefix(s, "/") && path.Clean(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}

// ControlGroup extracts the path from exactly one bounded unified-hierarchy record.
func ControlGroup(record []byte) (string, error) {
	line := strings.TrimSuffix(string(record), "\n")
	group, ok := strings.CutPrefix(line, "0::")
	if len(record) > procLimit || !ok || !validGroup(group) {
		return "", errPeer
	}
	return group, nil
}

func withinGroup(ref, group string) bool {
	return validGroup(ref) && ref != "/" && validGroup(group) && (group == ref || strings.HasPrefix(group, ref+"/"))
}

func checkTaskGroup(p PeerReader, pid int, tid, ref string) error {
	record, err := p.Cgroup(pid, tid)
	if err != nil {
		return err
	}
	group, err := ControlGroup(record)
	if err != nil || !withinGroup(ref, group) {
		return errPeer
	}
	return nil
}

func allowedThreadCount(status []byte) bool {
	if len(status) > statusLimit {
		return false
	}
	found := false
	for line := range bytes.SplitSeq(status, []byte{'\n'}) {
		value, ok := bytes.CutPrefix(line, []byte("Threads:"))
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(string(bytes.TrimSpace(value)), 10, 32)
		if found || err != nil || n == 0 || n > maxPeerThreads {
			return false
		}
		found = true
	}
	return found
}

// Field 2 of proc stat is a parenthesized comm which can itself contain
// spaces, newlines and closing parentheses. Field 3 follows the last ')'.
func liveLeader(pid int, stat []byte) bool {
	if len(stat) > procLimit {
		return false
	}
	pidText, rest, ok := strings.Cut(string(stat), " (")
	if !ok || pidText != strconv.Itoa(pid) {
		return false
	}
	end := strings.LastIndexByte(rest, ')')
	if end < 0 {
		return false
	}
	state := rest[end+1:]
	return len(state) >= 3 && state[0] == ' ' && state[2] == ' ' && strings.IndexByte("RSDTtPI", state[1]) >= 0
}

// admit checks the connecting process at admission, before any read. The
// pidfd spans the numeric PID lookup, all task reads and final liveness poll.
// A later migration, inheritance or transfer of the connection is invisible.
func admit(c net.Conn, p PeerReader, ref string) error {
	cred, err := p.Credentials(c)
	if err != nil {
		return err
	}
	if cred == nil || int(cred.Uid) != os.Geteuid() || cred.Pid <= 0 {
		return errPeer
	}
	fd, err := p.PIDFD(c)
	if err != nil {
		return err
	}
	defer p.CloseFD(fd)
	info, err := p.FDInfo(fd)
	if err != nil {
		return err
	}
	if len(info) > procLimit {
		return errPeer
	}
	pid := 0
	for line := range strings.SplitSeq(string(info), "\n") {
		if value, found := strings.CutPrefix(line, "Pid:"); found {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n != int(cred.Pid) || pid != 0 {
				return errPeer
			}
			pid = n
		}
	}
	if pid == 0 {
		return errPeer
	}
	// An exited leader can keep reporting its former cgroup while the
	// remaining threads keep the pidfd alive.
	stat, err := p.Stat(pid)
	if err != nil {
		return err
	}
	if !liveLeader(pid, stat) {
		return errLeaderState
	}
	leader := strconv.Itoa(pid)
	if err := checkTaskGroup(p, pid, leader, ref); err != nil {
		return err
	}
	status, err := p.Status(pid)
	if err != nil {
		return err
	}
	if !allowedThreadCount(status) {
		return errPeer
	}
	threads, err := p.Threads(pid)
	if err != nil {
		return err
	}
	if len(threads) > maxPeerThreads {
		return errPeer
	}
	leaderSeen := false
	for _, tid := range threads {
		if tid == leader {
			leaderSeen = true
			continue
		}
		if err := checkTaskGroup(p, pid, tid, ref); err != nil {
			// The task directory is scoped to this process. A worker
			// can exit between enumeration and opening or reading cgroup.
			if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
				continue
			}
			return err
		}
	}
	if !leaderSeen {
		return errPeer
	}
	alive, err := p.Alive(fd)
	if err != nil {
		return fmt.Errorf("pidfd poll: %w", err)
	}
	if !alive {
		return errPeer
	}
	return nil
}
