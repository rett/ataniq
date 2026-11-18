//go:build linux && ataniq_canary

// Command ataniq-canary provisions the folders of the one canary lead and
// switches the stand-in agent to replay mode. It is the owner's tool, run by
// hand before the daemon starts the lead and between two epochs; the daemon
// never runs it. It exists only in a build with the ataniq_canary tag.
//
//	ataniq-canary provision --config CONFIG
//	ataniq-canary replay-on --config CONFIG
//	ataniq-canary show --config CONFIG
//	ataniq-canary check-inputs --config CONFIG --inputs FILE
//	ataniq-canary --version
//
// provision makes the folders of the layout, clones the throwaway repository
// from the mirror into the tree, copies the managed settings, the guard hook
// and the replay screens, and writes the marker last. It refuses the default
// roots, a root that carries the marker and a lead that has started. replay-on
// writes the replay switch file and nothing else; it is the only subcommand
// allowed after a first start. show and check-inputs change nothing: show
// prints the values of the config that the attended run script needs, as
// key=value lines, and check-inputs checks the sandbox inputs file against the
// config (show.go). All four read the config with leadcanary.ReadConfig. The
// work is in internal/leadcanary/provision; this file only parses the command
// line and prints.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision"
)

// version is what --version prints: the name and the build tag.
const version = "ataniq-canary (build tag ataniq_canary)"

// Exit statuses.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

const usage = `usage: ataniq-canary provision --config CONFIG
       ataniq-canary replay-on --config CONFIG
       ataniq-canary show --config CONFIG
       ataniq-canary check-inputs --config CONFIG --inputs FILE
       ataniq-canary --version`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}

// run is the command: it returns the exit status. getenv is the environment the
// default-root check reads.
func run(ctx context.Context, args []string, out, errOut io.Writer, getenv func(string) string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(out, version)
		return exitOK
	}
	if len(args) == 0 {
		fmt.Fprintln(errOut, usage)
		return exitUsage
	}
	sub := args[0]
	switch sub {
	case "provision", "replay-on", "show", "check-inputs":
	default:
		fmt.Fprintf(errOut, "ataniq-canary: unknown subcommand %q\n%s\n", sub, usage)
		return exitUsage
	}
	fl := flag.NewFlagSet(sub, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	config := fl.String("config", "", "the canary config file")
	inputs := new(string)
	need := "--config CONFIG is"
	if sub == "check-inputs" {
		inputs = fl.String("inputs", "", "the sandbox inputs file")
		need = "--config CONFIG and --inputs FILE are"
	}
	if err := fl.Parse(args[1:]); err != nil || fl.NArg() != 0 || *config == "" || (sub == "check-inputs" && *inputs == "") {
		fmt.Fprintf(errOut, "ataniq-canary %s: %s required and nothing else is accepted\n%s\n", sub, need, usage)
		return exitUsage
	}
	cfg, err := leadcanary.ReadConfig(*config)
	if err != nil {
		fmt.Fprintf(errOut, "ataniq-canary %s: %v\n", sub, err)
		return exitFailed
	}
	opts := provision.Options{
		Getenv: getenv,
		Log:    func(line string) { fmt.Fprintln(out, line) },
	}
	switch sub {
	case "provision":
		err = provision.Provision(ctx, cfg, opts)
	case "show":
		err = show(cfg, out)
	case "check-inputs":
		err = checkInputs(cfg, *inputs, out)
	default:
		err = provision.ReplayOn(cfg, opts)
	}
	if err != nil {
		fmt.Fprintf(errOut, "ataniq-canary %s: %v\n", sub, err)
		return exitFailed
	}
	return exitOK
}
