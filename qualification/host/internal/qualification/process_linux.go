//go:build linux && amd64

package qualification

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"ataniq/internal/pidfd"
	"ataniq/internal/systemd"
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

func bootID() (string, error) { return pidfd.BootID() }

func validBoot(s string) bool { return pidfd.ValidBoot(s) }

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
	st, err := pidfd.ParseStat(string(b))
	switch {
	case errors.Is(err, pidfd.ErrStatFormat):
		return p, errors.New("invalid process stat")
	case errors.Is(err, pidfd.ErrStatShort) || st.Gone():
		return p, errors.New("process not living")
	case err != nil:
		return p, errors.New("invalid process starttime")
	}
	p.Start = st.Start
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
	st, err = pidfd.ParseStat(string(b))
	if err != nil || st.Start != p.Start || st.Gone() {
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
	f, err := pidfd.Open(p.PID)
	if err != nil {
		return nil, err
	}
	actual, err := process(p.PID)
	if err != nil || !sameProcess(p, actual) {
		f.Close()
		return nil, errors.New("process identity changed")
	}
	return f, nil
}

func exited(fd *os.File) (bool, error) { return pidfd.Exited(fd) }

// Recovery has no surviving pidfd. Only same-boot PID/starttime evidence may
// establish that the original process ended. An executable change is exec,
// not PID reuse. Permission failures never mean absence.
func processEnded(p Process) (bool, error) { return pidfd.Ended(p.Boot, p.PID, p.Start) }

func originalEnded(start uint64, stat string) (bool, error) {
	return pidfd.OriginalEnded(start, stat)
}

func boundary(path string) (Boundary, error) { return systemd.BoundaryOf(path) }

func populated(want Boundary) (bool, error) { return systemd.Populated(want) }

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
