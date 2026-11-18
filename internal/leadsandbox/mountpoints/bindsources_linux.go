//go:build linux

package mountpoints

import (
	"errors"
	"strings"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
)

// openat2Func is the system call; a test replaces it to stand in for a kernel
// without openat2.
type openat2Func func(dirfd int, path string, how *unix.OpenHow) (int, error)

// bindResolveFlags is what every lookup from the root of the file system uses:
// no symbolic link and no magic link on any component, the last one included.
// Mount points are crossed on purpose: /usr and the like are mounts, and so may
// be the folder that holds a source or the state root. (The walk beneath the
// state root's handle is safefile's, which crosses no mount point.)
const bindResolveFlags = unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

// CheckBindSources is the walk of rule B1 over the bind sources, made at the
// moment of use: right before a launch, with every sandbox that could write a
// source proven stopped. bubblewrap resolves a bind source on the host, outside
// every sandbox, and follows a link there; a link planted anywhere on the way
// would bring a folder of the owner into the sandbox (rule G1). For each row it
// reaches the path the row is bound from without following a link in any
// component, and decides on the descriptor it got, not on the name, that the
// source is what the row binds. Every descriptor is closed before it returns:
// nothing is read, created or changed, and nothing an earlier call found is
// reused.
//
// The caller passes the rows [ResolveSources] gave. How a row is walked:
//
//   - A row Ataniq supplies whose source lies below the state root
//     ([SourceWalk].BelowState: the folders the lead writes, the entries of the
//     git metadata folder, the mirror, the mailbox, the published folder). The
//     state root is opened by name, once for the call, and the source is
//     opened beneath that handle in one lookup ([safefile.Root.OpenDirHandle]
//     or [safefile.Root.OpenFileHandle]: openat2 with RESOLVE_BENEATH and
//     RESOLVE_NO_SYMLINKS; the type is read from the descriptor). A mount point
//     below the state root is not crossed. Before that, the state root's own
//     path is looked up from the root of the file system without following a
//     link, and the folder that lookup found must be the folder that was opened
//     by name (same device and inode), so a link above the state root refuses
//     these rows too.
//   - Any other row Ataniq supplies (the skills, the instruction file, the git
//     identity folder, the assignment, the projection, the managed settings,
//     the probe, the spec folder, resolv.conf, the agent and Qargi installs, the
//     sources of the tree placeholders). The source is looked up from the root
//     of the file system with openat2 (RESOLVE_NO_SYMLINKS and
//     RESOLVE_NO_MAGICLINKS, O_PATH), in one lookup, and the type is read from
//     the descriptor that lookup returned. Mount points are crossed: the
//     folder that holds a source may be a mount.
//   - A row of the host (Role profile.RoleHost: /usr, the /etc files). Its
//     bound path is the real path, which the caller must have the plan bind;
//     it is looked up from the root of the file system in the same way. Its
//     type is not checked: the row is the host's own, and no lead writes it.
//
// The type a row Ataniq supplies must have is the one its walk names
// ([SourceWalk].Dir): a folder, or a regular file. A file that is bound is
// only checked for its type: its content is not read, so neither a size nor a
// link count is looked at here (the files whose content this package reads,
// when it fills and checks the git metadata folder, are capped and refused
// when hard-linked).
//
// The path checked for a row is the path bubblewrap binds,
// [ResolvedSource].Bound: for a row of the host the real path; for every other
// row the Source itself, the lexical path the plan names, which ResolveSources
// has refused unless it equals its real path, and which the row's walk must
// lead to. So the path that is checked is the path that is bound, for every
// row. A skipped row (an optional source the host lacks) is not checked.
//
// A path that is itself a link, or lies below one, is refused with [ErrSymlink]
// and the first link on the way is named; a path that does not exist is
// [ErrMissing]; one below a file, or one of another type than the row binds (a
// file for a folder, a folder for a file, a FIFO, a socket, a device), is
// [ErrWrongType]; a path that is not absolute and clean, a row of the host
// without a real path, and a row Ataniq supplies without a walk, or with a walk
// that does not lead to its source, are [ErrInvalid]: there is no weaker check
// for a row that does not say what it binds. A kernel that refuses openat2 (no
// support, or a filter), a mount point below the state root and a state root
// that changed while it was opened are [ErrUnreadable]: there is no fallback to
// a path-based open. Each problem is about one row: its Path is the path
// checked, its Inside the row's inside path, and its detail names the row. The
// problems come in the order of the rows, and the report is bounded like any
// other. The caller runs it right before every launch, after [ResolveSources];
// it says what was there at that moment, and what bubblewrap then resolves by
// name can still change if something else can write above a source, which is
// why the sources and everything above them must be Ataniq's, and why the
// design has the source passed to bubblewrap as the opened handle where
// bubblewrap can take it ([SourceWalks] gives a launcher-side step the walk to
// make; this function passes no descriptor on).
func CheckBindSources(rows []ResolvedSource) Report {
	return checkBindSources(rows, unix.Openat2)
}

