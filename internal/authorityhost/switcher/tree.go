package switcher

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"syscall"
)

// readDirBatch is how many entries of a folder are read at a time.
const readDirBatch = 256

// walkTree calls fn for every entry below the folder r (not r itself), with
// its path relative to r and what Lstat returned, a folder before what it
// holds. It reads each folder in batches and holds one folder open at a time;
// a folder that is replaced while it is opened is an error. fn sees links,
// devices and the rest as they are: refusing them is its job.
func walkTree(r *os.Root, fn func(rel string, info fs.FileInfo) error) error {
	dirs := []string{"."}
	for len(dirs) > 0 {
		dir := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		subs, err := walkDir(r, dir, fn)
		if err != nil {
			return err
		}
		dirs = append(dirs, subs...)
	}
	return nil
}

// walkDir calls fn for each entry of the folder dir of r and returns its
// folders.
func walkDir(r *os.Root, dir string, fn func(rel string, info fs.FileInfo) error) ([]string, error) {
	before, err := r.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, failf(CodeBadEntry, "%s is not a folder", q(dir))
	}
	f, err := r.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if after, err := f.Stat(); err != nil || !os.SameFile(before, after) {
		return nil, failf(CodeFileChanged, "%s changed while it was read", q(dir))
	}
	var subs []string
	for {
		ents, err := f.ReadDir(readDirBatch)
		for _, ent := range ents {
			rel := path.Join(dir, ent.Name())
			info, err := r.Lstat(rel)
			if err != nil {
				return nil, err
			}
			if err := fn(rel, info); err != nil {
				return nil, err
			}
			if info.IsDir() {
				subs = append(subs, rel)
			}
		}
		if errors.Is(err, io.EOF) {
			return subs, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// links is the number of hard links of what info describes; 0 when it cannot
// be read.
func links(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 0
}

// entryKind refuses what a release may not hold: anything but a folder or a
// regular file with one link.
func entryKind(rel string, info fs.FileInfo) error {
	switch m := info.Mode(); {
	case m&fs.ModeSymlink != 0:
		return failf(CodeBadEntry, "%s is a symbolic link; a release holds only folders and regular files", q(rel))
	case m.IsDir():
		return nil
	case !m.IsRegular():
		return failf(CodeBadEntry, "%s is not a regular file", q(rel))
	case links(info) != 1:
		return failf(CodeBadEntry, "%s has more than one hard link", q(rel))
	}
	return nil
}

// specialBits are the mode bits a release never holds.
const specialBits = fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// entryTrusted refuses an entry of a release that could have been changed
// since it was imported, or that runs with other rights: one writable by its
// group or others, one with a set-user-id, set-group-id or sticky bit, or one
// whose owner the owner check refuses.
func (s *Switcher) entryTrusted(rel string, info fs.FileInfo) error {
	switch {
	case info.Mode().Perm()&0o022 != 0:
		return failf(CodeUntrusted, "%s is writable by its group or by others", q(rel))
	case info.Mode()&specialBits != 0:
		return failf(CodeUntrusted, "%s has a set-id or sticky bit", q(rel))
	}
	if err := s.cfg.Owner(info); err != nil {
		return failf(CodeUntrusted, "%s: %s", q(rel), q(err.Error()))
	}
	return nil
}
