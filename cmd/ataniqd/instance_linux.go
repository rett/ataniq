package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ataniq/internal/instanceid"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"golang.org/x/sys/unix"
)

func (a *app) initCmd(args []string) int {
	var stateDir string
	var label *string
	var restored bool
	if code := a.flags("init", args, func(fs *flag.FlagSet) {
		fs.StringVar(&stateDir, "state-dir", "", "state folder")
		fs.BoolVar(&restored, "restored", false, "start a fresh store incarnation for a restored authority key")
		fs.Func("label", "display label", func(value string) error {
			label = &value
			return instanceid.ValidateLabel(value)
		})
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
	if restored && label != nil {
		fmt.Fprintln(a.stderr, "ataniqd init: --restored and --label exclude each other")
		return leaddaemon.ExitUsage
	}
	id, err := initializeInstance(stateDir, label, restored)
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

func initializeInstance(stateDir string, label *string, restored bool) (instanceid.Identity, error) {
	root, err := instanceid.OpenRoot(stateDir, !restored)
	if err != nil {
		return instanceid.Identity{}, err
	}
	defer root.Close()
	// Serialize initializers, including restore on a root with no journal.
	if err := unix.Flock(int(root.Fd()), unix.LOCK_EX); err != nil {
		return instanceid.Identity{}, err
	}
	current, err := instanceid.CheckInit(root, restored)
	if err != nil {
		return instanceid.Identity{}, err
	}
	if restored {
		lock, err := leadlock.Acquire(leadlock.DaemonPath(stateDir))
		if err != nil {
			return instanceid.Identity{}, err
		}
		defer lock.Release()
		return instanceid.RestoreAt(root)
	}
	switch {
	case current.Authority == "":
		if err := validateBeforeKey(root); err != nil {
			if errors.Is(err, instanceid.ErrMissing) {
				return instanceid.Identity{}, fmt.Errorf("%s: bound journal requires its original key %s", journalPath(stateDir), filepath.Join(stateDir, instanceid.FileName))
			}
			return instanceid.Identity{}, fmt.Errorf("%s: %w", journalPath(stateDir), err)
		}
	case label != nil:
		return instanceid.Identity{}, fmt.Errorf("%s already exists; --label is accepted only on first init", filepath.Join(stateDir, instanceid.FileName))
	}
	var display string
	if label != nil {
		display = *label
	}
	id, err := leadregistry.InitInstance(stateDir, leaddaemon.RegistryDirName, display)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			if !filepath.IsAbs(pe.Path) {
				return id, &os.PathError{Op: pe.Op, Path: filepath.Join(stateDir, pe.Path), Err: pe.Err}
			}
			return id, err
		}
		return id, fmt.Errorf("%s: %w", journalPath(stateDir), err)
	}
	return id, nil
}

func initHint(stateDir string) string {
	return "ataniqd init --state-dir '" + strings.ReplaceAll(stateDir, "'", `'"'"'`) + "'"
}

func journalPath(stateDir string) string {
	return filepath.Join(stateDir, leaddaemon.RegistryDirName, leadregistry.FileName)
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
		err = fmt.Errorf("%s: %w; for a new or pre-identity root use %s; for key recovery follow the restore procedure", filepath.Join(cfg.StateDir, instanceid.FileName), instanceid.ErrMissing, initHint(cfg.StateDir))
	}
	return &leaddaemon.StartupError{Step: leaddaemon.StepState, Err: err}
}
