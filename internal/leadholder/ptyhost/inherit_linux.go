//go:build linux

package ptyhost

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// CloseOnExecFrom marks every open descriptor numbered fd or above
// close-on-exec, so that no descriptor the process holds from before reaches a
// child it starts. The holder calls it once at start, with 3, before the first
// Start. It uses close_range(2) with CLOSE_RANGE_CLOEXEC (Linux 5.11), and
// falls back to marking each descriptor listed in /proc/self/fd (a descriptor
// the process opens later without close-on-exec, from another goroutine, is not
// covered). It returns an error when neither can be done: the holder should
// then refuse to start an agent. See "What the child inherits" in the package
// comment.
func CloseOnExecFrom(fd int) error {
	if fd < 0 {
		return fmt.Errorf("ptyhost: CloseOnExecFrom(%d): negative descriptor", fd)
	}
	err := unix.CloseRange(uint(fd), ^uint(0), unix.CLOSE_RANGE_CLOEXEC)
	if err == nil {
		return nil
	}
	// ENOSYS: before 5.9. EINVAL: before 5.11, the flag is unknown. EPERM:
	// a filter forbids the call.
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("ptyhost: close_range: %w", err)
	}
	if perr := closeOnExecFromProc(fd, "/proc"); perr != nil {
		return fmt.Errorf("ptyhost: close_range: %v; fallback: %w", err, perr)
	}
	return nil
}

// closeOnExecFromProc marks the descriptors listed in procRoot/self/fd that
// are fd or above close-on-exec, one by one.
func closeOnExecFromProc(fd int, procRoot string) error {
	d, err := os.Open(procRoot + "/self/fd")
	if err != nil {
		return err
	}
	var nums []int
	for {
		names, rerr := d.Readdirnames(256)
		for _, name := range names {
			if n, aerr := strconv.Atoi(name); aerr == nil && n >= fd {
				nums = append(nums, n)
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				d.Close()
				return rerr
			}
			break
		}
	}
	d.Close()
	for _, n := range nums {
		if _, err := unix.FcntlInt(uintptr(n), unix.F_SETFD, unix.FD_CLOEXEC); err != nil && !errors.Is(err, unix.EBADF) {
			return fmt.Errorf("descriptor %d: %w", n, err)
		}
	}
	return nil
}

// IgnoredSignals lists the signals this process ignores (the SigIgn mask in
// /proc/self/status), in ascending order. A child inherits them: see "What the
// child inherits" in the package comment. The Go runtime's own handlers are not
// ignores and are not listed.
func IgnoredSignals() ([]syscall.Signal, error) {
	return ignoredSignals("/proc")
}

func ignoredSignals(procRoot string) ([]syscall.Signal, error) {
	b, err := readBounded(procRoot+"/self/status", 64<<10)
	if err != nil {
		return nil, fmt.Errorf("ptyhost: ignored signals: %w", err)
	}
	return parseSigIgn(b)
}

// parseSigIgn reads the SigIgn line of a status file.
func parseSigIgn(status []byte) ([]syscall.Signal, error) {
	sc := bufio.NewScanner(bytes.NewReader(status))
	for sc.Scan() {
		v, ok := strings.CutPrefix(sc.Text(), "SigIgn:")
		if !ok {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("ptyhost: ignored signals: bad SigIgn line %q", truncate(sc.Text(), 64))
		}
		var sigs []syscall.Signal
		for n := 1; n <= 64; n++ {
			if mask&(1<<uint(n-1)) != 0 {
				sigs = append(sigs, syscall.Signal(n))
			}
		}
		return sigs, nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("ptyhost: ignored signals: no SigIgn line in the status file")
}
