package systemdmgr

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"ataniq/internal/pidfd"
	"ataniq/internal/systemd"
)

// cgroupMount is where the unified hierarchy is mounted. internal/systemd
// keeps its own copy private (and replaces it in its tests only), so the walk
// below, which has no helper there, names it again. Tests replace it.
var cgroupMount = "/sys/fs/cgroup"

// Bounds of the search of [kernel.FindNamed]: how deep it goes and how many
// directories it will list or queue at most. findMaxDirs is a variable only so
// that a test can lower it.
const findMaxDepth = 8

var findMaxDirs = 20000

// findReadChunk is how many entries one read of a directory takes. A directory
// is read in chunks of this size and each entry is checked against the bounds
// as it comes, so that the search stops at its bound without first reading
// the whole of a directory that has very many entries. It is a variable only
// so that a test can lower it.
var findReadChunk = 256

// kernel is the part of the host that the manager reads outside systemd: the
// identity of a control group, its populated flag, the control groups below a
// base that carry a name, and the boot identity. The real implementation is a
// thin call into internal/systemd and internal/pidfd; tests use a fake,
// because internal/systemd offers no seam for the cgroup root.
type kernel interface {
	// Lookup returns the boundary at path. found is false, with a nil error,
	// only when the path positively does not exist (ENOENT with the unified
	// hierarchy present); any other failure is an error.
	Lookup(path string) (b systemd.Boundary, found bool, err error)
	// Populated reports whether the control group has a process; it fails
	// when the identity is not that of b.
	Populated(b systemd.Boundary) (bool, error)
	// FindNamed returns the paths of the directories named name at most
	// [findMaxDepth] levels below base (a control group path). It fails when
	// the search is incomplete: a bound was hit or a directory could not be
	// read.
	FindNamed(base, name string) ([]string, error)
	// BootID returns the boot identity.
	BootID() (string, error)
}

type hostKernel struct{}

var _ kernel = hostKernel{}

func (hostKernel) Lookup(p string) (systemd.Boundary, bool, error) {
	b, err := systemd.BoundaryOf(p)
	if err == nil {
		return b, true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return systemd.Boundary{}, false, err
	}
	// ENOENT proves the group gone only when the unified hierarchy is there at
	// all: without the mount every path is absent.
	if _, serr := os.Stat(cgroupMount + "/cgroup.controllers"); serr != nil {
		return systemd.Boundary{}, false, fmt.Errorf("unified cgroup hierarchy not found: %w", serr)
	}
	return systemd.Boundary{}, false, nil
}

func (hostKernel) Populated(b systemd.Boundary) (bool, error) { return systemd.Populated(b) }

func (hostKernel) BootID() (string, error) { return pidfd.BootID() }

func (hostKernel) FindNamed(base, name string) ([]string, error) {
	if base == "" || base == "/" || path.Clean(base) != base || path.Base(name) != name || name == "" {
		return nil, errors.New("invalid search base or name")
	}
	var found []string
	visited := 0
	type dir struct {
		p     string
		depth int
	}
	queue := []dir{{base, 0}}
	// scan reads one directory in chunks, appending to found and to queue.
	scan := func(d dir) error {
		f, err := os.Open(cgroupMount + d.p)
		if err != nil {
			if d.depth > 0 && errors.Is(err, fs.ErrNotExist) {
				return nil // removed since its parent was listed: not there
			}
			return err
		}
		defer f.Close()
		for {
			entries, err := f.ReadDir(findReadChunk)
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				child := d.p + "/" + e.Name()
				if e.Name() == name {
					found = append(found, child)
				}
				if d.depth+1 > findMaxDepth {
					return errors.New("control group tree is deeper than the search bound")
				}
				// The bound is on the directories seen so far (visited and
				// still queued), checked before the queue grows and before
				// the next chunk is read, so that one directory with very many
				// child groups neither grows the queue past the bound nor is
				// read to its end first.
				if visited+len(queue)+1 > findMaxDirs {
					return errors.New("control group search is over its bound")
				}
				queue = append(queue, dir{child, d.depth + 1})
			}
			switch {
			case errors.Is(err, io.EOF):
				return nil
			case err != nil && d.depth > 0 && errors.Is(err, fs.ErrNotExist):
				return nil // removed while it was read: what was listed stands
			case err != nil:
				return err
			}
		}
	}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		visited++
		if err := scan(d); err != nil {
			return nil, err
		}
	}
	return found, nil
}
