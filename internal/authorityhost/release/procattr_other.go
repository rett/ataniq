//go:build !linux

package release

import "syscall"

// procAttr is a command's own process group.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
