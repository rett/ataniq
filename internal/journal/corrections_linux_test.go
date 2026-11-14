package journal

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"ataniq/internal/dispatch"
	"modernc.org/sqlite"
)

func TestPreparedObservationPreservesBlobOutcome(t *testing.T) {
	for _, state := range []dispatch.State{dispatch.Started, dispatch.Denied} {
		for _, detail := range []string{"", "\x00\xffexact\x00"} {
			t.Run(string(state)+"/"+map[bool]string{true: "empty", false: "binary"}[detail == ""], func(t *testing.T) {
				ctx := context.Background()
				path, o := fixture(t)
				s := mustCreate(t, path, o)
				i := testIntent()
				a := register(t, s, i)
				if st, _, err := a.Prepare(ctx, i); st != dispatch.PrepareNew || err != nil {
					t.Fatal(st, err)
				}
				outcome := dispatch.Receipt{Intent: i, ID: "outcome\x00\xff", State: state, Evidence: detail}
				observation := dispatch.Receipt{Intent: i, ID: "observation", State: dispatch.Prepared}
				for _, receipt := range []dispatch.Receipt{outcome, observation, observation} {
					if err := a.Save(ctx, receipt); err != nil {
						t.Fatal("save exact receipt", err)
					}
				}
				wantReceipts := []dispatch.Receipt{outcome, observation}
				r := inspect(t, s, i.ID)
				if r.Uncertain || !reflect.DeepEqual(r.Current, outcome) || !reflect.DeepEqual(r.Receipts, wantReceipts) {
					t.Fatal("observation changed projection or receipts", r)
				}
				b := &fakeSink{}
				got, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(ctx, i)
				if err != nil || !reflect.DeepEqual(got, outcome) || b.calls.Load() != 0 || b.effects.Load() != 0 {
					t.Fatal("duplicate replay or changed outcome", got, err)
				}
				if err := a.Release(ctx); err != nil {
					t.Fatal(err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(ctx, path, o)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				r = inspect(t, s, i.ID)
				want := outcome
				if state == dispatch.Started {
					want.State = dispatch.Uncertain
				}
				if r.Active || r.Uncertain != (state == dispatch.Started) || !reflect.DeepEqual(r.Current, want) || !reflect.DeepEqual(r.Receipts, wantReceipts) {
					t.Fatal("reopen changed original evidence or retirement projection", r)
				}
				got, err = (dispatch.Coordinator{Journal: activate(t, s, i), Boundary: b}).Dispatch(ctx, i)
				if err != nil || !reflect.DeepEqual(got, want) || b.calls.Load() != 0 || b.effects.Load() != 0 {
					t.Fatal("reopen replay", got, err)
				}
			})
		}
	}
}

func TestReleaseResponseRetryExactGeneration(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "settled", true: "unknown-save"}[unknown], func(t *testing.T) {
			ctx := context.Background()
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			i := testIntent()
			a, b := settledDenial(t, s, i)
			original := inspect(t, s, i.ID).Receipts
			if unknown {
				s.fault = failAt("save-before")
				if err := a.Save(ctx, dispatch.Receipt{Intent: i, ID: "unknown", State: dispatch.Started}); err == nil {
					t.Fatal("missing Save fault")
				}
			}
			s.fault = failAt("release-after")
			if err := a.Release(ctx); err == nil {
				t.Fatal("missing Release fault")
			}
			s.fault = nil
			before, err := s.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if before.Records[0].Active || !before.Records[0].Uncertain {
				t.Fatal("missing response failure overlay")
			}
			results := make(chan error, 8)
			var wg sync.WaitGroup
			for n := 0; n < cap(results); n++ {
				wg.Add(1)
				go func() { defer wg.Done(); results <- a.Release(ctx) }()
			}
			wg.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				} else if !errors.Is(err, ErrInactive) {
					t.Fatal(err)
				}
			}
			if successes != 1 {
				t.Fatalf("proved retirement retries succeeded %d times", successes)
			}
			after, err := s.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Events, after.Events) || !reflect.DeepEqual(after.Records[0].Receipts, original) || after.Records[0].Uncertain != unknown {
				t.Fatal("retry changed durable evidence or lost durable uncertainty", after)
			}
			fresh := activate(t, s, i)
			if bytes.Equal(fresh.generation, a.generation) {
				t.Fatal("generation reused")
			}
			if err := a.Release(ctx); !errors.Is(err, ErrInactive) {
				t.Fatal("stale Release", err)
			}
			if err := a.Save(ctx, dispatch.Receipt{Intent: i, ID: "stale", State: dispatch.Started}); !errors.Is(err, ErrInactive) {
				t.Fatal(err)
			}
			want := dispatch.Denied
			if unknown {
				want = dispatch.Uncertain
			}
			got, err := (dispatch.Coordinator{Journal: fresh, Boundary: b}).Dispatch(ctx, i)
			if err != nil || got.State != want || b.calls.Load() != 1 || b.effects.Load() != 0 || fresh.fenced.Load() {
				t.Fatal("new activation isolation", got, err)
			}
			late := dispatch.Receipt{Intent: i, ID: "late\x00\xff", State: dispatch.Started, Evidence: "\xff\x00late"}
			if err := fresh.Save(ctx, late); err != nil {
				t.Fatal(err)
			}
			r := inspect(t, s, i.ID)
			if !r.Active || !r.Uncertain || !reflect.DeepEqual(r.Receipts, append(original, late)) {
				t.Fatal("lost contradiction or fresh guard", r)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(ctx, path, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			assertNoReplay(t, s, i, b, dispatch.Uncertain)
			if r := inspect(t, s, i.ID); !reflect.DeepEqual(r.Receipts, append(original, late)) {
				t.Fatal("reopen lost evidence", r)
			}
		})
	}
}

