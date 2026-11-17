package systemdmgr

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ataniq/internal/leadunit"
	"ataniq/internal/systemd"
)

// DefaultTimeout is the longest one tool call runs unless [Options] says less.
// It is also the most: internal/systemd cuts every call off after
// [systemd.MaxCall].
const DefaultTimeout = 5 * time.Second

// ErrUnsupportedSignal is returned by Signal for any signal but SIGTERM and
// SIGKILL. Nothing was sent.
var ErrUnsupportedSignal = errors.New("systemdmgr: signal not supported")

// Options configures [New].
type Options struct {
	// Runner runs the tools. Nil means an [ExecRunner] over
	// [DefaultToolDirs].
	Runner Runner
	// Timeout bounds each tool call, under the caller's context. Zero means
	// [DefaultTimeout]; a larger value is cut to it.
	Timeout time.Duration
}

// Manager is the [leadunit.Manager] of the systemd user manager. It holds no
// state between calls and is safe for concurrent use when its Runner is.
type Manager struct {
	run     Runner
	k       kernel
	timeout time.Duration
}

var _ leadunit.Manager = (*Manager)(nil)

// New returns a Manager. With no Runner in opts it resolves the tools once,
// from [DefaultToolDirs], and fails when one is missing.
func New(opts Options) (*Manager, error) {
	r := opts.Runner
	if r == nil {
		er, err := NewExecRunner(DefaultToolDirs)
		if err != nil {
			return nil, err
		}
		r = er
	}
	return newManager(r, hostKernel{}, opts.Timeout), nil
}

func newManager(r Runner, k kernel, timeout time.Duration) *Manager {
	if timeout <= 0 || timeout > DefaultTimeout {
		timeout = DefaultTimeout
	}
	return &Manager{run: r, k: k, timeout: timeout}
}

// call runs one tool under the per-call timeout.
func (m *Manager) call(ctx context.Context, argv []string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	return m.run.Run(cctx, argv)
}

// validUnitName is [leadunit.ValidateUnitName] and a refusal of a name that
// starts with "-". The name is the one caller-supplied word that reaches a
// command line besides the command's own arguments, and a leading dash would
// let a tool read it as an option wherever the "--" terminators are missed.
// The terminators are in place; this is the second guard, and it runs before
// any call.
func validUnitName(name string) error {
	if err := leadunit.ValidateUnitName(name); err != nil {
		return err
	}
	if name[0] == '-' {
		return fmt.Errorf("%w: unit name %q starts with a dash", leadunit.ErrInvalidSpec, name)
	}
	return nil
}

func lost(what string, cause error) error {
	return fmt.Errorf("%w: %s: %w", leadunit.ErrReplyLost, what, cause)
}

// Start implements [leadunit.Manager]: one StartTransientUnit call with the
// fixed properties, the description, the working directory, the environment
// and the command (environment expansion off), then one Inspect. It never
// retries.
//
// A context that is done before the call, an invalid spec, an unreachable
// manager ([leadunit.ErrUnavailable]) and a refusal because the name is taken
// ([leadunit.ErrUnitExists]) mean nothing was started. Any other failure,
// once the request may have been sent, wraps [leadunit.ErrReplyLost] and
// returns a zero State. Start does not return before the tool has ended, even
// when ctx ends first (bounded by the per-call deadline, see [ExecRunner.Run]),
// so the request is never sent after Start returned. What the tool had
// already written to the bus can still be processed by the manager after it
// returned: a caller that inspects right after a lost reply and finds the unit
// not loaded should wait a short settle time and inspect again before it
// treats that as final. After an accepted job the state of the unit is
// returned as it is, including Loaded false for a unit that already ended and
// was collected.
//
// The state is read once, right after the job was accepted, and the job
// completes later: with Type=exec the unit can still be "activating", and what
// systemd fills in at the exec (the invocation id, the control group, the main
// pid) can still be empty. A caller that needs them inspects again. Signal
// refuses a State without an invocation id, and Empty is false while the unit
// is still loaded, so a partial State is never acted on.
func (m *Manager) Start(ctx context.Context, spec leadunit.Spec) (leadunit.State, error) {
	if err := ctx.Err(); err != nil {
		return leadunit.State{}, err
	}
	if err := spec.Validate(); err != nil {
		return leadunit.State{}, err
	}
	if err := validUnitName(spec.Unit); err != nil {
		return leadunit.State{}, err
	}
	argv, err := startArgv(spec, m.timeout)
	if err != nil {
		return leadunit.State{}, err
	}
	out, err := m.call(ctx, argv)
	if err != nil {
		return leadunit.State{}, startFailure(spec.Unit, out, err)
	}
	if _, err := systemd.ParseJobReply(out); err != nil {
		return leadunit.State{}, lost("start reply is not a job: "+snippet(out), err)
	}
	st, err := m.Inspect(ctx, spec.Unit)
	if err != nil {
		return leadunit.State{}, lost("inspection after an accepted job", err)
	}
	return st, nil
}

