//go:build linux && amd64

// Command qualify records explicit, bounded host qualification attempts.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"

	"ataniq/qualification/host/internal/qualification"
)

func run(args []string, out io.Writer) int {
	if len(args) == 2 && args[0] == "private-final-descriptors" {
		if qualification.PrivateFinalDescriptors(args[1]) != nil {
			return 4
		}
		return 0
	}
	if len(args) == 2 && args[0] == "private-inject" {
		if qualification.PrivateInject(args[1]) != nil {
			return 4
		}
		return 0
	}
	if len(args) == 0 {
		return 2
	}
	f := flag.NewFlagSet("qualify", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	allow := f.Bool("allow-host-operations", false, "explicit host service opt-in")
	config := f.String("config", "", "exact configuration file")
	campaign := f.String("campaign", "", "owned stable campaign root")
	id := f.String("attempt", "", "exact persisted attempt")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 {
		return 2
	}
	switch args[0] {
	case "inspect":
		if *allow || *config != "" || *campaign == "" {
			return 2
		}
		if qualification.InspectHost(*campaign, *id, out) != nil {
			return 4
		}
		return 0
	case "run":
		if !*allow || *config == "" || *campaign != "" || *id != "" {
			return 2
		}
		c, err := qualification.ReadHostConfig(*config)
		if err != nil {
			return 2
		}
		r := qualification.RunHost(c)
		if json.NewEncoder(out).Encode(r) != nil {
			return 1
		}
		if r.Outcome == "observed" {
			return 0
		}
		if r.Outcome == "unsupported" {
			return 3
		}
		return 4
	case "reconcile":
		if !*allow || *campaign == "" || *id == "" || *config != "" {
			return 2
		}
		r := qualification.ReconcileHost(*campaign, *id)
		if json.NewEncoder(out).Encode(r) != nil {
			return 1
		}
		return 4
	default:
		return 2
	}
}

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }
