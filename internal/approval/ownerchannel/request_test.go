package ownerchannel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
)

func TestARequestBecomesAPendingRecord(t *testing.T) {
	e := newEnv(t)
	e.start()
	var during RequestStatus
	e.verif.onVerify = func(context.Context, VerifyInput) {
		during, _ = e.svc.Status("rq-test-01")
	}

	st := e.submit("feature-a")
	if st.State != RequestPending || st.PendingID == "" {
		t.Fatalf("the status is %+v, want a pending record", st)
	}
	if during.State != RequestVerifying {
		t.Errorf("while the suite ran the request was %q, want verifying", during.State)
	}
	if got, _ := e.svc.Status("rq-test-01"); got.State != RequestPending {
		t.Errorf("afterwards the request is %q", got.State)
	}
	p := e.pending(st.PendingID)
	if p.Binding.Merge == nil || p.Binding.Merge.Commit != mergeID || p.Binding.Effects[0].Old != baseID {
		t.Errorf("the pending record does not bind the host's merge: %+v", p.Binding)
	}
	if len(p.Binding.Evidence) != 1 || p.Binding.Evidence[0].Kind != record.EvidenceSuite {
		t.Errorf("the pending record has no suite evidence: %+v", p.Binding.Evidence)
	}
	if e.state(st.PendingID) != record.StatePending {
		t.Errorf("the door's state is %s", e.state(st.PendingID))
	}
	if n := e.merger.pinCount(); n != 1 {
		t.Errorf("%d pins, want the merge pinned until the card ends", n)
	}
	if got := e.mail.all(); len(got) != 0 {
		t.Errorf("the lead got %d replies before any decision: %v", len(got), got)
	}
	in := e.merger.inputs[0]
	if in.Feature != "feature-a" || in.Target != "develop" || in.RequestID != "rq-test-01" || in.TargetTip != baseID || in.FeatureTip != featureID {
		t.Errorf("the merge was built from %+v", in)
	}
	if in.Key == "rq-test-01" || len(in.Key) != 32 {
		t.Errorf("the pin's key is %q: it must be host-made, not the request id", in.Key)
	}
}

func TestARequestIsRefusedWithItsCodeAndAReply(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *env)
		req   func(r *record.Request)
		want  record.Code
		// pins is how many pins may remain.
	}{
		{"the door refuses the request", func(e *env) {}, func(r *record.Request) { r.Epoch = 6 }, record.CodePreviousEpoch},
		{"the feature moved", func(e *env) { e.intake.featureTip = "5555555555555555555555555555555555555555" }, nil, record.CodeFeatureMoved},
		{"the merge conflicts", func(e *env) {
			e.merger.buildErr = &record.Error{Code: record.CodeMergeConflict, Detail: "the feature does not merge"}
		}, nil, record.CodeMergeConflict},
		{"the target already holds the feature", func(e *env) { e.merger.buildErr = ErrNothingToMerge }, nil, record.CodeRequestInvalid},
		{"the merge cannot be built", func(e *env) { e.merger.buildErr = errors.New("git is gone") }, nil, record.CodeRequestInvalid},
		{"the pin is unclear and made", func(e *env) { e.merger.unclear, e.merger.pinAnyway = true, true }, nil, record.CodeRequestInvalid},
		{"the pin is unclear and not made", func(e *env) { e.merger.unclear = true }, nil, record.CodeRequestInvalid},
		{"the suite failed", func(e *env) { e.verif.result = Verification{Detail: VerifyFailed} }, nil, record.CodeVerificationFailed},
		{"the run was stopped", func(e *env) { e.verif.result = Verification{Detail: VerifyStopped} }, nil, record.CodeVerificationFailed},
		{"the output was cut", func(e *env) { e.verif.result = Verification{Detail: VerifyOutputCut} }, nil, record.CodeVerificationFailed},
		{"the run could not start", func(e *env) { e.verif.err = errors.New("no sandbox") }, nil, record.CodeVerificationFailed},
		{"a pass with no suite record is not a pass", func(e *env) {
			e.verif.result = Verification{Clean: true}
		}, nil, record.CodeVerificationFailed},
		{"the review cannot be made", func(e *env) { e.intake.reviewErr = errors.New("no mirror") }, nil, record.CodeRequestInvalid},
		{"the feature cannot be resolved", func(e *env) { e.intake.resolveErr = errors.New("no mirror") }, nil, record.CodeRequestInvalid},
		{"a prune is not handled", func(e *env) {}, func(r *record.Request) {
			r.Verb, r.Argv, r.FeatureSHA = record.VerbBranchPrune, []string{}, ""
		}, record.CodeVerbNotRequestable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.start()
			c.setup(e)
			b := e.request("feature-a", c.req)
			st, err := e.svc.Submit(context.Background(), e.collected(b))
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if st.State != RequestRefused || st.Code != c.want {
				t.Fatalf("the status is %+v, want refused %s", st, c.want)
			}
			if st.Detail == "" {
				t.Error("the refusal has no detail")
			}
			replies := e.mail.repliesFor("rq-test-01")
			if len(replies) != 1 || replies[0].Code != c.want || replies[0].PendingID != "" || replies[0].Line == "" {
				t.Errorf("the lead was told %+v, want one reply with %s", replies, c.want)
			}
			if got, _ := e.svc.Status("rq-test-01"); got != st {
				t.Errorf("the kept status is %+v, want %+v", got, st)
			}
			if entries, _ := e.door.List(context.Background()); len(entries) != 0 {
				t.Errorf("the door has %d pending records after a refusal", len(entries))
			}
			if n := e.merger.pinCount(); n != 0 {
				t.Errorf("%d pins remain after a refusal", n)
			}
			counts := e.entries(ownerjournal.KindCount)
			if len(counts) != 1 {
				t.Fatalf("%d count entries, want one for the refused request", len(counts))
			}
			// The same feature may be asked for again: the refused request no
			// longer stands at the door (when it was admitted at all).
			if c.want != record.CodePreviousEpoch {
				if again := e.submitRefusedOnce("feature-a"); again.Code == record.CodeRequestDuplicate {
					t.Error("the refused request still stands at the door: a new one is a duplicate")
				}
			}
		})
	}
}

