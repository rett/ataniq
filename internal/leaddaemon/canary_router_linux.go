//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// errNotALead is what the router answers for a role that is none of its
// instances'.
var errNotALead = errors.New("the role is not a lead of this daemon")

// errNoHolder is what the router answers for a role that has no holder command:
// a lead of scratch mode that is not provisioned. A holder is never started
// without the digest of the lead's profile.
var errNoHolder = errors.New("the role has no holder command")

// leadInstance is one role's share of the daemon: the config derived for it,
// the lifecycle's preflight and holder command for it, its evidence file and
// the epochs that file shows as spent. The operations and the state they change
// (the admission, the in-flight flag, the spent epochs and the lock around
// them) are in the role's [canaryExt], which the router builds once the daemon
// listens; every lock is the role's own.
type leadInstance struct {
	cfg       leadcanary.Config
	preflight leadlifecycle.Preflight
	// holder is the lifecycle's holder command for the role. The canary's is
	// set when the daemon is set up and never changes. A lead of scratch mode
	// has none until its record was read or its define finished, and that
	// command is replaced when it is defined, always under the role's operation
	// lock ([leadMode]); the accessors below make the read of the lifecycle
	// safe whatever the lock is.
	holder leadlifecycle.HolderCommand
	hmu    sync.RWMutex
	ev     *leadcanary.Evidence
	// spent is the epochs whose message the evidence shows as sent, read when
	// the evidence is opened.
	spent map[uint64]bool
	ext   *canaryExt
}

// holderCommand returns the role's holder command, nil when it has none.
func (in *leadInstance) holderCommand() leadlifecycle.HolderCommand {
	in.hmu.RLock()
	defer in.hmu.RUnlock()
	return in.holder
}

// setHolderCommand replaces the role's holder command.
func (in *leadInstance) setHolderCommand(h leadlifecycle.HolderCommand) {
	in.hmu.Lock()
	in.holder = h
	in.hmu.Unlock()
}

// leadRouter is the set of roles a daemon runs, fixed when the daemon starts.
// The lifecycle's three seams, which all take the role (the preflight, the
// holder command and the limits), are answered by looking the role up, and a
// role that is not in the set is refused by each of them. Canary mode is the
// router with one role. There is no lock: the map is written before the daemon
// serves and only read afterwards.
type leadRouter struct {
	byRole map[leadrole.Role]*leadInstance
	// order is the roles in the order they were added; every loop over the
	// instances (open, announce, close) follows it.
	order []leadrole.Role
	// notLead is the fixed text of the refusal of a call that names a role of
	// no instance.
	notLead string
}

var (
	_ leadlifecycle.Preflight     = (*leadRouter)(nil)
	_ leadlifecycle.HolderCommand = (*leadRouter)(nil)
)

// newLeadRouter returns an empty router whose refusal of an unknown role says
// notLead.
func newLeadRouter(notLead string) *leadRouter {
	return &leadRouter{byRole: make(map[leadrole.Role]*leadInstance), notLead: notLead}
}

// add puts an instance in the router, under the role of its config. A role that
// is zero or already there is an error.
func (r *leadRouter) add(in *leadInstance) error {
	role := in.cfg.Role
	if role.IsZero() {
		return errors.New("an instance without a role")
	}
	if _, dup := r.byRole[role]; dup {
		return errors.New("two instances have the same role")
	}
	r.byRole[role] = in
	r.order = append(r.order, role)
	return nil
}

// Check is the lifecycle's preflight: the role's own preflight decides, and a
// role that is not in the router fails with [leadcanary.ErrPreflightRefused].
func (r *leadRouter) Check(ctx context.Context, role leadrole.Role, e leadregistry.Entry) error {
	in, ok := r.byRole[role]
	if !ok {
		return fmt.Errorf("%w: %w", leadcanary.ErrPreflightRefused, errNotALead)
	}
	return in.preflight.Check(ctx, role, e)
}

// Build is the lifecycle's holder command: the role's own builds it, and a role
// that is not in the router, or has no holder command yet, has none.
func (r *leadRouter) Build(role leadrole.Role, e leadregistry.Entry, intent leadregistry.Intent) ([]string, string, []string, error) {
	in, ok := r.byRole[role]
	if !ok {
		return nil, "", nil, errNotALead
	}
	h := in.holderCommand()
	if h == nil {
		return nil, "", nil, errNoHolder
	}
	return h.Build(role, e, intent)
}

// Limits is the lifecycle's limits seam: the three limits of the role's config,
// and nil for a role that is not in the router (the preflight has refused it
// before).
func (r *leadRouter) Limits(role leadrole.Role) *leadunit.Limits {
	in, ok := r.byRole[role]
	if !ok {
		return nil
	}
	l := in.cfg.Limits
	return &leadunit.Limits{MemoryMax: l.MemoryMaxBytes, SwapMax: l.SwapMaxBytes, TasksMax: l.TasksMax}
}

// ext returns the extension of the role the text names. The text is parsed and
// must read back as itself: a spelling the role grammar would turn into
// another role is no role of the router.
func (r *leadRouter) ext(text string) (*canaryExt, bool) {
	role, err := leadrole.Parse(text)
	if err != nil || role.String() != text {
		return nil, false
	}
	in, ok := r.byRole[role]
	if !ok || in.ext == nil {
		return nil, false
	}
	return in.ext, true
}

// routerExt is the clientapi extension the router registers: the five
// operations, each carried out by the extension of the role the params name.
type routerExt struct {
	router *leadRouter
}

var _ clientapi.Extension = (*routerExt)(nil)

// Operations lists the five operations.
func (x *routerExt) Operations() []string { return CanaryOperations() }

// Mutating is true: define, start, ticket, inject and stop change things.
func (x *routerExt) Mutating() bool { return true }

// Handle decodes the strict params of op and carries it out on the extension of
// the role they name; a role of no instance is refused with the router's text,
// after the params were found well formed and before anything else.
func (x *routerExt) Handle(ctx context.Context, op string, params json.RawMessage) (any, *clientapi.ErrorBody) {
	role, call, f := decodeOp(op, params)
	if f != nil {
		return nil, f
	}
	ext, ok := x.router.ext(role)
	if !ok {
		return nil, refused(x.router.notLead)
	}
	return call(ctx, ext)
}
