//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// The operations of the canary extension. Each takes strict params that carry
// "role", which must be a role of the daemon's router: the config's in canary
// mode, which has the one.
const (
	OpCanaryDefine = "canary.define"
	OpCanaryStart  = "canary.start"
	OpCanaryTicket = "canary.ticket"
	OpCanaryInject = "canary.inject"
	OpCanaryStop   = "canary.stop"
)

// CanaryOperations returns the operations of the extension, in the order hello
// lists them, in a fresh slice.
func CanaryOperations() []string {
	return []string{OpCanaryDefine, OpCanaryStart, OpCanaryTicket, OpCanaryInject, OpCanaryStop}
}

// The error codes the extension adds to the client protocol's.
const (
	// CodeRefused: the daemon refused the operation and changed nothing that
	// matters (the message did not pass the admission, a second message in the
	// epoch, injection that is off in the config, a role other than the
	// config's, the registry or the lifecycle refused). One answer is the
	// exception: when the registry refuses with a stale epoch or a state it
	// does not change from, the refusal may follow a launch or a stop that
	// was already carried out, and the text says to check the role's status.
	CodeRefused clientapi.ErrorCode = "refused"
	// CodeUnavailable: the operation could not be carried out: nothing reached
	// the holder (the control socket could not be dialled or the request frame
	// could not be written), the holder refused an inject with disabled,
	// wrong-epoch, busy or bad-request, or the unit manager could not do it.
	CodeUnavailable clientapi.ErrorCode = "unavailable"
)

// The prefix of the command ids the daemon gives the holder in canary mode, and
// the reason code of an operation whose epoch is not the role's.
const (
	canaryIDPrefix = "canary-"
	codeStaleEpoch = "stale-epoch"
)

// The profile the registry shows as requested for the canary role. The
// registry wants every field, the config carries the runtime, its version and
// the permission mode the fence expects, and nothing else of a profile.
const (
	requestedPlaceholder = "canary"
)

// Params of the operations (the JSON member names are the wire's).
type (
	// CanaryParams are the params of define, start and stop.
	CanaryParams struct {
		Role string `json:"role"`
	}
	// CanaryTicketParams are the params of ticket; AttachRole is "writer" or
	// "viewer".
	CanaryTicketParams struct {
		Role       string `json:"role"`
		AttachRole string `json:"attach_role"`
	}
	// CanaryInjectParams are the params of inject.
	CanaryInjectParams struct {
		Role      string `json:"role"`
		MessageID string `json:"message_id"`
		Body      string `json:"body"`
	}
)

// Results of the operations.
type (
	// CanaryDefineResult is the result of define.
	CanaryDefineResult struct {
		Role  string `json:"role"`
		State string `json:"state"`
	}
	// CanaryStartResult is the result of start.
	CanaryStartResult struct {
		Role   string `json:"role"`
		State  string `json:"state"`
		Epoch  uint64 `json:"epoch"`
		Unit   string `json:"unit"`
		Waiver string `json:"waiver"`
	}
	// CanaryTicketResult is the result of ticket. The ticket itself is never in
	// it: it is in the ticket file, which the console reads, and nowhere else.
	CanaryTicketResult struct {
		Role       string `json:"role"`
		Epoch      uint64 `json:"epoch"`
		TicketFile string `json:"ticket_file"`
		Folder     string `json:"folder"`
	}
	// CanaryInjectResult is the result of inject.
	CanaryInjectResult struct {
		Role      string            `json:"role"`
		Epoch     uint64            `json:"epoch"`
		MessageID string            `json:"message_id"`
		Result    wire.InjectResult `json:"result"`
	}
	// CanaryStopResult is the result of stop.
	CanaryStopResult struct {
		Role  string `json:"role"`
		State string `json:"state"`
		Proof string `json:"proof"`
	}
)

// Fixed texts of the errors. None includes anything a peer sent.
const (
	msgBadParams    = "the parameters are not valid for this operation"
	msgNotCanary    = "the role is not the canary role"
	msgNotRunning   = "the role is not running"
	msgSecond       = "a message was already sent in this epoch"
	msgInFlight     = "a message is in flight"
	msgInjectOff    = "injection is off in the canary config"
	msgUnreachable  = "the holder could not be reached"
	msgHolderNo     = "the holder refused the message"
	msgTicketNo     = "the holder refused the ticket"
	msgTicketLost   = "the holder did not answer the ticket request"
	msgTicketFile   = "the holder took the ticket but the ticket file could not be written"
	msgFolder       = "the role's runtime folder is missing or not private"
	msgStateFolder  = "the holder's state folder is missing or not private"
	msgRoleFolder   = "the folder of the role's lock is missing or not private"
	msgRecordLeft   = "the lead's record folder from an earlier recording is still there; move or delete it, or set record_screens to false"
	msgRegistryNo   = "the registry refused"
	msgForeignUnit  = "a unit with the role's name is not the role's recorded unit"
	msgFailed       = "the operation failed; see the daemon's log"
	msgStaleEpoch   = "the epoch is not the role's current epoch"
	msgStopping     = "the daemon is stopping"
	attachRoleWrite = string(wire.RoleWriter)
	attachRoleView  = string(wire.RoleViewer)
)

