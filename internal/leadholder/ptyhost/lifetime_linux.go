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

// Wait blocks until the direct child has exited and returns how it ended. It
// may be called any number of times. The child is not reaped at that moment:
// its zombie keeps the pid, the process group id and the session id reserved
// until the session is proven empty or the host is closed (see the package
// comment), so Wait does not mean the tree is gone.
func (h *Host) Wait() Exit {
	<-h.done
	return h.exit
}

// Exited is closed once the direct child has exited and its exit facts are
// known (it may still be a zombie, see Wait).
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
// descendant that started its own session escapes it). With the cgroup
// method the control group walk and the session scan must both be empty. A tree is never
// gone while the direct child is not yet reaped. gone false with a proof
// that says why is also the answer when the check itself failed.
//
// Once the direct child's zombie was let go before the session was proven
// empty (Close came first, or the wait for the child failed), the session id
// is no longer reserved, and neither method proves the tree gone any more:
// with the cgroup method too, because it needs the session scan.
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
// walk that hit its bound and every group it could not read. The caller holds
// h.mu. The direct child's zombie, if it is not reaped yet, is not a process
// of the tree: its pid is reserved, so a listing of that number can only be
// the zombie itself (a zombie normally is not listed at all).
func (h *Host) scanCgroup() cgroupScan {
	ignore := func(pid int) bool { return !h.reaped && pid == h.pid }
	s := walkCgroup(filepath.Dir(h.cgroupProcs), h.walkMax, os.Getpid(), ignore)
	if s.truncated {
		h.cgroupTruncated.Add(1)
	}
	if s.errs > 0 {
		h.cgroupErrors.Add(uint64(s.errs))
	}
	return s
}

// cgroupGone judges the holder's group and every group below it, and the
// session: the tree is gone only when no process but the holder is listed in
// any of the groups and no live process is left in the child's session. The
// session scan covers a process that left the groups during the walk, which
// the walk cannot see (a delegated group lets a process move; the scan does
// not catch one that also started a session of its own). A walk that hit its
// bound or could not read a group proves nothing. A tree that is gone lets
// the zombie of the direct child go.
func (h *Host) cgroupGone() (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
	if ok, why := h.sessionEmptyLocked(); !ok {
		return false, fmt.Sprintf("cgroup %s: the group and its %d group(s) hold only the holder, but %s", h.cgroupProcs, s.groups, why)
	}
	return true, fmt.Sprintf("cgroup %s: only the holder remains in the group and its %d group(s), and session %d holds no live process", h.cgroupProcs, s.groups, h.pid)
}

// sessionEmptyLocked scans the child's session; the caller holds h.mu, which
// the reap also takes, so the zombie that reserves the session id cannot be
// reaped between the scan and the verdict. When the session is empty it is
// latched and the zombie is let go. Once the child was reaped without the
// session having been proven empty (the host was closed first, or the child
// could not be waited for), the id is no longer reserved: a process that
// happens to hold that number as its session id is not a member, and nothing
// is proven. why says what is wrong when ok is false.
func (h *Host) sessionEmptyLocked() (ok bool, why string) {
	if h.sessionEmpty.Load() {
		return true, ""
	}
	if h.reaped {
		return false, fmt.Sprintf("session %d: the child was reaped before the session was proven empty, so the id is no longer reserved: nothing proven", h.pid)
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
	h.latchEmpty()
	return true, ""
}

// sessionGone is the session proof (see sessionEmptyLocked).
func (h *Host) sessionGone() (bool, string) {
	const weaker = "weaker proof: a process that started its own session is not seen"
	if h.sessionEmpty.Load() {
		return true, fmt.Sprintf("session %d: no live process in the session (%s; %s)", h.pid, weaker, h.cgroupNote)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if ok, why := h.sessionEmptyLocked(); !ok {
		return false, why
	}
	return true, fmt.Sprintf("session %d: no live process in the session (%s; %s)", h.pid, weaker, h.cgroupNote)
}

// Stop ends the child and what it left in its session. It sends SIGTERM to
// the child's process group, waits up to grace for the child to have exited
// and the tree to be gone, then sends SIGKILL to the group and to every
// process left in the session, repeating for up to killWait (3 s) while
// the tree is not gone. It returns when the tree is gone or that time has
// passed; the caller reads Wait and TreeGone for the facts. A process that
// left the session is out of reach here (TreeGone says so for the session
// method). With the cgroup method Stop also signals every process listed in
// the holder's group and the groups below it, except the holder, through
// pidfds after checking the membership again (see signalCgroup), so a process
// that left the session and the process group is reached too. Stop signals by
// group or session id only while the direct child's
// zombie still reserves that id, and session members through a pidfd after
// their session id was read again. It may be called again and concurrently.
func (h *Host) Stop(grace time.Duration) {
	h.signalGroup(syscall.SIGTERM)
	h.signalCgroup(syscall.SIGTERM)
	if h.waitGone(grace) {
		return
	}
	deadline := time.Now().Add(h.killFor)
	for {
		h.signalGroup(syscall.SIGKILL)
		h.killSession(syscall.SIGKILL)
		h.signalCgroup(syscall.SIGKILL)
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

// signalGroup signals the child's process group. The leader's pid is also the
// group id, and it is reserved while the leader is not reaped (a zombie
// holds it), so the signal cannot reach an unrelated group; under the mutex
// the leader cannot be reaped between the check and the signal. After the reap
// the id is not reserved and nothing is signalled by it: by then the session
// was proven empty, or the host was closed and has given up acting on it.
func (h *Host) signalGroup(sig syscall.Signal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.reaped {
		_ = syscall.Kill(-h.pid, sig)
	}
}

// killSession signals the live processes of the session, each through a
// pidfd and after its session id was read again. It does this only while the
// leader is not reaped, so the session id is reserved and a process that
// reports it really is in the session; once the session was seen empty, or
// the leader was reaped, nothing is signalled by that id.
func (h *Host) killSession(sig syscall.Signal) {
	if h.sessionEmpty.Load() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reaped {
		return
	}
	members, _, _, err := h.scanSession()
	if err != nil {
		return
	}
	for _, m := range members {
		h.signalMember(m.pid, sig)
	}
}

// signalMember signals one process of the session through a pidfd. The caller
// holds h.mu with the leader unreaped.
func (h *Host) signalMember(pid int, sig syscall.Signal) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		h.pidfdFailed(err)
		return
	}
	defer unix.Close(fd)
	// The pidfd names the process that had this pid when it was opened; the
	// session id read now must still be ours or it is not signalled.
	st, ok := h.readStat(pid)
	if !ok || st.sid != h.pid || st.dead() {
		return
	}
	if err := unix.PidfdSendSignal(fd, sig, nil, 0); err != nil {
		h.pidfdFailed(err)
	}
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
