//go:build linux

package release

import "syscall"

// procAttr is a command's own process group, killed when the builder dies.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
