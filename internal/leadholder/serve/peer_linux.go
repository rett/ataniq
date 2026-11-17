//go:build linux

package serve

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/wire"
)

// soPeerPidfd is SO_PEERPIDFD (Linux 6.5), which golang.org/x/sys/unix does
// not name. The number is the same on the architectures this feature targets.
const soPeerPidfd = 77

// peerCred reads SO_PEERCRED of a unix connection.
func peerCred(c *net.UnixConn) (PeerCred, error) {
	var cred *unix.Ucred
	var gerr error
	rc, err := c.SyscallConn()
	if err != nil {
		return PeerCred{}, err
	}
	if err := rc.Control(func(fd uintptr) {
		cred, gerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return PeerCred{}, err
	}
	if gerr != nil {
		return PeerCred{}, gerr
	}
	return PeerCred{UID: cred.Uid, PID: int(cred.Pid)}, nil
}

// peerPID returns the pid of the process at the other end, from the pidfd of
// SO_PEERPIDFD (the descriptor is closed again). A kernel without the option
// is an error, and the peer is refused.
func (s *Server) peerPID(c *net.UnixConn) (int, error) {
	var pidfd int
	var gerr error
	rc, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	if err := rc.Control(func(fd uintptr) {
		pidfd, gerr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, soPeerPidfd)
	}); err != nil {
		return 0, err
	}
	if gerr != nil {
		return 0, fmt.Errorf("SO_PEERPIDFD: %w", gerr)
	}
	defer unix.Close(pidfd)
	return s.cfg.PidfdPID(pidfd)
}

// pidfdPID reads the pid a pidfd names from /proc/self/fdinfo, where it is
// the line "Pid:". A process that is gone shows -1.
func pidfdPID(fd int) (int, error) {
	f, err := os.Open("/proc/self/fdinfo/" + strconv.Itoa(fd))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return 0, err
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte("Pid:")); ok {
			n, err := strconv.Atoi(string(bytes.TrimSpace(rest)))
			if err != nil || n <= 0 {
				return 0, errors.New("the peer process is gone")
			}
			return n, nil
		}
	}
	return 0, errors.New("no Pid line in the pidfd's fdinfo")
}

// daemonPID reads the pid written in the daemon's lock file and reports
// whether the file is locked now: a non-blocking exclusive flock on a fresh
// open of it must fail with "would block", so somebody holds it. The probe
// takes no lock when the file is held. When it is not held, the probe's lock is
// let go at once and the peer is refused.
func (s *Server) daemonPID() (pid int, locked bool, err error) {
	fd, err := unix.Open(s.cfg.DaemonLock, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, false, err
	}
	f := os.NewFile(uintptr(fd), s.cfg.DaemonLock)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return 0, false, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != os.Geteuid() {
		return 0, false, errors.New("the daemon lock is not a regular file of this user")
	}
	b, err := io.ReadAll(io.LimitReader(f, 64))
	if err != nil {
		return 0, false, err
	}
	pid, err = strconv.Atoi(string(bytes.TrimSpace(b)))
	if err != nil || pid <= 0 {
		return 0, false, errors.New("the daemon lock holds no pid")
	}
	probe, err := unix.Open(s.cfg.DaemonLock, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, false, err
	}
	defer unix.Close(probe)
	switch err := unix.Flock(probe, unix.LOCK_EX|unix.LOCK_NB); {
	case err == nil:
		_ = unix.Flock(probe, unix.LOCK_UN)
		return pid, false, nil
	case errors.Is(err, unix.EWOULDBLOCK):
		return pid, true, nil
	default:
		return 0, false, err
	}
}

// checkControlPeer decides whether a control connection comes from the
// daemon: the peer's UID is the holder's own (checked by the caller), and its
// process is the one whose pid the daemon lock file holds while it is locked.
// It returns a short code for the log when the peer is refused.
//
// A process of the same user that writes its own pid into the lock file and
// holds a lock on it passes. That is the limit that the lead-hosting design
// states (section 2.1): this check keeps out processes that did not set that
// up, nothing more.
func (s *Server) checkControlPeer(c *net.UnixConn) (string, bool) {
	pid, err := s.peerPID(c)
	if err != nil {
		return "peer-pid", false
	}
	want, locked, err := s.daemonPID()
	switch {
	case err != nil:
		return "daemon-lock", false
	case pid != want:
		return "not-the-daemon", false
	case !locked:
		return "daemon-lock-not-held", false
	}
	return "", true
}

// sameTicket compares a presented ticket with a registered one in constant
// time.
func sameTicket(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// readCmdline reads /proc/PID/cmdline: the arguments with the NULs made
// spaces, made fit for a frame and the event log, at most [maxCmdline] bytes.
// Anything that goes wrong gives "": the peer may be gone.
func readCmdline(pid int) string {
	if pid <= 0 {
		return ""
	}
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 4*maxCmdline))
	b = bytes.TrimRight(b, "\x00")
	b = bytes.ReplaceAll(b, []byte{0}, []byte{' '})
	return wire.Sanitize(string(b), maxCmdline, false)
}
