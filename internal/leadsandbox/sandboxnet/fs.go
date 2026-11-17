package sandboxnet

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// FS is the small view of the host file system that DeriveFromFS needs. Tests
// pass a fake; [HostFS] is the real one.
type FS interface {
	// ReadFile returns at most limit bytes of the named file. A file that is
	// absent is reported with an error for which errors.Is(err,
	// fs.ErrNotExist) holds.
	ReadFile(name string, limit int) ([]byte, error)
	// RealPath returns name with every symbolic link resolved.
	RealPath(name string) (string, error)
}

// DeriveFromFS reads the host's /etc/resolv.conf, where it resolves to, and
// the systemd-resolved upstream file, and calls [Derive].
//
// Each file is read with a limit one byte above [MaxResolvConfBytes], so an
// oversized file is detected and refused rather than truncated. An absent
// upstream file is normal (the host does not run systemd-resolved). Any other
// failure to read a file is returned as an ordinary error, not as a refusal.
func DeriveFromFS(fsys FS) (Result, error) {
	host, err := fsys.ReadFile(HostResolvConfPath, MaxResolvConfBytes+1)
	if err != nil {
		return Result{}, fmt.Errorf("sandboxnet: read host resolv.conf: %w", err)
	}
	real, err := fsys.RealPath(HostResolvConfPath)
	if err != nil {
		return Result{}, fmt.Errorf("sandboxnet: resolve host resolv.conf: %w", err)
	}
	in := Input{HostContent: host, HostRealPath: real}
	up, err := fsys.ReadFile(SystemdUpstreamPath, MaxResolvConfBytes+1)
	switch {
	case err == nil:
		in.UpstreamPresent = true
		in.UpstreamContent = up
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		// systemd-resolved is not running here.
	default:
		return Result{}, fmt.Errorf("sandboxnet: read systemd-resolved upstream file: %w", err)
	}
	return Derive(in)
}

// HostFS is the real file system, read by path. It is for root-owned host
// files only; nothing a lead can write is read through it.
type HostFS struct{}

// ReadFile reads at most limit bytes of the named regular file. The file is
// opened without blocking, so a FIFO or a device left at the path is refused
// by the type check on the open descriptor instead of waiting for a writer.
func (HostFS) ReadFile(name string, limit int) ([]byte, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return io.ReadAll(io.LimitReader(f, int64(limit)))
}

// RealPath resolves every symbolic link in name.
func (HostFS) RealPath(name string) (string, error) { return filepath.EvalSymlinks(name) }
