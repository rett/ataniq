// Package safefs reads a folder whose content a lead may have written,
// without ever following what the lead planted in it.
//
// # Why
//
// A lead can put a symlink, a FIFO or a device in a folder it shares with
// the owner, and it can swap the folder itself while the owner reads it.
// Owner-side code that opens such content by path can be sent to any file
// the owner can read, can block forever on a FIFO, or can read a device.
// This package is the one way owner-side code opens that content.
//
// # How
//
// [OpenRoot] opens the folder once and keeps the descriptor. Every later
// call resolves its relative name from that descriptor with openat2 and the
// flags RESOLVE_BENEATH, RESOLVE_NO_SYMLINKS and RESOLVE_NO_MAGICLINKS.
// The kernel then refuses a symlink in any position, a name that leaves the
// folder, and a /proc magic link. A swap of the folder's path after
// [OpenRoot] changes nothing: the handle still names the original folder.
// [Root.Open] never blocks on a FIFO and returns regular files only.
// [Root.TreeSize] walks by descriptors and never opens anything but a folder
// or a regular file it has just seen.
//
// # Errors
//
// A name that is absolute, empty or has a ".." element is refused with
// [ErrEscape] before any system call. Errors wrap the underlying errno, so
// errors.Is works with [io/fs.ErrNotExist] and with unix.ELOOP. A symlink
// the kernel refuses satisfies both unix.ELOOP and [ErrEscape]. [OpenRoot]
// refuses a path that is not clean, or that ends in "." or "..", with
// [ErrEscape].
//
// # Limits of this package
//
// It is Linux only: on another system every call returns [ErrUnsupported].
// On a Linux kernel without openat2 (before 5.6) [OpenRoot] returns
// [ErrUnsupported]; there is no fallback to opening by path.
// It does not check permissions, does not write, does not lock, and does
// not stop the lead from changing a file while it is read: a caller that
// needs a stable copy reads the bytes once, with a limit, and works on the
// copy. The folders above the one given to [OpenRoot] belong to the owner
// and are resolved the ordinary way.
package safefs
