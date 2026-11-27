// Package hookport serves the holder's per-intent observation socket on Linux.
// Admission checks the connecting process's uid, cgroup and liveness before
// reading. Any admitted peer can forge events or pass on its connection.
package hookport

import (
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

const procLimit = 4096

var errPeer = errors.New("hookport: peer refused")

// PeerReader supplies kernel evidence. PIDFD transfers ownership to the caller.
// FDInfo and Cgroup return a complete proc record or an error on overflow.
type PeerReader interface {
	Credentials(net.Conn) (*unix.Ucred, error)
	PIDFD(net.Conn) (int, error)
	FDInfo(fd int) ([]byte, error)
	Cgroup(pid int) ([]byte, error)
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

func readProc(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, procLimit+1))
	if len(b) > procLimit {
		return nil, errPeer
	}
	return b, err
}

func (KernelPeers) FDInfo(fd int) ([]byte, error) {
	return readProc("/proc/self/fdinfo/" + strconv.Itoa(fd))
}

func (KernelPeers) Cgroup(pid int) ([]byte, error) {
	return readProc("/proc/" + strconv.Itoa(pid) + "/cgroup")
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

// admit checks the connecting process at admission, before any read. The
// pidfd spans the numeric PID lookup, cgroup read and final liveness poll.
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
	record, err := p.Cgroup(pid)
	if err != nil {
		return err
	}
	group, err := ControlGroup(record)
	if err != nil || !withinGroup(ref, group) {
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
