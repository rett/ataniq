package ownerchannel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// The object ids of the record vectors: the target's tip, the feature's
// tip and the merge the host built from them.
const (
	baseID    = "93c8d68b93c8d68b93c8d68b93c8d68b93c8d68b"
	featureID = "8281895a8281895a8281895a8281895a8281895a"
	mergeID   = "0194dafc0194dafc0194dafc0194dafc0194dafc"
	targetRef = "refs/heads/develop"
)

var start = time.Date(2026, 11, 20, 14, 2, 4, 0, time.UTC)

// errInjected is what a fault hook answers to stop the service.
var errInjected = errors.New("injected fault")

// vectorPending reads the vectors' pending record, whose summary (one
// flagged path, a suite record) the fakes hand back as the host's findings.
func vectorPending(t testing.TB) record.Pending {
	t.Helper()
	b, err := os.ReadFile("../record/testdata/pending-valid.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := record.DecodePending(b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeIntake stands for the intake. Its mirror is a map of refs, so that an
// apply moves a ref and a second apply finds it moved.
type fakeIntake struct {
	mu sync.Mutex

	summary record.Summary
	refs    map[string]string

	resolveErr error
	resolves   int
	// featureTip is what Resolve answers for the feature's tip.
	featureTip string
	reviewErr  error
	tipErr     error
	freshErr   error
	// stale makes the evidence at a use not the newest.
	stale bool

	// applyErr is returned by Apply, with the outcome unknown.
	applyErr   error
	applies    int
	applied    map[string]int
	mirrorErr  error
	mirrorWord door.Outcome

	paths  []record.PathChange
	patch  []string
	digest string
}

func newIntake(t testing.TB) *fakeIntake {
	p := vectorPending(t)
	return &fakeIntake{
		summary:    *p.Binding.Summary,
		refs:       map[string]string{targetRef: baseID},
		applied:    map[string]int{},
		featureTip: featureID,
		digest:     p.Binding.Summary.ReviewDigest,
	}
}

func (f *fakeIntake) Resolve(_ context.Context, _ ResolveInput) (Resolved, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves++
	if f.resolveErr != nil {
		return Resolved{}, f.resolveErr
	}
	return Resolved{TargetRef: targetRef, TargetTip: f.refs[targetRef], FeatureTip: f.featureTip}, nil
}

func (f *fakeIntake) Review(_ context.Context, in ReviewInput) (record.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reviewErr != nil {
		return record.Summary{}, f.reviewErr
	}
	s := f.summary
	s.Base, s.Tip = in.Base, in.Merge
	return s, nil
}

func (f *fakeIntake) TargetTip(context.Context, string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tipErr != nil {
		return "", f.tipErr
	}
	return f.refs[targetRef], nil
}

func (f *fakeIntake) Paths(context.Context, ReviewRef) (ReviewPaths, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ReviewPaths{ReviewDigest: f.digest, Paths: f.paths}, nil
}

func (f *fakeIntake) Patch(context.Context, ReviewRef) (ReviewPatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ReviewPatch{ReviewDigest: f.digest, Lines: f.patch}, nil
}

func (f *fakeIntake) Fresh(_ context.Context, in FreshInput) (door.Fresh, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.freshErr != nil {
		return door.Fresh{}, f.freshErr
	}
	out := door.Fresh{}
	for _, e := range in.Pending.Binding.Effects {
		out.Refs = append(out.Refs, door.RefValue{Ref: e.Ref, Value: f.refs[e.Ref]})
	}
	if s := in.Pending.Binding.Summary; s != nil {
		out.ReviewDigest, out.RuleSetSHA256 = s.ReviewDigest, s.ExecConfigSet.SHA256
	}
	for _, e := range in.Pending.Binding.Evidence {
		out.Evidence = append(out.Evidence, door.EvidenceState{RecordID: e.RecordID, RecordSHA256: e.RecordSHA256, Newest: !f.stale})
	}
	return out, nil
}

func (f *fakeIntake) Apply(_ context.Context, in ApplyInput) (door.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applies++
	if f.applyErr != nil {
		return door.OutcomeUnknown, f.applyErr
	}
	out := door.OutcomeApplied
	for _, e := range in.Effects {
		if f.refs[e.Ref] != e.Old {
			return door.OutcomeNotApplied, nil
		}
	}
	for _, e := range in.Effects {
		f.refs[e.Ref] = e.New
		f.applied[e.Ref]++
	}
	return out, nil
}

func (f *fakeIntake) Mirror(_ context.Context, in ApplyInput) (door.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mirrorErr != nil {
		return "", f.mirrorErr
	}
	if f.mirrorWord != "" {
		return f.mirrorWord, nil
	}
	allNew, allOld := true, true
	for _, e := range in.Effects {
		allNew = allNew && f.refs[e.Ref] == e.New
		allOld = allOld && f.refs[e.Ref] == e.Old
	}
	switch {
	case allNew:
		return door.OutcomeApplied, nil
	case allOld:
		return door.OutcomeNotApplied, nil
	}
	return door.OutcomeUnknown, nil
}

func (f *fakeIntake) appliesSeen() (calls, moves int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applies, f.applied[targetRef]
}

func (f *fakeIntake) ref() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refs[targetRef]
}

// fakeMerger stands for the merge builder, with the pins it made.
type fakeMerger struct {
	mu   sync.Mutex
	pins map[string]string

	buildErr error
	// unclear makes Build return the merge and ErrPinUnclear, with the pin
	// made or not as pinAnyway says.
	unclear   bool
	pinAnyway bool
	builds    int
	inputs    []MergeInput
}

func newMerger() *fakeMerger { return &fakeMerger{pins: map[string]string{}} }

func (m *fakeMerger) Build(_ context.Context, in MergeInput) (BuiltMerge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.builds++
	m.inputs = append(m.inputs, in)
	if m.buildErr != nil {
		return BuiltMerge{}, m.buildErr
	}
	built := BuiltMerge{Commit: mergeID, Parents: [2]string{in.TargetTip, in.FeatureTip}}
	if m.unclear {
		if m.pinAnyway {
			m.pins[in.Key] = mergeID
		}
		return built, fmt.Errorf("%w: update-ref was killed", ErrPinUnclear)
	}
	m.pins[in.Key] = mergeID
	return built, nil
}

func (m *fakeMerger) Pinned(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pins[key], nil
}

func (m *fakeMerger) Unpin(_ context.Context, key, commit string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pins[key] != commit {
		return errors.New("fake merger: the pin holds another commit")
	}
	delete(m.pins, key)
	return nil
}

func (m *fakeMerger) pinCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pins)
}

