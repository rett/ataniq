//go:build linux

package mirror

import (
	"io/fs"
	"syscall"

	"golang.org/x/sys/unix"
)

// oNoFollow makes an open refuse a symlink as the last element.
const oNoFollow = unix.O_NOFOLLOW

// ownerUID returns the owning user of a file as the system reports it.
// It accepts the result of os.Lstat and of the safefs calls.
func ownerUID(fi fs.FileInfo) (uint32, bool) {
	switch st := fi.Sys().(type) {
	case *syscall.Stat_t:
		return st.Uid, true
	case *unix.Stat_t:
		return st.Uid, true
	}
	return 0, false
}
