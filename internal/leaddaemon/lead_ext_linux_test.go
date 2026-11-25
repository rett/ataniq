//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadcontrol"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadwire"
)

// extFixture is the extension of the canary's one role over a registry that
// holds the role launched and shown ready, and a fake holder on the role's
// control socket. There is no daemon: the tests call the operations of the
// extension itself, with the epoch they expect.
type extFixture struct {
	*canaryEnv
	x      *canaryExt
	holder *fakeHolder
	epoch  uint64
	log    *strings.Builder
}

func newExtFixture(t *testing.T, answer holderAnswer, prefix string) *extFixture {
	t.Helper()
	c := newCanaryEnv(t)
	reg, err := leadregistry.Open(filepath.Join(c.state, RegistryDirName), leadregistry.Options{Now: c.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	if _, err := reg.Define(c.cfg.Role, leadregistry.ManagerAtaniq, profile()); err != nil {
		t.Fatal(err)
	}
	e := c.launch(reg, c.cfg.Role, leadregistry.StateReady)
	ev, err := leadcanary.OpenEvidence(c.cfg, c.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ev.Close() })
	adm, err := leadcanary.NewAdmission(c.cfg.Role)
	if err != nil {
		t.Fatal(err)
	}
	log := &strings.Builder{}
	f := &extFixture{canaryEnv: c, epoch: e.Epoch, log: log}
	f.x = &canaryExt{
		cfg: c.cfg, role: c.cfg.Role, holderDir: c.holderDir, reg: reg, ev: ev, adm: adm,
		dial: 2 * time.Second, wait: 300 * time.Millisecond, spent: map[uint64]bool{}, idPrefix: prefix,
		logf:  func(format string, a ...any) { fmt.Fprintf(log, format+"\n", a...) },
		build: sameHolderBuild(),
	}
	f.holder = newFakeHolder(t, c.holderDir, answer)
	return f
}

// An inject or a ticket that names an epoch that is not the role's is refused
// with the code stale-epoch before anything reaches the holder, and spends
// nothing. The expected epoch that matches reaches the holder in the frame.
func TestExpectedEpochOfInjectAndTicket(t *testing.T) {
	f := newExtFixture(t, submitted, "")
	ctx := context.Background()
	stale := f.epoch + 1

	if _, fl := f.x.injectFor(ctx, CanaryInjectParams{MessageID: "msg-one", Body: "body text"}, stale); fl == nil ||
		!fl.refused || fl.code != "stale-epoch" || fl.msg != msgStaleEpoch {
		t.Fatalf("inject with a stale epoch: %+v", fl)
	}
	if _, fl := f.x.ticketFor(ctx, wire.RoleViewer, stale); fl == nil || !fl.refused || fl.code != "stale-epoch" {
		t.Fatalf("ticket with a stale epoch: %+v", fl)
	}
	if n := len(f.holder.requests("")); n != 0 {
		t.Fatalf("a request reached the holder: %d", n)
	}
	if f.x.messageSpent(f.epoch) || f.x.messageSpent(stale) {
		t.Fatal("a refused message spent an epoch")
	}
	if ev := f.lastEvidence(); ev["event"] != "inject-refused" || ev["code"] != "stale-epoch" {
		t.Fatalf("the evidence's last line %v", ev)
	}

	// The right epoch goes through, and is the epoch of the frame.
	res, fl := f.x.injectFor(ctx, CanaryInjectParams{MessageID: "msg-one", Body: "body text"}, f.epoch)
	if fl != nil || res.Epoch != f.epoch {
		t.Fatalf("inject with the epoch: %+v %+v", res, fl)
	}
	if _, fl := f.x.ticketFor(ctx, wire.RoleWriter, f.epoch); fl != nil {
		t.Fatalf("ticket with the epoch: %+v", fl)
	}
	for _, r := range f.holder.requests("") {
		if r.Epoch != f.epoch {
			t.Errorf("the %s request carries epoch %d, want %d", r.Op, r.Epoch, f.epoch)
		}
	}
	// 0 is the current epoch for the canary's own calls.
	if _, fl := f.x.ticketFor(ctx, wire.RoleWriter, 0); fl != nil {
		t.Fatalf("ticket with epoch 0: %+v", fl)
	}
}

// The holder's own check of the epoch is the last guard: a role that was
// stopped and started between the check and the frame is refused by the holder,
// which the extension reports as a stale epoch for a ticket and as the holder's
// refusal for an inject (nothing was written to the lead).
func TestHolderWrongEpochIsTheLastGuard(t *testing.T) {
	f := newExtFixture(t, respondWith(wire.Response{OK: false, Code: wire.CodeWrongEpoch}), "")
	ctx := context.Background()
	if _, fl := f.x.ticketFor(ctx, wire.RoleViewer, f.epoch); fl == nil || !fl.refused || fl.code != "stale-epoch" {
		t.Fatalf("ticket: %+v", fl)
	}
	if _, fl := f.x.injectFor(ctx, CanaryInjectParams{MessageID: "msg-one", Body: "body text"}, f.epoch); fl == nil ||
		fl.refused || fl.code != "holder-wrong-epoch" {
		t.Fatalf("inject: %+v", fl)
	}
}

// The command ids the daemon gives the holder begin with the prefix of the
// mode: canary- by default, lead- for the lead operations, and they stay within
// the holder's bound.
func TestHolderCommandIDPrefix(t *testing.T) {
	for prefix, want := range map[string]string{"": "canary-", "lead-": "lead-"} {
		f := newExtFixture(t, submitted, prefix)
		if _, fl := f.x.ticketFor(context.Background(), wire.RoleViewer, f.epoch); fl != nil {
			t.Fatal(fl)
		}
		reqs := f.holder.requests(wire.OpTicket)
		if len(reqs) != 1 || !strings.HasPrefix(reqs[0].CommandID, want+"ticket-") {
			t.Fatalf("prefix %q: command ids %+v", prefix, reqs)
		}
		if len(reqs[0].CommandID) > wire.MaxCommandID {
			t.Fatalf("the command id %q is over the holder's bound", reqs[0].CommandID)
		}
	}
}

// A start that was handed to the manager when the daemon was told to stop is
// not answered as "the daemon is stopping": the error that says the outcome is
// not known comes first, whatever the context says.
func TestUncertainOutcomeComesBeforeTheContext(t *testing.T) {
	x := &canaryExt{logf: func(string, ...any) {}}
	for name, tc := range map[string]struct {
		err error
		msg string
	}{
		"launch": {fmt.Errorf("%w: %w", leadlifecycle.ErrLaunchUncertain, context.Canceled),
			"the start's outcome is not known; the role is shown unknown"},
		"stop": {errors.Join(leadlifecycle.ErrStopUnproven, context.Canceled),
			"the stop is not proven; the role is shown unknown"},
		"plain context": {context.Canceled, msgStopping},
	} {
		if got := x.failure("start", tc.err); got == nil || got.Message != tc.msg {
			t.Errorf("%s: answer %+v, want %q", name, got, tc.msg)
		}
	}
}

// A role of the router that has no holder command (a lead that is not
// provisioned) has none to give the lifecycle, and gets one that the router
// then uses; the swap is safe against the lifecycle's reads.
func TestRouterHolderCommandIsOptionalAndReplaceable(t *testing.T) {
	c := newCanaryEnv(t)
	r := newLeadRouter("not here")
	in := &leadInstance{cfg: c.cfg, preflight: &seamPreflight{}}
	if err := r.add(in); err != nil {
		t.Fatal(err)
	}
	role := c.cfg.Role
	if _, _, _, err := r.Build(role, leadregistry.Entry{Role: role}, leadregistry.Intent{}); !errors.Is(err, errNoHolder) {
		t.Fatalf("Build without a holder command: %v", err)
	}
	h := &seamHolder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			in.setHolderCommand(h)
			in.setHolderCommand(nil)
		}
	}()
	for i := 0; i < 200; i++ {
		_, _, _, _ = r.Build(role, leadregistry.Entry{Role: role}, leadregistry.Intent{})
	}
	<-done
	in.setHolderCommand(h)
	if _, _, _, err := r.Build(role, leadregistry.Entry{Role: role}, leadregistry.Intent{}); err != nil {
		t.Fatalf("Build with a holder command: %v", err)
	}
}

