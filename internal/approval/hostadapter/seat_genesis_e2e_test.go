//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/gatehost"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/thread"
	"ataniq/internal/seatgate"
)

// These tests run the guided new-project interview over the real gate on the
// loopback address, with the owner channel's service of the pairing world
// (seat_decide_e2e_test.go) behind the gate beside it, and the seat's link
// replaced by a raw frame sender, a seatgate client that sends the words of
// seat.genesis as the seat's link will:
//
//	the thread store (package genesis/thread), a folder on disk
//	  -> gatehost (seat.genesis) -> seatgate.Serve, TLS 1.3 with pins
//	  -> seatgate.Client, which sends the frames
//
// The seat link and the pages are not part of it. A "restart of the daemon" is
// a stop of the gate and the store and a start of new ones on the same folder.

// genesisGate is a gate and the thread store behind it, as the daemon makes
// them, started on a folder.
type genesisGate struct {
	addr string
	log  *genesisLog
	stop func()
}

// genesisLog collects the thread store's log lines.
type genesisLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *genesisLog) logf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, a...))
}

func (l *genesisLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

// startGenesisGate opens the thread store in dir and serves a deciding gate
// with it, pinning the world's seat key; stop ends the gate and then closes
// the store, as the daemon does.
func (w *decideWorld) startGenesisGate(dir string) *genesisGate {
	w.t.Helper()
	lg := &genesisLog{}
	store, err := thread.Open(dir, thread.Options{Log: lg.logf})
	if err != nil {
		w.t.Fatalf("thread.Open: %v", err)
	}
	pinned := func(fp string) bool { return fp == w.seatID.Fingerprint() }
	h, err := gatehost.NewDeciding(w.svc, w.svc, w.gate, pinned, nil, gatehost.WithGenesis(store))
	if err != nil {
		w.t.Fatal(err)
	}
	if got := h.Caps(); got[len(got)-1] != gatehost.CapGenesis {
		w.t.Fatalf("the gate lists %q", got)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		w.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- seatgate.Serve(ctx, ln, seatgate.ServerConfig{TLS: seatgate.ServerTLS(w.gateID, []string{w.seatID.Fingerprint()}), Caps: h.Caps()}, h)
	}()
	var once sync.Once
	g := &genesisGate{addr: ln.Addr().String(), log: lg}
	g.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-served:
				if err != nil {
					w.t.Errorf("Serve: %v", err)
				}
			case <-time.After(10 * time.Second):
				w.t.Error("Serve did not end")
			}
			if err := store.Close(); err != nil {
				w.t.Errorf("closing the store: %v", err)
			}
		})
	}
	w.t.Cleanup(g.stop)
	return g
}

// genesisSeat is the raw frame sender: the seat's key, dialed to a gate.
type genesisSeat struct {
	t *testing.T
	c *seatgate.Client
}

func (w *decideWorld) dialGenesis(g *genesisGate) *genesisSeat {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pairCallTime)
	defer cancel()
	c, err := seatgate.Dial(ctx, g.addr, seatgate.ClientConfig{
		TLS: seatgate.ClientTLS(w.seatID, w.gateID.Fingerprint(), "gate"), Caps: []string{"pair", "read", "login", "decide", "genesis"},
		DialTimeout: 5 * time.Second, HelloTimeout: 5 * time.Second,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = c.Close() })
	return &genesisSeat{t: w.t, c: c}
}

// send sends seat.genesis with the params, and returns the result or the
// gate's code.
func (s *genesisSeat) send(p ownerwire.SeatGenesisParams) (ownerwire.SeatGenesisResult, string) {
	s.t.Helper()
	b, err := ownerwire.EncodeSeatGenesisParams(p)
	if err != nil {
		s.t.Fatal(err)
	}
	var raw json.RawMessage
	if err := s.c.Call(pairCtx(s.t), ownerwire.OpSeatGenesis, json.RawMessage(b), &raw); err != nil {
		var re *seatgate.RemoteError
		if errors.As(err, &re) {
			return ownerwire.SeatGenesisResult{}, re.Code
		}
		s.t.Fatalf("seat.genesis %s: %v", p.Verb, err)
	}
	res, err := ownerwire.DecodeSeatGenesisResult(raw)
	if err != nil {
		s.t.Fatalf("the result of %s does not decode: %v", p.Verb, err)
	}
	return res, ""
}

