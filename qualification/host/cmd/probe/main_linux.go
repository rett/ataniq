//go:build linux && amd64

// Command probe emits one bounded observation for one bounded request on stdin.
package main

import (
	"flag"
	"io"
	"os"

	"ataniq/qualification/host/internal/qualification"
)

func run(args []string, in io.Reader, out io.Writer) int {
	return runWithActors(args, in, out, func(in io.Reader, out io.Writer) error {
		r, rok := in.(*os.File)
		w, wok := out.(*os.File)
		if !rok || !wok {
			return io.ErrClosedPipe
		}
		return qualification.R1Memory(r, w)
	}, qualification.R1PrivateChild)
}

func runWithActors(args []string, in io.Reader, out io.Writer, memory func(io.Reader, io.Writer) error, child func() int) int {
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	// Invalid arguments are untrusted too. Never echo them or their values.
	flags.SetOutput(io.Discard)
	root := flags.String("fixture-root", "", "absolute owned frozen fake fixture directory")
	hold := flags.Bool("private-ready-hold", false, "private finite two-frame fixture")
	r1 := flags.Bool("r1-memory", false, "fixed opt-in R1 memory actor")
	r1Child := flags.Bool("private-r1-child", false, "private fixed R1 child")
	o := qualification.ErrorObservation()
	exit := 2
	if err := flags.Parse(args); err == nil && flags.NArg() == 0 {
		if *r1 || *r1Child {
			count := 0
			flags.Visit(func(*flag.Flag) { count++ })
			if count != 1 || *r1 && *r1Child {
				return 2
			}
			if *r1Child {
				return child()
			}
			if memory(in, out) != nil {
				return 1
			}
			return 0
		}
		if r, err := qualification.DecodeRequest(in); err == nil {
			if *hold {
				if r.Case != qualification.CaseInventory {
					return 2
				}
				if qualification.ReadyHold(r, out) != nil {
					return 1
				}
				return 0
			}
			o = qualification.Probe(r, qualification.Config{FixtureRoot: *root})
			if o.Status != "error" {
				exit = 0
			}
		}
	}
	if err := qualification.EncodeObservation(out, o); err != nil {
		return 1
	}
	return exit
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout)) }