// startFailure classifies a failed StartTransientUnit call. Only evidence that
// nothing was sent or that the bus refused the name is "nothing started";
// everything else is a lost reply.
func startFailure(unit, out string, err error) error {
	text := strings.TrimSpace(out)
	switch {
	case errors.Is(err, ErrNotLaunched):
		return fmt.Errorf("%w: %w", leadunit.ErrUnavailable, err)
	case strings.HasPrefix(text, "Failed to connect to bus"):
		return fmt.Errorf("%w: %s", leadunit.ErrUnavailable, snippet(text))
	case text == "Call failed: Unit "+unit+" was already loaded or has a fragment file." ||
		text == "Call failed: Unit "+unit+" already exists.":
		return leadunit.ErrUnitExists
	}
	return lost("start call failed: "+snippet(text), err)
}

// Inspect implements [leadunit.Manager] with one "systemctl --user show".
//
// LoadState=not-found (or an empty LoadState with no invocation and no
// control group) is the positive answer Loaded false, provided the unit is
// also inactive with no control group and no main process; a contradiction is
// an error. Any other LoadState but "loaded", a property that does not parse,
// a loaded unit whose control group cannot be read and a failed read of the
// boot identity are errors, never "not loaded". The description counts as
// ours only for a unit that systemd reports as transient. A failure to reach
// the tool is [leadunit.ErrUnavailable].
func (m *Manager) Inspect(ctx context.Context, unit string) (leadunit.State, error) {
	if err := ctx.Err(); err != nil {
		return leadunit.State{}, err
	}
	if err := validUnitName(unit); err != nil {
		return leadunit.State{}, err
	}
	out, err := m.call(ctx, showArgv(unit))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return leadunit.State{}, cerr
		}
		return leadunit.State{}, fmt.Errorf("%w: systemctl show: %w", leadunit.ErrUnavailable, err)
	}
	p, err := systemd.ParseProperties(out, showProperties, maxOutput)
	if err != nil {
		return leadunit.State{}, fmt.Errorf("systemdmgr: inspect %s: %w", unit, err)
	}
	st, err := stateFromProperties(unit, p)
	if err != nil {
		return leadunit.State{}, fmt.Errorf("systemdmgr: inspect %s: %w", unit, err)
	}
	if st.Loaded && st.ControlGroup != "" {
		b, found, err := m.k.Lookup(st.ControlGroup)
		if err != nil {
			return leadunit.State{}, fmt.Errorf("systemdmgr: inspect %s: control group unreadable: %w", unit, err)
		}
		if !found {
			return leadunit.State{}, fmt.Errorf("systemdmgr: inspect %s: control group of a loaded unit does not exist", unit)
		}
		st.ControlGroupID = groupID(b.Identity)
	}
	boot, err := m.k.BootID()
	if err != nil {
		return leadunit.State{}, fmt.Errorf("systemdmgr: inspect %s: boot identity: %w", unit, err)
	}
	st.BootID = boot
	return st, nil
}

