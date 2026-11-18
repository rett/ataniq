package clientapi

import "path/filepath"

// SocketName is the file name of the socket inside the folder.
const SocketName = "daemon.sock"

// SocketPath returns the socket path for dir. It is in a file for every
// system, like SocketName, because code that only names the path (the
// daemon's default paths) is built for systems that the listener is not.
func SocketPath(dir string) string { return filepath.Join(dir, SocketName) }