// msgRegistryCheck is the fixed text for a refusal of the registry that may
// follow something the daemon already did.
const msgRegistryCheck = "the registry refused; check the role's status before you retry"

// canaryExt is one role's side of the extension: the five operations for one
// lead, with that lead's evidence handle, admission, spent epochs and in-flight
// flag. Every lock it holds is the role's own, so one role's inject never waits
// on another's. The router (canary_router_linux.go) owns one per role and
// reaches it by the role of a call's params.
type canaryExt struct {
	cfg       leadcanary.Config
	role      leadrole.Role
	holderDir string
	ctl       *leadlifecycle.Controller
	reg       *leadregistry.Registry
	ev        *leadcanary.Evidence
	adm       *leadcanary.Admission
	dial      time.Duration
	wait      time.Duration
	logf      func(format string, a ...any)
	profile   leadregistry.Requested
	// idPrefix begins the command ids the daemon gives the holder: "canary-"
	// for the canary, "lead-" for a lead of scratch mode (see [leadMode]).
	idPrefix string
	// scratch is true for a lead of scratch mode and false for the canary. A
	// lead's message that the holder answered as not delivered (nothing
	// written, see [leadcanary.ReleasesMessage]) is not spent, so the lead may
	// send another in the epoch; the canary's is spent whatever the answer.
	scratch bool
	// build is the check of the holder's build before a launch and before an
	// attach (launchSkew, attachSkew).
	build *buildid.Checker

	mu sync.Mutex
	// spent holds the epochs whose one message is spent. It starts from the
	// inject-sent lines of the evidence (see spentEpochs and
	// scratchSpentEpochs), so that a restart of the daemon inside an epoch
	// admits no second message that the evidence does not free.
	spent    map[uint64]bool
	inFlight bool   // a message is being sent
	commands uint64 // counter of the command ids
}

// newCanaryExt returns the extension of the canary's one role: the first
// instance of the mode's router (the canary is the router with one role).
func newCanaryExt(m *canaryMode, ctl *leadlifecycle.Controller, reg *leadregistry.Registry) *canaryExt {
	return m.newExt(m.router.byRole[m.router.order[0]], ctl, reg)
}

// newExt returns the extension of one instance of the router, which has read
// back its evidence by now.
func (m *canaryMode) newExt(in *leadInstance, ctl *leadlifecycle.Controller, reg *leadregistry.Registry) *canaryExt {
	cfg := in.cfg
	// Validated at setup: the names, the admission and the runtime folder
	// cannot fail for this config.
	holderDir, _ := cfg.HolderRuntimeDir()
	adm, _ := leadcanary.NewAdmissionFor(cfg.Reserved, cfg.Role)
	spent := make(map[uint64]bool, len(in.spent))
	for epoch := range in.spent {
		spent[epoch] = true
	}
	return &canaryExt{
		cfg: cfg, role: cfg.Role, holderDir: holderDir, ctl: ctl, reg: reg, ev: in.ev, adm: adm,
		dial: m.dialFor, wait: m.injectFor, logf: m.d.logf, spent: spent, idPrefix: m.idPrefix, scratch: m.lead != nil,
		build: m.build,
		profile: leadregistry.Requested{
			// The registry's runtime enum is checked by the registry; the config
			// names the key. The model, the effort and the access mode are not
			// in the config.
			Provider:       leadregistry.Provider(cfg.Agent.Runtime),
			Version:        cfg.Agent.RuntimeVersion,
			Model:          requestedPlaceholder,
			Effort:         requestedPlaceholder,
			PermissionMode: cfg.Agent.FooterMode,
			AccessMode:     requestedPlaceholder,
		},
	}
}

// opCall is an operation whose params are decoded and whose role is known, ready
// to run against that role's extension.
type opCall func(ctx context.Context, x *canaryExt) (any, *clientapi.ErrorBody)