// fakeVerifier stands for the suite run.
type fakeVerifier struct {
	mu       sync.Mutex
	result   Verification
	err      error
	running  int
	most     int
	runs     int
	onVerify func(ctx context.Context, in VerifyInput)
}

func newVerifier(t testing.TB) *fakeVerifier {
	p := vectorPending(t)
	return &fakeVerifier{result: Verification{Clean: true, Evidence: p.Binding.Evidence[0]}}
}

func (v *fakeVerifier) Verify(ctx context.Context, in VerifyInput) (Verification, error) {
	v.mu.Lock()
	v.running++
	v.runs++
	if v.running > v.most {
		v.most = v.running
	}
	hook, res, err := v.onVerify, v.result, v.err
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.running--
		v.mu.Unlock()
	}()
	if hook != nil {
		hook(ctx, in)
	}
	return res, err
}

// fakeMailbox stands for the lead's mailbox.
type fakeMailbox struct {
	mu      sync.Mutex
	replies []Reply
	err     error
}

func (m *fakeMailbox) Reply(_ context.Context, r Reply) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.replies = append(m.replies, r)
	return nil
}

func (m *fakeMailbox) all() []Reply {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Reply(nil), m.replies...)
}

// repliesFor returns the replies about one request.
func (m *fakeMailbox) repliesFor(requestID string) []Reply {
	var out []Reply
	for _, r := range m.all() {
		if r.RequestID == requestID {
			out = append(out, r)
		}
	}
	return out
}

// countingDoor counts the calls that matter on the stand-in.
type countingDoor struct {
	door.Door
	mu      sync.Mutex
	decides int
	uses    int
	settles int
	// failDecide makes Decide fail as a call, not as a refusal, and failList
	// does the same for List.
	failDecide error
	failList   error
	// lists counts the calls of List.
	lists int
	// failShow makes Show fail as a call, and refuseSettle makes the next
	// Settle answer with a refusal, once.
	failShow     error
	refuseSettle *record.Error
	// refuseUse makes every Use answer with a refusal, as long as it is
	// set, without the stand-in seeing the call.
	refuseUse *record.Error
}

func (c *countingDoor) Show(ctx context.Context, id string) (door.Shown, error) {
	c.mu.Lock()
	fail := c.failShow
	c.mu.Unlock()
	if fail != nil {
		return door.Shown{}, fail
	}
	return c.Door.Show(ctx, id)
}

func (c *countingDoor) List(ctx context.Context) ([]door.Entry, error) {
	c.mu.Lock()
	c.lists++
	fail := c.failList
	c.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	return c.Door.List(ctx)
}

