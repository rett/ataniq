package gatehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatgate"
)

const aThread = "0123456789abcdef"

// gateErr is an error of the thread store: it carries a gate word, as the
// store's refusals do.
type gateErr struct{ code string }

func (e *gateErr) Error() string    { return "genesis: " + e.code }
func (e *gateErr) GateCode() string { return e.code }

// fakeGenesis is the thread store as the handler sees it: it records the
// params it was given and answers a result of the verb, or err.
type fakeGenesis struct {
	mu     sync.Mutex
	params []ownerwire.SeatGenesisParams
	ctxs   []context.Context
	err    error
	// result, when set, replaces the result of the verb.
	result *ownerwire.SeatGenesisResult
}

func aQuestion() ownerwire.SeatGenesisResult {
	return ownerwire.SeatGenesisResult{
		State: ownerwire.GenesisStateQuestion, Thread: aThread, Round: 1, Of: 6, CatalogDigest: strings.Repeat("ab", 32),
		Progress: ownerwire.GenesisProgress{Filled: 0, Total: 1},
		Nodes: []ownerwire.GenesisNode{{
			ID: "kind", Type: ownerwire.GenesisTypeSingle, Prompt: "What kind of project is it?", Topic: "kind", AllowOther: true,
			Options: []ownerwire.GenesisOption{{ID: "software", Label: "Software", Example: "an app"}, {ID: "media", Label: "Media", Example: "a video"}},
		}},
	}
}

// resultOf is the result the fake gives each verb.
func resultOf(p ownerwire.SeatGenesisParams) ownerwire.SeatGenesisResult {
	r := aQuestion()
	switch p.Verb {
	case ownerwire.GenesisVerbSummary, ownerwire.GenesisVerbFinish:
		r.State, r.Nodes, r.Round = ownerwire.GenesisStateSummary, []ownerwire.GenesisNode{}, 0
		r.Groups = &ownerwire.GenesisGroups{
			YouChose:      []ownerwire.GenesisRow{{Node: "kind", Field: "kind", Label: "What kind of project is it?", Value: "Software"}},
			DecidedForYou: []ownerwire.GenesisRow{}, Defaults: []ownerwire.GenesisRow{},
		}
		if p.Verb == ownerwire.GenesisVerbFinish {
			r.State, r.Digest = ownerwire.GenesisStateFinished, strings.Repeat("cd", 32)
		}
	case ownerwire.GenesisVerbDiscard:
		r.State, r.Nodes, r.Round, r.Progress = ownerwire.GenesisStateDiscarded, []ownerwire.GenesisNode{}, 0, ownerwire.GenesisProgress{}
	case ownerwire.GenesisVerbChange:
		r.Reset = 3
	case ownerwire.GenesisVerbList:
		r = ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateThreads, Nodes: []ownerwire.GenesisNode{}, Threads: []ownerwire.GenesisThread{
			{Thread: aThread, State: ownerwire.GenesisStateQuestion, Round: 1, Of: 6, Filled: 0, Total: 1},
		}}
	}
	return r
}

func (f *fakeGenesis) Genesis(ctx context.Context, p ownerwire.SeatGenesisParams) (ownerwire.SeatGenesisResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.params = append(f.params, p)
	f.ctxs = append(f.ctxs, ctx)
	if f.err != nil {
		return ownerwire.SeatGenesisResult{}, f.err
	}
	if f.result != nil {
		return *f.result, nil
	}
	return resultOf(p), nil
}

func (f *fakeGenesis) seen() []ownerwire.SeatGenesisParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ownerwire.SeatGenesisParams(nil), f.params...)
}

// newGenesisHandler makes a handler given the fake store.
func newGenesisHandler(t *testing.T, g GenesisPort) (*Handler, *logBook) {
	t.Helper()
	lb := &logBook{}
	h, err := New(&fakePort{}, pinnedOnly, lb.logf, WithGenesis(g))
	if err != nil {
		t.Fatal(err)
	}
	return h, lb
}

