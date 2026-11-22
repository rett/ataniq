//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
)

// ownerFake is an OwnerService that records what it was asked and answers
// from the functions a test sets. Nothing in it touches a door or a journal.
type ownerFake struct {
	mu sync.Mutex

	decides   []ownerDecideCall
	beats     []ownerchannel.Source
	limitSets []ownerLimitSetCall
	calls     []string

	list     func() (ownerwire.Listing, error)
	show     func(id string) (ownerwire.ShowResult, error)
	paths    func(p ownerwire.PageParams) (ownerwire.PathsPage, error)
	patch    func(p ownerwire.PageParams) (ownerwire.PatchPage, error)
	decide   func(src ownerchannel.Source, f ownerwire.DecisionFrame) (ownerwire.DecideResult, error)
	beat     func(src ownerchannel.Source) error
	limit    func(project string) (ownerwire.LimitView, error)
	limitSet func(src ownerchannel.Source, p ownerwire.LimitSetParams) (ownerwire.LimitView, error)
	looked   func() error

	pairStart   func(src ownerchannel.Source, p ownerwire.PairStartParams) (ownerwire.PairStartResult, error)
	pairStatus  func() (ownerwire.PairStatus, error)
	pairConfirm func(src ownerchannel.Source) (ownerwire.KeyItem, error)
	pairReject  func(src ownerchannel.Source) error
	pairPresent func(src ownerchannel.Source, p ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error)
	keys        func() (ownerwire.KeysView, error)
	keyRevoke   func(src ownerchannel.Source, p ownerwire.KeyRevokeParams) (ownerwire.KeysView, error)
	keysOff     func(src ownerchannel.Source) (ownerwire.KeysView, error)
	// sources are the sources the pairing writes were handed, in order.
	sources []ownerchannel.Source
}

type ownerDecideCall struct {
	Src   ownerchannel.Source
	Frame ownerwire.DecisionFrame
}

type ownerLimitSetCall struct {
	Src    ownerchannel.Source
	Params ownerwire.LimitSetParams
}

func (f *ownerFake) note(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *ownerFake) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *ownerFake) decided() []ownerDecideCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ownerDecideCall(nil), f.decides...)
}

func (f *ownerFake) beaten() []ownerchannel.Source {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ownerchannel.Source(nil), f.beats...)
}

var errOwnerNotSet = errors.New("the test did not set this answer")

func (f *ownerFake) List(context.Context) (ownerwire.Listing, error) {
	f.note("list")
	if f.list == nil {
		return ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}}, nil
	}
	return f.list()
}

func (f *ownerFake) Show(_ context.Context, id string) (ownerwire.ShowResult, error) {
	f.note("show " + id)
	if f.show == nil {
		return ownerwire.ShowResult{}, errOwnerNotSet
	}
	return f.show(id)
}

func (f *ownerFake) Paths(_ context.Context, p ownerwire.PageParams) (ownerwire.PathsPage, error) {
	f.note("paths")
	if f.paths == nil {
		return ownerwire.PathsPage{}, errOwnerNotSet
	}
	return f.paths(p)
}

func (f *ownerFake) Patch(_ context.Context, p ownerwire.PageParams) (ownerwire.PatchPage, error) {
	f.note("patch")
	if f.patch == nil {
		return ownerwire.PatchPage{}, errOwnerNotSet
	}
	return f.patch(p)
}

func (f *ownerFake) Decide(_ context.Context, src ownerchannel.Source, fr ownerwire.DecisionFrame) (ownerwire.DecideResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "decide")
	f.decides = append(f.decides, ownerDecideCall{Src: src, Frame: fr})
	fn := f.decide
	f.mu.Unlock()
	if fn == nil {
		return ownerwire.DecideResult{State: record.StateApplied, Line: "applied"}, nil
	}
	return fn(src, fr)
}

func (f *ownerFake) Beat(src ownerchannel.Source) error {
	f.mu.Lock()
	f.calls = append(f.calls, "beat")
	f.beats = append(f.beats, src)
	fn := f.beat
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(src)
}

func (f *ownerFake) Limit(_ context.Context, project string) (ownerwire.LimitView, error) {
	f.note("limit " + project)
	if f.limit == nil {
		return ownerwire.LimitView{}, errOwnerNotSet
	}
	return f.limit(project)
}

func (f *ownerFake) LimitSet(_ context.Context, src ownerchannel.Source, p ownerwire.LimitSetParams) (ownerwire.LimitView, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "limit-set")
	f.limitSets = append(f.limitSets, ownerLimitSetCall{Src: src, Params: p})
	fn := f.limitSet
	f.mu.Unlock()
	if fn == nil {
		return ownerwire.LimitView{}, errOwnerNotSet
	}
	return fn(src, p)
}

