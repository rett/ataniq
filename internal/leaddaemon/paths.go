package leaddaemon

import (
	"errors"
	"path/filepath"

	"ataniq/internal/clientapi"
)

// stateName and runtimeName are the folder names under the user's state and
// runtime folders.
const (
	stateName   = "ataniq"
	runtimeName = "ataniq"
)

// DefaultStateDir returns the default state folder: $XDG_STATE_HOME/ataniq,
// else $HOME/.local/state/ataniq. getenv reads the environment. A variable
// that is not an absolute path is ignored, as the XDG rules say; when no
// absolute one is left it fails.
func DefaultStateDir(getenv func(string) string) (string, error) {
	if x := getenv("XDG_STATE_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, stateName), nil
	}
	if h := getenv("HOME"); filepath.IsAbs(h) {
		return filepath.Join(h, ".local", "state", stateName), nil
	}
	return "", errors.New("no state folder: neither XDG_STATE_HOME nor HOME is an absolute path; use --state-dir")
}

// DefaultRuntimeDir returns the default runtime folder,
// $XDG_RUNTIME_DIR/ataniq. There is no default without an absolute
// XDG_RUNTIME_DIR, and never a shared folder such as /tmp in its place.
func DefaultRuntimeDir(getenv func(string) string) (string, error) {
	if x := getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(x) {
		return filepath.Join(x, runtimeName), nil
	}
	return "", errors.New("no runtime folder: XDG_RUNTIME_DIR is not an absolute path, and no shared folder is used in its place; use --runtime-dir")
}

// DefaultSocketPath returns the default socket path, from the default
// runtime folder.
func DefaultSocketPath(getenv func(string) string) (string, error) {
	dir, err := DefaultRuntimeDir(getenv)
	if err != nil {
		return "", err
	}
	return clientapi.SocketPath(dir), nil
}

// SocketLockPath returns the path of the socket lock of a runtime folder,
// <dir>/daemon.sock.lock. The daemon holds it from before it removes a stale
// socket until after its listener is closed, so that two daemons with
// different state folders and one runtime folder do not take each other's
// socket. Like the daemon lock, the file stays after the daemon ends and
// means nothing without the lock on it.
func SocketLockPath(dir string) string {
	return filepath.Join(dir, clientapi.SocketName+".lock")
}
