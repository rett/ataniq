//go:build linux && ataniq_canary

package leaddaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadcontrol"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
	"ataniq/internal/leadwire"
)

// The six operations end to end: define provisions the lead and defines it,
// start starts it, the ticket and the message go to the holder with the epoch
// that was named, stop proves it stopped, and lead.list shows each step.
func TestLeadFlow(t *testing.T) {
	l := newLeadEnv(t)
	l.start()

	row := l.row("alpha")
	if row.Defined || row.Provision != leadwire.ProvisionNone || row.State != "" || row.Last != nil {
		t.Fatalf("the row before define: %+v", row)
	}
	if res := l.list(); res.MaxRunning != 2 || res.Running != 0 || len(res.Leads) != 2 ||
		res.Leads[0].Key != "alpha" || res.Leads[1].Key != "beta" {
		t.Fatalf("the list before define: %+v", res)
	}

	// Start before define: no record, so no holder command.
	r := l.startLead("alpha")
	want(t, r, leadwire.StatusRefused, "not-provisioned")

	d := l.define("alpha")
	want(t, d, leadwire.StatusDone, "")
	var dr leadwire.DefineResult
	result(t, d, &dr)
	if dr.Role != roleOf("alpha") || dr.State != "stopped" || len(dr.ProfileDigest) != 64 {
		t.Fatalf("define result: %+v", dr)
	}
	row = l.row("alpha")
	if !row.Defined || row.Provision != leadwire.ProvisionDone || row.State != "stopped" ||
		row.Last == nil || row.Last.Op != leadwire.OpDefine || row.Last.Result != nil {
		t.Fatalf("the row after define: %+v", row)
	}
	// Defined again: skipped provisioning, the same digest.
	d2 := l.define("alpha")
	var dr2 leadwire.DefineResult
	result(t, d2, &dr2)
	if dr2.ProfileDigest != dr.ProfileDigest {
		t.Fatalf("the digest changed: %s then %s", dr.ProfileDigest, dr2.ProfileDigest)
	}

	h := l.holder("alpha", submitted)
	s := l.startLead("alpha")
	want(t, s, leadwire.StatusDone, "")
	var sr leadwire.StartResult
	result(t, s, &sr)
	if sr.Role != roleOf("alpha") || sr.Epoch != 1 || sr.Unit != "ataniq-lead-scratch-alpha.service" || sr.State != "starting" || sr.Waiver == "" {
		t.Fatalf("start result: %+v", sr)
	}
	// The holder was started with the digest of the record, and the epoch.
	specs := l.fake.StartedSpecs()
	if len(specs) != 1 || !argHas(specs[0].Argv, "--profile-digest", dr.ProfileDigest) || !argHas(specs[0].Argv, "--role", roleOf("alpha")) {
		t.Fatalf("the unit spec: %+v", specs)
	}
	if res := l.list(); res.Running != 1 {
		t.Fatalf("running %d after a start", res.Running)
	}

	tk := l.ticket("alpha", sr.Epoch, leadwire.AttachViewer)
	want(t, tk, leadwire.StatusDone, "")
	var tr leadwire.TicketResult
	result(t, tk, &tr)
	if tr.Epoch != sr.Epoch || tr.Role != roleOf("alpha") || filepath.Base(tr.TicketFile) != TicketFileName || tr.Folder != l.holderDir("alpha") {
		t.Fatalf("ticket result: %+v", tr)
	}
	ticketText, err := os.ReadFile(tr.TicketFile)
	if err != nil || len(ticketText) == 0 {
		t.Fatalf("the ticket file: %q %v", ticketText, err)
	}

	body := "please run the sample task\n"
	in := l.inject("alpha", sr.Epoch, "msg-one", body)
	want(t, in, leadwire.StatusDone, "")
	var ir leadwire.InjectResult
	result(t, in, &ir)
	if ir.Epoch != sr.Epoch || ir.MessageID != "msg-one" || ir.Result.Outcome != "submitted" || ir.Result.Written != "whole" {
		t.Fatalf("inject result: %+v", ir)
	}
	if row := l.row("alpha"); !row.MessageSpent || row.Epoch != sr.Epoch || row.State == "" {
		t.Fatalf("the row after the message: %+v", row)
	}
	// The ticket and the body are in no table line and no log line.
	table := l.tableBytes()
	for _, secret := range []string{string(ticketText), "please run the sample task"} {
		if bytes.Contains(table, []byte(secret)) || strings.Contains(l.log.String(), secret) {
			t.Fatalf("%q is in the table or the log", secret)
		}
	}
	// The holder got the frames with the lead- command ids and the epoch.
	for _, req := range h.requests("") {
		if req.Epoch != sr.Epoch || !strings.HasPrefix(req.CommandID, "lead-") {
			t.Fatalf("a holder request: %+v", req)
		}
	}

	st := l.stopLead("alpha", sr.Epoch)
	want(t, st, leadwire.StatusDone, "")
	var stop leadwire.StopResult
	result(t, st, &stop)
	if stop.State != "stopped" || stop.Proof == "" || stop.Role != roleOf("alpha") {
		t.Fatalf("stop result: %+v", stop)
	}
	if res := l.list(); res.Running != 0 {
		t.Fatalf("running %d after the stop", res.Running)
	}
	if got := strings.Join(l.evidenceOf("alpha"), " "); got != "daemon-start define define start ticket inject-sent inject-result stop" {
		t.Fatalf("evidence events: %s", got)
	}
}

