package thread

import (
	"os"
	"syscall"
)

// ownedByUs reports whether the file or folder is the daemon's user's.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int64(st.Uid) == int64(os.Geteuid())
}
