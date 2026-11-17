package intake

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/intake/execconfig"
)

// Defaults of [Limits].
const (
	// DefaultTimeout is the time limit of one call.
	DefaultTimeout = 60 * time.Second
	// DefaultMaxCommits is the most commits a review lists.
	DefaultMaxCommits = 5000
	// DefaultMaxPaths is the most paths a review lists.
	DefaultMaxPaths = 20000
	// DefaultMaxDiffBytes is the most bytes of patch [Diff] reads from git.
	DefaultMaxDiffBytes int64 = 16 << 20
)

// Errors of this package. Test them with errors.Is.
var (
	// ErrNoRef means that a ref does not exist under the exact name given.
	ErrNoRef = errors.New("intake: ref not found")
	// ErrLimit means that a limit of [Limits] was reached: too many commits,
	// too many paths, too long a path, too much patch, or too much time.
	ErrLimit = errors.New("intake: limit reached")
	// ErrRefName means that a ref name is not accepted: it is outside the
	// namespace the package expects, or it is not a valid full ref name.
	ErrRefName = errors.New("intake: ref name not accepted")
	// ErrNotCommit means that a ref does not point at a commit object
	// itself. A tag object or a blob under a ref is refused.
	ErrNotCommit = errors.New("intake: ref does not point at a commit")
)

// Source is the repository to read: a bare repository that Ataniq owns.
type Source struct {
	// Git is the git executable, from gitenv.New or gitenv.Find.
	Git gitenv.Git
	// GitDir is the absolute path of the bare repository.
	GitDir string
}

// Limits bounds one call of [Inspect] or [Diff]. A zero field means its
// default; a negative field is an error.
type Limits struct {
	// Timeout is the time limit of the whole call, not of each git run.
	Timeout time.Duration
	// MaxCommits is the most commits in base..tip.
	MaxCommits int
	// MaxPaths is the most paths in the net change, the most entries in
	// the tip's tree, and the base of the limit on the paths of the
	// commit-by-commit changes (four times as many records).
	MaxPaths int
	// MaxDiffBytes is the most bytes of patch git may write for [Diff].
	MaxDiffBytes int64
}

// DefaultLimits returns the limits used for a zero field: 60 seconds, 5000
// commits, 20000 paths and 16 MiB of patch.
func DefaultLimits() Limits {
	return Limits{
		Timeout:      DefaultTimeout,
		MaxCommits:   DefaultMaxCommits,
		MaxPaths:     DefaultMaxPaths,
		MaxDiffBytes: DefaultMaxDiffBytes,
	}
}

// resolved returns l with the defaults filled in, or an error for a
// negative field.
func (l Limits) resolved() (Limits, error) {
	if l.Timeout < 0 || l.MaxCommits < 0 || l.MaxPaths < 0 || l.MaxDiffBytes < 0 {
		return Limits{}, errors.New("intake: limits must not be negative")
	}
	d := DefaultLimits()
	if l.Timeout == 0 {
		l.Timeout = d.Timeout
	}
	if l.MaxCommits == 0 {
		l.MaxCommits = d.MaxCommits
	}
	if l.MaxPaths == 0 {
		l.MaxPaths = d.MaxPaths
	}
	if l.MaxDiffBytes == 0 {
		l.MaxDiffBytes = d.MaxDiffBytes
	}
	return l, nil
}

// Request names what to review.
type Request struct {
	// BaseRef is the full name of the upstream branch. It must start with
	// "refs/heads/".
	BaseRef string
	// TipRef is the full name of the lead's branch. It must start with
	// "refs/leads/".
	TipRef string
	// Rules is the execution-config set. The zero Set matches nothing.
	Rules execconfig.Set
	// Limits bounds the call. Zero fields take their defaults.
	Limits Limits
}

// Commit is one commit of base..tip. Every text field is raw lead text.
type Commit struct {
	// ID is the full commit id.
	ID string
	// Parents are the full ids of the parents, in order.
	Parents []string
	// AuthorName and the fields like it are the bytes git stores, not
	// changed by a mailmap. The email has no angle brackets.
	AuthorName, AuthorEmail       string
	CommitterName, CommitterEmail string
	// AuthorTime and CommitTime are what the commit says, in UTC. They are
	// the lead's claim, not a fact.
	AuthorTime, CommitTime time.Time
	// Subject is the first paragraph of the message with its lines joined
	// by spaces, as git prints "%s". It is raw lead text.
	Subject string
}

