package consoleclient

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
)

// decisionOf is the owner's decision of a card: an approve with the card's
// own flagged list signed off, or a deny.
func decisionOf(c testCard, approve bool) card.Decision {
	d := card.Decision{ID: c.p.ID, BindingSHA256: c.p.BindingSHA256, Approve: approve}
	if approve && c.p.Binding.Summary != nil {
		for _, fl := range c.p.Binding.Summary.Flagged {
			d.SignedOff = append(d.SignedOff, record.SignOff{Kind: fl.Kind, Path: fl.Path})
		}
	}
	return d
}

func decideResult(t *testing.T, state record.State, line string, held bool) []byte {
	t.Helper()
	return mustEncode(t)(ownerwire.EncodeDecideResult(ownerwire.DecideResult{State: state, Line: line, Held: held}))
}

// decidingFake serves the card and answers every owner.decide with answer.
func decidingFake(t *testing.T, c testCard, answer []byte) (*ownerFake, *Owner) {
	t.Helper()
	f := startOwner(t)
	o := newOwner(t, f.dir)
	f.onResult(ownerwire.OpDecide, answer)
	openOne(t, f, o, c)
	return f, o
}

func flaggedCard(t *testing.T) testCard {
	t.Helper()
	paths := []record.PathChange{testChange("Makefile", "M", 4, 1), testChange("lib/a.go", "M", 3, 1)}
	return newTestCard(t, "flagged", "build-target", t0, paths, 0,
		record.Flag{Kind: record.FlagExecConfig, Path: "Makefile", Pattern: "Makefile"})
}

func TestADecisionGoesOutWithTheFieldsOfTheFrameAndNothingElse(t *testing.T) {
	c := flaggedCard(t)
	f, o := decidingFake(t, c, decideResult(t, record.StateApproved, "approved: waiting to be applied", false))
	out, err := o.Decide(decisionOf(c, true))
	if err != nil {
		t.Fatal(err)
	}
	if out != (card.Outcome{State: record.StateApproved, Line: "approved: waiting to be applied"}) {
		t.Fatalf("outcome = %+v", out)
	}
	sent := f.of(ownerwire.OpDecide)
	if len(sent) != 1 {
		t.Fatalf("%d decisions reached the daemon", len(sent))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sent[0].params), &fields); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Exactly the fields of an approve: no reason, no time, no confirm code,
	// nothing the daemon fills.
	if !reflect.DeepEqual(keys, []string{"binding_sha256", "decision", "id", "signed_off"}) {
		t.Fatalf("an approve's fields = %v", keys)
	}
	frame, err := ownerwire.DecodeDecisionFrame([]byte(sent[0].params))
	if err != nil {
		t.Fatal(err)
	}
	want := ownerwire.DecisionFrame{
		ID: c.p.ID, BindingSHA256: c.p.BindingSHA256, Decision: record.DecisionApprove,
		SignedOff: []record.SignOff{{Kind: record.FlagExecConfig, Path: "Makefile"}},
	}
	if !reflect.DeepEqual(frame, want) {
		t.Fatalf("frame = %+v, want %+v", frame, want)
	}
}

func TestADenyCarriesItsReasonAndNoSignOffs(t *testing.T) {
	c := flaggedCard(t)
	f, o := decidingFake(t, c, decideResult(t, record.StateDenied, "denied by the owner", false))
	d := decisionOf(c, false)
	d.Reason = "not this week"
	out, err := o.Decide(d)
	if err != nil || out.State != record.StateDenied {
		t.Fatalf("%+v %v", out, err)
	}
	frame, err := ownerwire.DecodeDecisionFrame([]byte(f.of(ownerwire.OpDecide)[0].params))
	if err != nil {
		t.Fatal(err)
	}
	want := ownerwire.DecisionFrame{ID: c.p.ID, BindingSHA256: c.p.BindingSHA256, Decision: record.DecisionDeny, SignedOff: []record.SignOff{}, Reason: "not this week"}
	if !reflect.DeepEqual(frame, want) {
		t.Fatalf("frame = %+v", frame)
	}
	if !strings.Contains(f.of(ownerwire.OpDecide)[0].params, `"signed_off":[]`) {
		t.Fatalf("a deny's sign-offs are not an empty list: %s", f.of(ownerwire.OpDecide)[0].params)
	}
}

