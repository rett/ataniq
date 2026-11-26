//go:build !linux

package link

import "net"

// Without peer credentials, share the local budget across Unix clients.
func unixPeer(*net.UnixConn) string { return "local" }
