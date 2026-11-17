package safefile

import (
	"errors"
	"strconv"
)

// The refusals a caller can test with errors.Is. Every error the package
// returns for a refusal wraps one of them; an error that is none of them
// carries the operating system's own error (for example a missing file wraps
// an errno that matches fs.ErrNotExist).
var (
	// ErrSymlink: a component of the path is a symbolic link (or a magic
	// link), a link that points inside the folder or nowhere included. For
	// the last component: ReadFile, ReadFileInfo, AppendFile and ReadDirNames
	// return it; WriteFileExclusive returns it, wrapping the errno of the
	// create (so it also matches fs.ErrExist); MkdirAll returns it, as its
	// walk opens every component; Lstat does not, it reports the link (see
	// [Info.IsSymlink]). For a parent component every method returns it.
	ErrSymlink = errors.New("a component of the path is a symbolic link")
	// ErrNotRegular: the entry is a FIFO, device, socket or folder where a
	// regular file is required. A device is refused here only when its open
	// succeeded and fstat saw it; see the package documentation.
	ErrNotRegular = errors.New("not a regular file")
	// ErrNotDirectory: a component that must be a folder is not one.
	ErrNotDirectory = errors.New("a component that must be a folder is not one")
	// ErrTooLarge: a file is larger than the cap, a file grew past the cap
	// while it was read, or a folder holds more entries than the cap.
	ErrTooLarge = errors.New("larger than the cap")
	// ErrEscapes: the path is absolute, contains "..", resolves outside the
	// folder, or crosses a mount point below it (EXDEV), or, for MkdirAll,
	// a folder on the path moved while the call ran.
	ErrEscapes = errors.New("path escapes the folder")
	// ErrUnsupported: openat2 is not available (ENOSYS), or a filter denied
	// it (EPERM, which is not told apart from a permission failure), or
	// [OpenRoot]'s probe of it failed. No path-based fallback is tried.
	ErrUnsupported = errors.New("openat2 is unavailable (not implemented, denied with EPERM: not told apart, or not working)")
	// ErrHardLink: a regular file has more than one hard link and the call
	// does not accept that.
	ErrHardLink = errors.New("regular file has more than one hard link")
	// ErrInvalidArgument: an empty or unclean path, a NUL byte, a negative
	// cap or a mode with bits other than permission bits.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrClosed: the Root was closed.
	ErrClosed = errors.New("folder handle is closed")
)

// maxErrPath bounds the path an error message repeats.
const maxErrPath = 200

// Error is the error type of every failure of a [Root] method. Err is one of
// the sentinel errors above, possibly joined with the system's errno, or the
// errno itself.
type Error struct {
	Op   string // the operation: "read", "lstat", "mkdir", "create", "append", "readdir", "open"
	Path string // the path as the caller gave it
	Err  error
}

func (e *Error) Error() string {
	p := e.Path
	if len(p) > maxErrPath {
		p = p[:maxErrPath] + "..."
	}
	return "safefile: " + e.Op + " " + strconv.Quote(p) + ": " + e.Err.Error()
}

// Unwrap returns the wrapped error so errors.Is and errors.As see through it.
func (e *Error) Unwrap() error { return e.Err }