// FlagKind says why a path needs its own sign-off.
type FlagKind string

// The kinds of flag. See the package comment.
const (
	FlagExecConfig FlagKind = "exec-config"
	FlagSymlink    FlagKind = "symlink"
	FlagSubmodule  FlagKind = "submodule"
	FlagAliased    FlagKind = "aliased"
	FlagHistory    FlagKind = "history-only"
)

// Flag marks one path.
type Flag struct {
	Kind FlagKind
	// Path is the path as git stores it: raw lead text.
	Path string
	// Pattern is the rule that matched, when one did. It is the rule's own
	// text, not the lead's.
	Pattern string
	// Reason is the package's text for the owner. Lead text in it is
	// escaped with leadtext.Escape.
	Reason string
}

// Change is one path of the net change of the tip's tree against the base's
// tree. There is no rename detection: a moved file is a deletion and an
// addition.
type Change struct {
	// Path is the path as git stores it: raw lead text.
	Path string
	// Status is 'A' (added), 'M' (modified), 'D' (deleted) or 'T' (the type
	// of the entry changed, for example from a file to a symlink).
	Status byte
	// OldMode and NewMode are the git file modes as numbers, so a symlink
	// is 0o120000, a submodule 0o160000 and a regular file 0o100644. A mode
	// is 0 where there is no entry.
	OldMode, NewMode uint32
	// OldID and NewID are the full object ids of the entries. They are all
	// zeros where there is no entry.
	OldID, NewID string
	// Flags are the flags of this path, one for each kind, in the order
	// exec-config, symlink, submodule, aliased.
	Flags []Flag
}

// Review is the result of [Inspect]. Its text fields are raw lead text; see
// the package comment.
type Review struct {
	// BaseRef and TipRef are the refs as the request gave them.
	BaseRef, TipRef string
	// Base and Tip are the full commit ids the refs pointed at when the
	// review was made.
	Base, Tip string
	// Commits are the commits of base..tip, newest first (git's default
	// order, by commit date).
	Commits []Commit
	// Changes are the net change, sorted by path bytes.
	Changes []Change
	// Flagged holds every flag that needs its own sign-off, sorted by path
	// and then by kind, one for each kind and path: the flags of the
	// changes and the history-only flags.
	Flagged []Flag
}

// NotFastForwardError is returned when the base is not an ancestor of the
// tip: the branches diverged, or the tip is behind the base.
type NotFastForwardError struct {
	// Base and Tip are the full commit ids.
	Base, Tip string
	// MergeBase is the best common ancestor, or "" when there is none.
	MergeBase string
}

// Error describes the problem. It holds object ids only.
func (e *NotFastForwardError) Error() string {
	mb := e.MergeBase
	if mb == "" {
		mb = "none"
	}
	return fmt.Sprintf("intake: base %s is not an ancestor of tip %s (merge base %s)", e.Base, e.Tip, mb)
}

// Inspect reviews the tip of req.TipRef against req.BaseRef. See the package
// comment. It runs only read-only git plumbing in src.GitDir.
func Inspect(ctx context.Context, src Source, req Request) (*Review, error) {
	lim, err := req.Limits.resolved()
	if err != nil {
		return nil, err
	}
	if err := checkRefName(req.BaseRef, "refs/heads/"); err != nil {
		return nil, err
	}
	if err := checkRefName(req.TipRef, "refs/leads/"); err != nil {
		return nil, err
	}
	r := newRunner(src, lim)
	base, tip, err := r.resolve(ctx, req.BaseRef, req.TipRef)
	if err != nil {
		return nil, err
	}
	rev := &Review{BaseRef: req.BaseRef, TipRef: req.TipRef, Base: base, Tip: tip}
	if base == tip {
		return rev, nil
	}
	if err := r.requireAncestor(ctx, base, tip); err != nil {
		return nil, err
	}
	if rev.Commits, err = r.commits(ctx, base, tip, lim); err != nil {
		return nil, err
	}
	if rev.Changes, err = r.changes(ctx, base, tip, lim); err != nil {
		return nil, err
	}
	for i := range rev.Changes {
		rev.Changes[i].Flags = entryFlags(req.Rules, rev.Changes[i])
	}
	if err := r.aliasFlags(ctx, req.Rules, tip, rev.Changes, lim); err != nil {
		return nil, err
	}
	rev.Flagged = collect(rev.Changes, nil)
	return rev, nil
}
