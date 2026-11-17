package verify

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// Removal of what the sandbox left.
//
// The scratch and home folders are written by sandboxed code, so everything
// below them is hostile: symlinks to anywhere, FIFOs, folders with no write
// bit, files that belong to the caller through id mapping but are read-only,
// and trees deeper than a path can be. This code relies on four things:
//
//  1. WorkRoot is owned by the caller with mode 0700. Only the caller's own
//     ids, which the sandbox may map onto, can change anything in it, and
//     only while a process of the run is still alive.
//  2. Every step is relative to a folder descriptor that this code opened
//     with O_NOFOLLOW and O_DIRECTORY. A symlink is therefore never opened:
//     it is found by fstatat with AT_SYMLINK_NOFOLLOW and removed with
//     unlinkat, which removes the link and not its target. No path string is
//     ever resolved below the work root.
//  3. A folder without write or search permission is made 0700 plus its
//     other bits before it is entered. The folder is opened first with
//     O_PATH|O_NOFOLLOW|O_DIRECTORY, which needs no permission on it, and
//     changed through /proc/self/fd/N, which names that very descriptor;
//     the chmod follows only that magic link, to the inode already held.
//     This needs /proc and that the caller owns the folder; if not, the
//     removal fails and the error names the folder.
//  4. Depth is bounded in descriptors, not in path length: a folder deeper
//     than maxRemoveDepth is renamed up to the top folder (it has the
//     caller's write bit by then), which makes its subtree shallow again.
//     The removal of a tree therefore holds at most maxRemoveDepth
//     descriptors.
//
// Unlinking needs write permission on the parent folder only, so files are
// not changed. A run that leaves a process behind which keeps creating files
// can make the removal fail with "directory not empty"; that is reported.

const (
	maxRemoveDepth = 200
	dirBatch       = 4096
)

// unlinkat is a variable so a test can make one removal fail.
var unlinkat = unix.Unlinkat

// workRoot is an open WorkRoot folder.
type workRoot struct {
	fd      int
	liftSeq int
}

// openWorkRoot opens path without following a symlink in the last element and
// checks that it is a folder owned by the caller with mode 0700.
func openWorkRoot(path string) (*workRoot, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("verify: work root %s: %w", path, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("verify: work root %s: %w", path, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		unix.Close(fd)
		return nil, fmt.Errorf("verify: work root %s is not a folder", path)
	case int(st.Uid) != os.Geteuid():
		unix.Close(fd)
		return nil, fmt.Errorf("verify: work root %s is not owned by the caller", path)
	case st.Mode&0o7777 != 0o700:
		unix.Close(fd)
		return nil, fmt.Errorf("verify: work root %s has mode %04o, not 0700", path, st.Mode&0o7777)
	}
	return &workRoot{fd: fd}, nil
}

func (w *workRoot) close() { unix.Close(w.fd) }

// makeDir makes a new folder with mode 0700 and a unique name that starts
// with "verify-" and kind, and returns the name. On an error after the folder
// exists the name is returned too, so the caller removes it.
func (w *workRoot) makeDir(kind string) (string, error) {
	for try := 0; try < 100; try++ {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("verify: random name: %w", err)
		}
		name := "verify-" + kind + "-" + hex.EncodeToString(b[:])
		err := unix.Mkdirat(w.fd, name, 0o700)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("verify: make %s folder: %w", kind, err)
		}
		// The umask may have taken bits away; the name is new and the work
		// root is ours alone.
		if err := unix.Fchmodat(w.fd, name, 0o700, 0); err != nil {
			return name, fmt.Errorf("verify: make %s folder: %w", kind, err)
		}
		return name, nil
	}
	return "", errors.New("verify: no unused folder name")
}

// removeDir removes the folder name below the work root and all it holds.
func (w *workRoot) removeDir(name string) error {
	top, err := openWritableDir(w.fd, name)
	if err != nil {
		switch {
		case errors.Is(err, unix.ENOENT):
			return nil
		case errors.Is(err, unix.ENOTDIR):
			// Not a folder any more (a symlink, say): take the name away.
			if err := unlinkat(w.fd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				return fmt.Errorf("remove %s: %w", name, err)
			}
			return nil
		}
		return err
	}
	defer unix.Close(top)
	if err := w.removeContents(top, top, 0, "."); err != nil {
		return err
	}
	if err := unlinkat(w.fd, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove folder: %w", err)
	}
	return nil
}

// openWritableDir opens the folder name below parent for reading, making it
// readable, writable and searchable first when it is not. It never follows a
// symlink: a name that is one gives ENOTDIR.
func openWritableDir(parent int, name string) (int, error) {
	p, err := unix.Openat(parent, name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(p)
	var st unix.Stat_t
	if err := unix.Fstat(p, &st); err != nil {
		return -1, err
	}
	if st.Mode&0o700 != 0o700 {
		if err := unix.Fchmodat(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(p), (st.Mode&0o7777)|0o700, 0); err != nil {
			return -1, fmt.Errorf("make folder writable: %w", err)
		}
	}
	return unix.Openat(p, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// removeContents removes every entry of the folder dir (an open descriptor),
// not the folder itself. top is the descriptor of the folder being removed,
// where too-deep folders are moved to. rel is the path for error messages.
func (w *workRoot) removeContents(dir, top, depth int, rel string) error {
	for {
		names, err := readBatch(dir)
		if err != nil {
			return fmt.Errorf("read %s: %w", shown(rel), err)
		}
		if len(names) == 0 {
			return nil
		}
		var first error
		progress := 0
		for _, n := range names {
			if err := w.removeEntry(dir, top, depth, rel, n); err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			progress++
		}
		if progress == 0 {
			return first
		}
	}
}

func (w *workRoot) removeEntry(dir, top, depth int, rel, name string) error {
	here := rel + "/" + name
	var st unix.Stat_t
	if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("%s: %w", shown(here), err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		// A file, a symlink, a FIFO, a socket or a device: only the name goes.
		if err := unlinkat(dir, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("%s: %w", shown(here), err)
		}
		return nil
	}
	sub, err := openWritableDir(dir, name)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("%s: %w", shown(here), err)
	}
	defer unix.Close(sub)
	if depth+1 >= maxRemoveDepth {
		return w.lift(dir, name, top, here)
	}
	if err := w.removeContents(sub, top, depth+1, here); err != nil {
		return err
	}
	if err := unlinkat(dir, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("%s: %w", shown(here), err)
	}
	return nil
}

// lift moves the folder name of dir up into the top folder under a new name,
// so that the next pass over the top folder finds it at depth 1.
func (w *workRoot) lift(dir int, name string, top int, here string) error {
	for try := 0; try < 100; try++ {
		w.liftSeq++
		to := ".lifted-" + strconv.Itoa(w.liftSeq)
		err := unix.Renameat(dir, name, top, to)
		if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: too deep and could not be moved up: %w", shown(here), err)
		}
		return nil
	}
	return fmt.Errorf("%s: too deep and no free name to move it to", shown(here))
}

// shown returns a path for an error message. The names come from the
// sandbox, so the path is quoted (no control character or newline gets
// through) and only its last part is kept.
func shown(rel string) string {
	const keep = 200
	if len(rel) > keep {
		rel = "..." + rel[len(rel)-keep:]
	}
	return strconv.QuoteToASCII(rel)
}

// readBatch returns up to dirBatch entry names of the folder, reading it
// afresh through a new descriptor so that earlier removals show.
func readBatch(dir int) ([]string, error) {
	fd, err := unix.Openat(dir, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "dir")
	defer f.Close()
	names, err := f.Readdirnames(dirBatch)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return names, err
}
