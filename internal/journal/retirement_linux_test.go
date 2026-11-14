package journal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ataniq/internal/dispatch"
)

func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Error("scheduling seam timed out")
	}
}

func awaitResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not finish")
		return nil
	}
}

func settledDenial(t *testing.T, s *Store, i dispatch.Intent) (*Attempt, *fakeSink) {
	t.Helper()
	a := register(t, s, i)
	b := &fakeSink{}
	b.denied.Store(true)
	r, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
	if err != nil || r.State != dispatch.Denied || b.calls.Load() != 1 || b.effects.Load() != 0 {
		t.Fatal("denial setup", r, err)
	}
	return a, b
}

func assertNoReplay(t *testing.T, s *Store, i dispatch.Intent, b *fakeSink, state dispatch.State) {
	t.Helper()
	b.denied.Store(false)
	a := activate(t, s, i)
	r, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
	if err != nil || r.State != state || b.calls.Load() != 1 || b.effects.Load() != 0 {
		t.Fatal("retirement lost evidence or replayed", r, err, b.calls.Load(), b.effects.Load())
	}
}

// Unlike the historical draft Release-before reproduction, this Save was
// admitted BEFORE retirement. The seam completes its failure between the two
// retirement samples, exposing the final source's fenced-before-pending hole.
func TestRetirementAdmittedFailureHandoff(t *testing.T) {
	for _, closeStore := range []bool{false, true} {
		name := "Release"
		if closeStore {
			name = "Close"
		}
		t.Run(name, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			i := testIntent()
			a, b := settledDenial(t, s, i)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			paused, proceed := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(proceed) }) }
			defer unblock()
			saved := make(chan error, 1)
			s.observe = func(point string) {
				pause := point == "save-admitted" && !closeStore || point == "failure-before" && closeStore
				if pause {
					close(paused)
					awaitSignal(t, proceed)
				}
				if point == "retirement-sampled" {
					cancel()
					unblock()
					if err := awaitResult(t, saved); !errors.Is(err, context.Canceled) {
						t.Error("admitted Save did not cancel", err)
					}
				}
			}
			if closeStore {
				// Force enter to return before publication; its lifetime read
				// lock is already gone when Close acquires the write lock.
				s.gate <- struct{}{}
				cancel()
			}
			go func() {
				saved <- a.Save(ctx, dispatch.Receipt{Intent: i, ID: "lost", State: dispatch.Started})
			}()
			awaitSignal(t, paused)
			if closeStore {
				<-s.gate
			}
			var err error
			if closeStore {
				err = s.Close()
			} else {
				err = a.Release(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			s.observe = nil
			if !a.fenced.Load() || a.pending.Load() != 0 {
				t.Fatal("failure did not finish publication")
			}
			if !closeStore {
				r := inspect(t, s, i.ID)
				if r.Active || !r.Uncertain {
					t.Error("lost admitted failure at retirement", r.Active, r.Uncertain)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			s, err = Open(context.Background(), path, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			assertNoReplay(t, s, i, b, dispatch.Uncertain)
		})
	}
}

func TestCloseRejectsNewAdmission(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	a, b := settledDenial(t, s, i)
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(proceed) }) }
	defer unblock()
	s.fault = func(point string) error {
		if point == "close-before" {
			close(entered)
			awaitSignal(t, proceed)
		}
		return nil
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	awaitSignal(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	late := make(chan error, 1)
	go func() { late <- a.Save(ctx, dispatch.Receipt{Intent: i, ID: "late", State: dispatch.Started}) }()
	select {
	case err := <-late:
		if !errors.Is(err, ErrInactive) {
			t.Error("late Save must fail inactive", err)
		}
	case <-time.After(100 * time.Millisecond):
		// Finish Close before waiting for the old implementation's blocked
		// caller. This branch is an assertion failure, not a deadlocked test.
		t.Error("Close did not publish retirement before clearing guards")
		unblock()
		if err := awaitResult(t, late); !errors.Is(err, ErrInactive) {
			t.Error("late Save must fail inactive", err)
		}
	}
	unblock()
	if err := awaitResult(t, closed); err != nil {
		t.Fatal(err)
	}
	s.fault = nil
	if a.fenced.Load() || a.pending.Load() != 0 {
		t.Error("new closed call mutated old activation")
	}
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertNoReplay(t, s, i, b, dispatch.Denied)
}

func TestForeignCanceledCallsIsolation(t *testing.T) {
	for _, method := range []string{"Save", "Prepare"} {
		for _, blocked := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/canceled", true: "/blocked"}[blocked], func(t *testing.T) {
				path, o := fixture(t)
				s := mustCreate(t, path, o)
				i := testIntent()
				a := register(t, s, i)
				foreign := cloneIntent(i)
				foreign.Payload[0]++
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				if blocked {
					s.gate <- struct{}{}
				} else {
					cancel()
				}
				if method == "Save" {
					if err := a.Save(ctx, dispatch.Receipt{Intent: foreign, ID: "foreign", State: dispatch.Started}); !errors.Is(err, ErrConflict) {
						t.Error("foreign Save", err)
					}
				} else {
					st, _, err := a.Prepare(ctx, foreign)
					if st != dispatch.PrepareConflict || err != nil {
						t.Error("foreign Prepare", st, err)
					}
				}
				if blocked {
					<-s.gate
				}
				if a.fenced.Load() || a.pending.Load() != 0 {
					t.Error("foreign call poisoned legitimate activation")
				}
				b := &fakeSink{}
				r, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
				if err != nil || r.State != dispatch.Started || b.calls.Load() != 1 || b.effects.Load() != 1 {
					t.Fatal("legitimate dispatch was poisoned", r, err, b.effects.Load())
				}
			})
		}
	}
}

