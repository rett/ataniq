package noaicheck

import (
	"errors"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"syscall"
)

// commLen is the longest process name the kernel keeps in comm (its
// TASK_COMM_LEN less the terminating byte).
const commLen = 15

// Incomplete hit names of the process check: no usable process list
// ("HIT incomplete proc <why>"), processes whose identity could not be
// read for lack of permission ("HIT incomplete proc-access <count>"), and
// processes whose folder stays but which give no name at all
// ("HIT incomplete proc-identity <count>").
const (
	PathProc         = "proc"
	PathProcAccess   = "proc-access"
	PathProcIdentity = "proc-identity"
)

// DetailNoIdentity is the detail of "HIT incomplete proc no-identity":
// proc/ lists processes but the name and executable of none of them could
// be read, so the view of the processes is not usable.
const DetailNoIdentity = "no-identity"

// procState is what reading one process gave.
type procState int

const (
	procRead    procState = iota // read, or a kernel thread (comm, no exe)
	procGone                     // it ended while it was read: its folder is gone
	procDenied                   // comm or exe refused for lack of permission, twice
	procUnnamed                  // its folder stays, and neither comm nor exe gives a name
)

// CheckProcesses is check 5: a running process whose name (proc/PID/comm,
// which the kernel cuts to 15 bytes) or whose executable's base name (the
// target of proc/PID/exe) is an executable name of ai-names.list is a hit
// of kind "process", with the matched name as its detail.
//
// The check fails closed: a missing or unreadable proc/, or one without a
// single process, is "incomplete proc", and so is a proc/ that lists
// processes of which not one name or executable could be read
// ("incomplete proc no-identity"). Only a name that is not empty counts as
// an identity. A process that ends while it is read (its folder is gone)
// is passed over, and so is a kernel thread (its comm is read, it has no
// exe). A process whose folder stays but which gives no name (comm missing
// or empty, and no exe) is not passed over: the run ends with one
// "incomplete proc-identity N", N the number of such processes, so a
// readable process cannot hide others the check cannot see. A process
// whose comm or exe is refused for lack of permission, twice, is not
// passed over either: run as root (the host's case) each is a hit of kind
// "unreadable"; run as another user the run ends with one
// "incomplete proc-access N", N the number of such processes.
func (c *Checker) CheckProcesses() {
	pids, denied, unnamed, seen := 0, 0, 0, 0
	_, _, err := c.t.walk("proc", 1, func(e *wentry) bool {
		if isPID(e.name) {
			pids++
			st, identified := c.checkProcess(e.rel)
			switch st {
			case procDenied:
				denied++
			case procUnnamed:
				unnamed++
			}
			if identified {
				seen++
			}
		}
		return false
	}, c.unreadable)
	switch {
	case errors.Is(err, errBudget):
		return
	case errors.Is(err, fs.ErrNotExist):
		c.hit(KindIncomplete, PathProc, "missing")
	case err != nil:
		c.hit(KindIncomplete, PathProc, describe(err))
	case pids == 0:
		c.hit(KindIncomplete, PathProc, "no processes")
	case seen == 0:
		c.hit(KindIncomplete, PathProc, DetailNoIdentity)
	}
	if unnamed > 0 {
		c.hit(KindIncomplete, PathProcIdentity, strconv.Itoa(unnamed))
	}
	if denied > 0 && c.euid != 0 {
		c.hit(KindIncomplete, PathProcAccess, strconv.Itoa(denied))
	}
}

// checkProcess checks the process whose folder is dir, and says whether a
// name that is not empty was read from its comm or exe. A comm or exe that
// is not there means the process ended only when its folder is gone too;
// while the folder stays it is missing evidence.
func (c *Checker) checkProcess(dir string) (procState, bool) {
	var names []string
	gone := func() bool {
		_, _, err := c.t.lstat(dir)
		return vanished(err)
	}
	reported := false
	comm := path.Join(dir, "comm")
	data, err := retryDenied(func() ([]byte, error) {
		_, data, err := c.t.readFile(comm, 4096)
		return data, err
	})
	switch {
	case err == nil:
		if n := strings.TrimSuffix(string(data), "\n"); n != "" {
			names = append(names, n)
		}
	case vanished(err):
		if gone() {
			return procGone, false
		}
	case denied(err):
		return c.deniedProcess(comm, err), false
	default:
		c.unreadable(comm, err)
		reported = true
	}
	exe := path.Join(dir, "exe")
	target, err := retryDenied(func() ([]byte, error) {
		t, err := c.t.readlink(exe)
		return []byte(t), err
	})
	switch {
	case err == nil:
		if t := strings.TrimSuffix(string(target), " (deleted)"); t != "" {
			names = append(names, path.Base(t))
		}
	case errors.Is(err, fs.ErrNotExist) && len(names) > 0:
		// A kernel thread (or a zombie): comm and no executable.
	case vanished(err):
		if gone() {
			return procGone, len(names) > 0
		}
	case denied(err):
		return c.deniedProcess(exe, err), len(names) > 0
	default:
		c.unreadable(exe, err)
		reported = true
	}
	if m := c.matchProcess(names); m != "" {
		c.hit(KindProcess, show(dir), m)
	}
	if len(names) == 0 && !reported {
		return procUnnamed, false
	}
	return procRead, len(names) > 0
}

// deniedProcess records a process refused for lack of permission: as root
// a hit of its own; otherwise it is counted (see CheckProcesses).
func (c *Checker) deniedProcess(rel string, err error) procState {
	if c.euid == 0 {
		c.unreadable(rel, err)
	}
	return procDenied
}

// retryDenied runs read and, when it is refused for lack of permission,
// once more: a process changing its identity (an exec) refuses briefly.
func retryDenied(read func() ([]byte, error)) ([]byte, error) {
	data, err := read()
	if denied(err) {
		data, err = read()
	}
	return data, err
}

// matchProcess returns the list name one of names stands for, or "".
func (c *Checker) matchProcess(names []string) string {
	for _, n := range names {
		for _, a := range c.lists.AINames {
			if n == a || (len(a) > commLen && n == a[:commLen]) {
				return a
			}
		}
	}
	return ""
}

func isPID(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// vanished reports an error that means the process ended while it was read.
func vanished(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// denied reports an error that means the read was refused for lack of
// permission.
func denied(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}
