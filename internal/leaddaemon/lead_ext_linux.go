//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"

	"golang.org/x/sys/unix"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision"
	"ataniq/internal/leadcontrol"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadunit"
	"ataniq/internal/leadwire"
)

// This file is the lead.* extension of scratch mode: the six operations of the
// lead control plane over the router's leads, the lifecycle and the command
// table. The operations and what they answer are the structs of
// internal/leadwire, as they are.
//
// A mutating operation goes through these steps, in this order:
//
//  1. the params are decoded strictly: every key of the operation, exactly
//     spelled and not null, and nothing else;
//  2. the role is looked up. A role that is not a lead of the daemon is the
//     error refused, and that is the only place that error is used: no
//     command is admitted for it;
//  3. the digest of the command is made: SHA-256 of the operation name, a NUL
//     and json.Marshal of the decoded params with the client and the command
//     id emptied. It is made from the decoded struct, never from the bytes of
//     the request;
//  4. the command is admitted to the table (made durable before Admit
//     returns). A command whose key is held already is answered by the stored
//     receipt, whatever its status: there is no lock and no effect on that
//     path, so a replay is answered while the first call still runs;
//  5. a new command acts, and is finished exactly once, whatever the act does,
//     even a panic: the receipt carries the status that says what may have
//     happened.
//
// The caller is the connection of the daemon's own user. The client string of
// a command is a namespace for the command ids, not an identity.

// The reason codes of a receipt that this file adds to leadwire's and the
// canary's. Each is a word of [a-z][a-z0-9-]{0,63}.
const (
	codeRoleBusy         = "role-busy"
	codeNotProvisioned   = "not-provisioned"
	codeFolderMissing    = "folder-missing"
	codeProvisionIncomp  = "provision-incomplete"
	codeProvisionFailed  = "provision-failed"
	codeProvisionRefused = "provision-refused"
	codeRecordInvalid    = "record-invalid"
	codeDigestPin        = "digest-pin"
	codeHolderCommand    = "holder-command"
	codeRoleNotDefined   = "role-not-defined"
	codeNotManaged       = "not-managed"
	codePreflight        = "preflight"
	codeForeignUnit      = "foreign-unit"
	codeUnitLoaded       = "unit-loaded"
	codeLimits           = "limits"
	codeLaunchRefused    = "launch-refused"
	codeRegistryRefused  = "registry-refused"
	codeEpochExhausted   = "epoch-exhausted"
	codeNoProof          = "previous-unit-not-gone"
	codeStartFailed      = "start-failed"
	codeLaunchUncertain  = "launch-uncertain"
	codeStopUnproven     = "stop-unproven"
	codeIntentPending    = "intent-pending"
	codeRegistryCheck    = "registry-check"
	codeRegistryBroken   = "registry-broken"
	codeDaemonStopping   = "daemon-stopping"
	codeFailed           = "failed"
	codePanic            = "panic"
	codeDaemonBroken     = "daemon-broken"
	codeReceipt          = "receipt-not-recorded"
)

// The fixed texts of the receipts and the errors this file answers. They are
// printable ASCII of at most 256 bytes without <, > and &, and none holds
// anything a peer sent.
const (
	msgNotALead = "the role is not a lead of this daemon"
	msgConflict = "the command id is held for another command"
	msgExpired  = "the command id is no longer held; read lead.list"
	msgTableFul = "the command table is full"
	msgTableNo  = "the command table cannot take the command; see the daemon's log"

	msgRoleBusy        = "another command for the role is running"
	msgNotProvisioned  = "the lead is not provisioned; define it first"
	msgMaxRunning      = "the daemon is at its running limit; a lead shown starting may have ended and is freed by lead.stop"
	msgProvisionIncomp = "an earlier define was cut short and left the lead's folders; the owner removes them with scratch-clean"
	msgProvisionFailed = "provisioning failed; see the daemon's log; if it left the lead's folders the owner removes them with scratch-clean"
	msgProvisionRefuse = "provisioning refused the lead; see the daemon's log"
	msgRecordInvalid   = "the lead's record is not valid; the owner checks the lead's folders"
	msgDigestPin       = "the profile digest is not the one pinned for the lead"
	msgHolderCommand   = "the lead is defined but its holder command cannot be built; see the daemon's log"
	msgNotDefined      = "the role is not defined"
	msgNotManaged      = "the role is not run by this daemon"
	msgPreflight       = "the preflight refused the start"
	msgUnitLoaded      = "a unit with the role's name is loaded"
	msgLimits          = "the unit's limits are outside the bounds"
	msgLaunchRefused   = "the registry does not admit a launch in the role's state"
	msgNoProof         = "the previous unit is not proven gone"
	msgStartFailed     = "the start failed and nothing was started"
	msgLaunchUncertain = "the start's outcome is not known; the role is shown unknown"
	msgStopUnproven    = "the stop is not proven; read the lead's state"
	msgIntentPending   = "the launch intent is not settled yet"
	msgRegistryUnsure  = "the registry may or may not hold the change; read the lead's state"
	msgRegistryBroken  = "the registry cannot be written; the daemon must be restarted"
	msgDaemonStopping  = "the daemon was stopping; read the lead's state"
	msgFailedRead      = "the operation failed; see the daemon's log, and read the lead's state"
	msgPanic           = "the operation failed unexpectedly; see the daemon's log, and read the lead's state"
	msgDaemonBroken    = "the registry or the command table cannot be written; the daemon must be restarted"
	msgReceiptNo       = "the command ran but its receipt could not be recorded in full; read the lead's state"
	msgRecordNotFound  = "the lead has no record, so provisioning is not complete"
)