// ok sends and requires a result.
func (s *genesisSeat) ok(p ownerwire.SeatGenesisParams) ownerwire.SeatGenesisResult {
	s.t.Helper()
	res, code := s.send(p)
	if code != "" {
		s.t.Fatalf("%s was refused %s", p.Verb, code)
	}
	return res
}

// refused sends and requires the gate's code.
func (s *genesisSeat) refused(p ownerwire.SeatGenesisParams, want string) {
	s.t.Helper()
	if res, code := s.send(p); code != want {
		s.t.Fatalf("%s: code %q (result %+v), want %s", p.Verb, code, res, want)
	}
}

func gStart() ownerwire.SeatGenesisParams {
	return ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbStart}
}
func gRound(id string) ownerwire.SeatGenesisParams {
	return ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbRound, Thread: id}
}
func gAnswer(id string, round int, a ...ownerwire.GenesisAnswer) ownerwire.SeatGenesisParams {
	return ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbAnswer, Thread: id, Round: round, Answers: a}
}
func gPick(node, id string) ownerwire.GenesisAnswer {
	return ownerwire.GenesisAnswer{Node: node, Kind: ownerwire.GenesisKindChoice, IDs: []string{id}}
}
func gPicks(node string, ids ...string) ownerwire.GenesisAnswer {
	return ownerwire.GenesisAnswer{Node: node, Kind: ownerwire.GenesisKindChoices, IDs: ids}
}
func gDecide(node string) ownerwire.GenesisAnswer {
	return ownerwire.GenesisAnswer{Node: node, Kind: ownerwire.GenesisKindYouDecide}
}
func gSkip(node string) ownerwire.GenesisAnswer {
	return ownerwire.GenesisAnswer{Node: node, Kind: ownerwire.GenesisKindSkip}
}
func gOther(node, text string) ownerwire.GenesisAnswer {
	return ownerwire.GenesisAnswer{Node: node, Kind: ownerwire.GenesisKindOther, Text: text}
}

// webAppRounds are the answers of each round of the web app path, in the
// catalog's order.
var webAppRounds = [][]ownerwire.GenesisAnswer{
	{gPick("kind", "software")},
	{gPick("software.sub_kind", "web_app"), gPick("goal", "own_use"), gPick("audience", "me")},
	{gPick("software.stack", "go"), gDecide("software.data"), gPick("software.auth", "passkey")},
	{gPick("software.deploy_kind", "local"), gPick("hosts", "this_computer"), gPick("budget_band", "none")},
	{gPicks("deliverables", "first_version"), gPick("quality_bar", "solid"), gSkip("schedule")},
	{gPicks("review_points", "plan", "demo", "publish")},
}

func nodeIDs(r ownerwire.SeatGenesisResult) []string {
	var out []string
	for _, n := range r.Nodes {
		out = append(out, n.ID)
	}
	return out
}