func TestSnapshotCoherentFailureOverlay(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i, j := testIntent(), testIntent()
	i.ID, j.ID = "a", "b"
	a, _ := settledDenial(t, s, i)
	b, _ := settledDenial(t, s, j)
	var once sync.Once
	s.observe = func(point string) {
		if point != "snapshot-record" {
			return
		}
		once.Do(func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			// A always fails before B: (false,true) never exists.
			for _, a := range []*Attempt{a, b} {
				err := a.Save(ctx, dispatch.Receipt{Intent: a.intent, ID: "lost", State: dispatch.Started})
				if !errors.Is(err, context.Canceled) {
					t.Error("waiting Save", err)
				}
			}
		})
	}
	snap, err := s.Snapshot(context.Background())
	s.observe = nil
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Records) != 2 || snap.Watermark != 6 || len(snap.Events) != 6 {
		t.Fatal("durable transaction torn", snap)
	}
	if !snap.Records[0].Uncertain && snap.Records[1].Uncertain {
		t.Fatal("incoherent overlay: observed A=false, B=true although A failed before B")
	}
	for _, r := range snap.Records {
		if (r.Current.State == dispatch.Uncertain) != r.Uncertain || len(r.Receipts) != 1 || r.Receipts[0].State != dispatch.Denied {
			t.Fatal("projection changed original evidence", r)
		}
	}
	fresh, err := s.Snapshot(context.Background())
	if err != nil || !fresh.Records[0].Uncertain || !fresh.Records[1].Uncertain {
		t.Fatal("subsequent snapshot lost failure overlay", err)
	}
}

