package intake

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
	// maxStderr is how much standard error of git is read and dropped.
	maxStderr = 64 << 10
	// smallOutput is the output cap of a git run that prints a few lines.
	smallOutput = 16 << 10
	// maxPathBytes is the longest path accepted. It is the usual PATH_MAX of
	// Linux; a longer path cannot be checked out there.
	maxPathBytes = 4096
	// maxFieldBytes is the longest NUL-terminated field of a listing: a path
	// with the mode and the ids before it.
	maxFieldBytes = maxPathBytes + 256
	// bytesPerRecord is the output a listing may have for each record: a
	// record has about 150 bytes of mode, ids and status, and a path. A
	// listing of paths that are 900 bytes long on average is refused.
	bytesPerRecord = 1024
	// maxCommitField is the longest field of a commit: a header with its
	// continuation lines, or the subject.
	maxCommitField = 32 << 10
)

// gitExit is the error of a git run that ended with a non-zero status. It
// carries the status and nothing git wrote, which may name lead data.
type gitExit struct {
	sub  string
	exit int
}

func (e *gitExit) Error() string {
	return fmt.Sprintf("intake: git %s failed with status %d", e.sub, e.exit)
}

// errTimeout is returned when the time of the call is used up.
func errTimeout(cause error) error {
	return fmt.Errorf("%w: %w", ErrLimit, cause)
}

// runner runs git for one call. All runs share one deadline.
type runner struct {
	src      Source
	deadline time.Time
}

func newRunner(src Source, lim Limits) *runner {
	return &runner{src: src, deadline: time.Now().Add(lim.Timeout)}
}

// git runs one git subcommand with args (the subcommand first). The output
// is returned unless sink is set, which then receives it as it arrives.
// maxOut caps the bytes of standard output. On an error it returns no output.
func (r *runner) git(ctx context.Context, args []string, stdin io.Reader, maxOut int64, sink io.Writer) ([]byte, error) {
	out, err := r.gitOut(ctx, args, stdin, maxOut, sink)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// gitOut is [runner.git] that also returns the standard output that was read
// before an error, which the caller may look at but must not show: git wrote
// it, and it may name lead data.
func (r *runner) gitOut(ctx context.Context, args []string, stdin io.Reader, maxOut int64, sink io.Writer) ([]byte, error) {
	c := gitenv.Cmd{GitDir: r.src.GitDir, Args: args}
	argv, err := r.src.Git.Argv(c)
	if err != nil {
		return nil, fmt.Errorf("intake: %w", err)
	}
	left := time.Until(r.deadline)
	if left <= 0 {
		return nil, errTimeout(boundedexec.ErrTimeout)
	}
	if maxOut <= 0 {
		maxOut = smallOutput
	}
	res, err := boundedexec.Run(ctx, boundedexec.Cmd{
		Argv:      argv,
		Env:       r.src.Git.Env(c),
		Stdin:     stdin,
		Stdout:    sink,
		Timeout:   left,
		MaxStdout: maxOut,
		MaxStderr: maxStderr,
	})
	if err != nil {
		var ee *boundedexec.ExitError
		switch {
		case errors.As(err, &ee):
			return res.Stdout, &gitExit{sub: args[0], exit: ee.Exit}
		case errors.Is(err, ErrLimit):
			return res.Stdout, err
		case errors.Is(err, boundedexec.ErrOutputLimit):
			return res.Stdout, fmt.Errorf("%w: the output of git %s is too large", ErrLimit, args[0])
		case errors.Is(err, boundedexec.ErrTimeout):
			return res.Stdout, errTimeout(err)
		}
		return res.Stdout, fmt.Errorf("intake: git %s: %w", args[0], err)
	}
	return res.Stdout, nil
}

// capture is an io.Writer that keeps what it is given and refuses a stream
// with too many NUL-terminated fields or too long a field. The listings of
// git use NUL as the field end, so this bounds the number and the size of the
// records before the whole output is held.
type capture struct {
	buf              []byte
	nuls, maxNuls    int
	run, maxRun      int
	limitDescription string
}

func newCapture(maxNuls, maxRun int, what string) *capture {
	return &capture{maxNuls: maxNuls, maxRun: maxRun, limitDescription: what}
}

func (c *capture) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == 0 {
			c.nuls++
			c.run = 0
			if c.nuls > c.maxNuls {
				return 0, fmt.Errorf("%w: more than the limit of %s", ErrLimit, c.limitDescription)
			}
			continue
		}
		c.run++
		if c.run > c.maxRun {
			return 0, fmt.Errorf("%w: a path or a name is longer than the limit", ErrLimit)
		}
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

// capFor returns n bytes of output, but at least smallOutput, for a listing
// whose size is computed from a limit.
func capFor(n int64) int64 { return max(n, smallOutput) }
