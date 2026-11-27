package hookport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"ataniq/internal/leadholder/hookport/testdata/probe"
	"ataniq/internal/leadholder/hookport/testdata/pthread"
	"ataniq/internal/leadholder/ptyhost"
	"golang.org/x/sys/unix"
)

const trapExit = 91

func digest(program []unix.SockFilter) string {
	var b bytes.Buffer
	if err := binary.Write(&b, binary.LittleEndian, program); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b.Bytes()))
}

// Only test code builds the diagnostic program. Its data word identifies the
// exact return instruction; its real SIGSYS siginfo also names the syscall.
func attribution() []unix.SockFilter {
	p := holderProgram()
	for i, ins := range p {
		if ins.Code == unix.BPF_RET|unix.BPF_K && ins.K&0xffff0000 == unix.SECCOMP_RET_ERRNO {
			p[i].K = unix.SECCOMP_RET_TRAP | uint32(i)
		}
	}
	return p
}

func TestFilterProgram(t *testing.T) {
	seen := map[uint32]bool{}
	for _, op := range operations() {
		seen[op.nr] = true
	}
	prod, attr := holderProgram(), attribution()
	if len(prod) != len(attr) {
		t.Fatal("program length differs")
	}
	for _, nr := range append(slices.Clone(denied[:]), unix.SYS_CLONE) {
		if !seen[nr] {
			t.Fatalf("no kernel control for syscall %d", nr)
		}
	}
	// The operation table also pins the required deny list when the
	// environment cannot run a control. Legacy clone has a flags rule.
	for nr := range seen {
		if nr != unix.SYS_CLONE && !slices.Contains(denied[:], nr) {
			t.Fatalf("missing deny rule for syscall %d", nr)
		}
	}
	for i, a := range attr {
		b := prod[i]
		if b.Code == unix.BPF_RET|unix.BPF_K && b.K&0xffff0000 == unix.SECCOMP_RET_ERRNO {
			b.K = unix.SECCOMP_RET_TRAP | uint32(i)
		}
		if a != b {
			t.Fatalf("attribution differs at instruction %d", i)
		}
	}
}

func ruleMark(nr uint32) uint32 {
	p := holderProgram()
	for i, v := range p {
		if v.Code == unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K && v.K == nr {
			if nr == unix.SYS_CLONE {
				return uint32(i + 3)
			}
			return uint32(i + 1)
		}
	}
	panic("operation lacks a production rule")
}

func catchTrap() error {
	entry, restorer := probe.Handler()
	act := struct{ handler, flags, restorer, mask uintptr }{entry, 4 | 0x04000000, restorer, 0} // SA_SIGINFO | SA_RESTORER
	_, _, e := unix.RawSyscall6(unix.SYS_RT_SIGACTION, uintptr(unix.SIGSYS), uintptr(unsafe.Pointer(&act)), 0, 8, 0, 0)
	return sysErr(e)
}

