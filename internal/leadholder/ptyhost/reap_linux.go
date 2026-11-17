//go:build linux

package ptyhost

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The si_code values of a SIGCHLD siginfo that say how a child ended.
const (
	cldExited = 1 // the child called exit; si_status is the status
	cldKilled = 2 // killed by a signal; si_status is the signal
	cldDumped = 3 // killed by a signal and dumped core
)

// reap follows the direct child through its end, in three steps.
//
//  1. It waits with waitid(WEXITED|WNOWAIT): the child has exited, and stays
//     a zombie. The zombie holds the child's pid, so the kernel cannot give
//     that number, which is also the child's process group id and its session
//     id, to another process.
//  2. It takes the exit facts from the siginfo, scans the session once, and
//     closes done, so that Wait returns as soon as the child has exited. The
//     scan is authoritative: with the leader unreaped no process can join
//     the session, so an empty session stays empty.
//  3. It waits for release, which is the proof that the session is empty (or
//     the end of the host, Close), and only then reaps the zombie, under the
//     mutex that every signal by session or group id holds.
func (h *Host) reap() {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, h.pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			// Nothing to hold: the child is gone or was never ours to wait for,
			// so no id is reserved. The tree proofs by session then prove
			// nothing (see sessionGone).
			h.mu.Lock()
			h.exit = Exit{Pid: h.pid, How: "unknown", Code: -1, Err: fmt.Errorf("waitid: %w", err)}
			h.reaped = true
			h.mu.Unlock()
			close(h.done)
			return
		}
		break
	}
	h.exit = exitFromSiginfo(h.pid, &info)
	if members, trunc, nerr, err := h.scanSession(); err == nil && !trunc && nerr == 0 && len(members) == 0 {
		h.latchEmpty()
	}
	close(h.done)

	<-h.release
	h.mu.Lock()
	defer h.mu.Unlock()
	var ws unix.WaitStatus
	for {
		_, err := unix.Wait4(h.pid, &ws, 0, nil)
		if err == unix.EINTR {
			continue
		}
		break // the facts were taken from the siginfo; an error here (ECHILD) leaves nothing to reap
	}
	h.reaped = true
}

// letGo allows the reap of the zombie. It is called when the session is
// proven empty (also as part of the control group proof, which needs the
// session empty too), and by Close.
func (h *Host) letGo() { h.releaseOnce.Do(func() { close(h.release) }) }

// latchEmpty records that the session has no live member and lets the zombie
// go. The fact is never undone.
func (h *Host) latchEmpty() {
	h.sessionEmpty.Store(true)
	h.letGo()
}

// isReaped reports whether the child was reaped (or never could be held): from
// then on its pid, and the session and group ids that are the same number, are
// no longer reserved.
func (h *Host) isReaped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reaped
}

// exitFromSiginfo decodes the siginfo that waitid filled in for the child pid.
// unix.Siginfo does not export the SIGCHLD fields (pid, uid, status), so they
// are read from the structure's bytes at the offsets the kernel's ABI gives:
// the union that holds them starts after the three leading ints, padded to
// the pointer size. Anything that does not look like the child's own SIGCHLD
// report is an unknown exit.
func exitFromSiginfo(pid int, info *unix.Siginfo) Exit {
	b := unsafe.Slice((*byte)(unsafe.Pointer(info)), unsafe.Sizeof(*info))
	off := 16
	if unsafe.Sizeof(uintptr(0)) == 4 {
		off = 12
	}
	if len(b) < off+12 {
		return Exit{Pid: pid, How: "unknown", Code: -1, Err: fmt.Errorf("waitid: siginfo of %d bytes", len(b))}
	}
	gotPid := int(int32(binary.NativeEndian.Uint32(b[off:])))
	status := int(int32(binary.NativeEndian.Uint32(b[off+8:])))
	if info.Signo != int32(unix.SIGCHLD) || gotPid != pid {
		return Exit{Pid: pid, How: "unknown", Code: -1, Err: fmt.Errorf("waitid: siginfo for signal %d, pid %d, want SIGCHLD for %d", info.Signo, gotPid, pid)}
	}
	switch info.Code {
	case cldExited:
		return Exit{Pid: pid, How: "exit", Code: status}
	case cldKilled:
		return Exit{Pid: pid, How: "signal", Code: -1, Signal: syscall.Signal(status)}
	case cldDumped:
		return Exit{Pid: pid, How: "signal", Code: -1, Signal: syscall.Signal(status), CoreDump: true}
	}
	return Exit{Pid: pid, How: "unknown", Code: -1, Err: fmt.Errorf("waitid: unexpected si_code %d", info.Code)}
}