// The proof a stop of a lead that was defined and never started carries: the
// registry holds none for it, and the receipt never shows an empty proof.
const msgProofNeverStarted = "no launch was ever recorded and no unit of the role is loaded"

// outcome is what a command ended with, before it is made the receipt's final
// answer.
type outcome struct {
	status leadcontrol.Status
	code   string
	msg    string
	result any
}

func done(result any) outcome { return outcome{status: leadcontrol.StatusDone, result: result} }

func refusedOut(code, msg string) outcome {
	return outcome{status: leadcontrol.StatusRefused, code: code, msg: msg}
}

func unavailableOut(code, msg string) outcome {
	return outcome{status: leadcontrol.StatusUnavailable, code: code, msg: msg}
}

func uncertainOut(code, msg string) outcome {
	return outcome{status: leadcontrol.StatusUncertain, code: code, msg: msg}
}

// fromFail makes the outcome of an operation of the canary extension's core
// that did not give a result.
func fromFail(f *opFail) outcome {
	if f.refused {
		return refusedOut(f.code, f.msg)
	}
	return unavailableOut(f.code, f.msg)
}

// leadExt is the lead.* extension. It satisfies [clientapi.Extension].
type leadExt struct {
	m *leadMode
}

var _ clientapi.Extension = (*leadExt)(nil)

// Operations lists the six operations of leadwire, in its order.
func (x *leadExt) Operations() []string { return leadwire.Operations() }

// Mutating is true: five of the six change something.
func (x *leadExt) Mutating() bool { return true }

// command is a mutating operation that was decoded.
type command struct {
	op     string
	client string
	id     string
	role   string
	// canon is the decoded params with the client and the command id emptied,
	// which the digest is made of.
	canon any
	// check validates what needs the role to be known first; nil when there is
	// nothing to check.
	check func() *clientapi.ErrorBody
	// act carries the command out. It is called only for a command that was
	// admitted as new.
	act func(ctx context.Context, m *leadMode, lr *leadRole) outcome
}

// Handle serves op: lead.list, or a mutating operation in the order of this
// file's comment.
func (x *leadExt) Handle(ctx context.Context, op string, params json.RawMessage) (any, *clientapi.ErrorBody) {
	m := x.m
	if op == leadwire.OpList {
		return m.list(params)
	}
	cmd, f := decodeCommand(op, params)
	if f != nil {
		return nil, f
	}
	lr, ok := m.lookup(cmd.role)
	if !ok {
		return nil, refused(msgNotALead)
	}
	if cmd.check != nil {
		if f := cmd.check(); f != nil {
			return nil, f
		}
	}
	digest, err := commandDigest(op, cmd.canon)
	if err != nil {
		return nil, &clientapi.ErrorBody{Code: clientapi.CodeInvalidRequest, Message: msgBadParams}
	}
	admitted, fresh, err := m.table.Admit(leadcontrol.Binding{
		Client: cmd.client, ID: cmd.id, Op: op, Role: lr.role, Digest: digest,
	})
	if err != nil {
		return nil, m.admitError(err)
	}
	if !fresh {
		// A replay: the stored receipt, whatever its status. Nothing is taken
		// and nothing runs.
		return toWire(admitted), nil
	}
	return m.run(ctx, cmd, lr, admitted), nil
}

