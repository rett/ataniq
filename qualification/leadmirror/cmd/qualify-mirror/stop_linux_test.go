package main

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/exchange/boundedexec"
)

// TestStopContextEndsOnEveryStopSignal: the context ends on each signal of
// boundedexec.StopSignals, not on interrupt alone. A terminate or a hang-up
// that killed the process by default would leave the sandboxed child and the
// scratch folders behind.
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
// under nohup, and terminate still ends the context.
func TestStopContextKeepsIgnoredSignals(t *testing.T) {
	signal.Ignore(syscall.SIGHUP)
	t.Cleanup(func() { signal.Reset(syscall.SIGHUP) })
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