func TestADecisionTheWirePackageRefusesIsNotSent(t *testing.T) {
	c := flaggedCard(t)
	f, o := decidingFake(t, c, decideResult(t, record.StateApproved, "x", false))
	bad := map[string]card.Decision{}
	d := decisionOf(c, true)
	d.Reason = "an approve has no reason"
	bad["an approve with a reason"] = d
	d = decisionOf(c, false)
	d.SignedOff = []record.SignOff{{Kind: record.FlagExecConfig, Path: "Makefile"}}
	bad["a deny with sign-offs"] = d
	d = decisionOf(c, false)
	d.Reason = strings.Repeat("r", record.MaxReasonBytes+1)
	bad["a reason too long"] = d
	d = decisionOf(c, true)
	d.BindingSHA256 = "short"
	bad["a digest that is none"] = d
	d = decisionOf(c, true)
	d.SignedOff = append(d.SignedOff, d.SignedOff[0])
	bad["a repeated sign-off"] = d
	d = decisionOf(c, false)
	d.Reason = "line\nbreak"
	bad["a reason with a control character"] = d
	for name, d := range bad {
		_, err := o.Decide(d)
		if code, ok := record.CodeOf(err); !ok || code != record.CodeDecisionInvalid && code != record.CodeRecordInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := f.count(ownerwire.OpDecide); n != 0 {
		t.Fatalf("%d refused decisions were sent", n)
	}
	// A refused decision is not in doubt: a good one goes out.
	if _, err := o.Decide(decisionOf(c, true)); err != nil {
		t.Fatal(err)
	}
	if n := f.count(ownerwire.OpDecide); n != 1 {
		t.Fatalf("%d", n)
	}
}

func TestADecisionOnACardThatWasNotOpenIsNotSent(t *testing.T) {
	c := flaggedCard(t)
	f := startOwner(t)
	o := newOwner(t, f.dir)
	f.onResult(ownerwire.OpDecide, decideResult(t, record.StateApproved, "x", false))
	if _, err := o.Decide(decisionOf(c, true)); !errors.Is(err, ErrCardNotOpen) {
		t.Fatalf("%v", err)
	}
	if len(f.sent()) != 0 {
		t.Fatalf("%v", f.sent())
	}
}

func TestTheOutcomeIsWhatTheDaemonSaysAndItsLineIsCleaned(t *testing.T) {
	c := flaggedCard(t)
	for _, tc := range []struct {
		name  string
		state record.State
		held  bool
	}{
		{"approved", record.StateApproved, false},
		{"held", record.StatePending, true},
		{"not applied", record.StateNotApplied, false},
		{"expired", record.StateExpired, false},
	} {
		line := tc.name + " " + hostileMark
		f, o := decidingFake(t, c, decideResult(t, tc.state, line, tc.held))
		out, err := o.Decide(decisionOf(c, true))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if out.State != tc.state || out.Held != tc.held || out.Line != clean(line) || strings.ContainsAny(out.Line, "\u202e\u200b\u2066") {
			t.Errorf("%s: %+v", tc.name, out)
		}
		_ = f
	}
}

func TestADecisionIsSentOnceAndAFinalOutcomeIsAnsweredAgainWithoutSending(t *testing.T) {
	c := flaggedCard(t)
	f, o := decidingFake(t, c, decideResult(t, record.StateApproved, "approved", false))
	first, err := o.Decide(decisionOf(c, true))
	if err != nil {
		t.Fatal(err)
	}
	// The owner's second key press, or a repeat after a redraw, is answered
	// from what the daemon said.
	for i := 0; i < 3; i++ {
		again, err := o.Decide(decisionOf(c, true))
		if err != nil || again != first {
			t.Fatalf("%d: %+v %v", i, again, err)
		}
	}
	// Also a different decision for the same card: it was decided.
	other, err := o.Decide(decisionOf(c, false))
	if err != nil || other != first {
		t.Fatalf("%+v %v", other, err)
	}
	if n := f.count(ownerwire.OpDecide); n != 1 {
		t.Fatalf("%d decisions reached the daemon", n)
	}
}

func TestManyDecisionsAtOnceSendOne(t *testing.T) {
	c := flaggedCard(t)
	f, o := decidingFake(t, c, decideResult(t, record.StateApproved, "approved", false))
	var wg sync.WaitGroup
	outs := make([]card.Outcome, 12)
	errs := make([]error, 12)
	for i := range outs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i], errs[i] = o.Decide(decisionOf(c, true))
		}()
	}
	wg.Wait()
	if n := f.count(ownerwire.OpDecide); n != 1 {
		t.Fatalf("%d decisions reached the daemon", n)
	}
	for i := range outs {
		if errs[i] != nil || outs[i].State != record.StateApproved {
			t.Errorf("%d: %+v %v", i, outs[i], errs[i])
		}
	}
}

