//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

func TestCanaryHelloListsTheOperations(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	ctx := t.Context()
	cl, err := clientapi.Dial(ctx, c.running.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	h, err := cl.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := append(clientapi.Operations(), CanaryOperations()...)
	if strings.Join(h.Operations, " ") != strings.Join(want, " ") || !h.Mutations {
		t.Fatalf("hello: operations %v mutations %v, want %v and true", h.Operations, h.Mutations, want)
	}
	if got := strings.Join(CanaryOperations(), " "); got != "canary.define canary.start canary.ticket canary.inject canary.stop" {
		t.Fatalf("the operations are %q", got)
	}
}

// TestCanaryFlow runs define, start, ticket, inject and stop, and checks the
// registry states, the control requests, the unit specification and the
// evidence lines of each.
func TestCanaryFlow(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	c.holder = newFakeHolder(t, c.holderDir, submitted)

	// define
	if d := c.define(); d.Role != canaryRoleText || d.State != "stopped" {
		t.Fatalf("define: %+v", d)
	}
	if state, reason, epoch, found := c.shown(); !found || state != "stopped" || reason != "defined" || epoch != 0 {
		t.Fatalf("after define: %q %q %d %v", state, reason, epoch, found)
	}
	if got := c.events(); strings.Join(got, " ") != "daemon-start define" {
		t.Fatalf("evidence events %v", got)
	}
	if m := c.lastEvidence(); m["qualification"] != leadcanary.QualificationText || m["login"] != leadcanary.LoginText || m["role"] != canaryRoleText || m["agent"] != "stand-in" {
		t.Fatalf("define line: %v", m)
	}

	// start
	s := c.startRole()
	if s.Role != canaryRoleText || s.State != "starting" || s.Epoch != 1 || s.Unit != "ataniq-lead-throwaway-one.service" || s.Waiver != canaryroot.WaiverText {
		t.Fatalf("start: %+v", s)
	}
	if state, _, epoch, _ := c.shown(); state != "starting" || epoch != 1 {
		t.Fatalf("after start: %q %d", state, epoch)
	}
	specs := c.fake.StartedSpecs()
	if len(specs) != 1 {
		t.Fatalf("%d units started", len(specs))
	}
	wantLimits := leadunit.Limits{MemoryMax: 4 << 30, SwapMax: 0, TasksMax: 512}
	if specs[0].Limits == nil || *specs[0].Limits != wantLimits {
		t.Fatalf("the unit specification carries limits %+v, want %+v", specs[0].Limits, wantLimits)
	}
	if got, ok := c.fake.UnitLimits(s.Unit); !ok || got != wantLimits {
		t.Fatalf("the unit has limits %+v (%v)", got, ok)
	}
	argv := strings.Join(specs[0].Argv, " ")
	for _, want := range []string{"/opt/sample/holder", "--role " + canaryRoleText, "--epoch 1", "--canary", "--inject",
		"--daemon-lock " + leadlock.DaemonPath(c.state), "--runtime-dir " + c.holderDir} {
		if !strings.Contains(argv, want) {
			t.Errorf("the holder command lacks %q: %s", want, argv)
		}
	}
	if m := c.lastEvidence(); m["event"] != "start" || m["epoch"] != float64(1) || m["waiver"] != canaryroot.WaiverText ||
		m["qualification"] != leadcanary.QualificationText || m["login"] != leadcanary.LoginText {
		t.Fatalf("start line: %v", m)
	}

	// ticket
	var tk CanaryTicketResult
	c.mustCall(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, &tk)
	reqs := c.holder.requests(wire.OpTicket)
	if len(reqs) != 1 {
		t.Fatalf("%d ticket requests", len(reqs))
	}
	r := reqs[0]
	if r.Epoch != 1 || r.Role != wire.RoleWriter || wire.CheckTicket(r.Ticket) != nil || r.CommandID == "" {
		t.Fatalf("the ticket request: %+v", r)
	}
	wantFile := filepath.Join(c.holderDir, "ticket")
	if tk.Role != canaryRoleText || tk.Epoch != 1 || tk.TicketFile != wantFile || tk.Folder != c.holderDir {
		t.Fatalf("ticket: %+v", tk)
	}
	fi, err := os.Lstat(wantFile)
	if err != nil || fi.Mode() != 0o600 {
		t.Fatalf("ticket file: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(wantFile); string(b) != r.Ticket {
		t.Fatalf("the ticket file holds %q, the holder was given %q", b, r.Ticket)
	}
	raw, _ := json.Marshal(tk)
	ev, _ := os.ReadFile(c.cfg.EvidencePath())
	for what, text := range map[string]string{"the answer": string(raw), "the evidence": string(ev), "the log": c.log.String()} {
		if strings.Contains(text, r.Ticket) {
			t.Errorf("the ticket is in %s", what)
		}
	}
	if m := c.lastEvidence(); m["event"] != "ticket" || m["epoch"] != float64(1) {
		t.Fatalf("ticket line: %v", m)
	}

	// inject
	res, err := c.inject("msg-one", "canary-token: sample-token-0123456789\n")
	if err != nil {
		t.Fatal(err)
	}
	want := wire.InjectResult{Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole, Detail: "sample detail"}
	if res.Role != canaryRoleText || res.Epoch != 1 || res.MessageID != "msg-one" || res.Result != want {
		t.Fatalf("inject: %+v", res)
	}
	reqs = c.holder.requests(wire.OpInject)
	if len(reqs) != 1 {
		t.Fatalf("%d inject requests", len(reqs))
	}
	adm, err := leadcanary.NewAdmission(c.cfg.Role)
	if err != nil {
		t.Fatal(err)
	}
	text, err := adm.Admit("msg-one", "canary-token: sample-token-0123456789\n")
	if err != nil {
		t.Fatal(err)
	}
	if r := reqs[0]; r.Epoch != 1 || r.MessageID != "msg-one" || r.Text != text || r.CommandID == "" ||
		!strings.HasPrefix(r.Text, "[ataniq-msg id=msg-one from=") {
		t.Fatalf("the inject request: %+v", r)
	}
	lines := c.evidence()
	sent, result := lines[len(lines)-2], lines[len(lines)-1]
	if sent["event"] != "inject-sent" || sent["message_id"] != "msg-one" || sent["epoch"] != float64(1) {
		t.Fatalf("inject-sent line: %v", sent)
	}
	if result["event"] != "inject-result" || result["outcome"] != "submitted" || result["written"] != "whole" ||
		result["message_id"] != "msg-one" || result["detail"] != "sample detail" {
		t.Fatalf("inject-result line: %v", result)
	}
	ev, _ = os.ReadFile(c.cfg.EvidencePath())
	if strings.Contains(string(ev), "sample-token") {
		t.Fatal("the body is in the evidence")
	}

	// a second message in the epoch
	_, err = c.inject("msg-two", "another message\n")
	wantCode(t, err, CodeRefused)
	if n := len(c.holder.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d inject requests after the second message", n)
	}
	if m := c.lastEvidence(); m["event"] != "inject-refused" || m["code"] != "second-message" || m["message_id"] != "msg-two" {
		t.Fatalf("second message line: %v", m)
	}

	// stop
	st := c.stopRole()
	if st.Role != canaryRoleText || st.State != "stopped" || st.Proof == "" {
		t.Fatalf("stop: %+v", st)
	}
	if state, reason, _, _ := c.shown(); state != "stopped" || reason != "stopped" {
		t.Fatalf("after stop: %q %q", state, reason)
	}
	if m := c.lastEvidence(); m["event"] != "stop" || m["epoch"] != float64(1) {
		t.Fatalf("stop line: %v", m)
	}

	// a second start of the stopped role: a new epoch, and its message is new
	s2 := c.startRole()
	if s2.Epoch != 2 || s2.State != "starting" {
		t.Fatalf("second start: %+v", s2)
	}
	if res, err = c.inject("msg-two", "another message\n"); err != nil || res.Epoch != 2 {
		t.Fatalf("inject in the second epoch: %+v %v", res, err)
	}
	if reqs = c.holder.requests(wire.OpInject); len(reqs) != 2 || reqs[1].Epoch != 2 || reqs[1].MessageID != "msg-two" {
		t.Fatalf("inject requests %+v", reqs)
	}
	if got := len(c.fake.StartedSpecs()); got != 2 {
		t.Fatalf("%d units started", got)
	}
	if spec := c.fake.StartedSpecs()[1]; spec.Epoch != 2 || spec.Limits == nil || *spec.Limits != wantLimits {
		t.Fatalf("second spec: %+v", spec)
	}
}

func TestCanaryDaemonStartIsFirstAndEvidenceIsPrivate(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	if got := c.events(); strings.Join(got, " ") != "daemon-start" {
		t.Fatalf("events %v", got)
	}
	fi, err := os.Lstat(c.cfg.EvidencePath())
	if err != nil || fi.Mode() != 0o600 {
		t.Fatalf("evidence file: %v %v", fi, err)
	}
}

func TestCanarySecondStartOfARunningRoleIsRefused(t *testing.T) {
	c := newCanaryEnv(t)
	c.up(submitted)
	var r CanaryStartResult
	err := c.call(OpCanaryStart, c.role(), &r)
	wantCode(t, err, CodeRefused)
	if _, _, epoch, _ := c.shown(); epoch != 1 {
		t.Fatalf("epoch %d", epoch)
	}
	if got := len(c.fake.StartedSpecs()); got != 1 {
		t.Fatalf("%d units started", got)
	}
}

func TestCanaryStartNeedsTheRuntimeFolder(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	c.define()
	if err := os.Remove(c.holderDir); err != nil {
		t.Fatal(err)
	}
	var r CanaryStartResult
	ce := wantCode(t, c.call(OpCanaryStart, c.role(), &r), CodeRefused)
	if ce.Message != msgFolder {
		t.Errorf("the refusal says %q, want %q", ce.Message, msgFolder)
	}
	if _, _, epoch, _ := c.shown(); epoch != 0 {
		t.Fatalf("epoch %d: the registry changed", epoch)
	}
	if len(c.fake.StartedSpecs()) != 0 {
		t.Fatal("a unit was started")
	}
}

// With the three folders that provisioning makes, the start goes on to the
// lifecycle: one unit is started, and the fixed refusal texts of the three
// folders are three different texts.
func TestCanaryStartWithTheFoldersOfProvisioning(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	c.define()
	var r CanaryStartResult
	if err := c.call(OpCanaryStart, c.role(), &r); err != nil {
		t.Fatal(err)
	}
	if r.State != "starting" || r.Epoch != 1 {
		t.Fatalf("the start answered %+v", r)
	}
	if got := len(c.fake.StartedSpecs()); got != 1 {
		t.Fatalf("%d units started, want 1", got)
	}
	if msgFolder == msgStateFolder || msgFolder == msgRoleFolder || msgStateFolder == msgRoleFolder {
		t.Fatal("two folders share a refusal text")
	}
}

// A config whose layout cannot be worked out (the daemon validates it when it
// starts, so this is not expected) does not make the start skip the holder's
// state folder: it is refused with the folder's fixed text, the path is not in
// the client's message, and the detail goes to the log.
func TestCanaryStartRefusesWhenTheLayoutCannotBeWorkedOut(t *testing.T) {
	role, err := leadrole.Parse(canaryRoleText)
	if err != nil {
		t.Fatal(err)
	}
	holderDir := filepath.Join(t.TempDir(), "holder-runtime")
	if err := os.Mkdir(holderDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	enrollTestProjects(t, state, role.Project())
	reg, err := leadregistry.Open(filepath.Join(state, RegistryDirName), leadregistry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestRegistry(t, reg)
	var logged []string
	x := &canaryExt{
		// A state root that is not an absolute path: Layout refuses it.
		reg:       reg,
		cfg:       leadcanary.Config{Role: role, StateRoot: "relative/state"},
		role:      role,
		holderDir: holderDir,
		logf:      func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) },
	}
	if _, err := x.cfg.Layout(); err == nil {
		t.Fatal("the layout of the bad config was accepted")
	}
	// A nil controller: a start that went on would panic on it.
	res, eb := x.start(context.Background())
	if res != nil || eb == nil || eb.Code != CodeRefused || eb.Message != msgStateFolder {
		t.Fatalf("start answered %v, %+v", res, eb)
	}
	if strings.Contains(eb.Message, "relative") {
		t.Fatalf("the refusal names a path: %q", eb.Message)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "the holder's state folder") {
		t.Fatalf("the log has %q", logged)
	}
}

// The start is refused, with nothing changed, for each folder that provisioning
// makes and the holder's unit needs before it writes anything. A holder whose
// role lock folder is missing ends at once with the reason in the journal only,
// and the daemon's start had answered that it was starting.
func TestCanaryStartNeedsTheFoldersOfTheHolder(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(c *canaryEnv) string
		text string
		// mode, when set, changes the folder's mode instead of removing it.
		mode os.FileMode
	}{
		{"holder state folder", func(c *canaryEnv) string {
			return filepath.Join(c.state, "leads", "throwaway-one-lead", "holder")
		}, msgStateFolder, 0},
		{"role lock folder", func(c *canaryEnv) string { return filepath.Join(c.state, "roles", "throwaway-one") }, msgRoleFolder, 0},
		{"role lock folder with a wrong mode", func(c *canaryEnv) string { return filepath.Join(c.state, "roles", "throwaway-one") }, msgRoleFolder, 0o755},
		{"holder state folder with a wrong mode", func(c *canaryEnv) string {
			return filepath.Join(c.state, "leads", "throwaway-one-lead", "holder")
		}, msgStateFolder, 0o750},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanaryEnv(t)
			c.start()
			c.define()
			dir := tc.path(c)
			if tc.mode != 0 {
				if err := os.Chmod(dir, tc.mode); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			var r CanaryStartResult
			ce := wantCode(t, c.call(OpCanaryStart, c.role(), &r), CodeRefused)
			if ce.Message != tc.text {
				t.Errorf("the refusal says %q, want %q", ce.Message, tc.text)
			}
			if _, _, epoch, _ := c.shown(); epoch != 0 {
				t.Fatalf("epoch %d: the registry changed", epoch)
			}
			if len(c.fake.StartedSpecs()) != 0 {
				t.Fatal("a unit was started")
			}
		})
	}
}

