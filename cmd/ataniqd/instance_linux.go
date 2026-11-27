package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"ataniq/internal/instanceid"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadregistry"
)

func (a *app) initCmd(args []string) int {
	var stateDir, label string
	if code := a.flags("init", args, func(fs *flag.FlagSet) {
		fs.StringVar(&stateDir, "state-dir", "", "state folder")
		fs.StringVar(&label, "label", "", "display label")
	}); code >= 0 {
		return code
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(a.stderr, "ataniqd init: refusing to initialize as root")
		return leaddaemon.ExitConfig
	}
	if stateDir == "" {
		var err error
		if stateDir, err = leaddaemon.DefaultStateDir(a.getenv); err != nil {
			fmt.Fprintln(a.stderr, "ataniqd init:", err)
			return leaddaemon.ExitConfig
		}
	}
	id, err := leadregistry.InitInstance(stateDir, leaddaemon.RegistryDirName, label)
	if err != nil {
		fmt.Fprintln(a.stderr, "ataniqd init:", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitState
	}
	fmt.Fprintf(a.stdout, "authority=%s\nincarnation=%s\n", id.Authority, id.Incarnation)
	if id.Label != "" {
		fmt.Fprintf(a.stdout, "label=%s\n", leaddaemon.SafeText(id.Label))
	}
	return leaddaemon.ExitOK
}

func (a *app) requireInstance(cfg leaddaemon.Config) error {
	if os.Geteuid() == 0 {
		return &leaddaemon.StartupError{Step: leaddaemon.StepConfig, Err: errors.New("refusing to run as root")}
	}
	_, err := instanceid.Load(cfg.StateDir)
	if err == nil {
		return nil
	}
	// Keep the place refusal ahead of any suggestion to initialize a
	// scratch directory. The daemon checks the place on successful runs.
	if placeErr := checkInstancePlace(cfg, a.getenv); placeErr != nil {
		return &leaddaemon.StartupError{Step: leaddaemon.StepConfig, Err: placeErr}
	}
	if errors.Is(err, instanceid.ErrMissing) {
		err = fmt.Errorf("%w in %q; for a new or pre-identity root use ataniqd init --state-dir DIR; for a bound journal restore its original key", err, cfg.StateDir)
	}
	return &leaddaemon.StartupError{Step: leaddaemon.StepState, Err: err}
}
