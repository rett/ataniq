package main

import (
	"context"
	"os"
	"os/signal"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/intake/verify"
)

// stopContext returns a context that ends on every signal that
// boundedexec.StopSignals lists: interrupt, terminate and hang-up. A sandboxed
// child is in a process group of its own, so a signal to this command does
// not reach it. It is killed, and its scratch folders are removed, only when
// the run returns, and a run returns early only when its context ends.
func stopContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), boundedexec.StopSignals()...)
}

// main wires the real dependencies and exits. The work is in run.
func main() {
	ctx, stop := stopContext()
	d := deps{
		LoadPlanner: loadTemplate,
		Runner:      verify.ExecRunner{},
	}
	// A missing git is reported by the commands that need it, so that help
	// and usage errors still work.
	if g, err := gitenv.Find(); err == nil {
		d.Git = g
	}
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, d)
	stop()
	os.Exit(code)
}
