//go:build linux && ataniq_canary

package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/wire"
)

const (
	canaryID   = "msg-1"
	canaryMark = "sample-body-4d2b8a60"
	canaryText = "[ataniq-msg id=msg-1 role=probe] " + canaryMark
)

// fakeDeliverer is a Deliverer that records its calls and does what fn says.
type fakeDeliverer struct {
	fn func(ctx context.Context, id, text string) wire.InjectResult

	mu      sync.Mutex
	ids     []string
	texts   []string
	ctxs    []context.Context
	entered chan struct{} // one send per call, when set
	active  atomic.Int32
	most    atomic.Int32
}

func (f *fakeDeliverer) Deliver(ctx context.Context, id, text string) wire.InjectResult {
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		m := f.most.Load()
		if n <= m || f.most.CompareAndSwap(m, n) {
			break
		}
	}
	f.mu.Lock()
	f.ids = append(f.ids, id)
	f.texts = append(f.texts, text)
	f.ctxs = append(f.ctxs, ctx)
	entered := f.entered
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if f.fn == nil {
		return submitted()
	}
	return f.fn(ctx, id, text)
}

func (f *fakeDeliverer) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ids)
}

// first is what the first call got.
func (f *fakeDeliverer) first() (id, text string, ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ids[0], f.texts[0], f.ctxs[0]
}

func submitted() wire.InjectResult {
	return wire.InjectResult{
		Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole, Detail: "sample detail",
	}
}

// canaryEnv is a holder whose control server was given d.
func canaryEnv(t *testing.T, d Deliverer, mods ...func(*envOpts)) *env {
	t.Helper()
	mods = append(mods, func(o *envOpts) {
		prev := o.cfg
		o.cfg = func(c *Config) {
			if prev != nil {
				prev(c)
			}
			c.Deliverer = d
		}
	})
	e := newEnv(t, "raw; say READY; wait", mods...)
	waitScreen(t, e.sess, "READY")
	return e
}

func injectReq(cmd string) wire.Request {
	return wire.Request{Op: wire.OpInject, Epoch: testEpoch, CommandID: cmd, MessageID: canaryID, Text: canaryText}
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// injectEvents waits for n inject events and returns every one there is.
func (e *env) injectEvents(n int) []eventlog.Event {
	e.t.Helper()
	var out []eventlog.Event
	poll(e.t, func() string { return "inject events" }, func() bool {
		out = out[:0]
		for _, ev := range e.eventsNow() {
			if ev.Event == "inject" {
				out = append(out, ev)
			}
		}
		return len(out) >= n
	})
	return out
}

// noInjectEvents checks that the log has none, after the command event of cmd
// (events are written in order, so everything the request caused is before it).
func (e *env) noInjectEvents(cmd string) {
	e.t.Helper()
	e.waitEvent("the command "+cmd, func(ev eventlog.Event) bool { return ev.Event == "state" && ev.CommandID == cmd })
	for _, ev := range e.eventsNow() {
		if ev.Event == "inject" {
			e.t.Errorf("an inject event: %+v", ev)
		}
	}
}

// noTextInLog checks the raw log for the text, the mark of it and its id.
func (e *env) noTextInLog() {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.logDir, eventlog.FileName))
	if err != nil {
		e.t.Fatal(err)
	}
	for _, needle := range []string{canaryText, canaryMark, "ataniq-msg"} {
		if strings.Contains(string(raw), needle) {
			e.t.Errorf("%q is in the event log", needle)
		}
	}
}

func TestInjectTimeoutIsFifteenSeconds(t *testing.T) {
	if InjectTimeout != 15*time.Second {
		t.Errorf("InjectTimeout is %v", InjectTimeout)
	}
}

