package noaicheck

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
)

// maxLinks bounds the links followed while resolving one path, as the kernel
// does, so that a link loop ends with an error instead of a hang.
const maxLinks = 40

// errLoop is returned when resolving a path follows more than maxLinks links.
var errLoop = errors.New("too many levels of symbolic links")

// errTooLarge is returned when a file is larger than the reader allows.
var errTooLarge = errors.New("file too large")

// tree reads beneath one root folder. Paths are slash-separated and relative
// to the root ("usr/bin", "." for the root itself); a path shown to a person
// is the same path with a leading '/'.
type tree struct {
	r *os.Root
}

func openTree(dir string) (*tree, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &tree{r: r}, nil
}

func (t *tree) close() error { return t.r.Close() }

// show is the path as a person reads it: absolute within the root.
func show(rel string) string {
	if rel == "." || rel == "" {
		return "/"
	}
	return "/" + rel
}

// clean turns an absolute or relative path into a clean path relative to the
// root; ".." never climbs above the root.
func clean(p string) string {
	p = path.Clean("/" + p)
	if p == "/" {
		return "."
	}
	return p[1:]
}

// resolve follows every link in rel, component by component, within the root:
// an absolute target starts again at the root and ".." stops at it, as for a
// process whose root folder is the tree's root. The result holds no link
// (barring a change while it runs) and names an existing entry.
func (t *tree) resolve(rel string) (string, error) {
	parts := strings.Split(clean(rel), "/")
	done := "."
	links := 0
	for len(parts) > 0 {
		c := parts[0]
		parts = parts[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			done = clean(path.Dir(done))
			continue
		}
		next := path.Join(done, c)
		fi, err := t.r.Lstat(next)
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			done = next
			continue
		}
		links++
		if links > maxLinks {
			return "", &fs.PathError{Op: "resolve", Path: show(rel), Err: errLoop}
		}
		target, err := t.r.Readlink(next)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(target, "/") {
			done = "."
		}
		parts = append(strings.Split(target, "/"), parts...)
	}
	return done, nil
}

// lstat resolves the folder of rel and stats its last component without
// following it.
func (t *tree) lstat(rel string) (string, fs.FileInfo, error) {
	rel = clean(rel)
	if rel == "." {
		fi, err := t.r.Lstat(".")
		return ".", fi, err
	}
	dir, err := t.resolve(path.Dir(rel))
	if err != nil {
		return "", nil, err
	}
	p := path.Join(dir, path.Base(rel))
	fi, err := t.r.Lstat(p)
	return p, fi, err
}

// readDir resolves rel and lists the folder it names. The entries' types are
// those of the entries themselves: a link is a link.
func (t *tree) readDir(rel string) (string, []fs.DirEntry, error) {
	p, err := t.resolve(rel)
	if err != nil {
		return "", nil, err
	}
	f, err := t.r.OpenFile(p, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return p, nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	sortEntries(entries)
	return p, entries, err
}

// sortEntries orders a folder's entries by name, so that a report lists hits
// in the same order on every run.
func sortEntries(entries []fs.DirEntry) {
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
}

// readFile resolves rel and reads it when it is a regular file of at most
// max bytes. A FIFO or device is never opened for reading content: the type
// is checked first and the open does not block.
func (t *tree) readFile(rel string, max int64) (string, []byte, error) {
	p, err := t.resolve(rel)
	if err != nil {
		return "", nil, err
	}
	fi, err := t.r.Lstat(p)
	if err != nil {
		return p, nil, err
	}
	if !fi.Mode().IsRegular() {
		return p, nil, &fs.PathError{Op: "read", Path: show(p), Err: errors.New("not a regular file")}
	}
	f, err := t.r.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return p, nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return p, nil, err
	}
	if int64(len(data)) > max {
		return p, nil, &fs.PathError{Op: "read", Path: show(p), Err: errTooLarge}
	}
	return p, data, nil
}

// readlink reads the target of the link rel without following it.
func (t *tree) readlink(rel string) (string, error) {
	return t.r.Readlink(clean(rel))
}

// walk lists the folder start and, below it, every folder down to maxDepth
// levels (1: start's own entries only). It never follows a link inside the
// walk, so it ends; start itself is resolved. visit is called for every
// entry with its path relative to the root. A folder that cannot be read
// is passed to fail; a start that does not exist is not.
func (t *tree) walk(start string, maxDepth int, visit func(rel string, d fs.DirEntry), fail func(rel string, err error)) {
	p, entries, err := t.readDir(start)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			if p == "" {
				p = clean(start)
			}
			fail(p, err)
		}
		return
	}
	t.walkEntries(p, entries, 1, maxDepth, visit, fail)
}

func (t *tree) walkEntries(dir string, entries []fs.DirEntry, depth, maxDepth int, visit func(string, fs.DirEntry), fail func(string, error)) {
	for _, d := range entries {
		rel := path.Join(dir, d.Name())
		visit(rel, d)
		if !d.IsDir() || depth >= maxDepth {
			continue
		}
		f, err := t.r.OpenFile(rel, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				fail(rel, err)
			}
			continue
		}
		sub, err := f.ReadDir(-1)
		f.Close()
		sortEntries(sub)
		if err != nil {
			fail(rel, err)
			continue
		}
		t.walkEntries(rel, sub, depth+1, maxDepth, visit, fail)
	}
}

// describe is an error's kind without its path, for a person.
func describe(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		err = le.Err
	}
	return fmt.Sprint(err)
}