// admitError turns the refusal of Admit into the error that answers it. Nothing
// was admitted.
func (m *leadMode) admitError(err error) *clientapi.ErrorBody {
	switch {
	case errors.Is(err, leadcontrol.ErrInvalid):
		return &clientapi.ErrorBody{Code: clientapi.CodeInvalidRequest, Message: msgBadParams}
	case errors.Is(err, leadcontrol.ErrConflict):
		return &clientapi.ErrorBody{Code: clientapi.ErrorCode(leadwire.ErrCodeConflict), Message: msgConflict}
	case errors.Is(err, leadcontrol.ErrExpired):
		return &clientapi.ErrorBody{Code: clientapi.ErrorCode(leadwire.ErrCodeConflict), Message: msgExpired}
	case errors.Is(err, leadcontrol.ErrFull):
		return unavailable(msgTableFul)
	}
	m.d.logf("scratch: the command table refused a command: %s", SafeText(err.Error()))
	if errors.Is(err, leadcontrol.ErrBroken) || errors.Is(err, leadcontrol.ErrClosed) {
		m.markBroken("the command table cannot be written")
	}
	return unavailable(msgTableNo)
}

// run carries out a command that was admitted as new and finishes it, exactly
// once, whatever the act does: a panic in it is recovered and the command is
// finished uncertain, its locks and its reservation freed by the defers of the
// act on the way out. It returns the receipt.
func (m *leadMode) run(ctx context.Context, cmd *command, lr *leadRole, admitted leadcontrol.Receipt) leadwire.Receipt {
	var out outcome
	func() {
		defer func() {
			if r := recover(); r != nil {
				m.d.logf("scratch: the %s of the lead %q panicked (%T); the command is finished uncertain", cmd.op, lr.key, r)
				out = uncertainOut(codePanic, msgPanic)
			}
		}()
		if m.broken.Load() {
			// Nothing was done, and nothing will be: the registry or the table
			// cannot be written.
			out = refusedOut(codeDaemonBroken, msgDaemonBroken)
			return
		}
		out = cmd.act(ctx, m, lr)
	}()
	return m.finish(admitted, out)
}

// finish makes the final answer of an admitted command durable. A line the
// table refuses as too long or as not well formed leaves the command open, so
// it is finished again with no result and a fixed code (a done command then
// reads uncertain: it ran, and its answer is not kept); any other failure means
// the table cannot be written, which is logged and stops the daemon from
// admitting more commands, and the command is answered as it was admitted.
func (m *leadMode) finish(admitted leadcontrol.Receipt, out outcome) leadwire.Receipt {
	rec, err := m.table.Finish(admitted.CommandID, out.status, out.code, out.msg, out.result)
	if err == nil {
		return toWire(rec)
	}
	m.d.logf("scratch: the receipt of %s cannot be recorded: %s", admitted.Op, SafeText(err.Error()))
	if errors.Is(err, leadcontrol.ErrTooLarge) || errors.Is(err, leadcontrol.ErrInvalid) {
		st := out.status
		if st == leadcontrol.StatusDone {
			st = leadcontrol.StatusUncertain
		}
		rec, err = m.table.Finish(admitted.CommandID, st, codeReceipt, msgReceiptNo, nil)
		if err == nil {
			return toWire(rec)
		}
		m.d.logf("scratch: the receipt of %s cannot be recorded without its result either: %s", admitted.Op, SafeText(err.Error()))
	}
	m.markBroken("the command table cannot finish a command")
	return toWire(admitted)
}

// toWire is the receipt of the table as the wire's struct.
func toWire(r leadcontrol.Receipt) leadwire.Receipt {
	return leadwire.Receipt{
		CommandID: r.CommandID, Op: r.Op, Role: r.Role, Status: leadwire.Status(r.Status),
		Code: r.Code, Message: r.Message, Result: r.Result, AdmittedAt: r.AdmittedAt, FinishedAt: r.FinishedAt,
	}
}

// commandDigest is the digest of a command: SHA-256 of the operation name, a
// NUL and the JSON of its decoded params with the client and the command id
// emptied (canon). The order of the struct's fields makes it deterministic.
func commandDigest(op string, canon any) ([sha256.Size]byte, error) {
	b, err := json.Marshal(canon)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	h := sha256.New()
	h.Write([]byte(op))
	h.Write([]byte{0})
	h.Write(b)
	var out [sha256.Size]byte
	h.Sum(out[:0])
	return out, nil
}

