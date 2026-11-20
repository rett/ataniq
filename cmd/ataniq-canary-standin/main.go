//go:build linux && ataniq_canary

// Command ataniq-canary-standin is the stand-in agent of the canary slice. It
// runs on a terminal, as an agent would, and only wires the standin package to
// standard input and output. It is a static program, with no cgo, because it
// is bound into a sandbox that has little else. Its working directory is the
// working tree it commits in, and HOME says where replay mode finds its
// screens. HOME and GIT_CONFIG_GLOBAL are the only variables its git gets
// (inside the sandbox, the second names the identity file provisioning wrote).
// See the package for what it does.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"ataniq/internal/leadcanary/standin"
)

const version = "ataniq-canary-standin (build tag ataniq_canary)"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv("HOME"), os.Getenv("GIT_CONFIG_GLOBAL"), int(os.Stdin.Fd())))
}

// run is the command: it returns the exit status. home and gitConfigGlobal are
// the values of HOME and GIT_CONFIG_GLOBAL in its environment. termFD is the
// descriptor of the terminal that is put in raw mode, or negative for none.
func run(args []string, in io.Reader, out, errOut io.Writer, home, gitConfigGlobal string, termFD int) int {
	switch {
	case len(args) == 1 && args[0] == "--version":
		fmt.Fprintln(out, version)
		return 0
	case len(args) != 0:
		fmt.Fprintln(errOut, "usage: ataniq-canary-standin [--version]")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	err := standin.Run(ctx, standin.Config{Home: home, GitConfigGlobal: gitConfigGlobal, In: in, Out: out, TermFD: termFD})
	if err != nil {
		fmt.Fprintln(errOut, "ataniq-canary-standin:", err)
		return 1
	}
	return 0
}