func argHas(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// The same command id with the same payload returns the stored receipt and runs
// nothing, whatever the order of the keys in the request: the digest is made
// from the decoded params, never from the bytes. Anything else under the same id
// is a conflict, and a role that is not a lead is the error refused, with no
// receipt and no line in the table.
func TestLeadReplayConflictAndDigest(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	first := l.cmd(leadwire.OpDefine, "alpha", "replay-1", nil)
	want(t, first, leadwire.StatusDone, "")
	events := strings.Join(l.evidenceOf("alpha"), " ")

	var again leadwire.Receipt
	reordered := json.RawMessage(`{"role":"scratch-alpha/lead","command_id":"replay-1","client":"test-client"}`)
	if err := l.call(leadwire.OpDefine, reordered, &again); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Fatalf("the replay is not the stored receipt:\n%s\n%s", a, b)
	}
	if got := strings.Join(l.evidenceOf("alpha"), " "); got != events {
		t.Fatalf("a replay ran the define again: %s", got)
	}
	if first.Role != roleOf("alpha") || first.Op != leadwire.OpDefine || first.CommandID != "replay-1" {
		t.Fatalf("the receipt names %+v", first)
	}

	before := l.tableBytes()
	conflict := func(op string, p map[string]any) {
		t.Helper()
		err := l.call(op, p, &leadwire.Receipt{})
		ce := wantCode(t, err, "conflict")
		if ce.Message != msgConflict {
			t.Fatalf("conflict text %q", ce.Message)
		}
	}
	conflict(leadwire.OpDefine, params("beta", "replay-1", nil))
	conflict(leadwire.OpStart, params("alpha", "replay-1", nil))
	if !bytes.Equal(before, l.tableBytes()) {
		t.Fatal("a conflict wrote to the table")
	}

	// A stop for another epoch is another payload.
	stop := l.cmd(leadwire.OpStop, "alpha", "replay-2", map[string]any{"epoch": 0})
	_ = stop
	conflict(leadwire.OpStop, params("alpha", "replay-2", map[string]any{"epoch": 7}))

	// A role that is not a lead of the daemon: refused, nothing admitted.
	before = l.tableBytes()
	for _, role := range []string{"scratch-gamma/lead", "throwaway-one/lead", "Scratch-alpha/lead", "scratch-alpha", "", "scratch-alpha/lead "} {
		p := params("alpha", "other-1", nil)
		p["role"] = role
		err := l.call(leadwire.OpDefine, p, &leadwire.Receipt{})
		ce := wantCode(t, err, clientapi.ErrorCode(leadwire.ErrCodeRefused))
		if ce.Message != msgNotALead {
			t.Fatalf("role %q: %q", role, ce.Message)
		}
	}
	if !bytes.Equal(before, l.tableBytes()) {
		t.Fatal("a refused role wrote to the table")
	}
}

// Params that are not exactly the operation's are refused before anything is
// admitted.
func TestLeadParamsAreStrict(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	before := l.tableBytes()
	for name, p := range map[string]any{
		"no epoch":        json.RawMessage(`{"client":"c","command_id":"x1","role":"scratch-alpha/lead"}`),
		"extra key":       json.RawMessage(`{"client":"c","command_id":"x1","role":"scratch-alpha/lead","epoch":0,"more":1}`),
		"case variant":    json.RawMessage(`{"client":"c","command_id":"x1","Role":"scratch-alpha/lead","epoch":0}`),
		"null":            json.RawMessage(`{"client":"c","command_id":"x1","role":null,"epoch":0}`),
		"null epoch":      json.RawMessage(`{"client":"c","command_id":"x1","role":"scratch-alpha/lead","epoch":null}`),
		"negative epoch":  json.RawMessage(`{"client":"c","command_id":"x1","role":"scratch-alpha/lead","epoch":-1}`),
		"string epoch":    json.RawMessage(`{"client":"c","command_id":"x1","role":"scratch-alpha/lead","epoch":"1"}`),
		"empty client":    json.RawMessage(`{"client":"","command_id":"x1","role":"scratch-alpha/lead","epoch":0}`),
		"space in client": json.RawMessage(`{"client":"a b","command_id":"x1","role":"scratch-alpha/lead","epoch":0}`),
		"bad id":          json.RawMessage(`{"client":"c","command_id":"x 1","role":"scratch-alpha/lead","epoch":0}`),
		"long id":         json.RawMessage(`{"client":"c","command_id":"` + strings.Repeat("i", 65) + `","role":"scratch-alpha/lead","epoch":0}`),
	} {
		err := l.call(leadwire.OpStop, p, &leadwire.Receipt{})
		ce, ok := err.(*clientapi.Error)
		if !ok || ce.Code != clientapi.CodeInvalidRequest || ce.Message != msgBadParams {
			t.Errorf("%s: error %v, want invalid-request", name, err)
		}
	}
	// A ticket with a malformed attach role of a lead is invalid; of a role that
	// is not a lead, the refusal of the role.
	err := l.call(leadwire.OpTicket, params("alpha", "x2", map[string]any{"epoch": 1, "attach_role": "owner"}), &leadwire.Receipt{})
	wantCode(t, err, clientapi.CodeInvalidRequest)
	other := params("alpha", "x3", map[string]any{"epoch": 1, "attach_role": "owner"})
	other["role"] = "scratch-gamma/lead"
	err = l.call(leadwire.OpTicket, other, &leadwire.Receipt{})
	wantCode(t, err, clientapi.ErrorCode(leadwire.ErrCodeRefused))
	// lead.list takes no params, or an empty object, and nothing else.
	if err := l.call(leadwire.OpList, struct{}{}, &leadwire.ListResult{}); err != nil {
		t.Fatalf("list with {}: %v", err)
	}
	err = l.call(leadwire.OpList, map[string]any{"client": "c"}, &leadwire.ListResult{})
	wantCode(t, err, clientapi.CodeInvalidRequest)
	if !bytes.Equal(before, l.tableBytes()) {
		t.Fatal("a refused request wrote to the table")
	}
}

