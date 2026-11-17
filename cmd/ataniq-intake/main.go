package main

import (
	"context"
	"os"
	"os/signal"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/intake/verify"
)

// main wires the real dependencies and exits. The work is in run.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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
