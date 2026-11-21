//go:build linux && ataniq_ownerchannel

package leaddaemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/approval/mergebuild"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/intake/execconfig"
	"ataniq/internal/intake/verify"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadunit"
)

// These tests are the contract between the daemon's owner connection and the
// host's real side of a card: the real service, journal and adapter, the
// stand-in door, a temporary bare mirror read and written by the real git, and
// the daemon itself (leaddaemon.Run) on a private socket, spoken to over the
// client protocol as the console speaks it. The daemon's own tests use a fake
// service; the tests of package hostadapter drive the whole path from the
// console's model. What only this file shows is what the daemon does with the
// service's answers: which code each kind of failure takes on the wire, what
// the daemon starts and closes, and that a second service cannot open the same
// journal. The names hold OwnerChannel so that the DONE of the task runs them.
//
// The stand-in door is kinder than the real one, the suite is a fake and the
// peer's terminal is declared by a fake inspector: a case that passes here
// proves the host's side, and is run again at step 2b on a seated lead.

const (
	cProject  = "project-a"
	cTarget   = "refs/heads/develop"
	cFeature  = "refs/leads/role-a/heads/feature/x"
	cToken    = "h-contract-93a1"
	cClient   = "ataniq-console 0.0.1"
	cSession  = "0123456789abcdef"
	cNote     = "a note"
	cRequest  = "rq-contract-01"
	cTTY      = "/dev/pts/4"
	cMaxWait  = 30 * time.Second
	cEpoch    = 7
	cLeadRole = "role-a"
)

var cStart = time.Date(2026, 11, 20, 14, 2, 4, 0, time.UTC)

type cTTYInspector struct{}

func (cTTYInspector) Inspect(leaddaemon.PeerRef) (leaddaemon.PeerProcess, error) {
	return leaddaemon.PeerProcess{TTY: cTTY}, nil
}

type cSuite struct{}

func (cSuite) Run(context.Context, hostadapter.SuiteRequest) (hostadapter.SuiteResult, error) {
	return hostadapter.SuiteResult{Passed: true, Output: []byte("ok\n"), Started: cStart, Duration: time.Second}, nil
}

// cDoor is the stand-in with a switch that makes the call that hands over a
// decision fail, as a door that cannot be reached does.
type cDoor struct {
	*doortest.Door
	down atomic.Bool
}

func (d *cDoor) Decide(ctx context.Context, r door.Record) (door.Decided, error) {
	if d.down.Load() {
		return door.Decided{}, errors.New("the door is out of reach")
	}
	return d.Door.Decide(ctx, r)
}

type cEnder struct{ d *doortest.Door }

func (e cEnder) EndRequest(_ context.Context, id string) error { return e.d.Withdraw(id) }

type cLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *cLog) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *cLog) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// cWorld is one test's mirror, door, folders and daemon.
type cWorld struct {
	t       *testing.T
	git     gitenv.Git
	mirror  string
	idx     string
	env     []string
	clock   *doortest.Clock
	stand   *cDoor
	state   string // the owner channel's state folder
	drop    string
	base    string
	runtime string
	logs    *cLog
	counts  *leaddaemon.OwnerCounts

	target, feature string

	mu       sync.Mutex
	opened   *hostadapter.Opened
	builds   int
	closes   int
	fault    func(point string) error
	oc       *leaddaemon.OwnerChannel
	reqCount int
}