// Two connections start one role: one Start call. The second command finishes
// refused/role-busy and nothing queues; a replay of the first, while it runs, is
// answered at once with the receipt in progress.
func TestLeadTwoConnectionsStartOneRole(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)

	entered, release := make(chan struct{}, 4), make(chan struct{})
	l.gate.setHook(func(context.Context, leadunit.Spec) (leadunit.State, error, bool) {
		entered <- struct{}{}
		<-release
		return leadunit.State{}, nil, false
	})
	first := make(chan leadwire.Receipt, 1)
	go func() {
		var r leadwire.Receipt
		if err := l.call(leadwire.OpStart, params("alpha", "start-a", nil), &r); err != nil {
			t.Error(err)
		}
		first <- r
	}()
	<-entered

	second := l.cmd(leadwire.OpStart, "alpha", "start-b", nil)
	want(t, second, leadwire.StatusRefused, "role-busy")
	// Any other command of the role is refused the same way, a define too.
	want(t, l.define("alpha"), leadwire.StatusRefused, "role-busy")
	want(t, l.stopLead("alpha", 0), leadwire.StatusRefused, "role-busy")
	var replay leadwire.Receipt
	if err := l.call(leadwire.OpStart, params("alpha", "start-a", nil), &replay); err != nil {
		t.Fatal(err)
	}
	want(t, replay, leadwire.StatusInProgress, "")
	// The list does not wait for the role's lock.
	if row := l.row("alpha"); row.Last == nil || row.Last.Status != leadwire.StatusRefused {
		t.Fatalf("the row while the start runs: %+v", row.Last)
	}

	close(release)
	r := <-first
	want(t, r, leadwire.StatusDone, "")
	if n := l.gate.starts.Load(); n != 1 {
		t.Fatalf("%d Start calls, want 1", n)
	}
	var sr leadwire.StartResult
	result(t, r, &sr)
	// The second command's receipt is the same on a replay, and still refused.
	var again leadwire.Receipt
	if err := l.call(leadwire.OpStart, params("alpha", "start-b", nil), &again); err != nil {
		t.Fatal(err)
	}
	want(t, again, leadwire.StatusRefused, "role-busy")
}

// A daemon that was killed between the admission of a start and the return of
// the manager's Start leaves the command admitted and never finished. After the
// restart its replay is uncertain and the manager sees no second Start, and the
// lead is not wedged.
func TestLeadKilledBetweenAdmitAndStart(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)
	if err := l.running.stop(); err != nil {
		t.Fatal(err)
	}

	// What the dead daemon wrote: the admit line, with the digest of this
	// command, and nothing after it.
	tbl, err := leadcontrol.Open(filepath.Join(l.state, LeadCommandsName), leadcontrol.Options{})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := commandDigest(leadwire.OpStart, leadwire.StartParams{Role: roleOf("alpha")})
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := tbl.Admit(leadcontrol.Binding{
		Client: "test-client", ID: "killed-1", Op: leadwire.OpStart, Role: mustRole(t, roleOf("alpha")), Digest: digest,
	}); err != nil || !fresh {
		t.Fatalf("admit: %v %v", err, fresh)
	}
	if err := tbl.Close(); err != nil {
		t.Fatal(err)
	}

	l.start()
	var r leadwire.Receipt
	if err := l.call(leadwire.OpStart, params("alpha", "killed-1", nil), &r); err != nil {
		t.Fatal(err)
	}
	want(t, r, leadwire.StatusUncertain, leadcontrol.CodeInterrupted)
	if n := l.gate.starts.Load(); n != 0 {
		t.Fatalf("the replay started the lead: %d Start calls", n)
	}
	if got := len(l.fake.CallsOf(leadunit.MethodStart)); got != 0 {
		t.Fatalf("the fake saw %d Start calls", got)
	}
	if row := l.row("alpha"); row.Last == nil || row.Last.Status != leadwire.StatusUncertain {
		t.Fatalf("the row: %+v", row.Last)
	}
	// A new command id starts the lead.
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")
}

// The daemon is told to stop while the manager has the start request: the
// command is uncertain, never "unavailable", and after the restart the replay
// says so and nothing is started again.
func TestLeadShutdownDuringAStart(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)

	entered := make(chan struct{}, 1)
	l.gate.setHook(func(ctx context.Context, _ leadunit.Spec) (leadunit.State, error, bool) {
		entered <- struct{}{}
		<-ctx.Done()
		// As the manager answers a request whose context ended while the reply
		// was awaited: the reply is lost, the context says why.
		return leadunit.State{}, errors.Join(leadunit.ErrReplyLost, ctx.Err()), true
	})
	answered := make(chan leadwire.Receipt, 1)
	go func() {
		var r leadwire.Receipt
		if err := l.call(leadwire.OpStart, params("alpha", "start-j", nil), &r); err == nil {
			answered <- r
		}
		close(answered)
	}()
	<-entered
	if err := l.running.stop(); err != nil {
		t.Fatalf("the daemon did not stop clean: %v", err)
	}
	if r, ok := <-answered; ok && r.Status != leadwire.StatusUncertain {
		t.Fatalf("the answer at shutdown: %+v", r)
	}

	l.gate.setHook(nil)
	l.start()
	var r leadwire.Receipt
	if err := l.call(leadwire.OpStart, params("alpha", "start-j", nil), &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != leadwire.StatusUncertain {
		t.Fatalf("the replay after the restart: %+v", r)
	}
	if n := l.gate.starts.Load(); n != 1 {
		t.Fatalf("%d Start calls, want 1", n)
	}
}

