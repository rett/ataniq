//go:build linux && ataniq_canary

package leaddaemon

import (
	"os"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// CanaryTag is the build tag of canary mode. A binary built with it says so in
// its version output.
const CanaryTag = "ataniq_canary"

// Defaults of [Canary].
const (
	// DefaultInjectWait is how long the daemon waits for the holder's answer to
	// an inject request, counted from the moment the request frame is written:
	// the holder's own bound on a delivery (15 s) and 5 s.
	DefaultInjectWait = 20 * time.Second
	// DefaultDialTimeout bounds the dial of the holder's control socket and the
	// write of one request frame.
	DefaultDialTimeout = 5 * time.Second
)

// CanaryConfig is what a build with the ataniq_canary tag adds to [Config]. In
// a build without the tag the struct is empty (canary_off_linux.go) and
// nothing can set it.
type CanaryConfig struct {
	// Canary, when not nil, puts the daemon in canary mode. Nil is the
	// read-only skeleton: no extension is registered and no canary holder
	// command is ever built.
	Canary *Canary
}

// Canary is the canary mode of one [Run].
//
// The daemon's StateDir and RuntimeDir must be Config.StateRoot and
// Config.RuntimeRoot: the roots are the config's. Before anything is made, Run
// validates the config (every limit is required), runs [leadcanary.Config.CheckRoots]
// (the default roots, nesting, the marker, the prefix) and refuses to start on
// any failure, at the config step. It then gives the lifecycle the canary
// preflight, the canary holder command and a Limits seam that answers the
// config's three limits for the config's role, opens the evidence file and, once
// it listens, writes the daemon-start line and registers the canary extension
// (see the operations in canary_ext_linux.go).
type Canary struct {
	// Config is the canary config, as leadcanary.ReadConfig returns it.
	Config leadcanary.Config
	// Getenv reads the environment for the root check; default os.Getenv.
	Getenv func(string) string
	// InjectWait is the wait for the holder's answer to an inject; default
	// [DefaultInjectWait].
	InjectWait time.Duration
	// DialTimeout bounds the dial of the holder's control socket and the write
	// of a request frame; default [DefaultDialTimeout].
	DialTimeout time.Duration
}

// newHolderCommand builds the canary holder command. It is a variable so that a
// test can see that the builder is called with canary mode and never without.
var newHolderCommand = func(cfg leadcanary.Config, daemonLock string) (leadlifecycle.HolderCommand, error) {
	h, err := leadcanary.NewHolderCommand(cfg, daemonLock)
	if err != nil {
		return nil, err
	}
	return h, nil
}

// canaryMode is the canary parts of one [Run]; nil when canary mode is off.
type canaryMode struct {
	d         *daemon
	cfg       leadcanary.Config
	preflight leadlifecycle.Preflight
	holder    leadlifecycle.HolderCommand
	injectFor time.Duration
	dialFor   time.Duration
	ev        *leadcanary.Evidence
}

// canarySetup checks the canary config and builds the parts that need no file.
// Every refusal is a [StartupError] of [StepConfig].
func (d *daemon) canarySetup() (*canaryMode, error) {
	c := d.Canary
	if c == nil {
		return nil, nil
	}
	cfg := c.Config
	if err := cfg.Validate(); err != nil {
		return nil, fail(StepConfig, "canary mode: %v", err)
	}
	if d.StateDir != cfg.StateRoot || d.RuntimeDir != cfg.RuntimeRoot {
		return nil, fail(StepConfig, "canary mode: the state and runtime folders are the canary config's roots")
	}
	getenv := c.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if err := cfg.CheckRoots(getenv); err != nil {
		return nil, fail(StepConfig, "canary mode: %v", err)
	}
	pre, err := leadcanary.NewPreflight(cfg.Role)
	if err != nil {
		return nil, fail(StepConfig, "canary mode: %v", err)
	}
	holder, err := newHolderCommand(cfg, leadlock.DaemonPath(d.StateDir))
	if err != nil {
		return nil, fail(StepConfig, "canary mode: %v", err)
	}
	m := &canaryMode{d: d, cfg: cfg, preflight: pre, holder: holder, injectFor: c.InjectWait, dialFor: c.DialTimeout}
	if m.injectFor <= 0 {
		m.injectFor = DefaultInjectWait
	}
	if m.dialFor <= 0 {
		m.dialFor = DefaultDialTimeout
	}
	return m, nil
}

// openEvidence opens the evidence file in the state root.
func (m *canaryMode) openEvidence() error {
	if m == nil {
		return nil
	}
	ev, err := leadcanary.OpenEvidence(m.cfg, m.d.Clock.Now)
	if err != nil {
		return err
	}
	m.ev = ev
	return nil
}

func (m *canaryMode) closeEvidence() error {
	if m == nil || m.ev == nil {
		return nil
	}
	return m.ev.Close()
}

// configure gives the lifecycle the canary preflight, the canary holder command
// and the limits of the config for the config's role.
func (m *canaryMode) configure(o *leadlifecycle.Options) {
	if m == nil {
		return
	}
	o.Preflight = m.preflight
	o.Holder = m.holder
	role, l := m.cfg.Role, m.cfg.Limits
	o.Limits = func(r leadrole.Role) *leadunit.Limits {
		if r != role {
			return nil
		}
		return &leadunit.Limits{MemoryMax: l.MemoryMaxBytes, SwapMax: l.SwapMaxBytes, TasksMax: l.TasksMax}
	}
}

// announce writes the daemon-start line; the daemon is listening.
func (m *canaryMode) announce() error {
	if m == nil {
		return nil
	}
	return m.ev.Append(leadcanary.Entry{Event: leadcanary.EventDaemonStart})
}

// extensions returns the canary extension.
func (m *canaryMode) extensions(ctl *leadlifecycle.Controller, reg *leadregistry.Registry) []clientapi.Extension {
	if m == nil {
		return nil
	}
	return []clientapi.Extension{newCanaryExt(m, ctl, reg)}
}