// decodeLead decodes the params of an operation strictly: one JSON object whose
// keys are exactly keys, each present and none null, and nothing after it.
func decodeLead(params json.RawMessage, v any, keys ...string) *clientapi.ErrorBody {
	bad := &clientapi.ErrorBody{Code: clientapi.CodeInvalidRequest, Message: msgBadParams}
	if len(params) == 0 {
		return bad
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(params, &members); err != nil || len(members) != len(keys) {
		return bad
	}
	for _, k := range keys {
		raw, ok := members[k]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return bad
		}
	}
	return decodeStrict(params, v, keys...)
}

// decodeCommand decodes the params of a mutating operation into a command.
func decodeCommand(op string, params json.RawMessage) (*command, *clientapi.ErrorBody) {
	switch op {
	case leadwire.OpDefine:
		var p leadwire.DefineParams
		if f := decodeLead(params, &p, "client", "command_id", "role"); f != nil {
			return nil, f
		}
		canon := p
		canon.Client, canon.CommandID = "", ""
		return &command{op: op, client: p.Client, id: p.CommandID, role: p.Role, canon: canon,
			act: func(ctx context.Context, m *leadMode, lr *leadRole) outcome { return m.define(ctx, lr) }}, nil
	case leadwire.OpStart:
		var p leadwire.StartParams
		if f := decodeLead(params, &p, "client", "command_id", "role"); f != nil {
			return nil, f
		}
		canon := p
		canon.Client, canon.CommandID = "", ""
		return &command{op: op, client: p.Client, id: p.CommandID, role: p.Role, canon: canon,
			act: func(ctx context.Context, m *leadMode, lr *leadRole) outcome { return m.start(ctx, lr) }}, nil
	case leadwire.OpStop:
		var p leadwire.StopParams
		if f := decodeLead(params, &p, "client", "command_id", "role", "epoch"); f != nil {
			return nil, f
		}
		canon := p
		canon.Client, canon.CommandID = "", ""
		return &command{op: op, client: p.Client, id: p.CommandID, role: p.Role, canon: canon,
			act: func(ctx context.Context, m *leadMode, lr *leadRole) outcome { return m.stop(ctx, lr, p.Epoch) }}, nil
	case leadwire.OpTicket:
		var p leadwire.TicketParams
		if f := decodeLead(params, &p, "client", "command_id", "role", "epoch", "attach_role"); f != nil {
			return nil, f
		}
		canon := p
		canon.Client, canon.CommandID = "", ""
		return &command{op: op, client: p.Client, id: p.CommandID, role: p.Role, canon: canon,
			// After the role: a malformed attach role of a role that is not
			// the daemon's is still the refusal of the role.
			check: func() *clientapi.ErrorBody {
				if p.AttachRole != leadwire.AttachWriter && p.AttachRole != leadwire.AttachViewer {
					return &clientapi.ErrorBody{Code: clientapi.CodeInvalidRequest, Message: msgBadParams}
				}
				return nil
			},
			act: func(ctx context.Context, m *leadMode, lr *leadRole) outcome { return m.ticket(ctx, lr, p) }}, nil
	case leadwire.OpInject:
		var p leadwire.InjectParams
		if f := decodeLead(params, &p, "client", "command_id", "role", "epoch", "message_id", "body"); f != nil {
			return nil, f
		}
		canon := p
		canon.Client, canon.CommandID = "", ""
		return &command{op: op, client: p.Client, id: p.CommandID, role: p.Role, canon: canon,
			act: func(ctx context.Context, m *leadMode, lr *leadRole) outcome { return m.inject(ctx, lr, p) }}, nil
	}
	return nil, &clientapi.ErrorBody{Code: clientapi.CodeUnsupportedOperation, Message: msgBadParams}
}

// withRoleLock runs fn holding the role's operation lock, taken with a
// try-lock: when another command of the role holds it the command finishes
// refused (role-busy) and nothing queues. The lock is released by the defer, a
// panic in fn included.
func (m *leadMode) withRoleLock(lr *leadRole, op string, fn func() outcome) outcome {
	if !lr.tryLock() {
		return refusedOut(codeRoleBusy, msgRoleBusy)
	}
	defer lr.unlock()
	if leadActHook != nil {
		leadActHook(op)
	}
	return fn()
}

// registryDown reports an error of the registry that says it cannot be written
// any more, and marks the daemon broken for it.
func (m *leadMode) registryDown(err error) bool {
	if errors.Is(err, leadregistry.ErrBroken) || errors.Is(err, leadregistry.ErrClosed) {
		m.markBroken("the registry cannot be written")
		return true
	}
	return false
}

