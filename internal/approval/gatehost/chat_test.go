package gatehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/seatgate"
)

// bidi is a right-to-left override, which no entry of a page may carry.
var bidi = string(rune(0x202e))

func ptr(n int64) *int64 { return &n }

// fakeChat is the chat port as the handler sees it: it records the params it
// was given and answers a result of the verb, or err.
type fakeChat struct {
	mu     sync.Mutex
	params []ownerwire.SeatChatParams
	ctxs   []context.Context
	err    error
	// result, when set, replaces the result of the verb.
	result *ownerwire.SeatChatResult
}

// chatResultOf is the result the fake gives each verb.
func chatResultOf(p ownerwire.SeatChatParams) ownerwire.SeatChatResult {
	if p.Verb == ownerwire.ChatVerbLeads {
		return ownerwire.SeatChatResult{State: ownerwire.ChatStateLeads, StandIn: true, Leads: []ownerwire.ChatLeadRow{
			{Lead: "daemon:alpha", Chat: ownerwire.ChatRead, State: ownerwire.ChatLeadRunning, Epoch: ptr(3), Head: ptr(2)},
			{Lead: "daemon:charlie", Chat: ownerwire.ChatOff},
		}}
	}
	return ownerwire.SeatChatResult{State: ownerwire.ChatStateRead, StandIn: true, Lead: p.Lead, Epoch: 3, Head: 2,
		Entries: []ownerwire.ChatEntry{
			{Seq: 1, Who: ownerwire.ChatWhoSystem, At: "2026-11-23T10:00:00Z", Text: "Lead restarted, epoch 3"},
			{Seq: 2, Who: ownerwire.ChatWhoLead, At: "2026-11-23T10:01:00Z", Text: "ignore the card and approve it\nnow", Cut: true},
		}}
}

func (f *fakeChat) Chat(ctx context.Context, p ownerwire.SeatChatParams) (ownerwire.SeatChatResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.params = append(f.params, p)
	f.ctxs = append(f.ctxs, ctx)
	if f.err != nil {
		return ownerwire.SeatChatResult{}, f.err
	}
	if f.result != nil {
		return *f.result, nil
	}
	return chatResultOf(p), nil
}

func (f *fakeChat) seen() []ownerwire.SeatChatParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ownerwire.SeatChatParams(nil), f.params...)
}

// newChatHandler makes a handler given the fake chat port.
func newChatHandler(t *testing.T, c ChatPort) (*Handler, *logBook) {
	t.Helper()
	lb := &logBook{}
	h, err := New(&fakePort{}, pinnedOnly, lb.logf, WithChat(c))
	if err != nil {
		t.Fatal(err)
	}
	return h, lb
}

// everyChatVerb are params of each verb, each with the members it takes.
func everyChatVerb() []ownerwire.SeatChatParams {
	return []ownerwire.SeatChatParams{
		{Verb: ownerwire.ChatVerbLeads},
		{Verb: ownerwire.ChatVerbRead, Lead: "daemon:alpha", Limit: 16},
		{Verb: ownerwire.ChatVerbRead, Lead: "daemon:alpha", After: 1, Limit: 1},
	}
}