func TestGrantConflictCommitStatusAndEvidence(t *testing.T) {
	for _, point := range []string{"", "conflict-before", "conflict-after"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			i := testIntent()
			i.Grant = "grant\x00\xff"
			first := register(t, s, i)
			if st, _, err := first.Prepare(ctx, i); st != dispatch.PrepareNew || err != nil {
				t.Fatal(st, err)
			}
			j := cloneIntent(i)
			j.ID, j.Attempt, j.Payload = "other", "other", []byte{255, 0, 3}
			a := register(t, s, j)
			if point != "" {
				s.fault = failAt(point)
			}
			st, _, err := a.Prepare(ctx, j)
			if point == "" {
				if st != dispatch.PrepareConflict || err != nil {
					t.Fatal(st, err)
				}
			} else if st != dispatch.PrepareUnknown || err == nil {
				t.Error("failed conflict must be unknown", st, err)
			}
			s.fault = nil
			check := func() {
				t.Helper()
				snap, err := s.Snapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(snap.ConsumedGrants, []dispatch.Intent{i}) {
					t.Fatal("changed consumed grant", snap.ConsumedGrants)
				}
				if !reflect.DeepEqual(inspect(t, s, j.ID).Intent, j) {
					t.Fatal("changed conflicting binding")
				}
				binding, _ := encodeIntent(i)
				count := 0
				for _, e := range snap.Events {
					if e.Kind == "grant-conflict" {
						count++
						if e.IntentID != j.ID || !bytes.Equal(e.Detail, binding) {
							t.Fatal("changed conflict evidence", e)
						}
					}
				}
				want := 1
				if point == "conflict-before" {
					want = 0
				}
				if count != want {
					t.Fatal("conflict event count", count, want)
				}
			}
			check()
			b := &fakeSink{}
			if point != "" {
				r, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(ctx, j)
				if err != nil || r.State != dispatch.Uncertain {
					t.Fatal(r, err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(ctx, path, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			check()
			for _, in := range []dispatch.Intent{i, j} {
				r, err := (dispatch.Coordinator{Journal: activate(t, s, in), Boundary: b}).Dispatch(ctx, in)
				if err != nil || r.State != dispatch.Uncertain {
					t.Fatal(r, err)
				}
			}
			if b.calls.Load() != 0 || b.effects.Load() != 0 {
				t.Fatal("conflict replayed")
			}
		})
	}
}

func TestReleaseRetryPreservesUnknownOperations(t *testing.T) {
	for _, operation := range []string{"prepare", "save", "revoke"} {
		for _, timing := range []string{"before", "after"} {
			t.Run(operation+"-"+timing, func(t *testing.T) {
				ctx := context.Background()
				path, o := fixture(t)
				s := mustCreate(t, path, o)
				i := testIntent()
				i.Grant = "one-use"
				a := register(t, s, i)
				if operation != "prepare" {
					if st, _, err := a.Prepare(ctx, i); st != dispatch.PrepareNew || err != nil {
						t.Fatal(st, err)
					}
				}
				s.fault = failAt(operation + "-" + timing)
				var err error
				switch operation {
				case "prepare":
					_, _, err = a.Prepare(ctx, i)
				case "save":
					err = a.Save(ctx, dispatch.Receipt{Intent: i, ID: "unknown", State: dispatch.Started, Evidence: "exact\x00\xff"})
				case "revoke":
					err = a.Revoke(ctx)
				}
				if err == nil {
					t.Fatal("missing injected operation failure")
				}
				s.fault = nil
				if err := a.Save(ctx, dispatch.Receipt{Intent: i, ID: "denial", State: dispatch.Denied}); err != nil {
					t.Fatal(err)
				}
				s.fault = failAt("release-after")
				if err := a.Release(ctx); err == nil {
					t.Fatal("missing retirement response fault")
				}
				s.fault = nil
				before, err := s.Snapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := a.Release(ctx); err != nil {
					t.Fatal("retry", err)
				}
				after, err := s.Snapshot(ctx)
				if err != nil || !reflect.DeepEqual(before, after) || !after.Records[0].Uncertain || after.Records[0].Active {
					t.Fatal("reconciliation changed unknown outcome", err)
				}
				b := &fakeSink{}
				for n := 0; n < 2; n++ {
					r, err := (dispatch.Coordinator{Journal: activate(t, s, i), Boundary: b}).Dispatch(ctx, i)
					if err != nil || r.State != dispatch.Uncertain || b.calls.Load() != 0 || b.effects.Load() != 0 {
						t.Fatal("unknown operation replayed", r, err)
					}
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					if n == 0 {
						s, err = Open(ctx, path, o)
						if err != nil {
							t.Fatal(err)
						}
						defer s.Close()
					}
				}
			})
		}
	}
}

func TestReleaseRetryRequiresExactInactiveProof(t *testing.T) {
	for _, mismatch := range []string{"binding", "generation"} {
		t.Run(mismatch, func(t *testing.T) {
			ctx := context.Background()
			path, o := fixture(t)
			s, err := Create(ctx, path, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close() // Deliberately corrupted generation cannot retire.
			i := testIntent()
			a, _ := settledDenial(t, s, i)
			s.fault = failAt("release-after")
			if err := a.Release(ctx); err == nil {
				t.Fatal("missing fault")
			}
			s.fault = nil
			query, replacement := "UPDATE attempts SET generation=? WHERE id=?", bytes.Repeat([]byte{1}, 16)
			if mismatch == "binding" {
				foreign := cloneIntent(i)
				foreign.Payload = []byte("foreign")
				replacement, _ = encodeIntent(foreign)
				query = "UPDATE attempts SET binding=? WHERE id=?"
			}
			if _, err := s.db.ExecContext(ctx, query, replacement, []byte(i.ID)); err != nil {
				t.Fatal(err)
			}
			if err := a.Release(ctx); !errors.Is(err, ErrInactive) {
				t.Fatal("accepted mismatching proof", err)
			}
			if s.views[i.ID] != a {
				t.Fatal("deleted unproved view")
			}
			if _, err := s.Activate(ctx, i); !errors.Is(err, ErrInactive) {
				t.Fatal("unproved activation replaced", err)
			}
		})
	}
}

type probeFailure struct {
	sqlite.ExecQuerierContext
	err         error
	readback    bool
	readbackErr error
}

func (p *probeFailure) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query == `SELECT "journal_dqs_probe"` {
		return nil, p.err
	}
	if query == "PRAGMA page_count" {
		p.readback = true
		if p.readbackErr != nil {
			return nil, p.readbackErr
		}
	}
	return p.ExecQuerierContext.QueryContext(ctx, query, args)
}

func TestDQSProbeExactDiagnostic(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(c any) error {
		raw := c.(sqlite.ExecQuerierContext)
		_, expected := raw.QueryContext(ctx, `SELECT "journal_dqs_probe"`, nil)
		var se *sqlite.Error
		if !errors.As(expected, &se) {
			t.Fatalf("unexpected probe error: %v", expected)
		}
		t.Logf("pinned DQS diagnostic: code=%d message=%q", se.Code(), se.Error())
		_, other := raw.QueryContext(ctx, `SELECT "other_missing_column"`, nil)
		for _, tc := range []struct {
			name   string
			err    error
			accept bool
		}{
			{"expected", expected, true},
			{"busy", errors.New("database is locked (5)"), false},
			{"io", errors.New("disk I/O error (10)"), false},
			{"other-column", other, false},
			{"untyped-same-message", errors.New(expected.Error()), false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				for _, readOnly := range []bool{false, true} {
					p := &probeFailure{ExecQuerierContext: raw, err: tc.err}
					o, _ = o.normalized()
					err := configure(ctx, p, o, readOnly)
					if tc.accept && (err != nil || !p.readback) {
						t.Fatal("expected probe refused", err)
					}
					if !tc.accept && (err == nil || !errors.Is(err, tc.err) || p.readback) {
						t.Error("unexpected probe failure accepted or lost", err, p.readback)
					}
				}
			})
		}
		readbackErr := errors.New("page count readback failed")
		p := &probeFailure{ExecQuerierContext: raw, err: expected, readbackErr: readbackErr}
		if err := configure(ctx, p, o, false); !errors.Is(err, readbackErr) || !p.readback {
			t.Fatal("lost readback failure", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHeldCloseAdmissionCancellation(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	_, b := settledDenial(t, s, i)
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
	closed := make(chan error, 2)
	go func() { closed <- s.Close() }()
	awaitSignal(t, entered)
	go func() { closed <- s.Close() }()
	for _, canceled := range []bool{false, true} {
		for _, info := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			if canceled {
				cancel()
			}
			result := make(chan error, 1)
			go func() {
				if info {
					_, err := s.Info(ctx)
					result <- err
					return
				}
				_, leave, err := s.enter(ctx)
				if err == nil {
					leave()
				}
				result <- err
			}()
			select {
			case err := <-result:
				want := os.ErrClosed
				if canceled {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Error("Close admission classification", err, want)
				}
			case <-time.After(100 * time.Millisecond):
				t.Error("admission blocked behind Close")
				unblock()
				awaitResult(t, result)
			}
			cancel()
		}
	}
	unblock()
	for n := 0; n < 2; n++ {
		if err := awaitResult(t, closed); err != nil {
			t.Fatal(err)
		}
	}
	s.fault = nil
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertNoReplay(t, s, i, b, dispatch.Denied)
}

func TestCloseWaitsForAdmittedSave(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	a := register(t, s, i)
	if st, _, err := a.Prepare(context.Background(), i); st != dispatch.PrepareNew || err != nil {
		t.Fatal(st, err)
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(proceed) }) }
	defer unblock()
	s.fault = func(point string) error {
		if point == "save-before" {
			close(entered)
			awaitSignal(t, proceed)
		}
		return nil
	}
	saved := make(chan error, 1)
	receipt := dispatch.Receipt{Intent: i, ID: "admitted", State: dispatch.Started, Evidence: "exact\x00\xff"}
	go func() { saved <- a.Save(context.Background(), receipt) }()
	awaitSignal(t, entered)
	closing, closed := make(chan struct{}), make(chan error, 1)
	go func() { close(closing); closed <- s.Close() }()
	awaitSignal(t, closing)
	select {
	case err := <-closed:
		t.Fatal("Close passed admitted Save", err)
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	if err := awaitResult(t, saved); err != nil {
		t.Fatal(err)
	}
	if err := awaitResult(t, closed); err != nil {
		t.Fatal(err)
	}
	s.fault = nil
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := inspect(t, s, i.ID)
	if r.Active || !r.Uncertain || !reflect.DeepEqual(r.Receipts, []dispatch.Receipt{receipt}) {
		t.Fatal("Close lost admitted evidence", r)
	}
	b := &fakeSink{}
	got, err := (dispatch.Coordinator{Journal: activate(t, s, i), Boundary: b}).Dispatch(context.Background(), i)
	if err != nil || got.State != dispatch.Uncertain || b.calls.Load() != 0 || b.effects.Load() != 0 {
		t.Fatal("admitted Save replayed", got, err)
	}
}

func TestCapacityRecoveryExplicitLargerBound(t *testing.T) {
	ctx := context.Background()
	path, o := fixture(t)
	o.MaxBytes = 64 << 10
	s, err := Create(ctx, path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	original := mustInfo(t, s)
	i := testIntent()
	a := register(t, s, i)
	if st, _, err := a.Prepare(ctx, i); st != dispatch.PrepareNew || err != nil {
		t.Fatal(st, err)
	}
	receipt := dispatch.Receipt{Intent: i, ID: "known", State: dispatch.Started, Evidence: "\xff\x00exact"}
	if err := a.Save(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	requireFull := func(err error) {
		t.Helper()
		var se *sqlite.Error
		if !errors.As(err, &se) || se.Code() != 13 {
			t.Fatal("expected SQLITE_FULL", err)
		}
	}
	count := 0
	for ; count < 4096; count++ {
		_, err = s.db.ExecContext(ctx, "INSERT INTO events(attempt_id,kind,detail) VALUES(?,'f',x'')", []byte(i.ID))
		if err != nil {
			requireFull(err)
			break
		}
	}
	if count == 4096 {
		t.Fatal("bounded fixture did not reach FULL")
	}
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Records[0].Active {
		t.Fatal("lost guard before recovery")
	}
	requireFull(s.Close())
	small, err := Open(ctx, path, o)
	if err == nil {
		small.Close()
		t.Fatal("same-bound recovery unexpectedly succeeded")
	}
	requireFull(err)
	o.MaxBytes = 128 << 10
	s, err = Open(ctx, path, o)
	if err != nil {
		t.Fatal("explicit larger bound refused", err)
	}
	defer s.Close()
	got := mustInfo(t, s)
	if got.StoreID != original.StoreID || got.IncarnationID != original.IncarnationID || got.Authority != original.Authority || got.MaxBytes != o.MaxBytes {
		t.Fatal("identity or bound changed", got)
	}
	after, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events)+1 || !reflect.DeepEqual(after.Events[:len(before.Events)], before.Events) || after.Events[len(before.Events)].Kind != "recovered-uncertain" {
		t.Fatal("recovery lost original events")
	}
	r := after.Records[0]
	if r.Active || !r.Uncertain || !reflect.DeepEqual(r.Intent, i) || !reflect.DeepEqual(r.Receipts, []dispatch.Receipt{receipt}) {
		t.Fatal("recovery lost guard uncertainty or evidence", r)
	}
	b := &fakeSink{}
	rc, err := (dispatch.Coordinator{Journal: activate(t, s, i), Boundary: b}).Dispatch(ctx, i)
	if err != nil || rc.State != dispatch.Uncertain || b.calls.Load() != 0 || b.effects.Load() != 0 {
		t.Fatal("capacity recovery replayed", rc, err)
	}
	t.Logf("FULL after %d bounded events; explicit 64 KiB -> 128 KiB recovery preserved identity/evidence; boundary calls=0 effects=0", count)
}