// everyVerb are params of each of the eight verbs, each with the members it
// takes.
func everyVerb() []ownerwire.SeatGenesisParams {
	return []ownerwire.SeatGenesisParams{
		{Verb: ownerwire.GenesisVerbStart},
		{Verb: ownerwire.GenesisVerbRound, Thread: aThread},
		{Verb: ownerwire.GenesisVerbAnswer, Thread: aThread, Round: 2, Answers: []ownerwire.GenesisAnswer{
			{Node: "goal", Kind: ownerwire.GenesisKindChoice, IDs: []string{"try"}},
			{Node: "audience", Kind: ownerwire.GenesisKindOther, Text: "the people of my street"},
			{Node: "software.stack", Kind: ownerwire.GenesisKindChoices, IDs: []string{"go", "python"}},
		}},
		{Verb: ownerwire.GenesisVerbChange, Thread: aThread, Node: "goal", Answer: &ownerwire.GenesisPick{Kind: ownerwire.GenesisKindYouDecide}},
		{Verb: ownerwire.GenesisVerbSummary, Thread: aThread},
		{Verb: ownerwire.GenesisVerbFinish, Thread: aThread},
		{Verb: ownerwire.GenesisVerbDiscard, Thread: aThread},
		{Verb: ownerwire.GenesisVerbList},
	}
}

