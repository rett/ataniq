//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"

	"golang.org/x/sys/unix"
)

// closeRangeCloexec is CLOSE_RANGE_CLOEXEC of close_range(2) (Linux 5.11).
const closeRangeCloexec = 1 << 2

// closeRangeFunc is close_range(2).
type closeRangeFunc func(first, last, flags uint) error

func hostCloseRange(first, last, flags uint) error { return unix.CloseRange(first, last, flags) }

// listFDsFunc lists the open descriptors of the process.
type listFDsFunc func() ([]int, error)

// hostListFDs reads /proc/self/fd. The directory's own descriptor is in the
// list; setting close-on-exec on it, or finding it closed, does no harm.
func hostListFDs() ([]int, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(entries))
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

// setCloexec marks the descriptor close-on-exec. It fails with EBADF when the
// descriptor is not open, which is how the report descriptor is checked.
func setCloexec(fd int) error {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC)
	return err
}

// gaps returns the ranges [first, last] of descriptor numbers from 3 up that
// the kept descriptors leave: a kept number below 3 or twice is ignored.
func gaps(keep []int) [][2]uint {
	ks := make([]int, 0, len(keep))
	for _, k := range keep {
		if k >= minFD {
			ks = append(ks, k)
		}
	}
	sort.Ints(ks)
	var out [][2]uint
	next := uint(minFD)
	for _, k := range ks {
		if uint(k) > next {
			out = append(out, [2]uint{next, uint(k) - 1})
		}
		if uint(k) >= next {
			next = uint(k) + 1
		}
	}
	return append(out, [2]uint{next, math.MaxUint32})
}

// closeOnExecAbove2 makes every descriptor above 2 close-on-exec, except the
// kept ones, so that the execve that follows closes them. It does not close
// them itself: the Go runtime may hold descriptors of its own (the poller's)
// that it would still use between here and the execve, and closing one would
// make it throw. The effect for the program that is executed is the same.
//
// It asks close_range(2) with CLOSE_RANGE_CLOEXEC for each range between the
// kept descriptors. When the kernel refuses (before Linux 5.11, or a filter
// that does not know the call) it lists /proc/self/fd and marks each
// descriptor; when that fails too the error is returned and nothing may be
// executed.
func closeOnExecAbove2(keep []int, closeRange closeRangeFunc, list listFDsFunc, mark func(int) error) error {
	var first error
	for _, r := range gaps(keep) {
		if err := closeRange(r[0], r[1], closeRangeCloexec); err != nil {
			first = err
			break
		}
	}
	if first == nil {
		return nil
	}
	fds, err := list()
	if err != nil {
		return fmt.Errorf("close_range: %v, and the descriptors cannot be listed: %v", first, err)
	}
	kept := map[int]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	for _, fd := range fds {
		if fd < minFD || kept[fd] {
			continue
		}
		if err := mark(fd); err != nil && !errors.Is(err, unix.EBADF) {
			return fmt.Errorf("descriptor %d: %v", fd, err)
		}
	}
	return nil
}

// hostCloseOnExecAbove2 is closeOnExecAbove2 with the host's calls.
func hostCloseOnExecAbove2(keep []int) error {
	return closeOnExecAbove2(keep, hostCloseRange, hostListFDs, setCloexec)
}

// fdReader reads a descriptor with plain read(2) calls, so that no os.File
// (and no poller) is involved.
type fdReader struct{ fd int }

func (r fdReader) Read(p []byte) (int, error) {
	for {
		n, err := unix.Read(r.fd, p)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
}

// writeAll writes all of b with write calls, retrying a call that the kernel
// interrupted and continuing after a partial write; a call that fails, or one
// that writes nothing, is an error.
func writeAll(write func([]byte) (int, error), b []byte) error {
	for len(b) > 0 {
		n, err := write(b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// openRetry is open(2) retried when it is interrupted.
func openRetry(path string, flags int) (int, error) {
	for {
		fd, err := unix.Open(path, flags, 0)
		if err != unix.EINTR {
			return fd, err
		}
	}
}
