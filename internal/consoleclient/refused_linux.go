package consoleclient

import "syscall"

// On Linux a dial to a socket file that nothing listens on is refused.
func init() { errRefused = syscall.ECONNREFUSED }