// A tagged build whose Config has no deliverer keeps the gate closed: the
// answer and the event are the release build's, and nothing is written.
func TestInjectWithoutDeliverer(t *testing.T) {
	e := canaryEnv(t, nil)
	r := e.mustControl(injectReq("inj-none"))
	if r.OK || r.Code != wire.CodeDisabled || r.Inject != nil {
		t.Fatalf("answer %+v", r)
	}
	evs := e.injectEvents(1)
	if len(evs) != 1 || evs[0].Reason != "disabled" || evs[0].Outcome != "refused" || evs[0].Digest != "" || evs[0].MessageID != "" {
		t.Errorf("events %+v", evs)
	}
	e.noTextInLog()
}

// The result of the deliverer is the answer, and the two events carry the
// identity and the digest, never the text.
func TestInjectDelivers(t *testing.T) {
	for _, res := range []wire.InjectResult{
		submitted(),
		{Outcome: wire.InjectOutcomeNotNow, Reason: wire.InjectReasonNotReady, Written: wire.InjectWrittenNothing},
		{Outcome: wire.InjectOutcomeNotNow, Reason: wire.InjectReasonBlocked, Written: wire.InjectWrittenNothing, Detail: "x"},
		{Outcome: wire.InjectOutcomeRefused, Reason: wire.InjectReasonUntested, Written: wire.InjectWrittenNothing},
		{Outcome: wire.InjectOutcomeRefused, Reason: wire.InjectReasonText, Written: wire.InjectWrittenNothing},
		{Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonNotSubmitted, Written: wire.InjectWrittenPartial},
		{Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonDied, Written: wire.InjectWrittenWhole},
		{Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonDied, Written: wire.InjectWrittenUnknown},
	} {
		t.Run(string(res.Outcome)+"-"+string(res.Reason)+"-"+string(res.Written), func(t *testing.T) {
			d := &fakeDeliverer{fn: func(context.Context, string, string) wire.InjectResult { return res }}
			e := canaryEnv(t, d)
			r := e.mustControl(injectReq("inj-1"))
			if !r.OK || r.Code != "" || r.Status != nil || r.Inject == nil || *r.Inject != res {
				t.Fatalf("answer %+v, want ok with %+v", r, res)
			}
			if id, text, _ := d.first(); d.calls() != 1 || id != canaryID || text != canaryText {
				t.Errorf("the deliverer got %q %q", id, text)
			}
			evs := e.injectEvents(2)
			if len(evs) != 2 {
				t.Fatalf("events %+v", evs)
			}
			for i, ev := range evs {
				if ev.MessageID != canaryID || ev.CommandID != "inj-1" || ev.Digest != digestOf(canaryText) || ev.Reason == "disabled" && i == 0 {
					t.Errorf("event %d: %+v", i, ev)
				}
			}
			if evs[0].Outcome != "started" || evs[0].Reason != "" || evs[0].State != "" {
				t.Errorf("before: %+v", evs[0])
			}
			if evs[1].Outcome != string(res.Outcome) || evs[1].Reason != string(res.Reason) || evs[1].State != "written-"+string(res.Written) {
				t.Errorf("after: %+v", evs[1])
			}
			e.waitEvent("the command", func(ev eventlog.Event) bool {
				return ev.Event == "state" && ev.Reason == "inject" && ev.CommandID == "inj-1" && ev.Outcome == "ok"
			})
			e.noTextInLog()
			// The session's disabled stub was not called.
			for _, ev := range e.eventsNow() {
				if ev.Event == "inject" && ev.Reason == "disabled" {
					t.Errorf("the disabled stub logged %+v", ev)
				}
			}
		})
	}
}

