//go:build linux && amd64

package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

const FinalDescriptorPolicy = "final-fds-v1"

// This receipt describes verified preflight and a planned exec policy only.
// It is published BEFORE unsharing; it cannot establish post-exec closure.
type DescriptorReceipt struct {
	Version          int        `json:"version"`
	Attempt          string     `json:"attempt"`
	Invocation       string     `json:"invocation"`
	Policy           string     `json:"policy"`
	PlanSHA256       string     `json:"plan_sha256"`
	Role             string     `json:"role"`
	Process          Process    `json:"process"`
	Descriptors      []FDRecord `json:"descriptors"`
	Allowed          []int      `json:"allowed"`
	Denied           []int      `json:"denied"`
	Command          []string   `json:"command"`
	EmptyEnvironment bool       `json:"empty_environment"`
	Stage            string     `json:"stage"`
}

func permittedDescriptor(role string, fd int) bool {
	if fd >= 0 && fd <= 2 {
		return true
	}
	switch role {
	case "candidate":
		return fd == BPFFD
	case "direct":
		return fd == FakeFD
	case "weakened-synthetic":
		return fd == BPFFD || fd == 64 || fd == FakeFD || fd == 768 || fd == 1000
	}
	return false
}

func permittedForAttempt(a *Attempt, fd int) bool {
	if a.Config.DescriptorProfile != FD1TypedProfile {
		return permittedDescriptor(a.Config.Role, fd)
	}
	if fd >= 0 && fd <= 2 {
		return true
	}
	if fd == BPFFD {
		return a.Config.Role == "candidate" || a.Config.Role == "weakened-synthetic"
	}
	return (a.Config.Role == "direct" || a.Config.Role == "weakened-synthetic") && (fd == FD1DirectoryFD || fd == FD1PipeFD || fd == FD1SocketFD || fd == FD1RegularFD)
}

func descriptorReceipt(a *Attempt) DescriptorReceipt {
	r := DescriptorReceipt{Version: 1, Attempt: a.ID, Invocation: a.Injector.Invocation, Policy: a.DescriptorPolicy, PlanSHA256: a.PlanSHA256, Role: a.Config.Role, Process: a.Injector.Process, Descriptors: expectedDescriptors(a), Command: a.TargetCommand, EmptyEnvironment: true, Stage: "prepared-before-unshare", Allowed: []int{}, Denied: []int{}}
	for _, d := range r.Descriptors {
		if permittedForAttempt(a, d.FD) {
			r.Allowed = append(r.Allowed, d.FD)
		} else {
			r.Denied = append(r.Denied, d.FD)
		}
	}
	return r
}

func validateDescriptorReceipt(a *Attempt, r DescriptorReceipt) error {
	if a.DescriptorPolicy != FinalDescriptorPolicy || a.Injector == nil || !sameJSON(r, descriptorReceipt(a)) {
		return errors.New("final descriptor receipt mismatch")
	}
	return nil
}

type terminalFD struct {
	fd     int
	stat   syscall.Stat_t
	access int
	keep   bool
}

type descriptorExec struct {
	fds         []terminalFD
	command     []string
	environment []string
	diagnostic  []byte
	stat        syscall.Stat_t
}

