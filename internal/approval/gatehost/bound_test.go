package gatehost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/seatgate"
)

// testClock is a clock a test moves by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// boundHandler is a handler over f on a hand-moved clock, with two seat keys
// pinned.
func boundHandler(t *testing.T, f *fakePort) (*Handler, *logBook, *testClock) {
	t.Helper()
	lb := &logBook{}
	h, err := New(f, func(fp string) bool { return fp == seatFP || fp == secondFP }, lb.logf)
	if err != nil {
		t.Fatal(err)
	}
	clk := &testClock{t: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)}
	h.now = clk.now
	return h, lb, clk
}

// secondFP is a second pinned seat key.
var secondFP = strings.Repeat("ef56", 16)

// pair makes one seat.pair call with the test's params.
func pair(t *testing.T, h *Handler, peer seatgate.Peer) error {
	t.Helper()
	_, err := h.Handle(context.Background(), peer, ownerwire.OpSeatPair, validParams(t, ownerwire.OpSeatPair))
	return err
}

// voidCard is the refusal of an owner.show for a card that is not there: a
// refusal of what the peer named, which counts.
var voidCard = &record.Error{Code: record.CodeApprovalVoid, Detail: "no pending record has this id"}

// refusedShow makes one owner.show call, which f answers with its showErr.
func refusedShow(t *testing.T, h *Handler, peer seatgate.Peer) error {
	t.Helper()
	_, err := h.Handle(context.Background(), peer, ownerwire.OpShow, validParams(t, ownerwire.OpShow))
	return err
}

// A pinned peer that floods a request the handler refuses for what it sent
// gets RefusalBound of them to the service, each logged; then every request
// of it, a read and a pairing too, is refused too-many-refusals without the
// service, logged once.
func TestAFloodOfRefusalsIsBounded(t *testing.T) {
	if RefusalBound != 16 || RefusalWindow != 10*time.Minute {
		t.Fatalf("bound %d per %v", RefusalBound, RefusalWindow)
	}
	f := &fakePort{showErr: voidCard}
	h, lb, clk := boundHandler(t, f)
	for i := 0; i < RefusalBound; i++ {
		if err := refusedShow(t, h, seat); codeOfErr(err) != string(record.CodeApprovalVoid) {
			t.Fatalf("try %d: %v", i, err)
		}
		clk.add(time.Second)
	}
	if n := len(f.seen()); n != RefusalBound {
		t.Fatalf("the service had %d calls", n)
	}
	for i := 0; i < 50; i++ {
		if err := refusedShow(t, h, seat); codeOfErr(err) != CodeTooManyRefusals {
			t.Fatalf("past the bound, try %d: %v", i, err)
		}
	}
	// The peer is shut out from everything, not from that read alone.
	for _, op := range Operations() {
		if _, err := h.Handle(context.Background(), seat, op, validParams(t, op)); codeOfErr(err) != CodeTooManyRefusals {
			t.Errorf("%s past the bound: %v", op, err)
		}
	}
	if n := len(f.seen()); n != RefusalBound {
		t.Errorf("past the bound the service had %d calls", n-RefusalBound)
	}
	lines := lb.all()
	if len(lines) != RefusalBound+1 {
		t.Fatalf("%d log lines: %q", len(lines), lines)
	}
	for _, l := range lines[:RefusalBound] {
		if !strings.Contains(l, "owner.show refused approval-void") {
			t.Errorf("log %q", l)
		}
	}
	if want := "seat gate: the peer " + seatFP[:16] + " was refused 16 times within 10m0s; it is refused too-many-refusals, unlogged, until they age out"; lines[RefusalBound] != want {
		t.Errorf("log %q", lines[RefusalBound])
	}
}

