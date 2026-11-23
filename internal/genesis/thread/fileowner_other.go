//go:build !linux

package thread

import "os"

// ownedByUs reports whether the file or folder is the daemon's user's. Only
// Linux says; on any other system the store trusts the folder's mode alone.
func ownedByUs(os.FileInfo) bool { return true }
