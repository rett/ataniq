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

// Limits bounds the work of a [Mirror]. A zero field means the default. A
// negative field is an error. The limits of one [Mirror.Fetch] are another
// type, [FetchLimits], so that the two meanings of a time limit cannot be
// mixed up: the result of [Mirror.Limits] is not a fetch's limits.
type Limits struct {
	// Timeout is the time limit of each git run (default [DefaultTimeout]).
	Timeout time.Duration
	// MaxRefs is the most refs a listing returns (default [DefaultMaxRefs]).
	MaxRefs int
}

// FetchLimits bounds one [Mirror.Fetch]. A zero field means the default of
// [DefaultFetchLimits]. A negative field is an error. It is not the [Limits]
// of a [Mirror]: Timeout here is the time limit of the whole fetch, and
// MaxRefs is the most branches that one lead may have.
type FetchLimits struct {
	// Timeout is the time limit of the whole fetch.
	Timeout time.Duration
	// MaxSource is the most that the clone's object store may measure in
	// the sandbox, in bytes.
	MaxSource int64
	// MaxStaged is the most that one fetch may add, in allocated bytes of
	// the staging repository, which starts with a few small files.
	MaxStaged int64
	// MaxMirror is the most that the mirror may measure after a fetch, in
	// allocated bytes.
	MaxMirror int64
	// MaxEntries is the most files and folders that any measure counts.
	MaxEntries int64
	// MaxRefs is the most branches that one lead may have. A request of
	// more than 20 000 is refused, whatever the lead has: the move names
	// every new tip as an argument of one git run, and the system limit on
	// the size of the arguments and the environment of one command (about
	// 2 MiB on Linux) must not decide the outcome of a fetch.
	MaxRefs int
}

// maxFetchMaxRefs is the largest FetchLimits.MaxRefs that is accepted. An
// argument is at most 65 bytes (a SHA-256 id and its NUL) and 8 for its
// pointer, so 20 000 tips are about 1.5 MB.
const maxFetchMaxRefs = 20000

// Defaults of a fetch. See [DefaultFetchLimits].
const (
	defaultFetchTimeout = 10 * time.Minute
	defaultMaxSource    = 2 << 30
	defaultMaxStaged    = 1 << 30
	defaultMaxMirror    = 8 << 30
	defaultMaxEntries   = 2_000_000
	defaultFetchMaxRefs = 2000
)

// DefaultLimits returns the limits of a [Mirror] that has none of its own:
// [DefaultTimeout] for each git run and [DefaultMaxRefs] for a listing.
func DefaultLimits() Limits {
	return Limits{Timeout: DefaultTimeout, MaxRefs: DefaultMaxRefs}
}

// DefaultFetchLimits returns the limits of a fetch when a [FetchRequest] leaves
// them out: 10 minutes for the whole fetch, 2 GiB for the lead's object store,
// 1 GiB for what one fetch adds, 8 GiB for the mirror, 2 000 000 entries in any
// measure and 2 000 branches for one lead.
func DefaultFetchLimits() FetchLimits {
	return FetchLimits{
		Timeout:    defaultFetchTimeout,
		MaxSource:  defaultMaxSource,
		MaxStaged:  defaultMaxStaged,
		MaxMirror:  defaultMaxMirror,
		MaxEntries: defaultMaxEntries,
		MaxRefs:    defaultFetchMaxRefs,
	}
}

// withDefaults returns the limits with each zero field set from
// [DefaultFetchLimits].
func (l FetchLimits) withDefaults() FetchLimits {
	d := DefaultFetchLimits()
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

func (l FetchLimits) check() error {
	if l.Timeout < 0 || l.MaxRefs < 0 || l.MaxSource < 0 || l.MaxStaged < 0 ||
		l.MaxMirror < 0 || l.MaxEntries < 0 {
		return errors.New("mirror: limits must not be negative")
	}
	if l.MaxRefs > maxFetchMaxRefs {
		return fmt.Errorf("mirror: the limit of %d branches for one lead is more than a fetch accepts, %d", l.MaxRefs, maxFetchMaxRefs)
	}
	return nil
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
	if l.Timeout < 0 || l.MaxRefs < 0 {
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
	return runGitWatch(ctx, g, c, stdin, timeout, maxStdout, nil, 0)
}

// runGitWatch is [runGit] with a watchdog that boundedexec calls every
// interval while git runs (zero means its default). An error from the
// watchdog stops git and comes back wrapped, so errors.As finds it.
func runGitWatch(ctx context.Context, g gitenv.Git, c gitenv.Cmd, stdin io.Reader, timeout time.Duration, maxStdout int64, watch func() error, interval time.Duration) ([]byte, error) {
	argv, err := g.Argv(c)
	if err != nil {
		return nil, fmt.Errorf("mirror: %w", err)
	}
	res, err := boundedexec.Run(ctx, boundedexec.Cmd{
		Argv:          argv,
		Env:           g.Env(c),
		Dir:           c.Dir,
		Stdin:         stdin,
		Timeout:       timeout,
		MaxStdout:     maxStdout,
		MaxStderr:     maxStderr,
		Watch:         watch,
		WatchInterval: interval,
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

// runLimit returns the time limit for one git run of the mirror: the limit of
// a git run, or the time that is left in ctx when that is shorter.
func (m *Mirror) runLimit(ctx context.Context) (time.Duration, error) {
	d := m.limits.timeout()
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if left <= 0 {
			return 0, context.DeadlineExceeded
		}
		d = min(d, left)
	}
	return d, nil
}

// run runs one git command with the limit that [Mirror.runLimit] gives.
func (m *Mirror) run(ctx context.Context, c gitenv.Cmd, stdin io.Reader, maxStdout int64) ([]byte, error) {
	d, err := m.runLimit(ctx)
	if err != nil {
		return nil, err
	}
	return runGit(ctx, m.git, c, stdin, d, maxStdout)
}
