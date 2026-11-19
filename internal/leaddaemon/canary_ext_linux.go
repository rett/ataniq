//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// The operations of the canary extension. Each takes strict params that carry
// "role", which must be the config's role.
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
	msgRegistryNo   = "the registry refused"
	msgForeignUnit  = "a unit with the role's name is not the role's recorded unit"
	msgFailed       = "the operation failed; see the daemon's log"
	msgStopping     = "the daemon is stopping"
	attachRoleWrite = string(wire.RoleWriter)
	attachRoleView  = string(wire.RoleViewer)
)

// msgRegistryCheck is the fixed text for a refusal of the registry that may
// follow something the daemon already did.
const msgRegistryCheck = "the registry refused; check the role's status before you retry"

// canaryExt is the extension: the five operations of canary mode.
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

	mu sync.Mutex
	// spent holds the epochs whose one message is spent. It starts from the
	// inject-sent lines of the evidence (see spentEpochs), so that a restart of
	// the daemon inside an epoch admits no second message.
	spent    map[uint64]bool
	inFlight bool   // a message is being sent
	commands uint64 // counter of the command ids
}

var _ clientapi.Extension = (*canaryExt)(nil)

func newCanaryExt(m *canaryMode, ctl *leadlifecycle.Controller, reg *leadregistry.Registry) *canaryExt {
	cfg := m.cfg
	// Validated at setup: the names, the admission and the runtime folder
	// cannot fail for this config.
	holderDir, _ := cfg.HolderRuntimeDir()
	adm, _ := leadcanary.NewAdmission(cfg.Role)
	spent := make(map[uint64]bool, len(m.spent))
	for epoch := range m.spent {
		spent[epoch] = true
	}
	return &canaryExt{
		cfg: cfg, role: cfg.Role, holderDir: holderDir, ctl: ctl, reg: reg, ev: m.ev, adm: adm,
		dial: m.dialFor, wait: m.injectFor, logf: m.d.logf, spent: spent,
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

// Operations lists the five operations.
func (x *canaryExt) Operations() []string { return CanaryOperations() }

// Mutating is true: define, start, ticket, inject and stop change things.
func (x *canaryExt) Mutating() bool { return true }

// Handle decodes the strict params of op and carries it out.
func (x *canaryExt) Handle(ctx context.Context, op string, params json.RawMessage) (any, *clientapi.ErrorBody) {
	switch op {
	case OpCanaryDefine, OpCanaryStart, OpCanaryStop:
		var p CanaryParams
		if f := decodeStrict(params, &p, "role"); f != nil {
			return nil, f
		}
		if f := x.checkRole(p.Role); f != nil {
			return nil, f
		}
		switch op {
		case OpCanaryDefine:
			return x.define()
		case OpCanaryStart:
			return x.start(ctx)
		}
		return x.stop(ctx)
	case OpCanaryTicket:
		var p CanaryTicketParams
		if f := decodeStrict(params, &p, "role", "attach_role"); f != nil {
			return nil, f
		}
		if f := x.checkRole(p.Role); f != nil {
			return nil, f
		}
		if p.AttachRole != attachRoleWrite && p.AttachRole != attachRoleView {
			return nil, &clientapi.ErrorBody{Code: clientapi.CodeInvalidRequest, Message: msgBadParams}
		}
		return x.ticket(ctx, wire.Role(p.AttachRole))
	case OpCanaryInject:
		var p CanaryInjectParams
		if f := decodeStrict(params, &p, "role", "message_id", "body"); f != nil {
			return nil, f
		}
		if f := x.checkRole(p.Role); f != nil {
			return nil, f
		}
		return x.inject(ctx, p)
	}
	return nil, &clientapi.ErrorBody{Code: clientapi.CodeUnsupportedOperation, Message: msgBadParams}
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

// checkRole refuses a role other than the config's, before anything else.
func (x *canaryExt) checkRole(role string) *clientapi.ErrorBody {
	if role != x.role.String() {
		return refused(msgNotCanary)
	}
	return nil
}

// record appends an evidence line. The operation it describes has happened by
// then, so a failure to write is the daemon's log's to carry.
func (x *canaryExt) record(en leadcanary.Entry) {
	if err := x.ev.Append(en); err != nil {
		x.logf("canary evidence: %s: %s", en.Event, SafeText(err.Error()))
	}
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
	case errors.Is(err, leadregistry.ErrLaunchRefused):
		return refused("the registry does not admit a launch in the role's state")
	case errors.Is(err, leadregistry.ErrInvalid):
		return refused(msgRegistryNo)
	case errors.Is(err, leadlifecycle.ErrNoProof):
		return unavailable("the previous unit is not proven gone")
	case errors.Is(err, leadlifecycle.ErrStartFailed):
		return unavailable("the start failed and nothing was started")
	case errors.Is(err, leadlifecycle.ErrLaunchUncertain):
		return unavailable("the start's outcome is not known; the role is shown unknown")
	case errors.Is(err, leadlifecycle.ErrStopUnproven):
		return unavailable("the stop is not proven; the role is shown unknown")
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
	return "canary-" + op + "-" + strconv.FormatUint(epoch, 10) + "-" + strconv.FormatUint(n, 10)
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
	// Provisioning makes the holder's runtime folder; the daemon checks it
	// before it changes anything.
	fd, err := openPrivateDir(x.holderDir)
	if err != nil {
		x.logf("canary start: the runtime folder: %s", SafeText(err.Error()))
		return nil, refused(msgFolder)
	}
	unix.Close(fd)
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
// in the evidence, not in the log.
func (x *canaryExt) ticket(ctx context.Context, attach wire.Role) (any, *clientapi.ErrorBody) {
	e, running := x.runningEntry()
	if !running {
		return nil, refused(msgNotRunning)
	}
	ticket, err := wire.NewTicket()
	if err != nil {
		x.logf("canary ticket: cannot mint a ticket: %s", SafeText(err.Error()))
		return nil, unavailable(msgFailed)
	}
	resp, wrote, err := callHolder(ctx, x.holderDir, wire.Request{
		Op: wire.OpTicket, Epoch: e.Epoch, Role: attach, Ticket: ticket,
		CommandID: x.commandID("ticket", e.Epoch),
	}, x.dial, x.dial)
	switch {
	case err != nil && !wrote:
		x.logf("canary ticket: %s", SafeText(err.Error()))
		return nil, unavailable(msgUnreachable)
	case err != nil:
		x.logf("canary ticket: no usable answer: %s", SafeText(err.Error()))
		return nil, unavailable(msgTicketLost)
	case !resp.OK:
		x.logf("canary ticket: the holder refused: %s", SafeText(resp.Code))
		return nil, refused(msgTicketNo)
	}
	file, err := writeTicketFile(x.holderDir, ticket)
	if err != nil {
		x.logf("canary ticket: the ticket file: %s", SafeText(err.Error()))
		return nil, unavailable(msgTicketFile)
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
// second message, the holder's socket that cannot be dialled. Once the frame
// is written the message is spent for the epoch whatever comes back, and
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
// unavailable.
func (x *canaryExt) inject(ctx context.Context, p CanaryInjectParams) (any, *clientapi.ErrorBody) {
	e, running := x.runningEntry()
	epoch := e.Epoch
	if !x.cfg.Inject {
		x.refuse(p.MessageID, epoch, "inject-off")
		return nil, refused(msgInjectOff)
	}
	text, err := x.adm.Admit(p.MessageID, p.Body)
	if err != nil {
		code := "bad-message"
		var ae *leadcanary.AdmitError
		if errors.As(err, &ae) {
			code = string(ae.Code)
		}
		x.refuse(p.MessageID, epoch, code)
		return nil, refused("the message is refused (" + code + ")")
	}
	if !running {
		x.refuse(p.MessageID, epoch, "not-running")
		return nil, refused(msgNotRunning)
	}
	x.mu.Lock()
	switch {
	case x.spent[epoch]:
		x.mu.Unlock()
		x.refuse(p.MessageID, epoch, "second-message")
		return nil, refused(msgSecond)
	case x.inFlight:
		x.mu.Unlock()
		x.refuse(p.MessageID, epoch, "message-in-flight")
		return nil, refused(msgInFlight)
	}
	x.inFlight = true
	x.mu.Unlock()
	wrote := false
	defer func() {
		x.mu.Lock()
		x.inFlight = false
		if wrote {
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
		return nil, unavailable(msgUnreachable)
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
		x.refuse(p.MessageID, epoch, "holder-"+resp.Code)
		return nil, unavailable(msgHolderNo)
	}
	x.record(leadcanary.ResultEntry(p.MessageID, epoch, result))
	return CanaryInjectResult{Role: x.role.String(), Epoch: epoch, MessageID: p.MessageID, Result: result}, nil
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
	x.logf("canary inject: the message of epoch %d is sent and the evidence has no inject-sent line for it: the epoch is spent in this daemon only, so stop the role before the daemon is restarted", epoch)
}

// holderRefusal reports whether code is one of the refusals that mean nothing
// was written to the lead.
func holderRefusal(code string) bool {
	switch code {
	case wire.CodeDisabled, wire.CodeWrongEpoch, wire.CodeBusy, wire.CodeBadRequest:
		return true
	}
	return false
}

// refuse writes an inject-refused line. The message id goes in only when it is
// well formed: the evidence refuses any other.
func (x *canaryExt) refuse(messageID string, epoch uint64, code string) {
	if wire.CheckMessageID(messageID) != nil {
		messageID = ""
	}
	x.record(leadcanary.Entry{Event: leadcanary.EventInjectRefused, Epoch: epoch, MessageID: messageID, Code: code})
}
