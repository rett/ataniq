//go:build linux

package serve

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// openRuntimeDir opens the runtime folder and checks it on the descriptor, so
// that what was checked is what the sockets are made in: a directory (a
// symlink is refused), owned by this user, with mode exactly 0700. The caller
// closes the descriptor.
func openRuntimeDir(dir string) (int, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		switch {
		case errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR):
			return -1, fmt.Errorf("serve: the runtime folder %s is a symlink or not a directory", dir)
		case errors.Is(err, unix.ENOENT):
			return -1, fmt.Errorf("serve: the runtime folder %s does not exist", dir)
		}
		return -1, fmt.Errorf("serve: open the runtime folder %s: %w", dir, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("serve: stat the runtime folder %s: %w", dir, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != os.Geteuid() {
		unix.Close(fd)
		return -1, fmt.Errorf("serve: the runtime folder %s is not a directory of this user", dir)
	}
	if perm := st.Mode & 0o7777; perm != 0o700 {
		unix.Close(fd)
		return -1, fmt.Errorf("serve: the runtime folder %s has mode %04o, want 0700", dir, perm)
	}
	return fd, nil
}

// listenIn makes the socket name in the folder dirfd with mode 0600 and
// returns its listener. A stale socket of this user is removed first; anything
// else of that name is an error. The socket is bound, set to 0600 and only
// then made to listen, so that no connection is possible while its mode is the
// umask's; the folder is 0700 besides. The path is made through
// /proc/self/fd, which keeps it short (a socket path has about 100 bytes) and
// ties it to the folder that was checked.
func listenIn(dirfd int, name string) (*net.UnixListener, error) {
	var st unix.Stat_t
	switch err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err == nil:
		if st.Mode&unix.S_IFMT != unix.S_IFSOCK || int(st.Uid) != os.Geteuid() {
			return nil, fmt.Errorf("serve: %s exists and is not a socket of this user", name)
		}
		if err := unix.Unlinkat(dirfd, name, 0); err != nil {
			return nil, fmt.Errorf("serve: remove the stale socket %s: %w", name, err)
		}
	case errors.Is(err, unix.ENOENT):
	default:
		return nil, fmt.Errorf("serve: stat %s: %w", name, err)
	}
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("serve: socket: %w", err)
	}
	fail := func(err error) (*net.UnixListener, error) {
		unix.Close(fd)
		_ = unix.Unlinkat(dirfd, name, 0)
		return nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: fmt.Sprintf("/proc/self/fd/%d/%s", dirfd, name)}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("serve: bind %s: %w", name, err)
	}
	if err := unix.Fchmodat(dirfd, name, 0o600, 0); err != nil {
		return fail(fmt.Errorf("serve: chmod %s: %w", name, err))
	}
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&0o7777 != 0o600 {
		return fail(fmt.Errorf("serve: the socket %s does not have mode 0600", name))
	}
	if err := unix.Listen(fd, 16); err != nil {
		return fail(fmt.Errorf("serve: listen %s: %w", name, err))
	}
	f := os.NewFile(uintptr(fd), name)
	ln, err := net.FileListener(f)
	f.Close()
	if err != nil {
		_ = unix.Unlinkat(dirfd, name, 0)
		return nil, fmt.Errorf("serve: listen %s: %w", name, err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		ln.Close()
		_ = unix.Unlinkat(dirfd, name, 0)
		return nil, fmt.Errorf("serve: %s is not a unix listener", name)
	}
	ul.SetUnlinkOnClose(false)
	return ul, nil
}