// decodeOp decodes the strict params of op and returns the role they name and
// the call to make on that role's extension. The role is not checked here: the
// router looks it up. Errors are the answers for a malformed request.
func decodeOp(op string, params json.RawMessage) (string, opCall, *clientapi.ErrorBody) {
	switch op {
	case OpCanaryDefine, OpCanaryStart, OpCanaryStop:
		var p CanaryParams
		if f := decodeStrict(params, &p, "role"); f != nil {
			return "", nil, f
		}
		switch op {
		case OpCanaryDefine:
			return p.Role, func(_ context.Context, x *canaryExt) (any, *clientapi.ErrorBody) { return x.define() }, nil
		case OpCanaryStart:
			return p.Role, func(ctx context.Context, x *canaryExt) (any, *clientapi.ErrorBody) { return x.start(ctx) }, nil
		}
		return p.Role, func(ctx context.Context, x *canaryExt) (any, *clientapi.ErrorBody) { return x.stop(ctx) }, nil
	case OpCanaryTicket:
		var p CanaryTicketParams
		if f := decodeStrict(params, &p, "role", "attach_role"); f != nil {
			return "", nil, f
		}
		return p.Role, func(ctx context.Context, x *canaryExt) (any, *clientapi.ErrorBody) {
			// After the role: a malformed attach role of a role that is not
			// the daemon's is still the refusal of the role.
			if p.AttachRole != attachRoleWrite && p.AttachRole != attachRoleView {
				return nil, &clientapi.ErrorBody{Code: clientapi.CodeInvalidRequest, Message: msgBadParams}
			}
			return x.ticket(ctx, wire.Role(p.AttachRole))
		}, nil
	case OpCanaryInject:
		var p CanaryInjectParams
		if f := decodeStrict(params, &p, "role", "message_id", "body"); f != nil {
			return "", nil, f
		}
		return p.Role, func(ctx context.Context, x *canaryExt) (any, *clientapi.ErrorBody) { return x.inject(ctx, p) }, nil
	}
	return "", nil, &clientapi.ErrorBody{Code: clientapi.CodeUnsupportedOperation, Message: msgBadParams}
}

// decodeStrict decodes params into v: one JSON object whose keys are exactly
// spelled members of keys (every one is required to be a known member; case
// variants and unknown members are refused), with nothing after it.
func decodeStrict(params json.RawMessage, v any, keys ...string) *clientapi.ErrorBody {
	bad := &clientapi.ErrorBody{Code: clientapi.CodeInvalidRequest, Message: msgBadParams}
	if len(params) == 0 {
		return bad
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(params, &members); err != nil {
		return bad
	}
	for k := range members {
		known := false
		for _, want := range keys {
			known = known || k == want
		}
		if !known {
			return bad
		}
	}
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return bad
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return bad
	}
	return nil
}

func refused(msg string) *clientapi.ErrorBody {
	return &clientapi.ErrorBody{Code: CodeRefused, Message: msg}
}

func unavailable(msg string) *clientapi.ErrorBody {
	return &clientapi.ErrorBody{Code: CodeUnavailable, Message: msg}
}

// record appends an evidence line. The operation it describes has happened by
// then, so a failure to write is the daemon's log's to carry.
func (x *canaryExt) record(en leadcanary.Entry) { x.recorded(en) }

// recorded is [canaryExt.record] that says whether the line is in the evidence.
func (x *canaryExt) recorded(en leadcanary.Entry) bool {
	err := x.ev.Append(en)
	if err != nil {
		x.logf("canary evidence: %s: %s", en.Event, SafeText(err.Error()))
	}
	return err == nil
}

// failure maps the error of a registry or lifecycle call to a fixed answer and
// logs the error itself. A refusal of the registry or of the lifecycle is the
// error refused; what could not be carried out or is not known is unavailable.
// An error may match more than one case (the lifecycle joins what it found
// with what the registry then said), so the order is: the lifecycle's own
// refusals, then what the lifecycle could not do or does not know, then a
// refusal of the registry with nothing more specific about it. Of those, a
// stale epoch and a refused state change may come after the lifecycle acted,
// and their answer says to check the role's status and not that nothing
// changed.
func (x *canaryExt) failure(op string, err error) *clientapi.ErrorBody {
	x.logf("canary %s: %s", op, SafeText(err.Error()))
	switch {
	// An error that says the outcome is not known comes first: the daemon that
	// is told to stop while the manager has the start request returns an error
	// that wraps the lifecycle's and the context's, and the answer must not say
	// that nothing was started.
	case errors.Is(err, leadlifecycle.ErrLaunchUncertain):
		return unavailable("the start's outcome is not known; the role is shown unknown")
	case errors.Is(err, leadlifecycle.ErrStopUnproven):
		return unavailable("the stop is not proven; the role is shown unknown")
	case errors.Is(err, context.Canceled):
		return unavailable(msgStopping)
	case errors.Is(err, leadregistry.ErrNoRole):
		return refused("the role is not defined")
	case errors.Is(err, leadlifecycle.ErrNotManaged):
		return refused("the role is not run by this daemon")
	case errors.Is(err, leadlifecycle.ErrPreflight):
		return refused("the preflight refused the start")
	case errors.Is(err, leadlifecycle.ErrForeignUnit):
		// Before ErrUnitLoaded, which the lifecycle wraps beside it.
		return refused(msgForeignUnit)
	case errors.Is(err, leadlifecycle.ErrUnitLoaded):
		return refused("a unit with the role's name is loaded")
	case errors.Is(err, leadunit.ErrInvalidSpec):
		return refused("the unit's limits are outside the bounds")
	case errors.Is(err, leadregistry.ErrLaunchProfile):
		// Before ErrLaunchRefused, which it matches too.
		return refused(msgProfileNotLaunchable)
	case errors.Is(err, leadregistry.ErrLaunchRefused):
		return refused("the registry does not admit a launch in the role's state")
	case errors.Is(err, leadregistry.ErrInvalid):
		return refused(msgRegistryNo)
	case errors.Is(err, leadlifecycle.ErrNoProof):
		return unavailable("the previous unit is not proven gone")
	case errors.Is(err, leadlifecycle.ErrStartFailed):
		return unavailable("the start failed and nothing was started")
	case errors.Is(err, leadlifecycle.ErrIntentPending):
		return unavailable("the launch intent is not settled yet")
	case errors.Is(err, leadregistry.ErrEpochExhausted):
		// Only a launch that has not begun meets it: nothing was done.
		return refused(msgRegistryNo)
	case errors.Is(err, leadregistry.ErrStaleEpoch),
		errors.Is(err, leadregistry.ErrStateTransition):
		// The lifecycle also returns these bare after it acted (a launch that
		// is to be recorded, a stop that is to be shown proven), so the text
		// does not say that nothing changed.
		return refused(msgRegistryCheck)
	}
	return unavailable(msgFailed)
}