// A request for another epoch is refused before anything: no call, no event.
func TestInjectWrongEpoch(t *testing.T) {
	d := &fakeDeliverer{}
	e := canaryEnv(t, d)
	req := injectReq("inj-old")
	req.Epoch = testEpoch + 1
	r := e.mustControl(req)
	if r.OK || r.Code != wire.CodeWrongEpoch || r.Inject != nil {
		t.Fatalf("answer %+v", r)
	}
	e.waitEvent("the refusal", func(ev eventlog.Event) bool {
		return ev.Event == "refusal" && ev.Reason == "wrong-epoch" && ev.CommandID == "inj-old"
	})
	if d.calls() != 0 {
		t.Error("the deliverer was called")
	}
	for _, ev := range e.eventsNow() {
		if ev.Event == "inject" {
			t.Errorf("an inject event: %+v", ev)
		}
	}
}

// An inject with no text or no message id is bad-request, before anything. The
// wire carries the form with neither only; the handler checks for itself, so a
// request that the wire would not decode is refused as well.
func TestInjectBadRequest(t *testing.T) {
	d := &fakeDeliverer{}
	e := canaryEnv(t, d)
	r := e.mustControl(wire.Request{Op: wire.OpInject, Epoch: testEpoch, CommandID: "inj-bare"})
	if r.OK || r.Code != wire.CodeBadRequest || r.Inject != nil {
		t.Fatalf("the old form: %+v", r)
	}
	e.noInjectEvents("inj-bare")
	for name, req := range map[string]wire.Request{
		"no text":       {Op: wire.OpInject, Epoch: testEpoch, MessageID: canaryID},
		"no id":         {Op: wire.OpInject, Epoch: testEpoch, Text: canaryText},
		"a bad id":      {Op: wire.OpInject, Epoch: testEpoch, MessageID: "Bad Id", Text: canaryText},
		"a bad text":    {Op: wire.OpInject, Epoch: testEpoch, MessageID: canaryID, Text: "a\x00b"},
		"a too long id": {Op: wire.OpInject, Epoch: testEpoch, MessageID: strings.Repeat("a", wire.MaxMessageID+1), Text: canaryText},
	} {
		if r := e.srv.dispatch(req); r.OK || r.Code != wire.CodeBadRequest || r.Inject != nil {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if d.calls() != 0 {
		t.Error("the deliverer was called")
	}
	for _, ev := range e.eventsNow() {
		if ev.Event == "inject" {
			t.Errorf("an inject event: %+v", ev)
		}
	}
}

// One delivery at a time: a request while one runs is busy, and refused before
// anything; the first is answered when its deliverer returns, and then another
// delivery can run.
func TestInjectBusy(t *testing.T) {
	release := make(chan struct{})
	d := &fakeDeliverer{entered: make(chan struct{}, 4), fn: func(context.Context, string, string) wire.InjectResult {
		<-release
		return submitted()
	}}
	e := canaryEnv(t, d)
	first := make(chan wire.Response, 1)
	go func() { first <- e.mustControl(injectReq("inj-a")) }()
	<-d.entered

	r := e.mustControl(injectReq("inj-b"))
	if r.OK || r.Code != wire.CodeBusy || r.Inject != nil {
		t.Fatalf("the second request: %+v", r)
	}
	if d.calls() != 1 {
		t.Errorf("%d calls", d.calls())
	}
	close(release)
	select {
	case r := <-first:
		if !r.OK || r.Inject == nil || r.Inject.Outcome != wire.InjectOutcomeSubmitted {
			t.Errorf("the first request: %+v", r)
		}
	case <-time.After(patience):
		t.Fatal("no answer to the first request")
	}
	if r := e.mustControl(injectReq("inj-c")); !r.OK || r.Inject == nil {
		t.Errorf("the third request: %+v", r)
	}
	evs := e.injectEvents(4)
	for _, ev := range evs {
		if ev.CommandID == "inj-b" {
			t.Errorf("the refused request has an event: %+v", ev)
		}
	}
}

// Many requests at once: the deliverer never runs twice at a time, and every
// request is either delivered (ok with a result) or refused busy.
func TestInjectOneAtATime(t *testing.T) {
	d := &fakeDeliverer{fn: func(context.Context, string, string) wire.InjectResult {
		time.Sleep(60 * time.Millisecond)
		return submitted()
	}}
	e := canaryEnv(t, d)
	const n = 6
	var wg sync.WaitGroup
	var ok, busy atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.control(injectReq("inj-many"))
			switch {
			case err != nil:
				t.Errorf("control: %v", err)
			case r.OK && r.Inject != nil:
				ok.Add(1)
			case !r.OK && r.Code == wire.CodeBusy:
				busy.Add(1)
			default:
				t.Errorf("answer %+v", r)
			}
		}()
	}
	wg.Wait()
	if d.most.Load() != 1 {
		t.Errorf("%d deliveries ran at once", d.most.Load())
	}
	if ok.Load() < 1 || int(ok.Load()) != d.calls() || ok.Load()+busy.Load() != n {
		t.Errorf("ok %d, busy %d, calls %d", ok.Load(), busy.Load(), d.calls())
	}
}

