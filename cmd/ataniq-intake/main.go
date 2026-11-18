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
//
// An interrupt or a hang-up that was ignored when the command started (under
// nohup, for example) stays ignored: the Go runtime keeps an inherited ignore
// for those two signals only, signal.Ignored reports it, and the signal is
// left out here. It neither kills the command nor cancels the run: the owner
// chose to ignore it. A terminate that was ignored at start is different. The
// runtime installs its own handler for it whatever the command inherited,
// signal.Ignored does not report it, and it cancels the run like any other
// terminate.
func stopContext() (context.Context, context.CancelFunc) {
	sigs := notIgnored(boundedexec.StopSignals(), signal.Ignored)
	if len(sigs) == 0 {
		// NotifyContext with no signals would catch every signal.
		return context.WithCancel(context.Background())
	}
	return signal.NotifyContext(context.Background(), sigs...)
}

// notIgnored returns the signals of all for which ignored is false.
func notIgnored(all []os.Signal, ignored func(os.Signal) bool) []os.Signal {
	var out []os.Signal
	for _, s := range all {
		if !ignored(s) {
			out = append(out, s)
		}
	}
	return out
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
