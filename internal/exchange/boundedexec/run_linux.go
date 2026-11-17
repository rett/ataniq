package boundedexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func run(ctx context.Context, c Cmd) (Result, error) {
	st := newStopper()
	out := &stdoutWriter{max: c.MaxStdout, sink: c.Stdout, st: st}
	errw := &stderrWriter{max: c.MaxStderr}

	env := c.Env
	if env == nil {
		// A nil Env would make os/exec inherit the caller's environment.
		env = []string{}
	}
	dir := c.Dir
	if dir == "" {
		dir = "/"
	}
	// The command is built by hand: exec.Command would search PATH, and
	// exec.CommandContext would kill only the leader, not the group.
	cmd := &exec.Cmd{
		Path:        c.Argv[0],
		Args:        c.Argv,
		Env:         env,
		Dir:         dir,
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}

	// os/exec is given only *os.File ends, so it starts no copy goroutine of
	// its own. Its Wait would wait for such a goroutine without a bound, and
	// a goroutine that is stuck in the caller's Read or Write never ends. The
	// copies run here, and Run stops waiting for them after pipeWait.
	var (
		childEnds []*os.File // the command's ends; closed here once it started
		parentEnd []*os.File // our ends; closed when Run returns
	)
	closeAll := func(fs []*os.File) {
		for _, f := range fs {
			_ = f.Close()
		}
	}
	defer func() { closeAll(parentEnd) }()
	pipe := func() (r, w *os.File, err error) {
		r, w, err = os.Pipe()
		if err != nil {
			err = fmt.Errorf("boundedexec: pipe: %w", err)
		}
		return r, w, err
	}
	fail := func(err error) (Result, error) {
		closeAll(childEnds)
		return Result{Exit: -1}, err
	}

	var stdinFeed func()
	switch in := c.Stdin.(type) {
	case nil: // /dev/null, opened by os/exec
	case *os.File:
		cmd.Stdin = in
	default:
		pr, pw, err := pipe()
		if err != nil {
			return fail(err)
		}
		cmd.Stdin = pr
		childEnds = append(childEnds, pr)
		parentEnd = append(parentEnd, pw)
		// If Read blocks, this goroutine is left behind, blocked in the
		// caller's reader, until Read returns. Nothing waits for it. When
		// Run returns, pw is closed, so a Write into a pipe that nobody
		// reads any more ends too.
		stdinFeed = func() {
			_, _ = io.Copy(pw, in)
			_ = pw.Close()
		}
	}
	outR, outW, err := pipe()
	if err != nil {
		return fail(err)
	}
	childEnds = append(childEnds, outW)
	parentEnd = append(parentEnd, outR)
	errR, errW, err := pipe()
	if err != nil {
		return fail(err)
	}
	childEnds = append(childEnds, errW)
	parentEnd = append(parentEnd, errR)
	cmd.Stdout, cmd.Stderr = outW, errW

	err = cmd.Start()
	closeAll(childEnds) // the command holds its own copies now
	if err != nil {
		return Result{Exit: -1}, fmt.Errorf("boundedexec: start: %w", err)
	}
	pid := cmd.Process.Pid
	if stdinFeed != nil {
		go stdinFeed()
	}
	// The copy goroutines for standard output and standard error. The one
	// for standard output can block in Cmd.Stdout.Write, where Run cannot
	// reach it; see the wait for the pipes below.
	outDone, errDone := copyPipe(outR, out), copyPipe(errR, errw)

	// exited closes when the leader has ended but is not yet reaped. A
	// zombie keeps its process group id reserved, so signalling the group
	// until cmd.Wait has run cannot reach an unrelated process.
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		var info unix.Siginfo
		for {
			err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
			if !errors.Is(err, unix.EINTR) {
				return
			}
		}
	}()
	killGroup := func() { _ = unix.Kill(-pid, unix.SIGKILL) }

	done := make(chan struct{})
	if c.Watch != nil {
		interval := c.WatchInterval
		if interval == 0 {
			interval = DefaultWatchInterval
		}
		go watch(c.Watch, interval, st, done)
	}

	timer := time.NewTimer(c.Timeout)
	defer timer.Stop()
	select {
	case <-exited:
	case <-timer.C:
		st.stop(ErrTimeout)
	case <-ctx.Done():
		st.stop(ctx.Err())
	case <-st.notify:
	}
	// On a stop the group is killed first; then the leader is waited for.
	if st.reason() != nil {
		killGroup()
	}
	<-exited
	// The leader has ended. No watchdog call starts from here on, and one
	// that is already running cannot make itself the cause: draining the
	// pipes can take a while, and the command was not stopped by the
	// watchdog.
	close(done)
	st.sealWatch()
	// Whatever else is in the leader's group goes too, so no background
	// process outlives the run.
	killGroup()
	werr := cmd.Wait()

	// The leader is reaped. Wait for the copy goroutines for a fixed time,
	// not for ever: a process outside the group (setsid) can hold a pipe
	// open, and the writer behind Cmd.Stdout can block. A goroutine that
	// has not ended by then is left behind. If it is blocked in the pipe,
	// closing our end (the deferred close) ends it; if it is blocked in
	// Cmd.Stdout.Write, it stays blocked in the caller's writer until that
	// returns, and the Result holds what was copied before.
	expired := make(chan struct{})
	defer time.AfterFunc(pipeWait, func() { close(expired) }).Stop()
	held := false
	for _, d := range []<-chan struct{}{outDone, errDone} {
		select {
		case <-d:
			continue
		default:
		}
		select {
		case <-d:
		case <-expired:
			held = true
		}
	}

	res := Result{Stdout: out.bytes(), Exit: -1}
	res.Stderr, res.StderrCut = errw.result()
	if ps := cmd.ProcessState; ps != nil {
		res.Exit = ps.ExitCode()
	}
	if cause := st.reason(); cause != nil {
		return res, cause
	}
	var xe *exec.ExitError
	switch {
	case errors.As(werr, &xe):
		return res, &ExitError{Exit: res.Exit, Stderr: append([]byte(nil), res.Stderr...)}
	case werr != nil:
		return res, fmt.Errorf("boundedexec: wait: %w", werr)
	case held:
		return res, ErrPipeHeld
	}
	return res, nil
}

// copyPipe copies r to w in a goroutine of its own and closes r when the
// copy ends. The returned channel closes when the goroutine has ended.
func copyPipe(r *os.File, w io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(w, r)
		_ = r.Close()
	}()
	return done
}

// watch calls fn every interval until done closes or fn returns an error.
func watch(fn func() error, interval time.Duration, st *stopper, done <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		select {
		case <-done:
			return
		default:
		}
		if err := fn(); err != nil {
			st.stopWatch(err)
			return
		}
	}
}
