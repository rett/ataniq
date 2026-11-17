//go:build linux

package mountpoints

import (
	"path"
	"path/filepath"
	"strings"

	"ataniq/internal/leadsandbox/profile"
)

// Resolver evaluates symbolic links in an absolute path and returns the clean
// real path. It reports a missing path with an error that matches
// fs.ErrNotExist.
type Resolver func(path string) (string, error)

// RealResolver is the resolver of the real file system: filepath.EvalSymlinks.
func RealResolver(path string) (string, error) { return filepath.EvalSymlinks(path) }

// ResolvedSource is one host bind source and its real path.
type ResolvedSource struct {
	// Inside is the target of the bind, Source the path the table names and
	// Real that path with its links evaluated.
	Inside string
	Source string
	Real   string
	// Skipped says the source does not exist and its row is optional (it is
	// applied only if the host has it): Real is empty.
	Skipped bool
}

// Resolution is what [ResolveSources] found: the sources in the order of the
// bind table, and the problems.
type Resolution struct {
	Sources []ResolvedSource
	Report
}

// ResolveSources is where lexical paths meet the file system. The profile
// checks bind sources lexically, so a link could hide a never-mounted folder.
// ResolveSources resolves each host bind source of the table, and each
// never-mounted path and the other runtime's config folder too, through
// resolve, and runs the profile's never-mounted check (which covers the
// other runtime's config folder) on the real paths. A never-mounted path that
// does not exist is resolved by its longest existing prefix. A resolved path
// that cannot be used (relative, not clean, too long, a control character) is
// a problem, never dropped. It also refuses:
//
//   - a source that is missing, unless its row is optional;
//   - a real source that overlaps a read-write folder of another row, or a
//     read-write folder that overlaps another, as the profile checks
//     lexically (the .git rows, which are bound over the tree on purpose,
//     are the exception);
//   - a .git row whose source is reached through a symbolic link.
//
// It reads no file and creates nothing. The error is set only when the inputs
// are refused or resolve is nil (a *Problem of kind ErrInvalid).
func ResolveSources(in profile.Inputs, resolve Resolver) (Resolution, error) {
	if resolve == nil {
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
		r, err := realOf(resolve, n)
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
	realPaths := make([]string, len(t))
	for i, b := range t {
		if b.Source == "" {
			continue
		}
		r, err := resolve(b.Source)
		if err != nil {
			if isNotExist(err) && b.Optional {
				res.Sources = append(res.Sources, ResolvedSource{Inside: b.Inside, Source: b.Source, Skipped: true})
			} else if isNotExist(err) {
				res.add(problem(ErrMissing, b.Source, "the source of %s does not exist", b.Inside))
			} else {
				res.add(problem(ErrUnreadable, b.Source, "the source of %s cannot be resolved: %v", b.Inside, err))
			}
			continue
		}
		if cerr := profile.CheckPath("resolved path", r); cerr != nil {
			res.add(problem(ErrUnreadable, b.Source, "the source of %s resolves to a path that is not usable: %v", b.Inside, cerr))
			continue
		}
		realPaths[i] = r
		res.Sources = append(res.Sources, ResolvedSource{Inside: b.Inside, Source: b.Source, Real: r})
		if cerr := profile.CheckNeverMounted(r, never); cerr != nil {
			res.add(problem(ErrNeverMounted, b.Source, "the source of %s resolves to %s: %s", b.Inside, r, reason(cerr)))
		}
		if b.SourceRole == profile.RoleTreeGit && r != b.Source {
			res.add(problem(ErrSymlink, b.Source, "the source of %s is reached through a symbolic link (real path %s)", b.Inside, r))
		}
	}
	for i, b := range t {
		if realPaths[i] == "" || b.SourceRole == profile.RoleTreeGit {
			continue
		}
		for j, w := range t {
			if w.Kind != profile.KindRwBind || realPaths[j] == "" || w.Inside == b.Inside {
				continue
			}
			if related(realPaths[i], realPaths[j]) {
				res.add(problem(ErrOverlap, b.Source, "the source of %s resolves to %s, which overlaps the lead-writable folder %s (%s)", b.Inside, realPaths[i], realPaths[j], w.SourceRole))
			}
		}
	}
	return res, nil
}

// realOf resolves p through resolve. A path that does not exist is resolved by
// its longest existing prefix, with the rest appended unchanged, so that a
// link above an absent path is still evaluated. The result is not checked.
func realOf(resolve Resolver, p string) (string, error) {
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
		parent := path.Dir(q)
		if parent == q {
			return "", err
		}
		rest = "/" + path.Base(q) + rest
		q = parent
	}
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