func TestCanaryRoleOutsideTheConfigIsRefused(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	c.holder = newFakeHolder(t, c.holderDir, submitted)
	before := len(c.fake.Calls())
	for _, role := range []string{"throwaway-two/lead", "other/lead", "supervisor", "throwaway-one/coord/f", ""} {
		for _, op := range CanaryOperations() {
			var params any = CanaryParams{Role: role}
			switch op {
			case OpCanaryTicket:
				params = CanaryTicketParams{Role: role, AttachRole: "writer"}
			case OpCanaryInject:
				params = CanaryInjectParams{Role: role, MessageID: "m", Body: "text"}
			}
			wantCode(t, c.call(op, params, nil), CodeRefused)
		}
	}
	if _, _, _, found := c.shown(); found {
		t.Fatal("a role was defined")
	}
	if n := len(c.holder.requests("")); n != 0 {
		t.Fatalf("%d requests reached the holder", n)
	}
	if after := len(c.fake.Calls()); after != before {
		t.Fatalf("the manager was called %d times", after-before)
	}
	if got := c.events(); strings.Join(got, " ") != "daemon-start" {
		t.Fatalf("events %v", got)
	}
}

func TestCanaryParamsAreStrict(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	for name, tc := range map[string]struct {
		op     string
		params any
	}{
		"no params":          {OpCanaryDefine, nil},
		"unknown member":     {OpCanaryDefine, map[string]any{"role": canaryRoleText, "extra": 1}},
		"other case":         {OpCanaryStart, map[string]any{"Role": canaryRoleText}},
		"wrong type":         {OpCanaryStop, map[string]any{"role": 5}},
		"ticket no role":     {OpCanaryTicket, map[string]any{"role": canaryRoleText}},
		"ticket bad role":    {OpCanaryTicket, map[string]any{"role": canaryRoleText, "attach_role": "admin"}},
		"ticket body":        {OpCanaryTicket, map[string]any{"role": canaryRoleText, "attach_role": "viewer", "body": "x"}},
		"inject unknown":     {OpCanaryInject, map[string]any{"role": canaryRoleText, "message_id": "m", "body": "b", "text": "x"}},
		"inject wrong type":  {OpCanaryInject, map[string]any{"role": canaryRoleText, "message_id": 1, "body": "b"}},
		"define with a body": {OpCanaryDefine, map[string]any{"role": canaryRoleText, "body": "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			wantCode(t, c.call(tc.op, tc.params, nil), clientapi.CodeInvalidRequest)
		})
	}
	if _, _, _, found := c.shown(); found {
		t.Fatal("a role was defined")
	}
}