// runningEntry returns the role's record and whether it is running: launched
// (or adopted) in its current epoch and shown starting, ready or degraded.
func (x *canaryExt) runningEntry() (leadregistry.Entry, bool) {
	e, err := x.reg.Get(x.role)
	if err != nil {
		return leadregistry.Entry{}, false
	}
	in := e.Intent
	if e.Epoch == 0 || in == nil || in.Epoch != e.Epoch ||
		(in.Outcome != leadregistry.OutcomeLaunched && in.Outcome != leadregistry.OutcomeAdopted) {
		return e, false
	}
	switch e.Shown.State {
	case leadregistry.StateStarting, leadregistry.StateReady, leadregistry.StateDegraded:
		return e, true
	}
	return e, false
}

// commandID returns a command id for the holder's event log.
func (x *canaryExt) commandID(op string, epoch uint64) string {
	x.mu.Lock()
	x.commands++
	n := x.commands
	x.mu.Unlock()
	prefix := x.idPrefix
	if prefix == "" {
		prefix = canaryIDPrefix
	}
	return prefix + op + "-" + strconv.FormatUint(epoch, 10) + "-" + strconv.FormatUint(n, 10)
}

// define defines the role, as the lead of the daemon (run by ataniq), and
// answers its shown state.
func (x *canaryExt) define() (any, *clientapi.ErrorBody) {
	e, err := x.reg.Define(x.role, leadregistry.ManagerAtaniq, x.profile)
	if err != nil {
		return nil, x.failure("define", err)
	}
	x.record(leadcanary.Entry{Event: leadcanary.EventDefine})
	return CanaryDefineResult{Role: x.role.String(), State: string(e.Shown.State)}, nil
}

// start starts the role through the lifecycle.
func (x *canaryExt) start(ctx context.Context) (any, *clientapi.ErrorBody) {
	// Provisioning makes the folders the holder's unit needs before it has
	// written anything: the runtime folder, the holder's state folder (the
	// unit's working directory) and the folder of the role lock, where the
	// holder makes the lock file but not the folder. The daemon checks them
	// before it changes anything: without this, a folder that was never made
	// shows only as a unit that ends at once, with the reason in the journal.
	for _, f := range x.provisionedFolders() {
		fd, err := f.open()
		if err != nil {
			x.logf("canary start: %s: %s", f.what, SafeText(err.Error()))
			return nil, refused(f.msg)
		}
		unix.Close(fd)
	}
	if x.recordFolderLeft() {
		x.logf("canary start: %s", logRecordLeft)
		return nil, refused(msgRecordLeft)
	}
	// The holder the unit would run is the daemon's own build, or nothing
	// changes.
	if f := x.launchSkew("canary start"); f != nil {
		return nil, f.body()
	}
	e, err := x.ctl.Start(ctx, x.role)
	if err != nil {
		return nil, x.failure("start", err)
	}
	unit := ""
	if e.Intent != nil {
		unit = e.Intent.UnitName
	}
	x.record(leadcanary.Entry{Event: leadcanary.EventStart, Epoch: e.Epoch})
	return CanaryStartResult{
		Role: x.role.String(), State: string(e.Shown.State), Epoch: e.Epoch, Unit: unit,
		Waiver: canaryroot.WaiverText,
	}, nil
}

// provisionedFolder is a folder that provisioning makes and the daemon checks
// before it starts the role: what it is called in the log and the fixed text of
// the refusal. A folder whose path cannot be worked out has err set instead of
// a path, and is refused like a missing one: the check is never skipped.
type provisionedFolder struct {
	path, what, msg string
	err             error
}

// open opens the folder as one that is private to the caller, or answers why
// it is not (or why its path is not known).
func (f provisionedFolder) open() (int, error) {
	if f.err != nil {
		return -1, f.err
	}
	return openPrivateDir(f.path)
}