func genesisParams(t testing.TB, p ownerwire.SeatGenesisParams) json.RawMessage {
	t.Helper()
	b, err := ownerwire.EncodeSeatGenesisParams(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The handler lists genesis last, and only when it was given the store: with
// either constructor, with or without a service that can wait.
func TestGenesisIsListedOnlyWithTheStore(t *testing.T) {
	g := &fakeGenesis{}
	for _, c := range []struct {
		name string
		make func() (*Handler, error)
		want []string
	}{
		{"New", func() (*Handler, error) { return New(&fakePort{}, pinnedOnly, nil) }, []string{"pair", "read"}},
		{"New with a store", func() (*Handler, error) { return New(&fakePort{}, pinnedOnly, nil, WithGenesis(g)) }, []string{"pair", "read", "genesis"}},
		{"New with a nil store", func() (*Handler, error) { return New(&fakePort{}, pinnedOnly, nil, WithGenesis(nil)) }, []string{"pair", "read"}},
		{"New over a service that waits", func() (*Handler, error) { return New(&waitingPort{}, pinnedOnly, nil, WithGenesis(g)) }, []string{"pair", "read", "watch", "genesis"}},
		{"NewDeciding", func() (*Handler, error) {
			return NewDeciding(&fakePort{}, &fakeSeat{}, theGate, pinnedOnly, nil)
		}, []string{"pair", "read", "login", "decide"}},
		{"NewDeciding with a store", func() (*Handler, error) {
			return NewDeciding(&fakePort{}, &fakeSeat{}, theGate, pinnedOnly, nil, WithGenesis(g))
		}, []string{"pair", "read", "login", "decide", "genesis"}},
		{"NewDeciding over a service that waits with a store", func() (*Handler, error) {
			return NewDeciding(&waitingPort{}, &fakeSeat{}, theGate, pinnedOnly, nil, WithGenesis(g))
		}, []string{"pair", "read", "login", "decide", "watch", "genesis"}},
	} {
		h, err := c.make()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := h.Caps(); !slices.Equal(got, c.want) {
			t.Errorf("%s lists %q, want %q", c.name, got, c.want)
		}
	}
	if len(Caps()) != 2 || len(DecidingCaps()) != 4 {
		t.Errorf("Caps %q, DecidingCaps %q hold the store", Caps(), DecidingCaps())
	}
}

// Without the store, seat.genesis is unknown-op before its params are read,
// and the refusal counts.
func TestWithoutTheStoreGenesisIsUnknown(t *testing.T) {
	for _, mk := range []func(t *testing.T) (*Handler, *logBook){
		func(t *testing.T) (*Handler, *logBook) { return newHandler(t, &fakePort{}) },
		func(t *testing.T) (*Handler, *logBook) {
			h, lb, _ := newDeciding(t, &fakePort{}, &fakeSeat{})
			return h, lb
		},
	} {
		h, lb := mk(t)
		for _, params := range []json.RawMessage{nil, genesisParams(t, everyVerb()[0]), json.RawMessage(`not json`)} {
			out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatGenesis, params)
			if out != nil || codeOfErr(err) != CodeUnknownOp {
				t.Fatalf("%s: %s %v", params, out, err)
			}
		}
		for _, l := range lb.all() {
			if !strings.Contains(l, "an unknown operation refused unknown-op") {
				t.Fatalf("log %q", l)
			}
		}
		if h.Counts()[CodeUnknownOp] != 3 {
			t.Fatalf("counts %v", h.Counts())
		}
	}
}

// Each verb reaches the store as the wire's decoder read it, and its result is
// the wire's encoding of the store's; the source of the call is nothing the
// store is given, and a served call writes no line and counts nothing.
func TestEveryVerbIsHandedToTheStoreAndAnswered(t *testing.T) {
	g := &fakeGenesis{}
	h, lb := newGenesisHandler(t, g)
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")
	for _, p := range everyVerb() {
		out, err := h.Handle(ctx, seat, ownerwire.OpSeatGenesis, genesisParams(t, p))
		if err != nil {
			t.Fatalf("%s: %v", p.Verb, err)
		}
		got, err := ownerwire.DecodeSeatGenesisResult(out)
		if err != nil || !reflect.DeepEqual(got, resultOf(p)) {
			t.Fatalf("%s answered %s: %v", p.Verb, out, err)
		}
	}
	if seen := g.seen(); !reflect.DeepEqual(seen, everyVerb()) {
		t.Fatalf("the store was given %+v", seen)
	}
	for _, c := range g.ctxs {
		if c.Value(key{}) != "marker" {
			t.Fatal("the store was not given the request's context")
		}
	}
	if len(lb.all()) != 0 || len(h.Counts()) != 0 {
		t.Fatalf("a served call logged or counted: %q %v", lb.all(), h.Counts())
	}
}

// Over the real gate on the loopback address: the hello lists genesis, each
// verb is served, and a refusal is the word on the wire.
func TestGenesisOverTheGate(t *testing.T) {
	g := &fakeGenesis{}
	gate := serveWith(t, func(isSeat func(string) bool, lb *logBook) *Handler {
		h, err := New(&fakePort{}, isSeat, lb.logf, WithGenesis(g))
		if err != nil {
			t.Fatal(err)
		}
		return h
	})
	c := gate.dial()
	if !slices.Equal(c.Caps(), []string{"pair", "read", "genesis"}) {
		t.Fatalf("the hello listed %q", c.Caps())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, p := range everyVerb() {
		var res ownerwire.SeatGenesisResult
		if err := c.Call(ctx, ownerwire.OpSeatGenesis, genesisParams(t, p), &res); err != nil {
			t.Fatalf("%s: %v", p.Verb, err)
		}
		if !reflect.DeepEqual(res, resultOf(p)) {
			t.Fatalf("%s: %+v", p.Verb, res)
		}
	}
	g.err = &gateErr{ownerwire.CodeGenesisBusy}
	err := c.Call(ctx, ownerwire.OpSeatGenesis, genesisParams(t, everyVerb()[0]), nil)
	if remoteCode(err) != ownerwire.CodeGenesisBusy {
		t.Fatalf("a busy store: %v", err)
	}
	// A frame the wire's decoder refuses never reaches the store.
	n := len(g.seen())
	err = c.Call(ctx, ownerwire.OpSeatGenesis, json.RawMessage(`{"verb":"start","thread":"0123456789abcdef"}`), nil)
	if remoteCode(err) != CodeBadParams || len(g.seen()) != n {
		t.Fatalf("a start with a thread: %v (%d calls)", err, len(g.seen())-n)
	}
	// An operation of the owner connection is still unknown-op.
	err = c.Call(ctx, ownerwire.OpDecide, json.RawMessage(`{}`), nil)
	if remoteCode(err) != CodeUnknownOp {
		t.Fatalf("owner.decide: %v", err)
	}
}

// serveWith serves a handler through seatgate.Serve on the loopback address,
// as serveWait does for a service that waits; the hello lists the handler's own
// capabilities.
func serveWith(t *testing.T, mk func(isSeat func(string) bool, lb *logBook) *Handler) *waitGate {
	t.Helper()
	g := &waitGate{t: t, gateID: newIdentity(t, "gate"), seatID: newIdentity(t, "seat"), lb: &logBook{}}
	g.h = mk(func(fp string) bool { return fp == g.seatID.Fingerprint() }, g.lb)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g.addr = ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	cfg := seatgate.ServerConfig{TLS: seatgate.ServerTLS(g.gateID, []string{g.seatID.Fingerprint()}), Caps: g.h.Caps()}
	go func() { served <- seatgate.Serve(ctx, ln, cfg, g.h) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not end")
		}
	})
	return g
}

