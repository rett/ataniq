package release

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"syscall"
)

// readDirBatch is how many entries of a folder are read at a time.
const readDirBatch = 256

// errTooMany is returned by walk when a tree holds more than MaxEntries
// entries or is nested deeper than maxDepth.
var errTooMany = errors.New("too many entries or too deep")

// entry is one folder or file of a tree, as Lstat saw it.
type entry struct {
	path  string
	mode  fs.FileMode
	size  int64
	nlink uint64
}

func (e entry) dir() bool     { return e.mode.IsDir() }
func (e entry) regular() bool { return e.mode.IsRegular() }

// walk lists every entry at and below the folder start of root (start itself
// is not listed), sorted by path, reading each folder in batches and holding
// one open at a time. It never follows a link: a link, device, fifo or socket
// is listed as what Lstat says it is and not entered or opened; refusing it
// is the caller's job. n counts the entries across calls.
func walk(root *os.Root, start string, n *int) ([]entry, error) {
	var out []entry
	var rec func(dir string, depth int) error
	rec = func(dir string, depth int) error {
		if depth > maxDepth {
			return errTooMany
		}
		before, err := root.Lstat(dir)
		if err != nil {
			return err
		}
		if !before.IsDir() {
			return errors.New("not a folder")
		}
		f, err := root.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		if after, err := f.Stat(); err != nil || !os.SameFile(before, after) {
			return errors.New("folder changed while it was read")
		}
		var subs []string
		for {
			ents, err := f.ReadDir(readDirBatch)
			for _, de := range ents {
				rel := path.Join(dir, de.Name())
				if dir == "." {
					rel = de.Name()
				}
				info, lerr := root.Lstat(rel)
				if lerr != nil {
					return lerr
				}
				if *n++; *n > MaxEntries {
					return errTooMany
				}
				out = append(out, entry{path: rel, mode: info.Mode(), size: info.Size(), nlink: nlink(info)})
				if info.IsDir() {
					subs = append(subs, rel)
				}
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
		}
		for _, sub := range subs {
			if err := rec(sub, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := rec(start, 0); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

func nlink(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}

// openRegular opens the regular file p of root for reading, without
// blocking, and checks that what it opened is the regular file Lstat saw
// (the same file, of the same size). The caller closes it.
func openRegular(root *os.Root, p string) (*os.File, fs.FileInfo, error) {
	before, err := root.Lstat(p)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, errors.New("not a regular file")
	}
	f, err := root.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != before.Size() {
		f.Close()
		return nil, nil, errors.New("file changed while it was opened")
	}
	return f, after, nil
}

// errSmallTooLarge is readSmall's error for a file over maxSmallFile bytes.
var errSmallTooLarge = errors.New("file too large")

// readSmall reads the regular file p of root whole, up to maxSmallFile bytes.
func readSmall(root *os.Root, p string) ([]byte, error) {
	f, info, err := openRegular(root, p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Size() > maxSmallFile {
		return nil, errSmallTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, info.Size()+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size() {
		return nil, errors.New("file changed while it was read")
	}
	return data, nil
}
