//go:build linux && ataniq_canary

package leaddaemon

import (
	"errors"
	"fmt"
	"os"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
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
	// write of one request frame, the two together. With [DefaultInjectWait] it
	// bounds an inject exchange at 25 s, under the 30 s the command's client
	// waits ([CanaryInjectTimeout]).
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
// config's three limits for the config's role, opens the evidence file, reads
// back from it the epochs whose one message was sent (an evidence file that
// cannot be read back refuses the start, at the state step) and, once it
// listens, writes the daemon-start line and registers the canary extension (see
// the operations in canary_ext_linux.go).
type Canary struct {
	// Config is the canary config, as leadcanary.ReadConfig returns it.
	Config leadcanary.Config
	// Getenv reads the environment for the root check; default os.Getenv.
	Getenv func(string) string
	// InjectWait is the wait for the holder's answer to an inject; default
	// [DefaultInjectWait].
	InjectWait time.Duration
	// DialTimeout bounds the dial of the holder's control socket and the write
	// of a request frame, the two together; default [DefaultDialTimeout].
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

// canaryMode is the canary parts of one [Run]; nil when canary mode is off. It
// holds the router of the roles the daemon runs (one, the config's, in canary
// mode) and the waits every instance shares.
type canaryMode struct {
	d         *daemon
	router    *leadRouter
	injectFor time.Duration
	dialFor   time.Duration
	// idPrefix begins the command ids the daemon gives the holder; empty means
	// the canary's.
	idPrefix string
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
	router := newLeadRouter(msgNotCanary)
	if err := router.add(&leadInstance{cfg: cfg, preflight: pre, holder: holder}); err != nil {
		return nil, fail(StepConfig, "canary mode: %v", err)
	}
	m := &canaryMode{d: d, router: router, injectFor: c.InjectWait, dialFor: c.DialTimeout}
	if m.injectFor <= 0 {
		m.injectFor = DefaultInjectWait
	}
	if m.dialFor <= 0 {
		m.dialFor = DefaultDialTimeout
	}
	return m, nil
}

// openEvidence opens the evidence file of every instance in the state root and
// reads back the epochs whose one message it shows as sent. Both locks are
// held, so nothing else appends to the files. A file that cannot be read back
// line by line refuses the start: the daemon admits no message it cannot rule
// out. The refusal says what setting the file aside costs: a new file spends no
// epoch's message, so a lead that still runs in an epoch whose message was sent
// would be given a second one. The operator stops the role's unit first. When
// one instance fails, the files already opened are closed.
func (m *canaryMode) openEvidence() error {
	if m == nil {
		return nil
	}
	for _, role := range m.router.order {
		in := m.router.byRole[role]
		// OpenEvidence judges the file (a regular file of the caller, mode
		// 0600, one link) and makes it when it is not there.
		ev, err := leadcanary.OpenEvidence(in.cfg, m.d.Clock.Now)
		if err == nil {
			var spent map[uint64]bool
			spent, err = spentEpochs(in.cfg.EvidencePath(), in.cfg.Role.String())
			if err != nil {
				_ = ev.Close()
				err = fmt.Errorf("canary mode: %w; no message is admitted from an evidence file that cannot be read back; set the file aside only once the role's unit is stopped, since a new file spends no epoch's message", err)
			} else {
				for epoch := range spent {
					m.d.logf("canary: the evidence shows the message of epoch %d as sent; the epoch takes no other", epoch)
				}
				in.ev, in.spent = ev, spent
			}
		}
		if err != nil {
			_ = m.closeEvidence()
			return err
		}
	}
	return nil
}

// closeEvidence closes every evidence file that is open, in the reverse of the
// order they were opened, and returns what failed.
func (m *canaryMode) closeEvidence() error {
	if m == nil {
		return nil
	}
	var errs []error
	for i := len(m.router.order) - 1; i >= 0; i-- {
		in := m.router.byRole[m.router.order[i]]
		if in.ev == nil {
			continue
		}
		errs = append(errs, in.ev.Close())
		in.ev = nil
	}
	return errors.Join(errs...)
}

// configure gives the lifecycle the router as its preflight and holder command,
// and the router's limits for the roles it has.
func (m *canaryMode) configure(o *leadlifecycle.Options) {
	if m == nil {
		return
	}
	o.Preflight = m.router
	o.Holder = m.router
	o.Limits = m.router.Limits
}

// announce writes the daemon-start line of every instance; the daemon is
// listening.
func (m *canaryMode) announce() error {
	if m == nil {
		return nil
	}
	for _, role := range m.router.order {
		if err := m.router.byRole[role].ev.Append(leadcanary.Entry{Event: leadcanary.EventDaemonStart}); err != nil {
			return err
		}
	}
	return nil
}

// extensions returns the canary extension: the router's, over one extension of
// each role.
func (m *canaryMode) extensions(ctl *leadlifecycle.Controller, reg *leadregistry.Registry) []clientapi.Extension {
	if m == nil {
		return nil
	}
	for _, role := range m.router.order {
		in := m.router.byRole[role]
		in.ext = m.newExt(in, ctl, reg)
	}
	return []clientapi.Extension{&routerExt{router: m.router}}
}
