//go:build linux && amd64

package qualification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func descriptorChildArgs(kind string) []string {
	for j, s := range os.Args {
		if s == kind {
			return os.Args[j+1:]
		}
	}
	return nil
}

// All terminal tests run in owned disposable children. No descriptor mutation
// in the test orchestrator, services, namespaces or backend launches.
func runDescriptorChild(t *testing.T, role, scenario string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"fake-file", "bpf", "request", "output", "stderr"} {
		data := []byte(nil)
		if name == "fake-file" {
			data = syntheticFile
		}
		if name == "bpf" {
			data = []byte("owned fake BPF bytes")
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDescriptorChild$", "--", "descriptor-child", dir, role, scenario)
	cmd.Env = []string{}
	in, err := os.Open(filepath.Join(dir, "request"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(dir, "output"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stderr, err := os.OpenFile(filepath.Join(dir, "stderr"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = stderr
	err = cmd.Run()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if ctx.Err() != nil {
		t.Fatal("descriptor child exceeded watchdog")
	}
	b, e := os.ReadFile(filepath.Join(dir, "output"))
	if e != nil {
		t.Fatal(e)
	}
	d, e := os.ReadFile(filepath.Join(dir, "stderr"))
	if e != nil {
		t.Fatal(e)
	}
	return code, string(b), string(d)
}

func childAttempt(t *testing.T, dir, role string) *Attempt {
	t.Helper()
	a := &Attempt{DescriptorPolicy: FinalDescriptorPolicy, Config: HostConfig{Version: 1, Campaign: dir, Driver: "/owned/driver", Probe: "/owned/probe", Bwrap: "/owned/bwrap", BPF: "/owned/bpf", Systemctl: "/owned/systemctl", SystemdRun: "/owned/systemd-run", Role: role, RoleID: "0011223344556677", RunID: "0123456789abcdef", RequestID: "fedcba9876543210", Case: "inventory", ReviewedBPF: true}, Streams: map[string]FileIdentity{}, Snapshots: map[string]Subject{}}
	for fd, name := range []string{"request", "output", "stderr"} {
		s, _, err := subject(filepath.Join(dir, name), false, false, true, MaxOutputBytes)
		if err != nil {
			t.Fatal(err)
		}
		a.Streams[name] = s.Identity
		if flags, err := fdFlags(fd); err != nil || flags != 0 {
			t.Fatalf("stdio not inherited: %d %v", flags, err)
		}
	}
	var err error
	a.Fixture, _, err = subject(filepath.Join(dir, "fake-file"), false, false, true, 4096)
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := subject(filepath.Join(dir, "bpf"), false, false, true, 32768)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots["bpf"] = s
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		t.Fatal(err)
	}
	used := map[int]bool{}
	for _, fd := range injectionFDs() {
		if _, err := fdFlags(fd); err == nil {
			used[fd] = true
		} else if err != syscall.EBADF {
			t.Fatal(err)
		}
	}
	if err := vacantFDs(lim.Cur, used); err != nil {
		os.Exit(77)
	}
	for _, fd := range injectionFDs() {
		path := a.Fixture.Path
		if fd == BPFFD {
			path = s.Path
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Dup3(int(f.Fd()), fd, 0); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	a.TargetCommand = []string{os.Args[0], "-test.run=^TestDescriptorTarget$", "--", "descriptor-target", dir, role}
	return a
}

// Install a tiny test-only seccomp errno rule after preflight. Pin the goroutine
// before both thread-local prctl calls and keep it pinned through return until
// this isolated child execs or exits the whole process. The production terminal
// lock safely nests; its block contains no hooks or callbacks.
func denyTestSyscall(t *testing.T, number uint32, arg1 *uint32, errno syscall.Errno) {
	t.Helper()
	filters := []syscall.SockFilter{{Code: 0x20, K: 0}, {Code: 0x15, Jt: 0, Jf: 1, K: number}, {Code: 0x06, K: 0x00050000 | uint32(errno)}, {Code: 0x06, K: 0x7fff0000}}
	if arg1 != nil {
		filters = []syscall.SockFilter{{Code: 0x20, K: 0}, {Code: 0x15, Jf: 3, K: number}, {Code: 0x20, K: 24}, {Code: 0x15, Jf: 1, K: *arg1}, {Code: 0x06, K: 0x00050000 | uint32(errno)}, {Code: 0x06, K: 0x7fff0000}}
	}
	prog := syscall.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	runtime.LockOSThread()
	_, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, 38, 1, 0, 0, 0, 0)
	if e != 0 {
		os.Exit(77)
	}
	_, _, e = syscall.RawSyscall6(syscall.SYS_PRCTL, 22, 2, uintptr(unsafe.Pointer(&prog)), 0, 0, 0)
	runtime.KeepAlive(filters)
	if e != 0 {
		os.Exit(77)
	}
}

func TestDescriptorChild(t *testing.T) {
	args := descriptorChildArgs("descriptor-child")
	if args == nil {
		return
	}
	dir, role, scenario := args[0], args[1], args[2]
	if strings.HasPrefix(scenario, "typed-") {
		typedDescriptorChild(t, dir, role, strings.TrimPrefix(scenario, "typed-"))
		return
	}
	a := childAttempt(t, dir, role)
	// Preflight failures are produced using only owned regular file fixtures.
	switch scenario {
	case "identity":
		a.Streams["request"] = a.Fixture.Identity
	case "fake-identity":
		a.Fixture.Identity.Inode++
	case "fake-hash":
		a.Fixture.SHA256 = strings.Repeat("0", 64)
	case "bpf-hash":
		s := a.Snapshots["bpf"]
		s.SHA256 = strings.Repeat("0", 64)
		a.Snapshots["bpf"] = s
	case "bpf-identity":
		s := a.Snapshots["bpf"]
		s.Identity.Inode++
		a.Snapshots["bpf"] = s
	case "bpf-offset":
		syscall.Seek(BPFFD, 1, 0)
	case "bpf-write", "stdin-write", "stdout-read":
		fd, path, flags := BPFFD, a.Snapshots["bpf"].Path, syscall.O_RDWR
		if scenario == "stdin-write" {
			fd = 0
			path = filepath.Join(dir, "request")
		}
		if scenario == "stdout-read" {
			fd = 1
			path = filepath.Join(dir, "output")
			flags = syscall.O_RDONLY
		}
		f, err := syscall.Open(path, flags, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Dup3(f, fd, 0); err != nil {
			t.Fatal(err)
		}
		syscall.Close(f)
	case "stdio-cloexec":
		syscall.CloseOnExec(0)
	case "bpf-cloexec":
		syscall.CloseOnExec(BPFFD)
	case "missing-alias":
		syscall.Close(1000)
	case "no-append":
		if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, 1, syscall.F_SETFL, 0); e != 0 {
			t.Fatal(e)
		}
	case "bad-role":
		a.Config.Role = "other"
	case "legacy":
		a.DescriptorPolicy = ""
	}
	p, err := prepareDescriptorExec(a)
	if err != nil {
		os.Exit(42)
	}
	switch {
	case strings.HasPrefix(scenario, "deny-close-"):
		n, err := strconv.Atoi(strings.TrimPrefix(scenario, "deny-close-"))
		if err != nil {
			t.Fatal(err)
		}
		denyTestSyscall(t, 436, nil, syscall.Errno(n))
	case scenario == "deny-fstat":
		denyTestSyscall(t, syscall.SYS_FSTAT, nil, syscall.EACCES)
	case scenario == "deny-fcntl":
		n := uint32(syscall.F_SETFD)
		denyTestSyscall(t, syscall.SYS_FCNTL, &n, syscall.EPERM)
	case scenario == "noop-setfd":
		n := uint32(syscall.F_SETFD)
		denyTestSyscall(t, syscall.SYS_FCNTL, &n, 0)
	case scenario == "deny-getfl":
		n := uint32(syscall.F_GETFL)
		denyTestSyscall(t, syscall.SYS_FCNTL, &n, syscall.EPERM)
	case scenario == "deny-getfd":
		n := uint32(syscall.F_GETFD)
		denyTestSyscall(t, syscall.SYS_FCNTL, &n, syscall.EPERM)
	case scenario == "deny-lseek":
		denyTestSyscall(t, syscall.SYS_LSEEK, nil, syscall.EPERM)
	case scenario == "deny-exec":
		denyTestSyscall(t, syscall.SYS_EXECVE, nil, syscall.EACCES)
	case scenario == "exec-missing":
		p.command = []string{filepath.Join(dir, "missing-target")}
	case scenario == "post-identity":
		if err := syscall.Dup3(0, BPFFD, 0); err != nil {
			t.Fatal(err)
		}
	case scenario == "post-access":
		if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, BPFFD, syscall.F_SETFL, syscall.O_APPEND); e != 0 {
			t.Fatal(e)
		}
	case scenario == "post-offset":
		syscall.Seek(BPFFD, 1, 0)
	case scenario == "sibling-table":
		// Deterministically change a sibling's OLD table after a test-only
		// unshare. This fixture has ordinary synchronization; the production
		// terminal block below still has no callbacks or synchronization seam.
		ready, mutate, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			runtime.LockOSThread()
			close(ready)
			<-mutate
			if syscall.Dup3(FakeFD, BPFFD, 0) != nil || syscall.Dup3(FakeFD, 1001, 0) != nil {
				os.Exit(43)
			}
			close(done)
			select {}
		}()
		<-ready
		runtime.LockOSThread()
		_, _, e := syscall.RawSyscall(436, 3, uintptr(^uint32(0)), 2|4)
		if e != 0 {
			os.Exit(77)
		}
		for _, fd := range injectionFDs() {
			if _, _, e := syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, 0); e != 0 {
				os.Exit(43)
			}
		}
		close(mutate)
		<-done
	}
	p.exec()
	t.Fatal("terminal block returned")
}

func TestDescriptorTarget(t *testing.T) {
	args := descriptorChildArgs("descriptor-target")
	if args == nil {
		return
	}
	dir, role := args[0], args[1]
	if len(os.Environ()) != 0 {
		t.Fatal("target inherited environment")
	}
	fake, _, err := subject(filepath.Join(dir, "fake-file"), false, false, true, 4096)
	if err != nil {
		t.Fatal(err)
	}
	bpf, _, err := subject(filepath.Join(dir, "bpf"), false, false, true, 32768)
	if err != nil {
		t.Fatal(err)
	}
	a := &Attempt{Fixture: fake, Snapshots: map[string]Subject{"bpf": bpf}, Streams: map[string]FileIdentity{}}
	for _, name := range []string{"request", "output", "stderr"} {
		s, _, err := subject(filepath.Join(dir, name), false, false, true, MaxOutputBytes)
		if err != nil {
			t.Fatal(err)
		}
		a.Streams[name] = s.Identity
	}
	fds, err := (kernelMetadata{}).fds()
	if err != nil {
		t.Fatal(err)
	}
	if r := observeDescriptorIdentities(a, descriptorInventory(fds)); !r.Complete {
		t.Fatalf("real post-exec table lacks owned stdio anchors: %+v", r)
	}
	for _, d := range fds {
		isFake := d.Device == fake.Identity.Device && d.Inode == fake.Identity.Inode
		isBPF := d.Device == bpf.Identity.Device && d.Inode == bpf.Identity.Inode
		if (isFake || isBPF) && !permittedDescriptor(role, d.Number) {
			t.Fatalf("denied identity after exec at %d", d.Number)
		}
	}
	for _, fd := range append(injectionFDs(), 1001) {
		if permittedDescriptor(role, fd) {
			s := fake
			if fd == BPFFD {
				s = bpf
			}
			if err := verifyFD(fd, s, 32768); err != nil {
				t.Fatal(err)
			}
			flags, err := fdFlags(fd)
			if err != nil || flags != 0 {
				t.Fatalf("allowed descriptor flags %d %v", flags, err)
			}
		} else if _, err := fdFlags(fd); err != syscall.EBADF {
			t.Fatalf("denied number %d %v", fd, err)
		}
	}
	if role != "candidate" {
		if err := verifyFakeFD(FakeFD, fake); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := syscall.Write(1, []byte("target-ok\n")); err != nil {
		t.Fatal(err)
	}
}

func TestFinalDescriptorExecRolesAndAliases(t *testing.T) {
	for _, role := range []string{"candidate", "direct", "weakened-synthetic"} {
		t.Run(role, func(t *testing.T) {
			code, out, diag := runDescriptorChild(t, role, "ok")
			if code == 77 || code == 125 && (strings.Contains(diag, "stage=01 errno=0001") || strings.Contains(diag, "stage=01 errno=0038") || strings.Contains(diag, "stage=01 errno=0022")) {
				t.Skipf("kernel-restricted control remains unqualified: %s", diag)
			}
			if code != 0 || !strings.Contains(out, "target-ok") {
				t.Fatalf("code %d stdout %q stderr %q", code, out, diag)
			}
		})
	}
}

func TestFinalDescriptorPrivateTableAgainstSiblingMutation(t *testing.T) {
	code, out, diag := runDescriptorChild(t, "candidate", "sibling-table")
	if code == 77 {
		t.Skip("kernel-restricted private-table fixture remains unqualified")
	}
	if code != 0 || !strings.Contains(out, "target-ok") {
		t.Fatalf("code %d stdout %q stderr %q", code, out, diag)
	}
}

func TestFinalDescriptorPreflightRejects(t *testing.T) {
	for _, scenario := range []string{"identity", "fake-identity", "fake-hash", "bpf-hash", "bpf-identity", "bpf-offset", "bpf-write", "stdin-write", "stdout-read", "stdio-cloexec", "bpf-cloexec", "missing-alias", "no-append", "bad-role", "legacy"} {
		t.Run(scenario, func(t *testing.T) {
			code, out, diag := runDescriptorChild(t, "candidate", scenario)
			if code == 77 {
				t.Skip("sparse fixture unavailable; control unqualified")
			}
			if code != 42 || out != "" || diag != "" {
				t.Fatalf("code %d stdout %q stderr %q", code, out, diag)
			}
		})
	}
}

func TestFinalDescriptorTerminalFailuresExitWholeProcess(t *testing.T) {
	cases := []struct {
		scenario string
		stage    int
		errno    syscall.Errno
	}{
		{"deny-close-38", 1, syscall.ENOSYS}, {"deny-close-22", 1, syscall.EINVAL}, {"deny-close-1", 1, syscall.EPERM}, {"deny-close-12", 1, syscall.ENOMEM}, {"deny-close-24", 1, syscall.EMFILE},
		{"deny-fstat", 2, syscall.EACCES}, {"deny-fcntl", 6, syscall.EPERM}, {"deny-exec", 8, syscall.EACCES}, {"exec-missing", 8, syscall.ENOENT},
		{"noop-setfd", 7, syscall.EINVAL}, {"deny-getfl", 3, syscall.EPERM}, {"deny-getfd", 4, syscall.EPERM}, {"deny-lseek", 5, syscall.EPERM},
		{"post-identity", 2, syscall.ESTALE}, {"post-access", 3, syscall.EACCES}, {"post-offset", 5, syscall.EINVAL},
	}
	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			code, out, diag := runDescriptorChild(t, "candidate", tc.scenario)
			if code == 77 {
				t.Skip("kernel denied owned seccomp/sparse fixture; control unqualified")
			}
			want := fmt.Sprintf("final-fds stage=%02d errno=%04d\n", tc.stage, tc.errno)
			if tc.stage != 1 && strings.Contains(diag, "stage=01") {
				t.Skipf("close_range unavailable; terminal control unqualified: %s", diag)
			}
			if code != 125 || out != "" || diag != want {
				t.Fatalf("code %d stdout %q stderr %q want %q", code, out, diag, want)
			}
		})
	}
}

func TestSparseAliasesBoundedAndRecorded(t *testing.T) {
	a := &Attempt{DescriptorPolicy: FinalDescriptorPolicy, Streams: map[string]FileIdentity{}, Snapshots: map[string]Subject{}}
	descriptors := expectedDescriptors(a)
	if len(descriptors) != 8 {
		t.Fatal(descriptors)
	}
	for _, fd := range injectionFDs() {
		if vacantFDs(uint64(fd), nil) == nil || vacantFDs(1024, map[int]bool{fd: true}) == nil {
			t.Fatal("slot/rlimit accepted", fd)
		}
	}
	for _, d := range descriptors {
		if d.FD != BPFFD && d.FD > 2 && d.Role != "synthetic-alias" && d.Role != "synthetic-regular" {
			t.Fatal(d)
		}
	}
	if vacantFDs(1024, nil) != nil {
		t.Fatal("selected bounded slots rejected")
	}
	if PrivateFinalDescriptors("relative") == nil {
		t.Fatal("untyped root accepted")
	}
}

func descriptorStdio(a *Attempt) []fdMetadata {
	var fds []fdMetadata
	for fd, name := range []string{"request", "output", "stderr"} {
		id := a.Streams[name]
		fds = append(fds, fdMetadata{fd, id.Device, id.Inode, syscall.S_IFREG | 0600})
	}
	return fds
}

func descriptorInventory(fds []fdMetadata) Observation {
	o := Observation{Facts: []Fact{known("fds.count", strconv.Itoa(len(fds)))}}
	for j, d := range fds {
		o.Facts = append(o.Facts, known(fmt.Sprintf("fds.%d", j), fmt.Sprintf("fd=%d dev=%d ino=%d mode=%o", d.Number, d.Device, d.Inode, d.Mode)))
	}
	return o
}

func descriptorObservationAttempt() *Attempt {
	return &Attempt{DescriptorPolicy: FinalDescriptorPolicy, Config: HostConfig{Role: "candidate", Case: "inventory"}, Fixture: Subject{Identity: FileIdentity{Device: 1, Inode: 11}}, Snapshots: map[string]Subject{"bpf": {Identity: FileIdentity{Device: 1, Inode: 22}}}, Streams: map[string]FileIdentity{"request": {Device: 1, Inode: 31}, "output": {Device: 1, Inode: 32}, "stderr": {Device: 1, Inode: 33}}}
}

func TestDescriptorInventoryIdentityObservations(t *testing.T) {
	a := descriptorObservationAttempt()
	for _, tc := range []struct {
		role  string
		fds   []fdMetadata
		valid bool
	}{
		{"candidate", nil, true}, {"candidate", []fdMetadata{{1000, 1, 11, syscall.S_IFREG}}, false}, {"candidate", []fdMetadata{{99, 1, 22, syscall.S_IFREG}}, false},
		{"candidate", []fdMetadata{{BPFFD, 1, 999, syscall.S_IFREG}}, false}, {"candidate", []fdMetadata{{FakeFD, 1, 999, syscall.S_IFREG}}, false},
		{"direct", []fdMetadata{{FakeFD, 1, 11, syscall.S_IFREG}}, true}, {"direct", []fdMetadata{{FakeFD, 1, 11, syscall.S_IFREG}, {768, 1, 11, syscall.S_IFREG}}, false},
		{"weakened-synthetic", []fdMetadata{{64, 1, 11, syscall.S_IFREG}, {FakeFD, 1, 11, syscall.S_IFREG}, {768, 1, 11, syscall.S_IFREG}, {1000, 1, 11, syscall.S_IFREG}}, true},
	} {
		a.Config.Role = tc.role
		o := descriptorInventory(append(descriptorStdio(a), tc.fds...))
		r := observeDescriptorIdentities(a, o)
		if !r.Complete || (validateDescriptorObservations(a, o) == nil) != tc.valid {
			t.Fatal(tc, r)
		}
		if len(tc.fds) == 1 && tc.fds[0].Number == BPFFD && r.BPF513 != "reused" {
			t.Fatal("reuse erased", r)
		}
	}
	o := Observation{Facts: []Fact{known("fds.count", "1")}}
	if observeDescriptorIdentities(a, o).Complete || validateDescriptorObservations(a, o) == nil {
		t.Fatal("partial inventory accepted")
	}
	a.Config.Case = "fake-fd"
	a.Config.Role = "candidate"
	n := int(syscall.EBADF)
	o = Observation{Status: "observed", Errno: &n, Facts: []Fact{known("stage", "fstat")}}
	if validateDescriptorObservations(a, o) != nil {
		t.Fatal("exact EBADF rejected")
	}
	o.Facts = []Fact{known("stage", "fd-identity")}
	if validateDescriptorObservations(a, o) == nil {
		t.Fatal("identity mismatch accepted as closure")
	}
}

func TestDescriptorInventoryRequiresExactTableAndStdio(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Attempt, *Observation)
	}{
		{"zero", func(a *Attempt, o *Observation) { *o = descriptorInventory(nil) }},
		{"empty", func(a *Attempt, o *Observation) { o.Facts = nil }},
		{"missing-stdin", func(a *Attempt, o *Observation) {
			fds := descriptorStdio(a)
			fds[0].Number = 3
			*o = descriptorInventory(fds)
		}},
		{"missing-stdout", func(a *Attempt, o *Observation) {
			fds := descriptorStdio(a)
			*o = descriptorInventory([]fdMetadata{fds[0], fds[2], {99, 1, 999, syscall.S_IFREG}})
		}},
		{"missing-stderr", func(a *Attempt, o *Observation) {
			fds := descriptorStdio(a)
			fds[2].Number = 3
			*o = descriptorInventory(fds)
		}},
		{"wrong-stdin-device", func(a *Attempt, o *Observation) {
			fds := descriptorStdio(a)
			fds[0].Device++
			*o = descriptorInventory(fds)
		}},
		{"wrong-stdout-inode", func(a *Attempt, o *Observation) {
			fds := descriptorStdio(a)
			fds[1].Inode++
			*o = descriptorInventory(fds)
		}},
		{"wrong-stderr-type", func(a *Attempt, o *Observation) {
			fds := descriptorStdio(a)
			fds[2].Mode = syscall.S_IFCHR | 0600
			*o = descriptorInventory(fds)
		}},
		{"swapped-stdio", func(a *Attempt, o *Observation) {
			fds := descriptorStdio(a)
			fds[0].Number, fds[1].Number = 1, 0
			*o = descriptorInventory(fds)
		}},
		{"missing-durable-stream", func(a *Attempt, o *Observation) { delete(a.Streams, "stderr") }},
		{"missing-row", func(a *Attempt, o *Observation) { o.Facts = o.Facts[:3] }},
		{"count-too-small", func(a *Attempt, o *Observation) { o.Facts[0] = known("fds.count", "2") }},
		{"count-too-large", func(a *Attempt, o *Observation) { o.Facts[0] = known("fds.count", "4") }},
		{"count-over-cap", func(a *Attempt, o *Observation) { o.Facts[0] = known("fds.count", strconv.Itoa(MaxEntries+1)) }},
		{"count-negative", func(a *Attempt, o *Observation) { o.Facts[0] = known("fds.count", "-1") }},
		{"count-noncanonical", func(a *Attempt, o *Observation) { o.Facts[0] = known("fds.count", "03") }},
		{"duplicate-count", func(a *Attempt, o *Observation) { o.Facts = append(o.Facts, o.Facts[0]) }},
		{"duplicate-row", func(a *Attempt, o *Observation) { o.Facts = append(o.Facts, o.Facts[1]) }},
		{"duplicate-fd", func(a *Attempt, o *Observation) {
			fds := append(descriptorStdio(a), descriptorStdio(a)[0])
			*o = descriptorInventory(fds)
		}},
		{"undeclared-row", func(a *Attempt, o *Observation) {
			o.Facts = append(o.Facts, known("fds.3", "fd=1000 dev=1 ino=11 mode=100000"))
		}},
		{"out-of-range-row", func(a *Attempt, o *Observation) { o.Facts[1].Name = "fds.128" }},
		{"negative-row-index", func(a *Attempt, o *Observation) { o.Facts[1].Name = "fds.-1" }},
		{"noncanonical-row-index", func(a *Attempt, o *Observation) { o.Facts[1].Name = "fds.00" }},
		{"negative-fd", func(a *Attempt, o *Observation) { o.Facts[1] = known("fds.0", "fd=-1 dev=1 ino=31 mode=100600") }},
		{"malformed-row", func(a *Attempt, o *Observation) {
			o.Facts[1] = known("fds.0", "fd=0 dev=1 ino=31 mode=100600 trailing")
		}},
		{"unknown-count", func(a *Attempt, o *Observation) { o.Facts[0] = Fact{Name: "fds.count", State: "unknown"} }},
		{"unknown-row", func(a *Attempt, o *Observation) { o.Facts[1] = Fact{Name: "fds.0", State: "unknown"} }},
		{"absent-row-value", func(a *Attempt, o *Observation) { o.Facts[1] = Fact{Name: "fds.0", State: "known"} }},
		{"missing-row-state", func(a *Attempt, o *Observation) { o.Facts[1] = Fact{Name: "fds.0", State: "missing"} }},
		{"unknown-table", func(a *Attempt, o *Observation) { o.Facts = append(o.Facts, Fact{Name: "fds", State: "unknown"}) }},
		{"missing-table", func(a *Attempt, o *Observation) { o.Facts = append(o.Facts, Fact{Name: "fds", State: "missing"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := descriptorObservationAttempt()
			o := descriptorInventory(descriptorStdio(a))
			tc.mutate(a, &o)
			if r := observeDescriptorIdentities(a, o); r.Complete || validateDescriptorObservations(a, o) == nil {
				t.Fatalf("incomplete or contradictory inventory accepted: %+v", r)
			}
		})
	}
	// Row order and unrelated unavailable inventory families do not change
	// the exact FD index set or the owned stdio identity requirement.
	a := descriptorObservationAttempt()
	o := descriptorInventory(descriptorStdio(a))
	o.Facts[1], o.Facts[3] = o.Facts[3], o.Facts[1]
	o.Facts = append(o.Facts, Fact{Name: "namespace.net", State: "unknown"})
	if r := observeDescriptorIdentities(a, o); !r.Complete || validateDescriptorObservations(a, o) != nil {
		t.Fatalf("valid stdio-only table rejected: %+v", r)
	}
}

func TestLegacyRecordInspectAndPlanDigest(t *testing.T) {
	x := testHost(t) // This fixture deliberately retains the pre-policy schema.
	if err := x.c.run(x.a, x.h); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(x.c.Path, x.a.ID, "intent.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte("descriptor_policy")) || bytes.Contains(before, []byte("target_command")) {
		t.Fatal("zero fields changed legacy encoding")
	}
	legacyDigest := func(a *Attempt) string {
		// This exact anonymous structure is the pre-change digest encoding.
		b, _ := json.Marshal(struct {
			Config                   HostConfig
			Profile                  string
			Command, Launch, Cleanup []string
			Subjects, Snapshots      map[string]Subject
			Fixture                  Subject
			Request                  Request
			Streams                  map[string]FileIdentity
			Boot, FixtureNonce       string
			Control                  *ControlBinding
		}{a.Config, a.Profile, a.Command, a.Launch, a.Cleanup, a.Subjects, a.Snapshots, a.Fixture, a.Request, a.Streams, a.Boot, a.FixtureNonce, a.Control})
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	if planDigest(x.a) != legacyDigest(x.a) {
		t.Fatal("legacy plan digest changed")
	}
	if got := planDigest(&Attempt{}); got != "6e309093859a3d7652a3fd3c724596c57c8a64dd79dfd79db266dda04a49b3e1" {
		t.Fatal("empty legacy digest changed:", got)
	}
	x.c.Close()
	var inspected bytes.Buffer
	if err := InspectHost(x.c.Path, x.a.ID, &inspected); err != nil {
		t.Fatal(err)
	}
	var a Attempt
	if err := decodeHostRecord(&inspected, &a); err != nil || a.DescriptorPolicy != "" || a.PlanSHA256 != legacyDigest(&a) || len(expectedDescriptors(&a)) != 5 {
		t.Fatal("legacy inspection changed", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy record rewritten", err)
	}
	if PrivateInject(filepath.Dir(path)) == nil || PrivateFinalDescriptors(filepath.Dir(path)) == nil {
		t.Fatal("legacy launch accepted")
	}
}

func TestPrepareAlwaysBindsCurrentFinalDescriptorChain(t *testing.T) {
	x := testHost(t)
	config := x.a.Config
	config.Campaign = t.TempDir()
	if err := os.Chmod(config.Campaign, 0700); err != nil {
		t.Fatal(err)
	}
	// Inert ELF subjects validate the concrete plan without launching anything.
	for _, name := range []string{"driver", "probe", "bwrap", "bpf"} {
		data := launcherBytes("", elf.DT_NULL, "")
		if name == "bpf" {
			data = make([]byte, 8)
		}
		if err := os.Chmod(selected(config)[name], 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(selected(config)[name], data, 0500); err != nil {
			t.Fatal(err)
		}
	}
	c, err := OpenCampaign(config.Campaign)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a, err := c.Prepare(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Path, a.ID)
	if a.DescriptorPolicy != FinalDescriptorPolicy || !sameJSON(a.Command, []string{filepath.Join(path, "driver"), "private-final-descriptors", path}) || a.TargetCommand[0] != filepath.Join(path, "probe") || strings.Contains(strings.Join(a.TargetCommand, " "), "private-final") {
		t.Fatal("recursive or unbound target", a.Command, a.TargetCommand)
	}
	if _, err := c.read(a.ID); err != nil {
		t.Fatal("new intent not readable", err)
	}
	for _, which := range []string{"policy", "command", "target", "launch", "bpf", "stdio"} {
		b, _ := json.Marshal(a)
		var mutated Attempt
		json.Unmarshal(b, &mutated)
		switch which {
		case "policy":
			mutated.DescriptorPolicy = "unknown"
		case "command":
			mutated.Command[1] = "private-inject"
		case "target":
			mutated.TargetCommand[0] = "/owned/other"
		case "launch":
			mutated.Launch = append(mutated.Launch, "other")
		case "bpf":
			s := mutated.Snapshots["bpf"]
			s.SHA256 = strings.Repeat("0", 64)
			mutated.Snapshots["bpf"] = s
		case "stdio":
			mutated.Streams["request"] = mutated.Fixture.Identity
		}
		if planDigest(&mutated) == a.PlanSHA256 || validatePlan(&mutated, path) == nil {
			t.Fatal("unbound plan change", which)
		}
	}
	if PrivateInject(path) == nil || PrivateFinalDescriptors(path) == nil {
		t.Fatal("unreleased new intent accepted")
	}
	for _, role := range []string{"direct", "candidate", "weakened-synthetic"} {
		a.Config.Role = role
		args := targetCommand(a, path)
		if (args[0] == filepath.Join(path, "probe")) != (role == "direct") {
			t.Fatal("wrong role target", role, args)
		}
	}
	a.Config.Role = "weakened-synthetic"
	a.Config.Case = "hold"
	if a.Config.validate() == nil {
		t.Fatal("ordinary weakened bypass accepted")
	}
}

func currentDescriptorFixture(t *testing.T, x *hostFixture) {
	t.Helper()
	a := x.a
	a.DescriptorPolicy = FinalDescriptorPolicy
	path := filepath.Join(x.c.Path, a.ID)
	a.Command = command(a, path)
	a.TargetCommand = targetCommand(a, path)
	i, err := readInjection(filepath.Join(path, "injection.json"))
	if err != nil {
		t.Fatal(err)
	}
	i.DescriptorPolicy = a.DescriptorPolicy
	i.Descriptors = expectedDescriptors(a)
	b, _ := json.Marshal(i)
	if err := os.Remove(filepath.Join(path, "injection.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "injection.json"), b, 0400); err != nil {
		t.Fatal(err)
	}
	a.PlanSHA256 = planDigest(a)
	copy := *a
	copy.Injector = &i
	r := descriptorReceipt(&copy)
	b, _ = json.Marshal(r)
	if err := exclusiveFile(filepath.Join(path, "descriptor-policy.json"), b, 0400); err != nil {
		t.Fatal(err)
	}
	if err := x.c.Save(a, "prepared", ""); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentDescriptorCollectionPreservesFailedIsolation(t *testing.T) {
	for _, kind := range []string{"stdio-only", "leak", "zero", "missing-stdio", "wrong-stdio", "nonregular-stdio", "count-mismatch", "unavailable-table"} {
		t.Run(kind, func(t *testing.T) {
			x := testHost(t)
			x.a.Config.Role = "candidate"
			currentDescriptorFixture(t, x)
			fds := descriptorStdio(x.a)
			switch kind {
			case "leak":
				fds = append(fds, fdMetadata{1000, x.a.Fixture.Identity.Device, x.a.Fixture.Identity.Inode, syscall.S_IFREG})
			case "zero":
				fds = nil
			case "missing-stdio":
				fds = fds[1:]
			case "wrong-stdio":
				fds[1].Inode++
			case "nonregular-stdio":
				fds[2].Mode = syscall.S_IFIFO
			}
			facts := descriptorInventory(fds).Facts
			if kind == "count-mismatch" {
				facts[0] = known("fds.count", "4")
			}
			status, reason := "observed", ""
			if kind == "unavailable-table" {
				facts = append(facts, Fact{Name: "fds", State: "unknown"})
				status, reason = "inconclusive", "inventory incomplete or unavailable"
			}
			o := Observation{Version: 1, RunID: x.a.Config.RunID, RequestID: x.a.Config.RequestID, Case: CaseInventory, Status: status, Reason: reason, Facts: facts}
			var output bytes.Buffer
			if err := EncodeObservation(&output, o); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(x.c.Path, x.a.ID, "output"), output.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			err := x.c.run(x.a, x.h)
			if (err != nil) != (kind != "stdio-only") || !x.a.Empty || x.a.Phase != "complete" || x.a.DescriptorReceipt == nil || x.a.Observation == nil || x.a.DescriptorObservation == nil {
				t.Fatal("lifecycle or observation erased", err)
			}
			if x.a.DescriptorObservation.Complete != (kind == "stdio-only" || kind == "leak") {
				t.Fatal("incorrect inventory completeness", x.a.DescriptorObservation)
			}
			x.c.Close()
			c, err := OpenCampaign(x.c.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			stored := c.Records[x.a.ID]
			if stored == nil || c.Blocked || !sameJSON(stored.DescriptorObservation, x.a.DescriptorObservation) || !sameJSON(stored.Observation, &o) {
				t.Fatal("failed isolation evidence not retained")
			}
		})
	}
}

func TestCurrentDescriptorReceiptAndInjectorBinding(t *testing.T) {
	x := testHost(t)
	currentDescriptorFixture(t, x)
	bindFixture(t, x)
	r := descriptorReceipt(x.a)
	if validateDescriptorReceipt(x.a, r) != nil {
		t.Fatal("current receipt rejected")
	}
	for _, which := range []string{"plan", "policy", "invocation", "process", "allow", "target", "environment", "stage"} {
		b, _ := json.Marshal(r)
		var changed DescriptorReceipt
		json.Unmarshal(b, &changed)
		switch which {
		case "plan":
			changed.PlanSHA256 = strings.Repeat("0", 64)
		case "policy":
			changed.Policy = ""
		case "invocation":
			changed.Invocation = strings.Repeat("c", 32)
		case "process":
			changed.Process.Start++
		case "allow":
			changed.Allowed = append(changed.Allowed, 1000)
		case "target":
			changed.Command = []string{"/owned/other"}
		case "environment":
			changed.EmptyEnvironment = false
		case "stage":
			changed.Stage = "closure-proved"
		}
		if validateDescriptorReceipt(x.a, changed) == nil {
			t.Fatal("receipt change accepted", which)
		}
	}
	i := *x.a.Injector
	i.DescriptorPolicy = ""
	if validateInjection(x.a, i, x.service) == nil {
		t.Fatal("legacy injection rebound as hardened")
	}
	i = *x.a.Injector
	i.Descriptors = i.Descriptors[:len(i.Descriptors)-1]
	if validateInjection(x.a, i, x.service) == nil {
		t.Fatal("missing role identity accepted")
	}
}