func prepareDescriptorExec(a *Attempt) (*descriptorExec, error) {
	if a.DescriptorPolicy != FinalDescriptorPolicy || a.Config.validate() != nil || len(a.TargetCommand) == 0 {
		return nil, errors.New("invalid final descriptor role or target")
	}
	if a.Config.DescriptorProfile == FD1TypedProfile {
		if err := authenticateFD1Runtime(a, a.FD1Runtime); err != nil {
			return nil, err
		}
		if err := verifyFD1Endpoints(a); err != nil {
			return nil, err
		}
	}
	p := &descriptorExec{command: append([]string(nil), a.TargetCommand...), environment: []string{}, diagnostic: []byte("final-fds stage=00 errno=0000\n")}
	for _, d := range expectedDescriptors(a) {
		var st syscall.Stat_t
		flags, err := fdFlags(d.FD)
		access, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(d.FD), syscall.F_GETFL, 0)
		wantAccess := syscall.O_RDONLY
		if d.FD == 1 || d.FD == 2 {
			wantAccess = syscall.O_WRONLY | syscall.O_APPEND
		}
		kind := uint32(syscall.S_IFREG)
		if a.Config.DescriptorProfile == FD1TypedProfile && d.FD > 2 && d.FD != BPFFD {
			kind = fd1Kind(d.FD)
			wantAccess = fd1ExpectedFlags(d.FD) & (syscall.O_ACCMODE | syscall.O_APPEND)
		}
		if syscall.Fstat(d.FD, &st) != nil || err != nil || e != 0 || flags != 0 || st.Mode&syscall.S_IFMT != kind || kind == syscall.S_IFREG && st.Nlink != 1 || (FileIdentity{st.Dev, st.Ino, st.Uid}) != d.Identity || int(access)&(syscall.O_ACCMODE|syscall.O_APPEND|0x200000) != wantAccess {
			return nil, errors.New("final descriptor identity/access/flags mismatch")
		}
		p.fds = append(p.fds, terminalFD{d.FD, st, int(access), permittedForAttempt(a, d.FD)})
	}
	for _, fd := range fakeFDs(a) {
		if err := verifyFakeFD(fd, a.Fixture); err != nil {
			return nil, err
		}
	}
	// Ordinary owned frozen read-only file, with no concurrent writable alias.
	// Pread validates content without consuming the shared open-file offset.
	if err := verifyFD(BPFFD, a.Snapshots["bpf"], 32768); err != nil {
		return nil, err
	}
	if off, err := syscall.Seek(BPFFD, 0, 1); err != nil || off != 0 {
		return nil, errors.New("BPF offset must be zero")
	}
	return p, nil
}

// PrivateFinalDescriptors is a dedicated static-driver reexec, still ahead of
// the complete backend chain. The injector supplied an EMPTY environment: its
// invocation is recovered only from exact released durable evidence, never env.
func PrivateFinalDescriptors(path string) error {
	if !absolute(path) || !attemptID(filepath.Base(path)) {
		return errors.New("invalid final descriptor root")
	}
	root := filepath.Dir(path)
	d, id, err := ownedDir(root)
	if err != nil {
		return err
	}
	d.Close()
	c := &Campaign{Path: root, Identity: id}
	a, err := c.read(filepath.Base(path))
	if err != nil || a.DescriptorPolicy != FinalDescriptorPolicy || a.Phase != "released" || a.Injector == nil || a.Target == nil || time.Now().Unix() > a.NotAfter {
		return errors.New("final descriptor intent not released")
	}
	if err := entryDispatchCheck(a); err != nil {
		return err
	}
	if err := freshFD1Control(a, time.Now().Unix()); err != nil {
		return err
	}
	b, err := readOwned(filepath.Join(path, "release.json"), 4096)
	var r release
	if err != nil || decode(bytes.NewReader(b), 4096, &r) != nil || r != (release{1, a.ID, a.Injector.Invocation}) {
		return errors.New("final descriptor release mismatch")
	}
	i, err := readInjection(filepath.Join(path, "injection.json"))
	if err != nil || !sameJSON(i, *a.Injector) {
		return errors.New("final descriptor injector mismatch")
	}
	self, err := process(os.Getpid())
	if err != nil || !sameProcess(self, a.Injector.Process) || !sameProcess(self, *a.Target) || !reflect.DeepEqual(self.Namespaces, a.Injector.Process.Namespaces) || self.Executable != a.Snapshots["driver"].Identity {
		return errors.New("final descriptor process mismatch")
	}
	bnd, err := boundary(self.Cgroup)
	if err != nil || bnd != a.Injector.Boundary || a.Parent == nil || self.Cgroup != a.Parent.Boundary.Path+"/"+a.Service {
		return errors.New("final descriptor boundary mismatch")
	}
	parent, err := boundary(a.Parent.Boundary.Path)
	if err != nil || parent != a.Parent.Boundary {
		return errors.New("final descriptor parent mismatch")
	}
	for _, name := range []string{"driver", "probe", "bwrap", "bpf"} {
		s, _, err := chainSubject(name, a.Snapshots[name].Path, true)
		if err != nil || s != a.Snapshots[name] {
			return errors.New("final descriptor frozen subject changed")
		}
	}
	fake, _, err := subject(a.Fixture.Path, false, false, true, 4096)
	if err != nil || fake != a.Fixture {
		return errors.New("final descriptor fixture changed")
	}
	p, err := prepareDescriptorExec(a)
	if err != nil {
		return err
	}
	receipt, err := json.Marshal(descriptorReceipt(a))
	if err != nil || len(receipt) > MaxOutputBytes {
		return errors.New("final descriptor evidence excessive")
	}
	if err := publishOwned(path, "descriptor-policy.json", receipt); err != nil {
		return err
	}
	p.exec()
	return nil // exec never returns, even on failure.
}