// A final answer the table refuses as not well formed, or too long, leaves the
// command open, so it is finished again with no result and a fixed code: the id
// is never left in progress. A command that was done reads uncertain then (it ran,
// and its answer is not kept); a refusal keeps its status.
func TestFinishFallsBackWhenTheTableRefusesTheAnswer(t *testing.T) {
	w := provisiontest.NewScratch(shortTB{TB: t, dir: t.TempDir()})
	m := bareMode(t, w)
	for name, tc := range map[string]struct {
		out  outcome
		want leadwire.Status
	}{
		"a text that is not printable": {refusedOut("sample-code", "bad\x01text"), leadwire.StatusRefused},
		"a code that is not a token":   {unavailableOut("Bad Code", "fine"), leadwire.StatusUnavailable},
		"a result that is too long": {done(leadwire.DefineResult{
			Role: roleOf("alpha"), State: "stopped", ProfileDigest: strings.Repeat("d", leadcontrol.MaxLineBytes),
		}), leadwire.StatusUncertain},
		"an uncertain answer that is invalid": {uncertainOut("x", strings.Repeat("m", 300)), leadwire.StatusUncertain},
	} {
		t.Run(name, func(t *testing.T) {
			adm := admit(t, m, "alpha", leadwire.OpStop, "fin-"+strings.ReplaceAll(name, " ", "-"))
			rec := m.finish(adm, tc.out)
			if rec.Status != tc.want || rec.Code != codeReceipt || rec.Message != msgReceiptNo || rec.Result != nil {
				t.Fatalf("receipt %+v, want %s %s", rec, tc.want, codeReceipt)
			}
			if rec.Status == leadwire.StatusInProgress || rec.FinishedAt == nil {
				t.Fatal("the command was left in progress")
			}
			if m.broken.Load() {
				t.Fatal("a refused answer broke the daemon")
			}
		})
	}
}

