//go:build linux

package ptyhost

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	// DefaultCgroupWalkLimit bounds how many control groups one walk of the
	// holder's group and its descendants reads when Config.CgroupWalkLimit is
	// zero.
	DefaultCgroupWalkLimit = 4096

	// maxWalkPids bounds how many process ids one walk keeps.
	maxWalkPids = 1 << 16

	// signalBatch is how many pidfds are open at once while the processes of
	// the group are signalled.
	signalBatch = 128
)

// privateCgroup decides, before the child exists, whether this process runs
// in a cgroup v2 group of its own: the group named in procRoot/self/cgroup
// lists only self in its cgroup.procs, and has no child group (a process in a
// child group is not listed in the parent's cgroup.procs, so a group that
// already has children is not judged by that file alone). It returns the
// cgroup.procs path, or an empty path and the reason the group is not used.
func privateCgroup(procRoot, cgroupRoot string, self, walkLimit int) (procs, note string) {
	b, err := readBounded(filepath.Join(procRoot, "self", "cgroup"), maxCgroupBytes)
	if err != nil {
		return "", "no control group information: " + err.Error()
	}
	path := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "0::"); ok {
			path = rest
			break
		}
	}
	if path == "" {
		return "", "no cgroup v2 entry (0::) in the control group file"
	}
	if strings.HasSuffix(path, " (deleted)") || !strings.HasPrefix(path, "/") {
		return "", "unusable control group path"
	}
	procs = filepath.Join(cgroupRoot, filepath.Clean(path), "cgroup.procs")
	pids, trunc, err := readProcs(procs)
	switch {
	case err != nil:
		return "", "control group not readable: " + err.Error()
	case trunc:
		return "", "control group lists too many processes"
	}
	selfSeen := false
	for _, p := range pids {
		if p != self {
			return "", "control group is shared with other processes"
		}
		selfSeen = true
	}
	if !selfSeen {
		return "", "holder not listed in its control group"
	}
	has, err := hasChildGroup(filepath.Dir(procs), walkLimit)
	switch {
	case err != nil:
		return "", "control group directory not readable: " + err.Error()
	case has:
		return "", "control group already has child groups"
	}
	return procs, ""
}

// readProcs reads a cgroup.procs file, bounded. truncated is true when the
// file is longer than the bound (the pids read are then incomplete).
func readProcs(path string) (pids []int, truncated bool, err error) {
	b, err := readBounded(path, maxProcsBytes+1)
	if err != nil {
		return nil, false, err
	}
	if len(b) > maxProcsBytes {
		return nil, true, nil
	}
	for _, line := range strings.Fields(string(b)) {
		pid, perr := strconv.Atoi(line)
		if perr != nil || pid <= 0 {
			return nil, false, fmt.Errorf("unexpected line in cgroup.procs: %q", truncate(line, 32))
		}
		pids = append(pids, pid)
	}
	return pids, false, nil
}

// childGroups lists the sub-directories of a group directory (its child
// groups), at most limit directory entries read in all. more is true when the
// limit stopped the listing. Symbolic links are not followed.
func childGroups(dir string, limit int) (names []string, more bool, err error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, false, err
	}
	defer d.Close()
	seen := 0
	for {
		es, rerr := d.ReadDir(256)
		for _, e := range es {
			if seen >= limit {
				return names, true, nil
			}
			seen++
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return names, false, nil
			}
			return names, false, rerr
		}
	}
}

// hasChildGroup reports whether the group directory has a child group. A
// directory too large to list within limit entries is an error: nothing is
// proven.
func hasChildGroup(dir string, limit int) (bool, error) {
	names, more, err := childGroups(dir, limit)
	switch {
	case len(names) > 0:
		return true, nil
	case err != nil:
		return false, err
	case more:
		return false, fmt.Errorf("more than %d entries", limit)
	}
	return false, nil
}

// cgroupMember is a process listed in the holder's group or one below it, and
// the cgroup.procs file that lists it.
type cgroupMember struct {
	pid   int
	procs string
}

// cgroupScan is the result of one walk of the holder's group and every group
// below it.
type cgroupScan struct {
	// members are the processes listed other than those the caller ignored.
	members []cgroupMember
	// selfSeen is true when the holder is listed in the top group.
	selfSeen bool
	// groups is how many groups were read.
	groups int
	// truncated is true when a bound stopped the walk (the group limit or the
	// process limit): what was not read is unknown.
	truncated bool
	// errs counts the groups that could not be read, and err is the first
	// such error. A group that vanished during the walk is not an error: it
	// was empty, because a group is removed only when it is.
	errs int
	err  error
}

// complete reports whether every group was read: the lists in members are
// then the whole truth at the time of the walk.
func (s cgroupScan) complete() bool { return !s.truncated && s.errs == 0 }