// A start whose reply is lost leaves the lead unknown; it counts against
// max_running (a unit may run), and lead.stop still reaches the lifecycle for
// it: inside the late-start window the lifecycle cannot settle the intent, which
// is the answer unavailable/intent-pending, not a refusal.
func TestLeadUnknownCountsAndStopReachesTheLifecycle(t *testing.T) {
	l := newLeadEnv(t)
	l.w.Config.MaxRunning = 1
	l.resetScratch()
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	want(t, l.define("beta"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)
	l.holder("beta", submitted)

	l.gate.setHook(func(ctx context.Context, spec leadunit.Spec) (leadunit.State, error, bool) {
		return leadunit.State{}, errors.Join(leadunit.ErrReplyLost, context.DeadlineExceeded), true
	})
	r := l.startLead("alpha")
	want(t, r, leadwire.StatusUncertain, codeLaunchUncertain)
	if row := l.row("alpha"); row.State != "unknown" {
		t.Fatalf("the row: %+v", row)
	}
	l.gate.setHook(nil)
	if res := l.list(); res.Running != 1 {
		t.Fatalf("running %d with one unknown", res.Running)
	}
	// At the cap: the other lead is refused, with the text that says what frees
	// the place.
	b := l.startLead("beta")
	want(t, b, leadwire.StatusRefused, leadwire.CodeMaxRunning)
	if !strings.Contains(b.Message, "lead.stop") || !strings.Contains(b.Message, "starting") {
		t.Fatalf("the max-running text: %q", b.Message)
	}
	if got := len(l.fake.CallsOf(leadunit.MethodStart)); got != 0 {
		t.Fatalf("a refused start reached the manager: %d", got)
	}

	want(t, l.stopLead("alpha", 0), leadwire.StatusUnavailable, codeIntentPending)
	if got := len(l.fake.CallsOf(leadunit.MethodSignal)); got != 0 {
		t.Fatalf("a stop that cannot settle the intent signalled %d times", got)
	}
}

// A start that failed and left the role unknown (the manager refused it because
// the unit name is taken, and the lifecycle could not prove nothing was
// started) is uncertain, with the text that says the role is shown unknown, and
// not unavailable with the text "nothing was started". A start that failed and
// whose absence the lifecycle proved stays unavailable/start-failed.
func TestLeadStartFailedLeavingUnknownIsUncertain(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)

	l.gate.setHook(func(ctx context.Context, spec leadunit.Spec) (leadunit.State, error, bool) {
		return leadunit.State{}, leadunit.ErrUnitExists, true
	})
	r := l.startLead("alpha")
	want(t, r, leadwire.StatusUncertain, codeLaunchUncertain)
	if r.Message != msgLaunchUncertain {
		t.Fatalf("the text: %q", r.Message)
	}
	if row := l.row("alpha"); row.State != "unknown" {
		t.Fatalf("the row: %+v", row)
	}

	// A refused launch whose absence is proven is still unavailable, and the
	// lead is stopped again.
	l.gate.setHook(nil)
	want(t, l.stopLead("alpha", 0), leadwire.StatusDone, "")
	l.gate.setHook(func(ctx context.Context, spec leadunit.Spec) (leadunit.State, error, bool) {
		return leadunit.State{}, errors.New("the manager refused"), true
	})
	f := l.startLead("alpha")
	want(t, f, leadwire.StatusUnavailable, codeStartFailed)
	if row := l.row("alpha"); row.State != "stopped" {
		t.Fatalf("the row after a proven failed start: %+v", row)
	}
}

// A holder that refuses an inject with wrong-epoch (the role was stopped and
// started after the daemon's epoch check and before the holder read the frame)
// is answered like the daemon's own mismatch, refused/stale-epoch, and not
// unavailable/holder-wrong-epoch. The canary's operation keeps its answer.
func TestLeadInjectHolderWrongEpochIsStale(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	sr := l.up("alpha", respondWith(wire.Response{OK: false, Code: wire.CodeWrongEpoch}))
	r := l.inject("alpha", sr.Epoch, "msg-late", "body one")
	want(t, r, leadwire.StatusRefused, codeStaleEpoch)
	if r.Message != msgStaleEpoch {
		t.Fatalf("the text: %q", r.Message)
	}
}

