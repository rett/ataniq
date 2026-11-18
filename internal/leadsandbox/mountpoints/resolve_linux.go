//go:build linux

package mountpoints

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"ataniq/internal/leadsandbox/profile"
)

// Resolver evaluates symbolic links in an absolute path and returns the clean
// real path. It reports a missing path with an error that matches
// fs.ErrNotExist.
type Resolver func(path string) (string, error)

// RealResolver is the resolver of the real file system: filepath.EvalSymlinks.
func RealResolver(path string) (string, error) { return filepath.EvalSymlinks(path) }

// Readlinker reads the target of a symbolic link without following it, as
// os.Readlink does. It reports a path that is not a link, or is missing, with
// an error that matches syscall.EINVAL, fs.ErrNotExist or syscall.ENOTDIR.
type Readlinker func(path string) (string, error)

// maxLinkHops bounds the dangling links realOf follows for one path.
const maxLinkHops = 40

// ResolvedSource is one bind source of the table and its real path.
type ResolvedSource struct {
	// Inside is the target of the bind, Source the path the table names and
	// Real that path with its links evaluated.
	Inside string
	Source string
	Real   string
	// Role is the row's source role (profile.RoleHost, profile.RoleAgent and
	// the like). It decides which path is the bound one: for a row of the host
	// (profile.RoleHost, a path that equals its inside path, such as /usr or
	// /etc/localtime) the real path, which the caller passes to the plan; for
	// every other row the Source itself, which must equal Real
	// ([ResolveSources] refuses a row whose Real differs).
	Role string
	// Skipped says the source does not exist and its row is optional (it is
	// applied only if the host has it): Real is empty.
	Skipped bool
	// Walk says how the source of a row Ataniq supplies is reached by handle:
	// the folder to open by name, the path beneath it and the type the source
	// must have. [ResolveSources] sets it from the bind table for every row
	// that is not a row of the host; for a row of the host it is the zero
	// value. [CheckBindSources] refuses a row Ataniq supplies without it, or
	// with one that does not lead to Source. See [SourceWalk].
	Walk SourceWalk
}

// Bound is the path bubblewrap is to bind for the row, and the path
// [CheckBindSources] checks: Real for a row of the host, Source for every other
// row. It is empty for a skipped row.
func (r ResolvedSource) Bound() string {
	switch {
	case r.Skipped:
		return ""
	case r.Role == profile.RoleHost:
		return r.Real
	}
	return r.Source
}

// Resolution is what [ResolveSources] found: the sources in the order of the
// bind table, and the problems.
type Resolution struct {
	Sources []ResolvedSource
	Report
}

// HostSources gives the real path of each row of the host (Role
// profile.RoleHost) that exists, keyed by its inside path: the map the plan
// binds those rows from ([plan.Options].HostSources). A skipped row (an
// optional source the host lacks) has no entry, and the options must then say
// the host lacks the row ([plan.Options].HostPresent), as the plan refuses a
// source for a row it leaves out. Every other row keeps the source the inputs
// name, which [ResolveSources] has refused unless it is its own real path.
func (r Resolution) HostSources() map[string]string {
	m := map[string]string{}
	for _, s := range r.Sources {
		if s.Role == profile.RoleHost && !s.Skipped {
			m[s.Inside] = s.Real
		}
	}
	return m
}

