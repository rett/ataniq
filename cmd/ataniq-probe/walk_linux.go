//go:build linux

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/safefile"
)

// walkError is a bind source that a walk refused: exit 96.
type walkError struct{ msg string }

func (e *walkError) Error() string { return e.msg }

// describe names a walk in a message: the option, the descriptor, and what
// went wrong, then the folder and the path, each clipped, so that the reason
// is never the part a long path pushes out of the line.
func (w fdWalk) describe(format string, args ...any) *walkError {
	return &walkError{msg: fmt.Sprintf("%s %d: %s (folder %s, path %s)", w.option(), w.FD,
		fmt.Sprintf(format, args...), quoteN(w.Root, maxWalkPath), quoteN(w.Rel, maxWalkPath))}
}

// walkSources is the walk of rule B1 for the bind sources of the launch plan,
// made in the last host-side code before bubblewrap: the descriptors it
// returns are the ones bubblewrap inherits and binds from, so the object that
// is walked to and checked here is the object that is mounted, whatever
// happens to its name afterwards.
//
// For each walk it opens the folder the walk starts from (once for all the
// walks that name the same folder) without following a symbolic link in any
// component of the folder's path ([safefile.OpenRootNoLinks]); it then opens
// the path beneath that handle in one openat2 lookup with RESOLVE_BENEATH and
// RESOLVE_NO_SYMLINKS ([safefile.Root.OpenDirHandle] or
// [safefile.Root.OpenFileHandle]: an O_PATH descriptor, with the type, a
// folder or a regular file, read from the descriptor and not from the name);
// and it takes a descriptor of its own on that object. Nothing an earlier
// check found is used, and nothing is opened by name below a folder.
//
// The first refusal ends it: every descriptor opened so far is closed and the
// error is a *walkError that names the walk. On success the folders are
// closed, and each returned descriptor is close-on-exec until it is placed.
// A regular file is checked for its type only: nothing is read, so neither a
// size nor a link count is looked at.
func walkSources(walks []fdWalk) ([]openedFile, error) {
	roots := map[string]*safefile.Root{}
	// Closed before anything is placed: a folder's descriptor must not sit at
	// a number that a placement then overwrites.
	defer func() {
		for _, r := range roots {
			r.Close()
		}
	}()
	var out []openedFile
	refuse := func(err error) ([]openedFile, error) {
		for _, o := range out {
			unix.Close(o.fd)
		}
		return nil, err
	}
	for _, w := range walks {
		root, ok := roots[w.Root]
		if !ok {
			r, err := safefile.OpenRootNoLinks(w.Root)
			if err != nil {
				return refuse(w.describe("the folder the walk starts from %s", rootReason(err)))
			}
			roots[w.Root], root = r, r
		}
		var h *safefile.Handle
		var err error
		if w.Dir {
			h, err = root.OpenDirHandle(w.Rel)
		} else {
			h, err = root.OpenFileHandle(w.Rel)
		}
		if err != nil {
			return refuse(w.describe("%s", sourceReason(root, w, err)))
		}
		fd, err := h.Dup()
		h.Close()
		if err != nil {
			return refuse(w.describe("the source was opened and its descriptor cannot be duplicated: %v", errno(err)))
		}
		out = append(out, openedFile{fd: fd, target: w.FD, desc: fmt.Sprintf("%s %d", w.option(), w.FD)})
	}
	return out, nil
}

// errno is the system's error behind a safefile error, or the error itself.
func errno(err error) error {
	var e unix.Errno
	if errors.As(err, &e) {
		return e
	}
	return err
}

// rootReason says why the folder a walk starts from was not opened. The words
// follow "the folder the walk starts from ".
func rootReason(err error) string {
	switch {
	case errors.Is(err, safefile.ErrSymlink):
		return "is reached through a symbolic link, or is one: a link is never followed"
	case errors.Is(err, fs.ErrNotExist):
		return "does not exist"
	case errors.Is(err, safefile.ErrNotDirectory):
		return "is not a folder, or a component of its path is not one"
	case errors.Is(err, safefile.ErrUnsupported):
		return fmt.Sprintf("cannot be opened by handle: openat2 is not available here (%v), and there is no fallback to a path", errno(err))
	}
	return fmt.Sprintf("cannot be opened without following links: %v", errno(err))
}

// sourceReason says why the path beneath the folder was not opened as the
// source the walk names. For a link and a wrong type it looks once more, with
// the same handle and without following anything, to say where the link is or
// what is there: that look is for the message and decides nothing, the open
// has already refused.
func sourceReason(root *safefile.Root, w fdWalk, err error) string {
	want := "a regular file"
	if w.Dir {
		want = "a folder"
	}
	switch {
	case errors.Is(err, safefile.ErrSymlink):
		return fmt.Sprintf("the source is reached through a symbolic link at %s, or is one: a link is never followed", quoteN(firstLink(root, w.Rel), maxWalkPath))
	case errors.Is(err, fs.ErrNotExist):
		return "the source does not exist"
	case errors.Is(err, safefile.ErrNotDirectory), errors.Is(err, safefile.ErrNotRegular):
		if info, lerr := root.Lstat(w.Rel); lerr == nil {
			return fmt.Sprintf("the source is %s, and the walk binds %s", typeName(info), want)
		}
		return "a component of the path of the source is not a folder"
	case errors.Is(err, safefile.ErrEscapes):
		return "the source lies at or beyond a mount point below the folder, which is not crossed"
	case errors.Is(err, safefile.ErrUnsupported):
		return fmt.Sprintf("the source cannot be walked by handle: openat2 is not available here (%v), and there is no fallback to a path", errno(err))
	}
	return fmt.Sprintf("the source cannot be opened without following links: %v", errno(err))
}

// firstLink names the first prefix of rel that is a symbolic link, as a path
// beneath the folder, and rel itself when none is found (a link that was
// removed in the meantime).
func firstLink(root *safefile.Root, rel string) string {
	comps := strings.Split(rel, "/")
	for i := range comps {
		prefix := strings.Join(comps[:i+1], "/")
		info, err := root.Lstat(prefix)
		if err != nil {
			break
		}
		if info.IsSymlink() {
			return prefix
		}
	}
	return rel
}

// typeName names what an entry is.
func typeName(info safefile.Info) string {
	switch t := info.Type(); {
	case info.IsRegular():
		return "a regular file"
	case info.IsDir():
		return "a folder"
	case info.IsSymlink():
		return "a symbolic link"
	case t&fs.ModeNamedPipe != 0:
		return "a FIFO"
	case t&fs.ModeSocket != 0:
		return "a socket"
	case t&fs.ModeDevice != 0:
		return "a device"
	}
	return "an entry of an unknown type"
}
