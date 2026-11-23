package seatlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/thread"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// storeDesk is the daemon's side of seat.genesis as the gate serves it: the
// wire's decoder, a real thread store under catalog v1, and the store's
// refusal words as the gate's codes. It records the params of every call.
type storeDesk struct {
	s *thread.Store

	mu     sync.Mutex
	params []ownerwire.SeatGenesisParams
	// answer, when set, replaces the store's answer with these bytes; code,
	// when set, refuses every call with it.
	answer []byte
	code   string
}

func newStoreDesk(t *testing.T) *storeDesk {
	t.Helper()
	s, err := thread.Open(filepath.Join(t.TempDir(), thread.DirName), thread.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &storeDesk{s: s}
}

func (f *storeDesk) handle(ctx context.Context, _ seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
	if op != ownerwire.OpSeatGenesis {
		return nil, seatgate.NewCodedError("unknown-op")
	}
	p, err := ownerwire.DecodeSeatGenesisParams(params)
	if err != nil {
		return nil, seatgate.NewCodedError("bad-params")
	}
	f.mu.Lock()
	f.params = append(f.params, p)
	answer, code := f.answer, f.code
	f.mu.Unlock()
	if code != "" {
		return nil, seatgate.NewCodedError(code)
	}
	if answer != nil {
		return answer, nil
	}
	res, err := f.s.Genesis(ctx, p)
	if err != nil {
		if c, ok := thread.RefusalCode(err); ok {
			return nil, seatgate.NewCodedError(c)
		}
		return nil, seatgate.NewCodedError("failed")
	}
	return ownerwire.EncodeSeatGenesisResult(res)
}

func (f *storeDesk) seen() []ownerwire.SeatGenesisParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ownerwire.SeatGenesisParams(nil), f.params...)
}

func (f *storeDesk) set(answer []byte, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer, f.code = answer, code
}

// genesisGate serves the desk on a gate that lists caps, and returns the link
// with a clock of its own for the link's memory, which moves only when the
// test moves it.
func genesisGate(t *testing.T, caps []string) (*storeDesk, *Daemon, *gate, *testClock) {
	t.Helper()
	f := newStoreDesk(t)
	seat := newIdentity(t, "seat")
	g := serveGateCaps(t, []string{seat.Fingerprint()}, caps, f.handle)
	d := dialPinned(t, g, seat)
	clock := &testClock{t: decideNow}
	d.gnow = clock.now
	return f, d, g, clock
}

var bg = context.Background()

func call(t *testing.T, d *Daemon, c seatapp.GenesisCall) seatapp.GenesisView {
	t.Helper()
	v, err := d.Genesis(bg, c)
	if err != nil {
		t.Fatalf("%s: %v", c.Verb, err)
	}
	return v
}

// The app's words of the interview are the wire's.
func TestSeatLinkGenesisWordsAreTheWires(t *testing.T) {
	for _, w := range [][2]string{
		{seatapp.GenesisVerbStart, ownerwire.GenesisVerbStart},
		{seatapp.GenesisVerbList, ownerwire.GenesisVerbList},
		{seatapp.GenesisVerbRound, ownerwire.GenesisVerbRound},
		{seatapp.GenesisVerbAnswer, ownerwire.GenesisVerbAnswer},
		{seatapp.GenesisVerbChange, ownerwire.GenesisVerbChange},
		{seatapp.GenesisVerbSummary, ownerwire.GenesisVerbSummary},
		{seatapp.GenesisVerbFinish, ownerwire.GenesisVerbFinish},
		{seatapp.GenesisVerbDiscard, ownerwire.GenesisVerbDiscard},
		{seatapp.GenesisKindChoice, ownerwire.GenesisKindChoice},
		{seatapp.GenesisKindChoices, ownerwire.GenesisKindChoices},
		{seatapp.GenesisKindOther, ownerwire.GenesisKindOther},
		{seatapp.GenesisKindYouDecide, ownerwire.GenesisKindYouDecide},
		{seatapp.GenesisKindSkip, ownerwire.GenesisKindSkip},
		{seatapp.GenesisStateQuestion, ownerwire.GenesisStateQuestion},
		{seatapp.GenesisStateSummary, ownerwire.GenesisStateSummary},
		{seatapp.GenesisStateFinished, ownerwire.GenesisStateFinished},
		{seatapp.GenesisStateDiscarded, ownerwire.GenesisStateDiscarded},
		{seatapp.GenesisStateThreads, ownerwire.GenesisStateThreads},
		{seatapp.GenesisTypeSingle, ownerwire.GenesisTypeSingle},
		{seatapp.GenesisTypeMulti, ownerwire.GenesisTypeMulti},
		{seatapp.GenesisTypeYesNo, ownerwire.GenesisTypeYesNo},
		{seatapp.GenesisTypeScale, ownerwire.GenesisTypeScale},
	} {
		if w[0] != w[1] {
			t.Errorf("the app says %q where the wire says %q", w[0], w[1])
		}
	}
	if !slices.Contains(Caps(), CapGenesis) {
		t.Errorf("the seat's hello lacks %q: %v", CapGenesis, Caps())
	}
}

