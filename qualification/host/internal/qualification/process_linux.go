//go:build linux && amd64

package qualification

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

func kernelRead(path string, cap int64) ([]byte, error) {
	f, err := openAbsolute(path, syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, cap+1))
	if err != nil || int64(len(b)) > cap {
		return nil, errors.New("kernel read failed or excessive")
	}
	return b, nil
}

func bootID() (string, error) {
	b, err := kernelRead("/proc/sys/kernel/random/boot_id", 64)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if !validBoot(s) {
		return "", errors.New("missing boot identity")
	}
	return s, nil
}

func validBoot(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !isHex(r) {
			return false
		}
	}
	return true
}

func process(pid int) (Process, error) {
	if pid < 1 {
		return Process{}, errors.New("invalid host pid")
	}
	p := Process{PID: pid, Namespaces: map[string]FileIdentity{}}
	var err error
	p.Boot, err = bootID()
	if err != nil {
		return p, err
	}
	base := fmt.Sprintf("/proc/%d", pid)
	b, err := kernelRead(base+"/stat", 8192)
	if err != nil {
		return p, err
	}
	s := string(b)
	end := strings.LastIndex(s, ") ")
	if end < 0 {
		return p, errors.New("invalid process stat")
	}
	fields := strings.Fields(s[end+2:])
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return p, errors.New("process not living")
	}
	p.Start, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil || p.Start == 0 {
		return p, errors.New("invalid process starttime")
	}
	fi, err := os.Stat(base + "/exe")
	if err != nil {
		return p, err
	}
	p.Executable = identity(fi)
	b, err = kernelRead(base+"/cgroup", 4096)
	if err != nil {
		return p, err
	}
	line := strings.TrimSuffix(string(b), "\n")
	if !strings.HasPrefix(line, "0::/") || strings.Contains(line, "\n") {
		return p, errors.New("unified cgroup required")
	}
	p.Cgroup = strings.TrimPrefix(line, "0::")
	for _, name := range []string{"pid", "mnt", "net", "user", "ipc", "uts"} {
		fi, err = os.Stat(base + "/ns/" + name)
		if err != nil {
			return p, err
		}
		p.Namespaces[name] = identity(fi)
	}
	b, err = kernelRead(base+"/status", 16384)
	if err != nil {
		return p, err
	}
	p.Status = string(b)
	b, err = kernelRead(base+"/uid_map", 4096)
	if err != nil {
		return p, err
	}
	p.UIDMap = string(b)
	b, err = kernelRead(base+"/gid_map", 4096)
	if err != nil {
		return p, err
	}
	p.GIDMap = string(b)
	b, err = kernelRead(base+"/mountinfo", 32768)
	if err != nil {
		return p, err
	}
	p.Mounts, err = mountState(b)
	if err != nil {
		return p, err
	}
	// Detect exit/reuse across these reads.
	b, err = kernelRead(base+"/stat", 8192)
	if err != nil {
		return p, err
	}
	end = strings.LastIndex(string(b), ") ")
	if end < 0 {
		return p, errors.New("changed process")
	}
	fields = strings.Fields(string(b)[end+2:])
	if len(fields) < 20 || fields[19] != strconv.FormatUint(p.Start, 10) || fields[0] == "Z" || fields[0] == "X" {
		return p, errors.New("changed process")
	}
	return p, nil
}

func sameProcess(a, b Process) bool {
	return a.PID == b.PID && a.Start == b.Start && a.Executable == b.Executable && a.Cgroup == b.Cgroup && a.Boot == b.Boot
}

func mountState(b []byte) (MountState, error) {
	var m MountState
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			return m, errors.New("invalid bounded mountinfo")
		}
		var dst *string
		switch f[4] {
		case "/":
			dst = &m.Root
		case "/probe":
			dst = &m.Probe
		case "/fixtures/fake-file":
			dst = &m.Fixture
		case "/proc":
			dst = &m.Proc
		}
		if dst != nil {
			if *dst != "" || len(line) > 4096 {
				return m, errors.New("ambiguous mount state")
			}
			*dst = line
		}
	}
	if m.Root == "" {
		return m, errors.New("missing root mount state")
	}
	return m, nil
}

func readOnlyMount(line string) bool {
	f := strings.Fields(line)
	return len(f) >= 10 && strings.Contains(","+f[5]+",", ",ro,")
}