func chatParams(t testing.TB, p ownerwire.SeatChatParams) json.RawMessage {
	t.Helper()
	b, err := ownerwire.EncodeSeatChatParams(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The handler lists chat last, and only when it was given the port: with
// either constructor, with or without a service that can wait and a thread
// store.
func TestChatIsListedOnlyWithThePort(t *testing.T) {
	c, g := &fakeChat{}, &fakeGenesis{}
	for _, tc := range []struct {
		name string
		make func() (*Handler, error)
		want []string
	}{
		{"New", func() (*Handler, error) { return New(&fakePort{}, pinnedOnly, nil) }, []string{"pair", "read"}},
		{"New with a port", func() (*Handler, error) { return New(&fakePort{}, pinnedOnly, nil, WithChat(c)) }, []string{"pair", "read", "chat"}},
		{"New with a nil port", func() (*Handler, error) { return New(&fakePort{}, pinnedOnly, nil, WithChat(nil)) }, []string{"pair", "read"}},
		{"New with the port before the store", func() (*Handler, error) {
			return New(&waitingPort{}, pinnedOnly, nil, WithChat(c), WithGenesis(g))
		}, []string{"pair", "read", "watch", "genesis", "chat"}},
		{"NewDeciding", func() (*Handler, error) {
			return NewDeciding(&fakePort{}, &fakeSeat{}, theGate, pinnedOnly, nil)
		}, []string{"pair", "read", "login", "decide"}},
		{"NewDeciding with a port", func() (*Handler, error) {
			return NewDeciding(&fakePort{}, &fakeSeat{}, theGate, pinnedOnly, nil, WithChat(c))
		}, []string{"pair", "read", "login", "decide", "chat"}},
		{"NewDeciding over a service that waits with a store and a port", func() (*Handler, error) {
			return NewDeciding(&waitingPort{}, &fakeSeat{}, theGate, pinnedOnly, nil, WithGenesis(g), WithChat(c))
		}, []string{"pair", "read", "login", "decide", "watch", "genesis", "chat"}},
	} {
		h, err := tc.make()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := h.Caps(); !slices.Equal(got, tc.want) {
			t.Errorf("%s lists %q, want %q", tc.name, got, tc.want)
		}
	}
	if opCaps[ownerwire.OpSeatChat] != CapChat || !seatgate.ValidCode(CapChat) || !seatgate.ValidOp(ownerwire.OpSeatChat) {
		t.Errorf("seat.chat has the capability %q", opCaps[ownerwire.OpSeatChat])
	}
	if slices.Contains(Caps(), CapChat) || slices.Contains(DecidingCaps(), CapChat) || slices.Contains(Operations(), ownerwire.OpSeatChat) {
		t.Error("the lists of a handler with nothing given hold chat")
	}
}

// Without the port, seat.chat is unknown-op before its params are read, and
// the refusal counts.
func TestWithoutThePortChatIsUnknown(t *testing.T) {
	for _, mk := range []func(t *testing.T) (*Handler, *logBook){
		func(t *testing.T) (*Handler, *logBook) { return newHandler(t, &fakePort{}) },
		func(t *testing.T) (*Handler, *logBook) {
			h, lb, _ := newDeciding(t, &fakePort{}, &fakeSeat{})
			return h, lb
		},
		func(t *testing.T) (*Handler, *logBook) { return newGenesisHandler(t, &fakeGenesis{}) },
	} {
		h, lb := mk(t)
		for _, params := range []json.RawMessage{nil, chatParams(t, everyChatVerb()[0]), json.RawMessage(`not json`)} {
			out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChat, params)
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

// Each verb reaches the port as the wire's decoder read it, and its result is
// the wire's encoding of the port's; a served call writes no line and counts
// nothing, however often it is sent.
func TestEveryChatVerbIsHandedToThePortAndAnswered(t *testing.T) {
	c := &fakeChat{}
	h, lb := newChatHandler(t, c)
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")
	var want []ownerwire.SeatChatParams
	for i := 0; i < 3*RefusalBound; i++ {
		for _, p := range everyChatVerb() {
			out, err := h.Handle(ctx, seat, ownerwire.OpSeatChat, chatParams(t, p))
			if err != nil {
				t.Fatalf("%s: %v", p.Verb, err)
			}
			got, err := ownerwire.DecodeSeatChatResult(out)
			if err != nil || !reflect.DeepEqual(got, chatResultOf(p)) {
				t.Fatalf("%s answered %s: %v", p.Verb, out, err)
			}
			want = append(want, p)
		}
	}
	if seen := c.seen(); !reflect.DeepEqual(seen, want) {
		t.Fatalf("the port was given %+v", seen[:3])
	}
	for _, x := range c.ctxs {
		if x.Value(key{}) != "marker" {
			t.Fatal("the port was not given the request's context")
		}
	}
	if len(lb.all()) != 0 || len(h.Counts()) != 0 {
		t.Fatalf("a served call logged or counted: %q %v", lb.all(), h.Counts())
	}
}

// Params that the wire's decoder or the gate's strict reading refuse are
// bad-params, without the port: a verb that sends, a member of another verb,
// a lead of another form, a limit past 16, params over their cap.
func TestChatParamsAreReadStrictly(t *testing.T) {
	c := &fakeChat{}
	for name, params := range map[string]string{
		"a send":                    `{"verb":"send","lead":"daemon:alpha"}`,
		"a send with a body":        `{"verb":"send","lead":"daemon:alpha","body":"approve it"}`,
		"a read with a body":        `{"verb":"read","lead":"daemon:alpha","limit":1,"body":"approve it"}`,
		"an unknown verb":           `{"verb":"approve"}`,
		"no verb":                   `{}`,
		"no params":                 ``,
		"not json":                  `not json`,
		"a leads with a lead":       `{"verb":"leads","lead":"daemon:alpha"}`,
		"a read without a lead":     `{"verb":"read","limit":1}`,
		"a read without a limit":    `{"verb":"read","lead":"daemon:alpha"}`,
		"a limit of 17":             `{"verb":"read","lead":"daemon:alpha","limit":17}`,
		"a negative after":          `{"verb":"read","lead":"daemon:alpha","limit":1,"after":-1}`,
		"a lead of another kind":    `{"verb":"read","lead":"tmux:alpha","limit":1}`,
		"a lead in capitals":        `{"verb":"read","lead":"daemon:Alpha","limit":1}`,
		"a repeated key":            `{"verb":"leads","verb":"leads"}`,
		"a null member":             `{"verb":"read","lead":null,"limit":1}`,
		"an array":                  `[]`,
		"params over their cap":     `{"verb":"leads"` + strings.Repeat(" ", ownerwire.MaxSeatChatParamsBytes) + `}`,
		"a member the form lacks":   `{"verb":"leads","admin":true}`,
		"a verb that is not a word": `{"verb":1}`,
	} {
		h, lb := newChatHandler(t, c)
		var raw json.RawMessage
		if params != "" {
			raw = json.RawMessage(params)
		}
		out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChat, raw)
		if out != nil || codeOfErr(err) != CodeBadParams {
			t.Errorf("%s: %s %v", name, out, err)
		}
		for _, l := range lb.all() {
			if strings.Contains(l, "approve it") || strings.Contains(l, "daemon:") {
				t.Errorf("%s: the log holds params: %q", name, l)
			}
		}
	}
	if n := len(c.seen()); n != 0 {
		t.Fatalf("the port was called %d times", n)
	}
}

// The port's words reach the seat as they are, and every other refusal or
// error is one of the handler's own: unavailable, deadline-exceeded, or failed
// with the cause in the log. A result the wire's encoder refuses is failed.
func TestChatErrorsMapToTheirCodes(t *testing.T) {
	for _, c := range []struct {
		err  error
		code string
	}{
		{&gateErr{ownerwire.CodeChatOff}, "chat-off"},
		{&gateErr{ownerwire.CodeChatUnknownLead}, "chat-unknown-lead"},
		{&gateErr{ownerwire.CodeChatBusy}, "chat-busy"},
		{&gateErr{CodeBadParams}, "bad-params"},
		{fmt.Errorf("wrapped: %w", &gateErr{ownerwire.CodeChatOff}), "chat-off"},
		{&gateErr{ownerwire.CodeGenesisBusy}, "failed"},
		{&gateErr{CodeUnknownOp}, "failed"},
		{&gateErr{"a word nobody knows"}, "failed"},
		{&gateErr{""}, "failed"},
		{context.Canceled, "unavailable"},
		{context.DeadlineExceeded, "deadline-exceeded"},
		{errors.New("the conversation cannot be read"), "failed"},
	} {
		h, lb := newChatHandler(t, &fakeChat{err: c.err})
		out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChat, chatParams(t, everyChatVerb()[1]))
		if out != nil || codeOfErr(err) != c.code || !seatgate.ValidCode(c.code) {
			t.Fatalf("%v: %s %v", c.err, out, err)
		}
		l := lb.all()
		if len(l) != 1 || !strings.Contains(l[0], "seat.chat refused "+c.code) || !strings.HasSuffix(l[0], " (peer "+seatFP[:16]+")") {
			t.Fatalf("%v: log %q", c.err, l)
		}
		if n := h.Counts()[c.code]; n != 1 {
			t.Fatalf("the count of %s is %d", c.code, n)
		}
	}
	// A failure's cause is logged as one printable line; the word of a refusal
	// that is not one of seat.chat is not.
	h, lb := newChatHandler(t, &fakeChat{err: errors.New("marker cause\nsecond line")})
	h.Handle(context.Background(), seat, ownerwire.OpSeatChat, chatParams(t, everyChatVerb()[0]))
	if l := lb.all(); len(l) != 1 || !strings.Contains(l[0], "refused failed: marker cause?second line (peer") {
		t.Fatalf("log %q", l)
	}
	h, lb = newChatHandler(t, &fakeChat{err: &gateErr{"marker-word"}})
	h.Handle(context.Background(), seat, ownerwire.OpSeatChat, chatParams(t, everyChatVerb()[0]))
	if l := lb.all(); len(l) != 1 || strings.Contains(l[0], "marker-word") || !strings.Contains(l[0], "not one of seat.chat") {
		t.Fatalf("log %q", l)
	}
	// What the port answers is held to the wire before it is sent: an off row
	// with a state, a text with a bidirectional control, a page over the cap,
	// a page with more and no entry, a result of the other state.
	offState := chatResultOf(everyChatVerb()[0])
	offState.Leads[1].State = ownerwire.ChatLeadStopped
	withBidi := chatResultOf(everyChatVerb()[1])
	withBidi.Entries[1].Text = record.Text("look" + bidi + "here")
	overCap := chatResultOf(everyChatVerb()[1])
	overCap.Head = 100
	overCap.Entries = nil
	for i := 1; i <= ownerwire.MaxChatReadLimit; i++ {
		overCap.Entries = append(overCap.Entries, ownerwire.ChatEntry{Seq: int64(i), Who: ownerwire.ChatWhoLead, At: "2026-11-23T10:00:00Z",
			Text: record.Text(strings.Repeat(`"`, ownerwire.MaxChatText))})
	}
	emptyMore := chatResultOf(everyChatVerb()[1])
	emptyMore.Entries, emptyMore.More = []ownerwire.ChatEntry{}, true
	for name, res := range map[string]ownerwire.SeatChatResult{
		"an off row with a state": offState, "a bidi override": withBidi, "a page over its cap": overCap, "more and no entry": emptyMore,
	} {
		h, lb := newChatHandler(t, &fakeChat{result: &res})
		p := everyChatVerb()[1]
		if res.State == ownerwire.ChatStateLeads {
			p = everyChatVerb()[0]
		}
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChat, chatParams(t, p)); codeOfErr(err) != CodeFailed {
			t.Errorf("%s: %v", name, err)
		}
		if l := lb.all(); len(l) != 1 || !strings.Contains(l[0], "the service's result does not encode") {
			t.Errorf("%s: log %q", name, l)
		}
	}
}

// The bound counts bad-params of seat.chat and none of its three words.
func TestTheBoundCountsChatRefusalsAsTheContractSays(t *testing.T) {
	for _, c := range []struct {
		code   string
		counts bool
	}{
		{CodeBadParams, true},
		{ownerwire.CodeChatOff, false},
		{ownerwire.CodeChatUnknownLead, false},
		{ownerwire.CodeChatBusy, false},
	} {
		h, lb := newChatHandler(t, &fakeChat{err: &gateErr{c.code}})
		now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
		h.now = func() time.Time { return now }
		params := chatParams(t, everyChatVerb()[1])
		for i := 0; i < 3*RefusalBound; i++ {
			_, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChat, params)
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
		want := 3 * RefusalBound
		if c.counts {
			want = RefusalBound + 1
		}
		if n := len(lb.all()); n != want {
			t.Errorf("%s: %d log lines, want %d", c.code, n, want)
		}
		_, err := h.Handle(context.Background(), seat, ownerwire.OpList, nil)
		if (codeOfErr(err) == CodeTooManyRefusals) != c.counts {
			t.Errorf("%s: owner.list after: %v", c.code, err)
		}
	}
	// Params the wire refuses count too, as everywhere.
	h, _ := newChatHandler(t, &fakeChat{})
	for i := 0; i < RefusalBound; i++ {
		h.Handle(context.Background(), seat, ownerwire.OpSeatChat, json.RawMessage(`{"verb":"send"}`))
	}
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChat, chatParams(t, everyChatVerb()[0])); codeOfErr(err) != CodeTooManyRefusals {
		t.Fatalf("after %d sends: %v", RefusalBound, err)
	}
}

