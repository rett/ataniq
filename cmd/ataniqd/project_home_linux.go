package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

func (a *app) projectHomeCmd(args []string) int {
	fs := flag.NewFlagSet("ataniqd project-home", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() { fmt.Fprintln(a.stderr, "usage:", projectHomeUsage) }
	state := fs.String("state-dir", "", "initialized daemon state directory")
	home := fs.String("home", "", "self or the home instance's authority ID")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return leaddaemon.ExitOK
		}
		return leaddaemon.ExitUsage
	}
	project := fs.Arg(0)
	if *state == "" || *home == "" || fs.NArg() != 1 || !leadrole.ValidProject(project) {
		fs.Usage()
		return leaddaemon.ExitUsage
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(a.stderr, "ataniqd project-home: refusing to enroll as root")
		return leaddaemon.ExitConfig
	}
	authority, err := leadregistry.EnrollProjectHome(*state, *home, project)
	if err != nil {
		fmt.Fprintln(a.stderr, "ataniqd project-home:", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitState
	}
	if _, err := fmt.Fprintf(a.stdout, "project=%s home_authority=%s\n", project, authority); err != nil {
		fmt.Fprintln(a.stderr, "ataniqd project-home:", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitFailure
	}
	return leaddaemon.ExitOK
}