func TestCanaryNothingRunsBeforeTheRoleIsStarted(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	c.holder = newFakeHolder(t, c.holderDir, submitted)
	var tk CanaryTicketResult
	wantCode(t, c.call(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, &tk), CodeRefused)
	_, err := c.inject("m", "text")
	wantCode(t, err, CodeRefused)
	if m := c.lastEvidence(); m["event"] != "inject-refused" || m["code"] != "not-running" {
		t.Fatalf("line: %v", m)
	}
	if n := len(c.holder.requests("")); n != 0 {
		t.Fatalf("%d requests reached the holder", n)
	}
	if _, err := os.Lstat(filepath.Join(c.holderDir, "ticket")); err == nil {
		t.Fatal("a ticket file was written")
	}
}

func TestCanaryTicketFileIsReplacedAndAHolderRefusalIsRefused(t *testing.T) {
	c := newCanaryEnv(t)
	c.up(submitted)
	var a, b CanaryTicketResult
	c.mustCall(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, &a)
	first, _ := os.ReadFile(a.TicketFile)
	c.mustCall(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "viewer"}, &b)
	second, _ := os.ReadFile(b.TicketFile)
	reqs := c.holder.requests(wire.OpTicket)
	if len(reqs) != 2 || reqs[0].Role != wire.RoleWriter || reqs[1].Role != wire.RoleViewer ||
		string(first) != reqs[0].Ticket || string(second) != reqs[1].Ticket || string(first) == string(second) {
		t.Fatalf("tickets %q %q, requests %+v", first, second, reqs)
	}
	entries, err := os.ReadDir(c.holderDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "control.sock ticket" {
		t.Fatalf("the folder holds %v", names)
	}

	// The holder says busy: the ticket is refused and the file stays as it was.
	c.holder.close()
	c.holder = newFakeHolder(t, c.holderDir, respondWith(wire.Response{Code: wire.CodeBusy, Text: "a writer is attached"}))
	err = c.call(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, nil)
	wantCode(t, err, CodeRefused)
	if now, _ := os.ReadFile(a.TicketFile); string(now) != string(second) {
		t.Fatalf("the ticket file changed to %q", now)
	}
	// No holder: unavailable.
	c.holder.close()
	err = c.call(OpCanaryTicket, CanaryTicketParams{Role: canaryRoleText, AttachRole: "writer"}, nil)
	wantCode(t, err, CodeUnavailable)
}

