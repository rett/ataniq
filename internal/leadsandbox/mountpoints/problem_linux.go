//go:build linux

package mountpoints

import (
	"errors"
	"fmt"
	"io/fs"

	"ataniq/internal/leadsandbox/safefile"
)

// The kinds of problem. A [Problem] wraps exactly one of them, so a caller
// tests with errors.Is.
var (
	// ErrNoStatement: Prepare was called without the caller's statement that
	// no lead has run in these folders yet.
	ErrNoStatement = errors.New("the caller did not state that no lead has run in these folders yet")
	// ErrInvalid: the inputs or a placeholder cannot be used (the profile
	// refuses them, or a mount point has a kind this package does not make).
	ErrInvalid = errors.New("invalid input")
	// ErrMissing: an entry that must exist does not.
	ErrMissing = errors.New("missing")
	// ErrWrongType: an entry is not of the type its bind needs (a folder for a
	// folder, a regular file for a file), or a parent component is not a folder.
	ErrWrongType = errors.New("wrong type")
	// ErrSymlink: a path component, the last one included, is a symbolic link.
	ErrSymlink = errors.New("a path component is a symbolic link")
	// ErrNotExcluded: a tree placeholder is not listed in .git/info/exclude.
	ErrNotExcluded = errors.New("not listed in .git/info/exclude")
	// ErrUnreadable: an entry could not be examined or read (an operating
	// system error, a file over the cap, a mount point crossed).
	ErrUnreadable = errors.New("cannot be examined or read")
	// ErrNotOwnClone: the tree is not the lead's own full clone.
	ErrNotOwnClone = errors.New("not the lead's own clone")
	// ErrMode: a folder does not have mode 0700.
	ErrMode = errors.New("not mode 0700")
	// ErrOwner: a folder is not owned by the caller's user.
	ErrOwner = errors.New("not owned by the caller")
	// ErrNeverMounted: a real path is, or lies in or above, a never-mounted
	// path or the other runtime's config folder.
	ErrNeverMounted = errors.New("a never-mounted path")
	// ErrIncomplete: a Prepare that failed or was interrupted left the
	// temporary folder it builds the git metadata folder in. It is refused
	// and never reused.
	ErrIncomplete = errors.New("left by a Prepare that did not finish")
	// ErrInclude: the clone's .git/config has an include or includeIf
	// section, which makes git read another file as configuration; Prepare
	// refuses to copy it.
	ErrInclude = errors.New("the git configuration has an include or includeIf section")
	// ErrOverlap: a real source overlaps a lead-writable folder, or two
	// lead-writable folders overlap.
	ErrOverlap = errors.New("overlaps a lead-writable folder")
)

const (
	// MaxProblems bounds the list a [Report] keeps.
	MaxProblems = 128
	// maxDetail bounds the text of one problem.
	maxDetail = 300
)

// Problem is one thing a check or Prepare refused. Kind is one of the Err
// values above; Path is the host path (or the profile path) it is about;
// Detail says why; Err is the operating system or safefile error behind it,
// when there is one. Names in Path and Detail come from the caller's layout,
// not from the lead, except where the detail quotes an operating system error.
type Problem struct {
	Kind   error
	Path   string
	Detail string
	Err    error
}

// Error implements error.
func (p *Problem) Error() string {
	s := "mountpoints: " + p.Kind.Error()
	if p.Path != "" {
		s += ": " + p.Path
	}
	if p.Detail != "" {
		s += ": " + p.Detail
	}
	return s
}

// Unwrap returns the kind and the underlying error, so errors.Is sees both.
func (p *Problem) Unwrap() []error {
	if p.Err == nil {
		return []error{p.Kind}
	}
	return []error{p.Kind, p.Err}
}

func problem(kind error, path, format string, args ...any) *Problem {
	d := fmt.Sprintf(format, args...)
	if len(d) > maxDetail {
		d = d[:maxDetail] + "..."
	}
	return &Problem{Kind: kind, Path: path, Detail: d}
}

// fromErr turns a safefile or operating system error into a problem about the
// host path.
func fromErr(path string, err error) *Problem {
	kind := ErrUnreadable
	switch {
	case errors.Is(err, safefile.ErrSymlink):
		kind = ErrSymlink
	case errors.Is(err, safefile.ErrNotDirectory), errors.Is(err, safefile.ErrNotRegular), errors.Is(err, safefile.ErrHardLink):
		kind = ErrWrongType
	case errors.Is(err, fs.ErrNotExist):
		kind = ErrMissing
	}
	p := problem(kind, path, "%v", err)
	p.Err = err
	return p
}

// Report is what a check found: every problem, in the order of the bind table
// (and the order of the checks), up to [MaxProblems]. Truncated says there
// were more.
type Report struct {
	Problems  []*Problem
	Truncated bool
}

// OK reports that nothing was found.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Has reports whether a problem of the kind was found.
func (r Report) Has(kind error) bool {
	for _, p := range r.Problems {
		if errors.Is(p, kind) {
			return true
		}
	}
	return false
}

// Err returns nil when nothing was found, else an error that matches every
// kind found (errors.Is) and prints each problem.
func (r Report) Err() error {
	if r.OK() {
		return nil
	}
	return &ReportError{Report: r}
}

func (r *Report) add(p *Problem) {
	if p == nil {
		return
	}
	if len(r.Problems) >= MaxProblems {
		r.Truncated = true
		return
	}
	r.Problems = append(r.Problems, p)
}

// ReportError is the error [Report.Err] returns.
type ReportError struct{ Report Report }

// Error implements error: the problems, one after another.
func (e *ReportError) Error() string {
	s := fmt.Sprintf("mountpoints: %d problem(s)", len(e.Report.Problems))
	if e.Report.Truncated {
		s += " (more not listed)"
	}
	for _, p := range e.Report.Problems {
		s += "; " + p.Error()
	}
	return s
}

// Unwrap returns the problems.
func (e *ReportError) Unwrap() []error {
	out := make([]error, len(e.Report.Problems))
	for i, p := range e.Report.Problems {
		out[i] = p
	}
	return out
}
