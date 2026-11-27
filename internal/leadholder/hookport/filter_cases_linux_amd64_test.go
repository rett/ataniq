package hookport

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"ataniq/internal/leadholder/hookport/testdata/probe"
	"golang.org/x/sys/unix"
)

// Each entry describes one valid operation. All descriptors, paths, child
// processes and namespace privileges it needs are prepared before filtering.
type operation struct {
	name      string
	nr        uint32
	flag      uintptr
	namespace bool
}

func operations() []operation {
	var ops []operation
	for _, c := range []struct {
		name string
		nr   uint32
		ns   bool
	}{
		{"execve", unix.SYS_EXECVE, false}, {"execveat", unix.SYS_EXECVEAT, false},
		{"socket", unix.SYS_SOCKET, false}, {"socketpair", unix.SYS_SOCKETPAIR, false}, {"connect", unix.SYS_CONNECT, false},
		{"ptrace", unix.SYS_PTRACE, false}, {"pidfd_getfd", unix.SYS_PIDFD_GETFD, false}, {"process_vm_writev", unix.SYS_PROCESS_VM_WRITEV, false},
		{"setns", unix.SYS_SETNS, true}, {"mount", unix.SYS_MOUNT, true}, {"umount2", unix.SYS_UMOUNT2, true}, {"pivot_root", unix.SYS_PIVOT_ROOT, true},
		{"fsopen", unix.SYS_FSOPEN, true}, {"fsconfig", unix.SYS_FSCONFIG, true}, {"fsmount", unix.SYS_FSMOUNT, true}, {"fspick", unix.SYS_FSPICK, true},
		{"open_tree", unix.SYS_OPEN_TREE, true}, {"open_tree_attr", unix.SYS_OPEN_TREE_ATTR, true}, {"move_mount", unix.SYS_MOVE_MOUNT, true}, {"mount_setattr", unix.SYS_MOUNT_SETATTR, true},
		{"io_uring_setup", unix.SYS_IO_URING_SETUP, false}, {"io_uring_enter", unix.SYS_IO_URING_ENTER, false}, {"io_uring_register", unix.SYS_IO_URING_REGISTER, false},
		{"clone3", unix.SYS_CLONE3, false},
	} {
		ops = append(ops, operation{name: c.name, nr: c.nr, namespace: c.ns})
	}
	for _, ns := range []struct {
		name string
		flag uintptr
	}{
		{"mnt", unix.CLONE_NEWNS}, {"cgroup", unix.CLONE_NEWCGROUP}, {"uts", unix.CLONE_NEWUTS}, {"ipc", unix.CLONE_NEWIPC},
		{"user", unix.CLONE_NEWUSER}, {"pid", unix.CLONE_NEWPID}, {"net", unix.CLONE_NEWNET}, {"time", unix.CLONE_NEWTIME},
	} {
		ops = append(ops, operation{"unshare-" + ns.name, unix.SYS_UNSHARE, ns.flag, true})
		if ns.flag != unix.CLONE_NEWTIME {
			ops = append(ops, operation{"clone-" + ns.name, unix.SYS_CLONE, ns.flag, true})
		}
	}
	return ops
}

type call func() error

// Discard descriptor results in these one-operation children. Process exit
// releases them, including in a control whose call replaces the process.
func onlyErr(_ int, err error) error { return err }

func sysErr(e syscall.Errno) error {
	if e == 0 {
		return nil
	}
	return e
}

// forkResult never lets the raw fork child run Go with copied runtime
// locks. For unshare it also supplies the single-threaded calling process
// required by CLONE_NEWUSER and CLONE_NEWPID.
func forkResult(nr, arg, size, after, flag uintptr) error {
	pid := probe.ForkCall(nr, arg, size, after, flag)
	if pid < 0 {
		return syscall.Errno(-pid)
	}
	var s unix.WaitStatus
	var err error
	for {
		_, err = unix.Wait4(int(pid), &s, 0, nil)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return err
	}
	if !s.Exited() {
		return fmt.Errorf("fork child wait status %v", s)
	}
	if s.ExitStatus() == trapExit {
		os.Exit(trapExit)
	}
	return sysErr(syscall.Errno(s.ExitStatus()))
}