// Without "genesis" in the hello, every call is not offered and nothing is
// sent.
func TestSeatLinkGenesisNotOffered(t *testing.T) {
	_, d, g, _ := genesisGate(t, []string{"pair", "read"})
	for _, verb := range []string{seatapp.GenesisVerbStart, seatapp.GenesisVerbList} {
		if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: verb}); !errors.Is(err, seatapp.ErrNotOffered) {
			t.Errorf("%s: %v", verb, err)
		}
	}
	if n := g.calls.Load(); n != 0 {
		t.Errorf("the gate was asked %d times", n)
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, err := d.Genesis(ctx, seatapp.GenesisCall{Verb: seatapp.GenesisVerbList}); !errors.Is(err, seatapp.ErrDaemonUnavailable) {
		t.Errorf("an ended context: %v", err)
	}
}

// A whole interview through the link, the gate and a real store: each verb
// goes as the wire's params, each answer comes back as the app's values,
// and a summary's rows carry their questions from the catalog the thread is
// walked under.
func TestSeatLinkGenesisAWholeInterview(t *testing.T) {
	f, d, _, clock := genesisGate(t, []string{"pair", "read", "genesis"})
	if l := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbList}); l.State != seatapp.GenesisStateThreads || l.Threads == nil || len(l.Threads) != 0 {
		t.Fatalf("the first list: %+v", l)
	}
	first := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart})
	if first.State != seatapp.GenesisStateQuestion || first.Round != 1 || first.Of != 6 || !seatapp.ValidThreadID(first.Thread) ||
		len(first.Questions) != 1 || first.Questions[0].ID != "kind" || len(first.Questions[0].Options) != 3 || !first.Questions[0].Other {
		t.Fatalf("the first round: %+v", first)
	}
	id := first.Thread
	// The rounds of the web app path, in the app's words.
	rounds := [][]seatapp.GenesisAnswer{
		{{Node: "kind", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"software"}}}},
		{{Node: "software.sub_kind", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"web_app"}}},
			{Node: "goal", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"own_use"}}},
			{Node: "audience", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"me"}}}},
		{{Node: "software.stack", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindOther, Text: "Go and a small database"}},
			{Node: "software.data", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindYouDecide}},
			{Node: "software.auth", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"passkey"}}}},
		{{Node: "software.deploy_kind", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"local"}}},
			{Node: "hosts", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"this_computer"}}},
			{Node: "budget_band", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"none"}}}},
		{{Node: "deliverables", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoices, IDs: []string{"first_version"}}},
			{Node: "quality_bar", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"solid"}}},
			{Node: "schedule", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindSkip}}},
		{{Node: "review_points", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoices, IDs: []string{"plan", "demo"}}}},
	}
	var v seatapp.GenesisView
	for i, answers := range rounds {
		clock.advance(time.Minute)
		v = call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: id, Round: i + 1, Answers: answers})
	}
	if v.State != seatapp.GenesisStateSummary || v.Filled != v.Total || len(v.YouChose) == 0 {
		t.Fatalf("the summary: %+v", v)
	}
	// The params the gate was given are the wire's form of the app's.
	seen := f.seen()
	ans := seen[len(seen)-4]
	if ans.Verb != ownerwire.GenesisVerbAnswer || ans.Round != 3 || len(ans.Answers) != 3 ||
		!reflect.DeepEqual(ans.Answers[0], ownerwire.GenesisAnswer{Node: "software.stack", Kind: ownerwire.GenesisKindOther, Text: "Go and a small database"}) ||
		!reflect.DeepEqual(ans.Answers[1], ownerwire.GenesisAnswer{Node: "software.data", Kind: ownerwire.GenesisKindYouDecide}) ||
		!reflect.DeepEqual(ans.Answers[2].IDs, []string{"passkey"}) {
		t.Fatalf("the third answer reached the gate as %+v", ans)
	}
	// The owner's words are marked so; every row has its question.
	var stack *seatapp.GenesisRow
	for _, g := range [][]seatapp.GenesisRow{v.YouChose, v.DecidedForYou, v.Defaults} {
		for i := range g {
			if g[i].Question == nil || g[i].Question.ID != g[i].Node || g[i].Question.Prompt != g[i].Label {
				t.Errorf("the row %s has the question %+v", g[i].Node, g[i].Question)
			}
			if g[i].Node == "software.stack" {
				stack = &g[i]
			}
		}
	}
	if stack == nil || !stack.OwnersWords || stack.Value != "Go and a small database" {
		t.Fatalf("the stack row: %+v", stack)
	}
	// A change, then the summary again; the list says where the thread
	// stands; finish freezes it.
	clock.advance(time.Minute)
	ch := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbChange, Thread: id, Node: "goal",
		Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"share"}}})
	if ch.Thread != id || (ch.State != seatapp.GenesisStateSummary && ch.State != seatapp.GenesisStateQuestion) {
		t.Fatalf("the change: %+v", ch)
	}
	if l := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbList}); len(l.Threads) != 1 || l.Threads[0].Thread != id || l.Threads[0].State != ch.State {
		t.Fatalf("the list: %+v", l)
	}
	sum := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbSummary, Thread: id})
	if sum.State != seatapp.GenesisStateSummary {
		t.Fatalf("the summary verb: %+v", sum)
	}
	fin := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbFinish, Thread: id})
	if fin.State != seatapp.GenesisStateFinished || len(fin.Digest) != 64 {
		t.Fatalf("the finish: %+v", fin)
	}
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: id, Round: 1, Answers: rounds[0]}); !errors.Is(err, seatapp.ErrGenesisDone) {
		t.Fatalf("an answer to a finished thread: %v", err)
	}
	// A second thread, discarded: unknown afterwards.
	other := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart})
	if dv := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbDiscard, Thread: other.Thread}); dv.State != seatapp.GenesisStateDiscarded {
		t.Fatalf("the discard: %+v", dv)
	}
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: other.Thread}); !errors.Is(err, seatapp.ErrGenesisUnknownThread) {
		t.Fatalf("a round of a discarded thread: %v", err)
	}
	// Secret-shaped text is refused, and the error holds none of it.
	third := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart})
	secret := "-----BEGIN OPENSSH PRIVATE KEY-----"
	_, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: third.Thread, Round: 1,
		Answers: []seatapp.GenesisAnswer{{Node: "kind", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindOther, Text: secret}}}})
	if !errors.Is(err, seatapp.ErrGenesisSecretShaped) || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "OPENSSH") {
		t.Fatalf("a secret: %v", err)
	}
	// A third open thread is busy.
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart})
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart}); !errors.Is(err, seatapp.ErrGenesisBusy) {
		t.Fatalf("a third thread: %v", err)
	}
}

