//go:build linux && ataniq_canary

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadsandbox/safefile"
)

// This file holds the two subcommands of scratch mode that are the owner's
// alone, because they make or remove folders and the daemon never does either
// to a place it has not been given: scratch-init makes the scratch place's
// state root and writes its root marker, the place gate, and scratch-clean
// removes what a define of one lead left when it was cut short. Both read the
// scratch config (leadcanary.ReadScratchConfig), not the canary's.

// runScratch runs scratch-init or scratch-clean.
func runScratch(ctx context.Context, sub string, args []string, out, errOut io.Writer, getenv func(string) string) int {
	fl := flag.NewFlagSet(sub, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	config := fl.String("config", "", "the scratch config file")
	lead := new(string)
	need := "--config CONFIG is"
	if sub == "scratch-clean" {
		lead = fl.String("lead", "", "the key of the lead")
		need = "--config CONFIG and --lead KEY are"
	}
	if err := fl.Parse(args); err != nil || fl.NArg() != 0 || *config == "" || (sub == "scratch-clean" && *lead == "") {
		fmt.Fprintf(errOut, "ataniq-canary %s: %s required and nothing else is accepted\n%s\n", sub, need, usage)
		return exitUsage
	}
	s, err := leadcanary.ReadScratchConfig(*config)
	if err != nil {
		fmt.Fprintf(errOut, "ataniq-canary %s: %v\n", sub, err)
		return exitFailed
	}
	opts := provision.Options{Getenv: getenv, Log: func(line string) { fmt.Fprintln(out, line) }}
	if sub == "scratch-init" {
		err = scratchInit(s, opts)
	} else {
		err = scratchClean(ctx, s, *lead, opts)
	}
	if err != nil {
		fmt.Fprintf(errOut, "ataniq-canary %s: %v\n", sub, err)
		return exitFailed
	}
	return exitOK
}

// scratchInit makes the scratch place of the config: it refuses the account's
// default roots, makes the state root (mode 0700, its parent must be there)
// when it is not there, and writes the root marker scratch-root.json in it.
// That marker is the place gate of scratch mode: only this command writes it,
// so a daemon cannot make a place into a scratch place by itself. A root that
// already carries the right marker is kept and nothing is written, so the
// command may be run again; a root that carries the canary's marker, or a
// marker that is not the one this command writes, is refused. It makes no lead
// folder, no mirror and no runtime root: a define does.
func scratchInit(s leadcanary.ScratchConfig, opts provision.Options) error {
	say := func(line string) {
		if opts.Log != nil {
			opts.Log(line)
		}
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if err := s.CheckRoots(getenv); err != nil {
		return err
	}
	leads, err := s.Leads()
	if err != nil {
		return err
	}
	if err := makeStateRoot(s.StateRoot, say); err != nil {
		return err
	}
	// Any lead's project names the marker rule: the scratch marker names none.
	project := leads[0].Role.Project()
	switch err := canaryroot.CheckMarker(s.StateRoot, project); {
	case err == nil:
		say("kept file " + filepath.Join(s.StateRoot, canaryroot.ScratchMarkerName))
		return nil
	case !isCode(err, canaryroot.CodeNoMarker):
		return err
	}
	if err := canaryroot.WriteScratchMarker(s.StateRoot); err != nil {
		return err
	}
	say("made file " + filepath.Join(s.StateRoot, canaryroot.ScratchMarkerName))
	return nil
}

// isCode reports a canaryroot refusal with the code.
func isCode(err error, code canaryroot.Code) bool {
	var re *canaryroot.RefusedError
	return errors.As(err, &re) && re.Code == code
}

// makeStateRoot makes the state root with mode 0700 when it is not there and
// checks what is there: a real folder (no symbolic link in any component) that
// the caller owns, with no access for group or others.
func makeStateRoot(root string, say func(string)) error {
	made := false
	if err := os.Mkdir(root, 0o700); err == nil {
		made = true
		// The umask may have cleared bits of the mode; the folder is 0700.
		if err := os.Chmod(root, 0o700); err != nil {
			return fmt.Errorf("state root %s: %w", root, err)
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("state root %s: %w (its parent folder must exist)", root, err)
	}
	h, err := safefile.OpenRootNoLinks(root)
	if err != nil {
		return fmt.Errorf("state root %s: %w", root, err)
	}
	defer h.Close()
	info, err := h.Lstat(".")
	if err != nil {
		return fmt.Errorf("state root %s: %w", root, err)
	}
	switch {
	case !info.IsDir():
		return fmt.Errorf("state root %s: is not a folder", root)
	case info.UID != uint32(unix.Geteuid()):
		return fmt.Errorf("state root %s: is not owned by the caller", root)
	case info.Mode.Perm()&0o077 != 0:
		return fmt.Errorf("state root %s: has mode %04o, must have no access for group or others", root, info.Mode.Perm())
	}
	if made {
		say("made folder " + root)
	} else {
		say("kept folder " + root)
	}
	return nil
}

// scratchClean removes what a define of the lead with the key left when it was
// cut short ([provision.CleanScratch]).
func scratchClean(_ context.Context, s leadcanary.ScratchConfig, key string, opts provision.Options) error {
	leads, err := s.Leads()
	if err != nil {
		return err
	}
	project := leadrole.ScratchPrefix + key
	for _, cfg := range leads {
		if cfg.Role.Project() == project {
			return provision.CleanScratch(cfg, opts)
		}
	}
	return fmt.Errorf("--lead: the config lists no lead with that key")
}
