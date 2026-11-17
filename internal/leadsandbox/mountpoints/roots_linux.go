//go:build linux

package mountpoints

import (
	"fmt"
	"io/fs"
	"path"
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

// walked is one path the start walk looks at (rule B1): a mount point, or a
// folder that holds mount points and is walked itself.
type walked struct {
	Point
	// holder says the path is not a mount point but a folder that mount
	// points lie in, walked itself so that a problem with it names it: the
	// tree's .git. Nothing is bound at it and nothing makes it; it may be
	// absent, and what lies below it is then reported as missing.
	holder bool
}

// what names the kind of path in a message.
func (w walked) what() string {
	if w.holder {
		return "the folder the mount points lie in"
	}
	return "the mount point"
}

// walkList is the list of the start walk, in the order of the points: every
// mount point, and before the first of the tree's .git rows the .git folder
// itself.
func walkList(pts []Point) []walked {
	out := make([]walked, 0, len(pts)+1)
	seen := map[string]bool{}
	for _, p := range pts {
		if rel := path.Dir(p.Rel); p.Git && rel != "." && !seen[p.RootDir+"/"+rel] {
			seen[p.RootDir+"/"+rel] = true
			out = append(out, walked{holder: true, Point: Point{
				Inside:   path.Dir(p.Inside),
				HostPath: p.RootDir + "/" + rel,
				Root:     p.Root,
				RootDir:  p.RootDir,
				Rel:      rel,
				Dir:      true,
				Git:      true,
			}})
		}
		out = append(out, walked{Point: p})
	}
	return out
}

// below reports whether the path w lies below one of the refused folders of
// its root: a folder that was refused is reported once, and what lies in it is
// not looked at.
func (w walked) below(refused []walked) bool {
	for _, r := range refused {
		if r.RootDir == w.RootDir && strings.HasPrefix(w.Rel, r.Rel+"/") {
			return true
		}
	}
	return false
}

// inspect walks to one path of the start walk from the handle of the folder it
// lies in: one lookup beneath the handle that follows no link, in any
// component, with the type decided on the descriptor the lookup returned
// (rule B1). It returns whether the entry exists and has the right type; a
// problem for a link, a wrong type, or an error other than "not there". The
// problem is about the path walked; when it is a component on the way that is
// refused (a link, or not a folder), the detail names that component.
func inspect(root *safefile.Root, w walked) (present bool, prob *Problem) {
	info, err := root.Lstat(w.Rel)
	if err != nil {
		if isNotExist(err) {
			return false, nil
		}
		prob := fromErr(w.HostPath, err)
		if at, why := refusedOnTheWay(root, w.Rel); at != "" {
			prob.Detail = clip(fmt.Sprintf("%s/%s, on the way to %s, %s", w.RootDir, at, w.what(), why))
		}
		return false, prob
	}
	switch {
	case info.IsSymlink():
		return false, problem(ErrSymlink, w.HostPath, "%s is a symbolic link", w.what())
	case w.Dir && !info.IsDir():
		return false, problem(ErrWrongType, w.HostPath, "%s: want a folder, found %s", w.what(), describe(info))
	case !w.Dir && !info.IsRegular():
		return false, problem(ErrWrongType, w.HostPath, "%s: want a regular file, found %s", w.what(), describe(info))
	}
	return true, nil
}

// refusedOnTheWay names the first component above rel that is a symbolic link
// or not a folder, and says which, for the message of a refusal that has
// already happened: it decides nothing. It returns "" when it finds none (the
// component changed in the meantime).
func refusedOnTheWay(root *safefile.Root, rel string) (at, why string) {
	comps := strings.Split(rel, "/")
	for i := 1; i < len(comps); i++ {
		prefix := strings.Join(comps[:i], "/")
		info, err := root.Lstat(prefix)
		switch {
		case err != nil:
			return "", ""
		case info.IsSymlink():
			return prefix, "is a symbolic link: nothing is looked up through a link"
		case !info.IsDir():
			return prefix, "is " + describe(info) + ", not a folder"
		}
	}
	return "", ""
}

func describe(info safefile.Info) string {
	switch {
	case info.IsDir():
		return "a folder"
	case info.IsRegular():
		return "a regular file"
	case info.IsSymlink():
		return "a symbolic link"
	case info.Mode&fs.ModeNamedPipe != 0:
		return "a FIFO"
	case info.Mode&fs.ModeSocket != 0:
		return "a socket"
	case info.Mode&fs.ModeDevice != 0:
		return "a device"
	}
	return "a " + info.Type().String() + " entry"
}