// A call the wire does not take is not sent: a thread id of another form, an
// answer the wire refuses, a verb that is not one.
func TestSeatLinkGenesisCallsNotOfTheFormAreNotSent(t *testing.T) {
	_, d, g, _ := genesisGate(t, []string{"genesis"})
	const id = "0123456789abcdef"
	choice := func(ids ...string) []seatapp.GenesisAnswer {
		return []seatapp.GenesisAnswer{{Node: "kind", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: ids}}}
	}
	for name, c := range map[string]struct {
		call seatapp.GenesisCall
		want error
	}{
		"a thread in capitals":   {seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: strings.ToUpper(id)}, seatapp.ErrGenesisUnknownThread},
		"a short thread":         {seatapp.GenesisCall{Verb: seatapp.GenesisVerbFinish, Thread: id[:15]}, seatapp.ErrGenesisUnknownThread},
		"a path for a thread":    {seatapp.GenesisCall{Verb: seatapp.GenesisVerbDiscard, Thread: "../../etc/passwd"}, seatapp.ErrGenesisUnknownThread},
		"no thread":              {seatapp.GenesisCall{Verb: seatapp.GenesisVerbSummary}, seatapp.ErrGenesisUnknownThread},
		"an id in capitals":      {seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: id, Round: 1, Answers: choice("Software")}, seatapp.ErrGenesisBadAnswer},
		"two ids for a choice":   {seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: id, Round: 1, Answers: choice("a", "b")}, seatapp.ErrGenesisBadAnswer},
		"no answer":              {seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: id, Round: 1}, seatapp.ErrGenesisBadAnswer},
		"round seven":            {seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: id, Round: 7, Answers: choice("a")}, seatapp.ErrGenesisBadAnswer},
		"text over its cap":      {seatapp.GenesisCall{Verb: seatapp.GenesisVerbChange, Thread: id, Node: "goal", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindOther, Text: strings.Repeat("x", 201)}}, seatapp.ErrGenesisBadAnswer},
		"text with a newline":    {seatapp.GenesisCall{Verb: seatapp.GenesisVerbChange, Thread: id, Node: "goal", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindOther, Text: "a\nb"}}, seatapp.ErrGenesisBadAnswer},
		"a change of no kind":    {seatapp.GenesisCall{Verb: seatapp.GenesisVerbChange, Thread: id, Node: "goal"}, seatapp.ErrGenesisBadAnswer},
		"a start with a thread":  {seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart, Thread: id}, errGenesisCall},
		"a list with a round":    {seatapp.GenesisCall{Verb: seatapp.GenesisVerbList, Round: 1}, errGenesisCall},
		"a verb that is not one": {seatapp.GenesisCall{Verb: "approve"}, errGenesisCall},
	} {
		if _, err := d.Genesis(bg, c.call); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if n := g.calls.Load(); n != 0 {
		t.Errorf("the gate was asked %d times", n)
	}
}