func prepare(op operation, dir, marker string) (call, error) {
	switch op.nr {
	case unix.SYS_EXECVE:
		return func() error { return unix.Exec(marker, []string{marker}, nil) }, nil
	case unix.SYS_EXECVEAT:
		fd, err := unix.Open(marker, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		empty := byte(0)
		name, _ := unix.BytePtrFromString("marker")
		argv := [2]*byte{name, nil}
		return func() error {
			_, _, e := unix.Syscall6(unix.SYS_EXECVEAT, uintptr(fd), uintptr(unsafe.Pointer(&empty)), uintptr(unsafe.Pointer(&argv[0])), 0, unix.AT_EMPTY_PATH, 0)
			return sysErr(e)
		}, nil
	case unix.SYS_SOCKET:
		return func() error { return onlyErr(unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)) }, nil
	case unix.SYS_SOCKETPAIR:
		return func() error { _, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0); return err }, nil
	case unix.SYS_CONNECT:
		// The checked directory can have a long name. Bind and dial it through
		// the same short, pinned directory path.
		l, err := Listen(dir)
		if err != nil {
			return nil, err
		}
		d, err := unix.Open(dir, unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			return nil, err
		}
		return func() error {
			err := unix.Connect(fd, &unix.SockaddrUnix{Name: fmt.Sprintf("/proc/self/fd/%d/port", d)})
			runtime.KeepAlive(l)
			return err
		}, nil
	case unix.SYS_PTRACE, unix.SYS_PIDFD_GETFD, unix.SYS_PROCESS_VM_WRITEV:
		return childOperation(op.nr, marker)
	case unix.SYS_CLONE:
		return func() error { return forkResult(unix.SYS_CLONE, op.flag|uintptr(unix.SIGCHLD), 0, 0, 0) }, nil
	case unix.SYS_CLONE3:
		args := [11]uint64{4: uint64(unix.SIGCHLD)}
		return func() error {
			err := forkResult(unix.SYS_CLONE3, uintptr(unsafe.Pointer(&args)), unsafe.Sizeof(args), 0, 0)
			runtime.KeepAlive(&args)
			return err
		}, nil
	case unix.SYS_UNSHARE:
		return func() error { return forkResult(unix.SYS_CLONE, uintptr(unix.SIGCHLD), 0, unix.SYS_UNSHARE, op.flag) }, nil
	case unix.SYS_SETNS:
		fd, err := unix.Open("/proc/self/ns/uts", unix.O_RDONLY, 0)
		return func() error { return unix.Setns(fd, unix.CLONE_NEWUTS) }, err
	case unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER:
		return ringOperation(op.nr)
	default:
		return mountOperation(op.nr, dir)
	}
}

func childOperation(nr uint32, marker string) (call, error) {
	child := exec.Command(marker, "target")
	child.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	input, err := child.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := child.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = child.Start(); err != nil {
		return nil, err
	}
	var address uintptr
	if _, err = fmt.Fscanf(bufio.NewReader(out), "%x", &address); err != nil {
		return nil, err
	}
	pid := child.Process.Pid
	var invoke call
	switch nr {
	case unix.SYS_PTRACE:
		invoke = func() error { return unix.PtraceSeize(pid) }
	case unix.SYS_PIDFD_GETFD:
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			return nil, err
		}
		invoke = func() error { return onlyErr(unix.PidfdGetfd(fd, 0, 0)) }
	case unix.SYS_PROCESS_VM_WRITEV:
		// Only the byte explicitly supplied by this fixture child.
		sample := byte(1)
		invoke = func() error {
			n, err := unix.ProcessVMWritev(pid, []unix.Iovec{{Base: &sample, Len: 1}}, []unix.RemoteIovec{{Base: address, Len: 1}}, 0)
			if err == nil && n != 1 {
				return errors.New("sample byte not written")
			}
			return err
		}
	}
	return func() error { err := invoke(); runtime.KeepAlive(input); return err }, nil
}

