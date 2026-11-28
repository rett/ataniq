package hookport

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// Listen binds port in a fresh, private intent directory supplied by the
// holder. It refuses existing entries named port. The caller owns removal of
// the intent directory and socket after stopping the loop.
func Listen(dir string) (net.Listener, error) {
	d, err := unix.Open(dir, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(d)
	var st unix.Stat_t
	if err := unix.Fstat(d, &st); err != nil {
		return nil, err
	}
	if int(st.Uid) != os.Geteuid() || st.Mode&07777 != 0700 {
		return nil, errors.New("hookport: intent directory must be private and owned by the holder")
	}
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}
	f := os.NewFile(uintptr(fd), "port")
	defer f.Close()
	if err = unix.Bind(fd, &unix.SockaddrUnix{Name: fmt.Sprintf("/proc/self/fd/%d/port", d)}); err != nil {
		return nil, fmt.Errorf("bind: %w", err)
	}
	ready := false
	defer func() {
		if !ready {
			_ = unix.Unlinkat(d, "port", 0)
		}
	}()
	if err = unix.Fchmodat(d, "port", 0600, 0); err != nil {
		return nil, err
	}
	if err = unix.Fstatat(d, "port", &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if st.Mode&(unix.S_IFMT|07777) != unix.S_IFSOCK|0600 || int(st.Uid) != os.Geteuid() {
		return nil, errors.New("hookport: unsafe socket entry")
	}
	if err = unix.Listen(fd, 0); err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	l, err := net.FileListener(f)
	if err != nil {
		return nil, err
	}
	ready = true
	return l, nil
}
