package pidfd

import (
	"os"

	"golang.org/x/sys/unix"
)

// Open opens a pidfd for pid with pidfd_open(2). A pidfd is retained across
// cancellation, while a start time alone is not an exit handle. Open does not
// prove that the process is the one a caller expects: the caller must compare
// its own identity record after Open returns and drop the descriptor on a
// mismatch. It returns the kernel's error, for example ENOSYS or EPERM, on
// kernels or sandboxes that deny pidfd_open; callers treat that as
// unsupported or inconclusive.
func Open(pid int) (*os.File, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "owned-target-pidfd"), nil
}

// Exited reports whether the process behind a pidfd has exited, with a
// non-blocking poll. It reports exit of that one process only, not of its
// children or of its control group, and an exited process may still be a
// zombie its parent has not reaped.
//
// A signal can interrupt the poll (EINTR), which the Go runtime's own
// preemption signals make frequent under the race detector. The poll has a
// zero timeout and cannot block, so Exited repeats it on EINTR and on
// nothing else.
func Exited(f *os.File) (bool, error) {
	return exitedWith(f, unix.Poll)
}

// exitedWith is Exited with the poll call supplied, so a test can make it
// fail with EINTR.
func exitedWith(f *os.File, pollFn func([]unix.PollFd, int) (int, error)) (bool, error) {
	poll := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	for {
		_, err := pollFn(poll, 0)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		return Events(poll[0].Revents)
	}
}

// Events decodes the revents of a poll on a pidfd. POLLIN (the process has
// exited) and POLLHUP (it has exited and been reaped) mean exited; no events
// mean alive; any other bit, such as POLLERR or POLLNVAL, is an error and
// never an answer.
func Events(events int16) (bool, error) {
	const terminal = unix.POLLIN | unix.POLLHUP
	if events & ^int16(terminal) != 0 {
		return false, errUnexpectedPoll
	}
	return events&terminal != 0, nil
}