// ResolveSources is where lexical paths meet the file system. The profile
// checks bind sources lexically, so a link could hide a never-mounted folder.
// ResolveSources resolves each host bind source of the table, and each
// never-mounted path and the other runtime's config folder too, through
// resolve, and runs the profile's never-mounted check (which covers the
// other runtime's config folder) on the real paths. A never-mounted path that
// does not exist is resolved by its longest existing prefix. A resolved path
// that cannot be used (relative, not clean, too long, a control character) is
// a problem, never dropped. The profile's state checks run again on the real
// paths too: a source that resolves into or above another lead's state, the
// lead's holder folder, another project's mirror folder, another role's
// mailbox, another project's published folder, or a coordinator's tree, home
// or login is refused, and so is one that resolves into the lead's own mailbox
// or the project's own published folder and is not the row that binds exactly
// that folder (the folders are resolved the same way; the rules are
// profile.StateFolders.Reason). It also refuses:
//
//   - a source that is missing, unless its row is optional;
//   - for every row that is not a row of the host (profile.RoleHost), a source
//     whose real path differs from its lexical one, that is, one with a
//     symbolic link anywhere on its path (ErrSymlink, naming the row and the
//     real path). bubblewrap binds the path the plan names and follows a link
//     in it, so a source Ataniq supplies must be given as its real path: then
//     the path that is checked is the path that is bound. A row of the host
//     (/usr, the /etc files) may resolve through links of the host, which no
//     lead writes; its real path is reported in [ResolvedSource].Real, and the
//     caller passes that path to the plan;
//   - a real source that overlaps a read-write folder of another row, or a
//     read-write folder that overlaps another, as the profile checks
//     lexically (no source lies in a folder the lead can write; the sources of
//     the .git rows are Ataniq's copies in the git metadata folder, and are
//     checked like any other).
//
// A never-mounted path that does not exist and is a dangling link (its
// target does not exist either) is resolved through the link: its target is
// resolved by its longest existing prefix in turn, so a folder the owner
// creates there later is already known by its real path. A link that cannot be
// read, a chain of more than 40 links, or a target whose ".." follows another
// component (which a clean would resolve differently from the kernel) is a
// problem of kind ErrUnreadable, never the lexical path alone.
//
// It reads no file and creates nothing; it reads the target of a link with
// os.Readlink. The error is set only when the inputs are refused or resolve is
// nil (a *Problem of kind ErrInvalid).
//
// It works on names, by design. Resolving a name (filepath.EvalSymlinks for
// the real file system) is kept for two things: the real path of a row of the
// host, which is what the plan binds for it, and the comparison on real paths
// described above, which a walk by handle cannot make (a never-mounted path may
// not exist, and a rule about a folder is a rule about its name). What it
// resolved for a row Ataniq supplies is never opened or bound: such a row is
// bound by the name the inputs give, which is refused here unless it is its own
// real path, and the object behind that name is checked by [CheckBindSources],
// by handle, at the moment of use. Each row Ataniq supplies carries the walk
// for that check ([ResolvedSource].Walk).
func ResolveSources(in profile.Inputs, resolve Resolver) (Resolution, error) {
	return ResolveSourcesWith(in, resolve, os.Readlink)
}

// ResolveSourcesWith is ResolveSources with the reader of links given, for a
// caller whose resolver is not the real file system.
func ResolveSourcesWith(in profile.Inputs, resolve Resolver, readlink Readlinker) (Resolution, error) {
	if resolve == nil || readlink == nil {
		return Resolution{}, problem(ErrInvalid, "", "no resolver given")
	}
	t, err := in.Binds()
	if err != nil {
		p := problem(ErrInvalid, "", "%v", err)
		p.Err = err
		return Resolution{}, p
	}
	var res Resolution
	never := append([]string(nil), in.NeverMounted...)
	if other := in.OtherConfigDir(); other != "" {
		never = append(never, other)
	}
	for _, n := range append([]string(nil), never...) {
		r, err := realOf(resolve, readlink, n)
		switch {
		case err != nil:
			res.add(problem(ErrUnreadable, n, "the never-mounted path cannot be resolved: %v", err))
		case r != n:
			if cerr := profile.CheckPath("resolved path", r); cerr != nil {
				res.add(problem(ErrUnreadable, n, "the never-mounted path resolves to a path that is not usable: %v", cerr))
				continue
			}
			never = append(never, r)
		}
	}
	// The state folders the profile knows, on their real paths: a source in
	// another lead's state, in the lead's holder folder, in another project's
	// mirror, in another role's mailbox, in another project's published folder
	// or in a coordinator's tree, home or login is refused whatever the link
	// that leads there (profile.StateFolders.Reason). A folder that does not
	// exist is resolved by its longest existing prefix.
	st := in.Layout().StateFolders()
	stateOK := true
	for _, dst := range st.Paths() {
		lexical := *dst
		r, err := realOf(resolve, readlink, lexical)
		if err == nil {
			err = profile.CheckPath("resolved path", r)
		}
		if err != nil {
			res.add(problem(ErrUnreadable, lexical, "the state folder cannot be resolved: %v", err))
			stateOK = false
			continue
		}
		*dst = r
	}
	realPaths := make([]string, len(t))
	for i, b := range t {
		if b.Source == "" {
			continue
		}
		r, err := resolve(b.Source)
		if err != nil {
			if isNotExist(err) && b.Optional {
				res.Sources = append(res.Sources, ResolvedSource{Inside: b.Inside, Source: b.Source, Role: b.SourceRole, Skipped: true})
			} else if isNotExist(err) {
				res.add(problem(ErrMissing, b.Source, "the source of %s does not exist", b.Inside).at(b.Inside))
			} else {
				res.add(problem(ErrUnreadable, b.Source, "the source of %s cannot be resolved: %v", b.Inside, err).at(b.Inside))
			}
			continue
		}
		if cerr := profile.CheckPath("resolved path", r); cerr != nil {
			res.add(problem(ErrUnreadable, b.Source, "the source of %s resolves to a path that is not usable: %v", b.Inside, cerr).at(b.Inside))
			continue
		}
		realPaths[i] = r
		walk, _ := sourceWalk(in.StateRoot, b)
		res.Sources = append(res.Sources, ResolvedSource{Inside: b.Inside, Source: b.Source, Real: r, Role: b.SourceRole, Walk: walk})
		// Rule G1: bubblewrap binds the path the plan names, and follows a
		// link in it. A row Ataniq supplies must name its real path, so that
		// the path that is checked is the path that is bound.
		if b.SourceRole != profile.RoleHost && r != b.Source {
			res.add(problem(ErrSymlink, b.Source, "the source of %s is reached through a symbolic link (its real path is %s): give the real path, because bubblewrap binds the path the plan names and follows a link in it", b.Inside, r).at(b.Inside))
		}
		if cerr := profile.CheckNeverMounted(r, never); cerr != nil {
			res.add(problem(ErrNeverMounted, b.Source, "the source of %s resolves to %s: %s", b.Inside, r, reason(cerr)).at(b.Inside))
		}
		if stateOK {
			if why := st.Reason(r, b.SourceRole); why != "" {
				res.add(problem(ErrNeverMounted, b.Source, "the source of %s resolves to %s, which %s", b.Inside, r, why).at(b.Inside))
			}
		}
	}
	for i, b := range t {
		if realPaths[i] == "" {
			continue
		}
		for j, w := range t {
			if w.Kind != profile.KindRwBind || realPaths[j] == "" || w.Inside == b.Inside {
				continue
			}
			if related(realPaths[i], realPaths[j]) {
				res.add(problem(ErrOverlap, b.Source, "the source of %s resolves to %s, which overlaps the lead-writable folder %s (%s)", b.Inside, realPaths[i], realPaths[j], w.SourceRole).at(b.Inside))
			}
		}
	}
	return res, nil
}

