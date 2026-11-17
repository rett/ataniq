package safefs

import (
	"errors"
	"io"
	"io/fs"
	"math"
	"strings"
)

// ErrUnsupported is returned when the system has no symlink-safe access: not
// Linux, or a Linux kernel before 5.6 that has no openat2.
var ErrUnsupported = errors.New("safefs: not supported on this system")

// ErrNotRegular is returned when a name that must be a regular file is a
// folder, a FIFO, a socket, a device or anything else.
var ErrNotRegular = errors.New("safefs: not a regular file")

// ErrLimit is returned when a byte, entry or depth limit is passed.
var ErrLimit = errors.New("safefs: limit passed")

// ErrCrossDevice is returned when a tree walk meets another filesystem.
var ErrCrossDevice = errors.New("safefs: crosses to another filesystem")

// ErrEscape is returned for a name that is not strictly below the folder:
// an absolute name, an empty name, a name with a ".." element, or a name
// that goes through a symlink.
var ErrEscape = errors.New("safefs: name is not beneath the folder")

// MaxDepth is the deepest nesting of folders that [Root.TreeSize] walks. The
// walk keeps one descriptor open for each folder level, so this bound is also
// the most descriptors a walk holds. A deeper tree is [ErrLimit].
const MaxDepth = 256

// SizeLimits bounds a [Root.TreeSize] walk. Both fields are required and
// must be greater than zero.
type SizeLimits struct {
	// MaxBytes is the most allocated bytes the walk may count.
	MaxBytes int64
	// MaxEntries is the most entries (files, folders and other names) the
	// walk may count.
	MaxEntries int64
}

// Size is what a [Root.TreeSize] walk counted.
type Size struct {
	// Bytes is the allocated size: st_blocks * 512 of every regular file
	// and folder.
	Bytes int64
	// Entries is the number of names counted, the starting name included.
	Entries int64
}

// escapeError is the error for a name the kernel refused because it leaves
// the folder or goes through a symlink. It matches ErrEscape and the errno.
type escapeError struct{ errno error }

func (e escapeError) Error() string   { return ErrEscape.Error() + ": " + e.errno.Error() }
func (e escapeError) Unwrap() []error { return []error{ErrEscape, e.errno} }

// pathError wraps err as a *fs.PathError for op and rel.
func pathError(op, rel string, err error) error {
	return &fs.PathError{Op: op, Path: rel, Err: err}
}

// cleanRel checks a relative name before any system call and returns it in
// lexical form: no empty, "." or trailing-slash parts. A name that is empty,
// absolute or has a ".." element is refused with ErrEscape. The name "." is
// the folder itself.
func cleanRel(op, rel string) (string, error) {
	if rel == "" || rel[0] == '/' {
		return "", pathError(op, rel, ErrEscape)
	}
	var parts []string
	for _, p := range strings.Split(rel, "/") {
		switch p {
		case "..":
			return "", pathError(op, rel, ErrEscape)
		case "", ".":
		default:
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ".", nil
	}
	return strings.Join(parts, "/"), nil
}

// ReadFile opens the regular file rel with [Root.Open] and reads at most max
// bytes. A file with more than max bytes is [ErrLimit]. The decision is made
// by reading, not by the size the file reports, because the lead can change
// the file while it is read. A max below zero is refused.
func (r *Root) ReadFile(rel string, max int64) ([]byte, error) {
	if max < 0 {
		return nil, pathError("read", rel, errors.New("safefs: negative limit"))
	}
	f, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Read one byte past the limit: that byte shows the file is too large.
	limit := max
	if limit < math.MaxInt64 {
		limit++
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, pathError("read", rel, err)
	}
	if int64(len(data)) > max {
		return nil, pathError("read", rel, ErrLimit)
	}
	return data, nil
}

// IsDir reports whether rel is a real folder. A symlink to a folder is not
// one. A name that does not exist is an error ([io/fs.ErrNotExist]).
func (r *Root) IsDir(rel string) (bool, error) {
	fi, err := r.Lstat(rel)
	if err != nil {
		return false, err
	}
	return fi.IsDir(), nil
}