// provisionedFolders lists the folders canary.start requires, in the order it
// looks at them: the holder's runtime folder, the holder's state folder and the
// folder of the role lock, all from the config. The config is validated when
// the daemon starts, so a path that cannot be worked out is not expected; if it
// happens, the start is refused for that folder, not made without the check.
func (x *canaryExt) provisionedFolders() []provisionedFolder {
	folders := []provisionedFolder{{path: x.holderDir, what: "the runtime folder", msg: msgFolder}}
	state := provisionedFolder{what: "the holder's state folder", msg: msgStateFolder}
	if l, err := x.cfg.Layout(); err != nil {
		state.err = err
	} else {
		state.path = l.Holder()
	}
	folders = append(folders, state)
	lock := provisionedFolder{what: "the folder of the role lock", msg: msgRoleFolder}
	if p := x.role.LockPath(x.cfg.StateRoot); p == "" {
		lock.err = errors.New("the role has no lock path")
	} else {
		lock.path = path.Dir(p)
	}
	return append(folders, lock)
}

// logRecordLeft is the daemon's log line, after the operation's prefix, for a
// start refused by recordFolderLeft. It names no path.
const logRecordLeft = "the lead records its screens and its record folder is still there"

// recordFolderLeft says a lead that records its screens (RecordScreens) has
// something at the name of its record folder in the runtime folder: an earlier
// recording, or anything else (a link included, not followed). Its holder
// would refuse --record-dir and end at once, with the reason only in the
// unit's journal, so the start is refused here first, before anything changes.
// The holder keeps its own check. A folder that cannot be looked at for
// another reason than its absence counts as there.
func (x *canaryExt) recordFolderLeft() bool {
	if !x.cfg.RecordScreens {
		return false
	}
	var st unix.Stat_t
	return !errors.Is(unix.Lstat(path.Join(x.holderDir, leadcanary.RecordDirName), &st), unix.ENOENT)
}

// stop stops the role through the lifecycle and answers the proof text.
func (x *canaryExt) stop(ctx context.Context) (any, *clientapi.ErrorBody) {
	e, err := x.ctl.Stop(ctx, x.role)
	if err != nil {
		return nil, x.failure("stop", err)
	}
	x.record(leadcanary.Entry{Event: leadcanary.EventStop, Epoch: e.Epoch})
	return CanaryStopResult{Role: x.role.String(), State: string(e.Shown.State), Proof: e.Shown.Proof}, nil
}

// ticket mints a ticket, registers it with the holder and writes the ticket
// file for the console. The ticket is in nothing else: not in the answer, not
// in the evidence, not in the log. The canary's ticket is for the role's
// current epoch.
func (x *canaryExt) ticket(ctx context.Context, attach wire.Role) (any, *clientapi.ErrorBody) {
	res, f := x.ticketFor(ctx, attach, 0)
	if f != nil {
		return nil, f.body()
	}
	return res, nil
}

// launchSkew checks, before a launch, the build of the holder executable the
// config names against the build the daemon expects (buildid). It returns nil
// for the expected build, and otherwise the refusal of [skewRefusal].
func (x *canaryExt) launchSkew(op string) *opFail {
	return x.skewRefusal(op, x.build.BeforeLaunch(x.cfg.Holder.Executable))
}

// attachSkew checks, before anything is asked of the running holder of the
// epoch, the build it recorded in holder.json in its state folder against the
// build the daemon expects (buildid). A state file of another epoch says
// nothing of the holder of this one (most often that holder has not written
// its own yet) and is refused with stale-epoch, before its build is looked
// at. It returns nil for a file of the epoch that records the expected build,
// and otherwise the refusal of [skewRefusal].
func (x *canaryExt) attachSkew(op string, epoch uint64) *opFail {
	layout, err := x.cfg.Layout()
	if err != nil {
		// Validated at setup; a layout that cannot be worked out is refused
		// like a record that cannot be read.
		return x.skewRefusal(op, &buildid.SkewError{Expected: x.build.Expected, Why: buildid.WhyUnreadable, Cause: err})
	}
	return x.skewRefusal(op, x.build.BeforeAttach(layout.Holder(), epoch))
}

// skewRefusal is nil for a nil err, and otherwise the refusal of a holder
// check: the code holder-version-skew, with a message that says what was
// found and names both builds (a holder that records no build, an older
// holder, is refused as such), or for a state file of another epoch the code
// stale-epoch, with a message that names both epochs. The cause of a read
// that failed goes to the log.
func (x *canaryExt) skewRefusal(op string, err error) *opFail {
	if err == nil {
		return nil
	}
	var se *buildid.SkewError
	if errors.As(err, &se) && se.Cause != nil {
		x.logf("%s: %s (%s)", op, SafeText(err.Error()), SafeText(se.Cause.Error()))
	} else {
		x.logf("%s: %s", op, SafeText(err.Error()))
	}
	if errors.Is(err, buildid.ErrOtherEpoch) {
		return refusal(codeStaleEpoch, SafeText(err.Error()))
	}
	return refusal(codeHolderVersionSkew, SafeText(err.Error()))
}