// The whole interview of the web app path over the gate: two threads open at
// once, a third refused, a secret refused, an earlier answer changed with its
// reset, the daemon restarted between two rounds, the summary and the finish
// with the digest, and a thread frozen.
func TestGenesisInterviewOverTheGate(t *testing.T) {
	w := newDecideWorld(t, nil)
	dir := filepath.Join(privateDir(t), "genesis")
	g := w.startGenesisGate(dir)
	seat := w.dialGenesis(g)
	if !slices.Contains(seat.c.Caps(), "genesis") {
		t.Fatalf("the hello lists %q", seat.c.Caps())
	}

	// The first thread: the kind and the second round.
	a := seat.ok(gStart())
	if a.State != ownerwire.GenesisStateQuestion || a.Round != 1 || !reflect.DeepEqual(nodeIDs(a), []string{"kind"}) || len(a.Thread) != 16 {
		t.Fatalf("the first round: %+v", a)
	}
	aThread := a.Thread
	res := seat.ok(gAnswer(aThread, 1, webAppRounds[0]...))
	if res.Round != 2 || !reflect.DeepEqual(nodeIDs(res), []string{"software.sub_kind", "goal", "audience"}) {
		t.Fatalf("the second round: %+v", res)
	}

	// The second thread, to the third round, with a secret refused on the way
	// and an earlier answer changed.
	b := seat.ok(gStart())
	bThread := b.Thread
	seat.ok(gAnswer(bThread, 1, webAppRounds[0]...))
	seat.ok(gAnswer(bThread, 2, webAppRounds[1]...))
	secret := "pass" + "word=hunter" + "2"
	seat.refused(gAnswer(bThread, 3, gOther("software.stack", secret)), ownerwire.CodeGenesisSecretShaped)
	if got := seat.ok(gRound(bThread)); got.Round != 3 || !reflect.DeepEqual(nodeIDs(got), []string{"software.stack", "software.data", "software.auth"}) {
		t.Fatalf("after the refused secret: %+v", got)
	}
	third := seat.ok(gAnswer(bThread, 3, webAppRounds[2]...))
	if third.Round != 4 {
		t.Fatalf("the fourth round: %+v", third)
	}
	changed := seat.ok(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbChange, Thread: bThread, Node: "software.sub_kind",
		Answer: &ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"web_tool"}}})
	if changed.Reset != 3 || changed.State != ownerwire.GenesisStateQuestion ||
		!reflect.DeepEqual(nodeIDs(changed), []string{"software.stack", "software.data", "software.auth"}) {
		t.Fatalf("the change: reset %d, %+v", changed.Reset, changed)
	}
	if again := seat.ok(gRound(bThread)); again.Reset != 0 || !reflect.DeepEqual(nodeIDs(again), nodeIDs(changed)) {
		t.Fatalf("the round after the change: %+v", again)
	}

	// A third open thread is refused, as often as it is asked, and the seat is
	// not shut out for it.
	for i := 0; i < 2*gatehost.RefusalBound; i++ {
		seat.refused(gStart(), ownerwire.CodeGenesisBusy)
	}
	if got := seat.ok(gRound(aThread)); got.Round != 2 {
		t.Fatalf("a read after the refusals: %+v", got)
	}

	// The daemon is restarted between two rounds: the thread resumes where it
	// was, and so does the other.
	beforeA, beforeB := seat.ok(gRound(aThread)), seat.ok(gRound(bThread))
	_ = seat.c.Close()
	g.stop()
	if lines := g.log.all(); len(lines) < 3 || strings.Contains(strings.Join(lines, "\n"), "hunter") {
		t.Fatalf("the store's log %q", lines)
	}
	g = w.startGenesisGate(dir)
	seat = w.dialGenesis(g)
	if got := seat.ok(gRound(aThread)); !reflect.DeepEqual(got, beforeA) {
		t.Fatalf("after the restart: %+v, want %+v", got, beforeA)
	}
	if got := seat.ok(gRound(bThread)); !reflect.DeepEqual(got, beforeB) {
		t.Fatalf("after the restart, the other thread: %+v, want %+v", got, beforeB)
	}
	seat.refused(gStart(), ownerwire.CodeGenesisBusy)
	// A seat that lost its own memory of the threads (a restart of the seat)
	// finds them with a list: both, each where it stands (the two were
	// started in one second, so their order is the ids').
	listed := seat.ok(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbList})
	rounds := map[string]int{}
	for _, e := range listed.Threads {
		rounds[e.Thread] = e.Round
	}
	if listed.State != ownerwire.GenesisStateThreads || len(listed.Threads) != 2 ||
		rounds[aThread] != beforeA.Round || rounds[bThread] != beforeB.Round {
		t.Fatalf("the list after the restart: %+v", listed)
	}

	// The first thread goes on to the end.
	for i := 1; i < len(webAppRounds); i++ {
		res = seat.ok(gAnswer(aThread, i+1, webAppRounds[i]...))
		if i+1 < len(webAppRounds) && (res.State != ownerwire.GenesisStateQuestion || res.Round != i+2) {
			t.Fatalf("after round %d: %+v", i+1, res)
		}
	}
	if res.State != ownerwire.GenesisStateSummary || res.Groups == nil || res.Progress.Filled != 14 || res.Progress.Total != 14 || res.Digest != "" {
		t.Fatalf("the summary: %+v", res)
	}
	fields := map[string]string{}
	for _, row := range append(append(res.Groups.YouChose, res.Groups.DecidedForYou...), res.Groups.Defaults...) {
		fields[row.Field] = row.Value
	}
	if fields["kind"] != "Software" || fields["software.stack"] == "" || fields["schedule"] == "" || len(fields) != 14 {
		t.Fatalf("the rows: %v", fields)
	}
	if got := seat.ok(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbSummary, Thread: aThread}); !reflect.DeepEqual(got, res) {
		t.Fatalf("the summary read again: %+v", got)
	}
	fin := seat.ok(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbFinish, Thread: aThread})
	if fin.State != ownerwire.GenesisStateFinished || len(fin.Digest) != 64 || fin.Groups == nil || !reflect.DeepEqual(fin.Groups, res.Groups) {
		t.Fatalf("finish: %+v", fin)
	}
	seat.refused(gAnswer(aThread, 1, webAppRounds[0]...), ownerwire.CodeGenesisDone)
	seat.refused(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbChange, Thread: aThread, Node: "goal",
		Answer: &ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"share"}}}, ownerwire.CodeGenesisDone)
	seat.refused(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbDiscard, Thread: aThread}, ownerwire.CodeGenesisDone)
	// The frozen thread makes room for a third, which can be started.
	c := seat.ok(gStart())
	seat.ok(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbDiscard, Thread: c.Thread})

	// What the brief was frozen with survives another restart, and an unknown
	// thread is told as such, and counts.
	_ = seat.c.Close()
	g.stop()
	g = w.startGenesisGate(dir)
	seat = w.dialGenesis(g)
	if got := seat.ok(gRound(aThread)); !reflect.DeepEqual(got, fin) {
		t.Fatalf("a finished thread after the restart: %+v, want %+v", got, fin)
	}
	seat.refused(gRound(c.Thread), ownerwire.CodeGenesisUnknownThread)
	seat.refused(gRound("ffffffffffffffff"), ownerwire.CodeGenesisUnknownThread)

	// The folder: private, one file for each thread, none holds the secret.
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the folder: %v %v", fi, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("the folder holds %d files: %v", len(entries), err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if fi, _ := e.Info(); fi.Mode().Perm() != 0o600 || !strings.HasSuffix(e.Name(), ".json") || bytes.Contains(data, []byte("hunter")) {
			t.Errorf("%s: mode %v, or it holds the secret", e.Name(), fi.Mode())
		}
	}
}

