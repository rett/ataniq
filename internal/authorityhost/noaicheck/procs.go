package noaicheck

import (
	"errors"
	"io/fs"
	"path"
	"strings"
	"syscall"
)

// commLen is the longest process name the kernel keeps in comm (its
// TASK_COMM_LEN less the terminating byte).
const commLen = 15

// CheckProcesses is check 5: a running process whose name (proc/PID/comm,
// which the kernel cuts to 15 bytes) or whose executable's base name (the
// target of proc/PID/exe) is an executable name of ai-names.list is a hit
// of kind "process", with the matched name as its detail. A process that
// ends while it is read, or whose exe cannot be read (another user's
// process when the check does not run as root, a kernel thread), is passed
// over; the check runs as root on the host.
func (c *Checker) CheckProcesses() {
	p, entries, err := c.t.readDir("proc")
	if err != nil {
		c.unreadable("proc", err)
		return
	}
	for _, e := range entries {
		if !isPID(e.Name()) {
			continue
		}
		dir := path.Join(p, e.Name())
		var names []string
		if _, data, err := c.t.readFile(path.Join(dir, "comm"), 4096); err == nil {
			names = append(names, strings.TrimSuffix(string(data), "\n"))
		}
		if target, err := c.t.readlink(path.Join(dir, "exe")); err == nil {
			names = append(names, path.Base(strings.TrimSuffix(target, " (deleted)")))
		} else if !gone(err) {
			c.unreadable(path.Join(dir, "exe"), err)
		}
		if m := c.matchProcess(names); m != "" {
			c.hit(KindProcess, show(dir), m)
		}
	}
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

// gone reports an error that means the process ended or its exe is not
// there to read: none of them is a hit.
func gone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) ||
		errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL)
}
