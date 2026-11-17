//go:build linux

package mountpoints

import (
	"strings"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
)

// Modes of what Prepare makes, before the umask.
const (
	dirMode  = 0o755
	fileMode = 0o644
)

// maxExclude bounds .git/info/exclude when it is read.
const maxExclude = 1 << 20

// rootSet opens the folders the mount points lie in, once each. A folder that
// lies below the state root is first looked up from the state root without
// following links, so that a link at any component of its path is found.
type rootSet struct {
	stateRoot string
	state     *safefile.Root
	stateErr  *Problem
	stateDone bool
	roots     map[string]*rootEntry
}

type rootEntry struct {
	root     *safefile.Root
	prob     *Problem
	reported bool
}

func newRootSet(in profile.Inputs) *rootSet {
	return &rootSet{stateRoot: in.StateRoot, roots: map[string]*rootEntry{}}
}

func (s *rootSet) close() {
	for _, e := range s.roots {
		if e.root != nil {
			e.root.Close()
		}
	}
	if s.state != nil {
		s.state.Close()
	}
}

// stateHandle opens the state root once.
func (s *rootSet) stateHandle() (*safefile.Root, *Problem) {
	if !s.stateDone {
		s.stateDone = true
		r, err := safefile.OpenRoot(s.stateRoot)
		if err != nil {
			s.stateErr = fromErr(s.stateRoot, err)
		} else {
			s.state = r
		}
	}
	return s.state, s.stateErr
}

// get opens the folder at dir. When it cannot be used it returns the problem
// the first time and (nil, nil) afterwards: the caller reports a folder once
// and skips what lies in it.
func (s *rootSet) get(dir string) (*safefile.Root, *Problem) {
	if e, ok := s.roots[dir]; ok {
		if e.prob != nil && !e.reported {
			e.reported = true
			return nil, e.prob
		}
		return e.root, nil
	}
	e := &rootEntry{}
	s.roots[dir] = e
	e.root, e.prob = s.open(dir)
	if e.prob != nil {
		e.reported = true
		return nil, e.prob
	}
	return e.root, nil
}

func (s *rootSet) open(dir string) (*safefile.Root, *Problem) {
	if rel, ok := strings.CutPrefix(dir, s.stateRoot+"/"); ok {
		state, prob := s.stateHandle()
		if prob != nil {
			return nil, prob
		}
		info, err := state.Lstat(rel)
		if err != nil {
			return nil, fromErr(dir, err)
		}
		if info.IsSymlink() {
			return nil, problem(ErrSymlink, dir, "the folder is a symbolic link")
		}
		if !info.IsDir() {
			return nil, problem(ErrWrongType, dir, "not a folder")
		}
	}
	r, err := safefile.OpenRoot(dir)
	if err != nil {
		return nil, fromErr(dir, err)
	}
	return r, nil
}

// inspect looks up one mount point from its folder without following links.
// It returns whether the entry exists and has the right type; a problem for a
// link, a wrong type, or an error other than "not there".
func inspect(root *safefile.Root, p Point) (present bool, prob *Problem) {
	info, err := root.Lstat(p.Rel)
	if err != nil {
		if isNotExist(err) {
			return false, nil
		}
		return false, fromErr(p.HostPath, err)
	}
	switch {
	case info.IsSymlink():
		return false, problem(ErrSymlink, p.HostPath, "the mount point is a symbolic link")
	case p.Dir && !info.IsDir():
		return false, problem(ErrWrongType, p.HostPath, "want a folder, found %s", describe(info))
	case !p.Dir && !info.IsRegular():
		return false, problem(ErrWrongType, p.HostPath, "want a regular file, found %s", describe(info))
	}
	return true, nil
}

func describe(info safefile.Info) string {
	switch {
	case info.IsDir():
		return "a folder"
	case info.IsRegular():
		return "a regular file"
	case info.IsSymlink():
		return "a symbolic link"
	}
	return "a " + info.Type().String() + " entry"
}