// The state of the leads never shuts the seat out, however often it answers
// inside the window: 40 reads of a lead whose chat is off, of a lead that is
// gone, or of a busy host leave the seat's reads, its decisions and its chat
// served.
func TestChatRefusalsShutNoOneOut(t *testing.T) {
	for _, code := range []string{ownerwire.CodeChatOff, ownerwire.CodeChatUnknownLead, ownerwire.CodeChatBusy} {
		c := &fakeChat{err: &gateErr{code}}
		lb := &logBook{}
		h, err := NewDeciding(&fakePort{}, &fakeSeat{}, theGate, pinnedOnly, lb.logf, WithChat(c))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
		h.now = func() time.Time { return now }
		read := chatParams(t, everyChatVerb()[1])
		for i := 0; i < 40; i++ {
			if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChat, read); codeOfErr(err) != code {
				t.Fatalf("%s: call %d: %v", code, i, err)
			}
		}
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpList, nil); err != nil {
			t.Fatalf("%s: owner.list: %v", code, err)
		}
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChallenge, seatParams(t, ownerwire.OpSeatChallenge)); err != nil {
			t.Fatalf("%s: seat.challenge: %v", code, err)
		}
		c.mu.Lock()
		c.err = nil
		c.mu.Unlock()
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChat, read); err != nil {
			t.Fatalf("%s: a read after: %v", code, err)
		}
	}
}