// define provisions the lead when that is not done, defines it in the registry
// and gives it its holder command. It is repeatable after a restart: a lead
// whose provisioning is done skips provisioning, reads its record and goes on.
// The order is: provision (the record is the last act), the registry's Define,
// the holder command with the digest of the record, the receipt.
func (m *leadMode) define(ctx context.Context, lr *leadRole) outcome {
	return m.withRoleLock(lr, "define", func() outcome {
		lr.defining.Store(true)
		defer lr.defining.Store(false)
		// Provisioning runs on the server's context with a bound of its own,
		// and with the explicit environment of its git commands.
		pctx, cancel := context.WithTimeout(ctx, m.provisionT)
		defer cancel()

		st, err := provision.ScratchStateOf(lr.cfg)
		if err != nil {
			m.d.logf("scratch: the state of the lead %q cannot be read: %s", lr.key, SafeText(err.Error()))
			return refusedOut(codeRecordInvalid, msgRecordInvalid)
		}
		switch st {
		case provision.StateIncomplete:
			return refusedOut(codeProvisionIncomp, msgProvisionIncomp)
		case provision.StateNone:
			_, err := provision.ProvisionScratch(pctx, lr.cfg, m.cfg.Sandbox, provision.Options{
				Getenv: m.getenv,
				Log:    func(s string) { m.d.logf("scratch: %s: %s", lr.key, SafeText(s)) },
			})
			// A lead that was provisioned in the meantime is not a failure.
			if err != nil && !errors.Is(err, provision.ErrFirstStart) {
				return m.provisionFailed(lr, err)
			}
		}
		rec, err := provision.ReadLeadRecord(lr.cfg)
		if err != nil {
			m.d.logf("scratch: the record of the lead %q: %s", lr.key, SafeText(err.Error()))
			if errors.Is(err, fs.ErrNotExist) {
				return refusedOut(codeProvisionIncomp, msgRecordNotFound)
			}
			return refusedOut(codeRecordInvalid, msgRecordInvalid)
		}
		if pin, ok := lr.cfg.PinnedDigest(); ok && pin != rec.ProfileDigest {
			return refusedOut(codeDigestPin, msgDigestPin)
		}
		holder, err := m.holderFor(lr, rec.ProfileDigest, leadlock.DaemonPath(m.d.StateDir))
		if err != nil {
			m.d.logf("scratch: the holder command of the lead %q: %s", lr.key, SafeText(err.Error()))
			return refusedOut(codeRecordInvalid, msgRecordInvalid)
		}
		e, err := m.reg.Define(lr.role, leadregistry.ManagerAtaniq, lr.in.ext.profile)
		if err != nil {
			m.d.logf("scratch: define %q: %s", lr.key, SafeText(err.Error()))
			switch {
			case m.registryDown(err):
				return uncertainOut(codeRegistryBroken, msgRegistryBroken)
			case errors.Is(err, leadregistry.ErrInvalid), errors.Is(err, leadregistry.ErrTooManyRoles):
				return refusedOut(codeRegistryRefused, msgRegistryNo)
			}
			return uncertainOut(codeFailed, msgRegistryUnsure)
		}
		// The holder command is set once the registry has the role, under the
		// role's lock, so that a start never builds a command for a digest the
		// define did not read.
		lr.in.setHolderCommand(holder)
		lr.in.ext.record(leadcanary.Entry{Event: leadcanary.EventDefine})
		return done(leadwire.DefineResult{Role: lr.role.String(), State: string(e.Shown.State), ProfileDigest: rec.ProfileDigest})
	})
}

// provisionFailed answers a provisioning that did not finish. A lead that is
// found cut short or whose digest is not its pin is refused, and so is a lead
// for which provisioning made nothing; a provisioning that left folders is
// unavailable, and says what the owner does.
func (m *leadMode) provisionFailed(lr *leadRole, err error) outcome {
	m.d.logf("scratch: provisioning of the lead %q failed: %s", lr.key, SafeText(err.Error()))
	switch {
	case errors.Is(err, provision.ErrDigestPin):
		return refusedOut(codeDigestPin, msgDigestPin)
	case errors.Is(err, provision.ErrIncomplete):
		return refusedOut(codeProvisionIncomp, msgProvisionIncomp)
	}
	st, serr := provision.ScratchStateOf(lr.cfg)
	if serr == nil && st == provision.StateNone && ctxFree(err) {
		return refusedOut(codeProvisionRefused, msgProvisionRefuse)
	}
	return unavailableOut(codeProvisionFailed, msgProvisionFailed)
}

