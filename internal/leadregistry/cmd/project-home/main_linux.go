// Command project-home records a project's initial designation in a stopped
// daemon's registry. It uses the same exclusive writer lock as the daemon.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ataniq/internal/instanceid"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

func enroll(args []string, out io.Writer) (retErr error) {
	fs := flag.NewFlagSet("project-home", flag.ContinueOnError)
	state := fs.String("state-dir", "", "initialized daemon state directory")
	home := fs.String("home", "", "self or the home instance's authority ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *state == "" || fs.NArg() != 1 || !leadrole.ValidProject(fs.Arg(0)) || *home == "" {
		return errors.New("usage: project-home --state-dir DIR --home self|AUTHORITY PROJECT")
	}
	id, err := instanceid.Load(*state)
	if err != nil {
		return err
	}
	authority := *home
	if authority == "self" {
		authority = id.Authority
	}
	r, err := leadregistry.Open(filepath.Join(*state, leadregistry.DirName), leadregistry.Options{})
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, r.Close()) }()
	project := fs.Arg(0)
	row, err := r.ProjectHome(project)
	switch {
	case errors.Is(err, leadregistry.ErrHomeMissing):
		if err := r.SetProjectAssignment(project, authority, leadregistry.ProjectVersion{}, true); err != nil {
			return err
		}
	case err != nil:
		return err
	case row.HomeAuthority != authority || row.Holder == nil:
		return errors.New("project already enrolled with a different assignment")
	}
	_, err = fmt.Fprintf(out, "project=%s home_authority=%s\n", project, authority)
	return err
}

func main() {
	if err := enroll(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "project-home:", err)
		os.Exit(1)
	}
}
