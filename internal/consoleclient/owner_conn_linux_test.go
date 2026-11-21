package consoleclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
)

func TestADaemonWithNoOwnerOperationGivesNoOwnerChannel(t *testing.T) {
	f := startOwnerWith(t, clientapi.Options{}, nil)
	o := newOwner(t, f.dir)
	if _, err := o.List(); !errors.Is(err, ErrNoOwnerChannel) {
		t.Fatalf("List: %v, want ErrNoOwnerChannel", err)
	}
	ok, why := o.Decider()
	if ok || why != "no owner channel in this build" {
		t.Fatalf("Decider = %v, %q", ok, why)
	}
	if _, err := o.Open("ap-x"); !errors.Is(err, ErrNoOwnerChannel) {
		t.Fatalf("Open: %v", err)
	}
	if _, err := o.Limit("project-a"); !errors.Is(err, ErrNoOwnerChannel) {
		t.Fatalf("Limit: %v", err)
	}
	// Only the hello was asked: no owner operation reached the daemon, and
	// the daemon refused none.
	snap := f.counters.Snapshot()
	if snap.Unsupported != 0 || snap.Invalid != 0 || snap.Internal != 0 || len(f.sent()) != 0 {
		t.Fatalf("an operation reached the daemon: %+v %v", snap, f.sent())
	}
	if snap.Served[clientapi.OpHello] == 0 {
		t.Fatalf("the daemon was not asked: %+v", snap)
	}
	// Nothing is kept: a daemon that gets the channel later is found by the
	// next call.
	if o.conn != nil {
		t.Fatal("a connection to a daemon with no owner channel was kept")
	}
}

func TestADaemonWithSomeOwnerOperationsIsAnotherVersion(t *testing.T) {
	ops := ownerwire.Operations()
	f := startOwnerWith(t, clientapi.Options{}, ops[:len(ops)-1])
	o := newOwner(t, f.dir)
	if _, err := o.List(); !errors.Is(err, ErrOwnerVersion) {
		t.Fatalf("List: %v, want ErrOwnerVersion", err)
	}
	if ok, why := o.Decider(); ok || why != ErrOwnerVersion.Error() {
		t.Fatalf("Decider = %v, %q", ok, why)
	}
	if len(f.sent()) != 0 {
		t.Fatalf("an owner operation was sent: %v", f.sent())
	}
}

func TestEveryOwnerOperationIsNeededToUseTheChannel(t *testing.T) {
	// Leaving out any one operation of the list makes the channel unusable:
	// the check is on every name and not on the first.
	all := ownerwire.Operations()
	for i := range all {
		var ops []string
		ops = append(ops, all[:i]...)
		ops = append(ops, all[i+1:]...)
		f := startOwnerWith(t, clientapi.Options{}, ops)
		o := newOwner(t, f.dir)
		if _, err := o.List(); !errors.Is(err, ErrOwnerVersion) {
			t.Errorf("without %s: %v", all[i], err)
		}
		o.Close()
		f.stop()
	}
}

func TestAPeerOfAnotherProtocolIsNotAnOwnerChannel(t *testing.T) {
	hello := ownerHello()
	hello.Protocols = []string{"other.protocol/9"}
	p := startRawPeer(t, func(req clientapi.Request) []byte { return rawOK(t, req, hello) })
	o := newOwner(t, p.dir)
	_, err := o.List()
	var u *Unavailable
	if !errors.As(err, &u) || u.State != StateOtherProtocol {
		t.Fatalf("List: %v", err)
	}
	if ops := p.sent(); len(ops) != 1 || ops[0] != clientapi.OpHello {
		t.Fatalf("sent %v, want only hello", ops)
	}
}

func rawOK(t *testing.T, req clientapi.Request, result any) []byte {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return rawLine(t, clientapi.Response{Protocol: clientapi.Protocol, RequestID: req.RequestID, OK: true, Result: raw})
}

