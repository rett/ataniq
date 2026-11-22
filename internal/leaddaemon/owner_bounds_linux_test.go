//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/clientapi"
)

// ownerShortIdle starts the daemon with the server's idle time cut to idle, as
// a test needs to wait for it.
func ownerShortIdle(t *testing.T, idle, deciderIdle time.Duration) *ownerRig {
	t.Helper()
	r := newUnstartedOwnerRig(t)
	r.oc.DeciderIdle = deciderIdle
	cfg := r.config()
	cfg.ServeHook = func(l net.Listener, opts *clientapi.Options) (net.Listener, func(), error) {
		opts.IdleTimeout = idle
		return r.oc.Serve(l, opts)
	}
	r.run = r.e.startDaemon(cfg)
	return r
}

// TestOwnerTheDecidersConnectionOutlivesTheServersIdleTime: the server closes
// a connection that sends nothing for its idle time; the connection that
// holds the decider's place is kept for the longer idle time of the owner
// channel, so the place is not lost while the owner reads a card and a
// decision typed after the wait is still taken. A connection that is not the
// decider is closed at the server's time. When the decider's own longer time
// passes the connection ends and the place is free.
func TestOwnerTheDecidersConnectionOutlivesTheServersIdleTime(t *testing.T) {
	r := ownerShortIdle(t, 150*time.Millisecond, 2*time.Second)
	decider, other := r.dial(), r.dial()
	r.mustBeDecider(decider, ownerSession)
	if res, err := r.askDecider(other, "other-tool 1.0", ownerSession2); err != nil || res.Decider {
		t.Fatalf("%+v, %v", res, err)
	}

	time.Sleep(600 * time.Millisecond) // four times the server's idle time
	if err := other.Call(ownerCtx(t), ownerwire.OpLooked, nil, nil); err == nil {
		t.Fatal("a connection that is not the decider outlived the server's idle time")
	}
	if _, err := r.decide(decider, ownerFrame("ap-one")); err != nil {
		t.Fatalf("the decider's connection was closed while idle: %v", err)
	}
	// A request sets the time again: another wait shorter than the bound.
	time.Sleep(600 * time.Millisecond)
	if _, err := r.decide(decider, ownerFrame("ap-two")); err != nil {
		t.Fatalf("the decider's connection was closed after the second wait: %v", err)
	}

	// Past the bound the connection ends and the place is free.
	next := r.dial()
	deadline := time.Now().Add(15 * time.Second)
	for {
		res, err := r.askDecider(next, ownerConsoleClient, ownerSession2)
		if err == nil && res.Decider {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the place was not freed after the decider's idle bound: %+v, %v", res, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := decider.Call(ownerCtx(t), ownerwire.OpLooked, nil, nil); err == nil {
		t.Fatal("the old decider's connection is still open")
	}
}

// TestOwnerTheDeciderIdleTimeDefaults: the owner channel's idle time for the
// decider is ten minutes unless set.
func TestOwnerTheDeciderIdleTimeDefaults(t *testing.T) {
	if DefaultDeciderIdle != 10*time.Minute || (&OwnerChannel{}).deciderIdle() != DefaultDeciderIdle {
		t.Fatalf("%v", DefaultDeciderIdle)
	}
	if got := (&OwnerChannel{DeciderIdle: time.Second}).deciderIdle(); got != time.Second {
		t.Fatalf("%v", got)
	}
}

// TestOwnerDecisionFramesOfAConnectionThatIsNotTheDeciderAreBounded: the first
// DecisionRefusalBound frames of a connection that is not the decider are
// refused and journaled as before; the next is refused without the journal and
// the connection is closed, once and counted; the decider and a new connection
// are not affected, and the decider's own frames are never counted.
func TestOwnerDecisionFramesOfAConnectionThatIsNotTheDeciderAreBounded(t *testing.T) {
	r := newOwnerRig(t)
	decider, noisy := r.dial(), r.dial()
	r.mustBeDecider(decider, ownerSession)
	for i := 0; i < 3*DecisionRefusalBound; i++ { // the decider's frames do not count
		if _, err := r.decide(decider, ownerFrame("ap-ok")); err != nil {
			t.Fatal(err)
		}
	}
	base := len(r.svc.decided())

	for i := 0; i < DecisionRefusalBound; i++ {
		_, err := r.decide(noisy, ownerFrame("ap-noise"))
		wantOwnerCode(t, err, "not-decider")
	}
	if got := len(r.svc.decided()) - base; got != DecisionRefusalBound {
		t.Fatalf("%d frames reached the service within the bound, want %d", got, DecisionRefusalBound)
	}
	if r.oc.Counts().DecisionFlooded.Load() != 0 {
		t.Fatal("closed within the bound")
	}

	// The next frame is not journaled and ends the connection.
	if _, err := r.decide(noisy, ownerFrame("ap-noise")); err == nil {
		t.Fatal("a frame past the bound was taken")
	}
	if got := len(r.svc.decided()) - base; got != DecisionRefusalBound {
		t.Fatalf("a frame past the bound reached the service: %d", got)
	}
	if _, err := r.decide(noisy, ownerFrame("ap-noise")); err == nil {
		t.Fatal("the connection is still open")
	}
	if got := len(r.svc.decided()) - base; got != DecisionRefusalBound {
		t.Fatalf("a frame on a closed connection reached the service: %d", got)
	}
	r.wantCounts(map[string]uint64{
		"granted": 1, "decision-not-decider": DecisionRefusalBound + 1, "decision-flooded": 1,
	})
	// The decider and a new connection still work.
	if _, err := r.decide(decider, ownerFrame("ap-ok")); err != nil {
		t.Fatal(err)
	}
	_, err := r.decide(r.dial(), ownerFrame("ap-new"))
	wantOwnerCode(t, err, "not-decider")
}

// TestOwnerAFrameThatDoesNotReadCountsTowardTheBound: a frame of a connection
// that is not the decider that is not even a decision is refused without the
// journal, and still counts, so malformed frames cannot be sent without end.
func TestOwnerAFrameThatDoesNotReadCountsTowardTheBound(t *testing.T) {
	r := newOwnerRig(t)
	c := r.dial()
	var closed bool
	for i := 0; i < DecisionRefusalBound+4 && !closed; i++ {
		err := c.Call(context.Background(), ownerwire.OpDecide, json.RawMessage(`{"nonsense":1}`), nil)
		if err == nil {
			t.Fatal("a malformed frame was taken")
		}
		closed = r.oc.Counts().DecisionFlooded.Load() > 0
	}
	if !closed {
		t.Fatal("the connection was never closed")
	}
	if n := len(r.svc.decided()); n != 0 {
		t.Fatalf("%d malformed frames were journaled", n)
	}
}

// TestOwnerTheExtensionRefusesAPeerWithNoKnownProcessId: even when the
// inspector would grant, a connection whose peer's process id is not known
// (0) is not given the place. The check is the extension's own, kept apart
// from the inspector's.
func TestOwnerTheExtensionRefusesAPeerWithNoKnownProcessId(t *testing.T) {
	oc := &OwnerChannel{Inspector: &ownerInspect{}}
	reg := newOwnerConns(time.Minute)
	x := &ownerExt{oc: oc, svc: &ownerFake{}, reg: reg}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := reg.wrap(a, time.Now())
	if c.pid != 0 {
		t.Fatalf("a pipe has a peer process id: %d", c.pid)
	}
	reg.bind(c) // this goroutine is the connection's
	defer c.Close()
	params, err := json.Marshal(ownerwire.DeciderParams{Client: ownerConsoleClient, Session: ownerSession})
	if err != nil {
		t.Fatal(err)
	}
	out, fail := x.decider(context.Background(), params)
	if fail != nil {
		t.Fatalf("%+v", fail)
	}
	var res ownerwire.DeciderResult
	if err := json.Unmarshal(out.(json.RawMessage), &res); err != nil || res.Decider || res.Reason != reasonNotJudged {
		t.Fatalf("%+v, %v", res, err)
	}
	if insp := oc.Inspector.(*ownerInspect); len(insp.inspected()) != 0 {
		t.Fatal("the inspector was asked about a peer with no process id")
	}
	if oc.Counts().NotInspected.Load() != 1 {
		t.Fatal("not counted")
	}
}

// TestOwnerRefusedSeatPairCallsOfAConnectionAreBounded: seat.pair is refused to
// every connection of a build with no seat source, and each refusal was a line
// of the daemon's log. The first DecisionRefusalBound refusals of a connection
// are logged as before; the next ends the connection, once and counted, and no
// line is written for the calls after it. Every refusal is counted. Another
// connection is not affected.
func TestOwnerRefusedSeatPairCallsOfAConnectionAreBounded(t *testing.T) {
	r := newOwnerRig(t)
	fakePairService(r.svc)
	noisy, other := r.dial(), r.dial()
	valid := ownerwire.SeatPairParams{Code: pairTestCode, CredentialID: make([]byte, 32), PublicKey: make([]byte, 91)}
	lines := func() int { return strings.Count(r.logs.String(), "a seat.pair call was refused") }
	for i := 0; i < DecisionRefusalBound; i++ {
		wantOwnerCode(t, noisy.Call(ownerCtx(t), ownerwire.OpSeatPair, valid, nil), "unavailable")
	}
	if got := lines(); got != DecisionRefusalBound {
		t.Fatalf("%d log lines within the bound, want %d", got, DecisionRefusalBound)
	}
	if r.oc.Counts().DecisionFlooded.Load() != 0 {
		t.Fatal("closed within the bound")
	}
	// The next is refused and ends the connection.
	var closed bool
	for i := 0; i < 4 && !closed; i++ {
		_ = noisy.Call(ownerCtx(t), ownerwire.OpSeatPair, valid, nil)
		closed = r.oc.Counts().DecisionFlooded.Load() > 0
	}
	if !closed {
		t.Fatal("the connection was never closed")
	}
	if got := lines(); got > DecisionRefusalBound {
		t.Fatalf("%d log lines past the bound, want at most %d", got, DecisionRefusalBound)
	}
	if got := r.oc.Counts().DecisionFlooded.Load(); got != 1 {
		t.Fatalf("%d flood closes, want 1", got)
	}
	if err := noisy.Call(ownerCtx(t), ownerwire.OpSeatPair, valid, nil); err == nil {
		t.Fatal("the connection is still open")
	}
	if got := r.oc.Counts().DecisionFlooded.Load(); got != 1 {
		t.Fatalf("%d flood closes after a call on the closed connection, want 1", got)
	}
	if got := r.oc.Counts().SeatRefused.Load(); got < uint64(DecisionRefusalBound)+1 {
		t.Fatalf("%d refusals counted", got)
	}
	// Another connection is refused as before, and logs.
	before := lines()
	wantOwnerCode(t, other.Call(ownerCtx(t), ownerwire.OpSeatPair, valid, nil), "unavailable")
	if lines() != before+1 {
		t.Fatalf("a refusal on another connection wrote no line")
	}
	if got := r.svc.called(); len(got) != 0 {
		t.Fatalf("the service was called: %v", got)
	}
	if strings.Contains(r.logs.String(), pairTestCode) {
		t.Fatalf("the code is in the log:\n%s", r.logs.String())
	}
}