// The seat's frames are held to the wire: a frame the wire's decoder refuses
// is bad-params and the store never sees it, and the refusals that are the
// seat's own doing shut it out at the bound.
func TestGenesisRefusalsAreBoundedOverTheGate(t *testing.T) {
	w := newDecideWorld(t, nil)
	dir := filepath.Join(privateDir(t), "genesis")
	g := w.startGenesisGate(dir)
	seat := w.dialGenesis(g)
	var raw json.RawMessage
	err := seat.c.Call(pairCtx(t), ownerwire.OpSeatGenesis, json.RawMessage(`{"verb":"start","thread":"0123456789abcdef"}`), &raw)
	var re *seatgate.RemoteError
	if !errors.As(err, &re) || re.Code != gatehost.CodeBadParams {
		t.Fatalf("a start with a thread: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused frame wrote %d files", len(entries))
	}
	a := seat.ok(gStart())
	// A seat that keeps sending answers the question does not allow is shut
	// out, reads and all, as for any refusal of what it sent.
	var last string
	for i := 0; i < gatehost.RefusalBound+2; i++ {
		_, last = seat.send(gAnswer(a.Thread, 1, gPick("kind", "nothing")))
	}
	if last != gatehost.CodeTooManyRefusals {
		t.Fatalf("after %d bad answers: %q", gatehost.RefusalBound+2, last)
	}
	if _, code := seat.send(gRound(a.Thread)); code != gatehost.CodeTooManyRefusals {
		t.Fatalf("a read of a seat that is shut out: %q", code)
	}
}