func (f *ownerFake) Looked(context.Context) error {
	f.note("looked")
	if f.looked == nil {
		return nil
	}
	return f.looked()
}

// ownerInspect is a PeerInspector that answers from a function and records
// what it was asked. By default every peer has the terminal ownerTTY.
type ownerInspect struct {
	mu    sync.Mutex
	seen  []PeerRef
	reply func(PeerRef) (PeerProcess, error)
}

const ownerTTY = "/dev/pts/7"

func (i *ownerInspect) Inspect(p PeerRef) (PeerProcess, error) {
	i.mu.Lock()
	i.seen = append(i.seen, p)
	fn := i.reply
	i.mu.Unlock()
	if fn == nil {
		return PeerProcess{TTY: ownerTTY}, nil
	}
	return fn(p)
}

func (i *ownerInspect) inspected() []PeerRef {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]PeerRef(nil), i.seen...)
}

// ownerRig is a daemon that serves the owner connection over a fake service
// and a fake inspector.
type ownerRig struct {
	t      *testing.T
	e      *env
	svc    *ownerFake
	insp   *ownerInspect
	oc     *OwnerChannel
	run    *running
	logs   *syncBuffer
	mu     sync.Mutex
	built  int
	closed int
}

// newOwnerRig starts the daemon with the owner channel's hook set.
func newOwnerRig(t *testing.T) *ownerRig {
	t.Helper()
	r := newUnstartedOwnerRig(t)
	r.run = r.e.startDaemon(r.config())
	return r
}

func newUnstartedOwnerRig(t *testing.T) *ownerRig {
	t.Helper()
	r := &ownerRig{t: t, e: newEnv(t), svc: &ownerFake{}, insp: &ownerInspect{}, logs: &syncBuffer{}}
	r.oc = &OwnerChannel{
		Build: func() (OwnerService, func() error, error) {
			r.mu.Lock()
			r.built++
			r.mu.Unlock()
			return r.svc, func() error {
				r.mu.Lock()
				r.closed++
				r.mu.Unlock()
				return nil
			}, nil
		},
		Inspector: r.insp,
		Log: func(format string, a ...any) {
			r.logs.Write([]byte(strings.TrimSuffix(fmt.Sprintf(format, a...), "\n") + "\n"))
		},
	}
	return r
}

func (r *ownerRig) config() Config {
	cfg := r.e.config()
	cfg.ServeHook = r.oc.Serve
	return cfg
}

func (r *ownerRig) counts() map[string]uint64 {
	c := r.oc.Counts()
	return map[string]uint64{
		"granted": c.DeciderGranted.Load(), "second": c.SecondConsole.Load(), "not-console": c.NotConsole.Load(),
		"no-terminal": c.NoTerminal.Load(), "not-inspected": c.NotInspected.Load(),
		"session-changed": c.SessionChanged.Load(), "decision-not-decider": c.DecisionNotDecider.Load(),
		"decision-invalid": c.DecisionInvalid.Load(), "no-connection": c.NoConnection.Load(),
		"decision-flooded": c.DecisionFlooded.Load(),
		"pair-not-decider": c.PairNotDecider.Load(), "seat-refused": c.SeatRefused.Load(),
	}
}

// wantCounts checks the non-zero counters: the ones named have exactly their
// value and every other is 0.
func (r *ownerRig) wantCounts(want map[string]uint64) {
	r.t.Helper()
	got := r.counts()
	for k, v := range got {
		if v != want[k] {
			r.t.Errorf("counter %s is %d, want %d (all: %v)", k, v, want[k], got)
		}
	}
}

// dial opens a connection to the daemon, closed when the test ends.
func (r *ownerRig) dial() *clientapi.Client {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := clientapi.Dial(ctx, r.run.socket)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { c.Close() })
	return c
}

func ownerCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

const ownerConsoleClient = "ataniq-console 0.0.11"

// ownerSession and ownerSession2 are two valid session ids.
const (
	ownerSession  = "0123456789abcdef"
	ownerSession2 = "fedcba9876543210"
)

// askDecider calls owner.decider on c.
func (r *ownerRig) askDecider(c *clientapi.Client, client, session string) (ownerwire.DeciderResult, error) {
	r.t.Helper()
	var res ownerwire.DeciderResult
	err := c.Call(ownerCtx(r.t), ownerwire.OpDecider, ownerwire.DeciderParams{Client: client, Session: session}, &res)
	return res, err
}