// Params that the wire's decoder or the gate's strict reading refuse are
// bad-params, without the store.
func TestGenesisParamsAreReadStrictly(t *testing.T) {
	g := &fakeGenesis{}
	for name, params := range map[string]string{
		"unknown verb":                `{"verb":"restart"}`,
		"no verb":                     `{}`,
		"no params":                   ``,
		"not json":                    `not json`,
		"a start with a thread":       `{"verb":"start","thread":"0123456789abcdef"}`,
		"a list with a thread":        `{"verb":"list","thread":"0123456789abcdef"}`,
		"a list with a round":         `{"verb":"list","round":1}`,
		"a round without a thread":    `{"verb":"round"}`,
		"a thread of another form":    `{"verb":"round","thread":"0123456789ABCDEF"}`,
		"a short thread":              `{"verb":"round","thread":"0123456789abcde"}`,
		"a round with a round":        `{"verb":"round","thread":"0123456789abcdef","round":1}`,
		"an answer without answers":   `{"verb":"answer","thread":"0123456789abcdef","round":1}`,
		"an answer of round 0":        `{"verb":"answer","thread":"0123456789abcdef","round":0,"answers":[{"node":"kind","kind":"skip"}]}`,
		"an answer of round 7":        `{"verb":"answer","thread":"0123456789abcdef","round":7,"answers":[{"node":"kind","kind":"skip"}]}`,
		"four answers":                `{"verb":"answer","thread":"0123456789abcdef","round":1,"answers":[{"node":"a","kind":"skip"},{"node":"b","kind":"skip"},{"node":"c","kind":"skip"},{"node":"d","kind":"skip"}]}`,
		"a node twice":                `{"verb":"answer","thread":"0123456789abcdef","round":1,"answers":[{"node":"a","kind":"skip"},{"node":"a","kind":"skip"}]}`,
		"a choice with two ids":       `{"verb":"answer","thread":"0123456789abcdef","round":1,"answers":[{"node":"a","kind":"choice","ids":["x","y"]}]}`,
		"a skip with text":            `{"verb":"answer","thread":"0123456789abcdef","round":1,"answers":[{"node":"a","kind":"skip","text":"x"}]}`,
		"an other with no text":       `{"verb":"answer","thread":"0123456789abcdef","round":1,"answers":[{"node":"a","kind":"other"}]}`,
		"an other over 200 bytes":     `{"verb":"answer","thread":"0123456789abcdef","round":1,"answers":[{"node":"a","kind":"other","text":"` + strings.Repeat("x", 201) + `"}]}`,
		"a change without an answer":  `{"verb":"change","thread":"0123456789abcdef","node":"goal"}`,
		"a change without a node":     `{"verb":"change","thread":"0123456789abcdef","answer":{"kind":"skip"}}`,
		"a member the form lacks":     `{"verb":"round","thread":"0123456789abcdef","admin":true}`,
		"a repeated key":              `{"verb":"round","verb":"round","thread":"0123456789abcdef"}`,
		"a null member":               `{"verb":"round","thread":null}`,
		"a verb that is not a string": `{"verb":1}`,
		"an array":                    `[]`,
	} {
		h, lb := newGenesisHandler(t, g)
		var raw json.RawMessage
		if params != "" {
			raw = json.RawMessage(params)
		}
		out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatGenesis, raw)
		if out != nil || codeOfErr(err) != CodeBadParams {
			t.Errorf("%s: %s %v", name, out, err)
		}
		for _, l := range lb.all() {
			if strings.Contains(l, "xxxx") || strings.Contains(l, "the people") {
				t.Errorf("%s: the log holds params: %q", name, l)
			}
		}
	}
	if n := len(g.seen()); n != 0 {
		t.Fatalf("the store was called %d times", n)
	}
}