// gPlan asks the plan of a thread.
func gPlan(id string) ownerwire.SeatGenesisParams {
	return ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbPlan, Thread: id}
}

// The plan over the gate: the web app interview walked to its summary gives a
// preview, compiled on each read and stored nowhere; finish stores it, and the
// plan of the finished thread is that one, stored, the same after a restart of
// the daemon. An interview finished early has no plan (genesis-not-ready), nor
// has a kind in the owner's own words (genesis-no-template), and neither shuts
// the seat out.
func TestGenesisPlanOverTheGate(t *testing.T) {
	w := newDecideWorld(t, nil)
	dir := filepath.Join(privateDir(t), "genesis")
	g := w.startGenesisGate(dir)
	seat := w.dialGenesis(g)

	a := seat.ok(gStart()).Thread
	var sum ownerwire.SeatGenesisResult
	for i, round := range webAppRounds {
		sum = seat.ok(gAnswer(a, i+1, round...))
	}
	if sum.State != ownerwire.GenesisStateSummary || !sum.Progress.PlanReady {
		t.Fatalf("the summary: %+v", sum)
	}
	pre := seat.ok(gPlan(a))
	if pre.State != ownerwire.GenesisStatePlan || pre.Stored || pre.Thread != a || pre.Plan == nil || pre.Plan.Revision != 1 ||
		pre.Plan.PrevDigest != "" || len(pre.PlanDigest) != 64 || pre.Plan.CatalogDigest != sum.CatalogDigest {
		t.Fatalf("the preview: %+v", pre)
	}
	if d, err := ownerwire.GenesisPlanDigest(*pre.Plan); err != nil || d != pre.PlanDigest {
		t.Fatalf("the preview's digest %s is not its plan's %s: %v", pre.PlanDigest, d, err)
	}
	if again := seat.ok(gPlan(a)); again.PlanDigest != pre.PlanDigest || !reflect.DeepEqual(again, pre) {
		t.Fatalf("the preview asked again: %+v, want %+v", again, pre)
	}
	// A preview is stored nowhere: the thread's file has no plan.
	file := filepath.Join(dir, a+".json")
	if data, err := os.ReadFile(file); err != nil || bytes.Contains(data, []byte(`"plan_digest"`)) {
		t.Fatalf("the open thread's file: %v", err)
	}

	fin := seat.ok(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbFinish, Thread: a})
	if fin.State != ownerwire.GenesisStateFinished || !fin.Progress.PlanReady {
		t.Fatalf("finish: %+v", fin)
	}
	stored := seat.ok(gPlan(a))
	if !stored.Stored || stored.PlanDigest != pre.PlanDigest || !reflect.DeepEqual(stored.Plan, pre.Plan) {
		t.Fatalf("the stored plan: %+v, want the preview's %s", stored, pre.PlanDigest)
	}
	if data, err := os.ReadFile(file); err != nil || !bytes.Contains(data, []byte(`"plan_digest":"`+pre.PlanDigest+`"`)) {
		t.Fatalf("the finished thread's file does not hold its plan: %v", err)
	}

	// The daemon is restarted: the same plan comes back.
	_ = seat.c.Close()
	g.stop()
	lines := strings.Join(g.log.all(), "\n")
	if !strings.Contains(lines, "genesis: a plan was drawn") || strings.Contains(lines, pre.Plan.Title) || strings.Contains(lines, pre.PlanDigest) {
		t.Fatalf("the store's log %q", g.log.all())
	}
	g = w.startGenesisGate(dir)
	seat = w.dialGenesis(g)
	if got := seat.ok(gPlan(a)); !reflect.DeepEqual(got, stored) {
		t.Fatalf("after the restart: %+v, want %+v", got, stored)
	}

	// An interview finished early: no plan, before the finish or after.
	b := seat.ok(gStart()).Thread
	seat.ok(gAnswer(b, 1, webAppRounds[0]...))
	seat.refused(gPlan(b), ownerwire.CodeGenesisNotReady)
	early := seat.ok(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbFinish, Thread: b})
	if early.State != ownerwire.GenesisStateFinished || early.Progress.PlanReady {
		t.Fatalf("the early finish: %+v", early)
	}
	// The two refusals, more of them together than the refusal bound (the
	// gate's rate of 60 a minute allows no more), shut no one out.
	for i := 0; i < gatehost.RefusalBound/2; i++ {
		seat.refused(gPlan(b), ownerwire.CodeGenesisNotReady)
	}

	// A kind in the owner's own words: no template, as often as it is asked.
	c := seat.ok(gStart()).Thread
	seat.ok(gAnswer(c, 1, gOther("kind", "a board game for two players")))
	for i := 0; i < gatehost.RefusalBound/2; i++ {
		seat.refused(gPlan(c), ownerwire.CodeGenesisNoTemplate)
	}
	// None of it shut the seat out.
	if got := seat.ok(gPlan(a)); !reflect.DeepEqual(got, stored) {
		t.Fatalf("the stored plan after the refusals: %+v", got)
	}
}

