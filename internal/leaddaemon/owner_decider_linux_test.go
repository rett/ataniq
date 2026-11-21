//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/presence"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
)

// TestOwnerHelloListsTheOperationsOnlyWithTheHook: a daemon whose ServeHook is
// not set lists the two base operations and says it mutates nothing, as before;
// with the owner channel's hook it lists them and then the ten owner.*
// operations in the wire package's order, says it mutates, and still speaks
// only ataniq.client/1.
func TestOwnerHelloListsTheOperationsOnlyWithTheHook(t *testing.T) {
	e := newEnv(t)
	plain := e.startDaemon(e.config())
	h := ownerHello(t, plain.socket)
	if strings.Join(h.Operations, " ") != "hello status" || h.Mutations {
		t.Fatalf("without the hook: %v mutations %v", h.Operations, h.Mutations)
	}

	r := newOwnerRig(t)
	h = ownerHello(t, r.run.socket)
	want := append([]string{"hello", "status"}, ownerwire.Operations()...)
	if strings.Join(h.Operations, " ") != strings.Join(want, " ") || len(h.Operations) != 12 || !h.Mutations {
		t.Fatalf("with the hook: %v mutations %v, want %v", h.Operations, h.Mutations, want)
	}
	if strings.Join(h.Protocols, " ") != clientapi.Protocol || clientapi.Protocol != "ataniq.client/1" {
		t.Fatalf("protocols %v", h.Protocols)
	}
	if got := strings.Join(OwnerOperations(), " "); got != strings.Join(ownerwire.Operations(), " ") {
		t.Fatalf("OwnerOperations: %s", got)
	}
}

// TestOwnerTheDecidersPlaceGoesToAConsoleWithATerminal: the place is granted
// to a connection whose client names the console and whose peer the inspector
// found with a terminal; the inspector is asked about the peer process the
// daemon saw, and a decision from it reaches the service with the session it
// named, its process, its terminal and its client name, which the service
// writes into the decision.
func TestOwnerTheDecidersPlaceGoesToAConsoleWithATerminal(t *testing.T) {
	r := newOwnerRig(t)
	c := r.dial()
	res, err := r.askDecider(c, ownerConsoleClient, ownerSession)
	if err != nil || !res.Decider || res.Reason != "" {
		t.Fatalf("owner.decider: %+v, %v", res, err)
	}
	seen := r.insp.inspected()
	if len(seen) != 1 || seen[0].PID != os.Getpid() || seen[0].AcceptedAt.IsZero() || time.Since(seen[0].AcceptedAt) < 0 {
		t.Fatalf("the inspector was asked about %+v, want this process (%d)", seen, os.Getpid())
	}

	out, err := r.decide(c, ownerFrame("ap-one"))
	if err != nil || out.State != record.StateApplied {
		t.Fatalf("owner.decide: %+v, %v", out, err)
	}
	got := r.svc.decided()
	if len(got) != 1 {
		t.Fatalf("the service saw %d decisions", len(got))
	}
	wantSrc := ownerchannel.Source{
		Surface: "console", Session: ownerSession, Decider: true,
		PID: int64(os.Getpid()), TTY: ownerTTY, Client: ownerConsoleClient,
	}
	if got[0].Src != wantSrc {
		t.Fatalf("source %+v, want %+v", got[0].Src, wantSrc)
	}
	if got[0].Frame.ID != "ap-one" || got[0].Frame.Decision != record.DecisionApprove {
		t.Fatalf("frame %+v", got[0].Frame)
	}
	r.wantCounts(map[string]uint64{"granted": 1})
	// Asking again with the same session keeps the place and asks the
	// inspector nothing more.
	if res, err := r.askDecider(c, ownerConsoleClient, ownerSession); err != nil || !res.Decider {
		t.Fatalf("again: %+v, %v", res, err)
	}
	if n := len(r.insp.inspected()); n != 1 {
		t.Fatalf("the inspector was asked %d times", n)
	}
}