// The gate's codes map to the app's errors; another code is a RefusedError.
func TestSeatLinkGenesisCodes(t *testing.T) {
	f, d, _, _ := genesisGate(t, []string{"genesis"})
	for code, want := range map[string]error{
		"genesis-busy": seatapp.ErrGenesisBusy, "genesis-unknown-thread": seatapp.ErrGenesisUnknownThread,
		"genesis-bad-answer": seatapp.ErrGenesisBadAnswer, "genesis-over-cap": seatapp.ErrGenesisOverCap,
		"genesis-secret-shaped": seatapp.ErrGenesisSecretShaped, "genesis-done": seatapp.ErrGenesisDone,
		"genesis-stale-round": seatapp.ErrGenesisStaleRound, "unknown-op": seatapp.ErrNotOffered,
		"rate-limited": seatapp.ErrBusy, "busy": seatapp.ErrBusy, "too-many-refusals": seatapp.ErrBusy,
	} {
		f.set(nil, code)
		if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart}); !errors.Is(err, want) {
			t.Errorf("%s: %v", code, err)
		}
	}
	f.set(nil, "failed")
	var re *RefusedError
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart}); !errors.As(err, &re) || re.Op != ownerwire.OpSeatGenesis || re.Code != "failed" {
		t.Errorf("another code: %v", err)
	}
}

// An answer not of the wire's form, or not the answer to the call, is an
// error that holds nothing of it.
func TestSeatLinkGenesisAnswersNotOfTheForm(t *testing.T) {
	f, d, _, clock := genesisGate(t, []string{"genesis"})
	const id, other = "0123456789abcdef", "fedcba9876543210"
	mark := "MARKER-TEXT"
	q := func(thread string) ownerwire.SeatGenesisResult {
		return ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateQuestion, Thread: thread, Round: 1, Of: 6,
			CatalogDigest: strings.Repeat("ab", 32), Progress: ownerwire.GenesisProgress{Total: 1},
			Nodes: []ownerwire.GenesisNode{{ID: "kind", Type: "single", Prompt: mark, Topic: "kind",
				Options: []ownerwire.GenesisOption{{ID: "a", Label: mark, Example: mark}, {ID: "b", Label: "B", Example: "b"}}}}}
	}
	enc := func(r ownerwire.SeatGenesisResult) []byte {
		b, err := ownerwire.EncodeSeatGenesisResult(r)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	fin := q(id)
	fin.State, fin.Nodes, fin.Digest = ownerwire.GenesisStateFinished, []ownerwire.GenesisNode{}, strings.Repeat("cd", 32)
	fin.Groups = &ownerwire.GenesisGroups{YouChose: []ownerwire.GenesisRow{}, DecidedForYou: []ownerwire.GenesisRow{}, Defaults: []ownerwire.GenesisRow{}}
	threads := ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateThreads, Nodes: []ownerwire.GenesisNode{}, Threads: []ownerwire.GenesisThread{}}
	for name, c := range map[string]struct {
		call   seatapp.GenesisCall
		answer []byte
	}{
		"a member the wire lacks":    {seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: id}, []byte(`{"x":"` + mark + `"}`)},
		"another thread":             {seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: id}, enc(q(other))},
		"a question for a finish":    {seatapp.GenesisCall{Verb: seatapp.GenesisVerbFinish, Thread: id}, enc(q(id))},
		"a finished for an answer":   {seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: id, Round: 1, Answers: []seatapp.GenesisAnswer{{Node: "kind", Pick: seatapp.GenesisPick{Kind: "skip"}}}}, enc(fin)},
		"a list for a round":         {seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: id}, enc(threads)},
		"a question for a list":      {seatapp.GenesisCall{Verb: seatapp.GenesisVerbList}, enc(q(id))},
		"a discarded for a discard?": {seatapp.GenesisCall{Verb: seatapp.GenesisVerbDiscard, Thread: id}, enc(fin)},
	} {
		f.set(c.answer, "")
		clock.advance(time.Hour)
		_, err := d.Genesis(bg, c.call)
		if err == nil || !errors.Is(err, errGenesisAnswer) || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), mark) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Every call sent takes the room of the interview; without room it is busy