func newWorld(t *testing.T) *cWorld {
	t.Helper()
	g, err := gitenv.Find()
	if err != nil {
		t.Skipf("these tests need an installed git: %v", err)
	}
	base, err := os.MkdirTemp("", "cw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	w := &cWorld{t: t, git: g, base: base, clock: doortest.NewClock(cStart), logs: &cLog{}}
	w.mirror = filepath.Join(base, "mirror.git")
	w.idx = filepath.Join(base, "index")
	w.env = []string{
		"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_DIR=" + w.mirror, "GIT_INDEX_FILE=" + w.idx,
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
		"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700000000 +0000",
	}
	w.run("", "init", "--bare", "--quiet", "--template=", w.mirror)
	w.put("README.md", "readme\n")
	root := w.commit("root\n")
	w.put("target-side.txt", "target\n")
	w.target = w.commit("target moves\n", root)
	w.run("", "read-tree", root)
	w.put("feature.txt", "one\n")
	w.feature = w.commit("feature\n", root)
	w.run("", "update-ref", cTarget, w.target)
	w.run("", "update-ref", cFeature, w.feature)

	cfg := doortest.Sample(w.clock.Now)
	cfg.Command = "qargi"
	cfg.PlanToken = cToken
	w.stand = &cDoor{Door: doortest.New(cfg)}
	w.state = filepath.Join(base, "owner-state")
	w.drop = filepath.Join(base, "drop")
	for _, d := range []string{w.state, w.drop} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	w.runtime = filepath.Join(base, "run")
	w.oc = &leaddaemon.OwnerChannel{Build: w.build, Inspector: cTTYInspector{}, Log: func(f string, a ...any) { _, _ = fmt.Fprintf(w.logs, f+"\n", a...) }}
	w.counts = w.oc.Counts()
	return w
}

func (w *cWorld) run(stdin string, args ...string) string {
	w.t.Helper()
	cmd := exec.Command(w.git.Path, args...)
	cmd.Env = w.env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		w.t.Fatalf("git %q: %v", args, err)
	}
	return strings.TrimSuffix(string(out), "\n")
}

func (w *cWorld) put(path, content string) {
	w.t.Helper()
	id := w.run(content, "hash-object", "-w", "--stdin")
	w.run("", "update-index", "--add", "--cacheinfo", "100644,"+id+","+path)
}

func (w *cWorld) commit(msg string, parents ...string) string {
	w.t.Helper()
	tree := w.run("", "write-tree")
	args := []string{"commit-tree", tree, "-m", msg}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	return w.run("", args...)
}

func (w *cWorld) ref(name string) string {
	cmd := exec.Command(w.git.Path, "rev-parse", "--verify", "--quiet", name)
	cmd.Env = w.env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// build is the daemon's builder of the service: the real adapter on the
// owner folder, the stand-in door, the mirror and the drop folder.
func (w *cWorld) build() (leaddaemon.OwnerService, func() error, error) {
	o, err := hostadapter.Open(context.Background(), hostadapter.OpenConfig{
		Config: hostadapter.Config{
			Git: w.git, Suite: cSuite{}, Now: w.clock.Now,
			Projects: []hostadapter.Project{{
				Name: cProject, Mirror: w.mirror, Role: cLeadRole, Target: cTarget, Rules: execconfig.Default(),
				Identity: mergebuild.Identity{Name: "Host Identity", Email: "host@example.invalid"},
				Gates:    []verify.Gate{{Name: "suite", Argv: []string{"make", "test"}}},
			}},
		},
		StateDir: w.state, Door: w.stand, Ender: cEnder{w.stand.Door}, DropDir: w.drop, CollectProject: cProject, Manual: true,
		Service: ownerchannel.Config{Fault: w.faultHook},
	})
	if err != nil {
		return nil, nil, err
	}
	w.mu.Lock()
	w.opened = o
	w.builds++
	w.mu.Unlock()
	return o.Service, func() error {
		w.mu.Lock()
		w.closes++
		w.mu.Unlock()
		return o.Close()
	}, nil
}

// serve starts a daemon on the runtime folder and waits until it has built
// its service and listens. A daemon that ends first is the result: ended is
// then not nil and carries its error.
func (w *cWorld) serve(runtime string) (stop func(), ended <-chan error) {
	w.t.Helper()
	fake := leadunit.NewFake(leadunit.NewFakeClock(cStart))
	cfg := leaddaemon.Config{
		StateDir: filepath.Join(w.base, "daemon-state-"+filepath.Base(runtime)), RuntimeDir: runtime,
		NewManager: func(context.Context) (leadunit.Manager, error) { return fake, nil },
		Clock:      leadunit.NewFakeClock(cStart), EUID: os.Geteuid,
		BootID:  func() (string, error) { return fake.BootID(), nil },
		Version: "9.9.9-test", Log: w.logs, ServeHook: w.oc.Serve,
	}
	w.mu.Lock()
	builds0 := w.builds
	w.mu.Unlock()
	ready := make(chan string, 1)
	cfg.Ready = func(s string) { ready <- s }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- leaddaemon.Run(ctx, cfg) }()
	deadline := time.After(cMaxWait)
	listening := false
	for {
		select {
		case <-ready:
			listening = true
		case err := <-done:
			cancel()
			out := make(chan error, 1)
			out <- err
			return func() {}, out
		case <-deadline:
			cancel()
			w.t.Fatalf("the daemon did not come up in 30 s\n%s", w.logs)
		case <-time.After(5 * time.Millisecond):
		}
		w.mu.Lock()
		built := w.builds > builds0
		w.mu.Unlock()
		if listening && built {
			break
		}
	}
	var once sync.Once
	finish := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(cMaxWait):
				w.t.Error("the daemon did not stop in 30 s")
			}
		})
	}
	w.t.Cleanup(finish)
	return finish, nil
}

