package boundedexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// DefaultWatchInterval is how often Watch is called when Cmd.WatchInterval is
// zero.
const DefaultWatchInterval = 250 * time.Millisecond

// pipeWait is how long Run waits for the output pipes after the command's
// group has been killed. It only matters when a process outside the group
// still holds a pipe.
const pipeWait = 2 * time.Second

// Cmd describes one command and its bounds.
type Cmd struct {
	// Argv is the argument vector. Argv[0] is an absolute path and is never
	// looked up in PATH. No element holds a NUL byte.
	Argv []string
	// Env is the whole environment, as NAME=value entries with no repeated
	// name. Nothing is inherited from the calling process; nil means empty.
	Env []string
	// Dir is the working folder. Empty means "/". Otherwise it is absolute.
	// The command's process enters it by path (chdir), which follows
	// symlinks in every component, and the package does not check any of
	// them. The caller must not pass a folder that a lead or a sandbox can
	// write, or whose parents it can write: it could swap a component for a
	// symlink and send the command elsewhere.
	Dir string
	// Stdin is the standard input. Nil means no input (/dev/null). An
	// *os.File is handed to the command as it is. Any other reader is copied
	// to the command in a goroutine of Run's own, and Run never waits for
	// that goroutine. If Read fails with an error other than io.EOF, the
	// command is stopped (it is not given EOF, so it cannot take truncated
	// input for the whole) and Run returns that error, wrapped. A write
	// error towards the command, because it closed its input, is not an
	// error. If Read blocks (an io.Pipe that nobody writes to or
	// closes), the goroutine is left behind, blocked in the caller's Read,
	// until Read returns; Run still returns in time.
	Stdin io.Reader
	// Stdout, when set, receives standard output as it arrives, and
	// Result.Stdout stays empty. MaxStdout still counts the bytes and stops
	// the command. If a write to Stdout fails, the command is stopped and
	// Run returns that error.
	//
	// Run copies from the command's pipe to Stdout in a goroutine of its own
	// and, after the command has ended, waits for it only for a fixed two
	// seconds. If Write blocks past that, Run gives up on the goroutine and
	// returns; the goroutine is left behind, blocked in the caller's Write,
	// until Write returns. Result then holds what was copied so far (the
	// sink has the bytes written to it; Result.Stdout is empty). When the
	// command had not been stopped, Run returns ErrPipeHeld. Because that
	// Write can still be running after Run has returned, the writer must be
	// safe to use then, and the caller must not treat it as idle.
	Stdout io.Writer
	// Timeout is how long the command may run. It is required and positive.
	// Run returns within Timeout plus the fixed two-second wait for the
	// pipes described at Stdout and ErrPipeHeld, whatever the command, its
	// descendants, Stdin and Stdout do.
	Timeout time.Duration
	// MaxStdout is how many bytes of standard output are accepted. It is
	// required and positive. Output of exactly this size is accepted; one
	// byte more stops the command with ErrOutputLimit.
	MaxStdout int64
	// MaxStderr is how many bytes of standard error are kept. It is required
	// and positive. More is discarded without error and Result.StderrCut is
	// set.
	MaxStderr int64
	// Watch is an optional watchdog. It is called every WatchInterval while
	// the command runs, from a goroutine of its own, never twice at once. A
	// non-nil error stops the command. Run does not wait for a call that is
	// still running when the command has ended, and starts no new call after
	// that. An error that such a call returns later is ignored: the command
	// had already ended, so the watchdog did not stop it.
	Watch func() error
	// WatchInterval is the time between Watch calls. Zero means
	// DefaultWatchInterval. It must not be negative.
	WatchInterval time.Duration
}

// Result is what a run produced. It is filled in even when Run returns an
// error. When Run gave up waiting for the pipes, it holds what was copied
// until then.
type Result struct {
	// Stdout is the standard output read, up to MaxStdout bytes. It is empty
	// when Cmd.Stdout was set.
	Stdout []byte
	// Stderr is the standard error read, up to MaxStderr bytes.
	Stderr []byte
	// StderrCut is true when more standard error came than MaxStderr and the
	// rest was dropped.
	StderrCut bool
	// Exit is the exit status. It is -1 when the command did not exit by
	// itself: a signal ended it, or it never started.
	Exit int
}