// and is not sent. Other kinds keep their own room.
func TestSeatLinkGenesisRoom(t *testing.T) {
	f, d, g, _ := genesisGate(t, []string{"read", "genesis"})
	clock := &testClock{t: decideNow}
	d.budget.now = clock.now
	sent := 0
	for i := 0; i < genesisRoom; i++ {
		if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: "0123456789abcdef"}); !errors.Is(err, seatapp.ErrGenesisUnknownThread) {
			t.Fatalf("call %d: %v", i, err)
		}
		sent++
	}
	before := g.calls.Load()
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: "0123456789abcdef"}); !errors.Is(err, seatapp.ErrBusy) {
		t.Fatalf("a call past the room: %v", err)
	}
	if g.calls.Load() != before || len(f.seen()) != sent {
		t.Fatal("a call past the room was sent")
	}
	// The reads have their own room still.
	if !d.budget.take(kindRead) {
		t.Fatal("the reads' room was taken")
	}
	clock.advance(BudgetWindow)
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart}); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

// The link's memory: a list is answered from the last one for listFresh, and
// a round from the last answer of its thread for viewFresh; a call that
// changes threads forgets both before it is sent, whatever comes back; and a
// thread the gate no longer knows is forgotten.
func TestSeatLinkGenesisMemory(t *testing.T) {
	f, d, g, clock := genesisGate(t, []string{"genesis"})
	calls := func() int64 { return g.calls.Load() }
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbList})
	n := calls()
	for i := 0; i < 5; i++ {
		clock.advance(listFresh / 5)
		if i < 4 {
			call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbList})
			if calls() != n {
				t.Fatalf("a list %v after the last asked the gate", time.Duration(i+1)*listFresh/5)
			}
		}
	}
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbList})
	if calls() != n+1 {
		t.Fatal("a stale list was not asked again")
	}
	// A start forgets the list; the round of its thread is the start's.
	st := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart})
	n = calls()
	if r := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: st.Thread}); !reflect.DeepEqual(r, st) || calls() != n {
		t.Fatalf("the round after the start: %+v (%d calls)", r, calls()-n)
	}
	if l := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbList}); len(l.Threads) != 1 || calls() != n+1 {
		t.Fatalf("the list after a start: %+v (%d calls)", l, calls()-n)
	}
	// An answer: the round after it is the answer's, with no reset, and
	// asks nothing.
	ans := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: st.Thread, Round: 1,
		Answers: []seatapp.GenesisAnswer{{Node: "kind", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"software"}}}}})
	n = calls()
	if r := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: st.Thread}); r.Round != 2 || !reflect.DeepEqual(r, ans) || calls() != n {
		t.Fatalf("the round after the answer: %+v (%d calls)", r, calls()-n)
	}
	// A refused answer (here a round already taken, which is stale) forgets
	// the thread: the next round asks the gate.
	_, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: st.Thread, Round: 1,
		Answers: []seatapp.GenesisAnswer{{Node: "kind", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"software"}}}}})
	if !errors.Is(err, seatapp.ErrGenesisStaleRound) {
		t.Fatalf("an answer to an old round: %v", err)
	}
	n = calls()
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: st.Thread})
	if calls() != n+1 {
		t.Fatal("a round after a refused answer was not asked")
	}
	// The round's own answer is kept for viewFresh, and not after.
	clock.advance(viewFresh - time.Second)
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: st.Thread})
	if calls() != n+1 {
		t.Fatal("a round in viewFresh asked the gate")
	}
	clock.advance(time.Second)
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: st.Thread})
	if calls() != n+2 {
		t.Fatal("a stale round was not asked again")
	}
	// A thread the gate does not know (the daemon expired it) is
	// forgotten at once.
	f.set(nil, "genesis-unknown-thread")
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbSummary, Thread: st.Thread}); !errors.Is(err, seatapp.ErrGenesisUnknownThread) {
		t.Fatalf("summary: %v", err)
	}
	n = calls()
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: st.Thread}); !errors.Is(err, seatapp.ErrGenesisUnknownThread) || calls() != n+1 {
		t.Fatalf("the round of a thread the gate forgot: %v", err)
	}
	// A discard leaves nothing kept of its thread.
	f.set(nil, "")
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbDiscard, Thread: st.Thread})
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: st.Thread}); !errors.Is(err, seatapp.ErrGenesisUnknownThread) {
		t.Fatalf("a round after the discard: %v", err)
	}
}