func rawErr(t *testing.T, req clientapi.Request, code clientapi.ErrorCode, msg string) []byte {
	t.Helper()
	return rawLine(t, clientapi.Response{Protocol: clientapi.Protocol, RequestID: req.RequestID, Error: &clientapi.ErrorBody{Code: code, Message: msg}})
}

func rawLine(t *testing.T, r clientapi.Response) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestNoDaemonAndAMissingSocketGivePlainStates(t *testing.T) {
	dir := socketDir(t)
	o := newOwner(t, dir)
	for name, call := range map[string]func() error{
		"List":     func() error { _, err := o.List(); return err },
		"Open":     func() error { _, err := o.Open("ap-x"); return err },
		"SetLimit": func() error { _, err := o.SetLimit("project-a", limitsChange(5)); return err },
	} {
		err := call()
		var u *Unavailable
		if !errors.As(err, &u) || u.State != StateNoDaemon {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if u.Error() != stateTexts[StateNoDaemon][0] || u.NextStep() != stateTexts[StateNoDaemon][1] {
			t.Errorf("%s: %q / %q", name, u.Error(), u.NextStep())
		}
	}
	ok, why := o.Decider()
	if ok || why != stateTexts[StateNoDaemon][0] {
		t.Errorf("Decider = %v, %q", ok, why)
	}
	// Presence and the mark are best effort: they fail quietly.
	o.Activity(time.Now())
	o.Looked(time.Now())
}

func TestTheSessionIsToldToTheDaemonBeforeAnythingElseOnEveryConnection(t *testing.T) {
	f := startOwner(t)
	f.onResult(ownerwire.OpList, mustEncode(t)(ownerwire.EncodeListing(ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}})))
	random := bytes.NewReader([]byte{0xa1, 0xb2, 0xc3, 0xd4, 0xe5, 0xf6, 0x07, 0x18})
	o, err := NewOwner(OwnerConfig{Dir: f.dir, Client: "ataniq-console 9.9", Random: random, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if _, err := o.List(); err != nil {
		t.Fatal(err)
	}
	f.restart()
	if _, err := o.List(); err == nil {
		// The first call after the restart finds the dead connection.
		t.Log("the first call after a restart succeeded")
	}
	if _, err := o.List(); err != nil {
		t.Fatal(err)
	}
	calls := f.sent()
	var runs [][]string
	for _, c := range calls {
		for len(runs) < c.run {
			runs = append(runs, nil)
		}
		runs[c.run-1] = append(runs[c.run-1], c.op)
		if c.op == ownerwire.OpDecider {
			p, err := ownerwire.DecodeDeciderParams([]byte(c.params))
			if err != nil {
				t.Fatalf("the decider's params: %v", err)
			}
			if p.Client != "ataniq-console 9.9" || p.Session != "a1b2c3d4e5f60718" {
				t.Fatalf("decider params = %+v", p)
			}
		}
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %v", runs)
	}
	for i, ops := range runs {
		if len(ops) < 2 || ops[0] != ownerwire.OpDecider || ops[1] != ownerwire.OpList {
			t.Errorf("run %d: the daemon heard %v, want owner.decider first and then owner.list", i+1, ops)
		}
	}
}

func TestNewOwnerChecksItsInputs(t *testing.T) {
	if _, err := NewOwner(OwnerConfig{Dir: "d", Random: bytes.NewReader([]byte{1, 2, 3})}); err == nil {
		t.Error("a session source that fails made an owner client")
	}
	if _, err := NewOwner(OwnerConfig{Dir: "d", Client: strings.Repeat("x", ownerwire.MaxNameBytes+1)}); err == nil {
		t.Error("a client name the daemon refuses made an owner client")
	}
	o, err := NewOwner(OwnerConfig{Dir: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.session) != 16 || o.name != "ataniq-console" || o.timeout != DefaultOwnerTimeout {
		t.Fatalf("defaults: %q %q %v", o.session, o.name, o.timeout)
	}
	o2, _ := NewOwner(OwnerConfig{Dir: "d"})
	if o.session == o2.session {
		t.Error("two runs share a session id")
	}
}

func TestDeciderAsksAgainAndCleansTheReason(t *testing.T) {
	f := startOwner(t)
	answers := make(chan ownerwire.DeciderResult, 4)
	f.on(ownerwire.OpDecider, func(json.RawMessage) (any, *clientapi.ErrorBody) {
		return json.RawMessage(mustEncode(t)(ownerwire.EncodeDeciderResult(<-answers))), nil
	})
	o := newOwner(t, f.dir)
	answers <- ownerwire.DeciderResult{Decider: true}
	if ok, why := o.Decider(); !ok || why != "" {
		t.Fatalf("first: %v %q", ok, why)
	}
	if n := f.count(ownerwire.OpDecider); n != 1 {
		t.Fatalf("the first call asked %d times: the answer made with the connection is the first answer", n)
	}
	answers <- ownerwire.DeciderResult{Decider: false, Reason: "another console " + hostileMark + " decides"}
	ok, why := o.Decider()
	if ok || why != clean("another console "+hostileMark+" decides") || strings.ContainsAny(why, "\u202e\u200b\u2066") || !strings.Contains(why, "another console") {
		t.Fatalf("second: %v %q", ok, why)
	}
	if n := f.count(ownerwire.OpDecider); n != 2 {
		t.Fatalf("the second call did not ask again: %d", n)
	}
	answers <- ownerwire.DeciderResult{Decider: true}
	if ok, _ := o.Decider(); !ok {
		t.Fatal("the console that got the place back is not the decider")
	}
}

func TestActivityAndLookedSendOnlyTheirOperationWithNoTime(t *testing.T) {
	f := startOwner(t)
	f.onResult(ownerwire.OpBeat, ownerwire.EncodeNothing())
	f.onResult(ownerwire.OpLooked, ownerwire.EncodeNothing())
	o := newOwner(t, f.dir)
	at := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	o.Activity(at)
	o.Looked(at)
	o.Activity(at)
	for _, op := range []string{ownerwire.OpBeat, ownerwire.OpLooked} {
		calls := f.of(op)
		if len(calls) == 0 {
			t.Fatalf("%s was not sent", op)
		}
		for _, c := range calls {
			if c.params != "" {
				t.Errorf("%s carried params %q: no time of the console's goes to the daemon", op, c.params)
			}
		}
	}
	if f.count(ownerwire.OpBeat) != 2 || f.count(ownerwire.OpLooked) != 1 {
		t.Fatalf("beats %d, looked %d", f.count(ownerwire.OpBeat), f.count(ownerwire.OpLooked))
	}
	if strings.Contains(joinParams(f.sent()), "2001") {
		t.Fatal("the console's time was sent")
	}
	// An answer that is not nothing is a bad answer and ends the connection.
	f.on(ownerwire.OpBeat, func(json.RawMessage) (any, *clientapi.ErrorBody) {
		return json.RawMessage(`{"x":1}`), nil
	})
	before := f.count(ownerwire.OpDecider)
	o.Activity(at)
	o.Activity(at)
	if f.count(ownerwire.OpDecider) != before+1 {
		t.Fatal("a daemon that answered a beat with something is trusted still")
	}
}

func joinParams(calls []fakeCall) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString(c.params)
	}
	return b.String()
}

