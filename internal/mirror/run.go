package mirror

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
)

const (
	// DefaultTimeout is the time limit of one git run.
	DefaultTimeout = 60 * time.Second
	// DefaultMaxRefs is the most refs a listing may return.
	DefaultMaxRefs = 10000

	// maxStderr is how much standard error of git is kept. It is read and
	// dropped; error messages of this package never hold it.
	maxStderr = 64 << 10
	// smallOutput is the output cap of a git run that prints one line.
	smallOutput = 4 << 10
)

// Limits bounds the work of a [Mirror] and of one [Mirror.Fetch]. A zero
// field means the default. A negative field is an error.
//
// A Mirror reads two fields: Timeout, the time limit of each git run
// (default [DefaultTimeout]), and MaxRefs, the most refs a listing returns
// (default [DefaultMaxRefs]). A [FetchRequest] reads all fields, and there
// Timeout is the time limit of the whole fetch and MaxRefs is the most
// branches one lead may have; the defaults are those of [DefaultLimits]. The
// other fields mean nothing to a Mirror. Pass a Limits value for the one use
// that it is written for.
type Limits struct {
	// Timeout is, for a Mirror, the time limit of each git run, and, for a
	// fetch, the time limit of the whole fetch.
	Timeout time.Duration
	// MaxSource is the most that the clone's object store may measure in
	// the sandbox, in bytes. A fetch only.
	MaxSource int64
	// MaxStaged is the most that one fetch may add, in allocated bytes of
	// the staging repository, which starts with a few small files. A fetch
	// only.
	MaxStaged int64
	// MaxMirror is the most that the mirror may measure after a fetch, in
	// allocated bytes. A fetch only.
	MaxMirror int64
	// MaxEntries is the most files and folders that any measure counts. A
	// fetch only.
	MaxEntries int64
	// MaxRefs is, for a Mirror, the most refs a listing returns, and, for a
	// fetch, the most branches that one lead may have.
	MaxRefs int
}

// Defaults of a fetch. See [DefaultLimits].
const (
	defaultFetchTimeout = 10 * time.Minute
	defaultMaxSource    = 2 << 30
	defaultMaxStaged    = 1 << 30
	defaultMaxMirror    = 8 << 30
	defaultMaxEntries   = 2_000_000
	defaultFetchMaxRefs = 2000
)

// DefaultLimits returns the limits of a fetch when a [FetchRequest] leaves
// them out: 10 minutes for the whole fetch, 2 GiB for the lead's object store,
// 1 GiB for what one fetch adds, 8 GiB for the mirror, 2 000 000 entries in any
// measure and 2 000 branches for one lead.
func DefaultLimits() Limits {
	return Limits{
		Timeout:    defaultFetchTimeout,
		MaxSource:  defaultMaxSource,
		MaxStaged:  defaultMaxStaged,
		MaxMirror:  defaultMaxMirror,
		MaxEntries: defaultMaxEntries,
		MaxRefs:    defaultFetchMaxRefs,
	}
}

// forFetch returns the limits with each zero field set from [DefaultLimits].
func (l Limits) forFetch() Limits {
	d := DefaultLimits()
	if l.Timeout == 0 {
		l.Timeout = d.Timeout
	}
	if l.MaxSource == 0 {
		l.MaxSource = d.MaxSource
	}
	if l.MaxStaged == 0 {
		l.MaxStaged = d.MaxStaged
	}
	if l.MaxMirror == 0 {
		l.MaxMirror = d.MaxMirror
	}
	if l.MaxEntries == 0 {
		l.MaxEntries = d.MaxEntries
	}
	if l.MaxRefs == 0 {
		l.MaxRefs = d.MaxRefs
	}
	return l
}

func (l Limits) timeout() time.Duration {
	if l.Timeout == 0 {
		return DefaultTimeout
	}
	return l.Timeout
}

func (l Limits) maxRefs() int {
	if l.MaxRefs == 0 {
		return DefaultMaxRefs
	}
	return l.MaxRefs
}

func (l Limits) check() error {
	if l.Timeout < 0 || l.MaxRefs < 0 || l.MaxSource < 0 || l.MaxStaged < 0 ||
		l.MaxMirror < 0 || l.MaxEntries < 0 {
		return errors.New("mirror: limits must not be negative")
	}
	return nil
}

// Option changes how [Create] or [Open] makes a [Mirror].
type Option func(*options)

type options struct{ limits Limits }

// UseLimits gives the mirror the limits l, for the git runs of [Create] and
// [Open] themselves and for the mirror they return. Without it the defaults
// apply, among them the 60 second limit of each git run. A negative field
// makes Create and Open fail before they touch anything.
func UseLimits(l Limits) Option {
	return func(o *options) { o.limits = l }
}

// applyOptions returns the limits that the options set, or the error for
// limits that are not valid.
func applyOptions(opts []Option) (Limits, error) {
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if err := o.limits.check(); err != nil {
		return Limits{}, err
	}
	return o.limits, nil
}

// gitExit is the error of a git run that ended with a non-zero status. It
// carries the status and no output of git, which may name lead refs.
type gitExit struct {
	sub  string
	exit int
}

func (e *gitExit) Error() string {
	return fmt.Sprintf("mirror: git %s failed with status %d", e.sub, e.exit)
}

// runGit runs one git command through gitenv and boundedexec. Standard error
// is dropped from the error text.
func runGit(ctx context.Context, g gitenv.Git, c gitenv.Cmd, stdin io.Reader, timeout time.Duration, maxStdout int64) ([]byte, error) {
	argv, err := g.Argv(c)
	if err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	res, err := boundedexec.Run(ctx, boundedexec.Cmd{
		Argv:      argv,
		Env:       g.Env(c),
		Dir:       c.Dir,
		Stdin:     stdin,
		Timeout:   timeout,
		MaxStdout: maxStdout,
		MaxStderr: maxStderr,
	})
	if err != nil {
		var ee *boundedexec.ExitError
		if errors.As(err, &ee) {
			return nil, &gitExit{sub: c.Args[0], exit: ee.Exit}
		}
		return nil, fmt.Errorf("mirror: git %s: %w", c.Args[0], err)
	}
	return res.Stdout, nil
}