// submitRefusedOnce asks again and returns the status, whatever it is.
func (e *env) submitRefusedOnce(name string) RequestStatus {
	e.t.Helper()
	st, err := e.svc.Submit(context.Background(), e.collected(e.request(name, nil)))
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

// OC31: anything but a clean pass makes no card, and the pending list stays
// empty.
func TestOC31NoCleanPassNoCard(t *testing.T) {
	e := newEnv(t)
	e.start()
	for i, v := range []Verification{{Detail: VerifyFailed}, {Detail: VerifyStopped}, {Detail: VerifyOutputCut}, {Detail: "something else"}} {
		e.verif.result = v
		st := e.submit("feature-" + string(rune('a'+i)))
		if st.State != RequestRefused || st.Code != record.CodeVerificationFailed {
			t.Fatalf("%+v gave %+v", v, st)
		}
		if st.Detail == "" || len(st.Detail) > 200 {
			t.Errorf("the detail is %q", st.Detail)
		}
		e.advance(time.Minute)
	}
	// An unknown word is read as could-not-run.
	if got, _ := e.svc.Status("rq-test-04"); got.Detail != "the suite did not pass cleanly on the merge: could-not-run" {
		t.Errorf("an unknown word gave the detail %q", got.Detail)
	}
}

// OC14: one verification at a time, however many requests wait.
func TestOC14OneVerificationAtATime(t *testing.T) {
	e := newEnv(t)
	e.start()
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	e.verif.onVerify = func(ctx context.Context, in VerifyInput) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	const n = 5
	var wg sync.WaitGroup
	results := make([]RequestStatus, n)
	requests := make([][]byte, n)
	for i := range requests {
		requests[i] = e.requestAt("feature-"+string(rune('a'+i)), i)
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "feature-" + string(rune('a'+i))
			st, err := e.svc.Submit(context.Background(), Collected{
				Project: "project-a", Request: requests[i], At: start,
			})
			if err != nil {
				t.Errorf("Submit %s: %v", name, err)
			}
			results[i] = st
		}(i)
	}
	<-entered
	// One request is verifying. Give the others every chance to run too.
	time.Sleep(50 * time.Millisecond)
	verifying, collected := 0, 0
	for i := 0; i < n; i++ {
		st, ok := e.svc.Status("rq-flood-" + string(rune('a'+i)))
		if !ok {
			continue
		}
		switch st.State {
		case RequestVerifying:
			verifying++
		case RequestCollected:
			collected++
		}
	}
	if verifying != 1 || collected != n-1 {
		t.Errorf("%d verifying and %d collected, want 1 and %d", verifying, collected, n-1)
	}
	for i := 0; i < n; i++ {
		release <- struct{}{}
		if i < n-1 {
			<-entered
		}
	}
	wg.Wait()
	if e.verif.most != 1 {
		t.Errorf("%d verifications ran at once", e.verif.most)
	}
	if e.verif.runs != n {
		t.Errorf("%d verifications ran, want %d", e.verif.runs, n)
	}
	for i, st := range results {
		if st.State != RequestPending {
			t.Errorf("request %d ended %+v", i, st)
		}
	}
}