func TestRefusalsOfTheDaemonReachTheCallerByTheirCodeAndNoText(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	f.onResult(ownerwire.OpList, mustEncode(t)(ownerwire.EncodeListing(ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}})))
	for _, code := range []record.Code{
		record.CodeOwnerAway, record.CodeNotDecider, record.CodeDecisionInvalid, record.CodeBindingMismatch,
		record.CodeApprovalExpired, record.CodeApprovalVoid, record.CodeApprovalConsumed, record.CodeReviewChanged,
		record.CodeRecordInvalid, record.CodeLimitRefused,
	} {
		f.refuse(ownerwire.OpList, clientapi.ErrorCode(code), "fixed text of the daemon")
		_, err := o.List()
		var rec *record.Error
		if !errors.As(err, &rec) || rec.Code != code {
			t.Errorf("%s: %v", code, err)
			continue
		}
		if code != record.CodeLimitRefused && rec.Detail != "" {
			t.Errorf("%s: a detail from the daemon: %q", code, rec.Detail)
		}
	}
	// A refusal is an answer: the connection stays.
	if n := f.count(ownerwire.OpDecider); n != 1 {
		t.Fatalf("a refusal made %d connections", n)
	}
	// A code that is not one of the approval packages' is not passed on.
	for _, code := range []clientapi.ErrorCode{"made-up-code", "internal", clientapi.CodeUnsupportedOperation} {
		f.refuse(ownerwire.OpList, code, "")
		_, err := o.List()
		var rec *record.Error
		var u *Unavailable
		if errors.As(err, &rec) || !errors.As(err, &u) {
			t.Errorf("%s: %v", code, err)
		}
		if strings.Contains(err.Error(), string(code)) {
			t.Errorf("%s: the code is in the text %q", code, err)
		}
	}
}