// A table that cannot be written (here: closed) breaks the daemon: the command
// that could not be finished is answered as it was admitted, the log says that
// the daemon must be restarted, a command that is new is refused, and a replay of
// a command that was admitted is still answered by the table. lead.list still
// answers.
func TestBrokenDaemonAdmitsNothingNew(t *testing.T) {
	w := provisiontest.NewScratch(shortTB{TB: t, dir: t.TempDir()})
	m := bareMode(t, w)
	x := &leadExt{m: m}
	ctx := context.Background()
	body := func(id string) json.RawMessage {
		return json.RawMessage(`{"client":"c","command_id":"` + id + `","role":"` + roleOf("alpha") + `"}`)
	}
	// A command that was finished before the table broke.
	got, f := x.Handle(ctx, leadwire.OpDefine, body("before-1"))
	if f != nil {
		t.Fatal(f)
	}
	stored := got.(leadwire.Receipt)
	if stored.Status != leadwire.StatusUnavailable && stored.Status != leadwire.StatusUncertain && stored.Status != leadwire.StatusRefused && stored.Status != leadwire.StatusDone {
		t.Fatalf("receipt %+v", stored)
	}
	// The registry is found broken: a command that is new finishes refused, the
	// stored receipt is still the answer to its own id.
	m.markBroken("the registry cannot be written")
	got, f = x.Handle(ctx, leadwire.OpDefine, body("after-1"))
	if f != nil {
		t.Fatal(f)
	}
	if r := got.(leadwire.Receipt); r.Status != leadwire.StatusRefused || r.Code != codeDaemonBroken || r.Message != msgDaemonBroken {
		t.Fatalf("a new command after the daemon broke: %+v", r)
	}
	again, f := x.Handle(ctx, leadwire.OpDefine, body("before-1"))
	if f != nil {
		t.Fatal(f)
	}
	if a, b := mustJSON(t, stored), mustJSON(t, again); a != b {
		t.Fatalf("the replay after the break:\n%s\n%s", a, b)
	}
	if res, f := x.Handle(ctx, leadwire.OpList, nil); f != nil || len(res.(leadwire.ListResult).Leads) != 2 {
		t.Fatalf("list: %v %v", res, f)
	}

	// The table itself: closed, it cannot admit, and the failure is the table's
	// own: unavailable, never a receipt, and the log says to restart.
	m2 := bareMode(t, provisiontest.NewScratch(shortTB{TB: t, dir: t.TempDir()}))
	x2 := &leadExt{m: m2}
	adm := admit(t, m2, "alpha", leadwire.OpStop, "open-1")
	if err := m2.table.Close(); err != nil {
		t.Fatal(err)
	}
	rec := m2.finish(adm, done(nil))
	if rec.Status != leadwire.StatusInProgress || !m2.broken.Load() {
		t.Fatalf("a command whose finish failed: %+v broken %v", rec, m2.broken.Load())
	}
	if _, f := x2.Handle(ctx, leadwire.OpDefine, body("closed-1")); f == nil || f.Code != CodeUnavailable || f.Message != msgTableNo {
		t.Fatalf("a command for a table that cannot take it: %+v", f)
	}
	if !strings.Contains(m2.d.log.(*syncBuffer).String(), "restarted") {
		t.Fatalf("the log does not say to restart:\n%s", m2.d.log.(*syncBuffer))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The digest is made of the operation and the decoded params with the client and
// the command id emptied: the same payload under another client and id has the
// same digest, any other difference (the operation, the role, the epoch, the
// body, which is hashed as sent) has another, and the order of the keys of the
// request does not matter.
func TestCommandDigest(t *testing.T) {
	digest := func(op, raw string) [32]byte {
		t.Helper()
		cmd, f := decodeCommand(op, json.RawMessage(raw))
		if f != nil {
			t.Fatalf("%s %s: %v", op, raw, f)
		}
		d, err := commandDigest(op, cmd.canon)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	base := digest(leadwire.OpInject, `{"client":"c1","command_id":"i1","role":"scratch-alpha/lead","epoch":3,"message_id":"m1","body":"a <b> & c"}`)
	if got := digest(leadwire.OpInject, `{"body":"a <b> & c","message_id":"m1","epoch":3,"role":"scratch-alpha/lead","command_id":"other-id","client":"other"}`); got != base {
		t.Error("the digest depends on the client, the command id or the order of the keys")
	}
	for name, raw := range map[string]string{
		"epoch":   `{"client":"c1","command_id":"i1","role":"scratch-alpha/lead","epoch":4,"message_id":"m1","body":"a <b> & c"}`,
		"role":    `{"client":"c1","command_id":"i1","role":"scratch-beta/lead","epoch":3,"message_id":"m1","body":"a <b> & c"}`,
		"body":    `{"client":"c1","command_id":"i1","role":"scratch-alpha/lead","epoch":3,"message_id":"m1","body":"a <b> & d"}`,
		"message": `{"client":"c1","command_id":"i1","role":"scratch-alpha/lead","epoch":3,"message_id":"m2","body":"a <b> & c"}`,
	} {
		if digest(leadwire.OpInject, raw) == base {
			t.Errorf("the digest does not depend on the %s", name)
		}
	}
	// The same params of two operations differ by the operation.
	if digest(leadwire.OpStart, `{"client":"c","command_id":"i","role":"scratch-alpha/lead"}`) ==
		digest(leadwire.OpDefine, `{"client":"c","command_id":"i","role":"scratch-alpha/lead"}`) {
		t.Error("the digest does not depend on the operation")
	}
}

// The words of every receipt and error: codes are [a-z][a-z0-9-]{0,63}, texts are
// printable ASCII of at most 256 bytes without <, > and &, which cost six bytes on
// the wire. Read from the sources, so that a constant added later is held to it.
func TestFixedWordsOfTheReceipts(t *testing.T) {
	code := regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	checkText := func(name, s string) {
		t.Helper()
		if len(s) > 256 || strings.ContainsAny(s, "<>&") {
			t.Errorf("%s: %q", name, s)
		}
		for i := 0; i < len(s); i++ {
			if s[i] < 0x20 || s[i] > 0x7e {
				t.Errorf("%s: %q is not printable ASCII", name, s)
				break
			}
		}
	}
	var codes, texts int
	for _, file := range []string{"lead_ext_linux.go", "canary_ext_linux.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					switch {
					case strings.HasPrefix(n.Name, "code"):
						codes++
						if !code.MatchString(s) {
							t.Errorf("%s: code %q", n.Name, s)
						}
					case strings.HasPrefix(n.Name, "msg"):
						texts++
						checkText(n.Name, s)
					}
				}
			}
		}
	}
	if codes < 20 || texts < 30 {
		t.Fatalf("read %d codes and %d texts: the sources were not read", codes, texts)
	}
	// The codes of leadwire, and the ones the canary's core makes inline.
	for _, c := range []string{
		leadwire.CodeMaxRunning, leadwire.CodeSecondMessage, leadwire.CodeMessageInFlight, leadwire.CodeStaleEpoch,
		"not-running", "inject-off", "holder-unreachable", "bad-message", "ticket-failed", "ticket-lost", "ticket-refused", "ticket-file",
		"holder-" + wire.CodeDisabled, "holder-" + wire.CodeWrongEpoch, "holder-" + wire.CodeBusy, "holder-" + wire.CodeBadRequest,
		string(leadcanary.CodeBadID), string(leadcanary.CodeBadTarget), string(leadcanary.CodeBodyTooLong),
		string(leadcanary.CodeInvalidUTF8), string(leadcanary.CodeBadCharacter), string(leadcanary.CodeEmptyBody),
		string(leadcanary.CodeForgedHeader), string(leadcanary.CodeBadBody), string(leadcanary.CodeBadText),
		leadcontrol.CodeInterrupted,
	} {
		if !code.MatchString(c) {
			t.Errorf("code %q", c)
		}
	}
	checkText("the admission text", "the message is refused ("+string(leadcanary.CodeForgedHeader)+")")
	checkText("the interrupted text", leadcontrol.MessageInterrupted)
}

