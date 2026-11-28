//go:build !linux

package link

import "net"

// Refuse owner admission without kernel peer credentials.
func unixPeer(*net.UnixConn) (uint32, bool) { return 0, false }