// No unlock, callbacks, opens, proc enumeration, retry or ordinary error return
// below this point. Raw calls operate on this OS thread's private descriptor
// table. All Go values and bounded diagnostic storage were prepared above.
// The reviewed syscall.Exec allocates argv/env pointers, serializes runtime
// exec/thread creation and restores the original RLIMIT_NOFILE. These operations
// open no FDs. LockOSThread starts Go's template thread before the unshare; newm
// from a locked M uses that template rather than cloning this private table.
func (p *descriptorExec) exec() {
	runtime.LockOSThread()
	_, _, e := syscall.RawSyscall(436, 3, uintptr(^uint32(0)), 2|4)
	if e != 0 {
		p.die(1, e)
	}
	for j := range p.fds {
		d := &p.fds[j]
		st := &p.stat
		_, _, e = syscall.RawSyscall(syscall.SYS_FSTAT, uintptr(d.fd), uintptr(unsafe.Pointer(st)), 0)
		if e != 0 {
			p.die(2, e)
		}
		if st.Dev != d.stat.Dev || st.Ino != d.stat.Ino || st.Uid != d.stat.Uid || st.Mode != d.stat.Mode || st.Nlink != d.stat.Nlink || st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Nlink != 1 || st.Size != d.stat.Size || st.Mtim != d.stat.Mtim || st.Ctim != d.stat.Ctim {
			p.die(2, syscall.ESTALE)
		}
		access, _, e := syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(d.fd), syscall.F_GETFL, 0)
		if e != 0 {
			p.die(3, e)
		}
		if int(access) != d.access {
			p.die(3, syscall.EACCES)
		}
		flags, _, e := syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(d.fd), syscall.F_GETFD, 0)
		if e != 0 {
			p.die(4, e)
		}
		want := uintptr(syscall.FD_CLOEXEC)
		if d.fd <= 2 {
			want = 0
		}
		if flags != want {
			p.die(4, syscall.EINVAL)
		}
		if d.fd == BPFFD {
			off, _, e := syscall.RawSyscall(syscall.SYS_LSEEK, uintptr(d.fd), 0, 1)
			if e != 0 {
				p.die(5, e)
			}
			if off != 0 {
				p.die(5, syscall.EINVAL)
			}
		}
	}
	for j := range p.fds {
		d := &p.fds[j]
		if d.keep && d.fd > 2 {
			_, _, e = syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(d.fd), syscall.F_SETFD, 0)
			if e != 0 {
				p.die(6, e)
			}
		}
	}
	// Check both the compiled exceptions and every deliberately denied alias.
	for j := range p.fds {
		d := &p.fds[j]
		flags, _, e := syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(d.fd), syscall.F_GETFD, 0)
		if e != 0 {
			p.die(7, e)
		}
		want := uintptr(syscall.FD_CLOEXEC)
		if d.keep {
			want = 0
		}
		if flags != want {
			p.die(7, syscall.EINVAL)
		}
	}
	err := syscall.Exec(p.command[0], p.command, p.environment)
	errno, ok := err.(syscall.Errno)
	if !ok {
		errno = syscall.EINVAL
	}
	p.die(8, errno)
}

func (p *descriptorExec) die(stage byte, errno syscall.Errno) {
	p.diagnostic[16] = '0' + stage/10
	p.diagnostic[17] = '0' + stage%10
	n := uint64(errno) % 10000
	for j := 28; j >= 25; j-- {
		p.diagnostic[j] = '0' + byte(n%10)
		n /= 10
	}
	_, _, _ = syscall.RawSyscall(syscall.SYS_WRITE, 2, uintptr(unsafe.Pointer(&p.diagnostic[0])), uintptr(len(p.diagnostic)))
	for {
		syscall.RawSyscall(syscall.SYS_EXIT_GROUP, 125, 0, 0)
	}
}
