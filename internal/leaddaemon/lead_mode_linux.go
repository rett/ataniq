//go:build linux && ataniq_canary

package leaddaemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision"
	"ataniq/internal/leadcontrol"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// This file is scratch mode of the daemon: the lead control plane. The daemon
// serves the six lead.* operations (lead_ext_linux.go) for the leads of an
// owner-only scratch config, in place of the canary's five. Everything here is
// behind the ataniq_canary tag.

// LeadCommandsName is the name of the command table in the state root.
const LeadCommandsName = "lead-commands.jsonl"

// DefaultProvisionTimeout is the bound of one define's provisioning when
// [Scratch].ProvisionTimeout is not set: the clone and the copies of the lead
// take their own bounds, this one ends the whole of it. A define that takes
// longer than the console waits for its answer is answered, when the console
// asks again with the same command id, by the receipt that is in progress.
const DefaultProvisionTimeout = 10 * time.Minute

// idPrefixLead begins the command ids that scratch mode gives the holder.
const idPrefixLead = "lead-"

// Scratch is the scratch mode of one [Run]: the daemon serves the lead.*
// operations for the leads of Config. It excludes canary mode, and the
// daemon's StateDir and RuntimeDir must be Config.StateRoot and
// Config.RuntimeRoot: the roots are the config's.
//
// Before anything is made, Run validates the config (every lead's derived
// config included), checks that the roots are the config's, runs
// [canaryroot.Check] for the scratch place (the default roots, the nesting and
// the root marker that only the owner's command writes) and refuses to start on
// any failure, at the config step. Once the daemon holds its two locks it
// opens the evidence file of every lead and reads back the epochs whose one
// message is spent, reads the record of every lead that has one (the digest of
// its profile: a record that is not what provisioning writes, or a digest that
// is not the lead's pin, refuses the start) and opens the command table
// ([LeadCommandsName]); a start that is refused here is a refusal at the state
// step.
type Scratch struct {
	// Config is the scratch config, as leadcanary.ReadScratchConfig returns it.
	Config leadcanary.ScratchConfig
	// Getenv reads the environment for the root check; default os.Getenv.
	Getenv func(string) string
	// InjectWait is the wait for the holder's answer to an inject; default
	// [DefaultInjectWait].
	InjectWait time.Duration
	// DialTimeout bounds the dial of the holder's control socket and the write
	// of a request frame, the two together; default [DefaultDialTimeout].
	DialTimeout time.Duration
	// ProvisionTimeout bounds the provisioning of one define; default
	// [DefaultProvisionTimeout].
	ProvisionTimeout time.Duration
	// ReadyWithin is how long the observer that promotes a starting lead to
	// ready keeps asking its holder; default [DefaultReadyWithin].
	ReadyWithin time.Duration
	// ReadyBackoff is the first wait between two of its questions; default
	// [DefaultReadyBackoff].
	ReadyBackoff time.Duration
}

// leadActHook is called by a command, after it holds the role's operation lock
// (a define, a start or a stop) and, for a ticket or an inject, before it acts.
// Only a test sets it, to make an act panic or wait.
var leadActHook func(op string)

// leadRole is one lead of scratch mode: its instance in the router and what the
// operations keep for it. The operation lock is the role's own, and no lock is
// shared by two roles across a call to the lifecycle, the holder or
// provisioning.
type leadRole struct {
	role leadrole.Role
	cfg  leadcanary.Config
	key  string
	in   *leadInstance
	// op is the role's operation lock, a try-lock: a second command for the
	// role finishes refused (role-busy), it never queues. A define, a start and
	// a stop hold it for the whole command. The holder command of the role is
	// read and written under it (a define sets it), and the lifecycle reads it
	// through the instance's own accessors.
	op chan struct{}
	// defining is true while a define of this role runs in this daemon; the
	// list never calls the lead's provisioning incomplete then.
	defining atomic.Bool
	// ticketMu orders the tickets of the role: there is one ticket file per
	// lead, so two tickets would cross.
	ticketMu sync.Mutex
	// reserved is a start in flight, counted against max_running. It is
	// guarded by the mode's capMu.
	reserved bool
}

func (lr *leadRole) tryLock() bool {
	select {
	case lr.op <- struct{}{}:
		return true
	default:
		return false
	}
}

func (lr *leadRole) unlock() { <-lr.op }

