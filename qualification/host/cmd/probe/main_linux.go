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
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	// Invalid arguments are untrusted too. Never echo them or their values.
	flags.SetOutput(io.Discard)
	root := flags.String("fixture-root", "", "absolute owned frozen fake fixture directory")
	hold := flags.Bool("private-ready-hold", false, "private finite two-frame fixture")
	o := qualification.ErrorObservation()
	exit := 2
	if err := flags.Parse(args); err == nil && flags.NArg() == 0 {
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