// stateFromProperties builds the State of the properties, without the
// control group identity and the boot identity.
func stateFromProperties(unit string, p map[string]string) (leadunit.State, error) {
	if p["Id"] != unit {
		return leadunit.State{}, errors.New("answer is for another unit")
	}
	inv := p["InvocationID"]
	if inv != "" && !isHex32(inv) {
		return leadunit.State{}, errors.New("invalid invocation id")
	}
	cg := p["ControlGroup"]
	if cg != "" && (cg[0] != '/' || cg == "/" || strings.ContainsAny(cg, "\x00\n\r")) {
		return leadunit.State{}, errors.New("invalid control group")
	}
	pid, err := strconv.Atoi(p["MainPID"])
	if err != nil || pid < 0 {
		return leadunit.State{}, errors.New("invalid main pid")
	}
	transient := p["Transient"]
	if transient != "yes" && transient != "no" {
		return leadunit.State{}, errors.New("invalid Transient value")
	}
	active := leadunit.ParseActiveState(p["ActiveState"])
	st := leadunit.State{Unit: unit, Active: active}
	switch p["LoadState"] {
	case "not-found", "":
		if cg != "" || pid != 0 || p["ActiveState"] != "inactive" || (p["LoadState"] == "" && inv != "") {
			return leadunit.State{}, errors.New("unit reported as unknown but has a control group, a process, a state or an invocation")
		}
		st.Active = leadunit.ActiveUnknown
		return st, nil
	case "loaded":
	default:
		return leadunit.State{}, fmt.Errorf("unexpected LoadState %q", snippet(p["LoadState"]))
	}
	st.Loaded = true
	st.InvocationID = inv
	st.ControlGroup = cg
	st.MainPID = pid
	st.SetDescription(p["Description"])
	if transient != "yes" {
		// A unit file can carry any description; only a transient unit that
		// the daemon started is ours.
		st.Epoch, st.Intent, st.Ours = 0, "", false
	}
	return st, nil
}

func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// groupID renders a control group identity as the opaque ControlGroupID of a
// State.
func groupID(id systemd.FileIdentity) string {
	return fmt.Sprintf("%d:%d:%d", id.Device, id.Inode, id.UID)
}

// parseGroupID reads what [groupID] rendered.
func parseGroupID(s string) (systemd.FileIdentity, bool) {
	f := strings.Split(s, ":")
	if len(f) != 3 {
		return systemd.FileIdentity{}, false
	}
	dev, e1 := strconv.ParseUint(f[0], 10, 64)
	ino, e2 := strconv.ParseUint(f[1], 10, 64)
	uid, e3 := strconv.ParseUint(f[2], 10, 32)
	if e1 != nil || e2 != nil || e3 != nil {
		return systemd.FileIdentity{}, false
	}
	id := systemd.FileIdentity{Device: dev, Inode: ino, UID: uint32(uid)}
	if groupID(id) != s {
		return systemd.FileIdentity{}, false
	}
	return id, true
}

// Signal implements [leadunit.Manager]. Only SIGTERM and SIGKILL are sent
// ([ErrUnsupportedSignal] otherwise). It inspects the unit, returns
// [leadunit.ErrNotLoaded] when it is not loaded and
// [leadunit.ErrInvocationChanged] when its invocation is not st's (or st has
// none), and then runs "systemctl --user kill --kill-whom=all".
//
// The window. systemctl kill names the unit, not the invocation, so between
// the inspection and the kill the unit could end and another invocation start,
// and the signal would reach that one. The window is accepted: the unit name
// belongs to one role and only the one daemon starts units of that name, and
// the daemon starts a new invocation only after the previous one is proven
// gone and while it holds the role's lock; with Restart=no systemd starts no
// other. A signal that reaches a newer invocation of the same role's unit is
// the signal the daemon was about to send for that role anyway. The kill
// reports no success for a unit that is gone ([leadunit.ErrNotLoaded]).
func (m *Manager) Signal(ctx context.Context, st leadunit.State, sig syscall.Signal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validUnitName(st.Unit); err != nil {
		return err
	}
	var name string
	switch sig {
	case syscall.SIGTERM:
		name = "SIGTERM"
	case syscall.SIGKILL:
		name = "SIGKILL"
	default:
		return fmt.Errorf("%w: %d", ErrUnsupportedSignal, int(sig))
	}
	cur, err := m.Inspect(ctx, st.Unit)
	if err != nil {
		return err
	}
	if !cur.Loaded {
		return leadunit.ErrNotLoaded
	}
	if st.InvocationID == "" || cur.InvocationID == "" || cur.InvocationID != st.InvocationID {
		return leadunit.ErrInvocationChanged
	}
	if st.ControlGroupID != "" && cur.ControlGroupID != "" && st.ControlGroupID != cur.ControlGroupID {
		return leadunit.ErrControlGroupMismatch
	}
	out, err := m.call(ctx, killArgv(st.Unit, name))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if strings.Contains(out, "Unit "+st.Unit+" not loaded") {
			return leadunit.ErrNotLoaded
		}
		return fmt.Errorf("systemdmgr: kill %s: %s: %w", st.Unit, snippet(out), err)
	}
	return nil
}