// leadMode is the scratch parts of one [Run]; nil when scratch mode is off.
type leadMode struct {
	cm         *canaryMode
	d          *daemon
	cfg        leadcanary.ScratchConfig
	getenv     func(string) string
	provisionT time.Duration
	roles      map[leadrole.Role]*leadRole
	maxRunning int

	// readyWithin and readyBackoff are the observer's bounds (see
	// lead_ready_linux.go); ready is its observers.
	readyWithin  time.Duration
	readyBackoff time.Duration
	ready        readyObservers

	table *leadcontrol.Table
	ctl   *leadlifecycle.Controller
	reg   *leadregistry.Registry

	// capMu is the max_running mutex: the count of the leads that run and the
	// reservation of a start are made under it, and it is never held across a
	// call that starts or stops anything. The order of the locks is the role's
	// operation lock, then capMu, then the lifecycle and the registry; the
	// table's own mutex is a leaf.
	capMu sync.Mutex

	// broken is set when the registry or the command table was found unusable:
	// the daemon then admits no new command until it is restarted.
	broken atomic.Bool
}

// scratchSetup checks the scratch config and builds the parts that need no
// file. Every refusal is a [StartupError] of [StepConfig].
func (d *daemon) scratchSetup() (*canaryMode, error) {
	s := d.Scratch
	if d.Canary != nil {
		return nil, fail(StepConfig, "scratch mode: canary mode and scratch mode exclude each other")
	}
	if err := s.Config.Validate(); err != nil {
		return nil, fail(StepConfig, "scratch mode: %v", err)
	}
	if d.StateDir != s.Config.StateRoot || d.RuntimeDir != s.Config.RuntimeRoot {
		return nil, fail(StepConfig, "scratch mode: the state and runtime folders are the scratch config's roots")
	}
	leads, err := s.Config.Leads()
	if err != nil {
		return nil, fail(StepConfig, "scratch mode: %v", err)
	}
	getenv := s.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	// The place gate: the default roots, the nesting and the root marker. The
	// marker is the owner's, so the daemon cannot make the place.
	if err := canaryroot.Check(s.Config.StateRoot, s.Config.RuntimeRoot, leads[0].Role.Project(), getenv); err != nil {
		return nil, fail(StepConfig, "scratch mode: %v", err)
	}
	router := newLeadRouter(msgNotALead)
	m := &canaryMode{d: d, router: router, injectFor: s.InjectWait, dialFor: s.DialTimeout, idPrefix: idPrefixLead}
	if m.injectFor <= 0 {
		m.injectFor = DefaultInjectWait
	}
	if m.dialFor <= 0 {
		m.dialFor = DefaultDialTimeout
	}
	lm := &leadMode{
		cm: m, d: d, cfg: s.Config, getenv: getenv, provisionT: s.ProvisionTimeout,
		roles: make(map[leadrole.Role]*leadRole, len(leads)), maxRunning: s.Config.MaxRunning,
		readyWithin: s.ReadyWithin, readyBackoff: s.ReadyBackoff,
	}
	if lm.provisionT <= 0 {
		lm.provisionT = DefaultProvisionTimeout
	}
	for i, cfg := range leads {
		pre, err := leadcanary.NewPreflightFor(leadrole.ReservedScratch, cfg.Role)
		if err != nil {
			return nil, fail(StepConfig, "scratch mode: %v", err)
		}
		// No holder command yet: a lead has one once its record is read or its
		// define has finished, and never one for an unpinned digest.
		in := &leadInstance{cfg: cfg, preflight: pre}
		if err := router.add(in); err != nil {
			return nil, fail(StepConfig, "scratch mode: %v", err)
		}
		lm.roles[cfg.Role] = &leadRole{
			role: cfg.Role, cfg: cfg, key: s.Config.Entries[i].Key, in: in, op: make(chan struct{}, 1),
		}
	}
	m.lead = lm
	return m, nil
}

