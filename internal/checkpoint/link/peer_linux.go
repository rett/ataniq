package link

import (
	"net"
	"strconv"
	"syscall"
)

func unixPeer(c *net.UnixConn) string {
	raw, err := c.SyscallConn()
	if err != nil {
		return "local"
	}
	source := "local"
	_ = raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err == nil {
			source = strconv.FormatUint(uint64(cred.Uid), 10)
		}
	})
	return source
}