// opFail is why an operation of the extension did not give a result: whether it
// was refused (nothing was done) or could not be carried out, the reason code
// and the fixed text. The canary's operations answer it as an error; the lead
// operations of scratch mode keep the code and the text in a receipt.
type opFail struct {
	refused bool
	code    string
	msg     string
}

func refusal(code, msg string) *opFail { return &opFail{refused: true, code: code, msg: msg} }

func failure(code, msg string) *opFail { return &opFail{code: code, msg: msg} }

// body is the answer of an error.
func (f *opFail) body() *clientapi.ErrorBody {
	if f.refused {
		return refused(f.msg)
	}
	return unavailable(f.msg)
}

// ticketFor is [canaryExt.ticket] for an epoch the sender expects. want is the
// epoch the sender saw, and 0 means the role's current one (the canary's). A
// want that is not the role's current epoch is refused with the code
// stale-epoch before anything reaches the holder, and the request the holder
// gets carries the epoch that was checked, so that the holder's own check of
// the epoch is the last guard against a role that was stopped and started in
// between.
func (x *canaryExt) ticketFor(ctx context.Context, attach wire.Role, want uint64) (CanaryTicketResult, *opFail) {
	e, running := x.runningEntry()
	if !running {
		return CanaryTicketResult{}, refusal("not-running", msgNotRunning)
	}
	if want != 0 && want != e.Epoch {
		return CanaryTicketResult{}, refusal(codeStaleEpoch, msgStaleEpoch)
	}
	// The running holder recorded the daemon's own build for the epoch, or no
	// ticket is minted and the holder is asked nothing.
	if f := x.attachSkew("ticket", e.Epoch); f != nil {
		return CanaryTicketResult{}, f
	}
	ticket, err := wire.NewTicket()
	if err != nil {
		x.logf("canary ticket: cannot mint a ticket: %s", SafeText(err.Error()))
		return CanaryTicketResult{}, failure("ticket-failed", msgFailed)
	}
	resp, wrote, err := callHolder(ctx, x.holderDir, wire.Request{
		Op: wire.OpTicket, Epoch: e.Epoch, Role: attach, Ticket: ticket,
		CommandID: x.commandID("ticket", e.Epoch),
	}, x.dial, x.dial)
	switch {
	case err != nil && !wrote:
		x.logf("canary ticket: %s", SafeText(err.Error()))
		return CanaryTicketResult{}, failure("holder-unreachable", msgUnreachable)
	case err != nil:
		x.logf("canary ticket: no usable answer: %s", SafeText(err.Error()))
		return CanaryTicketResult{}, failure("ticket-lost", msgTicketLost)
	case !resp.OK:
		x.logf("canary ticket: the holder refused: %s", SafeText(resp.Code))
		if resp.Code == wire.CodeWrongEpoch {
			return CanaryTicketResult{}, refusal(codeStaleEpoch, msgTicketNo)
		}
		return CanaryTicketResult{}, refusal("ticket-refused", msgTicketNo)
	}
	file, err := writeTicketFile(x.holderDir, ticket)
	if err != nil {
		x.logf("canary ticket: the ticket file: %s", SafeText(err.Error()))
		return CanaryTicketResult{}, failure("ticket-file", msgTicketFile)
	}
	x.record(leadcanary.Entry{Event: leadcanary.EventTicket, Epoch: e.Epoch, Detail: "attach-role " + string(attach)})
	return CanaryTicketResult{Role: x.role.String(), Epoch: e.Epoch, TicketFile: file, Folder: x.holderDir}, nil
}

// inject sends the one message of the epoch.
//
// With "inject": false in the config the operation is refused at once: the
// holder was started without its inject gate and would answer disabled, so the
// daemon, which read the config, sends nothing and spends nothing.
//
// A refusal before the request frame is written leaves the message unspent: a
// message that does not pass the admission, a role that is not running, a
// holder that did not record the daemon's build for the epoch
// (holder-version-skew, or stale-epoch for a state file of another epoch; see
// attachSkew), a second message, the holder's socket that cannot be dialled. Once the frame
// is written the message is spent for the epoch whatever comes back (the
// canary's rule; the exception of scratch mode is below), and
// whatever becomes of the daemon: the inject-sent line of the evidence, which
// spends it for a daemon that starts later (see spentEpochs), is written right
// after the frame and before the wait for the answer, so a daemon that is
// killed while it waits leaves the epoch spent. When that line cannot be
// written the message is on its way all the same and is never reported as
// unsent (see sentUnrecorded). Only an ok answer whose inject result decodes
// is a result: no answer within the wait, a closed connection, an answer that
// does not decode, an ok answer without a result and a refusal code the daemon
// does not know are all the uncertain result of [leadcanary.NoAnswerResult],
// not an error. The holder's refusals disabled, wrong-epoch, busy and
// bad-request mean that nothing was written to the lead; they are the error
// unavailable, with the code holder-<code>, and an inject-refused line.
//
// A lead of scratch mode has two exceptions to the spent message, both of a
// message that was not delivered, and both once the line that says so is in
// the evidence (the line is what lets a daemon that starts later read the
// epoch as free; see scratchSpentEpochs): an ok answer whose result decodes,
// is valid and says not-now or refused with nothing written
// ([leadcanary.ReleasesMessage]), freed by the inject-result line, and one of
// the holder's own refusals above except busy ([holderFrees]), freed by the
// inject-refused line with the message id and the code holder-<code>. Every
// other case stays spent, a holder's busy included (it says a delivery is
// running that the daemon has no record of, which may have reached the lead):
// no answer, a closed connection, an answer that does not decode, an ok answer
// without a result, an uncertain, partial or whole result, a refusal code the
// daemon does not know, and a line that could not be written. The lead may
// then send another message in the epoch; it needs no other id than the
// admission asks, and a message id used before is not refused. The canary
// frees nothing: its message stays spent whatever the holder answers.
func (x *canaryExt) inject(ctx context.Context, p CanaryInjectParams) (any, *clientapi.ErrorBody) {
	res, f := x.injectFor(ctx, p, 0)
	if f != nil {
		return nil, f.body()
	}
	return res, nil
}