// ctxFree reports an error that has no context error in it.
func ctxFree(err error) bool {
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// start starts the lead through the lifecycle, within max_running.
func (m *leadMode) start(ctx context.Context, lr *leadRole) outcome {
	return m.withRoleLock(lr, "start", func() outcome {
		if lr.in.holderCommand() == nil {
			return refusedOut(codeNotProvisioned, msgNotProvisioned)
		}
		// The folders that provisioning makes are checked before anything
		// changes, as the canary's start does.
		for _, f := range lr.in.ext.provisionedFolders() {
			fd, err := f.open()
			if err != nil {
				m.d.logf("scratch start %q: %s: %s", lr.key, f.what, SafeText(err.Error()))
				return refusedOut(codeFolderMissing, f.msg)
			}
			unix.Close(fd)
		}
		if !m.reserve(lr) {
			return refusedOut(leadwire.CodeMaxRunning, msgMaxRunning)
		}
		// Released once Start has returned: by then the registry shows the lead.
		defer m.release(lr)
		e, err := m.ctl.Start(ctx, lr.role)
		if err != nil {
			if errors.Is(err, leadlifecycle.ErrStartFailed) && e.Shown.State == leadregistry.StateUnknown {
				// The lifecycle could not prove the unit gone after the failed
				// launch (it could not inspect it, found it loaded, or the name
				// is held), so it left the role unknown. "Nothing was started"
				// would be a claim of what is not known.
				m.d.logf("scratch start %q: %s", lr.key, SafeText(err.Error()))
				return uncertainOut(codeLaunchUncertain, msgLaunchUncertain)
			}
			return m.startFailure(lr, err)
		}
		unit := ""
		if e.Intent != nil {
			unit = e.Intent.UnitName
		}
		lr.in.ext.record(leadcanary.Entry{Event: leadcanary.EventStart, Epoch: e.Epoch})
		return done(leadwire.StartResult{
			Role: lr.role.String(), State: string(e.Shown.State), Epoch: e.Epoch, Unit: unit, Waiver: canaryroot.WaiverText,
		})
	})
}

// startFailure chooses the status of a start that returned an error by what may
// have happened: refused only for a failure before any change of the registry or
// the unit manager; what the lifecycle says is not known, a context that ended,
// a broken registry, and a stale epoch or a refused transition (which come after
// the lifecycle acted) are uncertain.
func (m *leadMode) startFailure(lr *leadRole, err error) outcome {
	m.d.logf("scratch start %q: %s", lr.key, SafeText(err.Error()))
	switch {
	case m.registryDown(err):
		return uncertainOut(codeRegistryBroken, msgRegistryBroken)
	case errors.Is(err, leadlifecycle.ErrLaunchUncertain):
		return uncertainOut(codeLaunchUncertain, msgLaunchUncertain)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return uncertainOut(codeDaemonStopping, msgDaemonStopping)
	case errors.Is(err, leadlifecycle.ErrStartFailed):
		// Before the foreign-unit cases: a start that failed because the name
		// is held changed the registry.
		return unavailableOut(codeStartFailed, msgStartFailed)
	case errors.Is(err, leadregistry.ErrStaleEpoch), errors.Is(err, leadregistry.ErrStateTransition):
		return uncertainOut(codeRegistryCheck, msgRegistryUnsure)
	case errors.Is(err, leadregistry.ErrNoRole):
		return refusedOut(codeRoleNotDefined, msgNotDefined)
	case errors.Is(err, leadlifecycle.ErrNotManaged):
		return refusedOut(codeNotManaged, msgNotManaged)
	case errors.Is(err, leadlifecycle.ErrPreflight):
		return refusedOut(codePreflight, msgPreflight)
	case errors.Is(err, leadlifecycle.ErrForeignUnit):
		return refusedOut(codeForeignUnit, msgForeignUnit)
	case errors.Is(err, leadlifecycle.ErrUnitLoaded):
		return refusedOut(codeUnitLoaded, msgUnitLoaded)
	case errors.Is(err, leadunit.ErrInvalidSpec):
		return refusedOut(codeLimits, msgLimits)
	case errors.Is(err, leadregistry.ErrLaunchRefused):
		return refusedOut(codeLaunchRefused, msgLaunchRefused)
	case errors.Is(err, leadregistry.ErrEpochExhausted):
		return refusedOut(codeEpochExhausted, msgRegistryNo)
	case errors.Is(err, leadregistry.ErrInvalid):
		return refusedOut(codeRegistryRefused, msgRegistryNo)
	case errors.Is(err, leadlifecycle.ErrNoProof):
		return unavailableOut(codeNoProof, msgNoProof)
	case errors.Is(err, leadlifecycle.ErrIntentPending):
		return unavailableOut(codeIntentPending, msgIntentPending)
	}
	return uncertainOut(codeFailed, msgFailedRead)
}

// stop stops the lead through the lifecycle whatever state the registry shows
// for it: the lifecycle's Stop signals the role's own units, so it is the way
// out of unknown and of a unit that a start left behind. An epoch of 0 means
// the current epoch; any other is compared with the registry's under the
// role's operation lock, which a start of the role also holds, so a stop that
// was meant for one epoch never stops the next.
func (m *leadMode) stop(ctx context.Context, lr *leadRole, epoch uint64) outcome {
	return m.withRoleLock(lr, "stop", func() outcome {
		if epoch != 0 {
			e, err := m.reg.Get(lr.role)
			switch {
			case errors.Is(err, leadregistry.ErrNoRole):
				return refusedOut(codeRoleNotDefined, msgNotDefined)
			case err != nil:
				m.registryDown(err)
				m.d.logf("scratch stop %q: %s", lr.key, SafeText(err.Error()))
				return unavailableOut(codeFailed, msgFailedRead)
			case e.Epoch != epoch:
				return refusedOut(codeStaleEpoch, msgStaleEpoch)
			}
		}
		e, err := m.ctl.Stop(ctx, lr.role)
		if err != nil {
			return m.stopFailure(lr, err)
		}
		switch {
		case (e.Shown.State == leadregistry.StateStopped || e.Shown.State == leadregistry.StateEnded) && e.Shown.Proof != "":
			// Stopped by this stop, or stopped or ended earlier with the proof
			// that the lifecycle (or the reconcile) recorded.
		case e.Shown.State == leadregistry.StateStopped && e.Epoch == 0 && e.Intent == nil:
			// Defined and never started: no launch was ever recorded, and the
			// lifecycle has just found no unit of the role's name loaded, so
			// there is no unit a stop could fail to end. The registry holds no
			// proof text for it, so the receipt carries a fixed one.
			e.Shown.Proof = msgProofNeverStarted
		default:
			// Anything else is not a stop that was proven: a state that is not
			// stopped or ended, or one without its proof.
			m.d.logf("scratch stop %q: the stop returned the state %q without a proof", lr.key, SafeText(string(e.Shown.State)))
			return uncertainOut(codeStopUnproven, msgStopUnproven)
		}
		lr.in.ext.record(leadcanary.Entry{Event: leadcanary.EventStop, Epoch: e.Epoch})
		return done(leadwire.StopResult{Role: lr.role.String(), State: string(e.Shown.State), Proof: e.Shown.Proof})
	})
}

// stopFailure chooses the status of a stop that returned an error.
func (m *leadMode) stopFailure(lr *leadRole, err error) outcome {
	m.d.logf("scratch stop %q: %s", lr.key, SafeText(err.Error()))
	switch {
	case m.registryDown(err):
		return uncertainOut(codeRegistryBroken, msgRegistryBroken)
	case errors.Is(err, leadlifecycle.ErrIntentPending):
		return unavailableOut(codeIntentPending, msgIntentPending)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return uncertainOut(codeDaemonStopping, msgDaemonStopping)
	case errors.Is(err, leadlifecycle.ErrStopUnproven):
		return uncertainOut(codeStopUnproven, msgStopUnproven)
	case errors.Is(err, leadregistry.ErrNoRole):
		return refusedOut(codeRoleNotDefined, msgNotDefined)
	case errors.Is(err, leadlifecycle.ErrNotManaged):
		return refusedOut(codeNotManaged, msgNotManaged)
	}
	return uncertainOut(codeFailed, msgFailedRead)
}

// ticket mints a ticket for the lead's epoch, one at a time for the role: the
// ticket file has one name.
func (m *leadMode) ticket(ctx context.Context, lr *leadRole, p leadwire.TicketParams) outcome {
	if leadActHook != nil {
		leadActHook("ticket")
	}
	if p.Epoch == 0 {
		return refusedOut(codeStaleEpoch, msgStaleEpoch)
	}
	lr.ticketMu.Lock()
	defer lr.ticketMu.Unlock()
	res, f := lr.in.ext.ticketFor(ctx, wire.Role(p.AttachRole), p.Epoch)
	if f != nil {
		return fromFail(f)
	}
	return done(leadwire.TicketResult{Role: res.Role, Epoch: res.Epoch, TicketFile: res.TicketFile, Folder: res.Folder})
}

// inject sends the one message of the lead's epoch. The order is the canary's,
// and the receipt comes last: the command is admitted (durably), the admission
// checks and the epoch are made, the frame is written, the inject-sent line of
// the evidence is written, and then the command is finished. The epoch's spent
// set (from the evidence, read when the daemon starts) stays the authority on
// whether a message was sent; the table never replaces it.
//
// The residual: a daemon killed after the frame is written and before the
// inject-sent line is, a window of a few milliseconds, leaves the epoch unspent
// after the restart, and the command's receipt reads uncertain. The owner reads
// the lead's screen before a second message.
func (m *leadMode) inject(ctx context.Context, lr *leadRole, p leadwire.InjectParams) outcome {
	if leadActHook != nil {
		leadActHook("inject")
	}
	if p.Epoch == 0 {
		return refusedOut(codeStaleEpoch, msgStaleEpoch)
	}
	res, f := lr.in.ext.injectFor(ctx, CanaryInjectParams{Role: p.Role, MessageID: p.MessageID, Body: p.Body}, p.Epoch)
	if f != nil {
		if f.code == "holder-"+wire.CodeWrongEpoch {
			// The role was stopped and started between the daemon's epoch
			// check and the holder's read of the frame: a mismatch of the
			// epoch, as the daemon's own check answers it. The text of the
			// refusal is the epoch's, and no text reached the lead.
			return refusedOut(codeStaleEpoch, msgStaleEpoch)
		}
		return fromFail(f)
	}
	return done(leadwire.InjectResult{
		Role: res.Role, Epoch: res.Epoch, MessageID: res.MessageID,
		Result: leadwire.Delivery{
			Outcome: string(res.Result.Outcome), Reason: string(res.Result.Reason),
			Written: string(res.Result.Written), Detail: res.Result.Detail,
		},
	})
}

// list answers lead.list: the cap, how many leads count against it and one row
// per configured lead. It takes no operation lock, so it answers while a define
// or a stop of a lead runs.
func (m *leadMode) list(params json.RawMessage) (any, *clientapi.ErrorBody) {
	if len(params) > 0 {
		var none struct{}
		if f := decodeStrict(params, &none); f != nil {
			return nil, f
		}
	}
	res := leadwire.ListResult{MaxRunning: m.maxRunning, Running: m.running(), Leads: make([]leadwire.Lead, 0, len(m.cm.router.order))}
	for _, role := range m.cm.router.order {
		res.Leads = append(res.Leads, m.row(m.roles[role]))
	}
	return res, nil
}

// row is one row of lead.list.
func (m *leadMode) row(lr *leadRole) leadwire.Lead {
	row := leadwire.Lead{
		Key: lr.key, Role: lr.role.String(), Inject: lr.cfg.Inject, Provision: m.provisionWord(lr),
	}
	if e, err := m.reg.Get(lr.role); err == nil {
		row.Defined = true
		row.State = string(e.Shown.State)
		row.Epoch = e.Epoch
		row.Unit = lr.role.UnitName()
		row.MessageSpent = e.Epoch != 0 && lr.in.ext.messageSpent(e.Epoch)
	}
	if rec, ok := m.table.Last(lr.role); ok {
		// The summary form of a receipt: no result, which the row's state, epoch
		// and unit say already, and which would not fit in the relay's size.
		rec.Result = nil
		w := toWire(rec)
		row.Last = &w
	}
	return row
}

// provisionWord is the row's provision word. A lead whose define is in flight
// in this daemon is never incomplete: its folders are being made.
func (m *leadMode) provisionWord(lr *leadRole) string {
	st, err := provision.ScratchStateOf(lr.cfg)
	if lr.defining.Load() && (err != nil || st != provision.StateDone) {
		return leadwire.ProvisionNone
	}
	switch {
	case err != nil:
		return leadwire.ProvisionIncomplete
	case st == provision.StateDone:
		return leadwire.ProvisionDone
	case st == provision.StateIncomplete:
		return leadwire.ProvisionIncomplete
	}
	return leadwire.ProvisionNone
}