func TestAHeldApproveMayBeSentAgainAndTheDaemonDecides(t *testing.T) {
	c := flaggedCard(t)
	f, o := decidingFake(t, c, decideResult(t, record.StatePending, "held past the daily limit", true))
	for i := 0; i < 2; i++ {
		out, err := o.Decide(decisionOf(c, true))
		if err != nil || !out.Held || out.State != record.StatePending {
			t.Fatalf("%d: %+v %v", i, out, err)
		}
	}
	if n := f.count(ownerwire.OpDecide); n != 2 {
		t.Fatalf("%d decisions reached the daemon, want 2: a held approve is not final", n)
	}
}

func TestADaemonsRefusalOfADecisionIsAnswerNotDoubt(t *testing.T) {
	c := flaggedCard(t)
	for _, code := range []record.Code{record.CodeOwnerAway, record.CodeNotDecider, record.CodeBindingMismatch, record.CodeApprovalExpired, record.CodeDecisionInvalid} {
		f, o := decidingFake(t, c, nil)
		f.refuse(ownerwire.OpDecide, clientapi.ErrorCode(code), "fixed")
		_, err := o.Decide(decisionOf(c, true))
		if got, ok := record.CodeOf(err); !ok || got != code {
			t.Fatalf("%s: %v", code, err)
		}
		// The owner may try again once the cause is gone: the refusal decided
		// nothing.
		f.onResult(ownerwire.OpDecide, decideResult(t, record.StateApproved, "approved", false))
		out, err := o.Decide(decisionOf(c, true))
		if err != nil || out.State != record.StateApproved {
			t.Fatalf("%s: second try %+v %v", code, out, err)
		}
		if n := f.count(ownerwire.OpDecide); n != 2 {
			t.Fatalf("%s: %d", code, n)
		}
	}
}

func TestALostConnectionDropsWhatWasTypedAndDecidesNothing(t *testing.T) {
	c := flaggedCard(t)
	f, o := decidingFake(t, c, decideResult(t, record.StateApproved, "approved", false))
	// The daemon restarts. The next list finds the connection dead, and the
	// one after it makes a new connection, over which the card was not read.
	f.restart()
	f.onResult(ownerwire.OpList, mustEncode(t)(ownerwire.EncodeListing(ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}})))
	f.onResult(ownerwire.OpDecide, decideResult(t, record.StateApproved, "approved", false))
	for i := 0; i < 3; i++ {
		if _, err := o.List(); err == nil {
			break
		}
	}
	if o.conn == nil {
		t.Fatal("the console did not reconnect")
	}
	_, err := o.Decide(decisionOf(c, true))
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("Decide: %v, want ErrConnectionLost", err)
	}
	if n := f.count(ownerwire.OpDecide); n != 0 {
		t.Fatalf("%d decisions reached the daemon over a connection the card was not read on", n)
	}
	// Opening the card again, on the new connection, makes a decision
	// possible: the owner sees the card again and decides again.
	f.serveCard(c)
	if _, err := o.Open(c.p.ID); err != nil {
		t.Fatal(err)
	}
	if out, err := o.Decide(decisionOf(c, true)); err != nil || out.State != record.StateApproved {
		t.Fatalf("%+v %v", out, err)
	}
	if n := f.count(ownerwire.OpDecide); n != 1 {
		t.Fatalf("%d", n)
	}
}

func TestADecisionOnADeadConnectionIsInDoubtAndIsNeverReplayed(t *testing.T) {
	c := flaggedCard(t)
	f, o := decidingFake(t, c, decideResult(t, record.StateApproved, "approved", false))
	f.restart()
	// The connection is dead, and the console has not noticed yet.
	_, err := o.Decide(decisionOf(c, true))
	var u *Unavailable
	if err == nil || errors.As(err, new(*record.Error)) {
		t.Fatalf("Decide on a dead connection: %v", err)
	}
	if !errors.As(err, &u) {
		t.Fatalf("%v", err)
	}
	// A second try, with the daemon back and the connection made again, is
	// refused: the first one's fate is not known. It reaches nobody.
	f.onResult(ownerwire.OpList, mustEncode(t)(ownerwire.EncodeListing(ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}})))
	f.onResult(ownerwire.OpDecide, decideResult(t, record.StateApproved, "approved", false))
	if _, err := o.List(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := o.Decide(decisionOf(c, true)); !errors.Is(err, ErrDecisionUnsettled) {
			t.Fatalf("second decision %d: %v", i, err)
		}
	}
	if n := f.count(ownerwire.OpDecide); n != 0 {
		t.Fatalf("%d decisions were replayed", n)
	}
	// Reading the card again says where it stands, and ends the doubt: this
	// is a new decision of the owner's, made on what he now sees.
	f.serveCard(c)
	if _, err := o.Open(c.p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Decide(decisionOf(c, true)); err != nil {
		t.Fatal(err)
	}
	if n := f.count(ownerwire.OpDecide); n != 1 {
		t.Fatalf("%d", n)
	}
}

