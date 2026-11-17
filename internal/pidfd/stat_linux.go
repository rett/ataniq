package pidfd

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Reasons ParseStat fails. They let a caller keep its own error text for each.
var (
	// ErrStatFormat means the text has no closing ") " after the command name.
	ErrStatFormat = errors.New("invalid process stat")
	// ErrStatShort means fewer than 20 fields follow the command name.
	ErrStatShort = errors.New("short process stat")
	// ErrStatStart means the start time is not a positive decimal number.
	ErrStatStart = errors.New("invalid process starttime")
)

// Stat is the part of /proc/<pid>/stat that identifies a process on one boot.
type Stat struct {
	// State is the one-letter state, such as "S", "Z" (zombie) or "X" (dead).
	State string
	// Start is the process start time in clock ticks after boot (field 22).
	Start uint64
}

// Gone reports whether the state is zombie or dead.
func (s Stat) Gone() bool { return s.State == "Z" || s.State == "X" }

// ParseStat parses the text of /proc/<pid>/stat. The command name is skipped by
// finding the last ") ", so a name containing parentheses or spaces cannot
// shift the fields. On ErrStatStart the returned State is still set, so a
// caller can look at the state first. The start time alone is not an identity:
// it must be combined with the boot identity and the pid.
func ParseStat(stat string) (Stat, error) {
	end := strings.LastIndex(stat, ") ")
	if end < 0 {
		return Stat{}, ErrStatFormat
	}
	f := strings.Fields(stat[end+2:])
	if len(f) < 20 {
		return Stat{}, ErrStatShort
	}
	s := Stat{State: f[0]}
	n, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil || n == 0 {
		return s, ErrStatStart
	}
	s.Start = n
	return s, nil
}

// ReadStat reads and parses /proc/<pid>/stat. A read error, such as ENOENT for
// a process that does not exist or EACCES, is returned as it is.
func ReadStat(pid int) (Stat, error) {
	b, err := readKernel(fmt.Sprintf("/proc/%d/stat", pid), 8192)
	if err != nil {
		return Stat{}, err
	}
	return ParseStat(string(b))
}

// maxPID is the largest process id the kernel hands out: PID_MAX_LIMIT, 1<<22
// on a 64-bit kernel. A larger number names no process, now or later.
const maxPID = 1 << 22

// Ended reports whether the original process, named by boot identity, pid and
// start time, has ended. It has no surviving pidfd to rely on: only same-boot
// pid and start-time evidence may establish the end. A different boot is an
// error, not an answer. A missing /proc entry is the only absence it accepts;
// permission and other read failures never mean absence. A changed
// executable is an exec, not a reused pid, and does not make the original
// ended. It does not prove that the control group or the children are gone.
// An impossible identity (a pid below 1 or above the kernel's maximum of
// 1<<22, or a start time of 0) is an error, not an answer: absence of such a
// process proves nothing about the original.
func Ended(boot string, pid int, start uint64) (bool, error) {
	if pid < 1 || pid > maxPID || start == 0 {
		return false, errors.New("invalid process identity")
	}
	current, err := BootID()
	if err != nil || current != boot {
		return false, errors.New("unsupported: recovery boot identity differs")
	}
	b, err := readKernel(fmt.Sprintf("/proc/%d/stat", pid), 8192)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return OriginalEnded(start, string(b))
}

// OriginalEnded decides from the text of /proc/<pid>/stat whether the process
// that had the given start time has ended: a different start time (the pid was
// reused), a zombie or a dead state. Malformed text is an error and never an
// answer, and so is a start time of 0, which names no process. The text must
// come from the same boot.
func OriginalEnded(start uint64, stat string) (bool, error) {
	if start == 0 {
		return false, errors.New("invalid recovery starttime")
	}
	s, err := ParseStat(stat)
	switch {
	case errors.Is(err, ErrStatFormat):
		return false, errors.New("invalid recovery process stat")
	case errors.Is(err, ErrStatShort):
		return false, errors.New("short recovery process stat")
	case err != nil:
		return false, errors.New("invalid recovery starttime")
	}
	return s.Start != start || s.Gone(), nil
}