// injectFor is [canaryExt.inject] for an epoch the sender expects, as
// [canaryExt.ticketFor] is for a ticket. The check of the epoch comes after the
// one of whether the role is running and before the one of whether the epoch's
// message is spent, so that a message that names an epoch that is not the
// role's is never counted against the role's; the check of the holder's build
// follows it, also before the spent message is looked at. The frame the holder gets
// carries the epoch that was checked.
func (x *canaryExt) injectFor(ctx context.Context, p CanaryInjectParams, want uint64) (CanaryInjectResult, *opFail) {
	e, running := x.runningEntry()
	epoch := e.Epoch
	if !x.cfg.Inject {
		x.refuse(p.MessageID, epoch, "inject-off")
		return CanaryInjectResult{}, refusal("inject-off", msgInjectOff)
	}
	text, err := x.adm.Admit(p.MessageID, p.Body)
	if err != nil {
		code := "bad-message"
		var ae *leadcanary.AdmitError
		if errors.As(err, &ae) {
			code = string(ae.Code)
		}
		x.refuse(p.MessageID, epoch, code)
		return CanaryInjectResult{}, refusal(code, "the message is refused ("+code+")")
	}
	if !running {
		x.refuse(p.MessageID, epoch, "not-running")
		return CanaryInjectResult{}, refusal("not-running", msgNotRunning)
	}
	if want != 0 && want != epoch {
		x.refuse(p.MessageID, epoch, codeStaleEpoch)
		return CanaryInjectResult{}, refusal(codeStaleEpoch, msgStaleEpoch)
	}
	// The running holder recorded the daemon's own build for the epoch, as
	// for a ticket, or no frame is written and the message is not spent.
	if f := x.attachSkew("inject", epoch); f != nil {
		x.refuse(p.MessageID, epoch, f.code)
		return CanaryInjectResult{}, f
	}
	x.mu.Lock()
	switch {
	case x.spent[epoch]:
		x.mu.Unlock()
		x.refuse(p.MessageID, epoch, "second-message")
		return CanaryInjectResult{}, refusal("second-message", msgSecond)
	case x.inFlight:
		x.mu.Unlock()
		x.refuse(p.MessageID, epoch, "message-in-flight")
		return CanaryInjectResult{}, refusal("message-in-flight", msgInFlight)
	}
	x.inFlight = true
	x.mu.Unlock()
	wrote := false
	// release is set at the one place that may leave the message unspent, the
	// end of a result that was written down. Every other way out, a panic
	// included, finds it false and spends the message once the frame is out.
	release := false
	defer func() {
		x.mu.Lock()
		x.inFlight = false
		if wrote && !release {
			x.spent[epoch] = true
		}
		x.mu.Unlock()
	}()

	// The inject-sent line is written as soon as the frame is, before the
	// wait: it is what spends the message for a daemon that starts later.
	sent := false
	resp, wrote, err := callHolderThen(ctx, x.holderDir, wire.Request{
		Op: wire.OpInject, Epoch: epoch, MessageID: p.MessageID, Text: text,
		CommandID: x.commandID("inject", epoch),
	}, x.dial, x.wait, func() { sent = x.recordSent(p.MessageID, epoch) })
	if !wrote {
		x.logf("canary inject: %s", SafeText(err.Error()))
		x.refuse(p.MessageID, epoch, "holder-unreachable")
		return CanaryInjectResult{}, failure("holder-unreachable", msgUnreachable)
	}
	if !sent {
		x.sentUnrecorded(p.MessageID, epoch)
	}

	result := leadcanary.NoAnswerResult()
	switch {
	case err != nil:
		x.logf("canary inject: no usable answer: %s", SafeText(err.Error()))
	case resp.OK && resp.Inject != nil && resp.Inject.Validate() == nil:
		result = *resp.Inject
	case !resp.OK && holderRefusal(resp.Code):
		// The holder wrote nothing to the lead for this message. A lead of
		// scratch mode takes the epoch's message back once the refusal is in
		// the evidence (the line is what lets a daemon that starts later read
		// the epoch as free; see scratchSpentEpochs), except after busy: a
		// holder that is delivering something the daemon has no record of may
		// have delivered it, so that refusal leaves the message spent. The
		// canary's message stays spent after any of them.
		recorded := x.refuse(p.MessageID, epoch, "holder-"+resp.Code)
		switch {
		case recorded && x.scratch && holderFrees(resp.Code):
			release = true
			x.logf("lead inject: the message of epoch %d was refused by the holder (%s): the epoch's message is not spent", epoch, SafeText(resp.Code))
		case x.scratch && resp.Code == wire.CodeBusy:
			x.logf("lead inject: the message of epoch %d was refused by the holder as busy: a delivery this daemon has no record of may be running, so the epoch's message stays spent", epoch)
		}
		return CanaryInjectResult{}, failure("holder-"+resp.Code, msgHolderNo)
	}
	if x.recorded(leadcanary.ResultEntry(p.MessageID, epoch, result)) && x.scratch && leadcanary.ReleasesMessage(result) {
		release = true
		x.logf("lead inject: the message of epoch %d was not delivered (%s): the epoch's message is not spent", epoch, SafeText(string(result.Outcome)))
	}
	return CanaryInjectResult{Role: x.role.String(), Epoch: epoch, MessageID: p.MessageID, Result: result}, nil
}