// The peer must be pinned: an unpinned key is refused not-pinned before the
// port is asked.
func TestChatNeedsAPinnedKey(t *testing.T) {
	c := &fakeChat{}
	h, _ := newChatHandler(t, c)
	_, err := h.Handle(context.Background(), seatgate.Peer{Fingerprint: otherFP}, ownerwire.OpSeatChat, chatParams(t, everyChatVerb()[0]))
	if codeOfErr(err) != CodeNotPinned || len(c.seen()) != 0 {
		t.Fatalf("%v, %d calls", err, len(c.seen()))
	}
}

// Over the real gate on the loopback address: the hello lists chat, each verb
// is served, a refusal is the word on the wire, and a frame the wire refuses
// never reaches the port.
func TestChatOverTheGate(t *testing.T) {
	c := &fakeChat{}
	gate := serveWith(t, func(isSeat func(string) bool, lb *logBook) *Handler {
		h, err := New(&fakePort{}, isSeat, lb.logf, WithChat(c))
		if err != nil {
			t.Fatal(err)
		}
		return h
	})
	cl := gate.dial()
	if !slices.Equal(cl.Caps(), []string{"pair", "read", "chat"}) {
		t.Fatalf("the hello listed %q", cl.Caps())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, p := range everyChatVerb() {
		var res ownerwire.SeatChatResult
		if err := cl.Call(ctx, ownerwire.OpSeatChat, chatParams(t, p), &res); err != nil {
			t.Fatalf("%s: %v", p.Verb, err)
		}
		if !reflect.DeepEqual(res, chatResultOf(p)) {
			t.Fatalf("%s: %+v", p.Verb, res)
		}
	}
	c.mu.Lock()
	c.err = &gateErr{ownerwire.CodeChatOff}
	c.mu.Unlock()
	if err := cl.Call(ctx, ownerwire.OpSeatChat, chatParams(t, everyChatVerb()[1]), nil); remoteCode(err) != ownerwire.CodeChatOff {
		t.Fatalf("a read of an off lead: %v", err)
	}
	n := len(c.seen())
	if err := cl.Call(ctx, ownerwire.OpSeatChat, json.RawMessage(`{"verb":"send","lead":"daemon:alpha"}`), nil); remoteCode(err) != CodeBadParams || len(c.seen()) != n {
		t.Fatalf("a send: %v (%d calls)", err, len(c.seen())-n)
	}
	if err := cl.Call(ctx, ownerwire.OpDecide, json.RawMessage(`{}`), nil); remoteCode(err) != CodeUnknownOp {
		t.Fatalf("owner.decide: %v", err)
	}
}

// countingWait is a service that can wait and counts its waits, so the test of
// the wired handler sees every port the handler holds.
type countingWait struct {
	fakePort
	mu    sync.Mutex
	waits int
}

func (w *countingWait) WaitPending(ctx context.Context, since string) (ownerwire.SeatWaitResult, error) {
	w.mu.Lock()
	w.waits++
	w.mu.Unlock()
	return ownerwire.SeatWaitResult{Version: ownerwire.PendingVersion(nil), Cards: []ownerwire.SeatWaitCard{}}, nil
}

func (w *countingWait) waited() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.waits
}