func kernelRefusal(err error) bool {
	for _, e := range []syscall.Errno{unix.EACCES, unix.EPERM, unix.ENOSYS, unix.EOPNOTSUPP, unix.ENOPROTOOPT} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func unavailable(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("ATANIQ_REQUIRE_SECCOMP") == "1" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}

func childError(stage string, err error, canSkip bool) {
	if canSkip && kernelRefusal(err) {
		fmt.Printf("UNAVAILABLE %s: %s\n", stage, err)
		os.Exit(77)
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", stage, err)
	os.Exit(1)
}

func TestRealListenAndPeerCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	l, client, err := socketSetup(dir)
	if err != nil {
		if kernelRefusal(err) {
			unavailable(t, err.Error())
		}
		t.Fatal(err)
	}
	defer l.Close()
	defer client.Close()
	st, err := os.Lstat(filepath.Join(dir, "port"))
	if err != nil || st.Mode() != os.ModeSocket|0600 {
		t.Fatal(st, err)
	}
	if another, err := Listen(dir); err == nil {
		another.Close()
		t.Fatal("replaced live socket")
	}
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ref, err := selfGroup()
	if err != nil {
		t.Fatal(err)
	}
	if err = admit(c, KernelPeers{}, ref); err != nil {
		if kernelRefusal(err) {
			unavailable(t, err.Error())
		}
		t.Fatal(err)
	}
}

func selfGroup() (string, error) {
	b, err := readProc("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	return ControlGroup(b)
}

func TestHP8(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", marker, "./testdata/marker")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("static exec marker: %v\n%s", err, out)
	}
	for _, op := range operations() {
		for _, mode := range []string{"control", "production", "attribution"} {
			t.Run(mode+"/"+op.name, func(t *testing.T) { runChild(t, op, mode, marker) })
		}
	}
	positive := []string{"accept-peer", "accepted-io", "stop", "threads"}
	if pthread.Linked {
		positive = append(positive, "pthread")
	}
	for _, name := range positive {
		t.Run("positive/"+name, func(t *testing.T) { runChild(t, operation{name: name}, "positive", marker) })
	}
	for _, abi := range []string{"x32", "i386"} {
		t.Run("kill/"+abi, func(t *testing.T) {
			op := operation{name: abi}
			runChild(t, op, "abi-control", marker)
			runChild(t, op, "kill", marker)
		})
	}
}

func runChild(t *testing.T, op operation, mode, marker string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHP8Child$", "--", mode, op.name, marker)
	cmd.Dir = t.TempDir()
	if mode == "attribution" {
		// Do not create a background C runtime thread between installing
		// the trap on clone3 and issuing the operation under observation.
		cmd.Env = append(os.Environ(), "GOMAXPROCS=1", "GOGC=off", "GODEBUG=asyncpreemptoff=1")
	}
	if err := os.Chmod(cmd.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	cmd.WaitDelay = time.Second
	if op.namespace {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:  unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWUTS,
			UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
		}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		t.Fatalf("child timed out: %s %s", out, stderr.Bytes())
	}
	if op.namespace && cmd.Process == nil && kernelRefusal(err) {
		unavailable(t, "kernel refused case namespace: "+err.Error())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 77 && bytes.Contains(out, []byte("UNAVAILABLE ")) {
		unavailable(t, strings.TrimSpace(string(out)))
	}
	program := holderProgram()
	if mode == "attribution" {
		program = attribution()
	}
	want := "READY " + digest(program) + "\n"
	if mode == "control" || mode == "abi-control" {
		want = "CONTROL\n"
	}
	tail, ok := bytes.CutPrefix(out, []byte(want))
	if !ok {
		t.Fatalf("missing installation/control marker: %v %q %s", err, out, stderr.Bytes())
	}
	if mode == "kill" || mode == "abi-control" {
		// An enclosing filter that already kills this benign ABI probe
		// cannot supply evidence for the holder's own kill rule.
		killed := exit != nil && exit.Sys().(syscall.WaitStatus).Signal() == unix.SIGSYS
		if mode == "abi-control" && killed && len(tail) == 0 {
			unavailable(t, "unfiltered "+op.name+" getpid killed by SIGSYS")
		}
		if mode == "kill" {
			if !killed || len(tail) != 0 {
				t.Fatalf("expected SIGSYS after installation: %v %q %s", err, tail, stderr.Bytes())
			}
			return
		}
	}
	if mode == "attribution" {
		// si_signo, si_errno (rule mark), SYS_SECCOMP, si_syscall, si_arch.
		if exit == nil || exit.ExitCode() != trapExit || len(tail) != 32 {
			t.Fatalf("missing kernel SIGSYS record: %v %x %s", err, tail, stderr.Bytes())
		}
		word := binary.LittleEndian.Uint32
		if word(tail) != uint32(unix.SIGSYS) || word(tail[4:]) != ruleMark(op.nr) || word(tail[8:]) != 1 || word(tail[24:]) != op.nr || word(tail[28:]) != unix.AUDIT_ARCH_X86_64 {
			t.Fatalf("wrong SIGSYS record %x", tail)
		}
		return
	}
	if err != nil {
		t.Fatalf("case failed: %v %s %s", err, out, stderr.Bytes())
	}
	result := "RESULT 0\n"
	switch {
	case mode == "production":
		errno := unix.EPERM
		if op.nr == unix.SYS_CLONE3 {
			errno = unix.ENOSYS
		}
		result = fmt.Sprintf("RESULT %d\n", errno)
	case mode == "control" && (op.nr == unix.SYS_EXECVE || op.nr == unix.SYS_EXECVEAT):
		result = "EXEC\n"
	}
	if string(tail) != result {
		t.Fatalf("got %q, want %q", tail, result)
	}
}

// The parent remains unfiltered. Every (mode, syscall) gets its own process,
// result channel (stdout) and timeout, including each successful exec.
func TestHP8Child(t *testing.T) {
	sep := slices.Index(os.Args, "--")
	if sep < 0 || len(os.Args) != sep+4 {
		return
	}
	mode, name, marker := os.Args[sep+1], os.Args[sep+2], os.Args[sep+3]
	runtime.LockOSThread()
	// Keep a second, known thread alive across installation.
	checkOld := make(chan chan error)
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		close(ready)
		for reply := range checkOld {
			reply <- socketDenied()
		}
	}()
	<-ready
	dir, err := os.Getwd()
	if err != nil {
		childError("working directory", err, false)
	}
	var invoke call
	if mode == "kill" || mode == "abi-control" {
		invoke = func() error {
			switch name {
			case "x32":
				unix.RawSyscall(0x40000000|unix.SYS_GETPID, 0, 0, 0)
			case "i386":
				probe.I386Getpid()
			default:
				return errors.New("unknown ABI")
			}
			// A native kernel without the x32 syscall table may return
			// ENOSYS. Any return distinguishes the control from SIGSYS.
			return nil
		}
	} else if mode == "positive" {
		invoke, err = positiveCase(name, dir, marker, checkOld)
	} else {
		var found bool
		for _, op := range operations() {
			if op.name == name {
				found = true
				invoke, err = prepare(op, dir, marker)
				break
			}
		}
		if !found {
			childError("case", errors.New("unknown operation"), false)
		}
	}
	if err != nil {
		childError("prepare "+name, err, true)
	}
	switch mode {
	case "production", "positive", "kill":
		filter, err := InstallFilter()
		if err != nil {
			childError("install production", err, true)
		}
		fmt.Printf("READY %x\n", filter.digest)
	case "attribution":
		if err := catchTrap(); err != nil {
			childError("sigaction", err, true)
		}
		p := attribution()
		marker := []byte("READY " + digest(p) + "\n")
		if err := install(p); err != nil {
			childError("install attribution", err, true)
		}
		// Keep the P until the marker is out. A scheduler handoff here
		// could call clone3 from libc, with SIGSYS masked, before the probe.
		n, _, e := unix.RawSyscall(unix.SYS_WRITE, 1, uintptr(unsafe.Pointer(&marker[0])), uintptr(len(marker)))
		if e != 0 || n != uintptr(len(marker)) {
			os.Exit(1)
		}
	case "control", "abi-control":
		fmt.Println("CONTROL")
	default:
		childError("mode", errors.New("unknown mode"), false)
	}
	err = invoke()
	if mode == "control" && err != nil {
		childError("unfiltered "+name, err, true)
	}
	if mode == "positive" && err != nil {
		childError("filtered "+name, err, false)
	}
	var errno syscall.Errno
	if err != nil && !errors.As(err, &errno) {
		childError("operation", err, false)
	}
	fmt.Printf("RESULT %d\n", errno)
	os.Exit(0)
}

func socketDenied() error {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == unix.EPERM {
		return nil
	}
	if err == nil {
		unix.Close(fd)
	}
	return fmt.Errorf("thread socket returned %v", err)
}

func positiveCase(name, dir, marker string, checkOld chan chan error) (call, error) {
	switch name {
	case "accept-peer", "accepted-io":
		l, client, err := socketSetup(dir)
		if err != nil {
			return nil, err
		}
		ref, err := selfGroup()
		if err != nil {
			return nil, err
		}
		return func() error {
			c, err := l.Accept()
			if err != nil {
				return err
			}
			defer c.Close()
			defer client.Close()
			if name == "accept-peer" {
				return admit(c, KernelPeers{}, ref)
			}
			for _, pair := range [][2]net.Conn{{client, c}, {c, client}} {
				if _, err := pair[0].Write([]byte("x")); err != nil {
					return err
				}
				var b [1]byte
				if _, err := io.ReadFull(pair[1], b[:]); err != nil {
					return err
				}
				if b[0] != 'x' {
					return errors.New("wrong byte")
				}
			}
			return nil
		}, nil
	case "stop":
		host, err := ptyhost.Start(ptyhost.Config{Argv: []string{marker, "target"}, NoCgroup: true, Cols: 80, Rows: 24})
		if err != nil {
			return nil, err
		}
		return func() error {
			defer host.Close()
			host.Stop(time.Second)
			e := host.Wait()
			if e.Signal != syscall.SIGTERM || e.How != "signal" {
				return fmt.Errorf("stop: %+v", e)
			}
			return nil
		}, nil
	case "threads":
		return func() error {
			tids, err := os.ReadDir("/proc/self/task")
			if err != nil {
				return err
			}
			old := map[int]bool{}
			for _, entry := range tids {
				tid, err := strconv.Atoi(entry.Name())
				if err != nil {
					return err
				}
				old[tid] = true
			}
			result := make(chan error)
			checkOld <- result
			if err := <-result; err != nil {
				return err
			}
			// Pin enough concurrent goroutines to exhaust every preexisting M.
			// Keep each pinned until the case exits, so TIDs cannot be recycled.
			fresh := make(chan bool)
			hold := make(chan struct{})
			defer close(hold)
			created := 0
			for range len(old) + 3 {
				go func() {
					runtime.LockOSThread()
					defer runtime.UnlockOSThread()
					fresh <- !old[unix.Gettid()]
					result <- socketDenied()
					<-hold
				}()
				if <-fresh {
					created++
				}
				if err := <-result; err != nil {
					return err
				}
			}
			if created < 2 {
				return errors.New("no evidence of two filtered new threads")
			}
			return nil
		}, nil
	case "pthread":
		return func() error {
			if n := pthread.Check(); n != int(unix.EPERM) {
				return fmt.Errorf("new C thread socket result %d", n)
			}
			return nil
		}, nil
	}
	return nil, errors.New("unknown positive case")
}
