package boundedexec

import (
	"context"
	"errors"
	"fmt"
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
		Stdin:       c.Stdin,
		Stdout:      out,
		Stderr:      errw,
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
		WaitDelay:   pipeWait,
	}
	if err := cmd.Start(); err != nil {
		return Result{Exit: -1}, fmt.Errorf("boundedexec: start: %w", err)
	}
	pid := cmd.Process.Pid

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
	// The leader has ended. Whatever else is in its group goes too, so no
	// background process outlives the run.
	killGroup()
	werr := cmd.Wait()
	close(done)

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
	case werr == nil:
		return res, nil
	case errors.As(werr, &xe):
		return res, &ExitError{Exit: res.Exit, Stderr: append([]byte(nil), res.Stderr...)}
	case errors.Is(werr, exec.ErrWaitDelay):
		return res, ErrPipeHeld
	default:
		return res, fmt.Errorf("boundedexec: wait: %w", werr)
	}
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
			st.stop(&WatchError{Err: err})
			return
		}
	}
}
