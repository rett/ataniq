//go:build !linux

package verify

import "errors"

var errUnsupported = errors.New("verify: unsupported on this system")

// workRoot is a stub: the verification run needs Linux.
type workRoot struct{}

func openWorkRoot(path string) (*workRoot, error) { return nil, errUnsupported }

func (w *workRoot) close() {}

func (w *workRoot) makeDir(kind string) (string, error) { return "", errUnsupported }

func (w *workRoot) removeDir(name string) error { return errUnsupported }