// The deliverer's context is bounded by the delivery's time.
func TestInjectContextIsBounded(t *testing.T) {
	d := &fakeDeliverer{}
	e := canaryEnv(t, d)
	if r := e.mustControl(injectReq("inj-ctx")); !r.OK {
		t.Fatalf("%+v", r)
	}
	_, _, ctx := d.first()
	deadline, ok := ctx.Deadline()
	if left := time.Until(deadline); !ok || left > InjectTimeout || left < InjectTimeout-5*time.Second {
		t.Errorf("deadline %v, in %v", ok, left)
	}
}

// A deliverer that outlasts the time is answered uncertain, died, unknown. It
// may still write, so the gate stays shut until it has returned: a request in
// that time is busy, and a delivery can start after.
func TestInjectOutlastingDeliverer(t *testing.T) {
	release := make(chan struct{})
	var sawEnd atomic.Bool
	d := &fakeDeliverer{entered: make(chan struct{}, 4), fn: func(ctx context.Context, _, _ string) wire.InjectResult {
		<-ctx.Done()
		sawEnd.Store(true)
		<-release // ignores the end of its context
		return submitted()
	}}
	e := canaryEnv(t, d)
	e.srv.inj.timeout.Store(int64(200 * time.Millisecond))

	start := time.Now()
	r := e.mustControl(injectReq("inj-slow"))
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the answer took %v", took)
	}
	want := injectUnknown("the delivery did not finish in time")
	if !r.OK || r.Inject == nil || *r.Inject != want {
		t.Fatalf("answer %+v, want ok with %+v", r, want)
	}
	poll(t, func() string { return "the end of the deliverer's context" }, sawEnd.Load)
	evs := e.injectEvents(2)
	if evs[1].Outcome != "uncertain" || evs[1].Reason != "died" || evs[1].State != "written-unknown" {
		t.Errorf("after: %+v", evs[1])
	}
	// Still running: no second delivery.
	if r := e.mustControl(injectReq("inj-again")); r.OK || r.Code != wire.CodeBusy {
		t.Errorf("while the first runs: %+v", r)
	}
	if d.calls() != 1 {
		t.Errorf("%d calls", d.calls())
	}
	close(release)
	poll(t, func() string { return "the gate to open again" }, func() bool {
		r := e.mustControl(injectReq("inj-later"))
		return r.OK && r.Inject != nil
	})
	e.noTextInLog()
}

