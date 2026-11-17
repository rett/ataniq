//go:build linux

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// defaultMaxLife bounds how long a child lives when nothing kills it, so that
// a unit leaked by a crashed harness ends by itself.
const defaultMaxLife = 10 * time.Minute

// childOptions are the arguments of "leadunit-qualify child".
type childOptions struct {
	ignoreTerm bool
	readyFile  string
	maxLife    time.Duration
}

func parseChild(args []string) (childOptions, error) {
	o := childOptions{maxLife: defaultMaxLife}
	fs := flag.NewFlagSet("leadunit-qualify child", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.ignoreTerm, "ignore-term", false, "ignore SIGTERM")
	fs.StringVar(&o.readyFile, "ready-file", "", "file to create, with the pid, once SIGTERM is ignored")
	fs.DurationVar(&o.maxLife, "max-life", defaultMaxLife, "exit by itself after this long")
	if err := fs.Parse(args); err != nil {
		return childOptions{}, err
	}
	if fs.NArg() != 0 {
		return childOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if o.maxLife <= 0 || o.maxLife > time.Hour {
		return childOptions{}, errors.New("--max-life must be above 0 and at most 1h")
	}
	return o, nil
}

// runChild is the helper that the qualification unit runs. It optionally
// ignores SIGTERM, writes its pid to the ready file once that is in effect
// (so the harness never sends SIGTERM too early), and then waits for SIGKILL
// or for its maximum life. Without --ignore-term it dies of SIGTERM like any
// process. It writes nothing to standard output.
func runChild(args []string, stderr io.Writer) int {
	o, err := parseChild(args)
	if err != nil {
		fmt.Fprintf(stderr, "leadunit-qualify child: %v\n", err)
		return exitUsage
	}
	if o.ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	if o.readyFile != "" {
		f, err := os.OpenFile(o.readyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			fmt.Fprintf(stderr, "leadunit-qualify child: ready file: %v\n", err)
			return exitRuntime
		}
		_, werr := f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			fmt.Fprintf(stderr, "leadunit-qualify child: ready file: %v\n", werr)
			return exitRuntime
		}
	}
	time.Sleep(o.maxLife)
	return exitOK
}