// mustBeDecider makes c the decider.
func (r *ownerRig) mustBeDecider(c *clientapi.Client, session string) {
	r.t.Helper()
	res, err := r.askDecider(c, ownerConsoleClient, session)
	if err != nil || !res.Decider {
		r.t.Fatalf("owner.decider: %+v, %v", res, err)
	}
}

// frame is a decision frame that passes the wire's checks: an approve of a
// card with no flagged path.
func ownerFrame(id string) ownerwire.DecisionFrame {
	return ownerwire.DecisionFrame{
		ID: id, BindingSHA256: strings.Repeat("ab", 32),
		Decision: record.DecisionApprove, SignedOff: []record.SignOff{},
	}
}

// decide sends the frame over c.
func (r *ownerRig) decide(c *clientapi.Client, f ownerwire.DecisionFrame) (ownerwire.DecideResult, error) {
	r.t.Helper()
	var res ownerwire.DecideResult
	err := c.Call(ownerCtx(r.t), ownerwire.OpDecide, f, &res)
	return res, err
}

// wantOwnerCode checks that err is the daemon's error with the code and returns
// it. Named apart from the canary tests' wantCode so that both builds compile.
func wantOwnerCode(t *testing.T, err error, code clientapi.ErrorCode) *clientapi.Error {
	t.Helper()
	var ce *clientapi.Error
	if !errors.As(err, &ce) || ce.Code != code {
		t.Fatalf("error %v, want the code %q", err, code)
	}
	return ce
}

// ownerHello returns the hello result over a fresh connection.
func ownerHello(t *testing.T, socket string) clientapi.HelloResult {
	t.Helper()
	ctx := ownerCtx(t)
	c, err := clientapi.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h, err := c.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// The pairing and key operations: each notes its call and the source it was
// handed, and answers from its function, or with errOwnerNotSet.

func (f *ownerFake) enter(name string, src *ownerchannel.Source) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	if src != nil {
		f.sources = append(f.sources, *src)
	}
}

func (f *ownerFake) PairStart(_ context.Context, src ownerchannel.Source, p ownerwire.PairStartParams) (ownerwire.PairStartResult, error) {
	f.enter(fmt.Sprintf("pair-start replace=%v", p.Replace), &src)
	if f.pairStart == nil {
		return ownerwire.PairStartResult{}, errOwnerNotSet
	}
	return f.pairStart(src, p)
}

func (f *ownerFake) PairStatus(context.Context) (ownerwire.PairStatus, error) {
	f.enter("pair-status", nil)
	if f.pairStatus == nil {
		return ownerwire.PairStatus{Phase: ownerwire.PairNone}, nil
	}
	return f.pairStatus()
}

func (f *ownerFake) PairConfirm(_ context.Context, src ownerchannel.Source) (ownerwire.KeyItem, error) {
	f.enter("pair-confirm", &src)
	if f.pairConfirm == nil {
		return ownerwire.KeyItem{}, errOwnerNotSet
	}
	return f.pairConfirm(src)
}

func (f *ownerFake) PairReject(_ context.Context, src ownerchannel.Source) error {
	f.enter("pair-reject", &src)
	if f.pairReject == nil {
		return nil
	}
	return f.pairReject(src)
}

func (f *ownerFake) PairPresent(_ context.Context, src ownerchannel.Source, p ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error) {
	f.enter("seat.pair", &src)
	if f.pairPresent == nil {
		return ownerwire.SeatPairResult{}, errOwnerNotSet
	}
	return f.pairPresent(src, p)
}

func (f *ownerFake) Keys(context.Context) (ownerwire.KeysView, error) {
	f.enter("keys", nil)
	if f.keys == nil {
		return ownerwire.KeysView{Keys: []ownerwire.KeyItem{}}, nil
	}
	return f.keys()
}

func (f *ownerFake) KeyRevoke(_ context.Context, src ownerchannel.Source, p ownerwire.KeyRevokeParams) (ownerwire.KeysView, error) {
	f.enter("key-revoke "+p.ID, &src)
	if f.keyRevoke == nil {
		return ownerwire.KeysView{}, errOwnerNotSet
	}
	return f.keyRevoke(src, p)
}

func (f *ownerFake) KeysOff(_ context.Context, src ownerchannel.Source) (ownerwire.KeysView, error) {
	f.enter("keys-off", &src)
	if f.keysOff == nil {
		return ownerwire.KeysView{}, errOwnerNotSet
	}
	return f.keysOff(src)
}
