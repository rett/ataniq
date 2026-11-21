package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
	"ataniq/internal/consoleclient"
	"ataniq/internal/terminalconsole"
)

// These tests run the console that the command line makes with --daemon in
// every build, against the client protocol's own server on a private socket
// with the owner channel's operations answered by a fake daemon. They prove
// that the console, the owner client and the wire work together; the daemon
// itself is a fake, and nothing here qualifies the owner channel.

// ownerDaemon is a fake daemon with an owner channel that holds one card.
type ownerDaemon struct {
	t    *testing.T
	dir  string
	card ownerCard

	mu      sync.Mutex
	ops     []string
	frames  []string
	decided bool
}

type ownerCard struct {
	data []byte
	p    record.Pending
}

func (d *ownerDaemon) Hello(context.Context) clientapi.HelloResult {
	return clientapi.HelloResult{Version: "0.0.0-test"}
}

func (d *ownerDaemon) Status(context.Context) (clientapi.StatusResult, error) {
	return clientapi.StatusResult{}, nil
}

func (d *ownerDaemon) Operations() []string { return ownerwire.Operations() }
func (d *ownerDaemon) Mutating() bool       { return true }

func (d *ownerDaemon) Handle(_ context.Context, op string, params json.RawMessage) (any, *clientapi.ErrorBody) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ops = append(d.ops, op)
	enc := func(b []byte, err error) (any, *clientapi.ErrorBody) {
		if err != nil {
			d.t.Error(err)
			return nil, &clientapi.ErrorBody{Code: "internal", Message: "test"}
		}
		return json.RawMessage(b), nil
	}
	p := d.card.p
	switch op {
	case ownerwire.OpDecider:
		return enc(ownerwire.EncodeDeciderResult(ownerwire.DeciderResult{Decider: true}))
	case ownerwire.OpList:
		l := ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}}
		item := ownerwire.PendingItem{ID: p.ID, SHA256: record.Digest(d.card.data), Project: p.Binding.Project, Verb: p.Binding.Verb,
			Feature: p.Binding.Argv[0], RequestedAt: p.Binding.RequestedAt, PendingExpiresAt: p.Binding.PendingExpiresAt}
		if d.decided {
			l.Recent = append(l.Recent, ownerwire.RecentItem{ID: p.ID, Project: p.Binding.Project, Verb: p.Binding.Verb, Feature: p.Binding.Argv[0],
				State: record.StateApproved, Line: "approved by the fake daemon", At: p.Binding.RequestedAt})
		} else {
			l.Pending = append(l.Pending, item)
		}
		return enc(ownerwire.EncodeListing(l))
	case ownerwire.OpShow:
		r := ownerwire.ShowResult{Record: ownerwire.NewRecord(d.card.data)}
		if d.decided {
			r.Outcome = &ownerwire.Outcome{State: record.StateApproved, Line: "approved by the fake daemon"}
		}
		return enc(ownerwire.EncodeShowResult(r))
	case ownerwire.OpBeat, ownerwire.OpLooked:
		return json.RawMessage(ownerwire.EncodeNothing()), nil
	case ownerwire.OpDecide:
		d.frames = append(d.frames, string(params))
		d.decided = true
		return enc(ownerwire.EncodeDecideResult(ownerwire.DecideResult{State: record.StateApproved, Line: "approved by the fake daemon"}))
	}
	return nil, &clientapi.ErrorBody{Code: "internal", Message: "no such operation in the test"}
}

// startOwnerDaemon serves the fake on a private socket until the test ends.
func startOwnerDaemon(t *testing.T) *ownerDaemon {
	t.Helper()
	base, err := os.MkdirTemp("", "oc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	d := &ownerDaemon{t: t, dir: filepath.Join(base, "run"), card: makeOwnerCard(t)}
	l, _, err := clientapi.Listen(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- clientapi.Serve(ctx, l, d, clientapi.Options{Extra: []clientapi.Extension{d}}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return")
		}
	})
	return d
}

func oid(seed string) string { return record.Digest([]byte(seed))[:40] }