// This is the accepted final contract for the old draft Release-before
// interleaving: new calls are inactive, with no timeout and no new failure.
func TestRetiredCallsDoNotMutateActivations(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	a, b := settledDenial(t, s, i)
	s.fault = func(point string) error {
		if point == "release-before" {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, _, err := a.Prepare(ctx, i); !errors.Is(err, ErrInactive) {
				t.Error("retiring Prepare", err)
			}
			if err := a.Save(ctx, dispatch.Receipt{Intent: i, ID: "late", State: dispatch.Started}); !errors.Is(err, ErrInactive) {
				t.Error("retiring Save", err)
			}
			if err := a.Revoke(ctx); !errors.Is(err, ErrInactive) {
				t.Error("retiring Revoke", err)
			}
			// Release itself is not pending and the first Release owns the
			// gate. A cancelled competing Release cannot publish a late fence.
			if err := a.Release(ctx); !errors.Is(err, ErrInactive) {
				t.Error("competing Release", err)
			}
		}
		return nil
	}
	if err := a.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.fault = nil
	newer := activate(t, s, i)
	s.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, prepareErr := a.Prepare(ctx, i)
	saveErr := a.Save(ctx, dispatch.Receipt{Intent: i, ID: "stale", State: dispatch.Started})
	revokeErr, releaseErr := a.Revoke(ctx), a.Release(ctx)
	<-s.gate
	for _, err := range []error{prepareErr, saveErr, revokeErr, releaseErr} {
		if !errors.Is(err, ErrInactive) {
			t.Error("stale call did not fail inactive", err)
		}
	}
	if a.fenced.Load() || newer.fenced.Load() || a.pending.Load() != 0 || newer.pending.Load() != 0 {
		t.Fatal("stale activation mutated")
	}
	b.denied.Store(false)
	r, err := (dispatch.Coordinator{Journal: newer, Boundary: b}).Dispatch(context.Background(), i)
	if err != nil || r.State != dispatch.Denied || b.calls.Load() != 1 || b.effects.Load() != 0 {
		t.Fatal("stale call changed newer activation", r, err)
	}
}

func TestRetirementFailureAndCancellation(t *testing.T) {
	for _, point := range []string{"release-before", "release-after", "close-before", "close-after"} {
		for _, canceled := range []bool{false, true} {
			name := point + map[bool]string{false: "/response-failure", true: "/canceled"}[canceled]
			t.Run(name, func(t *testing.T) {
				path, o := fixture(t)
				s, err := Create(context.Background(), path, o)
				if err != nil {
					t.Fatal(err)
				}
				original := s
				t.Cleanup(func() { original.Close() }) // expected Close failures are checked below
				i := testIntent()
				a, b := settledDenial(t, s, i)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				sentinel := errors.New("lost retirement response")
				s.fault = func(p string) error {
					if p != point {
						return nil
					}
					if canceled {
						cancel()
						return ctx.Err()
					}
					return sentinel
				}
				isClose := point == "close-before" || point == "close-after"
				if isClose {
					// Close has its own bounded context; this injects a
					// cancellation result, not caller cancellation of Close.
					err = s.Close()
				} else {
					err = a.Release(ctx)
				}
				wantErr := sentinel
				if canceled {
					wantErr = context.Canceled
				}
				if !errors.Is(err, wantErr) {
					t.Fatal("retirement acknowledged failure", err)
				}
				if !a.retiring.Load() {
					t.Fatal("failed retirement left admission open")
				}
				s.fault = nil
				wantState := dispatch.Uncertain
				if isClose {
					if point == "close-after" {
						wantState = dispatch.Denied
					}
				} else {
					r := inspect(t, s, i.ID)
					if r.Active != (point == "release-before") || !r.Uncertain {
						t.Fatal("lost guard or volatile response uncertainty", r)
					}
					// Retry may conservatively persist response uncertainty.
					err := a.Release(context.Background())
					if point == "release-after" && !errors.Is(err, ErrInactive) || point == "release-before" && err != nil {
						t.Fatal("bounded retirement retry", err)
					}
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
				}
				s, err = Open(context.Background(), path, o)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				assertNoReplay(t, s, i, b, wantState)
			})
		}
	}
}

func TestReleaseCancellationDuringClose(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	a, b := settledDenial(t, s, i)
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(proceed) }) }
	defer unblock()
	s.fault = func(point string) error {
		if point == "close-before" {
			close(entered)
			awaitSignal(t, proceed)
		}
		return nil
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	awaitSignal(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	released := make(chan error, 1)
	go func() { released <- a.Release(ctx) }()
	unblock()
	if err := awaitResult(t, closed); err != nil {
		t.Fatal(err)
	}
	if err := awaitResult(t, released); !errors.Is(err, ErrInactive) {
		t.Fatal("Release after closure", err)
	}
	s.fault = nil
	if a.fenced.Load() {
		t.Fatal("Release entry failure mutated retired activation")
	}
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertNoReplay(t, s, i, b, dispatch.Denied)
}
