//go:build linux

package assert

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultMountInfoPath is the mount table of the calling process.
const DefaultMountInfoPath = "/proc/self/mountinfo"

// HostSystem is the real System, for Linux. The zero value is ready to use.
// It starts no process, creates no file and sends nothing on a connection.
type HostSystem struct {
	// MountInfoPath overrides DefaultMountInfoPath (tests give a file).
	MountInfoPath string
}

var _ System = HostSystem{}

// Lstat reports on the path without following a link.
func (HostSystem) Lstat(path string) (FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Mode: fi.Mode(), Size: fi.Size()}, nil
}

// ReadFile opens the path without following a link and without blocking,
// refuses anything but a regular file, and reads at most limit bytes: a file
// that has more is refused with ErrTooLarge, not truncated.
func (HostSystem) ReadFile(path string, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("assert: read %s: the limit must be positive", path)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ELOOP {
			return nil, fmt.Errorf("assert: read %s: %w", path, ErrNotRegular)
		}
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("assert: read %s: %w", path, ErrNotRegular)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, &fs.PathError{Op: "read", Path: path, Err: err}
	}
	if len(data) > limit {
		return nil, fmt.Errorf("assert: read %s: %w (limit %d)", path, ErrTooLarge, limit)
	}
	return data, nil
}

// Mounts reads and parses the mount table, at most MaxMountTableBytes of it.
func (h HostSystem) Mounts() ([]Mount, error) {
	path := h.MountInfoPath
	if path == "" {
		path = DefaultMountInfoPath
	}
	data, err := h.ReadFile(path, MaxMountTableBytes)
	if err != nil {
		return nil, err
	}
	return ParseMountInfo(data)
}

// Environ returns the environment of the process.
func (HostSystem) Environ() []string { return os.Environ() }

// Dial connects with the timeout and closes the connection at once. Only the
// networks "tcp" and "unix" are allowed.
func (HostSystem) Dial(network, address string, timeout time.Duration) error {
	if network != "tcp" && network != "unix" {
		return fmt.Errorf("assert: dial: network %q is not allowed", network)
	}
	d := net.Dialer{Timeout: timeout}
	c, err := d.Dial(network, address)
	if err != nil {
		return err
	}
	return c.Close()
}

// LookupHost resolves the name with the process's resolver within the timeout.
func (HostSystem) LookupHost(host string, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}

// Writable asks access(2) with W_OK. A refusal for permission, a read-only
// file system, an immutable file or a busy executable is false and no error.
func (HostSystem) Writable(path string) (bool, error) {
	switch err := unix.Access(path, unix.W_OK); err {
	case nil:
		return true, nil
	case unix.EACCES, unix.EROFS, unix.EPERM, unix.ETXTBSY:
		return false, nil
	default:
		return false, &fs.PathError{Op: "access", Path: path, Err: err}
	}
}