// makeOwnerCard writes a small valid pending record for a feature finish.
func makeOwnerCard(t *testing.T) ownerCard {
	t.Helper()
	asked := time.Now().UTC().Add(-5 * time.Minute)
	target, featureTip, merge := oid("target"), oid("feature"), oid("merge")
	review := record.Digest([]byte("review"))
	paths := []record.PathChange{{Path: "lib/a.go", Status: "M", OldMode: "100644", NewMode: "100644", Added: 3, Removed: 1, NewBytes: 90}}
	plan := "merge feature/small-fix into develop --no-ff\nresult built by the host\n"
	p := record.Pending{
		Schema: record.SchemaPending, ID: "ap-small-fix", Planner: "planner-a", RequestID: "rq-small-fix",
		RequestSHA256: record.Digest([]byte("request")),
		Binding: record.Binding{
			Project: "project-a",
			Subject: record.Subject{Kind: record.SubjectSeat, RoleName: "role-a", Epoch: 7, SeatRev: 12,
				Holder: &record.Holder{Kind: "lead", DisplayID: "role-a@e7", Runtime: "runtime-a"}},
			Verb: record.VerbFeatureFinish, Argv: []string{"small-fix"}, Class: record.ClassRefMove,
			PlanToken: "h-" + oid("token")[:8],
			Effects:   []record.Effect{{Kind: record.EffectRef, Ref: "refs/heads/develop", Old: target, New: merge}},
			Merge:     &record.Merge{Commit: merge, Parents: []string{target, featureTip}, BuiltBy: record.BuiltByHost},
			Summary: &record.Summary{ReviewDigest: review, Base: target, Tip: merge, Files: 1, Added: 3, Removed: 1, NewBytes: 90, Paths: paths,
				ExecConfigSet: record.ExecConfigSet{Matcher: "matcher-a", SHA256: record.Digest([]byte("rules"))}},
			Evidence:         []record.Evidence{{Kind: record.EvidenceSuite, RecordID: "suite-1", RecordSHA256: record.Digest([]byte("suite"))}},
			RequestedAt:      record.FormatTime(asked),
			PendingExpiresAt: record.FormatTime(asked.Add(24 * time.Hour)),
		},
		PlanText: record.Text(plan), Note: "a small fix",
	}
	p.Binding.PlanSHA256 = record.PlanDigest(plan)
	var err error
	if p.BindingSHA256, err = record.BindingDigest(p.Binding); err != nil {
		t.Fatal(err)
	}
	data, err := record.EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := record.DecodePending(data)
	if err != nil {
		t.Fatal(err)
	}
	return ownerCard{data: data, p: decoded}
}

func (d *ownerDaemon) sentOps() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ops...)
}

func (d *ownerDaemon) decideFrames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.frames...)
}

// clock is the console's clock in these tests: it moves only when told, so
// that the 400 ms of a new card and the gap between typed keys are not left
// to the machine.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func ownerConsole(t *testing.T, dir string, clk *clock) *viewDriver {
	t.Helper()
	cfg, _, ok := parse([]string{"--daemon", dir}, &bytes.Buffer{})
	if !ok {
		t.Fatal("parse")
	}
	cfg.options.Now = clk.Now
	cfg.options.Refresh = -1
	m, closeOwner, err := daemonConsole(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeOwner)
	d := newViewDriver(t, m)
	d.send(tea.WindowSizeMsg{Width: 140, Height: 45})
	return d
}