func TestAHostileMessageOfARefusalIsNeverShown(t *testing.T) {
	// The real server refuses to send such a message; a hostile peer does not.
	var mode sync.Mutex
	code := clientapi.ErrorCode(record.CodeLimitRefused)
	p := startRawPeer(t, func(req clientapi.Request) []byte {
		switch req.Op {
		case clientapi.OpHello:
			return rawOK(t, req, ownerHello())
		case ownerwire.OpDecider:
			return rawOK(t, req, ownerwire.DeciderResult{Decider: true})
		}
		mode.Lock()
		defer mode.Unlock()
		return rawErr(t, req, code, "raise "+hostileMark+"\x1b[2J\n"+strings.Repeat("x", 5000))
	})
	o := newOwner(t, p.dir)
	_, err := o.SetLimit("project-a", limitsChange(7))
	var rec *record.Error
	if !errors.As(err, &rec) || rec.Code != record.CodeLimitRefused {
		t.Fatal(err)
	}
	if strings.ContainsAny(rec.Detail, "\x1b\n\u202e") || len(rec.Detail) > MaxText || !strings.HasPrefix(rec.Detail, "raise ") {
		t.Fatalf("the limit's reason = %q", rec.Detail)
	}
	if strings.ContainsAny(rec.Error(), "\x1b\n\u202e") {
		t.Fatalf("Error() = %q", rec.Error())
	}
	// Another code carries none of the text, and an unknown one is a plain
	// state.
	mode.Lock()
	code = clientapi.ErrorCode(record.CodeOwnerAway)
	mode.Unlock()
	_, err = o.SetLimit("project-a", limitsChange(7))
	if !errors.As(err, &rec) || rec.Detail != "" || strings.Contains(err.Error(), "raise") {
		t.Fatalf("%v", err)
	}
}

func TestARefusalOfTheUserIsAPlainState(t *testing.T) {
	f := startOwnerWith(t, clientapi.Options{PeerUID: func(net.Conn) (uint32, error) { return 4_000_000, nil }}, ownerwire.Operations())
	o := newOwner(t, f.dir)
	_, err := o.List()
	var u *Unavailable
	if !errors.As(err, &u) || u.State != StateRefused {
		t.Fatalf("List: %v", err)
	}
	if ok, why := o.Decider(); ok || why != stateTexts[StateRefused][0] {
		t.Fatalf("Decider = %v %q", ok, why)
	}
}

