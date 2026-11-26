package link

import (
	"net"
	"syscall"
)

func unixPeer(c *net.UnixConn) (uint32, bool) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *syscall.Ucred
	err = raw.Control(func(fd uintptr) { cred, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) })
	if err != nil || cred == nil {
		return 0, false
	}
	return cred.Uid, true
}