// start runs the daemon on the world's runtime folder; it fails the test when
// the daemon does not come up.
func (w *cWorld) start() (stop func()) {
	w.t.Helper()
	stop, ended := w.serve(w.runtime)
	if ended != nil {
		w.t.Fatalf("the daemon ended at start: %v\n%s", <-ended, w.logs)
	}
	return stop
}

// ask drops a request for the feature and collects it, which verifies the
// merge and makes a card, and returns the card's id.
func (w *cWorld) ask() string {
	w.t.Helper()
	w.reqCount++
	id := fmt.Sprintf("rq-contract-%02d", w.reqCount)
	req, err := record.EncodeRequest(record.Request{
		Schema: record.SchemaRequest, RequestID: id, Epoch: cEpoch, Verb: record.VerbFeatureFinish, Argv: []string{"x"},
		FeatureSHA: w.feature, NoteSHA256: record.Digest([]byte(cNote)), At: record.FormatTime(w.clock.Now()),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.drop, id+".request"), req, 0o600); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.drop, id+".note"), []byte(cNote), 0o600); err != nil {
		w.t.Fatal(err)
	}
	w.mu.Lock()
	o := w.opened
	w.mu.Unlock()
	if n, err := o.Collector.Scan(context.Background()); err != nil || n != 1 {
		w.t.Fatalf("Scan took %d requests: %v", n, err)
	}
	st, ok := o.Service.Status(id)
	if !ok || st.PendingID == "" {
		w.t.Fatalf("the request made no card: %+v", st)
	}
	return st.PendingID
}

func (w *cWorld) pending(id string) record.Pending {
	w.t.Helper()
	shown, err := w.stand.Show(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	p, err := record.DecodePending(shown.Pending.Bytes)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *cWorld) state0(id string) record.State {
	w.t.Helper()
	shown, err := w.stand.Show(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return shown.Entry.State
}

// conn is a connection to the daemon.
type conn struct {
	t *testing.T
	c *clientapi.Client
}

func (w *cWorld) dial(decider bool) *conn {
	w.t.Helper()
	return w.dialAt(w.runtime, decider)
}

func (w *cWorld) dialAt(runtime string, decider bool) *conn {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cMaxWait)
	defer cancel()
	c, err := clientapi.Dial(ctx, clientapi.SocketPath(runtime))
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = c.Close() })
	k := &conn{t: w.t, c: c}
	if decider {
		var res ownerwire.DeciderResult
		if err := k.call(ownerwire.OpDecider, ownerwire.DeciderParams{Client: cClient, Session: cSession}, &res); err != nil || !res.Decider {
			w.t.Fatalf("owner.decider: %+v, %v", res, err)
		}
		var none json.RawMessage
		if err := k.call(ownerwire.OpBeat, nil, &none); err != nil {
			w.t.Fatalf("owner.beat: %v", err)
		}
	}
	return k
}

func (k *conn) call(op string, params, result any) error {
	k.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cMaxWait)
	defer cancel()
	return k.c.Call(ctx, op, params, result)
}

func codeOf(err error) string {
	var ce *clientapi.Error
	if errors.As(err, &ce) {
		return string(ce.Code)
	}
	return ""
}