// res is a result of the memory's tests: a round of the thread.
func res(thread string, round int) *ownerwire.SeatGenesisResult {
	return &ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateQuestion, Thread: thread, Round: round}
}

// An answer that crossed a call changing its thread is not kept.
func TestSeatLinkGenesisMemoryKeepsNothingThatCrossedAChange(t *testing.T) {
	var m genesisMemory
	now := decideNow
	round := seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: "0123456789abcdef"}
	gen, _, ok := m.before(round, now)
	if ok {
		t.Fatal("an empty memory answered")
	}
	// An answer to the thread begins and ends while the round is asked.
	answer := seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: round.Thread}
	agen, _, _ := m.before(answer, now)
	m.after(answer, res(round.Thread, 2), agen, now)
	m.after(round, res(round.Thread, 1), gen, now)
	if _, got, ok := m.before(round, now); !ok || got.Round != 2 {
		t.Fatalf("the memory holds round %d (%v), want the answer's 2", got.Round, ok)
	}
	// A list that crossed a start is not kept either.
	list := seatapp.GenesisCall{Verb: seatapp.GenesisVerbList}
	lgen, _, _ := m.before(list, now)
	start := seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart}
	sgen, _, _ := m.before(start, now)
	m.after(list, &ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateThreads}, lgen, now)
	if _, _, ok := m.before(list, now); ok {
		t.Fatal("a list that crossed a start was kept")
	}
	m.after(start, nil, sgen, now)
	// The memory holds at most maxViews threads.
	for i := 0; i < 3*maxViews; i++ {
		c := seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: fmt.Sprintf("%016x", i)}
		g, _, _ := m.before(c, now)
		m.after(c, res(c.Thread, 1), g, now)
		if len(m.views) > maxViews {
			t.Fatalf("%d threads kept", len(m.views))
		}
	}
}