func TestCanaryInjectResults(t *testing.T) {
	notNow := wire.InjectResult{Outcome: wire.InjectOutcomeNotNow, Reason: wire.InjectReasonNotReady, Written: wire.InjectWrittenNothing, Detail: "not idle"}
	notSubmitted := wire.InjectResult{Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonNotSubmitted, Written: wire.InjectWrittenPartial}
	died := wire.InjectResult{Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonDied, Written: wire.InjectWrittenWhole, Detail: "gone"}
	untested := wire.InjectResult{Outcome: wire.InjectOutcomeRefused, Reason: wire.InjectReasonUntested, Written: wire.InjectWrittenNothing}
	for name, tc := range map[string]struct {
		res                  wire.InjectResult
		outcome, reason, wri string
	}{
		"not-now":       {notNow, "not-now", "not-ready", "nothing"},
		"not-submitted": {notSubmitted, "uncertain", "not-submitted", "partial"},
		"died":          {died, "uncertain", "died", "whole"},
		"untested":      {untested, "refused", "untested", "nothing"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCanaryEnv(t)
			res := tc.res
			c.up(respondWith(wire.Response{OK: true, Inject: &res}))
			got, err := c.inject("m-1", "body text\n")
			if err != nil || got.Result != tc.res {
				t.Fatalf("result %+v, error %v", got, err)
			}
			line := c.lastEvidence()
			if line["event"] != "inject-result" || line["outcome"] != tc.outcome || line["reason"] != tc.reason || line["written"] != tc.wri {
				t.Fatalf("line %v", line)
			}
			// Every result spends the epoch's message, not-now included.
			_, err = c.inject("m-2", "body text\n")
			wantCode(t, err, CodeRefused)
		})
	}
}