// The seat relays seat.pair for anyone who reaches its pairing page, so no
// refusal of a pairing counts against the seat's one key: a stranger who
// floods wrong codes, or registrations the daemon refuses, never shuts the
// owner's logins, reads and decisions out. Each try still reaches the
// service, which burns the code after five wrong ones. Only unknown-op and
// not-pinned, which are of the peer and not of a browser, count.
func TestRefusedPairingsDoNotShutTheSeatOut(t *testing.T) {
	for _, e := range []error{
		ownerchannel.ErrPairRefused, ownerchannel.ErrCredentialInvalid, ownerchannel.ErrNoPairing,
		ownerchannel.ErrPairingExpired, ownerchannel.ErrPairingOpen, ownerchannel.ErrPairingChanged,
		ownerchannel.ErrKeyUnknown, ownerchannel.ErrKeyAmbiguous,
		&record.Error{Code: record.CodeRecordInvalid, Detail: "the parameters are not valid for this operation"},
	} {
		f := &fakePort{err: e}
		h, lb, clk := boundHandler(t, f)
		want := codeOf(e)
		for i := 0; i < 4*RefusalBound; i++ {
			if err := pair(t, h, seat); codeOfErr(err) != want {
				t.Fatalf("%v, try %d: %v", e, i, err)
			}
			clk.add(time.Second)
		}
		if n := len(f.seen()); n != 4*RefusalBound {
			t.Fatalf("%v: the service had %d calls", e, n)
		}
		f.mu.Lock()
		f.err = nil
		f.mu.Unlock()
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpList, nil); err != nil {
			t.Fatalf("%v: a read after the refused pairings: %v", e, err)
		}
		if n := len(lb.all()); n != 4*RefusalBound {
			t.Errorf("%v: %d log lines", e, n)
		}
	}
	// Params the handler refuses bad-params do not count either: the seat
	// passes on what a browser posted.
	f := &fakePort{}
	h, _, _ := boundHandler(t, f)
	for i := 0; i < 4*RefusalBound; i++ {
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatPair, json.RawMessage(`{"code":"x"}`)); codeOfErr(err) != CodeBadParams {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpList, nil); err != nil {
		t.Fatalf("a read after the bad params: %v", err)
	}
	// A pairing of a key that is not pinned counts, as any request of it.
	stranger := seatgate.Peer{Fingerprint: otherFP}
	for i := 0; i < RefusalBound; i++ {
		if err := pair(t, h, stranger); codeOfErr(err) != CodeNotPinned {
			t.Fatalf("the stranger's try %d: %v", i, err)
		}
	}
	if err := pair(t, h, stranger); codeOfErr(err) != CodeTooManyRefusals {
		t.Fatalf("the stranger past the bound: %v", err)
	}
}

// The window slides: once the oldest counted refusal is RefusalWindow old,
// one request reaches the service again, and a refusal of it shuts the peer
// out again with one more line.
func TestTheBoundSlidesWithTheWindow(t *testing.T) {
	f := &fakePort{showErr: voidCard}
	h, lb, clk := boundHandler(t, f)
	for i := 0; i < RefusalBound; i++ {
		_ = refusedShow(t, h, seat)
		clk.add(time.Minute / 2)
	}
	// The first refusal was 8 minutes ago.
	clk.add(2*time.Minute - time.Second)
	if err := refusedShow(t, h, seat); codeOfErr(err) != CodeTooManyRefusals {
		t.Fatalf("a second before the window: %v", err)
	}
	clk.add(time.Second)
	if err := refusedShow(t, h, seat); codeOfErr(err) != string(record.CodeApprovalVoid) {
		t.Fatalf("at the window: %v", err)
	}
	if err := refusedShow(t, h, seat); codeOfErr(err) != CodeTooManyRefusals {
		t.Fatalf("after one more refusal: %v", err)
	}
	if n := len(f.seen()); n != RefusalBound+1 {
		t.Errorf("the service had %d calls", n)
	}
	if n := len(lb.all()); n != RefusalBound+3 {
		t.Errorf("%d log lines", n)
	}
	// Long after, the peer is as new: a read is served.
	clk.add(RefusalWindow)
	f.mu.Lock()
	f.showErr = nil
	f.mu.Unlock()
	if err := refusedShow(t, h, seat); err != nil {
		t.Errorf("after the window: %v", err)
	}
}

// The bound is per peer: one key shut out leaves another served.
func TestTheBoundIsPerPeer(t *testing.T) {
	f := &fakePort{showErr: voidCard}
	h, _, _ := boundHandler(t, f)
	for i := 0; i <= RefusalBound; i++ {
		_ = refusedShow(t, h, seat)
	}
	if err := refusedShow(t, h, seat); codeOfErr(err) != CodeTooManyRefusals {
		t.Fatalf("the first peer: %v", err)
	}
	second := seatgate.Peer{Fingerprint: secondFP}
	if err := refusedShow(t, h, second); codeOfErr(err) != string(record.CodeApprovalVoid) {
		t.Errorf("the second peer: %v", err)
	}
	if _, err := h.Handle(context.Background(), second, ownerwire.OpList, nil); err != nil {
		t.Errorf("the second peer's read: %v", err)
	}
}

