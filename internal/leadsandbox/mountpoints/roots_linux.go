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

// openRootByName opens a folder by its name as a handle. It is the only way
// this package opens a folder by name, and it is used for the state root alone:
// a folder the caller owns, with parents no lead can change. Every folder below
// the state root is opened from that handle (see subRoot). Nothing but a test
// replaces it: a test records every folder that is opened by name.
var openRootByName = safefile.OpenRoot

// afterSubRoot is called with the host path of a folder right after the folder
// was opened from a handle, before anything is done with it. Nothing but a
// test sets it: it stands in for a lead that changes what the name holds
// between the lookup and the next step.
var afterSubRoot = func(dir string) {}

// openState opens the state root by name.
func openState(stateRoot string) (*safefile.Root, *Problem) {
	r, err := openRootByName(stateRoot)
	if err != nil {
		return nil, fromErr(stateRoot, err)
	}
	return r, nil
}

// subRoot opens the folder at dir, which lies below the state root, from the
// state root's handle and not by name: one lookup without links, the type
// decided on the descriptor it returned ([safefile.Root.SubRoot]). The handle
// it returns is on the folder that was looked up, whatever its name holds
// afterwards, so the caller uses that handle for every later step and never
// opens dir again. A link on the way, the folder itself included, is a problem
// of kind ErrSymlink, anything but a folder ErrWrongType, an absent folder
// ErrMissing and a mount point on the way ErrUnreadable.
func subRoot(state *safefile.Root, stateRoot, dir string) (*safefile.Root, *Problem) {
	rel, ok := strings.CutPrefix(dir, stateRoot+"/")
	if !ok || rel == "" {
		return nil, problem(ErrInvalid, dir, "the folder does not lie below the state root %s: only a folder below it is opened, from its handle", stateRoot)
	}
	r, err := state.SubRoot(rel)
	if err != nil {
		return nil, fromErr(dir, err)
	}
	afterSubRoot(dir)
	return r, nil
}

// rootSet opens the folders the mount points lie in, and the other folders of
// the lead below the state root, once each. The state root is opened by name;
// every other folder is opened from the state root's handle (subRoot), and the
// handle is kept, so a folder is looked up once and everything later is beneath
// the descriptor that lookup returned.
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
		s.state, s.stateErr = openState(s.stateRoot)
	}
	return s.state, s.stateErr
}

// get opens the folder at dir, once: a later call returns the same handle.
// When the folder cannot be used it returns the problem the first time and
// (nil, nil) afterwards: the caller reports a folder once and skips what lies
// in it.
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
	state, prob := s.stateHandle()
	if prob != nil {
		return nil, prob
	}
	return subRoot(state, s.stateRoot, dir)
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