func TestCanaryHolderRefusalsAreUnavailableAndSpendTheMessage(t *testing.T) {
	for _, code := range []string{wire.CodeDisabled, wire.CodeWrongEpoch, wire.CodeBusy, wire.CodeBadRequest} {
		t.Run(code, func(t *testing.T) {
			c := newCanaryEnv(t)
			c.up(respondWith(wire.Response{Code: code, Text: "sample text"}))
			_, err := c.inject("m-1", "body text\n")
			ce := wantCode(t, err, CodeUnavailable)
			if strings.Contains(ce.Message, "sample text") {
				t.Fatalf("the holder's text is in the message %q", ce.Message)
			}
			lines := c.evidence()
			n := len(lines)
			if lines[n-2]["event"] != "inject-sent" || lines[n-1]["event"] != "inject-refused" || lines[n-1]["code"] != "holder-"+code {
				t.Fatalf("lines %v", lines[n-2:])
			}
			// The frame was written: the message is spent for the epoch.
			_, err = c.inject("m-2", "body text\n")
			wantCode(t, err, CodeRefused)
			if got := len(c.holder.requests(wire.OpInject)); got != 1 {
				t.Fatalf("%d requests reached the holder", got)
			}
		})
	}
}

func TestCanaryLostAnswersAreTheUncertainResult(t *testing.T) {
	for name, answer := range map[string]holderAnswer{
		"no answer":           silent,
		"closed connection":   closeAtOnce,
		"undecodable answer":  garbage,
		"ok without a result": respondWith(wire.Response{OK: true}),
		"unknown refusal":     respondWith(wire.Response{Code: "sample-code"}),
	} {
		t.Run(name, func(t *testing.T) {
			c := newCanaryEnv(t)
			c.up(answer)
			start := time.Now()
			got, err := c.inject("m-1", "body text\n")
			if err != nil {
				t.Fatalf("error %v: a lost answer is a result", err)
			}
			if want := leadcanary.NoAnswerResult(); got.Result != want {
				t.Fatalf("result %+v, want %+v", got.Result, want)
			}
			if name == "no answer" && time.Since(start) < 250*time.Millisecond {
				t.Fatalf("the daemon did not wait for the answer: %s", time.Since(start))
			}
			lines := c.evidence()
			n := len(lines)
			if lines[n-2]["event"] != "inject-sent" {
				t.Fatalf("lines %v", lines[n-2:])
			}
			if l := lines[n-1]; l["event"] != "inject-result" || l["outcome"] != "uncertain" || l["reason"] != "died" ||
				l["written"] != "unknown" || l["detail"] != "no answer from the holder" {
				t.Fatalf("line %v", l)
			}
			_, err = c.inject("m-2", "body text\n")
			wantCode(t, err, CodeRefused)
		})
	}
}