// The store's words reach the seat as they are, and every other refusal or
// error is one of the handler's own: unavailable, deadline-exceeded, or failed
// with the cause in the log.
func TestGenesisErrorsMapToTheirCodes(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{&gateErr{ownerwire.CodeGenesisBusy}, "genesis-busy"},
		{&gateErr{ownerwire.CodeGenesisUnknownThread}, "genesis-unknown-thread"},
		{&gateErr{ownerwire.CodeGenesisBadAnswer}, "genesis-bad-answer"},
		{&gateErr{ownerwire.CodeGenesisOverCap}, "genesis-over-cap"},
		{&gateErr{ownerwire.CodeGenesisSecretShaped}, "genesis-secret-shaped"},
		{&gateErr{ownerwire.CodeGenesisDone}, "genesis-done"},
		{&gateErr{ownerwire.CodeGenesisStaleRound}, "genesis-stale-round"},
		{&gateErr{CodeBadParams}, "bad-params"},
		{fmt.Errorf("wrapped: %w", &gateErr{ownerwire.CodeGenesisDone}), "genesis-done"},
		{&gateErr{"unknown-op"}, "failed"},
		{&gateErr{"a word nobody knows"}, "failed"},
		{&gateErr{""}, "failed"},
		{context.Canceled, "unavailable"},
		{context.DeadlineExceeded, "deadline-exceeded"},
		{errors.New("the thread cannot be written"), "failed"},
	} {
		g := &fakeGenesis{err: c.err}
		h, lb := newGenesisHandler(t, g)
		out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatGenesis, genesisParams(t, everyVerb()[1]))
		if out != nil || codeOfErr(err) != c.code || !seatgate.ValidCode(c.code) {
			t.Fatalf("%v: %s %v", c.err, out, err)
		}
		l := lb.all()
		if len(l) != 1 || !strings.Contains(l[0], "seat.genesis refused "+c.code) || !strings.HasSuffix(l[0], " (peer "+seatFP[:16]+")") {
			t.Fatalf("%v: log %q", c.err, l)
		}
		if n := h.Counts()[c.code]; n != 1 {
			t.Fatalf("the count of %s is %d", c.code, n)
		}
	}
	// A failure's cause is logged, as one printable line; the word of a refusal
	// that was not one of seat.genesis is not.
	g := &fakeGenesis{err: errors.New("marker cause\nsecond line")}
	h, lb := newGenesisHandler(t, g)
	h.Handle(context.Background(), seat, ownerwire.OpSeatGenesis, genesisParams(t, everyVerb()[0]))
	if l := lb.all(); len(l) != 1 || !strings.Contains(l[0], "refused failed: marker cause?second line (peer") {
		t.Fatalf("log %q", l)
	}
	g = &fakeGenesis{err: &gateErr{"marker-word"}}
	h, lb = newGenesisHandler(t, g)
	h.Handle(context.Background(), seat, ownerwire.OpSeatGenesis, genesisParams(t, everyVerb()[0]))
	if l := lb.all(); len(l) != 1 || strings.Contains(l[0], "marker-word") {
		t.Fatalf("log %q", l)
	}
	// A result the wire's encoder refuses is failed, and so is one over its cap.
	bad := aQuestion()
	bad.Nodes = nil
	over := aQuestion()
	over.Nodes[0].Prompt = strings.Repeat("x", ownerwire.MaxGenesisPrompt+1)
	for name, res := range map[string]ownerwire.SeatGenesisResult{"no questions": bad, "a prompt over its cap": over} {
		h, lb := newGenesisHandler(t, &fakeGenesis{result: &res})
		_, err := h.Handle(context.Background(), seat, ownerwire.OpSeatGenesis, genesisParams(t, everyVerb()[0]))
		if codeOfErr(err) != CodeFailed {
			t.Errorf("%s: %v", name, err)
		}
		if l := lb.all(); len(l) != 1 || !strings.Contains(l[0], "the service's result does not encode") {
			t.Errorf("%s: log %q", name, l)
		}
	}
}

// The bound counts the refusals of seat.genesis that are a misuse, a bad
// answer and bad params, and none of the others: the store's state (a stale
// round, busy, done, an unknown thread) and what the owner typed (secret-shaped,
// over a cap).
func TestTheBoundCountsGenesisRefusalsAsTheContractSays(t *testing.T) {
	for _, c := range []struct {
		code   string
		counts bool
	}{
		{ownerwire.CodeGenesisBadAnswer, true},
		{CodeBadParams, true},
		{ownerwire.CodeGenesisSecretShaped, false},
		{ownerwire.CodeGenesisOverCap, false},
		{ownerwire.CodeGenesisUnknownThread, false},
		{ownerwire.CodeGenesisBusy, false},
		{ownerwire.CodeGenesisDone, false},
		{ownerwire.CodeGenesisStaleRound, false},
	} {
		h, lb := newGenesisHandler(t, &fakeGenesis{err: &gateErr{c.code}})
		now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
		h.now = func() time.Time { return now }
		params := genesisParams(t, everyVerb()[1])
		for i := 0; i < 3*RefusalBound; i++ {
			_, err := h.Handle(context.Background(), seat, ownerwire.OpSeatGenesis, params)
			shut := codeOfErr(err) == CodeTooManyRefusals
			switch {
			case c.counts && i < RefusalBound && codeOfErr(err) != c.code:
				t.Fatalf("%s: call %d: %v", c.code, i, err)
			case c.counts && i >= RefusalBound && !shut:
				t.Fatalf("%s: call %d is not shut out: %v", c.code, i, err)
			case !c.counts && codeOfErr(err) != c.code:
				t.Fatalf("%s: call %d: %v", c.code, i, err)
			}
		}
		// A counted code is logged up to the bound and then once for the
		// shut-out; the others are logged on every call.
		want := 3 * RefusalBound
		if c.counts {
			want = RefusalBound + 1
		}
		if n := len(lb.all()); n != want {
			t.Errorf("%s: %d log lines, want %d", c.code, n, want)
		}
		// And the shut-out refuses a read as well, the others do not.
		_, err := h.Handle(context.Background(), seat, ownerwire.OpList, nil)
		if (codeOfErr(err) == CodeTooManyRefusals) != c.counts {
			t.Errorf("%s: owner.list after: %v", c.code, err)
		}
	}
}

