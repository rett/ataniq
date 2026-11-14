package journal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/dispatch"
	"modernc.org/sqlite"
)

func testIntent() dispatch.Intent {
	return dispatch.Intent{Authority: "local-authority", Project: "project", Command: "command", ID: "intent", Attempt: "attempt", Runtime: "runtime", Epoch: "epoch", Action: "action", Kind: "tool", Policy: "policy", Qualification: "qualification", Authorization: "authorization", Limits: "limits", Payload: []byte{0, 255, 1}}
}

func testPolicy(i dispatch.Intent) Policy {
	p := Policy{Intent: cloneIntent(i), ValidUntil: time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC)}
	if i.Grant != "" {
		p.RequireGrant = true
		p.Grant = &Grant{ID: i.Grant, Intent: cloneIntent(i), ValidUntil: p.ValidUntil}
	}
	return p
}

func register(t *testing.T, s *Store, i dispatch.Intent) *Attempt {
	t.Helper()
	a, err := s.Register(context.Background(), testPolicy(i))
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func activate(t *testing.T, s *Store, i dispatch.Intent) *Attempt {
	t.Helper()
	a, err := s.Activate(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func inspect(t *testing.T, s *Store, id string) Record {
	t.Helper()
	r, err := s.Inspect(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// sink counts actual effects separately from calls and database projection.
// Its current policy is independently read at the real fake effect seam.
type fakeSink struct {
	calls, effects atomic.Int64
	denied         atomic.Bool
	after          func()
}

func (b *fakeSink) Start(ctx context.Context, i dispatch.Intent) (dispatch.StartStatus, string, error) {
	b.calls.Add(1)
	if b.denied.Load() {
		return dispatch.StartDenied, "fresh authority denied", nil
	}
	b.effects.Add(1)
	if b.after != nil {
		b.after()
	}
	return dispatch.StartAcknowledged, "exact\x00\xffstart", nil
}

func TestGuardedCoordinatorConcurrentDuplicates(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	i.Grant = "approval"
	a := register(t, s, i)
	b := &fakeSink{}
	c := dispatch.Coordinator{Journal: a, Boundary: b}
	var wg sync.WaitGroup
	for n := 0; n < 24; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.Dispatch(context.Background(), i)
			if err != nil || r.State == dispatch.Uncertain {
				t.Errorf("dispatch: %+v %v", r, err)
			}
		}()
	}
	wg.Wait()
	if b.calls.Load() != 1 || b.effects.Load() != 1 {
		t.Fatalf("replayed: calls=%d effects=%d", b.calls.Load(), b.effects.Load())
	}
	r := inspect(t, s, i.ID)
	if !r.Prepared || r.Current.State != dispatch.Started || len(r.Receipts) != 1 {
		t.Fatalf("%+v", r)
	}
	if _, err := s.Activate(context.Background(), i); !errors.Is(err, ErrInactive) {
		t.Fatal("superseded active view", err)
	}
	if err := a.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(context.Background()); !errors.Is(err, ErrInactive) {
		t.Fatal("stale release", err)
	}
	a2 := activate(t, s, i)
	if _, _, err := a.Prepare(context.Background(), i); !errors.Is(err, ErrInactive) {
		t.Fatal(err)
	}
	if err := a.Release(context.Background()); !errors.Is(err, ErrInactive) {
		t.Fatal(err)
	}
	rc, err := (dispatch.Coordinator{Journal: a2, Boundary: b}).Dispatch(context.Background(), i)
	if err != nil || rc.State != dispatch.Uncertain || b.effects.Load() != 1 {
		t.Fatalf("replay after retirement: %+v %v effects=%d", rc, err, b.effects.Load())
	}
	s.Close()
	s, err = Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a2 = activate(t, s, i)
	_, err = (dispatch.Coordinator{Journal: a2, Boundary: b}).Dispatch(context.Background(), i)
	if err != nil || b.effects.Load() != 1 {
		t.Fatal("replay after reopen", err, b.effects.Load())
	}
}

func TestEveryImmutableBindingAndOpaqueBytes(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	i.Grant = "grant"
	a := register(t, s, i)
	if st, _, err := a.Prepare(context.Background(), i); st != dispatch.PrepareNew || err != nil {
		t.Fatal(st, err)
	}
	for _, name := range []string{"Authority", "Project", "Command", "ID", "Attempt", "Runtime", "Epoch", "Action", "Kind", "Policy", "Qualification", "Authorization", "Limits", "Grant", "Payload"} {
		t.Run(name, func(t *testing.T) {
			changed := cloneIntent(i)
			if name == "Payload" {
				changed.Payload[0]++
			} else {
				f := reflect.ValueOf(&changed).Elem().FieldByName(name)
				if name == "Kind" {
					f.SetString("retry")
				} else {
					f.SetString(f.String() + "-changed")
				}
			}
			st, _, err := a.Prepare(context.Background(), changed)
			if st != dispatch.PrepareConflict || err != nil {
				t.Fatal(st, err)
			}
			if err := a.Save(context.Background(), dispatch.Receipt{Intent: changed, ID: "foreign", State: dispatch.Denied}); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			if name != "ID" {
				if _, err := s.Register(context.Background(), testPolicy(changed)); err == nil {
					t.Fatal("rebound ID")
				}
			}
		})
	}
	if inspect(t, s, i.ID).Uncertain {
		t.Fatal("foreign binding poisoned original")
	}
	if err := a.Save(context.Background(), dispatch.Receipt{Intent: i, ID: "opaque\x00\xff", State: dispatch.Started, Evidence: "receipt\x00\xff"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := inspect(t, s, i.ID)
	if r.Receipts[0].ID != "opaque\x00\xff" || r.Receipts[0].Evidence != "receipt\x00\xff" {
		t.Fatal("receipt normalized")
	}
	for _, empty := range []bool{false, true} {
		j := testIntent()
		j.ID = fmt.Sprint("binary-", empty)
		fields := reflect.ValueOf(&j).Elem()
		for n := 0; n < fields.NumField(); n++ {
			f := fields.Field(n)
			name := fields.Type().Field(n).Name
			if f.Kind() == reflect.String && name != "Authority" && name != "Kind" && name != "Grant" {
				f.SetString(f.String() + "\x00\xff")
			}
		}
		if empty {
			j.Payload = nil
		}
		v := register(t, s, j)
		if st, _, err := v.Prepare(context.Background(), j); st != dispatch.PrepareNew || err != nil {
			t.Fatal(st, err)
		}
		if empty {
			j.Payload = []byte{}
		}
		if st, r, err := v.Prepare(context.Background(), j); st != dispatch.PrepareDuplicate || err != nil || !bytes.Equal(r.Intent.Payload, j.Payload) {
			t.Fatal(st, r, err)
		}
		if got := inspect(t, s, j.ID).Intent; !reflect.DeepEqual(identityFields(got), identityFields(j)) || !bytes.Equal(got.Payload, j.Payload) {
			t.Fatal("binding normalized")
		}
	}
}

func TestTrustedPolicyAndOneUseGrants(t *testing.T) {
	for _, kind := range []string{"missing", "missing grant", "wrong grant ID", "wrong target", "no grant policy", "expiry", "grant expiry", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			i := testIntent()
			i.Grant = "grant"
			p := testPolicy(i)
			switch kind {
			case "missing":
				p.ValidUntil = time.Time{}
			case "missing grant":
				p.Grant = nil
			case "wrong grant ID":
				p.Grant.ID = "other"
			case "wrong target":
				p.Grant.Intent.Epoch = "other"
			case "no grant policy":
				p.RequireGrant = false
			case "expiry":
				p.ValidUntil = time.Unix(1, 0)
			case "grant expiry":
				p.Grant.ValidUntil = time.Unix(1, 0)
			}
			a, err := s.Register(context.Background(), p)
			if kind != "expiry" && kind != "grant expiry" && kind != "revoked" {
				if err == nil || a != nil {
					t.Fatal("accepted invalid policy")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "revoked" {
				if err := a.Revoke(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			b := &fakeSink{}
			r, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
			if err == nil || r.State != dispatch.Denied || b.calls.Load() != 0 {
				t.Fatal(r, err, b.calls.Load())
			}
		})
	}
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	i.Grant = "one-use"
	a := register(t, s, i)
	st, _, err := a.Prepare(context.Background(), i)
	if st != dispatch.PrepareNew || err != nil {
		t.Fatal(st, err)
	}
	if err := a.Revoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2091, 1, 1, 0, 0, 0, 0, time.UTC) }
	if st, _, err := a.Prepare(context.Background(), i); st != dispatch.PrepareDuplicate || err != nil {
		t.Fatal("duplicate used stale policy", st, err)
	}
	if _, err := s.Register(context.Background(), testPolicy(i)); !errors.Is(err, ErrConflict) {
		t.Fatal("refreshed policy", err)
	}
	s.now = time.Now
	j := cloneIntent(i)
	j.ID = "second-effect"
	j.Attempt = "second-attempt"
	a2 := register(t, s, j)
	if st, _, err := a2.Prepare(context.Background(), j); st != dispatch.PrepareConflict || err != nil {
		t.Fatal("reused grant", st, err)
	}
	j.ID = "grant-free"
	j.Grant = ""
	a3 := register(t, s, j)
	if st, _, err := a3.Prepare(context.Background(), j); st != dispatch.PrepareNew || err != nil {
		t.Fatal("same command distinct effects", st, err)
	}
}

func TestFreshBoundaryAndCancelledSave(t *testing.T) {
	for _, mode := range []string{"fresh policy denial", "cancel after actual start", "cancel before lookup", "cancel existing before lookup"} {
		t.Run(mode, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			i := testIntent()
			a := register(t, s, i)
			b := &fakeSink{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "fresh policy denial" {
				s.fault = func(point string) error {
					if point == "prepare-after" {
						b.denied.Store(true)
					}
					return nil
				}
			}
			if mode == "cancel after actual start" {
				b.after = cancel
			}
			if mode == "cancel existing before lookup" {
				if _, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(mode, "before lookup") {
				cancel()
			}
			r, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(ctx, i)
			if mode == "fresh policy denial" {
				if err != nil || r.State != dispatch.Denied || b.effects.Load() != 0 {
					t.Fatal(r, err)
				}
				return
			}
			if err == nil || r.State != dispatch.Uncertain {
				t.Fatal("false denial or success", r, err)
			}
			want := int64(0)
			if mode != "cancel before lookup" {
				want = 1
			}
			s.fault = nil
			b.after = nil
			_, err = (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
			if err != nil || b.effects.Load() != want {
				t.Fatal("replay after cancellation", err, b.effects.Load())
			}
			s.Close()
			s, err = Open(context.Background(), path, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			a = activate(t, s, i)
			_, err = (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
			if err != nil || b.effects.Load() != want {
				t.Fatal("replay after reopen", err, b.effects.Load())
			}
		})
	}
}

func failAt(point string) func(string) error {
	return func(p string) error {
		if p == point {
			return errors.New("injected journal I/O response failure")
		}
		return nil
	}
}

func TestGuardedFaultsNeverReplay(t *testing.T) {
	for _, point := range []string{"prepare-before", "prepare-after", "save-before", "save-after", "revoke-before", "revoke-after", "release-before", "release-after"} {
		t.Run(point, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			i := testIntent()
			a := register(t, s, i)
			b := &fakeSink{}
			s.fault = failAt(point)
			var err error
			if strings.HasPrefix(point, "revoke") {
				err = a.Revoke(context.Background())
			} else if strings.HasPrefix(point, "release") {
				_, err = (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
				if err != nil {
					t.Fatal(err)
				}
				err = a.Release(context.Background())
			} else {
				_, err = (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
			}
			if err == nil {
				t.Fatal("acknowledged ambiguous failure")
			}
			want := b.effects.Load()
			s.fault = nil
			_, _ = (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
			if b.effects.Load() != want {
				t.Fatal("replayed after fault cleared")
			}
			s.Close()
			s, err = Open(context.Background(), path, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			a = activate(t, s, i)
			_, err = (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
			if err != nil || b.effects.Load() != want {
				t.Fatal("replayed after reopen", err, b.effects.Load(), want)
			}
			if !inspect(t, s, i.ID).Uncertain {
				t.Fatal("lost uncertainty")
			}
		})
	}
}

func TestActivationFailureAndRecoveryFailure(t *testing.T) {
	for _, point := range []string{"activate-before", "activate-after"} {
		t.Run(point, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			i := testIntent()
			s.fault = failAt(point)
			if a, err := s.Register(context.Background(), testPolicy(i)); err == nil || a != nil {
				t.Fatal("exposed failed capability")
			}
			s.fault = nil
			s.Close()
			if point == "activate-before" {
				s, err := Open(context.Background(), path, o)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if _, err := s.Activate(context.Background(), i); !errors.Is(err, sql.ErrNoRows) {
					t.Fatal(err)
				}
				return
			}
			if s, err := openWithFault(context.Background(), path, o, false, failAt("recover-before")); err == nil || s != nil {
				t.Fatal("accepted failed recovery")
			}
			s, err := Open(context.Background(), path, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			a := activate(t, s, i)
			b := &fakeSink{}
			r, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
			if err != nil || r.State != dispatch.Uncertain || b.effects.Load() != 0 {
				t.Fatal("guard absent prepared replay", r, err)
			}
			j := cloneIntent(i)
			j.ID = "unrelated"
			j.Grant = ""
			other := register(t, s, j)
			if _, err := (dispatch.Coordinator{Journal: other, Boundary: b}).Dispatch(context.Background(), j); err != nil || b.effects.Load() != 1 {
				t.Fatal("quarantined store", err)
			}
		})
	}
}

func TestReceiptsContradictionsAndLateSaveFailure(t *testing.T) {
	for _, mode := range []string{"same identical", "same different detail", "same different state", "different contradictory", "different same state", "foreign", "late failed contradiction"} {
		t.Run(mode, func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			i := testIntent()
			a := register(t, s, i)
			b := &fakeSink{}
			if _, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i); err != nil {
				t.Fatal(err)
			}
			r := dispatch.Receipt{Intent: cloneIntent(i), ID: "start", State: dispatch.Started, Evidence: "exact\x00\xffstart"}
			wantCount, wantUncertain := 2, false
			switch mode {
			case "same identical":
				wantCount = 1
			case "same different detail":
				r.Evidence = "contradiction"
				wantUncertain = true
			case "same different state":
				r.State = dispatch.Denied
				wantUncertain = true
			case "different contradictory":
				r.ID = "later"
				r.State = dispatch.Denied
				wantUncertain = true
			case "different same state":
				r.ID = "later"
			case "foreign":
				r.Intent.Epoch = "foreign"
				wantCount = 1
			case "late failed contradiction":
				r.State = dispatch.Denied
				s.fault = failAt("save-before")
				wantCount = 1
				wantUncertain = true
			}
			err := a.Save(context.Background(), r)
			if mode == "foreign" || mode == "late failed contradiction" {
				if err == nil {
					t.Fatal("expected error")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			s.fault = nil
			got := inspect(t, s, i.ID)
			if len(got.Receipts) != wantCount || got.Uncertain != wantUncertain {
				t.Fatalf("%s: %+v", mode, got)
			}
			if mode == "late failed contradiction" {
				if err := a.Save(context.Background(), dispatch.Receipt{Intent: i, ID: "another", State: dispatch.Started}); err != nil {
					t.Fatal(err)
				}
				wantCount++
			}
			_, err = (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i)
			if err != nil || b.effects.Load() != 1 {
				t.Fatal("late save replay", err)
			}
			s.Close()
			s, err = Open(context.Background(), path, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			got = inspect(t, s, i.ID)
			if len(got.Receipts) != wantCount || got.Receipts[0].Evidence != "exact\x00\xffstart" || !got.Uncertain {
				t.Fatal("lost original evidence or uncertainty", got)
			}
		})
	}
}

func TestBoundsBusyAndFullKeepGuard(t *testing.T) {
	for _, mode := range []string{"full prepare", "busy prepare", "oversize evidence", "queued cancellation"} {
		t.Run(mode, func(t *testing.T) {
			path, o := fixture(t)
			o.MaxBytes = 64 << 10
			o.OperationTimeout = 500 * time.Millisecond
			o.BusyTimeout = 20 * time.Millisecond
			s := mustCreate(t, path, o)
			i := testIntent()
			if mode == "full prepare" {
				i.Grant = "large-grant"
				i.Payload = bytes.Repeat([]byte{1}, 8<<10)
			}
			a := register(t, s, i)
			b := &fakeSink{}
			var unblock func()
			if mode == "full prepare" {
				full := false
				for n := 0; n < 32; n++ {
					if _, err := s.db.Exec("INSERT INTO events(attempt_id,kind,detail) VALUES(?,'fill',zeroblob(4096))", []byte(i.ID)); err != nil {
						var sqliteErr *sqlite.Error
						if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 13 {
							t.Fatal(err)
						}
						full = true
						break
					}
				}
				if !full {
					t.Fatal("fixture never reached bounded SQLITE_FULL")
				}
				unblock = func() { execFixture(t, s.db, "DELETE FROM events WHERE kind='fill'") }
			}
			if mode == "busy prepare" {
				db := fixtureDB(t, path)
				execFixture(t, db, "BEGIN IMMEDIATE")
				unblock = func() { execFixture(t, db, "ROLLBACK"); db.Close() }
			}
			if mode == "queued cancellation" {
				s.gate <- struct{}{}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				st, _, err := a.Prepare(ctx, i)
				cancel()
				<-s.gate
				if st != dispatch.PrepareUnknown || err == nil {
					t.Fatal(st, err)
				}
			} else if mode == "oversize evidence" {
				if _, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i); err != nil {
					t.Fatal(err)
				}
				err := a.Save(context.Background(), dispatch.Receipt{Intent: i, ID: "large", State: dispatch.Started, Evidence: strings.Repeat("e", maxEvidence+1)})
				if !errors.Is(err, ErrLimit) {
					t.Fatal(err)
				}
			} else {
				st, _, err := a.Prepare(context.Background(), i)
				if st != dispatch.PrepareUnknown || err == nil {
					t.Fatal(st, err)
				}
			}
			if unblock != nil {
				unblock()
			}
			s.fault = nil
			want := b.effects.Load()
			if _, err := (dispatch.Coordinator{Journal: a, Boundary: b}).Dispatch(context.Background(), i); err != nil || b.effects.Load() != want {
				t.Fatal("replayed bounded failure", err)
			}
		})
	}
}

func TestSnapshotConsistencyBoundsAndOwnedBuffers(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	i.Grant = "approval"
	a := register(t, s, i)
	if _, err := (dispatch.Coordinator{Journal: a, Boundary: &fakeSink{}}).Dispatch(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Watermark != 3 || len(snap.Events) != 3 || len(snap.ConsumedGrants) != 1 || len(snap.Records) != 1 {
		t.Fatalf("%+v", snap)
	}
	snap.Records[0].Intent.Payload[0] = 99
	snap.Records[0].Current.Intent.Payload[0] = 99
	snap.Records[0].Receipts[0].Intent.Payload[0] = 99
	snap.ConsumedGrants[0].Payload[0] = 99
	snap.Events[2].Detail[0] = 99
	fresh, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Records[0].Intent.Payload[0] != 0 || fresh.ConsumedGrants[0].Payload[0] != 0 || fresh.Events[2].Detail[0] == 99 {
		t.Fatal("aliased stored bytes")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 32; n++ {
			if err := a.Save(context.Background(), dispatch.Receipt{Intent: i, ID: fmt.Sprint("receipt-", n), State: dispatch.Started}); err != nil {
				t.Error(err)
			}
		}
	}()
	for n := 0; n < 16; n++ {
		snap, err := s.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if snap.Watermark != int64(len(snap.Events)) || len(snap.Events) != len(snap.Records[0].Receipts)+2 {
			t.Fatal("torn snapshot")
		}
	}
	wg.Wait()
	s.fault = failAt("snapshot")
	if snap, err := s.Snapshot(context.Background()); err == nil || snap.Watermark != 0 || snap.Records != nil {
		t.Fatal("partial success", snap, err)
	}
	s.fault = nil
	// Bounded fixture SQL fills events, not host disk or memory.
	execFixture(t, s.db, "WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<4096) INSERT INTO events(attempt_id,kind,detail) SELECT x'696e74656e74','fixture',x'' FROM n")
	if snap, err := s.Snapshot(context.Background()); !errors.Is(err, ErrLimit) || snap.Watermark != 0 || snap.Events != nil {
		t.Fatal("false complete over bound", err)
	}
}

func TestRetirementAndLateEvidence(t *testing.T) {
	for _, state := range []dispatch.State{dispatch.Prepared, dispatch.Started, dispatch.Denied} {
		t.Run(string(state), func(t *testing.T) {
			path, o := fixture(t)
			s := mustCreate(t, path, o)
			i := testIntent()
			a := register(t, s, i)
			if st, _, err := a.Prepare(context.Background(), i); st != dispatch.PrepareNew || err != nil {
				t.Fatal(st, err)
			}
			if state != dispatch.Prepared {
				if err := a.Save(context.Background(), dispatch.Receipt{Intent: i, ID: "first", State: state}); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Release(context.Background()); err != nil {
				t.Fatal(err)
			}
			r := inspect(t, s, i.ID)
			if r.Active || r.Uncertain != (state != dispatch.Denied) {
				t.Fatal("unsafe retirement", r)
			}
			if err := a.Save(context.Background(), dispatch.Receipt{Intent: i, ID: "late", State: dispatch.Denied}); !errors.Is(err, ErrInactive) {
				t.Fatal("stale save", err)
			}
			late := activate(t, s, i)
			if err := late.Save(context.Background(), dispatch.Receipt{Intent: i, ID: "late", State: dispatch.Started}); err != nil {
				t.Fatal(err)
			}
			if !inspect(t, s, i.ID).Uncertain {
				t.Fatal("new evidence cleared unresolved or contradictory state")
			}
		})
	}
}

func TestCloseWaitsAndGuardsPendingCalls(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	a := register(t, s, i)
	entered, proceed := make(chan struct{}), make(chan struct{})
	s.fault = func(point string) error {
		if point == "prepare-after" {
			close(entered)
			<-proceed
		}
		return nil
	}
	result := make(chan error, 1)
	go func() { _, _, err := a.Prepare(context.Background(), i); result <- err }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		t.Fatal("closed during operation", err)
	default:
	}
	close(proceed)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Prepare(context.Background(), i); err == nil {
		t.Fatal("closed view usable")
	}
	s, err := Open(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !inspect(t, s, i.ID).Uncertain {
		t.Fatal("prepared close lost uncertainty")
	}
}

func TestSnapshotByteBoundAndCancellation(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	i.Payload = bytes.Repeat([]byte{1}, 1<<20)
	a := register(t, s, i)
	if st, _, err := a.Prepare(context.Background(), i); st != dispatch.PrepareNew || err != nil {
		t.Fatal(st, err)
	}
	for n := 0; n < 16; n++ {
		if err := a.Save(context.Background(), dispatch.Receipt{Intent: i, ID: fmt.Sprint(n), State: dispatch.Started}); err != nil {
			t.Fatal(err)
		}
	}
	if snap, err := s.Snapshot(context.Background()); !errors.Is(err, ErrLimit) || snap.Watermark != 0 {
		t.Fatal("false complete byte bound", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if snap, err := s.Snapshot(ctx); err == nil || snap.Watermark != 0 {
		t.Fatal("false complete cancellation", err)
	}
}

func TestReleaseInvalidatesBeforeClearingGuard(t *testing.T) {
	path, o := fixture(t)
	s := mustCreate(t, path, o)
	i := testIntent()
	a := register(t, s, i)
	if _, _, err := a.Prepare(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(context.Background(), dispatch.Receipt{Intent: i, ID: "denial", State: dispatch.Denied}); err != nil {
		t.Fatal(err)
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	s.fault = func(point string) error {
		if point == "release-before" {
			close(entered)
			<-proceed
		}
		return nil
	}
	released := make(chan error, 1)
	go func() { released <- a.Release(context.Background()) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Save(ctx, dispatch.Receipt{Intent: i, ID: "late", State: dispatch.Started}); !errors.Is(err, ErrInactive) {
		t.Fatal("retiring view accepted late call", err)
	}
	close(proceed)
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	s.fault = nil
	if r := inspect(t, s, i.ID); r.Active || r.Uncertain {
		t.Fatal("settled retirement changed", r)
	}
}