// Empty implements [leadunit.Manager]. It is true only on positive proof, in
// exactly these cases, after one Inspect of the unit:
//
//  1. The unit is not loaded, or is loaded with another invocation and another
//     control group identity than st, and the control group st names (path and
//     identity) does not exist any more or exists with a different identity.
//  2. The control group st names exists with the same identity and
//     [systemd.Populated] says it has no process; for a loaded unit that
//     further needs the same invocation and a state of inactive, failed or
//     deactivating, because a unit that is active or activating can still
//     receive its first process. A loaded unit of the same invocation that is
//     inactive or failed, has no main process and reports no control group
//     (systemd clears it when the unit's group is released and keeps the unit
//     loaded until it is collected) is decided by the path and identity st
//     recorded, as in case 1 (gone or another identity: true) or case 2 (the
//     same identity: true only if it holds no process).
//  3. st has no invocation and no control group: the unit is not loaded and no
//     directory named like the unit exists below the control group of the user
//     manager (the control group of its root slice, "-.slice"), whatever slice
//     the unit was placed in. The search is bounded and an incomplete search
//     is an error, not a "no".
//
// Everything else is false, with an error when the cause is a failure or an
// inconsistency and without one when the group is plainly still there. A new
// invocation alone is never proof.
//
// True says that the control group of the unit is empty (or gone), not that
// every process the unit ever ran is gone: a process can move itself into
// another control group of the user's delegated subtree before its group is
// removed, and nothing here sees it.
func (m *Manager) Empty(ctx context.Context, st leadunit.State) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validUnitName(st.Unit); err != nil {
		return false, err
	}
	cur, err := m.Inspect(ctx, st.Unit)
	if err != nil {
		return false, err
	}
	if st.InvocationID == "" && st.ControlGroup == "" && st.ControlGroupID == "" {
		return m.noGroupEmpty(ctx, st.Unit, cur)
	}
	if st.ControlGroup == "" || st.ControlGroupID == "" {
		return false, errors.New("systemdmgr: state names an incomplete control group")
	}
	id, ok := parseGroupID(st.ControlGroupID)
	if !ok {
		return false, errors.New("systemdmgr: state has an unreadable control group identity")
	}
	want := systemd.Boundary{Path: st.ControlGroup, Identity: id}
	if cur.Loaded {
		if st.InvocationID == "" {
			return false, leadunit.ErrInvocationChanged
		}
		if cur.InvocationID == "" {
			return false, errors.New("systemdmgr: loaded unit has no invocation id")
		}
		if cur.InvocationID == st.InvocationID {
			if cur.ControlGroup == "" {
				// systemd clears ControlGroup when the unit has ended and its
				// group is released, while the unit stays loaded (a failed
				// unit until it is collected). That is not another identity:
				// decide by the group the State recorded, as in case 1, and
				// only for a unit that is plainly over (inactive or failed,
				// no main process). Anything else is a mismatch, as before.
				if (cur.Active == leadunit.ActiveInactive || cur.Active == leadunit.ActiveFailed) && cur.MainPID == 0 {
					return m.recordedGroupEmpty(st.ControlGroup, id, want)
				}
				return false, leadunit.ErrControlGroupMismatch
			}
			if cur.ControlGroup != st.ControlGroup || cur.ControlGroupID != st.ControlGroupID {
				return false, leadunit.ErrControlGroupMismatch
			}
			switch cur.Active {
			case leadunit.ActiveInactive, leadunit.ActiveFailed, leadunit.ActiveDeactivating:
				return m.populatedEmpty(want)
			}
			return false, nil
		}
		if cur.ControlGroupID == st.ControlGroupID {
			return false, errors.New("systemdmgr: another invocation reports the control group of the state")
		}
	}
	b, found, err := m.k.Lookup(st.ControlGroup)
	if err != nil {
		return false, err
	}
	if !found || b.Identity != id {
		return true, nil
	}
	if cur.Loaded {
		return false, nil
	}
	return m.populatedEmpty(want)
}

