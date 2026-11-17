//go:build linux

package ptyhost

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// killWait is how long Stop keeps killing after the grace period before
	// it gives up waiting (a process in uninterruptible sleep can outlast a
	// SIGKILL).
	killWait = 3 * time.Second
	// pollEvery is the deadline-poll step while Stop waits.
	pollEvery = 20 * time.Millisecond
)

// reap waits until the direct child has exited without reaping it, takes the
// session snapshot while its pid is still reserved, then reaps it under the
// mutex that guards signalling.
func (h *Host) reap() {
	defer close(h.done)
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, h.pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			h.finish(Exit{Pid: h.pid, How: "unknown", Code: -1, Err: fmt.Errorf("waitid: %w", err)})
			return
		}
		break
	}
	// The child is a zombie: its pid, and so its session id, cannot be
	// reused yet. An empty session now is authoritative.
	if members, trunc, nerr, err := h.scanSession(); err == nil && !trunc && nerr == 0 && len(members) == 0 {
		h.sessionEmpty.Store(true)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var ws unix.WaitStatus
	for {
		_, err := unix.Wait4(h.pid, &ws, 0, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			h.exit = Exit{Pid: h.pid, How: "unknown", Code: -1, Err: fmt.Errorf("wait4: %w", err)}
			break
		}
		switch {
		case ws.Exited():
			h.exit = Exit{Pid: h.pid, How: "exit", Code: ws.ExitStatus()}
		case ws.Signaled():
			h.exit = Exit{Pid: h.pid, How: "signal", Code: -1, Signal: ws.Signal(), CoreDump: ws.CoreDump()}
		default:
			h.exit = Exit{Pid: h.pid, How: "unknown", Code: -1, Err: fmt.Errorf("unexpected wait status %#x", uint32(ws))}
		}
		break
	}
	h.reaped = true
}

// finish records an exit when the child could not be reaped normally.
func (h *Host) finish(e Exit) {
	h.mu.Lock()
	h.exit = e
	h.reaped = true
	h.mu.Unlock()
}

// Wait blocks until the direct child has been reaped and returns how it
// ended. It may be called any number of times.
func (h *Host) Wait() Exit {
	<-h.done
	return h.exit
}

// Exited is closed once the direct child has exited and been reaped.
func (h *Host) Exited() <-chan struct{} { return h.done }

func (h *Host) hasExited() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// TreeMethod names the method TreeGone uses: "cgroup" when the process runs
// in a control group of its own, else "session", the weaker fallback.
func (h *Host) TreeMethod() string {
	if h.cgroupProcs != "" {
		return "cgroup"
	}
	return "session"
}

// TreeGone reports whether the agent's process tree is gone, and how that
// was decided. The proof string starts with the method, "cgroup" or
// "session", so that a caller can label a session proof as weaker (a
// descendant that started its own session escapes it). A tree is never
// gone while the direct child is not yet reaped. gone false with a proof
// that says why is also the answer when the check itself failed.
func (h *Host) TreeGone() (gone bool, proof string) {
	if !h.hasExited() {
		return false, h.TreeMethod() + ": the direct child has not exited"
	}
	if h.cgroupProcs != "" {
		return h.cgroupGone()
	}
	return h.sessionGone()
}

// scanCgroup walks the holder's group and the groups below it, and counts a
// walk that hit its bound and every group it could not read.
func (h *Host) scanCgroup() cgroupScan {
	s := walkCgroup(filepath.Dir(h.cgroupProcs), h.walkMax, os.Getpid(), nil)
	if s.truncated {
		h.cgroupTruncated.Add(1)
	}
	if s.errs > 0 {
		h.cgroupErrors.Add(uint64(s.errs))
	}
	return s
}