// A socket that cannot be dialled is unavailable, with the sent line written
// before the dial and the unsent line after it; the canary's message is spent
// all the same.
func TestCanaryNothingReachedTheHolderIsUnavailableAndSpendsTheMessage(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	c.define()
	c.startRole()
	// No fake holder is listening.
	_, err := c.inject("m-1", "body text\n")
	wantCode(t, err, CodeUnavailable)
	lines := c.evidence()
	n := len(lines)
	sent, unsent := lines[n-2], lines[n-1]
	if sent["event"] != "inject-sent" || sent["message_id"] != "m-1" || sent["effect_id"] == "" {
		t.Fatalf("line %v", sent)
	}
	if unsent["event"] != "inject-refused" || unsent["code"] != "unsent" || unsent["message_id"] != "m-1" || unsent["effect_id"] != sent["effect_id"] {
		t.Fatalf("line %v", unsent)
	}
	// The holder comes up: the canary's epoch takes no message.
	c.holder = newFakeHolder(t, c.holderDir, submitted)
	_, err = c.inject("m-2", "body text\n")
	if ce := wantCode(t, err, CodeRefused); ce.Message != msgSecond {
		t.Fatalf("message %q", ce.Message)
	}
	if n := len(c.holder.requests(wire.OpInject)); n != 0 {
		t.Fatalf("%d inject requests", n)
	}
}