// A read that began after a change was sent and came back before it (so no
// change began while it was asked) is not kept past the change's end: the
// change moves the generation on again when it ends, whatever its answer, and
// forgets what was kept meanwhile. Its own answer is kept when nothing else
// changed.
func TestSeatLinkGenesisMemoryForgetsReadsThatOverlappedAChange(t *testing.T) {
	const th = "0123456789abcdef"
	now := decideNow
	round := seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: th}
	list := seatapp.GenesisCall{Verb: seatapp.GenesisVerbList}
	for name, c := range map[string]struct {
		change seatapp.GenesisCall
		// answer is the change's own answer, or nil for one that was
		// refused, lost or failed.
		answer *ownerwire.SeatGenesisResult
	}{
		"an answer that came back":  {seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: th}, res(th, 2)},
		"an answer that was lost":   {seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: th}, nil},
		"a change that was refused": {seatapp.GenesisCall{Verb: seatapp.GenesisVerbChange, Thread: th}, nil},
		"a finish that came back":   {seatapp.GenesisCall{Verb: seatapp.GenesisVerbFinish, Thread: th}, &ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateFinished, Thread: th}},
		"a discard that came back":  {seatapp.GenesisCall{Verb: seatapp.GenesisVerbDiscard, Thread: th}, &ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateDiscarded, Thread: th}},
	} {
		var m genesisMemory
		// The change is sent.
		cgen, _, _ := m.before(c.change, now)
		// A round and a list begin after it, and come back before it with
		// the state before it.
		rgen, _, ok := m.before(round, now)
		if ok {
			t.Fatalf("%s: the memory answered a round while the change was out", name)
		}
		lgen, _, _ := m.before(list, now)
		m.after(round, res(th, 1), rgen, now)
		m.after(list, &ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateThreads,
			Threads: []ownerwire.GenesisThread{{Thread: th, State: ownerwire.GenesisStateQuestion, Round: 1, Of: 6}}}, lgen, now)
		// The change ends.
		m.after(c.change, c.answer, cgen, now)
		if _, _, ok := m.before(list, now); ok {
			t.Errorf("%s: the list from before the change is kept", name)
		}
		got, kept := m.views[th]
		switch {
		case c.answer != nil && c.change.Verb != seatapp.GenesisVerbDiscard:
			if !kept || !reflect.DeepEqual(got.res, *c.answer) {
				t.Errorf("%s: the thread's view is %+v (%v), want the change's own answer", name, got.res, kept)
			}
		case kept:
			t.Errorf("%s: the round from before the change is kept: %+v", name, got.res)
		}
		// A read that ends after the change ended, having begun before
		// it ended, is not kept either.
		m = genesisMemory{}
		cgen, _, _ = m.before(c.change, now)
		rgen, _, _ = m.before(round, now)
		m.after(c.change, c.answer, cgen, now)
		m.after(round, res(th, 1), rgen, now)
		if got, kept := m.views[th]; kept && got.res.Round == 1 {
			t.Errorf("%s: a round that crossed the change's end is kept", name)
		}
	}
}

// An answer that came back is the thread's next round: the page its post
// redirects to is one call of the gate with the answer, not two, and it shows
// the answer's view. The view lives viewFresh, and a call on another thread
// leaves it where it is.
func TestSeatLinkGenesisAnswerIsTheNextRound(t *testing.T) {
	_, d, g, clock := genesisGate(t, []string{"genesis"})
	a := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart})
	b := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart})
	pick := func(th string) seatapp.GenesisCall {
		return seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: th, Round: 1,
			Answers: []seatapp.GenesisAnswer{{Node: "kind", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"software"}}}}}
	}
	ansA := call(t, d, pick(a.Thread))
	n := g.calls.Load()
	if r := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: a.Thread}); !reflect.DeepEqual(r, ansA) || r.Round != 2 || g.calls.Load() != n {
		t.Fatalf("the round after the answer: %+v (%d calls), want the answer's %+v and none", r, g.calls.Load()-n, ansA)
	}
	// Another thread's answer leaves this thread's memory as it is.
	ansB := call(t, d, pick(b.Thread))
	n = g.calls.Load()
	if r := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: a.Thread}); !reflect.DeepEqual(r, ansA) || g.calls.Load() != n {
		t.Fatalf("the round of the first thread after an answer on the second: %+v (%d calls)", r, g.calls.Load()-n)
	}
	if r := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: b.Thread}); !reflect.DeepEqual(r, ansB) || g.calls.Load() != n {
		t.Fatalf("the round of the second thread: %+v (%d calls)", r, g.calls.Load()-n)
	}
	// It lives viewFresh, counted from the answer.
	clock.advance(viewFresh - time.Second)
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: a.Thread})
	if g.calls.Load() != n {
		t.Fatal("a round just inside viewFresh asked the gate")
	}
	clock.advance(time.Second)
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: a.Thread})
	if g.calls.Load() != n+1 {
		t.Fatalf("a round at viewFresh was answered from the memory (%d calls)", g.calls.Load()-n)
	}
}