// cgroupGone judges the holder's group and every group below it: the tree is
// gone only when no process but the holder is listed in any of them. A walk
// that hit its bound or could not read a group proves nothing.
func (h *Host) cgroupGone() (bool, string) {
	s := h.scanCgroup()
	switch {
	case len(s.members) > 0:
		m := s.members[0]
		return false, fmt.Sprintf("cgroup %s: process %d besides the holder is still listed in %s", h.cgroupProcs, m.pid, m.procs)
	case s.err != nil:
		return false, fmt.Sprintf("cgroup %s: cannot read the group or a sub-group (%d unreadable): %v", h.cgroupProcs, s.errs, s.err)
	case s.truncated:
		return false, fmt.Sprintf("cgroup %s: the walk of the sub-groups stopped at its limit (%d groups, %d read), nothing proven", h.cgroupProcs, h.walkMax, s.groups)
	case !s.selfSeen:
		return false, fmt.Sprintf("cgroup %s: the holder is not listed in its own group", h.cgroupProcs)
	}
	return true, fmt.Sprintf("cgroup %s: only the holder remains in the group and its %d group(s)", h.cgroupProcs, s.groups)
}

func (h *Host) sessionGone() (bool, string) {
	if h.sessionEmpty.Load() {
		return true, fmt.Sprintf("session %d: no live process in the session (weaker proof: a process that started its own session is not seen; %s)", h.pid, h.cgroupNote)
	}
	members, trunc, nerr, err := h.scanSession()
	switch {
	case len(members) > 0:
		return false, fmt.Sprintf("session %d: %d process(es) remain in the session, first %d", h.pid, len(members), members[0].pid)
	case err != nil:
		return false, fmt.Sprintf("session %d: cannot scan %s: %v", h.pid, h.procRoot, err)
	case trunc:
		return false, fmt.Sprintf("session %d: scan stopped at its limit of %d entries, nothing proven", h.pid, h.scanMax)
	case nerr > 0:
		return false, fmt.Sprintf("session %d: %d entries could not be read, nothing proven", h.pid, nerr)
	}
	h.sessionEmpty.Store(true)
	return true, fmt.Sprintf("session %d: no live process in the session (weaker proof: a process that started its own session is not seen; %s)", h.pid, h.cgroupNote)
}

// Stop ends the child and what it left in its session. It sends SIGTERM to
// the child's process group, waits up to grace for the child to be reaped
// and the tree to be gone, then sends SIGKILL to the group and to every
// process left in the session, repeating for up to killWait (3 s) while
// the tree is not gone. It returns when the tree is gone or that time has
// passed; the caller reads Wait and TreeGone for the facts. A process that
// left the session is out of reach here (TreeGone says so for the session
// method). Stop never signals a pid after the child was reaped, except
// session members it re-verified through a pidfd. It may be called again and
// concurrently.
func (h *Host) Stop(grace time.Duration) {
	h.signalGroup(syscall.SIGTERM)
	if h.waitGone(grace) {
		return
	}
	deadline := time.Now().Add(killWait)
	for {
		h.signalGroup(syscall.SIGKILL)
		h.killSession(syscall.SIGKILL, false)
		step := 5 * pollEvery
		if left := time.Until(deadline); left < step {
			step = left
		}
		if step <= 0 {
			return
		}
		if h.waitGone(step) {
			return
		}
	}
}