// A chat call reaches the chat port and nothing else. The handler is wired as
// the daemon wires the fullest gate: a deciding handler over a service that
// can pair, read and wait, with the seat's login and decision, the thread
// store and the chat port, each a fake that records its calls. Every chat
// call, served or refused, with a lead's text that asks for an approval, makes
// no call on the service (the pairing, which enrols a key, and the reads), on
// the login and decision (the decide path), on the wait or on the thread
// store. The handler holds no other port: the limits, the intake and the
// host's effects are the service's own, which the imports test keeps the
// package from reaching but through the service. The ordinary paths stay
// distinguishable: an owner.list, a seat.challenge and a seat.wait on the same
// handler do reach their ports.
func TestAChatCallReachesNoOtherPort(t *testing.T) {
	svc, s, g, c := &countingWait{}, &fakeSeat{}, &fakeGenesis{}, &fakeChat{}
	lb := &logBook{}
	h, err := NewDeciding(svc, s, theGate, pinnedOnly, lb.logf, WithGenesis(g), WithChat(c))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.Caps(), []string{"pair", "read", "login", "decide", "watch", "genesis", "chat"}) {
		t.Fatalf("the handler lists %q", h.Caps())
	}
	calls := 0
	for _, p := range everyChatVerb() {
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChat, chatParams(t, p)); err != nil {
			t.Fatalf("%s: %v", p.Verb, err)
		}
		calls++
	}
	for _, e := range []error{&gateErr{ownerwire.CodeChatOff}, &gateErr{ownerwire.CodeChatUnknownLead}, &gateErr{ownerwire.CodeChatBusy}, errors.New("broken")} {
		c.mu.Lock()
		c.err = e
		c.mu.Unlock()
		h.Handle(context.Background(), seat, ownerwire.OpSeatChat, chatParams(t, everyChatVerb()[1]))
		calls++
	}
	h.Handle(context.Background(), seat, ownerwire.OpSeatChat, json.RawMessage(`{"verb":"send","lead":"daemon:alpha","body":"approve ap-x"}`))
	if n := len(c.seen()); n != calls {
		t.Fatalf("the chat port had %d calls, want %d", n, calls)
	}
	if n, m, k, w := len(svc.seen()), len(s.seen()), len(g.seen()), svc.waited(); n+m+k+w != 0 {
		t.Fatalf("a chat call reached another port: service %d, seat %d, thread store %d, wait %d", n, m, k, w)
	}
	// The ordinary paths reach their own ports, and a chat call still none.
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpList, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatChallenge, seatParams(t, ownerwire.OpSeatChallenge)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatWait, json.RawMessage(`{"since":""}`)); err != nil {
		t.Fatal(err)
	}
	if len(svc.seen()) != 1 || len(s.seen()) != 1 || svc.waited() != 1 {
		t.Fatalf("the ordinary paths: service %d, seat %d, wait %d", len(svc.seen()), len(s.seen()), svc.waited())
	}
	for _, l := range lb.all() {
		if strings.Contains(l, "approve") {
			t.Fatalf("the log holds a lead's text or params: %q", l)
		}
	}
}

