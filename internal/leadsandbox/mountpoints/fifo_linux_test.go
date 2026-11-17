//go:build linux

package mountpoints

import "golang.org/x/sys/unix"

func mkfifo(p string) error { return unix.Mkfifo(p, 0o600) }