func checkBindSources(rows []ResolvedSource, call openat2Func) Report {
	var rep Report
	if len(rows) == 0 {
		return rep
	}
	root, err := openFS()
	if err != nil {
		rep.add(problem(ErrUnreadable, "/", "the root of the file system cannot be opened without following links: %v", err))
		return rep
	}
	defer unix.Close(root)
	c := &sourceChecker{fs: root, call: call, states: map[string]*stateEntry{}}
	defer c.close()
	for _, row := range rows {
		if row.Skipped {
			continue
		}
		rep.add(c.check(row))
	}
	return rep
}

// openFS opens "/" as a folder handle.
func openFS() (int, error) {
	for {
		fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// lookup opens rel below the root handle without following any link.
func lookup(call openat2Func, root int, rel string) (int, error) {
	how := unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: bindResolveFlags}
	for {
		fd, err := call(root, rel, &how)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// fstatFD is fstat on a descriptor, an O_PATH one included.
func fstatFD(fd int) (unix.Stat_t, error) {
	var st unix.Stat_t
	for {
		err := unix.Fstat(fd, &st)
		if err != unix.EINTR {
			return st, err
		}
	}
}

// sourceType is the type a bind source must have.
type sourceType int

const (
	anyType sourceType = iota // a row of the host: not checked
	dirType
	fileType
)

func typeOf(dir bool) sourceType {
	if dir {
		return dirType
	}
	return fileType
}

func (t sourceType) String() string {
	if t == dirType {
		return "a folder"
	}
	return "a regular file"
}

// describeMode names the type of what fstat reported.
func describeMode(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return "a folder"
	case unix.S_IFREG:
		return "a regular file"
	case unix.S_IFLNK:
		return "a symbolic link"
	case unix.S_IFIFO:
		return "a FIFO"
	case unix.S_IFSOCK:
		return "a socket"
	case unix.S_IFCHR, unix.S_IFBLK:
		return "a device"
	}
	return "an entry of an unknown type"
}

// sourceChecker is one call of CheckBindSources: the handle on the root of the
// file system, and the state roots it has opened (one, unless the rows name
// more).
type sourceChecker struct {
	fs     int
	call   openat2Func
	states map[string]*stateEntry
}

// stateEntry is a state root opened for the call, or why it cannot be used.
type stateEntry struct {
	root *safefile.Root
	// refuse makes the problem of a row whose source lies below a state root
	// that cannot be used; nil when the state root is usable.
	refuse func(row ResolvedSource, src string) *Problem
}

// refuseRows makes the refusal of every row below a state root that cannot be
// used: a problem of the kind, about the row's own source, that names the row
// and says why (the words follow "the source of <row> ").
func refuseRows(kind, err error, format string, args ...any) func(ResolvedSource, string) *Problem {
	return func(row ResolvedSource, src string) *Problem {
		p := problem(kind, src, "the source of %s "+format, append([]any{row.Inside}, args...)...)
		p.Err = err
		return p
	}
}

func (c *sourceChecker) close() {
	for _, e := range c.states {
		if e.root != nil {
			e.root.Close()
		}
	}
}

// check walks to the bound path of one row.
func (c *sourceChecker) check(row ResolvedSource) *Problem {
	src := row.Bound()
	if src == "" {
		return problem(ErrInvalid, "", "the row has no real path to check").at(row.Inside)
	}
	if err := profile.CheckPath("bind source", src); err != nil {
		return problem(ErrInvalid, clip(src), "%s", reason(err)).at(row.Inside)
	}
	if row.Role == profile.RoleHost {
		return c.fromFS(row, src, anyType)
	}
	w := row.Walk
	switch {
	case w.Root == "" || w.Rel == "":
		return problem(ErrInvalid, src, "the row %s does not say how its source is walked and which type it binds: pass the rows ResolveSources gave", row.Inside).at(row.Inside)
	case w.Inside != row.Inside || w.Path() != src:
		return problem(ErrInvalid, src, "the walk of the row %s does not lead to its source: pass the rows ResolveSources gave", row.Inside).at(row.Inside)
	}
	// The walk leads to src, which is absolute and clean: so Root is an
	// absolute, clean path too, and Rel a clean path beneath it without "..".
	if !w.BelowState {
		return c.fromFS(row, src, typeOf(w.Dir))
	}
	return c.fromState(row, src, w)
}

// fromFS reaches src from the root of the file system in one lookup that
// follows no link, and checks the type on the descriptor the lookup returned.
func (c *sourceChecker) fromFS(row ResolvedSource, src string, want sourceType) *Problem {
	fd, link, err := c.lookupFS(src)
	switch err {
	case nil:
	case unix.ELOOP:
		return problem(ErrSymlink, src, "the source of %s is reached through a symbolic link at %s: a link is never followed", row.Inside, link).at(row.Inside)
	case unix.ENOENT:
		return problem(ErrMissing, src, "the source of %s does not exist", row.Inside).at(row.Inside)
	case unix.ENOTDIR:
		return problem(ErrWrongType, src, "a component of the source of %s is not a folder", row.Inside).at(row.Inside)
	default:
		p := problem(ErrUnreadable, src, "the source of %s cannot be opened without following links: %v", row.Inside, err)
		p.Err = err
		return p.at(row.Inside)
	}
	defer unix.Close(fd)
	if want == anyType {
		return nil
	}
	st, err := fstatFD(fd)
	if err != nil {
		p := problem(ErrUnreadable, src, "the source of %s was opened and its type cannot be read: %v", row.Inside, err)
		p.Err = err
		return p.at(row.Inside)
	}
	if is := st.Mode & unix.S_IFMT; want == dirType && is != unix.S_IFDIR || want == fileType && is != unix.S_IFREG {
		return problem(ErrWrongType, src, "the source of %s is %s, and the row binds %s", row.Inside, describeMode(st.Mode), want).at(row.Inside)
	}
	return nil
}

// lookupFS opens the absolute, clean path p from the root of the file system
// without following a link. The caller closes the descriptor. On an error it
// returns the errno, and for ELOOP the first component that is a link (named
// for the message; it decides nothing).
func (c *sourceChecker) lookupFS(p string) (fd int, link string, err error) {
	rel := strings.TrimPrefix(p, "/")
	if rel == "" {
		rel = "."
	}
	fd, err = lookup(c.call, c.fs, rel)
	if err == unix.ELOOP {
		link = firstLink(c.fs, rel, c.call)
	}
	return fd, link, err
}

// state opens the state root at dir once for the call. Its path is first
// looked up from the root of the file system without following a link; it is
// then opened by name, as the folder the walk starts from; and the two must be
// the same folder.
func (c *sourceChecker) state(dir string) *stateEntry {
	if e, ok := c.states[dir]; ok {
		return e
	}
	e := &stateEntry{}
	c.states[dir] = e
	fd, link, err := c.lookupFS(dir)
	switch err {
	case nil:
	case unix.ELOOP:
		// The same words as for a link below the state root: the source is
		// reached through it.
		e.refuse = refuseRows(ErrSymlink, nil, "is reached through a symbolic link at %s: a link is never followed", link)
		return e
	case unix.ENOENT:
		e.refuse = refuseRows(ErrMissing, nil, "lies below the state root %s, which does not exist", dir)
		return e
	case unix.ENOTDIR:
		e.refuse = refuseRows(ErrWrongType, nil, "lies below the state root %s, and a component of that path is not a folder", dir)
		return e
	default:
		e.refuse = refuseRows(ErrUnreadable, err, "lies below the state root %s, which cannot be opened without following links: %v", dir, err)
		return e
	}
	defer unix.Close(fd)
	seen, err := fstatFD(fd)
	if err != nil {
		e.refuse = refuseRows(ErrUnreadable, err, "lies below the state root %s, which was opened and cannot be examined: %v", dir, err)
		return e
	}
	root, err := openRootByName(dir)
	if err != nil {
		e.refuse = refuseRows(fromErr(dir, err).Kind, err, "lies below the state root %s, which cannot be opened: %v", dir, err)
		return e
	}
	info, err := root.Lstat(".")
	if err != nil || info.Dev != uint64(seen.Dev) || info.Ino != uint64(seen.Ino) {
		root.Close()
		e.refuse = refuseRows(ErrUnreadable, err, "lies below the state root %s, which changed while it was opened: the folder found without following a link is not the folder opened by name", dir)
		return e
	}
	e.root = root
	return e
}

// fromState reaches the source of a row below the state root from the state
// root's handle: one lookup beneath it that follows no link and crosses no
// mount point, with the type decided on the descriptor the lookup returned.
func (c *sourceChecker) fromState(row ResolvedSource, src string, w SourceWalk) *Problem {
	st := c.state(w.Root)
	if st.refuse != nil {
		return st.refuse(row, src).at(row.Inside)
	}
	var h *safefile.Handle
	var err error
	if w.Dir {
		h, err = st.root.OpenDirHandle(w.Rel)
	} else {
		h, err = st.root.OpenFileHandle(w.Rel)
	}
	if err == nil {
		h.Close()
		return nil
	}
	var p *Problem
	switch {
	case errors.Is(err, safefile.ErrSymlink):
		// Name the first component that is a link; it decides nothing.
		at := firstLinkBelow(st.root, w.Root, w.Rel)
		p = problem(ErrSymlink, src, "the source of %s is reached through a symbolic link at %s: a link is never followed", row.Inside, at)
	case isNotExist(err):
		p = problem(ErrMissing, src, "the source of %s does not exist", row.Inside)
	case errors.Is(err, safefile.ErrNotDirectory), errors.Is(err, safefile.ErrNotRegular):
		// The open has refused the source. This lookup only says what is
		// there, for the message.
		if info, lerr := st.root.Lstat(w.Rel); lerr == nil {
			p = problem(ErrWrongType, src, "the source of %s is %s, and the row binds %s", row.Inside, describe(info), typeOf(w.Dir))
		} else {
			p = problem(ErrWrongType, src, "a component of the source of %s is not a folder", row.Inside)
		}
	case errors.Is(err, safefile.ErrEscapes):
		p = problem(ErrUnreadable, src, "the source of %s lies at or beyond a mount point below the state root %s, which is not crossed: %v", row.Inside, w.Root, err)
	default:
		p = problem(ErrUnreadable, src, "the source of %s cannot be opened without following links: %v", row.Inside, err)
	}
	p.Err = err
	return p.at(row.Inside)
}

// firstLink names the first prefix of rel that is a symbolic link, as an
// absolute path, for the message of a refusal that has already happened. It
// returns the whole source when none is found (a link that was removed in the
// meantime).
func firstLink(root int, rel string, call openat2Func) string {
	comps := strings.Split(rel, "/")
	for i := range comps {
		prefix := strings.Join(comps[:i+1], "/")
		fd, err := lookup(call, root, prefix)
		if err == nil {
			unix.Close(fd)
			continue
		}
		if err == unix.ELOOP {
			return "/" + prefix
		}
		break
	}
	return "/" + rel
}

// firstLinkBelow is firstLink for a path beneath the state root's handle: the
// first prefix of rel that is a symbolic link, as a host path below dir.
func firstLinkBelow(state *safefile.Root, dir, rel string) string {
	comps := strings.Split(rel, "/")
	for i := range comps {
		prefix := strings.Join(comps[:i+1], "/")
		info, err := state.Lstat(prefix)
		if err != nil {
			break
		}
		if info.IsSymlink() {
			return dir + "/" + prefix
		}
	}
	return dir + "/" + rel
}

// clip bounds a path that came from the caller before it is repeated.
func clip(p string) string {
	if len(p) > maxDetail {
		return p[:maxDetail] + "..."
	}
	return p
}