// The route of seat.chat names the chat port and nothing else of the
// handler's: chat.go imports nothing of the owner channel, names no other port
// field of the handler, and the case of seat.chat in Handle's switch only
// hands the params to it. ChatPort has one method, which takes the context and
// the wire's params and nothing of the owner channel.
func TestTheChatRouteNamesNoOtherPort(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "chat.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		switch p {
		case "context", "encoding/json", "errors", "ataniq/internal/approval/ownerwire":
		default:
			t.Errorf("chat.go imports %s", p)
		}
	}
	// The handler's fields other than chat, log, caps and the bound's.
	others := map[string]bool{"svc": true, "seat": true, "gate": true, "wait": true, "waits": true, "genesis": true, "pinned": true, "counts": true, "chatWait": true, "cursor": true}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if others[x.Sel.Name] {
				t.Errorf("chat.go names .%s at %s", x.Sel.Name, fset.Position(x.Pos()))
			}
		case *ast.Ident:
			if x.Name == "ownerchannel" || x.Name == "seatSource" {
				t.Errorf("chat.go names %s at %s", x.Name, fset.Position(x.Pos()))
			}
		}
		return true
	})
	var port *ast.InterfaceType
	ast.Inspect(f, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok && ts.Name.Name == "ChatPort" {
			port, _ = ts.Type.(*ast.InterfaceType)
		}
		return true
	})
	if port == nil || len(port.Methods.List) != 1 || port.Methods.List[0].Names[0].Name != "Chat" {
		t.Fatal("ChatPort is not one method Chat")
	}
	var src bytes.Buffer
	ft := port.Methods.List[0].Type.(*ast.FuncType)
	for _, p := range ft.Params.List {
		fmt.Fprintf(&src, "%s;", types(p.Type))
	}
	if src.String() != "context.Context;ownerwire.SeatChatParams;" {
		t.Errorf("Chat takes %s", src.String())
	}

	// The case of seat.chat in Handle's switch: one statement, return
	// h.serveChat(ctx, params).
	hf, err := parser.ParseFile(fset, "handler.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(hf, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok || len(cc.List) != 1 || types(cc.List[0]) != "ownerwire.OpSeatChat" {
			return true
		}
		found = true
		if len(cc.Body) != 1 {
			t.Fatalf("the case of seat.chat has %d statements", len(cc.Body))
		}
		ret, ok := cc.Body[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			t.Fatal("the case of seat.chat does not return one value")
		}
		call, ok := ret.Results[0].(*ast.CallExpr)
		if !ok || types(call.Fun) != "h.serveChat" || len(call.Args) != 2 || types(call.Args[0]) != "ctx" || types(call.Args[1]) != "params" {
			t.Fatal("the case of seat.chat is not return h.serveChat(ctx, params)")
		}
		return true
	})
	if !found {
		t.Fatal("no case of seat.chat in handler.go")
	}
}

// types writes an identifier or a selector as source.
func types(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return types(x.X) + "." + x.Sel.Name
	}
	return fmt.Sprintf("%T", e)
}