func TestADecisionWithNoAnswerInTimeIsInDoubtAndSentOnce(t *testing.T) {
	c := flaggedCard(t)
	f := startOwner(t)
	release := make(chan struct{})
	f.on(ownerwire.OpDecide, func(json.RawMessage) (any, *clientapi.ErrorBody) {
		<-release
		return json.RawMessage(decideResult(t, record.StateApproved, "approved", false)), nil
	})
	o, err := NewOwner(OwnerConfig{Dir: f.dir, Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	openOne(t, f, o, c)
	_, err = o.Decide(decisionOf(c, true))
	var u *Unavailable
	if !errors.As(err, &u) || u.State != StateSlow {
		t.Fatalf("%v", err)
	}
	close(release)
	// The owner presses the key again, and again: nothing is sent, whatever
	// the connection does, until the card is opened.
	for i := 0; i < 3; i++ {
		if _, err := o.Decide(decisionOf(c, true)); !errors.Is(err, ErrDecisionUnsettled) {
			t.Fatalf("%d: %v", i, err)
		}
	}
	// Give the daemon time to receive any frame that was sent late.
	time.Sleep(100 * time.Millisecond)
	if n := f.count(ownerwire.OpDecide); n != 1 {
		t.Fatalf("%d decisions reached the daemon, want exactly 1", n)
	}
}

func TestADecisionAnsweredWithABadAnswerIsInDoubt(t *testing.T) {
	c := flaggedCard(t)
	f, o := decidingFake(t, c, []byte(`{"state":"approved","line":"x","held":true}`))
	var u *Unavailable
	if _, err := o.Decide(decisionOf(c, true)); !errors.As(err, &u) || u.State != StateBadAnswer {
		t.Fatalf("%v", err)
	}
	if _, err := o.Decide(decisionOf(c, true)); !errors.Is(err, ErrDecisionUnsettled) {
		t.Fatalf("%v", err)
	}
	if n := f.count(ownerwire.OpDecide); n != 1 {
		t.Fatalf("%d", n)
	}
}

func TestAnUnauthorizedDecisionIsInDoubtToo(t *testing.T) {
	// Whatever is not a typed refusal of the daemon leaves the fate of the
	// decision open: an internal failure may have happened after it was
	// written.
	c := flaggedCard(t)
	f, o := decidingFake(t, c, nil)
	f.refuse(ownerwire.OpDecide, "internal", "")
	if _, err := o.Decide(decisionOf(c, true)); err == nil || errors.As(err, new(*record.Error)) {
		t.Fatalf("%v", err)
	}
	f.onResult(ownerwire.OpDecide, decideResult(t, record.StateApproved, "approved", false))
	if _, err := o.Decide(decisionOf(c, true)); !errors.Is(err, ErrDecisionUnsettled) {
		t.Fatalf("%v", err)
	}
	if n := f.count(ownerwire.OpDecide); n != 1 {
		t.Fatalf("%d", n)
	}
}

func TestOpenedCardsAreBounded(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	paths := []record.PathChange{testChange("a.go", "M", 1, 1)}
	var first testCard
	for i := 0; i < maxOpenCards+5; i++ {
		c := newTestCard(t, "card-"+strings.Repeat("x", 1)+itoa(i), "small-fix", t0, paths, 0)
		if i == 0 {
			first = c
		}
		f.serveCard(c)
		if _, err := o.Open(c.p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(o.cards) > maxOpenCards || len(o.order) > maxOpenCards {
		t.Fatalf("%d cards kept", len(o.cards))
	}
	// The oldest was let go: it must be opened again before it is decided.
	f.onResult(ownerwire.OpDecide, decideResult(t, record.StateApproved, "x", false))
	if _, err := o.Decide(decisionOf(first, true)); !errors.Is(err, ErrCardNotOpen) {
		t.Fatalf("%v", err)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
