//go:build linux

package mountpoints

import (
	"strings"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/profile"
)

// openat2Func is the system call; a test replaces it to stand in for a kernel
// without openat2.
type openat2Func func(dirfd int, path string, how *unix.OpenHow) (int, error)

// bindResolveFlags is what every lookup of a bind source uses: no symbolic
// link and no magic link on any component, the last one included. Mount
// points are crossed on purpose: /usr and the like are mounts.
const bindResolveFlags = unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

// CheckBindSources checks, without following any link, that the path each
// bind row will be bound from is reached by a path with no symbolic link in any
// component (rule G1). bubblewrap resolves a bind source on the host, outside
// every sandbox, and follows a link there; a link planted anywhere on the way
// would bring a folder of the owner into the sandbox. For each row it opens the
// root of the file system and then the row's bound path below it with openat2
// (RESOLVE_NO_SYMLINKS and RESOLVE_NO_MAGICLINKS, O_PATH), and closes the
// descriptor at once: nothing is read, created or changed.
//
// The caller passes the rows [ResolveSources] gave. The path checked for a row
// is the path bubblewrap binds, [ResolvedSource].Bound: for a row of the host
// (Role profile.RoleHost: /usr, the /etc files), whose path may go through a
// link of the host that no lead writes, the real path, which the caller must
// have the plan bind; for every other row the Source
// itself, the lexical path the plan names, which ResolveSources has refused
// unless it equals its real path. So the path that is checked is the path that
// is bound, for every row. A skipped row (an optional source the host lacks) is
// not checked.
//
// A path that is itself a link, or lies below one, is refused with [ErrSymlink]
// and the first link on the way is named; a path that does not exist is
// [ErrMissing], one below a file [ErrWrongType], a path that is not absolute and
// clean (or a row of the host without a real path) [ErrInvalid], and a kernel
// that refuses openat2 (no support, or a filter) [ErrUnreadable]: there is no
// fallback to a path-based open. Each problem is about one row: its Path is the
// path checked, its Inside the row's inside path, and its detail names the row.
// The problems come in the order of the rows, and the report is bounded like any
// other. The caller runs it right before every launch, after [ResolveSources];
// it says what the path was at that moment, and what bubblewrap then resolves
// can still change if something else can write above a source, which is why the
// sources and everything above them must be Ataniq's.
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
	for _, row := range rows {
		if row.Skipped {
			continue
		}
		rep.add(checkOneSource(root, row, call))
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

func checkOneSource(root int, row ResolvedSource, call openat2Func) *Problem {
	src := row.Bound()
	if src == "" {
		return problem(ErrInvalid, "", "the row has no real path to check").at(row.Inside)
	}
	if err := profile.CheckPath("bind source", src); err != nil {
		return problem(ErrInvalid, clip(src), "%s", reason(err)).at(row.Inside)
	}
	rel := strings.TrimPrefix(src, "/")
	if rel == "" {
		rel = "."
	}
	fd, err := lookup(call, root, rel)
	if err == nil {
		unix.Close(fd)
		return nil
	}
	switch err {
	case unix.ELOOP:
		// Name the first component that is a link; it decides nothing.
		at := firstLink(root, rel, call)
		return problem(ErrSymlink, src, "the source of %s is reached through a symbolic link at %s: a link is never followed", row.Inside, at).at(row.Inside)
	case unix.ENOENT:
		return problem(ErrMissing, src, "the source of %s does not exist", row.Inside).at(row.Inside)
	case unix.ENOTDIR:
		return problem(ErrWrongType, src, "a component of the source of %s is not a folder", row.Inside).at(row.Inside)
	}
	p := problem(ErrUnreadable, src, "the source of %s cannot be opened without following links: %v", row.Inside, err).at(row.Inside)
	p.Err = err
	return p
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

// clip bounds a path that came from the caller before it is repeated.
func clip(p string) string {
	if len(p) > maxDetail {
		return p[:maxDetail] + "..."
	}
	return p
}