// An answer the gate refused keeps nothing and forgets its thread, and so does
// one the link could not read: the next round asks the gate, which says where
// the thread stands.
func TestSeatLinkGenesisRefusedAnswerKeepsNothing(t *testing.T) {
	f, d, g, _ := genesisGate(t, []string{"genesis"})
	st := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart})
	// The start's view is kept; a refused answer drops it.
	f.set(nil, "genesis-stale-round")
	_, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: st.Thread, Round: 1,
		Answers: []seatapp.GenesisAnswer{{Node: "kind", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"software"}}}}})
	if !errors.Is(err, seatapp.ErrGenesisStaleRound) {
		t.Fatalf("a refused answer: %v", err)
	}
	f.set(nil, "")
	n := g.calls.Load()
	if r := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: st.Thread}); r.Round != 1 || g.calls.Load() != n+1 {
		t.Fatalf("the round after a refused answer: %+v (%d calls), want one call and round 1", r, g.calls.Load()-n)
	}
}

// Two changes of threads that overlap keep neither answer, and the memory is
// left holding nothing of the thread: each began or ended while the other
// was out. A change that ran alone is kept.
func TestSeatLinkGenesisMemoryKeepsNeitherOfTwoOverlappingChanges(t *testing.T) {
	const th = "0123456789abcdef"
	now := decideNow
	var m genesisMemory
	first := seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: th}
	second := seatapp.GenesisCall{Verb: seatapp.GenesisVerbChange, Thread: th}
	g1, _, _ := m.before(first, now)
	g2, _, _ := m.before(second, now)
	m.after(first, res(th, 2), g1, now)
	m.after(second, res(th, 3), g2, now)
	if _, _, ok := m.before(seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: th}, now); ok || len(m.views) != 0 {
		t.Fatalf("overlapping changes left %d views kept", len(m.views))
	}
	// The same two, ended in the other order.
	m = genesisMemory{}
	g1, _, _ = m.before(first, now)
	g2, _, _ = m.before(second, now)
	m.after(second, res(th, 3), g2, now)
	m.after(first, res(th, 2), g1, now)
	if len(m.views) != 0 {
		t.Fatalf("overlapping changes, ended the other way, left %d views kept", len(m.views))
	}
	// A change on another thread that overlaps keeps neither as well (the
	// link holds one generation), but what was kept of a third thread
	// before both began stays.
	m = genesisMemory{}
	other := "fedcba9876543210"
	og, _, _ := m.before(seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: other}, now)
	m.after(seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: other}, res(other, 1), og, now)
	g1, _, _ = m.before(first, now)
	m.after(first, res(th, 2), g1, now)
	if _, got, ok := m.before(seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: other}, now); !ok || got.Round != 1 {
		t.Fatalf("another thread's memory after a change: %d (%v), want its round 1 kept", got.Round, ok)
	}
	if _, got, ok := m.before(seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: th}, now); !ok || got.Round != 2 {
		t.Fatalf("the changed thread's memory: %d (%v), want the answer's round 2", got.Round, ok)
	}
}

// A summary's rows carry no question when the thread is walked under a
// catalog this build does not hold.
func TestSeatLinkGenesisRowsOfAnotherCatalogHaveNoQuestion(t *testing.T) {
	f, d, _, _ := genesisGate(t, []string{"genesis"})
	res := ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateSummary, Thread: "0123456789abcdef", Round: 1, Of: 6,
		CatalogDigest: strings.Repeat("ab", 32), Progress: ownerwire.GenesisProgress{Filled: 1, Total: 1}, Nodes: []ownerwire.GenesisNode{},
		Groups: &ownerwire.GenesisGroups{YouChose: []ownerwire.GenesisRow{{Node: "kind", Field: "kind", Label: "What kind of project is it?", Value: "Software"}},
			DecidedForYou: []ownerwire.GenesisRow{}, Defaults: []ownerwire.GenesisRow{}}}
	b, err := ownerwire.EncodeSeatGenesisResult(res)
	if err != nil {
		t.Fatal(err)
	}
	f.set(b, "")
	v := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbSummary, Thread: res.Thread})
	if len(v.YouChose) != 1 || v.YouChose[0].Question != nil || v.YouChose[0].Value != "Software" {
		t.Fatalf("the rows: %+v", v.YouChose)
	}
	// Under catalog v1, the same row has its question.
	cat, err := v1()
	if err != nil {
		t.Fatal(err)
	}
	res.CatalogDigest = cat.Digest()
	if b, err = ownerwire.EncodeSeatGenesisResult(res); err != nil {
		t.Fatal(err)
	}
	f.set(b, "")
	v = call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbSummary, Thread: res.Thread})
	if q := v.YouChose[0].Question; q == nil || q.ID != "kind" || len(q.Options) != 3 || !q.Other {
		t.Fatalf("the question of the row: %+v", q)
	}
}