// realOf resolves p through resolve. A path that does not exist is resolved by
// its longest existing prefix, with the rest appended unchanged, so that a
// link above an absent path is still evaluated. A prefix that does not exist
// but is a link (dangling) is followed to its target the same way, at most
// maxLinkHops times: bubblewrap and the owner would both create the target
// when they next write through the link. The result is not checked.
func realOf(resolve Resolver, readlink Readlinker, p string) (string, error) {
	hops := 0
	rest := ""
	for q := p; ; {
		r, err := resolve(q)
		if err == nil {
			if r == "/" && rest != "" {
				return rest, nil
			}
			return r + rest, nil
		}
		if !isNotExist(err) {
			return "", err
		}
		target, lerr := readlink(q)
		switch {
		case lerr == nil:
			// q is a dangling link: continue with its target and the rest.
			if hops++; hops > maxLinkHops {
				return "", fmt.Errorf("more than %d symbolic links on the way to %s", maxLinkHops, p)
			}
			next, err := linkTarget(resolve, q, target)
			if err != nil {
				return "", err
			}
			// Resolve the target; the rest is appended to its real path.
			q = next
			continue
		case isNotExist(lerr) || errors.Is(lerr, syscall.EINVAL):
			// Not a link: strip a component as before.
		default:
			return "", fmt.Errorf("cannot read %s as a link: %w", q, lerr)
		}
		parent := path.Dir(q)
		if parent == q {
			return "", err
		}
		rest = "/" + path.Base(q) + rest
		q = parent
	}
}

// linkTarget makes the target of the dangling link at q absolute: a relative
// target is taken from the real folder that holds the link. A ".." that
// follows another component is refused, because a clean would drop the
// component without looking at what it is.
func linkTarget(resolve Resolver, q, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("the link %s has an empty target", q)
	}
	seenName := false
	for _, c := range strings.Split(target, "/") {
		switch c {
		case "", ".":
		case "..":
			if seenName {
				return "", fmt.Errorf("the target %q of the link %s has a \"..\" after another component", target, q)
			}
		default:
			seenName = true
		}
	}
	if path.IsAbs(target) {
		return path.Clean(target), nil
	}
	dir, err := resolve(path.Dir(q))
	if err != nil {
		return "", fmt.Errorf("the folder of the link %s cannot be resolved: %w", q, err)
	}
	return path.Join(dir, target), nil
}

func reason(err error) string {
	if r, ok := err.(*profile.Refusal); ok {
		return r.Reason
	}
	return err.Error()
}

// related reports whether a and b are equal or one lies below the other.
func related(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
