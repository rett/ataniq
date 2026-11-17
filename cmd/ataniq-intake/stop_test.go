package main

import (
	"bytes"
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/exchange/boundedexec"
)

// TestStopContextEndsOnEveryStopSignal: the command's context ends on each
// signal of boundedexec.StopSignals, not on interrupt alone. A terminate or a
// hang-up that killed the process by default would leave the sandboxed
// child and the scratch folders behind.
func TestStopContextEndsOnEveryStopSignal(t *testing.T) {
	sigs := boundedexec.StopSignals()
	if len(sigs) == 0 {
		t.Fatal("no stop signals")
	}
	for _, sig := range sigs {
		s, ok := sig.(syscall.Signal)
		if !ok {
			t.Fatalf("signal %v is not a syscall.Signal", sig)
		}
		if signal.Ignored(sig) {
			// The test itself runs under nohup or in a background job.
			continue
		}
		// A backstop, so that a missing handler fails this test by name
		// and does not kill the test binary.
		backstop := make(chan os.Signal, 1)
		signal.Notify(backstop, sig)
		ctx, stop := stopContext()
		if err := syscall.Kill(os.Getpid(), s); err != nil {
			stop()
			signal.Stop(backstop)
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
			stop()
			signal.Stop(backstop)
			t.Fatalf("the context did not end on %v", sig)
		}
		stop()
		signal.Stop(backstop)
	}
}

// TestStopContextKeepsIgnoredSignals: a signal that is ignored does not end
// the context, as it would not end the process. Hang-up is ignored here, as
// under nohup, and the other stop signals still end the context.
func TestStopContextKeepsIgnoredSignals(t *testing.T) {
	ignoreHangUp(t)
	ctx, stop := stopContext()
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		t.Fatal("the context ended on a hang-up that was ignored")
	case <-time.After(300 * time.Millisecond):
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the context did not end on terminate")
	}
}

// TestStopContextAllIgnored: with every stop signal ignored the context is a
// plain cancellable one. It does not end on a signal, and it ends on stop.
func TestStopContextAllIgnored(t *testing.T) {
	for _, s := range boundedexec.StopSignals() {
		signal.Ignore(s)
	}
	t.Cleanup(func() {
		for _, s := range boundedexec.StopSignals() {
			signal.Reset(s)
		}
	})
	ctx, stop := stopContext()
	for _, s := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
		if err := syscall.Kill(os.Getpid(), s); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-ctx.Done():
		t.Fatal("the context ended on an ignored signal")
	case <-time.After(300 * time.Millisecond):
	}
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the context did not end on stop")
	}
}

// ignoreHangUp ignores hang-up for the test and restores the default after.
func ignoreHangUp(t *testing.T) {
	t.Helper()
	signal.Ignore(syscall.SIGHUP)
	t.Cleanup(func() { signal.Reset(syscall.SIGHUP) })
}

// blockingRunner is a verify.Runner that runs until its context ends.
type blockingRunner struct{ started chan struct{} }

func (b *blockingRunner) Run(ctx context.Context, c boundedexec.Cmd) (boundedexec.Result, error) {
	close(b.started)
	<-ctx.Done()
	return boundedexec.Result{Exit: -1}, ctx.Err()
}

// TestCancelledContextStopsVerifyAndLeavesNoFolder: when the context ends
// while a gate runs, verify returns and the scratch and home folders are
// gone from the work folder.
func TestCancelledContextStopsVerifyAndLeavesNoFolder(t *testing.T) {
	w := newWorld(t)
	br := &blockingRunner{started: make(chan struct{})}
	w.runner = br
	w.initMirror()
	w.lead(testRole)
	w.fetch(testRole)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errb bytes.Buffer
	d := deps{Git: w.git, LoadPlanner: w.loader, Runner: w.runner, Now: func() time.Time { return w.now }}
	args := append([]string{"verify", "--state", w.state, "--project", testProject}, w.verifyArgs(testRole, "main", "sleep 1000")...)
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, failOnRead{t}, &out, &errb, d) }()

	select {
	case <-br.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the gate did not start")
	}
	if entries, _ := os.ReadDir(w.work); len(entries) == 0 {
		t.Fatal("no scratch folder while the gate runs: the test shows nothing")
	}
	cancel()
	var code int
	select {
	case code = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("verify did not return after the context ended")
	}
	if code == exitOK {
		t.Fatalf("exit %d after a cancel\n%s\n%s", code, out.String(), errb.String())
	}
	if entries, _ := os.ReadDir(w.work); len(entries) != 0 {
		t.Fatalf("the work folder is not empty after the cancel: %v", entries)
	}
	if strings.Contains(out.String(), "Outcome: pass") {
		t.Fatalf("a cancelled run passed:\n%s", out.String())
	}
}