func TestACallThatTakesTooLongIsASlowState(t *testing.T) {
	f := startOwner(t)
	release := make(chan struct{})
	defer close(release)
	f.on(ownerwire.OpList, func(json.RawMessage) (any, *clientapi.ErrorBody) {
		<-release
		return nil, &clientapi.ErrorBody{Code: "internal"}
	})
	o, err := NewOwner(OwnerConfig{Dir: f.dir, Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	start := time.Now()
	_, err = o.List()
	var u *Unavailable
	if !errors.As(err, &u) || u.State != StateSlow {
		t.Fatalf("List: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("List waited %v", d)
	}
	// The connection that timed out is gone, and the session is told again
	// to the next one.
	if o.conn != nil {
		t.Fatal("a connection that timed out was kept")
	}
}

func TestCloseEndsCallsAndAllLaterOnesRefuse(t *testing.T) {
	f := startOwner(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	f.on(ownerwire.OpList, func(json.RawMessage) (any, *clientapi.ErrorBody) {
		close(entered)
		<-release
		return nil, &clientapi.ErrorBody{Code: "internal"}
	})
	o := newOwner(t, f.dir)
	errc := make(chan error, 1)
	go func() { _, err := o.List(); errc <- err }()
	<-entered
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a call in flight returned no error after Close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the call in flight")
	}
	if _, err := o.List(); !errors.Is(err, ErrOwnerClosed) {
		t.Fatalf("List after Close: %v", err)
	}
	if ok, why := o.Decider(); ok || why != ErrOwnerClosed.Error() {
		t.Fatalf("Decider after Close = %v %q", ok, why)
	}
	if _, err := o.Decide(decisionOf(testCard{}, true)); err == nil {
		t.Fatal("Decide after Close")
	}
	if err := o.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
}

// A daemon that listed no owner operation is asked once for as many calls as
// the view makes between two actions of the owner: the answer is remembered.
// Without the memory every poll of the view would dial and read a hello.
func TestTheNoOwnerChannelAnswerIsRememberedBetweenActionsOfTheOwner(t *testing.T) {
	f := startOwnerWith(t, clientapi.Options{}, nil)
	o := newOwner(t, f.dir)
	for i := 0; i < 5; i++ {
		if _, err := o.List(); !errors.Is(err, ErrNoOwnerChannel) {
			t.Fatalf("List %d: %v", i, err)
		}
		if ok, why := o.Decider(); ok || why != NoOwnerChannelText {
			t.Fatalf("Decider %d = %v, %q", i, ok, why)
		}
	}
	if n := f.counters.Snapshot().Served[clientapi.OpHello]; n != 1 {
		t.Fatalf("the daemon was asked for its hello %d times, want once", n)
	}
}

// A daemon restarted with the owner channel is found when the owner opens the
// approvals view again: the view says Looked when he leaves it, and the next
// call asks the daemon. Nothing asks on a timer.
func TestADaemonRestartedWithTheChannelIsFoundByTheNextActionOfTheOwner(t *testing.T) {
	f := startOwnerWith(t, clientapi.Options{}, nil)
	o := newOwner(t, f.dir)
	if _, err := o.List(); !errors.Is(err, ErrNoOwnerChannel) {
		t.Fatalf("List: %v, want ErrNoOwnerChannel", err)
	}
	f.ops = ownerwire.Operations()
	f.onResult(ownerwire.OpLooked, ownerwire.EncodeNothing())
	f.onResult(ownerwire.OpList, mustEncode(t)(ownerwire.EncodeListing(ownerwire.Listing{})))
	f.restart()
	// Nothing the view does between actions of the owner asks again.
	if _, err := o.List(); !errors.Is(err, ErrNoOwnerChannel) {
		t.Fatalf("List before any action: %v, want the remembered answer", err)
	}
	// He leaves the view; the next opening reads the new daemon.
	o.Looked(time.Time{})
	if _, err := o.List(); err != nil {
		t.Fatalf("List after the owner left the view: %v", err)
	}
	if ok, why := o.Decider(); !ok {
		t.Fatalf("Decider = %v, %q", ok, why)
	}
}