// open is what scratch mode does once the daemon holds both locks and the
// evidence of every lead is open: it reads the record of every lead that has
// one, and opens the command table.
func (l *leadMode) open() error {
	lock := leadlock.DaemonPath(l.d.StateDir)
	for _, role := range l.cm.router.order {
		lr := l.roles[role]
		rec, err := provision.ReadLeadRecord(lr.cfg)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// Not provisioned: the lead has no holder command and lead.start
			// refuses it until a define has finished.
			continue
		case err != nil:
			return fmt.Errorf("scratch mode: the record of the lead %q is not usable: %w; the owner checks the lead's folder", lr.key, err)
		}
		holder, err := l.holderFor(lr, rec.ProfileDigest, lock)
		if err != nil {
			return fmt.Errorf("scratch mode: the lead %q: %w", lr.key, err)
		}
		lr.in.setHolderCommand(holder)
		l.d.logf("scratch: the lead %q is provisioned; its holder is pinned to the digest of its record", lr.key)
	}
	path := filepath.Join(l.d.StateDir, LeadCommandsName)
	t, err := leadcontrol.Open(path, leadcontrol.Options{Now: l.d.Clock.Now})
	if err != nil {
		return fmt.Errorf("scratch mode: the command table %s cannot be opened: %w; it holds every command that was admitted, so set it aside only once every scratch unit is stopped: a daemon with a new table forgets what was admitted, and a client that asks again would run the command again", path, err)
	}
	l.table = t
	return nil
}

// holderFor builds the holder command of a lead for the digest of its profile:
// the pin of the scratch config, when there is one, must be the digest.
func (l *leadMode) holderFor(lr *leadRole, digest, daemonLock string) (leadlifecycle.HolderCommand, error) {
	if pin, ok := lr.cfg.PinnedDigest(); ok && pin != digest {
		return nil, errors.New("the profile digest of its record is not the one pinned in the scratch config")
	}
	if digest == leadcanary.UnpinnedDigest {
		return nil, errors.New("the profile digest is the unpinned one")
	}
	cfg, err := lr.cfg.WithProfileDigest(digest)
	if err != nil {
		return nil, err
	}
	return newHolderCommand(cfg, daemonLock)
}

// closeTable closes the command table when it is open.
func (l *leadMode) closeTable() error {
	if l == nil || l.table == nil {
		return nil
	}
	return l.table.Close()
}

// extension builds the extension of each lead's instance over the controller
// and the registry, and returns the lead.* extension.
func (l *leadMode) extension(ctl *leadlifecycle.Controller, reg *leadregistry.Registry) clientapi.Extension {
	l.ctl, l.reg = ctl, reg
	for _, role := range l.cm.router.order {
		in := l.cm.router.byRole[role]
		in.ext = l.cm.newExt(in, ctl, reg)
	}
	return &leadExt{m: l}
}

// lookup returns the lead the role text names. The text must read back as
// itself: a spelling the role grammar would turn into another role is no
// lead of the daemon.
func (l *leadMode) lookup(text string) (*leadRole, bool) {
	role, err := leadrole.Parse(text)
	if err != nil || role.String() != text {
		return nil, false
	}
	lr, ok := l.roles[role]
	return lr, ok && lr.in.ext != nil
}

// markBroken records that the registry or the table cannot be written. Every
// later command that is new is finished refused until the daemon is restarted.
func (l *leadMode) markBroken(why string) {
	if l.broken.CompareAndSwap(false, true) {
		l.d.logf("scratch: %s; the daemon admits no new command until it is restarted", why)
	}
}

// counts reports whether the lead counts against max_running: it is shown in
// any state but desired, stopped and ended (so starting, ready, degraded,
// stalled, stopping and unknown, because a unit may run), it has a launch
// intent that is pending, or a start of it is in flight. The caller holds
// capMu.
func (l *leadMode) counts(lr *leadRole) bool {
	if lr.reserved {
		return true
	}
	e, err := l.reg.Get(lr.role)
	if err != nil {
		return false
	}
	if e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending {
		return true
	}
	switch e.Shown.State {
	case leadregistry.StateDesired, leadregistry.StateStopped, leadregistry.StateEnded:
		return false
	}
	return true
}

// running is the number of leads that count against max_running.
func (l *leadMode) running() int {
	l.capMu.Lock()
	defer l.capMu.Unlock()
	return l.runningLocked()
}

func (l *leadMode) runningLocked() int {
	n := 0
	for _, role := range l.cm.router.order {
		if l.counts(l.roles[role]) {
			n++
		}
	}
	return n
}

// reserve counts a start of the lead against max_running and reports whether
// it fits. A start that fits holds its place until [leadMode.release], which
// the caller calls once the lifecycle's Start has returned, whatever it
// returned: by then the registry shows the lead.
func (l *leadMode) reserve(lr *leadRole) bool {
	l.capMu.Lock()
	defer l.capMu.Unlock()
	if l.runningLocked() >= l.maxRunning {
		return false
	}
	lr.reserved = true
	return true
}

func (l *leadMode) release(lr *leadRole) {
	l.capMu.Lock()
	lr.reserved = false
	l.capMu.Unlock()
}