// recordedGroupEmpty decides by the control group that a State recorded, for a
// unit that is over but still loaded: true when that exact path no longer
// exists or has another identity, otherwise true only if the group of the same
// identity holds no process. A failure to read is false with the error.
func (m *Manager) recordedGroupEmpty(path string, id systemd.FileIdentity, want systemd.Boundary) (bool, error) {
	b, found, err := m.k.Lookup(path)
	if err != nil {
		return false, err
	}
	if !found || b.Identity != id {
		return true, nil
	}
	return m.populatedEmpty(want)
}

func (m *Manager) populatedEmpty(b systemd.Boundary) (bool, error) {
	pop, err := m.k.Populated(b)
	if err != nil {
		return false, err
	}
	return !pop, nil
}

// noGroupEmpty is case 3 of [Manager.Empty].
func (m *Manager) noGroupEmpty(ctx context.Context, unit string, cur leadunit.State) (bool, error) {
	if cur.Loaded {
		return false, nil
	}
	base, err := m.userManagerGroup(ctx)
	if err != nil {
		return false, err
	}
	paths, err := m.k.FindNamed(base, unit)
	if err != nil {
		return false, err
	}
	return len(paths) == 0, nil
}

// userManagerGroup returns the control group of the user manager, from the
// ControlGroup of its root slice, and checks that it exists.
func (m *Manager) userManagerGroup(ctx context.Context) (string, error) {
	out, err := m.call(ctx, userManagerGroupArgv())
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return "", cerr
		}
		return "", fmt.Errorf("%w: systemctl show: %w", leadunit.ErrUnavailable, err)
	}
	p, err := systemd.ParseProperties(out, "ControlGroup", maxOutput)
	if err != nil {
		return "", fmt.Errorf("systemdmgr: user manager control group: %w", err)
	}
	base := p["ControlGroup"]
	if base == "" || base == "/" || base[0] != '/' || base[len(base)-1] == '/' || strings.Contains(base, "//") {
		return "", errors.New("systemdmgr: cannot derive the control group of the user manager")
	}
	if _, found, err := m.k.Lookup(base); err != nil {
		return "", fmt.Errorf("systemdmgr: control group of the user manager: %w", err)
	} else if !found {
		return "", errors.New("systemdmgr: control group of the user manager does not exist")
	}
	return base, nil
}

// Populated reports whether the control group that st names holds a process
// now. It is a reading for a qualification run, not a proof of emptiness: it
// answers true or false for the instant of the read, only for the control
// group of that path and identity, and says nothing of the unit or its
// invocation. It fails when st names no control group, when the identity is
// unreadable or no longer that of st, or when the group cannot be read.
func (m *Manager) Populated(ctx context.Context, st leadunit.State) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if st.ControlGroup == "" || st.ControlGroupID == "" {
		return false, errors.New("systemdmgr: state names no control group")
	}
	id, ok := parseGroupID(st.ControlGroupID)
	if !ok {
		return false, errors.New("systemdmgr: state has an unreadable control group identity")
	}
	return m.k.Populated(systemd.Boundary{Path: st.ControlGroup, Identity: id})
}