// TestOwnerWhoIsNotGrantedTheDecidersPlace: each way a connection can fail to
// qualify is refused with its own reason and its own count, gets no place, and
// a decision frame from it is refused not-decider, counted, and handed to the
// service as a frame of a connection that is not the decider so that the
// journal can keep it.
func TestOwnerWhoIsNotGrantedTheDecidersPlace(t *testing.T) {
	for _, tt := range []struct {
		name   string
		client string
		reply  func(PeerRef) (PeerProcess, error)
		reason string
		count  string
		asks   int
	}{
		{"a client that is not the console", "other-tool 1.0", nil, reasonNotConsole, "not-console", 0},
		{"the console's name as a prefix of another", "ataniq-consoles 1.0", nil, reasonNotConsole, "not-console", 0},
		{"a client with no terminal", ownerConsoleClient,
			func(PeerRef) (PeerProcess, error) { return PeerProcess{}, nil }, reasonNoTerminal, "no-terminal", 1},
		{"a peer the inspector cannot judge", ownerConsoleClient,
			func(PeerRef) (PeerProcess, error) { return PeerProcess{}, errors.New("the process id was reused") },
			reasonNotJudged, "not-inspected", 1},
		{"a terminal name with a control byte", ownerConsoleClient,
			func(PeerRef) (PeerProcess, error) { return PeerProcess{TTY: "/dev/pts/\x1b[2J"}, nil },
			reasonNoTerminal, "no-terminal", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newOwnerRig(t)
			r.insp.reply = tt.reply
			c := r.dial()
			res, err := r.askDecider(c, tt.client, ownerSession)
			if err != nil || res.Decider || res.Reason != tt.reason {
				t.Fatalf("owner.decider: %+v, %v, want not the decider because %q", res, err, tt.reason)
			}
			if n := len(r.insp.inspected()); n != tt.asks {
				t.Fatalf("the inspector was asked %d times, want %d", n, tt.asks)
			}
			_, err = r.decide(c, ownerFrame("ap-one"))
			wantOwnerCode(t, err, "not-decider")
			got := r.svc.decided()
			if len(got) != 1 || got[0].Src.Decider || got[0].Src.Session != ownerSession || got[0].Src.PID != 0 || got[0].Src.TTY != "" || got[0].Src.Client != "" {
				t.Fatalf("the service saw %+v", got)
			}
			r.wantCounts(map[string]uint64{tt.count: 1, "decision-not-decider": 1})
			// A beat from it reaches the service as the beat of a connection
			// that is not the decider.
			if err := c.Call(ownerCtx(t), ownerwire.OpBeat, nil, nil); err != nil {
				t.Fatal(err)
			}
			if b := r.svc.beaten(); len(b) != 1 || b[0].Decider || b[0].Session != ownerSession {
				t.Fatalf("beat %+v", b)
			}
		})
	}
}