// walkerPath is an interview the walker leads: the kind and the sub-kind the
// owner picks, and a question left unanswered in a round (none when blank is
// "").
type walkerPath struct {
	name, kind, subKind, template string
	blankRound                    int
	blank                         string
}

// walkOver answers, over the gate, each round the walker asks until the
// summary: the path's kind and sub-kind, and for every other question its
// first option, but the question the path leaves unanswered. Nothing is
// scripted by round: the questions are those the result names.
func walkOver(t *testing.T, seat *genesisSeat, p walkerPath) (string, ownerwire.SeatGenesisResult) {
	t.Helper()
	res := seat.ok(gStart())
	return res.Thread, answerOver(t, seat, p, res, false)
}

// answerOver answers the rounds from res on, as walkOver does, and returns
// the result that is not a round. Rounds count up from res's, or, again
// after a change past the last round, keep its number.
func answerOver(t *testing.T, seat *genesisSeat, p walkerPath, res ownerwire.SeatGenesisResult, again bool) ownerwire.SeatGenesisResult {
	t.Helper()
	id := res.Thread
	for rounds := res.Round - 1; res.State == ownerwire.GenesisStateQuestion; rounds++ {
		want := rounds + 1
		if again {
			want = ownerwire.MaxGenesisRounds
		}
		if rounds == 2*ownerwire.MaxGenesisRounds || res.Round != want || len(res.Nodes) < 1 || len(res.Nodes) > 3 {
			t.Fatalf("%s: round %d of %d holds %v", p.name, res.Round, res.Of, nodeIDs(res))
		}
		var as []ownerwire.GenesisAnswer
		for _, n := range res.Nodes {
			switch {
			case n.ID == p.blank && res.Round == p.blankRound:
			case n.ID == "kind":
				as = append(as, gPick(n.ID, p.kind))
			case strings.HasSuffix(n.ID, ".sub_kind"):
				as = append(as, gPick(n.ID, p.subKind))
			case n.Type == ownerwire.GenesisTypeMulti:
				as = append(as, gPicks(n.ID, n.Options[0].ID))
			default:
				as = append(as, gPick(n.ID, n.Options[0].ID))
			}
		}
		res = seat.ok(gAnswer(id, res.Round, as...))
	}
	return res
}

