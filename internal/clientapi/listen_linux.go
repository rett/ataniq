package clientapi

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// SocketName is the file name of the socket inside the folder.
const SocketName = "daemon.sock"

// maxSocketPath is the longest socket path in bytes: sockaddr_un.sun_path has
// 108 bytes on Linux and the address must keep its terminating NUL.
const maxSocketPath = 107

// SocketPath returns the socket path for dir.
func SocketPath(dir string) string { return filepath.Join(dir, SocketName) }

// checkDir verifies that dir is an absolute path to a folder that the caller
// owns and that no one else can enter, and returns the socket path in it. It
// does not follow a symbolic link at dir.
func checkDir(dir string) (path string, err error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", fmt.Errorf("clientapi: socket folder %q is not a clean absolute path", dir)
	}
	path = SocketPath(dir)
	if err := checkPathLen(path); err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("clientapi: %s is not a folder", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("clientapi: cannot read the folder's owner")
	}
	if st.Uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("clientapi: %s is not owned by the caller", dir)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("clientapi: %s is accessible to group or others (mode %04o)", dir, perm)
	}
	if fi.Mode().Perm()&0o700 != 0o700 {
		return "", fmt.Errorf("clientapi: %s is not usable by its owner (mode %04o)", dir, fi.Mode().Perm())
	}
	return path, nil
}

func checkPathLen(path string) error {
	if len(path) > maxSocketPath {
		return fmt.Errorf("clientapi: socket path is %d bytes, over the %d a Unix socket address holds", len(path), maxSocketPath)
	}
	return nil
}

// ensureDir creates dir with mode 0700 when it does not exist, then checks it
// as [checkDir] does. The parent of dir must exist.
func ensureDir(dir string) (string, error) {
	if filepath.IsAbs(dir) && filepath.Clean(dir) == dir {
		if err := checkPathLen(SocketPath(dir)); err != nil {
			return "", err
		}
		if err := os.Mkdir(dir, 0o700); err == nil {
			// The umask can only narrow 0700; this sets it exactly.
			if err := os.Chmod(dir, 0o700); err != nil {
				return "", err
			}
		} else if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return checkDir(dir)
}

// Listen makes the folder dir, binds daemon.sock in it and returns the
// listener and the socket path. dir must be a clean absolute path whose parent
// exists.
//
// The folder is created with mode 0700. An existing folder is accepted only if
// it is a real folder (not a symbolic link), owned by the caller's effective
// UID, and not accessible to group or others; otherwise Listen refuses and
// changes nothing. A socket path longer than a Unix socket address can hold is
// refused.
//
// The socket is bound with mode 0600 and there is no window in which it is
// wider for anyone but the caller. bind(2) creates the socket file with mode
// 0777 minus the process umask, and Listen does not touch the umask (it is
// process-wide, and other goroutines create files). Instead it binds inside the
// 0700 folder: connect(2) to a socket needs search permission on every folder
// of the path, so no other UID can reach the file while it is briefly wider
// than 0600, and Listen then sets mode 0600 with chmod before it returns. The
// processes that could reach the file in that moment are the caller's own
// (and root), who could reach it afterwards too. This holds as long as the
// folder is not replaced by someone who owns its parent; the parent of the
// folder is the owner's runtime folder.
//
// Listen never removes an existing file. When daemon.sock already exists the
// bind fails with an error that matches syscall.EADDRINUSE; a stale one is
// removed by [RemoveStale], which the caller invokes after it holds the daemon
// lock. Closing the returned listener unlinks the socket file.
func Listen(dir string) (net.Listener, string, error) {
	path, err := ensureDir(dir)
	if err != nil {
		return nil, "", err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, "", err
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != 0o600 {
		l.Close()
		return nil, "", fmt.Errorf("clientapi: %s is not a socket with mode 0600 after bind", path)
	}
	return l, path, nil
}

// RemoveStale removes a socket file left in dir by an earlier daemon, and
// reports whether it removed one. Listen never does this itself.
//
// Precondition: the caller holds the daemon lock (the exclusive lock that
// lets only one daemon run). With it held, no other daemon can be serving on
// this socket, so any socket file here is stale; RemoveStale does not probe
// the socket and cannot tell a live one from a stale one. Calling it without
// the lock can unlink a live daemon's socket.
//
// It checks the folder as [Listen] does, and removes the file only if it is a
// socket owned by the caller; a regular file, a symbolic link or anything else
// at that name is an error and is left alone. A missing file is not an error.
func RemoveStale(dir string) (bool, error) {
	path, err := checkDir(dir)
	if err != nil {
		return false, err
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return false, fmt.Errorf("clientapi: %s is not a socket", path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(os.Geteuid()) {
		return false, fmt.Errorf("clientapi: %s is not owned by the caller", path)
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