func mountOperation(nr uint32, dir string) (call, error) {
	// Called only inside the case's private user and mount namespaces.
	if err := unix.Mount("", "/", "", unix.MS_PRIVATE|unix.MS_REC, ""); err != nil {
		return nil, err
	}
	target := filepath.Join(dir, "mount")
	if err := os.Mkdir(target, 0700); err != nil {
		return nil, err
	}
	mount := func() error { return unix.Mount("tmpfs", target, "tmpfs", 0, "size=1m") }
	switch nr {
	case unix.SYS_MOUNT:
		return mount, nil
	case unix.SYS_FSOPEN:
		return func() error { return onlyErr(unix.Fsopen("tmpfs", 0)) }, nil
	case unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_MOVE_MOUNT:
		fs, err := unix.Fsopen("tmpfs", 0)
		if err != nil {
			return nil, err
		}
		if nr == unix.SYS_FSCONFIG {
			return func() error { return unix.FsconfigCreate(fs) }, nil
		}
		if err = unix.FsconfigCreate(fs); err != nil {
			return nil, err
		}
		if nr == unix.SYS_FSMOUNT {
			return func() error { return onlyErr(unix.Fsmount(fs, 0, 0)) }, nil
		}
		m, err := unix.Fsmount(fs, 0, 0)
		if err != nil {
			return nil, err
		}
		return func() error { return unix.MoveMount(m, "", unix.AT_FDCWD, target, unix.MOVE_MOUNT_F_EMPTY_PATH) }, nil
	}
	if err := mount(); err != nil {
		return nil, err
	}
	switch nr {
	case unix.SYS_UMOUNT2:
		return func() error { return unix.Unmount(target, 0) }, nil
	case unix.SYS_PIVOT_ROOT:
		old := filepath.Join(target, "old")
		if err := os.Mkdir(old, 0700); err != nil {
			return nil, err
		}
		return func() error { return unix.PivotRoot(target, old) }, nil
	case unix.SYS_FSPICK:
		return func() error { return onlyErr(unix.Fspick(unix.AT_FDCWD, target, 0)) }, nil
	case unix.SYS_OPEN_TREE:
		return func() error { return onlyErr(unix.OpenTree(unix.AT_FDCWD, target, unix.OPEN_TREE_CLONE)) }, nil
	case unix.SYS_MOUNT_SETATTR:
		return func() error {
			return unix.MountSetattr(unix.AT_FDCWD, target, 0, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY})
		}, nil
	case unix.SYS_OPEN_TREE_ATTR:
		name, err := unix.BytePtrFromString(target)
		if err != nil {
			return nil, err
		}
		attr := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY}
		return func() error {
			dirfd := unix.AT_FDCWD
			_, _, e := unix.Syscall6(unix.SYS_OPEN_TREE_ATTR, uintptr(dirfd), uintptr(unsafe.Pointer(name)), unix.OPEN_TREE_CLONE, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
			return sysErr(e)
		}, nil
	}
	return nil, errors.New("missing mount operation")
}

func ringOperation(nr uint32) (call, error) {
	setup := func() (int, error) {
		// sizeof(struct io_uring_params), zero flags, one submission entry.
		var params [120]byte
		fd, _, e := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, uintptr(unsafe.Pointer(&params)), 0)
		return int(fd), sysErr(e)
	}
	if nr == unix.SYS_IO_URING_SETUP {
		return func() error { return onlyErr(setup()) }, nil
	}
	fd, err := setup()
	if err != nil {
		return nil, err
	}
	return func() error {
		if nr == unix.SYS_IO_URING_ENTER {
			_, _, e := unix.Syscall6(unix.SYS_IO_URING_ENTER, uintptr(fd), 0, 0, 0, 0, 0)
			return sysErr(e)
		}
		var buf [16 + 256*8]byte // io_uring_probe and its probe_ops
		_, _, e := unix.Syscall6(unix.SYS_IO_URING_REGISTER, uintptr(fd), 8, uintptr(unsafe.Pointer(&buf)), 256, 0, 0)
		return sysErr(e)
	}, nil
}

// A client connects before installation. The listener, SO_PEERPIDFD lookup
// and actual data transfer can then be exercised separately under the filter.
func socketSetup(dir string) (net.Listener, net.Conn, error) {
	l, err := Listen(dir)
	if err != nil {
		return nil, nil, err
	}
	d, err := unix.Open(dir, unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		l.Close()
		return nil, nil, err
	}
	defer unix.Close(d)
	c, err := net.Dial("unix", fmt.Sprintf("/proc/self/fd/%d/port", d))
	if err != nil {
		l.Close()
		return nil, nil, err
	}
	return l, c, nil
}