// Interviews of every kind a template serves, led by the real walker over
// the real gate: each answers what each round asks, ends at a summary whose
// plan is ready within the six rounds, and finishing draws and stores the
// plan. The web app interview that leaves the goal unanswered in round 2 is
// the phone trial's: it ended at round 6 with the review points never asked,
// 13 of 14, and no plan; now the goal is asked again beside the build.
func TestGenesisWalkerInterviewsAreReadyOverTheGate(t *testing.T) {
	w := newDecideWorld(t, nil)
	g := w.startGenesisGate(filepath.Join(privateDir(t), "genesis"))
	for _, p := range []walkerPath{
		{name: "web app", kind: "software", subKind: "web_app", template: "software"},
		{name: "web app, the goal left in round 2", kind: "software", subKind: "web_app", template: "software", blankRound: 2, blank: "goal"},
		{name: "web tool", kind: "software", subKind: "web_tool", template: "software"},
		{name: "newsletter", kind: "publishing", subKind: "newsletter", template: "publishing"},
		{name: "article", kind: "publishing", subKind: "article", template: "publishing"},
		{name: "podcast", kind: "media", subKind: "podcast", template: "media"},
		{name: "book", kind: "media", subKind: "book", template: "media"},
	} {
		// A connection of its own for each interview: the gate's rate is
		// for each connection.
		seat := w.dialGenesis(g)
		id, sum := walkOver(t, seat, p)
		if sum.State != ownerwire.GenesisStateSummary || sum.Round > ownerwire.MaxGenesisRounds ||
			sum.Progress.Filled != sum.Progress.Total || sum.Progress.Total != 14 || !sum.Progress.PlanReady {
			t.Fatalf("%s: the summary: round %d, %d of %d, plan ready %v", p.name, sum.Round, sum.Progress.Filled, sum.Progress.Total, sum.Progress.PlanReady)
		}
		fin := seat.ok(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbFinish, Thread: id})
		if fin.State != ownerwire.GenesisStateFinished || !fin.Progress.PlanReady {
			t.Fatalf("%s: finish: %+v", p.name, fin)
		}
		pl := seat.ok(gPlan(id))
		if !pl.Stored || pl.Plan == nil || pl.Plan.Template.ID != p.template || len(pl.PlanDigest) != 64 {
			t.Fatalf("%s: the plan at finish: %+v", p.name, pl)
		}
		_ = seat.c.Close()
	}

	// A change of the sub-kind on the summary clears the four answers of
	// the build: they are asked again as round 6 of 6, and the brief ends
	// ready, with its plan drawn at finish.
	seat := w.dialGenesis(g)
	p := walkerPath{name: "web app changed to a web tool", kind: "software", subKind: "web_tool", template: "software"}
	id, sum := walkOver(t, seat, walkerPath{name: "web app", kind: "software", subKind: "web_app"})
	if !sum.Progress.PlanReady || sum.Round != ownerwire.MaxGenesisRounds {
		t.Fatalf("the summary before the change: %+v", sum)
	}
	ch := seat.ok(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbChange, Thread: id, Node: "software.sub_kind",
		Answer: &ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"web_tool"}}})
	if ch.Reset != 4 || ch.State != ownerwire.GenesisStateQuestion || ch.Round != ownerwire.MaxGenesisRounds || ch.Progress.PlanReady {
		t.Fatalf("the change: %+v", ch)
	}
	if sum = answerOver(t, seat, p, ch, true); sum.State != ownerwire.GenesisStateSummary ||
		sum.Progress.Filled != sum.Progress.Total || !sum.Progress.PlanReady {
		t.Fatalf("the summary after the change: %+v", sum)
	}
	if fin := seat.ok(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbFinish, Thread: id}); !fin.Progress.PlanReady {
		t.Fatalf("finish after the change: %+v", fin)
	}
	if pl := seat.ok(gPlan(id)); !pl.Stored || pl.Plan == nil || pl.Plan.Template.ID != p.template {
		t.Fatalf("the plan after the change: %+v", pl)
	}
	if lines := strings.Join(g.log.all(), "\n"); strings.Count(lines, "genesis: a plan was drawn") != 8 {
		t.Fatalf("the store's log %q", g.log.all())
	}
}