// TestOwnerASecondConsoleIsReadOnly: while one connection holds the place a
// second console that asks gets "read-only" and a count, and its decision frame
// is refused; the first still decides. When the first connection ends, the
// place is free, and the second is granted it on asking again; a connection
// that ended does not hold it.
func TestOwnerASecondConsoleIsReadOnly(t *testing.T) {
	r := newOwnerRig(t)
	first, second := r.dial(), r.dial()
	r.mustBeDecider(first, ownerSession)

	res, err := r.askDecider(second, ownerConsoleClient, ownerSession2)
	if err != nil || res.Decider || res.Reason != reasonSecond {
		t.Fatalf("second console: %+v, %v", res, err)
	}
	r.wantCounts(map[string]uint64{"granted": 1, "second": 1})
	if _, err := r.decide(second, ownerFrame("ap-two")); err == nil {
		t.Fatal("the second console's decision was taken")
	} else {
		wantOwnerCode(t, err, "not-decider")
	}
	if _, err := r.decide(first, ownerFrame("ap-one")); err != nil {
		t.Fatalf("the decider's decision: %v", err)
	}
	got := r.svc.decided()
	if len(got) != 2 || got[0].Src.Decider != false || got[0].Frame.ID != "ap-two" || got[1].Src.Decider != true || got[1].Frame.ID != "ap-one" {
		t.Fatalf("the service saw %+v", got)
	}
	if got[0].Src.Session != ownerSession2 || got[1].Src.Session != ownerSession {
		t.Fatalf("each connection's own session goes with its frames: %+v", got)
	}
	// The second's reads and its beats work; only a decision is refused.
	if err := second.Call(ownerCtx(t), ownerwire.OpLooked, nil, nil); err != nil {
		t.Fatal(err)
	}

	// The first ends; the place is free.
	first.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err = r.askDecider(second, ownerConsoleClient, ownerSession2)
		if err == nil && res.Decider {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the place was not freed when its connection ended: %+v, %v", res, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestOwnerOnlyOneConnectionIsEverTheDecider: many connections ask at once and
// exactly one is granted the place.
func TestOwnerOnlyOneConnectionIsEverTheDecider(t *testing.T) {
	r := newOwnerRig(t)
	const n = 8
	conns := make([]*clientapi.Client, n)
	for i := range conns {
		conns[i] = r.dial()
	}
	results := make(chan bool, n)
	for i := range conns {
		go func(c *clientapi.Client) {
			var res ownerwire.DeciderResult
			err := c.Call(ownerCtx(t), ownerwire.OpDecider, ownerwire.DeciderParams{Client: ownerConsoleClient, Session: ownerSession}, &res)
			results <- err == nil && res.Decider
		}(conns[i])
	}
	granted := 0
	for range conns {
		if <-results {
			granted++
		}
	}
	if granted != 1 {
		t.Fatalf("%d connections were granted the place", granted)
	}
	r.wantCounts(map[string]uint64{"granted": 1, "second": n - 1})
}

// TestOwnerTheSessionIsKeptForTheConnection: the session of owner.decider is
// kept; a second owner.decider that names another one is refused
// decision-invalid and counted, and changes nothing: the first session still
// goes with the connection's decisions. The same session again is fine.
func TestOwnerTheSessionIsKeptForTheConnection(t *testing.T) {
	r := newOwnerRig(t)
	c := r.dial()
	r.mustBeDecider(c, ownerSession)
	_, err := r.askDecider(c, ownerConsoleClient, ownerSession2)
	ce := wantOwnerCode(t, err, "decision-invalid")
	if strings.Contains(ce.Message, ownerSession2) {
		t.Fatalf("the refusal echoes the session: %q", ce.Message)
	}
	r.wantCounts(map[string]uint64{"granted": 1, "session-changed": 1})
	if _, err := r.decide(c, ownerFrame("ap-one")); err != nil {
		t.Fatal(err)
	}
	if got := r.svc.decided(); len(got) != 1 || got[0].Src.Session != ownerSession || !got[0].Src.Decider {
		t.Fatalf("the service saw %+v", got)
	}
	// A beat carries the session too.
	if err := c.Call(ownerCtx(t), ownerwire.OpBeat, nil, nil); err != nil {
		t.Fatal(err)
	}
	if b := r.svc.beaten(); len(b) != 1 || b[0].Session != ownerSession || !b[0].Decider || b[0].PID != int64(os.Getpid()) || b[0].TTY != ownerTTY {
		t.Fatalf("beat %+v", b)
	}
}

// TestOwnerTheClientOfTheGrantIsTheClientOfTheRecord: a connection that is
// refused as another tool and then asks again as the console, in the same
// session, is refused the second time too, decision-invalid and counted, so
// the client the decisions carry is never one the grant was not judged on. The
// console's own name asked first, then another name, changes nothing either.
func TestOwnerTheClientOfTheGrantIsTheClientOfTheRecord(t *testing.T) {
	r := newOwnerRig(t)
	c := r.dial()
	res, err := r.askDecider(c, "other-tool 1.0", ownerSession)
	if err != nil || res.Decider {
		t.Fatalf("the first ask: %+v, %v", res, err)
	}
	_, err = r.askDecider(c, ownerConsoleClient, ownerSession)
	wantOwnerCode(t, err, "decision-invalid")
	r.wantCounts(map[string]uint64{"not-console": 1, "session-changed": 1})
	if _, err := r.decide(c, ownerFrame("ap-one")); err == nil {
		t.Fatal("a connection refused as another tool decided")
	}
	if got := r.svc.decided(); len(got) != 1 || got[0].Src.Decider || got[0].Src.Client != "" {
		t.Fatalf("the service saw %+v", got)
	}
	if n := len(r.insp.inspected()); n != 0 {
		t.Fatalf("the inspector was asked %d times", n)
	}

	// The other order: the place held as the console, then another client name.
	c2 := r.dial()
	r.mustBeDecider(c2, ownerSession2)
	_, err = r.askDecider(c2, "other-tool 1.0", ownerSession2)
	wantOwnerCode(t, err, "decision-invalid")
	if _, err := r.decide(c2, ownerFrame("ap-two")); err != nil {
		t.Fatal(err)
	}
	got := r.svc.decided()
	if len(got) != 2 || got[1].Src.Client != ownerConsoleClient || !got[1].Src.Decider {
		t.Fatalf("the service saw %+v", got)
	}
}

// TestOwnerAConnectionThatNeverAskedGetsASessionOfTheDaemonsOwn: a decision
// frame from a connection that never called owner.decider is refused
// not-decider, and the service is told a well-formed session the daemon made
// for the connection, so that its journal can keep the refusal; two such
// connections have two sessions.
func TestOwnerAConnectionThatNeverAskedGetsASessionOfTheDaemonsOwn(t *testing.T) {
	r := newOwnerRig(t)
	a, b := r.dial(), r.dial()
	for _, c := range []*clientapi.Client{a, b} {
		_, err := r.decide(c, ownerFrame("ap-one"))
		wantOwnerCode(t, err, "not-decider")
	}
	got := r.svc.decided()
	if len(got) != 2 {
		t.Fatalf("the service saw %d frames", len(got))
	}
	for _, g := range got {
		if g.Src.Decider || !presence.ValidSession(g.Src.Session) || g.Src.Surface != "console" {
			t.Fatalf("source %+v", g.Src)
		}
	}
	if got[0].Src.Session == got[1].Src.Session {
		t.Fatalf("two connections share the session %q", got[0].Src.Session)
	}
	// The same connection keeps its session.
	if _, err := r.decide(a, ownerFrame("ap-three")); err == nil {
		t.Fatal("taken")
	}
	got = r.svc.decided()
	if got[2].Src.Session != got[0].Src.Session && got[2].Src.Session != got[1].Src.Session {
		t.Fatalf("a connection's fallback session changed: %+v", got)
	}
	r.wantCounts(map[string]uint64{"decision-not-decider": 3})
}

// TestOwnerADecisionFrameThatCarriesWhatTheDaemonFills: the daemon fills the
// pid, the terminal and the rest, so a frame that names any of them is refused
// decision-invalid before the service sees it, whoever sent it. A frame of a
// connection that is not the decider is refused not-decider whatever it
// holds, without the service when it does not even read.
func TestOwnerADecisionFrameThatCarriesWhatTheDaemonFills(t *testing.T) {
	r := newOwnerRig(t)
	c := r.dial()
	r.mustBeDecider(c, ownerSession)
	for _, extra := range []string{"console", "planner", "decided_at", "confirmed", "uses", "project", "pid", "tty", "session"} {
		frame := map[string]any{
			"id": "ap-one", "binding_sha256": strings.Repeat("ab", 32), "decision": "approve",
			"signed_off": []any{}, extra: "x",
		}
		err := c.Call(ownerCtx(t), ownerwire.OpDecide, frame, nil)
		wantOwnerCode(t, err, "decision-invalid")
	}
	if got := r.svc.decided(); len(got) != 0 {
		t.Fatalf("the service saw %+v", got)
	}
	r.wantCounts(map[string]uint64{"granted": 1, "decision-invalid": 9})

	other := r.dial()
	err := other.Call(ownerCtx(t), ownerwire.OpDecide, map[string]any{"id": "ap-one", "console": map[string]any{"decider": true}}, nil)
	wantOwnerCode(t, err, "not-decider")
	if got := r.svc.decided(); len(got) != 0 {
		t.Fatalf("a frame that does not read reached the service: %+v", got)
	}
}

// TestOwnerErrorsAreCodedAndTheirTextsAreFixed: a refusal of the service keeps
// its code and its host-written line; an error that is not a refusal is a code
// of its own with a fixed text, and what the error said (a path, say) is in the
// log and not in the answer.
func TestOwnerErrorsAreCodedAndTheirTextsAreFixed(t *testing.T) {
	r := newOwnerRig(t)
	c := r.dial()
	r.mustBeDecider(c, ownerSession)
	for _, tt := range []struct {
		name string
		err  error
		code clientapi.ErrorCode
		text string
	}{
		{"a refusal", &record.Error{Code: record.CodeOwnerAway, Detail: "no activity of the owner inside the presence window"}, "owner-away", "no activity of the owner inside the presence window"},
		{"a refusal with no detail", &record.Error{Code: record.CodeBindingMismatch}, "binding-mismatch", "binding-mismatch"},
		{"a refusal whose detail has a control byte", &record.Error{Code: record.CodeLimitRefused, Detail: "a\x1bb"}, "limit-refused", "limit-refused"},
		{"a wrapped refusal", errors.Join(errors.New("x"), &record.Error{Code: record.CodeReviewChanged, Detail: "the review changed"}), "review-changed", "the review changed"},
		{"the door", ownerchannel.ErrDoorUnavailable, "door-unavailable", msgOwnerDoor},
		{"the journal", ownerchannel.ErrJournalReopened, "journal-reopened", msgOwnerJournal},
		{"a stopped service", ownerchannel.ErrStopped, "unavailable", msgOwnerStopped},
		{"a closed service", ownerchannel.ErrClosed, "unavailable", msgOwnerStopped},
		{"anything else", errors.New("open /home/someone/secret: denied"), "failed", msgOwnerFailed},
		{"a code that would end the connection", &record.Error{Code: "busy", Detail: "x"}, "failed", msgOwnerFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r.svc.decide = func(ownerchannel.Source, ownerwire.DecisionFrame) (ownerwire.DecideResult, error) {
				return ownerwire.DecideResult{}, tt.err
			}
			_, err := r.decide(c, ownerFrame("ap-one"))
			ce := wantOwnerCode(t, err, tt.code)
			if ce.Message != tt.text {
				t.Fatalf("message %q, want %q", ce.Message, tt.text)
			}
			if strings.Contains(ce.Message, "/home/") {
				t.Fatalf("the answer holds what the error said: %q", ce.Message)
			}
		})
	}
	if !strings.Contains(r.logs.String(), `open /home/someone/secret: denied`) {
		t.Fatalf("the log lacks the error:\n%s", r.logs.String())
	}
}

// TestOwnerParamsAreStrict: a request whose params the wire's decoders refuse
// is answered with their code, and the service is not called.
func TestOwnerParamsAreStrict(t *testing.T) {
	r := newOwnerRig(t)
	c := r.dial()
	for _, tt := range []struct {
		op     string
		params any
		code   clientapi.ErrorCode
	}{
		{ownerwire.OpShow, map[string]any{"id": "ap-one", "more": 1}, "record-invalid"},
		{ownerwire.OpShow, nil, "record-invalid"},
		{ownerwire.OpPaths, map[string]any{"id": "ap-one"}, "record-invalid"},
		{ownerwire.OpPatch, map[string]any{"id": "ap-one", "review_digest": "x", "page": 0}, "record-invalid"},
		{ownerwire.OpDecider, map[string]any{"client": "ataniq-console 1", "session": "NOTHEX"}, "record-invalid"},
		{ownerwire.OpLimit, map[string]any{}, "record-invalid"},
		{ownerwire.OpLimitSet, map[string]any{"project": "p", "to": 0, "typed": true}, "record-invalid"},
		{ownerwire.OpList, map[string]any{"x": 1}, clientapi.CodeInvalidRequest},
		{ownerwire.OpBeat, map[string]any{"x": 1}, clientapi.CodeInvalidRequest},
		{ownerwire.OpLooked, map[string]any{"x": 1}, clientapi.CodeInvalidRequest},
	} {
		err := c.Call(ownerCtx(t), tt.op, tt.params, nil)
		wantOwnerCode(t, err, tt.code)
	}
	if got := r.svc.called(); len(got) != 0 {
		t.Fatalf("the service was called: %v", got)
	}
}

// TestOwnerTheRestOfTheOperationsReachTheService: list, show, limit, limit-set
// and looked reach the service with their params; the set of the limit carries
// the connection's source.
func TestOwnerTheRestOfTheOperationsReachTheService(t *testing.T) {
	r := newOwnerRig(t)
	c := r.dial()
	r.mustBeDecider(c, ownerSession)
	r.svc.limit = func(p string) (ownerwire.LimitView, error) {
		return ownerwire.LimitView{Project: p, Limit: 12, Days: []ownerwire.LimitDay{}, Suggestion: 6}, nil
	}
	r.svc.limitSet = func(_ ownerchannel.Source, p ownerwire.LimitSetParams) (ownerwire.LimitView, error) {
		return ownerwire.LimitView{Project: p.Project, Limit: p.To, Days: []ownerwire.LimitDay{}, Suggestion: 6}, nil
	}
	var view ownerwire.LimitView
	if err := c.Call(ownerCtx(t), ownerwire.OpLimit, ownerwire.LimitParams{Project: "proj"}, &view); err != nil || view.Project != "proj" || view.Limit != 12 {
		t.Fatalf("limit: %+v %v", view, err)
	}
	if err := c.Call(ownerCtx(t), ownerwire.OpLimitSet, ownerwire.LimitSetParams{Project: "proj", To: 9, Typed: false}, &view); err != nil || view.Limit != 9 {
		t.Fatalf("limit-set: %+v %v", view, err)
	}
	r.svc.mu.Lock()
	sets := append([]ownerLimitSetCall(nil), r.svc.limitSets...)
	r.svc.mu.Unlock()
	if len(sets) != 1 || sets[0].Params.To != 9 || !sets[0].Src.Decider || sets[0].Src.Session != ownerSession || sets[0].Src.PID != int64(os.Getpid()) {
		t.Fatalf("limit-set: %+v", sets)
	}
	var listing ownerwire.Listing
	if err := c.Call(ownerCtx(t), ownerwire.OpList, nil, &listing); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := c.Call(ownerCtx(t), ownerwire.OpLooked, nil, nil); err != nil {
		t.Fatalf("looked: %v", err)
	}
	r.svc.show = func(id string) (ownerwire.ShowResult, error) {
		return ownerwire.ShowResult{}, &record.Error{Code: record.CodeRecordInvalid, Detail: "no pending record has this id"}
	}
	err := c.Call(ownerCtx(t), ownerwire.OpShow, ownerwire.ShowParams{ID: "ap-nine"}, nil)
	wantOwnerCode(t, err, "record-invalid")
	want := "limit proj,limit-set,list,looked,show ap-nine"
	if got := strings.Join(r.svc.called(), ","); got != want {
		t.Fatalf("the service was called as %q, want %q", got, want)
	}
}
