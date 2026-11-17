//go:build !linux

package safefs

import (
	"io/fs"
	"os"
)

// Root is a folder opened for symlink-safe access. On this system it cannot
// be opened: every call returns [ErrUnsupported].
type Root struct{}

// OpenRoot returns [ErrUnsupported] on this system.
func OpenRoot(path string) (*Root, error) { return nil, pathError("open", path, ErrUnsupported) }

// Close returns [ErrUnsupported] on this system.
func (r *Root) Close() error { return ErrUnsupported }

// Open returns [ErrUnsupported] on this system.
func (r *Root) Open(rel string) (*os.File, error) {
	return nil, pathError("open", rel, ErrUnsupported)
}

// Lstat returns [ErrUnsupported] on this system.
func (r *Root) Lstat(rel string) (fs.FileInfo, error) {
	return nil, pathError("lstat", rel, ErrUnsupported)
}

// TreeSize returns [ErrUnsupported] on this system.
func (r *Root) TreeSize(rel string, lim SizeLimits) (Size, error) {
	return Size{}, pathError("treesize", rel, ErrUnsupported)
}