func TestCanaryAdmissionRefusalsDoNotSpendTheMessage(t *testing.T) {
	c := newCanaryEnv(t)
	c.up(submitted)
	long := strings.Repeat("x", leadcanary.MaxBodyBytes+1)
	for name, tc := range map[string]struct {
		id, body, code string
	}{
		"bad id":        {"Bad ID", "text", "bad-id"},
		"empty id":      {"", "text", "bad-id"},
		"long body":     {"m-1", long, "body-too-long"},
		"forged header": {"m-1", "[ataniq-msg id=x from=y kind=z]\ntext", "forged-header"},
		"empty body":    {"m-1", "", "empty-body"},
		"control char":  {"m-1", "bad \x01 text", "bad-character"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.inject(tc.id, tc.body)
			ce := wantCode(t, err, CodeRefused)
			if !strings.Contains(ce.Message, tc.code) {
				t.Fatalf("message %q lacks %q", ce.Message, tc.code)
			}
			l := c.lastEvidence()
			if l["event"] != "inject-refused" || l["code"] != tc.code {
				t.Fatalf("line %v", l)
			}
			if _, has := l["message_id"]; has && tc.code == "bad-id" {
				t.Fatalf("a bad id is in the evidence: %v", l)
			}
		})
	}
	if n := len(c.holder.requests("")); n != 0 {
		t.Fatalf("%d requests reached the holder", n)
	}
	// The message is still unspent.
	got, err := c.inject("m-1", "text\n")
	if err != nil || got.Result.Outcome != wire.InjectOutcomeSubmitted {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCanaryOnlyOneMessageGoesAtOnce(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	c.define()
	c.startRole()
	release := make(chan struct{})
	c.holder = newFakeHolder(t, c.holderDir, func(req wire.Request, conn net.Conn) {
		<-release
		submitted(req, conn)
	})
	// Registered after the holder's own cleanup, so it runs before it: a test
	// that fails below still lets the holder go, and ends at once.
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	// The first message goes from another goroutine, which must not end the
	// test itself: it reports through the channel.
	socket := c.running.socket
	first := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cl, err := clientapi.Dial(ctx, socket)
		if err != nil {
			first <- err
			return
		}
		defer cl.Close()
		var r CanaryInjectResult
		first <- cl.Call(ctx, OpCanaryInject, CanaryInjectParams{Role: canaryRoleText, MessageID: "m-1", Body: "text\n"}, &r)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(c.holder.requests(wire.OpInject)) == 0 {
		select {
		case err := <-first:
			t.Fatalf("the first message ended before it reached the holder: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the first message did not arrive")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, err := c.inject("m-2", "text\n")
	wantCode(t, err, CodeRefused)
	letGo()
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the first message did not end")
	}
	if n := len(c.holder.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d requests", n)
	}
}