// The largest answer of lead.list stays under the relay's size when marshalled as
// the server does: eight leads of the longest key, each with a latest receipt
// whose text is the longest the table keeps.
func TestLargestListStaysUnderTheRelay(t *testing.T) {
	w := provisiontest.NewScratch(shortTB{TB: t, dir: t.TempDir()})
	proto := w.Config.Entries[0]
	w.Config.Entries = nil
	for i := 0; i < leadwire.MaxLeads; i++ {
		e := proto
		e.Key = strings.Repeat(string(rune('a'+i)), leadwire.MaxKey)
		w.Config.Entries = append(w.Config.Entries, e)
	}
	m := bareMode(t, w)
	for _, role := range m.cm.router.order {
		lr := m.roles[role]
		if _, err := m.reg.Define(role, leadregistry.ManagerAtaniq, profile()); err != nil {
			t.Fatal(err)
		}
		adm, _, err := m.table.Admit(leadcontrol.Binding{Client: strings.Repeat("c", 64), ID: strings.Repeat(string(rune('a'+len(lr.key)%26)), 30) + lr.key[:20] + "x", Op: leadwire.OpInject, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.table.Finish(adm.CommandID, leadcontrol.StatusUncertain, strings.Repeat("c", 32), strings.Repeat("x", 256), nil); err != nil {
			t.Fatal(err)
		}
	}
	res, f := (&leadExt{m: m}).Handle(context.Background(), leadwire.OpList, nil)
	if f != nil {
		t.Fatal(f)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the largest lead.list is %d bytes", len(b))
	if len(b) >= leadwire.MaxRelayBytes {
		t.Fatalf("lead.list is %d bytes, want under %d", len(b), leadwire.MaxRelayBytes)
	}
	if n := len(res.(leadwire.ListResult).Leads); n != leadwire.MaxLeads {
		t.Fatalf("%d rows", n)
	}
}
