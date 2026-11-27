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
	var restored, leaveJournal bool
	if code := a.flags("init", args, func(fs *flag.FlagSet) {
		fs.StringVar(&stateDir, "state-dir", "", "state folder")
		fs.BoolVar(&restored, "restored", false, "start a fresh store incarnation for a restored authority key")
		fs.BoolVar(&leaveJournal, "leave-old-journal", false, "acknowledge that the journal left in place will be unusable after restore")
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
	if leaveJournal && !restored {
		fmt.Fprintln(a.stderr, "ataniqd init: --leave-old-journal requires --restored")
		return leaddaemon.ExitUsage
	}
	result, err := a.initializeInstance(stateDir, label, restored, leaveJournal)
	if err != nil {
		fmt.Fprintln(a.stderr, "ataniqd init:", leaddaemon.SafeText(err.Error()))
		return leaddaemon.ExitState
	}
	id := result.Identity
	fmt.Fprintf(a.stdout, "authority=%s\nincarnation=%s\n", id.Authority, id.Incarnation)
	if id.Label != "" {
		fmt.Fprintf(a.stdout, "label=%s\n", leaddaemon.SafeText(id.Label))
	}
	switch result.Journal {
	case "migrated":
		fmt.Fprintf(a.stdout, "journal=migrated records=%d backup=%s/%s\n", result.Records, leaddaemon.RegistryDirName, leadregistry.LegacyFileName)
	case "created", "existing":
		fmt.Fprintf(a.stdout, "journal=%s\n", result.Journal)
	}
	return leaddaemon.ExitOK
}

func (a *app) initializeInstance(stateDir string, label *string, restored, leaveJournal bool) (leadregistry.InitResult, error) {
	root, err := instanceid.OpenRoot(stateDir, !restored)
	if err != nil {
		return leadregistry.InitResult{}, err
	}
	defer root.Close()
	// Serialize initializers, including restore on a root with no journal.
	if err := unix.Flock(int(root.Fd()), unix.LOCK_EX); err != nil {
		return leadregistry.InitResult{}, err
	}
	if err := a.cleanRegistryChecks(root); err != nil {
		return leadregistry.InitResult{}, err
	}
	current, err := instanceid.CheckInit(root, restored)
	if errors.Is(err, instanceid.ErrMissing) {
		return leadregistry.InitResult{}, missingKeyError(stateDir, root)
	}
	if err != nil {
		return leadregistry.InitResult{}, err
	}
	if restored {
		lock, err := leadlock.Acquire(leadlock.DaemonPath(stateDir))
		if err != nil {
			return leadregistry.InitResult{}, err
		}
		defer lock.Release()
		header, err := leadregistry.ReadHeaderAt(root)
		if err != nil {
			return leadregistry.InitResult{}, fmt.Errorf("%s: %w", journalPath(stateDir), err)
		}
		if header != nil {
			warning := fmt.Sprintf("the journal at %s will be unusable in the new incarnation", journalPath(stateDir))
			if !leaveJournal {
				return leadregistry.InitResult{}, fmt.Errorf("%s; move it offline or acknowledge with --leave-old-journal; see %s", warning, restoreGuide)
			}
			fmt.Fprintf(a.stderr, "ataniqd init: warning: %s; see %s\n", leaddaemon.SafeText(warning), restoreGuide)
		}
		id, err := instanceid.RestoreAt(root)
		return leadregistry.InitResult{Identity: id}, err
	}
	if current.Authority != "" && label != nil {
		return leadregistry.InitResult{}, fmt.Errorf("%s already exists; --label is accepted only on first init", filepath.Join(stateDir, instanceid.FileName))
	}
	var display string
	if label != nil {
		display = *label
	}
	result, err := leadregistry.InitInstanceAt(root, leaddaemon.RegistryDirName, display)
	if err != nil {
		if errors.Is(err, instanceid.ErrMissing) {
			return result, missingKeyError(stateDir, root)
		}
		var pe *os.PathError
		if errors.As(err, &pe) {
			if !filepath.IsAbs(pe.Path) {
				return result, &os.PathError{Op: pe.Op, Path: filepath.Join(stateDir, pe.Path), Err: pe.Err}
			}
			return result, err
		}
		return result, fmt.Errorf("%s: %w", journalPath(stateDir), err)
	}
	return result, nil
}

func initHint(stateDir string) string {
	return "ataniqd init --state-dir '" + strings.ReplaceAll(stateDir, "'", `'"'"'`) + "'"
}

func journalPath(stateDir string) string {
	return filepath.Join(stateDir, leaddaemon.RegistryDirName, leadregistry.FileName)
}

const restoreGuide = "docs/federation.md#restore-an-instance"

func missingKeyError(state string, root *os.File) error {
	hint := "for a new or pre-identity root use " + initHint(state) + "; for a restore see " + restoreGuide
	if root != nil {
		header, err := leadregistry.ReadHeaderAt(root)
		switch {
		case err != nil:
			hint = fmt.Sprintf("cannot inspect %s: %v; see %s", journalPath(state), err, restoreGuide)
		case header == nil:
			if remaining, err := instanceid.CheckInit(root, false); err == nil && remaining.Incarnation != "" {
				hint = "no journal confirms the missing key; follow the restore procedure in " + restoreGuide + "; if first init was interrupted, retry " + initHint(state)
			}
		case header.Authority != "":
			remaining, err := instanceid.CheckInit(root, false)
			if err == nil && remaining.Incarnation == header.Incarnation {
				hint = fmt.Sprintf("the journal matches this store incarnation; if the root is intact, put the original key for authority %s back in place", header.Authority)
			} else {
				hint = "the journal belongs to another or missing incarnation; follow the restore procedure in " + restoreGuide
			}
		}
	}
	return fmt.Errorf("%s: %w; %s", filepath.Join(state, instanceid.FileName), instanceid.ErrMissing, hint)
}

func (a *app) requireInstance(cfg leaddaemon.Config, checkHeader bool) error {
	if os.Geteuid() == 0 {
		return &leaddaemon.StartupError{Step: leaddaemon.StepConfig, Err: errors.New("refusing to run as root")}
	}
	root, err := instanceid.OpenRoot(cfg.StateDir, false)
	var id instanceid.Identity
	if err == nil {
		defer root.Close()
		id, err = instanceid.LoadRoot(root)
	}
	if err == nil && checkHeader {
		header, err := leadregistry.ReadHeaderAt(root)
		if err == nil && header != nil {
			switch {
			case header.Authority == "":
				err = leadregistry.ErrUnbound
			case *header != id.Binding:
				err = leadregistry.ErrForeignInstance
			}
		}
		if err != nil {
			return &leaddaemon.StartupError{Step: leaddaemon.StepRegistry, Err: err}
		}
	}
	if err == nil {
		return nil
	}
	// Keep the place refusal ahead of any suggestion to initialize a
	// scratch directory. The daemon checks the place on successful runs.
	if placeErr := checkInstancePlace(cfg, a.getenv); placeErr != nil {
		return &leaddaemon.StartupError{Step: leaddaemon.StepConfig, Err: placeErr}
	}
	if errors.Is(err, instanceid.ErrMissing) {
		err = missingKeyError(cfg.StateDir, root)
	}
	return &leaddaemon.StartupError{Step: leaddaemon.StepState, Err: err}
}