// A deliverer that panics is answered uncertain, died, unknown, the panic's
// value (which can hold the text) is dropped, and the holder carries on.
func TestInjectPanickingDeliverer(t *testing.T) {
	d := &fakeDeliverer{fn: func(_ context.Context, _, text string) wire.InjectResult { panic("sample panic: " + text) }}
	e := canaryEnv(t, d)
	r := e.mustControl(injectReq("inj-panic"))
	want := injectUnknown("the delivery stopped unexpectedly")
	if !r.OK || r.Inject == nil || *r.Inject != want {
		t.Fatalf("answer %+v, want ok with %+v", r, want)
	}
	if strings.Contains(r.Inject.Detail, canaryMark) {
		t.Error("the text is in the answer")
	}
	evs := e.injectEvents(2)
	if evs[1].Outcome != "uncertain" || evs[1].Reason != "died" || evs[1].State != "written-unknown" {
		t.Errorf("after: %+v", evs[1])
	}
	e.noTextInLog()
	if st := e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}); !st.OK {
		t.Errorf("status after: %+v", st)
	}
	// The call has ended, so the gate is open again.
	if r := e.mustControl(injectReq("inj-after")); !r.OK || r.Inject == nil {
		t.Errorf("after the panic: %+v", r)
	}
}

// A deliverer that ends its goroutine without returning (as a test's FailNow
// does) is answered like a panic, and the gate opens again.
func TestInjectExitingDeliverer(t *testing.T) {
	d := &fakeDeliverer{fn: func(context.Context, string, string) wire.InjectResult {
		runtime.Goexit()
		return submitted()
	}}
	e := canaryEnv(t, d)
	want := injectUnknown("the delivery stopped unexpectedly")
	if r := e.mustControl(injectReq("inj-exit")); !r.OK || r.Inject == nil || *r.Inject != want {
		t.Errorf("%+v", r)
	}
	if r := e.mustControl(injectReq("inj-exit-2")); !r.OK || r.Inject == nil || *r.Inject != want {
		t.Errorf("the second: %+v", r)
	}
}

// A result that the wire does not carry is answered as one that could not be
// encoded, and logged as that.
func TestInjectUnencodableResult(t *testing.T) {
	want := wire.InjectResult{
		Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonDied, Written: wire.InjectWrittenUnknown,
		Detail: "the result could not be encoded",
	}
	for name, res := range map[string]wire.InjectResult{
		"zero":             {},
		"delivered":        {Outcome: "delivered", Written: wire.InjectWrittenWhole},
		"a bad pair":       {Outcome: wire.InjectOutcomeSubmitted, Reason: wire.InjectReasonDied, Written: wire.InjectWrittenWhole},
		"an unknown word":  {Outcome: wire.InjectOutcomeRefused, Reason: "other", Written: wire.InjectWrittenNothing},
		"a bad written":    {Outcome: wire.InjectOutcomeSubmitted, Written: "all"},
		"a non-ASCII text": {Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole, Detail: "café"},
		"a control byte":   {Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole, Detail: "a\nb"},
		"a long detail":    {Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole, Detail: strings.Repeat("a", wire.MaxInjectDetail+1)},
	} {
		t.Run(name, func(t *testing.T) {
			d := &fakeDeliverer{fn: func(context.Context, string, string) wire.InjectResult { return res }}
			e := canaryEnv(t, d)
			r := e.mustControl(injectReq("inj-bad"))
			if !r.OK || r.Code != "" || r.Inject == nil || *r.Inject != want {
				t.Fatalf("answer %+v, want ok with %+v", r, want)
			}
			evs := e.injectEvents(2)
			if evs[1].Outcome != "uncertain" || evs[1].Reason != "died" || evs[1].State != "written-unknown" {
				t.Errorf("after: %+v", evs[1])
			}
		})
	}
}

// The results the handler makes up are valid ones.
func TestInjectMadeUpResultsAreValid(t *testing.T) {
	for _, r := range []wire.InjectResult{resultUnencodable, resultPanicked, resultTimedOut, resultClosed} {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
		if _, err := wire.EncodeResponse(wire.Response{OK: true, Inject: &r}); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
}

// The control connection lets a delivery that outlasts its handshake time
// answer: the request's read time is over, and the answer has a time of its own.
func TestInjectOutlastsTheHandshakeTime(t *testing.T) {
	d := &fakeDeliverer{fn: func(context.Context, string, string) wire.InjectResult {
		time.Sleep(900 * time.Millisecond)
		return submitted()
	}}
	e := canaryEnv(t, d, func(o *envOpts) {
		o.cfg = func(c *Config) { c.HandshakeTimeout = 300 * time.Millisecond }
	})
	r := e.mustControl(injectReq("inj-long"))
	if !r.OK || r.Inject == nil || r.Inject.Outcome != wire.InjectOutcomeSubmitted {
		t.Fatalf("answer %+v", r)
	}
	// The other operations keep their time.
	if st := e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}); !st.OK {
		t.Errorf("status: %+v", st)
	}
}