// The store's state and what the owner typed never shut the seat out, however
// often they answer inside the window: 40 double taps on a round already
// taken leave the seat's reads and decisions served.
func TestGenesisStateAnswersShutNoOneOut(t *testing.T) {
	for _, code := range []string{ownerwire.CodeGenesisStaleRound, ownerwire.CodeGenesisUnknownThread, ownerwire.CodeGenesisBusy,
		ownerwire.CodeGenesisDone, ownerwire.CodeGenesisSecretShaped, ownerwire.CodeGenesisOverCap} {
		g := &fakeGenesis{err: &gateErr{code}}
		h, _ := newGenesisHandler(t, g)
		now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
		h.now = func() time.Time { return now }
		answer := genesisParams(t, everyVerb()[2])
		for i := 0; i < 40; i++ {
			_, err := h.Handle(context.Background(), seat, ownerwire.OpSeatGenesis, answer)
			if codeOfErr(err) != code {
				t.Fatalf("%s: call %d: %v", code, i, err)
			}
		}
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpList, nil); err != nil {
			t.Fatalf("%s: %v", code, err)
		}
	}
}

// The peer must be pinned: an unpinned key is refused not-pinned before the
// store is asked.
func TestGenesisNeedsAPinnedKey(t *testing.T) {
	g := &fakeGenesis{}
	h, _ := newGenesisHandler(t, g)
	_, err := h.Handle(context.Background(), seatgate.Peer{Fingerprint: otherFP}, ownerwire.OpSeatGenesis, genesisParams(t, everyVerb()[0]))
	if codeOfErr(err) != CodeNotPinned || len(g.seen()) != 0 {
		t.Fatalf("%v, %d calls", err, len(g.seen()))
	}
}

// A list is a read like round: it reaches the store with no member, answers
// the open threads, and a seat that lists often is never shut out by it; a
// result of another state, or one with a thread of its own, is not sent.
func TestGenesisListIsServedAndFree(t *testing.T) {
	g := &fakeGenesis{}
	h, lb := newGenesisHandler(t, g)
	list := genesisParams(t, ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbList})
	for i := 0; i < 3*RefusalBound; i++ {
		out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatGenesis, list)
		if err != nil {
			t.Fatalf("list %d: %v", i, err)
		}
		got, err := ownerwire.DecodeSeatGenesisResult(out)
		if err != nil || got.State != ownerwire.GenesisStateThreads || len(got.Threads) != 1 || got.Threads[0].Thread != aThread {
			t.Fatalf("list %d answered %s: %v", i, out, err)
		}
	}
	if seen := g.seen(); len(seen) != 3*RefusalBound || !reflect.DeepEqual(seen[0], ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbList}) {
		t.Fatalf("the store was given %+v", seen)
	}
	if len(lb.all()) != 0 || len(h.Counts()) != 0 {
		t.Fatalf("a list logged or counted: %q %v", lb.all(), h.Counts())
	}
	bad := resultOf(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbList})
	bad.Thread = aThread
	h, lb = newGenesisHandler(t, &fakeGenesis{result: &bad})
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatGenesis, list); codeOfErr(err) != CodeFailed {
		t.Fatalf("a list naming a thread of its own: %v", err)
	}
	if l := lb.all(); len(l) != 1 || !strings.Contains(l[0], "the service's result does not encode") {
		t.Fatalf("log %q", l)
	}
}
