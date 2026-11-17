//go:build linux

package main

import (
	"encoding/json"
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
	argsFile   string
	literals   []string
	maxLife    time.Duration
}

// stringList is a repeatable string flag.
type stringList struct{ vals *[]string }

func (l stringList) String() string { return "" }

func (l stringList) Set(v string) error {
	*l.vals = append(*l.vals, v)
	return nil
}

func parseChild(args []string) (childOptions, error) {
	o := childOptions{maxLife: defaultMaxLife}
	fs := flag.NewFlagSet("leadunit-qualify child", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.ignoreTerm, "ignore-term", false, "ignore SIGTERM")
	fs.StringVar(&o.readyFile, "ready-file", "", "file to create, with the pid, once SIGTERM is ignored")
	fs.StringVar(&o.argsFile, "args-file", "", "file to create, with the arguments the child was started with as a JSON list, before the ready file")
	fs.Var(stringList{&o.literals}, "literal", "a value the harness checks reaches the child unchanged (repeatable; the child does nothing with it)")
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
// ignores SIGTERM, records the arguments it was started with in the args file
// (so the harness can compare them with what it sent), writes its pid to the
// ready file once SIGTERM is ignored and the arguments are recorded (so the
// harness never sends SIGTERM too early), and then waits for SIGKILL or for
// its maximum life. Without --ignore-term it dies of SIGTERM like any
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
	if o.argsFile != "" {
		b, err := json.Marshal(args)
		if err == nil {
			err = writeNewFile(o.argsFile, append(b, '\n'))
		}
		if err != nil {
			fmt.Fprintf(stderr, "leadunit-qualify child: args file: %v\n", err)
			return exitRuntime
		}
	}
	if o.readyFile != "" {
		if err := writeNewFile(o.readyFile, []byte(strconv.Itoa(os.Getpid())+"\n")); err != nil {
			fmt.Fprintf(stderr, "leadunit-qualify child: ready file: %v\n", err)
			return exitRuntime
		}
	}
	time.Sleep(o.maxLife)
	return exitOK
}

// writeNewFile creates path, which must not exist, with mode 0600 and writes
// data to it.
func writeNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