// walkCgroup reads the cgroup.procs of the group in dir and of every group
// below it, breadth first, at most limit groups. Processes for which ignore
// returns true are not listed in members; self is the holder's own pid, whose
// presence in the top group is reported as selfSeen.
func walkCgroup(dir string, limit, self int, ignore func(pid int) bool) cgroupScan {
	var s cgroupScan
	queue := []string{dir}
	discovered := 1 // groups queued so far, including the top one
	pids := 0
	fail := func(err error) {
		s.errs++
		if s.err == nil {
			s.err = err
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		s.groups++
		procs := filepath.Join(cur, "cgroup.procs")
		list, trunc, err := readProcs(procs)
		switch {
		case err != nil && cur != dir && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENODEV)):
			continue // removed while we walked
		case err != nil:
			fail(err)
			continue
		case trunc:
			s.truncated = true
			continue
		}
		for _, p := range list {
			if cur == dir && p == self {
				s.selfSeen = true
				continue
			}
			if p == self || (ignore != nil && ignore(p)) {
				continue
			}
			if pids++; pids > maxWalkPids {
				s.truncated = true
				break
			}
			s.members = append(s.members, cgroupMember{pid: p, procs: procs})
		}
		names, more, err := childGroups(cur, limit+256)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			fail(err)
		}
		if more {
			s.truncated = true
		}
		for _, n := range names {
			if discovered >= limit {
				s.truncated = true
				break
			}
			discovered++
			queue = append(queue, filepath.Join(cur, n))
		}
	}
	return s
}

// signalCgroup signals every process listed in the holder's control group and
// the groups below it, except the holder: the processes that left the child's
// session and process group but stayed in the unit. cgroup.kill cannot be
// used, because the holder is in the same group. It does nothing when the
// cgroup method is not in use.
//
// A pid read from a cgroup.procs may have been reused by the time it is
// signalled. So each process is signalled through a pidfd opened first, and
// only if the pid is listed again in the same cgroup.procs after the pidfd is
// open: the pidfd names whichever process held the number when it was opened,
// and that process is in the group if the number is still listed (a process
// that took the number and is not in the group is not listed, and is skipped).
// The processes are handled in batches, so the number of open descriptors is
// bounded. A walk that could not be completed signals what it found; Stop
// tries again.
func (h *Host) signalCgroup(sig syscall.Signal) {
	if h.cgroupProcs == "" {
		return
	}
	h.mu.Lock()
	s := h.scanCgroup()
	h.mu.Unlock()
	for members := s.members; len(members) > 0; {
		n := min(len(members), signalBatch)
		h.signalListed(members[:n], sig, relistProcs)
		members = members[n:]
	}
}

// relistProcs reads a cgroup.procs file again as a set; nil when it cannot be
// read (a group that is gone lists nothing).
func relistProcs(procs string) map[int]bool {
	pids, truncated, err := readProcs(procs)
	if err != nil || truncated {
		return nil
	}
	set := make(map[int]bool, len(pids))
	for _, p := range pids {
		set[p] = true
	}
	return set
}

// signalListed signals the members through pidfds opened now, but only those
// that relist (called once per cgroup.procs file, after every pidfd is open)
// still shows in the file they came from. Failures other than a process that
// is already gone are counted.
func (h *Host) signalListed(members []cgroupMember, sig syscall.Signal, relist func(procs string) map[int]bool) {
	type opened struct {
		m  cgroupMember
		fd int
	}
	var fds []opened
	defer func() {
		for _, o := range fds {
			_ = unix.Close(o.fd)
		}
	}()
	for _, m := range members {
		fd, err := unix.PidfdOpen(m.pid, 0)
		if err != nil {
			h.pidfdFailed(err)
			continue
		}
		fds = append(fds, opened{m, fd})
	}
	lists := map[string]map[int]bool{}
	for _, o := range fds {
		set, seen := lists[o.m.procs]
		if !seen {
			set = relist(o.m.procs)
			lists[o.m.procs] = set
		}
		if set == nil {
			h.cgroupErrors.Add(1) // cannot confirm the membership: not signalled
			continue
		}
		if !set[o.m.pid] {
			continue
		}
		if err := unix.PidfdSendSignal(o.fd, sig, nil, 0); err != nil {
			h.pidfdFailed(err)
		}
	}
}

// pidfdFailed counts a pidfd_open or pidfd_send_signal that failed. ESRCH
// means the process is gone, which is what the caller wanted, and is not a
// failure; anything else (ENOSYS before Linux 5.3, EPERM, EMFILE) means a
// process that should have been signalled may not have been.
func (h *Host) pidfdFailed(err error) {
	if err != unix.ESRCH {
		h.pidfdFailures.Add(1)
	}
}