// Served calls never count, and neither do refusals for the daemon's own
// state: a seat that polls while the door is down is not shut out.
func TestOnlyRefusalsOfWhatThePeerSentCount(t *testing.T) {
	f := &fakePort{code: pairCode, open: true}
	h, _, _ := boundHandler(t, f)
	for i := 0; i < 5*RefusalBound; i++ {
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpList, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []error{
		errors.Join(ownerchannel.ErrDoorUnavailable, errors.New("down")),
		ownerchannel.ErrJournalReopened, ownerchannel.ErrClosed, ownerchannel.ErrStopped,
		ownerchannel.ErrKeysUnavailable, ownerchannel.ErrKeysFull,
		context.Canceled, context.DeadlineExceeded, errors.New("other"),
	} {
		f.mu.Lock()
		f.err = e
		f.mu.Unlock()
		for i := 0; i < 2*RefusalBound; i++ {
			if _, err := h.Handle(context.Background(), seat, ownerwire.OpShow, validParams(t, ownerwire.OpShow)); codeOfErr(err) == CodeTooManyRefusals {
				t.Fatalf("%v shut the peer out", e)
			}
		}
	}
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpList, nil); err != nil {
		t.Errorf("after the daemon's own refusals: %v", err)
	}
}

// Every code the handler answers with is classed: counted against the bound,
// the daemon's own, or one of normal use of a decision from the phone. A new
// code must be put in one of the three.
func TestEveryCodeIsClassed(t *testing.T) {
	own := map[string]bool{
		CodeUnavailable: true, CodeDeadline: true, CodeFailed: true, CodeDoorUnavailable: true,
		CodeJournalReopened: true, CodeKeysUnavailable: true, CodeKeysFull: true,
	}
	normal := map[string]bool{
		ownerwire.CodeChallengeStale: true, ownerwire.CodeChallengesFull: true,
		ownerwire.CodeKeyRevoked: true, ownerwire.CodeKindRefused: true,
	}
	all := []string{CodeUnknownOp, CodeBadParams, CodeNotPinned}
	for _, s := range sentinels {
		all = append(all, s.code)
	}
	for c := range passedRefusals {
		all = append(all, string(c))
	}
	all = append(all, CodeDeadline, CodeFailed)
	for _, c := range all {
		n := 0
		for _, class := range []map[string]bool{counted, own, normal} {
			if class[c] {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%q is counted %v, the daemon's own %v and of normal use %v", c, counted[c], own[c], normal[c])
		}
	}
	for c := range counted {
		if own[c] || normal[c] {
			t.Errorf("%q", c)
		}
	}
	if counted[CodeTooManyRefusals] || !seatgate.ValidCode(CodeTooManyRefusals) {
		t.Error("too-many-refusals")
	}
	for _, c := range []record.Code{record.CodeApprovalVoid, record.CodeReviewChanged} {
		if !counted[string(c)] {
			t.Errorf("%q is not counted", c)
		}
	}
}

// A key unpinned after its handshake counts its not-pinned refusals, so its
// log lines are bounded too.
func TestNotPinnedRefusalsAreBounded(t *testing.T) {
	f := &fakePort{code: pairCode, open: true}
	h, lb, _ := boundHandler(t, f)
	stranger := seatgate.Peer{Fingerprint: otherFP}
	for i := 0; i < 100; i++ {
		_, err := h.Handle(context.Background(), stranger, ownerwire.OpList, nil)
		want := CodeNotPinned
		if i >= RefusalBound {
			want = CodeTooManyRefusals
		}
		if codeOfErr(err) != want {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := len(lb.all()); n != RefusalBound+1 {
		t.Errorf("%d log lines", n)
	}
	if len(f.seen()) != 0 {
		t.Error("the service was called")
	}
}

// Requests in flight together are counted as they end: the peer is shut out
// after them however many there were, and the lines past the bound are not
// written.
func TestConcurrentRefusalsAreCounted(t *testing.T) {
	f := &fakePort{showErr: voidCard}
	h, lb, _ := boundHandler(t, f)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 16; j++ {
				_, _ = h.Handle(context.Background(), seat, ownerwire.OpShow, validParams(t, ownerwire.OpShow))
			}
		}()
	}
	wg.Wait()
	if err := refusedShow(t, h, seat); codeOfErr(err) != CodeTooManyRefusals {
		t.Errorf("after the flood: %v", err)
	}
	if n := len(lb.all()); n > RefusalBound+1 {
		t.Errorf("%d log lines", n)
	}
	if n := len(f.seen()); n < RefusalBound || n > RefusalBound+8 {
		t.Errorf("the service had %d calls", n)
	}
}

// The peers the bound keeps are swept of those with nothing inside the window
// once there are many.
func TestTheBoundForgetsQuietPeers(t *testing.T) {
	var b bound
	at := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	for i := 0; i < maxPeers; i++ {
		b.count(strings.Repeat("0", 60)+string(rune('a'+i%6))+string(rune('a'+i/6%6))+string(rune('a'+i/36))+"0", at)
	}
	if len(b.peers) != maxPeers {
		t.Fatalf("%d peers", len(b.peers))
	}
	b.count(seatFP, at.Add(RefusalWindow))
	if len(b.peers) != 1 {
		t.Errorf("%d peers after the sweep", len(b.peers))
	}
}
