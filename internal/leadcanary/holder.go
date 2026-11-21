//go:build linux && ataniq_canary

package leadcanary

import (
	"errors"
	"fmt"
	"path"
	"strconv"

	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadsandbox/profile"
)

// ErrHolderCommand is wrapped by every refusal of [HolderCommand.Build] and of
// [NewHolderCommand].
var ErrHolderCommand = errors.New("leadcanary: cannot build the holder command")

// holderEnv is the whole environment of the holder, and nothing else is
// inherited: the unit's environment is exactly what Build returns.
//
//   - PATH is fixed, so that a program the holder finds by name (the tools its
//     start sequence runs) comes from the system folders and never from the
//     manager's PATH or a user folder.
//   - LANG is fixed, so that the text the holder logs and the tools it runs do
//     not depend on the manager's locale.
//
// HOME, USER and the XDG_* variables are left out on purpose: the holder's root
// check (canaryroot) must hold in a bare environment, and giving it these would
// hide a fault of that check. The agent's environment is not this one: it is
// the --env flags, one each, and the holder gives the agent nothing else.
var holderEnv = []string{
	"PATH=/usr/local/bin:/usr/bin:/bin",
	"LANG=C.UTF-8",
}

// HolderEnvironment returns a copy of the whole environment Build gives the
// holder.
func HolderEnvironment() []string { return append([]string(nil), holderEnv...) }

// HolderCommand builds the command line of the canary holder. It satisfies
// leadlifecycle.HolderCommand. See Build for the line.
type HolderCommand struct {
	cfg        Config
	daemonLock string
	layout     profile.Layout
	runtimeDir string
	unit       string
}

var _ leadlifecycle.HolderCommand = (*HolderCommand)(nil)

// NewHolderCommand returns the builder for the config's role. daemonLock is the
// path of the daemon's own lock file, which the holder's peer check uses to
// accept the daemon; it must be an absolute clean path of plain text. The
// config is validated again ([Config.Validate]) and every path the line carries
// is checked against the holder's own limits (absolute, plain text, at most
// 4096 bytes), and the holder's state folder and runtime folder must differ.
func NewHolderCommand(cfg Config, daemonLock string) (*HolderCommand, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHolderCommand, err)
	}
	_, unit, err := NamesFor(cfg.Reserved, cfg.Role)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHolderCommand, err)
	}
	layout, err := cfg.Layout()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHolderCommand, err)
	}
	runtimeDir, err := cfg.HolderRuntimeDir()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHolderCommand, err)
	}
	for _, p := range []struct{ name, val string }{
		{"daemon lock", daemonLock}, {"tree", layout.Tree()}, {"state dir", layout.Holder()},
		{"runtime dir", runtimeDir}, {"role lock", cfg.Role.LockPath(cfg.StateRoot)},
	} {
		if !absClean(p.val) {
			return nil, fmt.Errorf("%w: the %s is not an absolute clean path of plain text of at most %d bytes", ErrHolderCommand, p.name, maxPath)
		}
	}
	if path.Clean(layout.Holder()) == path.Clean(runtimeDir) {
		return nil, fmt.Errorf("%w: the state dir and the runtime dir are the same folder", ErrHolderCommand)
	}
	return &HolderCommand{cfg: cfg, daemonLock: daemonLock, layout: layout, runtimeDir: runtimeDir, unit: unit}, nil
}

// Build returns the holder's argument vector, working directory and whole
// environment for one launch.
//
// The argument vector is the holder binary (an absolute path), then, in this
// order and each flag and its value as two elements:
//
//	--role <project>/lead  --epoch <n>  --launch-intent <intent id>
//	--runtime <agent.runtime>  --runtime-version <agent.runtime_version>
//	--profile-digest <holder.profile_digest>
//	--tree <layout tree>  --state-dir <layout holder folder>
//	--runtime-dir <runtime root>/leads/<layout role>
//	--role-lock <role lock path under the state root>
//	--daemon-lock <the daemon's lock file>
//	--cols <n>  --rows <n>
//	--canary  [--inject, only when the config says inject: true]
//	--sandbox-inputs <holder.sandbox_inputs>
//	--expected-mode <agent.footer_mode>
//	--env <KEY=VALUE>  (one per agent.env entry, in order)
//	--  <agent.argv...>
//
// The working directory is the holder's state folder (the layout's holder
// folder). The environment is [HolderEnvironment] and nothing inherited.
//
// It refuses (an error that wraps [ErrHolderCommand]) a role other than the
// config's, a record or an intent that does not belong to the role or has epoch
// 0, and an intent id that the holder's --launch-intent would not accept (1 to
// 128 characters of letters, digits and . _ : -, not starting with a hyphen).
func (h *HolderCommand) Build(role leadrole.Role, entry leadregistry.Entry, intent leadregistry.Intent) (argv []string, workdir string, env []string, err error) {
	switch {
	case role != h.cfg.Role || entry.Role != h.cfg.Role:
		return nil, "", nil, fmt.Errorf("%w: not the canary role", ErrHolderCommand)
	case intent.Epoch == 0 || entry.Epoch != intent.Epoch:
		return nil, "", nil, fmt.Errorf("%w: the intent's epoch is not the record's epoch, or is 0", ErrHolderCommand)
	case intent.UnitName != h.unit:
		return nil, "", nil, fmt.Errorf("%w: the intent's unit is not the role's unit", ErrHolderCommand)
	case !intentIDOK(intent.ID):
		return nil, "", nil, fmt.Errorf("%w: the intent id is not acceptable to the holder", ErrHolderCommand)
	}
	c := h.cfg
	argv = []string{
		c.Holder.Executable,
		"--role", c.Role.String(),
		"--epoch", strconv.FormatUint(intent.Epoch, 10),
		"--launch-intent", intent.ID,
		"--runtime", c.Agent.Runtime,
		"--runtime-version", c.Agent.RuntimeVersion,
		"--profile-digest", c.Holder.ProfileDigest,
		"--tree", h.layout.Tree(),
		"--state-dir", h.layout.Holder(),
		"--runtime-dir", h.runtimeDir,
		"--role-lock", c.Role.LockPath(c.StateRoot),
		"--daemon-lock", h.daemonLock,
		"--cols", strconv.Itoa(c.Holder.Cols),
		"--rows", strconv.Itoa(c.Holder.Rows),
		"--canary",
	}
	if c.Inject {
		argv = append(argv, "--inject")
	}
	argv = append(argv,
		"--sandbox-inputs", c.Holder.SandboxInputs,
		"--expected-mode", c.Agent.FooterMode,
	)
	for _, kv := range c.Agent.Env {
		argv = append(argv, "--env", kv)
	}
	argv = append(argv, "--")
	argv = append(argv, c.Agent.Argv...)
	return argv, h.layout.Holder(), HolderEnvironment(), nil
}

// intentIDOK is the holder's rule for --launch-intent (1 to 128 characters of
// letters, digits and . _ : -), with a first character that is not a hyphen so
// that the value can never be read as a flag.
func intentIDOK(s string) bool {
	if len(s) < 1 || len(s) > 128 || s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}