// An attach connection can never inject: the operation is on the control
// socket only. A request frame on the attach socket, before or after the Hello,
// and a Hello that carries the operation's fields all end the connection, and
// the deliverer is never called.
func TestAttachCannotInject(t *testing.T) {
	d := &fakeDeliverer{}
	e := canaryEnv(t, d)
	payload, err := wire.EncodeRequest(injectReq("inj-attach"))
	if err != nil {
		t.Fatal(err)
	}
	frame := rawFrame(wire.TypeRequest, uint32(len(payload)), payload)

	// Before the Hello.
	c := e.dialAttach()
	if _, err := c.Write(frame); err != nil {
		t.Fatal(err)
	}
	if b := readAll(c); len(b) != 0 {
		t.Errorf("%d bytes answered a request frame before the Hello", len(b))
	}
	e.waitEvent("the first frame refused", named("refusal", "handshake-bad-first-frame"))

	// After it, by a writer.
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	if _, err := w.conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	if err := w.waitEnd(); err == nil {
		t.Error("the writer's connection was not closed")
	}
	e.waitEvent("the frame refused", named("refusal", "unknown-frame-type"))

	// A Hello with the fields of the operation.
	hello, err := wire.EncodeHello(wire.Hello{
		Version: wire.ProtocolVersion, Ticket: e.newTicket(wire.RoleViewer), Name: "test-console", Columns: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(hello, &m); err != nil {
		t.Fatal(err)
	}
	m["op"], m["message_id"], m["text"] = "inject", canaryID, canaryText
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	c = e.dialAttach()
	if _, err := c.Write(rawFrame(wire.TypeHello, uint32(len(raw)), raw)); err != nil {
		t.Fatal(err)
	}
	if b := readAll(c); len(b) > 0 && b[0] == wire.TypeWelcome {
		t.Error("a Welcome for a Hello with the operation's fields")
	}

	if d.calls() != 0 {
		t.Errorf("the deliverer was called %d times", d.calls())
	}
	for _, ev := range e.eventsNow() {
		if ev.Event == "inject" {
			t.Errorf("an inject event: %+v", ev)
		}
	}
	// The same request on the control socket reaches the deliverer.
	if r := e.mustControl(injectReq("inj-control")); !r.OK || r.Inject == nil || d.calls() != 1 {
		t.Errorf("control: %+v, %d calls", r, d.calls())
	}
}

// Closing the service ends the deliverer's context and does not wait for it
// to return; a request after the close is refused before the deliverer.
func TestInjectAndClose(t *testing.T) {
	d := &fakeDeliverer{entered: make(chan struct{}, 2), fn: func(ctx context.Context, _, _ string) wire.InjectResult {
		<-ctx.Done()
		return submitted()
	}}
	e := canaryEnv(t, d)
	go func() { _, _ = e.control(injectReq("inj-close")) }()
	<-d.entered
	closed := make(chan struct{})
	go func() { _ = e.srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(patience):
		t.Fatal("Close did not return")
	}
	if _, _, ctx := d.first(); ctx.Err() == nil {
		t.Error("the deliverer's context did not end")
	}
	if r := e.srv.inject(injectReq("inj-late")); r.OK || r.Code != wire.CodeBusy {
		t.Errorf("after the close: %+v", r)
	}
	if d.calls() != 1 {
		t.Errorf("%d calls", d.calls())
	}
}
