//go:build linux && ataniq_canary

package provision

import (
	"errors"
	"fmt"
	"io/fs"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/standin"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadsandbox/safefile"
)

// ReplayOn writes the replay switch file, <home>/canary-replay/replay.on, and
// nothing else. It is the one operation allowed after a first start: the home
// is a folder the lead writes, so the file is made through a handle of the
// replay folder opened beneath the state root's handle, without following a
// symbolic link in any component, and created exclusively (a file that is
// already there is an error, a symbolic link included). It requires the whole
// canary root check, marker included: the state root must have been
// provisioned. It does not make the replay folder, which provisioning made
// with the screens in it.
func ReplayOn(cfg leadcanary.Config, opts Options) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := canaryroot.Check(cfg.StateRoot, cfg.RuntimeRoot, cfg.Role.Project(), opts.getenv()); err != nil {
		return err
	}
	p, err := PathsOf(cfg)
	if err != nil {
		return err
	}
	state, err := safefile.OpenRootNoLinks(p.StateRoot)
	if err != nil {
		return fmt.Errorf("state root %s: %w", p.StateRoot, err)
	}
	defer state.Close()
	folder, err := state.SubRoot(p.stateRel(p.ReplayDir))
	if err != nil {
		return fmt.Errorf("%s: %w", p.ReplayDir, err)
	}
	defer folder.Close()
	a := &area{root: folder, base: p.ReplayDir, log: opts.Log}
	if err := a.file(standin.SwitchFile, nil, 0o600); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s: the switch file is there already", p.ReplaySwitch)
		}
		return err
	}
	return nil
}