// A pidfd is retained across cancellation. Starttime alone is not an exit handle.
// This slice returns unsupported/inconclusive on kernels denying pidfd_open.
func processHandle(p Process) (*os.File, error) {
	fd, _, errno := syscall.Syscall(434, uintptr(p.PID), 0, 0) // pidfd_open, linux/amd64
	if errno != 0 {
		return nil, errno
	}
	f := os.NewFile(fd, "owned-target-pidfd")
	actual, err := process(p.PID)
	if err != nil || !sameProcess(p, actual) {
		f.Close()
		return nil, errors.New("process identity changed")
	}
	return f, nil
}

func exited(fd *os.File) (bool, error) {
	poll := struct {
		FD      int32
		Events  int16
		Revents int16
	}{int32(fd.Fd()), 1, 0}
	_, _, errno := syscall.Syscall(syscall.SYS_POLL, uintptr(unsafe.Pointer(&poll)), 1, 0)
	if errno != 0 {
		return false, errno
	}
	return pidfdEvents(poll.Revents)
}

func pidfdEvents(events int16) (bool, error) {
	if events & ^int16(1|16) != 0 {
		return false, errors.New("unexpected pidfd poll result")
	}
	return events&(1|16) != 0, nil
}

// Recovery has no surviving pidfd. Only same-boot PID/starttime evidence may
// establish that the original process ended. An executable change is exec,
// not PID reuse. Permission failures never mean absence.
func processEnded(p Process) (bool, error) {
	boot, err := bootID()
	if err != nil || boot != p.Boot {
		return false, errors.New("unsupported: recovery boot identity differs")
	}
	b, err := kernelRead(fmt.Sprintf("/proc/%d/stat", p.PID), 8192)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return originalEnded(p.Start, string(b))
}

func originalEnded(start uint64, stat string) (bool, error) {
	end := strings.LastIndex(stat, ") ")
	if end < 0 {
		return false, errors.New("invalid recovery process stat")
	}
	f := strings.Fields(stat[end+2:])
	if len(f) < 20 {
		return false, errors.New("short recovery process stat")
	}
	n, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil || n == 0 {
		return false, errors.New("invalid recovery starttime")
	}
	return n != start || f[0] == "Z" || f[0] == "X", nil
}

func boundary(path string) (Boundary, error) {
	if !strings.HasPrefix(path, "/") || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\n\r") {
		return Boundary{}, errors.New("invalid cgroup path")
	}
	f, err := openAbsolute("/sys/fs/cgroup"+path, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return Boundary{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Boundary{}, err
	}
	return Boundary{path, identity(fi)}, nil
}

func populated(want Boundary) (bool, error) {
	before, err := boundary(want.Path)
	if err != nil || before != want {
		return false, errors.New("cgroup identity missing or changed")
	}
	b, err := kernelRead("/sys/fs/cgroup"+want.Path+"/cgroup.events", 4096)
	if err != nil {
		return false, err
	}
	seen := false
	value := false
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			return false, errors.New("invalid cgroup events")
		}
		if f[0] == "populated" {
			if seen || f[1] != "0" && f[1] != "1" {
				return false, errors.New("invalid populated value")
			}
			seen = true
			value = f[1] == "1"
		}
	}
	after, err := boundary(want.Path)
	if err != nil || after != want || !seen {
		return false, errors.New("unproved boundary population")
	}
	return value, nil
}

func helperIn(want Boundary, exe FileIdentity) (Process, error) {
	b, err := kernelRead("/sys/fs/cgroup"+want.Path+"/cgroup.procs", 8192)
	if err != nil {
		return Process{}, err
	}
	ids := strings.Fields(string(b))
	if len(ids) > MaxEntries {
		return Process{}, errors.New("excessive exact cgroup members")
	}
	var match Process
	count := 0
	for _, s := range ids {
		pid, err := strconv.Atoi(s)
		if err != nil {
			return Process{}, err
		}
		p, err := process(pid)
		if err != nil {
			return Process{}, err
		}
		if p.Cgroup != want.Path {
			return Process{}, errors.New("member escaped exact cgroup")
		}
		if p.Executable == exe {
			match = p
			count++
		}
	}
	if count != 1 {
		return Process{}, errors.New("actual helper not uniquely established")
	}
	actual, err := boundary(want.Path)
	if err != nil || actual != want {
		return Process{}, errors.New("cgroup changed during enumeration")
	}
	return match, nil
}
