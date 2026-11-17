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

// Limits bounds the work of one [Mirror]. A zero field means the default.
type Limits struct {
	// Timeout is the time limit of each git run. Default [DefaultTimeout].
	Timeout time.Duration
	// MaxRefs is the most refs a listing returns. A listing with more is an
	// error. Default [DefaultMaxRefs].
	MaxRefs int
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
