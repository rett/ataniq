//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/pidfd"
)

// ProcInspector is the real [PeerInspector]: it reads /proc for the peer and
// holds a pidfd for it while it does, so that what it read and what it checked
// are one process.
//
// A pid can be reused between the connect and the look. The inspector opens a
// pidfd, reads the process's state, start time and terminal, and then asks the
// pidfd whether that process has exited; and it refuses a process that started
// after the daemon accepted the connection, since the peer was running when it
// connected. A process that is gone, a zombie, or younger than the connection
// is not judged.
//
// It does not prove that the process is the console: the name the peer gave is
// the peer's word, and any process of the same user may say it (owner-channel
// OC20).
type ProcInspector struct{}

// startSlack is the room the start time, which the kernel counts in clock
// ticks of 10 ms, and the two clocks leave between "started before the
// connection" and "started after it".
const startSlack = 250 * time.Millisecond

// clockTicks is USER_HZ, which is 100 on every Linux the daemon runs on.
const clockTicks = 100

// procReads is what the inspector reads from the system, apart so that a test
// can answer each question on its own.
type procReads struct {
	open     func(pid int) (*os.File, error)
	stat     func(pid int) ([]byte, error)
	exited   func(f *os.File) (bool, error)
	uptime   func() (time.Duration, error)
}

var realProcReads = procReads{
	open: pidfd.Open,
	stat: func(pid int) ([]byte, error) { return os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") },
	exited: pidfd.Exited, uptime: uptime,
}

// Inspect looks at the process p.PID.
func (ProcInspector) Inspect(p PeerRef) (PeerProcess, error) { return inspectProc(p, realProcReads) }

func inspectProc(p PeerRef, rd procReads) (PeerProcess, error) {
	if p.PID < 1 {
		return PeerProcess{}, errors.New("the peer's process id is not known")
	}
	f, err := rd.open(p.PID)
	if err != nil {
		return PeerProcess{}, fmt.Errorf("the peer's process cannot be held: %w", err)
	}
	defer f.Close()
	b, err := rd.stat(p.PID)
	if err != nil {
		return PeerProcess{}, fmt.Errorf("the peer's process cannot be read: %w", err)
	}
	stat := string(b)
	st, err := pidfd.ParseStat(stat)
	if err != nil {
		return PeerProcess{}, fmt.Errorf("the peer's process cannot be read: %w", err)
	}
	if st.Gone() {
		return PeerProcess{}, errors.New("the peer's process has ended")
	}
	tty, err := ttyOfStat(stat)
	if err != nil {
		return PeerProcess{}, err
	}
	// The process the pidfd holds must still be running now: what was read
	// above is then that process's.
	if exited, err := rd.exited(f); err != nil {
		return PeerProcess{}, fmt.Errorf("the peer's process cannot be checked: %w", err)
	} else if exited {
		return PeerProcess{}, errors.New("the peer's process has ended")
	}
	up, err := rd.uptime()
	if err != nil {
		return PeerProcess{}, err
	}
	if processAge(up, st.Start)+startSlack < time.Since(p.AcceptedAt) {
		return PeerProcess{}, errors.New("the process started after the connection was made: the process id was reused")
	}
	return PeerProcess{TTY: tty}, nil
}

// processAge returns how long before now, with the system up for up, a
// process started at start clock ticks after boot. A tick is 10 ms, so the
// product is made from the tick's length: start times past about 1067 days
// would overflow the nanosecond count of the other order of operations. A
// start time too large to be a duration is as large as one can be.
func processAge(up time.Duration, start uint64) time.Duration {
	const tick = time.Second / clockTicks
	if start > uint64(math.MaxInt64/int64(tick)) {
		return up - time.Duration(math.MaxInt64)
	}
	return up - time.Duration(start)*tick
}

// uptime reads the time since boot from /proc/uptime.
func uptime() (time.Duration, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("the uptime cannot be read: %w", err)
	}
	first, _, _ := strings.Cut(string(b), " ")
	secs, err := strconv.ParseFloat(first, 64)
	if err != nil || secs < 0 {
		return 0, errors.New("the uptime cannot be read")
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// ttyOfStat returns the controlling terminal named by the text of
// /proc/<pid>/stat: field 7, tty_nr, a device number, 0 when the process has
// none. A device that is not a pseudo-terminal or a console is an error: it is
// not a terminal the daemon can name.
func ttyOfStat(stat string) (string, error) {
	end := strings.LastIndex(stat, ") ")
	if end < 0 {
		return "", errors.New("the peer's process cannot be read")
	}
	f := strings.Fields(stat[end+2:])
	// After the command name: state, ppid, pgrp, session, tty_nr.
	if len(f) < 5 {
		return "", errors.New("the peer's process cannot be read")
	}
	n, err := strconv.ParseInt(f[4], 10, 64)
	if err != nil || n < 0 {
		return "", errors.New("the peer's terminal cannot be read")
	}
	if n == 0 {
		return "", nil
	}
	major := (n >> 8) & 0xfff
	minor := (n & 0xff) | ((n >> 12) & 0xfff00)
	switch {
	case major >= 136 && major <= 143:
		return "/dev/pts/" + strconv.FormatInt((major-136)*256+minor, 10), nil
	case major == 4 && minor < 64:
		return "/dev/tty" + strconv.FormatInt(minor, 10), nil
	}
	return "", errors.New("the peer's terminal is not a pseudo-terminal or a console")
}