var (
	// ErrTimeout means the command ran past Cmd.Timeout and was killed.
	ErrTimeout = errors.New("boundedexec: time limit reached")
	// ErrOutputLimit means the command wrote more than Cmd.MaxStdout bytes of
	// standard output and was killed.
	ErrOutputLimit = errors.New("boundedexec: standard output limit reached")
	// ErrPipeHeld means the command ended by itself with status 0, but its
	// output was not fully read within the two-second wait for the pipes.
	// Either a process outside its group (one that called setsid) still held
	// an output pipe, or Cmd.Stdout did not return from Write. The output may
	// be incomplete. The Result then has Exit 0, so a zero exit alone does
	// not mean a clean run. A stop or a non-zero exit is reported by its own
	// error instead.
	ErrPipeHeld = errors.New("boundedexec: an output pipe was still held after the command ended")
	// ErrUnsupported is returned on systems other than Linux.
	ErrUnsupported = errors.New("boundedexec: unsupported on this system")
)

// ExitError is returned when the command ends with a non-zero status. Exit is
// -1 when a signal that the package did not send ended it.
type ExitError struct {
	Exit int
	// Stderr is the raw standard error, up to Cmd.MaxStderr bytes. It is
	// untrusted text: the command, and anything that wrote to it, chose every
	// byte, including newlines, terminal escapes and invalid UTF-8. Do not
	// print it or log it as it is.
	Stderr []byte
}

// Error describes the exit and the start of its standard error. The standard
// error is cut to its first 200 bytes (on a rune boundary) and shown quoted
// with strconv.QuoteToASCII, so the text has no newline, no control byte and
// nothing but printable ASCII from the command.
func (e *ExitError) Error() string {
	const show = 200
	raw := bytes.TrimSpace(e.Stderr)
	if len(raw) == 0 {
		return fmt.Sprintf("boundedexec: command exited with status %d", e.Exit)
	}
	cut := len(raw) > show
	if cut {
		n := show
		// Step back to the start of a rune, but no further than one rune.
		for i := 0; i < utf8.UTFMax-1 && n > 0 && !utf8.RuneStart(raw[n]); i++ {
			n--
		}
		raw = raw[:n]
	}
	msg := strconv.QuoteToASCII(string(raw))
	if cut {
		msg += "..."
	}
	return fmt.Sprintf("boundedexec: command exited with status %d: %s", e.Exit, msg)
}

// WatchError is returned when the watchdog stopped the command.
type WatchError struct {
	Err error
}

// Error describes the watchdog's error.
func (e *WatchError) Error() string {
	return "boundedexec: stopped by the watchdog: " + e.Err.Error()
}

// Unwrap returns the error the watchdog gave.
func (e *WatchError) Unwrap() error { return e.Err }

// Run runs the command and waits for it. See the package comment for the
// guarantees. The context bounds the run in addition to Cmd.Timeout.
func Run(ctx context.Context, c Cmd) (Result, error) {
	if err := c.validate(); err != nil {
		return Result{Exit: -1}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{Exit: -1}, err
	}
	return run(ctx, c)
}

