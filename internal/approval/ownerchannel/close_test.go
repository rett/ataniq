package ownerchannel

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// settleGoroutines waits for the goroutines of finished work to end.
func settleGoroutines(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > want {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			n := runtime.Stack(buf, true)
			t.Fatalf("%d goroutines, want at most %d after Close:\n%s", runtime.NumGoroutine(), want, buf[:n])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNoGoroutineIsLeftBehindAfterClose(t *testing.T) {
	// Let goroutines of earlier tests end first.
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	if _, err := e.decide(p, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.restart()
	e.stop()
	settleGoroutines(t, before)
}

func TestCloseCancelsAndWaitsForCallsUnderWay(t *testing.T) {
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	e := newEnv(t)
	e.start()
	inside := make(chan struct{})
	e.verif.onVerify = func(ctx context.Context, in VerifyInput) {
		close(inside)
		<-ctx.Done()
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.Submit(context.Background(), e.collected(e.request("feature-a", nil)))
		done <- err
	}()
	<-inside
	if err := e.svc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("a request stopped by Close was returned as an error: %v", err)
	}
	// The stopped request was ended: no pin is left, and the lead was told
	// and the request counted, as for any refusal.
	if n := e.merger.pinCount(); n != 0 {
		t.Errorf("%d pins left", n)
	}
	if got, _ := e.svc.Status("rq-test-01"); got.State != RequestRefused || got.Code != record.CodeRequestInvalid {
		t.Errorf("the request's status is %+v", got)
	}
	replies := e.mail.repliesFor("rq-test-01")
	if len(replies) != 1 || replies[0].Code != record.CodeRequestInvalid || replies[0].Line == "" {
		t.Errorf("the lead was told %+v, want one reply", replies)
	}
	if _, err := e.svc.Submit(context.Background(), e.collected(e.request("feature-b", nil))); !errors.Is(err, ErrClosed) {
		t.Errorf("a closed service answered: %v", err)
	}
	if _, err := e.svc.List(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("a closed service answered: %v", err)
	}
	if err := e.svc.Close(); err != nil {
		t.Errorf("a second Close returned %v", err)
	}
	settleGoroutines(t, before)
	// The count was journaled before the journal was closed.
	e.start()
	if n := len(e.entries(ownerjournal.KindCount)); n != 1 {
		t.Errorf("%d count entries, want one for the stopped request", n)
	}
}

// Close stops the requests that wait for their turn as well, answers and
// counts each, and leaves no pin.
func TestCloseRefusesTheRequestsThatWaitForTheSlot(t *testing.T) {
	e := newEnv(t)
	admitted := make(chan struct{}, 2)
	e.at["admitted"] = func() { admitted <- struct{}{} }
	e.start()
	inside := make(chan struct{})
	e.verif.onVerify = func(ctx context.Context, in VerifyInput) {
		close(inside)
		<-ctx.Done()
	}
	done := make(chan RequestStatus, 2)
	submit := func(name string) {
		st, err := e.svc.Submit(context.Background(), e.collected(e.request(name, nil)))
		if err != nil {
			t.Errorf("Submit %s: %v", name, err)
		}
		done <- st
	}
	go submit("feature-a")
	<-inside
	go submit("feature-b")
	// The second request is admitted, and waits for the slot.
	<-admitted
	<-admitted
	time.Sleep(20 * time.Millisecond)
	if err := e.svc.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if st := <-done; st.State != RequestRefused || st.Code != record.CodeRequestInvalid {
			t.Errorf("a request stopped by Close ended as %+v", st)
		}
	}
	for _, id := range []string{"rq-test-01", "rq-test-02"} {
		if replies := e.mail.repliesFor(id); len(replies) != 1 {
			t.Errorf("%s: the lead was told %+v", id, replies)
		}
	}
	if n := e.merger.pinCount(); n != 0 {
		t.Errorf("%d pins left", n)
	}
	e.start()
	if n := len(e.entries(ownerjournal.KindCount)); n != 2 {
		t.Errorf("%d count entries, want one for each stopped request", n)
	}
	if entries, _ := e.door.List(context.Background()); len(entries) != 0 {
		t.Errorf("%d pending records", len(entries))
	}
}

// Close does not return while a call is under way, even one that does not
// give way to its cancelled context at once.
func TestCloseWaitsForACallUnderWay(t *testing.T) {
	e := newEnv(t)
	e.start()
	inside := make(chan struct{})
	release := make(chan struct{})
	e.verif.onVerify = func(ctx context.Context, in VerifyInput) {
		close(inside)
		<-release
	}
	submitted := make(chan struct{})
	go func() {
		defer close(submitted)
		_, _ = e.svc.Submit(context.Background(), e.collected(e.request("feature-a", nil)))
	}()
	<-inside
	closed := make(chan error, 1)
	go func() { closed <- e.svc.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while a call was under way", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return once the call ended")
	}
	<-submitted
}

// Run with -race: reads and decisions at once.
func TestConcurrentCallsAreSafe(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	var cards []record.Pending
	for i := 0; i < 4; i++ {
		cards = append(cards, e.card("feature-"+string(rune('a'+i))))
		e.advance(time.Minute)
	}
	e.beat()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if _, err := e.svc.List(ctx); err != nil {
					t.Error(err)
				}
				if _, err := e.svc.Show(ctx, cards[0].ID); err != nil {
					t.Error(err)
				}
				if err := e.svc.Beat(src); err != nil {
					t.Error(err)
				}
				if err := e.svc.Tick(ctx); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	// Two decisions at once for one card: one of them decides it.
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = e.svc.Decide(ctx, src, frame(cards[0], true))
		}(i)
	}
	for i := 1; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := e.svc.Decide(ctx, src, frame(cards[i], false)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		} else if _, isRefusal := record.CodeOf(err); !isRefusal {
			t.Errorf("a decision failed with %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d of two decisions for one card went through", ok)
	}
	if calls, moves := e.intake.appliesSeen(); calls != 1 || moves != 1 {
		t.Errorf("applied %d times, moved %d", calls, moves)
	}
}

func TestTheServiceNeedsItsParts(t *testing.T) {
	e := newEnv(t)
	for name, mod := range map[string]func(c *Config){
		"no journal":                   func(c *Config) { c.Journal = nil },
		"no door":                      func(c *Config) { c.Door = nil },
		"no merger":                    func(c *Config) { c.Merger = nil },
		"no intake":                    func(c *Config) { c.Intake = nil },
		"no verifier":                  func(c *Config) { c.Verifier = nil },
		"no mailbox":                   func(c *Config) { c.Mailbox = nil },
		"no clock":                     func(c *Config) { c.Now = nil },
		"a window under a minute":      func(c *Config) { c.PresenceWindow = time.Second },
		"an approval life of a second": func(c *Config) { c.ApprovalLife = time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			c := e.config(nil)
			mod(&c)
			if s, err := New(context.Background(), c); err == nil {
				_ = s.Close()
				t.Fatal("New accepted it")
			}
		})
	}
}

var _ = ownerwire.MaxRecent