// waitGone polls until the child is reaped and the tree is gone, or d has
// passed.
func (h *Host) waitGone(d time.Duration) bool {
	check := func() bool {
		if !h.hasExited() {
			return false
		}
		gone, _ := h.TreeGone()
		return gone
	}
	if check() {
		return true
	}
	if d <= 0 {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	exited := h.done
	for {
		select {
		case <-timer.C:
			return check()
		case <-exited:
			exited = nil // closed for good: look at it once, then poll
			if check() {
				return true
			}
		case <-tick.C:
			if check() {
				return true
			}
		}
	}
}

// signalGroup signals the child's process group. While the leader is not
// reaped its pid is reserved, so the group signal cannot reach an unrelated
// group; under the mutex it cannot be reaped between the check and the
// signal. After the reap the group's members, if any, are reached one by one
// through a pidfd.
func (h *Host) signalGroup(sig syscall.Signal) {
	h.mu.Lock()
	if !h.reaped {
		_ = syscall.Kill(-h.pid, sig)
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	h.killSession(sig, true)
}

// killSession signals the processes of the session (only those in the
// child's process group when groupOnly), each through a pidfd and after
// its session id was read again, so a pid that was reused in between is not
// hit. Once the session was seen empty nothing is signalled.
func (h *Host) killSession(sig syscall.Signal, groupOnly bool) {
	if h.sessionEmpty.Load() {
		return
	}
	members, _, _, err := h.scanSession()
	if err != nil {
		return
	}
	for _, m := range members {
		if groupOnly && m.pgrp != h.pid {
			continue
		}
		h.signalMember(m.pid, sig)
	}
}

func (h *Host) signalMember(pid int, sig syscall.Signal) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	// The pidfd names the process that had this pid when it was opened; the
	// session id read now must still be ours or it is not signalled.
	st, ok := h.readStat(pid)
	if !ok || st.sid != h.pid || st.dead() {
		return
	}
	_ = unix.PidfdSendSignal(fd, sig, nil, 0)
}

// member is one process found in the session.
type member struct{ pid, pgrp int }

type procStat struct {
	state byte
	pgrp  int
	sid   int
}

// dead reports a zombie or a process being removed: it runs no code.
func (s procStat) dead() bool { return s.state == 'Z' || s.state == 'X' || s.state == 'x' }

// parseStat reads state, process group and session from the text of
// /proc/PID/stat. The command name is in parentheses and may itself hold
// spaces and parentheses, so the fields are counted after the last ")".
func parseStat(b []byte) (procStat, bool) {
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return procStat{}, false
	}
	f := strings.Fields(string(b[i+1:]))
	// state ppid pgrp session ...
	if len(f) < 4 || len(f[0]) != 1 {
		return procStat{}, false
	}
	pgrp, err1 := strconv.Atoi(f[2])
	sid, err2 := strconv.Atoi(f[3])
	if err1 != nil || err2 != nil {
		return procStat{}, false
	}
	return procStat{state: f[0][0], pgrp: pgrp, sid: sid}, true
}

func (h *Host) readStat(pid int) (procStat, bool) {
	b, err := readBounded(filepath.Join(h.procRoot, strconv.Itoa(pid), "stat"), maxStatBytes)
	if err != nil {
		return procStat{}, false
	}
	return parseStat(b)
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// scanSession lists the live processes whose session id is the child's pid.
// It reads at most the host's entry limit of the proc directory; truncated
// is true when that limit stopped the scan. nerr counts entries that could
// not be read or parsed (an entry that vanished is not one). Both are
// counted in Counters.
func (h *Host) scanSession() (members []member, truncated bool, nerr int, err error) {
	d, err := os.Open(h.procRoot)
	if err != nil {
		return nil, false, 0, err
	}
	defer d.Close()
	seen := 0
	for {
		names, rerr := d.Readdirnames(256)
		for _, name := range names {
			if seen >= h.scanMax {
				truncated = true
				break
			}
			seen++
			pid, aerr := strconv.Atoi(name)
			if aerr != nil || pid <= 0 {
				continue
			}
			b, ferr := readBounded(filepath.Join(h.procRoot, name, "stat"), maxStatBytes)
			if ferr != nil {
				if errors.Is(ferr, os.ErrNotExist) || errors.Is(ferr, syscall.ESRCH) {
					continue
				}
				nerr++
				continue
			}
			st, ok := parseStat(b)
			if !ok {
				nerr++
				continue
			}
			if st.sid == h.pid && !st.dead() {
				members = append(members, member{pid: pid, pgrp: st.pgrp})
			}
		}
		if truncated {
			break
		}
		if rerr != nil {
			if rerr != io.EOF {
				err = rerr
			}
			break
		}
	}
	if truncated {
		h.scanTruncated.Add(1)
	}
	if nerr > 0 {
		h.scanErrors.Add(uint64(nerr))
	}
	return members, truncated, nerr, err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
