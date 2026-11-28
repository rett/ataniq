//go:build linux && ataniq_ownerchannel

package main

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"ataniq/internal/genesis/handoff"
	"ataniq/internal/genesis/thread"
	"ataniq/internal/leaddaemon"
)

// This file is the desk's side of the guided new-project interview, in a build
// with the ataniq_ownerchannel tag, the only build whose daemon keeps the
// interview's threads (owner/genesis of the state folder, opened by the seat
// gate in seatgate_linux.go): "ataniqd genesis list" and "ataniqd genesis
// handoff TAG". Both read the folder through the thread store's read-only
// reader, whether or not the daemon runs, and write nothing; they run no
// program, open no socket and never run Qargi. genesis_off_linux.go has the
// same names for every other build.

// exitGenesisRefused is the exit code of a hand-off refused for what was
// asked: a tag that names no finished interview with a plan, or more than
// one, or a plan of a kind that has no hand-off yet.
const exitGenesisRefused = 3

// genesisCommand handles "ataniqd genesis list [--state-dir DIR]" and
// "ataniqd genesis handoff TAG [--state-dir DIR]".
func (a *app) genesisCommand(args []string) (code int, ok bool) {
	if len(args) == 0 || args[0] != "genesis" {
		return 0, false
	}
	if len(args) < 2 || (args[1] != "list" && args[1] != "handoff") {
		fmt.Fprint(a.stderr, usage)
		return leaddaemon.ExitUsage, true
	}
	word, rest := args[1], args[2:]
	var tag string
	if word == "handoff" {
		// The tag comes first; the flags follow it.
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			fmt.Fprint(a.stderr, usage)
			return leaddaemon.ExitUsage, true
		}
		tag, rest = rest[0], rest[1:]
	}
	var stateDir string
	if code := a.flags("genesis "+word, rest, func(fs *flag.FlagSet) {
		fs.StringVar(&stateDir, "state-dir", "", "state folder")
	}); code >= 0 {
		return code, true
	}
	if stateDir == "" {
		var err error
		if stateDir, err = leaddaemon.DefaultStateDir(a.getenv); err != nil {
			fmt.Fprintf(a.stderr, "ataniqd: %s\n", leaddaemon.SafeText(err.Error()))
			return leaddaemon.ExitConfig, true
		}
	} else if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		fmt.Fprintf(a.stderr, "ataniqd genesis %s: the state folder %q is not a clean absolute path\n", word, leaddaemon.SafeText(stateDir))
		return leaddaemon.ExitConfig, true
	}
	dir := genesisDir(stateDir)
	if word == "list" {
		return a.genesisList(dir), true
	}
	return a.genesisHandoff(dir, tag), true
}

// genesisList prints one line per finished interview that holds a plan,
// oldest first: its tag, its kind, the plan's digest (12 digits) and the day
// it was finished. A file the reader skips is named on standard error, in
// the store's fixed text.
func (a *app) genesisList(dir string) int {
	list, warnings, err := thread.ReadFinished(dir)
	if err != nil {
		fmt.Fprintf(a.stderr, "ataniqd genesis list: %s\n", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitFailure
	}
	for _, w := range warnings {
		fmt.Fprintf(a.stderr, "ataniqd genesis list: %s\n", leaddaemon.SafeText(w))
	}
	if len(list) == 0 {
		fmt.Fprintln(a.stderr, "ataniqd genesis list: no finished interview holds a plan")
		return leaddaemon.ExitOK
	}
	for _, s := range list {
		fmt.Fprintf(a.stdout, "%s  %s  %s  %s\n", s.Tag, leaddaemon.SafeText(s.Kind), s.PlanShort, s.Finished.UTC().Format("2006-01-02"))
	}
	return leaddaemon.ExitOK
}

// genesisHandoff prints the hand-off of the plan of the finished interview
// whose tag is given (package genesis/handoff): comment lines and one command
// line for the owner to run himself. A refusal is its fixed text on standard
// error and exit code 3.
func (a *app) genesisHandoff(dir, tag string) int {
	p, id, err := thread.ReadPlan(dir, tag)
	var te *thread.TagError
	switch {
	case errors.As(err, &te):
		fmt.Fprintf(a.stderr, "ataniqd genesis handoff: %s\n", te.Error())
		return exitGenesisRefused
	case err != nil:
		fmt.Fprintf(a.stderr, "ataniqd genesis handoff: %s\n", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitFailure
	}
	h, err := handoff.Emit(p, id)
	var r *handoff.Refusal
	switch {
	case errors.As(err, &r):
		fmt.Fprintf(a.stderr, "ataniqd genesis handoff: %s\n", r.Error())
		return exitGenesisRefused
	case err != nil:
		fmt.Fprintf(a.stderr, "ataniqd genesis handoff: %s\n", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitFailure
	}
	fmt.Fprint(a.stdout, h.Text())
	return leaddaemon.ExitOK
}