// requestAt makes a request with a fixed id for the flood test.
func (e *env) requestAt(name string, i int) []byte {
	e.t.Helper()
	return e.request(name, func(r *record.Request) { r.RequestID = "rq-flood-" + string(rune('a'+i)) })
}

func TestACancelledRequestWaitingForItsTurnIsEnded(t *testing.T) {
	e := newEnv(t)
	e.start()
	entered := make(chan struct{})
	release := make(chan struct{})
	e.verif.onVerify = func(ctx context.Context, in VerifyInput) {
		close(entered)
		<-release
	}
	done := make(chan RequestStatus, 1)
	first, second := e.collected(e.requestAt("feature-a", 0)), e.collected(e.requestAt("feature-b", 1))
	go func() {
		st, _ := e.svc.Submit(context.Background(), first)
		done <- st
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := e.svc.Submit(ctx, second)
		errc <- err
	}()
	// The second request is admitted, then waits for the slot.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, ok := e.svc.Status("rq-flood-b"); ok && st.State == RequestCollected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second request never reached the queue")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled request returned %v", err)
	}
	close(release)
	e.verif.mu.Lock()
	e.verif.onVerify = nil
	e.verif.mu.Unlock()
	if st := <-done; st.State != RequestPending {
		t.Fatalf("the first request ended %+v", st)
	}
	// The cancelled request was ended at the door: asking again is not a
	// duplicate.
	if st := e.submitRefusedOnce("feature-b"); st.State != RequestPending {
		t.Errorf("asking again gave %+v", st)
	}
}

// The count from the first card is journaled for every request, the
// refused ones too.
func TestTheCountIsJournaledForEveryRequest(t *testing.T) {
	e := newEnv(t)
	e.start()

	// A small change with no flagged path would meet the terms.
	e.intake.summary.Flagged = []record.Flag{}
	e.intake.summary.Binary, e.intake.summary.Symlinks = []record.Text{}, []record.Text{}
	e.intake.summary.ModeChanges, e.intake.summary.Submodules = []record.Text{}, []record.Text{}
	small := e.card("feature-a")
	e.advance(time.Minute)
	// The vectors' change has a flagged path: it would not.
	e.intake.summary = *vectorPending(t).Binding.Summary
	flagged := e.card("feature-b")
	e.advance(time.Minute)
	e.verif.result = Verification{Detail: VerifyFailed}
	if st := e.submit("feature-c"); st.State != RequestRefused {
		t.Fatalf("%+v", st)
	}

	counts, err := e.svc.journal().Entries(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []ownerjournal.Entry
	for _, en := range counts {
		if en.Kind == ownerjournal.KindCount {
			got = append(got, en)
		}
	}
	if len(got) != 3 {
		t.Fatalf("%d count entries for 3 requests", len(got))
	}
	if got[0].PendingID != small.ID || got[1].PendingID != flagged.ID || got[2].PendingID != "" {
		t.Errorf("the counts name %q, %q, %q", got[0].PendingID, got[1].PendingID, got[2].PendingID)
	}
	l, err := e.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if l.Count.Requests != 3 || l.Count.WouldMeet != 1 {
		t.Errorf("the count is %+v, want 3 requests of which 1 would have met the terms", l.Count)
	}
}

func TestARequestTheDoorCannotBeReachedForIsNotACard(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.door.Door = unreachable{e.door.Door}
	_, err := e.svc.Submit(context.Background(), e.collected(e.request("feature-a", nil)))
	if !errors.Is(err, ErrDoorUnavailable) {
		t.Fatalf("Submit returned %v, want ErrDoorUnavailable", err)
	}
	if got := e.mail.all(); len(got) != 0 {
		t.Errorf("a failure of the door was answered as a refusal: %v", got)
	}
}

// unreachable is a door whose Admit cannot be reached.
type unreachable struct{ door.Door }

func (unreachable) Admit(context.Context, door.Record) (door.Admission, error) {
	return door.Admission{}, errors.New("connection refused")
}

func TestAnUndecodableRequestIsRefusedByItsForm(t *testing.T) {
	e := newEnv(t)
	e.start()
	st, err := e.svc.Submit(context.Background(), Collected{Project: "project-a", Request: []byte(`{"schema":1}`), At: e.clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != RequestRefused || st.Code != record.CodeRequestInvalid {
		t.Errorf("%+v", st)
	}
	if len(e.mail.all()) != 0 {
		t.Error("a request with no readable id was answered")
	}
}
