package hookport

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Native x86-64 ABI only. Neither the compat ABI nor x32 uses this table.
var denied = [...]uint32{
	unix.SYS_EXECVE, unix.SYS_EXECVEAT,
	unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT,
	unix.SYS_PTRACE, unix.SYS_PIDFD_GETFD, unix.SYS_PROCESS_VM_WRITEV,
	unix.SYS_UNSHARE, unix.SYS_SETNS,
	unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT,
	unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_FSPICK,
	unix.SYS_OPEN_TREE, unix.SYS_OPEN_TREE_ATTR, unix.SYS_MOVE_MOUNT, unix.SYS_MOUNT_SETATTR,
	unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	unix.SYS_CLONE3,
}

// CLONE_NEWTIME occupies a bit of legacy clone's exit signal, not a
// namespace flag on that ABI. unshare and clone3 cover time namespaces.
const cloneNamespaces = unix.CLONE_NEWNS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWUTS | unix.CLONE_NEWIPC | unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNET

func holderProgram() []unix.SockFilter {
	const (
		load = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		eq   = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
		bits = unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K
		ret  = unix.BPF_RET | unix.BPF_K
	)
	p := []unix.SockFilter{
		{Code: load, K: 4}, // seccomp_data.arch
		{Code: eq, K: unix.AUDIT_ARCH_X86_64, Jt: 1},
		{Code: ret, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: load, K: 0}, // nr
		{Code: bits, K: 0x40000000, Jf: 1},
		{Code: ret, K: unix.SECCOMP_RET_KILL_PROCESS},
	}
	for _, nr := range denied {
		errno := unix.EPERM
		if nr == unix.SYS_CLONE3 {
			errno = unix.ENOSYS // libc can fall back to clone
		}
		p = append(p, unix.SockFilter{Code: eq, K: nr, Jf: 1}, unix.SockFilter{Code: ret, K: unix.SECCOMP_RET_ERRNO | uint32(errno)})
	}
	return append(p,
		unix.SockFilter{Code: eq, K: unix.SYS_CLONE, Jf: 3},
		unix.SockFilter{Code: load, K: 16}, // args[0], low word on x86-64
		unix.SockFilter{Code: bits, K: cloneNamespaces, Jf: 1},
		unix.SockFilter{Code: ret, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		unix.SockFilter{Code: ret, K: unix.SECCOMP_RET_ALLOW})
}

// InstallFilter irreversibly restricts this process. The holder calls it
// after starting the sandbox and opening its listeners, before serving.
func InstallFilter() (Filtered, error) {
	program := holderProgram()
	b, err := binary.Append(nil, binary.LittleEndian, program)
	if err != nil {
		return Filtered{}, err
	}
	if err := install(program); err != nil {
		return Filtered{}, fmt.Errorf("hookport: holder filter unavailable: %w", err)
	}
	return Filtered{digest: sha256.Sum256(b)}, nil
}

func install(program []unix.SockFilter) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	fp := unix.SockFprog{Len: uint16(len(program)), Filter: &program[0]}
	tid, _, errno := unix.RawSyscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&fp)))
	runtime.KeepAlive(program)
	if errno != 0 {
		return errno
	}
	if tid != 0 {
		return errors.New("incomplete filter thread synchronization")
	}
	return nil
}
