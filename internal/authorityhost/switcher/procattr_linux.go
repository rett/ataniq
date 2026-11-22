//go:build linux

package switcher

import "syscall"

// procAttr is a command's own process group, killed when the switch dies.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