// startPlainDaemon serves a daemon of the release build: it answers hello and
// status and registers no owner operation.
func startPlainDaemon(t *testing.T) (dir string, plain *ownerDaemon) {
	t.Helper()
	base, err := os.MkdirTemp("", "oc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	dir = filepath.Join(base, "run")
	l, _, err := clientapi.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	plain = &ownerDaemon{t: t}
	go func() { done <- clientapi.Serve(ctx, l, plain, clientapi.Options{}) }()
	t.Cleanup(func() { cancel(); <-done })
	return dir, plain
}

// TestAConsoleWorksAgainstADaemonThatListsNoOwnerOperation holds what the
// console does in every build, which has the owner client, against the daemon
// of a release build: the console starts, the approvals screen gives the
// daemon's reason, no owner operation is sent, and the cockpit's approval
// controls say there is no owner channel and do not point at the approvals
// view.
func TestAConsoleWorksAgainstADaemonThatListsNoOwnerOperation(t *testing.T) {
	dir, plain := startPlainDaemon(t)
	clk := &clock{t: time.Now()}
	d := ownerConsole(t, dir, clk)
	d.send(tea.KeyPressMsg{Code: 'g', Text: "g"})
	screen := d.until("the reason on the approvals screen", func(v string) bool {
		return strings.Contains(v, "no owner channel in this build")
	})
	if strings.Contains(screen, "Pending (1)") {
		t.Fatal(screen)
	}
	if len(plain.sentOps()) != 0 {
		t.Fatalf("owner operations reached a daemon that has none: %v", plain.sentOps())
	}
	p := daemonProjection(consoleclient.Read(context.Background(), dir))
	approvals := 0
	for _, c := range p.Controls {
		if !c.Approval {
			continue
		}
		approvals++
		if !strings.Contains(c.Reason, noOwnerChannel) || strings.Contains(c.Reason, "approvals view") {
			t.Fatalf("%+v", c)
		}
	}
	if approvals == 0 || !strings.Contains(p.Gate.State, noOwnerChannel) || strings.Contains(p.Gate.State, "approvals view") {
		t.Fatalf("%d approval controls, gate %+v", approvals, p.Gate)
	}
	for _, c := range p.Contract {
		if c.Name == "Owner channel" && !strings.Contains(c.State, noOwnerChannel) {
			t.Fatalf("%+v", c)
		}
	}
}

func TestTheConsoleApprovesACardOverTheOwnerChannelOnce(t *testing.T) {
	daemon := startOwnerDaemon(t)
	clk := &clock{t: time.Now()}
	d := ownerConsole(t, daemon.dir, clk)
	press := func(k tea.KeyPressMsg) {
		t.Helper()
		clk.advance(100 * time.Millisecond)
		d.send(k)
	}
	ch := func(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

	press(ch('g'))
	d.until("the daemon's card in the list", func(v string) bool {
		return strings.Contains(v, "Pending (1)") && strings.Contains(v, "small-fix")
	})
	// The cockpit says the approvals are the daemon's, not "no owner channel".
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	d.until("the card", func(v string) bool { return strings.Contains(v, "APPROVAL ") && strings.Contains(v, "name=small-fix") })
	clk.advance(time.Second)
	for i := 0; i < 160; i++ {
		press(ch('j'))
	}
	press(ch('a'))
	view := d.until("the confirm field", func(v string) bool { return strings.Contains(v, "code> ____") })
	code := regexp.MustCompile(`APPROVAL ([A-Z0-9]{4}) `).FindStringSubmatch(strings.SplitN(view, "\n", 2)[0])
	if code == nil {
		t.Fatalf("no code in the title:\n%s", view)
	}
	for _, r := range strings.ToLower(code[1]) {
		press(ch(r))
	}
	if n := len(daemon.decideFrames()); n != 0 {
		t.Fatalf("a decision was sent before Enter: %d", n)
	}
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	d.until("the daemon's outcome line", func(v string) bool { return strings.Contains(v, "approved by the fake daemon") })

	frames := daemon.decideFrames()
	if len(frames) != 1 {
		t.Fatalf("%d decisions reached the daemon, want 1", len(frames))
	}
	frame, err := ownerwire.DecodeDecisionFrame([]byte(frames[0]))
	if err != nil {
		t.Fatal(err)
	}
	want := ownerwire.DecisionFrame{ID: daemon.card.p.ID, BindingSHA256: daemon.card.p.BindingSHA256, Decision: record.DecisionApprove, SignedOff: []record.SignOff{}}
	if frame.ID != want.ID || frame.BindingSHA256 != want.BindingSHA256 || frame.Decision != want.Decision || len(frame.SignedOff) != 0 || frame.Reason != "" {
		t.Fatalf("frame = %+v", frame)
	}
	// The confirm code never left the console.
	if strings.Contains(strings.ToUpper(frames[0]), code[1]) {
		t.Fatalf("the confirm code is in what was sent: %s", frames[0])
	}
	// The console took the presence beat to the daemon, in the daemon's own
	// terms (no time).
	if !contains(daemon.sentOps(), ownerwire.OpBeat) {
		t.Fatalf("no presence beat reached the daemon: %v", daemon.sentOps())
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestTheDaemonsApprovalsAreNotReadOnlyInTheCockpit(t *testing.T) {
	cfg, _, ok := parse([]string{"--daemon", t.TempDir()}, &bytes.Buffer{})
	if !ok {
		t.Fatal("parse")
	}
	m, closeOwner, err := daemonConsole(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeOwner()
	defer m.Close()
	// The model has a backend: the screen that says there is no owner
	// channel is not the one g opens.
	viewOf(m, 140, 45)
	m.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	v := viewOf(m, 140, 45)
	if !strings.Contains(v, "APPROVALS") || strings.Contains(v, terminalconsole.NoOwnerChannel) {
		t.Fatalf("the approvals screen is the one without a backend:\n%s", v)
	}
	// The cockpit's texts follow the daemon's hello: a daemon that lists the
	// owner channel makes the approval controls point at the approvals view.
	daemon := startOwnerDaemon(t)
	p := daemonProjection(consoleclient.Read(context.Background(), daemon.dir))
	approvals := 0
	for _, c := range p.Controls {
		if !c.Approval {
			continue
		}
		approvals++
		if strings.Contains(c.Reason, noOwnerChannel) || !strings.Contains(c.Reason, "approvals view with g") {
			t.Fatalf("%+v", c)
		}
	}
	if approvals == 0 || strings.Contains(p.Gate.State, noOwnerChannel) || !strings.Contains(p.Gate.State, "approvals view (g)") {
		t.Fatalf("%d approval controls, gate %+v", approvals, p.Gate)
	}
	// A result that did not come from a daemon that lists the channel does
	// not claim one.
	for _, r := range []consoleclient.Result{unavailableResult(), {State: consoleclient.StateOK}} {
		p := daemonProjection(r)
		if !strings.Contains(p.Gate.State, noOwnerChannel) {
			t.Fatalf("%+v", p.Gate)
		}
	}
}