func (c *countingDoor) Decide(ctx context.Context, d door.Record) (door.Decided, error) {
	c.mu.Lock()
	c.decides++
	fail := c.failDecide
	c.mu.Unlock()
	if fail != nil {
		return door.Decided{}, fail
	}
	return c.Door.Decide(ctx, d)
}

func (c *countingDoor) Use(ctx context.Context, in door.UseInput) (door.Used, error) {
	c.mu.Lock()
	c.uses++
	refuse := c.refuseUse
	c.mu.Unlock()
	if refuse != nil {
		return door.Used{}, refuse
	}
	return c.Door.Use(ctx, in)
}

func (c *countingDoor) Settle(ctx context.Context, in door.SettleInput) (door.Settled, error) {
	c.mu.Lock()
	c.settles++
	refuse := c.refuseSettle
	c.refuseSettle = nil
	c.mu.Unlock()
	if refuse != nil {
		return door.Settled{}, refuse
	}
	return c.Door.Settle(ctx, in)
}

func (c *countingDoor) counts() (decides, uses, settles int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.decides, c.uses, c.settles
}

// ender ends a request at the stand-in.
type ender struct{ d *doortest.Door }

func (e ender) EndRequest(_ context.Context, id string) error { return e.d.Withdraw(id) }

// env is one test's world: the stand-in door and the fakes, which outlive
// a service, and the journal's file.
type env struct {
	t      *testing.T
	clock  *doortest.Clock
	stand  *doortest.Door
	door   *countingDoor
	intake *fakeIntake
	merger *fakeMerger
	verif  *fakeVerifier
	mail   *fakeMailbox
	path   string

	svc *Service
	// fault is the point at which the service stops, once.
	fault string
	// at runs a function when the service passes a point.
	at       map[string]func()
	notices  []string
	noticeMu sync.Mutex
	// journalWrap, when set, wraps each store the service is given.
	journalWrap func(*ownerjournal.Store) Journal
	n           int
	// keys is the list of deciding keys each service is given, and
	// pairRand the random source of its pairing codes; both may be nil.
	keys     KeyStore
	pairRand io.Reader
	// lastCode is the code of the last pairing a test started by hand.
	lastCode string
}

// src is the connection of the owner's console, which may decide.
var src = Source{Surface: "console", Session: "0123456789abcdef", Decider: true, PID: 41822, TTY: "/dev/pts/4", Client: "console-a 0.0.1"}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := doortest.NewClock(start)
	cfg := doortest.Sample(clock.Now)
	cfg.Command = "qargi"
	cfg.FirstNumber = 7
	stand := doortest.New(cfg)
	dir := filepath.Join(t.TempDir(), "journal")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &env{
		t: t, clock: clock, stand: stand, door: &countingDoor{Door: stand},
		intake: newIntake(t), merger: newMerger(), verif: newVerifier(t), mail: &fakeMailbox{},
		path: filepath.Join(dir, "owner.db"), at: map[string]func(){},
	}
}

func (e *env) config(st *ownerjournal.Store) Config {
	var j Journal = st
	if e.journalWrap != nil {
		j = e.journalWrap(st)
	}
	return Config{
		Journal: j,
		Reopen: func() (Journal, error) {
			s, err := ownerjournal.Open(e.path)
			if err != nil {
				return nil, err
			}
			if e.journalWrap != nil {
				return e.journalWrap(s), nil
			}
			return s, nil
		},
		Door: e.door, Merger: e.merger, Intake: e.intake, Verifier: e.verif, Mailbox: e.mail,
		Ender: ender{e.stand}, Now: e.clock.Now,
		Keys: e.keys, PairRand: e.pairRand,
		Notice: func(l string) {
			e.noticeMu.Lock()
			e.notices = append(e.notices, l)
			e.noticeMu.Unlock()
		},
		Fault: func(point string) error {
			if f := e.at[point]; f != nil {
				f()
			}
			if e.fault == point {
				e.fault = ""
				return errInjected
			}
			return nil
		},
	}
}

