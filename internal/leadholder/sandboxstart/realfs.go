//go:build linux && ataniq_canary

package sandboxstart

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// This file makes and removes the two folders of the sequence in the runtime
// folder of the role: "report" (the report FIFO's) and "bound" (resolv.conf and
// the spec folder). Every step is beneath a descriptor: the runtime folder is
// opened once, without a link on any component of its path, and each folder
// and file is then made, opened and removed relative to the descriptor of its
// parent, with no link followed at its name. A folder is made 0700 by handle
// (fchmod on the descriptor that was opened, not on a name).

// maxLeftovers bounds how many names of a leftover folder are read: a folder
// that holds more than the sequence ever puts in it is refused.
const maxLeftovers = 16

// node describes an entry the sequence may find in one of its own folders, left
// by an earlier holder that was killed: its type (a regular file, a FIFO or a
// folder) and, for a folder, what may be in it. Anything else is refused.
type node struct {
	typ  uint32
	kids map[string]node
}

// openRuntimeFolder opens the runtime folder of the role by its whole path
// without following a symbolic link on any component, and checks on the
// descriptor that it is a folder of the caller that neither group nor others
// can use. It is the one lookup by name; everything after is beneath it.
func openRuntimeFolder(p string) (int, error) {
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, p, how)
	if err != nil {
		return -1, fmt.Errorf("open the runtime folder %s: %w", p, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("examine the runtime folder %s: %w", p, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		unix.Close(fd)
		return -1, fmt.Errorf("the runtime folder %s is not a folder", p)
	case st.Uid != uint32(unix.Geteuid()):
		unix.Close(fd)
		return -1, fmt.Errorf("the runtime folder %s is not owned by the calling user", p)
	case st.Mode&0o077 != 0:
		unix.Close(fd)
		return -1, fmt.Errorf("the runtime folder %s has mode %04o: group and others must have no access", p, st.Mode&0o7777)
	}
	return fd, nil
}

// freshFolder makes the folder name in the folder parent, 0700, and returns a
// descriptor on it. A folder of that name that is already there is the leftover
// of a holder that was killed (the role lock proves no other holder runs): it
// is kept only if it is a real folder of the caller, and what it holds is
// removed if it is one of the entries keep names, with the type it names;
// anything else in it refuses the start.
func freshFolder(parent int, name string, keep map[string]node) (int, error) {
	made := true
	if err := unix.Mkdirat(parent, name, 0o700); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return -1, fmt.Errorf("make the folder %s: %w", name, err)
		}
		made = false
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open the folder %s (a symbolic link is refused): %w", name, err)
	}
	fail := func(err error) (int, error) {
		unix.Close(fd)
		if made {
			unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
		}
		return -1, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fail(fmt.Errorf("examine the folder %s: %w", name, err))
	}
	if st.Uid != uint32(unix.Geteuid()) {
		return fail(fmt.Errorf("the folder %s is not owned by the calling user", name))
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		return fail(fmt.Errorf("set the mode of the folder %s: %w", name, err))
	}
	if !made {
		if err := clearEntries(fd, keep); err != nil {
			return fail(fmt.Errorf("the folder %s is left over from an earlier start: %w", name, err))
		}
	}
	return fd, nil
}

// clearEntries removes the entries of the folder fd, each of which must be one
// that allowed names, with the type it names.
func clearEntries(fd int, allowed map[string]node) error {
	names, err := listNames(fd)
	if err != nil {
		return err
	}
	for _, name := range names {
		want, ok := allowed[name]
		if !ok {
			return fmt.Errorf("it holds %q, which the start does not make", clipText(name))
		}
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("examine %q: %w", name, err)
		}
		if st.Mode&unix.S_IFMT != want.typ {
			return fmt.Errorf("%q is not of the type the start makes", name)
		}
		if want.typ != unix.S_IFDIR {
			if err := unix.Unlinkat(fd, name, 0); err != nil {
				return fmt.Errorf("remove %q: %w", name, err)
			}
			continue
		}
		sub, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open %q: %w", name, err)
		}
		err = clearEntries(sub, want.kids)
		unix.Close(sub)
		if err != nil {
			return fmt.Errorf("%q: %w", name, err)
		}
		if err := unix.Unlinkat(fd, name, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("remove the folder %q: %w", name, err)
		}
	}
	return nil
}

// listNames returns the names in the folder fd, at most maxLeftovers: a folder
// with more is an error.
func listNames(fd int) ([]string, error) {
	d, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(d), "folder")
	defer f.Close()
	names, err := f.Readdirnames(maxLeftovers + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > maxLeftovers {
		return nil, fmt.Errorf("it holds more than %d entries", maxLeftovers)
	}
	return names, nil
}

// unlink removes the entry name below fd (a folder when dir is set); an entry
// that is not there is not an error.
func unlink(fd int, name string, dir bool) error {
	flags := 0
	if dir {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(fd, name, flags); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

// writeExclusive creates the regular file name below fd, exclusively and
// without following a link at its name, gives it the mode and writes the data.
func writeExclusive(fd int, name string, data []byte, mode uint32) error {
	f, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	file := os.NewFile(uintptr(f), name)
	if err := unix.Fchmod(f, mode); err != nil {
		file.Close()
		return fmt.Errorf("set the mode of %s: %w", name, err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}

// boundDir is the bound folder as the real System makes it.
type boundDir struct {
	mu     sync.Mutex
	base   int // the runtime folder
	dir    int // bound
	spec   int // bound/spec
	closed bool
}

// MakeBound implements System.
func (*realSystem) MakeBound(runtimeRoot, layoutRole string) (Bound, error) {
	base, err := openRuntimeFolder(RuntimeFolder(runtimeRoot, layoutRole))
	if err != nil {
		return nil, err
	}
	dir, err := freshFolder(base, boundDirName, map[string]node{
		resolvName:  {typ: unix.S_IFREG},
		specDirName: {typ: unix.S_IFDIR, kids: map[string]node{specFileName: {typ: unix.S_IFREG}}},
	})
	if err != nil {
		unix.Close(base)
		return nil, err
	}
	spec, err := freshFolder(dir, specDirName, nil)
	if err != nil {
		unix.Close(dir)
		unix.Unlinkat(base, boundDirName, unix.AT_REMOVEDIR)
		unix.Close(base)
		return nil, err
	}
	return &boundDir{base: base, dir: dir, spec: spec}, nil
}

// specFileName is the name of the probe's spec in the spec folder.
const specFileName = "spec.json"

func (b *boundDir) write(fd int, name string, data []byte, mode uint32) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("the bound folder is closed")
	}
	return writeExclusive(fd, name, data, mode)
}

// WriteResolv implements Bound.
func (b *boundDir) WriteResolv(content []byte) error {
	return b.write(b.dir, resolvName, content, 0o644)
}

// WriteSpec implements Bound.
func (b *boundDir) WriteSpec(content []byte) error {
	return b.write(b.spec, specFileName, content, 0o600)
}

// Close implements Bound: it removes what was made and closes the descriptors.
func (b *boundDir) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	errs := []error{
		unlink(b.spec, specFileName, false),
		unlink(b.dir, specDirName, true),
		unlink(b.dir, resolvName, false),
		unlink(b.base, boundDirName, true),
	}
	for _, fd := range []int{b.spec, b.dir, b.base} {
		if err := unix.Close(fd); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