// The cap counts every lead whose shown state is not desired, stopped or ended,
// so a lead that is stopping and one that is unknown both hold a place, and a
// lead with a pending intent or a start in flight does too. A start is reserved
// before the lifecycle's Start and released after it, and a place is counted
// once.
func TestLeadCapCountsByExclusion(t *testing.T) {
	w := provisiontest.NewScratch(shortTB{TB: t, dir: t.TempDir()})
	w.Config.Entries = append(w.Config.Entries, w.Config.Entries[0])
	w.Config.Entries[2].Key = "gamma"
	w.Config.Entries = append(w.Config.Entries, w.Config.Entries[0])
	w.Config.Entries[3].Key = "delta"
	w.Config.MaxRunning = 2
	m := bareMode(t, w)
	roleAt := func(key string) *leadRole { return m.roles[mustRole(t, roleOf(key))] }
	for _, key := range []string{"alpha", "beta", "gamma", "delta"} {
		if _, err := m.reg.Define(roleAt(key).role, leadregistry.ManagerAtaniq, profile()); err != nil {
			t.Fatal(err)
		}
	}
	launch := func(key string, shown leadregistry.State) {
		t.Helper()
		in, err := m.reg.BeginLaunchWith(roleAt(key).role, leadregistry.LaunchOptions{BootIdentity: "boot"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.reg.RecordOutcome(roleAt(key).role, in.ID, in.Epoch, leadregistry.OutcomeLaunched,
			leadregistry.UnitEvidence{InvocationID: "inv-" + key, ControlGroupPath: "/cg/" + key, ControlGroupID: "1", BootID: "boot"}); err != nil {
			t.Fatal(err)
		}
		if shown != leadregistry.StateStarting {
			if _, err := m.reg.SetState(roleAt(key).role, leadregistry.StateChange{Epoch: in.Epoch, State: shown,
				Evidence: leadregistry.EvidenceObservation, Reason: leadregistry.Reason{Code: "test-setup"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := m.running(); n != 0 {
		t.Fatalf("running %d with nothing started", n)
	}
	launch("alpha", leadregistry.StateStopping)
	if n := m.running(); n != 1 {
		t.Fatalf("running %d with one stopping", n)
	}
	launch("beta", leadregistry.StateUnknown)
	if n := m.running(); n != 2 {
		t.Fatalf("running %d with one stopping and one unknown", n)
	}
	if m.reserve(roleAt("gamma")) || m.reserve(roleAt("delta")) {
		t.Fatal("a start was reserved at the cap")
	}
	// A lead that is stopped is not counted, and a pending intent is.
	if _, err := m.reg.SetState(roleAt("alpha").role, leadregistry.StateChange{Epoch: 1, State: leadregistry.StateStopped,
		Evidence: leadregistry.EvidenceObservation, Reason: leadregistry.Reason{Code: "test-setup"}, Proof: "sample proof"}); err != nil {
		t.Fatal(err)
	}
	if n := m.running(); n != 1 {
		t.Fatalf("running %d after one stopped", n)
	}
	// One place left: one reservation fits, the second does not, and a lead
	// that is reserved is counted once.
	if !m.reserve(roleAt("gamma")) {
		t.Fatal("the start did not fit under the cap")
	}
	if m.reserve(roleAt("delta")) {
		t.Fatal("a second start fitted over the cap")
	}
	if n := m.running(); n != 2 {
		t.Fatalf("running %d with a reservation", n)
	}
	m.release(roleAt("gamma"))
	if n := m.running(); n != 1 {
		t.Fatalf("running %d after the release", n)
	}
	if _, err := m.reg.BeginLaunchWith(roleAt("delta").role, leadregistry.LaunchOptions{BootIdentity: "boot"}); err != nil {
		t.Fatal(err)
	}
	if n := m.running(); n != 2 {
		t.Fatalf("running %d with a pending intent", n)
	}
}

// A stop that names epoch N never stops epoch N+1: the role is stopped and
// started again, and the old stop, still wanting epoch 1, is refused with
// nothing signalled.
func TestLeadStopForAnOldEpochNeverStopsTheNext(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	one := l.up("alpha", submitted)
	want(t, l.stopLead("alpha", one.Epoch), leadwire.StatusDone, "")
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")
	row := l.row("alpha")
	if row.Epoch != one.Epoch+1 {
		t.Fatalf("epoch %d, want %d", row.Epoch, one.Epoch+1)
	}
	signals := len(l.fake.CallsOf(leadunit.MethodSignal))

	r := l.stopLead("alpha", one.Epoch)
	want(t, r, leadwire.StatusRefused, "stale-epoch")
	if got := len(l.fake.CallsOf(leadunit.MethodSignal)); got != signals {
		t.Fatalf("the stale stop signalled: %d calls, had %d", got, signals)
	}
	if !l.fake.Loaded("ataniq-lead-scratch-alpha.service") {
		t.Fatal("the unit of epoch 2 was stopped")
	}
	// Epoch 0 is the current epoch.
	st := l.stopLead("alpha", 0)
	want(t, st, leadwire.StatusDone, "")
	// A stale epoch of a role that is not defined is refused too.
	want(t, l.stopLead("beta", 5), leadwire.StatusRefused, codeRoleNotDefined)
}

// lead.stop always reaches the lifecycle: for a lead that is shown stopped, it
// asks the manager about the unit. It is done for a lead that is stopped with
// its proof, and for one defined and never started (epoch 0, no intent, no
// proof in the registry), where the lifecycle has just found no unit loaded and
// no launch was ever recorded, so a fixed proof text stands for it.
func TestLeadStopAlwaysCallsTheLifecycle(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	inspects := len(l.fake.CallsOf(leadunit.MethodInspect))
	// Shown stopped, never started: the lifecycle looks at the unit, and the
	// stop is done with the fixed proof, not uncertain.
	r := l.stopLead("alpha", 0)
	if len(l.fake.CallsOf(leadunit.MethodInspect)) <= inspects {
		t.Fatal("the stop did not reach the lifecycle")
	}
	want(t, r, leadwire.StatusDone, "")
	var never leadwire.StopResult
	result(t, r, &never)
	if never.State != "stopped" || never.Proof != msgProofNeverStarted {
		t.Fatalf("the stop of a lead never started: %+v", never)
	}
	// After a real stop the proof is there, and a second stop is done.
	l.holder("alpha", submitted)
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")
	want(t, l.stopLead("alpha", 0), leadwire.StatusDone, "")
	want(t, l.stopLead("alpha", 0), leadwire.StatusDone, "")
}

// A lead that the reconcile marked ended, with its proof, is a lead whose unit is
// proven gone: lead.stop answers done with that proof, on every press, and
// signals nothing. A stop that could not prove the unit gone is still
// uncertain (the stop-failure statuses cover that answer).
func TestLeadStopOfAnEndedLeadIsDone(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	up := l.up("alpha", submitted)
	if !l.fake.Crash(up.Unit) {
		t.Fatalf("the unit %q was not loaded", up.Unit)
	}
	l.restart()
	row := l.row("alpha")
	if row.State != "ended" {
		t.Fatalf("the row after the unit went away: %+v", row)
	}
	signals := len(l.fake.CallsOf(leadunit.MethodSignal))
	for i := 0; i < 2; i++ {
		r := l.stopLead("alpha", 0)
		want(t, r, leadwire.StatusDone, "")
		var res leadwire.StopResult
		result(t, r, &res)
		if res.State != "ended" || res.Proof == "" {
			t.Fatalf("stop %d of an ended lead: %+v", i, res)
		}
	}
	if got := len(l.fake.CallsOf(leadunit.MethodSignal)); got != signals {
		t.Fatalf("the stop of an ended lead signalled: %d calls, had %d", got, signals)
	}
}

// An inject that names an epoch that is not the lead's writes no frame and spends
// nothing; epoch 0 is refused too. The right epoch goes through and spends the
// epoch, a new id then finishes refused/second-message, the spent set survives a
// restart of the daemon, and the message of a replayed id is not sent again.
func TestLeadInjectEpochAndTheOneMessage(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	sr := l.up("alpha", submitted)
	h := l.holders["alpha"]

	want(t, l.inject("alpha", sr.Epoch+1, "msg-stale", "body one"), leadwire.StatusRefused, "stale-epoch")
	want(t, l.inject("alpha", 0, "msg-zero", "body one"), leadwire.StatusRefused, "stale-epoch")
	want(t, l.ticket("alpha", sr.Epoch+1, leadwire.AttachWriter), leadwire.StatusRefused, "stale-epoch")
	want(t, l.ticket("alpha", 0, leadwire.AttachWriter), leadwire.StatusRefused, "stale-epoch")
	if n := len(h.requests("")); n != 0 {
		t.Fatalf("%d frames reached the holder", n)
	}
	if row := l.row("alpha"); row.MessageSpent {
		t.Fatal("a refused message spent the epoch")
	}
	for _, ev := range l.evidenceOf("alpha") {
		if ev == "inject-sent" {
			t.Fatal("the evidence has an inject-sent line for a refused message")
		}
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-bad", ""), leadwire.StatusRefused, "empty-body")

	id := "inject-1"
	first := l.cmd(leadwire.OpInject, "alpha", id, map[string]any{"epoch": sr.Epoch, "message_id": "msg-one", "body": "body one"})
	want(t, first, leadwire.StatusDone, "")
	if row := l.row("alpha"); !row.MessageSpent {
		t.Fatal("the message is not shown spent")
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-two", "body two"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
	var replay leadwire.Receipt
	if err := l.call(leadwire.OpInject, params("alpha", id, map[string]any{"epoch": sr.Epoch, "message_id": "msg-one", "body": "body one"}), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Status != leadwire.StatusDone || !bytes.Equal(replay.Result, first.Result) {
		t.Fatalf("the replay: %+v", replay)
	}
	if n := len(h.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d inject frames", n)
	}

	l.restart()
	if row := l.row("alpha"); !row.MessageSpent || row.Epoch != sr.Epoch {
		t.Fatalf("the row after the restart: %+v", row)
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-three", "body three"), leadwire.StatusRefused, leadwire.CodeSecondMessage)
	if n := len(h.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d inject frames after the restart", n)
	}
}

// While a message of the lead is being delivered a new id finishes
// refused/message-in-flight, and the lead's other commands are not blocked by it.
func TestLeadInjectInFlight(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	sr := l.up("alpha", silent)
	_ = l.up("beta", submitted)

	done := make(chan leadwire.Receipt, 1)
	go func() {
		var r leadwire.Receipt
		if err := l.call(leadwire.OpInject, params("alpha", "slow-1", map[string]any{"epoch": sr.Epoch, "message_id": "msg-slow", "body": "body"}), &r); err != nil {
			t.Error(err)
		}
		done <- r
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(l.holders["alpha"].requests(wire.OpInject)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the frame did not reach the holder")
		}
		time.Sleep(5 * time.Millisecond)
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-fast", "body"), leadwire.StatusRefused, leadwire.CodeMessageInFlight)
	// Another lead's message goes at once.
	bs := l.row("beta")
	want(t, l.inject("beta", bs.Epoch, "msg-beta", "body"), leadwire.StatusDone, "")
	r := <-done
	want(t, r, leadwire.StatusDone, "")
	var ir leadwire.InjectResult
	result(t, r, &ir)
	if ir.Result.Outcome != "uncertain" {
		t.Fatalf("the result of a message nobody answered: %+v", ir.Result)
	}
}

// A define whose provisioning is done skips it: after a restart a lead that was
// provisioned and never defined (the daemon was killed between the record and
// the registry) is defined with the digest of its record, and the lead's folders
// are not touched.
func TestLeadDefineIsRepeatableAfterARestart(t *testing.T) {
	l := newLeadEnv(t)
	res := l.provision("alpha")
	l.start()
	row := l.row("alpha")
	if row.Defined || row.Provision != leadwire.ProvisionDone {
		t.Fatalf("the row of a provisioned lead that is not defined: %+v", row)
	}
	d := l.define("alpha")
	want(t, d, leadwire.StatusDone, "")
	var dr leadwire.DefineResult
	result(t, d, &dr)
	if dr.ProfileDigest != res.ProfileDigest {
		t.Fatalf("the digest %s, the record's %s", dr.ProfileDigest, res.ProfileDigest)
	}
	l.restart()
	if row := l.row("alpha"); !row.Defined || row.Provision != leadwire.ProvisionDone {
		t.Fatalf("the row after the restart: %+v", row)
	}
	d2 := l.define("alpha")
	want(t, d2, leadwire.StatusDone, "")
}

// A lead whose define was cut short (its folders without the record) is refused
// provision-incomplete, and nothing is deleted. Provisioning of an incomplete
// lead is "incomplete" in the list, except while a define of it runs here.
func TestLeadDefineOfAnIncompleteLeadDeletesNothing(t *testing.T) {
	l := newLeadEnv(t)
	cfg := l.cfgOf("alpha")
	layout, _, err := leadcanary.NamesFor(leadrole.ReservedScratch, cfg.Role)
	if err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(l.state, leadcanary.ScratchDirName, layout)
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	l.start()
	if row := l.row("alpha"); row.Provision != leadwire.ProvisionIncomplete {
		t.Fatalf("provision %q", row.Provision)
	}
	r := l.define("alpha")
	want(t, r, leadwire.StatusRefused, "provision-incomplete")
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("the define deleted the folder: %v", err)
	}
	if row := l.row("alpha"); row.Defined || row.Provision != leadwire.ProvisionIncomplete {
		t.Fatalf("the row: %+v", row)
	}
}

// While a define of a role runs in this daemon its folders are being made, so
// the list never calls the lead incomplete; once it ends the word is the file
// system's again.
func TestLeadListNeverCallsADefiningLeadIncomplete(t *testing.T) {
	w := provisiontest.NewScratch(shortTB{TB: t, dir: t.TempDir()})
	m := bareMode(t, w)
	lr := m.roles[mustRole(t, roleOf("alpha"))]
	layout, _, err := leadcanary.NamesFor(leadrole.ReservedScratch, lr.role)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(w.State, leadcanary.ScratchDirName, layout), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := m.provisionWord(lr); got != leadwire.ProvisionIncomplete {
		t.Fatalf("provision %q with folders and no record", got)
	}
	lr.defining.Store(true)
	if got := m.provisionWord(lr); got != leadwire.ProvisionNone {
		t.Fatalf("provision %q while a define runs", got)
	}
	lr.defining.Store(false)
	if got := m.provisionWord(lr); got != leadwire.ProvisionIncomplete {
		t.Fatalf("provision %q after the define ended", got)
	}
}

// A digest the owner pinned that is not the daemon's own computation refuses the
// define, before anything is made.
func TestLeadDefineChecksThePin(t *testing.T) {
	l := newLeadEnv(t)
	l.w.Config.Entries[0].ProfileDigest = strings.Repeat("ab", 32)
	l.resetScratch()
	l.start()
	r := l.define("alpha")
	want(t, r, leadwire.StatusRefused, "digest-pin")
	if row := l.row("alpha"); row.Defined || row.Provision != leadwire.ProvisionNone {
		t.Fatalf("the row: %+v", row)
	}
}

// After a restart the lead is started with the digest of its record: the holder
// command is built when the daemon starts, from the record, and never with the
// unpinned digest.
func TestLeadStartAfterARestartCarriesTheRecordsDigest(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	d := l.define("alpha")
	var dr leadwire.DefineResult
	result(t, d, &dr)
	if dr.ProfileDigest == strings.Repeat("0", 64) {
		t.Fatal("the unpinned digest")
	}
	l.restart()
	l.holder("alpha", submitted)
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")
	specs := l.fake.StartedSpecs()
	if len(specs) != 1 || !argHas(specs[0].Argv, "--profile-digest", dr.ProfileDigest) {
		t.Fatalf("the unit spec after the restart: %+v", specs)
	}
	for _, spec := range specs {
		if argHas(spec.Argv, "--profile-digest", strings.Repeat("0", 64)) {
			t.Fatal("a holder was started with the unpinned digest")
		}
	}
}

// A panic in the act finishes the command uncertain and frees the role: the next
// command of the role is not refused as busy.
func TestLeadPanicInTheActFinishesUncertainAndFreesTheRole(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)
	t.Cleanup(func() { leadActHook = nil })
	leadActHook = func(op string) {
		if op == "start" {
			panic("sample panic")
		}
	}
	r := l.startLead("alpha")
	want(t, r, leadwire.StatusUncertain, codePanic)
	if strings.Contains(l.log.String(), "sample panic") {
		t.Fatal("the panic's value is in the log")
	}
	if !strings.Contains(l.log.String(), "panicked") {
		t.Fatalf("the log does not say the command panicked:\n%s", l.log)
	}
	leadActHook = nil
	// The lock is free again, and the receipt is final.
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")
	if row := l.row("alpha"); row.Last == nil || row.Last.Status != leadwire.StatusDone {
		t.Fatalf("the row: %+v", row.Last)
	}
}

// lead.list does not wait for a define that runs, and a second define of the
// role is refused as busy; one define at a time per role.
func TestLeadListDoesNotWaitForADefine(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { leadActHook = nil })
	leadActHook = func(op string) {
		if op == "define" {
			entered <- struct{}{}
			<-release
		}
	}
	first := make(chan leadwire.Receipt, 1)
	go func() {
		var r leadwire.Receipt
		if err := l.call(leadwire.OpDefine, params("alpha", "def-a", nil), &r); err != nil {
			t.Error(err)
		}
		first <- r
	}()
	<-entered
	start := time.Now()
	res := l.list()
	if time.Since(start) > 2*time.Second || len(res.Leads) != 2 {
		t.Fatalf("the list took %v: %+v", time.Since(start), res)
	}
	want(t, l.define("alpha"), leadwire.StatusRefused, "role-busy")
	// Another lead's define is not blocked by it.
	close(release)
	want(t, <-first, leadwire.StatusDone, "")
	leadActHook = nil
	want(t, l.define("beta"), leadwire.StatusDone, "")
}

// The define and the start of one role together, with the race detector: the
// holder command and the digest are read and written under the role's
// operation lock, and every command ends in a status that says what happened.
func TestLeadDefineAndStartTogether(t *testing.T) {
	l := newLeadEnv(t)
	l.provision("alpha")
	l.start()
	l.holder("alpha", submitted)
	var wg sync.WaitGroup
	got := make([]leadwire.Receipt, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			op := leadwire.OpDefine
			if i%2 == 1 {
				op = leadwire.OpStart
			}
			var r leadwire.Receipt
			if err := l.call(op, params("alpha", l.idN(i), nil), &r); err != nil {
				t.Error(err)
			}
			got[i] = r
		}()
	}
	wg.Wait()
	for _, r := range got {
		switch {
		case r.Status == leadwire.StatusDone,
			r.Status == leadwire.StatusRefused && (r.Code == "role-busy" || r.Code == codeRoleNotDefined):
		default:
			t.Errorf("%s: %s %s %s", r.Op, r.Status, r.Code, r.Message)
		}
	}
}

// No lock shared by two roles is held across the lifecycle's Start: while one
// lead's start waits in the manager another lead is started, listed and stopped,
// and the start in flight is counted once against max_running, not twice (once
// by its reservation and once by the state the registry shows).
func TestLeadOtherRolesAreNotBlockedByAStartInFlight(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	want(t, l.define("beta"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)
	l.holder("beta", submitted)

	entered, release := make(chan struct{}, 1), make(chan struct{})
	alphaUnit := "ataniq-lead-scratch-alpha.service"
	l.gate.setHook(func(_ context.Context, spec leadunit.Spec) (leadunit.State, error, bool) {
		if spec.Unit == alphaUnit {
			entered <- struct{}{}
			<-release
		}
		return leadunit.State{}, nil, false
	})
	first := make(chan leadwire.Receipt, 1)
	go func() {
		var r leadwire.Receipt
		if err := l.call(leadwire.OpStart, params("alpha", "slow-start", nil), &r); err != nil {
			t.Error(err)
		}
		first <- r
	}()
	<-entered
	if res := l.list(); res.Running != 1 {
		t.Fatalf("running %d with one start in flight", res.Running)
	}
	want(t, l.startLead("beta"), leadwire.StatusDone, "")
	if res := l.list(); res.Running != 2 {
		t.Fatalf("running %d with two starts", res.Running)
	}
	want(t, l.stopLead("beta", 0), leadwire.StatusDone, "")
	close(release)
	want(t, <-first, leadwire.StatusDone, "")
}

// The status of a start or a stop that returned an error is chosen by what may
// have happened: refused only for a failure before the registry or the unit
// manager changed; what the lifecycle says is not known, a context that ended, a
// broken registry, and a stale epoch or a refused transition after the lifecycle
// acted are uncertain. A broken registry also stops the daemon from admitting
// more commands.
func TestStartAndStopFailureStatuses(t *testing.T) {
	cause := errors.New("sample cause /sample/path")
	type tc struct {
		err    error
		status leadcontrol.Status
		code   string
	}
	check := func(t *testing.T, name string, got outcome, c tc) {
		t.Helper()
		if got.status != c.status || got.code != c.code {
			t.Errorf("%s: %s %s (%s), want %s %s", name, got.status, got.code, got.msg, c.status, c.code)
		}
		if strings.Contains(got.msg, "sample") {
			t.Errorf("%s: the error's text is in the answer %q", name, got.msg)
		}
	}
	starts := map[string]tc{
		"launch uncertain":                  {fmt.Errorf("%w: %w", leadlifecycle.ErrLaunchUncertain, cause), leadcontrol.StatusUncertain, codeLaunchUncertain},
		"launch uncertain with the context": {errors.Join(leadlifecycle.ErrLaunchUncertain, context.Canceled), leadcontrol.StatusUncertain, codeLaunchUncertain},
		"context ended":                     {fmt.Errorf("sample: %w", context.Canceled), leadcontrol.StatusUncertain, codeDaemonStopping},
		"deadline":                          {context.DeadlineExceeded, leadcontrol.StatusUncertain, codeDaemonStopping},
		"start failed":                      {fmt.Errorf("%w: %w", leadlifecycle.ErrStartFailed, cause), leadcontrol.StatusUnavailable, codeStartFailed},
		"start failed, a foreign unit":      {errors.Join(fmt.Errorf("%w: %w", leadlifecycle.ErrStartFailed, cause), leadlifecycle.ErrForeignUnit), leadcontrol.StatusUnavailable, codeStartFailed},
		"stale epoch":                       {fmt.Errorf("sample: %w", leadregistry.ErrStaleEpoch), leadcontrol.StatusUncertain, codeRegistryCheck},
		"refused transition":                {leadregistry.ErrStateTransition, leadcontrol.StatusUncertain, codeRegistryCheck},
		"no role":                           {leadregistry.ErrNoRole, leadcontrol.StatusRefused, codeRoleNotDefined},
		"not managed":                       {leadlifecycle.ErrNotManaged, leadcontrol.StatusRefused, codeNotManaged},
		"preflight":                         {fmt.Errorf("%w: %w", leadlifecycle.ErrPreflight, cause), leadcontrol.StatusRefused, codePreflight},
		"foreign unit":                      {errors.Join(leadlifecycle.ErrUnitLoaded, leadlifecycle.ErrForeignUnit), leadcontrol.StatusRefused, codeForeignUnit},
		"unit loaded":                       {leadlifecycle.ErrUnitLoaded, leadcontrol.StatusRefused, codeUnitLoaded},
		"limits":                            {leadunit.ErrInvalidSpec, leadcontrol.StatusRefused, codeLimits},
		"launch refused":                    {fmt.Errorf("%w (shown state is ready)", leadregistry.ErrLaunchState), leadcontrol.StatusRefused, codeLaunchRefused},
		"launch pending":                    {leadregistry.ErrLaunchPending, leadcontrol.StatusRefused, codeLaunchRefused},
		"epoch exhausted":                   {leadregistry.ErrEpochExhausted, leadcontrol.StatusRefused, codeEpochExhausted},
		"no proof":                          {fmt.Errorf("%w: %w", leadlifecycle.ErrNoProof, cause), leadcontrol.StatusUnavailable, codeNoProof},
		"anything else":                     {cause, leadcontrol.StatusUncertain, codeFailed},
		"a registry failure":                {leadregistry.ErrBroken, leadcontrol.StatusUncertain, codeRegistryBroken},
	}
	stops := map[string]tc{
		"intent pending":                 {fmt.Errorf("%w: sample", leadlifecycle.ErrIntentPending), leadcontrol.StatusUnavailable, codeIntentPending},
		"stop unproven":                  {fmt.Errorf("%w: %w", leadlifecycle.ErrStopUnproven, cause), leadcontrol.StatusUncertain, codeStopUnproven},
		"stop unproven with the context": {errors.Join(leadlifecycle.ErrStopUnproven, context.Canceled), leadcontrol.StatusUncertain, codeDaemonStopping},
		"context ended":                  {context.Canceled, leadcontrol.StatusUncertain, codeDaemonStopping},
		"no role":                        {leadregistry.ErrNoRole, leadcontrol.StatusRefused, codeRoleNotDefined},
		"not managed":                    {leadlifecycle.ErrNotManaged, leadcontrol.StatusRefused, codeNotManaged},
		"coordinators":                   {leadlifecycle.ErrCoordinatorNotStopped, leadcontrol.StatusUncertain, codeFailed},
		"anything else":                  {cause, leadcontrol.StatusUncertain, codeFailed},
		"a registry failure":             {fmt.Errorf("sample: %w", leadregistry.ErrBroken), leadcontrol.StatusUncertain, codeRegistryBroken},
	}
	for name, c := range starts {
		m := bareMode(t, provisiontest.NewScratch(shortTB{TB: t, dir: t.TempDir()}))
		check(t, "start/"+name, m.startFailure(m.roles[mustRole(t, roleOf("alpha"))], c.err), c)
		if want := c.code == codeRegistryBroken; m.broken.Load() != want {
			t.Errorf("start/%s: broken %v", name, m.broken.Load())
		}
	}
	for name, c := range stops {
		m := bareMode(t, provisiontest.NewScratch(shortTB{TB: t, dir: t.TempDir()}))
		check(t, "stop/"+name, m.stopFailure(m.roles[mustRole(t, roleOf("alpha"))], c.err), c)
		if want := c.code == codeRegistryBroken; m.broken.Load() != want {
			t.Errorf("stop/%s: broken %v", name, m.broken.Load())
		}
	}
}

// Provisioning has a bound of its own: a define that runs out of it is
// unavailable, not done, and the daemon goes on.
func TestLeadDefineIsBounded(t *testing.T) {
	if DefaultProvisionTimeout < time.Minute {
		t.Fatalf("the default bound is %v", DefaultProvisionTimeout)
	}
	l := newLeadEnv(t)
	l.scratch.ProvisionTimeout = time.Nanosecond
	l.start()
	r := l.define("alpha")
	want(t, r, leadwire.StatusUnavailable, codeProvisionFailed)
	if !strings.Contains(r.Message, "scratch-clean") {
		t.Fatalf("the text does not say what the owner does: %q", r.Message)
	}
	if res := l.list(); len(res.Leads) != 2 {
		t.Fatalf("the daemon does not answer after it: %+v", res)
	}
}