// start opens the journal and makes a service, which restarts from what the
// journal and the door hold.
func (e *env) start() {
	e.t.Helper()
	st, err := ownerjournal.Open(e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	svc, err := New(context.Background(), e.config(st))
	if err != nil {
		_ = st.Close()
		e.t.Fatalf("New: %v", err)
	}
	e.svc = svc
	e.t.Cleanup(func() { _ = svc.Close() })
}

// stop closes the service, which closes the journal.
func (e *env) stop() {
	e.t.Helper()
	if e.svc != nil {
		if err := e.svc.Close(); err != nil {
			e.t.Fatalf("Close: %v", err)
		}
	}
}

// restart stops the service and starts a new one on the same journal and
// door.
func (e *env) restart() {
	e.t.Helper()
	e.stop()
	e.start()
}

// request returns the bytes of a request to finish the feature name.
func (e *env) request(name string, change func(*record.Request)) []byte {
	e.t.Helper()
	e.n++
	r := record.Request{
		Schema:     record.SchemaRequest,
		RequestID:  fmt.Sprintf("rq-test-%02d", e.n),
		Epoch:      7,
		Verb:       record.VerbFeatureFinish,
		Argv:       []string{name},
		FeatureSHA: featureID,
		NoteSHA256: record.Digest([]byte("a note")),
		At:         record.FormatTime(e.clock.Now()),
	}
	if change != nil {
		change(&r)
	}
	b, err := record.EncodeRequest(r)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// collected wraps a request as the daemon would collect it.
func (e *env) collected(b []byte) Collected {
	return Collected{Project: "project-a", Request: b, Note: record.Text("a note"), At: e.clock.Now()}
}

// submit sends a request for the feature name and returns the status.
func (e *env) submit(name string) RequestStatus {
	e.t.Helper()
	st, err := e.svc.Submit(context.Background(), e.collected(e.request(name, nil)))
	if err != nil {
		e.t.Fatalf("Submit %s: %v", name, err)
	}
	return st
}

// card submits a request that must become a pending record, and returns
// the record.
func (e *env) card(name string) record.Pending {
	e.t.Helper()
	st := e.submit(name)
	if st.State != RequestPending {
		e.t.Fatalf("the request for %s is %s (%s: %s), want pending", name, st.State, st.Code, st.Detail)
	}
	return e.pending(st.PendingID)
}

// pending reads a pending record from the stand-in.
func (e *env) pending(id string) record.Pending {
	e.t.Helper()
	shown, err := e.stand.Show(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	p, err := record.DecodePending(shown.Pending.Bytes)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// state returns the door's state of a pending record.
func (e *env) state(id string) record.State {
	e.t.Helper()
	shown, err := e.stand.Show(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return shown.Entry.State
}

// beat tells the service the owner was active.
func (e *env) beat() {
	e.t.Helper()
	if err := e.svc.Beat(src); err != nil {
		e.t.Fatal(err)
	}
}

// frame makes the owner's frame for a card: an approve that signs off
// every flagged path, or a deny.
func frame(p record.Pending, approve bool) ownerwire.DecisionFrame {
	f := ownerwire.DecisionFrame{ID: p.ID, BindingSHA256: p.BindingSHA256, Decision: record.DecisionDeny, SignedOff: []record.SignOff{}}
	if approve {
		f.Decision = record.DecisionApprove
		if s := p.Binding.Summary; s != nil {
			for _, fl := range s.Flagged {
				f.SignedOff = append(f.SignedOff, record.SignOff{Kind: fl.Kind, Path: fl.Path})
			}
		}
	}
	return f
}

// decide beats and then decides.
func (e *env) decide(p record.Pending, approve bool) (ownerwire.DecideResult, error) {
	e.t.Helper()
	e.beat()
	return e.svc.Decide(context.Background(), src, frame(p, approve))
}

// entries returns the journal's entries of a kind.
func (e *env) entries(kind ownerjournal.Kind) []ownerjournal.Entry {
	e.t.Helper()
	all, err := e.svc.journal().Entries(0, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []ownerjournal.Entry
	for _, en := range all {
		if en.Kind == kind {
			out = append(out, en)
		}
	}
	return out
}

// kinds returns the kinds of every entry, in order.
func (e *env) kinds() []ownerjournal.Kind {
	e.t.Helper()
	all, err := e.svc.journal().Entries(0, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([]ownerjournal.Kind, 0, len(all))
	for _, en := range all {
		out = append(out, en.Kind)
	}
	return out
}

// wantCode fails the test unless err is a refusal with the code.
func wantCode(t *testing.T, err error, want record.Code) {
	t.Helper()
	c, ok := record.CodeOf(err)
	if !ok {
		t.Fatalf("the error is %v, want a refusal with %s", err, want)
	}
	if c != want {
		t.Fatalf("refused %s (%v), want %s", c, err, want)
	}
}

// advance moves the clock.
func (e *env) advance(d time.Duration) { e.clock.Advance(d) }

// noticed returns the notices so far.
func (e *env) noticed() []string {
	e.noticeMu.Lock()
	defer e.noticeMu.Unlock()
	return append([]string(nil), e.notices...)
}

// resetRef puts the mirror's target back at the tip the vectors' merge was
// built on, as a new history would have it.
func (e *env) resetRef() {
	e.intake.mu.Lock()
	e.intake.refs[targetRef] = baseID
	e.intake.mu.Unlock()
}