func approveFrame(p record.Pending) ownerwire.DecisionFrame {
	f := ownerwire.DecisionFrame{ID: p.ID, BindingSHA256: p.BindingSHA256, Decision: record.DecisionApprove, SignedOff: []record.SignOff{}}
	for _, fl := range p.Binding.Summary.Flagged {
		f.SignedOff = append(f.SignedOff, record.SignOff{Kind: fl.Kind, Path: fl.Path})
	}
	return f
}

// journal reads the owner channel's journal, which the daemon must have
// closed.
func (w *cWorld) journal() []ownerjournal.Entry {
	w.t.Helper()
	j, err := ownerjournal.Open(filepath.Join(w.state, hostadapter.OwnerDirName, hostadapter.JournalName))
	if err != nil {
		w.t.Fatalf("the journal: %v", err)
	}
	defer func() { _ = j.Close() }()
	es, err := j.Entries(0, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	return es
}

// Every operation of the owner connection is answered by the real service
// and the answers read back through the wire package's strict decoders: the
// daemon's registration and the service's results agree on the form. A
// decision from the decider ends in the ref moving.
func TestOwnerChannelContractEveryOperationIsAnsweredByTheRealService(t *testing.T) {
	w := newWorld(t)
	stop := w.start()
	cid := w.ask()
	p := w.pending(cid)
	k := w.dial(true)

	var hello clientapi.HelloResult
	if err := k.call(clientapi.OpHello, nil, &hello); err != nil {
		t.Fatal(err)
	}
	for _, op := range leaddaemon.OwnerOperations() {
		found := false
		for _, have := range hello.Operations {
			found = found || have == op
		}
		if !found {
			t.Errorf("hello does not list %s: %v", op, hello.Operations)
		}
	}

	var raw json.RawMessage
	if err := k.call(ownerwire.OpList, nil, &raw); err != nil {
		t.Fatal(err)
	}
	l, err := ownerwire.DecodeListing(raw)
	if err != nil || len(l.Pending) != 1 || l.Pending[0].ID != cid {
		t.Fatalf("owner.list: %+v, %v", l.Pending, err)
	}
	raw = nil
	if err := k.call(ownerwire.OpShow, ownerwire.ShowParams{ID: cid}, &raw); err != nil {
		t.Fatal(err)
	}
	shown, err := ownerwire.DecodeShowResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := record.DecodePending(shown.Record.Bytes); err != nil || got.ID != cid || got.BindingSHA256 != p.BindingSHA256 {
		t.Errorf("owner.show: %v", err)
	}
	review := p.Binding.Summary.ReviewDigest
	raw = nil
	if err := k.call(ownerwire.OpPaths, ownerwire.PageParams{ID: cid, ReviewDigest: review, Page: 0}, &raw); err != nil {
		t.Fatal(err)
	}
	if pg, err := ownerwire.DecodePathsPage(raw); err != nil || pg.Pages != 1 || len(pg.Paths) != 1 || string(pg.Paths[0].Path) != "feature.txt" {
		t.Errorf("owner.paths: %+v, %v", pg, err)
	}
	raw = nil
	if err := k.call(ownerwire.OpPatch, ownerwire.PageParams{ID: cid, ReviewDigest: review, Page: 0}, &raw); err != nil {
		t.Fatal(err)
	}
	if pg, err := ownerwire.DecodePatchPage(raw); err != nil || len(pg.Lines) == 0 {
		t.Errorf("owner.patch: %+v, %v", pg, err)
	}
	raw = nil
	if err := k.call(ownerwire.OpLimit, ownerwire.LimitParams{Project: cProject}, &raw); err != nil {
		t.Fatal(err)
	}
	if v, err := ownerwire.DecodeLimitView(raw); err != nil || v.Project != cProject || v.Limit != 12 {
		t.Errorf("owner.limit: %+v, %v", v, err)
	}
	raw = nil
	if err := k.call(ownerwire.OpLimitSet, ownerwire.LimitSetParams{Project: cProject, To: 8}, &raw); err != nil {
		t.Fatal(err)
	}
	if v, err := ownerwire.DecodeLimitView(raw); err != nil || v.Limit != 8 {
		t.Errorf("owner.limit-set: %+v, %v", v, err)
	}
	var none json.RawMessage
	if err := k.call(ownerwire.OpLooked, nil, &none); err != nil {
		t.Errorf("owner.looked: %v", err)
	}
	raw = nil
	if err := k.call(ownerwire.OpDecide, approveFrame(p), &raw); err != nil {
		t.Fatal(err)
	}
	if res, err := ownerwire.DecodeDecideResult(raw); err != nil || res.State != record.StateApplied {
		t.Fatalf("owner.decide: %+v, %v", res, err)
	}
	merge := p.Binding.Merge.Commit
	if got := w.ref(cTarget); got != merge {
		t.Errorf("the target is at %s, want the merge %s", got, merge)
	}
	if got := w.state0(cid); got != record.StateApplied {
		t.Errorf("the door holds %s", got)
	}
	stop()
	// One build and one close for the one daemon.
	w.mu.Lock()
	builds, closes := w.builds, w.closes
	w.mu.Unlock()
	if builds != 1 || closes != 1 {
		t.Errorf("the daemon built the service %d times and closed it %d times, want 1 and 1", builds, closes)
	}
}

// What a failure of the service looks like on the wire, with the real service
// behind it: a connection that is not the decider is refused not-decider and
// the frame is journaled as refused; a door that cannot be reached is
// door-unavailable, with the decision journaled and delivered later; a
// service that stopped answers unavailable to every call, and the next
// daemon delivers the journaled decision once.
func TestOwnerChannelContractWhatEachFailureTakesOnTheWire(t *testing.T) {
	w := newWorld(t)
	stop := w.start()
	cid := w.ask()
	p := w.pending(cid)

	// A second console is read-only.
	first := w.dial(true)
	second := w.dial(false)
	var res ownerwire.DeciderResult
	if err := second.call(ownerwire.OpDecider, ownerwire.DeciderParams{Client: cClient, Session: "fedcba9876543210"}, &res); err != nil || res.Decider {
		t.Fatalf("the second console: %+v, %v", res, err)
	}
	var out ownerwire.DecideResult
	if err := second.call(ownerwire.OpDecide, approveFrame(p), &out); codeOf(err) != string(record.CodeNotDecider) {
		t.Fatalf("a decision from a console that is not the decider: %v, want not-decider", err)
	}
	if w.counts.DecisionNotDecider.Load() != 1 {
		t.Errorf("%d decisions from a connection that is not the decider counted", w.counts.DecisionNotDecider.Load())
	}
	if got := w.state0(cid); got != record.StatePending {
		t.Fatalf("the door holds %s after a refused frame", got)
	}

	// A door that cannot be reached.
	w.stand.down.Store(true)
	if err := first.call(ownerwire.OpDecide, approveFrame(p), &out); codeOf(err) != "door-unavailable" {
		t.Fatalf("a decision with the door down: %v, want door-unavailable", err)
	}
	if got := w.state0(cid); got != record.StatePending {
		t.Fatalf("the door holds %s while it was down", got)
	}
	w.stand.down.Store(false)
	// The decision was journaled, so the service hands it over on its tick.
	w.mu.Lock()
	o := w.opened
	w.mu.Unlock()
	if err := o.Service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.state0(cid); got != record.StateApplied {
		t.Fatalf("the door holds %s after the tick, want applied", got)
	}
	if got := w.ref(cTarget); got != p.Binding.Merge.Commit {
		t.Errorf("the target is at %s, want the merge", got)
	}
	stop()
	es := w.journal()
	kinds := map[ownerjournal.Kind]int{}
	for _, e := range es {
		kinds[e.Kind]++
		if e.Kind == ownerjournal.KindRefused && !strings.Contains(string(e.Body), string(record.CodeNotDecider)) {
			t.Errorf("a refused entry holds %s", e.Body)
		}
	}
	if kinds[ownerjournal.KindRefused] != 1 || kinds[ownerjournal.KindDecision] != 1 || kinds[ownerjournal.KindDelivered] != 1 || kinds[ownerjournal.KindOutcome] != 1 {
		t.Errorf("journal kinds %v: want one refusal, and one decision delivered once with its outcome", kinds)
	}
}

// A service that stopped at a step answers unavailable to every call while
// the daemon itself still answers hello; the next daemon, on the same state
// folder, finds the journaled decision, delivers it once and applies it, and
// it lists nothing twice.
func TestOwnerChannelContractAStoppedServiceIsUnavailableAndTheNextDaemonFinishesIt(t *testing.T) {
	w := newWorld(t)
	var fire atomic.Bool
	w.mu.Lock()
	w.fault = func(point string) error {
		if point == "door-decided" && fire.CompareAndSwap(false, true) {
			return errors.New("stopped here")
		}
		return nil
	}
	w.mu.Unlock()
	stop := w.start()
	cid := w.ask()
	p := w.pending(cid)
	k := w.dial(true)
	var out ownerwire.DecideResult
	if err := k.call(ownerwire.OpDecide, approveFrame(p), &out); codeOf(err) != "unavailable" {
		t.Fatalf("a decision that stopped the service: %v, want unavailable", err)
	}
	var raw json.RawMessage
	if err := k.call(ownerwire.OpList, nil, &raw); codeOf(err) != "unavailable" {
		t.Errorf("a listing from a stopped service: %v, want unavailable", err)
	}
	var hello clientapi.HelloResult
	if err := k.call(clientapi.OpHello, nil, &hello); err != nil {
		t.Errorf("hello from the daemon of a stopped service: %v", err)
	}
	stop()

	w.mu.Lock()
	w.fault = nil
	w.mu.Unlock()
	stop = w.start()
	defer stop()
	if got := w.state0(cid); got != record.StateApplied {
		t.Fatalf("the door holds %s, want applied by the next daemon", got)
	}
	if got := w.ref(cTarget); got != p.Binding.Merge.Commit {
		t.Errorf("the target is at %s", got)
	}
	k2 := w.dial(true)
	raw = nil
	if err := k2.call(ownerwire.OpList, nil, &raw); err != nil {
		t.Fatal(err)
	}
	if l, err := ownerwire.DecodeListing(raw); err != nil || len(l.Pending) != 0 {
		t.Errorf("the listing after the restart: %+v, %v", l.Pending, err)
	}
}

// Two daemons cannot share a journal: a second daemon, on a runtime folder of
// its own but the same owner state folder, ends at its serve step because the
// service cannot be built, and the first daemon goes on answering. When the
// first ends, a new one opens the same journal and lists the card again.
func TestOwnerChannelContractASecondServiceCannotOpenTheJournal(t *testing.T) {
	w := newWorld(t)
	stop := w.start()
	cid := w.ask()

	second := filepath.Join(w.base, "run2")
	stop2, ended := w.serve(second)
	if ended == nil {
		stop2()
		t.Fatalf("a second daemon came up on the same journal")
	}
	err := <-ended
	var se *leaddaemon.StartupError
	if !errors.As(err, &se) || se.Step != leaddaemon.StepServe {
		t.Errorf("the second daemon ended with %v, want a startup error at the serve step", err)
	}
	k := w.dial(true)
	var raw json.RawMessage
	if err := k.call(ownerwire.OpList, nil, &raw); err != nil {
		t.Fatalf("the first daemon stopped answering: %v", err)
	}
	if l, err := ownerwire.DecodeListing(raw); err != nil || len(l.Pending) != 1 {
		t.Errorf("the first daemon's listing: %+v, %v", l.Pending, err)
	}
	stop()

	stop = w.start()
	defer stop()
	k = w.dial(true)
	raw = nil
	if err := k.call(ownerwire.OpList, nil, &raw); err != nil {
		t.Fatal(err)
	}
	if l, err := ownerwire.DecodeListing(raw); err != nil || len(l.Pending) != 1 || l.Pending[0].ID != cid {
		t.Errorf("the listing after a restart: %+v, %v", l.Pending, err)
	}
}

// faultHook is the service's hook for a stop between two steps; the test
// sets w.fault.
func (w *cWorld) faultHook(point string) error {
	w.mu.Lock()
	f := w.fault
	w.mu.Unlock()
	if f == nil {
		return nil
	}
	return f(point)
}