// messageSpent reports whether the one message of the epoch is spent, from the
// role's own set.
func (x *canaryExt) messageSpent(epoch uint64) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.spent[epoch]
}

// recordSent writes the inject-sent line of a message whose request frame is
// written and reports whether the line is in the evidence.
func (x *canaryExt) recordSent(messageID string, epoch uint64) bool {
	err := x.ev.Append(leadcanary.Entry{Event: leadcanary.EventInjectSent, Epoch: epoch, MessageID: messageID})
	if err != nil {
		x.logf("canary evidence: %s: %s", leadcanary.EventInjectSent, SafeText(err.Error()))
	}
	return err == nil
}

// sentUnrecorded is what the daemon does when the inject-sent line could not
// be written right after the frame. The message is on its way, so nothing
// about the operation changes: the epoch is spent in this daemon, the answer
// is the result (or the holder's refusal) as with the line, and the message is
// never reported as unsent. The line is tried once more, now that the wait is
// over. When that fails too the log says what is lost: the evidence does not
// spend the epoch, so a daemon that is restarted inside it would take a second
// message, and the role is to be stopped before the daemon is.
//
// A write that failed part way leaves a part of a line in the file, and a
// later line that is written starts with a newline: either way the next
// daemon cannot read the file back and refuses to start, which keeps the
// epoch closed.
//
// A line whose write succeeded and whose sync failed counts as not recorded
// (Append returns an error), so the retry may write a second inject-sent line
// for the same message, and the log may then say the line is missing when one
// is there. A reader of the evidence must not count these lines per epoch
// without looking at the message id.
func (x *canaryExt) sentUnrecorded(messageID string, epoch uint64) {
	if x.recordSent(messageID, epoch) {
		return
	}
	x.logf("canary inject: the message of epoch %d is sent and the evidence has no inject-sent line for it: the epoch is spent in this daemon unless the holder answers that nothing was written, so stop the role before the daemon is restarted", epoch)
}

// holderFrees reports whether a holder refusal gives the epoch's message back
// to a lead of scratch mode: disabled, wrong-epoch and bad-request do, since the
// holder refused the request before any delivery. Busy does not: the daemon
// sends one message at a time, so a holder that says a delivery is running is
// delivering something the daemon has no record of (the residual of an inject
// whose daemon was killed between the frame and the inject-sent line), which
// may have reached the lead. The holder's text is not trusted to tell that busy
// from another one.
func holderFrees(code string) bool {
	return holderRefusal(code) && code != wire.CodeBusy
}

// holderRefusal reports whether code is one of the refusals that mean nothing
// was written to the lead for the message that was refused.
func holderRefusal(code string) bool {
	switch code {
	case wire.CodeDisabled, wire.CodeWrongEpoch, wire.CodeBusy, wire.CodeBadRequest:
		return true
	}
	return false
}

// refuse writes an inject-refused line and reports whether it is in the
// evidence. The message id goes in only when it is well formed: the evidence
// refuses any other.
func (x *canaryExt) refuse(messageID string, epoch uint64, code string) bool {
	if wire.CheckMessageID(messageID) != nil {
		messageID = ""
	}
	return x.recorded(leadcanary.Entry{Event: leadcanary.EventInjectRefused, Epoch: epoch, MessageID: messageID, Code: code})
}