func (c *Cmd) validate() error {
	if len(c.Argv) == 0 {
		return errors.New("boundedexec: empty argument vector")
	}
	if !filepath.IsAbs(c.Argv[0]) {
		return fmt.Errorf("boundedexec: Argv[0] %q is not an absolute path", c.Argv[0])
	}
	for i, a := range c.Argv {
		if strings.IndexByte(a, 0) >= 0 {
			return fmt.Errorf("boundedexec: Argv[%d] holds a NUL byte", i)
		}
	}
	seen := make(map[string]bool, len(c.Env))
	for i, e := range c.Env {
		if strings.IndexByte(e, 0) >= 0 {
			return fmt.Errorf("boundedexec: Env[%d] holds a NUL byte", i)
		}
		name, _, ok := strings.Cut(e, "=")
		if !ok || name == "" {
			return fmt.Errorf("boundedexec: Env[%d] is not NAME=value", i)
		}
		if seen[name] {
			return fmt.Errorf("boundedexec: Env names %q twice", name)
		}
		seen[name] = true
	}
	if strings.IndexByte(c.Dir, 0) >= 0 {
		return errors.New("boundedexec: Dir holds a NUL byte")
	}
	if c.Dir != "" && !filepath.IsAbs(c.Dir) {
		return fmt.Errorf("boundedexec: Dir %q is not an absolute path", c.Dir)
	}
	if c.Timeout <= 0 {
		return errors.New("boundedexec: Timeout must be positive")
	}
	if c.MaxStdout <= 0 {
		return errors.New("boundedexec: MaxStdout must be positive")
	}
	if c.MaxStderr <= 0 {
		return errors.New("boundedexec: MaxStderr must be positive")
	}
	if c.WatchInterval < 0 {
		return errors.New("boundedexec: WatchInterval must not be negative")
	}
	return nil
}

// stopper records the first reason a run was stopped and tells the run loop.
type stopper struct {
	mu     sync.Mutex
	cause  error
	sealed bool
	notify chan struct{}
}

func newStopper() *stopper {
	return &stopper{notify: make(chan struct{}, 1)}
}

// stop records cause unless an earlier one is set, and wakes the run loop.
func (s *stopper) stop(cause error) { s.record(cause, false) }

// stopWatch records a watchdog error as the cause, unless an earlier cause is
// set or the watchdog is sealed.
func (s *stopper) stopWatch(err error) { s.record(&WatchError{Err: err}, true) }

func (s *stopper) record(cause error, fromWatch bool) {
	s.mu.Lock()
	if fromWatch && s.sealed {
		s.mu.Unlock()
		return
	}
	if s.cause == nil {
		s.cause = cause
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// sealWatch makes later watchdog errors count for nothing. It is called when
// the leader has ended: the watchdog did not stop a command that had already
// stopped.
func (s *stopper) sealWatch() {
	s.mu.Lock()
	s.sealed = true
	s.mu.Unlock()
}

func (s *stopper) reason() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cause
}

// stdoutWriter accepts at most max bytes, keeps them or forwards them, and
// stops the run when more arrive or the sink fails.
type stdoutWriter struct {
	mu   sync.Mutex
	max  int64
	n    int64
	buf  []byte
	sink io.Writer
	st   *stopper
	dead bool
}

// Write is called by one goroutine only, the one that copies the pipe. It
// holds mu while it writes to the sink, which can block for ever, so nothing
// else may take mu when a sink is set; see bytes.
func (w *stdoutWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead {
		return 0, ErrOutputLimit
	}
	room := w.max - w.n
	take := p
	over := false
	if int64(len(take)) > room {
		take = take[:room]
		over = true
	}
	if len(take) > 0 {
		if w.sink != nil {
			if n, err := w.sink.Write(take); err != nil {
				w.n += int64(n)
				w.dead = true
				w.st.stop(fmt.Errorf("boundedexec: write standard output: %w", err))
				return n, err
			}
		} else {
			w.buf = append(w.buf, take...)
		}
		w.n += int64(len(take))
	}
	if over {
		w.dead = true
		w.st.stop(ErrOutputLimit)
		return len(take), ErrOutputLimit
	}
	return len(p), nil
}

// bytes returns what was kept. It never waits for a Write that is stuck in the
// sink: with a sink nothing is kept, and sink does not change.
func (w *stdoutWriter) bytes() []byte {
	if w.sink != nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf...)
}

// stderrWriter keeps the first max bytes and drops the rest.
type stderrWriter struct {
	mu  sync.Mutex
	max int64
	buf []byte
	cut bool
}

func (w *stderrWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	room := w.max - int64(len(w.buf))
	if int64(len(p)) > room {
		w.buf = append(w.buf, p[:room]...)
		w.cut = true
	} else {
		w.buf = append(w.buf, p...)
	}
	return len(p), nil
}

func (w *stderrWriter) result() ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf...), w.cut
}
